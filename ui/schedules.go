package ui

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/schedule"
)

// Scheduled tasks for Ask Pinky (internal/schedule): a task the person sets
// to run on a phone at set times, once or again and again, optionally
// telling them only when a condition holds. They run while Pinky runs on
// this computer; a time missed while it was not running is reported, not
// run late (docs/notes/schedules.md). The right panel lists a phone's
// schedules under Tasks, Schedule… in the Ask box makes one, and a dialog
// edits one, with its next times and last runs.

// schedPilot is the Pilot that runs scheduled tasks (pilot.Runner).
type schedPilot interface {
	StartScheduled(cfg pilot.Config, device, task string, s pilot.Scheduled) error
}

// schedState is the scheduled tasks': the scheduler, the schedules as the
// store last said, and the dialog.
type schedState struct {
	sc   *schedule.Scheduler
	list []schedule.Schedule

	open     bool
	id       string // the schedule the dialog edits; "" for a new one
	task     string
	device   string
	repeat   string
	date     string
	times    string
	days     []int
	every    int
	cond     string
	cooldown int
	zone     string
	paused   bool
	err      string
	gen      int
}

// schedOn reports whether scheduled tasks show: Pinky keeps them, Ask Pinky
// is on and its runner runs them.
func (a *app) schedOn() bool {
	_, ok := a.opt.Pilot.(schedPilot)
	return ok && a.opt.Schedules != nil && a.sched.sc != nil && a.settings.AskPinky
}

// startSchedules starts running the schedules, once the client is ready.
func (a *app) startSchedules() {
	st := a.opt.Schedules
	if _, ok := a.opt.Pilot.(schedPilot); !ok || st == nil {
		return
	}
	a.sched.sc = schedule.New(st)
	st.OnChange(func() { a.post(a.readSchedules) })
	a.readSchedules()
	a.sched.sc.Loop(schedHost{a})
}

func (a *app) stopSchedules() {
	if a.sched.sc != nil {
		a.sched.sc.Close()
	}
}

func (a *app) readSchedules() { a.sched.list = a.opt.Schedules.List() }

// onMain runs fn on the main thread and waits for it; false when the
// client quits first.
func (a *app) onMain(ctx context.Context, fn func()) bool {
	done := make(chan struct{})
	a.post(func() {
		fn()
		close(done)
	})
	select {
	case <-done:
		return true
	case <-ctx.Done():
	case <-a.ctx.Done():
	}
	return false
}

// schedHost runs the schedules on the phones this computer reaches.
type schedHost struct{ a *app }

func (h schedHost) Check(s schedule.Schedule) string {
	a := h.a
	on := false
	if !a.onMain(a.ctx, func() { on = a.settings.AskPinky }) {
		return schedule.SkipBusy
	}
	if !on {
		return schedule.SkipOff
	}
	var d *gateway.DeviceView
	devs := a.api.Devices()
	for i := range devs {
		if devs[i].Info.ID == s.Device {
			d = &devs[i]
		}
	}
	switch {
	case d == nil:
		return schedule.SkipNoPhone
	case d.Paused || d.Status == gateway.DeviceOffline || d.Status == gateway.DeviceConnecting || d.Status == gateway.DeviceNeedsSetup:
		return schedule.SkipOffline
	case d.Status == gateway.DeviceLocked:
		return schedule.SkipLocked
	case d.Status == gateway.DeviceInControl:
		return schedule.SkipInUse
	case d.Status == gateway.DeviceNeedsYou || d.Status == gateway.DeviceWorking:
		return schedule.SkipBusy
	}
	if ps, ok := a.opt.Pilot.Status(s.Device); ok && ps.Running() {
		return schedule.SkipBusy
	}
	return ""
}

// schedPoll is how often a scheduled task's run is looked at.
var schedPoll = 500 * time.Millisecond

