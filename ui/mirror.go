package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/video"
)

// mirror is a phone's live screen: its stream, decoded on a goroutine of
// its own by a video.Player, and the person's input on it. One per phone,
// shown by the Phone screen or a tile of All phones. Its fields are used
// on the main thread.
type mirror struct {
	a       *app
	id      string
	agent   bool   // the display an agent works on in the background, which takes no input
	task    string // an agent's display: the task it belongs to
	ownTask string // an agent's display: a task on the phone's own screen (gateway.ErrOwnScreen), not asked for again
	gen     int
	cancel  context.CancelFunc
	player  *video.Player
	bitmap  *mg.Bitmap   // the picture at its own size
	frame   *video.Frame // the picture the bitmap shows
	state   string       // "connecting", "playing", "failed", "still" (held: no stream, the last picture kept)
	opening bool         // a try to open the stream is under way
	err     error
	retry   time.Time
	used    int
	closed  bool

	// halves are the picture halved, once and more, for the surfaces that
	// show it at half its size or less (mirrorLevel), made as they draw.
	halves halvings

	// A mirror paints again while pictures come and stops a moment after
	// the last (mirrorPaintFor): a still screen costs no frames. asleep
	// then has the player's next picture wake the windows (wake). The
	// player hands over no picture the same as the one before, so the
	// pictures a phone goes on sending after its screen stops wake nothing.
	shownAt  time.Time // the frame that took the last new picture (Painter.Now)
	openedAt time.Time // when the stream was last opened
	firstAt  time.Time // when this stream's first picture came, zero before it
	holdDue  bool      // a build is asked for once the picture may be held (stillFor)
	asleep   atomic.Bool
	// stalls counts the times the mirror's decoder gave no picture for
	// what came (video.ErrStalled); from the second on, or after one that
	// hung (video.ErrHung), it decodes in software. lateAt is when a late
	// picture was last logged.
	stalls int
	lateAt time.Time

	picW, picH int     // the stream's picture size the right way up: the device's pixels
	turn       int     // quarter turns clockwise that put the picture upright (driver.Packet.Turn)
	boxW, boxH float32 // the screen's size where the person's input comes from, in DIPs (toDevice)
	// boxes are the screen's size on each surface that shows it: the
	// phone's page or tile, and the phone's own window (phonewindow.go),
	// which may show it at the same time, at another size. An event takes
	// its surface's size into boxW and boxH.
	boxes   [surfaces][2]float32
	inputOn surface // where the person's last input came from, which shows compose

	down      bool         // a touch is down
	lastMove  time.Time    // throttles moves
	scrolling bool         // a wheel or touchpad gesture is underway
	scrollAt  screen.Point // where its finger started
	scrollPos [2]float64   // where its finger is
	scrollSeq int
	wheel     *wheelGlide  // a mouse wheel's notches gliding the finger (mirrorwheel.go)
	wheelSent [2]float64   // how far the finger has moved along wheel
	wheelTick bool         // wheel's finger is gliding, a tick to come
	pinch     *mirrorPinch // two fingers are down (mirrorpinch.go)
	compose   string       // an input method's composition

	// pointerOn and lookedAt track recent pointer/input activity on the
	// phone's page so a locked iPhone stays live while in use (pageStill).
	pointerOn bool
	lookedAt  time.Time
	lookDue   bool // timer pending to recheck pageStill after pageLookFor
}

// surface is where a mirror shows. One mirror, and its one stream, may show
// on the phone's page and in the phone's own window at once.
type surface int

const (
	onPage   surface = iota // the phone's page in the main window
	onTile                  // a tile of All phones, which takes no input
	onWindow                // the phone's own window (phonewindow.go)
	surfaces
)

// mirrorFor returns the phone's mirror, opening its stream if needed: a
// still opens it again, showing its picture until the stream's first. A
// build of a window that shows nothing (unseen) opens none.
func (a *app) mirrorFor(id string) *mirror {
	m := a.mirrors[id]
	if m == nil {
		m = &mirror{a: a, id: id}
		a.mirrors[id] = m
		if a.unseen {
			m.state = "still"
		} else {
			m.open()
		}
	}
	m.used = a.buildSeq
	if a.unseen {
		return m
	}
	if m.state == "still" {
		m.open()
	}
	if m.state == "failed" && !m.opening && time.Now().After(m.retry) && a.videoErr == nil {
		m.open()
	}
	return m
}

// stillAfter is how long a tile that shows a still waits after its
// stream's first picture before it lets the stream go: a screen that was
// changing as the stream opened settles meanwhile.
const stillAfter = time.Second

// stillGiveUp is how long a tile that holds a picture waits for its
// reopened stream's first before it keeps the picture it has.
const stillGiveUp = 10 * time.Second

// stillFor returns the phone's mirror for a tile that shows the phone's
// last picture rather than its live screen (tileStill): a mirror that
// already has a picture holds it and lets its stream go; one without opens
// its stream for a picture first. A stream nobody watches lets an iPhone
// stop its display stream, and with it screen sharing, and lock.
func (a *app) stillFor(id string) *mirror {
	if m := a.mirrors[id]; m != nil && m.state == "still" {
		m.used = a.buildSeq
		return m
	}
	m := a.mirrorFor(id)
	if m.bitmap == nil {
		return m
	}
	// A still opened again (its page, a task) whose stream fails or sends
	// no new picture keeps the one it has.
	since, wait := m.firstAt, stillAfter
	if since.IsZero() {
		since, wait = m.openedAt, stillGiveUp
	}
	if wait -= time.Since(since); wait > 0 && m.state != "failed" {
		if !m.holdDue {
			m.holdDue = true
			gen := m.gen
			time.AfterFunc(wait, func() {
				a.post(func() {
					if m.gen == gen {
						m.holdDue = false
					}
				})
			})
		}
		return m
	}
	m.hold()
	return m
}

// sweepMirrors lets go of the mirrors this build did not show, and of
// all of them while nothing of the main window shows on screen (unseen). A phone's
// own mirror that has a picture holds it, so that All phones shows it
// again without opening the stream for it; agents' displays, and phones
// gone or offline, close.
func (a *app) sweepMirrors() {
	for id, m := range a.mirrors {
		if (m.used == a.buildSeq && !a.unseen) || a.inWindow(m) {
			continue
		}
		if d := a.device(m.id); !m.agent && m.bitmap != nil && d != nil && d.Info.State == driver.StateOnline && d.Status != gateway.DeviceOffline {
			if m.state != "still" {
				m.hold()
			}
			continue
		}
		m.close()
		delete(a.mirrors, id)
	}
}

