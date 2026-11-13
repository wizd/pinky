package local

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// connectWait bounds how long a claim waits for the subscription it goes
// over, when the service has just started or restarted.
const connectWait = 5 * time.Second

// errNoService: the subscription the claims go over is not open.
var errNoService = errors.New("local: Pinky's accessibility service does not answer")

// Claims holds Pinky on the phone's claim on the phone and follows who
// holds it (docs/notes/companion-protocol.md, "claim"): the core claims the
// phone while its task runs, and a computer's claim keeps it from starting
// one. The claim goes over the subscription its events come on, so that it
// ends with the core's connection.
type Claims struct {
	client *companion.Client
	retry  time.Duration

	op sync.Mutex // one claim request at a time

	mu     sync.Mutex
	sub    *companion.Subscription
	ready  chan struct{} // closed once sub is set
	holder *companion.Claim
	held   bool // the core holds the phone, for task
	task   string
}

// NewClaims returns the claims of the phone the caller runs on, made
// through app's socket.
func NewClaims(app companion.App) *Claims { return newClaims(companion.NewLocal(app)) }

func newClaims(c *companion.Client) *Claims {
	return &Claims{client: c, retry: time.Second, ready: make(chan struct{})}
}

// Watch follows who holds the phone until ctx ends and calls changed each
// time that may have changed. It opens the subscription again when it ends
// (the service restarted) and makes the claim the core held again; lost is
// called when that fails, because a computer took the phone meanwhile.
func (c *Claims) Watch(ctx context.Context, changed, lost func()) {
	for ctx.Err() == nil {
		sub, err := c.client.Events(ctx, companion.EventClaim)
		if err != nil {
			sleep(ctx, c.retry)
			continue
		}
		c.mu.Lock()
		c.sub = sub
		close(c.ready)
		c.mu.Unlock()
		// Reclaim beside the loop: a request that holds c.op while it
		// waits for the next subscription must not wait on this one.
		go func(sub *companion.Subscription) {
			if !c.reclaim(ctx, sub) && ctx.Err() == nil {
				lost()
			}
		}(sub)
		for ev := range sub.C() {
			if ev.Kind != companion.EventClaim {
				continue
			}
			c.mu.Lock()
			c.holder = ev.Claim
			c.mu.Unlock()
			changed()
		}
		c.mu.Lock()
		c.sub = nil
		c.holder = nil
		c.ready = make(chan struct{})
		c.mu.Unlock()
		changed()
		sleep(ctx, c.retry)
	}
}

// reclaim makes the claim the core held on a new subscription; false when
// that failed and the claim is gone. A subscription that already ended
// leaves it to the next one.
func (c *Claims) reclaim(ctx context.Context, sub *companion.Subscription) bool {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	held, task, current := c.held, c.task, c.sub == sub
	c.mu.Unlock()
	if !held || !current {
		return true
	}
	if err := sub.Claim(ctx, companion.ByPhone, task); err != nil {
		c.mu.Lock()
		c.held, c.task = false, ""
		c.mu.Unlock()
		return false
	}
	return true
}

// Claim takes the phone for the core's task. busy is true when a computer
// holds it.
func (c *Claims) Claim(ctx context.Context, task string) (busy bool, err error) {
	c.op.Lock()
	defer c.op.Unlock()
	err = c.request(ctx, func(sub *companion.Subscription) error {
		return sub.Claim(ctx, companion.ByPhone, task)
	})
	if err != nil {
		if companion.IsCode(err, companion.CodeBusy) {
			return true, nil
		}
		return false, err
	}
	c.mu.Lock()
	c.held, c.task = true, task
	c.mu.Unlock()
	return false, nil
}

// Release gives the phone back once the core's task ended.
func (c *Claims) Release(ctx context.Context) {
	c.op.Lock()
	defer c.op.Unlock()
	c.mu.Lock()
	held := c.held
	c.held, c.task = false, ""
	sub := c.sub
	c.mu.Unlock()
	if !held || sub == nil {
		return
	}
	if err := sub.Release(ctx); err != nil {
		// The claim must not outlive the task: end the connection, and the
		// claim with it. Watch opens another.
		sub.Close()
	}
}

// StopComputer ends a computer's claim: the person pressed Stop on the
// pill while Pinky on a computer worked.
func (c *Claims) StopComputer(ctx context.Context) error {
	c.op.Lock()
	defer c.op.Unlock()
	return c.request(ctx, func(sub *companion.Subscription) error {
		_, err := sub.StopClaim(ctx)
		return err
	})
}

// request runs send on the subscription the claims go over. When that
// subscription turns out to have ended (the service restarted, and the
// core heard it only now), it runs send again on the one Watch opens next,
// so that the person's Start does not fail for it.
func (c *Claims) request(ctx context.Context, send func(*companion.Subscription) error) error {
	sub, err := c.connected(ctx)
	if err != nil {
		return err
	}
	err = send(sub)
	if !errors.Is(err, companion.ErrNotRunning) {
		return err
	}
	sub.Close()
	if sub, err = c.next(ctx, sub); err != nil {
		return err
	}
	return send(sub)
}

// next waits a moment for Watch to open a subscription other than old.
func (c *Claims) next(ctx context.Context, old *companion.Subscription) (*companion.Subscription, error) {
	deadline := time.NewTimer(connectWait)
	defer deadline.Stop()
	tick := time.NewTicker(c.retry / 4)
	defer tick.Stop()
	for {
		c.mu.Lock()
		sub := c.sub
		c.mu.Unlock()
		if sub != nil && sub != old {
			return sub, nil
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errNoService
		}
	}
}

// Computer reports a computer's claim: its task, and whether it can be
// stopped from the phone (an older desktop Pinky's cannot).
func (c *Claims) Computer() (task string, stoppable, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.holder == nil || c.holder.By != companion.ByComputer {
		return "", false, false
	}
	return c.holder.Task, !c.holder.Implicit, true
}

// connected waits a moment for the subscription the claims go over.
func (c *Claims) connected(ctx context.Context) (*companion.Subscription, error) {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	t := time.NewTimer(connectWait)
	defer t.Stop()
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, errNoService
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sub == nil {
		return nil, errNoService
	}
	return c.sub, nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
