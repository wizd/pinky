package standalone

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
)

// The history is the tasks Pinky did on the phone, for the task screen's
// Activity. The gateway keeps each task and its steps in its history
// database in DataDir and deletes them after Options.KeepDays. How the
// core's agent ended a task (its end, summary and usage: AgentState) is not
// the gateway's, so the core keeps it next to that database, in runs.json,
// one record a task. A run that failed before it reached the phone
// (phone_connect) has no gateway task, and is not in the history.

// ErrGone answers for a task the history no longer has: deleted after
// KeepDays, or never there.
var ErrGone = errors.New("standalone: the task is no longer in the history")

// PastTask is a task that ended.
type PastTask struct {
	ID    string `json:"id"`
	Title string `json:"title"` // the person's words on one line
	// State is the gateway's: done, stopped or failed. A task that never
	// ended, because the app stopped or was updated while it ran, is failed.
	State string `json:"state"`
	// Outcome is what the screens say, one of the Outcome constants.
	Outcome   string `json:"outcome"`
	StartedMS int64  `json:"started_ms"`
	EndedMS   int64  `json:"ended_ms,omitempty"` // zero for a task that never ended
	// Agent is how the core's agent ended it, as the task screen showed it
	// then; nil when the core kept no record of it.
	Agent *AgentState `json:"agent,omitempty"`
	// Steps are only in the answer for one task.
	Steps []Step `json:"steps,omitempty"`
}

// How a past task ended, as the screens say it. With the core's record it
// follows the agent's end, as the task screen did when the task ended (the
// app's Words.end); without one, the gateway's state.
const (
	OutcomeDone       = "done"
	OutcomeStopped    = "stopped" // the person stopped it, or it reached a limit
	OutcomeFailed     = "failed"
	OutcomeNotStarted = "not_started" // the API refused the key
)

// History lists the tasks that ended, newest first: limit of them (20 for
// 0, at most 100) that started before before (zero is now), and whether
// more started earlier. Running tasks are left out.
func (c *Core) History(ctx context.Context, before time.Time, limit int) ([]PastTask, bool, error) {
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	running := map[string]bool{}
	for _, t := range c.G.Tasks() {
		if t.Ended.IsZero() {
			running[t.ID] = true
		}
	}
	views, err := c.G.History(ctx, gateway.HistoryQuery{Before: before, Limit: limit + 1 + len(running)})
	if err != nil {
		return nil, false, err
	}
	runs := c.runIndex()
	out := make([]PastTask, 0, limit+1)
	for _, v := range views {
		if !running[v.ID] {
			out = append(out, pastTask(v, runs, false))
		}
	}
	more := len(out) > limit
	if more {
		out = out[:limit]
	}
	return out, more, nil
}

// PastTask is one task of the history, with its steps, or ErrGone.
func (c *Core) PastTask(ctx context.Context, id string) (PastTask, error) {
	runs := c.runIndex()
	// The core's record says when the task started; without one, page
	// back through the history.
	if r, ok := runs[id]; ok {
		views, err := c.G.History(ctx, gateway.HistoryQuery{Before: time.UnixMilli(r.StartedMS + 1), Limit: 5})
		if err != nil {
			return PastTask{}, err
		}
		for _, v := range views {
			if v.ID == id {
				return pastTask(v, runs, true), nil
			}
		}
	}
	const page = 50
	var before time.Time
	for range 20 {
		views, err := c.G.History(ctx, gateway.HistoryQuery{Before: before, Limit: page})
		if err != nil {
			return PastTask{}, err
		}
		for _, v := range views {
			if v.ID == id {
				return pastTask(v, runs, true), nil
			}
		}
		if len(views) < page {
			break
		}
		before = views[len(views)-1].Started
	}
	return PastTask{}, ErrGone
}

func pastTask(v gateway.TaskView, runs map[string]runRecord, steps bool) PastTask {
	p := PastTask{ID: v.ID, Title: v.Title, State: string(v.State), StartedMS: v.Started.UnixMilli()}
	if !v.Ended.IsZero() {
		p.EndedMS = v.Ended.UnixMilli()
	}
	switch v.State {
	case gateway.TaskDone:
		p.Outcome = OutcomeDone
	case gateway.TaskStopped:
		p.Outcome = OutcomeStopped
	default:
		p.State, p.Outcome = string(gateway.TaskFailed), OutcomeFailed
	}
	if r, ok := runs[v.ID]; ok {
		a := r.Agent
		p.Agent = &a
		p.Outcome = outcomeOf(pilot.End(a.End))
	}
	if steps {
		p.Steps = stepsOf(v)
	}
	return p
}

