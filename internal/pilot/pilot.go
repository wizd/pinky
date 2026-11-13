// Package pilot is Ask Pinky: Pinky's own agent, which does one task on
// one phone with a model the person chose. It is for simple tasks, for
// people who use no other agent: one task, one phone. What it carries
// from one task to the next is the person's notes, which they approve and
// can change (memory.go); what it knows of common apps is the guides of
// package guides (knowledge.go).
//
// It works through Pinky's own MCP server, like every agent: an MCP
// session over the SDK's in-memory transports, the phone_* tools with
// their descriptions, rendered screens and diffs, and a gateway session
// behind every call. Rules, approvals, read-only, blocked apps, Take over
// and Hand back, Stop all, recordings and pausing a phone apply as they
// are; nothing here calls a driver. The session's agent is named Pinky
// (mcp.BuiltinName) and needs no New agent approval: the person started
// it from Pinky's window.
package pilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/secret"
)

// Providers: the API a model is reached through.
const (
	// Anthropic is the Messages API: Anthropic's own, or a service that
	// speaks it at an address of the person's.
	Anthropic = "anthropic"
	// OpenAI is an OpenAI-compatible Chat Completions API.
	OpenAI = "openai"
)

// Defaults of a Config.
const (
	DefaultModel     = "claude-opus-5-5"
	DefaultMaxSteps  = 100
	DefaultMaxTime   = 20 * time.Minute
	DefaultMaxCost   = 2.0
	DefaultOpenAIURL = "https://api.openai.com/v1"
)

// Config is how a task runs: the model and the limits.
type Config struct {
	// Service is the service Settings chose (Services, by ID). A known
	// one sets Provider, and BaseURL and Model when they are empty. Empty
	// is the service of Provider and BaseURL (ServiceFor), as settings
	// saved before there were services have it.
	Service  string
	Provider string // Anthropic (default) or OpenAI
	// Model is the model's name; empty is DefaultModel with Anthropic. An
	// OpenAI-compatible API needs one.
	Model string
	// BaseURL is the API's address; empty is the provider's own. For the
	// Messages API it is what ANTHROPIC_BASE_URL is to Claude Code (the
	// path ends before /v1/messages); for an OpenAI-compatible API, the
	// address that /chat/completions is added to.
	BaseURL string
	// The limits; the first one reached ends the task. Steps are the
	// phone tools the model calls. Time leaves out what the task waits
	// for the person (a handoff, an approval). Cost is in US dollars,
	// from the usage the API reports; with a model whose prices Pinky
	// does not know, it is a budget of tokens at US$1 a million.
	MaxSteps int
	MaxTime  time.Duration
	MaxCost  float64
	// NoMemory leaves the person's notes out of the task: it neither reads
	// them nor suggests new ones.
	NoMemory bool
}

// Normalize fills in the defaults.
func (c Config) Normalize() Config {
	c.Service = strings.TrimSpace(c.Service)
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if c.Service == "" {
		c.Service = ServiceFor(c.Provider, c.BaseURL)
	}
	if s, ok := lookup(c.Service); ok {
		c.Provider = s.API
		if c.BaseURL == "" {
			c.BaseURL = s.BaseURL
		}
		if c.Model == "" && len(s.Models) > 0 {
			c.Model = s.Models[0].ID
		}
	}
	if c.Provider != OpenAI {
		c.Provider = Anthropic
	}
	if c.Model == "" && c.Provider == Anthropic {
		c.Model = DefaultModel
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = DefaultMaxSteps
	}
	if c.MaxTime <= 0 {
		c.MaxTime = DefaultMaxTime
	}
	if c.MaxCost <= 0 {
		c.MaxCost = DefaultMaxCost
	}
	return c
}

// KeyName is the name the service's API key is saved under (KeyName),
// which Options.Key is asked for.
func (c Config) KeyName() string { return KeyName(c.Service, c.Provider) }

// KeyOptional reports whether the service may be used without a key: a
// server on the person's own computer.
func (c Config) KeyOptional() bool {
	s, ok := lookup(c.Service)
	return ok && s.KeyOptional
}

// Priced reports whether Pinky knows the model's prices, so that the
// cost limit is in dollars; otherwise it is a budget of tokens.
func (c Config) Priced() bool {
	_, ok := priceOf(c)
	return ok
}

// TokenLimit is the cost limit in tokens, for a model whose prices Pinky
// does not know: US$1 a million.
func (c Config) TokenLimit() int64 { return int64(c.MaxCost * tokensPerDollar) }

// End is how a task ended.
type End string

const (
	Running   End = ""        // it has not ended
	Done      End = "done"    // the model did the task; Summary says what it did
	Failed    End = "failed"  // the model could not do it; Summary says why
	Stopped   End = "stopped" // the person stopped it
	StepLimit End = "steps"   // it reached Config.MaxSteps
	TimeLimit End = "time"    // it reached Config.MaxTime
	CostLimit End = "cost"    // it reached Config.MaxCost
	Refused   End = "refused" // the model declined the task
	NoKey     End = "no_key"  // no API key is saved for the provider
	BadKey    End = "bad_key" // the API did not accept the key; Summary has its words
	Error     End = "error"   // the API or the phone failed; Summary says how
	// Cancelled: the agent that handed Pinky the task (Status.By)
	// cancelled it (Runner.Cancel).
	Cancelled End = "cancelled"
)

