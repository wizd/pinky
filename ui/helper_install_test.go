package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// helperTeams are teams a Mac signs for in the tests: one with a name, and
// one with its ID alone.
var helperTeams = []driver.HelperTeam{{ID: "ABCDE12345", Name: "Sam Lee (Personal Team)"}, {ID: "FGHIJ67890"}}

// exampleTeams returns n teams to choose from: helperTeams, then teams of
// an organization, whose buttons are wider than the smallest window's page.
func exampleTeams(n int) []driver.HelperTeam {
	teams := slices.Clone(helperTeams)
	for i := len(teams); i < n; i++ {
		teams = append(teams, driver.HelperTeam{ID: fmt.Sprintf("KLMNO%05d", i), Name: "Shenzhen Example Technology Co., Ltd."})
	}
	return teams[:n]
}

// wideTeam is a team whose button is wider than the default window's page.
var wideTeam = driver.HelperTeam{ID: "WIDE012345", Name: "Guangdong Example Intelligent Manufacturing Technology Co., Ltd."}

// installApp is helperApp on a Mac with Xcode: Pinky installs the helper
// from its window.
func installApp(t *testing.T) (*testApp, string) {
	t.Helper()
	ta, id, _ := helperApp(t)
	ta.api.SetHelperInstall(true)
	return ta, id
}

// installed waits until the install from the window is done, and checks
// that it said so and that the phone is online with a helper good for a
// week.
func installed(t *testing.T, ta *testApp, id, name string) {
	t.Helper()
	// Without frames, which show the toasts and let go of them.
	ta.waitFor(t, "the install to end", func() bool { return ta.helper.runs[id] == nil })
	if !slices.Contains(ta.toasts, locales.HelperInstalledToast(name)) {
		t.Errorf("no word that it is installed: %q", ta.toasts)
	}
	ta.waitFor(t, "the phone online", func() bool {
		d := ta.iphoneView(t)
		return d.Info.State == driver.StateOnline && d.Info.HelperExpires.Sub(timeNow()) > 6*24*time.Hour
	})
}

// TestIPhoneHelperInstallFromPage: on a Mac with Xcode, an iPhone that
// needs the helper offers to install it from its page, in place of the
// link to how; while it installs, the page says what it does, in bold when
// the phone has to be unlocked; once done, the phone is online.
func TestIPhoneHelperInstallFromPage(t *testing.T) {
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, Detail: "Pinky is not installed on this iPhone."})
	ta.api.HoldHelper(true)
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	for _, want := range []string{locales.CheckHelperMissing(), locales.HelperInstall(), locales.HelperInstallNote(), locales.PhoneCheckup()} {
		if !hasText(tt, want) {
			t.Errorf("the page lacks %q: %q", want, tt.Texts())
		}
	}
	if hasText(tt, locales.CheckHelperHow()) || hasText(tt, locales.PhoneCheckupFix()) {
		t.Errorf("the page still says how to install it by hand: %q", tt.Texts())
	}

	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "Xcode building", func() bool { return hasText(tt, locales.HelperStepBuilding()) })
	if !hasText(tt, locales.CheckHelperTitle()) || !hasText(tt, locales.HelperCancel()) || hasText(tt, locales.HelperInstall()) {
		t.Errorf("while building: %q", tt.Texts())
	}
	// Xcode waits for the phone to be unlocked: said in bold.
	ta.helper.runs[id].step = driver.HelperUnlock
	tt.Frame()
	if !hasText(tt, locales.HelperStepUnlock(name)) {
		t.Errorf("no word to unlock the phone: %q", tt.Texts())
	}
	ta.api.HoldHelper(false)
	installed(t, ta, id, name)
	if runs := ta.api.HelperRuns(); len(runs) != 1 || runs[0] != (fakeapi.HelperRun{DeviceID: id}) {
		t.Errorf("installs %+v", runs)
	}
	tt.Frame()
	if hasText(tt, locales.PhoneNeedsSetup(name)) {
		t.Errorf("the page still needs setup: %q", tt.Texts())
	}
}