// hideSettle is how long the main window stays marked unseen after hiding
// or minimizing: macOS updates WindowHidden ~0.3 s later, so without this
// window an intervening build would reopen the streams.
const hideSettle = time.Second

// mainHid closes mirrors when the main window hides or minimizes, and
// keeps builds from reopening them for hideSettle while WindowHidden catches up.
func (a *app) mainHid() {
	a.hidAt = time.Now()
	a.closeMirrors()
	time.AfterFunc(hideSettle, func() { a.post(func() {}) })
}

// closeMirrors closes the mirrors as the main window hides or minimizes,
// but those the phones' own windows show.
func (a *app) closeMirrors() {
	for id, m := range a.mirrors {
		if a.inWindow(m) {
			continue
		}
		m.close()
		delete(a.mirrors, id)
	}
}

func (m *mirror) open() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.player != nil {
		p := m.player
		go p.Close()
		m.player = nil
	}
	if m.a.videoErr != nil {
		m.state, m.err = "failed", m.a.videoErr
		return
	}
	m.gen++
	gen := m.gen
	ctx, cancel := context.WithCancel(m.a.ctx)
	m.cancel = cancel
	// A retry keeps showing why the last try failed until this one is
	// done, rather than a spinner every few seconds for a phone that has
	// no picture.
	if m.state != "failed" {
		m.state, m.err = "connecting", nil
	}
	m.opening = true
	m.openedAt, m.firstAt, m.holdDue = time.Now(), time.Time{}, false
	a, task, software := m.a, m.task, m.stalls > 1
	go func() {
		logf("pinky: mirror %s: opening the stream (gen %d, agent %v)", m.id, gen, m.agent)
		if m.agent {
			a.logInfo(fmt.Sprintf("pinky: mirror %s: opening the stream", m.id), "agent", true)
		} else {
			a.logInfo(fmt.Sprintf("pinky: mirror %s: opening the stream", m.id))
		}
		var s driver.Stream
		var err error
		if m.agent {
			if as, ok := a.api.(agentStreamer); ok {
				s, err = as.AgentStream(ctx, m.id, driver.StreamOptions{})
			} else {
				err = fmt.Errorf("agent displays: %w", driver.ErrNotFound)
			}
		} else {
			s, err = a.api.Stream(ctx, m.id, driver.StreamOptions{})
		}
		var p *video.Player
		if err == nil {
			p, err = video.NewPlayer(s, video.PlayerOptions{YUV: true, SkipRepeats: true, OnFrame: m.wake,
				OnWaiting: m.nudge, StallAfter: mirrorStallAfter, Decoder: video.DecoderOptions{Software: software}})
			if err != nil {
				s.Close()
			}
		}
		if err != nil {
			logf("pinky: mirror %s: could not open the stream (gen %d): %v", m.id, gen, err)
			a.logInfo(fmt.Sprintf("pinky: mirror %s: could not open the stream", m.id), "err", err)
		} else {
			logf("pinky: mirror %s: stream opened (gen %d)", m.id, gen)
			a.logInfo(fmt.Sprintf("pinky: mirror %s: stream opened", m.id))
		}
		a.post(func() {
			logf("pinky: mirror %s: posted (gen %d, closed %v, current gen %d)", m.id, gen, m.closed, m.gen)
			if m.closed || m.gen != gen {
				if p != nil {
					go p.Close()
				}
				return
			}
			m.opening = false
			if err != nil {
				const wait = 3 * time.Second
				m.state, m.err = "failed", err
				m.retry = time.Now().Add(wait)
				if errors.Is(err, video.ErrNotFound) {
					a.videoErr = err
					m.logFailed(err, 0) // no retry: the decoder is missing
					return
				}
				if m.agent && errors.Is(err, gateway.ErrOwnScreen) {
					m.ownTask = task
					m.logFailed(err, 0) // no retry during this task (agentMirrorFor)
					return
				}
				m.retryLater()
				m.logFailed(err, wait)
				return
			}
			m.player, m.state = p, "playing"
			go func() {
				<-p.Done()
				a.post(func() {
					if m.player == p && !m.closed {
						m.ended(p.Err())
					}
				})
			}()
		})
	}()
}

// mirrorStallAfter is how long a mirror's decoder may take the stream's
// packets without giving a picture before the mirror opens the stream again
// (video.PlayerOptions.StallAfter).
const mirrorStallAfter = 3 * time.Second

// mirrorLate is how long a picture may wait to be painted before the mirror
// logs it: the windows stopped painting the mirror while pictures came.
const mirrorLate = 2 * time.Second

// ended records that the mirror's stream ended after it played, and writes
// it to the log: the mirror tries again in two seconds. A decoder that
// stalled is no failure of the stream: the mirror opens it again at once,
// with a new decoder, and shows its picture meanwhile; from the second
// stall on, the decoder works in software, and so it does at once after a
// decoder that hung (video.ErrHung): a hardware decoder stuck in the GPU's
// driver may well take the next one down with it.
func (m *mirror) ended(err error) {
	if errors.Is(err, video.ErrStalled) {
		m.stalls++
		if errors.Is(err, video.ErrHung) {
			m.stalls = max(m.stalls, 2)
		}
		m.a.logInfo(fmt.Sprintf("pinky: mirror %s: the picture stalled; opening the stream again", m.id),
			"err", err, "stalls", m.stalls, "software", m.stalls > 1)
		m.open()
		return
	}
	const wait = 2 * time.Second
	m.state, m.err = "failed", err
	m.retry = time.Now().Add(wait)
	m.logFailed(err, wait)
	m.retryLater()
}

// logFailed writes that the mirror's stream failed, with the error and how
// long the mirror waits before it tries again: the line that shows an
// unstable picture in the person's log. No wait means the mirror will not
// try again.
func (m *mirror) logFailed(err error, wait time.Duration) {
	args := []any{"err", err}
	if wait > 0 {
		args = append(args, "retry_in", wait)
	}
	m.a.logInfo(fmt.Sprintf("pinky: mirror %s: stream failed", m.id), args...)
}