// Status is a task of Pinky's own agent, running or ended.
type Status struct {
	Device  string // the phone's device id
	Task    string // the person's words
	Config  Config
	Started time.Time
	Ended   time.Time // zero while it runs
	End     End
	Summary string
	Steps   int
	// Cost is in US dollars, zero when the model's prices are unknown;
	// Tokens counts every token sent and received.
	Cost   float64
	Tokens int64
	// Priced says Cost is known; without it the cost limit is in tokens.
	Priced bool

	// TaskID is the task's id in the gateway, its Activity and history,
	// once Pinky has connected to the phone for it.
	TaskID string
	// Pending are the messages the person sent during the task (Steer)
	// or to continue it (Continue) that the model has not read yet,
	// oldest first: they can still be edited or withdrawn. Pinky gives
	// them to the model with its next request, and they become steps of
	// the task (gateway.PersonStep). A task that ends keeps the ones it
	// did not read, for Continue to send.
	Pending []Message
	// Continues counts the times the person continued the task.
	Continues int
	// Continuable says that the task ended and Continue can carry it on:
	// Pinky reached the phone and the model for it, and keeps the
	// conversation until Dismiss or the next task on the phone.
	Continuable bool
	// Resumed is when the task started or was last continued; the limits
	// count from there. Worked is the time of the stretches before it.
	Resumed time.Time
	Worked  time.Duration

	// ID names the task for the agent that handed it to Pinky
	// (phone_task_*); every task has one.
	ID string
	// By is the agent that handed Pinky the task over MCP (phone_task_run),
	// by the name the person knows it by; empty when the person started
	// it in Pinky's window.
	By string
	// Waiting is set while the task waits for the person: WaitApproval or
	// WaitHandoff.
	Waiting string
	// Waited is the time the task waited for the person, in all its
	// stretches, which its time limit leaves out.
	Waited time.Duration
	// Log is the task's latest steps, the newest last (at most maxLog);
	// Log[i] is step LogFirst+i, counted from 1.
	Log      []Step
	LogFirst int
	// Version counts the changes of the status.
	Version int

	// Scheduled is set for a scheduled task (StartScheduled); Met says
	// whether its condition held, when it has one and the model finished.
	// Continue makes it a task the person watches (run.attendLocked).
	Scheduled *Scheduled
	Met       *bool
	// Suggested are the notes the model suggested keeping, which the
	// person keeps (Runner.Keep) or not (Runner.Drop). They are the whole
	// task's: a continued task keeps those of its earlier stretches, and
	// MaxSuggestions counts them all.
	Suggested []Suggestion
}

// What a task waits for the person to do (Status.Waiting).
const (
	WaitApproval = "approval" // decide on an approval in Pinky
	WaitHandoff  = "handoff"  // do something on the phone and hand it back
)

// Step is a phone tool the model called, and how it went.
type Step struct {
	At     time.Time
	Tool   string // phone_tap, …
	Intent string // why, in the model's words, when it gave one
	Status string // settled, approval_denied, …, or the error's code
	// Note is what the model wrote before it called the tool, if
	// anything, cut to a line or two; for a message (gateway.PersonStep),
	// its words.
	Note string
	// From is, for a message, the agent that sent it; empty for the
	// person's (Message.From).
	From string
}

// maxLog is how many steps Status.Log keeps.
const maxLog = 200

// Running reports whether the task has not ended.
func (s Status) Running() bool { return s.Ended.IsZero() }

// Elapsed is the time the task has run, now, less the time it stood
// ended before it was continued.
func (s Status) Elapsed(now time.Time) time.Duration {
	end := s.Ended
	if end.IsZero() {
		end = now
	}
	from := s.Resumed
	if from.IsZero() {
		from = s.Started
	}
	return s.Worked + max(0, end.Sub(from))
}

// Message is what the person told Pinky about a task: typed while it ran
// (Steer), or to continue it (Continue).
type Message struct {
	ID   string
	Text string
	At   time.Time
	// From is the agent that handed Pinky the task when it sent the
	// message (Runner.SteerFor); empty for the person.
	From string
}

// Errors of Start.
var (
	ErrNoTask  = errors.New("pilot: no task")
	ErrNoModel = errors.New("pilot: no model")
	ErrBusy    = errors.New("pilot: Pinky is already doing a task on this phone")
	ErrClosed  = errors.New("pilot: closed")
	// ErrNotFound: no task has that ID.
	ErrNotFound = errors.New("pilot: no such task")
)

// Errors of Steer, Edit, Withdraw and Continue.
var (
	// ErrEmpty: the message has no words.
	ErrEmpty = errors.New("pilot: empty message")
	// ErrNotRunning: Pinky does no task on the phone; an ended one takes
	// Continue.
	ErrNotRunning = errors.New("pilot: Pinky is doing no task on this phone")
	// ErrRead: the model has read the message, which can no longer change.
	ErrRead = errors.New("pilot: Pinky has already read the message")
	// ErrNoMessage: the task has no such message, or it was withdrawn.
	ErrNoMessage = errors.New("pilot: no such message")
	// ErrCannotContinue: the phone has no task that ended which Pinky can
	// carry on (Status.Continuable).
	ErrCannotContinue = errors.New("pilot: this task cannot be continued")
)

// Options set up a Runner.
type Options struct {
	// Server is Pinky's MCP server, which every call goes through.
	Server *mcp.Server
	// Version is Pinky's version, for the MCP client's name.
	Version string
	// Key returns the API key saved under a name (Config.KeyName),
	// secret.ErrNotFound when there is none. The Runner asks for it as
	// each task starts and keeps it no longer than the task.
	Key func(ctx context.Context, name string) (string, error)
	// Notify is called, from any goroutine, when a task's status changes.
	Notify func()
	// Describe writes the line Pinky gives a task it ends itself (a
	// limit, an error) as the summary in Activity; nil is Describe, in
	// English.
	Describe func(Status) string
	// HTTPClient makes the API requests; nil is http.DefaultClient.
	HTTPClient *http.Client
	// Usage counts the tokens each request used (and checks, Check), by
	// day, service and model; nil counts nothing.
	Usage *Usage
	// Memories are the person's notes, which each task reads and suggests
	// more of; nil keeps no notes.
	Memories *Memories
	// Blocked reports whether the person blocked an app (the gateway's
	// policy.Engine.Blocked): a note about it is refused. nil blocks none.
	Blocked func(screen.App) bool
}

