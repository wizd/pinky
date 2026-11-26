package ios

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// Attaching the virtual HID keyboard (hidSession.ensureKeyboard) makes iOS
// treat the phone as having a hardware keyboard and hide the on-screen
// keyboard (UIKeyboardWillHideNotification). Most native fields keep first
// responder across that transition, but some custom or hybrid editors (such
// as Xianyu's service description editor) treat keyboard dismissal as leaving
// edit mode and immediately call resignFirstResponder(). Because Pinky drops
// its virtual keyboard at the end of every Type call (GitHub issue #65), a
// subsequent phone_tap + phone_type repeats the same cycle forever.
//
// To break that loop, Type snapshots the focused element before registering
// the virtual keyboard. If attaching the keyboard drops focus to nil, Pinky
// re-taps the field while the virtual keyboard stays attached (so no new
// UIKeyboardWillHide transition fires) and waits briefly for focus to return
// before clearing and pasting or typing.

const (
	// lastTapMaxAge bounds how long a prior Tap point remains eligible as a
	// fallback re-focus target when a focused field unmounts on blur.
	lastTapMaxAge = 2 * time.Minute

	// minRefocusPoints rejects empty or 1x1 dummy accessibility frames
	// (such as hidden Flutter/hybrid text input proxies).
	minRefocusPoints = 8

	// refocusWait and refocusPoll bound waiting for focus to return after
	// re-tapping a field whose focus was knocked out by ensureKeyboard.
	refocusWait = 800 * time.Millisecond
	refocusPoll = 50 * time.Millisecond

	// Timings for custom editors that never expose focusedElement in
	// remoteAXService (KeyboardOnly) and for the software-keyboard callout
	// paste fallback when HID Command-V is ignored.
	kbHideWait        = 500 * time.Millisecond
	kbHideSettle      = 100 * time.Millisecond
	kbOnlyRefocusWait = 350 * time.Millisecond
	calloutDetachWait = 250 * time.Millisecond
	calloutKBOpenWait = 800 * time.Millisecond
	calloutKBSettle   = 200 * time.Millisecond
	calloutLongPress  = 550 * time.Millisecond
	calloutMenuWait   = 400 * time.Millisecond
)

// tapRecord remembers the most recent non-long-press Tap point.
type tapRecord struct {
	point screen.Point
	at    time.Time
}

func (d *Device) recordTap(p screen.Point) {
	d.mu.Lock()
	d.lastTap = tapRecord{point: p, at: time.Now()}
	d.mu.Unlock()
}

func (d *Device) clearLastTap() {
	d.mu.Lock()
	d.lastTap = tapRecord{}
	d.mu.Unlock()
}

func (d *Device) recentTap(now time.Time) (screen.Point, bool) {
	d.mu.Lock()
	lt := d.lastTap
	d.mu.Unlock()
	if lt.at.IsZero() || now.Sub(lt.at) > lastTapMaxAge {
		return screen.Point{}, false
	}
	return lt.point, true
}

// focusSnap holds the pre-keyboard metadata of a focused element so Pinky can
// relocate and re-tap it if attaching the virtual keyboard knocks focus out.
// KeyboardOnly is true when focusedElement returned nil (e.g. a custom Flutter
// or Weex editor whose input proxy is hidden from remoteAXService) while the
// system software keyboard was visible and a recent tap landed outside it.
type focusSnap struct {
	Object       *axpObject
	Frame        axpRect
	HasFrame     bool
	Visible      bool
	Label        string
	Placeholder  string
	Ident        string
	KeyboardOnly bool
}

// focusSource abstracts the remoteAXService queries used for focus recovery,
// so the recovery logic can be unit-tested without a real phone.
type focusSource interface {
	focusedElement(ctx context.Context) (*axpObject, error)
	liveFrame(ctx context.Context, o *axpObject) (axpRect, bool)
	appFrame(ctx context.Context) (axpRect, bool)
	walkElements(ctx context.Context) ([]raxElement, error)
}

type deviceFocusSource struct {
	rax *raxSession
	lim walkLimits
}

func (s deviceFocusSource) focusedElement(ctx context.Context) (*axpObject, error) {
	return s.rax.focusedElement(ctx)
}

func (s deviceFocusSource) liveFrame(ctx context.Context, o *axpObject) (axpRect, bool) {
	return s.rax.liveFrame(ctx, o)
}

func (s deviceFocusSource) appFrame(ctx context.Context) (axpRect, bool) {
	return s.rax.frontmostAppFrame(ctx)
}

func (s deviceFocusSource) walkElements(ctx context.Context) ([]raxElement, error) {
	wctx, cancel := context.WithTimeout(ctx, s.lim.Budget+raxRequestTimeout)
	defer cancel()
	els, _, err := s.rax.walk(wctx, s.lim)
	return els, err
}

