package ui

import (
	"errors"
	"fmt"
	"image"
	"image/png"
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

// helperApp is a test app with an iPhone plugged in, and the address the
// how-to buttons open.
func helperApp(t *testing.T) (*testApp, string, *string) {
	t.Helper()
	opened := new(string)
	saved := openURL
	openURL = func(_ *mg.Context, u string, done func(error)) { *opened = u; done(nil) }
	t.Cleanup(func() { openURL = saved })
	ta := newTestAppWith(t, fakeapi.Options{Scenario: fakeapi.OnePhone, Speed: 50, HoldSetup: true})
	ta.api.ConnectIPhone()
	ta.waitFor(t, "the iPhone", func() bool { return len(ta.devices) > 1 })
	return ta, ta.iphoneView(t).Info.ID, opened
}

// helperDate is when the helper's signature runs out in the tests of what
// the views say about it: Thursday, November 12 at 3:14 PM by the clocks
// where the tests run (wallTime), a date of ordinary length. The tests set
// the time they pretend to run at before or after it (clockAt), so the
// words they check are the same whenever and wherever they run.
func helperDate() time.Time { return wallTime(2026, time.November, 12, 15, 14) }

// TestIPhoneHelperReminder: two days before the helper's signature runs
// out, an iPhone's page says when the helper stops working, with how to
// install it again, and so does its checkup. Days before, neither
// reminds; the checkup notes the day.
func TestIPhoneHelperReminder(t *testing.T) {
	exp := helperDate()
	now := exp.Add(-30 * time.Hour)
	clockAt(t, now)
	ta, id, opened := helperApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
		{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper, Detail: "WebDriverAgent answers; its provisioning profile runs out soon"},
		{ID: driver.CheckScreen, Status: driver.CheckPass},
	}})
	ta.sync()
	d := ta.iphoneView(t)
	soon := locales.CheckHelperSoon(d.Info.Name, exp.Local())
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, soon) {
		t.Fatalf("the phone's page lacks the reminder %q: %q", soon, tt.Texts())
	}
	if err := tt.Click(locales.CheckHelperHow()); err != nil || *opened != helperAgainDocs {
		t.Fatalf("how to install it again: %v, opened %q", err, *opened)
	}
	ta.openCheckup(d)
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
	if !hasText(tt, soon) || !hasText(tt, locales.CheckHelperHow()) {
		t.Errorf("the checkup lacks the reminder: %q", tt.Texts())
	}

	// Five days left: no reminder.
	exp = now.Add(5 * 24 * time.Hour)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.api.SetCheckup(id, driver.Checkup{Checks: []driver.Check{{ID: driver.CheckHelper, Status: driver.CheckPass}}})
	ta.sync()
	ta.chk.showing = ""
	tt.Frame()
	tt.Frame()
	if hasText(tt, locales.CheckHelperSoon(d.Info.Name, exp.Local())) || hasText(tt, locales.CheckHelperHow()) {
		t.Errorf("a reminder five days before: %q", tt.Texts())
	}
	ta.openCheckup(ta.iphoneView(t))
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
	if !hasText(tt, locales.CheckHelperPass()) || !hasText(tt, locales.CheckHelperUntil(exp.Local())) {
		t.Errorf("the checkup lacks the day it runs out: %q", tt.Texts())
	}
}

// reminderForm is a form the helper's reminder takes above the phone: its
// words and its buttons, on a Mac with Xcode (here) or not, after an
// install from the window that failed or asked which team (run).
type reminderForm struct {
	what   string
	here   bool
	run    *helperRun
	words  string
	labels []string
	cancel bool // it offers Cancel after its buttons too
	dated  bool // the words tell when the helper stops working
}

// reminderForms are the forms the reminder takes for the phone named name
// whose helper stops working at exp: how to install it again, installing
// it again from the window, each reason an install from the window fails
// for, and the teams to choose from, with Cancel: two, and more than a
// small window has room for.
func reminderForms(name string, exp time.Time) []reminderForm {
	again := []string{locales.HelperInstallAgain()}
	forms := []reminderForm{
		{what: "how to install it", words: locales.CheckHelperSoon(name, exp), labels: []string{locales.CheckHelperHow()}, dated: true},
		{what: "install again", here: true, words: locales.CheckHelperSoonHere(name, exp), labels: again, dated: true},
	}
	failed := []error{gateway.ErrPhoneInUse, errors.New("something else")}
	for _, fix := range []string{driver.HelperFixXcode, driver.HelperFixPhone, driver.HelperFixDeveloperMode, driver.HelperFixAppleID, driver.HelperFixOtherTeam,
		driver.HelperFixSigning, driver.HelperFixTrust, driver.HelperFixPasscode, driver.HelperFixUnlock, driver.HelperFixDownload} {
		failed = append(failed, &driver.HelperInstallError{Fix: fix, Err: errors.New(fix)})
	}
	for _, err := range failed {
		text, _, _ := helperErrorWords(err, name)
		forms = append(forms, reminderForm{what: "failed: " + err.Error(), here: true, run: &helperRun{err: text}, words: text, labels: again})
	}
	for _, n := range []int{2, 9} {
		teams := exampleTeams(n)
		f := reminderForm{what: fmt.Sprintf("%d teams", n), here: true, run: &helperRun{teams: teams}, words: locales.HelperChooseTeam(), cancel: true}
		for _, team := range teams {
			f.labels = append(f.labels, helperTeamLabel(team))
		}
		forms = append(forms, f)
	}
	return forms
}