// Runner runs Pinky's own agent, one task a phone at a time.
type Runner struct {
	opt    Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	runs   map[string]*run // by device id, the running and the last ended
	byID   map[string]*run // the tasks agents handed Pinky, by Status.ID (agents.go)
	closed bool
}

// New returns a Runner.
func New(o Options) *Runner {
	if o.Describe == nil {
		o.Describe = Describe
	}
	if o.HTTPClient == nil {
		o.HTTPClient = http.DefaultClient
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{opt: o, ctx: ctx, cancel: cancel, runs: map[string]*run{}, byID: map[string]*run{}}
}

// Start starts a task on a phone. It returns at once; Status follows it.
func (r *Runner) Start(cfg Config, device, task string) error {
	return r.StartWith(cfg, device, task, Extra{})
}

// StartWith starts a task on a phone with what the person gave besides
// its words (Extra), as Start does.
func (r *Runner) StartWith(cfg Config, device, task string, x Extra) error {
	_, err := r.start(cfg, Request{Device: device, Task: task, Extra: x})
	return err
}

// start starts a task, the person's or an agent's.
func (r *Runner) start(cfg Config, req Request) (*run, error) {
	task := strings.TrimSpace(req.Task)
	if task == "" || req.Device == "" {
		return nil, ErrNoTask
	}
	x, err := req.Extra.Check()
	if err != nil {
		return nil, err
	}
	req.Extra = x
	cfg = cfg.Normalize()
	if cfg.Model == "" {
		return nil, ErrNoModel
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, ErrClosed
	}
	if ru := r.runs[req.Device]; ru != nil && ru.status().Running() {
		r.mu.Unlock()
		return nil, ErrBusy
	}
	ctx, cancel := context.WithCancel(r.ctx)
	now := time.Now()
	ru := &run{r: r, cancel: cancel, req: req, read: map[string]bool{}, changed: make(chan struct{}),
		st: Status{Device: req.Device, Task: task, Config: cfg, Started: now, Resumed: now, ID: newID(), By: req.Agent.Name, Scheduled: req.Scheduled}}
	_, ru.st.Priced = priceOf(cfg)
	r.runs[req.Device] = ru
	if ru.st.By != "" {
		r.keepLocked(ru)
	}
	r.launch(ctx, cancel, ru)
	r.mu.Unlock()
	r.notify()
	return ru, nil
}

// launch runs ru's task, or its next stretch, until it ends. The caller
// holds r.mu.
func (r *Runner) launch(ctx context.Context, cancel context.CancelFunc, ru *run) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		end, summary := ru.work(ctx)
		ru.mu.Lock()
		now := time.Now()
		ru.st.End, ru.st.Summary, ru.st.Ended, ru.st.Waiting = end, summary, now, ""
		ru.st.Continuable = ru.m != nil && ru.st.TaskID != ""
		if ru.m != nil {
			// The key stays no longer than the stretch; Continue asks
			// for it again.
			ru.m.setKey("")
		}
		ru.changedLocked()
		ru.mu.Unlock()
		r.notify()
	}()
}

// Steer gives the running task on a phone a message from the person: more
// to do, or a change to the task. The model reads it with its next request
// (after the tool call in flight, or the person's decision Pinky waits
// for), as the person's words and never as the screen's; until then Edit
// and Withdraw change it (Status.Pending). It approves nothing: Pinky
// still asks the person before risky steps.
func (r *Runner) Steer(device, text string) (Message, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Message{}, ErrEmpty
	}
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru == nil {
		return Message{}, ErrNotRunning
	}
	return ru.steer(text, "")
}

// steer gives ru, running, a message (Steer) from the person, or from
// the agent that handed Pinky the task (Message.From).
func (ru *run) steer(text, from string) (Message, error) {
	ru.mu.Lock()
	if !ru.st.Running() {
		ru.mu.Unlock()
		return Message{}, ErrNotRunning
	}
	m := ru.addLocked(text, from)
	ru.mu.Unlock()
	ru.r.notify()
	return m, nil
}

// Edit changes a message the model has not read yet: ErrRead once it has.
// Empty text withdraws it.
func (r *Runner) Edit(device, id, text string) error {
	text = strings.TrimSpace(text)
	return r.pending(device, id, func(p []Message, i int) []Message {
		if text == "" {
			return append(p[:i:i], p[i+1:]...)
		}
		p = slices.Clone(p)
		p[i].Text = text
		return p
	})
}

// Withdraw takes back a message the model has not read yet: ErrRead once
// it has.
func (r *Runner) Withdraw(device, id string) error {
	return r.pending(device, id, func(p []Message, i int) []Message { return append(p[:i:i], p[i+1:]...) })
}

// pending changes the pending message id with f, which returns the new
// list without changing the old one (a Status may hold it).
func (r *Runner) pending(device, id string, f func(p []Message, i int) []Message) error {
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru == nil {
		return ErrNoMessage
	}
	ru.mu.Lock()
	i := slices.IndexFunc(ru.st.Pending, func(m Message) bool { return m.ID == id })
	if i < 0 {
		read := ru.read[id]
		ru.mu.Unlock()
		if read {
			return ErrRead
		}
		return ErrNoMessage
	}
	ru.st.Pending = f(ru.st.Pending, i)
	if len(ru.st.Pending) == 0 {
		ru.st.Pending = nil
	}
	ru.changedLocked()
	ru.mu.Unlock()
	r.notify()
	return nil
}