// retryLater opens the stream again once its retry is due, whether or not
// the view is built meanwhile: an idle window, or one on a locked screen,
// builds nothing.
func (m *mirror) retryLater() {
	gen := m.gen
	time.AfterFunc(max(time.Until(m.retry), 0), func() {
		m.a.post(func() {
			if !m.closed && m.gen == gen && m.state == "failed" && !m.opening && m.a.videoErr == nil {
				m.open()
			}
		})
	})
}

// missing reports an agent's display that the gateway says is not open,
// as before a background task starts it.
func (m *mirror) missing() bool {
	return m.state == "failed" && errors.Is(m.err, driver.ErrNotFound)
}

// mirrorPaintFor is how long a mirror goes on painting after its last
// picture: longer than the gaps between the pictures of a moving screen,
// even at 15 a second, so that it does not stop and wake between them.
const mirrorPaintFor = 250 * time.Millisecond

// sleep stops painting on a still screen, unless a picture came
// meanwhile, and reports whether it did.
func (m *mirror) sleep(pl *video.Player) bool {
	m.asleep.Store(true)
	if pl.Pending() {
		// It came before asleep was set, so it woke nothing.
		m.asleep.Store(false)
		return false
	}
	return true
}

// wake has the windows paint the mirror's new picture once it stopped
// painting on a still screen. The player calls it, on its goroutine,
// after each picture.
func (m *mirror) wake() {
	if m.asleep.CompareAndSwap(true, false) {
		m.a.post(func() {})
	}
}

// nudge has the windows paint the mirror again: pictures have waited for
// over a second while new ones came, so painting stopped though the mirror
// was not asleep, and wake, which posts only for a sleeping mirror, would
// never start it again. The player calls it, on its goroutine, at most
// once a second.
func (m *mirror) nudge() {
	m.asleep.Store(false)
	m.a.post(func() {})
}

// hold lets the mirror's stream go and keeps its picture: a still, until
// mirrorFor opens the stream again. A stream still opening is dropped.
func (m *mirror) hold() {
	logf("pinky: mirror %s: holding its picture", m.id)
	m.a.logInfo(fmt.Sprintf("pinky: mirror %s: holding its picture", m.id))
	m.gen++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if p := m.player; p != nil {
		m.player = nil
		go p.Close()
	}
	m.state, m.err, m.opening, m.holdDue = "still", nil, false, false
	m.asleep.Store(false)
}

func (m *mirror) close() {
	logf("pinky: mirror %s: closed", m.id)
	m.a.logInfo(fmt.Sprintf("pinky: mirror %s: closed", m.id))
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	if p := m.player; p != nil {
		m.player = nil
		go p.Close()
	}
}

// aspect is the screen's width over its height the right way up, from the
// stream once it plays (landscape when the phone is turned), else the
// phone's usual portrait (bezel.go).
func (m *mirror) aspect(d gateway.DeviceView) float32 {
	if m.picW > 0 && m.picH > 0 {
		return float32(m.picW) / float32(m.picH)
	}
	return bezelOf(d).defaultAspect()
}

// lastPicture copies the picture the mirror shows, for a screenshot: the
// player writes its planes again. turn puts it the right way up.
func (m *mirror) lastPicture() (pic *video.YUV, turn int) {
	if m.frame == nil || m.frame.YUV == nil {
		return nil, 0
	}
	return m.frame.YUV.Clone(), m.turn
}

// drawTurned draws a picture into r, as large as it fits, turned turn
// quarter turns clockwise, which puts a phone's picture the right way up
// when the phone sends its display as mounted (an iPhone or iPad on iOS
// 27; driver.Packet.Turn). src is the part of bm that holds the picture,
// in bm's pixels, or empty for all of it: the picture halved (halvings)
// takes up the last row or column of an odd size only in part.
func drawTurned(pt *mg.Painter, bm *mg.Bitmap, src, r mg.Rect, turn int) {
	turn &= 3
	if turn == 0 {
		drawContained(pt, bm, src, r)
		return
	}
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	in := r
	if turn%2 == 1 {
		// Drawn sideways into the box turned the other way, then turned.
		in = mg.Rect{X: cx - r.H/2, Y: cy - r.W/2, W: r.H, H: r.W}
	}
	pt.Transform(mg.Rotation(float32(90*turn)), cx, cy, func() { drawContained(pt, bm, src, in) })
}

// drawContained draws the part src of bm, or all of it when src is empty,
// as large as it fits in box, in the middle, as Painter.Image does with
// Contain.
func drawContained(pt *mg.Painter, bm *mg.Bitmap, src, box mg.Rect) {
	if src.W <= 0 || src.H <= 0 {
		pt.Image(bm, box, mg.Contain)
		return
	}
	s := min(box.W/src.W, box.H/src.H)
	w, h := src.W*s, src.H*s
	// On the screen's pixels, as Image places a bitmap, which ImageRegion
	// leaves to a transform.
	scale := pt.Scale()
	snap := func(v float32) float32 { return float32(math.Round(float64(v*scale))) / scale }
	x0, y0 := snap(box.X+(box.W-w)/2), snap(box.Y+(box.H-h)/2)
	x1, y1 := snap(box.X+(box.W+w)/2), snap(box.Y+(box.H+h)/2)
	pt.ImageRegion(bm, src, mg.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, mg.ImageOptions{})
}

// mirrorHalvings is the most times a mirror, or the recording player,
// halves its picture: 32 times smaller, as a 4K screen in a tile of All
// phones.
const mirrorHalvings = 5

// mirrorLevel returns how many times to halve a w×h picture, which turn
// quarter turns put the right way up, for a surface that shows it in a box
// of bw×bh DIPs, as large as it fits, at scale pixels a DIP. MyGo's
// renderers take one sample of a picture of video for each pixel they
// draw, so a picture at half its size or less shows only some of its
// pixels, and fine lines and text break up (#233). As MyGo halves a bitmap
// it shows smaller: the most halvings that leave the picture no smaller
// than it shows, whose pixels each average the 2×2 under them.
func mirrorLevel(w, h, turn int, bw, bh, scale float32) int {
	if turn&1 == 1 {
		w, h = h, w
	}
	if w <= 0 || h <= 0 || bw <= 0 || bh <= 0 || scale <= 0 {
		return 0
	}
	// The picture's pixels to one of the screen's.
	shrink := 1 / (min(bw/float32(w), bh/float32(h)) * scale)
	if shrink < 2 {
		return 0
	}
	return min(int(math.Log2(float64(shrink))), mirrorHalvings)
}