// outcomeOf is the outcome of the agent's end.
func outcomeOf(e pilot.End) string {
	switch e {
	case pilot.Done:
		return OutcomeDone
	case pilot.Stopped, pilot.StepLimit, pilot.TimeLimit, pilot.CostLimit:
		return OutcomeStopped
	case pilot.NoKey, pilot.BadKey:
		return OutcomeNotStarted
	}
	return OutcomeFailed
}

// stepsOf is a gateway task's steps as the screens show them.
func stepsOf(t gateway.TaskView) []Step {
	var out []Step
	for _, s := range t.Steps {
		step := Step{Text: s.Text, Status: s.Status, MS: s.Elapsed.Milliseconds()}
		step.Shown, step.Asked = stepShown(s.Text)
		out = append(out, step)
	}
	return out
}

// taskOf is the gateway task of the agent's task s on device: the one it
// connected to (s.TaskID, which a continued task keeps), or before that the
// newest one of the phone that started with it, never an earlier one. Only
// the agent starts tasks on the phone.
func (c *Core) taskOf(device string, s pilot.Status) *gateway.TaskView {
	if s.TaskID != "" {
		for _, t := range c.G.Tasks() {
			if t.ID == s.TaskID {
				return &t
			}
		}
		return nil
	}
	since := s.Started.Add(-time.Second)
	for _, t := range c.G.Tasks() {
		if t.DeviceID == device && !t.Started.Before(since) {
			return &t
		}
	}
	return nil
}

// runRecord is how the core's agent ended a gateway task.
type runRecord struct {
	Task      string     `json:"task"`       // the gateway task's id
	StartedMS int64      `json:"started_ms"` // the gateway task's start, to find it
	EndedMS   int64      `json:"ended_ms"`   // the agent's end, for KeepDays
	Agent     AgentState `json:"agent"`
}

// maxRuns caps runs.json for a phone that keeps every task: older tasks
// then show without the agent's end.
const maxRuns = 1000

func (c *Core) runsPath() string { return filepath.Join(c.opt.DataDir, "runs.json") }

// loadRuns reads runs.json. Without it, past tasks show without the
// agent's end.
func (c *Core) loadRuns() {
	if c.opt.DataDir == "" {
		return
	}
	b, err := os.ReadFile(c.runsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err == nil {
		err = json.Unmarshal(b, &c.runs)
	}
	if err != nil {
		c.runs = nil
		c.log.Warn("history: the agent's records are unreadable; past tasks show without them", "err", err)
	}
}

// record keeps how the agent ended s, its task on device, in memory and in
// runs.json. A run that never reached the phone has no task to keep it for.
func (c *Core) record(device string, s pilot.Status) {
	t := c.taskOf(device, s)
	if t == nil {
		return
	}
	r := runRecord{Task: t.ID, StartedMS: t.Started.UnixMilli(), EndedMS: s.Ended.UnixMilli(), Agent: *agentState(s)}
	var cutoff int64
	if d := c.opt.KeepDays; d > 0 {
		cutoff = time.Now().Add(-time.Duration(d) * 24 * time.Hour).UnixMilli()
	}
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.runs = keepRuns(c.runs, r, cutoff)
	if c.opt.DataDir == "" {
		return
	}
	if err := writeJSON(c.runsPath(), c.runs); err != nil {
		c.log.Warn("history: keeping the agent's record", "err", err)
	}
}

// keepRuns adds r to runs, leaving out records that ended before cutoff
// (Unix milliseconds; zero keeps them) and the oldest past maxRuns.
func keepRuns(runs []runRecord, r runRecord, cutoff int64) []runRecord {
	out := make([]runRecord, 0, len(runs)+1)
	for _, old := range runs {
		if old.Task != r.Task && old.EndedMS >= cutoff {
			out = append(out, old)
		}
	}
	out = append(out, r)
	if n := len(out); n > maxRuns {
		out = out[n-maxRuns:]
	}
	return out
}

// runIndex is the records by task.
func (c *Core) runIndex() map[string]runRecord {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	m := make(map[string]runRecord, len(c.runs))
	for _, r := range c.runs {
		m[r.Task] = r
	}
	return m
}

// writeJSON replaces path with v, so that a crash leaves either the old
// file or the new one.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
