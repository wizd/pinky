//go:build wdasim

package gateway

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// simulatorProvider exposes one WebDriverAgent server as a gateway phone.
type simulatorProvider struct {
	dev driver.Device
}

func (p *simulatorProvider) Platform() driver.Platform { return driver.IOS }

func (p *simulatorProvider) List(context.Context) ([]driver.Info, error) {
	return []driver.Info{p.dev.Info()}, nil
}

func (p *simulatorProvider) Open(context.Context, string) (driver.Device, error) {
	return p.dev, nil
}

// The baseline opens 音潮 on the Simulator and reads 20 screens through the
// same per-action gateway pipeline that phone_swipe uses. It records the
// infrastructure time only; an outer model's thinking is outside Pinky.
//
//	PINKY_WDA_URL=http://127.0.0.1:18100 PINKY_MJPEG_URL=http://127.0.0.1:19100 \
//	  go test -tags wdasim -count=1 -v -run TestReadListSimulatorBaseline ./internal/gateway
func TestReadListSimulatorBaseline(t *testing.T) {
	base := os.Getenv("PINKY_WDA_URL")
	if base == "" {
		t.Skip("PINKY_WDA_URL not set")
	}
	if os.Getenv("PINKY_TEST_EXPLORE_READY") != "1" {
		t.Skip("open 音潮 Explore at the top and set PINKY_TEST_EXPLORE_READY=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dev, err := ios.OpenWDAURL(ctx, base, os.Getenv("PINKY_MJPEG_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer dev.Close()

	pol := policy.Default()
	g, err := New(Options{
		Providers:      []driver.Provider{&simulatorProvider{dev: dev}},
		Policy:         pol,
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

	s := g.OpenSession(AgentInfo{Name: "Simulator baseline"})
	defer s.CloseNow()
	app := os.Getenv("PINKY_TEST_APP")
	if app == "" {
		app = "tech.ycyx.dev.yinchao"
	}
	res := s.Connect(ctx, ConnectRequest{
		Task:     "Measure long-list reading",
		Apps:     []string{app},
		ReadOnly: true,
	})
	if res.Error != "" {
		t.Fatalf("connect: %+v", res)
	}
	res = s.Observe(ctx, ObserveRequest{})
	if res.Error != "" {
		t.Fatalf("observe Explore: %+v", res)
	}
	list, ok := simLargestList(res.Screen)
	if !ok {
		t.Skip("the long Explore feed is not in front")
	}
	if list.Bounds.H < 1500 {
		t.Skip("the long Explore feed is not in front")
	}

	var labels int
	start := time.Now()
	var took []time.Duration
	for i := 0; i < 19; i++ {
		one := time.Now()
		res = s.Act(ctx, Call{}, Action{Tool: "swipe", Ref: list.Ref, Direction: "up"})
		if res.Error != "" {
			t.Fatalf("swipe %d: %+v", i+1, res)
		}
		took = append(took, time.Since(one))
		if res.Screen != nil {
			labels += countLabels(res.Screen)
		}
		if cur, next := simLargestList(res.Screen); next {
			list = cur
		} else {
			// A diff may leave only part of the screen. Read the next full
			// screen before continuing.
			res = s.Observe(ctx, ObserveRequest{})
			if res.Error != "" {
				t.Fatalf("observe after swipe %d: %+v", i+1, res)
			}
			if list, ok = simLargestList(res.Screen); !ok {
				t.Fatalf("list disappeared after swipe %d", i+1)
			}
		}
	}
	total := time.Since(start)
	var sum time.Duration
	for _, d := range took {
		sum += d
	}
	t.Logf("20 screens through phone_swipe pipeline: total %v, action sum %v, average %v, %d visible labels",
		total.Round(time.Millisecond), sum.Round(time.Millisecond), (sum / time.Duration(len(took))).Round(time.Millisecond), labels)
	for i, d := range took {
		t.Logf("swipe %02d: %v", i+1, d.Round(time.Millisecond))
	}
}

// TestReadListSimulator measures the same 音潮 Explore feed through the new
// one-call list pipeline. Partial results continue from their cursor, so the
// total includes every call needed to reach the requested screen count.
//
//	PINKY_WDA_URL=http://127.0.0.1:18100 PINKY_MJPEG_URL=http://127.0.0.1:19100 \
//	  go test -tags wdasim -count=1 -v -run TestReadListSimulator ./internal/gateway
func TestReadListSimulator(t *testing.T) {
	base := os.Getenv("PINKY_WDA_URL")
	if base == "" {
		t.Skip("PINKY_WDA_URL not set")
	}
	if os.Getenv("PINKY_TEST_EXPLORE_READY") != "1" {
		t.Skip("open 音潮 Explore at the top and set PINKY_TEST_EXPLORE_READY=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
		IdleTimeout:    5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	s := g.OpenSession(AgentInfo{Name: "Simulator read-list"})
	defer s.CloseNow()
	app := os.Getenv("PINKY_TEST_APP")
	if app == "" {
		app = "tech.ycyx.dev.yinchao"
	}
	scrollPercent := 60
	if raw := os.Getenv("PINKY_LIST_SCROLL"); raw != "" {
		scrollPercent, err = strconv.Atoi(raw)
		if err != nil || scrollPercent < 30 || scrollPercent > 80 {
			t.Fatalf("PINKY_LIST_SCROLL must be between 30 and 80")
		}
	}
	res := s.Connect(ctx, ConnectRequest{
		Task: "Read a long list", Apps: []string{app}, ReadOnly: true,
	})
	if res.Error != "" {
		t.Fatalf("connect: %+v", res)
	}
	if res = s.Observe(ctx, ObserveRequest{}); res.Error != "" {
		t.Fatalf("observe Explore: %+v", res)
	}
	list, ok := simLargestList(res.Screen)
	if !ok {
		t.Skip("the long Explore feed is not in front")
	}
	if list.Bounds.H < 1500 {
		t.Skip("the long Explore feed is not in front")
	}

	start := time.Now()
	var continuation string
	var calls []*ScanView
	var lines int
	for len(calls) < 3 {
		res = s.ReadList(ctx, ReadListRequest{
			Ref: list.Ref, MaxScreens: 20, MaxMS: 45000, OCR: "auto",
			ScrollPercent: scrollPercent, Continuation: continuation,
		})
		if res.Error != "" || res.Scan == nil {
			t.Fatalf("read list: %+v", res)
		}
		calls = append(calls, res.Scan)
		for _, page := range res.Scan.Sections {
			lines += len(page.Lines)
		}
		if res.Scan.Complete || res.Scan.Stop != "deadline" || res.Scan.Continuation == "" {
			break
		}
		continuation = res.Scan.Continuation
	}
	total := time.Since(start)
	pages, swipes, overlaps := 0, 0, 0
	for _, call := range calls {
		pages += call.Pages
		swipes += call.Swipes
		overlaps += call.Overlap
	}
	t.Logf("read_list simulator: total %v, calls %d, screens %d, swipes %d, new lines %d, overlaps %d, stop %q",
		total.Round(time.Millisecond), len(calls), pages, swipes, lines, overlaps, calls[len(calls)-1].Stop)
	for i, call := range calls {
		t.Logf("call %d: screens %d, swipes %d, lines %d, stop %q",
			i+1, call.Pages, call.Swipes, call.Lines, call.Stop)
	}
	if last := calls[len(calls)-1]; last.Stop != "end" || !last.Complete || pages < 2 || lines < 10 {
		t.Fatalf("simulator measurement was not a complete useful list: pages=%d lines=%d stop=%q complete=%v",
			pages, lines, last.Stop, last.Complete)
	}
}

func simLargestList(s *screen.Screen) (screen.Element, bool) {
	if s == nil {
		return screen.Element{}, false
	}
	var out screen.Element
	for _, e := range s.Elements {
		if e.Role != screen.RoleList || e.Bounds.Empty() {
			continue
		}
		if out.Ref == 0 || e.Bounds.W*e.Bounds.H > out.Bounds.W*out.Bounds.H {
			out = e
		}
	}
	return out, out.Ref != 0
}

func countLabels(s *screen.Screen) int {
	n := 0
	for _, e := range s.Elements {
		if strings.TrimSpace(e.Label) != "" {
			n++
		}
	}
	return n
}