// halvings are a picture of video halved once and more, for the surfaces
// that show it at half its size or less (mirrorLevel): a mirror's, which
// its surfaces share, and the recording player's. A level is made as the
// first surface that shows it draws after a new picture, from the level
// above: once a picture, none that no surface shows, and nothing between
// pictures, however often the surfaces draw.
type halvings struct {
	pic    *video.YUV // the picture, as its own bitmap shows it; nil for none
	n      uint64     // counts the pictures: pic is picture n
	levels [mirrorHalvings]pictureHalf
}

// pictureHalf is the picture halved some times, and the bitmap that shows
// it. A new picture is halved into the planes the bitmap does not show, as
// MyGo reads those it shows until the bitmap's next picture; the two keep
// their memory from one picture to the next.
type pictureHalf struct {
	planes [2]video.YUV
	cur    int    // the planes of picture pic
	pic    uint64 // the picture (halvings.n) planes[cur] holds, 0 for none
	bitmap *mg.Bitmap
	shows  int    // the planes the bitmap shows
	shown  uint64 // the picture it shows, 0 for none
}

// next takes pic as the picture, a new one, which its own bitmap shows
// from now on. Its planes must hold it until the next, as the bitmap's do:
// the levels are made from them as the surfaces draw.
func (h *halvings) next(pic *video.YUV) {
	h.pic = pic
	h.n++
}

// pictureFor returns the bitmap that shows the picture, which turn quarter
// turns put the right way up, in a box of r's size at scale pixels a DIP,
// and the part of it that holds the picture, in its pixels (empty for all
// of it): full, the picture's own bitmap, or the picture halved as often
// as the box's pixels need (mirrorLevel). The surfaces that show it at the
// same level share its bitmap.
func (h *halvings) pictureFor(full *mg.Bitmap, turn int, r mg.Rect, scale float32) (*mg.Bitmap, mg.Rect) {
	pic := h.pic
	if pic == nil {
		return full, mg.Rect{}
	}
	k := mirrorLevel(pic.Width, pic.Height, turn, r.W, r.H, scale)
	if k == 0 {
		return full, mg.Rect{}
	}
	l := &h.levels[k-1]
	if l.shown != h.n {
		l.bitmap = showPlanes(l.bitmap, h.halved(k))
		l.shows, l.shown = l.cur, h.n
	}
	n := float32(int(1) << k)
	return l.bitmap, mg.Rect{W: float32(pic.Width) / n, H: float32(pic.Height) / n}
}

// halved returns the picture halved k times, made from the picture halved
// k-1 times unless it is made already: once a picture for each level a
// surface shows, and for those above it.
func (h *halvings) halved(k int) *video.YUV {
	if k == 0 {
		return h.pic
	}
	l := &h.levels[k-1]
	if l.pic != h.n {
		src := h.halved(k - 1)
		into := 0
		if l.bitmap != nil {
			into = 1 - l.shows
		}
		video.Halve(&l.planes[into], src)
		l.cur, l.pic = into, h.n
	}
	return &l.planes[l.cur]
}

// toPicture turns a point of the upright picture back into the picture as
// the stream sends it, where the phone takes touches.
func (m *mirror) toPicture(p screen.Point) screen.Point {
	return turnPoint(p, m.picW, m.picH, (4-m.turn&3)&3)
}

// turnPoint returns where the pixel at p of a w×h picture lands when the
// picture turns q quarter turns clockwise.
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

// showPlanes shows a picture's planes in bm, made for the first one, and
// returns it. MyGo's renderers convert and scale the planes as they draw
// them, so a picture costs the CPU no more than the decoder's copy.
func showPlanes(bm *mg.Bitmap, pic *video.YUV) *mg.Bitmap {
	img := mg.YUVImage{Format: mg.I420, Width: pic.Width, Height: pic.Height,
		Planes: pic.Planes, Strides: pic.Strides, Matrix: mg.BT601, FullRange: pic.FullRange}
	if pic.NV12 {
		img.Format = mg.NV12
	}
	switch pic.Matrix {
	case video.BT709:
		img.Matrix = mg.BT709
	case video.BT2020:
		img.Matrix = mg.BT2020
	}
	if bm == nil {
		bm = mg.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	}
	bm.UpdateYUV(&img)
	return bm
}

// overlayStep is the step whose target the mirror outlines: the one in
// progress or waiting for the person.
func overlayStep(t *gateway.TaskView) (gateway.StepView, stepKind, bool) {
	if t == nil || len(t.Steps) == 0 || !running(t.State) || t.State == gateway.TaskPaused {
		return gateway.StepView{}, 0, false
	}
	i := len(t.Steps) - 1
	s := t.Steps[i]
	k := kindOf(*t, i)
	if (k != stepNow && k != stepWait) || (s.Target.Empty() && s.Point == (screen.Point{})) {
		return gateway.StepView{}, 0, false
	}
	return s, k, true
}

// agentOrTheAgent names a task's agent, for labels.
func agentOrTheAgent(t *gateway.TaskView) string {
	if t != nil && t.Agent != "" {
		return t.Agent
	}
	return locales.AgentGenericStart()
}

