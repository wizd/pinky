package ui

import (
	"errors"
	"fmt"
	"strings"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/pilot"
)

// memState is what Pinky's own agent remembers, as Settings › Agents ›
// Ask Pinky › Memory shows it, and the box's question after a task.
type memState struct {
	editing string // the memory being edited, memAdd for a new one
	value   string // what is typed
	err     string // why it was not saved
	errFor  string // the memory or suggestion the error is about
	gen     int    // bumped on each edit, to start the field afresh
	confirm bool   // Delete all asks first
}

// memAdd is the editor of a new memory.
const memAdd = "+"

// memoryError is why a memory was not kept, in the person's words.
func memoryError(err error) string {
	switch {
	case errors.Is(err, pilot.ErrSensitive):
		return locales.MemoryErrSensitive()
	case errors.Is(err, pilot.ErrMemoryLong):
		return locales.MemoryErrLong(pilot.MaxMemoryChars)
	case errors.Is(err, pilot.ErrMemoryFull):
		return locales.MemoryErrFull(pilot.MaxMemories)
	}
	return locales.MemoryErrSave(err.Error())
}

// memorySource is where a memory came from: the person, or a task in an
// app.
func memorySource(kind pilot.MemoryKind, app string) string {
	switch {
	case kind != pilot.Learned:
		return locales.MemoryFromYou()
	case app != "":
		return locales.MemoryLearnedIn(app)
	}
	return locales.MemoryLearned()
}

// askSuggestions asks, after a task, whether to remember what it
// suggested.
func (a *app) askSuggestions(c *mg.Context, s pilot.Status) {
	if len(s.Suggested) == 0 || a.opt.Pilot == nil || a.opt.Pilot.Memories() == nil {
		return
	}
	p := pal(c)
	st := &a.mem
	mg.Column(c.Key("ask-memory")).Gap(sp2).Margin(sp2, 0, 0, 0).Label(locales.AskMemoryTitle()).Children(func() {
		mg.Text(c, locales.AskMemoryTitle()).FontSize(fsSmall).FontWeight(fwSemibold)
		for i, sg := range s.Suggested {
			key := fmt.Sprintf("ask-memory-%d", i)
			mg.Column(c.Key(key)).Gap(sp1).Children(func() {
				mg.Text(c, sg.Text).FontSize(fsSmall).LineHeight(1.4).Selectable()
				mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
					mg.Text(c, memorySource(sg.Kind, sg.App)).FontSize(fsCaption).TextColor(p.ink3).Grow(1).MinWidth(0)
					switch {
					case sg.Kept != nil:
						mg.Text(c, locales.AskMemoryKept()).FontSize(fsCaption).TextColor(p.green)
					case sg.Dropped:
						mg.Text(c, locales.AskMemoryDropped()).FontSize(fsCaption).TextColor(p.ink3)
					default:
						if button(c, locales.AskMemoryDrop(), btnOpt{small: true}).Clicked() {
							_ = a.opt.Pilot.Drop(s.Device, i)
						}
						if button(c, locales.AskMemoryKeep(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
							st.err, st.errFor = "", ""
							if err := a.opt.Pilot.Keep(s.Device, i); err != nil {
								st.err, st.errFor = memoryError(err), key
							}
						}
					}
				})
				if st.err != "" && st.errFor == key {
					mg.Text(c, st.err).FontSize(fsCaption).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
				}
			})
		}
	})
}

