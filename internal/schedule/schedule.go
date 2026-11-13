// Package schedule is scheduled tasks for Pinky's own agent
// (internal/pilot): a task the person sets to run at a time, once or
// again and again, optionally telling them only when a condition holds
// ("every day at 8, check the weather; tell me only if it will rain").
//
// It knows when a schedule is due, keeps the schedules and their last runs
// in a file, and decides what to tell the person about a run. Running the
// task is the host's: desktop Pinky (ui) and Pinky on the phone
// (internal/standalone) each check their phone, start the agent and say
// the result in their own way (Host). docs/notes/schedules.md has the
// design and the policy for runs nobody watches.
package schedule

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// How a schedule repeats.
const (
	Once     = "once"     // on Date at Times[0]
	Daily    = "daily"    // every day at each of Times
	Weekdays = "weekdays" // Monday to Friday at each of Times
	Weekly   = "weekly"   // on Days at each of Times
	Interval = "interval" // every Every hours, from Times[0] to the end of each day
)

// Limits of a schedule.
const (
	MaxTimes    = 12   // times a day a schedule names
	MaxTask     = 2000 // characters of a task
	MaxRuns     = 10   // runs a schedule keeps, newest first
	MaxCooldown = 7 * 24 * 60
)

// Intervals are the hours Interval may repeat every.
var Intervals = []int{1, 2, 3, 4, 6, 8, 12}

// Schedule is a task for Pinky's own agent at set times.
type Schedule struct {
	ID string `json:"id"`
	// Task is the person's words, as they would type them into Ask Pinky.
	Task string `json:"task"`
	// Device is the phone it runs on, for desktop Pinky; Pinky on the
	// phone leaves it empty.
	Device string `json:"device,omitempty"`
	Repeat string `json:"repeat"`
	// Date is Once's day, "2006-01-02".
	Date string `json:"date,omitempty"`
	// Times are the times of day, "15:04", sorted: Once's one, the times
	// of Daily, Weekdays and Weekly, and the first of Interval.
	Times []string `json:"times,omitempty"`
	// Days are Weekly's days, 0 for Sunday to 6 for Saturday, sorted.
	Days []int `json:"days,omitempty"`
	// Every is Interval's hours, one of Intervals.
	Every int `json:"every,omitempty"`
	// Zone is the IANA time zone the times are in; empty is the one the
	// host is in (Store.SetLocal).
	Zone string `json:"zone,omitempty"`
	// Condition, when set, is when to tell the person ("it will rain"):
	// the agent says whether it holds, and only then are they told.
	Condition string `json:"condition,omitempty"`
	// Cooldown is the fewest minutes between two times a condition tells
	// the person ("at most once a day"); 0 tells them every time it holds.
	Cooldown int  `json:"cooldown,omitempty"`
	Paused   bool `json:"paused,omitempty"`

	Created time.Time `json:"created"`
	// Cursor is the last time handled: the schedule is next due at the
	// first of its times after it. Saving a schedule moves it to then, so
	// that an edit does not run what was due before.
	Cursor time.Time `json:"cursor"`
	// Runs are its last runs, newest first, MaxRuns at most.
	Runs []Run `json:"runs,omitempty"`
	// LastMet is the last run whose condition held, kept after Runs has
	// let it go: the next run compares with it ("cheaper by 50 than when
	// you last told me").
	LastMet *Run `json:"last_met,omitempty"`
	// Notified is when the person was last told of a condition that held,
	// for Cooldown.
	Notified time.Time `json:"notified,omitempty"`
}

