package ui

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/secret"
)

// askState is Ask Pinky's: the box at the top of Tasks that gives Pinky's
// own agent a task (Options.Pilot), and its part of Settings › Agents.
type askState struct {
	draft  string // the task being typed
	device string // the phone chosen on All phones
	gen    int    // bumped on each start, to start the box afresh
	err    string // why the last start failed
	errFor string // the phone it was for

	// The person's messages to the task: one being typed while it runs
	// (steer), one being edited (editID, edit), the one that continues it
	// once it ended (next), and why the last of these failed.
	steer  string
	next   string
	editID string
	edit   string
	mgen   int // bumped when a field empties, to start it afresh
	msgErr string
	msgFor string // the phone msgErr is for

	// keys says which services have an API key saved, by the name it is
	// saved under (pilot.Config.KeyName), once looked up off the main
	// thread.
	keys     map[string]bool
	checking map[string]bool
	// rechecked is when the last task that ended for want of a working
	// key ended, so that its note has the keychain asked once.
	rechecked time.Time

	// Settings › Agents › Ask Pinky.
	editing string // the editor open: "key", "model" or "url"
	value   string // what is typed in it
	verr    string // why it was not saved
	busy    bool   // the key is on its way to the keychain
	scroll  bool   // bring the settings into view, from the box's setup link

	// The check of the model (PilotChecker): the last result, the model
	// it was of (checkSig), and the model being checked, "" when none.
	check    *pilot.CheckResult
	checkFor string
	checkRun string

	// usage is what the model used in the last 30 days (PilotUsage), read
	// off the main thread at usageAt.
	usage     []pilot.UsageRow
	usageAt   time.Time
	usageBusy bool
}

// The editors of Ask Pinky's settings.
const (
	askEditKey   = "key"
	askEditModel = "model"
	askEditURL   = "url"
)

// modelName is a model as the person reads it: the name its service
// gives it, or its ID.
func modelName(cfg pilot.Config) string {
	if svc, ok := pilot.ServiceByID(cfg.Service); ok {
		for _, m := range svc.Models {
			if m.ID == cfg.Model {
				return m.Label()
			}
		}
	}
	return cfg.Model
}

// modelLine is the model of the box: its name, and its service's unless
// the service is Anthropic's or one the person typed.
func modelLine(cfg pilot.Config) string {
	svc, ok := pilot.ServiceByID(cfg.Service)
	if !ok || svc.Custom || svc.ID == pilot.Anthropic || cfg.Model == "" {
		return modelName(cfg)
	}
	return modelName(cfg) + " · " + serviceLabel(svc)
}

// serviceLabel is a service as Settings names it, in the language shown.
func serviceLabel(s pilot.Service) string {
	switch {
	case s.ID == pilot.CustomOpenAI:
		return locales.SettingsAskServiceOpenAI()
	case s.ID == pilot.CustomAnthropic:
		return locales.SettingsAskServiceAnthropic()
	case s.Region == pilot.RegionChina:
		return locales.SettingsAskServiceRegion(s.Name, locales.SettingsAskRegionChina())
	case s.Region == pilot.RegionGlobal:
		return locales.SettingsAskServiceRegion(s.Name, locales.SettingsAskRegionGlobal())
	}
	return s.Name
}

// askKey reports whether an API key is saved under a name
// (pilot.Config.KeyName), and whether that is known yet: the keychain is
// asked once, off the main thread.
func (a *app) askKey(name string) (has, known bool) {
	st := &a.ask
	if st.keys == nil {
		st.keys, st.checking = map[string]bool{}, map[string]bool{}
	}
	if has, ok := st.keys[name]; ok {
		return has, true
	}
	if !st.checking[name] {
		st.checking[name] = true
		store := a.secrets()
		go func() {
			has := store.Has(secret.ModelKeyAccount(name))
			a.post(func() {
				st.checking[name] = false
				st.keys[name] = has
			})
		}()
	}
	return false, false
}

// forgetKey has the keychain asked again, as after the API refused the
// key or found none.
func (a *app) forgetKey(name string) {
	if a.ask.keys != nil && !a.ask.checking[name] {
		delete(a.ask.keys, name)
	}
}

// askReady reports whether Ask Pinky can start a task: a key is saved,
// unless the service needs none, and a model chosen. known is false while
// the keychain has not answered.
func (a *app) askReady() (ready, known bool) {
	cfg := a.settings.PilotConfig()
	if cfg.KeyOptional() {
		return cfg.Model != "", true
	}
	has, known := a.askKey(cfg.KeyName())
	return has && cfg.Model != "", known
}

// openAskSettings shows Ask Pinky's part of Settings › Agents.
func (a *app) openAskSettings() {
	a.page, a.set.sec = pageSettings, secAgents
	a.ask.scroll = true
}

// askPhone is the phone the box gives a task to: the one shown, or on All
// phones the one chosen in the box.
func (a *app) askPhone() (gateway.DeviceView, bool) {
	if d := a.device(a.sel); d != nil {
		return *d, true
	}
	if d := a.device(a.ask.device); d != nil {
		return *d, true
	}
	if len(a.devices) == 0 {
		return gateway.DeviceView{}, false
	}
	a.ask.device = a.devices[0].Info.ID
	return a.devices[0], true
}

