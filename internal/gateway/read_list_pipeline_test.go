package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// These tests cover phone_read_list as a run of phone_swipe steps through
// the action pipeline: the person's controls, the agent's scope and the
// settle each swipe owes apply to it as to any other swipe.

func threePages() [][]string {
	return [][]string{
		{"first long title", "second long title", "third long title"},
		{"second long title", "third long title", "fourth long title"},
		{"third long title", "fourth long title", "fifth long title"},
	}
}

func swipeSteps(g *Gateway) int {
	n := 0
	for _, t := range g.Tasks() {
		for _, st := range t.Steps {
			if st.Tool == "phone_swipe" {
				n++
			}
		}
	}
	return n
}

func TestReadListStopMidReadReturnsNothing(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	var stopErr error
	dev.afterSwipe = func() { stopErr = g.Stop("pixel") }
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	if res.Status != StatusStopped || res.Scan != nil {
		t.Fatalf("stop mid-read: %+v", res)
	}
	if dev.swipes != 1 {
		t.Fatalf("%d swipes after the stop, want 1", dev.swipes)
	}
}

func TestReadListPauseMidReadReturnsNothing(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	var pauseErr error
	dev.afterSwipe = func() { pauseErr = g.PauseDevice("pixel") }
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if pauseErr != nil {
		t.Fatal(pauseErr)
	}
	if res.Error != ErrDevicePaused || res.Scan != nil {
		t.Fatalf("pause mid-read: %+v", res)
	}
	if dev.swipes != 1 {
		t.Fatalf("%d swipes after the pause, want 1", dev.swipes)
	}
}

func TestReadListWaitsForAnOpenApproval(t *testing.T) {
	g, dev := newTestGateway(t, func(o *Options) { o.MaxBlock = 40 * time.Millisecond })
	s, _ := connect(t, g, "Lunch")
	cart := toCart(t, s)
	res := tap(s, ref(t, cart, "Place order · $12.80"))
	mustStatus(t, res, StatusPending)
	read := s.ReadList(context.Background(), ReadListRequest{Area: &screen.Rect{X: 0, Y: 200, W: 1000, H: 1200}, MaxMS: 5000})
	if read.Status != StatusPending || read.ApprovalID != res.ApprovalID {
		t.Fatalf("read with an approval open: %+v", read)
	}
	if read.Scan != nil && read.Scan.Swipes != 0 {
		t.Fatalf("swiped with an approval open: %+v", read.Scan)
	}
	if dev.Current() != "cart" || swipeSteps(g) != 0 {
		t.Fatalf("acted with an approval open: screen %s, %d swipes", dev.Current(), swipeSteps(g))
	}
}

func TestReadListWatchOnly(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeWatchOnly})
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Error != ErrScopeDenied || dev.swipes != 0 {
		t.Fatalf("watch-only read: %+v, %d swipes", res, dev.swipes)
	}
}

func TestReadListReadOnly(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev)
	s := g.OpenSession(agent)
	t.Cleanup(s.Close)
	mustStatus(t, s.Connect(context.Background(), ConnectRequest{Task: "Read a list", ReadOnly: true}), StatusSettled)
	// Scrolling a list is reading: a read-only task reads it.
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.IsError() || res.Scan == nil || res.Scan.Swipes != 2 || !strings.Contains(scanText(res.Scan), "fifth long title") {
		t.Fatalf("read-only read: %+v", res)
	}

	// A swipe that starts on a wide button in the list scrolls it.
	dev.page, dev.swipes = 0, 0
	dev.overlay = []screen.Element{{
		Role: screen.RoleButton, Label: "Place order · $12.80",
		Bounds: screen.Rect{X: 0, Y: 1500, W: 1080, H: 500},
	}}
	res = s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.IsError() || res.Scan == nil || res.Scan.Swipes != 2 {
		t.Fatalf("read-only scroll from a wide button: %+v", res)
	}

	// But a swipe that may slide a protected slider is refused, and
	// nothing asks the person. (Scrolling from a wide button is a scroll.)
	dev.page, dev.swipes = 0, 0
	dev.overlay = []screen.Element{{
		Role: screen.RoleOther, Label: "Slide to pay $12.80",
		Bounds: screen.Rect{X: 0, Y: 800, W: 1080, H: 1200},
	}}
	res = s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Status != StatusBlocked || res.Scan == nil || res.Scan.Stop != "blocked" || dev.swipes != 0 {
		t.Fatalf("read-only over a protected control: %+v, %d swipes", res, dev.swipes)
	}
	if needs := g.Needs(); len(needs) != 0 {
		t.Fatalf("read-only read asked the person: %+v", needs)
	}
}

