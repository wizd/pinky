package ui

import (
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	mg "github.com/egoist/mygo/ui"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// "Only the apps I choose", on a first-connection card and under an agent
// in Settings › Agents, lists the apps on the connected phones to tick, as
// Settings › Safety › Add app lists them to block: the person need not know
// how the phone spells an app's name, and a name that matches no app cannot
// be saved by mistake. The apps the agent named for its task come ticked.
// Apps a rule blocks are listed too, greyed: no scope opens them.
//
// The list holds every app, in a box that scrolls, in the order of the
// app's language (by pinyin in Chinese), with a field above it that narrows
// it down and, below it, how many apps are chosen: a link to those alone.
// The apps the list opened with, those the agent named or the scope had,
// come first and stay there, so that a tick does not move a row under the
// pointer.
//
// What is chosen stays what the field kept, names with commas between
// (agentsState.apps), so a scope is stored and matched as before. While no
// phone lists its apps, the field itself shows.

// appsListHeight is how tall the list of apps gets before it scrolls:
// about eight rows. A list of more rows than appsNarrowFrom has the field
// that narrows it down.
const (
	appsListHeight = 236
	appsNarrowFrom = 8
)

// cardKept is how long what the person chose on a first-connection card
// that went unanswered waits for the agent's next one.
const cardKept = 10 * time.Minute

// leftCard is a first-connection card that went unanswered: the need
// whose choices agentsState still holds, and when it went.
type leftCard struct {
	id string
	at time.Time
}

// makeMaps makes the maps the scope picker writes to.
func (s *agentsState) makeMaps() {
	if s.choice == nil {
		s.choice = map[string]string{}
	}
	if s.apps == nil {
		s.apps = map[string]string{}
	}
	if s.find == nil {
		s.find, s.first, s.missing, s.asked = map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
		s.only, s.done, s.left = map[string]bool{}, map[string]bool{}, map[string]leftCard{}
	}
}

// reopen makes the list of key open anew, from the apps chosen now.
func (s *agentsState) reopen(key string) {
	delete(s.find, key)
	delete(s.first, key)
	delete(s.missing, key)
	delete(s.only, key)
}

// forget drops what was chosen for key.
func (s *agentsState) forget(key string) {
	delete(s.choice, key)
	delete(s.apps, key)
	delete(s.asked, key)
	delete(s.done, key)
	s.reopen(key)
}

// askedFor notes the apps an agent's first connection named, for the card
// of the need id to mark them.
func (s *agentsState) askedFor(id string, apps []string) {
	if len(apps) > 0 {
		s.makeMaps()
		s.asked[id] = strings.Join(apps, ", ")
	}
}

// answered notes that the person answered a need, for cardsChanged.
func (s *agentsState) answered(n gateway.NeedView) {
	if s.done != nil && newAgentNeed(n) {
		s.done[n.ID] = true
	}
}

// cardsChanged follows the first-connection cards as the needs change from
// old to now. A card left unanswered is denied after a minute, and an
// agent that asks again gets a new one: what the person chose on the old
// card (the choice, the ticks, what narrowed the list) goes to the new
// one, when it comes within cardKept. A card the person answered is
// forgotten.
func (s *agentsState) cardsChanged(old, now []gateway.NeedView) {
	if s.find == nil {
		return // no card was shown yet
	}
	open := func(list []gateway.NeedView, id string) bool {
		return slices.ContainsFunc(list, func(n gateway.NeedView) bool { return n.ID == id })
	}
	for _, n := range old {
		if !newAgentNeed(n) || open(now, n.ID) {
			continue
		}
		if _, shown := s.choice[n.ID]; s.done[n.ID] || !shown || n.Agent == "" {
			s.forget(n.ID)
			continue
		}
		if prev, ok := s.left[n.Agent]; ok {
			s.forget(prev.id)
		}
		s.left[n.Agent] = leftCard{id: n.ID, at: time.Now()}
	}
	for _, n := range now {
		prev, ok := s.left[n.Agent]
		if !ok || !newAgentNeed(n) || open(old, n.ID) {
			continue
		}
		delete(s.left, n.Agent)
		if _, shown := s.choice[n.ID]; !shown && time.Since(prev.at) < cardKept {
			s.choice[n.ID] = s.choice[prev.id]
			for _, m := range []map[string]string{s.apps, s.find, s.first, s.missing} {
				if v, ok := m[prev.id]; ok {
					m[n.ID] = v
				}
			}
			if s.only[prev.id] {
				s.only[n.ID] = true
			}
		}
		s.forget(prev.id)
	}
}

// appsSorted is the phones' apps in the order of a language, kept until
// the apps are read again or the language changes.
type appsSorted struct {
	list []gateway.InstalledApp
	from *gateway.InstalledApp // the first of the apps as they were read: another read has another
	n    int
	lang string
}

// sortedApps returns the phones' apps by name in the order of the app's
// language: by code point, as they come, Chinese names are scattered.
func (a *app) sortedApps() []gateway.InstalledApp {
	src := a.apps.list
	if len(src) == 0 {
		return nil
	}
	s := &a.agents.sorted
	lang := i18n.Locale()
	if s.from == &src[0] && s.n == len(src) && s.lang == lang {
		return s.list
	}
	tag, err := language.Parse(lang)
	if err != nil {
		tag = language.English
	}
	col := collate.New(tag, collate.IgnoreCase)
	list := slices.Clone(src)
	sort.SliceStable(list, func(i, j int) bool {
		return col.CompareString(nonEmpty(list[i].Name, list[i].ID), nonEmpty(list[j].Name, list[j].ID)) < 0
	})
	s.list, s.from, s.n, s.lang = list, &src[0], len(src), lang
	return list
}

// appRow is one line of the list: an app, or the apps of one name on
// several phones, which a scope names together.
type appRow struct {
	name    string // as shown
	keep    string // what a tick keeps: the name, as the phone shows it and as scopes match it, or the id when the name would not do
	apps    []gateway.InstalledApp
	blocked bool // a rule blocks every app of the row
}

// appRows groups the phones' apps by name, in the order of list.
func appRows(list []gateway.InstalledApp) []appRow {
	var rows []appRow
	at := map[string]int{}
	for _, x := range list {
		if x.Name == "" || strings.ContainsAny(x.Name, ",，、") {
			// No name, or one the stored list would split: kept by id.
			rows = append(rows, appRow{name: nonEmpty(x.Name, x.ID), keep: x.ID, apps: []gateway.InstalledApp{x}, blocked: x.Blocked})
			continue
		}
		k := strings.ToLower(x.Name)
		i, ok := at[k]
		if !ok {
			at[k] = len(rows)
			rows = append(rows, appRow{name: x.Name, keep: x.Name, apps: []gateway.InstalledApp{x}, blocked: x.Blocked})
			continue
		}
		rows[i].apps = append(rows[i].apps, x)
		rows[i].blocked = rows[i].blocked && x.Blocked
	}
	return rows
}

// named reports whether entry, a name or an id as typed, stored or given
// by an agent, names an app of the row.
func (r appRow) named(entry string) bool {
	q := strings.ToLower(strings.TrimSpace(entry))
	return slices.ContainsFunc(r.apps, func(x gateway.InstalledApp) bool { return appIs(x, q) })
}

// namedBy reports whether one of entries names an app of the row.
func (r appRow) namedBy(entries []string) bool {
	return slices.ContainsFunc(entries, r.named)
}

// holds reports whether q (lower case) is part of a name or id of the row.
func (r appRow) holds(q string) bool {
	return slices.ContainsFunc(r.apps, func(x gateway.InstalledApp) bool { return appHolds(x, q) })
}

// readApps reads the phones' apps again when what is known is older than
// half a minute, and reports whether Pinky can list them at all.
func (a *app) readApps() bool {
	l, canList := a.api.(installedAppLister)
	if canList && (a.apps.at.IsZero() || time.Since(a.apps.at) > 30*time.Second) {
		a.apps.load(a, l)
	}
	return canList
}

// appsPicker is the list of apps under "Only the apps I choose" for key, an
// agent or a first-connection need. It returns the apps chosen.
func (a *app) appsPicker(c *mg.Context, key string) []string {
	p := pal(c)
	st := &a.agents
	canList := a.readApps()
	if canList && !a.apps.loaded {
		// The apps are being read. No field for typed names meanwhile: the
		// list would take its place a moment later.
		mg.Text(c, locales.SettingsSafetyAppsLoading()).FontSize(fsSmall).TextColor(p.ink3).MaxWidth(360)
		return splitApps(st.apps[key])
	}
	var rows []appRow
	if canList {
		rows = appRows(a.notListed(a.sortedApps()))
	}
	if len(rows) == 0 {
		// No phone lists its apps: the names are typed.
		apps := st.apps[key]
		mg.TextInput(c.Key("apps-"+key), &apps).Placeholder(locales.SettingsAgentsAppsPlaceholder()).Label(locales.SettingsAgentsApps()).MaxWidth(360)
		st.apps[key] = apps
		if canList {
			mg.Text(c, locales.SettingsSafetyAppsNoPhone()).FontSize(fsSmall).TextColor(p.ink3).MaxWidth(360)
		}
		return splitApps(apps)
	}

	// What is chosen: rows of the list, and names that are no app on the
	// phones now (typed before, or of a phone that is not connected), which
	// stay chosen until unticked.
	entries := splitApps(st.apps[key])
	on := make([]bool, len(rows))
	var other []string
	for _, e := range entries {
		found := false
		for i, r := range rows {
			if r.named(e) {
				found = true
				on[i] = !r.blocked
			}
		}
		if !found && !slices.Contains(other, e) {
			other = append(other, e)
		}
	}
	asked := splitApps(st.asked[key])
	if _, ok := st.first[key]; !ok {
		// The list opens. An app the agent named that no phone has (an
		// English name on a phone that shows the app under its Chinese
		// one) is shown, so that the person sees what was asked for, but
		// is not chosen: its name would open nothing.
		var gone []string
		other = slices.DeleteFunc(other, func(e string) bool {
			if !slices.ContainsFunc(asked, func(a string) bool { return strings.EqualFold(a, e) }) {
				return false
			}
			gone = append(gone, e)
			return true
		})
		st.missing[key] = strings.Join(gone, ", ")
		st.first[key] = st.apps[key]
	}
	first := splitApps(st.first[key])
	missing := splitApps(st.missing[key])

	q := st.find[key]
	mg.Column(c).Gap(6).MaxWidth(360).Label(locales.SettingsAgentsApps()).Children(func() {
		if len(rows) > appsNarrowFrom {
			mg.TextInput(c.Key("find-apps-"+key), &q).Placeholder(locales.SettingsAgentsAppsSearch()).Label(locales.SettingsAgentsAppsSearch())
			st.find[key] = q
		}
		ql := strings.ToLower(strings.TrimSpace(q))
		// The apps the list opened with first, then the rest in order,
		// blocked ones last; then the names that are no app on the phones.
		var order []int
		for pass := 0; pass < 3; pass++ {
			for i, r := range rows {
				if (ql != "" && !r.holds(ql)) || (st.only[key] && !on[i]) {
					continue
				}
				top := !r.blocked && r.namedBy(first)
				if (pass == 0 && top) || (pass == 1 && !top && !r.blocked) || (pass == 2 && r.blocked) {
					order = append(order, i)
				}
			}
		}
		narrowed := func(names []string) []string {
			return slices.DeleteFunc(slices.Clone(names), func(e string) bool { return ql != "" && !strings.Contains(strings.ToLower(e), ql) })
		}
		others, missed := narrowed(other), narrowed(missing)
		if st.only[key] {
			missed = nil
		}
		if len(order)+len(others)+len(missed) == 0 {
			mg.Text(c, locales.SettingsAgentsAppsNone()).FontSize(fsSmall).TextColor(p.ink3).Role(mg.RoleStatus)
		} else {
			mg.Scroll(c.Key("apps-list-"+key)).MaxHeight(appsListHeight).Padding(8, 10).Gap(6).Radius(8).Background(p.panel).Border(1, p.line).Children(func() {
				row := func(id, name, note string, v *bool, off bool) bool {
					changed := false
					mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
						changed = mg.Checkbox(c.Key("app-"+key+"-"+id), v, name).Disabled(off).Changed() && !off
						if note != "" {
							mg.Text(c, note).FontSize(fsCaption).TextColor(p.ink3)
						}
					})
					return changed
				}
				for _, i := range order {
					r, v, note := rows[i], on[i], ""
					switch {
					case r.blocked:
						note = locales.SettingsAgentsAppsBlocked()
					case r.namedBy(asked):
						note = locales.SettingsAgentsAppsAsked()
					}
					if row(r.apps[0].ID, r.name, note, &v, r.blocked) {
						on[i] = v
					}
				}
				for _, e := range others {
					v := true
					if row("other-"+e, e, locales.SettingsAgentsAppsMissing(), &v, false) && !v {
						other = slices.DeleteFunc(other, func(o string) bool { return o == e })
					}
				}
				for _, e := range missed {
					v := false
					row("missing-"+e, e, locales.SettingsAgentsAppsMissing(), &v, true)
				}
			})
		}
		chosen := len(other)
		for _, v := range on {
			if v {
				chosen++
			}
		}
		// The count is a link to the apps chosen alone, and back: in a long
		// list they are not all in sight.
		switch {
		case chosen == 0:
			delete(st.only, key)
			mg.Text(c, locales.SettingsAgentsAppsChosen(chosen)).FontSize(fsCaption).TextColor(p.ink3)
		case st.only[key]:
			if linkButton(c.Key("all-apps-"+key), locales.SettingsAgentsAppsShowAll()).AlignSelf(mg.Start).Clicked() {
				delete(st.only, key)
			}
		default:
			if linkButton(c.Key("chosen-apps-"+key), locales.SettingsAgentsAppsChosen(chosen)).AlignSelf(mg.Start).Clicked() {
				st.only[key] = true
			}
		}
	})

	// The apps chosen, in the order they were chosen: those kept from
	// before as they stood, then those ticked now.
	var out []string
	took := make([]bool, len(rows))
	for _, e := range entries {
		named := false
		for i, r := range rows {
			if !r.named(e) {
				continue
			}
			named = true
			if on[i] && !took[i] {
				took[i] = true
				out = append(out, r.keep)
			}
		}
		if !named && slices.Contains(other, e) && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	for i, r := range rows {
		if on[i] && !took[i] {
			out = append(out, r.keep)
		}
	}
	st.apps[key] = strings.Join(out, ", ")
	return out
}
