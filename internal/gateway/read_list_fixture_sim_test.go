//go:build wdasim

package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestReadListStaticWebSimulator compares the two paths on the same fixed,
// 100-item page. It is the repeatable performance check; public sites in
// read_list_web_sim_test.go remain real-world smoke tests.
func TestReadListStaticWebSimulator(t *testing.T) {
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
		fmt.Fprintf(&html, `<section class="item"><h2><a href="#item%03d">Article %03d deterministic fixture title</a></h2><p>Stable excerpt %03d for list reading.</p></section>`,
			i, i, i)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html.String()))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dev, err := ios.OpenWDAURL(ctx, base, os.Getenv("PINKY_MJPEG_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()
	g, err := New(Options{
		Providers:      []driver.Provider{&simulatorProvider{dev: dev}},
		Policy:         policy.Default(),
		TrustNewAgents: true,
		NoScreenshots:  true,
		DataDir:        t.TempDir(),
		DiscoverEvery:  time.Hour,
		SettleMax:      3 * time.Second,
		PollInterval:   100 * time.Millisecond,
		IdleTimeout:    15 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	area := screen.Rect{X: 0, Y: 120, W: 1206, H: 2260}
	openWeb(t, udid, srv.URL)
	const fixtureScreens = 7
	old := measureWebSwipes(t, ctx, g, area, fixtureScreens, 30, 1000)
	openWeb(t, udid, srv.URL)
	next := measureWebList(t, ctx, g, area, fixtureScreens, 30)
	t.Logf("static 100-item fixture: old=%v (%d calls), phone_read_list=%v (%d screens, %d calls, stop %q), speedup %.2fx",
		old.elapsed.Round(time.Millisecond), old.calls, next.elapsed.Round(time.Millisecond),
		next.screens, next.calls, next.stop, float64(old.elapsed)/float64(next.elapsed))

	if next.screens < fixtureScreens || next.stop != "max_screens" {
		t.Fatalf("fixture scan: %+v", next)
	}
	for i := 1; i <= 5; i++ {
		title := fmt.Sprintf("Article %03d deterministic fixture title", i)
		if got := strings.Count(next.text, title); got != 1 {
			t.Fatalf("%q appeared %d times, want once\n%s", title, got, next.text)
		}
	}
}
