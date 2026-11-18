package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// lockDev is a demo phone whose LockReporter answers what the test says,
// while its screen reads still show the demo's own lock.
type lockDev struct {
	*demo.Device

	mu     sync.Mutex
	asks   int
	answer func(ctx context.Context) (bool, error) // nil: the demo's lock
}

func (x *lockDev) Locked(ctx context.Context) (bool, error) {
	x.mu.Lock()
	x.asks++
	answer := x.answer
	x.mu.Unlock()
	if answer == nil {
		return x.Device.Locked(ctx)
	}
	return answer(ctx)
}

func (x *lockDev) setAnswer(f func(ctx context.Context) (bool, error)) {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.answer, x.asks = f, 0
}

func (x *lockDev) askCount() int {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.asks
}

type lockDevProvider struct {
	*demo.Provider
	x *lockDev
}

func (p lockDevProvider) Open(context.Context, string) (driver.Device, error) { return p.x, nil }

// newLockGateway runs a gateway that never polls the lock state on its
// own, so only reads and actions learn it.
func newLockGateway(t *testing.T) (*Gateway, *lockDev) {
	t.Helper()
	x := &lockDev{Device: demo.NewDevice("pixel", "Pixel 9", driver.Android)}
	g, err := New(Options{
		Providers: []driver.Provider{lockDevProvider{demo.NewProvider(driver.Android, x.Device), x}},
		Policy:    testPolicy(t), TrustNewAgents: true, DataDir: t.TempDir(), NoScreenshots: true,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, x
}

// lockedThenCached locks the phone and lets an action learn it, so the
// gateway's cached state says locked.
func lockedThenCached(t *testing.T, g *Gateway, x *lockDev, s *Session, r int) {
	t.Helper()
	x.SetLocked(true)
	mustStatus(t, tap(s, r), StatusLocked)
	if !cachedLocked(g) {
		t.Fatal("the gateway did not record the lock")
	}
}

// cachedLocked reports the gateway's cached lock state of the phone.
func cachedLocked(g *Gateway) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.devices["pixel"].locked
}

// TestActionRightAfterOutsideUnlock unlocks the phone outside Pinky (a
// script, or the person) between two actions: the next action goes ahead
// at once, without waiting for the lock poll to notice.
func TestActionRightAfterOutsideUnlock(t *testing.T) {
	g, x := newLockGateway(t)
	s, first := connect(t, g, "Lunch")
	dinely := ref(t, first, "Dinely")
	lockedThenCached(t, g, x, s, dinely)
	x.SetLocked(false) // unlocked by a script; no poll runs
	res := tap(s, dinely)
	if res.IsError() || res.Status == StatusLocked {
		t.Fatalf("an action right after an outside unlock: %+v", res)
	}
	if cachedLocked(g) {
		t.Fatal("the gateway still records the phone locked")
	}
}

// TestFreshLockReadKeepsLockedPhoneLocked: the fresh read never lets an
// action through on a phone it cannot positively see unlocked.
func TestFreshLockReadKeepsLockedPhoneLocked(t *testing.T) {
	saved := freshLockTimeout
	freshLockTimeout = 100 * time.Millisecond
	t.Cleanup(func() { freshLockTimeout = saved })

	cases := []struct {
		name   string
		answer func(*Gateway) func(ctx context.Context) (bool, error)
	}{
		{"says locked", func(*Gateway) func(context.Context) (bool, error) {
			return func(context.Context) (bool, error) { return true, nil }
		}},
		{"read fails", func(*Gateway) func(context.Context) (bool, error) {
			return func(context.Context) (bool, error) { return false, errors.New("dumpsys failed") }
		}},
		{"read times out", func(*Gateway) func(context.Context) (bool, error) {
			return func(ctx context.Context) (bool, error) {
				<-ctx.Done()
				return false, ctx.Err()
			}
		}},
		{"locked again meanwhile", func(g *Gateway) func(context.Context) (bool, error) {
			// The poll records locked while the fresh read was out, and
			// the read answers unlocked: nothing tells which came last.
			return func(context.Context) (bool, error) {
				g.mu.Lock()
				d := g.devices["pixel"]
				g.mu.Unlock()
				g.setLocked(d, true)
				return false, nil
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, x := newLockGateway(t)
			s, first := connect(t, g, "Lunch")
			dinely := ref(t, first, "Dinely")
			lockedThenCached(t, g, x, s, dinely)
			// The screen reads would show it unlocked; only the lock read
			// decides, and it does not say unlocked.
			x.SetLocked(false)
			x.setAnswer(tc.answer(g))
			start := time.Now()
			mustStatus(t, tap(s, dinely), StatusLocked)
			if x.askCount() != 1 {
				t.Fatalf("the lock was read %d times, want once", x.askCount())
			}
			if took := time.Since(start); took > 5*time.Second {
				t.Fatalf("the refusal took %s", took)
			}
		})
	}
}

// TestFreshLockReadOnlyWhenCachedLocked: an action on a phone the gateway
// sees unlocked does not ask for the lock state.
func TestFreshLockReadOnlyWhenCachedLocked(t *testing.T) {
	g, x := newLockGateway(t)
	s, first := connect(t, g, "Lunch")
	x.setAnswer(nil)
	res := tap(s, ref(t, first, "Dinely"))
	if res.IsError() || res.Status == StatusLocked {
		t.Fatalf("tap: %+v", res)
	}
	if n := x.askCount(); n != 0 {
		t.Fatalf("an action on an unlocked phone read the lock %d times", n)
	}
}
