package local

import (
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Background displays on the phone itself (docs/notes/android-wadb.md):
// with the shell user's adb connection that Wireless debugging gives the
// app (package wadb), the core runs the same scrcpy server as desktop Pinky
// to create a virtual display, starts the task's apps there and takes its
// video for the floating window. Reads and input on it go through the
// accessibility service, which takes a display id (FeatureDisplays,
// FeatureDisplayInput), so injected input, which some phones drop on
// virtual displays, is needed only for keys the service has no action for.

// Shell is the shell user's adb connection to the phone's own adbd.
type Shell interface {
	// On reports whether the person turned background displays on: the
	// phone then offers them to the gateway, and Client tries to connect.
	On() bool
	// Client returns an adb client whose Direct reaches the phone's adbd,
	// or an error saying what the person has to do first (pair, turn
	// Wireless debugging on).
	Client(ctx context.Context) (*adb.Client, error)
}

// shellSerial is the serial the adb client is given: Direct ignores it.
const shellSerial = "self"

// minBackgroundSDK is the first Android whose virtual displays stay
// usable while the phone is locked: Android 13.
const minBackgroundSDK = 33

// SetShell gives the phone the shell connection that background displays
// need; nil takes it away.
func (p *Provider) SetShell(s Shell) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shell = s
	if p.dev != nil {
		p.dev.setShell(s)
	}
}

func (d *Device) setShell(s Shell) {
	d.bgMu.Lock()
	d.shell = s
	d.bgMu.Unlock()
}

func (d *Device) shellOn() (Shell, bool) {
	d.bgMu.Lock()
	s := d.shell
	d.bgMu.Unlock()
	return s, s != nil && s.On() && d.sdk >= minBackgroundSDK && d.displayInput
}

// BackgroundSupported implements driver.BackgroundSupporter: while the
// person has background displays on, on Android 13 and later.
func (d *Device) BackgroundSupported() bool {
	_, ok := d.shellOn()
	return ok
}

// BackgroundByDefault implements driver.BackgroundDefaulter: turning the
// feature on is the person's choice of background displays.
func (d *Device) BackgroundByDefault() bool { return d.BackgroundSupported() }

// OpenBackground implements driver.Backgrounder. Errors leave the task on
// the phone's own screen (the gateway says so to the agent).
func (d *Device) OpenBackground(ctx context.Context, opt driver.BackgroundOptions) (driver.Device, error) {
	sh, ok := d.shellOn()
	if !ok {
		return nil, fmt.Errorf("local: background displays are off: %w", driver.ErrUnsupported)
	}
	cl, err := sh.Client(ctx)
	if err != nil {
		return nil, err
	}
	// The display takes the main display's size, so that apps lay out as
	// the person knows them.
	t, err := d.c.Dump(ctx, companion.DumpOptions{MaxNodes: 1})
	if err != nil {
		return nil, d.wrap(err)
	}
	w, h, dpi := t.Display.Width, t.Display.Height, t.Display.DensityDPI
	if w <= 0 || h <= 0 {
		return nil, errors.New("local: the main display's size is unknown")
	}
	s, err := scrcpy.Start(ctx, cl, shellSerial, scrcpy.Options{
		// The floating window shows it small; the agent reads the tree.
		MaxSize:      1280,
		MaxFPS:       30,
		VideoBitRate: 4_000_000,
		Virtual:      &scrcpy.VirtualDisplay{Width: w, Height: h, DPI: dpi, NoSystemDecorations: true},
		// The keyboard stays on the display, off the person's screen.
		DisplayIMEPolicy: "local",
	})
	if err != nil {
		return nil, fmt.Errorf("local: background display: %w", err)
	}
	id, err := s.DisplayID(ctx)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("local: background display: %w", err)
	}
	b := &Background{d: d, s: s, display: id, w: w, h: h, done: make(chan struct{})}
	go b.read()
	if opt.App != "" {
		if err := b.Launch(ctx, opt.App); err != nil {
			b.Close()
			return nil, err
		}
	}
	d.bgMu.Lock()
	d.bg = b
	d.bgMu.Unlock()
	return b, nil
}

// Background is a virtual display the core created for a task.
type Background struct {
	d       *Device
	s       *scrcpy.Session
	display int
	w, h    int

	mu     sync.Mutex
	vw, vh int     // the video's size
	sub    *stream // the one consumer of the video
	reset  time.Time
	closed bool
	done   chan struct{}

	// The last app started on the display, for launchGrace.
	launchPkg  string
	launchedAt time.Time
	nudges     int
}

