package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

// switching reports whether the gateway still moves the phone for a task.
func switching(g *Gateway) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.devices["iphone"].switching != ""
}

// A launch that moves the phone to the helper answers within the call's
// budget, leaving switchReserve, however long the helper takes to start
// (a field report of 2026-11-22: phone_launch ended in the MCP client's
// "Request timed out"). The move ends in the background, and the next call
// finds the phone on the helper.
func TestSlowSwitchAnswersInTime(t *testing.T) {
	shortSwitchBack(t)
	p, _ := newTwoWayPhone()
	p.slow = 2500 * time.Millisecond
	budget := switchReserve + time.Second
	g := twoWayGateway(t, p, func(o *Options) { o.CallBudget = budget })
	s, _ := connect(t, g, "Bookkeeping")
	start := time.Now()
	res := launchAllowed(g, s, "招商银行")
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("launch took %v with a budget of %v", took, budget)
	}
	mustError(t, res, ErrDeviceOffline, "not back yet", "Nothing was done")
	// The next call waits for the phone, which comes up on the helper.
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
	if b := backendOf(g); b != wayHelper {
		t.Fatalf("backend %q", b)
	}
	waitUntil(t, "the move to end", func() bool { return !switching(g) })
	mustStatus(t, launchAllowed(g, s, "招商银行"), StatusSettled)
}

// A helper that fails after the call stopped waiting for it: the move in
// the background takes the phone back to CoreDevice, as the call would
// have.
func TestSlowFailingSwitchGoesBack(t *testing.T) {
	p, _ := newTwoWayPhone()
	p.helperErr, p.slow = errors.New("the runner quit"), 2500*time.Millisecond
	g := twoWayGateway(t, p, func(o *Options) {
		o.CallBudget = switchReserve + time.Second
		o.ReconnectGrace = time.Minute
	})
	s, _ := connect(t, g, "Bookkeeping")
	mustError(t, launchAllowed(g, s, "digibank"), ErrDeviceOffline, "not back yet")
	waitUntil(t, "the phone back on CoreDevice", func() bool { return backendOf(g) == wayCoreDevice && !switching(g) })
	if way, _ := p.state(); way != "" {
		t.Fatalf("provider way %q", way)
	}
	mustStatus(t, s.Observe(context.Background(), ObserveRequest{}), StatusSettled)
}

// A call on a phone that is still moving to the helper for its task waits
// for it within the call's budget, and then says so, instead of waiting
// for the move's whole minute.
func TestCallOnMovingPhoneAnswersInTime(t *testing.T) {
	old := switchReserve
	switchReserve = 300 * time.Millisecond
	t.Cleanup(func() { switchReserve = old })
	p, _ := newTwoWayPhone()
	p.slow = 3 * time.Second
	budget := 2 * switchReserve
	g := twoWayGateway(t, p, func(o *Options) { o.CallBudget = budget; o.ReconnectGrace = time.Minute })
	s := g.OpenSession(agent)
	defer s.Close()
	mustError(t, s.Connect(context.Background(), ConnectRequest{Task: "Balances", Connection: "helper"}), ErrDeviceOffline, "not back yet")
	start := time.Now()
	mustError(t, s.Observe(context.Background(), ObserveRequest{}), ErrDeviceOffline, "not back yet")
	if took := time.Since(start); took > budget {
		t.Fatalf("observe took %v with a budget of %v", took, budget)
	}
}