func (h schedHost) Run(ctx context.Context, s schedule.Schedule, r schedule.Run) schedule.Run {
	a := h.a
	var cfg pilot.Config
	if !a.onMain(ctx, func() { cfg = a.settings.PilotConfig() }) {
		r.End = string(pilot.Stopped)
		return r
	}
	loc, _ := s.Location(time.Local)
	ps := pilot.Scheduled{Unattended: !r.Manual, Condition: s.Condition, Zone: loc}
	if l := s.LastDone(); l != nil {
		ps.Last = &pilot.Earlier{At: l.Due, Summary: l.Summary}
	}
	if m := s.LastMet; m != nil {
		ps.LastMet = &pilot.Earlier{At: m.Due, Summary: m.Summary}
	}
	if err := a.opt.Pilot.(schedPilot).StartScheduled(cfg, s.Device, s.Task, ps); err != nil {
		switch {
		case errors.Is(err, pilot.ErrBusy):
			r.End, r.Reason = schedule.Skipped, schedule.SkipBusy
		case errors.Is(err, pilot.ErrNoModel):
			r.End = string(pilot.NoKey)
		default:
			r.End, r.Summary = string(pilot.Error), err.Error()
		}
		return r
	}
	denied := map[string]bool{}
	t := time.NewTicker(schedPoll)
	defer t.Stop()
	for {
		st, ok := a.opt.Pilot.Status(s.Device)
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
			h.denyPayments(s.Device, denied)
		}
		select {
		case <-ctx.Done():
			a.opt.Pilot.Stop(s.Device)
			r.End, r.Reason = string(pilot.Error), schedule.Interrupted
			return r
		case <-t.C:
		}
	}
}

// denyPayments denies at once what a payment rule asks of a scheduled task
// nobody watches: it never pays (docs/notes/schedules.md). Other approvals
// wait for the person, as always, and no answer is a denial.
func (h schedHost) denyPayments(device string, denied map[string]bool) {
	a := h.a
	for _, n := range a.api.Needs() {
		if n.DeviceID != device || n.Kind != gateway.NeedApproval || n.Agent != mcp.BuiltinName || denied[n.ID] || !schedule.IsPayment(n.Rule) {
			continue
		}
		denied[n.ID] = true
		if err := a.api.Decide(n.ID, gateway.Deny, locales.SchedPaymentDenied()); err != nil {
			log.Printf("pinky: scheduled task: deny %s: %v", n.ID, err)
		}
	}
}

func (h schedHost) Report(s schedule.Schedule, r schedule.Run) {
	if r.Notify {
		h.a.post(func() { h.a.notifySchedule(s, r) })
	}
}

// notifySchedule tells the person how a scheduled task went: a system
// notification, or a toast while Pinky's window is in front.
func (a *app) notifySchedule(s schedule.Schedule, r schedule.Run) {
	title, detail := a.runText(s, r)
	head := locales.SchedNotifyTitle(clipTask(s.Task))
	if a.focused || a.main == nil || !mygo.NotificationsSupported() {
		a.toasts = append(a.toasts, head+" · "+title)
		return
	}
	body := title
	if detail != "" {
		body = detail
	}
	nt := mygo.NewNotification(mygo.NotificationOptions{ID: fmt.Sprintf("schedule-%s-%d", s.ID, r.Due.Unix()),
		Group: "schedules", Title: head, Subtitle: a.phoneName(s.Device), Body: body})
	nt.OnClick(func() {
		a.openSchedule(s.ID)
		a.showMain()
		nt.Close()
	})
	go func() {
		if err := nt.Show(); err != nil && !errors.Is(err, mygo.ErrNotificationsDenied) {
			log.Printf("pinky: notification: %v", err)
		}
	}()
}

// clipTask is a task short enough for a title.
func clipTask(task string) string {
	task = strings.Join(strings.Fields(task), " ")
	if r := []rune(task); len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return task
}