// DisplayID is the display's id.
func (b *Background) DisplayID() int { return b.display }

// read hands the video to the consumer until the session ends.
func (b *Background) read() {
	defer close(b.done)
	for {
		p, err := b.s.ReadPacket()
		if err != nil {
			b.mu.Lock()
			sub := b.sub
			b.sub = nil
			b.mu.Unlock()
			if sub != nil {
				sub.end(err)
			}
			return
		}
		b.mu.Lock()
		if p.Session {
			b.vw, b.vh = p.Width, p.Height
		}
		sub := b.sub
		b.mu.Unlock()
		if sub != nil && !p.Session {
			sub.push(p)
		}
	}
}

func (b *Background) resetVideo() {
	b.mu.Lock()
	if time.Since(b.reset) < time.Second {
		b.mu.Unlock()
		return
	}
	b.reset = time.Now()
	b.mu.Unlock()
	_ = b.s.Send(scrcpy.ResetVideo())
}

func (b *Background) Info() driver.Info {
	i := b.d.Info()
	i.Backend = "accessibility+display"
	return i
}

// Observe reads the display's windows.
func (b *Background) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	t, err := b.d.c.Dump(ctx, companion.DumpOptions{Display: b.display})
	if err != nil {
		if errors.Is(err, driver.ErrUnsupported) && ctx.Err() == nil && b.untracked(time.Now()) {
			// Not tracked by accessibility yet (see launchGrace): an
			// empty display, not one Pinky cannot read.
			b.nudge(ctx, time.Now())
			return b.emptyScreen(), nil
		}
		return nil, b.d.wrap(err)
	}
	s := agentScreen(t, b.d.app.Package)
	s.CapturedAt = b.d.c.HostTime(t.Time)
	if s.App.Name == "" {
		s.App.Name = b.d.appName(s.App.ID)
	}
	if s.App.ID == "" {
		b.nudge(ctx, time.Now())
	}
	return s, nil
}

// Right after an app starts on the display, accessibility may not track
// the display yet (the companion answers unsupported, "not open to
// accessibility services") or show only the splash screen, which it
// leaves out. On the API 36 emulator the first read after the launch
// failed that way, and the gateway then moved the task to the phone's own
// screen for good. As desktop Pinky does (internal/driver/android,
// launchGrace), such reads count as an empty display for a while, and
// nudge the display once each: a key that does nothing, then a second
// start of the app.
const (
	launchGrace      = 10 * time.Second
	nudgeKeyAfter    = 1500 * time.Millisecond
	nudgeStartAfter  = 4 * time.Second
	nudgeAfterLaunch = 15 * time.Second
	keycodeUnknown   = 0 // KEYCODE_UNKNOWN: apps ignore it, but it counts as input on the display
)

// untracked reports whether a read that finds the display untracked at
// now means an empty display: before any app was started on it, or
// within launchGrace of the last start.
func (b *Background) untracked(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launchedAt.IsZero() || now.Sub(b.launchedAt) < launchGrace
}

// nudgeDue says which nudge, if any, is due at now for a read that found
// no app on the display: 1 for the key, 2 for starting the app again, 0
// for none. It counts the nudge as sent.
func (b *Background) nudgeDue(now time.Time) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.launchedAt.IsZero() || b.launchPkg == "" {
		return 0
	}
	since := now.Sub(b.launchedAt)
	if since >= nudgeAfterLaunch {
		return 0
	}
	switch {
	case b.nudges == 0 && since >= nudgeKeyAfter:
		b.nudges = 1
		return 1
	case b.nudges == 1 && since >= nudgeStartAfter:
		b.nudges = 2
		return 2
	}
	return 0
}

// nudge sends the nudge that is due, if any (see launchGrace).
func (b *Background) nudge(ctx context.Context, now time.Time) {
	switch b.nudgeDue(now) {
	case 1:
		if err := b.s.Send(scrcpy.InjectKeycode(scrcpy.ActionDown, keycodeUnknown, 0, 0)); err == nil {
			_ = b.s.Send(scrcpy.InjectKeycode(scrcpy.ActionUp, keycodeUnknown, 0, 0))
		}
	case 2:
		b.mu.Lock()
		pkg := b.launchPkg
		b.mu.Unlock()
		_, _ = b.s.StartApp(ctx, pkg)
	}
}