func TestReadListOutsideTheAgentsApps(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()} // in Dinely
	g := newDevGateway(t, base, dev)
	g.SetAgentScope(agent.Name, Scope{Mode: ScopeApps, Apps: []string{"Chatter"}})
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	// The agent may look at the screen, as phone_observe does, but not
	// swipe in an app it was not given.
	if res.Error != ErrScopeDenied || res.Scan == nil || res.Scan.Stop != "scope" || res.Scan.Pages != 1 || dev.swipes != 0 {
		t.Fatalf("read outside the agent's apps: %+v, %d swipes", res, dev.swipes)
	}
}

func TestReadListAsksNobodyUnderEveryAction(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev)
	g.Policy().SetPreset(policy.PresetEvery)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Status != StatusBlocked || res.Rule != policy.RuleEvery || res.Scan == nil || res.Scan.Stop != "blocked" {
		t.Fatalf("every-action read: %+v", res)
	}
	if !strings.Contains(res.Message, "approve every action") || strings.Contains(res.Message, "protected") {
		t.Fatalf("every-action message: %q", res.Message)
	}
	if res.Scan.Pages != 1 || dev.swipes != 0 || len(g.Needs()) != 0 {
		t.Fatalf("every-action read swiped or asked: %+v, %d swipes, needs %+v", res.Scan, dev.swipes, g.Needs())
	}
}

func TestReadListContinuationIsRejectedElsewhere(t *testing.T) {
	read := func(t *testing.T, s *Session) string {
		t.Helper()
		res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 1, MaxMS: 5000, OCR: "off"})
		if res.IsError() || res.Scan == nil || res.Scan.Continuation == "" {
			t.Fatalf("first part: %+v", res)
		}
		if !strings.HasPrefix(res.Scan.Continuation, "rl_") || strings.Contains(res.Scan.Continuation, "read-list") {
			t.Fatalf("continuation is not opaque: %q", res.Scan.Continuation)
		}
		return res.Scan.Continuation
	}
	again := func(s *Session, token string) *Result {
		return s.ReadList(context.Background(), ReadListRequest{Continuation: token, MaxScreens: 2, MaxMS: 5000, OCR: "off"})
	}

	t.Run("task", func(t *testing.T) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		dev := &readListDevice{Device: base, pages: threePages()}
		g := newDevGateway(t, base, dev)
		s, _ := connect(t, g, "Read a list")
		token := read(t, s)
		mustStatus(t, s.Release(context.Background(), ReleaseRequest{Outcome: OutcomeDone}), StatusSettled)
		mustStatus(t, s.Connect(context.Background(), ConnectRequest{Task: "Another task"}), StatusSettled)
		if res := again(s, token); res.Error != ErrInvalidArgument || dev.swipes != 0 {
			t.Fatalf("continuation in another task: %+v", res)
		}
	})
	t.Run("phone", func(t *testing.T) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		dev := &readListDevice{Device: base, pages: threePages()}
		other := demo.NewDevice("pixel8", "Pixel 8", driver.Android)
		g := newDevGateway(t, base, dev, func(o *Options) {
			o.Providers = append(o.Providers, demo.NewProvider(driver.Android, other))
		})
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		mustStatus(t, s.Connect(context.Background(), ConnectRequest{Call: Call{Device: "pixel"}, Task: "Read a list"}), StatusSettled)
		token := read(t, s)
		s2 := g.OpenSession(AgentInfo{Name: agent.Name, Client: agent.Client})
		t.Cleanup(s2.Close)
		mustStatus(t, s2.Connect(context.Background(), ConnectRequest{Call: Call{Device: "pixel8"}, Task: "Read on another phone"}), StatusSettled)
		res := s2.ReadList(context.Background(), ReadListRequest{Call: Call{Device: "pixel8"}, Continuation: token, MaxMS: 5000})
		if res.Error != ErrInvalidArgument {
			t.Fatalf("continuation on another phone: %+v", res)
		}
	})
	t.Run("app", func(t *testing.T) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		dev := &readListDevice{Device: base, pages: threePages()}
		g := newDevGateway(t, base, dev)
		s, _ := connect(t, g, "Read a list")
		token := read(t, s)
		dev.app = screen.App{ID: demo.Chatter, Name: "Chatter"}
		if res := again(s, token); res.Error != ErrInvalidArgument || dev.swipes != 0 {
			t.Fatalf("continuation in another app: %+v", res)
		}
	})
	t.Run("once", func(t *testing.T) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		dev := &readListDevice{Device: base, pages: threePages()}
		g := newDevGateway(t, base, dev)
		s, _ := connect(t, g, "Read a list")
		token := read(t, s)
		if res := again(s, token); res.IsError() {
			t.Fatalf("continuation: %+v", res)
		}
		if res := again(s, token); res.Error != ErrInvalidArgument {
			t.Fatalf("continuation used twice: %+v", res)
		}
	})
	// A call that passes another direction or area with the continuation is
	// told what to pass instead. The continuation is still there for that
	// call: the error would be of no use otherwise.
	t.Run("argument", func(t *testing.T) {
		base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
		dev := &readListDevice{Device: base, pages: threePages()}
		g := newDevGateway(t, base, dev)
		s, _ := connect(t, g, "Read a list")
		token := read(t, s)
		res := s.ReadList(context.Background(), ReadListRequest{Continuation: token, Direction: "down", MaxScreens: 2, MaxMS: 5000, OCR: "off"})
		if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "leave direction out") || dev.swipes != 0 {
			t.Fatalf("continuation with another direction: %+v", res)
		}
		res = s.ReadList(context.Background(), ReadListRequest{Continuation: token, Area: &screen.Rect{X: 0, Y: 300, W: 1080, H: 900},
			MaxScreens: 2, MaxMS: 5000, OCR: "off"})
		if res.Error != ErrInvalidArgument || !strings.Contains(res.Message, "another scroll_area") || dev.swipes != 0 {
			t.Fatalf("continuation with another area: %+v", res)
		}
		if res := again(s, token); res.IsError() {
			t.Fatalf("continuation after two refused calls: %+v", res)
		}
		if res := again(s, token); res.Error != ErrInvalidArgument {
			t.Fatalf("continuation used twice: %+v", res)
		}
	})
}

