package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// The checkup's words for iPhones: what the computer lacks (usbmuxd), the
// step a phone that is not online waits for (Info.Setup, which the
// gateway gives the "authorized" item as its fix), and the items of the
// iOS driver's checkup. Fixes that are steps of Add phone › iPhone get a
// link to those steps.

// helperDocs is how to install the WebDriverAgent helper, and
// helperAgainDocs how to install it again when its signature runs out.
const (
	helperDocs      = "https://github.com/DIYgod/pinky/blob/main/docs/notes/ios.md#installing-pinky-for-iphone"
	helperAgainDocs = "https://github.com/DIYgod/pinky/blob/main/docs/notes/ios.md#when-pinky-for-iphones-signature-runs-out"
)

// helperLink is how to install an iPhone's helper: again, when Pinky knows
// when its signature runs out.
func helperLink(d gateway.DeviceView) string {
	if !d.Info.HelperExpires.IsZero() {
		return helperAgainDocs
	}
	return helperDocs
}

// iphoneCheckTitle names the items only iPhones have; "" for others.
func iphoneCheckTitle(id string) string {
	switch id {
	case driver.CheckDeveloperMode:
		return locales.CheckDeveloperModeTitle()
	case driver.CheckDiskImage:
		return locales.CheckDiskImageTitle()
	case driver.CheckHelper:
		return locales.CheckHelperTitle()
	}
	return ""
}

// iphoneCheckWords puts an iPhone's item into words; ok is false for
// items the shared words say well enough.
func (a *app) iphoneCheckWords(d gateway.DeviceView, ck driver.Check) (w checkWords, ok bool) {
	pass := ck.Status == driver.CheckPass
	w = checkWords{title: checkTitle(ck.ID), detail: true}
	switch ck.ID {
	case driver.CheckHost:
		switch ck.Fix {
		case "apple_devices":
			w.text = locales.AddPhoneIPhoneStepDriverDetail()
		case "usbmuxd":
			w.text = locales.AddPhoneIPhoneStepUsbmuxdDetail()
		case "usbmuxd_stopped":
			w.text = locales.AddPhoneIPhoneUsbmuxdStopped()
		default:
			return w, false
		}
		return w, true
	case driver.CheckAuthorized:
		if pass && d.Info.Virtual {
			// A Simulator trusts every app on the Mac; what it waits for
			// is the helper Pinky builds for it.
			w.title, w.text, w.detail = locales.CheckSimHelperTitle(), locales.CheckSimHelperPass(), false
			return w, true
		}
		if pass {
			// An iPhone trusts the computer; it has no USB debugging.
			w.title, w.text, w.detail = locales.AddPhoneIPhoneStepTrust(), locales.CheckTrustedPass(), false
			return w, true
		}
		if ck.Status == driver.CheckSkip {
			return w, false
		}
		w.title, w.text, w.note = setupWords(d, ck.Fix, a.canInstallHelper())
		if w.text == "" {
			return w, false
		}
		// Steps in the person's words say it all, except why a mount
		// failed or the Simulator helper's build did.
		w.detail = ck.Fix == driver.SetupDiskImage || ck.Fix == driver.SetupSimulatorHelper
		if ck.Fix == driver.SetupDiskImageRefused && refusedOffersMirror(d.Info.DiskImage) {
			w.note = locales.CheckDiskImageRefusedNote()
		}
		return w, true
	case driver.CheckDeveloperMode:
		if pass {
			w.text, w.detail = locales.CheckDeveloperModePass(), false
		} else {
			w.text, w.note = locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneStepDevModeNote()
		}
		return w, true
	case driver.CheckDiskImage:
		switch {
		case pass:
			w.text = locales.CheckDiskImagePass()
			w.detail = false
		case ck.Fix == driver.SetupNoDiskImage:
			w.text = locales.CheckDiskImageNoCopy()
			w.detail = false
		default:
			w.text = locales.CheckDiskImageMissing()
		}
		return w, true
	case driver.CheckHelper:
		w.detail = false
		now := timeNow()
		text, ranOut := helperWords(d, now, a.canInstallHelper())
		exp := d.Info.HelperExpires
		switch {
		case pass:
			w.text = locales.CheckHelperPass()
			if exp.After(now) {
				w.note = locales.CheckHelperUntil(exp.Local())
			}
		case text != "" && (ck.Status == driver.CheckWarn || ranOut):
			// It stops soon, or its signature ran out: iOS won't start it
			// again, though it may still run.
			w.text = text
		case ck.Status == driver.CheckWarn:
			w.text, w.detail = locales.CheckHelperPass(), true
		default:
			w.text = locales.CheckHelperMissing()
		}
		return w, true
	case driver.CheckScreen:
		if pass || ck.Fix != driver.FixRestart {
			return w, false
		}
		// Pinky could not open the phone: it sent no live picture. The
		// driver's details say what came instead (ios.noVideoError).
		w.text = locales.CheckScreenNoLivePicture()
		return w, true
	case driver.CheckInput:
		switch {
		case pass:
			w.text, w.detail = locales.CheckInputIPhonePass(), false
		case ck.Fix == driver.FixInstallHelper:
			w.text, w.detail = locales.CheckInputIPhoneNoHelper(), false
		default:
			return w, false
		}
		return w, true
	}
	return w, false
}