// TestIPhoneHelperInstallFails: a failed install says why in words, with
// Xcode's own below and the log a click away, and installs again on the
// next click.
func TestIPhoneHelperInstallFails(t *testing.T) {
	ta, id := installApp(t)
	var revealed string
	ta.reveal = func(p string) { revealed = p }
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
	const why = "the developer certificate is not trusted on the phone"
	const log = "/tmp/pinky/WebDriverAgent/helper-install.log"
	ta.api.SetHelperError(&driver.HelperInstallError{Fix: driver.HelperFixTrust, Err: errors.New(why), Hint: "Trust it on the phone.", Log: log})
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the failure", func() bool { r := ta.helper.runs[id]; return r != nil && !r.running })
	for _, want := range []string{locales.HelperErrTrust(name), why, locales.HelperShowLog(), locales.HelperInstall()} {
		if !hasText(tt, want) {
			t.Errorf("the page lacks %q: %q", want, tt.Texts())
		}
	}
	if hasText(tt, "Trust it on the phone.") {
		t.Errorf("the command line's hint shows: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperShowLog()); err != nil || revealed != log {
		t.Errorf("Show the log: %v, revealed %q", err, revealed)
	}

	ta.api.SetHelperError(nil)
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, name)
	if len(ta.api.HelperRuns()) != 2 {
		t.Errorf("installs %+v", ta.api.HelperRuns())
	}

	// An agent holds the phone: Pinky does not take it away.
	if got, _, _ := helperErrorWords(fmt.Errorf("installing: %w", gateway.ErrPhoneInUse), name); got != locales.HelperErrInUse(name) {
		t.Errorf("in use: %q", got)
	}
}

// TestIPhoneHelperInstallTeams: when the Mac signs for several teams, the
// install asks which, and goes on with the one chosen.
func TestIPhoneHelperInstallTeams(t *testing.T) {
	ta, id := installApp(t)
	teams := helperTeams
	ta.api.SetHelperTeams(teams)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
	ta.sync()
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "the teams", func() bool { return hasText(tt, locales.HelperChooseTeam()) })
	first := locales.HelperTeam(teams[0].Name, teams[0].ID)
	if !hasText(tt, first) || !hasText(tt, teams[1].ID) {
		t.Fatalf("the teams: %q", tt.Texts())
	}
	if err := tt.Click(teams[1].ID); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, name)
	if runs := ta.api.HelperRuns(); len(runs) != 2 || runs[1].Team != teams[1].ID {
		t.Errorf("installs %+v", runs)
	}
}