// focusSnap reads the attributes of o needed to relocate it if focus drops.
func (s *raxSession) focusSnap(ctx context.Context, o *axpObject) focusSnap {
	snap := focusSnap{Object: o, Visible: true}
	r, err := s.request(ctx, axpRequest{
		Type:   axpReqAttributes,
		Client: raxClientType,
		Object: o,
		Params: map[string]any{"attributes": []int{
			axpAttrFrame, axpAttrIsVisible, axpAttrLabel, axpAttrPlaceholder, axpAttrIdentifier,
		}},
	})
	if err != nil {
		return snap
	}
	ad, ok := r.Result.(axpDict)
	if !ok {
		return snap
	}
	snap.Frame, snap.HasFrame = ad[axpAttrFrame].(axpRect)
	if v, ok := ad[axpAttrIsVisible].(bool); ok {
		snap.Visible = v
	}
	snap.Label, _ = ad[axpAttrLabel].(string)
	snap.Placeholder, _ = ad[axpAttrPlaceholder].(string)
	snap.Ident, _ = ad[axpAttrIdentifier].(string)
	return snap
}

// liveFrame reads o's current frame and visibility after a layout change.
func (s *raxSession) liveFrame(ctx context.Context, o *axpObject) (axpRect, bool) {
	if o == nil {
		return axpRect{}, false
	}
	r, err := s.request(ctx, axpRequest{
		Type:   axpReqAttributes,
		Client: raxClientType,
		Object: o,
		Params: map[string]any{"attributes": []int{axpAttrFrame, axpAttrIsVisible}},
	})
	if err != nil {
		return axpRect{}, false
	}
	ad, ok := r.Result.(axpDict)
	if !ok {
		return axpRect{}, false
	}
	if v, ok := ad[axpAttrIsVisible].(bool); ok && !v {
		return axpRect{}, false
	}
	f, ok := ad[axpAttrFrame].(axpRect)
	return f, ok
}

// frontmostAppFrame returns the frontmost application's frame in points, so
// single-element frames use the same Display Zoom / foldable scale as walks.
func (s *raxSession) frontmostAppFrame(ctx context.Context) (axpRect, bool) {
	app, err := s.frontmostApp(ctx)
	if err != nil {
		return axpRect{}, false
	}
	r, err := s.request(ctx, axpRequest{
		Type:   axpReqAttributes,
		Client: raxClientType,
		Object: app,
		Params: map[string]any{"attributes": []int{axpAttrFrame}},
	})
	if err != nil {
		return axpRect{}, false
	}
	ad, ok := r.Result.(axpDict)
	if !ok {
		return axpRect{}, false
	}
	f, ok := ad[axpAttrFrame].(axpRect)
	return f, ok && f.W > 0
}

// captureFocusSnap snapshots the currently focused element before the virtual
// keyboard attaches. hint may already hold the focused element from
// focusedField; when nil (e.g. a custom view whose value attribute failed),
// it queries focusedElement directly. When focusedElement is nil (as in
// custom/Flutter editors that hide their input proxy from remoteAXService),
// it checks whether the system software keyboard is up alongside a recent Tap
// outside the keyboard and returns a KeyboardOnly snap.
func (d *Device) captureFocusSnap(ctx context.Context, hint *axpObject, hintErr error) *focusSnap {
	rax, err := d.raxNow()
	if err != nil {
		return nil
	}
	c := context.WithoutCancel(ctx)
	el := hint
	if el == nil && hintErr != nil {
		el, _ = rax.focusedElement(c)
	}
	if el != nil {
		s := rax.focusSnap(c, el)
		return &s
	}
	pt, hasTap := d.recentTap(time.Now())
	if !hasTap {
		return nil
	}
	lim := d.walkLimits()
	wctx, cancel := context.WithTimeout(c, lim.Budget+raxRequestTimeout)
	els, _, err := rax.walk(wctx, lim)
	cancel()
	if err != nil || len(els) == 0 {
		return nil
	}
	u := d.freshDisplay(ctx).upright()
	kbShown, kbRect := detectSoftwareKeyboard(els, u)
	if !kbShown || !pointInScreen(pt, u) || (!kbRect.Empty() && kbRect.Contains(pt)) {
		return nil
	}
	d.log.Info("iphone: detected keyboard-only editor before virtual keyboard", "device", d.info.ID,
		"tap_x", pt.X, "tap_y", pt.Y, "kb_y", kbRect.Y)
	return &focusSnap{KeyboardOnly: true}
}

// ensureKeyboardWithFocus registers the virtual keyboard up front and
// re-focuses snap if registering the keyboard knocked focus out. tried is
// true when a refocus tap was attempted (whether or not focus came back).
// Callers hold inputMu.
func (d *Device) ensureKeyboardWithFocus(ctx context.Context, snap *focusSnap, st *TypeStats) (refocused, tried bool, err error) {
	h, err := d.waitInput(ctx)
	if err != nil {
		return false, false, err
	}
	_, created, err := h.ensureKeyboard(ctx)
	if err != nil {
		return false, false, err
	}
	if !created || snap == nil {
		return false, false, nil
	}
	return d.refocusIfLost(ctx, snap, st)
}