// showReminder puts the reminder in the form f on the iPhone id's page.
func showReminder(ta *testApp, id string, f reminderForm) {
	ta.api.SetHelperInstall(f.here)
	ta.helper.runs = nil
	if f.run != nil {
		ta.helper.runs = map[string]*helperRun{id: f.run}
	}
}

// reminderFits checks that the reminder in the form f has its own room
// above the phone d in a window of w × h, and returns its box: it is
// within the stage with its buttons within it, and Cancel whole and in
// view where it offers that, the phone below it is no lower than its
// least height, and the phone's keys are within the stage too.
func reminderFits(t *testing.T, tt *mg.Tester, what string, w, h float32, d gateway.DeviceView, f reminderForm) mg.Rect {
	t.Helper()
	const eps = 0.5
	box, ok := tt.Find(f.words) // the reminder's box, labeled with its words
	phone, okPhone := tt.Find(locales.PhoneScreenLabel(d.Info.Name))
	home, okHome := tt.Find(locales.PhoneKeyHome())
	if !ok || box.H == 0 || !okPhone || !okHome {
		t.Fatalf("%s: the reminder %v (shown %v), the phone %v (%v), Home %v (%v): %q", what, box, ok, phone, okPhone, home, okHome, tt.Texts())
	}
	// The stage: under the header, between the sidebar and the right
	// panel, within its padding.
	stage := mg.Rect{X: sidebarWidth + stagePad, Y: headerHeight + stagePad, W: w - sidebarWidth - rightPanelWidth - 2*stagePad, H: h - headerHeight - 2*stagePad}
	// The dock of the keys, around Home: its padding and its border.
	dockTop, dockBottom := home.Y-sp1-1, home.Y+home.H+sp1+1
	switch {
	case !rectWithin(box, stage, eps):
		t.Errorf("%s: the reminder %v is not within the stage %v", what, box, stage)
	case box.Y+box.H+stageGap > phone.Y+eps:
		t.Errorf("%s: the reminder %v runs into the phone %v", what, box, phone)
	case phone.H < phoneFloor-eps:
		t.Errorf("%s: the reminder %v leaves the phone %v less than its least height", what, box, phone)
	case phone.Y+phone.H+stageGap > dockTop+eps:
		t.Errorf("%s: the phone %v runs into its keys, from %.1f", what, phone, dockTop)
	case dockBottom > stage.Y+stage.H+eps:
		t.Errorf("%s: the keys, down to %.1f, run off the stage %v, under the reminder %v and the phone %v", what, dockBottom, stage, box, phone)
	}
	// The buttons, within the reminder's border and padding. Those that
	// scrolled out of view have no area.
	inner := mg.Rect{X: box.X + reminderBorder + reminderPadX, Y: box.Y + reminderBorder + reminderPadY,
		W: box.W - 2*reminderBorder - reminderPadX - reminderPadEnd, H: box.H - reminderChrome}
	for i, label := range f.labels {
		b, ok := tt.Find(label)
		switch {
		case !ok:
			t.Errorf("%s: no button %q: %q", what, label, tt.Texts())
		case i == 0 && b.H < smallButtonHeight-eps:
			t.Errorf("%s: the first button %q %v is cut", what, label, b)
		case b.W > 0 && b.H > 0 && !rectWithin(b, inner, eps):
			t.Errorf("%s: the button %q %v spills out of the reminder %v", what, label, b, box)
		}
	}
	if f.cancel {
		if b, ok := tt.Find(locales.HelperCancel()); !ok || b.H < smallButtonHeight-eps || !rectWithin(b, inner, eps) {
			t.Errorf("%s: Cancel %v (shown %v) is not whole within the reminder %v", what, b, ok, box)
		}
	}
	return box
}

// rectWithin reports whether r lies within in, give or take eps.
func rectWithin(r, in mg.Rect, eps float32) bool {
	return r.X >= in.X-eps && r.Y >= in.Y-eps && r.X+r.W <= in.X+in.W+eps && r.Y+r.H <= in.Y+in.H+eps
}

// wordsCut reports whether the last frame shows words cut short, with all
// of them in a tooltip, which describes them to assistive technology.
func wordsCut(tt *mg.Tester, words string) bool {
	for _, a := range tt.AccessAll() {
		if a.Description == words {
			return true
		}
	}
	return false
}

