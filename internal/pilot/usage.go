package pilot

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// UsageRow is what one model of one service used on one day, as the
// services reported it.
type UsageRow struct {
	Day     string `json:"day"`     // the day in local time, 2006-01-02
	Service string `json:"service"` // the service's ID (Services)
	// Host is the address's host for a custom entry, so that two
	// services typed by hand stay apart; empty for the others.
	Host       string `json:"host,omitempty"`
	Model      string `json:"model"`
	Requests   int    `json:"requests"`
	Input      int64  `json:"input"`
	Output     int64  `json:"output"`
	CacheRead  int64  `json:"cache_read,omitempty"`
	CacheWrite int64  `json:"cache_write,omitempty"`
	// Cost is in US dollars, for the requests whose prices Pinky knew;
	// Unpriced counts the tokens of the others.
	Cost     float64 `json:"cost,omitempty"`
	Unpriced int64   `json:"unpriced,omitempty"`
}

// Tokens is every token the row counts, sent and received.
func (r UsageRow) Tokens() int64 { return r.Input + r.Output + r.CacheRead + r.CacheWrite }

// Priced reports whether Cost covers all of the row's tokens.
func (r UsageRow) Priced() bool { return r.Unpriced == 0 }

// UsageKeepDays is how many days Usage keeps.
const UsageKeepDays = 180

// Usage counts the tokens Pinky's own agent uses, by day, service and
// model, in a small JSON file on this device only. Nothing of it leaves
// the device. A nil *Usage counts nothing.
type Usage struct {
	path string

	mu     sync.Mutex
	rows   []UsageRow
	loaded bool
}

// OpenUsage counts usage in the file at path; empty keeps it in memory.
// The file is read when first needed.
func OpenUsage(path string) *Usage { return &Usage{path: path} }

type usageFile struct {
	Version int        `json:"version"`
	Rows    []UsageRow `json:"rows"`
}

// add counts a request's usage at t.
func (u *Usage) add(t time.Time, cfg Config, us usage) {
	if u == nil || us == (usage{}) {
		return
	}
	cfg = cfg.Normalize()
	row := UsageRow{Day: t.Local().Format(time.DateOnly), Service: cfg.Service, Model: cfg.Model}
	if s, ok := lookup(cfg.Service); !ok || s.Custom {
		row.Host = host(cfg.BaseURL)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.load()
	i := slices.IndexFunc(u.rows, func(r UsageRow) bool {
		return r.Day == row.Day && r.Service == row.Service && r.Host == row.Host && r.Model == row.Model
	})
	if i < 0 {
		u.rows = append(u.rows, row)
		i = len(u.rows) - 1
	}
	r := &u.rows[i]
	r.Requests++
	r.Input += us.input
	r.Output += us.output
	r.CacheRead += us.cacheRead
	r.CacheWrite += us.cacheWrite
	if p, ok := priceOf(cfg); ok {
		r.Cost += p.at(t).cost(us)
	} else {
		r.Unpriced += us.input + us.output + us.cacheRead + us.cacheWrite
	}
	u.prune(t)
	u.save()
}

// Rows returns the rows of the days from since on (2006-01-02; empty for
// all), the newest day first, and within a day the most used first.
func (u *Usage) Rows(since string) []UsageRow {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.load()
	var out []UsageRow
	for _, r := range u.rows {
		if r.Day >= since {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b UsageRow) int {
		if c := cmp.Compare(b.Day, a.Day); c != 0 {
			return c
		}
		return cmp.Compare(b.Tokens(), a.Tokens())
	})
	return out
}

// Clear forgets all usage.
func (u *Usage) Clear() error {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rows, u.loaded = nil, true
	if u.path == "" {
		return nil
	}
	if err := os.Remove(u.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (u *Usage) load() {
	if u.loaded {
		return
	}
	u.loaded = true
	if u.path == "" {
		return
	}
	data, err := os.ReadFile(u.path)
	if err != nil {
		return
	}
	var f usageFile
	if json.Unmarshal(data, &f) == nil {
		u.rows = f.Rows
	}
}

// prune drops the days before the last UsageKeepDays.
func (u *Usage) prune(now time.Time) {
	first := now.Local().AddDate(0, 0, -UsageKeepDays+1).Format(time.DateOnly)
	u.rows = slices.DeleteFunc(u.rows, func(r UsageRow) bool { return r.Day < first })
}

// save writes the file; a failure only costs the count, never the task.
func (u *Usage) save() {
	if u.path == "" {
		return
	}
	data, err := json.Marshal(usageFile{Version: 1, Rows: u.rows})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(u.path), 0o700)
	tmp := u.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, u.path)
	}
}

// UsageTotal sums rows.
type UsageTotal struct {
	Tokens   int64
	Cost     float64 // US dollars, of the priced tokens
	Unpriced int64   // tokens whose prices Pinky did not know
	Requests int
}

// Total sums rows.
func Total(rows []UsageRow) UsageTotal {
	var t UsageTotal
	for _, r := range rows {
		t.Tokens += r.Tokens()
		t.Cost += r.Cost
		t.Unpriced += r.Unpriced
		t.Requests += r.Requests
	}
	return t
}
