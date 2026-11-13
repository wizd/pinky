//go:build wdasim

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
)

type readListSimProvider struct {
	dev driver.Device
}

func (p *readListSimProvider) Platform() driver.Platform { return driver.IOS }

func (p *readListSimProvider) List(context.Context) ([]driver.Info, error) {
	return []driver.Info{p.dev.Info()}, nil
}

func (p *readListSimProvider) Open(context.Context, string) (driver.Device, error) {
	return p.dev, nil
}

// TestReadListSimulatorMCP exercises the public MCP schema and render path
// against a fixed local page in Safari. The public feeds and the 音潮 app
// have separate gateway smoke tests because their content is not stable.
//
//	PINKY_WDA_URL=http://127.0.0.1:8100 PINKY_MJPEG_URL=http://127.0.0.1:9100 \
//	  PINKY_SIM_UDID=<udid> go test -tags wdasim -count=1 -v \
//	  -run TestReadListSimulatorMCP ./internal/mcp
func TestReadListSimulatorMCP(t *testing.T) {
	base := os.Getenv("PINKY_WDA_URL")
	udid := os.Getenv("PINKY_SIM_UDID")
	if base == "" || udid == "" {
		t.Skip("PINKY_WDA_URL and PINKY_SIM_UDID are required")
	}
	var html strings.Builder
	html.WriteString(`<!doctype html><meta name="viewport" content="width=device-width">
<style>
body{margin:0;font:42px -apple-system;background:#fff;color:#111}
.item{height:420px;box-sizing:border-box;padding:40px 28px;border-bottom:2px solid #888}
h2{margin:0 0 24px;font-size:46px}
p{margin:0;color:#444}
</style>`)
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&html, `<section class="item"><h2><a href="#item%03d">MCP article %03d fixed title</a></h2><p>Stable MCP excerpt %03d.</p></section>`,
			i, i, i)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html.String()))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dev, err := ios.OpenWDAURL(ctx, base, os.Getenv("PINKY_MJPEG_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	g, err := gateway.New(gateway.Options{
		Providers:      []driver.Provider{&readListSimProvider{dev: dev}},
		Policy:         policy.Default(),
		TrustNewAgents: true,
		NoScreenshots:  true,
		DataDir:        t.TempDir(),
		DiscoverEvery:  time.Hour,
		SettleMax:      3 * time.Second,
		PollInterval:   100 * time.Millisecond,
		IdleTimeout:    5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	h := &harness{g: g, srv: New(g, "test")}
	c := h.connect(t, "Simulator MCP")
	c.mustCall(t, "phone_connect", map[string]any{
		"task": "Read a fixed list", "apps": []string{"com.apple.mobilesafari"}, "read_only": true,
	})
	cmd := exec.Command("xcrun", "simctl", "openurl", udid, srv.URL+"?pinky_mcp_benchmark=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open fixture: %v\n%s", err, out)
	}
	time.Sleep(4 * time.Second)
	c.mustCall(t, "phone_observe", nil)

	start := time.Now()
	text, isErr := c.call(t, "phone_read_list", map[string]any{
		"scroll_area": map[string]any{"x": 0, "y": 120, "width": 1206, "height": 2260},
		"max_screens": 7, "max_ms": 45000, "ocr": "auto", "scroll_percent": 30,
		"intent": "Read a fixed list",
	})
	elapsed := time.Since(start)
	lines := strings.Count(text, "\n- ")
	if isErr || !strings.HasPrefix(text, "settled") || !strings.Contains(text, "pages: 7") ||
		!strings.Contains(text, "stop: max_screens") || lines < 5 {
		t.Fatalf("phone_read_list (%v):\n%s", elapsed.Round(time.Millisecond), text)
	}
	for i := 1; i <= 5; i++ {
		title := fmt.Sprintf("MCP article %03d fixed title", i)
		if got := strings.Count(text, title); got != 1 {
			t.Fatalf("%q appeared %d times, want once\n%s", title, got, text)
		}
	}
	t.Logf("phone_read_list through MCP on a fixed Simulator page: %v, %d new lines\n%s",
		elapsed.Round(time.Millisecond), lines, firstLine(text))
}
