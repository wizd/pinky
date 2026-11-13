package standalone

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/schedule"
)

// Scheduled tasks on the phone (docs/notes/schedules.md). The core keeps
// the schedules and decides what runs, but has no clock of its own that
// survives Android's Doze: Pinky's app sets an exact alarm for the next
// time due (next_ms), and when it fires sends schedule_due with the model's
// key and settings, the phone's time zone, and whether the phone is locked
// or in the person's hands. The core never keeps the key once the run
// ends, as for a task the person starts.
//
//	{"cmd":"schedules"}                               → {"ok":true,"schedules":[...],"next_ms":...}
//	{"cmd":"schedule_save","schedule":{...}}          → {"ok":true,"schedules":[the saved one],"next_ms":...}
//	{"cmd":"schedule_preview","schedule":{...}}       → {"ok":true,"upcoming":[ms, ms, ms]}
//	{"cmd":"schedule_delete","id":"..."}              → {"ok":true,"next_ms":...}
//	{"cmd":"schedule_pause","id":"...","paused":true} → {"ok":true,"next_ms":...}
//	{"cmd":"schedule_run","id":"...","key":"...",...} → {"ok":true}  (Run now; StartRequest without task)
//	{"cmd":"schedule_due","key":"...","locked":false,"in_use":false,...}
//	                                                  → {"ok":true,"next_ms":...}
//	{"cmd":"notice_seen","notice":3}                  → {"ok":true}
//
// Every schedule command may carry "zone", the phone's IANA time zone: Go
// on Android has no local zone of its own. next_ms is 0 when nothing is due
// again. The schedules' errors have codes (schedCode).
//
// What a run nobody watched came to waits in State.Notices until the app
// has told the person (notice_seen).

// Notice is a scheduled run the person is to hear of.
type Notice struct {
	ID        int64  `json:"id"`
	Schedule  string `json:"schedule"`
	Task      string `json:"task"`
	Condition string `json:"condition,omitempty"`
	// End is a pilot.End, or "skipped" with Reason one of schedule's Skip
	// constants (Missed earlier times passed over with it).
	End     string `json:"end"`
	Reason  string `json:"reason,omitempty"`
	Missed  int    `json:"missed,omitempty"`
	Summary string `json:"summary,omitempty"`
	Met     *bool  `json:"met,omitempty"`
	DueMS   int64  `json:"due_ms"`
}

// schedNow is the clock of the schedules; tests move it.
var schedNow = time.Now

// maxNotices bounds the notices the app has not taken.
const maxNotices = 20

// The codes of the schedules' errors.
const (
	CodeSchedNoTask   = "sched_no_task"
	CodeSchedLong     = "sched_long"
	CodeSchedNoTime   = "sched_no_time"
	CodeSchedTime     = "sched_time"
	CodeSchedTooMany  = "sched_too_many"
	CodeSchedDate     = "sched_date"
	CodeSchedPast     = "sched_past"
	CodeSchedNoDays   = "sched_no_days"
	CodeSchedZone     = "sched_zone"
	CodeSchedNotFound = "sched_not_found"
	CodeSchedRunning  = "sched_running"
	CodeSchedOther    = "sched_invalid" // a repeat, interval or cooldown the screens do not offer
)

func schedCode(err error) string {
	switch {
	case errors.Is(err, schedule.ErrNoTask):
		return CodeSchedNoTask
	case errors.Is(err, schedule.ErrLongTask):
		return CodeSchedLong
	case errors.Is(err, schedule.ErrNoTime):
		return CodeSchedNoTime
	case errors.Is(err, schedule.ErrTime):
		return CodeSchedTime
	case errors.Is(err, schedule.ErrTooMany):
		return CodeSchedTooMany
	case errors.Is(err, schedule.ErrDate):
		return CodeSchedDate
	case errors.Is(err, schedule.ErrPast):
		return CodeSchedPast
	case errors.Is(err, schedule.ErrNoDays):
		return CodeSchedNoDays
	case errors.Is(err, schedule.ErrZone):
		return CodeSchedZone
	case errors.Is(err, schedule.ErrNotFound):
		return CodeSchedNotFound
	case errors.Is(err, schedule.ErrRunning):
		return CodeSchedRunning
	case errors.Is(err, schedule.ErrRepeat), errors.Is(err, schedule.ErrInterval), errors.Is(err, schedule.ErrCooldown):
		return CodeSchedOther
	}
	return ""
}

// openSchedules opens the schedules, in DataDir or in memory.
func (c *Core) openSchedules() error {
	path := ""
	if c.opt.DataDir != "" {
		path = filepath.Join(c.opt.DataDir, "schedules.json")
	}
	st, err := schedule.Open(path)
	if err != nil {
		return err
	}
	c.sched = st
	c.sc = schedule.New(st)
	st.OnChange(c.changed)
	return nil
}