// widestDate returns the time of a year whose date makes words the widest
// in the language lang, as this system's fonts draw them: its longest month
// and day of the week with its widest digits, then the widest of a few
// times of that day, as the day and the time add up.
func widestDate(t *testing.T, lang string, words func(when time.Time) string) time.Time {
	t.Helper()
	var widest time.Time
	measure := false
	mt := mg.NewTester(func(c *mg.Context) {
		if !measure {
			return
		}
		measure = false
		var most float32
		try := func(when time.Time) {
			if w, _ := c.MeasureText(0, reminderSpan(words(when))); w > most {
				most, widest = w, when
			}
		}
		for day := wallTime(2027, time.January, 1, 12, 58); day.Year() == 2027; day = day.AddDate(0, 0, 1) {
			try(day)
		}
		day := widest
		for _, clock := range [][2]int{{10, 44}, {20, 8}} {
			try(wallTime(2027, day.Month(), day.Day(), clock[0], clock[1]))
		}
	}, 200, 100)
	inLanguage(t, lang, mt)
	measure = true
	mt.Frame()
	if widest.IsZero() {
		t.Fatalf("%s: no date measured", lang)
	}
	return widest
}

// TestIPhoneHelperReminderLayout: the reminder takes its own room above
// the phone, and the phone and its keys shrink for it rather than run off
// the window, the phone no lower than its least height: where the stage
// is narrow the reminder's buttons go under its words, and words that
// still take more room than there is are cut. In each of the client's
// languages, in the default window and in one smaller than the smallest,
// for every form the reminder takes, with a date of ordinary length and
// the widest of a year, and for a phone with a short name and one with a
// long name.
func TestIPhoneHelperReminderLayout(t *testing.T) {
	clockAt(t, helperDate().Add(-20*time.Hour))
	ta, id, _ := helperApp(t)
	ta.selectPhone(id)
	const longName = "Alexandra-Marie’s Work iPhone 17 Pro Max"
	sizes := [][2]int{{1280, 820}, {900, 600}}
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		// A window of each size, drawn again for each form.
		var windows []*mg.Tester
		for _, size := range sizes {
			tt := mg.NewTester(ta.view, size[0], size[1])
			tt.SetLocale(lang)
			windows = append(windows, tt)
		}
		dates := []time.Time{helperDate(), widestDate(t, lang, func(when time.Time) string { return locales.CheckHelperSoon(longName, when) })}
		for _, name := range []string{"", longName} {
			for i, exp := range dates {
				clockAt(t, exp.Add(-20*time.Hour))
				ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
				ta.sync()
				if name != "" {
					ta.device(id).Info.Name = name // as its owner named it
				}
				d := ta.iphoneView(t)
				for _, f := range reminderForms(d.Info.Name, exp) {
					switch {
					case !f.dated && i > 0:
						continue // the same words whatever the date
					case name != "" && !strings.Contains(f.words, name):
						continue // and whatever the phone's name
					}
					showReminder(ta, id, f)
					for j, tt := range windows {
						tt.Frame()
						tt.Frame()
						what := fmt.Sprintf("%s %v, %s, %q", lang, sizes[j], f.what, f.words)
						box := reminderFits(t, tt, what, float32(sizes[j][0]), float32(sizes[j][1]), d, f)
						// The words of every day show whole, in the
						// narrow window too.
						if f.dated && i == 0 && name == "" && wordsCut(tt, f.words) {
							t.Errorf("%s: the reminder %v cuts its words", what, box)
						}
					}
				}
			}
		}
	}
}

