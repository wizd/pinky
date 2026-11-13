package ios

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// restLink is a link on a tunnel with nothing behind it, which Close stops.
func restLink() *link {
	return &link{tunnel: &Tunnel{done: make(chan struct{})}}
}

// tunnelClosed reports whether l's tunnel was closed.
func tunnelClosed(l *link) bool {
	select {
	case <-l.tunnel.Done():
		return true
	default:
		return false
	}
}

// restDevice is a Device on a fake link whose wakes dial through dial.
func restDevice(t *testing.T, dial func(context.Context) (*link, error)) *Device {
	t.Helper()
	d := &Device{info: driver.Info{ID: "phone"}, l: restLink(), done: make(chan struct{}), openedAt: time.Now()}
	d.redial = dial
	t.Cleanup(func() { close(d.done) })
	return d
}

func TestDeviceRestsAndWakes(t *testing.T) {
	var dials atomic.Int32
	d := restDevice(t, func(context.Context) (*link, error) {
		dials.Add(1)
		return restLink(), nil
	})
	first := d.cur()
	later := time.Now().Add(2 * restIdle)

	// Just opened, used, or with an agent working: no rest.
	if d.restIfIdle(time.Now()) {
		t.Fatal("rested as it opened")
	}
	d.AgentWorking(true)
	if d.restIfIdle(later) {
		t.Fatal("rested while an agent works")
	}
	d.AgentWorking(false)
	d.mu.Lock()
	d.watchers = 1
	d.mu.Unlock()
	if d.restIfIdle(later) {
		t.Fatal("rested while someone watches")
	}
	d.mu.Lock()
	d.watchers = 0
	d.mu.Unlock()

	if !d.restIfIdle(later) || !d.Resting() {
		t.Fatal("an idle phone did not rest")
	}
	if !tunnelClosed(first) {
		t.Fatal("the tunnel stayed open")
	}
	// Resting is not gone: lockdown reads go on, and errors are not offline.
	if d.linkGone() || errors.Is(d.gone(errors.New("x")), driver.ErrOffline) {
		t.Fatal("a resting phone counts as gone")
	}
	if d.cur() != first || dials.Load() != 0 {
		t.Fatal("cur woke the phone")
	}
	if d.restIfIdle(later) {
		t.Fatal("rested twice")
	}

	// The next use wakes it, once, with a new tunnel.
	var wg sync.WaitGroup
	links := make([]*link, 4)
	for i := range links {
		wg.Add(1)
		go func() { defer wg.Done(); links[i] = d.link() }()
	}
	wg.Wait()
	if d.Resting() || dials.Load() != 1 {
		t.Fatalf("resting %v after %d dials", d.Resting(), dials.Load())
	}
	for _, l := range links {
		if l == first || l != d.cur() {
			t.Fatal("a caller got the old tunnel")
		}
	}
	// Used just now: no rest until restIdle passes again.
	if d.restIfIdle(time.Now()) {
		t.Fatal("rested right after a wake")
	}
}

func TestDeviceWakeFails(t *testing.T) {
	d := restDevice(t, func(context.Context) (*link, error) {
		return nil, errors.New("no route to the phone")
	})
	if !d.restIfIdle(time.Now().Add(2 * restIdle)) {
		t.Fatal("did not rest")
	}
	d.link()
	if !d.linkGone() {
		t.Fatal("a failed wake does not count as gone")
	}
	if err := d.gone(errors.New("read")); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("after a failed wake: %v", err)
	}
	if _, err := d.wakeUp(context.Background()); !errors.Is(err, driver.ErrOffline) || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("a second wake: %v", err)
	}
}

// fakeRun is a runner Pinky started; stopped reports whether stop ran.
func fakeRun() (*wdaRun, *atomic.Bool) {
	stopped := new(atomic.Bool)
	done := make(chan struct{})
	return &wdaRun{done: done, cancel: func() {
		if stopped.CompareAndSwap(false, true) {
			close(done)
		}
	}}, stopped
}

// idleWDA is a WDADevice on a phone, reached at a test server.
func idleWDA(t *testing.T, f *fakeWDA, run *wdaRun) *WDADevice {
	t.Helper()
	s := httptest.NewServer(f)
	t.Cleanup(s.Close)
	d := &WDADevice{info: driver.Info{ID: "phone"}, c: newWDAURL(s.URL, ""), run: run}
	d.entry.Properties.SerialNumber = "phone"
	d.c.enter = d.idle.enter
	d.idle.init()
	t.Cleanup(d.idle.close)
	return d
}

// Through the helper an idle phone keeps its runner (starting it again may
// ask for the passcode) and only ends its WebDriver session.
func TestWDADeviceIdleKeepsRunner(t *testing.T) {
	f := &fakeWDA{}
	run, stopped := fakeRun()
	d := idleWDA(t, f, run)
	ctx := context.Background()
	later := func() time.Time { return time.Now().Add(2 * restIdle) }

	if err := d.c.keys(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if d.restIfIdle(time.Now()) {
		t.Fatal("idle right after a request")
	}
	d.AgentWorking(true)
	if d.restIfIdle(later()) {
		t.Fatal("idle while an agent works")
	}
	d.AgentWorking(false)
	streamDone := make(chan struct{})
	d.idle.watch(streamDone)
	if d.restIfIdle(later()) {
		t.Fatal("idle while the picture is watched")
	}
	close(streamDone)
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		d.idle.mu.Lock()
		w := d.idle.watchers
		d.idle.mu.Unlock()
		if w == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the view was not let go")
		}
	}

	if !d.restIfIdle(later()) {
		t.Fatal("an idle phone kept its session")
	}
	if stopped.Load() || d.ended() {
		t.Fatal("going idle stopped the runner")
	}
	f.mu.Lock()
	deletes := len(f.bodies["DELETE /session/s1"])
	f.mu.Unlock()
	if deletes != 1 {
		t.Fatalf("%d session deletes", deletes)
	}
	if d.restIfIdle(later()) {
		t.Fatal("ended the session twice")
	}

	// The next request makes a new session on the same runner.
	if err := d.c.keys(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	sessions, keys := f.sessions, f.bodies["POST /wda/keys"]
	f.mu.Unlock()
	if sessions != 2 || len(keys) != 2 {
		t.Fatalf("%d sessions, keys %v", sessions, keys)
	}
}

// A runner Pinky did not start (Xcode's) is never touched.
func TestWDADeviceTheirsNeverRests(t *testing.T) {
	f := &fakeWDA{}
	d := idleWDA(t, f, nil)
	if err := d.c.keys(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if d.restIfIdle(time.Now().Add(2 * restIdle)) {
		t.Fatal("ended the session on a runner that is not Pinky's")
	}
}