// askBox is the box at the top of Tasks: a task for Pinky's own agent
// and Start; while it works, how far it is and Stop; once it ended, how.
func (a *app) askBox(c *mg.Context) {
	if a.opt.Pilot == nil || !a.settings.AskPinky {
		return
	}
	d, ok := a.askPhone()
	if !ok {
		return
	}
	p := pal(c)
	st := &a.ask
	id := d.Info.ID
	ps, has := a.opt.Pilot.Status(id)
	card(c, mg.Column(c.Key("ask")).Padding(14)).Gap(sp3).Label(locales.AskTitle()).Children(func() {
		if has && ps.Running() {
			a.askRunning(c, ps)
			return
		}
		if has {
			a.askEnded(c, ps)
		}
		ready, known := a.askReady()
		if !known {
			return
		}
		if !ready {
			a.askSetup(c)
			return
		}
		if a.sel == allPhones || a.device(a.sel) == nil {
			a.askPicker(c, id)
		}
		cfg := a.settings.PilotConfig()
		area := mg.TextArea(c.Key(fmt.Sprintf("ask-%d", st.gen)), &st.draft).Label(locales.AskField()).
			Placeholder(locales.AskPlaceholder(d.Info.Name)).Lines(2, 6)
		why := ""
		if t := a.taskOf(id); t != nil && running(t.State) {
			why = locales.AskBusy(nonEmpty(t.Agent, locales.AskAnotherAgent()), d.Info.Name)
		} else if d.Status == gateway.DeviceOffline {
			why = locales.PhoneOffline(d.Info.Name)
		}
		if why != "" {
			mg.Text(c, why).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2).Role(mg.RoleStatus)
		}
		if st.err != "" && st.errFor == id {
			mg.Text(c, st.err).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
		}
		// The model and the limit, on a line each, so that they fit beside
		// Start in the panel's width.
		mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
			mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				mg.Text(c, modelLine(cfg)).FontSize(fsCaption).FontWeight(fwMedium).TextColor(p.ink2).SingleLine()
				mg.Text(c, locales.AskTaskLimit(limitText(cfg))).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
				if today, ok := a.usageToday(); ok {
					mg.Text(c, locales.AskToday(today)).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
				}
			})
			// About the screen: a question about what the phone shows now,
			// which may be left empty to have the screen explained.
			screen := button(c, locales.AskScreen(), btnOpt{small: true}).Disabled(why != "").
				Tooltip(locales.AskScreenTooltip())
			if why == "" && screen.Clicked() {
				a.askStart(id, true)
			}
			if a.schedOn() && quietButton(c.Key("ask-schedule"), locales.SchedButton()).Clicked() {
				a.newSchedule(id, st.draft)
			}
			can := why == "" && strings.TrimSpace(st.draft) != ""
			start := button(c, locales.AskStart(), btnOpt{kind: btnPrimary, small: true}).Disabled(!can)
			if can && (start.Clicked() || area.Shortcut(mg.Cmd, mg.KeyEnter)) {
				a.askStart(id, false)
			}
		})
	})
}

// askPicker chooses the phone on All phones.
func (a *app) askPicker(c *mg.Context, id string) {
	labels := make([]string, len(a.devices))
	seen := map[string]int{}
	for i, d := range a.devices {
		l := d.Info.Name
		if seen[l]++; seen[l] > 1 {
			l = fmt.Sprintf("%s (%d)", l, seen[l])
		}
		labels[i] = l
	}
	cur := ""
	for i, d := range a.devices {
		if d.Info.ID == id {
			cur = labels[i]
		}
	}
	if mg.Select(c.Key("ask-phone"), &cur, labels).Label(locales.AskPhone()).Changed() {
		for i, l := range labels {
			if l == cur {
				a.ask.device = a.devices[i].Info.ID
			}
		}
	}
}

// askStart starts the task typed in the field; with screen, a question
// about what the phone shows, the screen explained when the field is empty.
func (a *app) askStart(id string, screen bool) {
	st := &a.ask
	task := st.draft
	if screen && strings.TrimSpace(task) == "" {
		task = locales.AskScreenDefault()
	}
	err := a.opt.Pilot.StartWith(a.settings.PilotConfig(), id, task, pilot.Extra{Screen: screen})
	switch {
	case err == nil:
		st.draft, st.err, st.errFor = "", "", ""
		st.gen++
	case errors.Is(err, pilot.ErrNoTask):
	case errors.Is(err, pilot.ErrNoModel):
		a.openAskSettings()
	default:
		st.err, st.errFor = locales.AskErrStart(err.Error()), id
	}
}

// askRunning is the box while Pinky works: how far it is, and Stop.
func (a *app) askRunning(c *mg.Context, s pilot.Status) {
	p := pal(c)
	c.After(time.Second)
	mg.Column(c).Gap(2).Children(func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			dot(c, p.green, true)
			mg.Text(c, locales.AskWorking()).FontWeight(fwSemibold).Grow(1).MinWidth(0)
			if button(c, locales.TasksStop(), btnOpt{small: true, kind: btnRed}).Label(locales.AskStop()).Clicked() {
				a.stop(s.Device)
			}
		})
		// How far it is, under the words and across the card, so that it
		// fits on a line in every language.
		mg.Text(c, usageText(s, c.Now())).FontSize(fsCaption).TextColor(p.ink2).FontFeatures("tnum").Margin(0, 0, 0, 8+sp2)
		if s.By != "" {
			mg.Text(c, locales.AskByAgent(s.By)).FontSize(fsCaption).TextColor(p.ink3).Margin(0, 0, 0, 8+sp2)
		}
	})
	a.askPending(c, s)
	a.askMessageRow(c, s.Device, false)
}