// setupWords says an iPhone's setup step (Info.Setup): a title, what to do
// and a note. here: Pinky installs the helper from the window, so the
// words about it leave out the command.
func setupWords(d gateway.DeviceView, step string, here bool) (title, text, note string) {
	if d.Info.Virtual {
		switch step {
		case driver.SetupPreparing:
			return locales.CheckSimHelperTitle(), locales.CheckSimHelperBuilding(), ""
		case driver.SetupSimulatorHelper:
			return locales.CheckSimHelperTitle(), locales.CheckSimHelperFailed(), ""
		}
	}
	switch step {
	case driver.SetupTrustRecord:
		// The phone trusts the computer; the record here is the problem, and
		// Detail says how an administrator lets Apple's device service
		// replace it.
		return locales.AddPhoneIPhoneTrustRecordTitle(), d.Info.Detail, ""
	case driver.SetupTrust:
		if d.Info.Connection == "wifi" {
			// The phone asks to trust a computer only on a cable.
			return locales.AddPhoneIPhoneStepTrust(), locales.AddPhoneIPhoneTrustWiFi(d.Info.Name), ""
		}
		return locales.AddPhoneIPhoneStepTrust(), locales.AddPhoneIPhoneStepTrustDetail(), ""
	case driver.SetupConfirmWiFi:
		return locales.AddPhoneIPhoneFoundTitle(), locales.FoundDetail(), ""
	case driver.SetupDeviceService:
		return locales.LostTitle(d.Info.Name), locales.LostDetail(d.Info.Name), locales.LostManual()
	case driver.SetupDeveloperMode:
		if d.Info.Connection == "wifi" {
			return locales.CheckDeveloperModeTitle(), locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneDevModeWiFiNote(d.Info.Name)
		}
		return locales.CheckDeveloperModeTitle(), locales.AddPhoneIPhoneStepDevModeDetail(), locales.AddPhoneIPhoneStepDevModeNote()
	case driver.SetupUnlock:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStepUnlock(), ""
	case driver.SetupPreparing:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStepPreparing(), ""
	case driver.SetupNoDiskImage:
		text := locales.AddPhoneIPhoneNoImageMac()
		if guideOS != "darwin" {
			text = locales.AddPhoneIPhoneNoImageCopy()
		}
		return locales.AddPhoneIPhoneNoImageTitle(), text, locales.CheckDiskImageDownloadNote()
	case driver.SetupDiskImage:
		return locales.CheckDiskImageTitle(), locales.AddPhoneIPhoneStuck(), locales.AddPhoneIPhoneRetrying()
	case driver.SetupDiskImageRefused:
		if d.Info.DiskImage == nil {
			text := d.Info.Detail
			if text == "" {
				text = locales.AddPhoneIPhoneStuck()
			}
			return locales.AddPhoneIPhoneRefusedTitle(), text, locales.AddPhoneIPhoneRefusedRetry()
		}
		return locales.AddPhoneIPhoneRefusedTitle(), refusedWords(*d.Info.DiskImage), locales.AddPhoneIPhoneRefusedRetry()
	case driver.SetupUpdate:
		return locales.AddPhoneIPhoneTooOldTitle(d.Info.Name), locales.AddPhoneIPhoneTooOld(d.Info.OSVersion), ""
	case driver.SetupMediaInUse:
		// iOS does not mirror the screen while an app uses the camera or
		// microphone; Pinky tries again by itself (ios/mediawait.go).
		return locales.MediaInUseTitle(), locales.MediaInUseDetail(d.Info.Name), locales.MediaInUseNote()
	case driver.SetupPasscode:
		// iOS asks now and then before the helper may control the phone.
		return locales.AddPhoneIPhoneStepPasscode(), locales.AddPhoneIPhoneStepPasscodeDetail(), ""
	case driver.SetupConnection:
		// Set to CoreDevice below iOS 27 (Settings › Phones).
		return locales.ConnectionTitle(), locales.ConnectionChosenNeedsIOS27(d.Info.OSVersion), ""
	case driver.SetupInstallHelper:
		// Its signature ran out (Info.HelperExpires), or it is missing.
		if text, ranOut := helperWords(d, timeNow(), here); ranOut {
			return locales.CheckHelperTitle(), text, ""
		}
		return locales.CheckHelperTitle(), locales.CheckHelperMissing(), ""
	}
	return "", "", ""
}

