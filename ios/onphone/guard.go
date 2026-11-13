package onphone

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// ErrOwnUI means the guard refused input while Pinky's own app was in
// front, where the person answers approvals and stops tasks. Nothing was
// done. It counts as driver.ErrUnsupported, which the gateway reports as an
// action that was not done, never as one that may have happened.
var ErrOwnUI error = ownUIError{}

type ownUIError struct{}

func (ownUIError) Error() string {
	return "Pinky's own app is in front, where only the person may act: wait for them to leave it, then observe again"
}

func (ownUIError) Is(target error) bool { return target == driver.ErrUnsupported }

// springboard is the home screen's bundle id.
const springboard = "com.apple.springboard"

// staleUI is how long after Pinky's app says it came to the front a read
// that finds another app there may correct it: an app that was killed
// never says it left.
const staleUI = 2 * time.Second

// gate is what the guard knows across connections: whether Pinky's own app
// is in front, and the app the task last worked in.
type gate struct {
	self string // Pinky's bundle id

	mu      sync.Mutex
	ui      bool
	uiSince time.Time
	changed chan struct{} // closed and replaced when ui changes
	last    string        // the last app read in front that is neither Pinky's nor the home screen
}

func newGate(self string) *gate { return &gate{self: self, changed: make(chan struct{})} }

// setUI records whether Pinky's app is in front, as the app reports it.
func (g *gate) setUI(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.setUILocked(on)
}

func (g *gate) setUILocked(on bool) {
	if on {
		g.uiSince = time.Now()
	}
	if g.ui == on {
		return
	}
	g.ui = on
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *gate) uiActive() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ui
}

// waitUI waits until ui is on, or for at most limit. It reports whether
// it is.
func (g *gate) waitUI(ctx context.Context, on bool, limit time.Duration) bool {
	t := time.NewTimer(limit)
	defer t.Stop()
	for {
		g.mu.Lock()
		ui, ch := g.ui, g.changed
		g.mu.Unlock()
		if ui == on {
			return true
		}
		select {
		case <-ch:
		case <-t.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

func (g *gate) lastApp() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last
}

// isSelf reports whether id is Pinky's app or one of its extensions. The
// app's id is the builder's choice (a free Apple ID cannot use Pinky's
// own), so it is learned from the app, besides the ids the policy knows.
func (g *gate) isSelf(id string) bool {
	return id != "" && (id == g.self || strings.HasPrefix(id, g.self+".") || policy.IsPinkyScreen(screen.App{ID: id}))
}

// check refuses input while Pinky's app is in front.
func (g *gate) check() error {
	if g.uiActive() {
		return ErrOwnUI
	}
	return nil
}

// saw learns from a screen read that started at start. Pinky's app gets
// the id the policy hides, also where the read could not name it; another
// app in front, read well after Pinky's app said it came to the front,
// means it left without saying so.
func (g *gate) saw(s *screen.Screen, start time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := s.App.ID
	switch {
	case g.isSelf(id), id == "" && g.ui:
		s.App.ID = policy.IPhoneApp
		return
	case id == "":
		return
	}
	if g.ui && start.After(g.uiSince.Add(staleUI)) {
		g.setUILocked(false)
	}
	if id != springboard {
		g.last = id
	}
}

// Guard is the iPhone the core drives: the iOS driver's Device, whose
// optional interfaces the gateway finds through embedding, with every
// method that acts on the phone refusing while Pinky's own app is in
// front. Reads pass through without pictures (treeOnly), and log why the
// accessibility service failed where the fallback's own error would hide
// it. The core itself leaves Pinky's app and comes back to the task's app
// through the embedded Device.
type Guard struct {
	*ios.Device
	g   *gate
	log *slog.Logger
}

// The core takes no pictures of the screen. On the iPhone 13 mini a
// screenshot is a PNG of 5 to 7 MB, which the driver decodes to 11 to 22 MB
// while Vision loads its text recognition for OCR: far more than the 50 MB
// iOS gives the tunnel extension, which it killed at the first read that
// took one (docs/notes/iphone-app.md, "End-to-end run"). So reads use the
// accessibility tree alone, and screenshots are unsupported: the agent is
// told the phone cannot take them.

// treeOnly is opt without OCR and with no picture allowed.
func treeOnly(opt driver.ObserveOptions) driver.ObserveOptions {
	opt.OCR, opt.OCRAll, opt.Frame = false, false, nil
	opt.MayCapture = func(*screen.Screen) bool { return false }
	return opt
}

// errNoPictures answers screenshots (treeOnly).
var errNoPictures = fmt.Errorf("Pinky on the iPhone takes no screenshots: they do not fit in the memory iOS gives it: %w", driver.ErrUnsupported)

func (d *Guard) Screenshot(context.Context) (image.Image, error) { return nil, errNoPictures }

func (d *Guard) ScreenshotPNG(context.Context) ([]byte, error) { return nil, errNoPictures }

// ScreenshotFromFrame implements driver.FrameShooter with no picture, so
// that the gateway asks Screenshot, which says why.
func (d *Guard) ScreenshotFromFrame(context.Context, func(context.Context) (image.Image, error)) (image.Image, bool) {
	return nil, false
}

func (d *Guard) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, _, err := d.ObserveWithStats(ctx, opt)
	return s, err
}

// slowRead is how long a read may take before the log says so.
const slowRead = 2 * time.Second

func (d *Guard) ObserveWithStats(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, ios.ObserveStats, error) {
	start := time.Now()
	s, st, err := d.Device.ObserveWithStats(ctx, treeOnly(opt))
	if err == nil && s != nil {
		d.g.saw(s, start)
	}
	if d.log != nil {
		switch {
		case err != nil:
			d.log.Warn("observe failed", "err", err, "after", time.Since(start).Round(time.Millisecond))
		case st.RAXError != nil:
			d.log.Warn("observe: remoteAXService failed", "err", st.RAXError, "source", st.Source)
		}
		if st.Total > slowRead {
			d.log.Info("observe: slow read", "walk", st.Walk.Round(time.Millisecond), "total", st.Total.Round(time.Millisecond))
		}
	}
	return s, st, err
}

func (d *Guard) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Tap(ctx, p, opt)
}

