package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestTaskOfAPinkyThatCrashed: a task that ran when Pinky was killed has
// no end in the history. The next Pinky ends it at its last step, as a
// gateway that stopped in order does (failed, "gateway stopped"), so that
// Activity shows no task that runs for ever and Export all has it.
func TestTaskOfAPinkyThatCrashed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	g1, _ := newTestGateway(t, func(o *Options) { o.DataDir = dir })
	s, _ := connect(t, g1, "Lunch")
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	g1.Flush()
	// Killed here: g1 writes nothing more before the next Pinky starts.

	g2, _ := newTestGateway(t, func(o *Options) { o.DataDir = dir })
	stored, err := g2.History(ctx, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("%d tasks in the history", len(stored))
	}
	task := stored[0]
	if task.State != TaskFailed {
		t.Errorf("state %q, want %q", task.State, TaskFailed)
	}
	if len(task.Steps) == 0 {
		t.Fatal("no steps in the history")
	}
	last := task.Steps[len(task.Steps)-1]
	if want := last.At.Add(last.Elapsed); !task.Ended.Equal(want) {
		t.Errorf("ended %v, want the end of its last step %v", task.Ended, want)
	}
	rec, ok, err := g2.store.Task(ctx, task.ID)
	if err != nil || !ok {
		t.Fatalf("stored task: ok=%v err=%v", ok, err)
	}
	if rec.Outcome != "gateway stopped" {
		t.Errorf("outcome %q, want \"gateway stopped\"", rec.Outcome)
	}
	// Export all leaves out what still runs: this one no longer does.
	out, err := g2.exportTasks(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != task.ID {
		t.Errorf("export all has %d tasks, want this one", len(out))
	}

}

// TestApprovalOfAPinkyThatCrashed: an approval that waited for the person
// when Pinky was killed can no longer be answered. The next Pinky takes it
// back, as a gateway that stopped in order does, so that the task's step
// in Activity does not wait for the person for ever.
func TestApprovalOfAPinkyThatCrashed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	g1, _ := newTestGateway(t, func(o *Options) { o.DataDir = dir; o.MaxBlock = 20 * time.Millisecond })
	s, _ := connect(t, g1, "Lunch")
	mustStatus(t, payInDinely(t, s), StatusPending)
	g1.Flush()
	// Killed here, with the approval open.

	g2, _ := newTestGateway(t, func(o *Options) { o.DataDir = dir })
	stored, err := g2.History(ctx, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("%d tasks in the history", len(stored))
	}
	task := stored[0]
	last := task.Steps[len(task.Steps)-1]
	if last.Status != "withdrawn" {
		t.Errorf("the approval's step %q has status %q, want withdrawn", last.Text, last.Status)
	}
	aps, err := g2.store.Approvals(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(aps) != 1 {
		t.Fatalf("%d approvals in the history", len(aps))
	}
	if aps[0].Decision != "withdrawn" || aps[0].Decided.IsZero() {
		t.Errorf("approval: decision %q, decided %v; want withdrawn at a time", aps[0].Decision, aps[0].Decided)
	}
}

// TestAllowedActionOfAPinkyThatCrashed: the person allowed an action, and
// Pinky was killed while it ran. Nobody knows whether it happened. The next
// Pinky says so on the task's step, which would otherwise wait for the
// person for ever, and keeps the answer the person gave.
func TestAllowedActionOfAPinkyThatCrashed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	g1, hd := newHookGateway(t, func(o *Options) { o.DataDir = dir })
	s, _ := connect(t, g1, "Lunch")
	// Once the person has answered, the phone answers no more: the
	// allowed tap never finishes.
	hang := make(chan struct{})
	answer(t, g1, AllowOnce, func(NeedView) {
		hd.setObserve(func(*screen.Screen) { <-hang })
	})
	done := make(chan *Result, 1)
	go func() { done <- payInDinely(t, s) }()
	defer func() { close(hang); <-done }()
	stored := func(st *history.Store) history.Approval {
		t.Helper()
		tasks, err := st.Tasks(ctx, history.Query{})
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks) != 1 {
			return history.Approval{}
		}
		aps, err := st.Approvals(ctx, tasks[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(aps) != 1 {
			return history.Approval{}
		}
		return aps[0]
	}
	deadline := time.Now().Add(3 * time.Second)
	for stored(g1.store).Decision != string(AllowOnce) {
		if time.Now().After(deadline) {
			t.Fatal("the answer was not stored")
		}
		time.Sleep(2 * time.Millisecond)
		g1.Flush()
	}
	// Killed here, with the allowed tap under way.

	g2, _ := newTestGateway(t, func(o *Options) { o.DataDir = dir })
	tasks, err := g2.History(ctx, HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("%d tasks in the history", len(tasks))
	}
	last := tasks[0].Steps[len(tasks[0].Steps)-1]
	if last.Status != string(StatusUncertain) {
		t.Errorf("the allowed step %q has status %q, want %s", last.Text, last.Status, StatusUncertain)
	}
	if ap := stored(g2.store); ap.Decision != string(AllowOnce) {
		t.Errorf("approval: decision %q, want the person's %s", ap.Decision, AllowOnce)
	}
}
