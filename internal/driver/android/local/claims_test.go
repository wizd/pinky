package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/companion"
)

// fakeApp speaks the claims part of the companion protocol on TCP, as the
// app does on its socket: one claim at a time, claim events to its
// subscribers, and a computer's claim that the test makes.
type fakeApp struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	conns    map[*fakeConn]bool
	claimer  *fakeConn // the core's claim
	task     string
	claims   int // the core's claims that were given
	computer *companion.Claim
}

type fakeConn struct {
	net.Conn
	wmu sync.Mutex
	sub bool // subscribed; guarded by fakeApp.mu
	// dead: the service behind it stopped, but the client was not told; its
	// next request ends it instead of an answer. Guarded by fakeApp.mu.
	dead bool
}

func (c *fakeConn) send(v any) {
	b, _ := json.Marshal(v)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.Write(append(b, '\n'))
}

func newFakeApp(t *testing.T) *fakeApp {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeApp{t: t, ln: ln, conns: map[*fakeConn]bool{}}
	t.Cleanup(func() {
		ln.Close()
		a.restart(nil)
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			fc := &fakeConn{Conn: c}
			a.mu.Lock()
			a.conns[fc] = true
			a.mu.Unlock()
			go a.serve(fc)
		}
	}()
	return a
}

func (a *fakeApp) serve(c *fakeConn) {
	defer func() {
		c.Close()
		a.mu.Lock()
		delete(a.conns, c)
		if a.claimer == c {
			a.claimer, a.task = nil, ""
			a.announceLocked()
		}
		a.mu.Unlock()
	}()
	rd := bufio.NewReader(c)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID      int64  `json:"id"`
			Cmd     string `json:"cmd"`
			By      string `json:"by"`
			Task    string `json:"task"`
			Release bool   `json:"release"`
			Stop    bool   `json:"stop"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			a.t.Errorf("bad request %q: %v", line, err)
			return
		}
		switch req.Cmd {
		case "events":
			c.send(map[string]any{"id": req.ID, "ok": true, "result": map[string]any{"kinds": []string{"claim"}}})
			a.mu.Lock()
			c.sub = true
			c.send(a.eventLocked())
			a.mu.Unlock()
		case "claim":
			a.mu.Lock()
			if c.dead {
				a.mu.Unlock()
				return
			}
			var result map[string]any
			var perr *companion.Error
			switch {
			case req.Stop:
				stopped := a.computer != nil && !a.computer.Implicit
				if stopped {
					a.computer = nil
					a.announceLocked()
				}
				result = map[string]any{"stopped": stopped}
			case req.Release:
				if a.claimer == c {
					a.claimer, a.task = nil, ""
					a.announceLocked()
				}
				result = map[string]any{}
			case req.By != companion.ByPhone:
				perr = &companion.Error{Code: "bad_request", Message: "by " + req.By}
			case a.computer != nil:
				perr = &companion.Error{Code: companion.CodeBusy, Message: "Pinky on a computer is using the phone", Claim: *a.computer}
			default:
				a.claimer, a.task = c, req.Task
				a.claims++
				a.announceLocked()
				result = map[string]any{"by": companion.ByPhone, "task": req.Task}
			}
			a.mu.Unlock()
			if perr != nil {
				c.send(map[string]any{"id": req.ID, "ok": false, "error": perr})
			} else {
				c.send(map[string]any{"id": req.ID, "ok": true, "result": result})
			}
		default:
			c.send(map[string]any{"id": req.ID, "ok": false, "error": map[string]any{"code": "unknown_command", "message": req.Cmd}})
		}
	}
}

func (a *fakeApp) eventLocked() map[string]any {
	ev := map[string]any{"event": "claim", "time": time.Now().UnixMilli(), "display": -1}
	switch {
	case a.claimer != nil:
		ev["by"], ev["task"] = companion.ByPhone, a.task
	case a.computer != nil:
		ev["by"], ev["task"], ev["implicit"] = a.computer.By, a.computer.Task, a.computer.Implicit
	}
	return ev
}

func (a *fakeApp) announceLocked() {
	ev := a.eventLocked()
	for c := range a.conns {
		if c.sub {
			c.send(ev)
		}
	}
}

// computerClaims sets or ends a computer's claim.
func (a *fakeApp) computerClaims(cl *companion.Claim) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.computer = cl
	a.announceLocked()
}

// restart ends every connection, as the service does when it restarts; a
// computer's claim, when given, comes before anyone reconnects.
func (a *fakeApp) restart(computer *companion.Claim) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if computer != nil {
		a.computer = computer
	}
	for c := range a.conns {
		c.Close()
	}
}

// restartQuietly stops the service without telling its clients, as
// Android's LocalSocket.close did: each learns it at its next request.
func (a *fakeApp) restartQuietly() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for c := range a.conns {
		c.dead = true
	}
}

// phone returns the core's claim and how many claims it was given.
func (a *fakeApp) phone() (task string, held bool, claims int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.task, a.claimer != nil, a.claims
}

// Runner: the client reaches the fake app through its forward.
func (a *fakeApp) Shell(context.Context, string) (string, error) {
	return "", errors.New("fake app: no shell")
}
func (a *fakeApp) Forward(context.Context, string) (string, error) { return a.ln.Addr().String(), nil }
func (a *fakeApp) RemoveForward(context.Context, string) error     { return nil }
func (a *fakeApp) Install(context.Context, string) error {
	return errors.New("fake app: no install")
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestClaims(t *testing.T) {
	app := newFakeApp(t)
	cl := newClaims(companion.NewFor(app, companion.Release))
	cl.retry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var changes atomic.Int32
	lost := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		cl.Watch(ctx, func() { changes.Add(1) }, func() { lost <- struct{}{} })
	}()
	defer func() {
		cancel()
		<-done
	}()
	computer := func() (string, bool, bool) { return cl.Computer() }

	// A computer's claim keeps Pinky on the phone from starting a task, and
	// Stop on the pill ends it.
	app.computerClaims(&companion.Claim{By: companion.ByComputer, Task: "Order lunch"})
	eventually(t, "the computer's claim shows", func() bool {
		task, stoppable, ok := computer()
		return ok && stoppable && task == "Order lunch"
	})
	if busy, err := cl.Claim(ctx, "Set a timer"); !busy || err != nil {
		t.Fatalf("Claim while a computer holds the phone = %v, %v; want busy", busy, err)
	}
	if err := cl.StopComputer(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the computer's claim ends", func() bool { _, _, ok := computer(); return !ok })

	// An older desktop Pinky's claim shows too, and cannot be stopped.
	app.computerClaims(&companion.Claim{By: companion.ByComputer, Implicit: true})
	eventually(t, "the implicit claim shows", func() bool {
		task, stoppable, ok := computer()
		return ok && !stoppable && task == ""
	})
	app.computerClaims(nil)
	eventually(t, "the implicit claim ends", func() bool { _, _, ok := computer(); return !ok })

	// The core holds the phone while its task runs, also across a restart
	// of the service, and gives it back.
	if busy, err := cl.Claim(ctx, "Set a timer"); busy || err != nil {
		t.Fatalf("Claim = %v, %v", busy, err)
	}
	if task, held, n := app.phone(); !held || task != "Set a timer" || n != 1 {
		t.Fatalf("the app's claim: %q, %v, %d", task, held, n)
	}
	app.restart(nil)
	eventually(t, "the claim is made again", func() bool {
		task, held, n := app.phone()
		return held && task == "Set a timer" && n == 2
	})
	cl.Release(ctx)
	eventually(t, "the claim is released", func() bool { _, held, _ := app.phone(); return !held })

	// A computer that takes the phone while the service restarts ends the
	// core's claim: lost.
	if busy, err := cl.Claim(ctx, "Water the plants"); busy || err != nil {
		t.Fatalf("Claim = %v, %v", busy, err)
	}
	app.restart(&companion.Claim{By: companion.ByComputer, Task: "Order lunch"})
	select {
	case <-lost:
	case <-time.After(3 * time.Second):
		t.Fatal("lost was not called")
	}
	eventually(t, "the computer's claim shows", func() bool { _, _, ok := computer(); return ok })
	if _, held, _ := app.phone(); held {
		t.Error("the app still has the core's claim")
	}
	select {
	case <-lost:
		t.Error("lost was called twice")
	default:
	}
	if changes.Load() == 0 {
		t.Error("changed was never called")
	}

	// A service that stopped without ending the core's connection: the
	// claim goes through on the connection Watch opens next, and so does
	// Stop on the pill.
	app.computerClaims(nil)
	eventually(t, "the phone is free", func() bool { _, _, ok := computer(); return !ok })
	app.restartQuietly()
	if busy, err := cl.Claim(ctx, "Feed the cat"); busy || err != nil {
		t.Fatalf("Claim after a quiet restart = %v, %v", busy, err)
	}
	if task, held, _ := app.phone(); !held || task != "Feed the cat" {
		t.Fatalf("the app's claim after a quiet restart: %q, %v", task, held)
	}
	cl.Release(ctx)
	app.restartQuietly()
	if err := cl.StopComputer(ctx); err != nil {
		t.Fatalf("StopComputer after a quiet restart: %v", err)
	}
}

// A request that finds its subscription dead waits for the next one while
// it holds the claims lock, so Watch must not wait on that lock to go on
// with a subscription it just opened (a 5 s stall, then "does not answer",
// when Stop on the pill came right after the service restarted).
func TestClaimsRequestWhileWatchReclaims(t *testing.T) {
	app := newFakeApp(t)
	cl := newClaims(companion.NewFor(app, companion.Release))
	cl.retry = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cl.Watch(ctx, func() {}, func() {})
	}()
	defer func() {
		cancel()
		<-done
	}()
	sub := func() *companion.Subscription {
		cl.mu.Lock()
		defer cl.mu.Unlock()
		return cl.sub
	}
	eventually(t, "Watch subscribes", func() bool { return sub() != nil })
	first := sub()

	// A request holds the lock while the service restarts: Watch opens a
	// new subscription and wants the lock to make the core's claim again.
	cl.op.Lock()
	defer cl.op.Unlock()
	app.restart(nil)
	eventually(t, "Watch subscribes again", func() bool { s := sub(); return s != nil && s != first })
	app.restartQuietly()

	rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
	defer rcancel()
	err := cl.request(rctx, func(s *companion.Subscription) error {
		_, err := s.StopClaim(rctx)
		return err
	})
	if err != nil {
		t.Fatalf("a request while Watch reclaims: %v", err)
	}
}