// refocusIfLost checks whether focus dropped to nil after the virtual
// keyboard attached, and if so re-taps the field while keeping the virtual
// keyboard attached. Callers hold inputMu.
func (d *Device) refocusIfLost(ctx context.Context, snap *focusSnap, st *TypeStats) (refocused, tried bool, err error) {
	if snap == nil || st.Refocused {
		return false, false, nil
	}
	rax, err := d.raxNow()
	if err != nil {
		return false, false, nil
	}
	pt, hasTap := d.recentTap(time.Now())
	si := d.freshDisplay(ctx)
	src := deviceFocusSource{rax: rax, lim: d.walkLimits()}
	ok, tried, via, err := recoverFocusAfterKeyboard(ctx, src, snap, pt, hasTap, si, d.tapLocked, refocusWait, refocusPoll)
	if err != nil {
		return false, tried, err
	}
	if ok {
		st.Refocused = true
		d.log.Info("iphone: refocused field after attaching virtual keyboard", "device", d.info.ID,
			"via", via, "keyboard_only", snap.KeyboardOnly)
	} else if tried {
		d.log.Info("iphone: refocus tap did not restore focus", "device", d.info.ID,
			"via", via, "keyboard_only", snap.KeyboardOnly)
	}
	return ok, tried, nil
}

// recoverFocusAfterKeyboard checks whether focus vanished after the virtual
// keyboard attached, and when it did, re-taps the best post-hide target and
// waits for focus to come back.
func recoverFocusAfterKeyboard(
	ctx context.Context,
	src focusSource,
	snap *focusSnap,
	lastTap screen.Point,
	hasLastTap bool,
	si screenInfo,
	tapFn func(context.Context, screen.Point) error,
	wait, poll time.Duration,
) (refocused, tried bool, via string, err error) {
	if snap == nil {
		return false, false, "", nil
	}
	c := context.WithoutCancel(ctx)
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	if snap.KeyboardOnly {
		u := si.upright()
		if !hasLastTap || !pointInScreen(lastTap, u) {
			return false, false, "", nil
		}
		// Wait for the software keyboard to finish dismissing (and any editor
		// top banner to collapse back to the pre-tap layout) before re-tapping
		// lastTap while the virtual HID keyboard stays attached.
		hideWait := min(kbHideWait, wait)
		hidePoll := min(80*time.Millisecond, max(poll, 10*time.Millisecond))
		for deadline := time.Now().Add(hideWait); ; {
			els, werr := src.walkElements(c)
			if werr != nil {
				break
			}
			if shown, _ := detectSoftwareKeyboard(els, u); !shown {
				break
			}
			if !time.Now().Before(deadline) {
				break
			}
			if err := sleepCtx(ctx, hidePoll); err != nil {
				return false, false, "", err
			}
		}
		if settle := min(kbHideSettle, wait/2); settle > 0 {
			if err := sleepCtx(ctx, settle); err != nil {
				return false, false, "", err
			}
		}
		if err := tapFn(ctx, lastTap); err != nil {
			return false, true, "keyboard-last-tap", err
		}
		afterWait := min(kbOnlyRefocusWait, wait)
		deadline := time.Now().Add(afterWait)
		for {
			el, ferr := src.focusedElement(c)
			if ferr == nil && el != nil {
				return true, true, "keyboard-last-tap", nil
			}
			if !time.Now().Before(deadline) {
				// Custom editors like Xianyu's service description never expose
				// focusedElement even when focused; proceed to paste and let
				// checkUnread verify whether the screen changed.
				return true, true, "keyboard-last-tap", nil
			}
			if err := sleepCtx(ctx, poll); err != nil {
				return false, true, "keyboard-last-tap", err
			}
		}
	}
	el, err := src.focusedElement(c)
	if err != nil || el != nil {
		return false, false, "", nil
	}
	pt, via, ok := chooseRefocusPoint(c, src, snap, lastTap, hasLastTap, si)
	if !ok {
		return false, false, "", nil
	}
	if err := tapFn(ctx, pt); err != nil {
		return false, true, via, err
	}
	deadline := time.Now().Add(wait)
	for {
		el, err := src.focusedElement(c)
		if err != nil {
			return false, true, via, nil
		}
		if el != nil {
			return true, true, via, nil
		}
		if !time.Now().Before(deadline) {
			return false, true, via, nil
		}
		if err := sleepCtx(ctx, poll); err != nil {
			return false, true, via, err
		}
	}
}

