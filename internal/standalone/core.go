// Package standalone is Pinky's core on the phone it drives, with no
// computer (docs/notes/android-app.md, docs/notes/standalone-phone.md). On
// Android it runs inside the accessibility service of Pinky's app
// (android/companion) and holds what the desktop app holds: the gateway
// with its rules, approvals and steps, the MCP server, and Ask Pinky's
// agent loop (internal/pilot), which drives the phone through the local
// driver (internal/driver/android/local). It knows nothing of Android
// itself, so that Pinky on an iPhone can use it too.
//
// The app's own screens talk to it over a control socket that only the
// app's own uid may open (control.go): they start a task, follow its steps
// and needs, answer approvals and stop it. The agent has no way to reach
// that socket: it only has the MCP tools, and the gateway blocks Pinky's
// own screens (policy.IsPinkyScreen).
package standalone

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/schedule"
	"github.com/DIYgod/pinky/internal/screen"
)

// Options set up a Core.
type Options struct {
	// Providers find the phone: the local driver's provider on the phone,
	// fake phones in tests.
	Providers []driver.Provider
	// Device is the device id tasks run on; empty is the first phone the
	// providers list.
	Device string
	// DataDir holds the history database, the rules and the person's
	// notes for the agent (memory.json); empty keeps history and notes in
	// memory and uses the bundled rules.
	DataDir string
	// KeepDays is how many days the history keeps a task that ended
	// (gateway.SetRetention); 0 keeps every task.
	KeepDays int
	// Preset is when agents ask the person (policy.Preset) on a first run,
	// before there is a rules file; zero keeps the bundled default.
	Preset policy.Preset
	// Version is the prototype's version, for the MCP client's name.
	Version string
	// Gateway tunes the gateway; Providers, DataDir, Policy and Logger
	// are set from the fields above.
	Gateway gateway.Options
	// Pilot tunes the agent; Server, Version, Key, Notify, Blocked and,
	// unless set, Memories are set by the Core.
	Pilot pilot.Options
	// Claims is who drives the phone: the core claims it while a task
	// runs, and starts none while Pinky on a computer holds it. Nil claims
	// nothing.
	Claims Claims
	// Shell is the app's adb connection to its own phone, for background
	// displays (display.go); nil leaves them out.
	Shell Shell
	// BackgroundTouch sends the person's finger on the floating window to
	// the background display the task works on.
	BackgroundTouch func(ctx context.Context, a driver.TouchAction, p screen.Point) error
	Log             *slog.Logger
}

// Core is Pinky on the phone.
type Core struct {
	opt    Options
	log    *slog.Logger
	G      *gateway.Gateway
	MCP    *mcp.Server
	pilot  *pilot.Runner
	mem    *pilot.Memories // the person's notes for the agent (memory.go)
	ctx    context.Context
	cancel context.CancelFunc

	startMu sync.Mutex // a task starts or ends one at a time (Start, ended)

	mu   sync.Mutex
	key  string // the API key of the running task, in memory only
	run  int    // counts the tasks started, so that an earlier one's end leaves a later one alone
	last *gateway.TaskView
	subs map[chan struct{}]struct{}

	runMu sync.Mutex
	runs  []runRecord // how the agent ended each task, oldest first (history.go)

	// The scheduled tasks (schedules.go); notices are those to tell the
	// person of, under mu.
	sched     *schedule.Store
	sc        *schedule.Scheduler
	notices   []Notice
	noticeSeq int64
}

// Errors of the control commands.
var (
	ErrNoDevice = errors.New("standalone: the phone is not ready: turn on Pinky in Accessibility settings")
	ErrNoKey    = errors.New("standalone: type the model's API key")
)