// refusedWords says which developer disk image Apple's signing server
// would not sign (driver.SetupDiskImageRefused) and how to get one it
// signs, by where the image came from.
func refusedWords(img driver.DiskImage) string {
	build, version := img.Build, img.Version
	if build == "" {
		build = "–"
	}
	if version == "" {
		version = "–"
	}
	switch img.Source {
	case driver.DiskImageFromXcode:
		return locales.AddPhoneIPhoneRefusedXcode(build, version)
	case driver.DiskImageFromMirror:
		return locales.AddPhoneIPhoneRefusedMirror(build, version)
	}
	return locales.AddPhoneIPhoneRefusedFolder(img.Dir, build, version)
}

// refusedNeedsXcode reports whether the way to an image Apple signs, after
// it refused img, is installing or updating Xcode from the App Store: for
// Xcode's own image and the mirror's, on a Mac.
func refusedNeedsXcode(img *driver.DiskImage) bool {
	return guideOS == "darwin" && img != nil && (img.Source == driver.DiskImageFromXcode || img.Source == driver.DiskImageFromMirror)
}

// refusedOffersMirror reports whether the steps offer to download an image
// from the third-party mirror after Apple refused img: when img came with
// Xcode or was put in Pinky's folder. Not for the mirror's own download,
// which says to update Xcode or Pinky, nor for PINKY_DDI's image, the only
// one Pinky uses while it is set.
func refusedOffersMirror(img *driver.DiskImage) bool {
	return img != nil && (img.Source == driver.DiskImageFromXcode || img.Source == driver.DiskImageFromFolder)
}

// chooseConnection opens Settings › Phones, where an iPhone's connection
// is chosen (connectionRows), for a phone whose chosen connection does
// not work with it (driver.SetupConnection).
func (a *app) chooseConnection() {
	a.page, a.set.sec = pageSettings, secPhones
}

// needsHelper reports whether an iPhone waits for its helper to be
// installed, as it is missing or its signature ran out.
func needsHelper(d gateway.DeviceView) bool {
	return d.Info.Platform == driver.IOS && d.Info.State == driver.StateNeedsSetup && d.Info.Setup == driver.SetupInstallHelper
}

