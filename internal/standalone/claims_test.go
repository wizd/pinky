package standalone

import (
	"context"
	"net/http"
	"sync"
	"testing"
)

// fakeClaims stands in for the app's claims (local.Claims).
type fakeClaims struct {
	watching chan struct{} // closed once Watch runs

	mu       sync.Mutex
	changed  func()
	lost     func()
	computer *ComputerState // a computer's claim
	held     bool           // the core's claim, for task
	task     string
	asked    int // Claim calls
	releases int // Release calls that gave the core's claim back
}

func newFakeClaims() *fakeClaims { return &fakeClaims{watching: make(chan struct{})} }

func (f *fakeClaims) Watch(ctx context.Context, changed, lost func()) {
	f.mu.Lock()
	f.changed, f.lost = changed, lost
	f.mu.Unlock()
	close(f.watching)
	<-ctx.Done()
}

func (f *fakeClaims) Claim(_ context.Context, task string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked++
	if f.computer != nil {
		return true, nil
	}
	f.held, f.task = true, task
	return false, nil
}

func (f *fakeClaims) Release(context.Context) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.held {
		f.releases++
	}
	f.held, f.task = false, ""
}

func (f *fakeClaims) Computer() (string, bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.computer == nil {
		return "", false, false
	}
	return f.computer.Task, f.computer.Stoppable, true
}

func (f *fakeClaims) StopComputer(context.Context) error {
	f.mu.Lock()
	stop := f.computer != nil && f.computer.Stoppable
	if stop {
		f.computer = nil
	}
	changed := f.changed
	f.mu.Unlock()
	if stop {
		changed()
	}
	return nil
}

// computerClaims sets or ends a computer's claim, as the app's claim events
// do.
func (f *fakeClaims) computerClaims(cs *ComputerState) {
	f.mu.Lock()
	f.computer = cs
	changed := f.changed
	f.mu.Unlock()
	changed()
}

// phone returns the core's claim, and the claims asked for and given back.
func (f *fakeClaims) phone() (task string, held bool, asked, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.task, f.held, f.asked, f.releases
}

// waitingTask scripts a task that looks at the phone once, then waits for
// the model until it is stopped; waiting hears when it waits.
func (h *harness) waitingTask(waiting chan<- struct{}) {
	h.api.mu.Lock()
	defer h.api.mu.Unlock()
	h.api.n, h.api.turns = 0, []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any { return call("t1", "phone_observe", nil) },
		func(_ string, r *http.Request) map[string]any {
			select {
			case waiting <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return nil
		},
	}
}

func TestClaims(t *testing.T) {
	claims := newFakeClaims()
	h := newHarness(t, func(o *Options) { o.Claims = claims })
	<-claims.watching
	ctl := h.dial(t)
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	start := func(task string) reply {
		t.Helper()
		return ctl.do(map[string]any{"cmd": "start", "task": task, "key": "sk-test", "base_url": h.url,
			"model": "fake"})
	}
	stopped := func(task string) State {
		t.Helper()
		return sub.until(task+" stops", func(s State) bool {
			return s.Agent != nil && s.Agent.Task == task && !s.Agent.Running
		})
	}

	// While Pinky on a computer works, Start waits; Stop on the pill ends
	// the computer's claim.
	claims.computerClaims(&ComputerState{Task: "Order lunch", Stoppable: true})
	st := sub.until("the computer's claim shows", func(s State) bool { return s.Computer != nil })
	if *st.Computer != (ComputerState{Task: "Order lunch", Stoppable: true}) {
		t.Fatalf("computer: %+v", *st.Computer)
	}
	if r := start("Set a timer"); r.OK || r.Code != CodeComputer {
		t.Fatalf("start while a computer works: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "stop", "computer": true}); !r.OK {
		t.Fatalf("stop the computer: %+v", r)
	}
	sub.until("the computer's claim ends", func(s State) bool { return s.Computer == nil })

	// A mistake in the form claims nothing.
	if r := start(" "); r.Code != CodeNoTask {
		t.Fatalf("start without a task: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Set a timer", "key": "sk-test", "provider": "openai",
		"base_url": h.url}); r.Code != CodeNoModel {
		t.Fatalf("start without a model: %+v", r)
	}
	if _, _, asked, _ := claims.phone(); asked != 1 {
		t.Fatalf("%d claims asked for; want 1", asked)
	}

	// The task holds the phone while it runs, also when the person presses
	// Start again, and gives it back when it ends.
	waiting := make(chan struct{}, 1)
	h.waitingTask(waiting)
	if r := start("Look around"); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	<-waiting
	if task, held, asked, _ := claims.phone(); !held || task != "Look around" || asked != 2 {
		t.Fatalf("the claim: %q, %v, %d", task, held, asked)
	}
	if r := start("Again"); r.Code != CodeBusy {
		t.Fatalf("start while a task runs: %+v", r)
	}
	if task, held, asked, _ := claims.phone(); !held || task != "Look around" || asked != 2 {
		t.Fatalf("the claim after a second start: %q, %v, %d", task, held, asked)
	}
	if r := ctl.do(map[string]any{"cmd": "stop"}); !r.OK {
		t.Fatalf("stop: %+v", r)
	}
	stopped("Look around")
	eventually(t, "the claim is given back", func() bool {
		_, held, _, releases := claims.phone()
		return !held && releases == 1
	})
	eventually(t, "the key is gone", func() bool { return h.key() == "" })

	// A task whose claim is lost stops.
	h.waitingTask(waiting)
	if r := start("Water the plants"); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	<-waiting
	claims.mu.Lock()
	lost := claims.lost
	claims.mu.Unlock()
	lost()
	if st := stopped("Water the plants"); st.Agent.End != "stopped" {
		t.Fatalf("after the claim was lost: %+v", st.Agent)
	}
	eventually(t, "the claim is given back", func() bool {
		_, held, _, releases := claims.phone()
		return !held && releases == 2
	})

	// The end of an earlier task leaves a later task's key and claim alone.
	h.core.mu.Lock()
	run := h.core.run
	h.core.key = "sk-later"
	h.core.mu.Unlock()
	claims.mu.Lock()
	claims.held = true
	claims.mu.Unlock()
	h.core.ended(run - 1)
	if _, held, _, _ := claims.phone(); !held || h.key() != "sk-later" {
		t.Fatalf("an earlier task's end: held %v, key %q", held, h.key())
	}
	h.core.ended(run)
	if _, held, _, _ := claims.phone(); held || h.key() != "" {
		t.Fatalf("the task's end: held %v, key %q", held, h.key())
	}
}
