package android

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// claims records the claim requests a fake phone got, and answers them
// with answer when it is set.
type claims struct {
	mu     sync.Mutex
	got    []map[string]any
	answer *companion.Error
}

func (c *claims) reply(req map[string]any) (any, *companion.Error) {
	if req["cmd"] != "claim" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, req)
	if c.answer != nil {
		return nil, c.answer
	}
	return map[string]any{"by": "computer"}, nil
}

func (c *claims) count(release bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.got {
		if (r["release"] == true) == release {
			n++
		}
	}
	return n
}

func (c *claims) busy(e *companion.Error) {
	c.mu.Lock()
	c.answer = e
	c.mu.Unlock()
}

func claimPhone(t *testing.T) (*quietPhone, *claims) {
	p := newQuietPhone(t)
	cl := &claims{}
	p.features = []string{companion.FeatureClaim}
	p.reply = cl.reply
	return p, cl
}

// An agent that starts working claims the phone on its subscription, which
// asks for claim events, and gives it back when it stops.
func TestCompanionClaimsWhileAgentWorks(t *testing.T) {
	p, cl := claimPhone(t)
	d, _ := quietDevice(t, p, "")
	d.AgentWorking(true)
	waitFor(t, "the phone is claimed", func() bool { return cl.count(false) == 1 })
	cl.mu.Lock()
	req := cl.got[0]
	cl.mu.Unlock()
	if req["by"] != "computer" {
		t.Errorf("claim = %v, want by computer", req)
	}
	if n := get(p, func() int { return len(p.claimSubs) }); n != 1 {
		t.Errorf("%d subscriptions to claim events, want 1", n)
	}
	if err := d.held(context.Background()); err != nil {
		t.Errorf("held while the agent holds the phone: %v", err)
	}
	if n := cl.count(false); n != 1 {
		t.Errorf("claimed %d times, want once", n)
	}
	d.AgentWorking(false)
	waitFor(t, "the phone is given back", func() bool { return cl.count(true) == 1 })
}

// While Pinky on the phone runs a task, the agent's calls fail with who
// holds it, and nothing else reaches the phone; once it is free the agent
// claims it.
func TestCompanionBusy(t *testing.T) {
	defer func(r time.Duration) { claimRetry = r }(claimRetry)
	claimRetry = 100 * time.Millisecond
	p, cl := claimPhone(t)
	cl.busy(&companion.Error{Code: companion.CodeBusy, Message: "Pinky on the phone is using the phone",
		Claim: companion.Claim{By: companion.ByPhone, Task: "Set a timer"}})
	d, _ := quietDevice(t, p, "")
	d.AgentWorking(true)
	waitFor(t, "the claim is tried", func() bool { return cl.count(false) >= 1 })

	ctx := context.Background()
	err := d.Tap(ctx, screen.Point{X: 10, Y: 10}, driver.TapOptions{})
	var be *driver.BusyError
	if !errors.Is(err, driver.ErrBusy) || !errors.As(err, &be) || be.By != "phone" || be.Task != "Set a timer" {
		t.Fatalf("Tap on a busy phone = %v (%+v)", err, be)
	}
	n := cl.count(false)
	if err := d.held(ctx); !errors.Is(err, driver.ErrBusy) {
		t.Errorf("held right after = %v, want busy", err)
	}
	if cl.count(false) != n {
		t.Error("asked the app again within claimRetry")
	}

	cl.busy(nil)
	time.Sleep(claimRetry)
	if err := d.held(ctx); err != nil {
		t.Errorf("held once the phone is free = %v", err)
	}
}

// Stop on the phone's pill ends the agent's task: its calls fail with
// ErrStoppedOnPhone until the next task, which claims the phone again.
func TestCompanionStoppedOnPhone(t *testing.T) {
	p, cl := claimPhone(t)
	d, logs := quietDevice(t, p, "")
	ctx := context.Background()
	d.AgentWorking(true)
	waitFor(t, "the phone is claimed", func() bool { return cl.count(false) == 1 })

	p.pushClaim(map[string]any{"stopped": true})
	waitFor(t, "the stop arrives", func() bool { return d.comp.stoppedOnPhone() })
	if err := d.Tap(ctx, screen.Point{X: 10, Y: 10}, driver.TapOptions{}); !errors.Is(err, driver.ErrStoppedOnPhone) {
		t.Fatalf("Tap after Stop on the phone = %v", err)
	}
	if !slices.ContainsFunc(logs(), func(l string) bool { return l == "android: phone: the person stopped the agent's task on the phone" }) {
		t.Error("the stop is not logged")
	}

	// The next task: Pinky's own stop ended the claim, so the driver claims again.
	d.AgentWorking(false)
	d.AgentWorking(true)
	waitFor(t, "the next task claims the phone", func() bool { return cl.count(false) == 2 })
	if err := d.held(ctx); err != nil {
		t.Errorf("held in the next task = %v", err)
	}
}

// An app without claims works as before: no claim, no claim events.
func TestCompanionWithoutClaims(t *testing.T) {
	p := newQuietPhone(t)
	cl := &claims{}
	p.reply = cl.reply
	d, _ := quietDevice(t, p, "")
	d.AgentWorking(true)
	waitFor(t, "the service is on", func() bool { return d.companionInUse() })
	if err := d.held(context.Background()); err != nil {
		t.Errorf("held = %v", err)
	}
	if cl.count(false) != 0 || get(p, func() int { return len(p.claimSubs) }) != 0 {
		t.Error("claimed on an app without claims")
	}
}

// A service that on-phone mode keeps on stays on when no agent works:
// turning it off would turn that mode off too.
func TestCompanionOnPhoneStaysOn(t *testing.T) {
	p := newQuietPhone(t)
	p.setting, p.unset, p.onPhone = companion.Release.Component(), false, true
	d, logs := quietDevice(t, p, "")
	d.AgentWorking(false)
	waitFor(t, "it says why", func() bool {
		return slices.ContainsFunc(logs(), func(l string) bool {
			return l == "android: phone: Pinky on the phone keeps the companion on (on-phone mode); leaving its service on"
		})
	})
	if !p.listed() {
		t.Error("turned off the service on-phone mode keeps on")
	}

	// After a task, too.
	d.AgentWorking(true)
	waitFor(t, "the service is the driver's", func() bool { return d.companionInUse() })
	d.AgentWorking(false)
	waitFor(t, "the driver is done with it", func() bool { return !d.companionInUse() })
	if !p.listed() {
		t.Error("turned off the service on-phone mode keeps on, after a task")
	}
}