// askPending is the person's messages Pinky has not read yet, each with
// Edit and Withdraw.
func (a *app) askPending(c *mg.Context, s pilot.Status) {
	p := pal(c)
	st := &a.ask
	for _, m := range s.Pending {
		mg.Column(c.Key("ask-msg-"+m.ID)).Padding(sp2, sp3).Radius(rControl).Background(p.bg2).Gap(sp1).Children(func() {
			if st.editID == m.ID {
				in := mg.TextInput(c.Key(fmt.Sprintf("ask-edit-msg-%s-%d", m.ID, st.mgen)), &st.edit).Label(locales.AskSteerField()).AutoFocus()
				mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
					save := button(c, locales.AskSave(), btnOpt{small: true, kind: btnPrimary})
					if save.Clicked() || in.Submitted() {
						a.askEdit(s.Device, m.ID, st.edit)
					}
					if button(c, locales.AskCancel(), btnOpt{small: true}).Clicked() {
						st.editID, st.edit = "", ""
						st.mgen++
					}
				})
				return
			}
			mg.Text(c, m.Text).FontSize(fsSmall).LineHeight(1.4).Selectable()
			if !s.Running() {
				// Once it ended, askUnread above says what became of them.
				return
			}
			mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
				mg.Text(c, locales.AskPendingNote()).FontSize(fsCaption).TextColor(p.ink3).Grow(1).MinWidth(0)
				if quietButton(c, locales.AskEdit()).Clicked() {
					st.editID, st.edit, st.msgErr = m.ID, m.Text, ""
					st.mgen++
				}
				if quietButton(c, locales.AskWithdraw()).Clicked() {
					a.askEdit(s.Device, m.ID, "")
				}
			})
		})
	}
}

// askMessageRow is the field for a message to Pinky and its button: Send
// while the task runs, Continue once it ended.
func (a *app) askMessageRow(c *mg.Context, device string, ended bool) {
	p := pal(c)
	st := &a.ask
	text, label, placeholder, action := &st.steer, locales.AskSteerField(), locales.AskSteerPlaceholder(), locales.AskSend()
	key := fmt.Sprintf("ask-steer-%d", st.mgen)
	if ended {
		text, label, placeholder, action = &st.next, locales.AskContinueField(), locales.AskContinuePlaceholder(), locales.AskContinue()
		key = fmt.Sprintf("ask-next-%d", st.mgen)
	}
	if st.msgErr != "" && st.msgFor == device {
		mg.Text(c, st.msgErr).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
	}
	mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
		in := mg.TextInput(c.Key(key), text).Label(label).Placeholder(placeholder).Grow(1).MinWidth(0)
		// Continue sends the unread messages even with nothing typed.
		can := strings.TrimSpace(*text) != ""
		if ended && !can {
			s, _ := a.opt.Pilot.Status(device)
			can = len(s.Pending) > 0
		}
		send := button(c, action, btnOpt{small: true, kind: btnPrimary}).Disabled(!can)
		if can && (send.Clicked() || in.Submitted()) {
			if ended {
				a.askContinue(device)
			} else {
				a.askSteer(device)
			}
		}
	})
}

func (a *app) askSteer(device string) {
	st := &a.ask
	_, err := a.opt.Pilot.Steer(device, st.steer)
	switch {
	case err == nil:
		st.steer, st.msgErr = "", ""
		st.mgen++
	case errors.Is(err, pilot.ErrNotRunning):
		// The task ended meanwhile: the words wait in Continue's field.
		st.next, st.steer, st.msgErr = st.steer, "", ""
		st.mgen++
	case errors.Is(err, pilot.ErrEmpty):
	default:
		st.msgErr, st.msgFor = locales.AskErrSend(err.Error()), device
	}
}

// askEdit changes a message Pinky has not read; empty text withdraws it.
func (a *app) askEdit(device, id, text string) {
	st := &a.ask
	var err error
	if strings.TrimSpace(text) == "" {
		err = a.opt.Pilot.Withdraw(device, id)
	} else {
		err = a.opt.Pilot.Edit(device, id, text)
	}
	st.editID, st.edit = "", ""
	st.mgen++
	switch {
	case err == nil, errors.Is(err, pilot.ErrNoMessage):
		st.msgErr = ""
	case errors.Is(err, pilot.ErrRead):
		st.msgErr, st.msgFor = locales.AskRead(), device
	default:
		st.msgErr, st.msgFor = locales.AskErrSend(err.Error()), device
	}
}

func (a *app) askContinue(device string) {
	st := &a.ask
	if err := a.opt.Pilot.Continue(device, st.next); err != nil {
		st.msgErr, st.msgFor = locales.AskErrSend(err.Error()), device
		return
	}
	st.next, st.msgErr = "", ""
	st.mgen++
}