// chooseRefocusPoint picks where to re-tap after the software keyboard hid:
//  1. The focused element's live post-hide frame (if still mounted, visible
//     and non-degenerate).
//  2. A strong match (identifier or placeholder) in a fresh accessibility walk.
//  3. The most recent non-long-press Tap point, when plausible for snap (e.g.
//     a Flutter/custom editor whose text input view unmounted when unfocused,
//     snapping the screen back to the keyboard-hidden layout where the agent
//     just tapped).
//  4. A label match in a fresh accessibility walk.
//  5. The element's pre-hide frame as a last resort.
func chooseRefocusPoint(
	ctx context.Context,
	src focusSource,
	snap *focusSnap,
	lastTap screen.Point,
	hasLastTap bool,
	si screenInfo,
) (screen.Point, string, bool) {
	u := si.upright()
	var (
		scaleLoaded bool
		cachedScale float64
	)
	singleScale := func() float64 {
		if !scaleLoaded {
			scaleLoaded = true
			if af, ok := src.appFrame(ctx); ok {
				cachedScale = raxScale(raxElement{Frame: af, HasFrame: true}, u)
			} else {
				cachedScale = u.Scale
			}
		}
		return cachedScale
	}
	if f, ok := src.liveFrame(ctx, snap.Object); ok {
		if sc := singleScale(); sc > 0 {
			if pt, ok := axpRectToPoint(turnSingleFrame(f, u, sc), u, sc); ok {
				return pt, "live", true
			}
		}
	}
	var (
		walked  bool
		walkEls []raxElement
	)
	loadWalk := func() []raxElement {
		if !walked {
			walked = true
			if els, err := src.walkElements(ctx); err == nil {
				walkEls = els
			}
		}
		return walkEls
	}
	if snap.Ident != "" || snap.Placeholder != "" {
		if els := loadWalk(); len(els) > 0 {
			if pt, ok := relocateInWalk(els, *snap, u, true); ok {
				return pt, "walk", true
			}
		}
	}
	if hasLastTap && pointInScreen(lastTap, u) && lastTapMatchesSnap(lastTap, *snap, u, singleScale()) {
		return lastTap, "last-tap", true
	}
	if snap.Label != "" {
		if els := loadWalk(); len(els) > 0 {
			if pt, ok := relocateInWalk(els, *snap, u, false); ok {
				return pt, "walk", true
			}
		}
	}
	if snap.HasFrame && snap.Visible {
		if sc := singleScale(); sc > 0 {
			if pt, ok := axpRectToPoint(turnSingleFrame(snap.Frame, u, sc), u, sc); ok {
				return pt, "pre-frame", true
			}
		}
	}
	return screen.Point{}, "", false
}

// turnSingleFrame applies the same foldable landscape frame rotation as
// turnRaxFrames to a single element frame when its coordinates lie in the
// display's own frame rather than the upright interface's.
func turnSingleFrame(f axpRect, u screenInfo, scale float64) axpRect {
	q := u.turn()
	if u.DisplayID < 2 || !q.swaps() || scale <= 0 || f.W <= 0 || f.H <= 0 {
		return f
	}
	w, h := float64(u.Width)/scale, float64(u.Height)/scale
	if f.Y+f.H > h*1.04 && f.Y+f.H <= w*1.04 && f.X+f.W <= h*1.04 {
		return turnRect(f, h, w, q)
	}
	return f
}

// lastTapMatchesSnap rejects a stale Tap point that cannot plausibly have
// focused snap — for example, a button tapped in another column or a field
// earlier on the page before focus moved without a Tap. When snap has no
// usable frame (such as a degenerate 1x1 custom input proxy), it returns true.
func lastTapMatchesSnap(lastTap screen.Point, snap focusSnap, u screenInfo, scale float64) bool {
	if !snap.HasFrame || !snap.Visible || snap.Frame.W < minRefocusPoints || snap.Frame.H < minRefocusPoints {
		return true
	}
	if scale <= 0 || u.Width <= 0 || u.Height <= 0 {
		return true
	}
	f := turnSingleFrame(snap.Frame, u, scale)
	r := pixelRect(f, scale, u.Width, u.Height)
	if r.W < minRefocusPoints || r.H < minRefocusPoints {
		return true
	}
	marginX := int(math.Round(24 * scale))
	if lastTap.X < r.X-marginX || lastTap.X > r.X+r.W+marginX {
		return false
	}
	// Opening an editor may either scroll the field up above the software
	// keyboard (up to ~60% of screen height below snap.Frame) or insert a top
	// helper banner above it (pushing snap.Frame down by ~150-200px, as in
	// Xianyu's service editor), so allow 20% upward and 60% downward.
	marginUp := max(int(math.Round(120*scale)), u.Height*2/10)
	if lastTap.Y < r.Y-marginUp || lastTap.Y > r.Y+r.H+u.Height*6/10 {
		return false
	}
	return true
}

// axpRectToPoint converts an accessibility frame in points to its center in
// upright screen pixels, rejecting tiny or off-screen rects.
func axpRectToPoint(f axpRect, u screenInfo, scale float64) (screen.Point, bool) {
	if f.W < minRefocusPoints || f.H < minRefocusPoints {
		return screen.Point{}, false
	}
	if scale <= 0 {
		scale = u.Scale
	}
	if scale <= 0 || u.Width <= 0 || u.Height <= 0 {
		return screen.Point{}, false
	}
	r := pixelRect(f, scale, u.Width, u.Height)
	if r.W < minRefocusPoints || r.H < minRefocusPoints {
		return screen.Point{}, false
	}
	return r.Center(), true
}

