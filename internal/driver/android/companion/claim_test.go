package companion

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Claims go over the subscription's own connection, between its events:
// the reader hands each reply to its request and passes the events on.
func TestClaimOnTheSubscription(t *testing.T) {
	p, c := readyClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub, err := c.Events(ctx, EventClaim)
	if err != nil {
		t.Fatal(err)
	}
	next := func() Event {
		t.Helper()
		select {
		case ev, ok := <-sub.C():
			if !ok {
				t.Fatalf("subscription ended: %v", sub.Err())
			}
			return ev
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for an event")
		}
		return Event{}
	}

	p.events <- `{"event":"claim","time":1,"display":-1}`
	if err := sub.Claim(ctx, ByComputer, "Order lunch"); err != nil {
		t.Fatal(err)
	}
	if req := p.lastRequest("claim"); req["by"] != "computer" || req["task"] != "Order lunch" || req["id"] != float64(2) {
		t.Errorf("claim request: %v", req)
	}
	if ev := next(); ev.Kind != EventClaim || ev.Claim != nil || ev.Stopped {
		t.Errorf("phone free: %+v", ev)
	}
	p.events <- `{"event":"claim","time":1,"display":-1,"by":"computer","task":"Order lunch"}`
	if ev := next(); ev.Claim == nil || *ev.Claim != (Claim{By: ByComputer, Task: "Order lunch"}) {
		t.Errorf("phone held: %+v", ev)
	}

	// Someone else holds the phone: the error says who.
	p.mu.Lock()
	p.claim = func(map[string]any) (any, *Error) {
		return nil, &Error{Code: CodeBusy, Message: "Pinky on the phone is using the phone",
			Claim: Claim{By: ByPhone, Task: "Set a timer"}}
	}
	p.mu.Unlock()
	err = sub.Claim(ctx, ByComputer, "Order lunch")
	var e *Error
	if !IsCode(err, CodeBusy) || !errors.As(err, &e) || e.By != ByPhone || e.Task != "Set a timer" {
		t.Errorf("claim while the phone is busy: %v (%+v)", err, e)
	}

	// Release, and stop, which says whether it ended a claim.
	p.mu.Lock()
	p.claim = func(req map[string]any) (any, *Error) {
		if req["stop"] == true {
			return map[string]any{"stopped": true}, nil
		}
		return map[string]any{}, nil
	}
	p.mu.Unlock()
	if err := sub.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if req := p.lastRequest("claim"); req["release"] != true {
		t.Errorf("release request: %v", req)
	}
	if stopped, err := sub.StopClaim(ctx); err != nil || !stopped {
		t.Errorf("StopClaim = %v, %v; want true", stopped, err)
	}
	p.events <- `{"event":"claim","time":1,"display":-1,"stopped":true}`
	if ev := next(); !ev.Stopped || ev.Claim != nil {
		t.Errorf("claim stopped: %+v", ev)
	}

	// The connection ending fails the request that waits for its reply,
	// and those after it.
	p.mu.Lock()
	p.claim = func(map[string]any) (any, *Error) { return nil, &Error{Code: fakeHangUp} }
	p.mu.Unlock()
	if err := sub.Claim(ctx, ByComputer, "Order lunch"); !errors.Is(err, ErrNotRunning) {
		t.Errorf("claim on a connection that ended: %v", err)
	}
	if err := sub.Release(ctx); !errors.Is(err, ErrNotRunning) {
		t.Errorf("release after the end: %v", err)
	}
}