// TestIPhoneHelperTeamsFit: the teams to choose from keep within the page
// that shows them, in the default window and the smallest, in each of the
// client's languages. On the page of an iPhone that needs the helper, which
// does not scroll, more teams than it has room for scroll, in a list whose
// scroll bar takes no press on a team's end, and Cancel and the page's
// links stay in the window; in the checkup, scrolled to, they keep within
// its panel. A
// team's name wider than the page is cut, and whole in a tooltip.
func TestIPhoneHelperTeamsFit(t *testing.T) {
	ta, id := installApp(t)
	name := ta.iphoneView(t).Info.Name
	ta.selectPhone(id)
	sets := []struct {
		what  string
		teams []driver.HelperTeam
	}{
		{"a team wider than the page", append(exampleTeams(2), wideTeam)},
		{"nine teams", exampleTeams(9)},
	}
	const eps = 0.5
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		for _, set := range sets {
			var labels []string
			for _, team := range set.teams {
				labels = append(labels, helperTeamLabel(team))
			}
			last := labels[len(labels)-1]
			for _, size := range [][2]int{{1280, 820}, {960, 620}} {
				w, h := float32(size[0]), float32(size[1])
				what := fmt.Sprintf("%s %v, %s", lang, size, set.what)
				stage := mg.Rect{X: sidebarWidth + stagePad, Y: headerHeight + stagePad, W: w - sidebarWidth - rightPanelWidth - 2*stagePad, H: h - headerHeight - 2*stagePad}
				page := stage // the page's column in it, at most 420 wide
				page.W = min(stage.W, 420)
				page.X += (stage.W - page.W) / 2
				show := func() *mg.Tester {
					ta.chk.showing = ""
					ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
					ta.sync()
					ta.helper.runs = map[string]*helperRun{id: {teams: set.teams}}
					tt := mg.NewTester(ta.view, size[0], size[1])
					inLanguage(t, lang, tt)
					tt.Frame()
					tt.Frame()
					return tt
				}
				tt := show()
				teamsWithin(t, tt, what+", the page", page, labels, buttonWidths(t, lang, false, labels), buttonHeight)
				for _, link := range []string{locales.HelperCancel(), locales.PhoneCheckup(), locales.RemovePhoneLink()} {
					if b, ok := tt.Find(link); !ok || b.H == 0 || !rectWithin(b, stage, eps) {
						t.Errorf("%s, the page: %q %v (shown %v) is not within the stage %v", what, link, b, ok, stage)
					}
				}
				if b, _ := tt.Find(last); b.H != 0 {
					if size[1] == 620 && len(labels) == 9 {
						t.Errorf("%s, the page: the last team %v shows without scrolling", what, b)
					}
				} else {
					// A press on the end of a team as wide as the list, with
					// the pointer on the list, which shows its bar, chooses the
					// team.
					wide, _ := tt.Find(labels[2])
					if wide.H < buttonHeight-eps || wide.W < page.W-eps {
						t.Fatalf("%s, the page: the third team %v is not whole and as wide as the page %v", what, wide, page)
					}
					x, y := wide.X+wide.W-2, wide.Y+wide.H/2
					tt.Move(x, y)
					chooses(t, ta, tt, what+", the end of the third team", set.teams[2].ID, func() { tt.ClickAt(x, y) })
					// Scrolled to its end, the list shows the last team, which
					// installs.
					tt = show()
					first, _ := tt.Find(labels[0])
					tt.Scroll(first.X+first.W/2, first.Y+first.H/2, 0, 2000)
					if b, _ := tt.Find(last); b.H < buttonHeight-eps || !rectWithin(b, page, eps) {
						t.Errorf("%s, the page: the last team %v is not whole within the page %v once the teams scroll", what, b, page)
					}
					chooses(t, ta, tt, what+", the last team", set.teams[len(set.teams)-1].ID, func() {
						if err := tt.Click(last); err != nil {
							t.Fatal(err)
						}
					})
				}

				// The checkup, where the teams follow the helper's item.
				ta.helper.runs = nil
				ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline})
				ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
					{ID: driver.CheckHelper, Status: driver.CheckFail, Fix: driver.FixInstallHelper},
				}})
				ta.sync()
				tt = mg.NewTester(ta.view, size[0], size[1])
				inLanguage(t, lang, tt)
				ta.openCheckup(ta.iphoneView(t))
				ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
				ta.helper.runs = map[string]*helperRun{id: {teams: set.teams}}
				tt.Frame()
				tt.Frame()
				panel, ok := tt.Find(locales.CheckupFor(name))
				if !ok || !hasText(tt, locales.HelperChooseTeam()) {
					t.Fatalf("%s: no checkup %v (shown %v) with the teams: %q", what, panel, ok, tt.Texts())
				}
				// The checkup's items scroll, and where their fold falls
				// depends on the fonts: with those of CI's Linux and Windows
				// runners, the teams start just under it at 960×620. Scroll
				// the first team into view, half a button at a time, as a
				// person would; squashed, it would still be cut.
				for range 40 {
					if first, _ := tt.Find(labels[0]); first.H >= smallButtonHeight-eps {
						break
					}
					tt.Scroll(panel.X+panel.W/2, panel.Y+panel.H/2, 0, smallButtonHeight/2)
					tt.Frame()
				}
				teamsWithin(t, tt, what+", the checkup", panel, labels, buttonWidths(t, lang, true, labels), smallButtonHeight)
			}
		}
	}
}

// teamsWithin checks that the buttons of the teams labeled labels, whose
// labels take widths, lie within area, the first whole at the height
// given, and that each is as wide as its label takes or cuts it with all of
// it in a tooltip. Those scrolled out of view have no area.
func teamsWithin(t *testing.T, tt *mg.Tester, what string, area mg.Rect, labels []string, widths []float32, height float32) {
	t.Helper()
	const eps = 0.5
	for i, label := range labels {
		b, ok := tt.Find(label)
		switch {
		case !ok:
			t.Errorf("%s: no team %q: %q", what, label, tt.Texts())
		case i == 0 && b.H < height-eps:
			t.Errorf("%s: the first team %q %v is cut", what, label, b)
		case b.W == 0 || b.H == 0:
		case !rectWithin(b, area, eps):
			t.Errorf("%s: the team %q %v spills out of %v", what, label, b, area)
		case b.W < widths[i]-eps && !wordsCut(tt, label):
			t.Errorf("%s: the team %q %v, %.1f wide whole, is cut short with no tooltip", what, label, b, widths[i])
		}
	}
}

// buttonWidths returns the widths of buttons with the labels, small or
// not, as the client draws them in the language lang.
func buttonWidths(t *testing.T, lang string, small bool, labels []string) []float32 {
	t.Helper()
	widths := make([]float32, len(labels))
	mt := mg.NewTester(func(c *mg.Context) {
		applyTheme(c)
		for i, label := range labels {
			widths[i] = buttonWidth(c, label, small)
		}
	}, 200, 100)
	inLanguage(t, lang, mt)
	mt.Frame()
	return widths
}