// Continue carries on the task on a phone that ended (done, stopped, a
// limit, failed) with a new message from the person, as the same task:
// the model goes on from its conversation so far, and the gateway's task
// runs again with its steps, in Activity and the history. text may be
// empty when messages the task did not read wait (Status.Pending); they
// go with it. The limits count anew from here, with the task's own model
// and limits; Status's steps, cost and time keep counting the whole task.
// The API key is asked for again, as at Start.
func (r *Runner) Continue(device, text string) error {
	text = strings.TrimSpace(text)
	r.mu.Lock()
	if err := r.continueLocked(device, text); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	r.notify()
	return nil
}

func (r *Runner) continueLocked(device, text string) error {
	if r.closed {
		return ErrClosed
	}
	ru := r.runs[device]
	if ru == nil {
		return ErrCannotContinue
	}
	return r.continueRunLocked(ru, text, "")
}

// continueRunLocked continues ru, the last task on its phone (Continue),
// with a message from the person or from the agent that handed Pinky the
// task (Message.From). The caller holds r.mu.
func (r *Runner) continueRunLocked(ru *run, text, from string) error {
	ru.mu.Lock()
	switch {
	case ru.st.Running():
		ru.mu.Unlock()
		return ErrBusy
	case !ru.st.Continuable:
		ru.mu.Unlock()
		return ErrCannotContinue
	case text == "" && len(ru.st.Pending) == 0:
		ru.mu.Unlock()
		return ErrEmpty
	}
	if text != "" {
		ru.addLocked(text, from)
	}
	ctx, cancel := context.WithCancel(r.ctx)
	now := time.Now()
	ru.st.Waited += ru.waited
	ru.cancel, ru.stopped, ru.cancelled, ru.waited = cancel, false, false, 0
	ru.base = ru.st
	ru.st.Worked += max(0, ru.st.Ended.Sub(ru.st.Resumed))
	ru.st.Ended, ru.st.End, ru.st.Summary, ru.st.Continuable = time.Time{}, Running, "", false
	ru.st.Continues++
	ru.st.Resumed = now
	ru.attendLocked()
	ru.changedLocked()
	ru.mu.Unlock()
	r.launch(ctx, cancel, ru)
	return nil
}

// Usage returns the usage rows from since on (Usage.Rows): nil when the
// Runner counts none.
func (r *Runner) Usage(since string) []UsageRow { return r.opt.Usage.Rows(since) }

// Stop ends the task on a phone at once, cancelling a model request in
// flight. Stop the gateway's task first (gateway.API.Stop): a task whose
// MCP session just goes away counts as done in Activity.
func (r *Runner) Stop(device string) {
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru != nil {
		ru.stop()
	}
}

// StopAll stops every task, as Stop does.
func (r *Runner) StopAll() {
	r.mu.Lock()
	var rs []*run
	for _, ru := range r.runs {
		rs = append(rs, ru)
	}
	r.mu.Unlock()
	for _, ru := range rs {
		ru.stop()
	}
}

// Memories returns the person's notes, nil when Pinky keeps none.
func (r *Runner) Memories() *Memories { return r.opt.Memories }

// Keep keeps the i-th note the task on a phone suggested.
func (r *Runner) Keep(device string, i int) error {
	return r.decide(device, i, func(s *Suggestion) error {
		m, err := r.opt.Memories.Add(s.Kind, s.Text, s.App)
		if err != nil {
			return err
		}
		s.Kept = &m
		return nil
	})
}

// Drop declines the i-th note the task on a phone suggested.
func (r *Runner) Drop(device string, i int) error {
	return r.decide(device, i, func(s *Suggestion) error {
		s.Dropped = true
		return nil
	})
}

func (r *Runner) decide(device string, i int, f func(*Suggestion) error) error {
	if r.opt.Memories == nil {
		return ErrMemoryGone
	}
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru == nil {
		return ErrMemoryGone
	}
	ru.mu.Lock()
	if i < 0 || i >= len(ru.st.Suggested) || ru.st.Suggested[i].Decided() {
		ru.mu.Unlock()
		return ErrMemoryGone
	}
	// Status hands out copies: change a copy of the list.
	list := append([]Suggestion(nil), ru.st.Suggested...)
	err := f(&list[i])
	if err == nil {
		ru.st.Suggested = list
	}
	ru.mu.Unlock()
	r.notify()
	return err
}

// Status returns the task on a phone: the running one, or the last that
// ended until Dismiss.
func (r *Runner) Status(device string) (Status, bool) {
	r.mu.Lock()
	ru := r.runs[device]
	r.mu.Unlock()
	if ru == nil {
		return Status{}, false
	}
	return ru.status(), true
}

// Dismiss forgets the ended task on a phone.
func (r *Runner) Dismiss(device string) {
	r.mu.Lock()
	if ru := r.runs[device]; ru != nil && !ru.status().Running() {
		delete(r.runs, device)
	}
	r.mu.Unlock()
	r.notify()
}

// Close stops every task and waits for them to end.
func (r *Runner) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.cancel()
	r.wg.Wait()
}

func (r *Runner) notify() {
	if r.opt.Notify != nil {
		r.opt.Notify()
	}
}