func (d *Guard) Swipe(ctx context.Context, from, to screen.Point, dur time.Duration) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Swipe(ctx, from, to, dur)
}

func (d *Guard) SwipeLatency(ctx context.Context, n int, vertical bool) ([]time.Duration, error) {
	if err := d.g.check(); err != nil {
		return nil, err
	}
	return d.Device.SwipeLatency(ctx, n, vertical)
}

func (d *Guard) Touch(ctx context.Context, a driver.TouchAction, p screen.Point) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Touch(ctx, a, p)
}

func (d *Guard) Drag(ctx context.Context, g driver.Drag) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Drag(ctx, g)
}

func (d *Guard) Type(ctx context.Context, text string, opt driver.TypeOptions) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Type(ctx, text, opt)
}

func (d *Guard) Key(ctx context.Context, k driver.Key) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Key(ctx, k)
}

func (d *Guard) Launch(ctx context.Context, app string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	if d.g.isSelf(app) {
		return ErrOwnUI
	}
	return d.Device.Launch(ctx, app)
}

func (d *Guard) LaunchID(ctx context.Context, bundleID string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	if d.g.isSelf(bundleID) {
		return ErrOwnUI
	}
	return d.Device.LaunchID(ctx, bundleID)
}

func (d *Guard) LaunchLocale(ctx context.Context, app string, loc driver.Locale) (func(context.Context) error, error) {
	if err := d.g.check(); err != nil {
		return nil, err
	}
	if d.g.isSelf(app) {
		return nil, ErrOwnUI
	}
	return d.Device.LaunchLocale(ctx, app, loc)
}

func (d *Guard) OpenLink(ctx context.Context, link string, app driver.App) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.OpenLink(ctx, link, app)
}

func (d *Guard) StopApp(ctx context.Context, id string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	if d.g.isSelf(id) {
		return ErrOwnUI
	}
	return d.Device.StopApp(ctx, id)
}

func (d *Guard) UninstallApp(ctx context.Context, id string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	if d.g.isSelf(id) {
		return ErrOwnUI
	}
	return d.Device.UninstallApp(ctx, id)
}

func (d *Guard) InstallPackage(ctx context.Context, paths []string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.InstallPackage(ctx, paths)
}

func (d *Guard) Unlock(ctx context.Context, pin string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Unlock(ctx, pin)
}

func (d *Guard) Wake(ctx context.Context) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Wake(ctx)
}
