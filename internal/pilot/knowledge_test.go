package pilot

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/DIYgod/pinky/guides"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestMemories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinky", "memory.json")
	m, err := OpenMemories(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 10)
	m.OnChange(func() { changed <- struct{}{} })
	a, err := m.Add(Said, "  Likes oat milk\nin coffee ", "ignored")
	if err != nil || a.Text != "Likes oat milk in coffee" || a.App != "" || a.Kind != Said || a.ID == "" {
		t.Fatalf("Add = %+v, %v", a, err)
	}
	<-changed
	b, err := m.Add(Learned, "Orders go through the 「我的」 tab", "Meituan")
	if err != nil || b.App != "Meituan" {
		t.Fatalf("Add = %+v, %v", b, err)
	}
	if again, err := m.Add(Said, "likes OAT milk in coffee", ""); err != nil || again.ID != a.ID {
		t.Fatalf("the same note again = %+v, %v", again, err)
	}
	if l := m.List(); len(l) != 2 || l[0].ID != b.ID {
		t.Fatalf("List, newest first = %+v", l)
	}
	if err := m.Edit(a.ID, "Likes soy milk in coffee"); err != nil {
		t.Fatal(err)
	}
	if err := m.Edit(a.ID, "Card 4111 1111 1111 1111"); !errors.Is(err, ErrSensitive) {
		t.Fatalf("Edit with a card number = %v", err)
	}
	if err := m.Edit("nope", "x"); !errors.Is(err, ErrMemoryGone) {
		t.Fatalf("Edit of no note = %v", err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("file: %v %v", fi, err)
		}
	}

	// The file keeps them.
	m2, err := OpenMemories(path)
	if err != nil {
		t.Fatal(err)
	}
	if l := m2.List(); len(l) != 2 || l[1].Text != "Likes soy milk in coffee" || l[1].Updated.IsZero() {
		t.Fatalf("reopened: %+v", l)
	}
	if err := m2.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	if l := m2.List(); len(l) != 1 || l[0].ID != a.ID {
		t.Fatalf("after Delete: %+v", l)
	}
	if err := m2.DeleteAll(); err != nil || len(m2.List()) != 0 {
		t.Fatalf("DeleteAll: %v %+v", err, m2.List())
	}

	full, _ := OpenMemories("")
	for i := range MaxMemories {
		if _, err := full.Add(Said, "Note number "+strings.Repeat("i", i+1), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := full.Add(Said, "One too many", ""); !errors.Is(err, ErrMemoryFull) {
		t.Fatalf("Add past the limit = %v", err)
	}
	if _, err := full.Add(Said, strings.Repeat("长", MaxMemoryChars+1), ""); !errors.Is(err, ErrMemoryLong) {
		t.Fatalf("a long note = %v", err)
	}
	if _, err := OpenMemories(filepath.Join(t.TempDir(), "none.json")); err != nil {
		t.Fatalf("no file yet = %v", err)
	}
}

// The rule: notes keep how the person likes things done and how to do
// things, never secrets, numbers that identify them or their money,
// email or street addresses.
func TestSensitive(t *testing.T) {
	for text, want := range map[string]bool{
		"Likes oat milk in coffee":                     false,
		"Pins the work group in WeChat":                false,
		"Wakes up at 7:00 on weekdays":                 false,
		"Orders lunch from 老乡鸡 under ¥40":              false,
		"Delivers to the saved 「家」 address in Meituan": false,
		"My WeChat password is hunter2":                true,
		"PIN 4321":                                     true,
		"支付密码是 1234":                                   true,
		"验证码发到我手机":                                     true,
		"Card 4111-1111-1111-1111":                     true,
		"Phone 138 0013 8000":                          true,
		"Ｍｙ number is １３８００１３８０００":                     true,
		"Email me at someone@example.com":              true,
		"Lives at 221B Baker Street":                   true,
		"家住北京市朝阳区建国路88号":                               true,
		"住所は渋谷区神南1丁目":                                  true,
		"身份证号见相册":                                      true,
		"パスワードはメモにある":                                  true,
		"Use the recovery code from the drawer":        true,
		"The API key is in the notes app":              true,
		"Ask before paying more than 200 yuan":         false,
		"Calls Mom on Sundays":                         false,
	} {
		if got := Sensitive(text); got != want {
			t.Errorf("Sensitive(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestNotesBudget(t *testing.T) {
	if notes(nil) != "" {
		t.Fatal("notes of none")
	}
	m, _ := OpenMemories("")
	for i := range MaxMemories {
		m.Add(Learned, strings.Repeat("长", MaxMemoryChars-4)+string(rune('a'+i%26))+string(rune('a'+i/26)), "Meituan")
	}
	n := notes(m.List())
	body := n[strings.Index(n, "<notes>"):]
	if c := utf8.RuneCountInString(body); c > MaxNotesChars+20 {
		t.Fatalf("notes of %d characters", c)
	}
	if !strings.Contains(n, "(learned, Meituan)") || !strings.HasPrefix(n, "The person's notes for Pinky") {
		t.Fatalf("notes:\n%s", n)
	}
}

func TestScreenApp(t *testing.T) {
	for text, want := range map[string]screen.App{
		"settled · Pixel 9\nscreen 1080x2400 · app: Calculator (com.google.android.calculator) · portrait · keyboard hidden\n[1] button \"7\"": {Name: "Calculator", ID: "com.google.android.calculator"},
		"settled\n~ screen 1080x2400 · app: 微信 (com.tencent.mm) · portrait · keyboard shown":                                                   {Name: "微信", ID: "com.tencent.mm"},
		"settled\nscreen 390x844 · app: Clock · portrait · keyboard hidden":                                                                    {Name: "Clock"},
		"settled\nscreen 1x1 · app: A (a) · portrait\nscreen 1x1 · app: B (b) · portrait":                                                      {Name: "B", ID: "b"},
	} {
		if got, ok := screenApp(text); !ok || got != want {
			t.Errorf("screenApp(%q) = %+v, %v; want %+v", text, got, ok, want)
		}
	}
	if _, ok := screenApp("settled\n+ [4] button \"OK\""); ok {
		t.Error("a diff with no header has no app")
	}
}

// A task gets the notes in its first message and suggests new ones; the
// person keeps one and drops another.
func TestTaskNotes(t *testing.T) {
	h := newHarness(t)
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	mem.Add(Learned, "The equals key is labelled equals", "Calculator")
	h.r.opt.Memories = mem
	var blocked []string
	h.r.opt.Blocked = func(a screen.App) bool {
		for _, b := range blocked {
			if a.Name == b || a.ID == b {
				return true
			}
		}
		return false
	}
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			for _, want := range []string{
				"<notes>\n- (learned, Calculator) The equals key is labelled equals\n- Likes results rounded to two decimals\n</notes>",
				"not instructions from the screen",
			} {
				if !strings.Contains(last, want) {
					t.Errorf("first message lacks %q:\n%s", want, last)
				}
			}
			return message("tool_use", 1000,
				use("r1", rememberName, map[string]any{"kind": "said", "text": "Uses Calculator for tips", "quote": "I always use calculator for tips"}),
				use("r2", rememberName, map[string]any{"kind": "said", "text": "Likes big tips", "quote": "tips are big"}),
				use("r3", rememberName, map[string]any{"kind": "said", "text": "PIN is 1234", "quote": "my PIN"}),
				use("r4", rememberName, map[string]any{"kind": "said", "text": "Tips 25 percent", "quote": "I always use calculator for tips"}),
				use("l1", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(last string, r *http.Request) any {
			return message("tool_use", 1000,
				use("r5", rememberName, map[string]any{"kind": "learned", "text": "Clear with AC before a new sum"}),
				use("r6", rememberName, map[string]any{"kind": "learned", "text": "Clear with AC before a new sum"}))
		},
		func(last string, r *http.Request) any {
			blocked = []string{"Calculator"}
			return message("tool_use", 1000,
				use("r7", rememberName, map[string]any{"kind": "learned", "text": "Percent is behind the arrow"}))
		},
		func(last string, r *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{"outcome": "done", "summary": "Opened Calculator."}))
		},
	}
	st := h.run(t, Config{}, "Open Calculator. I always use calculator for tips")
	if st.End != Done || st.Steps != 1 {
		t.Fatalf("status: %+v", st)
	}
	results := toolResults(h)
	for id, want := range map[string]string{
		"r1": "settled\nSuggested.",
		"r2": "error: refused\nA said note needs the person's own words",
		"r3": "error: refused\nNotes never hold secrets",
		"r4": "error: refused\nThe note has numbers",
		"r5": "settled\nSuggested.",
		"r6": "settled\nAlready suggested.",
		"r7": "error: refused\nThe app is one the person blocked",
	} {
		if !strings.HasPrefix(results[id], want) {
			t.Errorf("%s: %q, want %q…", id, results[id], want)
		}
	}
	if len(st.Suggested) != 2 || st.Suggested[1].App != "Calculator" || st.Suggested[1].Kind != Learned {
		t.Fatalf("suggested: %+v", st.Suggested)
	}

	if err := h.r.Keep("pixel", 0); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Drop("pixel", 1); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Keep("pixel", 1); !errors.Is(err, ErrMemoryGone) {
		t.Fatalf("Keep of a dropped note = %v", err)
	}
	st, _ = h.r.Status("pixel")
	if k := st.Suggested[0].Kept; k == nil || !st.Suggested[1].Dropped {
		t.Fatalf("suggested: %+v", st.Suggested)
	}
	if l := mem.List(); len(l) != 3 || l[0].Text != "Uses Calculator for tips" {
		t.Fatalf("notes: %+v", l)
	}
}

// toolResults are the tool results the model got, by the call's id.
func toolResults(h *harness) map[string]string {
	results := map[string]string{}
	for _, req := range h.api.requests() {
		msgs, _ := req.body["messages"].([]any)
		for _, m := range msgs {
			blocks, _ := m.(map[string]any)["content"].([]any)
			for _, b := range blocks {
				b, _ := b.(map[string]any)
				if b["type"] == "tool_result" {
					parts, _ := b["content"].([]any)
					p, _ := parts[0].(map[string]any)
					results[b["tool_use_id"].(string)], _ = p["text"].(string)
				}
			}
		}
	}
	return results
}

// A continued task is one conversation: the notes and a guide are in it
// once, the suggestions of its first stretch stay and count towards the
// cap, and the person's messages (steered or to continue) are their words
// as much as the task.
func TestContinueWithNotes(t *testing.T) {
	h := newHarness(t)
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	h.r.opt.Memories = mem
	lastText := func() string {
		reqs := h.api.requests()
		b, _ := json.Marshal(lastUser(t, reqs[len(reqs)-1]))
		return string(b)
	}
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			if _, err := h.r.Steer("pixel", "Keep it short please"); err != nil {
				t.Error(err)
			}
			return message("tool_use", 1000,
				use("r1", rememberName, map[string]any{"kind": "said", "text": "Uses Calculator for tips", "quote": "calculator for tips"}),
				use("l1", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000,
				use("r2", rememberName, map[string]any{"kind": "said", "text": "Likes short answers", "quote": "keep it short"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("f1", finishName, map[string]any{"outcome": "done", "summary": "Opened Calculator."}))
		},
		func(string, *http.Request) any {
			if s := lastText(); strings.Contains(s, "<notes>") || strings.Contains(s, "Pinky's guide to") {
				t.Errorf("the continued stretch sends notes or the guide again:\n%s", s)
			}
			return message("tool_use", 1000,
				use("r3", rememberName, map[string]any{"kind": "said", "text": "Rounds to 2 places", "quote": "round to 2 places"}),
				use("l2", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000,
				use("r4", rememberName, map[string]any{"kind": "learned", "text": "Clear with AC before a new sum"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("f2", finishName, map[string]any{"outcome": "done", "summary": "Opened it again."}))
		},
	}
	st := h.run(t, Config{}, "Open Calculator. I use calculator for tips")
	if st.End != Done || len(st.Suggested) != 2 {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", "Open it again and round to 2 places"); err != nil {
		t.Fatal(err)
	}
	st = h.wait(t)
	if st.End != Done || st.Continues != 1 || len(st.Suggested) != 3 || st.Suggested[0].Text != "Uses Calculator for tips" ||
		st.Suggested[1].Text != "Likes short answers" || st.Suggested[2].Text != "Rounds to 2 places" {
		t.Fatalf("status: %+v", st)
	}
	results := toolResults(h)
	for id, want := range map[string]string{
		"r1": "settled\nSuggested.",
		"r2": "settled\nSuggested.",
		"r3": "settled\nSuggested.",
		"r4": "error: refused\nA task suggests at most 3 notes.",
	} {
		if !strings.HasPrefix(results[id], want) {
			t.Errorf("%s: %q, want %q…", id, results[id], want)
		}
	}
	reqs := h.api.requests()
	raw := reqs[len(reqs)-1].raw
	g, _ := guides.For("com.google.android.calculator", "")
	if n := strings.Count(raw, "Likes results rounded to two decimals"); n != 1 {
		t.Errorf("the notes are in the conversation %d times", n)
	}
	if n := strings.Count(raw, g.Body[:40]); n != 1 {
		t.Errorf("the guide is in the conversation %d times", n)
	}
	if err := h.r.Keep("pixel", 0); err != nil {
		t.Fatal(err)
	}
	if l := mem.List(); len(l) != 2 || l[0].Text != "Uses Calculator for tips" {
		t.Fatalf("notes: %+v", l)
	}
}

// A task an agent hands Pinky reads the person's notes and gets guides
// like any other, but only the person's own messages are their words, and
// the suggestions are for the person in Pinky, never for the agent.
func TestAgentTaskNotes(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	h.r.opt.Memories = mem
	var a *agent
	var id string
	release := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			<-release
			for _, want := range []string{"<notes>\n- Likes results rounded to two decimals\n</notes>", agentNotes} {
				if !strings.Contains(last, want) {
					t.Errorf("first message lacks %q:\n%s", want, last)
				}
			}
			if text, isErr := a.call(t, "phone_task_steer", map[string]any{"task_id": id, "message": "They round to 2 places"}); isErr {
				t.Errorf("steer:\n%s", text)
			}
			if _, err := h.r.Steer("pixel", "Keep it short please"); err != nil {
				t.Error(err)
			}
			return message("tool_use", 1000,
				use("r1", rememberName, map[string]any{"kind": "said", "text": "Uses Calculator for tips", "quote": "calculator for tips"}),
				use("l1", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000,
				use("r2", rememberName, map[string]any{"kind": "said", "text": "Rounds to 2 places", "quote": "round to 2 places"}),
				use("r3", rememberName, map[string]any{"kind": "said", "text": "Likes short answers", "quote": "keep it short"}),
				use("r4", rememberName, map[string]any{"kind": "learned", "text": "Clear with AC before a new sum"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{"outcome": "done", "summary": "Opened Calculator."}))
		},
	}
	a = h.agent(t, "claude-code", Config{}, true)
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Open Calculator. They use calculator for tips"})
	id = taskID(t, text)
	close(release)
	end := a.waitEnd(t, id)
	if !strings.HasPrefix(end, "done") {
		t.Fatalf("end:\n%s", end)
	}

	results := toolResults(h)
	for id, want := range map[string]string{
		"r1": "error: refused\nAn agent handed you this task",
		"r2": "error: refused\nAn agent handed you this task",
		"r3": "settled\nSuggested.",
		"r4": "settled\nSuggested.",
	} {
		if !strings.HasPrefix(results[id], want) {
			t.Errorf("%s: %q, want %q…", id, results[id], want)
		}
	}
	reqs := h.api.requests()
	g, _ := guides.For("com.google.android.calculator", "")
	if n := strings.Count(reqs[len(reqs)-1].raw, g.Body[:40]); n != 1 {
		t.Errorf("the guide is in the conversation %d times", n)
	}

	// The person sees the suggestions in Pinky; the agent never does.
	st, _ := h.r.Status("pixel")
	if len(st.Suggested) != 2 || st.Suggested[0].Text != "Likes short answers" || st.By == "" {
		t.Fatalf("status: %+v", st)
	}
	for _, text := range []string{end, func() string { s, _ := a.call(t, "phone_task_status", map[string]any{"task_id": id}); return s }()} {
		for _, leak := range []string{"Likes short answers", "Clear with AC", "remember", "rounded to two decimals"} {
			if strings.Contains(text, leak) {
				t.Errorf("the agent sees %q:\n%s", leak, text)
			}
		}
	}
}

// A scheduled task reads the notes and gets guides like any other. While
// nobody watches it may still suggest notes, but they wait for the person
// on the ended task: none is kept until the person keeps it.
func TestScheduledNotes(t *testing.T) {
	h := newHarness(t)
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	h.r.opt.Memories = mem
	h.api.turns = []func(string, *http.Request) any{
		func(first string, _ *http.Request) any {
			notes := strings.Index(first, "<notes>\n- Likes results rounded to two decimals\n</notes>")
			cond := strings.Index(first, "<condition>\nit is over 10\n</condition>")
			if notes < 0 || cond < 0 {
				t.Errorf("first message without the notes or the condition:\n%s", first)
			}
			return message("tool_use", 1000,
				use("r1", rememberName, map[string]any{"kind": "said", "text": "Tracks the tip in Calculator", "quote": "tip in calculator"}),
				use("l1", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000,
				use("r2", rememberName, map[string]any{"kind": "learned", "text": "Clear with AC before a new sum"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{
				"outcome": "done", "summary": "It is 12.", conditionMet: true}))
		},
	}
	if err := h.r.StartScheduled(Config{}, "pixel", "Work out the tip in calculator", Scheduled{Unattended: true, Condition: "it is over 10"}); err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Done || st.Scheduled == nil || len(st.Suggested) != 2 || st.Suggested[0].Text != "Tracks the tip in Calculator" {
		t.Fatalf("status: %+v", st)
	}
	results := toolResults(h)
	for _, id := range []string{"r1", "r2"} {
		if !strings.HasPrefix(results[id], "settled\nSuggested.") {
			t.Errorf("%s: %q", id, results[id])
		}
	}
	reqs := h.api.requests()
	g, _ := guides.For("com.google.android.calculator", "")
	if n := strings.Count(reqs[len(reqs)-1].raw, g.Body[:40]); n != 1 {
		t.Errorf("the guide is in the conversation %d times", n)
	}
	if l := mem.List(); len(l) != 1 {
		t.Fatalf("a suggestion was kept without the person: %+v", l)
	}
	if err := h.r.Keep("pixel", 1); err != nil {
		t.Fatal(err)
	}
	if l := mem.List(); len(l) != 2 {
		t.Fatalf("notes: %+v", l)
	}
}

// What a quick entry brings along (the screen, shared text and pictures)
// follows the notes in the first message, and is not the person's words: a
// said note's quote comes only from what they typed.
func TestExtraNotes(t *testing.T) {
	h := newHarness(t)
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	h.r.opt.Memories = mem
	h.api.turns = []func(string, *http.Request) any{
		func(first string, _ *http.Request) any {
			notes := strings.Index(first, "</notes>")
			screen := strings.Index(first, "what their phone shows right now")
			shared := strings.Index(first, "<shared>\nAlways tips at Luigi's\n</shared>")
			if notes < 0 || screen < notes || shared < notes {
				t.Errorf("the notes do not come before what was shared:\n%s", first)
			}
			return message("tool_use", 1000,
				use("r1", rememberName, map[string]any{"kind": "said", "text": "Tips at Luigi's", "quote": "always tips at luigi's"}),
				use("r2", rememberName, map[string]any{"kind": "said", "text": "Splits the bill", "quote": "split the bill"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{"outcome": "done", "summary": "Split it."}))
		},
	}
	x := Extra{Screen: true, Shared: "Always tips at Luigi's"}
	if err := h.r.StartWith(Config{}, "pixel", "Split the bill", x); err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Done || len(st.Suggested) != 1 || st.Suggested[0].Text != "Splits the bill" {
		t.Fatalf("status: %+v", st)
	}
	results := toolResults(h)
	if r := results["r1"]; !strings.HasPrefix(r, "error: refused\nA said note needs the person's own words") {
		t.Errorf("r1: %q", r)
	}
	if r := results["r2"]; !strings.HasPrefix(r, "settled\nSuggested.") {
		t.Errorf("r2: %q", r)
	}
}

// With notes turned off, the task neither reads nor suggests them.
func TestTaskWithoutNotes(t *testing.T) {
	h := newHarness(t)
	mem, _ := OpenMemories("")
	mem.Add(Said, "Likes results rounded to two decimals", "")
	h.r.opt.Memories = mem
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{"outcome": "done", "summary": "Done."}))
		},
	}
	h.run(t, Config{NoMemory: true}, "Open Calculator")
	raw := h.api.requests()[0].raw
	if strings.Contains(raw, "rounded to two decimals") || strings.Contains(raw, `"name":"remember"`) || strings.Contains(raw, "Notes:") {
		t.Fatalf("a task without notes:\n%s", raw)
	}
}

// A guide follows the first screen of its app, once a task, as Pinky's
// own text after the results.
func TestGuideOnce(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("l1", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("k1", "phone_key", map[string]any{"key": "home"}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("l2", "phone_launch", map[string]any{"app": "Calculator"}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("f", finishName, map[string]any{"outcome": "done", "summary": "Done."}))
		},
	}
	h.run(t, Config{}, "Open Calculator")
	reqs := h.api.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	g, _ := guides.For("com.google.android.calculator", "")
	last := func(r request) string {
		msgs, _ := r.body["messages"].([]any)
		b, _ := json.Marshal(msgs[len(msgs)-1])
		return string(b)
	}
	if s := last(reqs[1]); !strings.Contains(s, `"text":"Pinky's guide to Calculator`) || !strings.Contains(s, `app_guide app=\"Calculator\"`) {
		t.Fatalf("the guide does not follow the first screen of Calculator:\n%s", s)
	}
	if strings.Count(reqs[3].raw, g.Body[:40]) != 1 {
		t.Fatalf("the guide is in the conversation %d times", strings.Count(reqs[3].raw, g.Body[:40]))
	}
	if strings.Contains(reqs[0].raw, "<app_guide") {
		t.Fatal("a guide for the home screen")
	}
}

// What notes and guides add to a task, in characters (about 4 to a token
// in English, 1 to 2 in Chinese). The limits keep each one small.
func TestPromptCost(t *testing.T) {
	cfg := Config{}.Normalize()
	base := utf8.RuneCountInString(systemPrompt(cfg, false))
	withMemory := utf8.RuneCountInString(systemPrompt(cfg, true))
	toolJSON, _ := json.Marshal(map[string]any{"name": rememberTool.name, "description": rememberTool.description, "input_schema": rememberTool.schema})
	m, _ := OpenMemories("")
	for _, s := range []string{"Likes oat milk in coffee", "Orders lunch under ¥40", "Wakes up at 7:00 on weekdays", "Calls Mom on Sundays", "Prefers window seats"} {
		m.Add(Said, s, "")
	}
	m.Add(Learned, "Orders are under the 「我的」 tab, then 「全部订单」", "Meituan")
	five := utf8.RuneCountInString(notes(m.List()))
	var most, total int
	all := guides.All()
	for _, g := range all {
		n := utf8.RuneCountInString(guideText(g))
		total += n
		most = max(most, n)
	}
	t.Logf("system prompt %d characters; notes' rules add %d", base, withMemory-base)
	t.Logf("remember tool: %d characters", len(toolJSON))
	t.Logf("six notes: %d characters; the most notes a task sends: %d", five, MaxNotesChars)
	t.Logf("guides: %d, %d characters on average, %d at most, once a task each", len(all), total/len(all), most)
	if d := withMemory - base; d > 700 {
		t.Errorf("the notes' rules add %d characters", d)
	}
	if len(toolJSON) > 1500 {
		t.Errorf("remember is %d characters", len(toolJSON))
	}
	if most > guides.MaxBody+300 {
		t.Errorf("a guide of %d characters", most)
	}
}
