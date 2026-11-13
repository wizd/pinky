package ios

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Idle phones through the WebDriverAgent helper (see rest.go). The helper's
// runner, an XCTest UI test, and the tunnel it goes through stay up while
// the phone is idle: iOS may ask for the passcode before the runner starts
// (on the iPhone 13 mini after a restart, and again half an hour later),
// and a runner started again after every idle spell could put that prompt
// in front of the person on most connects, which is worse than what the
// runner costs the phone. Pause connection still ends it.
//
// What comes back without the person is let go: once the phone has been
// unused for restIdle, the device ends its WebDriver session (the next
// request makes a new one, as after a runner restart) and stops reading
// app logs. Only on a runner Pinky started or an earlier Pinky left; one
// Xcode runs may serve another tool too.

// wdaIdle tracks a WDADevice's use.
type wdaIdle struct {
	mu        sync.Mutex
	inflight  int       // requests to WebDriverAgent under way
	usedAt    time.Time // the last request's start or end
	openedAt  time.Time
	watchers  int // views of the picture open
	watchedAt time.Time
	trimmed   bool // the session was ended for want of use

	working atomic.Bool
	loop    sync.Once
	done    chan struct{}
	closed  sync.Once
}

func (w *wdaIdle) init() {
	w.openedAt = time.Now()
	w.done = make(chan struct{})
}

func (w *wdaIdle) close() {
	if w.done != nil {
		w.closed.Do(func() { close(w.done) })
	}
}

// enter counts a request to WebDriverAgent until the returned func.
func (w *wdaIdle) enter() func() {
	w.mu.Lock()
	w.inflight++
	w.usedAt, w.trimmed = time.Now(), false
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		w.inflight--
		w.usedAt = time.Now()
		w.mu.Unlock()
	}
}

// watch counts a view of the picture until done is closed.
func (w *wdaIdle) watch(done <-chan struct{}) {
	w.mu.Lock()
	w.watchers++
	w.watchedAt = time.Now()
	w.mu.Unlock()
	go func() {
		<-done
		w.mu.Lock()
		w.watchers--
		w.watchedAt = time.Now()
		w.mu.Unlock()
	}()
}

// AgentWorking implements driver.WorkWatcher. The gateway calls it once the
// device is open, which is when idle checks start: by then the provider
// has said whose runner it is (leftover).
func (d *WDADevice) AgentWorking(on bool) {
	w := &d.idle
	w.working.Store(on)
	if on {
		w.mu.Lock()
		w.usedAt = time.Now()
		w.mu.Unlock()
	}
	if w.done != nil {
		w.loop.Do(func() { go d.restLoop() })
	}
}

// restIfIdle ends the WebDriver session and app log reads when the phone
// has been unused for restIdle; the runner and its tunnel stay. It reports
// whether it did.
func (d *WDADevice) restIfIdle(now time.Time) bool {
	w := &d.idle
	ours := d.run != nil || d.leftover != ""
	if !ours || d.entry.Properties.SerialNumber == "" || w.working.Load() || !d.inputMu.TryLock() {
		return false
	}
	defer d.inputMu.Unlock()
	w.mu.Lock()
	if w.trimmed || w.inflight > 0 || w.watchers > 0 ||
		!idleSince(now, restIdle, w.usedAt, w.watchedAt, w.openedAt) {
		w.mu.Unlock()
		return false
	}
	w.trimmed = true
	w.mu.Unlock()
	d.StopAppLogs()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	d.c.deleteSession(ctx) // counts no use
	cancel()
	logTo(d.log).Info("iphone: helper idle, session ended", "device", d.info.ID, "idle", restIdle)
	return true
}

// restLoop checks for idleness until the device closes.
func (d *WDADevice) restLoop() {
	t := time.NewTicker(idleCheck)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.restIfIdle(time.Now())
		case <-d.idle.done:
			return
		}
	}
}

var _ driver.WorkWatcher = (*WDADevice)(nil)
