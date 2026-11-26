package ios

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

type fakeFocusSource struct {
	focusSeq  []*axpObject // returned in order by focusedElement; last entry repeats
	focusErr  error
	focusCall int

	live    axpRect
	hasLive bool

	app    axpRect
	hasApp bool

	walkEls  []raxElement
	walkSeq  [][]raxElement
	walkCall int
	walkErr  error
}

func (f *fakeFocusSource) focusedElement(ctx context.Context) (*axpObject, error) {
	f.focusCall++
	if f.focusErr != nil {
		return nil, f.focusErr
	}
	if len(f.focusSeq) == 0 {
		return nil, nil
	}
	idx := f.focusCall - 1
	if idx >= len(f.focusSeq) {
		idx = len(f.focusSeq) - 1
	}
	return f.focusSeq[idx], nil
}

func (f *fakeFocusSource) liveFrame(ctx context.Context, o *axpObject) (axpRect, bool) {
	return f.live, f.hasLive
}

func (f *fakeFocusSource) appFrame(ctx context.Context) (axpRect, bool) {
	return f.app, f.hasApp
}

func (f *fakeFocusSource) walkElements(ctx context.Context) ([]raxElement, error) {
	f.walkCall++
	if f.walkErr != nil {
		return nil, f.walkErr
	}
	if len(f.walkSeq) > 0 {
		idx := f.walkCall - 1
		if idx >= len(f.walkSeq) {
			idx = len(f.walkSeq) - 1
		}
		return f.walkSeq[idx], nil
	}
	return f.walkEls, nil
}

