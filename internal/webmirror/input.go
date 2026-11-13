package webmirror

import (
	"math"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// The page can use the phone when its token is the control token and the
// person allows control in Settings: one finger (taps, drags and the
// wheel, which the page turns into drags) and the Back and Home keys,
// passed to the gateway as the window mirror passes them, so they pause
// an agent's task the same way.
//
// Each connection sends its input to the gateway in order, one call at a
// time, off the WebSocket's reader: a touch can take the driver a while,
// and the page's key frame requests must not wait behind it. Moves that
// pile up meanwhile are merged, never a down or an up. A finger still down
// when the connection ends is lifted, or the phone would hold it (scrcpy's
// injection keeps no state of its own). Only one connection at a time
// holds a finger down on a phone; another's down waits for its up.
//
// A page cannot pile up input while the gateway is busy: the queue holds
// at most maxQueue downs and keys (a down or a key past that is dropped,
// its gesture with it), and keys pass at most keyBurst at once, keyRate a
// second after that.

// role is what a page's token lets it do.
type role int

const (
	roleView role = iota
	roleControl
)

func (r role) String() string {
	if r == roleControl {
		return "control"
	}
	return "view"
}

// Input limits.
const (
	maxQueue = 32
	keyBurst = 4
	keyRate  = 4 // a second
)

// The keys the page may press.
var pageKeys = map[string]driver.Key{"back": driver.KeyBack, "home": driver.KeyHome}

// inputEvent is one call for the gateway: a touch, or a key when key is
// set.
type inputEvent struct {
	a   driver.TouchAction
	p   screen.Point
	key driver.Key
}

// session is one control connection's input.
type session struct {
	s      *Server
	device string

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []inputEvent
	closed bool
	w, h   int // the stream's picture, as decoded (not turned)
	down   bool
	last   screen.Point
	done   chan struct{}

	// keys is how many keys may pass now, keysAt when it was counted.
	keys   float64
	keysAt time.Time
}

func newSession(s *Server, device string) *session {
	ss := &session{s: s, device: device, done: make(chan struct{}), keys: keyBurst, keysAt: time.Now()}
	ss.cond = sync.NewCond(&ss.mu)
	go ss.run()
	return ss
}

// setSize records the stream picture's size, from its decoder
// configuration.
func (ss *session) setSize(w, h int) {
	ss.mu.Lock()
	ss.w, ss.h = w, h
	ss.mu.Unlock()
}

// touch takes a touch from the page: x and y from 0 to 1 across the
// upright picture the page shows, which the page turned turn quarter turns
// clockwise from the stream's picture.
func (ss *session) touch(a driver.TouchAction, x, y float64, turn int) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed || ss.w <= 0 || ss.h <= 0 || math.IsNaN(x) || math.IsNaN(y) {
		return
	}
	p := toPicture(x, y, ss.w, ss.h, turn)
	switch a {
	case driver.TouchDown:
		if ss.down || len(ss.queue) >= maxQueue || !ss.s.claimPress(ss.device, ss) {
			return
		}
		ss.down = true
	case driver.TouchMove:
		if !ss.down {
			return
		}
	case driver.TouchUp:
		if !ss.down {
			return
		}
		ss.down = false
		defer ss.s.releasePress(ss.device, ss)
	default:
		return
	}
	ss.last = p
	ss.pushLocked(inputEvent{a: a, p: p})
}

// key takes a key from the page; keys other than pageKeys are dropped.
func (ss *session) key(name string) {
	k, ok := pageKeys[name]
	if !ok {
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.closed || len(ss.queue) >= maxQueue || !ss.takeKeyLocked(time.Now()) {
		return
	}
	ss.pushLocked(inputEvent{key: k})
}

// takeKeyLocked lets one key pass at now, if the rate allows.
func (ss *session) takeKeyLocked(now time.Time) bool {
	ss.keys = min(keyBurst, ss.keys+now.Sub(ss.keysAt).Seconds()*keyRate)
	ss.keysAt = now
	if ss.keys < 1 {
		return false
	}
	ss.keys--
	return true
}

// pushLocked queues an event, merging a move into a move still queued.
func (ss *session) pushLocked(ev inputEvent) {
	if n := len(ss.queue); ev.key == "" && ev.a == driver.TouchMove && n > 0 &&
		ss.queue[n-1].key == "" && ss.queue[n-1].a == driver.TouchMove {
		ss.queue[n-1] = ev
	} else {
		ss.queue = append(ss.queue, ev)
	}
	ss.cond.Signal()
}

// close lifts a finger still down and ends the session once its queue is
// sent.
func (ss *session) close() {
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return
	}
	if ss.down {
		ss.down = false
		ss.pushLocked(inputEvent{a: driver.TouchUp, p: ss.last})
		ss.s.releasePress(ss.device, ss)
	}
	ss.closed = true
	ss.cond.Signal()
	ss.mu.Unlock()
}

func (ss *session) run() {
	defer close(ss.done)
	for {
		ss.mu.Lock()
		for len(ss.queue) == 0 && !ss.closed {
			ss.cond.Wait()
		}
		if len(ss.queue) == 0 {
			ss.mu.Unlock()
			return
		}
		ev := ss.queue[0]
		ss.queue = ss.queue[1:]
		ss.mu.Unlock()
		var err error
		if ev.key != "" {
			err = ss.s.opt.Source.Key(ss.device, ev.key)
		} else {
			err = ss.s.opt.Source.Touch(ss.device, ev.a, ev.p)
		}
		if err != nil {
			ss.s.log.Debug("web mirror: input not passed on", "device", ss.device, "err", err)
		}
	}
}

// claimPress gives ss the phone's one finger, unless another session
// holds it down.
func (s *Server) claimPress(device string, ss *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur := s.pressing[device]; cur != nil && cur != ss {
		return false
	}
	s.pressing[device] = ss
	return true
}

func (s *Server) releasePress(device string, ss *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pressing[device] == ss {
		delete(s.pressing, device)
	}
}

// toPicture maps a point the page gives, x and y from 0 to 1 across the
// upright picture, to the stream picture's pixels (w×h, before the turn),
// where the gateway takes touches (driver.Toucher), as the window mirror
// does (ui/mirror.go: toDevice, then toPicture).
func toPicture(x, y float64, w, h, turn int) screen.Point {
	turn &= 3
	uw, uh := w, h
	if turn%2 == 1 {
		uw, uh = h, w
	}
	p := screen.Point{
		X: clamp(int(x*float64(uw)), 0, uw-1),
		Y: clamp(int(y*float64(uh)), 0, uh-1),
	}
	return turnPoint(p, uw, uh, (4-turn)&3)
}

// turnPoint returns where the pixel at p of a w×h picture lands when the
// picture turns q quarter turns clockwise (ui/mirror.go's).
func turnPoint(p screen.Point, w, h, q int) screen.Point {
	switch q & 3 {
	case 1:
		return screen.Point{X: h - 1 - p.Y, Y: p.X}
	case 2:
		return screen.Point{X: w - 1 - p.X, Y: h - 1 - p.Y}
	case 3:
		return screen.Point{X: p.Y, Y: w - 1 - p.X}
	}
	return p
}

func clamp(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