// timeNow is the clock the helper's words are told by: whether an iPhone's
// helper stops working soon, or has stopped. Tests put their own in its
// place (testNow in main_test.go), so that these words do not change with
// the time the tests run at. Other views tell dates by the frame's clock,
// Context.Now.
var timeNow = time.Now

// helperWords says when the helper an iPhone is driven through stops
// working (Info.HelperExpires): within driver.HelperReminder, or that it
// did, which ranOut reports. "" when it lasts longer or is not known.
// here: Pinky installs it again from the window (Install again), so the
// words leave out how.
func helperWords(d gateway.DeviceView, now time.Time, here bool) (text string, ranOut bool) {
	exp := d.Info.HelperExpires
	switch {
	case exp.IsZero():
		return "", false
	case !now.Before(exp) && here:
		return locales.CheckHelperExpiredHere(d.Info.Name, exp.Local()), true
	case !now.Before(exp):
		return locales.CheckHelperExpired(d.Info.Name, exp.Local()), true
	case exp.Sub(now) > driver.HelperReminder:
		return "", false
	case here:
		return locales.CheckHelperSoonHere(d.Info.Name, exp.Local()), false
	}
	return locales.CheckHelperSoon(d.Info.Name, exp.Local()), false
}

// helperReminder draws the Phone screen's reminder above the mirror that
// the helper an iPhone is driven through stops working soon, width wide
// and at most room high, and returns its height: 0 when it shows none. On
// a Mac with Xcode it installs the helper again (helper.go), and says why
// the last try failed or which teams to choose from; elsewhere it links
// to how to install it again. Once the helper has stopped, the phone
// needs setup and says so in its place.
func (a *app) helperReminder(c *mg.Context, d gateway.DeviceView, width, room float32) float32 {
	if d.Info.Platform != driver.IOS || d.Info.HelperExpires.IsZero() {
		return 0
	}
	now := timeNow()
	here := a.canInstallHelper()
	text, _ := helperWords(d, now, here)
	if text == "" {
		// Draw it when the time comes.
		c.After(min(d.Info.HelperExpires.Add(-driver.HelperReminder).Sub(now), time.Hour))
		return 0
	}
	p := pal(c)
	id, name := d.Info.ID, d.Info.Name
	r := a.helper.runs[id]
	// The buttons, and what each does, and the words of Cancel after them,
	// where the reminder asks for a choice that can be left.
	var labels []string
	var acts []func()
	var cancel string
	switch {
	case r != nil && r.running:
		// Gone in a moment: Pinky lets go of the phone while it installs.
		text, _ = helperStepWords(r.step, name)
	case !here:
		labels, acts = []string{locales.CheckHelperHow()}, []func(){func() { openURL(c, helperAgainDocs, func(error) {}) }}
	case r != nil && len(r.teams) > 0:
		text = locales.HelperChooseTeam()
		for _, t := range r.teams {
			labels = append(labels, helperTeamLabel(t))
			acts = append(acts, func() { a.startHelperInstall(id, name, t.ID, nil) })
		}
		// The way back to Install again, as the needs-setup page's choice
		// has.
		cancel = locales.HelperCancel()
	default:
		if r != nil && r.err != "" {
			text = r.err
		}
		labels, acts = []string{locales.HelperInstallAgain()}, []func(){func() { a.startHelperInstall(id, name, "", nil) }}
	}
	l := layoutReminder(c, text, labels, cancel, width, room)
	words := func() {
		// As layoutReminder measured them.
		t := mg.RichText(c, reminderSpan(text)).Width(l.textW).Shrink(0)
		if l.lines > 0 {
			// The checkup has them whole too.
			t.MaxLines(l.lines).Tooltip(text)
		}
	}
	// The buttons from one to before another.
	buttons := func(from, to int) {
		for i := from; i < to; i++ {
			o := btnOpt{small: true}
			if l.buttons[i].cut {
				o.maxW = l.buttons[i].w
			}
			b := button(c, labels[i], o).Disabled(here && a.helperBusy())
			if l.buttons[i].cut {
				b.Tooltip(labels[i])
			}
			if l.scrolls {
				// The list they scroll in clips what is around them.
				ringInside(c, b)
			}
			if b.Clicked() {
				acts[i]()
			}
		}
	}
	// Cancel, as high as the buttons and on a line as layoutReminder
	// measured it, forgets the teams: the reminder offers Install again.
	quit := func() {
		if quietButton(c, cancel).Height(smallButtonHeight).Shrink(0).AlignItems(mg.Center).Clicked() {
			delete(a.helper.runs, id)
		}
	}
	// Keyed, a phone's reminder keeps its own state, as how far its teams
	// have scrolled, apart from another phone's.
	box := mg.Row(c.Key("helper-reminder-"+id)).Width(width).Height(l.height).Shrink(0).Padding(reminderPadY, reminderPadEnd, reminderPadY, reminderPadX).
		Radius(rCard).Background(p.amberTint).Border(reminderBorder, p.amberLine).Gap(reminderGap).Role(mg.RoleStatus).Label(text)
	if !l.below {
		box.AlignItems(mg.Center).Children(func() {
			mg.Icon(c, icAlert).FontSize(reminderIcon).TextColor(p.amberDot)
			words()
			buttons(0, len(labels))
			if cancel != "" {
				quit()
			}
		})
		return l.height
	}
	box.AlignItems(mg.Start).Children(func() {
		// Beside the first line of the words.
		mg.Icon(c, icAlert).FontSize(reminderIcon).TextColor(p.amberDot).Margin(max(0, (l.lineH-reminderIcon)/2), 0, 0, 0)
		mg.Column(c).Width(l.textW).Shrink(0).Gap(reminderStack).Children(func() {
			words()
			// In the rows layoutReminder counted for the height: a row
			// that wraps would work them out again by itself. Cancel, when
			// they have it, ends the last one's or has its own after it.
			rows := func(withCancel bool) {
				for from := 0; from < len(labels); {
					to := from + 1
					for to < len(labels) && l.buttons[to].row == l.buttons[from].row {
						to++
					}
					row := l.buttons[from].row
					mg.Row(c).Gap(reminderStack).Children(func() {
						buttons(from, to)
						if withCancel && l.cancel.row == row {
							quit()
						}
					})
					from = to
				}
				if withCancel && (len(labels) == 0 || l.cancel.row > l.buttons[len(labels)-1].row) {
					mg.Row(c).Children(quit)
				}
			}
			if !l.scrolls {
				mg.Column(c).Gap(reminderStack).Children(func() { rows(cancel != "") })
				return
			}
			// More teams than the window has room for. The list reaches
			// into the reminder's padding after its buttons, for its scroll
			// bar, which over their ends would take the presses there.
			// Cancel has a row of its own under it, in view.
			listH := l.buttonsH
			if cancel != "" {
				listH -= reminderPitch
			}
			mg.Scroll(c).Width(l.textW+reminderPadEnd).Height(listH).Shrink(0).Margin(0, -reminderPadEnd, 0, 0).Padding(0, reminderPadEnd, 0, 0).
				ScrollbarStyle(mg.ScrollbarStyle{TrackWidth: reminderPadEnd}).Gap(reminderStack).Children(func() { rows(false) })
			if cancel != "" {
				mg.Row(c).Children(quit)
			}
		})
	})
	return l.height
}

