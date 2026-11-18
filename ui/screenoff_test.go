package ui

import (
	"testing"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/ui/fakeapi"
	mg "github.com/egoist/mygo/ui"
)

// TestScreenOffMirror: an idle phone whose own screen is off without a
// lock screen (an Android TV in standby) sends no picture, and its mirror
// used to show Connecting for as long as it slept. It says the screen is
// off instead, and a click on the mirror turns the screen on rather than
// sending a touch the sleeping phone would drop.
func TestScreenOffMirror(t *testing.T) {
	ta := newTestApp(t, fakeapi.Companion)
	ta.waitFor(t, "the phone", func() bool { return len(ta.devices) > 0 })
	id := ta.devices[0].Info.ID
	if err := ta.api.SetScreenOff(id, true); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the screen to be off", func() bool { return ta.devices[0].ScreenOff })
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	b := findPhone(t, tt, "Pixel 9")
	if _, ok := tt.Find(locales.MirrorScreenOff("Pixel 9")); !ok {
		t.Fatalf("no Screen off pill: %q", tt.Texts())
	}
	if !hasText(tt, locales.MirrorScreenOffWake()) {
		t.Errorf("the mirror does not say a click wakes the screen: %q", tt.Texts())
	}
	if hasText(tt, locales.MirrorConnecting()) {
		t.Errorf("a phone with its screen off still shows Connecting: %q", tt.Texts())
	}

	n := len(ta.api.Inputs())
	tt.ClickAt(b.screen.X+b.screen.W/2, b.screen.Y+b.screen.H/2)
	ta.waitFor(t, "the screen to turn on", func() bool { return !ta.devices[0].ScreenOff })
	if got := ta.api.Inputs()[n:]; len(got) != 0 {
		t.Errorf("the click that woke the screen also sent %+v", got)
	}
	tt.Frame()
	if _, ok := tt.Find(locales.MirrorScreenOff("Pixel 9")); ok {
		t.Errorf("the Screen off pill stayed after waking: %q", tt.Texts())
	}
}

// TestScreenOffTile: the phone's tile in All phones says Screen off too.
func TestScreenOffTile(t *testing.T) {
	ta := newTestApp(t, fakeapi.Companion)
	ta.waitFor(t, "the phone", func() bool { return len(ta.devices) > 0 })
	if err := ta.api.SetScreenOff(ta.devices[0].Info.ID, true); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the screen to be off", func() bool { return ta.devices[0].ScreenOff })
	ta.selectPhone(allPhones)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, locales.MirrorScreenOffTile()) {
		t.Fatalf("no Screen off on the tile: %q", tt.Texts())
	}
}