// TestIPhoneHelperReminderCut: words that take more room than a small
// window has above the phone are cut to the lines that fit, and the
// pointer resting on them shows them whole; the button under them stays
// in reach. More teams than there is room for scroll under the words, in
// a list that cuts no focus ring and whose scroll bar takes no press on a
// team's end, with Cancel in view under it.
func TestIPhoneHelperReminderCut(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(-20*time.Hour))
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	ta.selectPhone(id)
	const w, h = 900, 600

	// A reason longer than any Pinky has, as a translation may get.
	words := strings.TrimSpace(strings.Repeat("Xcode couldn't install the helper while the phone was locked. ", 12))
	f := reminderForm{here: true, run: &helperRun{err: words}, words: words, labels: []string{locales.HelperInstallAgain()}}
	showReminder(ta, id, f)
	tt := mg.NewTester(ta.view, w, h)
	tt.Frame()
	tt.Frame()
	box := reminderFits(t, tt, "a long reason", w, h, d, f)
	if !wordsCut(tt, words) {
		t.Fatalf("the reminder %v shows all of its words", box)
	}
	shown := countText(tt, words)
	tt.Move(box.X+box.W/2, box.Y+reminderBorder+reminderPadY+4)
	tt.Advance(time.Second)
	if got := countText(tt, words); got != shown+1 {
		t.Errorf("the words show %d times with the pointer on them, %d before: no tooltip", got, shown)
	}
	ta.api.HoldHelper(true)
	if err := tt.Click(locales.HelperInstallAgain()); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the install", func() bool { return len(ta.api.HelperRuns()) == 1 })
	ta.helper.runs[id].cancel()
	ta.waitFor(t, "the install to stop", func() bool { return ta.helper.runs[id] == nil })
	ta.api.HoldHelper(false)

	// Nine teams: the last is out of view until the buttons scroll.
	forms := reminderForms(d.Info.Name, exp)
	f = forms[len(forms)-1]
	showReminder(ta, id, f)
	tt = mg.NewTester(ta.view, w, h)
	tt.Frame()
	tt.Frame()
	box = reminderFits(t, tt, f.what, w, h, d, f)
	first, _ := tt.Find(f.labels[0])
	last := f.labels[len(f.labels)-1]
	if b, _ := tt.Find(last); b.H != 0 {
		t.Fatalf("the last team %v shows without scrolling, in the reminder %v", b, box)
	}

	// The list cuts no focus ring: the first team's, at its top, shows
	// whole, inside the button.
	for i := 0; i < 80 && !tt.Focused(f.labels[0]); i++ {
		tt.Key(0, mg.KeyTab)
	}
	// ring reports whether the frame shows the focus ring's color within
	// three points of x, y, going dx, dy.
	ring := func(x, y float32, dx, dy int) bool {
		for i := range 4 {
			c, want := tt.Image().RGBAAt(int(x)+i*dx, int(y)+i*dy), lightPalette.brand
			if d := int(c.R) - int(want.R); d > -32 && d < 32 && int(c.G) < int(want.G)+32 && int(c.B) < int(want.B)+32 {
				return true
			}
		}
		return false
	}
	if !tt.Focused(f.labels[0]) || !ring(first.X, first.Y+first.H/2, 1, 0) || !ring(first.X+first.W/2, first.Y, 0, 1) || !ring(first.X+first.W-1, first.Y+first.H/2, -1, 0) {
		t.Errorf("the first team %v (focused %v) shows no focus ring along its start, its top or its end", first, tt.Focused(f.labels[0]))
	}

	// Its scroll bar keeps clear of them: a press on the end of a team as
	// wide as the list chooses the team, with the pointer on the list,
	// which shows the bar. The first such team may be cut by the end of the
	// list, over Cancel's row: scrolled into view whole first.
	wide, _ := tt.Find(f.labels[2])
	if wide.H > 0 && wide.H < smallButtonHeight {
		tt.Scroll(first.X+first.W/2, first.Y+first.H/2, 0, smallButtonHeight-wide.H)
		wide, _ = tt.Find(f.labels[2])
	}
	if wide.H < smallButtonHeight-0.5 || wide.X+wide.W < box.X+box.W-reminderBorder-reminderPadEnd-0.5 {
		t.Fatalf("the third team %v is not whole and as wide as the list, in the reminder %v", wide, box)
	}
	x, y := wide.X+wide.W-2, wide.Y+wide.H/2
	tt.Move(x, y)
	ta.api.HoldHelper(true)
	tt.ClickAt(x, y)
	if r := ta.helper.runs[id]; r == nil || !r.running {
		after, _ := tt.Find(f.labels[2])
		t.Fatalf("a press on the end of the team %v did not choose it: the team is at %v now", wide, after)
	}
	ta.waitFor(t, "the install for the wide team", func() bool {
		runs := ta.api.HelperRuns()
		return len(runs) == 2 && runs[1].Team == f.run.teams[2].ID
	})
	ta.helper.runs[id].cancel()
	ta.waitFor(t, "the install to stop", func() bool { return ta.helper.runs[id] == nil })
	ta.api.HoldHelper(false)

	// Scrolled to its end, the list shows the last team, which installs.
	showReminder(ta, id, f)
	tt = mg.NewTester(ta.view, w, h)
	tt.Frame()
	tt.Frame()
	first, _ = tt.Find(f.labels[0])
	tt.Scroll(first.X+first.W/2, first.Y+first.H/2, 0, 2000)
	reminderFits(t, tt, f.what+", scrolled", w, h, d, reminderForm{words: f.words, labels: []string{last}, cancel: true})
	// The keyboard's focus brings a team into view up to the list's edge,
	// where its ring shows too.
	for i := 0; i < 80 && !tt.Focused(last); i++ {
		tt.Key(0, mg.KeyTab)
	}
	if b, _ := tt.Find(last); !tt.Focused(last) || b.H < smallButtonHeight-0.5 || !ring(b.X+b.W/2, b.Y+b.H-1, 0, -1) {
		t.Errorf("the last team %v (focused %v) shows no focus ring along its bottom", b, tt.Focused(last))
	}
	// Held, so that the install has not put the phone back by the time
	// the test ends.
	ta.api.HoldHelper(true)
	if err := tt.Click(last); err != nil {
		t.Fatal(err)
	}
	team := f.run.teams[len(f.run.teams)-1].ID
	ta.waitFor(t, "the install for the last team", func() bool {
		runs := ta.api.HelperRuns()
		return len(runs) == 3 && runs[2].Team == team
	})
	ta.helper.runs[id].cancel()
	ta.waitFor(t, "the install to stop", func() bool { return ta.helper.runs[id] == nil })
	ta.api.HoldHelper(false)
}

