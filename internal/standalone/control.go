package standalone

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/schedule"
	"github.com/DIYgod/pinky/internal/screen"
)

// The control socket is how Pinky's own screens reach the core: line
// JSON, one request a line, one reply a line. Only the prototype's own
// uid may connect (peerAllowed): the agent runs in the core and reaches the
// phone through MCP tools only, so it can never send these commands.
//
//	{"cmd":"state"}                                     → {"ok":true,"state":{...}}
//	{"cmd":"subscribe"}                                 → the state now and after each change, one a line
//	{"cmd":"start","task":"...","key":"...",...}        → {"ok":true}  (StartRequest)
//	{"cmd":"decide","need":"...","decision":"allow_once"} → {"ok":true}
//	{"cmd":"preset","preset":1}                         → {"ok":true}  (policy.Preset)
//	{"cmd":"stop"} / {"cmd":"dismiss"}                  → {"ok":true}
//	{"cmd":"stop","computer":true}                      → {"ok":true}  (StopComputer)
//	{"cmd":"history","before":<ms>,"limit":20}          → {"ok":true,"history":[...],"more":true}
//	                                                      (History: PastTask, newest first; both fields optional)
//	{"cmd":"history","id":"..."}                        → {"ok":true,"history":[the task, with its steps]}
//	{"cmd":"check","service":"...","key":"...",...}     → {"ok":true,"check":{...}}  (StartRequest; pilot.CheckResult)
//	{"cmd":"usage","since":"2006-01-02"}                → {"ok":true,"usage":[...]}  (pilot.UsageRow, newest first)
//	{"cmd":"steer","text":"..."}                        → {"ok":true,"id":"m1"}  (Steer: to the running task)
//	{"cmd":"steer","id":"m1","text":"..."}              → {"ok":true}  (EditMessage; empty text withdraws)
//	{"cmd":"withdraw","id":"m1"}                        → {"ok":true}  (Withdraw)
//	{"cmd":"continue","text":"...","key":"..."}         → {"ok":true}  (Continue the task that ended)
//	{"cmd":"memories"}                                  → {"ok":true,"memories":[...]}  (MemoryState, newest first)
//	{"cmd":"memory_add","text":"..."}                   → {"ok":true,"memories":[...]}
//	{"cmd":"memory_edit","id":"...","text":"..."}       → {"ok":true,"memories":[...]}
//	{"cmd":"memory_delete","id":"..."}                  → {"ok":true,"memories":[...]}
//	{"cmd":"memory_delete_all"}                         → {"ok":true,"memories":[]}
//	{"cmd":"keep","index":0} / {"cmd":"drop","index":0} → {"ok":true}  (AgentState.Suggested)
//	{"cmd":"schedule_…"} / {"cmd":"notice_seen"}        → the scheduled tasks (schedules.go)
//
// A message waits in the state's agent.pending until the model reads it;
// steer with its id or withdraw then fail with the code read. Once a task
// ended, agent.continuable says continue may go on with it; text may be
// empty when messages wait.
//
// display.go has the commands of background displays (shell, video,
// takeover, handback, touch).
//
// Any command may name the language Pinky's screens show, "locale":
// "zh-Hans", "ja" or "en"; the state and the history then say the
// gateway's words in it too (shown.go).
//
// A failed command replies {"ok":false,"error":"...","code":"..."}: the
// code, when there is one, names an error the screens say in the person's
// language (the Code constants); the error is for logs.