// askEnded says how Pinky's last task on the phone ended, until the
// person dismisses it or starts another.
func (a *app) askEnded(c *mg.Context, s pilot.Status) {
	p := pal(c)
	title, reason := pilotEnd(s)
	if (s.End == pilot.NoKey || s.End == pilot.BadKey) && !s.Ended.Equal(a.ask.rechecked) {
		// The key may have changed since: ask the keychain again, once.
		a.ask.rechecked = s.Ended
		a.forgetKey(s.Config.KeyName())
	}
	mg.Column(c).Padding(sp3).Radius(rControl).Background(p.bg2).Gap(sp1).Role(mg.RoleStatus).Children(func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			switch s.End {
			case pilot.Done:
				mg.Icon(c, icCheck).FontSize(iconBody).TextColor(p.green)
			case pilot.Stopped:
				dot(c, p.ink3, false)
			default:
				dot(c, p.amberDot, false)
			}
			mg.Text(c, title).FontWeight(fwSemibold).Grow(1).MinWidth(0)
			if quietButton(c, locales.AskDismiss()).Clicked() {
				a.opt.Pilot.Dismiss(s.Device)
			}
		})
		if reason != "" {
			mg.Text(c, reason).FontSize(fsSmall).LineHeight(1.4).Selectable()
		}
		mg.Text(c, usageText(s, c.Now())).FontSize(fsCaption).TextColor(p.ink3).FontFeatures("tnum")
		if s.By != "" {
			mg.Text(c, locales.AskByAgent(s.By)).FontSize(fsCaption).TextColor(p.ink3)
		}
		a.askSuggestions(c, s)
	})
	if !s.Continuable {
		return
	}
	if len(s.Pending) > 0 {
		mg.Text(c, locales.AskUnread()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		a.askPending(c, s)
	}
	a.askMessageRow(c, s.Device, true)
}

// askSetup is the box while Ask Pinky lacks a key or a model.
func (a *app) askSetup(c *mg.Context) {
	p := pal(c)
	cfg := a.settings.PilotConfig()
	link := locales.AskSetupKey()
	if has, _ := a.askKey(cfg.KeyName()); (has || cfg.KeyOptional()) && cfg.Model == "" {
		link = locales.AskSetupModel()
	}
	mg.Column(c).Gap(sp1).Children(func() {
		mg.Text(c, locales.AskTitle()).FontSize(fsHeadline).FontWeight(fwSemibold)
		mg.Text(c, locales.AskSetup()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		if linkButton(c, link).AlignSelf(mg.Start).Margin(sp1, 0, 0, 0).Clicked() {
			a.openAskSettings()
		}
	})
}

// usageText is how much a task did and used: "12 steps · US$0.18 · 2 min".
func usageText(s pilot.Status, now time.Time) string {
	parts := []string{locales.AskSteps(s.Steps)}
	switch {
	case s.Priced:
		parts = append(parts, dollars(s.Cost))
	case s.Tokens > 0:
		parts = append(parts, locales.AskTokens(int(s.Tokens)))
	}
	return strings.Join(append(parts, elapsed(s.Elapsed(now))), " · ")
}

// limitText is a task's cost limit: in dollars, or in tokens for a model
// whose prices Pinky does not know.
func limitText(cfg pilot.Config) string {
	if !cfg.Priced() {
		return locales.AskTokens(int(cfg.TokenLimit()))
	}
	return dollars(cfg.MaxCost)
}

func dollars(v float64) string { return locales.AskDollars(strconv.FormatFloat(v, 'f', 2, 64)) }

// pilotEnd is how a task of Pinky's own agent ended, in the language
// shown: a title ("Stopped") and, when there is more to say, why.
func pilotEnd(s pilot.Status) (title, reason string) {
	return pilotEndIn(locales.In(i18n.Locale()), s)
}

func pilotEndIn(m locales.Messages, s pilot.Status) (title, reason string) {
	switch s.End {
	case pilot.Done:
		return m.AskEndDone(), s.Summary
	case pilot.Stopped:
		return m.AskEndStopped(), ""
	case pilot.StepLimit:
		return m.AskEndStopped(), m.AskLimitSteps(s.Config.MaxSteps)
	case pilot.TimeLimit:
		return m.AskEndStopped(), m.AskLimitTime(int(s.Config.MaxTime / time.Minute))
	case pilot.CostLimit:
		if s.Priced {
			return m.AskEndStopped(), m.AskLimitCost(m.AskDollars(strconv.FormatFloat(s.Config.MaxCost, 'f', 2, 64)))
		}
		return m.AskEndStopped(), m.AskLimitTokens(int(s.Config.TokenLimit()))
	case pilot.Refused:
		return m.AskEndFailed(), m.AskRefused()
	case pilot.NoKey:
		return m.AskEndNotStarted(), m.AskNoKey()
	case pilot.BadKey:
		return m.AskEndNotStarted(), m.AskBadKey()
	case pilot.Cancelled:
		return m.AskEndStopped(), m.AskCancelled(s.By)
	}
	return m.AskEndFailed(), s.Summary
}

// DescribePilot is the line Pinky's own agent leaves in Activity for a
// task it ends itself, in the app's language (pilot.Options.Describe).
func DescribePilot(s pilot.Status) string {
	m := locales.In(i18n.Locale())
	title, reason := pilotEndIn(m, s)
	if reason == "" {
		return title
	}
	return m.AskEndLine(title, reason)
}

// askSettings is Ask Pinky's part of Settings › Agents: whether the box
// shows, the service, its API key, model and address, the check, the
// limits, and what the model used.
func (a *app) askSettings(c *mg.Context) {
	if a.opt.Pilot == nil {
		return
	}
	st := &a.ask
	s := &a.settings
	cfg := s.PilotConfig()
	svc, _ := pilot.ServiceByID(cfg.Service)
	col := mg.Column(c.Key("ask-settings")).Gap(10).Label(locales.AskTitle())
	if st.scroll {
		st.scroll = false
		col.ScrollIntoView()
	}
	col.Children(func() {
		settingsHeading(c, locales.AskTitle(), locales.SettingsAskIntro())
		group(c).Children(func() {
			a.toggleRow(c, "ask-on", locales.SettingsAskOn(), locales.SettingsAskOnDesc(), false, &s.AskPinky)
			settingRow(c, locales.SettingsAskService(), locales.SettingsAskServiceDesc(), false, func() {
				list := pilot.Services()
				labels := make([]string, len(list))
				cur := ""
				for i, l := range list {
					labels[i] = serviceLabel(l)
					if l.ID == svc.ID {
						cur = labels[i]
					}
				}
				if mg.Select(c.Key("ask-service"), &cur, labels).Label(locales.SettingsAskService()).Changed() {
					for i, l := range labels {
						if l == cur && list[i].ID != svc.ID {
							s.PilotService = list[i].ID
							st.editing, st.value, st.verr = "", "", ""
							a.changed()
						}
					}
				}
			})
			a.askKeyRow(c, cfg, svc)
			a.askModelRow(c, cfg, svc)
			a.askURLRow(c, cfg, svc)
			a.askCheckRow(c, cfg)
			a.askLimitRows(c, cfg)
		})
		a.askUsage(c)
	})
}

func (a *app) askKeyRow(c *mg.Context, cfg pilot.Config, svc pilot.Service) {
	p := pal(c)
	st := &a.ask
	store := a.secrets()
	if !secret.Supported(store) {
		settingRow(c, locales.SettingsAskKey(), locales.SettingsAskKeyUnsupported(), false, nil)
		return
	}
	name := cfg.KeyName()
	desc := keyKeep()
	if svc.KeyOptional {
		desc = locales.SettingsAskKeyOptional(desc)
	}
	has, known := a.askKey(name)
	if st.editing == askEditKey {
		a.askEditor(c, locales.SettingsAskKey(), desc, true, locales.SettingsAskKeyPlaceholder(), func(v string) {
			a.saveKey(name, v)
		})
		return
	}
	settingRow(c, locales.SettingsAskKey(), desc, false, func() {
		if !known {
			return
		}
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Shrink(0).Children(func() {
			if has {
				mg.Row(c).Gap(6).AlignItems(mg.Center).Margin(0, sp1, 0, 0).Children(func() {
					mg.Icon(c, icLock).FontSize(iconSmall).TextColor(p.ink2)
					mg.Text(c, locales.SettingsAskKeySaved()).FontSize(fsSmall).TextColor(p.ink2)
				})
				if button(c, locales.SettingsChange(), btnOpt{small: true}).Label(locales.SettingsAskKeyChange()).Clicked() {
					st.editing, st.value, st.verr = askEditKey, "", ""
				}
				if button(c, locales.SettingsAskKeyRemove(), btnOpt{small: true, kind: btnRed}).Label(locales.SettingsAskKeyRemoveLabel()).Clicked() {
					a.removeKey(name)
				}
				return
			}
			if button(c, locales.SettingsAskKeyAdd(), btnOpt{small: true, kind: btnPrimary}).Label(locales.SettingsAskKeyAddLabel()).Clicked() {
				st.editing, st.value, st.verr = askEditKey, "", ""
			}
		})
	})
	if st.verr != "" && st.editing == "" {
		mg.Text(c, st.verr).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus).Margin(0, sp4, sp2, sp4)
	}
}