// The helper reminder's box: the most it is wide, its border, its padding
// (above and below, after its buttons, before its icon), its icon, the gap
// between its icon, words and buttons, and that between its words and the
// buttons under them.
const (
	reminderWidth  = 520
	reminderBorder = 1
	reminderPadY   = sp2
	reminderPadEnd = sp2
	reminderPadX   = 14
	reminderIcon   = iconBody
	reminderGap    = 10
	reminderStack  = sp2
	// reminderChrome is what its padding and border take of its height.
	reminderChrome = 2 * (reminderPadY + reminderBorder)
	// reminderPitch is a row of the buttons under the words, with the gap
	// under it.
	reminderPitch = smallButtonHeight + reminderStack
)

// reminderInner returns the width a reminder width wide leaves its words
// and buttons: what is within its border and padding, after its icon.
func reminderInner(width float32) float32 {
	return max(width-2*reminderBorder-reminderPadX-reminderPadEnd-reminderIcon-reminderGap, 1)
}

// reminderSpan is the helper reminder's words as it draws and measures
// them. A text of their size alone would have lower lines than
// Context.MeasureText tells, which measures spans over the theme's text.
func reminderSpan(text string) mg.Span { return mg.Span{Text: text, Size: fsSmall} }

// reminderLayout is how the helper reminder lays out in the width and the
// height it is given (layoutReminder).
type reminderLayout struct {
	below   bool    // the buttons under the words, not beside them
	textW   float32 // the width the words wrap in
	lineH   float32 // the height of a line of the words
	lines   int     // the lines the words are cut to; 0 when they show whole
	buttons []reminderButton
	// cancel is Cancel, after the buttons, where the reminder offers it, as
	// wide as its words (zero where it offers none): beside them, at the
	// end of the last one's row under the words, or in a row of its own
	// under them, as under buttons that scroll.
	cancel reminderButton
	// Under the words: the height the buttons take, Cancel's row too, and
	// whether they scroll in it, for want of room: in all of it but
	// Cancel's row.
	buttonsH float32
	scrolls  bool
	height   float32 // the reminder's, with its padding and border
}