// New starts the gateway and the MCP server.
func New(o Options) (*Core, error) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	var pol *policy.Engine
	if o.DataDir != "" {
		rules := filepath.Join(o.DataDir, "rules.yaml")
		_, statErr := os.Stat(rules)
		p, err := policy.Load(rules, filepath.Join(o.DataDir, "apps.yaml"))
		if err != nil {
			return nil, err
		}
		if errors.Is(statErr, fs.ErrNotExist) && o.Preset != policy.PresetNone {
			if err := p.SetPreset(o.Preset); err != nil {
				return nil, err
			}
		}
		pol = p
	} else {
		pol = policy.Default()
		if o.Preset != policy.PresetNone {
			if err := pol.SetPreset(o.Preset); err != nil {
				return nil, err
			}
		}
	}
	gopt := o.Gateway
	gopt.Providers, gopt.DataDir, gopt.Policy, gopt.Logger = o.Providers, o.DataDir, pol, o.Log
	g, err := gateway.New(gopt)
	if err != nil {
		return nil, err
	}
	if o.KeepDays > 0 {
		g.SetRetention(o.KeepDays)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Core{opt: o, log: o.Log, G: g, MCP: mcp.New(g, o.Version), ctx: ctx, cancel: cancel,
		subs: map[chan struct{}]struct{}{}}
	c.loadRuns()
	if err := c.openSchedules(); err != nil {
		cancel()
		g.Close()
		return nil, err
	}
	popt := o.Pilot
	popt.Server, popt.Version, popt.Notify = c.MCP, o.Version, c.changed
	if popt.Usage == nil {
		path := ""
		if o.DataDir != "" {
			path = filepath.Join(o.DataDir, "model-usage.json")
		}
		popt.Usage = pilot.OpenUsage(path)
	}
	if popt.APIs == nil && o.DataDir != "" {
		popt.APIs = pilot.OpenAPIs(filepath.Join(o.DataDir, "model-apis.json"))
	}
	if popt.Memories == nil {
		path := ""
		if o.DataDir != "" {
			path = filepath.Join(o.DataDir, "memory.json")
		}
		mem, err := pilot.OpenMemories(path)
		if err != nil {
			// A file Pinky cannot read: tasks run without notes rather
			// than not at all, and the file stays for the person.
			o.Log.Error("memory", "err", err)
			mem, _ = pilot.OpenMemories("")
		}
		popt.Memories = mem
	}
	c.mem = popt.Memories
	c.mem.OnChange(c.changed)
	popt.Blocked = func(a screen.App) bool {
		_, blocked := pol.Blocked(a)
		return blocked
	}
	// The key the screens sent with start or continue; empty for a
	// service that needs none (a local server), which the runner allows
	// and reports as NoKey for any other.
	popt.Key = func(context.Context, string) (string, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.key, nil
	}
	c.pilot = pilot.New(popt)
	go c.follow()
	if o.Claims != nil {
		go o.Claims.Watch(ctx, c.changed, c.claimLost)
	}
	if o.Shell != nil {
		o.Shell.Watch(c.changed)
	}
	return c, nil
}

// Close stops the task and the gateway.
func (c *Core) Close() {
	c.cancel()
	c.sc.Close()
	c.pilot.Close()
	c.G.Close()
}

// follow turns the gateway's events into state changes for subscribers.
func (c *Core) follow() {
	for range c.G.Subscribe(c.ctx) {
		c.changed()
	}
}

// changed wakes every subscriber; each reads the state when it is ready,
// so a burst of changes sends one state.
func (c *Core) changed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (c *Core) subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

// device returns the phone tasks run on.
func (c *Core) device() (gateway.DeviceView, bool) {
	for _, d := range c.G.Devices() {
		if c.opt.Device == "" || d.Info.ID == c.opt.Device {
			return d, true
		}
	}
	return gateway.DeviceView{}, false
}

// StartRequest starts a task.
type StartRequest struct {
	Task string `json:"task"`
	// Service is the service Settings chose (pilot.Services); empty is
	// the one of Provider and BaseURL, as the apps sent before.
	Service  string  `json:"service"`
	Provider string  `json:"provider"`
	Model    string  `json:"model"`
	BaseURL  string  `json:"base_url"`
	Key      string  `json:"key"` // kept in memory until the task ends
	MaxSteps int     `json:"max_steps"`
	MaxMin   int     `json:"max_minutes"`
	MaxCost  float64 `json:"max_cost"`
	// What the person gave with the task besides its words (pilot.Extra):
	// a question about the screen the phone shows as it starts (Pinky's
	// floating entry), and what they shared into Pinky from another app.
	Screen bool         `json:"screen,omitempty"`
	Shared string       `json:"shared,omitempty"`
	Images []StartImage `json:"images,omitempty"`
	// NoMemory leaves the person's notes out of the task (pilot.Config).
	NoMemory bool `json:"no_memory"`
}