// run is one task.
type run struct {
	r   *Runner
	req Request

	mu      sync.Mutex
	cancel  context.CancelFunc // the current stretch's
	st      Status
	stopped bool
	waited  time.Duration // spent waiting for the person, in this stretch
	// base is the status as the stretch started, which its limits count
	// from: all zero for the first.
	base Status
	seq  int             // numbers the person's messages
	read map[string]bool // the messages the model has read, by id
	// said are the words of the messages the model has read: with the
	// task, what a said note may quote.
	said []string

	// The conversation, kept after the task ends for Continue; carry are
	// the answers to the calls of the model's last turn that never ran,
	// for its next request, and carryGuide the guides that were to follow
	// them.
	m          model
	tools      []tool
	carry      []result
	carryGuide string

	// Of the work goroutine alone, which one stretch at a time runs: the
	// app of the last screen, and the guides the task has had, in any of
	// its stretches.
	app    screen.App
	guided map[string]bool

	// cancelled is set when the agent that handed Pinky the task
	// cancelled it: it ends as Cancelled, released as cancelled.
	cancelled bool
	// changed is closed, and replaced, at each change of st (Wait).
	changed chan struct{}
	// note is what the model wrote before its next call (Step.Note).
	note string
	// attended is what the model is told when the person continues a
	// scheduled task that nobody watched (attendLocked).
	attended string
}

func (ru *run) status() Status {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	s := ru.st
	s.Waited += ru.waited
	// Appends to the run's log never show in a copy.
	s.Log = s.Log[:len(s.Log):len(s.Log)]
	return s
}

func (ru *run) stop() {
	ru.mu.Lock()
	ru.stopped = true
	cancel := ru.cancel
	ru.mu.Unlock()
	cancel()
}

// addLocked adds a message from the person to those the model has not
// read. The caller holds ru.mu.
func (ru *run) addLocked(text, from string) Message {
	ru.seq++
	m := Message{ID: fmt.Sprintf("m%d", ru.seq), Text: text, At: time.Now(), From: from}
	ru.st.Pending = append(slices.Clone(ru.st.Pending), m)
	ru.changedLocked()
	return m
}

// takePending takes the messages the model has not read, for the request
// about to go: from now they are read, and steps of the task.
func (ru *run) takePending() []Message {
	ru.mu.Lock()
	msgs := ru.st.Pending
	ru.st.Pending = nil
	for _, m := range msgs {
		ru.read[m.ID] = true
		if m.From == "" {
			// The person's own words, which a said note may quote; an
			// agent's message is not.
			ru.said = append(ru.said, m.Text)
		}
	}
	device := ru.st.Device
	ru.mu.Unlock()
	if len(msgs) == 0 {
		return nil
	}
	for _, m := range msgs {
		if m.From != "" {
			_ = ru.r.opt.Server.AgentSaid(device, m.From, m.Text)
		} else {
			_ = ru.r.opt.Server.PersonSaid(device, m.Text)
		}
	}
	ru.logMessages(msgs)
	return msgs
}

// hasPending reports whether messages wait for the model.
func (ru *run) hasPending() bool {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	return len(ru.st.Pending) > 0
}

func (ru *run) update(f func(s *Status)) {
	ru.mu.Lock()
	f(&ru.st)
	ru.changedLocked()
	ru.mu.Unlock()
	ru.r.notify()
}

// changedLocked counts a change of the status and wakes Wait. The caller
// holds ru.mu.
func (ru *run) changedLocked() {
	ru.st.Version++
	close(ru.changed)
	ru.changed = make(chan struct{})
}

// firstReadRetry is how long a task waits before it reads the first
// screen again, when phone_connect could not.
var firstReadRetry = time.Second

// The tools of the MCP server the model does not get: phone_connect and
// phone_release, which the runner calls itself, and those a simple task
// on a phone it already holds does not need.
var leftOut = map[string]bool{
	"phone_connect": true, "phone_release": true, "phone_app": true, "phone_device": true,
	"phone_files": true, "phone_logs": true,
	// Pinky's own agent hands no task to itself.
	"phone_task_run": true, "phone_task_status": true, "phone_task_wait": true, "phone_task_cancel": true,
	"phone_task_list": true, "phone_task_steer": true,
}

// waits are the tools that wait for the person: their time does not count
// against Config.MaxTime.
var waits = map[string]bool{"phone_handoff": true, "phone_request_approval": true}