// mirrorView draws the screen inside a phone's bezel, or in the phone's own
// window: the video, the agent's overlay, the lock, and takes the person's
// input unless it is a tile.
func (a *app) mirrorView(c *mg.Context, m *mirror, d gateway.DeviceView, t *gateway.TaskView, radius float32, where surface) {
	p := pal(c)
	tile := where == onTile
	box := m.boxes[where]
	// The agent's own display is never locked and takes no input: the
	// person uses the phone's screen.
	locked := a.isLocked(d) && !m.agent
	step, kind, outline := overlayStep(t)
	var way shotMarks
	if outline && kind == stepWait {
		way = a.waitingWay(t, step)
	}
	scr := mg.Box(c).Fill().Radius(radius).Clip().Background(hex("#000000"))
	if where == onPage && !m.agent {
		// Keep a locked iPhone live while hovered and briefly after (pageStill).
		// pageStill ran earlier in this build, so rebuild when hover changes.
		on := scr.Hovered()
		if on || m.pointerOn {
			m.lookedAt = time.Now()
		}
		if on != m.pointerOn {
			a.post(func() {})
		}
		m.pointerOn = on
	}
	switch {
	case m.agent:
		scr.Label(locales.MirrorAgentScreenLabel(agentOrTheAgent(t), d.Info.Name))
	case !tile:
		scr.Focusable().FocusRing(false).Cursor(mg.CursorPointer).Label(locales.MirrorScreenLabel(d.Info.Name)).
			HandleInput(func(ev mg.InputEvent) bool {
				m.lookedAt = time.Now()
				// The event's position is on this surface, at its size.
				m.boxW, m.boxH = m.boxes[where][0], m.boxes[where][1]
				m.inputOn = where
				return a.mirrorInput(m, d, ev)
			})
		scr.TextCaret(mg.Rect{X: box[0]/2 - 1, Y: box[1] * 0.8, W: 2, H: 20})
	}
	scr.Draw(func(pt *mg.Painter, r mg.Rect) {
		m.boxes[where] = [2]float32{r.W, r.H}
		if !tile {
			m.boxW, m.boxH = r.W, r.H
		}
		if pl := m.player; pl != nil {
			if f := pl.Take(); f != nil {
				if m.bitmap == nil {
					// The view was built before the first picture, with
					// the Connecting overlay; painting does not build it
					// again, and a still screen sends nothing else.
					a.post(func() {})
				}
				if late := time.Since(f.Ready); late >= mirrorLate && time.Since(m.lateAt) >= time.Minute {
					// The windows stopped painting the mirror while pictures
					// came (nudge): say so, as it looks like a frozen phone.
					m.lateAt = time.Now()
					a.logInfo(fmt.Sprintf("pinky: mirror %s: painted a picture late", m.id), "waited", late.Round(100*time.Millisecond))
				}
				if m.firstAt.IsZero() {
					// A tile waiting to hold its picture (stillFor) waits
					// from now.
					m.firstAt, m.holdDue = time.Now(), false
					a.post(func() {})
				}
				// The bitmap of the picture's own size follows the
				// player's pictures, whose planes it shows: those of the
				// one before go back to the player, which writes them
				// again. Showing NV12's costs nothing, nor does a bitmap
				// no surface draws cost the GPU an upload. The halvings
				// are made as surfaces draw them (halvings).
				m.bitmap = showPlanes(m.bitmap, f.YUV)
				m.frame = f
				m.halves.next(f.YUV)
				w, h := f.Width, f.Height
				if f.Turn%2 == 1 {
					w, h = h, w
				}
				if m.picW > 0 && (w != m.picW || h != m.picH) {
					// The phone turned: lay the mirror out again for the
					// picture's new shape.
					a.post(func() {})
				}
				m.picW, m.picH, m.turn = w, h, f.Turn
				m.shownAt = pt.Now()
			}
			// Paint again at up to 60 frames a second, without building
			// the view, to pick up the next picture, until the screen
			// stays still.
			if pt.Now().Sub(m.shownAt) < mirrorPaintFor || !m.sleep(pl) {
				pt.AnimationFrameRate(60)
			}
		}
		// A still, or a still opening its stream again, shows the picture
		// it holds: halved as often as this surface's pixels need.
		if m.bitmap != nil && (m.state == "playing" || m.state == "still" || m.state == "connecting") {
			bm, src := m.halves.pictureFor(m.bitmap, m.turn, r, pt.Scale())
			pt.Clip(r, radius, func() { drawTurned(pt, bm, src, r, m.turn) })
		}
		if outline && !locked && m.picW > 0 {
			// Clipped to the screen: targets are in screen pixels, which on
			// an iPhone differ from the picture's by about 1%, so an
			// outline at the edge may reach a little past it.
			pt.Clip(r, radius, func() {
				drawOverlay(pt, r, p, step, kind, way, float32(m.picW), float32(m.picH), tile)
			})
		}
		if !tile {
			drawFingers(pt, r, m)
		}
	})
	scr.Children(func() {
		switch {
		case locked:
			// The lock: the screen dimmed, and a pill naming it. On the
			// Phone screen both give way while the pointer is on the
			// mirror, which then shows the lock screen as it is, for the
			// person to unlock by hand: the pill sits where the PIN pad
			// comes up. Clicks go through them either way.
			shown := float32(1)
			if !tile {
				shown = scr.Animate("lock", b2f(!scr.Hovered()), 150*time.Millisecond)
			}
			mg.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(mg.RGBA(10, 12, 18, 0.62)).Opacity(shown).PassThrough()
			// The pill sits a little below the middle, as on the prototype's lock screen.
			pill := mg.Row(c).Attach(mg.AnchorCenter, mg.AnchorCenter).Top(box[1]*0.04).Gap(8).AlignItems(mg.Center).
				Padding(9, 14).Radius(999).Background(mg.RGBA(255, 255, 255, 0.14)).Border(1, mg.RGBA(255, 255, 255, 0.22)).
				TextColor(hex("#ffffff")).Opacity(shown).PassThrough()
			if tile {
				pill.Padding(6, 10).Gap(6)
			}
			pill.Children(func() {
				mg.Icon(c, icLock).FontSize(iconBody)
				label := locales.MirrorLocked(d.Info.Name)
				if tile {
					label = locales.MirrorLockedTile()
				}
				mg.Text(c, label).FontSize(fsBody).FontWeight(fwSemibold).NoWrap()
			})
		case d.ScreenOff && !m.agent:
			// The phone's own screen is off without a lock screen (an
			// Android TV in standby): it sends no picture until it wakes,
			// so the mirror says so instead of seeming to connect. A click
			// or a key on the mirror wakes it (mirrorInput).
			mg.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Background(mg.RGBA(10, 12, 18, 0.62)).PassThrough()
			mg.Column(c).Attach(mg.AnchorCenter, mg.AnchorCenter).Top(box[1] * 0.04).Gap(8).AlignItems(mg.Center).PassThrough().Children(func() {
				pill := mg.Row(c).Gap(8).AlignItems(mg.Center).Padding(9, 14).Radius(999).
					Background(mg.RGBA(255, 255, 255, 0.14)).Border(1, mg.RGBA(255, 255, 255, 0.22)).TextColor(hex("#ffffff")).PassThrough()
				if tile {
					pill.Padding(6, 10).Gap(6)
				}
				pill.Children(func() {
					mg.Icon(c, icMoon).FontSize(iconBody)
					label := locales.MirrorScreenOff(d.Info.Name)
					if tile {
						label = locales.MirrorScreenOffTile()
					}
					mg.Text(c, label).FontSize(fsBody).FontWeight(fwSemibold).NoWrap()
				})
				if _, canWake := a.api.(screenWaker); canWake && !tile {
					mg.Text(c, locales.MirrorScreenOffWake()).FontSize(fsSmall).TextColor(hex("#d0d0d8")).PassThrough()
				}
			})
		case tile && m.state == "still" && m.bitmap != nil:
			// An idle iPhone's tile holds its last picture (stillFor): say so,
			// or the phone seems to have frozen while it goes on changing.
			mg.Text(c, locales.MirrorNotLiveTile()).Attach(mg.AnchorBottom, mg.AnchorBottom).Bottom(10).Padding(3, 8).Radius(999).
				Background(mg.RGBA(20, 20, 24, 0.72)).TextColor(hex("#ffffff")).FontSize(fsSmall).NoWrap().PassThrough()
		case (m.state == "connecting" || m.state == "playing") && m.bitmap == nil:
			mg.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(10).PassThrough().Children(func() {
				spinner(c)
				if !tile {
					mg.Text(c, locales.MirrorConnecting()).FontSize(fsSmall).TextColor(hex("#a3a3ad"))
				}
			})
		case m.state == "failed":
			mg.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Center().Gap(8).Padding(20).PassThrough().Children(func() {
				mg.Icon(c, icAlert).FontSize(iconBig).TextColor(hex("#f7a74e"))
				if !tile {
					mg.Text(c, mirrorError(m.err)).FontSize(fsSmall).TextColor(hex("#d0d0d8")).TextAlign(mg.Center).MaxWidth(240)
				}
			})
		}
		if m.compose != "" && !tile && m.inputOn == where {
			mg.Text(c, m.compose).Attach(mg.AnchorBottom, mg.AnchorBottom).Bottom(28).MaxWidth(box[0]-32).Padding(6, 10).Radius(8).
				Background(mg.RGBA(20, 20, 24, 0.9)).TextColor(hex("#ffffff")).FontSize(14).Underline().PassThrough()
		}
	})
}

