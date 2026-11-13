package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Store keeps the schedules, in a JSON file or in memory.
type Store struct {
	path string

	mu      sync.Mutex
	list    []Schedule // in the order they were made
	local   *time.Location
	changed []func()
}

// file is the store's file.
type file struct {
	Version   int        `json:"version"`
	Schedules []Schedule `json:"schedules"`
}

// Interrupted is the Reason of a run that was still running when Pinky
// last stopped: its result was never known.
const Interrupted = "interrupted"

// Open reads the schedules in path, which need not exist yet; an empty
// path keeps them in memory.
func Open(path string) (*Store, error) {
	st := &Store{path: path, local: time.Local}
	if path == "" {
		return st, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	st.list = f.Schedules
	// A run that was running when Pinky stopped never ended.
	for i := range st.list {
		for j := range st.list[i].Runs {
			if r := &st.list[i].Runs[j]; r.End == Running {
				r.End, r.Reason = "error", Interrupted
			}
		}
	}
	return st, nil
}

// SetLocal sets the time zone of schedules without one: the computer's or
// the phone's. Go on Android knows no local zone, so the phone's app
// names it.
func (st *Store) SetLocal(loc *time.Location) {
	if loc == nil {
		return
	}
	st.mu.Lock()
	st.local = loc
	st.mu.Unlock()
}

// Local is the time zone of schedules without one.
func (st *Store) Local() *time.Location {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.local
}

// OnChange calls f, from any goroutine, after each change.
func (st *Store) OnChange(f func()) {
	st.mu.Lock()
	st.changed = append(st.changed, f)
	st.mu.Unlock()
}

// List returns the schedules, in the order they were made.
func (st *Store) List() []Schedule {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]Schedule, len(st.list))
	for i, s := range st.list {
		out[i] = s.clone()
	}
	return out
}

// Get returns a schedule.
func (st *Store) Get(id string) (Schedule, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if i := st.index(id); i >= 0 {
		return st.list[i].clone(), true
	}
	return Schedule{}, false
}

// Save adds a schedule (an empty ID) or changes one: its task, times,
// condition and pause; its runs stay. It is next due at its first time
// after now: a change never runs a time that has passed.
func (st *Store) Save(s Schedule, now time.Time) (Schedule, error) {
	s = s.Normalize()
	st.mu.Lock()
	if err := s.Validate(now, st.local); err != nil {
		st.mu.Unlock()
		return Schedule{}, err
	}
	s.Cursor = now
	i := st.index(s.ID)
	if s.ID != "" && i < 0 {
		st.mu.Unlock()
		return Schedule{}, ErrNotFound
	}
	if i < 0 {
		s.ID, s.Created, s.Runs, s.LastMet, s.Notified = newID(), now, nil, nil, time.Time{}
		st.list = append(st.list, s)
	} else {
		old := st.list[i]
		s.Created, s.Runs, s.LastMet, s.Notified = old.Created, old.Runs, old.LastMet, old.Notified
		if s.Condition != old.Condition {
			// Another condition: the last time the old one held says
			// nothing about the new one.
			s.LastMet, s.Notified = nil, time.Time{}
		}
		st.list[i] = s
	}
	out := s.clone()
	return out, st.commit()
}

// Delete removes a schedule.
func (st *Store) Delete(id string) error {
	st.mu.Lock()
	i := st.index(id)
	if i < 0 {
		st.mu.Unlock()
		return ErrNotFound
	}
	st.list = slices.Delete(st.list, i, i+1)
	return st.commit()
}

// SetPaused pauses a schedule or resumes it. Resumed, it is next due at
// its first time after now: what was due while it was paused does not run.
func (st *Store) SetPaused(id string, paused bool, now time.Time) error {
	return st.update(id, func(s *Schedule) {
		if s.Paused && !paused {
			s.Cursor = now
		}
		s.Paused = paused
	})
}

