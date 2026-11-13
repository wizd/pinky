package standalone

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
)

func TestHistory(t *testing.T) {
	dir := t.TempDir()
	t.Run("tasks", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.DataDir = dir })
		ctl, sub := h.dial(t), h.dial(t)
		sub.send(map[string]any{"cmd": "subscribe"})
		sub.read()
		turns := func(ts ...func(string, *http.Request) map[string]any) {
			h.api.mu.Lock()
			h.api.n, h.api.turns = 0, ts
			h.api.mu.Unlock()
		}
		start := func(task string, extra map[string]any) {
			t.Helper()
			req := map[string]any{"cmd": "start", "task": task, "key": "sk-test", "base_url": h.url, "model": "fake"}
			for k, v := range extra {
				req[k] = v
			}
			if r := ctl.do(req); !r.OK {
				t.Fatalf("start %q: %+v", task, r)
			}
		}
		ended := func(s State) bool { return s.Agent != nil && !s.Agent.Running }
		history := func(req map[string]any) reply {
			t.Helper()
			req["cmd"] = "history"
			r := ctl.do(req)
			if !r.OK {
				t.Fatalf("history %v: %+v", req, r)
			}
			return r
		}
		// recorded waits until the history has n tasks, each with the
		// agent's end.
		recorded := func(n int) []PastTask {
			t.Helper()
			var list []PastTask
			eventually(t, strconv.Itoa(n)+" tasks are recorded", func() bool {
				list = history(map[string]any{}).History
				if len(list) != n {
					return false
				}
				for _, p := range list {
					if p.Agent == nil {
						return false
					}
				}
				return true
			})
			return list
		}

		if r := history(map[string]any{}); len(r.History) != 0 || r.More {
			t.Fatalf("before any task: %+v", r)
		}

		// A task the model finishes.
		turns(
			func(string, *http.Request) map[string]any { return call("t1", "phone_observe", nil) },
			func(string, *http.Request) map[string]any {
				return call("t2", "finish", map[string]any{"outcome": "done", "summary": "Looked."})
			},
		)
		start("Look around", nil)
		st := sub.until("the first task's end", ended)
		if st.Task == nil || st.Task.ID == "" {
			t.Fatalf("task: %+v", st.Task)
		}
		first := st.Task.ID
		recorded(1)
		h.core.Dismiss()

		// One that reaches its step limit: the gateway calls it failed, the
		// screens stopped.
		turns(func(string, *http.Request) map[string]any { return call("t1", "phone_observe", nil) })
		start("Count to one", map[string]any{"max_steps": 1})
		st = sub.until("the second task's end", ended)
		if st.Agent.End != string(pilot.StepLimit) {
			t.Fatalf("second task: %+v", st.Agent)
		}
		second := st.Task.ID
		recorded(2)
		h.core.Dismiss()

		// A running task is not in the history yet.
		asked := make(chan struct{})
		var once sync.Once
		turns(func(_ string, r *http.Request) map[string]any {
			once.Do(func() { close(asked) })
			<-r.Context().Done()
			return nil
		})
		start("Wait", nil)
		<-asked
		r := history(map[string]any{})
		if len(r.History) != 2 || r.More || r.History[0].ID != second || r.History[1].ID != first {
			t.Fatalf("while a task runs: %+v", r)
		}
		if r := ctl.do(map[string]any{"cmd": "stop"}); !r.OK {
			t.Fatalf("stop: %+v", r)
		}
		st = sub.until("the third task's end", ended)
		third := st.Task.ID

		list := recorded(3)
		want := []struct {
			id, state, outcome, end string
		}{
			{third, "stopped", OutcomeStopped, "stopped"},
			{second, "failed", OutcomeStopped, "steps"},
			{first, "done", OutcomeDone, "done"},
		}
		for i, w := range want {
			p := list[i]
			if p.ID != w.id || p.State != w.state || p.Outcome != w.outcome || p.Agent.End != w.end ||
				p.StartedMS == 0 || p.EndedMS < p.StartedMS || p.Steps != nil {
				t.Errorf("task %d: %+v %+v, want %+v", i, p, p.Agent, w)
			}
		}
		if a := list[2].Agent; a.Task != "Look around" || a.Summary != "Looked." || a.Steps != 1 || a.Model != "fake" {
			t.Errorf("the first task's end: %+v", a)
		}
		if list[1].Agent.MaxSteps != 1 || list[0].Title != "Wait" {
			t.Errorf("the second task's limit, the third's title: %+v, %q", list[1].Agent, list[0].Title)
		}

		// Pages.
		r = history(map[string]any{"limit": 2})
		if len(r.History) != 2 || !r.More || r.History[1].ID != second {
			t.Fatalf("first page: %+v", r)
		}
		r = history(map[string]any{"limit": 2, "before": r.History[1].StartedMS})
		if len(r.History) != 1 || r.More || r.History[0].ID != first {
			t.Fatalf("second page: %+v", r)
		}

		// One task, with its steps.
		r = history(map[string]any{"id": first})
		if len(r.History) != 1 || r.History[0].ID != first || len(r.History[0].Steps) == 0 || r.History[0].Agent == nil {
			t.Fatalf("one task: %+v", r)
		}
		if r := ctl.do(map[string]any{"cmd": "history", "id": "nope"}); r.OK || r.Code != CodeGone {
			t.Fatalf("a task the history does not have: %+v", r)
		}
	})

	open := func() *Core {
		c, err := New(Options{DataDir: dir, Version: "test", Log: quiet})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	outcomes := func(c *Core) (out []string, agents int) {
		t.Helper()
		list, more, err := c.History(context.Background(), time.Time{}, 0)
		if err != nil || more {
			t.Fatalf("history: %v, more %v", err, more)
		}
		for _, p := range list {
			out = append(out, p.Outcome)
			if p.Agent != nil {
				agents++
			}
		}
		return out, agents
	}

	// The history and the agent's records stay.
	c := open()
	if got, agents := outcomes(c); len(got) != 3 || agents != 3 || got[0] != OutcomeStopped || got[1] != OutcomeStopped ||
		got[2] != OutcomeDone {
		t.Fatalf("after a restart: %v, %d with the agent's end", got, agents)
	}
	c.Close()

	// Without the records, the gateway's state says how a task ended.
	if err := os.Remove(filepath.Join(dir, "runs.json")); err != nil {
		t.Fatal(err)
	}
	c = open()
	defer c.Close()
	if got, agents := outcomes(c); len(got) != 3 || agents != 0 || got[0] != OutcomeStopped || got[1] != OutcomeFailed ||
		got[2] != OutcomeDone {
		t.Fatalf("without the records: %v, %d with the agent's end", got, agents)
	}
}

