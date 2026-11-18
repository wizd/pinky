package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A touch on the phone's own screen never pauses a task that works on a
// background display, whether no task held the phone when it came, a
// background task did, or the display was still opening (issue #131). The
// Take over button, and a touch that paused a task on the phone's own
// screen, stay until Hand back, across tasks.
func TestBackgroundTaskAfterTouches(t *testing.T) {
	ctx := context.Background()
	// background connects a new task on a background display, without
	// own_screen, and returns its session and result.
	background := func(t *testing.T, g *Gateway) (*Session, *Result) {
		t.Helper()
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		return s, s.Connect(ctx, ConnectRequest{Task: "Lunch"})
	}
	setup := func(t *testing.T) (*extraDev, *Gateway) {
		t.Helper()
		x := newExtraDev("pixel", "Pixel 9")
		g := newExtraGateway(t, x)
		if err := g.SetBackground("pixel", true); err != nil {
			t.Fatal(err)
		}
		return x, g
	}
	works := func(t *testing.T, g *Gateway, s *Session, res *Result) {
		t.Helper()
		mustStatus(t, res, StatusSettled)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
		if d := g.Devices()[0]; d.Status == DeviceInControl {
			t.Fatalf("device %+v", d)
		}
	}
	paused := func(t *testing.T, g *Gateway, s *Session, res *Result) {
		t.Helper()
		mustStatus(t, res, StatusPaused)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusPaused)
		if d := g.Devices()[0]; d.Status != DeviceInControl {
			t.Fatalf("device %+v", d)
		}
	}
	touch := func(t *testing.T, g *Gateway) {
		t.Helper()
		if err := g.PersonTouched("pixel"); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("touch right after release", func(t *testing.T) {
		_, g := setup(t)
		s, res := background(t, g)
		works(t, g, s, res)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		touch(t, g)
		s2, res := background(t, g)
		works(t, g, s2, res)
	})

	t.Run("touch with no task", func(t *testing.T) {
		x, g := setup(t)
		s, res := background(t, g)
		works(t, g, s, res)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		time.Sleep(30 * time.Millisecond)
		x.PersonTouch() // through the driver's watcher, as on a phone
		touch(t, g)
		time.Sleep(30 * time.Millisecond)
		s2, res := background(t, g)
		works(t, g, s2, res)
	})

	t.Run("touch while the display opens", func(t *testing.T) {
		x, g := setup(t)
		x.mu.Lock()
		x.bgOpening = func() { g.PersonTouched("pixel") }
		x.mu.Unlock()
		s, res := background(t, g)
		works(t, g, s, res)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		s2, res := background(t, g)
		works(t, g, s2, res)
	})

	t.Run("touch while a display that fails opens", func(t *testing.T) {
		// The task moves to the phone's own screen, which the person was
		// just using: they keep it until they hand it over.
		x, g := setup(t)
		x.mu.Lock()
		x.bgErr = errors.New("no display")
		x.bgOpening = func() { g.PersonTouched("pixel") }
		x.mu.Unlock()
		x.Show("settings")
		s, res := background(t, g)
		mustStatus(t, res, StatusPaused)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusTookOver)
		if x.Current() != "settings" {
			t.Fatalf("the agent took the person's screen: %q", x.Current())
		}
	})

	t.Run("touch while the phone was away", func(t *testing.T) {
		_, g := setup(t)
		s, res := background(t, g)
		works(t, g, s, res)
		// The phone dropped and came back (Wi-Fi ADB): its display went
		// with it, and the next call opens another.
		d, err := g.device("pixel")
		if err != nil {
			t.Fatal(err)
		}
		g.mu.Lock()
		bg := d.lease.bg
		d.lease.bg = nil
		g.mu.Unlock()
		bg.Close()
		touch(t, g)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"}), StatusSettled)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		s2, res := background(t, g)
		works(t, g, s2, res)
	})

	t.Run("take over with no task", func(t *testing.T) {
		_, g := setup(t)
		if err := g.TakeOver("pixel"); err != nil {
			t.Fatal(err)
		}
		s, res := background(t, g)
		paused(t, g, s, res)
		if err := g.HandBack("pixel", ""); err != nil {
			t.Fatal(err)
		}
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	})

	t.Run("take over during a background task", func(t *testing.T) {
		_, g := setup(t)
		s, res := background(t, g)
		works(t, g, s, res)
		if err := g.TakeOver("pixel"); err != nil {
			t.Fatal(err)
		}
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		touch(t, g)
		s2, res := background(t, g)
		paused(t, g, s2, res)
	})

	t.Run("touch during a task on the phone's own screen", func(t *testing.T) {
		_, g := setup(t)
		s := g.OpenSession(agent)
		t.Cleanup(s.Close)
		mustStatus(t, s.Connect(ctx, ConnectRequest{Task: "Tidy the home screen", OwnScreen: true}), StatusSettled)
		touch(t, g)
		mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusTookOver)
		mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
		s2, res := background(t, g)
		paused(t, g, s2, res)
	})
}
