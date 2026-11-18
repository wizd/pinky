package ui

import (
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/demogw"
)

// TestPanelAppsPickerKeepsButtons: choosing "Only the apps I choose" on a
// first connection in the floating panel shows the list of apps, and the
// panel grows so that Deny and Approve still show (DIYgod/pinky#158).
func TestPanelAppsPickerKeepsButtons(t *testing.T) {
	ga := newGatewayApp(t, false)
	done := ga.agent(t, "Claude Code", pixel, (*demogw.Agent).ReorderLunch)
	var n gateway.NeedView
	ga.waitFor(t, "the New agent approval", func() (ok bool) { n, ok = ga.need(newAgentNeed); return })
	// More apps than the list shows at once, as on a real phone: the field
	// that narrows them down shows, and the list takes its whole height.
	pickerApps(ga)
	panel := mg.NewTester(ga.panelView, panelW, panelH)
	panel.Frame()
	ga.agents.makeMaps()
	ga.agents.choice[n.ID] = string(gateway.ScopeApps)
	ga.waitFor(t, "the list of apps", func() bool {
		panel.SetSize(ga.panelSize()) // as fitPanel sizes the window
		panel.Frame()
		return !hasText(panel, "Looking for the apps on your phones…")
	})
	panel.SetSize(ga.panelSize())
	panel.Frame()
	w, h := ga.panelSize()
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		panel.SetLocale(lang)
		panel.Frame()
		// Deny and Approve are the panel's last row: the lowest box drawn
		// must end inside the panel.
		var bottom float32
		for _, s := range panel.Texts() {
			if r, ok := panel.Find(s); ok && r.H > 0 {
				bottom = max(bottom, r.Y+r.H)
			}
		}
		if bottom == 0 || bottom > float32(h) || w != panelW {
			t.Errorf("%s: the panel's content ends at %v, the %dx%d panel at %d: %q", lang, bottom, w, h, h, panel.Texts())
		}
		// Find clips to the panel, and a button pushed out of it has no
		// height there; its accessibility bounds are where it is laid out.
		for _, b := range []string{locales.NeedsDeny(), locales.NeedsApprove()} {
			if r, ok := panel.AccessByRole(mg.RoleButton, b); !ok || r.Bounds.Y+r.Bounds.H > float32(h) {
				t.Errorf("%s: %q at %+v, outside the %dx%d panel", lang, b, r.Bounds, w, h)
			}
		}
	}
	panel.SetLocale("en")
	panel.Frame()
	for _, b := range []string{"Deny", "Approve"} {
		if r, ok := panel.Find(b); !ok || r.H == 0 || r.Y+r.H > float32(h) {
			t.Errorf("%q at %+v, outside the %dx%d panel", b, r, w, h)
		}
	}
	if err := panel.Click("Deny"); err != nil {
		t.Fatal(err)
	}
	_ = wait(t, done, "the agent")
}