// keyKeep says where the key stays, in the system's words.
func keyKeep() string {
	switch runtime.GOOS {
	case "darwin":
		return locales.SettingsAskKeyKeepMac()
	case "windows":
		return locales.SettingsAskKeyKeepWindows()
	}
	return locales.SettingsAskKeyKeepOther()
}

// saveKey saves an API key in the keychain under a name
// (pilot.Config.KeyName), off the main thread, then checks the model with
// it. The key leaves the client's state at once and is never read back.
func (a *app) saveKey(name, key string) {
	st := &a.ask
	key = strings.TrimSpace(key)
	st.value, st.verr, st.busy = "", "", true
	st.gen++
	store := a.secrets()
	go func() {
		err := store.Save(secret.ModelKeyAccount(name), key)
		a.post(func() {
			st.busy = false
			if err != nil {
				st.verr = locales.SettingsAskKeyErrSave(err.Error())
				return
			}
			st.keys[name], st.editing = true, ""
			a.toasts = append(a.toasts, locales.SettingsAskKeySavedToast())
			a.runCheck(a.settings.PilotConfig())
		})
	}()
}

func (a *app) removeKey(name string) {
	st := &a.ask
	store := a.secrets()
	go func() {
		err := store.Delete(secret.ModelKeyAccount(name))
		a.post(func() {
			if err != nil && !errors.Is(err, secret.ErrNotFound) {
				st.verr = locales.SettingsAskKeyErrRemove(err.Error())
				return
			}
			st.keys[name], st.verr = false, ""
			st.check, st.checkFor = nil, ""
			a.toasts = append(a.toasts, locales.SettingsAskKeyRemovedToast())
		})
	}()
}