// TestIPhoneHelperReminderCancel: the reminder's choice of a team can be
// left, as the needs-setup page's can. Cancel, after two teams and under
// nine that scroll, in the default window and in one smaller than the
// smallest, forgets the teams without installing anything, and the
// reminder offers Install again.
func TestIPhoneHelperReminderCancel(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(-20*time.Hour))
	ta, id := installApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	ta.selectPhone(id)
	soon := locales.CheckHelperSoonHere(d.Info.Name, exp.Local())
	for _, n := range []int{2, 9} {
		for _, size := range [][2]int{{1280, 820}, {900, 600}} {
			what := fmt.Sprintf("%d teams in %v", n, size)
			showReminder(ta, id, reminderForm{here: true, run: &helperRun{teams: exampleTeams(n)}})
			tt := mg.NewTester(ta.view, size[0], size[1])
			tt.Frame()
			tt.Frame()
			if !hasText(tt, locales.HelperChooseTeam()) {
				t.Fatalf("%s: the reminder does not ask which team: %q", what, tt.Texts())
			}
			if err := tt.Click(locales.HelperCancel()); err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			tt.Frame()
			if r := ta.helper.runs[id]; r != nil || hasText(tt, locales.HelperChooseTeam()) || !hasText(tt, soon) || !hasText(tt, locales.HelperInstallAgain()) {
				t.Errorf("%s: after Cancel, the run %+v and the words %q", what, r, tt.Texts())
			}
		}
	}
	if runs := ta.api.HelperRuns(); len(runs) != 0 {
		t.Errorf("Cancel installed the helper: %+v", runs)
	}
}

