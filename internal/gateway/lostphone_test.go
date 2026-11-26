package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// TestLostPhoneNeedsSomeoneAtTheComputer: a phone the computer's device
// service lost on Wi-Fi (driver.SetupDeviceService) comes back only when a
// person at the computer restarts the service with an administrator's
// password. An agent that connects is told so, and that an unattended run
// should not wait for it; the person gets a Needs you card (which the app
// notifies) without the agent handing over; a handoff that is still open
// says so again each time it returns pending, so an agent nobody watches
// does not wait out the whole handoff; and the card goes once the phone is
// back.
func TestLostPhoneNeedsSomeoneAtTheComputer(t *testing.T) {
	p := &stepProvider{dev: demo.NewDevice("iphone", "iPhone 13 mini", driver.IOS)}
	lost := driver.Info{ID: "iphone", Platform: driver.IOS, Name: "iPhone 13 mini", Model: "iPhone 13 mini", Connection: "wifi",
		State: driver.StateNeedsSetup, Setup: driver.SetupDeviceService,
		Detail: "This Mac's device service (usbmuxd) lost the iPhone on Wi-Fi."}
	p.list(&lost)
	g, err := New(Options{Providers: []driver.Provider{p}, Policy: testPolicy(t), TrustNewAgents: true,
		MaxBlock: 100 * time.Millisecond, ReconnectGrace: 50 * time.Millisecond, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	viewUntil(t, g, "iphone", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	for _, n := range g.Needs() {
		if n.Kind == NeedSetup {
			t.Fatalf("a card without a task waiting: %+v", n)
		}
	}

	ctx := context.Background()
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Check the weather"})
	if res.Status != StatusNeedsSetup || res.Setup != driver.SetupDeviceService || res.Task == "" {
		t.Fatalf("connecting: %+v", res)
	}
	for _, want := range []string{lost.Detail, "Only a person at this computer", "administrator password", "Needs you",
		"If nobody is at the computer", "do not wait", "phone_release", "phone_handoff"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("connecting: the message lacks %q: %s", want, res.Message)
		}
	}

	card := needsUntil(t, g, "the lost phone", func(n NeedView) bool { return n.Kind == NeedSetup && n.DeviceID == "iphone" })
	if card.Title != "Restart the device service for iPhone 13 mini" || card.TaskID != res.Task || card.Agent != "Claude Code" ||
		!strings.Contains(card.Detail, "only someone at the computer can restart it") || !strings.HasSuffix(card.Detail, "Claude Code is waiting · Check the weather") {
		t.Errorf("the card: %+v", card)
	}

	// The handoff blocks for MaxBlock, then returns pending with the same
	// advice.
	h := s.Handoff(ctx, HandoffRequest{Reason: "Please restart the device service in Pinky"})
	if h.Status != StatusPending || h.ApprovalID == "" {
		t.Fatalf("the handoff: %+v", h)
	}
	for _, want := range []string{"Still waiting", "needs a person at the computer", "stop waiting", "phone_release"} {
		if !strings.Contains(h.Message, want) {
			t.Errorf("the pending handoff lacks %q: %s", want, h.Message)
		}
	}

	// Restarted: the phone is back, the handoff ends, the card goes.
	done := make(chan *Result, 1)
	go func() {
		done <- s.Handoff(ctx, HandoffRequest{Call: Call{ApprovalID: h.ApprovalID}, Reason: "Please restart the device service in Pinky"})
	}()
	back := lost
	back.State, back.Setup, back.Detail = driver.StateOnline, "", ""
	p.list(&back)
	select {
	case r := <-done:
		if r.IsError() || r.Status == StatusPending {
			t.Fatalf("the handoff once the phone was back: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handoff did not end once the phone was back")
	}
	for _, n := range g.Needs() {
		if n.Kind == NeedSetup {
			t.Errorf("the card stayed: %+v", n)
		}
	}
}

// TestPendingHandoffWithoutLostPhone: a handoff on a phone that does not
// wait for the device service returns pending without the advice for one.
func TestPendingHandoffWithoutLostPhone(t *testing.T) {
	p := &stepProvider{dev: demo.NewDevice("px", "Pixel 8a", driver.Android)}
	allow := driver.Info{ID: "px", Platform: driver.Android, Name: "Pixel 8a", Model: "Pixel 8a", Connection: "usb", Backend: "scrcpy",
		State: driver.StateUnauthorized, Detail: "Unlock the phone and allow USB debugging for this computer."}
	p.list(&allow)
	g, err := New(Options{Providers: []driver.Provider{p}, Policy: testPolicy(t), TrustNewAgents: true,
		MaxBlock: 100 * time.Millisecond, ReconnectGrace: 50 * time.Millisecond, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	viewUntil(t, g, "px", "listed", func(v DeviceView) bool { return v.Status == DeviceNeedsSetup })
	ctx := context.Background()
	s := g.OpenSession(AgentInfo{Name: "Claude Code"})
	defer s.Close()
	if res := s.Connect(ctx, ConnectRequest{Task: "Lunch"}); res.Status != StatusNeedsSetup || strings.Contains(res.Message, "administrator") {
		t.Fatalf("connecting: %+v", res)
	}
	h := s.Handoff(ctx, HandoffRequest{Reason: "Allow USB debugging on the phone"})
	if h.Status != StatusPending || strings.Contains(h.Message, "device service") || strings.Contains(h.Message, "stop waiting") {
		t.Fatalf("the handoff: %+v", h)
	}
	for _, n := range g.Needs() {
		if n.Kind == NeedSetup {
			t.Errorf("a setup card for a step on the phone the agent asks for itself: %+v", n)
		}
	}
}
