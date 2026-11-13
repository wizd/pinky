package standalone

import (
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
)

// State is what Pinky's screens show: the phone, the agent's task with its
// steps, and what waits for the person.
type State struct {
	Device   DeviceState    `json:"device"`
	Preset   int            `json:"preset"` // when agents ask: policy.Preset
	Computer *ComputerState `json:"computer,omitempty"`
	Agent    *AgentState    `json:"agent,omitempty"`
	Task     *TaskState     `json:"task,omitempty"`
	Needs    []Need         `json:"needs"`
	// Shell is the background displays' setup (display.go); nil without
	// a Shell.
	Shell *ShellState `json:"shell,omitempty"`
	// Background says tasks run on a background display, which the
	// floating window shows (the video command).
	Background bool `json:"background,omitempty"`
	// Notices are scheduled runs to tell the person of (schedules.go).
	Notices []Notice `json:"notices,omitempty"`
}

// ComputerState is Pinky on a computer while it holds the phone: the
// screens start no task meanwhile, and the pill offers Stop when it is
// Stoppable (an older desktop Pinky is not).
type ComputerState struct {
	Task      string `json:"task,omitempty"`
	Stoppable bool   `json:"stoppable"`
}

// DeviceState is the phone as the gateway sees it.
type DeviceState struct {
	Ready  bool   `json:"ready"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Line   string `json:"line,omitempty"`
	Detail string `json:"detail,omitempty"`
	// LineShown is Line in the screens' language, when it differs
	// (shown.go).
	LineShown string `json:"line_shown,omitempty"`
}

// AgentState is Ask Pinky's task: running, or the last one that ended.
// End is a pilot.End; the screens say it in the person's language, and
// Summary only says more for done, failed and error (it is the model's or
// the API's own words, and English for the other ends).
type AgentState struct {
	Task  string `json:"task"`
	Model string `json:"model"`
	// Provider is the API the task's model speaks (pilot.Anthropic or
	// pilot.OpenAI), the name keys were saved under before there were
	// services: apps of before read continue's key by it.
	Provider string `json:"provider,omitempty"`
	// Service is the task's model service (pilot.Services), and KeyName
	// the name its key is saved under (pilot.KeyName): the key continue
	// needs. KeyOptional says the service needs none (a local server).
	Service     string  `json:"service,omitempty"`
	KeyName     string  `json:"key_name,omitempty"`
	KeyOptional bool    `json:"key_optional,omitempty"`
	Running     bool    `json:"running"`
	End         string  `json:"end,omitempty"`
	Summary     string  `json:"summary,omitempty"`
	Steps       int     `json:"steps"`
	Cost        float64 `json:"cost"`
	Tokens      int64   `json:"tokens"`
	Seconds     int64   `json:"seconds"`
	// Priced says Cost is in US dollars; without it the model's prices are
	// unknown, and the cost limit is TokenLimit tokens.
	Priced bool `json:"priced"`
	// The task's limits, so that the screens can say which one ended it.
	MaxSteps   int     `json:"max_steps"`
	MaxMinutes int     `json:"max_minutes"`
	MaxCost    float64 `json:"max_cost"`
	TokenLimit int64   `json:"token_limit"`
	// Pending is the person's messages the model has not read yet, oldest
	// first: they can still be edited or withdrawn (steer, withdraw).
	// Once the task ended, they wait for continue.
	Pending []Message `json:"pending,omitempty"`
	// Continuable says the task that ended can go on (continue); Continues
	// counts how often it did. Steps, Cost, Tokens and Seconds are the
	// whole task's; the limits count anew each time.
	Continuable bool `json:"continuable,omitempty"`
	Continues   int  `json:"continues,omitempty"`
	// Scheduled says a schedule started the task, and Unattended that
	// nobody pressed Run now: the app notifies the person of what it asks.
	Scheduled  bool `json:"scheduled,omitempty"`
	Unattended bool `json:"unattended,omitempty"`
	// Suggested are the notes the task suggested keeping, which the
	// person keeps or drops by index (memory.go).
	Suggested []SuggestionState `json:"suggested,omitempty"`
}

// Message is a message of the person's to the agent.
type Message struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	MS   int64  `json:"ms"` // when it was sent, Unix milliseconds
}

// TaskState is the gateway's task: its steps in plain words. ID is the
// task's in the history (PastTask.ID).
type TaskState struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	State string `json:"state"`
	Steps []Step `json:"steps"`
}

// Step is one step of the task. Text is the gateway's English, which the
// screens read for what kind of step it is; Shown and Asked say it in the
// screens' language when that is another (Shown, stepShown).
type Step struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	MS     int64  `json:"ms"`
	// Shown is Text in the screens' language: its first line, and its
	// second when it has one.
	Shown string `json:"shown,omitempty"`
	// Asked is, for a step that waited for the person ("Waiting for you:
	// Tapped “Pay”"), what was asked in the screens' language ("点按“Pay”").
	Asked string `json:"asked,omitempty"`
}

// Need is an approval or a handoff the agent waits for.
type Need struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	App        string `json:"app,omitempty"`
	Rule       string `json:"rule,omitempty"`
	DeadlineMS int64  `json:"deadline_ms,omitempty"` // Unix milliseconds; no answer by then means deny
	AllowTask  bool   `json:"allow_task,omitempty"`
	// Title, Detail and Rule in the screens' language, when they differ
	// (shown.go).
	TitleShown  string `json:"title_shown,omitempty"`
	DetailShown string `json:"detail_shown,omitempty"`
	RuleShown   string `json:"rule_shown,omitempty"`
}

// State reads the current state.
func (c *Core) State() State {
	st := State{Preset: int(c.G.Policy().Preset())}
	d, ok := c.device()
	if ok {
		st.Device = DeviceState{Ready: d.Info.State == "online", Name: d.Info.Name, Status: string(d.Status),
			Line: d.StatusLine, Detail: d.Info.Detail, LineShown: lineShown(d)}
	}
	if sh := c.opt.Shell; sh != nil {
		s := sh.State()
		st.Shell = &s
	}
	if ok {
		st.Background = c.G.Background(d.Info.ID)
	}
	if cl := c.opt.Claims; cl != nil {
		if task, stoppable, held := cl.Computer(); held {
			st.Computer = &ComputerState{Task: task, Stoppable: stoppable}
		}
	}
	// The agent's task, never an earlier one or a dismissed one.
	var task *gateway.TaskView
	if ok {
		if s, has := c.pilot.Status(d.Info.ID); has {
			st.Agent = agentState(s)
			task = c.taskOf(d.Info.ID, s)
		}
	}
	c.mu.Lock()
	if task != nil {
		c.last = task
	} else if st.Agent != nil {
		task = c.last // ended more than ten minutes ago: keep its steps until dismissed
	}
	c.mu.Unlock()
	if task != nil {
		st.Task = &TaskState{ID: task.ID, Title: task.Title, State: string(task.State), Steps: stepsOf(*task)}
	}
	st.Needs = []Need{}
	for _, n := range c.G.Needs() {
		if ok && n.DeviceID != d.Info.ID {
			continue
		}
		need := Need{ID: n.ID, Kind: string(n.Kind), Title: n.Title, Detail: n.Detail, App: n.App, Rule: n.Rule,
			AllowTask: n.AllowTask}
		need.TitleShown, need.DetailShown, need.RuleShown = needShown(n)
		if !n.Deadline.IsZero() {
			need.DeadlineMS = n.Deadline.UnixMilli()
		}
		st.Needs = append(st.Needs, need)
	}
	st.Notices = c.pendingNotices()
	return st
}

func agentState(s pilot.Status) *AgentState {
	cfg := s.Config.Normalize()
	a := &AgentState{Task: s.Task, Model: s.Config.Model, Provider: cfg.Provider, Service: cfg.Service, KeyName: cfg.KeyName(),
		KeyOptional: cfg.KeyOptional(), Running: s.Running(), End: string(s.End),
		Summary: s.Summary, Steps: s.Steps, Cost: s.Cost, Tokens: s.Tokens,
		Seconds: int64(s.Elapsed(time.Now()).Seconds()), Priced: s.Priced,
		MaxSteps: s.Config.MaxSteps, MaxMinutes: int(s.Config.MaxTime / time.Minute), MaxCost: s.Config.MaxCost,
		TokenLimit: s.Config.TokenLimit(), Continuable: s.Continuable, Continues: s.Continues,
		Suggested: suggestionStates(s.Suggested)}
	for _, m := range s.Pending {
		a.Pending = append(a.Pending, Message{ID: m.ID, Text: m.Text, MS: m.At.UnixMilli()})
	}
	if s.Scheduled != nil {
		a.Scheduled, a.Unattended = true, s.Scheduled.Unattended
	}
	if !s.Running() && a.Summary == "" {
		a.Summary = pilot.Describe(s)
	}
	return a
}
