package local

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

type fakeShell struct{ on bool }

func (f *fakeShell) On() bool { return f.on }
func (f *fakeShell) Client(context.Context) (*adb.Client, error) {
	return nil, errors.New("not in this test")
}

// Background displays are offered only while the person has them on, on
// Android 13 or later, with a companion that takes input on other displays.
func TestBackgroundNeedsShellSDKAndService(t *testing.T) {
	sh := &fakeShell{on: true}
	for _, c := range []struct {
		name  string
		shell Shell
		sdk   int
		input bool
		want  bool
	}{
		{"all there", sh, 34, true, true},
		{"Android 13", sh, 33, true, true},
		{"Android 12", sh, 32, true, false},
		{"old companion", sh, 34, false, false},
		{"no shell", nil, 34, true, false},
		{"turned off", &fakeShell{}, 34, true, false},
	} {
		d := &Device{sdk: c.sdk, displayInput: c.input}
		d.setShell(c.shell)
		if got := d.BackgroundSupported(); got != c.want {
			t.Errorf("%s: supported %v, want %v", c.name, got, c.want)
		}
		if got := d.BackgroundByDefault(); got != c.want {
			t.Errorf("%s: by default %v, want %v", c.name, got, c.want)
		}
		if !c.want {
			if _, err := d.OpenBackground(context.Background(), driver.BackgroundOptions{}); !errors.Is(err, driver.ErrUnsupported) {
				t.Errorf("%s: open: %v, want ErrUnsupported", c.name, err)
			}
		}
	}
}

func TestProviderSetShellReachesTheDevice(t *testing.T) {
	p := &Provider{dev: &Device{sdk: 34, displayInput: true}}
	p.SetShell(&fakeShell{on: true})
	if !p.dev.BackgroundSupported() {
		t.Fatal("SetShell did not reach the open device")
	}
	p.SetShell(nil)
	if p.dev.BackgroundSupported() {
		t.Fatal("SetShell(nil) left background displays on")
	}
}

func TestHasFeatures(t *testing.T) {
	if !hasFeatures([]string{"a", "b", "c"}, "c", "a") || hasFeatures([]string{"a"}, "a", "b") {
		t.Fatal("hasFeatures")
	}
}

func TestUntrackedDisplayReadsAsEmptyAfterALaunch(t *testing.T) {
	b := &Background{w: 1080, h: 2400}
	now := time.Now()
	if !b.untracked(now) {
		t.Fatal("before any launch an untracked display is empty")
	}
	if n := b.nudgeDue(now); n != 0 {
		t.Fatalf("nudge %d before any launch", n)
	}
	b.launchPkg, b.launchedAt = "com.android.settings", now
	if !b.untracked(now.Add(launchGrace - time.Millisecond)) {
		t.Fatal("within launchGrace an untracked display is empty")
	}
	if b.untracked(now.Add(launchGrace)) {
		t.Fatal("after launchGrace an untracked display is a failure")
	}

	steps := []struct {
		after time.Duration
		want  int
	}{
		{time.Second, 0},
		{nudgeKeyAfter, 1},
		{2 * time.Second, 0}, // the key once
		{nudgeStartAfter, 2},
		{5 * time.Second, 0}, // the start once
	}
	for _, s := range steps {
		if got := b.nudgeDue(now.Add(s.after)); got != s.want {
			t.Fatalf("after %v: nudge %d, want %d", s.after, got, s.want)
		}
	}
	b.nudges = 0
	if got := b.nudgeDue(now.Add(nudgeAfterLaunch)); got != 0 {
		t.Fatalf("nudge %d once the launch is old", got)
	}

	s := b.emptyScreen()
	if s.Width != 1080 || s.Height != 2400 || s.Orientation != "portrait" || len(s.Elements) != 0 {
		t.Fatalf("empty screen %+v", s)
	}
}
