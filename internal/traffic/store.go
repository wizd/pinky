package traffic

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bounds of what a Store keeps, all phones together.
const (
	MaxRequests = 2000
	MaxBytes    = 32 << 20
	// BodyMax is how much of each body a Store keeps.
	BodyMax = 16 << 10
	// BlockedGap is how close blocked connections to one host follow each
	// other for the store to keep them as one row (Request.Count).
	BlockedGap = time.Minute
)

// Store keeps captured requests in memory only, oldest dropped first once
// it holds MaxRequests or MaxBytes. The gateway drops a phone's requests
// when its task ends. The zero value is not ready; use NewStore.
type Store struct {
	mu       sync.Mutex
	max      int
	maxBytes int
	next     int
	list     []*entry // in the order they were added
	bytes    int
	dropped  map[string]int // by phone: how many the caps pushed out
	// gens counts each phone's drops, so that a request that started
	// before one is not kept after it (AddSince).
	gens map[string]int
}

type entry struct {
	r      Request
	bodies Bodies
	size   int
}

// NewStore returns a Store with the given bounds; 0 takes the defaults.
func NewStore(maxRequests, maxBytes int) *Store {
	if maxRequests <= 0 {
		maxRequests = MaxRequests
	}
	if maxBytes <= 0 {
		maxBytes = MaxBytes
	}
	return &Store{max: maxRequests, maxBytes: maxBytes, dropped: map[string]int{}, gens: map[string]int{}}
}

// Gen returns a phone's generation: how many times its requests were
// dropped.
func (s *Store) Gen(device string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gens[device]
}

// AddSince keeps a request that started at generation gen, unless the
// phone's requests were dropped since (its task ended while the request
// ran); it returns the id and whether it was kept.
func (s *Store) AddSince(gen int, r Request, b Bodies) (string, bool) {
	return s.add(&gen, r, b)
}

// Add keeps a request and its bodies, cut to BodyMax each, and returns
// its id.
func (s *Store) Add(r Request, b Bodies) string {
	id, _ := s.add(nil, r, b)
	return id
}

func (s *Store) add(gen *int, r Request, b Bodies) (string, bool) {
	b.Request, b.RequestCut = cutBody(b.Request, b.RequestCut)
	b.Response, b.ResponseCut = cutBody(b.Response, b.ResponseCut)
	size := len(b.Request) + len(b.Response) + len(r.URL) + 256
	for _, h := range r.Headers {
		size += len(h.Name) + len(h.Value) + 4
	}
	for _, h := range r.ResponseHeaders {
		size += len(h.Name) + len(h.Value) + 4
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != nil && *gen != s.gens[r.Device] {
		return "", false
	}
	if r.Blocked {
		if e := s.repeatLocked(r); e != nil {
			e.r.Count = e.r.Connections() + r.Connections()
			// Connections are kept as they open, nearly in order.
			last := e.r.Latest()
			if l := r.Latest(); l.After(last) {
				last = l
			}
			if r.Start.Before(e.r.Start) {
				e.r.Start = r.Start
			}
			e.r.Last = last
			if last.After(e.r.End) {
				e.r.End = last
			}
			return e.r.ID, true
		}
	}
	s.next++
	r.ID = strconv.Itoa(s.next)
	s.list = append(s.list, &entry{r: r, bodies: b, size: size})
	s.bytes += size
	n := 0
	for len(s.list)-n > 1 && (len(s.list)-n > s.max || s.bytes > s.maxBytes) {
		e := s.list[n]
		s.bytes -= e.size
		s.dropped[e.r.Device]++
		s.list[n] = nil
		n++
	}
	if n > 0 {
		s.list = slices.Delete(s.list, 0, n)
	}
	return r.ID, true
}

// repeatLocked returns the phone's blocked row that a blocked connection
// r repeats: the newest one to the same URL whose span reaches within
// BlockedGap of r's start. The caller holds s.mu.
func (s *Store) repeatLocked(r Request) *entry {
	for i := len(s.list) - 1; i >= 0; i-- {
		e := s.list[i]
		if e.r.Device != r.Device || !e.r.Blocked || e.r.URL != r.URL || e.r.Method != r.Method {
			continue
		}
		if r.Start.Sub(e.r.Latest()) <= BlockedGap && e.r.Start.Sub(r.Latest()) <= BlockedGap {
			return e
		}
		return nil
	}
	return nil
}

func cutBody(b []byte, cut bool) ([]byte, bool) {
	if len(b) > BodyMax {
		return bytes.Clone(b[:BodyMax]), true
	}
	return b, cut
}

// Requests returns the requests that match q, by when they started.
func (s *Store) Requests(q Query) []Request {
	s.mu.Lock()
	var out []Request
	for _, e := range s.list {
		if q.Wanted(e.r) {
			out = append(out, e.r)
		}
	}
	s.mu.Unlock()
	slices.SortStableFunc(out, func(a, b Request) int { return a.Start.Compare(b.Start) })
	return out
}

// Bodies returns one of a phone's requests' bodies, decompressed when
// they were gzip or deflate, each at most max bytes.
func (s *Store) Bodies(device, id string, max int) (Bodies, error) {
	s.mu.Lock()
	var b Bodies
	found := false
	for _, e := range s.list {
		if e.r.ID == id && e.r.Device == device {
			b, found = e.bodies, true
			break
		}
	}
	s.mu.Unlock()
	if !found {
		return Bodies{}, ErrGone
	}
	b.Request, b.RequestCut = decoded(b.Request, b.RequestEncoding, b.RequestCut, max)
	b.Response, b.ResponseCut = decoded(b.Response, b.ResponseEncoding, b.ResponseCut, max)
	return b, nil
}

// decoded returns a body as the app reads it: decompressed when it can
// be, as far as the kept part goes, and at most max bytes.
func decoded(body []byte, enc string, cut bool, max int) ([]byte, bool) {
	var r io.Reader
	switch strings.ToLower(strings.TrimSpace(enc)) {
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return clip(body, cut, max)
		}
		r = zr
	case "deflate":
		r = flate.NewReader(bytes.NewReader(body))
	default:
		return clip(body, cut, max)
	}
	var out bytes.Buffer
	// A cut stream ends early with an error; what came before it stands.
	_, _ = io.Copy(&out, io.LimitReader(r, int64(max)+1))
	return clip(out.Bytes(), cut, max)
}

func clip(b []byte, cut bool, max int) ([]byte, bool) {
	if len(b) > max {
		return b[:max], true
	}
	return b, cut
}

// Drop forgets a phone's requests.
func (s *Store) Drop(device string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = slices.DeleteFunc(s.list, func(e *entry) bool {
		if e.r.Device == device {
			s.bytes -= e.size
			return true
		}
		return false
	})
	delete(s.dropped, device)
	s.gens[device]++
}

// Dropped says how many of a phone's requests the bounds pushed out since
// it was last dropped.
func (s *Store) Dropped(device string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped[device]
}

// Len returns how many requests and bytes the store holds.
func (s *Store) Len() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list), s.bytes
}
