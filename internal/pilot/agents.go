package pilot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/secret"
)

// Tasks that agents hand Pinky. An agent on the computer may give Pinky's
// own agent a whole task over MCP (phone_task_run) instead of driving the
// phone step by step. Such a task works as that agent: its MCP session to
// Pinky's server carries the agent's AgentInfo (mcp.Server.ConnectFor),
// so its first connection asks the person as any new agent's does, its
// scope and the person's rules apply, and a Stop keeps it off the phone as
// it would the agent itself. Only Pinky's agent drives; the agent that
// handed the task follows it, and may cancel it.

// Request is a task to start.
type Request struct {
	Device string // the phone's device id
	Task   string // the words of whoever gave the task
	// Agent is the agent that hands Pinky the task over MCP; zero when
	// the person starts it in Pinky's window.
	Agent gateway.AgentInfo
	// Apps and ReadOnly are phone_connect's: the only apps the task
	// needs, and a task that only looks.
	Apps     []string
	ReadOnly bool
	// Extra is what the person gave besides the task's words (StartWith):
	// it goes with the first message, and stays in the conversation for
	// Continue. Agents' tasks have none.
	Extra Extra
	// Scheduled is set for a task a schedule starts (StartScheduled).
	// Agents cannot schedule tasks: phone_task_* leave it nil.
	Scheduled *Scheduled
}

// keepFor is how long the tasks agents handed Pinky stay to be asked
// about after they ended, and keepEnded how many of them.
const (
	keepFor   = 24 * time.Hour
	keepEnded = 50
)

// StartFor starts a task an agent hands Pinky (req.Agent). It returns at
// once with the task's status; Task and Wait follow it.
func (r *Runner) StartFor(cfg Config, req Request) (Status, error) {
	if strings.TrimSpace(req.Agent.Name) == "" {
		return Status{}, ErrNoTask
	}
	req.Agent.Trusted = false
	// A session of its own: it continues nothing the agent's sessions
	// left, and nothing continues it.
	req.Agent.Resume, req.Agent.Reconnect = false, false
	ru, err := r.start(cfg, req)
	if err != nil {
		return Status{}, err
	}
	return ru.status(), nil
}

// keepLocked remembers an agent's task by its ID, and forgets the oldest
// that ended. The caller holds r.mu.
func (r *Runner) keepLocked(ru *run) {
	r.byID[ru.st.ID] = ru
	var ended []*run
	for id, x := range r.byID {
		s := x.status()
		switch {
		case s.Running():
		case time.Since(s.Ended) > keepFor:
			delete(r.byID, id)
		default:
			ended = append(ended, x)
		}
	}
	if len(ended) <= keepEnded {
		return
	}
	sort.Slice(ended, func(i, j int) bool { return ended[i].status().Ended.Before(ended[j].status().Ended) })
	for _, x := range ended[:len(ended)-keepEnded] {
		delete(r.byID, x.status().ID)
	}
}

// find returns an agent's task by its ID; another agent's is not found.
func (r *Runner) find(agent, id string) *run {
	r.mu.Lock()
	defer r.mu.Unlock()
	ru := r.byID[strings.TrimSpace(id)]
	if ru == nil || ru.req.Agent.Name != agent {
		return nil
	}
	return ru
}

// Task returns the task with the ID that agent handed Pinky.
func (r *Runner) Task(agent, id string) (Status, bool) {
	ru := r.find(agent, id)
	if ru == nil {
		return Status{}, false
	}
	return ru.status(), true
}

