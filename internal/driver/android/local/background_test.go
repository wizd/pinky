package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
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

// treePhone answers the companion's tree request for the display with
// tree, and with unsupported (the display is not open to accessibility
// services) while tree is nil.
type treePhone struct {
	ln   net.Listener
	mu   sync.Mutex
	tree *companion.Tree
}

func (p *treePhone) set(t *companion.Tree) {
	p.mu.Lock()
	p.tree = t
	p.mu.Unlock()
}

func (p *treePhone) handle(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID  int64  `json:"id"`
			Cmd string `json:"cmd"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		p.mu.Lock()
		tree := p.tree
		p.mu.Unlock()
		resp := map[string]any{"id": req.ID, "ok": false, "error": map[string]string{"code": "unsupported", "message": "not open to accessibility services"}}
		if req.Cmd == "tree" && tree != nil {
			resp = map[string]any{"id": req.ID, "ok": true, "result": tree}
		}
		b, _ := json.Marshal(resp)
		if _, err := c.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

func (p *treePhone) Shell(context.Context, string) (string, error) { return "", nil }
func (p *treePhone) Forward(context.Context, string) (string, error) {
	return p.ln.Addr().String(), nil
}
func (p *treePhone) RemoveForward(context.Context, string) error { return nil }
func (p *treePhone) Install(context.Context, string) error       { return nil }

// TestBackgroundEmptyOnceItsAppLeft: once accessibility read the display,
// a read that finds it untracked after launchGrace is an empty display
// (its app left it), not one Pinky cannot read, and sends no nudge. A
// display never read still fails then.
func TestBackgroundEmptyOnceItsAppLeft(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	p := &treePhone{ln: ln, tree: settings()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	c := companion.New(p)
	t.Cleanup(func() { c.Close() })
	d := &Device{c: c, sdk: 34, displayInput: true}
	ctx := context.Background()
	launched := func(b *Background) *Background {
		b.launchPkg, b.launchedAt = "com.android.settings", time.Now().Add(-nudgeAfterLaunch-time.Second)
		return b
	}

	b := launched(&Background{d: d, display: 7, w: 1080, h: 2400})
	s, err := b.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s.App.ID != "com.android.settings" {
		t.Fatalf("first read = %+v, %v", s, err)
	}
	p.set(nil) // the app left
	s, err = b.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatalf("read once the app left: %v, want an empty display", err)
	}
	if s.App.ID != "" || len(s.Elements) != 0 || s.Width != 1080 || s.Height != 2400 {
		t.Fatalf("read once the app left = %+v, want an empty display", s)
	}
	if b.nudges != 0 {
		t.Errorf("%d nudges for a display whose app left", b.nudges)
	}

	never := launched(&Background{d: d, display: 8, w: 1080, h: 2400})
	if _, err := never.Observe(ctx, driver.ObserveOptions{}); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("display never read: %v, want ErrUnsupported", err)
	}
}