func TestReadListEndMarkerCountsOnlyAtTheEndReadToward(t *testing.T) {
	cases := []struct {
		name      string
		direction string
		pages     [][]string
	}{
		// Read downwards (towards the top), "No more results" at the
		// bottom says nothing about what is above.
		{"bottom while reading up the list", "down", [][]string{{"first long title", "second long title", "No more results"}}},
		// In the middle of the screen it is a row like any other.
		{"middle of the screen", "up", [][]string{{"first long title", "No more results", "second long title"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
			dev := &readListDevice{Device: base, pages: c.pages}
			g := newDevGateway(t, base, dev)
			s, _ := connect(t, g, "Read a list")
			res := s.ReadList(context.Background(), ReadListRequest{Direction: c.direction, MaxScreens: 1, MaxMS: 5000, OCR: "off"})
			if res.IsError() || res.Scan == nil || res.Scan.Stop == "end" || res.Scan.Complete {
				t.Fatalf("marker taken as the end: %+v", res.Scan)
			}
		})
	}
}

func TestReadListOwesAndWaitsOutTheSettle(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages(), unsettled: true}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	// The list's text held still, so the read went on (a live video can
	// keep the pixels moving), but the settle stays owed.
	if res.IsError() || res.Scan == nil || res.Scan.Swipes != 2 || res.Scan.Continuation == "" {
		t.Fatalf("unsettled read: %+v", res)
	}
	g.mu.Lock()
	due := g.devices["pixel"].settleDue
	g.mu.Unlock()
	if due.IsZero() {
		t.Fatal("the swipe that did not settle owes no settle")
	}

	// The next call waits for the screen to settle before it reads it.
	dev.unsettled = false
	waits := dev.waits
	res = s.ReadList(context.Background(), ReadListRequest{Continuation: res.Scan.Continuation, MaxScreens: 1, MaxMS: 5000, OCR: "off"})
	if res.IsError() || dev.waits == waits {
		t.Fatalf("read did not wait out the owed settle: %+v, waits %d", res, dev.waits-waits)
	}
}

func TestReadListStartsNoSwipeWithoutTheCallBudget(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages()}
	g := newDevGateway(t, base, dev, func(o *Options) { o.CallBudget = 3 * time.Second })
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 30000, OCR: "off"})
	if res.IsError() || res.Scan == nil || res.Scan.Stop != "deadline" || res.Scan.Swipes != 0 ||
		res.Scan.Continuation == "" || dev.swipes != 0 {
		t.Fatalf("read without budget for a swipe: %+v, %d swipes", res.Scan, dev.swipes)
	}
}

func TestReadListIsUncertainWhenTheScreenCannotBeReadAfterASwipe(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{Device: base, pages: threePages(), failAfter: 1}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Status != StatusUncertain || res.Scan == nil || res.Scan.Stop != "uncertain" ||
		res.Scan.Continuation != "" || res.Scan.Pages != 1 {
		t.Fatalf("unreadable screen after a swipe: %+v", res)
	}
	if swipeSteps(g) != 1 {
		t.Fatalf("%d swipe steps, want 1", swipeSteps(g))
	}
}