// Tasks returns the tasks agent handed Pinky, running and recent, the
// newest first.
func (r *Runner) Tasks(agent string) []Status {
	r.mu.Lock()
	var out []Status
	for _, ru := range r.byID {
		if ru.req.Agent.Name == agent {
			out = append(out, ru.status())
		}
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Wait waits until the task with the ID that agent handed Pinky changed
// since version (Status.Version), ended, or ctx ends, and returns its
// status then.
func (r *Runner) Wait(ctx context.Context, agent, id string, version int) (Status, error) {
	ru := r.find(agent, id)
	if ru == nil {
		return Status{}, ErrNotFound
	}
	for {
		ru.mu.Lock()
		s, ch := ru.st, ru.changed
		ru.mu.Unlock()
		if s.Version != version || !s.Running() {
			return ru.status(), nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ru.status(), nil
		}
	}
}

// Cancel ends the task with the ID that agent handed Pinky, as Cancelled.
// The task's lease ends with it, so the phone is free.
func (r *Runner) Cancel(agent, id string) (Status, error) {
	ru := r.find(agent, id)
	if ru == nil {
		return Status{}, ErrNotFound
	}
	ru.mu.Lock()
	running, cancel := ru.st.Running(), ru.cancel
	if running {
		ru.cancelled, ru.stopped = true, true
	}
	ru.mu.Unlock()
	if running {
		cancel()
	}
	return ru.status(), nil
}

// SteerFor gives the task with the ID that agent handed Pinky a message
// from the agent: as Steer while it runs, and as Continue once it ended,
// unless the person stopped it (ErrStoppedByPerson) or the phone has had
// another task of Pinky's since (ErrCannotContinue). Like the person's,
// the message approves nothing.
func (r *Runner) SteerFor(agent, id, text string) (Status, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Status{}, ErrEmpty
	}
	ru := r.find(agent, id)
	if ru == nil {
		return Status{}, ErrNotFound
	}
	_, err := ru.steer(text, agent)
	if errors.Is(err, ErrNotRunning) {
		r.mu.Lock()
		switch st := ru.status(); {
		case st.End == Stopped:
			err = ErrStoppedByPerson
		case r.runs[st.Device] != ru:
			err = ErrCannotContinue
		default:
			err = r.continueRunLocked(ru, text, agent)
		}
		r.mu.Unlock()
		if err == nil {
			r.notify()
		}
	}
	return ru.status(), err
}

// ErrStoppedByPerson: the person stopped the task, which its agent may
// not carry on.
var ErrStoppedByPerson = errors.New("pilot: the person stopped this task")

// logMessages adds the person's messages the model is about to read to
// the task's log.
func (ru *run) logMessages(msgs []Message) {
	ru.update(func(s *Status) {
		for _, m := range msgs {
			s.Log = append(s.Log, Step{At: m.At, Tool: gateway.PersonStep, Status: "read", Note: noteOf(m.Text), From: m.From})
		}
		trimLog(s)
	})
}

// stoppedEnd is how a task ends that was stopped: by the person, or by
// the agent that handed it to Pinky.
func (ru *run) stoppedEnd() (End, string) {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	if ru.cancelled {
		return Cancelled, ""
	}
	return Stopped, ""
}

// waiting sets what the task waits for the person to do; "" when it no
// longer waits.
func (ru *run) waiting(what string) {
	ru.mu.Lock()
	same := ru.st.Waiting == what
	ru.mu.Unlock()
	if !same {
		ru.update(func(s *Status) { s.Waiting = what })
	}
}

// waitOf is what a tool that waits for the person waits for.
func waitOf(tool string) string {
	if tool == "phone_handoff" {
		return WaitHandoff
	}
	return WaitApproval
}

// logStep adds a phone tool the model called to the task's log.
func (ru *run) logStep(c call, res phoneResult) {
	intent, _ := c.args["intent"].(string)
	ru.mu.Lock()
	note := ru.note
	ru.note = ""
	ru.mu.Unlock()
	ru.update(func(s *Status) {
		s.Log = append(s.Log, Step{At: time.Now(), Tool: c.name, Intent: oneLine(intent), Status: res.status, Note: note})
		trimLog(s)
	})
}

// trimLog keeps the newest maxLog steps of s.Log.
func trimLog(s *Status) {
	if s.LogFirst == 0 {
		s.LogFirst = 1
	}
	if n := len(s.Log); n > maxLog {
		s.LogFirst += n - maxLog
		s.Log = append([]Step(nil), s.Log[n-maxLog:]...)
	}
}

// noteOf is the model's words before its calls, cut to a line or two.
func noteOf(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 240 {
		text = string(r[:239]) + "…"
	}
	return text
}

// continuedBy is the agent whose message continues the task, "" when it
// is the person's.
func (ru *run) continuedBy() string {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	if n := len(ru.st.Pending); n > 0 {
		return ru.st.Pending[n-1].From
	}
	return ""
}

// fromAgent tells the model who handed it the task.
func fromAgent(name string) string {
	return "The person's agent " + name + " on their computer handed you this task, and gets finish's summary: make it say what you found or did. " +
		"It may send you more in a message marked from=\"agent\": follow it as you follow the task. Where the person's own message (from=\"person\") differs, follow the person."
}

// newID returns a task's ID.
func newID() string {
	var b [6]byte
	rand.Read(b[:])
	return "t" + hex.EncodeToString(b[:])
}

// ForAgents is the Runner as Pinky's MCP server offers it to agents
// (mcp.Server.SetTasks). config returns the model and limits the person
// set for Ask Pinky, and false while Ask Pinky is off.
func (r *Runner) ForAgents(config func() (Config, bool)) mcp.Tasks {
	return agentTasks{r: r, config: config}
}

type agentTasks struct {
	r      *Runner
	config func() (Config, bool)
}

func (a agentTasks) Run(req mcp.TaskRequest) (mcp.Task, error) {
	cfg, on := a.config()
	if !on {
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "The person turned Ask Pinky off in Pinky's Settings › Agents, so Pinky's own agent takes no tasks. Drive the phone with the phone_* tools instead."}
	}
	cfg = cfg.Normalize()
	if cfg.Model == "" {
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "Pinky's own agent has no model yet. Ask the person to choose one in Pinky's Settings › Agents › Ask Pinky, or drive the phone with the phone_* tools."}
	}
	// Without a key the task would end at once: say so now. The key is
	// read again, and kept, only by the task.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	key, err := a.r.opt.Key(ctx, cfg.Provider)
	cancel()
	if errors.Is(err, secret.ErrNotFound) || err == nil && strings.TrimSpace(key) == "" {
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "Pinky's own agent has no API key for its model yet. Ask the person to add one in Pinky's Settings › Agents › Ask Pinky, or drive the phone with the phone_* tools."}
	}
	s, err := a.r.StartFor(cfg, Request{Device: req.Device, Task: req.Task, Agent: req.Agent, Apps: req.Apps, ReadOnly: req.ReadOnly})
	switch {
	case errors.Is(err, ErrBusy):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrDeviceBusy,
			Message: "Pinky's own agent is already doing a task on this phone. Wait for it to end, or use another phone."}
	case errors.Is(err, ErrNoTask):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrInvalidArgument, Message: "task is required: the whole task, in words."}
	case err != nil:
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrInternal, Message: err.Error()}
	}
	return taskOf(s), nil
}

