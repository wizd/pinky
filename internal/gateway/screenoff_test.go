package gateway

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestWatchedScreenOffShows says "Screen off" for an idle phone whose own
// screen is off without a lock screen, such as an Android TV in standby,
// while a mirror watches it: its mirror gets no picture and would
// otherwise seem to be connecting. Wake turns the screen on, and the
// phone is idle again.
func TestWatchedScreenOffShows(t *testing.T) {
	saved := idleLockEvery
	idleLockEvery = 10 * time.Millisecond
	t.Cleanup(func() { idleLockEvery = saved })
	x := newExtraDev("tv", "TV")
	g := newExtraGateway(t, x)
	view := func() DeviceView {
		if ds := g.Devices(); len(ds) > 0 {
			return ds[0]
		}
		return DeviceView{}
	}
	waitUntil(t, "the TV to come online", func() bool { return view().Status == DeviceIdle })

	x.setScreen(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := g.Stream(ctx, "tv", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitUntil(t, "Screen off", func() bool { v := view(); return v.ScreenOff && v.StatusLine == "Screen off" })
	if v := view(); v.Status != DeviceIdle {
		t.Fatalf("a TV with its screen off shows %q, want idle", v.Status)
	}

	if err := g.Wake("tv"); err != nil {
		t.Fatal(err)
	}
	if v := view(); v.ScreenOff || v.StatusLine != "Idle" {
		t.Fatalf("after Wake the TV shows %q (screen off %v)", v.StatusLine, v.ScreenOff)
	}
	x.mu.Lock()
	wakes := x.wakes
	x.mu.Unlock()
	if wakes != 1 {
		t.Fatalf("Wake pressed wake %d times, want 1", wakes)
	}
}

// TestScreenOffNeedsWatcherAndNoLock reads the screen only for a phone a
// mirror watches, and a locked phone says Locked, not Screen off: the lock
// screen shows when it wakes.
func TestScreenOffNeedsWatcherAndNoLock(t *testing.T) {
	saved := idleLockEvery
	idleLockEvery = 10 * time.Millisecond
	t.Cleanup(func() { idleLockEvery = saved })
	x := newExtraDev("pixel", "Pixel 9")
	g := newExtraGateway(t, x)
	view := func() DeviceView {
		if ds := g.Devices(); len(ds) > 0 {
			return ds[0]
		}
		return DeviceView{}
	}
	waitUntil(t, "the phone to come online", func() bool { return view().Status == DeviceIdle })

	x.setScreen(false)
	time.Sleep(100 * time.Millisecond) // several lock reads
	if v := view(); v.ScreenOff {
		t.Fatal("the screen of a phone nobody watches was read")
	}

	ctx, cancel := context.WithCancel(context.Background())
	s, err := g.Stream(ctx, "pixel", driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "Screen off", func() bool { return view().ScreenOff })
	x.SetLocked(true)
	waitUntil(t, "Locked", func() bool { return view().Status == DeviceLocked })
	if v := view(); v.ScreenOff {
		t.Fatal("a locked phone still says its screen is off")
	}

	x.SetLocked(false)
	waitUntil(t, "Screen off again", func() bool { return view().ScreenOff })
	// Nobody watches any more: nothing keeps it current, so it goes.
	s.Close()
	cancel()
	waitUntil(t, "Screen off to go once nobody watches", func() bool { return !view().ScreenOff })
}

// TestWakeUnknownPhone refuses a phone the gateway does not know.
func TestWakeUnknownPhone(t *testing.T) {
	g, _ := newTestGateway(t)
	if err := g.Wake("nope"); !errors.Is(err, ErrUnknownDevice) {
		t.Fatalf("Wake on an unknown phone: %v", err)
	}
}