// TestLayoutReminder: whatever its words and buttons, with Cancel after
// them or not, the reminder takes no more than the room it is given, down
// to a line of its words with its buttons, cuts its words no shorter than
// that room asks, and lays its buttons out within its width: beside the
// words where that is no higher, under them where it is.
func TestLayoutReminder(t *testing.T) {
	const eps = 0.01
	short := "The helper stops working soon."
	long := strings.TrimSpace(strings.Repeat("Xcode couldn't install the helper while the phone was locked. ", 6))
	texts := []string{short, long}
	for _, lang := range testLanguages() {
		texts = append(texts, locales.In(lang).CheckHelperSoon("iPhone 16", helperDate()))
	}
	one := []string{locales.CheckHelperHow()}
	var teams []string
	for _, team := range append(slices.Clone(helperTeams), driver.HelperTeam{ID: "KLMNO12345", Name: "Shenzhen Example Technology Co., Ltd."},
		driver.HelperTeam{ID: "PQRST12345"}, driver.HelperTeam{ID: "UVWXY12345"}, driver.HelperTeam{ID: "Z"}) {
		teams = append(teams, helperTeamLabel(team))
	}
	// No Cancel, and Cancel in each language.
	cancels := []string{""}
	for _, lang := range testLanguages() {
		cancels = append(cancels, locales.In(lang).HelperCancel())
	}
	check := func(c *mg.Context, text string, labels []string, cancel string, width, room float32) {
		l := layoutReminder(c, text, labels, cancel, width, room)
		what := fmt.Sprintf("%q with %d buttons and %q, %v wide in %v: %+v", text, len(labels), cancel, width, room, l)
		inner := reminderInner(width)
		_, all := c.MeasureText(l.textW, reminderSpan(text))
		words := all
		if l.lines > 0 {
			words = float32(l.lines) * l.lineH
			if words >= all {
				t.Errorf("%s: cut to no fewer lines than the words have, %v high", what, all)
			}
		}
		// What is under the words, and whether the buttons, and Cancel
		// after them, have room beside them.
		var rest float32 = reminderChrome
		beside, fits := inner, true
		for _, b := range l.buttons {
			beside -= reminderGap + b.w
			fits = fits && !b.cut
		}
		var cancelW float32
		if cancel != "" {
			cancelW = quietButtonWidth(c, cancel)
			beside -= reminderGap + cancelW
			if l.cancel.w != cancelW || l.cancel.cut {
				t.Errorf("%s: Cancel is not %v wide", what, cancelW)
			}
		} else if l.cancel != (reminderButton{}) {
			t.Errorf("%s: Cancel where there is none", what)
		}
		fits = fits && beside >= 1
		if !l.below {
			if got := max(words, smallButtonHeight) + reminderChrome; got < l.height-eps || got > l.height+eps {
				t.Errorf("%s: words %v high beside the buttons", what, words)
			}
			if !fits || l.textW < beside-eps || l.textW > beside+eps {
				t.Errorf("%s: the buttons leave the words %v beside them", what, beside)
			}
			for _, b := range append(slices.Clone(l.buttons), l.cancel) {
				if b.row != 0 {
					t.Errorf("%s: a button beside the words is not in their row", what)
				}
			}
		} else {
			rest += reminderStack + l.buttonsH
			if got := words + rest; got < l.height-eps || got > l.height+eps {
				t.Errorf("%s: words %v high over the buttons", what, words)
			}
			if l.textW != inner {
				t.Errorf("%s: the words over the buttons are not %v wide", what, inner)
			}
			rows, across := 0, float32(0)
			for i, b := range l.buttons {
				switch {
				case i == 0:
					across = b.w
				case b.row == rows:
					across += reminderStack + b.w
				case b.row == rows+1:
					rows, across = rows+1, b.w
				default:
					t.Errorf("%s: button %d is in row %d after row %d", what, i, b.row, rows)
				}
				if across > inner+eps {
					t.Errorf("%s: row %d of the buttons is %v wide, of %v", what, rows, across, inner)
				}
			}
			// The rows of the buttons and of Cancel after them: at the end
			// of the last where it fits there, else in a row of its own,
			// which it has under buttons that scroll too.
			flow, low := rows+1, float32(smallButtonHeight)
			if cancel != "" {
				shares := across+reminderStack+cancelW <= inner
				if !shares {
					flow++
				}
				row := rows + 1
				if shares && !l.scrolls {
					row = rows
				}
				if l.cancel.row != row {
					t.Errorf("%s: Cancel in row %d, not %d, after buttons in %d", what, l.cancel.row, row, rows+1)
				}
				low += reminderPitch
			}
			whole := float32(flow)*smallButtonHeight + float32(flow-1)*reminderStack
			if l.scrolls && (l.buttonsH < low || l.buttonsH >= whole) || !l.scrolls && l.buttonsH != whole {
				t.Errorf("%s: the buttons take %v, %v without scrolling", what, l.buttonsH, whole)
			}
		}
		if len(l.buttons) != len(labels) || l.textW < 1 {
			t.Errorf("%s: %d buttons", what, len(l.buttons))
		}
		// The least it can be: a line of the words beside the buttons, or
		// over a row of them where they have no room beside the words, and
		// over Cancel's own row too where it is not in that one.
		least := max(l.lineH, smallButtonHeight) + reminderChrome
		if !fits {
			least = l.lineH + reminderStack + smallButtonHeight + reminderChrome
			if cancel != "" && l.cancel.row > 0 {
				least += reminderPitch
			}
		}
		if l.height > max(room, least)+eps {
			t.Errorf("%s: more than the room, and than the least %v", what, least)
		}
		if l.lines > 0 && float32(l.lines+1)*l.lineH+rest <= room {
			t.Errorf("%s: another line of the words would fit", what)
		}
	}
	ran := false
	mg.NewTester(func(c *mg.Context) {
		applyTheme(c)
		if ran {
			return
		}
		ran = true
		for _, text := range texts {
			for _, labels := range [][]string{nil, one, teams[:2], teams} {
				for _, cancel := range cancels {
					if cancel != "" && labels == nil {
						continue // Cancel follows teams
					}
					for _, width := range []float32{180, 270, 320, 380, 520} {
						for _, room := range []float32{-20, 30, 64, 90, 100, 110, 120, 150, 194, 214, 300, 1000} {
							check(c, text, labels, cancel, width, room)
						}
					}
				}
			}
		}
		// A few words and a button, with room: side by side, as before.
		if l := layoutReminder(c, short, one, "", 520, 1000); l.below || l.lines != 0 {
			t.Errorf("a few words and a button: %+v", l)
		}
		// One line of words is not cut, however little room there is.
		if l := layoutReminder(c, short, nil, "", 520, 10); l.lines != 0 {
			t.Errorf("a few words with no room: %+v", l)
		}
		// Many words in a narrow stage: the button under them, all of
		// them while there is room, cut when there is not; beside them,
		// cut, where there is no room for a line of them over the button.
		if l := layoutReminder(c, long, one, "", 320, 1000); !l.below || l.lines != 0 {
			t.Errorf("many words in a narrow stage: %+v", l)
		}
		if l := layoutReminder(c, long, one, "", 320, 120); !l.below || l.lines == 0 {
			t.Errorf("many words in a narrow stage with little room: %+v", l)
		}
		if l := layoutReminder(c, long, one, "", 320, 64); l.below || l.lines == 0 || l.height > 64 {
			t.Errorf("many words in a narrow stage with room for a button only: %+v", l)
		}
		// More teams than there is room for scroll; a team wider than the
		// reminder is cut.
		if l := layoutReminder(c, short, teams, "", 320, 150); !l.below || !l.scrolls {
			t.Errorf("many teams with little room: %+v", l)
		}
		if l := layoutReminder(c, short, teams, "", 320, 1000); !l.below || l.scrolls || !l.buttons[2].cut {
			t.Errorf("many teams with room: %+v", l)
		}
		// Cancel after the teams: beside them with the words, at the end of
		// the last row under the words where it fits there, in a row of its
		// own where it does not, and under teams that scroll, in view. Two
		// rows with Cancel in the second do not scroll, which would save
		// nothing.
		cancel := locales.In("ja").HelperCancel()
		if l := layoutReminder(c, short, teams[1:2], cancel, 520, 1000); l.below || l.cancel.row != 0 {
			t.Errorf("a team and Cancel, with room: %+v", l)
		}
		if l := layoutReminder(c, long, teams[1:2], cancel, 320, 1000); !l.below || l.scrolls || l.cancel.row != 0 {
			t.Errorf("many words over a team and Cancel: %+v", l)
		}
		if l := layoutReminder(c, short, []string{teams[0], teams[2]}, cancel, 320, 1000); !l.below || l.scrolls || l.cancel.row != l.buttons[1].row+1 {
			t.Errorf("Cancel after a team as wide as the reminder: %+v", l)
		}
		if l := layoutReminder(c, short, teams, cancel, 320, 150); !l.below || !l.scrolls || l.cancel.row != l.buttons[len(teams)-1].row+1 {
			t.Errorf("many teams and Cancel with little room: %+v", l)
		}
		if l := layoutReminder(c, short, teams[:2], cancel, 320, 100); !l.below || l.scrolls || l.cancel.row != l.buttons[1].row || l.buttons[1].row != 1 {
			t.Errorf("two rows of teams and Cancel with little room: %+v", l)
		}
	}, 200, 100)
	if !ran {
		t.Fatal("the view did not run")
	}
}