// work runs the task, or its next stretch after Continue, to its end.
func (ru *run) work(ctx context.Context) (End, string) {
	ru.mu.Lock()
	cfg, device, task, taskID, m, sch := ru.st.Config, ru.st.Device, ru.st.Task, ru.st.TaskID, ru.m, ru.st.Scheduled
	attended := ru.attended
	ru.attended = ""
	ru.mu.Unlock()
	key, err := ru.r.opt.Key(ctx, cfg.KeyName())
	if cfg.KeyOptional() && (errors.Is(err, secret.ErrNotFound) || err == nil) {
		key, err = strings.TrimSpace(key), nil
	}
	switch {
	case ctx.Err() != nil:
		return ru.stoppedEnd()
	case errors.Is(err, secret.ErrNotFound) || err == nil && strings.TrimSpace(key) == "" && !cfg.KeyOptional():
		return NoKey, ""
	case err != nil:
		return Error, err.Error()
	}
	key = strings.TrimSpace(key)

	// The MCP session lives as long as the stretch, also after a Stop
	// cancelled ctx, so that it can still end the task. A session that
	// continues the task takes the gateway's task back.
	sctx, scancel := context.WithCancel(context.Background())
	defer scancel()
	st, ct := sdk.NewInMemoryTransports()
	connect := func(ctx context.Context, t sdk.Transport) error {
		return ru.r.opt.Server.ConnectBuiltinTask(ctx, t, taskID)
	}
	if ru.req.Agent.Name != "" {
		// An agent's task works as that agent, also when the person
		// continues it: its scope, its approval as a new agent, a Stop
		// that keeps it off the phone.
		connect = func(ctx context.Context, t sdk.Transport) error {
			return ru.r.opt.Server.ConnectFor(ctx, t, ru.req.Agent, taskID)
		}
	}
	if err := connect(sctx, st); err != nil {
		return Error, err.Error()
	}
	cl := sdk.NewClient(&sdk.Implementation{Name: "pinky-agent", Title: mcp.BuiltinName, Version: ru.r.opt.Version}, nil)
	cs, err := cl.Connect(sctx, ct, nil)
	if err != nil {
		return Error, err.Error()
	}
	defer cs.Close()
	defer func() {
		// The agent cancelled it: the task ends as cancelled in Activity
		// rather than as the session's going away.
		ru.mu.Lock()
		cancelled := ru.cancelled
		ru.mu.Unlock()
		if cancelled {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			st := ru.status()
			st.End = Cancelled
			cs.CallTool(ctx, &sdk.CallToolParams{Name: "phone_release", Arguments: map[string]any{
				"outcome": "cancelled", "summary": ru.r.opt.Describe(st)}})
		}
	}()

	tools := ru.tools
	memory := !cfg.NoMemory && ru.r.opt.Memories != nil
	if m == nil {
		list, err := cs.ListTools(ctx, nil)
		if err != nil {
			return ru.ended(ctx, cs, err)
		}
		for _, t := range list.Tools {
			if leftOutFor(sch, t.Name) {
				continue
			}
			s, _ := t.InputSchema.(map[string]any)
			if s == nil {
				b, _ := json.Marshal(t.InputSchema)
				json.Unmarshal(b, &s)
			}
			tools = append(tools, tool{name: t.Name, description: t.Description, schema: s})
		}
		// Whether the task uses the notes is decided as it starts: a
		// continued task goes on with the same tools and system prompt.
		if memory {
			tools = append(tools, rememberTool)
		}
		tools = append(tools, finishFor(sch))
	}

	connectArgs := map[string]any{"device": device, "task": oneLine(task)}
	if len(ru.req.Apps) > 0 {
		connectArgs["apps"] = ru.req.Apps
	}
	if ru.req.ReadOnly {
		connectArgs["read_only"] = true
	}
	if ru.req.Extra.Screen {
		// A question about what the person sees: the phone's own screen,
		// even when tasks run on a background display, whose screen is
		// not theirs.
		connectArgs["own_screen"] = true
	}
	res := ru.call(ctx, cs, "phone_connect", connectArgs)
	if res.status == "internal" && ctx.Err() == nil {
		// The phone is leased; only its first screen failed to read. One
		// failed read (a busy accessibility service, as on the phone
		// itself) need not end the task before the model is asked: read
		// once more.
		select {
		case <-time.After(firstReadRetry):
		case <-ctx.Done():
		}
		if again := ru.call(ctx, cs, "phone_observe", map[string]any{"device": device}); ctx.Err() == nil && !again.isError {
			res = again
		}
	}
	switch {
	case ctx.Err() != nil || res.status == "stopped":
		return ru.stoppedEnd()
	case res.isError:
		return Error, res.message()
	case res.status == "approval_denied":
		// An agent's first connection the person did not allow.
		return Error, res.message()
	}
	if taskID == "" {
		taskID = ru.r.opt.Server.TaskOf(device, ru.req.Agent.Name)
		ru.update(func(s *Status) { s.TaskID = taskID })
	}

	var text string
	if m == nil {
		m, err = newModel(cfg, key, ru.r.opt.HTTPClient, systemPrompt(cfg, memory)+scheduledPrompt(sch), tools)
		if err != nil {
			return ru.end(cs, Error, err.Error())
		}
		ru.mu.Lock()
		ru.m, ru.tools = m, tools
		ru.mu.Unlock()
		// The notes go once, in the task's first message: a continued
		// task has them in its conversation already.
		var kept string
		if memory {
			kept = notes(ru.r.opt.Memories.List())
		}
		text = firstMessage(task, device, res.text, time.Now().In(zone(sch)), kept, ru.guideFor(res.text))
		if extra := scheduledMessage(sch); extra != "" {
			text = strings.Replace(text, "</task>\n\n", "</task>\n\n"+extra, 1)
		}
		text += ru.req.Extra.prompt()
		if ru.req.Agent.Name != "" {
			text += "\n\n" + fromAgent(ru.req.Agent.Name)
			if kept != "" {
				text += "\n\n" + agentNotes
			}
		}
		if a, ok := m.(attacher); ok && len(ru.req.Extra.Images) > 0 {
			a.attach(ru.req.Extra.images())
		}
	} else {
		m.setKey(key)
		// Guides the last stretch had no request left to send, then the
		// guide of the screen now, if the task has not had it.
		ru.mu.Lock()
		carried := ru.carryGuide
		ru.carryGuide = ""
		ru.mu.Unlock()
		text = continueMessage(device, res.text, time.Now().In(zone(sch)), ru.continuedBy())
		if attended != "" {
			text += "\n\n" + attended
		}
		text = joinText(joinText(text, ru.guideFor(res.text)), carried)
	}
	return ru.loop(ctx, cs, m, tools, key, text)
}