func (a *app) askModelRow(c *mg.Context, cfg pilot.Config, svc pilot.Service) {
	st := &a.ask
	s := &a.settings
	set := func(v string) {
		s.setPilotModel(svc.ID, strings.TrimSpace(v))
		st.editing = ""
		a.changed()
		a.runCheck(s.PilotConfig())
	}
	desc := locales.SettingsAskModelDesc()
	switch {
	case svc.ID == pilot.Anthropic:
		desc = locales.SettingsAskModelClaude()
	case len(svc.Models) > 0:
		desc = locales.SettingsAskModelSuggested()
	case svc.Local:
		desc = locales.SettingsAskModelLocal()
	}
	if st.editing == askEditModel {
		a.askEditor(c, locales.SettingsAskModel(), desc, false, locales.SettingsAskModelPlaceholder(), set)
		return
	}
	if len(svc.Models) == 0 {
		a.askValueRow(c, locales.SettingsAskModel(), desc, nonEmpty(cfg.Model, locales.SettingsAskModelNone()),
			locales.SettingsAskModelChange(), askEditModel, cfg.Model)
		return
	}
	// A service Pinky suggests models of: those, the one typed if it is
	// another, and Other… to type one.
	settingRow(c, locales.SettingsAskModel(), desc, false, func() {
		var labels, ids []string
		cur := ""
		for _, m := range svc.Models {
			labels, ids = append(labels, m.Label()), append(ids, m.ID)
			if m.ID == cfg.Model {
				cur = m.Label()
			}
		}
		if cur == "" && cfg.Model != "" {
			cur = cfg.Model
			labels, ids = append(labels, cfg.Model), append(ids, cfg.Model)
		}
		other := locales.SettingsAskModelOther()
		labels = append(labels, other)
		if mg.Select(c.Key("ask-model-"+svc.ID), &cur, labels).Label(locales.SettingsAskModel()).Changed() {
			if cur == other {
				st.editing, st.value, st.verr = askEditModel, cfg.Model, ""
				return
			}
			for i, l := range labels[:len(ids)] {
				if l == cur && ids[i] != cfg.Model {
					set(ids[i])
				}
			}
		}
	})
}

func (a *app) askURLRow(c *mg.Context, cfg pilot.Config, svc pilot.Service) {
	st := &a.ask
	s := &a.settings
	var desc string
	switch {
	case svc.ID == pilot.CustomAnthropic:
		desc = locales.SettingsAskURLDescAnthropic()
	case svc.ID == pilot.CustomOpenAI:
		desc = locales.SettingsAskURLDescOpenAI()
	case svc.Local:
		desc = locales.SettingsAskURLDescLocal()
	default:
		desc = locales.SettingsAskURLDescService()
	}
	def := nonEmpty(svc.BaseURL, locales.SettingsAskURLDefault())
	if st.editing == askEditURL {
		a.askEditor(c, locales.SettingsAskURL(), desc, false, def, func(v string) {
			v = strings.TrimRight(strings.TrimSpace(v), "/")
			if v != "" && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "http://") {
				st.verr = locales.SettingsAskURLErr()
				return
			}
			if v == svc.BaseURL {
				v = "" // the service's own: follow it
			}
			s.setPilotURL(svc.ID, v)
			st.editing = ""
			a.changed()
			a.runCheck(s.PilotConfig())
		})
		return
	}
	a.askValueRow(c, locales.SettingsAskURL(), desc, nonEmpty(cfg.BaseURL, def),
		locales.SettingsAskURLChange(), askEditURL, cfg.BaseURL)
}

// checkSig names the model a check is of.
func checkSig(cfg pilot.Config) string {
	return cfg.Service + "\n" + cfg.BaseURL + "\n" + cfg.Model
}

// checkWait bounds a check from Settings: the request, and the keychain
// before it.
const checkWait = 90 * time.Second

// runCheck checks the model of cfg off the main thread (PilotChecker),
// as each key, model or address is saved.
func (a *app) runCheck(cfg pilot.Config) {
	ck, ok := a.opt.Pilot.(PilotChecker)
	if !ok {
		return
	}
	st := &a.ask
	sig := checkSig(cfg)
	st.checkRun = sig
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), checkWait)
		defer cancel()
		r := ck.Check(ctx, cfg)
		a.post(func() {
			if st.checkRun != sig {
				return // another check started since
			}
			st.checkRun, st.check, st.checkFor = "", &r, sig
			if r.Problem == pilot.CheckNoKey || r.Problem == pilot.CheckBadKey {
				a.forgetKey(cfg.KeyName())
			}
			st.usageAt = time.Time{} // the check used a few tokens
		})
	}()
}

// askCheckRow checks the model now, and says how the last check of it
// went.
func (a *app) askCheckRow(c *mg.Context, cfg pilot.Config) {
	if _, ok := a.opt.Pilot.(PilotChecker); !ok {
		return
	}
	p := pal(c)
	st := &a.ask
	sig := checkSig(cfg)
	var res *pilot.CheckResult
	if st.checkFor == sig {
		res = st.check
	}
	settingRow(c, locales.SettingsAskCheck(), locales.SettingsAskCheckDesc(), false, func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Shrink(0).Children(func() {
			if st.checkRun == sig {
				spinner(c)
				mg.Text(c, locales.SettingsAskChecking()).FontSize(fsSmall).TextColor(p.ink2).Role(mg.RoleStatus)
				return
			}
			label := locales.SettingsAskCheckNow()
			if res != nil {
				label = locales.SettingsAskCheckAgain()
			}
			if button(c, label, btnOpt{small: true}).Label(locales.SettingsAskCheckLabel()).Clicked() {
				a.runCheck(cfg)
			}
		})
	})
	if res == nil || st.checkRun == sig {
		return
	}
	mg.Column(c).Gap(2).Margin(0, sp4, sp2, sp4).Role(mg.RoleStatus).Children(func() {
		mg.Row(c).Gap(6).AlignItems(mg.Center).Children(func() {
			if res.OK {
				mg.Icon(c, icCheck).FontSize(iconSmall).TextColor(p.green)
				mg.Text(c, checkText(*res)).FontSize(fsSmall).LineHeight(1.4).Grow(1).MinWidth(0)
				return
			}
			mg.Text(c, checkText(*res)).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Grow(1).MinWidth(0)
		})
		if d := checkDetail(*res); d != "" {
			mg.Text(c, locales.SettingsAskCheckDetail(d)).FontSize(fsCaption).LineHeight(1.4).TextColor(p.ink3).Selectable()
		}
	})
}