// emptyScreen is the display with nothing on it that accessibility shows.
func (b *Background) emptyScreen() *screen.Screen {
	o := "portrait"
	if b.w > b.h {
		o = "landscape"
	}
	return &screen.Screen{Width: b.w, Height: b.h, Orientation: o, CapturedAt: time.Now()}
}

// Screenshot is not offered: the core on the phone sends no pictures.
func (b *Background) Screenshot(ctx context.Context) (image.Image, error) {
	return nil, fmt.Errorf("local: screenshots of a background display: %w", driver.ErrUnsupported)
}

func (b *Background) gesture(ctx context.Context, strokes ...companion.Stroke) error {
	return b.d.wrap(b.d.c.Gesture(ctx, b.display, strokes...))
}

func (b *Background) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	dur := tapDuration
	if opt.LongPress {
		dur = longPressDuration
	}
	return b.gesture(ctx, companion.Stroke{Points: []screen.Point{p}, Duration: dur})
}

func (b *Background) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	if dur <= 0 {
		dur = swipeDuration
	}
	return b.gesture(ctx, companion.Stroke{Points: []screen.Point{from, to}, Duration: dur})
}

// Touch implements driver.Toucher for the person's hand in the floating
// window, with scrcpy's injected touches: the point is in the video's
// pixels.
func (b *Background) Touch(ctx context.Context, a driver.TouchAction, p screen.Point) error {
	var action uint8
	switch a {
	case driver.TouchDown:
		action = scrcpy.ActionDown
	case driver.TouchMove:
		action = scrcpy.ActionMove
	case driver.TouchUp:
		action = scrcpy.ActionUp
	default:
		return fmt.Errorf("local: touch %q: %w", a, driver.ErrUnsupported)
	}
	b.mu.Lock()
	vw, vh := b.vw, b.vh
	b.mu.Unlock()
	if vw == 0 || vh == 0 {
		vw, vh = b.w, b.h
	}
	pressure := 1.0
	if a == driver.TouchUp {
		pressure = 0
	}
	pos := scrcpy.Position{X: int32(p.X), Y: int32(p.Y), Width: uint16(vw), Height: uint16(vh)}
	return b.s.Send(scrcpy.InjectTouch(action, scrcpy.PointerGenericFinger, pos, pressure, 0, 0))
}

func (b *Background) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	r, err := b.d.c.SetText(ctx, companion.Target{}, text, companion.SetTextOptions{
		Append: opt.Append, Submit: opt.Submit, Display: b.display,
	})
	if err != nil {
		return b.d.wrap(err)
	}
	if opt.Submit && !r.Submitted {
		return b.Key(ctx, driver.KeyEnter)
	}
	return nil
}

// Key presses back or enter with the service on the display, and the
// other keys with scrcpy's injected key events.
func (b *Background) Key(ctx context.Context, k driver.Key) error {
	switch k {
	case driver.KeyBack, driver.KeyEnter:
		err := b.d.c.Global(ctx, b.display, string(k))
		if !companion.IsCode(err, companion.CodeNotFocused) {
			return b.d.wrap(err)
		}
		// Back needs the display to have the phone's focus: inject it.
	}
	code, ok := keycodes[k]
	if !ok {
		return fmt.Errorf("local: key %q: %w", k, driver.ErrUnsupported)
	}
	if err := b.s.Send(scrcpy.InjectKeycode(scrcpy.ActionDown, code, 0, 0)); err != nil {
		return err
	}
	return b.s.Send(scrcpy.InjectKeycode(scrcpy.ActionUp, code, 0, 0))
}

// keycodes are Android's KEYCODE_ values for the keys scrcpy injects.
var keycodes = map[driver.Key]int32{
	driver.KeyBack:    4,
	driver.KeyHome:    3,
	driver.KeyRecents: 187,
	driver.KeyEnter:   66,
}

// Launch starts an app on the display.
func (b *Background) Launch(ctx context.Context, app string) error {
	pkg, err := b.d.resolve(ctx, app)
	if err != nil {
		return err
	}
	started, err := b.s.StartApp(ctx, pkg)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("local: launch %q: %w", app, driver.ErrNotFound)
		}
		return err
	}
	if started.Display != b.display {
		// The app refused a second display: the gateway moves the task to
		// the phone's own screen.
		return fmt.Errorf("local: %s runs only on the phone's own screen: %w", pkg, driver.ErrUnsupported)
	}
	b.mu.Lock()
	b.launchPkg, b.launchedAt, b.nudges = pkg, time.Now(), 0
	b.mu.Unlock()
	return nil
}

