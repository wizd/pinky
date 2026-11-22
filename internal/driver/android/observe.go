package android

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

const (
	keyguardCmd = `dumpsys window policy | grep -E 'showing=|mIsShowing=|isKeyguardShowing=|mShowingLockscreen=|isStatusBarKeyguard='`
	// imeWindowCmd reads the keyboard window from the window manager,
	// which is much faster than `dumpsys input_method` (that also dumps
	// the keyboard app: 1.7 s on the Pixel).
	imeWindowCmd = `dumpsys window InputMethod | grep -E 'isVisible=|isOnScreen='`
)

// Observe implements driver.Device. With the companion app it reads the
// element tree, toasts and notifications from the app (about 50 ms);
// otherwise from `uiautomator dump` (about 2.2 s, most of it uiautomator
// waiting for the UI to idle). Either way the lock and keyboard state come
// from the window manager, in parallel. Element bounds use the
// screenshot's pixel space. With opt.OCR, text the tree misses over a web
// view or a canvas is read from the pixels too (text.go). A screen that
// never stops changing, which uiautomator cannot read, comes back
// Unsettled, without a tree (observeRestless).
func (d *Device) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s, _, err := d.ObserveWithText(ctx, opt)
	return s, err
}

// ObserveWithText is Observe with what it did about OCR.
func (d *Device) ObserveWithText(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, TextStats, error) {
	if err := d.acting(ctx); err != nil {
		return nil, TextStats{}, err
	}
	since := time.Now()
	s, sc, err := d.observeTree(ctx)
	if err != nil {
		return nil, TextStats{}, err
	}
	src := textSource{
		shot:  d.screencap,
		moved: d.movedSince(0),
	}
	if d.info.Connection != "usb" {
		// Over Wi-Fi a screenshot's PNG (1 to 2 MB) takes seconds to come,
		// and tens of seconds over a phone's mobile uplink; a frame of the
		// video costs a key frame. Over USB a screenshot is the cheaper
		// one: a frame waits for the encoder to restart when no mirror is
		// open (1.1 to 1.5 s on an emulator, against 0.13 s for screencap).
		src.frame = opt.Frame
	}
	st := addText(ctx, s, sc, opt, since, src)
	d.textLog.note(d.cfg.logf, d.info.ID, st)
	return s, st, nil
}

// observeTree reads the tree, and what it says about where text is
// missing. On a screen that never stops changing (an animated or live
// page, GitHub issue #174) uiautomator cannot read it: the companion is
// tried again, as it reads without waiting, and without it the read says
// what it can without a tree (observeRestless).
func (d *Device) observeTree(ctx context.Context) (*screen.Screen, textScan, error) {
	if s, sc, ok, err := d.observeCompanionFor(ctx, forAgent); ok || err != nil {
		return s, sc, err
	}
	if was, ok := d.stillRestless(); ok {
		// uiautomator just waited its 10 s in vain on this window: unless
		// another one came to the front, it would again.
		s, sc, win, err := d.observeRestless(ctx)
		if err != nil {
			return nil, textScan{}, err
		}
		if win == was {
			return s, sc, nil
		}
	}
	s, sc, err := d.observeDump(ctx)
	if !errors.Is(err, errNeverIdle) {
		if err == nil {
			d.noteRestless(false, "")
		}
		return s, sc, err
	}
	if s, sc, ok, err := d.observeCompanionFor(ctx, forRestless); ok || err != nil {
		return s, sc, err
	}
	s, sc, win, err := d.observeRestless(ctx)
	if err != nil {
		return nil, textScan{}, err
	}
	d.noteRestless(true, win)
	return s, sc, nil
}

// restlessSkip is how long after uiautomator found a window never still
// reads of the same window do without it. uiautomator waits 10 s for the
// screen to stop changing before it gives up, which every read and every
// action's two reads would wait again.
var restlessSkip = 20 * time.Second

// noteRestless records whether uiautomator found the screen never still,
// with win in front.
func (d *Device) noteRestless(restless bool, win string) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if restless {
		d.restlessAt, d.restlessWin = time.Now(), win
	} else {
		d.restlessAt, d.restlessWin = time.Time{}, ""
	}
}

// stillRestless returns the window uiautomator found never still less than
// restlessSkip ago.
func (d *Device) stillRestless() (string, bool) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.restlessAt.IsZero() || time.Since(d.restlessAt) >= restlessSkip {
		return "", false
	}
	return d.restlessWin, true
}

// restlessCmd reads what a read without a tree says: the lock, the
// keyboard and the focused activity, with its window manager line kept as
// the window's identity.
const restlessCmd = keyguardCmd + "; echo --ime--; " + imeWindowCmd + "; echo --focus--; dumpsys window displays | grep -E 'mFocusedApp='"

