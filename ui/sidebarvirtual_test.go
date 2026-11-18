package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestSidebarNamesVirtualPhones: a Simulator or an emulator says so under
// its name in the sidebar, running or not. A Simulator is named after the
// phone it copies, so an offline "iPhone Air" Simulator read as a second
// copy of the person's iPhone Air, whose row looked the same.
func TestSidebarNamesVirtualPhones(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	tt := mg.NewTester(ta.view, 1280, 820)
	const sim, emu = "5F1E7A2C-SIM-17", "emulator-5554"
	for _, start := range []struct {
		pf driver.Platform
		id string
	}{{driver.IOS, sim}, {driver.Android, "Pixel_9_API_36"}} {
		if err := ta.api.StartVirtual(t.Context(), start.pf, start.id); err != nil {
			t.Fatal(err)
		}
	}
	ta.api.BootVirtual()
	online := func(id string) bool { d := ta.device(id); return d != nil && d.Info.State == driver.StateOnline }
	ta.waitFor(t, "both running", func() bool { return online(sim) && online(emu) })
	for _, lang := range testLanguages() {
		t.Run(langShort(lang), func(t *testing.T) {
			inLanguage(t, lang, tt)
			ta.sync()
			tt.Frame()
			for _, id := range []string{sim, emu} {
				d := ta.device(id)
				want := virtualKind(d.Info.Platform) + " · " + statusLine(*d)
				if !hasText(tt, want) {
					t.Errorf("%s running: no %q in %q", id, want, tt.Texts())
				}
			}
			// A phone that is not virtual keeps its status alone.
			if d := ta.devices[0]; !d.Info.Virtual && sidebarLine(d) != statusLine(d) {
				t.Errorf("%s: %q", d.Info.Name, sidebarLine(d))
			}
		})
	}
	if err := ta.api.StopVirtual(t.Context(), driver.IOS, sim); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the Simulator offline", func() bool { return !online(sim) })
	ta.sync()
	tt.Frame()
	want := locales.PhoneKindSimulator() + " · " + statusLine(*ta.device(sim))
	if !hasText(tt, want) {
		t.Errorf("stopped: no %q in %q", want, tt.Texts())
	}
}