// setZone takes the phone's time zone for the schedules.
func (c *Core) setZone(name string) {
	if name == "" {
		return
	}
	if loc, err := time.LoadLocation(name); err == nil {
		c.sched.SetLocal(loc)
	} else {
		c.log.Warn("schedules: unknown time zone", "zone", name, "err", err)
	}
}

// nextMS is when the app's alarm should next wake the core: Unix
// milliseconds, 0 for never.
func nextMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// PhoneNow is what only the app knows of the phone when a schedule is due.
type PhoneNow struct {
	// Locked: the keyguard shows or the screen is off. Pinky never unlocks
	// the phone for a scheduled task; it runs one on a background display
	// that is set up (backgroundReady), or skips the time.
	Locked bool `json:"locked"`
	// InUse: the screen is on and the person touched it a moment ago.
	InUse bool `json:"in_use"`
	// Off: the person turned Pinky on the phone off in its screen, so
	// nothing would show the run or its approvals.
	Off bool `json:"off"`
}

// schedHost runs the schedules on this phone, with the model settings and
// key of the alarm that woke it.
type schedHost struct {
	c     *Core
	req   StartRequest
	phone PhoneNow
}

func (h schedHost) Check(s schedule.Schedule) string {
	c := h.c
	d, ok := c.device()
	switch {
	case h.phone.Off:
		return schedule.SkipOff
	case !ok || d.Info.State != "online":
		return schedule.SkipOffline
	case (h.phone.Locked || d.Status == gateway.DeviceLocked) && !c.backgroundReady(d.Info.ID):
		return schedule.SkipLocked
	case h.phone.InUse:
		return schedule.SkipInUse
	}
	if cl := c.opt.Claims; cl != nil {
		if _, _, held := cl.Computer(); held {
			return schedule.SkipBusy
		}
	}
	if st, ok := c.pilot.Status(d.Info.ID); ok && st.Running() {
		return schedule.SkipBusy
	}
	return ""
}

func (h schedHost) Run(ctx context.Context, s schedule.Schedule, r schedule.Run) schedule.Run {
	c := h.c
	loc, _ := s.Location(c.sched.Local())
	ps := pilot.Scheduled{Unattended: !r.Manual, Condition: s.Condition, Zone: loc}
	if l := s.LastDone(); l != nil {
		ps.Last = &pilot.Earlier{At: l.Due, Summary: l.Summary}
	}
	if m := s.LastMet; m != nil {
		ps.LastMet = &pilot.Earlier{At: m.Due, Summary: m.Summary}
	}
	req := h.req
	req.Task = s.Task
	if err := c.start(req, &ps); err != nil {
		switch {
		case errors.Is(err, pilot.ErrBusy), errors.Is(err, ErrComputer):
			r.End, r.Reason = schedule.Skipped, schedule.SkipBusy
		case errors.Is(err, ErrNoDevice):
			r.End, r.Reason = schedule.Skipped, schedule.SkipOffline
		case errors.Is(err, ErrNoKey), errors.Is(err, pilot.ErrNoModel):
			r.End, r.Reason = schedule.Skipped, schedule.SkipNoKey
		default:
			r.End, r.Summary = string(pilot.Error), err.Error()
		}
		return r
	}
	d, _ := c.device()
	ch, done := c.subscribe()
	defer done()
	denied := map[string]bool{}
	for {
		st, ok := c.pilot.Status(d.Info.ID)
		if !ok {
			r.End = string(pilot.Error)
			return r
		}
		if !st.Running() {
			r.End, r.Summary, r.Met = string(st.End), st.Summary, st.Met
			r.Started, r.Ended = st.Started, st.Ended
			r.Steps, r.Cost, r.Tokens, r.Priced = st.Steps, st.Cost, st.Tokens, st.Priced
			return r
		}
		if !r.Manual {
			c.denyPayments(d.Info.ID, denied)
		}
		select {
		case <-ctx.Done():
			c.Stop()
			r.End, r.Reason = string(pilot.Error), schedule.Interrupted
			return r
		case <-ch:
		case <-time.After(time.Second):
		}
	}
}

// denyPayments denies at once what a payment rule asks of a scheduled task
// nobody watches: it never pays. Other approvals wait for the person, whom
// the app notifies.
func (c *Core) denyPayments(device string, denied map[string]bool) {
	for _, n := range c.G.Needs() {
		if n.DeviceID != device || n.Kind != gateway.NeedApproval || n.Agent != mcp.BuiltinName || denied[n.ID] || !schedule.IsPayment(n.Rule) {
			continue
		}
		denied[n.ID] = true
		if err := c.G.Decide(n.ID, gateway.Deny, "A scheduled task never pays: Pinky denied this step by itself."); err != nil {
			c.log.Warn("schedules: deny a payment", "need", n.ID, "err", err)
		}
	}
}