// A task that never ended, because the app stopped while it ran, is in the
// history as failed.
func TestHistoryUnended(t *testing.T) {
	v := gateway.TaskView{ID: "t", Title: "Order lunch", State: gateway.TaskWorking, Started: time.UnixMilli(1000)}
	p := pastTask(v, nil, false)
	if p.State != "failed" || p.Outcome != OutcomeFailed || p.EndedMS != 0 || p.StartedMS != 1000 {
		t.Fatalf("%+v", p)
	}
}

func TestOutcomeOf(t *testing.T) {
	for end, want := range map[pilot.End]string{
		pilot.Done: OutcomeDone, pilot.Stopped: OutcomeStopped, pilot.StepLimit: OutcomeStopped,
		pilot.TimeLimit: OutcomeStopped, pilot.CostLimit: OutcomeStopped, pilot.NoKey: OutcomeNotStarted,
		pilot.BadKey: OutcomeNotStarted, pilot.Failed: OutcomeFailed, pilot.Refused: OutcomeFailed,
		pilot.Error: OutcomeFailed,
	} {
		if got := outcomeOf(end); got != want {
			t.Errorf("%q: %q, want %q", end, got, want)
		}
	}
}

func TestKeepRuns(t *testing.T) {
	var runs []runRecord
	for i := range maxRuns + 2 {
		runs = keepRuns(runs, runRecord{Task: strconv.Itoa(i), EndedMS: int64(i)}, 0)
	}
	if len(runs) != maxRuns || runs[0].Task != "2" || runs[maxRuns-1].Task != strconv.Itoa(maxRuns+1) {
		t.Fatalf("past the cap: %d, %s … %s", len(runs), runs[0].Task, runs[len(runs)-1].Task)
	}
	// A task's new record replaces its old one.
	runs = keepRuns(runs, runRecord{Task: "5", EndedMS: 5000}, 0)
	if len(runs) != maxRuns || runs[3].Task != "6" || runs[maxRuns-1].Task != "5" {
		t.Fatalf("again: %d, %s, %s", len(runs), runs[3].Task, runs[maxRuns-1].Task)
	}
	// Records that ended before the cutoff go.
	runs = keepRuns(runs, runRecord{Task: "new", EndedMS: 6000}, int64(maxRuns))
	if len(runs) != 4 || runs[0].Task != strconv.Itoa(maxRuns) || runs[1].Task != strconv.Itoa(maxRuns+1) ||
		runs[2].Task != "5" || runs[3].Task != "new" {
		t.Fatalf("after the cutoff: %+v", runs)
	}
}