// StartImage is a picture the person shared with a task: "image/jpeg",
// "image/png", "image/gif" or "image/webp", and its bytes (base64 in
// JSON).
type StartImage struct {
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

func (r StartRequest) extra() pilot.Extra {
	x := pilot.Extra{Screen: r.Screen, Shared: r.Shared}
	for _, im := range r.Images {
		x.Images = append(x.Images, pilot.Image{Data: im.Data, MIME: im.MIME})
	}
	return x
}

// Config is the task's pilot.Config.
func (r StartRequest) Config() pilot.Config {
	return pilot.Config{Service: r.Service, Provider: r.Provider, Model: r.Model, BaseURL: r.BaseURL, MaxSteps: r.MaxSteps,
		MaxTime: time.Duration(r.MaxMin) * time.Minute, MaxCost: r.MaxCost, NoMemory: r.NoMemory}
}

// Start starts the person's task on the phone. The task claims the phone
// first, so Start fails with ErrComputer while Pinky on a computer uses it;
// the checks that need no phone come before, so that a mistake in the form
// claims nothing.
func (c *Core) Start(r StartRequest) error { return c.start(r, nil) }

// start starts a task the person starts, or a scheduled one.
func (c *Core) start(r StartRequest, sched *pilot.Scheduled) error {
	d, ok := c.device()
	if !ok || d.Info.State != driver.StateOnline {
		return ErrNoDevice
	}
	cfg := r.Config()
	key := strings.TrimSpace(r.Key)
	if key == "" && !cfg.KeyOptional() {
		return ErrNoKey
	}
	task := strings.TrimSpace(r.Task)
	if task == "" {
		return pilot.ErrNoTask
	}
	if cfg.Normalize().Model == "" {
		return pilot.ErrNoModel
	}
	if _, err := r.extra().Check(); err != nil {
		return err
	}
	c.startMu.Lock()
	defer c.startMu.Unlock()
	// The running task holds the claim: claiming again would rename it,
	// and the failed start would give it back.
	if s, ok := c.pilot.Status(d.Info.ID); ok && s.Running() {
		return pilot.ErrBusy
	}
	if cl := c.opt.Claims; cl != nil {
		ctx, cancel := context.WithTimeout(c.ctx, claimWait)
		busy, err := cl.Claim(ctx, task)
		cancel()
		if err != nil {
			return err
		}
		if busy {
			return ErrComputer
		}
	}
	c.mu.Lock()
	c.key, c.last = key, nil
	c.run++
	run := c.run
	c.mu.Unlock()
	var err error
	if sched != nil {
		err = c.pilot.StartScheduled(cfg, d.Info.ID, task, *sched)
	} else {
		err = c.pilot.StartWith(cfg, d.Info.ID, task, r.extra())
	}
	if err != nil {
		c.endedLocked(run)
		return err
	}
	go c.endTask(d.Info.ID, run)
	return nil
}

// Steer sends the person's message to the running task; the model reads
// it at its next step (pilot.Runner.Steer).
func (c *Core) Steer(text string) (pilot.Message, error) {
	d, ok := c.device()
	if !ok {
		return pilot.Message{}, ErrNoDevice
	}
	return c.pilot.Steer(d.Info.ID, text)
}

// EditMessage changes a message the model has not read; empty text
// withdraws it (pilot.Runner.Edit).
func (c *Core) EditMessage(id, text string) error {
	d, ok := c.device()
	if !ok {
		return ErrNoDevice
	}
	return c.pilot.Edit(d.Info.ID, id, text)
}

// Withdraw takes back a message the model has not read.
func (c *Core) Withdraw(id string) error {
	d, ok := c.device()
	if !ok {
		return ErrNoDevice
	}
	return c.pilot.Withdraw(d.Info.ID, id)
}

// Continue goes on with the task that ended, with the person's message
// (pilot.Runner.Continue): the same task in the history, its limits
// counted anew. Like Start, it needs the key again and claims the phone.
func (c *Core) Continue(text, key string) error {
	d, ok := c.device()
	if !ok || d.Info.State != driver.StateOnline {
		return ErrNoDevice
	}
	key = strings.TrimSpace(key)
	c.startMu.Lock()
	defer c.startMu.Unlock()
	s, has := c.pilot.Status(d.Info.ID)
	switch {
	case key == "" && !(has && s.Config.KeyOptional()):
		// The task's own service, whatever the settings say now
		// (AgentState.KeyName).
		return ErrNoKey
	case has && s.Running():
		return pilot.ErrBusy
	case !has || !s.Continuable:
		return pilot.ErrCannotContinue
	case strings.TrimSpace(text) == "" && len(s.Pending) == 0:
		return pilot.ErrEmpty
	}
	if cl := c.opt.Claims; cl != nil {
		ctx, cancel := context.WithTimeout(c.ctx, claimWait)
		busy, err := cl.Claim(ctx, s.Task)
		cancel()
		if err != nil {
			return err
		}
		if busy {
			return ErrComputer
		}
	}
	c.mu.Lock()
	c.key = key
	c.run++
	run := c.run
	c.mu.Unlock()
	if err := c.pilot.Continue(d.Info.ID, text); err != nil {
		c.endedLocked(run)
		return err
	}
	go c.endTask(d.Info.ID, run)
	return nil
}

// endTask waits for run's task to end, keeps how it ended for the history,
// then ends it.
func (c *Core) endTask(device string, run int) {
	defer c.ended(run)
	ch, done := c.subscribe()
	defer done()
	for {
		s, ok := c.pilot.Status(device)
		if !ok {
			return
		}
		if !s.Running() {
			c.record(device, s)
			return
		}
		select {
		case <-ch:
		case <-time.After(time.Second):
		case <-c.ctx.Done():
			return
		}
	}
}

// ended drops the key of run's task and gives the phone back, unless
// another task started since.
func (c *Core) ended(run int) {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	c.endedLocked(run)
}

func (c *Core) endedLocked(run int) {
	c.mu.Lock()
	current := run == c.run
	if current {
		c.key = ""
	}
	c.mu.Unlock()
	if current && c.opt.Claims != nil {
		// Not c.ctx: the claim must not outlive the task when the core
		// closes either.
		ctx, cancel := context.WithTimeout(context.Background(), claimWait)
		defer cancel()
		c.opt.Claims.Release(ctx)
	}
}

// Stop stops the task: the gateway's first, so Activity says the person
// stopped it, then the agent's.
func (c *Core) Stop() {
	if d, ok := c.device(); ok {
		_ = c.G.Stop(d.Info.ID)
		c.pilot.Stop(d.Info.ID)
	} else {
		_ = c.G.StopAll()
		c.pilot.StopAll()
	}
}

// Decide answers a need: an approval, or a handoff (allow_once hands
// back, deny declines).
func (c *Core) Decide(need string, d gateway.Decision, note string) error {
	return c.G.Decide(need, d, note)
}

// SetPreset changes when agents ask the person, as Settings › Safety does
// on the computer.
func (c *Core) SetPreset(p policy.Preset) error {
	if err := c.G.Policy().SetPreset(p); err != nil {
		return err
	}
	c.changed()
	return nil
}

// Check checks a model before a task uses it (pilot.Check), with the key
// the screens send; the key is not kept. It blocks for up to a minute.
func (c *Core) Check(r StartRequest) pilot.CheckResult {
	ctx, cancel := context.WithTimeout(c.ctx, time.Minute)
	defer cancel()
	return c.pilot.CheckWith(ctx, r.Config(), r.Key)
}

// Usage is what Pinky's agent used on this phone, by day, service and
// model, from since on (pilot.Usage.Rows).
func (c *Core) Usage(since string) []pilot.UsageRow { return c.pilot.Usage(since) }

// Dismiss forgets the ended task.
func (c *Core) Dismiss() {
	if d, ok := c.device(); ok {
		c.pilot.Dismiss(d.Info.ID)
	}
	c.mu.Lock()
	c.last = nil
	c.mu.Unlock()
	c.changed()
}
