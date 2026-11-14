package ui

import (
	"context"
	"slices"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestDeleteTask deletes one task from its ⋯ menu in Activity: an alert
// asks first, Cancel deletes nothing, Delete deletes it through the
// gateway, and Activity shows the task after it. Delete is disabled while
// the task runs and for a gateway that cannot delete one.
func TestDeleteTask(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	i := slices.IndexFunc(ta.act.tasks, func(x gateway.TaskView) bool { return x.ID == "h-lunch" })
	if i < 0 || i == len(ta.act.tasks)-1 {
		t.Fatalf("Activity's tasks: %+v", ta.act.tasks)
	}
	next := ta.act.tasks[i+1].ID
	ta.act.sel = "h-lunch"
	tt.Frame()

	choose := func() {
		t.Helper()
		if err := tt.Click("More actions"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if err := tt.Click("Delete"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	choose()
	mustHave(t, tt, "Delete “Reorder last week's lunch”?", "screen recording are removed from this computer")
	if err := tt.Click("Cancel"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ta.act.asking || len(ta.api.Deleted()) != 0 {
		t.Fatalf("Cancel: asking %v, deleted %v", ta.act.asking, ta.api.Deleted())
	}

	choose()
	tt.Key(0, mg.KeyEnter) // the alert's Delete
	tt.Frame()
	ta.waitFor(t, "the delete", func() bool { tt.Frame(); return ta.act.deleting == "" && len(ta.api.Deleted()) == 1 })
	if d := ta.api.Deleted(); d[0] != "h-lunch" {
		t.Fatalf("deleted %v", d)
	}
	ta.waitFor(t, "Activity read again", func() bool {
		tt.Frame()
		return !slices.ContainsFunc(ta.act.tasks, func(x gateway.TaskView) bool { return x.ID == "h-lunch" })
	})
	if ta.act.sel != next {
		t.Errorf("shows %q after the delete, want the next task %q", ta.act.sel, next)
	}
	tt.Frame()
	if slices.Contains(tt.Texts(), "Reorder last week's lunch") {
		t.Errorf("the deleted task still shows: %q", tt.Texts())
	}
	mustHave(t, tt, "Deleted “Reorder last week's lunch”")

	// A running task, and a gateway that cannot delete: Delete asks nothing.
	ta.act.sel = next
	tasks := ta.tasks
	ta.tasks = append(slices.Clone(tasks), gateway.TaskView{ID: next, State: gateway.TaskWorking})
	tryDelete := func(what string) {
		t.Helper()
		tt.Frame()
		if err := tt.Click("More actions"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Click("Delete") // disabled: an error, or nothing
		tt.Frame()
		if ta.act.asking {
			t.Errorf("Delete asked for %s", what)
			ta.act.asking = false
		}
		tt.Key(0, mg.KeyEscape) // the menu
		tt.Frame()
	}
	tryDelete("a running task")
	ta.tasks = tasks
	ta.app.api = struct{ gateway.API }{ta.api}
	tryDelete("a gateway that cannot delete")
}

// TestDeleteTaskFails shows the gateway's error and keeps the task.
func TestDeleteTaskFails(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.page = pageActivity
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "Activity", func() bool { tt.Frame(); return ta.act.loaded })
	ta.app.api = refusingDelete{ta.api}
	task := ta.act.tasks[0]
	ta.act.sel = task.ID
	ta.deleteTask(task)
	ta.waitFor(t, "the refusal", func() bool { return ta.act.deleting == "" })
	if !slices.Contains(ta.toasts, "Couldn't delete the task: the task is still running") {
		t.Errorf("toasts %q", ta.toasts)
	}
	if ta.act.sel != task.ID || !slices.ContainsFunc(ta.act.tasks, func(x gateway.TaskView) bool { return x.ID == task.ID }) {
		t.Errorf("a failed delete left the task: sel %q", ta.act.sel)
	}
}

type refusingDelete struct{ gateway.API }

func (refusingDelete) DeleteTasks(context.Context, []string) error {
	return gateway.ErrTaskRunning
}