func mirrorError(err error) string {
	if err == nil {
		return locales.MirrorStreamEnded()
	}
	if se := (*driver.SetupError)(nil); errors.As(err, &se) && se.Setup == driver.SetupMediaInUse {
		// An app on the iPhone uses the camera or microphone; the mirror
		// tries again by itself.
		return locales.MirrorNoPicture(locales.MediaInUseTitle())
	}
	msg := err.Error()
	if errors.Is(err, video.ErrNotFound) {
		// The first sentence says what is missing and how to install it.
		if i := strings.Index(msg, ". "); i > 0 {
			return msg[:i+1] + " " + strings.TrimSuffix(msg[i+2:], ": "+video.ErrNotFound.Error())
		}
		return msg
	}
	if errors.Is(err, driver.ErrOffline) {
		return locales.MirrorOffline()
	}
	if errors.Is(err, driver.ErrUnsupported) {
		return locales.MirrorUnsupported()
	}
	return locales.MirrorNoPicture(msg)
}

// waitingWay is how the finger of the step that waits for the person
// would move (a swipe's, a drag's or a pinch's way), from the step's
// approval: the step itself keeps only its target and first point. It is
// empty for other steps.
func (a *app) waitingWay(t *gateway.TaskView, s gateway.StepView) shotMarks {
	for _, n := range a.needs {
		if n.Kind != gateway.NeedApproval || n.TaskID != t.ID || !(n.Created.Equal(s.At) || n.Target == s.Target) {
			continue
		}
		if m := marksOf(n); m.swipe() {
			return m
		}
	}
	return shotMarks{}
}

// drawOverlay outlines the step's target and, while the agent touches,
// draws the touch with two ripples, as the prototype does. A step that
// waits for the person to allow a swipe, a drag or a pinch also shows the
// finger's way (way), as its approval card does.
func drawOverlay(pt *mg.Painter, r mg.Rect, p *palette, s gateway.StepView, kind stepKind, way shotMarks, pw, ph float32, tile bool) {
	sx, sy := r.W/pw, r.H/ph
	if !s.Target.Empty() {
		box := mg.Rect{
			X: r.X + float32(s.Target.X)*sx - 5, Y: r.Y + float32(s.Target.Y)*sy - 5,
			W: float32(s.Target.W)*sx + 10, H: float32(s.Target.H)*sy + 10,
		}
		width := float32(2)
		if tile {
			width = 1.5
		}
		pt.Stroke(box, p.ovl, min(16, box.H/2), width)
	}
	if way.swipe() {
		at := func(q screen.Point) mg.Point { return mg.Point{X: r.X + float32(q.X)*sx, Y: r.Y + float32(q.Y)*sy} }
		var route []mg.Point
		for _, q := range way.route() {
			route = append(route, at(q))
		}
		halo := mg.RGBA(255, 255, 255, 0.92)
		drawArrow(pt, route, tile, p.ovl, halo)
		if way.pinch() {
			drawArrow(pt, []mg.Point{at(way.point2), at(way.to2)}, tile, p.ovl, halo)
		}
	}
	if kind != stepNow || s.Point == (screen.Point{}) {
		return
	}
	cx, cy := r.X+float32(s.Point.X)*sx, r.Y+float32(s.Point.Y)*sy
	size := min(r.W*0.2, 56)
	if tile {
		size = 36
	}
	now := pt.Now()
	for _, delay := range []time.Duration{0, 900 * time.Millisecond} {
		phase := float32((now.Add(-delay).UnixMilli())%1800) / 1800
		ease := 1 - float32(math.Pow(float64(1-phase), 3))
		radius := size / 2 * (0.35 + 1.45*ease)
		alpha := 0.7 * (1 - ease)
		var ring mg.Path
		ring.Circle(cx, cy, radius)
		pt.StrokePath(&ring, 2, p.ovl.Alpha(alpha))
	}
	if !tile {
		var halo, center mg.Path
		halo.Circle(cx, cy, 9)
		pt.FillPath(&halo, mg.RGBA(255, 255, 255, 0.9))
		center.Circle(cx, cy, 6)
		pt.FillPath(&center, p.ovl)
	}
	pt.AnimationFrame()
}