func (a *app) phoneName(id string) string {
	if d := a.device(id); d != nil {
		return d.Info.Name
	}
	return id
}

// runText is how a run went, in the language shown: a title and, when
// there is more to say, what.
func (a *app) runText(s schedule.Schedule, r schedule.Run) (title, detail string) {
	phone := a.phoneName(s.Device)
	switch {
	case r.End == schedule.Running:
		return locales.AskWorking(), ""
	case r.End == schedule.Skipped:
		switch r.Reason {
		case schedule.SkipNotRunning:
			return locales.SchedSkipped(), locales.SchedSkipNotRunning(r.Missed + 1)
		case schedule.SkipLocked:
			return locales.SchedSkipped(), locales.SchedSkipLocked(phone)
		case schedule.SkipOffline:
			return locales.SchedSkipped(), locales.SchedSkipOffline(phone)
		case schedule.SkipInUse:
			return locales.SchedSkipped(), locales.SchedSkipInUse(phone)
		case schedule.SkipNoPhone:
			return locales.SchedSkipped(), locales.SchedSkipNoPhone()
		case schedule.SkipOff:
			return locales.SchedSkipped(), locales.SchedSkipOff()
		}
		return locales.SchedSkipped(), locales.SchedSkipBusy(phone)
	case r.Reason == schedule.Interrupted:
		return locales.AskEndFailed(), locales.SchedInterrupted()
	}
	title, detail = pilotEnd(pilot.Status{End: pilot.End(r.End), Summary: r.Summary, Priced: r.Priced, Config: a.settings.PilotConfig()})
	if r.Met != nil && s.Condition != "" {
		met := locales.SchedNotMet()
		if *r.Met {
			met = locales.SchedMet()
		}
		title += " · " + met
	}
	return title, detail
}

// scheduledSection lists the schedules of the phone shown, or of every
// phone, under Tasks.
func (a *app) scheduledSection(c *mg.Context) {
	if !a.schedOn() {
		return
	}
	var list []schedule.Schedule
	for _, s := range a.sched.list {
		if a.sel == allPhones || a.device(a.sel) == nil || s.Device == a.sel {
			list = append(list, s)
		}
	}
	if len(list) == 0 {
		return
	}
	mg.Column(c).Label(locales.SchedSection()).Children(func() {
		sectionLabel(c, locales.SchedSection())
		mg.Column(c).Gap(8).Children(func() {
			for _, s := range list {
				a.schedRow(c.Key("sched-"+s.ID), s)
			}
			if linkButton(c.Key("sched-new"), locales.SchedNew()).AlignSelf(mg.Start).Clicked() {
				a.newSchedule(a.sel, "")
			}
		})
	})
}

// schedRow is a schedule in the list: its task, when it runs next and how
// it last went. It opens the dialog.
func (a *app) schedRow(c *mg.Context, s schedule.Schedule) {
	p := pal(c)
	row := mg.ButtonBase(c).Padding(sp2, sp3).Radius(rControl).Border(1, p.line).Background(p.card).Gap(10).
		AlignItems(mg.Center).Justify(mg.Start).Label(locales.SchedOpen(clipTask(s.Task)))
	row = raised(c, row)
	if row.Hovered() {
		row.Background(p.bg3)
	}
	if row.Clicked() {
		a.openSchedule(s.ID)
	}
	running := len(s.Runs) > 0 && s.Runs[0].End == schedule.Running
	row.Children(func() {
		switch {
		case running:
			dot(c, p.green, true)
		case s.Paused:
			mg.Icon(c, icPause).FontSize(iconSmall).TextColor(p.ink3)
		default:
			dot(c, p.accentInk, false)
		}
		mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			mg.Text(c, clipTask(s.Task)).FontSize(fsSmall).FontWeight(fwMedium).SingleLine()
			parts := []string{a.nextText(s)}
			if a.sel == allPhones || a.device(a.sel) == nil {
				parts = append([]string{a.phoneName(s.Device)}, parts...)
			}
			if len(s.Runs) > 0 && !running {
				t, _ := a.runText(s, s.Runs[0])
				parts = append(parts, t)
			}
			mg.Text(c, strings.Join(parts, " · ")).FontSize(fsCaption).TextColor(p.ink2).SingleLine()
		})
	})
}