func pointInScreen(p screen.Point, u screenInfo) bool {
	if u.Width <= 0 || u.Height <= 0 {
		return p.X >= 0 && p.Y >= 0
	}
	return p.X >= 0 && p.Y >= 0 && p.X < u.Width && p.Y < u.Height
}

// relocateInWalk finds the best visible non-keyboard element matching snap
// after the keyboard hid and returns its center in upright screen pixels.
// strongOnly restricts matching to identifier and placeholder (excluding
// weaker Label matches).
func relocateInWalk(els []raxElement, snap focusSnap, u screenInfo, strongOnly bool) (screen.Point, bool) {
	if len(els) == 0 || els[0].Object == nil {
		return screen.Point{}, false
	}
	scale := raxScale(els[0], u)
	if scale <= 0 {
		return screen.Point{}, false
	}
	els = turnRaxFrames(els, u, scale)
	appPID := els[0].Object.PID
	inKeyboard := make([]bool, len(els))
	for i, e := range els {
		if p := e.Parent; p > 0 && p < i {
			inKeyboard[i] = inKeyboard[p]
		}
		if e.Type == xcKeyboard {
			inKeyboard[i] = true
		}
	}
	// Match in priority order: identifier, placeholder, placeholder shown as
	// static label (common when a custom editor unmounts on blur), label.
	var matchers []func(raxElement) bool
	if strongOnly {
		matchers = []func(raxElement) bool{
			func(e raxElement) bool { return snap.Ident != "" && e.Ident == snap.Ident },
			func(e raxElement) bool { return snap.Placeholder != "" && e.Placeholder == snap.Placeholder },
			func(e raxElement) bool { return snap.Placeholder != "" && e.Label == snap.Placeholder },
		}
	} else {
		matchers = []func(raxElement) bool{
			func(e raxElement) bool { return snap.Label != "" && e.Label == snap.Label },
		}
	}
	for _, match := range matchers {
		for i, e := range els {
			if i == 0 || inKeyboard[i] || !e.Visible || !e.HasFrame {
				continue
			}
			if e.Object != nil && e.Object.PID != appPID {
				continue
			}
			if !match(e) {
				continue
			}
			if pt, ok := axpRectToPoint(e.Frame, u, scale); ok {
				return pt, true
			}
		}
	}
	return screen.Point{}, false
}