// memorySettings is Ask Pinky's memory in Settings › Agents: whether tasks
// use it, and every memory, to change or delete.
func (a *app) memorySettings(c *mg.Context) {
	if a.opt.Pilot == nil {
		return
	}
	mem := a.opt.Pilot.Memories()
	if mem == nil {
		return
	}
	p := pal(c)
	st := &a.mem
	s := &a.settings
	list := mem.List()
	mg.Column(c.Key("memory-settings")).Gap(10).Label(locales.SettingsMemory()).Children(func() {
		settingsHeading(c, locales.SettingsMemory(), locales.SettingsMemoryIntro())
		group(c).Children(func() {
			on := !s.PilotNoMemory
			if a.toggleRowFor(c, "memory-on", locales.SettingsMemoryOn(), locales.SettingsMemoryOnDesc(), false, &on) {
				s.PilotNoMemory = !on
				a.changed()
			}
			if len(list) == 0 && st.editing != memAdd {
				groupRow(c, mg.Column(c), false).Children(func() {
					mg.Text(c, locales.SettingsMemoryEmpty()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
				})
			}
			for _, m := range list {
				if st.editing == m.ID {
					a.memoryEditor(c, func(text string) error { return mem.Edit(m.ID, text) })
					continue
				}
				groupRow(c, mg.Row(c.Key("memory-"+m.ID)).Gap(sp4).AlignItems(mg.Center), false).Children(func() {
					mg.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						mg.Text(c, m.Text).LineHeight(1.4).Selectable()
						mg.Text(c, memorySource(m.Kind, m.App)).FontSize(fsCaption).TextColor(p.ink3)
					})
					if button(c, locales.SettingsMemoryEdit(), btnOpt{small: true}).Label(locales.SettingsMemoryEdit() + ": " + m.Text).Clicked() {
						st.editing, st.value, st.err = m.ID, m.Text, ""
						st.gen++
					}
					if button(c, locales.SettingsMemoryDelete(), btnOpt{small: true}).Label(locales.SettingsMemoryDelete() + ": " + m.Text).Clicked() {
						_ = mem.Delete(m.ID)
					}
				})
			}
			if st.editing == memAdd {
				a.memoryEditor(c, func(text string) error {
					_, err := mem.Add(pilot.Said, text, "")
					return err
				})
			}
			groupRow(c, mg.Row(c).Gap(sp2).AlignItems(mg.Center), true).Children(func() {
				add := button(c, locales.SettingsMemoryAdd(), btnOpt{small: true}).Disabled(len(list) >= pilot.MaxMemories)
				if add.Clicked() {
					st.editing, st.value, st.err = memAdd, "", ""
					st.gen++
				}
				mg.Row(c).Grow(1)
				if len(list) > 0 && button(c, locales.SettingsMemoryDeleteAll(), btnOpt{small: true, kind: btnRed}).Clicked() {
					st.confirm = true
				}
			})
		})
	})
	if st.confirm {
		switch mg.AlertDialog(c, &st.confirm, locales.SettingsMemoryDeleteAllTitle(), locales.SettingsMemoryDeleteAllMessage(),
			locales.SettingsCancel(), locales.SettingsMemoryDeleteAll()) {
		case 1:
			_ = mem.DeleteAll()
			st.editing = ""
		}
	}
}

// memoryEditor is a memory being written: a field with Save and Cancel.
func (a *app) memoryEditor(c *mg.Context, save func(string) error) {
	p := pal(c)
	st := &a.mem
	groupRow(c, mg.Column(c).Gap(sp3), false).Children(func() {
		in := mg.TextInput(c.Key(fmt.Sprintf("memory-edit-%d", st.gen)), &st.value).Label(locales.SettingsMemory()).
			Placeholder(locales.SettingsMemoryPlaceholder()).AutoFocus()
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			empty := strings.TrimSpace(st.value) == ""
			ok := button(c, locales.SettingsSave(), btnOpt{small: true, kind: btnPrimary}).Disabled(empty)
			if (ok.Clicked() || in.Submitted()) && !empty {
				if err := save(st.value); err != nil {
					st.err = memoryError(err)
				} else {
					st.editing, st.value, st.err = "", "", ""
				}
			}
			if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
				st.editing, st.value, st.err = "", "", ""
			}
		})
		if st.err != "" {
			mg.Text(c, st.err).FontSize(fsSmall).LineHeight(1.4).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}
