package schedule

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

var shanghai = mustZone("Asia/Shanghai")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

func at(loc *time.Location, s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseTimes(t *testing.T) {
	got, err := ParseTimes("20:00, 8:00，12:30 8 8:00")
	if err != nil || !slices.Equal(got, []string{"08:00", "12:30", "20:00"}) {
		t.Fatalf("ParseTimes = %v, %v", got, err)
	}
	for _, bad := range []string{"24:00", "8:60", "noon", "8:5:1", "-1"} {
		if _, err := ParseTimes(bad); !errors.Is(err, ErrTime) {
			t.Errorf("ParseTimes(%q) = %v, want ErrTime", bad, err)
		}
	}
}

func TestNext(t *testing.T) {
	// Thursday 13 November 2026.
	now := at(shanghai, "2026-11-13 09:00")
	cases := []struct {
		name string
		s    Schedule
		want []string
	}{
		{"daily, several times", Schedule{Repeat: Daily, Times: []string{"08:00", "12:30", "20:00"}},
			[]string{"2026-11-13 12:30", "2026-11-13 20:00", "2026-11-14 08:00"}},
		{"weekdays skip the weekend", Schedule{Repeat: Weekdays, Times: []string{"08:00"}},
			[]string{"2026-11-14 08:00", "2026-11-17 08:00", "2026-11-18 08:00"}[0:0]},
		{"weekly", Schedule{Repeat: Weekly, Times: []string{"09:00"}, Days: []int{1, 5}},
			[]string{"2026-11-16 09:00", "2026-11-20 09:00", "2026-11-23 09:00"}},
		{"interval", Schedule{Repeat: Interval, Times: []string{"08:15"}, Every: 6},
			[]string{"2026-11-13 14:15", "2026-11-13 20:15", "2026-11-14 08:15"}},
		{"once", Schedule{Repeat: Once, Date: "2026-11-14", Times: []string{"07:30"}},
			[]string{"2026-11-14 07:30"}},
		{"once in the past", Schedule{Repeat: Once, Date: "2026-11-13", Times: []string{"08:59"}}, nil},
	}
	cases[1].want = []string{"2026-11-16 08:00", "2026-11-17 08:00", "2026-11-18 08:00"}
	for _, c := range cases {
		got := c.s.Upcoming(now, shanghai, 3)
		var s []string
		for _, g := range got {
			s = append(s, g.In(shanghai).Format("2006-01-02 15:04"))
		}
		if !slices.Equal(s, c.want) {
			t.Errorf("%s: Upcoming = %v, want %v", c.name, s, c.want)
		}
	}
}

func TestNextTimeZone(t *testing.T) {
	ny := mustZone("America/New_York")
	s := Schedule{Repeat: Daily, Times: []string{"08:00"}, Zone: "America/New_York"}
	// 2026-11-01 is the end of daylight saving time in New York.
	got := s.Upcoming(at(ny, "2026-10-31 07:00"), shanghai, 2)
	for _, g := range got {
		if l := g.In(ny); l.Hour() != 8 || l.Minute() != 0 {
			t.Errorf("due at %v, want 08:00 New York time", l)
		}
	}
	if got[1].Sub(got[0]) != 25*time.Hour {
		t.Errorf("across the change of clocks: %v apart, want 25h", got[1].Sub(got[0]))
	}
	// An empty zone is the host's.
	s.Zone = ""
	if n, _ := s.Next(at(shanghai, "2026-11-13 07:00"), shanghai); !n.Equal(at(shanghai, "2026-11-13 08:00")) {
		t.Errorf("Next in the host's zone = %v", n)
	}
}

func TestValidate(t *testing.T) {
	now := at(shanghai, "2026-11-13 09:00")
	ok := Schedule{Task: "Check the weather", Repeat: Daily, Times: []string{"08:00"}}
	if err := ok.Validate(now, shanghai); err != nil {
		t.Fatalf("Validate = %v", err)
	}
	cases := []struct {
		change func(*Schedule)
		want   error
	}{
		{func(s *Schedule) { s.Task = "" }, ErrNoTask},
		{func(s *Schedule) { s.Repeat = "monthly" }, ErrRepeat},
		{func(s *Schedule) { s.Times = nil }, ErrNoTime},
		{func(s *Schedule) { s.Times = []string{"25:00"} }, ErrTime},
		{func(s *Schedule) { s.Repeat = Weekly }, ErrNoDays},
		{func(s *Schedule) { s.Repeat, s.Every = Interval, 5 }, ErrInterval},
		{func(s *Schedule) { s.Zone = "Mars/Olympus" }, ErrZone},
		{func(s *Schedule) { s.Repeat, s.Date = Once, "2026-11-13" }, ErrPast},
		{func(s *Schedule) { s.Repeat, s.Date = Once, "13/11/2026" }, ErrDate},
		{func(s *Schedule) { s.Cooldown = -1 }, ErrCooldown},
	}
	for i, c := range cases {
		s := ok
		c.change(&s)
		if err := s.Validate(now, shanghai); !errors.Is(err, c.want) {
			t.Errorf("case %d: Validate = %v, want %v", i, err, c.want)
		}
	}
}

func TestIsPayment(t *testing.T) {
	for _, r := range []string{"Payments", "支付", "Purchases"} {
		if !IsPayment(r) {
			t.Errorf("IsPayment(%q) = false", r)
		}
	}
	for _, r := range []string{"Commits", "Send messages", "Every action", ""} {
		if IsPayment(r) {
			t.Errorf("IsPayment(%q) = true", r)
		}
	}
}

// host is a fake Host.
type host struct {
	mu      sync.Mutex
	check   string
	run     func(Schedule, Run) Run
	ran     []Run
	reports []Run
	done    chan struct{}
}

func newHost() *host { return &host{done: make(chan struct{}, 10)} }

func (h *host) Check(Schedule) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.check
}

