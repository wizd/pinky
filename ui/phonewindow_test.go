package ui

import (
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// windowTester draws the phone's own window, w × h.
func windowTester(ta *testApp, id string, w, h int) *mg.Tester {
	return mg.NewTester(func(c *mg.Context) { ta.phoneWindowView(c, id) }, w, h)
}

// TestPhoneWindowInput: the phone's own window shows the mirror of its page,
// with the one stream, and takes the person's clicks there, at its own size,
// in the phone's pixels, while the page shows the same mirror at another.
// A click takes over; Hand back in the window's bar gives the phone back.
func TestPhoneWindowInput(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.waitFor(t, "the payment approval", func() bool { _, ok := ta.approval(); return ok })
	img, _ := fakeCheckout(t, 360, 800, 3, false)
	m := stillMirror(ta, "pixel-9", img)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	page := findPhone(t, tt, "Pixel 9")

	// From the ⋯ menu under the mirror.
	if err := tt.Click(locales.PhoneMoreControls()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click(locales.PhoneOpenWindow()); err != nil {
		t.Fatal(err)
	}
	pw := ta.phoneWins["pixel-9"]
	if pw == nil {
		t.Fatal("no window after Open in a window")
	}
	wt := windowTester(ta, "pixel-9", 300, 640)
	wt.Frame()
	tt.Frame()
	if len(ta.mirrors) != 1 || ta.mirrors["pixel-9"] != m || pw.mirror != m {
		t.Fatalf("the window does not share the page's mirror: %d mirrors, window's %p, page's %p", len(ta.mirrors), pw.mirror, m)
	}
	scr, ok := wt.Find(locales.MirrorScreenLabel("Pixel 9"))
	if !ok {
		t.Fatalf("no screen in the window: %q", wt.Texts())
	}
	if scr.H == page.screen.H {
		t.Fatalf("the window's screen %v is the page's size %v: the test proves nothing", scr, page.screen)
	}
	if r := scr.W / scr.H; r < 0.44 || r > 0.46 {
		t.Errorf("the window's screen %v is not the phone's shape", scr)
	}
	for _, f := range [][2]float32{{0.5, 0.5}, {0.1, 0.05}, {0.9, 0.95}} {
		down, up := clickScreen(t, ta, wt, scr, f[0], f[1])
		x, y := int(f[0]*float32(m.picW)), int(f[1]*float32(m.picH))
		if !nearPoint(down, x, y) || down != up {
			t.Errorf("a click at %.2f, %.2f of the window's screen touched %v to %v, want %d, %d", f[0], f[1], down, up, x, y)
		}
		// The page, at its own size, between the window's clicks.
		tt.Frame()
		page = findPhone(t, tt, "Pixel 9")
		down, _ = clickScreen(t, ta, tt, page.screen, f[0], f[1])
		if !nearPoint(down, x, y) {
			t.Errorf("a click at %.2f, %.2f of the page's screen touched %v, want %d, %d", f[0], f[1], down, x, y)
		}
		wt.Frame()
	}

	// The clicks took over: the window's bar says so, with Hand back.
	ta.waitFor(t, "takeover", func() bool { return ta.devices[0].Status == gateway.DeviceInControl })
	wt.Frame()
	if !hasText(wt, locales.PhoneHandBack()) || !hasText(wt, "You're in control.") {
		t.Fatalf("no banner in the window: %q", wt.Texts())
	}
	if err := wt.Click(locales.PhoneHandBack()); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the hand back", func() bool { return ta.devices[0].Status != gateway.DeviceInControl })
	wt.Frame()
	if hasText(wt, locales.PhoneHandBack()) {
		t.Errorf("Hand back stayed: %q", wt.Texts())
	}

	// Opened again, it is the same window.
	ta.openPhoneWindow("pixel-9")
	if len(ta.phoneWins) != 1 || ta.phoneWins["pixel-9"] != pw {
		t.Errorf("opening again made another window: %v", ta.phoneWins)
	}
}

// TestPhoneWindowKeepsTheStream: the mirror the window shows stays open while
// the main window hides or shows another page, and goes with the window.
func TestPhoneWindowKeepsTheStream(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	img, _ := fakeCheckout(t, 360, 800, 3, false)
	m := stillMirror(ta, "pixel-9", img)
	ta.openPhoneWindow("pixel-9")
	wt := windowTester(ta, "pixel-9", 300, 640)
	wt.Frame()

	ta.closeMirrors() // the main window hid
	if ta.mirrors["pixel-9"] != m {
		t.Fatal("hiding the main window closed the window's mirror")
	}
	tt := mg.NewTester(ta.view, 1280, 820)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	tt.Frame() // sweeps the mirrors the page no longer shows
	if ta.mirrors["pixel-9"] != m || m.state != "playing" {
		t.Fatalf("another page let the window's mirror go: %v, %q", ta.mirrors["pixel-9"], m.state)
	}
	ta.closePhoneWindow("pixel-9")
	if ta.phoneWins["pixel-9"] != nil || ta.mirrors["pixel-9"] != nil {
		t.Errorf("closing the window kept %v and the mirror %v", ta.phoneWins, ta.mirrors)
	}
}

// TestPhoneWindowStates: the right-click menus open the window; a phone with
// no live screen shows why, with Show in Pinky; an iPhone has only Home;
// Keep on top turns on and off; and a phone removed takes its window along.
func TestPhoneWindowStates(t *testing.T) {
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.waitFor(t, "the lunch task", func() bool { return ta.taskOf("pixel-9") != nil })
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.RightClick("Galaxy S25, Offline"); err != nil { // its row in the sidebar
		t.Fatal(err)
	}
	if menu := tt.Menu(); !slices.Contains(menu, locales.PhoneOpenWindow()) {
		t.Fatalf("the row's menu: %q", menu)
	}
	if err := tt.ChooseMenuItem(locales.PhoneOpenWindow()); err != nil {
		t.Fatal(err)
	}
	if ta.phoneWins["galaxy-s25"] == nil {
		t.Fatal("no window for Galaxy S25")
	}
	wt := windowTester(ta, "galaxy-s25", 300, 640)
	wt.Frame()
	if !hasText(wt, "Galaxy S25 is offline") || hasText(wt, locales.PhoneKeyHome()) {
		t.Errorf("the offline phone's window: %q", wt.Texts())
	}
	if ta.mirrors["galaxy-s25"] != nil {
		t.Error("an offline phone's window opened a stream")
	}
	if err := wt.Click(locales.PhoneWindowShowInPinky()); err != nil {
		t.Fatal(err)
	}
	if ta.sel != "galaxy-s25" || ta.page != pagePhones {
		t.Errorf("Show in Pinky chose %q on page %v", ta.sel, ta.page)
	}

	if err := tt.Click("All phones"); err != nil {
		t.Fatal(err)
	}
	if err := tt.RightClick("Show iPhone 17"); err != nil { // its tile
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem(locales.PhoneOpenWindow()); err != nil {
		t.Fatal(err)
	}
	it := windowTester(ta, "iphone-17", 300, 640)
	it.Frame()
	if _, ok := it.Find(locales.PhoneKeyHome()); !ok {
		t.Errorf("no Home key in the iPhone's window: %q", it.Texts())
	}
	if _, ok := it.Find(locales.PhoneKeyBack()); ok {
		t.Error("a Back key in the iPhone's window")
	}
	if err := it.Click(locales.PhoneWindowOnTop()); err != nil {
		t.Fatal(err)
	}
	if !ta.winTop["iphone-17"] {
		t.Error("Keep on top did not turn on")
	}
	if err := it.Click(locales.PhoneWindowOnTopOff()); err != nil {
		t.Fatal(err)
	}
	if ta.winTop["iphone-17"] {
		t.Error("Keep on top did not turn off")
	}

	// The iPhone goes away, and its window with it.
	ta.api.Play(fakeapi.OnePhone)
	ta.sync()
	it.Frame()
	ta.sync()
	if ta.phoneWins["iphone-17"] != nil {
		t.Error("the removed phone's window stayed")
	}
}

// TestPhoneWindowShape: the window's size for the screen's shape, as it
// opens, as the person resizes it, and as the phone turns.
func TestPhoneWindowShape(t *testing.T) {
	screen := func(w, h int) (float32, float32) {
		return float32(w - 2*winPad), float32(h - winPad - winBar)
	}
	const phone = float32(9.0 / 20)
	w, h := phoneWindowSize(phone, 1512, 945)
	if sw, sh := screen(w, h); sh > 0.75*945 || abs32(sw/sh-phone) > 0.01 {
		t.Errorf("a new phone window is %d × %d", w, h)
	}
	w, h = phoneWindowSize(4.0/3, 1512, 945) // an iPad turned sideways
	if sw, sh := screen(w, h); sw > 0.5*1512+1 || abs32(sw/sh-4.0/3) > 0.01 {
		t.Errorf("a new tablet window is %d × %d", w, h)
	}
	for _, byWidth := range []bool{true, false} {
		w, h = fitPhoneWindow(400, 700, phone, byWidth)
		if sw, sh := screen(w, h); abs32(sw/sh-phone) > 0.01 || (byWidth && w != 400) || (!byWidth && h != 700) {
			t.Errorf("fit by width %v: %d × %d", byWidth, w, h)
		}
	}
	// Turned sideways, the screen keeps its area and fits the display.
	w0, h0 := fitPhoneWindow(316, 700, phone, true)
	w, h = turnPhoneWindow(w0, h0, 1/phone, 1512, 945)
	sw0, sh0 := screen(w0, h0)
	sw, sh := screen(w, h)
	if abs32(sw/sh-1/phone) > 0.01 || abs32(sw*sh-sw0*sh0)/(sw0*sh0) > 0.02 {
		t.Errorf("turned: %d × %d from %d × %d", w, h, w0, h0)
	}
	w, h = turnPhoneWindow(w0, h0, 1/phone, 800, 600)
	if w > 720 || h > 540 {
		t.Errorf("turned on a small display: %d × %d", w, h)
	}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// TestPhoneWindowScreenshots draws the phone's own window into
// $PINKY_SCREENSHOTS, in light and dark, in each of the client's languages:
// phone-window-<lang>-<theme>.png has it as the agent works, in the person's
// control with Hand back, and offline, side by side. The mirror shows a page
// drawn in the test.
func TestPhoneWindowScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	const scale, w, h = 2, 300, 640
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		checkout, _ := fakeCheckout(t, 360, 800, 3, lang == "zh-Hans")
		working := newTestApp(t, fakeapi.OnePhone)
		working.waitFor(t, "the lunch task", func() bool { return working.taskOf("pixel-9") != nil })
		takeover := newTestAppTimeout(t, fakeapi.Takeover, 10*time.Minute)
		takeover.waitFor(t, "the takeover", func() bool {
			return len(takeover.devices) > 0 && takeover.devices[0].Status == gateway.DeviceInControl
		})
		offline := newTestApp(t, fakeapi.ThreePhones)
		offline.sync()
		for _, dark := range []bool{false, true} {
			var shots []*image.RGBA
			for _, s := range []struct {
				ta *testApp
				id string
			}{{working, "pixel-9"}, {takeover, "pixel-9"}, {offline, "galaxy-s25"}} {
				if s.id == "pixel-9" {
					stillMirror(s.ta, s.id, checkout)
				}
				s.ta.openPhoneWindow(s.id)
				wt := windowTester(s.ta, s.id, w, h)
				inLanguage(t, lang, wt)
				wt.SetScale(scale)
				wt.SetDark(dark)
				wt.Frame()
				shots = append(shots, wt.Image())
			}
			gap := 24 * scale
			out := image.NewRGBA(image.Rect(0, 0, len(shots)*w*scale+(len(shots)-1)*gap, h*scale))
			for i, s := range shots {
				draw.Draw(out, s.Bounds().Add(image.Pt(i*(w*scale+gap), 0)), s, image.Point{}, draw.Src)
			}
			theme := "light"
			if dark {
				theme = "dark"
			}
			f, err := os.Create(filepath.Join(dir, "phone-window-"+langShort(lang)+"-"+theme+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, out); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}