func (h schedHost) Report(s schedule.Schedule, r schedule.Run) {
	if !r.Notify {
		return
	}
	c := h.c
	c.mu.Lock()
	c.noticeSeq++
	c.notices = append(c.notices, Notice{ID: c.noticeSeq, Schedule: s.ID, Task: s.Task, Condition: s.Condition,
		End: r.End, Reason: r.Reason, Missed: r.Missed, Summary: r.Summary, Met: r.Met, DueMS: r.Due.UnixMilli()})
	if n := len(c.notices); n > maxNotices {
		c.notices = append([]Notice(nil), c.notices[n-maxNotices:]...)
	}
	c.mu.Unlock()
	c.changed()
}

// noticeSeen forgets a notice the app has shown.
func (c *Core) noticeSeen(id int64) {
	c.mu.Lock()
	for i, n := range c.notices {
		if n.ID == id {
			c.notices = append(c.notices[:i:i], c.notices[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	c.changed()
}

// pendingNotices are the notices the app has not shown.
func (c *Core) pendingNotices() []Notice {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.notices) == 0 {
		return nil
	}
	return append([]Notice(nil), c.notices...)
}

// Due handles the times due now: the app's alarm fired.
func (c *Core) Due(req StartRequest, phone PhoneNow) time.Time {
	return c.sc.Tick(schedNow(), schedHost{c: c, req: req, phone: phone})
}

// RunSchedule runs a schedule's task now, with the person there.
func (c *Core) RunSchedule(id string, req StartRequest) error {
	if _, ok := c.sched.Get(id); !ok {
		return schedule.ErrNotFound
	}
	if d, ok := c.device(); ok {
		if st, ok := c.pilot.Status(d.Info.ID); ok && st.Running() {
			return pilot.ErrBusy
		}
	}
	return c.sc.RunNow(id, schedNow(), schedHost{c: c, req: req})
}

// NextDue is when the alarm should next fire.
func (c *Core) NextDue() time.Time { return c.sc.NextTick(schedNow()) }

// schedule answers a schedule command.
func (c *Core) schedule(r request) reply {
	c.setZone(r.Zone)
	now := schedNow()
	var err error
	switch r.Cmd {
	case "schedules":
		return reply{OK: true, Schedules: c.sched.List(), NextMS: nextMS(c.NextDue())}
	case "schedule_save":
		if r.Schedule == nil {
			err = schedule.ErrNoTask
			break
		}
		s := *r.Schedule
		s.Device = ""
		var saved schedule.Schedule
		if saved, err = c.sched.Save(s, now); err == nil {
			return reply{OK: true, Schedules: []schedule.Schedule{saved}, NextMS: nextMS(c.NextDue())}
		}
	case "schedule_preview":
		if r.Schedule == nil {
			err = schedule.ErrNoTask
			break
		}
		s := r.Schedule.Normalize()
		if err = s.Validate(now, c.sched.Local()); err == nil {
			up := []int64{}
			for _, t := range s.Upcoming(now, c.sched.Local(), 3) {
				up = append(up, t.UnixMilli())
			}
			return reply{OK: true, Upcoming: up}
		}
	case "schedule_delete":
		err = c.sched.Delete(r.ID)
	case "schedule_pause":
		err = c.sched.SetPaused(r.ID, r.Paused, now)
	case "schedule_run":
		err = c.RunSchedule(r.ID, r.StartRequest)
	case "schedule_due":
		return reply{OK: true, NextMS: nextMS(c.Due(r.StartRequest, r.PhoneNow))}
	case "notice_seen":
		c.noticeSeen(r.Notice)
	}
	if err != nil {
		code := schedCode(err)
		if code == "" {
			code = codeOf(err)
		}
		return reply{Error: err.Error(), Code: code}
	}
	return reply{OK: true, NextMS: nextMS(c.NextDue())}
}

// backgroundReady reports whether tasks run on a background display now:
// the person turned it on and paired Pinky with Wireless debugging, and its
// port is there. Android 13 and later keep such a display unlocked, so a
// scheduled task runs while the phone is locked. Not while the person uses
// it: should the display fail to open, the task moves to the phone's own
// screen, under their fingers. Nor before the setup is done or after a
// restart turned Wireless debugging off, when tasks run on the phone's own
// screen (docs/notes/schedules.md).
func (c *Core) backgroundReady(device string) bool {
	sh := c.opt.Shell
	return sh != nil && shellReady(sh.State().Status) && c.G.Background(device)
}

// shellReady says whether a Shell's status lets a task open a background
// display: paired, with Wireless debugging's port.
func shellReady(status string) bool {
	return status == "ready" || status == "connected"
}