// TestIPhoneHelperReminderGivesWay: while the person is in control, the
// banner takes the reminder's place above the phone; once they hand the
// phone back, the reminder is back. Neither covers the phone.
func TestIPhoneHelperReminderGivesWay(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(-20*time.Hour))
	ta, id, _ := helperApp(t)
	ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	form := reminderForms(d.Info.Name, exp)[0]
	soon := form.words
	img := image.NewRGBA(image.Rect(0, 0, 1170, 2532))
	stillMirror(ta, id, img)
	ta.selectPhone(id)
	const w, h = 1280, 820
	tt := mg.NewTester(ta.view, w, h)
	tt.Frame()
	reminderShows := func(what string) {
		t.Helper()
		if findPhone(t, tt, d.Info.Name).banner {
			t.Fatalf("%s: the banner shows with the reminder: %q", what, tt.Texts())
		}
		reminderFits(t, tt, what, w, h, d, form)
	}
	reminderShows("before")

	ta.takeOver(id)
	ta.waitFor(t, "the person in control", func() bool { return ta.iphoneView(t).Status == gateway.DeviceInControl })
	tt.Frame()
	tt.Frame()
	b := findPhone(t, tt, d.Info.Name)
	if hasText(tt, soon) {
		t.Fatalf("the reminder shows with the banner: %q", tt.Texts())
	}
	checkBanner(t, b, w, h)

	ta.handBack(id)
	ta.waitFor(t, "the phone handed back", func() bool { return ta.iphoneView(t).Status != gateway.DeviceInControl })
	tt.Frame()
	tt.Frame()
	reminderShows("after Hand back")
}

// TestIPhoneHelperRanOut: once the helper's signature ran out, the iPhone
// needs setup, and its page, its checkup and Add phone say why in words,
// with how to install it again, not the driver's English or the steps of
// preparing it.
func TestIPhoneHelperRanOut(t *testing.T) {
	exp := helperDate()
	clockAt(t, exp.Add(3*time.Hour))
	ta, id, opened := helperApp(t)
	const detail = "The provisioning profile of Pinky on the iPhone ran out"
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, Detail: detail, HelperExpires: exp})
	ta.sync()
	d := ta.iphoneView(t)
	ranOut := locales.CheckHelperExpired(d.Info.Name, exp.Local())
	ta.selectPhone(id)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.Frame()
	if !hasText(tt, ranOut) || hasText(tt, detail) || hasText(tt, locales.PhoneSetUpIPhone()) {
		t.Fatalf("the phone's page: %q; want %q, without the driver's words or the steps", tt.Texts(), ranOut)
	}
	if err := tt.Click(locales.CheckHelperHow()); err != nil || *opened != helperAgainDocs {
		t.Fatalf("how to install it again: %v, opened %q", err, *opened)
	}
	// Listed, not opened: it needs setup, in the sidebar too.
	if _, ok := tt.Find(locales.SidebarPhoneRow(d.Info.Name, locales.GwStatusNeedsSetup())); !ok ||
		d.Status != gateway.DeviceNeedsSetup || hasText(tt, locales.PhoneOffline(d.Info.Name)) {
		t.Fatalf("listed as %q: %q", d.Status, tt.Texts())
	}

	*opened = ""
	ta.openCheckup(d)
	ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
	for _, want := range []string{locales.CheckHelperTitle(), ranOut} {
		if !hasText(tt, want) {
			t.Errorf("the checkup lacks %q: %q", want, tt.Texts())
		}
	}
	if err := tt.Click(locales.CheckHelperHow()); err != nil || *opened != helperAgainDocs {
		t.Errorf("the checkup's how to install it again: %v, opened %q", err, *opened)
	}

	ta.chk.showing = ""
	ta.page = pageAddPhone
	ta.add.reset(ta.app)
	ta.add.step, ta.add.focus = "iphone", id
	tt.Frame()
	tt.Frame()
	if !hasText(tt, ranOut) || hasText(tt, locales.AddPhoneIPhoneRetrying()) || !hasText(tt, locales.CheckHelperHow()) {
		t.Errorf("Add phone › iPhone: %q", tt.Texts())
	}

	// Chosen in Settings › Phones without the helper installed.
	ta.page = pagePhones
	ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, Detail: "Pinky is not installed on this iPhone."})
	ta.sync()
	ta.selectPhone(id)
	tt.Frame()
	if !hasText(tt, locales.CheckHelperMissing()) {
		t.Errorf("without the helper: %q", tt.Texts())
	}
	*opened = ""
	if err := tt.Click(locales.CheckHelperHow()); err != nil || *opened != helperDocs {
		t.Errorf("how to install it: %v, opened %q", err, *opened)
	}
}