// Run is one time a schedule was due, or was run by hand.
type Run struct {
	// Due is when it was due; for Run now, when the person pressed it.
	Due     time.Time `json:"due"`
	Manual  bool      `json:"manual,omitempty"` // Run now
	Started time.Time `json:"started,omitempty"`
	Ended   time.Time `json:"ended,omitempty"`
	// End is how it ended: a pilot.End ("done", "failed", "stopped",
	// "steps", …), Running while it runs, or Skipped.
	End string `json:"end"`
	// Reason is why a run was skipped (the Skip constants).
	Reason  string `json:"reason,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Met says whether the schedule's condition held, when it has one
	// and the agent finished.
	Met *bool `json:"met,omitempty"`
	// Notify says the person is told of this run (Store.Finish).
	Notify bool `json:"notify,omitempty"`
	// Missed counts the earlier times that were due and missed with it,
	// while Pinky was not running.
	Missed int `json:"missed,omitempty"`

	Steps  int     `json:"steps,omitempty"`
	Cost   float64 `json:"cost,omitempty"`
	Tokens int64   `json:"tokens,omitempty"`
	Priced bool    `json:"priced,omitempty"`
}

// Ends of a Run besides pilot's.
const (
	Running = "running"
	Skipped = "skipped"
)

// Why a run was skipped. Busy and InUse wait a while first (Host.Check).
const (
	SkipNotRunning = "not_running" // Pinky was not running when it was due
	SkipLocked     = "locked"      // the phone was locked, or its screen off
	SkipOffline    = "offline"     // the phone was not connected or not ready
	SkipBusy       = "busy"        // another task, or Pinky on a computer, had the phone
	SkipInUse      = "in_use"      // the person was using the phone
	SkipNoKey      = "no_key"      // no API key, or no model, to run with
	SkipNoPhone    = "no_phone"    // the schedule's phone is no longer Pinky's
	SkipOff        = "off"         // the person turned Pinky's own agent off
)

// Errors of Validate. The hosts say them in the person's language.
var (
	ErrNoTask   = errors.New("schedule: no task")
	ErrLongTask = errors.New("schedule: the task is too long")
	ErrRepeat   = errors.New("schedule: unknown repeat")
	ErrNoTime   = errors.New("schedule: no time")
	ErrTime     = errors.New("schedule: a time is not HH:MM")
	ErrTooMany  = errors.New("schedule: too many times")
	ErrDate     = errors.New("schedule: the date is not YYYY-MM-DD")
	ErrPast     = errors.New("schedule: the time has passed")
	ErrNoDays   = errors.New("schedule: no days")
	ErrInterval = errors.New("schedule: unknown interval")
	ErrZone     = errors.New("schedule: unknown time zone")
	ErrCooldown = errors.New("schedule: bad cooldown")
	ErrNoPhone  = errors.New("schedule: no phone")
	ErrNotFound = errors.New("schedule: no such schedule")
	ErrRunning  = errors.New("schedule: a scheduled task is already running")
)

// ParseTimes reads times of day as people type them: "8:00, 12:30 20:00",
// "8" for 8:00. It returns them as "15:04", sorted, without repeats.
func ParseTimes(s string) ([]string, error) {
	f := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == ';' || r == '，' || r == '、' || r == '\t' || r == '\n'
	})
	var out []string
	for _, t := range f {
		n, err := parseTime(t)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out, nil
}

func parseTime(t string) (string, error) {
	t = strings.ReplaceAll(strings.TrimSpace(t), "：", ":")
	h, m, hasMin := strings.Cut(t, ":")
	if !hasMin {
		m = "0"
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(h) > 2 || len(m) > 2 || h == "" {
		return "", ErrTime
	}
	return fmt.Sprintf("%02d:%02d", hh, mm), nil
}

func clock(t string) (h, m int) {
	fmt.Sscanf(t, "%d:%d", &h, &m)
	return
}

// Normalize tidies what the person typed: the task's spaces, the times
// and days sorted.
func (s Schedule) Normalize() Schedule {
	s.Task = strings.TrimSpace(s.Task)
	s.Condition = strings.TrimSpace(s.Condition)
	s.Zone = strings.TrimSpace(s.Zone)
	s.Repeat = strings.TrimSpace(s.Repeat)
	if ts, err := ParseTimes(strings.Join(s.Times, ",")); err == nil {
		s.Times = ts
	}
	if s.Repeat == Once || s.Repeat == Interval {
		s.Times = s.Times[:min(len(s.Times), 1)]
	}
	var days []int
	for _, d := range s.Days {
		if d >= 0 && d <= 6 && !slices.Contains(days, d) {
			days = append(days, d)
		}
	}
	slices.Sort(days)
	s.Days = days
	if s.Repeat != Weekly {
		s.Days = nil
	}
	if s.Repeat != Once {
		s.Date = ""
	}
	if s.Repeat != Interval {
		s.Every = 0
	}
	if s.Condition == "" {
		s.Cooldown = 0
	}
	return s
}

// Validate checks a schedule the person saves, at now, in local for an
// empty Zone. A Once in the past is an error.
func (s Schedule) Validate(now time.Time, local *time.Location) error {
	if s.Task == "" {
		return ErrNoTask
	}
	if len([]rune(s.Task)) > MaxTask || len([]rune(s.Condition)) > MaxTask {
		return ErrLongTask
	}
	if _, err := s.Location(local); err != nil {
		return err
	}
	for _, t := range s.Times {
		if _, err := parseTime(t); err != nil {
			return err
		}
	}
	if s.Cooldown < 0 || s.Cooldown > MaxCooldown {
		return ErrCooldown
	}
	switch s.Repeat {
	case Once:
		if len(s.Times) == 0 {
			return ErrNoTime
		}
		if _, err := time.Parse(time.DateOnly, s.Date); err != nil {
			return ErrDate
		}
		if _, ok := s.Next(now, local); !ok {
			return ErrPast
		}
	case Daily, Weekdays, Weekly:
		if len(s.Times) == 0 {
			return ErrNoTime
		}
		if len(s.Times) > MaxTimes {
			return ErrTooMany
		}
		if s.Repeat == Weekly && len(s.Days) == 0 {
			return ErrNoDays
		}
	case Interval:
		if len(s.Times) == 0 {
			return ErrNoTime
		}
		if !slices.Contains(Intervals, s.Every) {
			return ErrInterval
		}
	default:
		return ErrRepeat
	}
	return nil
}

// Location is the schedule's time zone; local for an empty Zone.
func (s Schedule) Location(local *time.Location) (*time.Location, error) {
	if s.Zone == "" {
		if local == nil {
			return time.Local, nil
		}
		return local, nil
	}
	loc, err := time.LoadLocation(s.Zone)
	if err != nil {
		return nil, ErrZone
	}
	return loc, nil
}

// Next is the first time the schedule is due after after, in local for an
// empty Zone; false when it is due no more (a Once that is past, or a
// schedule that cannot be read). A paused schedule has its times all the
// same.
func (s Schedule) Next(after time.Time, local *time.Location) (time.Time, bool) {
	loc, err := s.Location(local)
	if err != nil || len(s.Times) == 0 {
		return time.Time{}, false
	}
	at := func(y int, m time.Month, d int, t string) time.Time {
		h, mi := clock(t)
		return time.Date(y, m, d, h, mi, 0, 0, loc)
	}
	if s.Repeat == Once {
		day, err := time.ParseInLocation(time.DateOnly, s.Date, loc)
		if err != nil {
			return time.Time{}, false
		}
		t := at(day.Year(), day.Month(), day.Day(), s.Times[0])
		return t, t.After(after)
	}
	a := after.In(loc)
	// Eight days cover every week; one more for a time a change of
	// clocks moved past midnight.
	for i := 0; i <= 8; i++ {
		day := time.Date(a.Year(), a.Month(), a.Day()+i, 12, 0, 0, 0, loc)
		if !s.on(day.Weekday()) {
			continue
		}
		for _, t := range s.dayTimes() {
			if n := at(day.Year(), day.Month(), day.Day(), t); n.After(after) {
				return n, true
			}
		}
	}
	return time.Time{}, false
}

// on reports whether the schedule runs on a day of the week.
func (s Schedule) on(d time.Weekday) bool {
	switch s.Repeat {
	case Weekdays:
		return d != time.Saturday && d != time.Sunday
	case Weekly:
		return slices.Contains(s.Days, int(d))
	}
	return true
}

// dayTimes are the times of a day the schedule runs on, sorted.
func (s Schedule) dayTimes() []string {
	if s.Repeat != Interval {
		return s.Times
	}
	h, m := clock(s.Times[0])
	var out []string
	for ; h < 24 && s.Every > 0; h += s.Every {
		out = append(out, fmt.Sprintf("%02d:%02d", h, m))
	}
	return out
}

// Upcoming is the next n times the schedule is due after after.
func (s Schedule) Upcoming(after time.Time, local *time.Location, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		t, ok := s.Next(after, local)
		if !ok {
			break
		}
		out = append(out, t)
		after = t
	}
	return out
}

// Done reports whether a Once has run: it is due no more.
func (s Schedule) Done(local *time.Location) bool {
	_, ok := s.Next(s.Cursor, local)
	return s.Repeat == Once && !ok
}

// LastDone is the last run that ended done, for the next run to compare
// with; nil when there is none.
func (s Schedule) LastDone() *Run {
	for i := range s.Runs {
		if s.Runs[i].End == "done" {
			r := s.Runs[i]
			return &r
		}
	}
	return nil
}

// IsPayment reports whether an approval's rule is about moving money: a
// scheduled task nobody watches never pays (docs/notes/schedules.md).
func IsPayment(rule string) bool {
	r := strings.ToLower(rule)
	for _, w := range []string{"payment", "pay ", "purchase", "money", "transfer", "支付", "付款", "购买", "转账", "支払", "購入", "送金"} {
		if strings.Contains(r, w) {
			return true
		}
	}
	return r == "pay"
}