// observeRestless reads a screen whose tree cannot be read because it
// never stops changing: its size, app, lock and keyboard from the window
// manager, and no elements. The whole screen is an area whose text the
// tree misses, so a read with OCR adds the text it reads from a picture
// (text.go), which does not need the screen to be still; without a
// recognizer the area is marked no_ocr. win is the focused activity, as
// the window manager names it.
func (d *Device) observeRestless(ctx context.Context) (s *screen.Screen, sc textScan, win string, err error) {
	captured := time.Now()
	statec := make(chan string, 1)
	go func() {
		out, err := d.client.Shell(ctx, d.info.ID, restlessCmd)
		if err != nil {
			d.cfg.logf("android: %s: read state: %v", d.info.ID, err)
		}
		statec <- string(out)
	}()
	appsc := make(chan struct{})
	go func() {
		defer close(appsc)
		_, _ = d.cachedApps(ctx, 0)
	}()
	natW, natH, err := d.naturalSize(ctx)
	if err == nil {
		// The current size says how the screen is turned; the natural size
		// keeps the pixel space the dump's reads use.
		if cw, ch, cerr := d.displaySize(ctx); cerr == nil {
			natW, natH = orient(natW, natH, cw, ch)
		}
	}
	stateOut := <-statec
	<-appsc
	if err != nil {
		return nil, textScan{}, "", err
	}
	if ctx.Err() != nil {
		return nil, textScan{}, "", ctx.Err()
	}
	sec := splitSections(stateOut)
	locked, _ := parseKeyguard(sec[""])
	s = &screen.Screen{
		Width:       natW,
		Height:      natH,
		Orientation: "portrait",
		Keyboard:    parseIMEWindowVisible(sec["ime"]),
		Locked:      locked,
		CapturedAt:  captured,
		Unsettled:   true,
	}
	if natW > natH {
		s.Orientation = "landscape"
	}
	pkg := parseFocusedApp(sec["focus"])
	s.App = screen.App{ID: pkg, Name: d.appName(pkg)}
	full := screen.Rect{W: natW, H: natH}
	sc = textScan{areas: []textArea{{rect: full, kind: "window", path: "restless"}}}
	return s, sc, strings.TrimSpace(sec["focus"]), nil
}

// observeCompanionFor reads the screen from the companion, when it is on
// for use; ok is false when the caller should read it another way.
func (d *Device) observeCompanionFor(ctx context.Context, use companionUse) (*screen.Screen, textScan, bool, error) {
	c := d.companionClientFor(ctx, use)
	if c == nil {
		return nil, textScan{}, false, nil
	}
	s, sc, err := d.observeCompanion(ctx, c)
	if err == nil && emptyWithKeyboard(s) {
		err = errEmptyTree
	}
	if err == nil {
		return s, sc, true, nil
	}
	if ctx.Err() != nil {
		return nil, textScan{}, false, ctx.Err()
	}
	// Turn it off before uiautomator runs, and fall back.
	d.companionFailed(err)
	return nil, textScan{}, false, nil
}

// movedSince returns textSource.moved for a display: whether a window
// event came since the tree was read, from the companion's events, or
// without them whether the window manager's focused app is another one.
func (d *Device) movedSince(display int) func(ctx context.Context, since time.Time, app screen.App) bool {
	return func(ctx context.Context, since time.Time, app screen.App) bool {
		if changed, ok := d.comp.windowSince(display, since); ok {
			return changed
		}
		out, err := d.client.Shell(ctx, d.info.ID, "dumpsys window displays | grep -E 'mFocusedApp='")
		if err != nil {
			return false
		}
		pkg := parseFocusedApp(string(out))
		return pkg != "" && app.ID != "" && pkg != app.ID
	}
}

// ReadsText implements driver.TextReader: this computer has a recognizer.
func (d *Device) ReadsText() bool { return ocrSupported() == nil }

// errEmptyTree is a companion tree with no elements while an app shows the
// keyboard, even with the keyboard's own nodes: the read missed something,
// so uiautomator reads the screen instead.
var errEmptyTree = errors.New("the tree has no elements while the keyboard shows")

// emptyWithKeyboard reports whether a screen is empty although an app has
// the keyboard up, which only happens when the tree is incomplete.
func emptyWithKeyboard(s *screen.Screen) bool {
	return len(s.Elements) == 0 && s.Keyboard && !s.Locked && s.App.ID != ""
}

