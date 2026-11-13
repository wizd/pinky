package ui

import (
	"strings"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/pilot"
)

// clickSeen clicks name, first scrolling it into the window.
func clickSeen(t *testing.T, tt *mg.Tester, name string) {
	t.Helper()
	for range 5 {
		err := tt.Click(name)
		if err == nil {
			tt.Frame()
			return
		}
		r, ok := tt.Access(name)
		if !ok || !strings.Contains(err.Error(), "outside the window") {
			t.Fatal(err)
		}
		b := r.Bounds
		tt.Scroll(b.X+b.W/2, 400, 0, b.Y-400)
		tt.Frame()
	}
	t.Fatalf("%q stays outside the window", name)
}

// After a task, the box asks whether to remember what it suggested; the
// person keeps one and not the other.
func TestAskPinkySuggestions(t *testing.T) {
	ta, fp, tt := newAskApp(t, true, true)
	id := ta.devices[0].Info.ID
	fp.mu.Lock()
	fp.status[id] = pilot.Status{Device: id, Task: "Open Calculator", End: pilot.Done, Ended: time.Now(), Steps: 2,
		Suggested: []pilot.Suggestion{
			{Kind: pilot.Said, Text: "Uses Calculator for tips"},
			{Kind: pilot.Learned, Text: "Clear with AC before a new sum", App: "Calculator"},
		}}
	fp.mu.Unlock()
	frameUntil(t, ta, tt, "Remember for later tasks?")
	for _, want := range []string{"Uses Calculator for tips", "From you", "Learned in Calculator"} {
		if !hasText(tt, want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	clickSeen(t, tt, "Remember")
	frameUntil(t, ta, tt, "Remembered")
	clickSeen(t, tt, "Don't")
	frameUntil(t, ta, tt, "Not remembered")
	if l := fp.mem.List(); len(l) != 1 || l[0].Text != "Uses Calculator for tips" || l[0].Kind != pilot.Said {
		t.Fatalf("memory: %+v", l)
	}
}

// Settings › Agents lists the notes, to add, change and delete.
func TestMemorySettings(t *testing.T) {
	ta, fp, tt := newAskApp(t, true, true)
	fp.mem.Add(pilot.Learned, "Orders are under the 「我的」 tab", "Meituan")
	ta.page, ta.set.sec = pageSettings, secAgents
	frameUntil(t, ta, tt, "Orders are under the 「我的」 tab")
	if !hasText(tt, "Learned in Meituan") {
		t.Errorf("no source in %q", tt.Texts())
	}

	// Add one, and see a sensitive one refused.
	clickSeen(t, tt, "Add…")
	tt.Type("My PIN is 4321")
	tt.Frame()
	clickSeen(t, tt, "Save")
	frameUntil(t, ta, tt, "Memory never keeps passwords")
	ta.mem.value = "Prefers window seats"
	tt.Frame()
	clickSeen(t, tt, "Save")
	frameUntil(t, ta, tt, "Prefers window seats")
	if l := fp.mem.List(); len(l) != 2 || l[0].Text != "Prefers window seats" || l[0].Kind != pilot.Said {
		t.Fatalf("after Add: %+v", l)
	}

	// Edit it.
	clickSeen(t, tt, "Edit: Prefers window seats")
	ta.mem.value = "Prefers aisle seats"
	tt.Frame()
	clickSeen(t, tt, "Save")
	frameUntil(t, ta, tt, "Prefers aisle seats")

	// Delete one, then all.
	clickSeen(t, tt, "Delete: Prefers aisle seats")
	if l := fp.mem.List(); len(l) != 1 {
		t.Fatalf("after Delete: %+v", l)
	}
	clickSeen(t, tt, "Delete All")
	if !ta.mem.confirm || !hasText(tt, "Delete all of Ask Pinky's memory?") {
		t.Fatalf("Delete All did not ask first: %q", tt.Texts())
	}
	tt.Key(0, mg.KeyEscape)
	tt.Frame()
	if len(fp.mem.List()) != 1 {
		t.Fatal("Escape deleted the notes")
	}
	clickSeen(t, tt, "Delete All")
	tt.Key(0, mg.KeyEnter) // the dialog's Delete All
	tt.Frame()
	frameUntil(t, ta, tt, "Nothing yet.")
	if l := fp.mem.List(); len(l) != 0 {
		t.Fatalf("after Delete All: %+v", l)
	}

	// Turned off, tasks leave the notes out.
	clickSeen(t, tt, "Use memory")
	if !ta.settings.PilotNoMemory || !ta.settings.PilotConfig().NoMemory {
		t.Error("Use memory off does not reach the tasks")
	}
}