// loop asks the model and calls its tools until the task ends. text is the
// first request's message; the person's messages go with each request.
func (ru *run) loop(ctx context.Context, cs *sdk.ClientSession, m model, tools []tool, key, text string) (End, string) {
	cfg := ru.status().Config
	ru.mu.Lock()
	results := ru.carry
	ru.carry = nil
	ru.mu.Unlock()
	// rest are the calls of the model's last turn without an answer: when
	// the task ends before they run, they get one with the next request.
	var rest []call
	// guide are the guides of the screens of the last turn, which go
	// after their results as Pinky's own text: not a tool's result, so
	// they stay when old results are cleared. A guide is marked sent as
	// it is found: one the stretch had no request left for goes with the
	// next stretch's first.
	var guide []string
	defer func() {
		carry := append([]result(nil), results...)
		for _, c := range rest {
			carry = append(carry, notRun(c))
		}
		ru.mu.Lock()
		ru.carry = carry
		ru.carryGuide = strings.Join(guide, "\n\n")
		ru.mu.Unlock()
	}()
	nudged := false
	for {
		if end := ru.limit(); end != Running {
			return ru.end(cs, end, "")
		}
		if msgs := ru.takePending(); len(msgs) > 0 {
			text = joinText(text, personMessages(msgs))
			nudged = false
		}
		// The model request gets only the task's remaining time.
		mctx, mcancel := ru.budget(ctx)
		t, err := m.send(mctx, results, text)
		mcancel()
		results, text = nil, ""
		if err == nil {
			guide = nil
		}
		if t != nil {
			ru.count(t.usage, cfg)
		}
		if end := ru.limit(); ctx.Err() == nil && end == TimeLimit {
			return ru.end(cs, end, "")
		}
		if err != nil {
			return ru.ended(ctx, cs, redact(err, key))
		}
		if t.refused {
			return ru.end(cs, Refused, "")
		}
		ru.mu.Lock()
		ru.note = noteOf(t.text)
		ru.mu.Unlock()
		if len(t.calls) == 0 {
			if ru.hasPending() {
				// The person wrote meanwhile: their words are the answer.
				continue
			}
			if nudged {
				return ru.end(cs, Failed, strings.TrimSpace(t.text))
			}
			// It wrote to the person, who cannot answer: once, ask it to
			// go on or finish.
			nudged = true
			text = nudge
			continue
		}
		for i, c := range t.calls {
			rest = t.calls[i:]
			switch {
			case c.name == finishName:
				return ru.finish(ctx, cs, c)
			case c.bad != "":
				results = append(results, result{id: c.id, text: "error: invalid_argument\n" + c.bad, isError: true})
				continue
			case leftOut[c.name] || !has(tools, c.name):
				results = append(results, result{id: c.id, text: "error: unknown_tool\nThere is no tool " + c.name + ".", isError: true})
				continue
			case c.name == rememberName:
				// Not a step: it changes nothing on the phone.
				r := ru.remember(c)
				results = append(results, result{id: c.id, text: r, isError: strings.HasPrefix(r, "error: ")})
				continue
			}
			if end := ru.limit(); end != Running {
				return ru.end(cs, end, "")
			}
			ru.update(func(s *Status) { s.Steps++ })
			res := ru.call(ctx, cs, c.name, c.args)
			ru.logStep(c, res)
			if ctx.Err() != nil || res.status == "stopped" {
				return ru.stoppedEnd()
			}
			results = append(results, result{id: c.id, text: res.text, images: res.images, isError: res.isError})
			if g := ru.guideFor(res.text); g != "" {
				guide = append(guide, g)
			}
		}
		rest = nil
		text = strings.Join(guide, "\n\n")
	}
}

// notRun is the answer to a call of the model's that never ran because the
// task ended first, sent if the person continues the task.
func notRun(c call) result {
	if c.name == finishName {
		return result{id: c.id, text: "settled\nThe task ended here."}
	}
	return result{id: c.id, text: "error: not_run\nThe task ended before this call finished, so it may not have run. " +
		"Look at the screen before you do anything else.", isError: true}
}