// chooses checks that press, on the iPhone id's teams in tt, installs the
// helper with the team named team, and stops the install.
func chooses(t *testing.T, ta *testApp, tt *mg.Tester, what, team string, press func()) {
	t.Helper()
	id := ta.iphoneView(t).Info.ID
	n := len(ta.api.HelperRuns())
	// Held, so that the install has not put the phone back by the time it
	// is stopped.
	ta.api.HoldHelper(true)
	defer ta.api.HoldHelper(false)
	press()
	if r := ta.helper.runs[id]; r == nil || !r.running {
		t.Fatalf("%s: no install: %q", what, tt.Texts())
	}
	ta.waitFor(t, what, func() bool {
		runs := ta.api.HelperRuns()
		return len(runs) == n+1 && runs[n].Team == team
	})
	ta.helper.runs[id].cancel()
	ta.waitFor(t, "the install to stop", func() bool { return ta.helper.runs[id] == nil })
}

// TestIPhoneHelperInstallAgain: two days before the helper's signature runs
// out, the reminder above the phone installs it again; the checkup offers
// it once, from the helper's item.
func TestIPhoneHelperInstallAgain(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(-30*time.Hour))
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
		{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper, Detail: "WebDriverAgent answers; its provisioning profile runs out soon"},
		{ID: driver.CheckInput, Status: driver.CheckWarn, Fix: driver.FixInstallHelper},
	}})
	ta.sync()
	d := ta.iphoneView(t)
	soon := locales.CheckHelperSoonHere(d.Info.Name, exp.Local())
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, soon) || !hasText(tt, locales.HelperInstallAgain()) || hasText(tt, locales.CheckHelperHow()) {
		t.Fatalf("the reminder: %q", tt.Texts())
	}

	ta.openCheckup(d)
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
	// One button: its label and its text.
	if !hasText(tt, soon) || countText(tt, locales.HelperInstallAgain()) != 2 || hasText(tt, locales.CheckHelperHow()) {
		t.Errorf("the checkup: %q", tt.Texts())
	}
	ta.chk.showing = ""
	tt.Frame()

	ta.api.HoldHelper(true)
	if err := tt.Click(locales.HelperInstallAgain()); err != nil {
		t.Fatal(err)
	}
	ta.frameUntil(t, tt, "Xcode building", func() bool { return hasText(tt, locales.HelperStepBuilding()) })
	ta.api.HoldHelper(false)
	installed(t, ta, id, d.Info.Name)
	tt.Frame()
	if hasText(tt, soon) || hasText(tt, locales.HelperInstallAgain()) {
		t.Errorf("the reminder stays: %q", tt.Texts())
	}
}

// TestIPhoneHelperRanOutHere: once the helper ran out, the page installs
// it again, in words that leave out the command.
func TestIPhoneHelperRanOutHere(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(3*time.Hour))
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, locales.CheckHelperExpiredHere(d.Info.Name, exp.Local())) || !hasText(tt, locales.HelperInstallAgain()) || hasText(tt, locales.CheckHelperHow()) {
		t.Fatalf("the page: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperInstallAgain()); err != nil {
		t.Fatal(err)
	}
	installed(t, ta, id, d.Info.Name)
}

// TestAddOlderIPhoneHelper: Add phone › older iPhone › helper installs it
// on the iPhone Xcode finds, Pinky having found none, and goes back to the
// steps once done; without Xcode it says how. iPhone Mirroring is not
// there yet, and says so.
func TestAddOlderIPhoneHelper(t *testing.T) {
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.OnePhone, Speed: 50})
	ta.sync()
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	ta.add.step, ta.add.old = "iphone", true
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if err := tt.Click(locales.AddPhoneOlderMirroring()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, locales.AddPhoneOlderMirrorNotYet()) {
		t.Errorf("iPhone Mirroring: %q", tt.Texts())
	}
	if err := tt.Click(locales.AddPhoneOlderHelper()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasText(tt, locales.AddPhoneOlderHelperHow()) || !hasText(tt, locales.CheckHelperHow()) || hasText(tt, locales.HelperInstall()) {
		t.Errorf("without Xcode: %q", tt.Texts())
	}

	ta.api.SetHelperInstall(true)
	tt.Frame()
	if hasText(tt, locales.AddPhoneOlderHelperHow()) || !hasText(tt, locales.HelperInstallNote()) {
		t.Errorf("with Xcode: %q", tt.Texts())
	}
	if err := tt.Click(locales.HelperInstall()); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the install to end", func() bool { return len(ta.helper.runs) == 0 })
	if ta.add.old || ta.add.oldPick != "" {
		t.Errorf("still on the older iPhone's page: old %v, pick %q", ta.add.old, ta.add.oldPick)
	}
	if runs := ta.api.HelperRuns(); len(runs) != 1 || runs[0].DeviceID != "" {
		t.Errorf("installs %+v", runs)
	}
	if !slices.ContainsFunc(ta.toasts, func(s string) bool { return strings.Contains(s, locales.HelperThisIPhone()) }) {
		t.Errorf("toasts %q", ta.toasts)
	}
}