// nextText is when a schedule runs next.
func (a *app) nextText(s schedule.Schedule) string {
	if s.Paused {
		return locales.SchedPaused()
	}
	local := a.opt.Schedules.Local()
	next, ok := s.Next(s.Cursor, local)
	if !ok {
		return locales.SchedNoNext()
	}
	if now := time.Now(); next.Before(now) {
		next = now
	}
	loc, _ := s.Location(local)
	return locales.SchedNextAt(next.In(loc))
}

// newSchedule opens the dialog for a new schedule on a phone, with a task.
func (a *app) newSchedule(device, task string) {
	if a.device(device) == nil {
		if d, ok := a.askPhone(); ok {
			device = d.Info.ID
		}
	}
	st := &a.sched
	*st = schedState{sc: st.sc, list: st.list, gen: st.gen + 1, open: true,
		task: task, device: device, repeat: schedule.Daily, times: "08:00", every: 4,
		date: time.Now().AddDate(0, 0, 1).Format(time.DateOnly)}
}

// openSchedule opens the dialog on a schedule.
func (a *app) openSchedule(id string) {
	s, ok := a.opt.Schedules.Get(id)
	if !ok {
		return
	}
	st := &a.sched
	*st = schedState{sc: st.sc, list: st.list, gen: st.gen + 1, open: true, id: s.ID,
		task: s.Task, device: s.Device, repeat: s.Repeat, date: s.Date, times: strings.Join(s.Times, ", "),
		days: slices.Clone(s.Days), every: s.Every, cond: s.Condition, cooldown: s.Cooldown, zone: s.Zone, paused: s.Paused}
	if st.every == 0 {
		st.every = 4
	}
	if st.date == "" {
		st.date = time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	}
	if a.device(s.Device) != nil && a.sel != allPhones {
		a.selectPhone(s.Device)
	}
}

// schedDraft is the schedule as the dialog has it.
func (a *app) schedDraft() schedule.Schedule {
	st := &a.sched
	return schedule.Schedule{ID: st.id, Task: st.task, Device: st.device, Repeat: st.repeat, Date: strings.TrimSpace(st.date),
		Times: []string{st.times}, Days: st.days, Every: st.every, Zone: st.zone, Condition: st.cond,
		Cooldown: st.cooldown, Paused: st.paused}.Normalize()
}

// schedErr says why a schedule cannot be saved.
func schedErr(err error) string {
	switch {
	case errors.Is(err, schedule.ErrNoTask):
		return locales.SchedErrNoTask()
	case errors.Is(err, schedule.ErrLongTask):
		return locales.SchedErrLong()
	case errors.Is(err, schedule.ErrNoTime):
		return locales.SchedErrNoTime()
	case errors.Is(err, schedule.ErrTime):
		return locales.SchedErrTime()
	case errors.Is(err, schedule.ErrTooMany):
		return locales.SchedErrTooMany(schedule.MaxTimes)
	case errors.Is(err, schedule.ErrDate):
		return locales.SchedErrDate()
	case errors.Is(err, schedule.ErrPast):
		return locales.SchedErrPast()
	case errors.Is(err, schedule.ErrNoDays):
		return locales.SchedErrNoDays()
	case errors.Is(err, schedule.ErrZone):
		return locales.SchedErrZone()
	case errors.Is(err, schedule.ErrRunning):
		return locales.SchedErrRunning()
	case errors.Is(err, schedule.ErrNoPhone):
		return locales.SchedErrNoPhone()
	}
	return locales.SchedErrOther(err.Error())
}

