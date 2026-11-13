package standalone

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// fakeShell records what the core asks of it.
type fakeShell struct {
	mu      sync.Mutex
	st      ShellState
	pairs   []string
	changed func()
}

func (f *fakeShell) State() ShellState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeShell) set(change func(*ShellState)) {
	f.mu.Lock()
	change(&f.st)
	cb := f.changed
	f.mu.Unlock()
	if cb != nil {
		cb()
	}
}

func (f *fakeShell) SetOn(on bool) error {
	f.set(func(s *ShellState) {
		s.On = on
		s.Status = map[bool]string{true: "unpaired", false: "off"}[on]
	})
	return nil
}

func (f *fakeShell) SetPort(port int) error {
	f.set(func(s *ShellState) { s.Port = port })
	return nil
}

func (f *fakeShell) Pair(ctx context.Context, port int, code string) error {
	if code != "123456" {
		return fmt.Errorf("pairing: %w", ErrWrongCode)
	}
	f.mu.Lock()
	f.pairs = append(f.pairs, fmt.Sprint(port, " ", code))
	f.mu.Unlock()
	f.set(func(s *ShellState) { s.On, s.Status = true, "no_port" })
	return nil
}

func (f *fakeShell) Forget() error {
	f.set(func(s *ShellState) { s.Status = "unpaired" })
	return nil
}

func (f *fakeShell) Watch(changed func()) {
	f.mu.Lock()
	f.changed = changed
	f.mu.Unlock()
}

func TestShellCommands(t *testing.T) {
	sh := &fakeShell{st: ShellState{Status: "off"}}
	h := newHarness(t, func(o *Options) { o.Shell = sh })
	c := h.dial(t)
	if st := c.do(map[string]any{"cmd": "state"}).State; st.Shell == nil || st.Shell.Status != "off" {
		t.Fatalf("state's shell: %+v", st.Shell)
	}

	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.until("the first state", func(State) bool { return true })

	if r := c.do(map[string]any{"cmd": "shell", "op": "on"}); !r.OK {
		t.Fatalf("on: %+v", r)
	}
	// A change of the shell's state reaches subscribers.
	sub.until("the shell on", func(s State) bool { return s.Shell != nil && s.Shell.On })

	if r := c.do(map[string]any{"cmd": "shell", "op": "pair", "port": 37000, "code": "000000"}); r.OK || r.Code != CodeWrongCode {
		t.Fatalf("wrong code: %+v", r)
	}
	if r := c.do(map[string]any{"cmd": "shell", "op": "pair", "port": 37000, "code": "123456"}); !r.OK {
		t.Fatalf("pair: %+v", r)
	}
	if r := c.do(map[string]any{"cmd": "shell", "op": "port", "port": 41000}); !r.OK {
		t.Fatalf("port: %+v", r)
	}
	if st := c.do(map[string]any{"cmd": "state"}).State; st.Shell.Port != 41000 || st.Shell.Status != "no_port" {
		t.Fatalf("after pairing: %+v", st.Shell)
	}
	if r := c.do(map[string]any{"cmd": "shell", "op": "forget"}); !r.OK {
		t.Fatalf("forget: %+v", r)
	}
	if r := c.do(map[string]any{"cmd": "shell", "op": "sideways"}); r.OK {
		t.Fatal("an unknown op was taken")
	}
	sh.mu.Lock()
	pairs := sh.pairs
	sh.mu.Unlock()
	if len(pairs) != 1 || pairs[0] != "37000 123456" {
		t.Fatalf("pairings %q", pairs)
	}
}

func TestDisplayCommandsWithoutShell(t *testing.T) {
	h := newHarness(t)
	c := h.dial(t)
	if st := c.do(map[string]any{"cmd": "state"}).State; st.Shell != nil || st.Background {
		t.Fatalf("a core without a shell shows %+v, background %v", st.Shell, st.Background)
	}
	if r := c.do(map[string]any{"cmd": "shell", "op": "on"}); r.OK || r.Code != CodeNoShell {
		t.Fatalf("shell without one: %+v", r)
	}
	if r := c.do(map[string]any{"cmd": "touch", "action": "down", "x": 1, "y": 2}); r.OK || r.Code != CodeNoShell {
		t.Fatalf("touch without a background display: %+v", r)
	}
	// No task: no display to show, now.
	v := h.dial(t)
	if r := v.do(map[string]any{"cmd": "video"}); r.OK || r.Code != CodeNoDisplay {
		t.Fatalf("video without a task: %+v", r)
	}
}

func TestTouchTakesOver(t *testing.T) {
	type touch struct {
		a driver.TouchAction
		p screen.Point
	}
	var mu sync.Mutex
	var got []touch
	h := newHarness(t, func(o *Options) {
		o.BackgroundTouch = func(_ context.Context, a driver.TouchAction, p screen.Point) error {
			mu.Lock()
			got = append(got, touch{a, p})
			mu.Unlock()
			return nil
		}
	})
	c := h.dial(t)
	for _, a := range []string{"down", "move", "up"} {
		if r := c.do(map[string]any{"cmd": "touch", "action": a, "x": 100, "y": 200}); !r.OK {
			t.Fatalf("touch %s: %+v", a, r)
		}
	}
	mu.Lock()
	if len(got) != 3 || got[0].a != driver.TouchDown || got[2].a != driver.TouchUp || got[1].p != (screen.Point{X: 100, Y: 200}) {
		t.Fatalf("touches %+v", got)
	}
	mu.Unlock()
	if r := c.do(map[string]any{"cmd": "handback", "note": "done"}); !r.OK {
		t.Fatalf("handback: %+v", r)
	}
	if r := c.do(map[string]any{"cmd": "takeover"}); !r.OK {
		t.Fatalf("takeover: %+v", r)
	}
}

func TestWriteFrames(t *testing.T) {
	ch := make(chan driver.Packet, 2)
	ch <- driver.Packet{Data: []byte{0, 0, 0, 1, 0x67}, Config: true}
	ch <- driver.Packet{Data: []byte{0, 0, 0, 1, 0x65, 9}, PTS: 1500 * time.Microsecond, KeyFrame: true}
	close(ch)
	var buf bytes.Buffer
	if err := writeFrames(context.Background(), &buf, ch); err != io.EOF {
		t.Fatalf("end: %v", err)
	}
	b := buf.Bytes()
	read := func() (uint64, []byte) {
		h, n := binary.BigEndian.Uint64(b), binary.BigEndian.Uint32(b[8:])
		d := b[12 : 12+n]
		b = b[12+n:]
		return h, d
	}
	h, d := read()
	if h != frameConfig || !bytes.Equal(d, []byte{0, 0, 0, 1, 0x67}) {
		t.Fatalf("config frame %x %x", h, d)
	}
	h, d = read()
	if h != frameKeyFrame|1500 || len(d) != 6 {
		t.Fatalf("key frame %x %x", h, d)
	}
	if len(b) != 0 {
		t.Fatalf("%d bytes left", len(b))
	}
}