func (b *Background) Apps(ctx context.Context) ([]driver.App, error) { return b.d.Apps(ctx) }

// Stream implements driver.Device: the display's video, for the floating
// window and the recording.
func (b *Background) Stream(ctx context.Context, _ driver.StreamOptions) (driver.Stream, error) {
	st := &stream{b: b, ch: make(chan driver.Packet, streamBuffer), waitStart: true}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("local: the background display closed: %w", driver.ErrOffline)
	}
	old := b.sub
	b.sub = st
	b.mu.Unlock()
	if old != nil {
		old.end(errors.New("local: replaced by a new stream"))
	}
	b.resetVideo()
	return st, nil
}

// WaitSettled waits for the service's events to calm down.
func (b *Background) WaitSettled(ctx context.Context, max time.Duration) (bool, error) {
	ok, err := b.d.c.WaitSettled(ctx, max)
	return ok, b.d.wrap(err)
}

// Locked implements driver.LockReporter: from Android 13 a virtual display
// works while the phone is locked.
func (b *Background) Locked(ctx context.Context) (bool, error) { return false, nil }

// Done is closed once the display is gone.
func (b *Background) Done() <-chan struct{} { return b.done }

// Close closes the display and the apps on it.
func (b *Background) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.d.bgMu.Lock()
	if b.d.bg == b {
		b.d.bg = nil
	}
	b.d.bgMu.Unlock()
	return b.s.Close()
}

// streamBuffer is how many packets a slow consumer may fall behind before
// packets are dropped until a fresh key frame.
const streamBuffer = 128

// stream is the display's video for one consumer, from a configuration
// packet on, as the desktop driver's.
type stream struct {
	b  *Background
	ch chan driver.Packet

	mu        sync.Mutex
	waitStart bool
	dropping  bool
	closed    bool
	err       error
}

func (st *stream) push(p scrcpy.Packet) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return
	}
	switch {
	case st.dropping:
		if len(st.ch) > cap(st.ch)/2 {
			return
		}
		if !p.Config {
			go st.b.resetVideo()
			return
		}
		st.dropping = false
	case st.waitStart:
		if !p.Config {
			return
		}
		st.waitStart = false
	}
	select {
	case st.ch <- driver.Packet{Data: p.Data, PTS: p.PTS, Config: p.Config, KeyFrame: p.KeyFrame}:
	default:
		st.dropping = true
	}
}

// RequestKeyFrame asks the encoder for a fresh configuration and key frame.
func (st *stream) RequestKeyFrame() { st.b.resetVideo() }

func (st *stream) end(err error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		return
	}
	st.closed, st.err = true, err
	close(st.ch)
}

func (st *stream) Codec() driver.Codec {
	switch st.b.s.Codec {
	case scrcpy.CodecH265:
		return driver.HEVC
	}
	return driver.H264
}

func (st *stream) Size() (int, int) {
	st.b.mu.Lock()
	defer st.b.mu.Unlock()
	return st.b.vw, st.b.vh
}

func (st *stream) Packets() <-chan driver.Packet { return st.ch }

func (st *stream) Err() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.err
}

func (st *stream) Close() error {
	st.b.mu.Lock()
	if st.b.sub == st {
		st.b.sub = nil
	}
	st.b.mu.Unlock()
	st.end(nil)
	return nil
}

var (
	_ driver.Backgrounder        = (*Device)(nil)
	_ driver.BackgroundDefaulter = (*Device)(nil)
	_ driver.BackgroundSupporter = (*Device)(nil)
	_ driver.Device              = (*Background)(nil)
	_ driver.Settler             = (*Background)(nil)
	_ driver.LockReporter        = (*Background)(nil)
	_ driver.Toucher             = (*Background)(nil)
)

// BackgroundTouch sends the person's finger on the floating window to the
// background display a task works on; driver.ErrNotFound when none is open.
func (p *Provider) BackgroundTouch(ctx context.Context, a driver.TouchAction, pt screen.Point) error {
	p.mu.Lock()
	d := p.dev
	p.mu.Unlock()
	if d == nil {
		return fmt.Errorf("local: no background display: %w", driver.ErrNotFound)
	}
	d.bgMu.Lock()
	b := d.bg
	d.bgMu.Unlock()
	if b == nil {
		return fmt.Errorf("local: no background display: %w", driver.ErrNotFound)
	}
	return b.Touch(ctx, a, pt)
}