// reminderButton is a button of the helper reminder: its width, whether
// that cuts its label, and its row of the buttons under the words.
type reminderButton struct {
	w   float32
	cut bool
	row int
}

// layoutReminder lays out the helper reminder, width wide, with its words,
// its buttons' labels and the words of Cancel after them ("" where it
// offers none): the buttons beside the words, as a window with room shows
// them, or under the words where that makes the reminder lower, as in a
// narrow window, where the words beside a button would wrap into a tall
// column. The reminder is at most room high, which leaves the phone below
// it its least height, down to a line of its words with its buttons: words
// that would take more are cut to the lines there is room for, and more
// rows of buttons than there is room for scroll, with Cancel under them.
func layoutReminder(c *mg.Context, text string, labels []string, cancel string, width, room float32) reminderLayout {
	span := reminderSpan(text)
	inner := reminderInner(width)
	_, lineH := c.MeasureText(0, span)
	_, wordsH := c.MeasureText(inner, span)
	l := reminderLayout{textW: inner, lineH: lineH}

	// The buttons, and Cancel after them, in the rows they take under the
	// words, and what they leave the words beside them.
	beside, rows, x := inner, 1, float32(0)
	place := func(w float32) reminderButton {
		beside -= reminderGap + w
		b := reminderButton{w: min(w, inner), cut: w > inner}
		if x > 0 && x+reminderStack+b.w > inner {
			rows, x = rows+1, 0
		}
		if x > 0 {
			x += reminderStack
		}
		x += b.w
		b.row = rows - 1
		return b
	}
	for _, label := range labels {
		l.buttons = append(l.buttons, place(buttonWidth(c, label, true)))
	}
	buttonRows := rows
	// The least the buttons take under the words: a row, and Cancel's own
	// under it where Cancel is not in it, as under buttons that scroll.
	least := float32(smallButtonHeight)
	if cancel != "" {
		l.cancel = place(quietButtonWidth(c, cancel))
		if l.cancel.row > 0 {
			least += reminderPitch
		}
	}
	buttonsH := float32(rows)*smallButtonHeight + float32(rows-1)*reminderStack
	belowH := wordsH + reminderStack + buttonsH + reminderChrome
	// Beside the words where that is no higher, and where there is no room
	// for even a line of the words over the least the buttons take.
	if beside >= 1 {
		_, besideH := c.MeasureText(beside, span)
		if max(besideH, smallButtonHeight)+reminderChrome <= belowH || lineH+reminderStack+least+reminderChrome > room {
			l.textW = beside
			l.lines, besideH = cutLines(besideH, lineH, room-reminderChrome)
			l.height = max(besideH, smallButtonHeight) + reminderChrome
			return l
		}
	}
	// Under the words, which keep two lines. Rows of buttons that do not
	// fit under them scroll, in the height of the rows that do and half of
	// the next, which shows that there are more: of a row at least. Cancel
	// then has a row of its own under them, in view.
	l.below, l.buttonsH = true, buttonsH
	left := room - reminderChrome - reminderStack
	if most := left - min(wordsH, 2*lineH); buttonRows > 1 && buttonsH > most {
		under, list := float32(0), float32(smallButtonHeight)
		if cancel != "" {
			under = reminderPitch
		}
		if whole := int((most - under - smallButtonHeight/2.0) / reminderPitch); whole > 0 {
			list = float32(whole)*reminderPitch + smallButtonHeight/2.0
		}
		// Not where Cancel's row would take what the list gives up.
		if list+under < buttonsH {
			l.buttonsH, l.scrolls = list+under, true
			if cancel != "" {
				l.cancel.row = buttonRows
			}
		}
	}
	l.lines, wordsH = cutLines(wordsH, lineH, left-l.buttonsH)
	l.height = wordsH + reminderStack + l.buttonsH + reminderChrome
	return l
}