// mirrorInput turns the person's pointer, wheel, pinches and keys on the
// mirror into touches, keys and text in device pixels. The gateway pauses
// the agent on any of them.
func (a *app) mirrorInput(m *mirror, d gateway.DeviceView, ev mg.InputEvent) bool {
	if d.ScreenOff && !m.agent && (ev.Kind == mg.InputPointerDown || ev.Kind == mg.InputKeyDown) {
		// A phone whose screen is off sends no picture and takes no
		// touches: the click or key turns its screen on instead.
		if w, ok := a.api.(screenWaker); ok {
			id := d.Info.ID
			a.call(id, locales.MirrorCallWake(), func() error { return w.Wake(id) })
		}
		return true
	}
	if m.picW == 0 || m.boxW == 0 || d.Status == gateway.DeviceOffline {
		return false
	}
	id := d.Info.ID
	toDevice := m.toDevice
	touch := func(act driver.TouchAction, pt screen.Point) {
		pt = m.toPicture(pt)
		a.call(id, locales.MirrorCallTouch(), func() error { return a.api.Touch(id, act, pt) })
	}
	key := func(k driver.Key) {
		name := string(k)
		if translated() {
			name = keyName(name)
		}
		a.call(id, locales.MirrorCallKey(name), func() error { return a.api.Key(id, k) })
	}
	liftScroll := func() {
		if m.scrolling {
			m.scrolling = false
			m.wheel, m.wheelTick = nil, false
			touch(driver.TouchUp, m.scrollPoint())
		}
	}
	switch ev.Kind {
	case mg.InputPointerDown:
		switch ev.Button {
		case 0:
			a.endPinch(m, id)
			if ev.Mods&mg.Alt != 0 && a.canPinch(d) {
				liftScroll()
				a.mirrorDrag(m, id, driver.TouchDown, toDevice(ev.X, ev.Y))
				return true
			}
			m.down = true
			touch(driver.TouchDown, toDevice(ev.X, ev.Y))
		case 1:
			key(driver.KeyBack) // right click is Back
		case 2:
			key(driver.KeyHome) // middle click is Home
		}
		return true
	case mg.InputPointerMove:
		if p := m.pinch; p != nil && p.drag && ev.Button == 0 {
			if time.Since(m.lastMove) >= 8*time.Millisecond {
				m.lastMove = time.Now()
				a.mirrorDrag(m, id, driver.TouchMove, toDevice(ev.X, ev.Y))
			}
			return true
		}
		if m.down && ev.Button == 0 {
			if time.Since(m.lastMove) >= 8*time.Millisecond {
				m.lastMove = time.Now()
				touch(driver.TouchMove, toDevice(ev.X, ev.Y))
			}
			return true
		}
		return false
	case mg.InputPointerUp:
		if p := m.pinch; p != nil && p.drag && ev.Button == 0 {
			a.mirrorDrag(m, id, driver.TouchUp, toDevice(ev.X, ev.Y))
			return true
		}
		if ev.Button == 0 && m.down {
			m.down = false
			touch(driver.TouchUp, toDevice(ev.X, ev.Y))
		}
		return true
	case mg.InputScroll:
		if m.pinch == nil {
			a.mirrorScroll(m, id, ev, toDevice, touch)
		}
		return true
	case mg.InputZoom:
		if !a.canPinch(d) {
			return false
		}
		if m.down || (m.pinch != nil && m.pinch.drag) {
			return true // a finger is down already
		}
		liftScroll()
		a.mirrorZoom(m, id, ev)
		return true
	case mg.InputKeyDown:
		if ev.Mods&mg.Cmd != 0 && ev.Key == mg.KeyV {
			if text := mygo.Clipboard.ReadText(); text != "" {
				a.call(id, locales.MirrorCallPaste(), func() error { return a.api.Text(id, text) })
			}
			return true
		}
		if ev.Mods&(mg.Cmd|mg.Ctrl|mg.Alt) != 0 && ev.Mods&mg.Shift == 0 && ev.Key != mg.KeyBackspace {
			return false // leave shortcuts to the app
		}
		if k, ok := driverKey(ev.Key); ok {
			key(k)
			return true
		}
		return false
	case mg.InputText:
		text := strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, ev.Text)
		m.compose = ""
		if text != "" {
			a.call(id, locales.MirrorCallType(), func() error { return a.api.Text(id, text) })
		}
		return true
	case mg.InputCompose:
		m.compose = ev.Text
		if a.main != nil {
			a.main.Invalidate()
		}
		a.invalidate() // the phone's own window
		return true
	}
	return false
}

// toDevice maps a point on the mirror, in DIPs from its corner, to the
// device's pixels.
func (m *mirror) toDevice(x, y float32) screen.Point {
	px := int(float64(x) / float64(m.boxW) * float64(m.picW))
	py := int(float64(y) / float64(m.boxH) * float64(m.picH))
	return screen.Point{X: clampInt(px, 0, m.picW-1), Y: clampInt(py, 0, m.picH-1)}
}

// mirrorScroll turns a wheel or touchpad scroll into a finger dragging the
// content: down where the pointer is, moving against the scroll, and up
// once the scrolling stops. A touchpad's scroll moves the finger as it
// comes, and its momentum keeps it moving; a mouse wheel's notches glide
// it (mirrorWheel).
func (a *app) mirrorScroll(m *mirror, id string, ev mg.InputEvent, toDevice func(x, y float32) screen.Point, touch func(driver.TouchAction, screen.Point)) {
	if !ev.Precise {
		a.mirrorWheel(m, ev, toDevice, touch)
		return
	}
	sx := float64(m.picW) / float64(m.boxW)
	sy := float64(m.picH) / float64(m.boxH)
	if m.wheel != nil {
		// A touchpad after a wheel: the finger goes on from where the
		// wheel's notches put it.
		off, _ := m.wheel.at(time.Now().Add(time.Hour))
		m.wheelMove(off, touch)
		m.wheel, m.wheelTick = nil, false
	}
	m.scrollStart(toDevice(ev.X, ev.Y), touch)
	pos := m.scrollBy(-float64(ev.DX)*sx, -float64(ev.DY)*sy, touch)
	m.scrollSeq++
	seq := m.scrollSeq
	time.AfterFunc(140*time.Millisecond, func() {
		a.post(func() {
			if m.scrollSeq == seq && m.scrolling {
				m.scrolling = false
				touch(driver.TouchUp, pos)
			}
		})
	})
}