// The repeats the dialog offers, in its order.
var schedRepeats = []string{schedule.Once, schedule.Daily, schedule.Weekdays, schedule.Weekly, schedule.Interval}

func repeatLabel(r string) string {
	switch r {
	case schedule.Once:
		return locales.SchedRepeatOnce()
	case schedule.Weekdays:
		return locales.SchedRepeatWeekdays()
	case schedule.Weekly:
		return locales.SchedRepeatWeekly()
	case schedule.Interval:
		return locales.SchedRepeatInterval()
	}
	return locales.SchedRepeatDaily()
}

// The cooldowns the dialog offers, in minutes.
var schedCooldowns = []int{0, 60, 24 * 60}

func cooldownLabel(m int) string {
	switch m {
	case 0:
		return locales.SchedCooldownAlways()
	case 60:
		return locales.SchedCooldownHour()
	case 24 * 60:
		return locales.SchedCooldownDay()
	}
	return locales.AskMinutes(m)
}

// weekday is a day of the week to name: one in the week of 2 November 2026,
// which starts on a Sunday.
func weekday(d int) time.Time { return time.Date(2026, 11, 1+d, 12, 0, 0, 0, time.UTC) }

// field is a labelled field of the dialog.
func field(c *mg.Context, label string, fn func()) {
	p := pal(c)
	mg.Column(c).Gap(6).Children(func() {
		mg.Text(c, label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(p.ink2)
		fn()
	})
}

// schedDialog edits a schedule: its task and phone, when it runs, when to
// tell the person, its next times and last runs; Run now, Pause and
// Delete.
func (a *app) schedDialog(c *mg.Context) {
	st := &a.sched
	if !st.open {
		return
	}
	if !a.schedOn() {
		st.open = false
		return
	}
	p := pal(c)
	title := locales.SchedTitleNew()
	if st.id != "" {
		title = locales.SchedTitleEdit()
	}
	key := func(k string) *mg.Context { return c.Key(fmt.Sprintf("sched-%s-%d", k, st.gen)) }
	sheet(c.Key("schedule"), &st.open, func() {
		mg.Text(c, title).FontSize(fsTitle3).FontWeight(fwSemibold).Role(mg.RoleHeading)
		mg.Scroll(c).Width(520).Shrink(1).MinHeight(0).Gap(14).Label(title).Children(func() {
			field(c, locales.SchedTask(), func() {
				mg.TextArea(key("task"), &st.task).Label(locales.SchedTask()).
					Placeholder(locales.AskPlaceholder(a.phoneName(st.device))).Lines(2, 5).AutoFocus()
			})
			a.schedPhoneField(c)
			field(c, locales.SchedRepeat(), func() {
				labels := make([]string, len(schedRepeats))
				for i, r := range schedRepeats {
					labels[i] = repeatLabel(r)
				}
				cur := repeatLabel(st.repeat)
				if mg.Select(key("repeat"), &cur, labels).Label(locales.SchedRepeat()).Changed() {
					for i, l := range labels {
						if l == cur {
							st.repeat = schedRepeats[i]
						}
					}
				}
			})
			a.schedWhenFields(c, key)
			field(c, locales.SchedCondition(), func() {
				mg.TextInput(key("cond"), &st.cond).Label(locales.SchedCondition()).Placeholder(locales.SchedConditionHint())
				mg.Text(c, locales.SchedConditionDesc()).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3)
			})
			if strings.TrimSpace(st.cond) != "" {
				field(c, locales.SchedCooldown(), func() {
					labels := make([]string, len(schedCooldowns))
					for i, m := range schedCooldowns {
						labels[i] = cooldownLabel(m)
					}
					cur := cooldownLabel(st.cooldown)
					if !slices.Contains(schedCooldowns, st.cooldown) {
						labels = append(labels, cur)
					}
					if mg.Select(key("cooldown"), &cur, labels).Label(locales.SchedCooldown()).Changed() {
						for i, l := range labels {
							if i < len(schedCooldowns) && l == cur {
								st.cooldown = schedCooldowns[i]
							}
						}
					}
				})
			}
			field(c, locales.SchedZone(), func() {
				mg.TextInput(key("zone"), &st.zone).Label(locales.SchedZone()).Placeholder(localZoneName()).MaxWidth(260)
			})
			a.schedPreview(c)
			if st.id != "" {
				a.schedRuns(c)
			}
			mg.Text(c, locales.SchedDesktopNote()).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3)
		})
		if st.err != "" {
			mg.Text(c, st.err).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
		}
		a.schedButtons(c)
	})
}

