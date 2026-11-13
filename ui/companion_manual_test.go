package ui

import (
	"slices"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestCompanionManual: a phone that does not let Pinky turn the app's
// service on (vivo's OriginOS 6, GitHub issue #94) gets words that say so
// and where the person turns it on, and no Turn on button, on its card, in
// its checkup and on its line in Settings › Phones.
func TestCompanionManual(t *testing.T) {
	ta := newTestApp(t, fakeapi.Companion)
	ta.api.SetCompanion("pixel-9", driver.CompanionManual)
	ta.api.SetCheckup("pixel-9", driver.Checkup{Family: "vivo", System: "OriginOS 6", Checks: []driver.Check{
		{ID: driver.CheckADB, Status: driver.CheckPass, Detail: "adb server version 41"},
		{ID: driver.CheckCompanion},
	}})
	ta.sync()
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the companion state", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] == driver.CompanionManual })
	tt.Frame()
	for _, want := range []string{locales.CompanionManualTitle(), locales.CompanionManualBody(),
		locales.CheckCompanionManualOther(), locales.CompanionNotNow()} {
		if !hasText(tt, want) {
			t.Errorf("the card lacks %q: %q", want, tt.Texts())
		}
	}
	if slices.Contains(tt.Texts(), locales.CompanionTurnOn()) || tt.Click(locales.CompanionTurnOnLabel("Pixel 9")) == nil {
		t.Errorf("the card offers Turn on: %q", tt.Texts())
	}

	// The checkup says the same, with the way to the switch on vivo's
	// phones, and the card then has it too.
	if err := tt.Click("More phone controls"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Checkup"); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf("pixel-9"); return r != nil && r.have })
	for _, want := range []string{locales.CheckCompanionManual(), locales.CheckCompanionManualVivo()} {
		if !hasText(tt, want) {
			t.Errorf("the checkup lacks %q: %q", want, tt.Texts())
		}
	}
	if slices.Contains(tt.Texts(), locales.CompanionTurnOn()) {
		t.Errorf("the checkup offers Turn on: %q", tt.Texts())
	}
	if got := companionManualWhere(ta.chk.family["pixel-9"]); got != locales.CheckCompanionManualVivo() {
		t.Errorf("the card's way to the switch after the checkup = %q", got)
	}

	ta.page, ta.set.sec = pageSettings, secPhones
	tt.Frame()
	if !hasText(tt, locales.CompanionLineManual()) {
		t.Errorf("Settings › Phones: %q", tt.Texts())
	}
}

// An install whose service then cannot come on, because the phone ignored
// Pinky's change, says why; once the phone's state says it too, the card
// does not repeat it in red.
func TestCompanionInstallOnAPhoneThatIgnoresTheChange(t *testing.T) {
	ta := newTestApp(t, fakeapi.Companion)
	ta.api.SetInstallError("pixel-9", &driver.CompanionStartError{Reason: driver.CompanionStartSettingIgnored,
		Detail: "the phone took the change to enabled_accessibility_services without an error but did not keep Pinky's entry in it"})
	ta.sync()
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the card", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] == driver.CompanionMissing })
	tt.Frame()
	if err := tt.Click("Install Pinky on Pixel 9"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the install to fail", func() bool { tt.Frame(); return ta.comp.err["pixel-9"] != "" })
	if got := ta.comp.err["pixel-9"]; got != locales.CompanionErrSettingIgnored() {
		t.Errorf("the install error = %q", got)
	}
	tt.Frame()
	if !hasText(tt, locales.CompanionErrSettingIgnored()) {
		t.Errorf("the card does not say why: %q", tt.Texts())
	}

	// The driver remembers the phone (the fake only once told).
	ta.api.SetCompanion("pixel-9", driver.CompanionManual)
	ta.comp.checked["pixel-9"] = time.Time{}
	ta.waitFor(t, "the manual state", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] == driver.CompanionManual })
	tt.Frame()
	if !hasText(tt, locales.CompanionManualBody()) || hasText(tt, locales.CompanionErrSettingIgnored()) {
		t.Errorf("the card: %q", tt.Texts())
	}
}

// TestCompanionManualOn: once the person turned the service on on such a
// phone, the checkup and Settings › Phones say that it stays on until they
// turn it off on the phone, and that the phone's apps can see it; the card
// offers nothing.
func TestCompanionManualOn(t *testing.T) {
	ta := newTestApp(t, fakeapi.Companion)
	ta.api.SetCompanion("pixel-9", driver.CompanionManualOn)
	ta.api.SetCheckup("pixel-9", driver.Checkup{Family: "vivo", System: "OriginOS 6", Checks: []driver.Check{
		{ID: driver.CheckADB, Status: driver.CheckPass, Detail: "adb server version 41"},
		{ID: driver.CheckCompanion},
	}})
	ta.sync()
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.waitFor(t, "the companion state", func() bool { tt.Frame(); return ta.comp.state["pixel-9"] == driver.CompanionManualOn })
	tt.Frame()
	for _, unwanted := range []string{locales.CompanionManualTitle(), locales.CompanionTurnOn(), locales.CompanionNotNow()} {
		if hasText(tt, unwanted) {
			t.Errorf("the card shows %q: %q", unwanted, tt.Texts())
		}
	}

	if err := tt.Click("More phone controls"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("Checkup"); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf("pixel-9"); return r != nil && r.have })
	if !hasText(tt, locales.CheckCompanionManualOn()) || hasText(tt, locales.CheckCompanionPass()) {
		t.Errorf("the checkup: %q", tt.Texts())
	}

	ta.page, ta.set.sec = pageSettings, secPhones
	tt.Frame()
	if !hasText(tt, locales.CompanionLineManualOn()) {
		t.Errorf("Settings › Phones: %q", tt.Texts())
	}
}