// NextDue is when the first schedule is due next after now, among those
// not paused; zero when none is.
func (st *Store) NextDue(now time.Time) time.Time {
	st.mu.Lock()
	defer st.mu.Unlock()
	var next time.Time
	for _, s := range st.list {
		if s.Paused {
			continue
		}
		t, ok := s.Next(s.Cursor, st.local)
		if !ok {
			continue
		}
		if t.Before(now) {
			t = now
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}

// due is a time a schedule is due: the latest of its times at or before
// now, and how many earlier ones it passed over.
type due struct {
	s      Schedule
	at     time.Time
	missed int
}

// dueAt lists the schedules due at now.
func (st *Store) dueAt(now time.Time) []due {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []due
	for _, s := range st.list {
		if s.Paused {
			continue
		}
		t, ok := s.Next(s.Cursor, st.local)
		if !ok || t.After(now) {
			continue
		}
		d := due{s: s.clone(), at: t}
		for i := 0; i < 100_000; i++ {
			n, ok := s.Next(d.at, st.local)
			if !ok || n.After(now) {
				break
			}
			d.at = n
			d.missed++
		}
		out = append(out, d)
	}
	return out
}

// begin records a run that starts, and moves the schedule past its time.
func (st *Store) begin(id string, r Run) error {
	return st.update(id, func(s *Schedule) {
		if !r.Manual && r.Due.After(s.Cursor) {
			s.Cursor = r.Due
		}
		s.Runs = append([]Run{r}, s.Runs...)
		if len(s.Runs) > MaxRuns {
			s.Runs = s.Runs[:MaxRuns]
		}
	})
}

// finish records how a run ended, in place of the running one begin
// recorded (or as a new one for a skipped time), and decides whether the
// person is told of it (Run.Notify). It returns the run as recorded.
func (st *Store) finish(id string, r Run) (Schedule, Run, error) {
	var out Schedule
	err := st.update(id, func(s *Schedule) {
		// The cooldown counts from the times the runs were due, not when
		// they ended: "once a day" at 8:00 every day tells every day.
		r.Notify = notify(s, r, r.Due)
		if r.Notify && s.Condition != "" && r.Met != nil && *r.Met && !r.Manual {
			s.Notified = r.Due
		}
		if r.Met != nil && *r.Met {
			m := r
			s.LastMet = &m
		}
		if r.End == Skipped && !r.Manual && r.Due.After(s.Cursor) {
			s.Cursor = r.Due
		}
		replaced := false
		for i := range s.Runs {
			if s.Runs[i].End == Running && s.Runs[i].Due.Equal(r.Due) && s.Runs[i].Manual == r.Manual {
				s.Runs[i], replaced = r, true
				break
			}
		}
		if !replaced {
			s.Runs = append([]Run{r}, s.Runs...)
			if len(s.Runs) > MaxRuns {
				s.Runs = s.Runs[:MaxRuns]
			}
		}
		out = s.clone()
	})
	return out, r, err
}

// notify decides whether the person is told of a run: of every run nobody
// pressed Run now for, but those they stopped themselves and those whose
// condition did not hold, or held again before Cooldown.
func notify(s *Schedule, r Run, at time.Time) bool {
	switch {
	case r.Manual:
		return false // they pressed Run now: they see it
	case r.End == "stopped":
		return false // they stopped it
	case r.End == Skipped:
		return true
	case s.Condition != "" && r.End == "done" && r.Met != nil:
		if !*r.Met {
			return false
		}
		return s.Cooldown == 0 || s.Notified.IsZero() || at.Sub(s.Notified) >= time.Duration(s.Cooldown)*time.Minute
	}
	return true
}

func (st *Store) update(id string, f func(*Schedule)) error {
	st.mu.Lock()
	i := st.index(id)
	if i < 0 {
		st.mu.Unlock()
		return ErrNotFound
	}
	f(&st.list[i])
	return st.commit()
}

// commit writes the file and tells who listens; st.mu is held, and
// released.
func (st *Store) commit() error {
	var err error
	if st.path != "" {
		err = writeFile(st.path, file{Version: 1, Schedules: st.list})
	}
	calls := slices.Clone(st.changed)
	st.mu.Unlock()
	for _, f := range calls {
		f()
	}
	return err
}

func writeFile(path string, f file) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (st *Store) index(id string) int {
	if id == "" {
		return -1
	}
	for i, s := range st.list {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func (s Schedule) clone() Schedule {
	s.Times = slices.Clone(s.Times)
	s.Days = slices.Clone(s.Days)
	s.Runs = slices.Clone(s.Runs)
	if s.LastMet != nil {
		m := *s.LastMet
		s.LastMet = &m
	}
	return s
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
