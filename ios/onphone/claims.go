package onphone

import (
	"context"
	"sync"
	"time"
)

// The iPhone has no companion socket for claims (standalone.Claims): Pinky
// on a computer holds the phone while Pinky's app hosts WebDriverAgent for
// it (docs/notes/iphone-app.md, "Hosting WebDriverAgent"). The app runs in
// test mode then, and says so every few seconds with a Darwin notification
// (<app>.computer.on), which the process hosting the core passes to
// Core.ComputerSeen. The computer holds the phone until computerHold after
// the last one: the app in test mode stops when the computer is done with
// it, and says nothing more.
//
// One phone has one remote accessibility client, so while the computer
// holds it the core starts no task (standalone.ErrComputer), and a task
// running when the computer takes the phone stops. Stop from the phone does
// not reach the computer (stoppable false): the person stops it there, or
// leaves the app the computer runs in test mode.
const (
	computerHold  = 15 * time.Second
	computerCheck = time.Second
)

// computerClaims is standalone.Claims for the iPhone.
type computerClaims struct {
	now func() time.Time

	mu      sync.Mutex
	seen    time.Time // the last word from the app in test mode
	mine    bool      // the core's task holds the phone
	notify  func()    // the core publishes its state again
	changed chan struct{}
}

func (c *computerClaims) setNotify(f func()) {
	c.mu.Lock()
	c.notify = f
	c.mu.Unlock()
}

func newComputerClaims() *computerClaims {
	return &computerClaims{now: time.Now, changed: make(chan struct{}, 1)}
}

// heard records a word from the app in test mode.
func (c *computerClaims) heard() {
	c.mu.Lock()
	c.seen = c.now()
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *computerClaims) held() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.heldLocked()
}

func (c *computerClaims) heldLocked() bool {
	return !c.seen.IsZero() && c.now().Sub(c.seen) < computerHold
}

// Watch tells the core when the computer takes the phone or lets it go, and
// stops the core's task (lost) when the computer takes the phone from it.
func (c *computerClaims) Watch(ctx context.Context, changed, lost func()) {
	t := time.NewTicker(computerCheck)
	defer t.Stop()
	was := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.changed:
		case <-t.C:
		}
		c.mu.Lock()
		now := c.heldLocked()
		took := now && !was && c.mine
		if took {
			c.mine = false
		}
		notify := c.notify
		c.mu.Unlock()
		if now != was {
			was = now
			changed()
			if notify != nil {
				notify()
			}
		}
		if took {
			lost()
		}
	}
}

func (c *computerClaims) Claim(_ context.Context, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.heldLocked() {
		return true, nil
	}
	c.mine = true
	return false, nil
}

func (c *computerClaims) Release(context.Context) {
	c.mu.Lock()
	c.mine = false
	c.mu.Unlock()
}

func (c *computerClaims) Computer() (string, bool, bool) {
	return "", false, c.held()
}

// StopComputer cannot reach the computer; Computer says so (stoppable
// false), so Pinky's screens offer no Stop.
func (c *computerClaims) StopComputer(context.Context) error { return nil }