// observeDump reads the screen with `uiautomator dump`.
func (d *Device) observeDump(ctx context.Context) (*screen.Screen, textScan, error) {
	captured := time.Now()
	type dumpResult struct {
		tree *uiTree
		err  error
	}
	dumpc := make(chan dumpResult, 1)
	go func() {
		t, err := d.dumpUI(ctx)
		dumpc <- dumpResult{t, err}
	}()
	statec := make(chan string, 1)
	go func() {
		out, err := d.client.Shell(ctx, d.info.ID, keyguardCmd+"; echo --ime--; "+imeWindowCmd)
		if err != nil {
			d.cfg.logf("android: %s: read state: %v", d.info.ID, err)
		}
		statec <- string(out)
	}()
	appsc := make(chan struct{})
	go func() {
		defer close(appsc)
		_, _ = d.cachedApps(ctx, 0)
	}()

	natW, natH, err := d.naturalSize(ctx)
	if err != nil {
		return nil, textScan{}, err
	}
	r := <-dumpc
	stateOut := <-statec
	<-appsc
	if r.err != nil {
		return nil, textScan{}, r.err
	}
	w, h := rotate(natW, natH, r.tree.Rotation)
	sec := splitSections(stateOut)
	locked, _ := parseKeyguard(sec[""])
	scr := &screen.Screen{
		Width:       w,
		Height:      h,
		Orientation: "portrait",
		Keyboard:    parseIMEWindowVisible(sec["ime"]),
		Locked:      locked,
		Elements:    convertTree(r.tree, w, h),
		CapturedAt:  captured,
	}
	if w > h {
		scr.Orientation = "landscape"
	}
	pkg := r.tree.Package()
	scr.App = screen.App{ID: pkg, Name: d.appName(pkg)}
	return scr, scanDump(r.tree, w, h), nil
}

// dumpUI runs `uiautomator dump`, writing to /dev/tty so the XML comes back
// on stdout, or to a temporary file on phones where that does not work.
func (d *Device) dumpUI(ctx context.Context) (*uiTree, error) {
	d.dumpMu.Lock()
	defer d.dumpMu.Unlock()
	if !d.dumpFile {
		out, err := d.client.Exec(ctx, d.info.ID, "uiautomator dump /dev/tty")
		if err != nil {
			return nil, wrapErr(err)
		}
		t, perr := parseUIDump(out)
		if perr == nil {
			return t, nil
		}
		if !errors.Is(perr, errNoHierarchy) || dumpFailed(out) {
			return nil, dumpError(out, perr)
		}
		d.cfg.logf("android: %s: uiautomator cannot dump to /dev/tty (%q); using a file", d.info.ID, firstLine(out))
		d.dumpFile = true
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	path := "/data/local/tmp/pinky-ui-" + hex.EncodeToString(b[:]) + ".xml"
	// Only its idle error comes through: it says why there is no file.
	out, err := d.client.Exec(ctx, d.info.ID,
		"uiautomator dump "+path+" 2>&1 | grep -F 'could not get idle state'; cat "+path+" 2>/dev/null; rm -f "+path)
	if err != nil {
		return nil, wrapErr(err)
	}
	t, perr := parseUIDump(out)
	if perr != nil {
		return nil, dumpError(out, perr)
	}
	return t, nil
}

func dumpFailed(out []byte) bool {
	return bytes.Contains(out, []byte("ERROR")) || bytes.Contains(out, []byte("Exception"))
}

// errNeverIdle: uiautomator gave up waiting for the screen to stop
// changing (10 s), and read nothing.
var errNeverIdle = errors.New("android: uiautomator dump: the screen never stopped changing (could not get idle state)")

func dumpError(out []byte, err error) error {
	if bytes.Contains(out, []byte("could not get idle state")) {
		return errNeverIdle
	}
	if l := firstLine(out); l != "" {
		return fmt.Errorf("android: uiautomator dump: %v: %s", err, l)
	}
	return fmt.Errorf("android: uiautomator dump: %w", err)
}

func firstLine(out []byte) string {
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// Screenshot implements driver.Device with `screencap -p`, in the same
// pixel space as Observe.
func (d *Device) Screenshot(ctx context.Context) (image.Image, error) {
	if err := d.acting(ctx); err != nil {
		return nil, err
	}
	out, err := d.screencap(ctx)
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("android: screencap: %v (%d bytes: %q)", err, len(out), firstLine(out))
	}
	return img, nil
}

// Locked implements driver.LockReporter: true while the lock screen is
// showing, even when an app such as an alarm covers it.
func (d *Device) Locked(ctx context.Context) (bool, error) {
	out, err := d.client.Shell(ctx, d.info.ID, keyguardCmd)
	if err != nil {
		return false, wrapErr(err)
	}
	if showing, ok := parseKeyguard(string(out)); ok {
		return showing, nil
	}
	// Some builds keep the flag only in the full window dump.
	out, err = d.client.Shell(ctx, d.info.ID, `dumpsys window | grep -E 'isKeyguardShowing=|mShowingLockscreen=|isStatusBarKeyguard='`)
	if err != nil {
		return false, wrapErr(err)
	}
	showing, ok := parseKeyguard(string(out))
	if !ok {
		return false, errors.New("android: cannot tell whether the phone is locked")
	}
	return showing, nil
}