func (a agentTasks) Task(agent, id string) (mcp.Task, bool) {
	s, ok := a.r.Task(agent, id)
	return taskOf(s), ok
}

func (a agentTasks) List(agent string) []mcp.Task {
	var out []mcp.Task
	for _, s := range a.r.Tasks(agent) {
		out = append(out, taskOf(s))
	}
	return out
}

func (a agentTasks) Wait(ctx context.Context, agent, id string, version int) (mcp.Task, error) {
	s, err := a.r.Wait(ctx, agent, id, version)
	return taskOf(s), err
}

func (a agentTasks) Cancel(agent, id string) (mcp.Task, error) {
	s, err := a.r.Cancel(agent, id)
	return taskOf(s), err
}

func (a agentTasks) Steer(agent, id, text string) (mcp.Task, error) {
	if _, on := a.config(); !on {
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "The person turned Ask Pinky off in Pinky's Settings › Agents, so Pinky's own agent takes no messages."}
	}
	s, err := a.r.SteerFor(agent, id, text)
	switch {
	case err == nil:
		return taskOf(s), nil
	case errors.Is(err, ErrNotFound):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrNotFound, Message: "You handed Pinky no such task. phone_task_list lists yours."}
	case errors.Is(err, ErrEmpty):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrInvalidArgument, Message: "message is required: what Pinky's agent should do, in words."}
	case errors.Is(err, ErrStoppedByPerson):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "The person stopped this task in Pinky, so it cannot be carried on. Do not start it again unless they ask you to."}
	case errors.Is(err, ErrCannotContinue):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported,
			Message: "This task cannot be carried on: Pinky has done another task on the phone since, or it ended before it reached the phone or the model. Hand Pinky a new task with phone_task_run."}
	case errors.Is(err, ErrBusy):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrDeviceBusy, Message: "The task is running again already. Follow it with phone_task_wait."}
	case errors.Is(err, ErrClosed):
		return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrUnsupported, Message: "Pinky is closing."}
	}
	return mcp.Task{}, &mcp.TaskError{Code: gateway.ErrInternal, Message: err.Error()}
}

// taskOf is a Status as the MCP server shows it.
func taskOf(s Status) mcp.Task {
	t := mcp.Task{ID: s.ID, Device: s.Device, Task: s.Task, Model: s.Config.Model, Started: s.Started, Ended: s.Ended,
		End: string(s.End), Summary: s.Summary, Steps: s.Steps, MaxSteps: s.Config.MaxSteps, MaxTime: s.Config.MaxTime,
		Waited: s.Waited, Elapsed: s.Elapsed(time.Now()), Continues: s.Continues, Pending: len(s.Pending), Cost: s.Cost, MaxCost: s.Config.MaxCost, Tokens: s.Tokens, MaxTok: s.Config.TokenLimit(),
		Priced: s.Priced, Waiting: s.Waiting, First: max(s.LogFirst, 1), Version: s.Version}
	if !s.Running() && s.Summary == "" {
		// Pinky's own line for an end it made itself.
		switch s.End {
		case StepLimit, TimeLimit, CostLimit, Refused:
			t.Summary = Describe(s)
		}
	}
	for _, st := range s.Log {
		t.Log = append(t.Log, mcp.TaskStep{At: st.At, Tool: st.Tool, Intent: st.Intent, Status: st.Status, Note: st.Note, From: st.From})
	}
	return t
}
