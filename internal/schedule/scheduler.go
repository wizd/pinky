package schedule

import (
	"context"
	"sync"
	"time"
)

// Host runs scheduled tasks: desktop Pinky or Pinky on the phone.
type Host interface {
	// Check says whether the schedule's phone can take its task now: ""
	// when it can, or why not, one of the Skip constants. SkipBusy and
	// SkipInUse wait (Scheduler.Wait) before the time is skipped; the
	// others skip it at once.
	Check(s Schedule) string
	// Run does the schedule's task and returns r with how it ended (End,
	// Summary, Met and what it used). It blocks until the task ends, or
	// until ctx does.
	Run(ctx context.Context, s Schedule, r Run) Run
	// Report hears each run that ended or was skipped, as recorded; the
	// host tells the person when r.Notify is set.
	Report(s Schedule, r Run)
}

// Defaults of a Scheduler.
const (
	// DefaultGrace: a time due longer ago than this, while Pinky was not
	// running or the computer slept, is not run late but reported missed.
	DefaultGrace = 30 * time.Minute
	// DefaultWait: a time due while the phone is busy or in the person's
	// hands waits this long for it, then is skipped.
	DefaultWait = 15 * time.Minute
	// retry is how soon a time that waits is looked at again.
	retry = time.Minute
)

// Scheduler runs a Store's schedules through a Host, one task at a time.
type Scheduler struct {
	Store *Store
	Grace time.Duration
	Wait  time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	running string // the schedule whose task runs
	waiting bool   // a due time waits for the phone or the running task
	ended   []func()
}

// New returns a Scheduler for st.
func New(st *Store) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{Store: st, Grace: DefaultGrace, Wait: DefaultWait, ctx: ctx, cancel: cancel}
}

// OnEnded calls f, from any goroutine, when a scheduled task ends: a time
// that waited for it may run now.
func (sc *Scheduler) OnEnded(f func()) {
	sc.mu.Lock()
	sc.ended = append(sc.ended, f)
	sc.mu.Unlock()
}

// Running returns the schedule whose task runs.
func (sc *Scheduler) Running() (string, bool) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.running, sc.running != ""
}

// Tick handles the times due at now: it reports those missed while Pinky
// was not running, skips those the phone cannot take, and starts the task
// of one in the background. It returns when to tick again: the next time
// due, or soon while a time waits; zero when nothing is due again.
func (sc *Scheduler) Tick(now time.Time, h Host) time.Time {
	waiting := false
	for _, d := range sc.Store.dueAt(now) {
		s := d.s
		if now.Sub(d.at) > sc.Grace {
			sc.skip(h, s, Run{Due: d.at, Missed: d.missed}, SkipNotRunning, now)
			continue
		}
		sc.mu.Lock()
		busy := sc.running != ""
		sc.mu.Unlock()
		if busy {
			// Another schedule's task runs: wait for it, as long as the
			// time is not missed.
			waiting = true
			continue
		}
		why := h.Check(s)
		switch {
		case why == "":
			sc.start(h, s, Run{Due: d.at, Missed: d.missed}, now)
		case (why == SkipBusy || why == SkipInUse) && now.Sub(d.at) < sc.Wait:
			waiting = true
		default:
			sc.skip(h, s, Run{Due: d.at, Missed: d.missed}, why, now)
		}
	}
	sc.mu.Lock()
	sc.waiting = waiting
	sc.mu.Unlock()
	return sc.NextTick(now)
}

// NextTick is when Tick should run next after now; zero when nothing is
// due again.
func (sc *Scheduler) NextTick(now time.Time) time.Time {
	next := sc.Store.NextDue(now)
	sc.mu.Lock()
	waiting := sc.waiting
	sc.mu.Unlock()
	soon := now.Add(retry)
	// A time still due after Tick waits for the phone or the running
	// task: look again soon, not at once.
	if !next.IsZero() && !next.After(now) {
		next = soon
	}
	if waiting && (next.IsZero() || soon.Before(next)) {
		next = soon
	}
	return next
}

// RunNow runs a schedule's task at once, as the person asked: whatever its
// times, paused or not. The person is there, so it is not told of the
// result (Run.Manual).
func (sc *Scheduler) RunNow(id string, now time.Time, h Host) error {
	s, ok := sc.Store.Get(id)
	if !ok {
		return ErrNotFound
	}
	sc.mu.Lock()
	busy := sc.running != ""
	sc.mu.Unlock()
	if busy {
		return ErrRunning
	}
	sc.start(h, s, Run{Due: now, Manual: true}, now)
	return nil
}

// start runs a schedule's task in the background.
func (sc *Scheduler) start(h Host, s Schedule, r Run, now time.Time) {
	sc.mu.Lock()
	if sc.running != "" {
		sc.mu.Unlock()
		return
	}
	sc.running = s.ID
	sc.mu.Unlock()
	r.Started, r.End = now, Running
	_ = sc.Store.begin(s.ID, r)
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		out := h.Run(sc.ctx, s, r)
		out.Due, out.Manual, out.Missed = r.Due, r.Manual, r.Missed
		if out.Started.IsZero() {
			out.Started = r.Started
		}
		if out.Ended.IsZero() {
			out.Ended = time.Now()
		}
		if out.End == "" || out.End == Running {
			out.End = "error"
		}
		sc.mu.Lock()
		sc.running = ""
		sc.mu.Unlock()
		if got, rec, err := sc.Store.finish(s.ID, out); err == nil {
			h.Report(got, rec)
		}
		sc.mu.Lock()
		fs := sc.ended
		sc.mu.Unlock()
		for _, f := range fs {
			f()
		}
	}()
}

// skip records a time that did not run, and why.
func (sc *Scheduler) skip(h Host, s Schedule, r Run, why string, now time.Time) {
	r.End, r.Reason, r.Ended = Skipped, why, now
	if got, rec, err := sc.Store.finish(s.ID, r); err == nil {
		h.Report(got, rec)
	}
}

// Close stops the running task and waits for it to end.
func (sc *Scheduler) Close() {
	sc.cancel()
	sc.wg.Wait()
}

// Loop ticks until Close: at each time due, at least every minute (the
// computer may sleep, or its clock change), and after each change of the
// schedules or end of a task. Desktop Pinky runs it; Pinky on Android
// ticks when the app's alarm wakes it instead.
func (sc *Scheduler) Loop(h Host) {
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	sc.Store.OnChange(poke)
	sc.OnEnded(poke)
	sc.wg.Add(1)
	go func() {
		defer sc.wg.Done()
		for {
			now := time.Now()
			wait := time.Minute
			if next := sc.Tick(now, h); !next.IsZero() && next.Sub(now) < wait {
				wait = max(next.Sub(now), time.Second)
			}
			t := time.NewTimer(wait)
			select {
			case <-sc.ctx.Done():
				t.Stop()
				return
			case <-wake:
			case <-t.C:
			}
			t.Stop()
		}
	}()
}