// cutLines cuts words h high, in lines at most lineH high, to the lines
// that fit in room, one at least: it returns how many, 0 when they all
// fit or are one line, and the height they take.
func cutLines(h, lineH, room float32) (lines int, height float32) {
	if h <= room || h <= lineH || lineH <= 0 {
		return 0, h
	}
	lines = max(int(room/lineH), 1)
	return lines, float32(lines) * lineH
}

// iphoneCheckAction draws what an iPhone's item offers to do, and reports
// whether it did: the steps in Add phone for a setup step, how to install
// the helper.
func (a *app) iphoneCheckAction(c *mg.Context, d gateway.DeviceView, ck driver.Check) bool {
	if d.Info.Virtual {
		// A Simulator has none of an iPhone's steps; its helper's build
		// can be tried again.
		if ck.Fix == driver.SetupSimulatorHelper {
			mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
				if button(c, locales.CheckSimHelperRetry(), btnOpt{small: true}).Clicked() {
					a.startVirtualPhone(d)
				}
			})
			return true
		}
		return ck.Fix == driver.SetupPreparing
	}
	switch ck.Fix {
	case driver.SetupTrust, driver.SetupTrustRecord, driver.SetupDeveloperMode, driver.SetupUnlock, driver.SetupPreparing,
		driver.SetupNoDiskImage, driver.SetupDiskImage, driver.SetupDiskImageRefused:
		mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
			if button(c, locales.PhoneSetUpIPhone(), btnOpt{small: true}).Clicked() {
				a.page = pageAddPhone
				a.add.reset(a)
				a.add.step, a.add.focus = "iphone", d.Info.ID
			}
		})
		return true
	case driver.FixInstallHelper:
		// On a Mac with Xcode, Pinky installs it from here: from the
		// helper's item, not again from the input's, which needs it too.
		if ck.ID == driver.CheckInput && a.canInstallHelper() {
			return true
		}
		mg.Column(c).Margin(4, 0, 0, 0).Children(func() {
			again := !d.Info.HelperExpires.IsZero()
			if !a.helperInstall(c, d.Info.ID, d.Info.Name, helperOpt{again: again, small: true}) &&
				button(c, locales.CheckHelperHow(), btnOpt{small: true}).Clicked() {
				openURL(c, helperLink(d), func(error) {})
			}
		})
		return true
	case driver.SetupConnection:
		mg.Row(c).Margin(4, 0, 0, 0).Children(func() {
			if button(c, locales.ConnectionChoose(), btnOpt{small: true}).Clicked() {
				a.chooseConnection()
			}
		})
		return true
	}
	return false
}
