package android

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// TestBackgroundEmptyOnceItsAppLeft: when the app on a background display
// leaves it (Back from its first screen, a crash, Android stopping it),
// accessibility stops tracking the display and the companion says it is
// not open to accessibility services, as it says of a display it can never
// read. A display it read before then reads as empty, not as one Pinky
// cannot read, or the gateway moves the task to the person's own screen
// (seen on an emulator, Android 15, one Back 12 s after a launch). A
// display it never read still fails once the launch is old.
func TestBackgroundEmptyOnceItsAppLeft(t *testing.T) {
	ts := newTreeServer(t)
	ts.set(7, colorOSTree("com.android.settings"))
	d := hiddenDevice(t, &hiddenPhone{}, ts)
	ctx := context.Background()
	launched := func(b *Background) *Background {
		b.started.Store(true)
		b.mu.Lock()
		b.launchedAt = time.Now().Add(-nudgeAfterLaunch - time.Second)
		b.mu.Unlock()
		return b
	}

	b := launched(&Background{d: d, display: 7, w: scrW, h: scrH, closed: make(chan struct{})})
	s, err := b.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s.App.ID != "com.android.settings" {
		t.Fatalf("first read = %+v, %v", s, err)
	}
	ts.set(7, nil) // the app left: the companion answers unsupported
	s, err = b.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatalf("read once the app left: %v, want an empty display", err)
	}
	if s.App.ID != "" || len(s.Elements) != 0 || s.Width != scrW || s.Height != scrH {
		t.Fatalf("read once the app left = %+v, want an empty display", s)
	}

	never := launched(&Background{d: d, display: 8, w: scrW, h: scrH, closed: make(chan struct{})})
	if _, err := never.Observe(ctx, driver.ObserveOptions{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("display never read: %v, want ErrUnsupported", err)
	}
}