// A field that keeps focus when the virtual keyboard attaches does not get
// tapped again.
func TestRecoverFocusStillFocused(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	src := &fakeFocusSource{focusSeq: []*axpObject{obj}}
	tapped := 0
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, &focusSnap{Object: obj},
		screen.Point{X: 270, Y: 805}, true, si,
		func(context.Context, screen.Point) error { tapped++; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || ok || tried || via != "" || tapped != 0 {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v tapped=%d; want no refocus", ok, tried, via, err, tapped)
	}
}

// When no field had focus before ensureKeyboard (e.g. Calculator), nothing is
// tapped.
func TestRecoverFocusNilSnap(t *testing.T) {
	src := &fakeFocusSource{}
	tapped := 0
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	ok, tried, _, err := recoverFocusAfterKeyboard(
		context.Background(), src, nil,
		screen.Point{X: 270, Y: 805}, true, si,
		func(context.Context, screen.Point) error { tapped++; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || ok || tried || tapped != 0 {
		t.Fatalf("got ok=%v tried=%v err=%v tapped=%d; want no tap", ok, tried, err, tapped)
	}
}

// A mounted UITextView whose frame shifted back down when the software
// keyboard hid is re-tapped at its live post-hide frame, not its pre-hide
// frame.
func TestRecoverFocusPrefersLivePostHideFrame(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	src := &fakeFocusSource{
		// 1st check after ensureKeyboard: nil (lost focus)
		// 2nd check right after tap: nil (still focusing)
		// 3rd check on next poll: obj (focus restored)
		focusSeq: []*axpObject{nil, nil, obj},
		live:     axpRect{X: 20, Y: 250, W: 380, H: 100}, // post-hide: center (210, 300) pt -> (630, 900) px
		hasLive:  true,
	}
	snap := &focusSnap{
		Object:   obj,
		Frame:    axpRect{X: 20, Y: 400, W: 380, H: 100}, // pre-hide shifted position
		HasFrame: true,
		Visible:  true,
	}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{X: 270, Y: 805}, true, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		100*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "live" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=live", ok, tried, via, err)
	}
	want := screen.Point{X: 630, Y: 900}
	if gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// When an editor unmounts its text input proxy on blur (e.g. Flutter's
// FlutterTextInputView returning empty/1x1 or hidden), recovery falls back to
// the recent Tap point that focused the field in the keyboard-hidden layout.
func TestRecoverFocusFallsBackToLastTapWhenUnmounted(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	for _, tc := range []struct {
		name    string
		live    axpRect
		hasLive bool
	}{
		{"unmounted", axpRect{}, false},
		{"degenerate 1x1", axpRect{X: 0, Y: 0, W: 1, H: 1}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeFocusSource{
				focusSeq: []*axpObject{nil, obj},
				live:     tc.live,
				hasLive:  tc.hasLive,
			}
			si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
			want := screen.Point{X: 270, Y: 805}
			var gotPt screen.Point
			ok, tried, via, err := recoverFocusAfterKeyboard(
				context.Background(), src, &focusSnap{Object: obj},
				want, true, si,
				func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
				50*time.Millisecond, 5*time.Millisecond,
			)
			if err != nil || !ok || !tried || via != "last-tap" || gotPt != want {
				t.Fatalf("got ok=%v tried=%v via=%q pt=%v err=%v; want ok=true tried=true via=last-tap pt=%v", ok, tried, via, gotPt, err, want)
			}
		})
	}
}

// Without a live frame or recent tap (e.g. an auto-focused field), recovery
// relocates the field in a fresh walk, ignoring keyboard elements and matching
// placeholder text exposed as a static label after blur.
func TestRecoverFocusRelocatesInWalk(t *testing.T) {
	obj := &axpObject{PID: 42, ID: 1}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	els := []raxElement{
		{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true},
		// Keyboard subtree carrying the same text must be skipped.
		{Object: &axpObject{PID: 42, ID: 2}, Parent: 0, Type: xcKeyboard, Frame: axpRect{0, 600, 428, 326}, HasFrame: true, Visible: true},
		{Object: &axpObject{PID: 42, ID: 3}, Parent: 1, Label: "请输入服务描述", Frame: axpRect{10, 620, 200, 40}, HasFrame: true, Visible: true},
		// Unfocused placeholder in the app itself.
		{Object: &axpObject{PID: 42, ID: 4}, Parent: 0, Type: xcStaticText, Label: "请输入服务描述", Frame: axpRect{20, 250, 380, 60}, HasFrame: true, Visible: true},
	}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
		walkEls:  els,
	}
	snap := &focusSnap{
		Object:      obj,
		Placeholder: "请输入服务描述",
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{}, false, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "walk" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=walk", ok, tried, via, err)
	}
	// (20+190)*3 = 630, (250+30)*3 = 840
	want := screen.Point{X: 630, Y: 840}
	if gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// Last resort falls back to the pre-hide frame when live frame, lastTap and
// walk all miss.
func TestRecoverFocusFallsBackToPreFrame(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
		walkErr:  errors.New("walk failed"),
	}
	snap := &focusSnap{
		Object:   obj,
		Frame:    axpRect{X: 40, Y: 200, W: 300, H: 80}, // center (190, 240) pt -> (570, 720) px
		HasFrame: true,
		Visible:  true,
		Label:    "desc",
	}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{}, false, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "pre-frame" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=pre-frame", ok, tried, via, err)
	}
	if want := (screen.Point{X: 570, Y: 720}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// If re-tapping while the virtual keyboard is attached still does not restore
// focus, recoverFocusAfterKeyboard returns ok=false, tried=true without
// erroring so callers do not re-tap a second time and checkTyped reports
// unfocused.
func TestRecoverFocusStillUnfocusedAfterTap(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil},
	}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	tapped := 0
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, &focusSnap{Object: obj},
		screen.Point{X: 270, Y: 805}, true, si,
		func(context.Context, screen.Point) error { tapped++; return nil },
		20*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || ok || !tried || via != "last-tap" || tapped != 1 {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v tapped=%d; want ok=false tried=true via=last-tap tapped=1", ok, tried, via, err, tapped)
	}
}

// recordTap, clearLastTap and expiry behave as expected.
func TestRecentTapLifecycle(t *testing.T) {
	d := &Device{}
	now := time.Now()
	if _, ok := d.recentTap(now); ok {
		t.Fatal("fresh device reported a recent tap")
	}
	want := screen.Point{X: 270, Y: 805}
	d.recordTap(want)
	if got, ok := d.recentTap(time.Now()); !ok || got != want {
		t.Fatalf("recentTap = %v, %v; want %v, true", got, ok, want)
	}
	if _, ok := d.recentTap(time.Now().Add(lastTapMaxAge + time.Second)); ok {
		t.Fatal("expired tap was still returned")
	}
	d.clearLastTap()
	if _, ok := d.recentTap(time.Now()); ok {
		t.Fatal("cleared tap was still returned")
	}
}