// isASCIILetterKey reports whether label is a single ASCII letter key (a-z or
// A-Z). Digits are excluded so in-app numeric keypads (such as Xianyu's price
// pad) are never mistaken for the iOS system keyboard.
func isASCIILetterKey(label string) bool {
	s := strings.TrimSpace(label)
	if len(s) != 1 {
		return false
	}
	b := s[0]
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isSystemKeyboardMarker reports whether label is a characteristic iOS system
// keyboard control key (globe, dictation, space, emoji, shift).
func isSystemKeyboardMarker(label string) bool {
	s := strings.TrimSpace(label)
	if s == "" {
		return false
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(s, "下一个键盘"),
		strings.HasPrefix(low, "next keyboard"),
		s == "听写", low == "dictation",
		s == "空格键", low == "space",
		s == "表情符号", low == "emoji",
		low == "shift":
		return true
	}
	return false
}

// detectSoftwareKeyboard reports whether els shows the iOS system software
// keyboard and returns its bounding rectangle in upright screen pixels.
func detectSoftwareKeyboard(els []raxElement, u screenInfo) (bool, screen.Rect) {
	if len(els) == 0 || els[0].Object == nil {
		return false, screen.Rect{}
	}
	scale := raxScale(els[0], u)
	if scale <= 0 {
		scale = u.Scale
	}
	if scale <= 0 || u.Width <= 0 || u.Height <= 0 {
		return false, screen.Rect{}
	}
	els = turnRaxFrames(els, u, scale)
	appPID := els[0].Object.PID
	lowerY := u.Height * 45 / 100

	remoteChars := map[int]int{}
	for _, e := range els {
		if e.Object != nil && e.Object.PID != appPID && !e.Web && isKeyChar(e.Label) {
			remoteChars[e.Object.PID]++
		}
	}
	remoteKB := map[int]bool{}
	hasRemoteKB := false
	for pid, n := range remoteChars {
		if n >= 8 {
			remoteKB[pid] = true
			hasRemoteKB = true
		}
	}

	inKeyboard := make([]bool, len(els))
	hasXCKeyboard := false
	lowerAlpha := 0
	lowerTraitKeys := 0
	hasSysMarker := false
	for i, e := range els {
		if p := e.Parent; p > 0 && p < i {
			inKeyboard[i] = inKeyboard[p]
		}
		if e.Type == xcKeyboard {
			inKeyboard[i] = true
			hasXCKeyboard = true
		}
		if i == 0 || !e.IsElement || !e.Visible || !e.HasFrame {
			continue
		}
		r := pixelRect(e.Frame, scale, u.Width, u.Height)
		if r.Empty() || r.Y < lowerY {
			continue
		}
		if isASCIILetterKey(e.Label) {
			lowerAlpha++
		}
		if e.Type == xcKey || e.Traits&traitKeyboardKey != 0 {
			lowerTraitKeys++
		}
		if isSystemKeyboardMarker(e.Label) {
			hasSysMarker = true
		}
	}

	shown := hasXCKeyboard || hasRemoteKB || lowerAlpha >= 8 || (lowerTraitKeys >= 8 && hasSysMarker)
	if !shown {
		return false, screen.Rect{}
	}
	var kb screen.Rect
	for i, e := range els {
		if i == 0 || !e.Visible || !e.HasFrame {
			continue
		}
		r := pixelRect(e.Frame, scale, u.Width, u.Height)
		if r.Empty() {
			continue
		}
		isKB := inKeyboard[i] ||
			(e.Object != nil && remoteKB[e.Object.PID]) ||
			e.Type == xcKey ||
			e.Traits&traitKeyboardKey != 0 ||
			(r.Y >= lowerY && (isASCIILetterKey(e.Label) || isSystemKeyboardMarker(e.Label)))
		if isKB {
			kb = union(kb, r)
		}
	}
	return true, kb
}

// softwareKeyboardPointsRect detects the system software keyboard directly in
// point coordinates using els[0].Frame, for shownFrom which has no screenInfo.
func softwareKeyboardPointsRect(els []raxElement) (bool, axpRect) {
	if len(els) == 0 || els[0].Object == nil || !els[0].HasFrame || els[0].Frame.W <= 0 || els[0].Frame.H <= 0 {
		return false, axpRect{}
	}
	appFrame := els[0].Frame
	lowerY := appFrame.Y + appFrame.H*0.45
	appPID := els[0].Object.PID

	lowerAlpha := 0
	lowerTraitKeys := 0
	hasSysMarker := false
	for i, e := range els {
		if i == 0 || !e.IsElement || !e.Visible || !e.HasFrame || e.Object == nil || e.Object.PID != appPID {
			continue
		}
		if e.Frame.Y < lowerY {
			continue
		}
		if isASCIILetterKey(e.Label) {
			lowerAlpha++
		}
		if e.Type == xcKey || e.Traits&traitKeyboardKey != 0 {
			lowerTraitKeys++
		}
		if isSystemKeyboardMarker(e.Label) {
			hasSysMarker = true
		}
	}
	if lowerAlpha < 8 && !(lowerTraitKeys >= 8 && hasSysMarker) {
		return false, axpRect{}
	}
	var (
		has bool
		x0  float64
		y0  float64
		x1  float64
		y1  float64
	)
	for i, e := range els {
		if i == 0 || !e.IsElement || !e.Visible || !e.HasFrame || e.Object == nil || e.Object.PID != appPID {
			continue
		}
		if e.Frame.Y < lowerY {
			continue
		}
		if e.Type == xcKey || e.Traits&traitKeyboardKey != 0 || isASCIILetterKey(e.Label) || isSystemKeyboardMarker(e.Label) {
			ex1, ey1 := e.Frame.X+e.Frame.W, e.Frame.Y+e.Frame.H
			if !has {
				x0, y0, x1, y1, has = e.Frame.X, e.Frame.Y, ex1, ey1, true
			} else {
				x0 = min(x0, e.Frame.X)
				y0 = min(y0, e.Frame.Y)
				x1 = max(x1, ex1)
				y1 = max(y1, ey1)
			}
		}
	}
	if !has {
		return false, axpRect{}
	}
	return true, axpRect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func rectInPointsRect(f, kb axpRect) bool {
	if f.W <= 0 || f.H <= 0 || kb.W <= 0 || kb.H <= 0 {
		return false
	}
	cx, cy := f.X+f.W/2, f.Y+f.H/2
	return cx >= kb.X && cx <= kb.X+kb.W && cy >= kb.Y && cy <= kb.Y+kb.H
}

// findPasteMenuItem looks for a visible iOS edit callout or QuickType paste
// action ("粘贴" / "Paste") in els and returns its center in upright pixels.
// It never matches iOS's Allow Paste permission prompt buttons.
func findPasteMenuItem(els []raxElement, u screenInfo) (screen.Point, string, bool) {
	if len(els) == 0 || els[0].Object == nil {
		return screen.Point{}, "", false
	}
	scale := raxScale(els[0], u)
	if scale <= 0 {
		scale = u.Scale
	}
	if scale <= 0 || u.Width <= 0 || u.Height <= 0 {
		return screen.Point{}, "", false
	}
	els = turnRaxFrames(els, u, scale)
	appPID := els[0].Object.PID
	inKeyboard := make([]bool, len(els))
	for i, e := range els {
		if p := e.Parent; p > 0 && p < i {
			inKeyboard[i] = inKeyboard[p]
		}
		if e.Type == xcKeyboard {
			inKeyboard[i] = true
		}
	}
	isPermission := func(lbl string) bool {
		low := strings.ToLower(lbl)
		return strings.Contains(lbl, "允许") || strings.Contains(low, "allow")
	}
	// Pass 1: exact "粘贴" / "Paste" on a non-keyboard menu item or button.
	for i, e := range els {
		if i == 0 || inKeyboard[i] || e.Type == xcKey || e.Traits&traitKeyboardKey != 0 ||
			!e.IsElement || !e.Visible || !e.HasFrame {
			continue
		}
		if e.Type != xcMenuItem && e.Type != xcButton {
			continue
		}
		lbl := strings.TrimSpace(e.Label)
		if lbl == "" || isPermission(lbl) {
			continue
		}
		if lbl == "粘贴" || strings.EqualFold(lbl, "paste") {
			if pt, ok := axpRectToPoint(e.Frame, u, scale); ok {
				return pt, lbl, true
			}
		}
	}
	// Pass 2: QuickType / remote suggestion bar offering a paste item.
	for i, e := range els {
		if i == 0 || !e.IsElement || !e.Visible || !e.HasFrame {
			continue
		}
		lbl := strings.TrimSpace(e.Label)
		if lbl == "" || isPermission(lbl) {
			continue
		}
		low := strings.ToLower(lbl)
		isPaste := lbl == "粘贴" || low == "paste" ||
			(e.Object != nil && e.Object.PID != appPID && (strings.HasPrefix(lbl, "粘贴") || strings.HasPrefix(low, "paste ")))
		if !isPaste {
			continue
		}
		if pt, ok := axpRectToPoint(e.Frame, u, scale); ok {
			return pt, lbl, true
		}
	}
	return screen.Point{}, "", false
}

// chooseCalloutPressPoint picks a safe point inside the currently open editor
// to long-press for the iOS "粘贴"/"Paste" callout menu. When opening the
// editor inserts a top banner (such as Xianyu's "AI帮你写" bar) that shifts
// the editor downward over a cover photo tile at lastTap, it targets the
// shifted text/placeholder element rather than raw lastTap.
func chooseCalloutPressPoint(els []raxElement, lastTap screen.Point, kbRect screen.Rect, u screenInfo) screen.Point {
	scale := u.Scale
	if len(els) > 0 {
		if sc := raxScale(els[0], u); sc > 0 {
			scale = sc
		}
		els = turnRaxFrames(els, u, scale)
	}
	kbTop := u.Height
	if !kbRect.Empty() && kbRect.Y > 0 {
		kbTop = kbRect.Y
	}
	safeBottom := kbTop
	if scale > 0 {
		if margin := int(math.Round(80 * scale)); safeBottom > margin*2 {
			safeBottom -= margin
		}
	}
	if len(els) > 0 && els[0].Object != nil && scale > 0 {
		appPID := els[0].Object.PID
		inKeyboard := make([]bool, len(els))
		for i, e := range els {
			if p := e.Parent; p > 0 && p < i {
				inKeyboard[i] = inKeyboard[p]
			}
			if e.Type == xcKeyboard {
				inKeyboard[i] = true
			}
		}
		maxShift := u.Height * 20 / 100
		// Pass 1: a visible text field/view near lastTap above the keyboard.
		for i, e := range els {
			if i == 0 || inKeyboard[i] || !e.Visible || !e.HasFrame || e.Object == nil || e.Object.PID != appPID {
				continue
			}
			if e.Type != xcTextView && e.Type != xcTextField && e.Type != xcSearchField {
				continue
			}
			r := pixelRect(e.Frame, scale, u.Width, u.Height)
			if r.Empty() || r.Center().Y >= safeBottom {
				continue
			}
			if r.Contains(lastTap) || (r.Center().Y >= lastTap.Y-60 && r.Center().Y <= lastTap.Y+maxShift) {
				return r.Center()
			}
		}
		// Pass 2: a wide placeholder StaticText inside the open editor below or
		// at lastTap (e.g. Xianyu's service description placeholder pushed down
		// ~150px by the "AI帮你写" bar).
		minW := u.Width / 3
		for i, e := range els {
			if i == 0 || inKeyboard[i] || !e.IsElement || !e.Visible || !e.HasFrame || e.Object == nil || e.Object.PID != appPID {
				continue
			}
			if e.Type != xcStaticText || strings.TrimSpace(e.Label) == "" {
				continue
			}
			r := pixelRect(e.Frame, scale, u.Width, u.Height)
			if r.W < minW || r.Center().Y >= safeBottom {
				continue
			}
			if r.Center().Y >= lastTap.Y-40 && r.Center().Y <= lastTap.Y+maxShift {
				return r.Center()
			}
		}
	}
	pt := lastTap
	if safeBottom > 0 && pt.Y >= safeBottom {
		pt.Y = safeBottom - 10
		if pt.Y < 0 {
			pt.Y = 0
		}
	}
	return pt
}

// pasteViaCallout is the Stage 2 fallback when a KeyboardOnly custom editor
// refuses HID Command-V (Stage 1 left the screen unchanged). It detaches the
// virtual HID keyboard, puts text on the pasteboard, re-opens the editor with
// the system software keyboard, opens iOS's "粘贴"/"Paste" callout menu, and
// verifies the result against a fresh software-keyboard shownText baseline.
// Callers hold inputMu.
func (d *Device) pasteViaCallout(ctx context.Context, pb *pbConn, saved *pbSnapshot, text string, st *TypeStats) (string, error) {
	pt, hasTap := d.recentTap(time.Now())
	if !hasTap || pb == nil {
		return "", errNotTaken
	}
	rax, err := d.raxNow()
	if err != nil {
		return "", err
	}
	d.releaseKeyboard()
	if err := sleepCtx(ctx, calloutDetachWait); err != nil {
		return "", err
	}

	sctx, cancel := context.WithTimeout(ctx, pbStalled)
	mine, err := pb.set(sctx, textItems(text))
	cancel()
	if err != nil {
		var ce *CoreDeviceError
		if errors.As(err, &ce) {
			return "", fmt.Errorf("type: the phone refused the text: %w", err)
		}
		st.Restore = restorePasteboard(pb, saved, nil)
		return "", fmt.Errorf("type: %w", err)
	}
	defer func() {
		t2 := time.Now()
		st.Restore = restorePasteboard(pb, saved, mine)
		st.Restored = time.Since(t2)
	}()

	if err := d.tapLocked(ctx, pt); err != nil {
		return "", err
	}
	u := d.freshDisplay(ctx).upright()
	walkNow := func() ([]raxElement, bool, error) {
		lim := d.walkLimits()
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lim.Budget+raxRequestTimeout)
		defer cancel()
		return rax.walk(wctx, lim)
	}
	var (
		openEls      []raxElement
		openComplete bool
		kbShown      bool
		kbRect       screen.Rect
	)
	for deadline := time.Now().Add(calloutKBOpenWait); ; {
		els, complete, werr := walkNow()
		if werr == nil && len(els) > 0 {
			openEls, openComplete = els, complete
			if kbShown, kbRect = detectSoftwareKeyboard(els, u); kbShown {
				break
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		if err := sleepCtx(ctx, 100*time.Millisecond); err != nil {
			return "", err
		}
	}
	if err := sleepCtx(ctx, calloutKBSettle); err != nil {
		return "", err
	}
	if els, complete, werr := walkNow(); werr == nil && len(els) > 0 {
		openEls, openComplete = els, complete
		kbShown, kbRect = detectSoftwareKeyboard(els, u)
	}
	if len(openEls) == 0 {
		d.log.Info("iphone: stage 2 callout paste could not walk screen", "device", d.info.ID)
		return "", errNotTaken
	}
	beforeSoft := shownFrom(openEls, openComplete)

	menuPt, menuLabel, found := findPasteMenuItem(openEls, u)
	pressPt := chooseCalloutPressPoint(openEls, pt, kbRect, u)
	if !found {
		if err := d.longPressLocked(ctx, pressPt, calloutLongPress); err != nil {
			return "", err
		}
		if err := sleepCtx(ctx, calloutMenuWait); err != nil {
			return "", err
		}
		if menuEls, _, werr := walkNow(); werr == nil && len(menuEls) > 0 {
			menuPt, menuLabel, found = findPasteMenuItem(menuEls, u)
		}
	}
	if !found {
		// On some editors, tapping the active cursor toggles the edit menu
		// when a long press opens the magnifier instead.
		if err := d.tapLocked(ctx, pressPt); err != nil {
			return "", err
		}
		if err := sleepCtx(ctx, calloutMenuWait); err != nil {
			return "", err
		}
		if menuEls, _, werr := walkNow(); werr == nil && len(menuEls) > 0 {
			menuPt, menuLabel, found = findPasteMenuItem(menuEls, u)
		}
	}
	if !found {
		d.log.Info("iphone: stage 2 callout paste found no paste menu", "device", d.info.ID,
			"press_x", pressPt.X, "press_y", pressPt.Y, "kb_shown", kbShown)
		app := "the app"
		if a, ok := d.foregroundApp(ctx); ok && a.Name != "" {
			app = a.Name
		}
		return "", notTakenError("paste", app)
	}
	d.log.Info("iphone: stage 2 callout paste tapping menu item", "device", d.info.ID,
		"label", menuLabel, "x", menuPt.X, "y", menuPt.Y)
	if err := d.tapLocked(ctx, menuPt); err != nil {
		return "", err
	}
	note, err := d.checkUnread(ctx, text, &beforeSoft, "paste", st)
	d.log.Info("iphone: stage 2 callout paste result", "device", d.info.ID,
		"label", menuLabel, "checked", st.Checked, "ok", err == nil)
	return note, err
}