// schedPhoneField chooses the phone the schedule runs on.
func (a *app) schedPhoneField(c *mg.Context) {
	st := &a.sched
	labels := []string{}
	ids := []string{}
	for _, d := range a.devices {
		labels, ids = append(labels, d.Info.Name), append(ids, d.Info.ID)
	}
	if st.device != "" && !slices.Contains(ids, st.device) {
		labels, ids = append(labels, st.device), append(ids, st.device)
	}
	if len(ids) < 2 {
		return
	}
	cur := ""
	for i, id := range ids {
		if id == st.device {
			cur = labels[i]
		}
	}
	field(c, locales.AskPhone(), func() {
		if mg.Select(c.Key(fmt.Sprintf("sched-phone-%d", st.gen)), &cur, labels).Label(locales.AskPhone()).Changed() {
			for i, l := range labels {
				if l == cur {
					st.device = ids[i]
				}
			}
		}
	})
}

// schedWhenFields are the fields of the repeat chosen: a date, times, days
// or hours.
func (a *app) schedWhenFields(c *mg.Context, key func(string) *mg.Context) {
	st := &a.sched
	if st.repeat == schedule.Once {
		field(c, locales.SchedDate(), func() {
			mg.TextInput(key("date"), &st.date).Label(locales.SchedDate()).Placeholder("YYYY-MM-DD").MaxWidth(200)
		})
	}
	if st.repeat == schedule.Weekly {
		field(c, locales.SchedDays(), func() {
			mg.Row(c).Gap(6).Wrap().Children(func() {
				for _, d := range []int{1, 2, 3, 4, 5, 6, 0} {
					on := slices.Contains(st.days, d)
					kind := btnDefault
					if on {
						kind = btnPrimary
					}
					if button(key(fmt.Sprintf("day-%d", d)), locales.SchedDay(weekday(d)), btnOpt{small: true, kind: kind}).Clicked() {
						if on {
							st.days = slices.DeleteFunc(st.days, func(x int) bool { return x == d })
						} else {
							st.days = append(st.days, d)
						}
					}
				}
			})
		})
	}
	if st.repeat == schedule.Interval {
		mg.Row(c).Gap(sp3).Children(func() {
			field(c, locales.SchedStartAt(), func() {
				mg.TextInput(key("start"), &st.times).Label(locales.SchedStartAt()).Placeholder("08:00").MaxWidth(140)
			})
			field(c, locales.SchedEvery(), func() {
				labels := make([]string, len(schedule.Intervals))
				for i, h := range schedule.Intervals {
					labels[i] = locales.SchedHours(h)
				}
				cur := locales.SchedHours(st.every)
				if mg.Select(key("every"), &cur, labels).Label(locales.SchedEvery()).Changed() {
					for i, l := range labels {
						if l == cur {
							st.every = schedule.Intervals[i]
						}
					}
				}
			})
		})
		return
	}
	label := locales.SchedTimes()
	if st.repeat == schedule.Once {
		label = locales.SchedTime()
	}
	field(c, label, func() {
		mg.TextInput(key("times"), &st.times).Label(label).Placeholder("08:00, 12:30, 20:00").MaxWidth(320)
	})
}