// A strong walk match (Ident/Placeholder) takes precedence over lastTap so a
// tap on a previous screen's button that auto-focused a field cannot clobber
// the wrong coordinate.
func TestRecoverFocusStrongWalkBeatsLastTap(t *testing.T) {
	obj := &axpObject{PID: 42, ID: 1}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	els := []raxElement{
		{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true},
		{Object: &axpObject{PID: 42, ID: 4}, Parent: 0, Type: xcStaticText, Label: "请输入服务描述", Frame: axpRect{20, 250, 380, 60}, HasFrame: true, Visible: true},
	}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
		walkEls:  els,
	}
	snap := &focusSnap{
		Object:      obj,
		Placeholder: "请输入服务描述",
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{X: 640, Y: 2400}, true, si, // stale tap on a bottom button
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "walk" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=walk", ok, tried, via, err)
	}
	if want := (screen.Point{X: 630, Y: 840}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// When snap has a real frame, a stale lastTap outside its horizontal span (or
// well above it) is rejected rather than tapping an unrelated control.
func TestRecoverFocusRejectsImplausibleLastTap(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
	}
	// Field on the right half: X=200..400pt (600..1200px), Y=200..260pt (600..780px)
	snap := &focusSnap{
		Object:   obj,
		Frame:    axpRect{X: 200, Y: 200, W: 200, H: 60},
		HasFrame: true,
		Visible:  true,
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{X: 100, Y: 650}, true, si, // left column — outside snap's horizontal span
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "pre-frame" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=pre-frame", ok, tried, via, err)
	}
	if want := (screen.Point{X: 900, Y: 690}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// Display Zoom (app frame width smaller than physical/Scale) and missing
// si.Scale both resolve via appFrame through raxScale.
func TestRecoverFocusUsesAppFrameScale(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	// 1170x2532 screen in Display Zoom (320pt wide app frame -> scale 1170/320 = 3.65625),
	// with si.Scale == 0.
	si := screenInfo{Width: 1170, Height: 2532, Scale: 0}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
		app:      axpRect{X: 0, Y: 0, W: 320, H: 693},
		hasApp:   true,
		live:     axpRect{X: 16, Y: 100, W: 288, H: 64}, // center (160, 132) pt -> (585, 483) px
		hasLive:  true,
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, &focusSnap{Object: obj},
		screen.Point{}, false, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "live" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=live", ok, tried, via, err)
	}
	if want := (screen.Point{X: 585, Y: 483}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// On a foldable inner display (DisplayID >= 2) rotated to landscape, a live
// frame reported in the display's own unturned coordinate space is turned into
// upright pixels just like turnRaxFrames does.
func TestRecoverFocusTurnsFoldableLandscapeLiveFrame(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	// Portrait display 2007x2853 (669x951 pt @3x), turned 90° clockwise -> upright 2853x2007 (951x669 pt).
	si := screenInfo{DisplayID: 2, Width: 2007, Height: 2853, Scale: 3, Orientation: "rot90"}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
		app:      axpRect{X: 0, Y: 0, W: 669, H: 951},
		hasApp:   true,
		// Frame in unturned display space: X=100..200, Y=700..800 (past upright H=669, fits W=951).
		live:    axpRect{X: 100, Y: 700, W: 100, H: 100},
		hasLive: true,
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, &focusSnap{Object: obj},
		screen.Point{}, false, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "live" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=live", ok, tried, via, err)
	}
	// turnRect(q=1, w=669, h=951): (x, y) -> (951 - y, x)
	// [100..200, 700..800] -> [151..251, 100..200] -> center (201, 150) pt -> (603, 450) px.
	if want := (screen.Point{X: 603, Y: 450}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// When opening an editor inserts a top banner (e.g. Xianyu's "AI帮你写" bar,
// ~150px tall), snap.Frame shifts downward relative to the original tap;
// lastTapMatchesSnap still accepts lastTap within the upward banner allowance.
func TestRecoverFocusAcceptsLastTapAboveBannerShiftedSnap(t *testing.T) {
	obj := &axpObject{PID: 10, ID: 1}
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil, obj},
	}
	// Editor frame pushed down to Y=320pt (960px) by a ~150px top banner;
	// agent originally tapped Y=805px (155px above r.Y).
	snap := &focusSnap{
		Object:   obj,
		Frame:    axpRect{X: 20, Y: 320, W: 388, H: 200},
		HasFrame: true,
		Visible:  true,
	}
	var gotPt screen.Point
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, snap,
		screen.Point{X: 270, Y: 805}, true, si,
		func(_ context.Context, p screen.Point) error { gotPt = p; return nil },
		50*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "last-tap" {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v; want ok=true tried=true via=last-tap", ok, tried, via, err)
	}
	if want := (screen.Point{X: 270, Y: 805}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// detectSoftwareKeyboard recognizes an in-process QWERTY system keyboard (as
// on iOS 27), computes its bounding rect, and ignores an in-app numeric price
// keypad (digits 0-9 without ASCII letters or system keyboard markers).
func TestDetectSoftwareKeyboard(t *testing.T) {
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	appEl := raxElement{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true}

	// System QWERTY keys in lower half (in-process under appPID).
	qwerty := []raxElement{appEl}
	for i, ch := range []string{"q", "w", "e", "r", "t", "y", "u", "i", "o", "p"} {
		qwerty = append(qwerty, raxElement{
			Object:    &axpObject{PID: 42, ID: uint64(i + 1)},
			Parent:    0,
			Type:      xcButton,
			Label:     ch,
			Frame:     axpRect{X: float64(i*40 + 10), Y: 640, W: 36, H: 46},
			HasFrame:  true,
			IsElement: true,
			Visible:   true,
		})
	}
	shown, kbRect := detectSoftwareKeyboard(qwerty, si)
	if !shown {
		t.Fatal("QWERTY keyboard was not detected")
	}
	if kbRect.Contains(screen.Point{X: 260, Y: 795}) {
		t.Fatalf("kbRect %+v should not contain editor tap (260, 795)", kbRect)
	}
	if !kbRect.Contains(screen.Point{X: 150, Y: 1980}) {
		t.Fatalf("kbRect %+v should contain key tap (150, 1980)", kbRect)
	}

	// Xianyu's in-app numeric price keypad (0-9, ".", "确定", "包邮") must NOT
	// be mistaken for the system software keyboard.
	pricePad := []raxElement{appEl}
	for i, lbl := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", ".", "确定", "包邮"} {
		pricePad = append(pricePad, raxElement{
			Object:    &axpObject{PID: 42, ID: uint64(i + 1)},
			Parent:    0,
			Type:      xcButton,
			Label:     lbl,
			Frame:     axpRect{X: float64((i%3)*100 + 20), Y: float64(640 + (i/3)*60), W: 90, H: 50},
			HasFrame:  true,
			IsElement: true,
			Visible:   true,
		})
	}
	if shown, _ := detectSoftwareKeyboard(pricePad, si); shown {
		t.Fatal("in-app numeric price keypad was mistaken for system software keyboard")
	}
}

// A KeyboardOnly editor (focusedElement always nil, such as Xianyu's service
// description editor) waits for the software keyboard to finish dismissing,
// re-taps lastTap while the virtual HID keyboard stays attached, and returns
// refocused=true even when focusedElement stays nil.
func TestRecoverFocusKeyboardOnly(t *testing.T) {
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	appEl := raxElement{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true}
	withKB := []raxElement{appEl}
	for i, ch := range []string{"q", "w", "e", "r", "t", "y", "u", "i"} {
		withKB = append(withKB, raxElement{
			Object: &axpObject{PID: 42, ID: uint64(i + 1)}, Parent: 0, Type: xcButton, Label: ch,
			Frame: axpRect{X: float64(i*40 + 10), Y: 640, W: 36, H: 46}, HasFrame: true, IsElement: true, Visible: true,
		})
	}
	withoutKB := []raxElement{
		appEl,
		{Object: &axpObject{PID: 42, ID: 99}, Parent: 0, Type: xcStaticText, Label: "1. 服务流程", Frame: axpRect{20, 270, 380, 30}, HasFrame: true, IsElement: true, Visible: true},
	}
	src := &fakeFocusSource{
		focusSeq: []*axpObject{nil},
		walkSeq:  [][]raxElement{withKB, withoutKB},
	}
	var gotPt screen.Point
	tapped := 0
	ok, tried, via, err := recoverFocusAfterKeyboard(
		context.Background(), src, &focusSnap{KeyboardOnly: true},
		screen.Point{X: 260, Y: 795}, true, si,
		func(_ context.Context, p screen.Point) error { tapped++; gotPt = p; return nil },
		60*time.Millisecond, 5*time.Millisecond,
	)
	if err != nil || !ok || !tried || via != "keyboard-last-tap" || tapped != 1 {
		t.Fatalf("got ok=%v tried=%v via=%q err=%v tapped=%d; want ok=true tried=true via=keyboard-last-tap tapped=1", ok, tried, via, err, tapped)
	}
	if src.walkCall < 2 {
		t.Fatalf("expected walk to wait for software keyboard to hide, walkCall=%d", src.walkCall)
	}
	if want := (screen.Point{X: 260, Y: 795}); gotPt != want {
		t.Fatalf("tapped %v, want %v", gotPt, want)
	}
}

// findPasteMenuItem matches "粘贴"/"Paste" callout items and never matches iOS's
// Allow Paste permission buttons.
func TestFindPasteMenuItem(t *testing.T) {
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	appEl := raxElement{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true}
	allowOnly := []raxElement{
		appEl,
		{Object: &axpObject{PID: 42, ID: 1}, Parent: 0, Type: xcButton, Label: "允许粘贴", Frame: axpRect{100, 400, 120, 40}, HasFrame: true, IsElement: true, Visible: true},
		{Object: &axpObject{PID: 42, ID: 2}, Parent: 0, Type: xcButton, Label: "Allow Paste", Frame: axpRect{100, 450, 120, 40}, HasFrame: true, IsElement: true, Visible: true},
	}
	if _, lbl, ok := findPasteMenuItem(allowOnly, si); ok {
		t.Fatalf("matched permission button %q", lbl)
	}
	withMenu := append(allowOnly, raxElement{
		Object: &axpObject{PID: 42, ID: 3}, Parent: 0, Type: xcMenuItem, Label: "粘贴",
		Frame: axpRect{140, 280, 60, 36}, HasFrame: true, IsElement: true, Visible: true,
	})
	pt, lbl, ok := findPasteMenuItem(withMenu, si)
	if !ok || lbl != "粘贴" {
		t.Fatalf("got ok=%v lbl=%q; want ok=true lbl=粘贴", ok, lbl)
	}
	if want := (screen.Point{X: 510, Y: 894}); pt != want {
		t.Fatalf("menu point %v, want %v", pt, want)
	}
}

// chooseCalloutPressPoint moves off lastTap onto the banner-shifted wide editor
// placeholder so long-press does not hit a cover photo tile that slid into
// lastTap's coordinates.
func TestChooseCalloutPressPoint(t *testing.T) {
	si := screenInfo{Width: 1284, Height: 2778, Scale: 3}
	kbRect := screen.Rect{X: 0, Y: 1900, W: 1284, H: 878}
	els := []raxElement{
		{Object: &axpObject{PID: 42, ID: 0}, Parent: -1, Frame: axpRect{0, 0, 428, 926}, HasFrame: true, Visible: true},
		{Object: &axpObject{PID: 42, ID: 1}, Parent: 0, Type: xcStaticText, Label: "1. 服务流程", Frame: axpRect{20, 322, 388, 30}, HasFrame: true, IsElement: true, Visible: true},
	}
	got := chooseCalloutPressPoint(els, screen.Point{X: 260, Y: 795}, kbRect, si)
	if want := (screen.Point{X: 642, Y: 1011}); got != want {
		t.Fatalf("press point %v, want %v", got, want)
	}
}