// checkText says how a check went, in the language shown.
func checkText(r pilot.CheckResult) string {
	switch r.Problem {
	case "":
		return locales.SettingsAskCheckOK(strconv.FormatFloat(float64(r.Millis)/1000, 'f', 1, 64))
	case pilot.CheckNoModel:
		return locales.CheckNoModel()
	case pilot.CheckNoKey:
		return locales.CheckNoKey()
	case pilot.CheckBadKey:
		return locales.CheckBadKey()
	case pilot.CheckNoCredit:
		return locales.CheckNoCredit()
	case pilot.CheckNotFound:
		return locales.CheckNotFound()
	case pilot.CheckBusy:
		return locales.CheckBusy()
	case pilot.CheckNoTools:
		return locales.CheckNoTools()
	case pilot.CheckUnreachable:
		return locales.CheckUnreachable()
	case pilot.CheckBadAnswer:
		return locales.CheckBadAnswer()
	}
	return locales.CheckRejected()
}

// checkDetail is what the service said of a failed check, with its HTTP
// status; empty when it said nothing worth showing.
func checkDetail(r pilot.CheckResult) string {
	if r.OK || r.Detail == "" || r.Problem == pilot.CheckNoTools && r.Status == 0 {
		return ""
	}
	if r.Status != 0 {
		return fmt.Sprintf("HTTP %d · %s", r.Status, r.Detail)
	}
	return r.Detail
}

// usageRefresh is how often the client reads usage again.
const usageRefresh = 10 * time.Second

// usageRows are the rows of the last 30 days (PilotUsage), as last read;
// it reads them again off the main thread when they are old.
func (a *app) usageRows() ([]pilot.UsageRow, bool) {
	pu, ok := a.opt.Pilot.(PilotUsage)
	if !ok {
		return nil, false
	}
	st := &a.ask
	if !st.usageBusy && time.Since(st.usageAt) > usageRefresh {
		st.usageBusy = true
		since := time.Now().AddDate(0, 0, -29).Format(time.DateOnly)
		go func() {
			rows := pu.Usage(since)
			a.post(func() {
				st.usage, st.usageAt, st.usageBusy = rows, time.Now(), false
			})
		}()
	}
	return st.usage, true
}

// usageSince sums the rows of the days from since on.
func usageSince(rows []pilot.UsageRow, since string) pilot.UsageTotal {
	var in []pilot.UsageRow
	for _, r := range rows {
		if r.Day >= since {
			in = append(in, r)
		}
	}
	return pilot.Total(in)
}

// usageToday is what the model used today, for the box; false when
// nothing.
func (a *app) usageToday() (string, bool) {
	rows, ok := a.usageRows()
	if !ok {
		return "", false
	}
	t := usageSince(rows, time.Now().Format(time.DateOnly))
	if t.Tokens == 0 {
		return "", false
	}
	return amountText(t.Tokens, t.Cost, t.Unpriced), true
}

// amountText is tokens and, when some had known prices, what they cost:
// "48,210 tokens · US$0.214".
func amountText(tokens int64, cost float64, unpriced int64) string {
	s := locales.AskTokens(int(tokens))
	if unpriced < tokens {
		prec := 2
		if cost < 1 {
			prec = 3
		}
		s += " · " + locales.AskDollars(strconv.FormatFloat(cost, 'f', prec, 64))
	}
	return s
}

// usageDays is how many days of rows Usage lists one by one.
const usageDays = 7

// askUsage is what the model used: today, the last 7 and 30 days, and the
// last days by service and model.
func (a *app) askUsage(c *mg.Context) {
	rows, ok := a.usageRows()
	if !ok {
		return
	}
	p := pal(c)
	now := time.Now()
	subHeading(c, locales.SettingsAskUsage(), locales.SettingsAskUsageDesc())
	group(c).Label(locales.SettingsAskUsage()).Children(func() {
		periods := []struct {
			title string
			days  int
		}{{locales.DayToday(), 0}, {locales.SettingsAskUsageWeek(), 6}, {locales.SettingsAskUsageMonth(), 29}}
		for i, pr := range periods {
			t := usageSince(rows, now.AddDate(0, 0, -pr.days).Format(time.DateOnly))
			v := locales.SettingsAskUsageNone()
			if t.Tokens > 0 {
				v = amountText(t.Tokens, t.Cost, t.Unpriced)
			}
			settingRow(c, pr.title, "", i == len(periods)-1, func() {
				mg.Text(c, v).FontSize(fsSmall).TextColor(p.ink2).FontFeatures("tnum")
			})
		}
	})
	var days []string
	for _, r := range rows {
		if len(days) == 0 || days[len(days)-1] != r.Day {
			days = append(days, r.Day)
		}
	}
	if len(days) > usageDays {
		days = days[:usageDays]
	}
	if len(days) == 0 {
		return
	}
	group(c).Children(func() {
		for i, day := range days {
			t, err := time.ParseInLocation(time.DateOnly, day, time.Local)
			if err != nil {
				continue
			}
			groupRow(c, mg.Column(c).Gap(6), i == len(days)-1).Children(func() {
				mg.Text(c, dayLabel(t, now)).FontWeight(fwMedium)
				for _, r := range rows {
					if r.Day != day {
						continue
					}
					mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
						mg.Text(c, usageWho(r)).FontSize(fsSmall).TextColor(p.ink2).SingleLine().Grow(1).Shrink(1).MinWidth(0)
						mg.Text(c, locales.SettingsAskUsageRequests(r.Requests)+" · "+amountText(r.Tokens(), r.Cost, r.Unpriced)).
							FontSize(fsSmall).TextColor(p.ink2).FontFeatures("tnum").Shrink(0)
					})
				}
			})
		}
	})
}