// mirrorWheel glides the scrolling finger by a mouse wheel's notches
// (mirrorwheel.go): a move every wheelTick while they move it, then up, at
// once when the wheel was spinning so that the phone flings, or after
// wheelHold at rest.
func (a *app) mirrorWheel(m *mirror, ev mg.InputEvent, toDevice func(x, y float32) screen.Point, touch func(driver.TouchAction, screen.Point)) {
	d := float64(m.boxH) * wheelNotch
	dx := -wheelNotches(ev.DX, runtime.GOOS) * d * float64(m.picW) / float64(m.boxW)
	dy := -wheelNotches(ev.DY, runtime.GOOS) * d * float64(m.picH) / float64(m.boxH)
	if dx == 0 && dy == 0 {
		return
	}
	m.scrollStart(toDevice(ev.X, ev.Y), touch)
	if m.wheel == nil {
		m.wheel = &wheelGlide{}
		m.wheelSent = [2]float64{}
	}
	m.wheel.add(time.Now(), dx, dy)
	m.scrollSeq++ // no lift at rest: the finger moves on
	if m.wheelTick {
		return // the running tick takes the notch along
	}
	m.wheelTick = true
	g := m.wheel
	lift := func(pos screen.Point) {
		m.scrolling = false
		m.wheel, m.wheelTick = nil, false
		touch(driver.TouchUp, pos)
	}
	var tick func()
	tick = func() {
		if m.wheel != g || !m.scrolling {
			return // lifted meanwhile
		}
		off, moving := g.at(time.Now())
		pos := m.wheelMove(off, touch)
		if moving {
			time.AfterFunc(wheelTick, func() { a.post(tick) })
			return
		}
		m.wheelTick = false
		if g.spinning() {
			lift(pos) // moving still: the phone flings
			return
		}
		seq := m.scrollSeq
		time.AfterFunc(wheelHold, func() {
			a.post(func() {
				if m.scrollSeq == seq && m.scrolling && m.wheel == g {
					lift(pos)
				}
			})
		})
	}
	tick()
}

// wheelMove moves the wheel's finger to off from where its notches began
// and returns where it is.
func (m *mirror) wheelMove(off [2]float64, touch func(driver.TouchAction, screen.Point)) screen.Point {
	dx, dy := off[0]-m.wheelSent[0], off[1]-m.wheelSent[1]
	m.wheelSent = off
	if dx == 0 && dy == 0 {
		return m.scrollPoint()
	}
	return m.scrollBy(dx, dy, touch)
}

// scrollStart puts the scrolling finger down at p unless it is down.
func (m *mirror) scrollStart(p screen.Point, touch func(driver.TouchAction, screen.Point)) {
	if m.scrolling {
		return
	}
	m.scrolling = true
	m.scrollAt = p
	m.scrollPos = [2]float64{float64(p.X), float64(p.Y)}
	touch(driver.TouchDown, p)
}

// scrollBy moves the scrolling finger by dx, dy device pixels and returns
// where it is.
func (m *mirror) scrollBy(dx, dy float64, touch func(driver.TouchAction, screen.Point)) screen.Point {
	nx, ny := m.scrollPos[0]+dx, m.scrollPos[1]+dy
	if nx < 0 || ny < 0 || nx >= float64(m.picW) || ny >= float64(m.picH) {
		// The finger reached the edge: lift it and start again.
		touch(driver.TouchUp, m.scrollPoint())
		touch(driver.TouchDown, m.scrollAt)
		nx, ny = float64(m.scrollAt.X)+dx, float64(m.scrollAt.Y)+dy
	}
	m.scrollPos = [2]float64{nx, ny}
	pos := m.scrollPoint()
	touch(driver.TouchMove, pos)
	return pos
}

// scrollPoint is where the scrolling finger is, on the screen.
func (m *mirror) scrollPoint() screen.Point {
	return screen.Point{X: clampInt(int(m.scrollPos[0]), 0, m.picW-1), Y: clampInt(int(m.scrollPos[1]), 0, m.picH-1)}
}

// driverKey maps the keys that type no text.
func driverKey(k mg.Key) (driver.Key, bool) {
	switch k {
	case mg.KeyEnter:
		return driver.KeyEnter, true
	case mg.KeyTab:
		return driver.KeyTab, true
	case mg.KeyBackspace:
		return driver.KeyDelete, true
	case mg.KeyDelete:
		return driver.KeyForwardDelete, true
	case mg.KeyEscape:
		return driver.KeyEscape, true
	case mg.KeyUp:
		return driver.KeyDpadUp, true
	case mg.KeyDown:
		return driver.KeyDpadDown, true
	case mg.KeyLeft:
		return driver.KeyDpadLeft, true
	case mg.KeyRight:
		return driver.KeyDpadRight, true
	case mg.KeyPageUp:
		return driver.KeyPageUp, true
	case mg.KeyPageDown:
		return driver.KeyPageDown, true
	}
	return "", false
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return max(lo, min(v, hi))
}

// videoLine describes the decoder for Settings › Advanced.
func videoLine(a *app) string { return videoLineIn(locales.In(i18n.Locale()), a) }

// videoLineIn is videoLine in the language of m; feedback's diagnostics
// are English.
func videoLineIn(m locales.Messages, a *app) string {
	if a.videoErr != nil {
		return a.videoErr.Error()
	}
	line := video.Version()
	for _, mr := range a.mirrors {
		if mr.player == nil {
			continue
		}
		st := mr.player.Stats()
		dec := m.MirrorDecoderSoftware()
		if st.Hardware != "" {
			dec = st.Hardware
		}
		line = m.MirrorDecoderStats(line, mr.id, dec, fmt.Sprintf("%.0f", st.FPS), st.Latency.Round(100*time.Microsecond).String())
	}
	return line
}

// turnRGBA turns a picture turn quarter turns clockwise.
func turnRGBA(img *image.RGBA, turn int) *image.RGBA {
	turn &= 3
	if turn == 0 {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	tw, th := w, h
	if turn%2 == 1 {
		tw, th = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := turnPoint(screen.Point{X: x, Y: y}, w, h, turn)
			si, di := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y), out.PixOffset(p.X, p.Y)
			copy(out.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return out
}