// TestIPhoneHelperInstallScreenshots draws installing the helper from the
// window into $PINKY_SCREENSHOTS, in each of the client's languages: the
// page of an iPhone without it, the install under way, a failure, the
// reminder, the reminder asking which team, the checkup, and Add phone's
// older iPhone:
//
//	PINKY_SCREENSHOTS=dir go test ./ui -run TestIPhoneHelperInstallScreenshots
func TestIPhoneHelperInstallScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	dark := os.Getenv("PINKY_SCREENSHOTS_DARK") != ""
	ta, id := installApp(t)
	shoot := func(name, lang string, size ...int) {
		t.Helper()
		shootWindow(t, ta, filepath.Join(dir, name+"-"+lang+".png"), lang, dark, size...)
	}
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		ta.helper.runs = nil
		ta.page, ta.chk.showing = pagePhones, ""
		ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper})
		ta.sync()
		ta.selectPhone(id)
		shoot("helper-install", lang)

		ta.helper.runs = map[string]*helperRun{id: {running: true, step: driver.HelperUnlock, cancel: func() {}}}
		shoot("helper-installing", lang)
		ta.helper.runs[id] = &helperRun{teams: helperTeams}
		shoot("helper-teams", lang)
		// More teams than the smallest window has room for, one wider.
		ta.helper.runs[id] = &helperRun{teams: append(exampleTeams(8), wideTeam)}
		shoot("helper-teams-many-small", lang, 960, 620)
		name := ta.iphoneView(t).Info.Name
		text, detail, log := helperErrorWords(&driver.HelperInstallError{Fix: driver.HelperFixPasscode,
			Err: errors.New("the iPhone asked for its passcode to run automation, and nobody entered it"), Log: "/Users/sam/Library/Application Support/Pinky/WebDriverAgent/helper-install.log"}, name)
		ta.helper.runs[id] = &helperRun{err: text, detail: detail, log: log}
		shoot("helper-failed", lang)

		ta.helper.runs = nil
		exp := helperDate()
		clockAt(t, exp.Add(-30*time.Hour))
		ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
		ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
			{ID: driver.CheckDeveloperMode, Status: driver.CheckPass},
			{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper},
			{ID: driver.CheckInput, Status: driver.CheckPass},
		}})
		ta.sync()
		shoot("helper-again", lang)
		// The reminder asks which team, in the default and the smallest window.
		ta.helper.runs = map[string]*helperRun{id: {teams: append(slices.Clone(helperTeams), driver.HelperTeam{ID: "KLMNO12345", Name: "Shenzhen Example Technology Co., Ltd."})}}
		shoot("helper-again-teams", lang)
		shoot("helper-again-teams-small", lang, 960, 620)
		// More teams than it has room for there: they scroll, over Cancel.
		ta.helper.runs[id] = &helperRun{teams: exampleTeams(9)}
		shoot("helper-again-teams-many-small", lang, 960, 620)
		ta.helper.runs = nil
		tt := mg.NewTester(ta.view, 1280, 820)
		ta.openCheckup(ta.iphoneView(t))
		ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
		shoot("helper-again-checkup", lang)
		// Its teams, below the fold of the smallest window.
		ta.helper.runs = map[string]*helperRun{id: {teams: append(exampleTeams(3), wideTeam)}}
		shootScrolled(t, ta, filepath.Join(dir, "helper-again-checkup-teams-small-"+lang+".png"), lang, dark, 2000, 960, 620)
		ta.helper.runs = nil

		ta.chk.showing = ""
		ta.page = pageAddPhone
		ta.add.reset(ta.app)
		ta.add.known[id] = true
		ta.add.step, ta.add.old, ta.add.oldPick = "iphone", true, "helper"
		shoot("helper-add-older", lang)
	}
}
