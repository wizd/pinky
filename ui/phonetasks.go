package ui

import (
	"strings"

	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/update"
)

// Pinky's app on a phone (Pinky on the phone) also does simple tasks on
// that phone by itself, with the person's own model and key, without this
// computer. Desktop Pinky says so where people look: once in the phone's
// sidebar, until they put the card away, and on each phone in Settings ›
// Phones, with how to set it up (the README) and, for an iPhone on a Mac
// with Xcode, the install of the app.

// phoneTasksDocs is how to set it up, in the README, and phoneTasksDocsZh
// in its Chinese version.
const (
	phoneTasksDocs   = "https://github.com/DIYgod/pinky#pinky-on-the-phone"
	phoneTasksDocsZh = "https://github.com/DIYgod/pinky/blob/main/README.zh-CN.md#pinky-on-the-phone"
)

// tasksTipState is whether the person put the card away, read from
// update.json once.
type tasksTipState struct {
	loaded, seen bool
}

func phoneTasksLink() string {
	if _, lang, ok := locales.Catalog.Message(i18n.Locale(), "phoneTasksHow"); ok && strings.HasPrefix(lang, "zh") {
		return phoneTasksDocsZh
	}
	return phoneTasksDocs
}

func (a *app) phoneTasksSeen() bool {
	s := &a.tasksTip
	if !s.loaded {
		s.loaded = true
		if a.opt.Updates != nil {
			s.seen = a.opt.Updates.State().PhoneTasksSeen
		}
	}
	return s.seen
}

func (a *app) putAwayPhoneTasks() {
	a.tasksTip.loaded, a.tasksTip.seen = true, true
	if a.opt.Updates != nil {
		a.opt.Updates.Change(func(s *update.State) { s.PhoneTasksSeen = true })
	}
}

// phoneTasksFor reports whether a phone can have Pinky's app do tasks on
// it: a real phone (Simulators run no packet tunnel; the companion on an
// emulator is for testing), and on Android one with the app installed, as
// the companion card offers it otherwise.
func (a *app) phoneTasksFor(d gateway.DeviceView) bool {
	if d.Info.Virtual {
		return false
	}
	switch d.Info.Platform {
	case driver.IOS:
		return true
	case driver.Android:
		switch a.companionFor(d) {
		case driver.CompanionOn, driver.CompanionReady, driver.CompanionManualOn, driver.CompanionOff,
			driver.CompanionManual, driver.CompanionOutdated, driver.CompanionPublicKey:
			return true
		}
	}
	return false
}

// phoneTasksWords is what to do for it on the phone.
func phoneTasksWords(d gateway.DeviceView) string {
	if d.Info.Platform == driver.IOS {
		return locales.PhoneTasksIPhone(d.Info.Name)
	}
	return locales.PhoneTasksAndroid(d.Info.Name)
}

// phoneTasksCard is the card in the phone's sidebar, while the phone is
// connected and free, with its app's companion card done (Android), and
// until the person puts it away: once for every phone, so it never nags.
func (a *app) phoneTasksCard(c *mg.Context, d gateway.DeviceView) {
	id := d.Info.ID
	// Not while the app installs: the phone's page shows that, as Pinky
	// lets go of the phone meanwhile.
	if a.phoneTasksSeen() || !a.phoneTasksFor(d) || d.Status != gateway.DeviceIdle || a.helperRunning(id) {
		return
	}
	if d.Info.Platform == driver.Android {
		switch a.companionFor(d) {
		case driver.CompanionOn, driver.CompanionReady, driver.CompanionManualOn:
		default:
			return // the companion card says what to do first
		}
	}
	p := pal(c)
	ic := icAndroid
	if d.Info.Platform == driver.IOS {
		ic = icIphone
	}
	mg.Column(c.Key("phone-tasks-"+id)).Padding(14).Radius(12).Background(p.bg2).Border(1, p.line).Gap(8).
		Label(locales.PhoneTasksTitle()).Children(func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Icon(c, ic).FontSize(iconBody).TextColor(p.ink2)
			mg.Text(c, locales.PhoneTasksTitle()).FontSize(fsHeadline).FontWeight(fwSemibold).Grow(1).MinWidth(0)
		})
		mg.Text(c, phoneTasksWords(d)).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
		a.phoneTasksActions(c, d, true)
	})
}

// phoneTasksRow is the phone's row in Settings › Phones.
func (a *app) phoneTasksRow(c *mg.Context, d gateway.DeviceView) {
	if !a.phoneTasksFor(d) {
		return
	}
	p := pal(c)
	groupRow(c, mg.Column(c).Gap(sp2).Justify(mg.Center), false).Children(func() {
		mg.Text(c, locales.PhoneTasksRow()).FontWeight(fwMedium)
		mg.Text(c, phoneTasksWords(d)).FontSize(fsSmall).TextColor(p.ink2).LineHeight(1.4)
		a.phoneTasksActions(c, d, false)
	})
}

// phoneTasksActions: an iPhone's install from this Mac, how to set it up,
// and on the card, Got it.
func (a *app) phoneTasksActions(c *mg.Context, d gateway.DeviceView, card bool) {
	if d.Info.Platform == driver.IOS {
		// Draws nothing where Pinky cannot build the app (no Mac with
		// Xcode); the README says how then.
		a.helperInstall(c, d.Info.ID, d.Info.Name, helperOpt{small: true})
	}
	mg.Row(c).Gap(16).Wrap().AlignItems(mg.Center).Children(func() {
		if linkButton(c, locales.PhoneTasksHow()).Clicked() {
			openURL(c, phoneTasksLink(), func(error) {})
		}
		if card && quietButton(c, locales.PhoneTasksGotIt()).Clicked() {
			a.putAwayPhoneTasks()
		}
	})
}
