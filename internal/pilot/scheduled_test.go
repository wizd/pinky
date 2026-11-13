package pilot

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestScheduled(t *testing.T) {
	h := newHarness(t)
	ny, _ := time.LoadLocation("America/New_York")
	last := time.Date(2026, 11, 12, 8, 0, 0, 0, ny)
	h.api.turns = []func(string, *http.Request) any{
		func(first string, _ *http.Request) any {
			for _, want := range []string{
				"<task>\nCheck the weather\n</task>\n\nTell the person only if, in their words:\n<condition>\nit will rain\n</condition>",
				"The last time this task ran, you found (Thursday 12 November 2026, 08:00):\n<earlier>\nSunny all day.\n</earlier>",
				"The last time the condition held, you found",
				"(EST)",
			} {
				if !strings.Contains(first, want) {
					t.Errorf("first message without %q:\n%s", want, first)
				}
			}
			return message("tool_use", 1000, use("t1", finishName, map[string]any{
				"outcome": "done", "summary": "Rain from 3 pm.", conditionMet: true}))
		},
	}
	err := h.r.StartScheduled(Config{}, "pixel", "Check the weather", Scheduled{
		Unattended: true, Condition: "it will rain", Zone: ny,
		Last:    &Earlier{At: last, Summary: "Sunny all day."},
		LastMet: &Earlier{At: last.AddDate(0, 0, -3), Summary: "Showers in the evening."},
	})
	if err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Done || st.Met == nil || !*st.Met || st.Scheduled == nil {
		t.Fatalf("status: %+v", st)
	}

	b := h.api.requests()[0].body
	sys := fmt.Sprint(b["system"])
	for _, want := range []string{"This task was scheduled", "Never pay", "there is no phone_handoff", "condition_met"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt without %q", want)
		}
	}
	tools, _ := b["tools"].([]any)
	for _, tl := range tools {
		tl, _ := tl.(map[string]any)
		switch tl["name"] {
		case "phone_handoff":
			t.Error("phone_handoff offered to an unattended task")
		case finishName:
			if s := fmt.Sprint(tl["input_schema"]); !strings.Contains(s, "required:[outcome summary condition_met]") {
				t.Errorf("finish: %s", s)
			}
		}
	}
	// finishTool itself is left as it was.
	if _, ok := finishTool.schema["properties"].(map[string]any)[conditionMet]; ok {
		t.Error("finishTool changed")
	}
}

func TestScheduledAttended(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(first string, _ *http.Request) any {
			if strings.Contains(first, "<condition>") || strings.Contains(first, "<earlier>") {
				t.Errorf("first message:\n%s", first)
			}
			// Without condition_met, the condition did not hold.
			return message("tool_use", 1000, use("t1", finishName, map[string]any{"outcome": "done", "summary": "Dry."}))
		},
	}
	// Run now: the person is there; the condition is still asked about.
	if err := h.r.StartScheduled(Config{}, "pixel", "Check the weather", Scheduled{}); err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Done || st.Met != nil {
		t.Fatalf("status: %+v", st)
	}
	b := h.api.requests()[0].body
	if sys := fmt.Sprint(b["system"]); strings.Contains(sys, "Never pay") || !strings.Contains(sys, "This task was scheduled") {
		t.Errorf("system prompt: %s", sys)
	}
	found := false
	for _, tl := range b["tools"].([]any) {
		found = found || tl.(map[string]any)["name"] == "phone_handoff"
	}
	if !found {
		t.Error("no phone_handoff for a task the person is there for")
	}
}

// A scheduled task nobody watched, continued by the person, is theirs: no
// longer unattended, its condition answered, and the model told so.
func TestScheduledContinue(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", finishName, map[string]any{
				"outcome": "failed", "summary": "The shop wants a sign-in.", conditionMet: false}))
		},
		func(string, *http.Request) any {
			reqs := h.api.requests()
			blocks := lastUser(t, reqs[len(reqs)-1])
			text, _ := blocks[len(blocks)-1]["text"].(string)
			for _, want := range []string{"The person has continued it", attendedNote, "<message>\nI signed in, go on\n</message>"} {
				if !strings.Contains(text, want) {
					t.Errorf("continue message without %q:\n%s", want, text)
				}
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{
				"outcome": "done", "summary": "Ordered nothing; the price is 12.", conditionMet: false}))
		},
	}
	if err := h.r.StartScheduled(Config{}, "pixel", "Check the price", Scheduled{Unattended: true, Condition: "it is under 10"}); err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Failed || st.Met == nil || *st.Met || !st.Continuable || !st.Scheduled.Unattended {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", "I signed in, go on"); err != nil {
		t.Fatal(err)
	}
	st = h.wait(t)
	if st.End != Done || st.Met != nil || st.Scheduled == nil || st.Scheduled.Unattended || st.Scheduled.Condition != "" {
		t.Fatalf("status: %+v, scheduled %+v", st, st.Scheduled)
	}
}
