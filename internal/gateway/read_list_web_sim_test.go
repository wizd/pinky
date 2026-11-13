//go:build wdasim

package gateway

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestReadListWebSimulator compares the existing step-by-step path with
// phone_read_list on one public mobile-web feed. It resets Safari to url
// between runs and therefore measures Pinky work rather than the outer
// model's thinking time.
//
//	PINKY_WDA_URL=http://127.0.0.1:18100 PINKY_MJPEG_URL=http://127.0.0.1:19100 \
//	PINKY_SIM_UDID=<udid> PINKY_WEB_URL=https://www.bilibili.com/ \
//	PINKY_WEB_AREA=0,60,1206,2160 \
//	go test -tags wdasim -count=1 -v -run TestReadListWebSimulator ./internal/gateway
func TestReadListWebSimulator(t *testing.T) {
	base := os.Getenv("PINKY_WDA_URL")
	url := os.Getenv("PINKY_WEB_URL")
	udid := os.Getenv("PINKY_SIM_UDID")
	if base == "" || url == "" || udid == "" {
		t.Skip("PINKY_WDA_URL, PINKY_WEB_URL and PINKY_SIM_UDID are required")
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("PINKY_WEB_MODE")))
	if mode == "" {
		mode = "both"
	}
	if mode != "both" && mode != "old" && mode != "new" {
		t.Fatalf("PINKY_WEB_MODE must be both, old or new")
	}
	area, err := parseWebArea(os.Getenv("PINKY_WEB_AREA"), screen.Rect{X: 0, Y: 60, W: 1206, H: 2160})
	if err != nil {
		t.Fatal(err)
	}
	screens := 30
	if raw := os.Getenv("PINKY_WEB_SCREENS"); raw != "" {
		screens, err = strconv.Atoi(raw)
		if err != nil || screens < 1 || screens > 30 {
			t.Fatalf("PINKY_WEB_SCREENS must be between 1 and 30")
		}
	}
	scrollPercent := 60
	if raw := os.Getenv("PINKY_WEB_SCROLL"); raw != "" {
		scrollPercent, err = strconv.Atoi(raw)
		if err != nil || scrollPercent < 30 || scrollPercent > 80 {
			t.Fatalf("PINKY_WEB_SCROLL must be between 30 and 80")
		}
	}
	oldSwipeMS := 300
	if raw := os.Getenv("PINKY_WEB_OLD_MS"); raw != "" {
		oldSwipeMS, err = strconv.Atoi(raw)
		if err != nil || oldSwipeMS < 100 || oldSwipeMS > 3000 {
			t.Fatalf("PINKY_WEB_OLD_MS must be between 100 and 3000")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
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
		IdleTimeout:    20 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	var baseline webMeasure
	if mode != "new" {
		openWeb(t, udid, url)
		baseline = measureWebSwipes(t, ctx, g, area, screens, scrollPercent, oldSwipeMS)
		t.Logf("%s baseline (%d ms swipes): %v for %d swipes",
			url, oldSwipeMS, baseline.elapsed.Round(time.Millisecond), baseline.swipes)
	}
	if mode == "old" {
		return
	}

	openWeb(t, udid, url)
	read := measureWebList(t, ctx, g, area, screens, scrollPercent)
	if mode == "new" {
		t.Logf("%s phone_read_list: %v (%d screens, %d swipes, %d calls, stop %q)",
			url, read.elapsed.Round(time.Millisecond), read.screens, read.swipes, read.calls, read.stop)
		return
	}
	t.Logf("%s: old=%v (%d swipes), phone_read_list=%v (%d screens, %d swipes, %d calls, stop %q), speedup %.2fx",
		url, baseline.elapsed.Round(time.Millisecond), baseline.swipes,
		read.elapsed.Round(time.Millisecond), read.screens, read.swipes, read.calls, read.stop,
		float64(baseline.elapsed)/float64(read.elapsed))
}

type webMeasure struct {
	elapsed time.Duration
	screens int
	swipes  int
	calls   int
	stop    string
	text    string
}

func measureWebSwipes(t *testing.T, ctx context.Context, g *Gateway, area screen.Rect,
	screens, scrollPercent, swipeMS int) webMeasure {
	t.Helper()
	s := g.OpenSession(AgentInfo{Name: "Web baseline"})
	defer s.CloseNow()
	if res := s.Connect(ctx, ConnectRequest{Task: "Measure a web feed", Apps: []string{"com.apple.mobilesafari"}, ReadOnly: true}); res.Error != "" {
		t.Fatalf("connect: %+v", res)
	}
	res := s.Observe(ctx, ObserveRequest{})
	if res.Error != "" {
		t.Fatalf("observe: %+v", res)
	}
	distance := area.H * scrollPercent / 100
	from := screen.Point{X: area.X + area.W*4/5, Y: area.Y + area.H/2 + distance/2}
	to := screen.Point{X: from.X, Y: area.Y + area.H/2 - distance/2}
	start := time.Now()
	for i := 1; i < screens; i++ {
		res = s.Act(ctx, Call{}, Action{Tool: "swipe", From: &from, To: &to, DurationMS: swipeMS})
		if res.Error != "" {
			t.Fatalf("swipe %d: %+v", i, res)
		}
		if i == 1 || i%5 == 0 || i == screens-1 {
			t.Logf("old path: %d/%d screens after %v", i+1, screens, time.Since(start).Round(time.Millisecond))
		}
	}
	elapsed := time.Since(start)
	if res := s.Release(ctx, ReleaseRequest{Outcome: "done", Summary: "Measured the old web path"}); res.Error != "" {
		t.Fatalf("release: %+v", res)
	}
	return webMeasure{elapsed: elapsed, screens: screens, swipes: screens - 1, calls: screens - 1}
}

func measureWebList(t *testing.T, ctx context.Context, g *Gateway, area screen.Rect, screens, scrollPercent int) webMeasure {
	t.Helper()
	s := g.OpenSession(AgentInfo{Name: "Web list read"})
	defer s.CloseNow()
	if res := s.Connect(ctx, ConnectRequest{Task: "Read a web feed", Apps: []string{"com.apple.mobilesafari"}, ReadOnly: true}); res.Error != "" {
		t.Fatalf("connect: %+v", res)
	}
	if res := s.Observe(ctx, ObserveRequest{}); res.Error != "" {
		t.Fatalf("observe: %+v", res)
	}
	start := time.Now()
	var continuation string
	measure := webMeasure{}
	for measure.screens < screens && measure.calls < 8 {
		res := s.ReadList(ctx, ReadListRequest{
			Area: &area, Direction: "up", MaxScreens: screens, MaxMS: 45000,
			OCR: "auto", ScrollPercent: scrollPercent, Continuation: continuation,
		})
		if res.Error != "" || res.Scan == nil {
			t.Fatalf("read list: %+v", res)
		}
		measure.calls++
		measure.screens += res.Scan.Pages
		measure.swipes += res.Scan.Swipes
		measure.stop = res.Scan.Stop
		measure.text += scanText(res.Scan) + "\n"
		if res.Scan.Complete || res.Scan.Continuation == "" {
			break
		}
		continuation = res.Scan.Continuation
	}
	measure.elapsed = time.Since(start)
	return measure
}

func parseWebArea(raw string, fallback screen.Rect) (screen.Rect, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return screen.Rect{}, fmt.Errorf("PINKY_WEB_AREA must be x,y,width,height")
	}
	var n [4]int
	for i := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return screen.Rect{}, fmt.Errorf("PINKY_WEB_AREA: %w", err)
		}
		n[i] = v
	}
	return screen.Rect{X: n[0], Y: n[1], W: n[2], H: n[3]}, nil
}

func openWeb(t *testing.T, udid, url string) {
	t.Helper()
	fresh, err := benchmarkURL(url)
	if err != nil {
		t.Fatalf("build benchmark URL: %v", err)
	}
	cmd := exec.Command("xcrun", "simctl", "openurl", udid, fresh)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("open %s: %v\n%s", fresh, err, out)
	}
	time.Sleep(4 * time.Second)
}

func benchmarkURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("pinky_benchmark", strconv.FormatInt(time.Now().UnixNano(), 10))
	u.RawQuery = q.Encode()
	return u.String(), nil
}