// schedPreview shows the next three times, or why there are none.
func (a *app) schedPreview(c *mg.Context) {
	p := pal(c)
	s := a.schedDraft()
	local := a.opt.Schedules.Local()
	if s.Task == "" {
		s.Task = "-" // the times show before the task is typed
	}
	field(c, locales.SchedNext(), func() {
		if err := s.Validate(time.Now(), local); err != nil {
			mg.Text(c, schedErr(err)).FontSize(fsSmall).TextColor(p.ink2).Role(mg.RoleStatus)
			return
		}
		loc, _ := s.Location(local)
		next := s.Upcoming(time.Now(), local, 3)
		if len(next) == 0 {
			mg.Text(c, locales.SchedNoNext()).FontSize(fsSmall).TextColor(p.ink2)
		}
		for _, t := range next {
			mg.Text(c, locales.SchedWhen(t.In(loc))).FontSize(fsSmall).FontFeatures("tnum")
		}
	})
}

// schedRuns lists the schedule's last runs, newest first.
func (a *app) schedRuns(c *mg.Context) {
	p := pal(c)
	s, ok := a.opt.Schedules.Get(a.sched.id)
	if !ok {
		return
	}
	loc, _ := s.Location(a.opt.Schedules.Local())
	field(c, locales.SchedRuns(), func() {
		if len(s.Runs) == 0 {
			mg.Text(c, locales.SchedNoRuns()).FontSize(fsSmall).TextColor(p.ink2)
			return
		}
		for _, r := range s.Runs {
			title, detail := a.runText(s, r)
			when := locales.SchedWhen(r.Due.In(loc))
			if r.Manual {
				when += " · " + locales.SchedRunNow()
			}
			mg.Column(c).Gap(2).Padding(sp2, sp3).Radius(rControl).Background(p.bg2).Children(func() {
				mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
					mg.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).Grow(1).MinWidth(0)
					mg.Text(c, when).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
				})
				if detail != "" {
					mg.Text(c, detail).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink2).Selectable()
				}
			})
		}
	})
}

// schedButtons are the dialog's buttons: Delete, Pause and Run now for a
// saved schedule; Cancel and Save.
func (a *app) schedButtons(c *mg.Context) {
	st := &a.sched
	store := a.opt.Schedules
	mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
		if st.id != "" {
			if button(c, locales.SchedDelete(), btnOpt{small: true, kind: btnRed}).Clicked() {
				if err := store.Delete(st.id); err != nil {
					st.err = schedErr(err)
				} else {
					st.open = false
				}
			}
			pause := locales.SchedPause()
			if st.paused {
				pause = locales.SchedResume()
			}
			if button(c, pause, btnOpt{small: true}).Clicked() {
				if err := store.SetPaused(st.id, !st.paused, time.Now()); err != nil {
					st.err = schedErr(err)
				} else {
					st.paused = !st.paused
				}
			}
			if button(c, locales.SchedRunNow(), btnOpt{small: true}).Clicked() {
				if err := st.sc.RunNow(st.id, time.Now(), schedHost{a}); err != nil {
					st.err = schedErr(err)
				} else {
					st.open = false
					if a.device(st.device) != nil {
						a.selectPhone(st.device)
					}
				}
			}
		}
		mg.Box(c).Grow(1)
		if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
			st.open = false
		}
		if button(c, locales.SettingsSave(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
			if st.device == "" {
				st.err = schedErr(schedule.ErrNoPhone)
				return
			}
			if _, err := store.Save(a.schedDraft(), time.Now()); err != nil {
				st.err = schedErr(err)
				return
			}
			st.open = false
			if st.id == "" {
				a.ask.draft = ""
				a.ask.gen++
			}
		}
	})
}

// localZoneName is the computer's time zone, as the dialog suggests it.
func localZoneName() string {
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		return tz
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	return time.Local.String()
}