type request struct {
	Cmd      string `json:"cmd"`
	Locale   string `json:"locale"` // the screens' language: setLocale
	Need     string `json:"need"`
	Decision string `json:"decision"`
	Note     string `json:"note"`
	Preset   int    `json:"preset"`
	Computer bool   `json:"computer"` // stop: Pinky on a computer, not the task
	// history: one task, or the tasks that started before Before (Unix
	// milliseconds), Limit of them.
	ID     string `json:"id"`
	Before int64  `json:"before"`
	Limit  int    `json:"limit"`
	// steer and continue: the person's message to the agent; the
	// notes' commands: a note's words. keep and drop: which suggestion
	// they answer.
	Text  string `json:"text"`
	Index int    `json:"index"`
	// shell (display.go): what to do, Wireless debugging's port and the
	// pairing code.
	Op   string `json:"op"`
	Port int    `json:"port"`
	Code string `json:"code"`
	// touch: the phase and the point in the video's pixels.
	Action string `json:"action"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	// usage: the first day, 2006-01-02; empty for all.
	Since string `json:"since"`
	StartRequest
	// The schedules' commands (schedules.go): a schedule to save or
	// preview, whether to pause the one named by ID, the phone's time
	// zone, what the app knows of the phone, and a notice shown.
	Schedule *schedule.Schedule `json:"schedule"`
	Paused   bool               `json:"paused"`
	Zone     string             `json:"zone"`
	Notice   int64              `json:"notice"`
	PhoneNow
}

type reply struct {
	OK      bool       `json:"ok"`
	Error   string     `json:"error,omitempty"`
	Code    string     `json:"code,omitempty"`
	State   *State     `json:"state,omitempty"`
	History []PastTask `json:"history,omitempty"`
	More    bool       `json:"more,omitempty"`  // older tasks follow
	ID      string     `json:"id,omitempty"`    // steer: the new message's
	Video   *videoInfo `json:"video,omitempty"` // video: the stream that follows
	// Check is how a model answered the check command.
	Check *pilot.CheckResult `json:"check,omitempty"`
	// Usage answers the usage command; an empty list is [].
	Usage *[]pilot.UsageRow `json:"usage,omitempty"`
	// The schedules' commands (schedules.go).
	Schedules []schedule.Schedule `json:"schedules,omitempty"`
	NextMS    int64               `json:"next_ms,omitempty"`
	Upcoming  []int64             `json:"upcoming,omitempty"`
	// Memories answers the notes' commands, empty or not.
	Memories *[]MemoryState `json:"memories,omitempty"`
}

// maxLine bounds a command: pilot.MaxImages pictures in base64, and room
// for the rest.
const maxLine = pilot.MaxImages*pilot.MaxImageBytes*4/3 + 1<<20

// historyWait bounds a history command, which reads the database.
const historyWait = 10 * time.Second

// The codes of errors the screens know.
const (
	CodeNoDevice = "no_device" // ErrNoDevice: the service is off
	CodeNoKey    = "no_key"    // ErrNoKey
	CodeNoModel  = "no_model"  // an OpenAI-compatible API without a model
	CodeNoTask   = "no_task"   // an empty task
	CodeBusy     = "busy"      // a task already runs
	CodeComputer = "computer"  // ErrComputer: Pinky on a computer uses the phone
	CodeGone     = "gone"      // ErrGone: the task is no longer in the history
	// The person's messages to the agent (steer, withdraw, continue).
	CodeEmpty          = "empty"           // no message
	CodeNotRunning     = "not_running"     // no task runs to send it to
	CodeRead           = "read"            // the model read it already: it can no longer change
	CodeNoMessage      = "no_message"      // no such message: withdrawn already
	CodeCannotContinue = "cannot_continue" // no task that ended can go on
	CodeShared         = "shared"          // pilot.ErrExtra: what was shared is too large or not a picture
)

// Code is the code of a control command's error, or "" for one the
// screens show as it is: for cores that answer the commands themselves
// (ios/onphone).
func Code(err error) string { return codeOf(err) }

// codeOf is the code of a command's error, or "" for one the screens show
// as it is.
func codeOf(err error) string {
	switch {
	case errors.Is(err, ErrNoDevice):
		return CodeNoDevice
	case errors.Is(err, ErrNoKey):
		return CodeNoKey
	case errors.Is(err, pilot.ErrNoModel):
		return CodeNoModel
	case errors.Is(err, pilot.ErrNoTask):
		return CodeNoTask
	case errors.Is(err, pilot.ErrBusy):
		return CodeBusy
	case errors.Is(err, ErrComputer):
		return CodeComputer
	case errors.Is(err, pilot.ErrExtra):
		return CodeShared
	case errors.Is(err, ErrGone):
		return CodeGone
	case errors.Is(err, pilot.ErrEmpty):
		return CodeEmpty
	case errors.Is(err, pilot.ErrNotRunning):
		return CodeNotRunning
	case errors.Is(err, pilot.ErrRead):
		return CodeRead
	case errors.Is(err, pilot.ErrNoMessage):
		return CodeNoMessage
	case errors.Is(err, pilot.ErrCannotContinue):
		return CodeCannotContinue
	case errors.Is(err, ErrNoShell):
		return CodeNoShell
	case errors.Is(err, ErrWrongCode):
		return CodeWrongCode
	}
	return MemoryCode(err)
}

// Serve answers the control socket until ctx ends. On the phone, ln
// listens at an abstract address (net.Listen("unix", "@name")).
func (c *Core) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if err := peerAllowed(conn); err != nil {
			c.log.Warn("control: refused a connection", "err", err)
			conn.Close()
			continue
		}
		go c.serveConn(ctx, conn)
	}
}

func (c *Core) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	in := bufio.NewScanner(conn)
	// A start may carry pictures the person shared (StartRequest.Images).
	in.Buffer(make([]byte, 64<<10), maxLine)
	enc := json.NewEncoder(conn)
	for in.Scan() {
		var r request
		if err := json.Unmarshal(in.Bytes(), &r); err != nil {
			_ = enc.Encode(reply{Error: "bad request: " + err.Error()})
			continue
		}
		setLocale(r.Locale)
		switch r.Cmd {
		case "subscribe":
			c.stream(ctx, enc)
			return
		case "video":
			c.video(ctx, conn, enc)
			return
		}
		rep := c.handle(r)
		if err := enc.Encode(rep); err != nil {
			return
		}
	}
}

func (c *Core) handle(r request) reply {
	var err error
	switch r.Cmd {
	case "state":
		st := c.State()
		return reply{OK: true, State: &st}
	case "start":
		err = c.Start(r.StartRequest)
	case "decide":
		err = c.Decide(r.Need, gateway.Decision(r.Decision), r.Note)
	case "preset":
		err = c.SetPreset(policy.Preset(r.Preset))
	case "stop":
		if r.Computer {
			err = c.StopComputer()
		} else {
			c.Stop()
		}
	case "dismiss":
		c.Dismiss()
	case "history":
		return c.history(r)
	case "steer":
		if r.ID != "" {
			err = c.EditMessage(r.ID, r.Text)
			break
		}
		m, err := c.Steer(r.Text)
		if err != nil {
			return reply{Error: err.Error(), Code: codeOf(err)}
		}
		return reply{OK: true, ID: m.ID}
	case "withdraw":
		err = c.Withdraw(r.ID)
	case "continue":
		err = c.Continue(r.Text, r.Key)
	case "shell":
		err = c.shell(r)
	case "takeover":
		err = c.TakeOver()
	case "handback":
		err = c.HandBack(r.Note)
	case "touch":
		err = c.Touch(driver.TouchAction(r.Action), screen.Point{X: r.X, Y: r.Y})
	case "check":
		res := c.Check(r.StartRequest)
		return reply{OK: true, Check: &res}
	case "usage":
		rows := c.Usage(r.Since)
		if rows == nil {
			rows = []pilot.UsageRow{}
		}
		return reply{OK: true, Usage: &rows}
	case "schedules", "schedule_save", "schedule_preview", "schedule_delete", "schedule_pause", "schedule_run",
		"schedule_due", "notice_seen":
		return c.schedule(r)
	case "memories", "memory_add", "memory_edit", "memory_delete", "memory_delete_all":
		return c.memoryCommand(r)
	case "keep":
		err = c.Keep(r.Index)
	case "drop":
		err = c.Drop(r.Index)
	default:
		err = fmt.Errorf("unknown command %q", r.Cmd)
	}
	if err != nil {
		return reply{Error: err.Error(), Code: codeOf(err)}
	}
	return reply{OK: true}
}

// memoryCommand changes the notes as r says and replies with them.
func (c *Core) memoryCommand(r request) reply {
	var err error
	switch r.Cmd {
	case "memory_add":
		err = c.AddMemory(r.Text)
	case "memory_edit":
		err = c.EditMemory(r.ID, r.Text)
	case "memory_delete":
		err = c.DeleteMemory(r.ID)
	case "memory_delete_all":
		err = c.DeleteAllMemories()
	}
	if err != nil {
		return reply{Error: err.Error(), Code: codeOf(err)}
	}
	list := c.Memories()
	return reply{OK: true, Memories: &list}
}

func (c *Core) history(r request) reply {
	ctx, cancel := context.WithTimeout(c.ctx, historyWait)
	defer cancel()
	if r.ID != "" {
		t, err := c.PastTask(ctx, r.ID)
		if err != nil {
			return reply{Error: err.Error(), Code: codeOf(err)}
		}
		return reply{OK: true, History: []PastTask{t}}
	}
	var before time.Time
	if r.Before > 0 {
		before = time.UnixMilli(r.Before)
	}
	list, more, err := c.History(ctx, before, r.Limit)
	if err != nil {
		return reply{Error: err.Error(), Code: codeOf(err)}
	}
	return reply{OK: true, History: list, More: more}
}

// stream sends the state now and after each change, at most every 100 ms,
// until the reader goes away.
func (c *Core) stream(ctx context.Context, enc *json.Encoder) {
	ch, done := c.subscribe()
	defer done()
	for {
		st := c.State()
		if err := enc.Encode(reply{OK: true, State: &st}); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ch:
		case <-time.After(time.Second): // deadlines and elapsed time move on their own
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}