// TestIPhoneHelperScreenshots draws an iPhone's page with the reminder,
// its checkup, and its page once the helper ran out, into
// $PINKY_SCREENSHOTS, in each of the client's languages:
//
//	PINKY_SCREENSHOTS=dir go test ./ui -run TestIPhoneHelperScreenshots
func TestIPhoneHelperScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	dark := os.Getenv("PINKY_SCREENSHOTS_DARK") != ""
	exp := helperDate()
	clockAt(t, exp.Add(-30*time.Hour))
	ta, id, _ := helperApp(t)
	shoot := func(name, lang string, size ...int) {
		t.Helper()
		shootWindow(t, ta, filepath.Join(dir, name+"-"+lang+".png"), lang, dark, size...)
	}
	for _, lang := range testLanguages() {
		inLanguage(t, lang)
		ta.api.SetIPhone(id, driver.Info{State: driver.StateOnline, HelperExpires: exp})
		ta.api.SetCheckup(id, driver.Checkup{System: "iOS 18.6", Checks: []driver.Check{
			{ID: driver.CheckDeveloperMode, Status: driver.CheckPass},
			{ID: driver.CheckDiskImage, Status: driver.CheckPass},
			{ID: driver.CheckHelper, Status: driver.CheckWarn, Fix: driver.FixInstallHelper},
			{ID: driver.CheckScreen, Status: driver.CheckPass},
			{ID: driver.CheckInput, Status: driver.CheckPass},
		}})
		ta.sync()
		ta.selectPhone(id)
		ta.chk.showing = ""
		shoot("helper-reminder", lang)
		shoot("helper-reminder-narrow", lang, 900, 600)
		tt := mg.NewTester(ta.view, 1280, 820)
		ta.openCheckup(ta.iphoneView(t))
		ta.frameUntil(t, tt, "the checkup", func() bool { r := ta.checkupOf(id); return r != nil && r.have && !r.running })
		shoot("helper-checkup", lang)
		ta.chk.showing = ""
		ta.api.SetIPhone(id, driver.Info{State: driver.StateNeedsSetup, Setup: driver.SetupInstallHelper, HelperExpires: exp.Add(-48 * time.Hour),
			Detail: "The provisioning profile of Pinky on the iPhone ran out"})
		ta.sync()
		shoot("helper-ran-out", lang)
	}
}

// shootWindow draws the client in lang, in a window of the default size or
// of the size given, at two pixels a point, and writes the picture to file.
func shootWindow(t *testing.T, ta *testApp, file, lang string, dark bool, size ...int) {
	t.Helper()
	shootScrolled(t, ta, file, lang, dark, 0, size...)
}

// shootScrolled is shootWindow with what scrolls in the middle of the stage
// scrolled down by dy first, for what is below the fold.
func shootScrolled(t *testing.T, ta *testApp, file, lang string, dark bool, dy float32, size ...int) {
	t.Helper()
	w, h := 1280, 820
	if len(size) == 2 {
		w, h = size[0], size[1]
	}
	st := mg.NewTester(ta.view, w, h)
	inLanguage(t, lang, st)
	st.SetScale(2)
	st.SetDark(dark)
	st.Frame()
	st.Frame()
	if dy != 0 {
		st.Scroll(sidebarWidth+float32(w-sidebarWidth-rightPanelWidth)/2, headerHeight+float32(h-headerHeight)/2, 0, dy)
		st.Frame()
	}
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, st.Image()); err != nil {
		t.Fatal(err)
	}
}