// ended ends the task after err, or as stopped when the person stopped it.
func (ru *run) ended(ctx context.Context, cs *sdk.ClientSession, err error) (End, string) {
	if ctx.Err() != nil {
		return ru.stoppedEnd()
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.status == http.StatusUnauthorized {
		return ru.end(cs, BadKey, ae.message)
	}
	return ru.end(cs, Error, err.Error())
}

// finish ends the task with the model's own outcome and summary.
func (ru *run) finish(ctx context.Context, cs *sdk.ClientSession, c call) (End, string) {
	outcome, _ := c.args["outcome"].(string)
	summary, _ := c.args["summary"].(string)
	summary = strings.TrimSpace(summary)
	end := Failed
	if strings.EqualFold(strings.TrimSpace(outcome), "done") {
		end = Done
	}
	if sch := ru.status().Scheduled; sch != nil && sch.Condition != "" {
		met, _ := c.args[conditionMet].(bool)
		ru.update(func(s *Status) { s.Met = &met })
	}
	res := ru.call(ctx, cs, "phone_release", map[string]any{"outcome": string(end), "summary": summary})
	if ctx.Err() != nil || res.status == "stopped" {
		return ru.stoppedEnd()
	}
	return end, summary
}

// end ends a task Pinky stops itself, with its line in Activity.
func (ru *run) end(cs *sdk.ClientSession, end End, summary string) (End, string) {
	ru.mu.Lock()
	stopped := ru.stopped
	st := ru.st
	ru.mu.Unlock()
	if stopped {
		return ru.stoppedEnd()
	}
	st.End, st.Summary = end, summary
	// A context of its own: the task's may be cancelled by now.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ru.call(ctx, cs, "phone_release", map[string]any{"outcome": "failed", "summary": ru.r.opt.Describe(st)})
	return end, summary
}

// limit reports the limit the task has reached, if any.
func (ru *run) limit() End {
	ru.mu.Lock()
	defer ru.mu.Unlock()
	s, b := ru.st, ru.base
	switch {
	case s.Steps-b.Steps >= s.Config.MaxSteps:
		return StepLimit
	case time.Since(s.Resumed)-ru.waited >= s.Config.MaxTime:
		return TimeLimit
	case s.Priced && s.Cost-b.Cost >= s.Config.MaxCost,
		!s.Priced && s.Tokens-b.Tokens >= s.Config.TokenLimit():
		return CostLimit
	}
	return Running
}

// budget is ctx cut off when the task's time limit is reached.
func (ru *run) budget(ctx context.Context) (context.Context, context.CancelFunc) {
	ru.mu.Lock()
	left := ru.st.Config.MaxTime - (time.Since(ru.st.Resumed) - ru.waited)
	ru.mu.Unlock()
	return context.WithTimeout(ctx, left)
}

// tokensPerDollar is the budget of a model whose prices Pinky does not
// know.
const tokensPerDollar = 1_000_000

func (ru *run) count(u usage, cfg Config) {
	ru.r.opt.Usage.add(time.Now(), cfg, u)
	ru.update(func(s *Status) {
		s.Tokens += u.input + u.output + u.cacheRead + u.cacheWrite
		if p, ok := priceOf(cfg); ok {
			s.Cost += p.at(time.Now()).cost(u)
		}
	})
}

// phoneResult is a tool's result as the model gets it, with its status.
type phoneResult struct {
	text       string
	images     []image
	isError    bool
	status     string // settled, approval_pending, …, or the error's code
	approvalID string
}

// message is what a result says after its status line.
func (p phoneResult) message() string {
	_, rest, _ := strings.Cut(p.text, "\n")
	if rest = strings.TrimSpace(rest); rest != "" {
		line, _, _ := strings.Cut(rest, "\n")
		return line
	}
	return p.status
}

// call calls a tool, waiting on by itself while the person decides on an
// approval or a handoff: the result's approval_id goes back with the same
// call, as the server's instructions tell agents. phone_batch is left to
// the model, whose approval_id goes in the batch's step.
func (ru *run) call(ctx context.Context, cs *sdk.ClientSession, name string, args map[string]any) phoneResult {
	start := time.Now()
	waited := waits[name]
	if waited {
		ru.waiting(waitOf(name))
	}
	defer func() {
		if waited {
			ru.mu.Lock()
			ru.waited += time.Since(start)
			ru.mu.Unlock()
			ru.waiting("")
		}
	}()
	for {
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			return phoneResult{text: "error: internal\n" + err.Error(), isError: true, status: "internal"}
		}
		p := parse(res)
		if p.status != "approval_pending" {
			return p
		}
		if !waited {
			ru.waiting(WaitApproval)
		}
		waited = true
		if p.approvalID == "" || name == "phone_batch" {
			return p
		}
		a := make(map[string]any, len(args)+1)
		for k, v := range args {
			a[k] = v
		}
		a["approval_id"] = p.approvalID
		args = a
	}
}

func parse(res *sdk.CallToolResult) phoneResult {
	var p phoneResult
	var texts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			texts = append(texts, c.Text)
		case *sdk.ImageContent:
			p.images = append(p.images, image{data: c.Data, mime: c.MIMEType})
		}
	}
	p.text = strings.Join(texts, "\n")
	p.isError = res.IsError
	first, _, _ := strings.Cut(p.text, "\n")
	parts := strings.Split(first, " · ")
	p.status = strings.TrimSpace(parts[0])
	if code, ok := strings.CutPrefix(p.status, "error: "); ok {
		p.status, p.isError = code, true
	}
	for _, part := range parts[1:] {
		if id, ok := strings.CutPrefix(part, "approval_id: "); ok {
			p.approvalID = strings.TrimSpace(id)
		}
	}
	return p
}

func has(tools []tool, name string) bool {
	for _, t := range tools {
		if t.name == name {
			return true
		}
	}
	return false
}

// oneLine makes the person's words the task's one-line name.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// redact keeps the API key out of an error, should the API repeat it.
func redact(err error, key string) error {
	if key == "" || !strings.Contains(err.Error(), key) {
		return err
	}
	var ae *apiError
	if errors.As(err, &ae) {
		c := *ae
		c.message = strings.ReplaceAll(c.message, key, "…")
		return &c
	}
	return errors.New(strings.ReplaceAll(err.Error(), key, "…"))
}

// Describe is the line, in English, Pinky gives a task it ends itself.
func Describe(s Status) string {
	switch s.End {
	case StepLimit:
		return fmt.Sprintf("Stopped: reached the limit of %d steps.", s.Config.MaxSteps)
	case TimeLimit:
		return fmt.Sprintf("Stopped: reached the limit of %d minutes.", int(s.Config.MaxTime.Minutes()))
	case CostLimit:
		if s.Priced {
			return fmt.Sprintf("Stopped: reached the limit of US$%.2f.", s.Config.MaxCost)
		}
		return fmt.Sprintf("Stopped: reached the limit of %d tokens.", s.Config.TokenLimit())
	case Refused:
		return "Couldn't finish: the model declined this task."
	case BadKey:
		return "Couldn't start: the API did not accept the key."
	case Failed:
		if s.Summary != "" {
			return "Couldn't finish: " + s.Summary
		}
		return "Couldn't finish."
	case Error:
		return "Couldn't finish: " + s.Summary
	case Cancelled:
		return "Cancelled by " + s.By + "."
	}
	return s.Summary
}