// usageWho is the service and model of a row: "DeepSeek · deepseek-chat".
func usageWho(r pilot.UsageRow) string {
	who := r.Host
	if svc, ok := pilot.ServiceByID(r.Service); ok && (who == "" || !svc.Custom) {
		who = serviceLabel(svc)
	}
	if who == "" {
		who = r.Service
	}
	return who + " · " + modelName(pilot.Config{Service: r.Service, Model: r.Model})
}

// askValueRow is a setting with its value and Change.
func (a *app) askValueRow(c *mg.Context, title, desc, value, changeLabel, editor, current string) {
	p := pal(c)
	st := &a.ask
	settingRow(c, title, desc, false, func() {
		mg.Row(c).Gap(10).AlignItems(mg.Center).Shrink(1).MinWidth(0).Children(func() {
			mg.Text(c, value).FontSize(fsSmall).TextColor(p.ink2).SingleLine().Shrink(1).MinWidth(0).MaxWidth(260)
			if button(c, locales.SettingsChange(), btnOpt{small: true}).Label(changeLabel).Clicked() {
				st.editing, st.value, st.verr = editor, current, ""
			}
		})
	})
}

// askEditor is a setting being edited: a field with Save and Cancel.
func (a *app) askEditor(c *mg.Context, title, desc string, password bool, placeholder string, save func(string)) {
	p := pal(c)
	st := &a.ask
	groupRow(c, mg.Column(c).Gap(sp3), false).Children(func() {
		rowWords(c, mg.Column(c), title, desc)
		in := mg.TextInput(c.Key(fmt.Sprintf("ask-edit-%s-%d", st.editing, st.gen)), &st.value).Label(title).
			Placeholder(placeholder).AutoFocus().MaxWidth(420)
		if password {
			in.Password()
		}
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			empty := password && strings.TrimSpace(st.value) == ""
			ok := button(c, locales.SettingsSave(), btnOpt{small: true, kind: btnPrimary}).Disabled(st.busy || empty)
			if (ok.Clicked() || in.Submitted()) && !st.busy && !empty {
				save(st.value)
			}
			if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
				st.editing, st.value, st.verr = "", "", ""
				st.gen++
			}
			if st.busy {
				spinner(c)
			}
		})
		if st.verr != "" {
			mg.Text(c, st.verr).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

// The limits Settings offers; one from the settings file that is not
// among them is offered too.
var (
	stepChoices   = []int{25, 50, 100, 200, 500}
	minuteChoices = []int{5, 10, 20, 30, 60}
	costChoices   = []float64{0.5, 1, 2, 5, 10}
)

func (a *app) askLimitRows(c *mg.Context, cfg pilot.Config) {
	s := &a.settings
	limitRow(c, "ask-steps", locales.SettingsAskSteps(), locales.SettingsAskStepsDesc(), false,
		stepChoices, cfg.MaxSteps, func(v int) string { return locales.AskSteps(v) },
		func(v int) { s.PilotMaxSteps = v; a.changed() })
	limitRow(c, "ask-time", locales.SettingsAskTime(), locales.SettingsAskTimeDesc(), false,
		minuteChoices, int(cfg.MaxTime/time.Minute), func(v int) string { return locales.AskMinutes(v) },
		func(v int) { s.PilotMaxMinutes = v; a.changed() })
	desc := locales.SettingsAskCostDesc()
	if !cfg.Priced() {
		desc = locales.SettingsAskCostDescTokens()
	}
	limitRow(c, "ask-cost", locales.SettingsAskCost(), desc, true,
		costChoices, cfg.MaxCost, dollars,
		func(v float64) { s.PilotMaxCost = v; a.changed() })
}

// limitRow is a limit chosen from choices.
func limitRow[T comparable](c *mg.Context, key, title, desc string, last bool, choices []T, cur T, label func(T) string, set func(T)) {
	settingRow(c, title, desc, last, func() {
		found := false
		for _, v := range choices {
			found = found || v == cur
		}
		if !found {
			choices = append([]T{cur}, choices...)
		}
		labels := make([]string, len(choices))
		for i, v := range choices {
			labels[i] = label(v)
		}
		sel := label(cur)
		if mg.Select(c.Key(key), &sel, labels).Label(title).Changed() {
			for i, l := range labels {
				if l == sel {
					set(choices[i])
				}
			}
		}
	})
}