func (h *host) Run(ctx context.Context, s Schedule, r Run) Run {
	h.mu.Lock()
	h.ran = append(h.ran, r)
	f := h.run
	h.mu.Unlock()
	if f != nil {
		return f(s, r)
	}
	r.End, r.Summary = "done", "Did it."
	return r
}

func (h *host) Report(s Schedule, r Run) {
	h.mu.Lock()
	h.reports = append(h.reports, r)
	h.mu.Unlock()
	if r.End != Skipped {
		h.done <- struct{}{}
	}
}

func (h *host) last() Run {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reports[len(h.reports)-1]
}

func (h *host) wait(t *testing.T) {
	t.Helper()
	select {
	case <-h.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not end")
	}
}

func newStore(t *testing.T) *Store {
	st, err := Open(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.SetLocal(shanghai)
	return st
}

func TestTick(t *testing.T) {
	st := newStore(t)
	sc := New(st)
	defer sc.Close()
	h := newHost()
	created := at(shanghai, "2026-11-13 07:00")
	s, err := st.Save(Schedule{Task: "Check the weather", Repeat: Daily, Times: []string{"08:00", "20:00"}}, created)
	if err != nil {
		t.Fatal(err)
	}
	if next := sc.Tick(at(shanghai, "2026-11-13 07:59"), h); !next.Equal(at(shanghai, "2026-11-13 08:00")) {
		t.Fatalf("before its time: next tick %v", next)
	}
	if len(h.ran) != 0 {
		t.Fatal("ran before its time")
	}

	// At 08:00 it runs and the person is told.
	next := sc.Tick(at(shanghai, "2026-11-13 08:00"), h)
	h.wait(t)
	if !next.Equal(at(shanghai, "2026-11-13 20:00")) {
		t.Errorf("after the run: next tick %v", next)
	}
	if r := h.last(); r.End != "done" || !r.Notify || !r.Due.Equal(at(shanghai, "2026-11-13 08:00")) {
		t.Errorf("run = %+v", r)
	}

	// Locked at 20:00: skipped at once, and the person is told.
	h.check = SkipLocked
	sc.Tick(at(shanghai, "2026-11-13 20:00"), h)
	if r := h.last(); r.End != Skipped || r.Reason != SkipLocked || !r.Notify {
		t.Errorf("locked: %+v", r)
	}

	// Busy at 08:00 the next day: it waits, then is skipped.
	h.check = SkipBusy
	if next := sc.Tick(at(shanghai, "2026-11-14 08:00"), h); !next.Equal(at(shanghai, "2026-11-14 08:01")) {
		t.Errorf("waiting: next tick %v", next)
	}
	if n := len(h.reports); n != 2 {
		t.Fatalf("waiting reported something: %d reports", n)
	}
	sc.Tick(at(shanghai, "2026-11-14 08:16"), h)
	if r := h.last(); r.End != Skipped || r.Reason != SkipBusy {
		t.Errorf("busy too long: %+v", r)
	}

	// Pinky did not run for two days: one missed run for the four times
	// (the last, and three before it), not a storm.
	h.check = ""
	sc.Tick(at(shanghai, "2026-11-16 09:00"), h)
	if r := h.last(); r.End != Skipped || r.Reason != SkipNotRunning || r.Missed != 3 {
		t.Errorf("missed: %+v", r)
	}
	if len(h.ran) != 1 {
		t.Errorf("ran %d times, want 1", len(h.ran))
	}
	got, _ := st.Get(s.ID)
	if len(got.Runs) != 4 || got.Runs[0].Reason != SkipNotRunning {
		t.Errorf("runs = %+v", got.Runs)
	}

	// Paused, it does not run; resumed, not what was due meanwhile.
	st.SetPaused(s.ID, true, at(shanghai, "2026-11-16 09:00"))
	sc.Tick(at(shanghai, "2026-11-16 20:05"), h)
	st.SetPaused(s.ID, false, at(shanghai, "2026-11-16 21:00"))
	sc.Tick(at(shanghai, "2026-11-16 21:00"), h)
	if len(h.ran) != 1 || len(h.reports) != 4 {
		t.Errorf("paused: %d runs, %d reports", len(h.ran), len(h.reports))
	}
}

func TestCondition(t *testing.T) {
	st := newStore(t)
	sc := New(st)
	defer sc.Close()
	h := newHost()
	met := true
	h.run = func(s Schedule, r Run) Run {
		r.End, r.Summary, r.Met = "done", "Rain at 3 pm.", &met
		return r
	}
	s, _ := st.Save(Schedule{Task: "Check the weather", Condition: "it will rain", Cooldown: 24 * 60,
		Repeat: Interval, Times: []string{"08:00"}, Every: 4}, at(shanghai, "2026-11-13 07:00"))

	sc.Tick(at(shanghai, "2026-11-13 08:00"), h)
	h.wait(t)
	if r := h.last(); !r.Notify {
		t.Errorf("the condition held: not told: %+v", r)
	}
	// Held again within the cooldown: not told again.
	sc.Tick(at(shanghai, "2026-11-13 12:00"), h)
	h.wait(t)
	if r := h.last(); r.Notify {
		t.Errorf("within the cooldown: told again")
	}
	// It did not hold: not told.
	met = false
	sc.Tick(at(shanghai, "2026-11-13 16:00"), h)
	h.wait(t)
	if r := h.last(); r.Notify {
		t.Errorf("did not hold: told")
	}
	got, _ := st.Get(s.ID)
	if got.LastMet == nil || !got.LastMet.Due.Equal(at(shanghai, "2026-11-13 12:00")) {
		t.Errorf("LastMet = %+v", got.LastMet)
	}
	if d := got.LastDone(); d == nil || !d.Due.Equal(at(shanghai, "2026-11-13 16:00")) {
		t.Errorf("LastDone = %+v", d)
	}
	// A day later, held: told again.
	met = true
	sc.Tick(at(shanghai, "2026-11-14 08:00"), h)
	h.wait(t)
	if r := h.last(); !r.Notify {
		t.Errorf("after the cooldown: not told")
	}
	// Run now: the person is there, so not told.
	if err := sc.RunNow(s.ID, at(shanghai, "2026-11-14 09:00"), h); err != nil {
		t.Fatal(err)
	}
	h.wait(t)
	if r := h.last(); r.Notify || !r.Manual {
		t.Errorf("run now: %+v", r)
	}
}

func TestStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	st, _ := Open(path)
	now := at(shanghai, "2026-11-13 09:00")
	if _, err := st.Save(Schedule{Task: " ", Repeat: Daily, Times: []string{"8:00"}}, now); !errors.Is(err, ErrNoTask) {
		t.Errorf("empty task: %v", err)
	}
	s, err := st.Save(Schedule{Task: "Look at the news", Repeat: Weekly, Days: []int{5, 1, 5}, Times: []string{"20:00", "8:00"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Days, []int{1, 5}) || !slices.Equal(s.Times, []string{"08:00", "20:00"}) {
		t.Errorf("not normalized: %+v", s)
	}
	// A run left running when Pinky stopped reads as interrupted.
	st.begin(s.ID, Run{Due: now, End: Running, Started: now})
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := st2.Get(s.ID)
	if !ok || got.Runs[0].Reason != Interrupted || got.Runs[0].End != "error" {
		t.Errorf("reopened: %+v", got)
	}
	// Ten runs at most.
	for i := range 12 {
		st2.finish(s.ID, Run{Due: now.Add(time.Duration(i) * time.Minute), End: "done"})
	}
	got, _ = st2.Get(s.ID)
	if len(got.Runs) != MaxRuns {
		t.Errorf("%d runs kept", len(got.Runs))
	}
	if err := st2.Delete(s.ID); err != nil || len(st2.List()) != 0 {
		t.Errorf("Delete: %v, %d left", err, len(st2.List()))
	}
}
