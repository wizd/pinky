package pilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
)

// lastUser is the blocks of a Messages API request's last message, which
// is the person's.
func lastUser(t *testing.T, r request) []map[string]any {
	t.Helper()
	msgs, _ := r.body["messages"].([]any)
	m, _ := msgs[len(msgs)-1].(map[string]any)
	if m["role"] != "user" {
		t.Fatalf("last message: %v", m)
	}
	var out []map[string]any
	for _, b := range m["content"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

// alternates fails when a Messages API request has two turns of the same
// role in a row.
func alternates(t *testing.T, r request) {
	t.Helper()
	msgs, _ := r.body["messages"].([]any)
	prev := ""
	for i, m := range msgs {
		role := m.(map[string]any)["role"].(string)
		if role == prev {
			t.Fatalf("two %s turns in a row at %d: %s", role, i, r.raw)
		}
		prev = role
	}
}

// personSteps are the person's messages among a task's steps.
func personSteps(tv gateway.TaskView) []string {
	var out []string
	for _, s := range tv.Steps {
		if s.Tool == gateway.PersonStep {
			out = append(out, s.Text)
		}
	}
	return out
}

func TestSteer(t *testing.T) {
	h := newHarness(t)
	var first Message
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			// The person writes while the model thinks: one message they
			// edit, one they take back.
			var err error
			if first, err = h.r.Steer("pixel", "Use the red one"); err != nil {
				t.Error(err)
			}
			if err := h.r.Edit("pixel", first.ID, "  Use the blue one  "); err != nil {
				t.Error(err)
			}
			second, _ := h.r.Steer("pixel", "Never mind")
			if err := h.r.Withdraw("pixel", second.ID); err != nil {
				t.Error(err)
			}
			if err := h.r.Withdraw("pixel", second.ID); !errors.Is(err, ErrNoMessage) {
				t.Errorf("withdrawn twice: %v", err)
			}
			if st, _ := h.r.Status("pixel"); len(st.Pending) != 1 || st.Pending[0].Text != "Use the blue one" {
				t.Errorf("pending: %+v", st.Pending)
			}
			return message("tool_use", 100, use("t1", "phone_observe", nil))
		},
		func(string, *http.Request) any {
			if st, _ := h.r.Status("pixel"); len(st.Pending) != 0 {
				t.Errorf("still pending once read: %+v", st.Pending)
			}
			if err := h.r.Edit("pixel", first.ID, "Too late"); !errors.Is(err, ErrRead) {
				t.Errorf("edit after reading: %v", err)
			}
			if err := h.r.Withdraw("pixel", first.ID); !errors.Is(err, ErrRead) {
				t.Errorf("withdraw after reading: %v", err)
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Picked blue."}))
		},
	}
	if _, err := h.r.Steer("pixel", "Hello"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("steer with no task: %v", err)
	}
	st := h.run(t, Config{}, "Pick a color")
	if st.End != Done {
		t.Fatalf("status: %+v", st)
	}
	if _, err := h.r.Steer("pixel", "More"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("steer after the end: %v", err)
	}
	if _, err := h.r.Steer("pixel", "  "); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}

	// The message goes after the tool's result, in a text of its own, as
	// the person's.
	reqs := h.api.requests()
	blocks := lastUser(t, reqs[1])
	if len(blocks) != 2 || blocks[0]["type"] != "tool_result" || blocks[1]["type"] != "text" {
		t.Fatalf("blocks: %v", blocks)
	}
	text, _ := blocks[1]["text"].(string)
	if !strings.Contains(text, "<message>\nUse the blue one\n</message>") || !strings.Contains(text, "not from the phone's screen") ||
		strings.Contains(reqs[1].raw, "Never mind") || strings.Contains(reqs[1].raw, "red one") {
		t.Fatalf("message: %s", text)
	}
	if !strings.Contains(reqs[0].raw, "approves nothing") {
		t.Fatal("the system prompt does not say a message approves nothing")
	}
	if got := personSteps(h.task(t)); len(got) != 1 || got[0] != "You said: “Use the blue one”" {
		t.Fatalf("steps: %q", got)
	}
}

func TestSteerBeforeTheModel(t *testing.T) {
	// A message typed while Pinky connects goes with the first request.
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", finishName, map[string]any{"outcome": "done", "summary": "Done."}))
		},
	}
	h.r.opt.Key = func(context.Context, string) (string, error) {
		if _, err := h.r.Steer("pixel", "Be quick"); err != nil {
			t.Error(err)
		}
		return testKey, nil
	}
	if st := h.run(t, Config{}, "Look around"); st.End != Done {
		t.Fatalf("status: %+v", st)
	}
	blocks := lastUser(t, h.api.requests()[0])
	text, _ := blocks[len(blocks)-1]["text"].(string)
	if !strings.Contains(text, "<task>\nLook around\n</task>") || !strings.Contains(text, "<message>\nBe quick\n</message>") {
		t.Fatalf("first message: %s", text)
	}
}

func TestSteerAnswersText(t *testing.T) {
	// The model writes instead of calling a tool while the person's
	// message waits: the message is the answer, not the nudge.
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			h.r.Steer("pixel", "Yes, the blue one")
			return message("end_turn", 100, say("Which color?"))
		},
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "Yes, the blue one") || strings.Contains(last, "Nobody reads") {
				t.Errorf("answer: %s", last)
			}
			return message("tool_use", 100, use("t1", finishName, map[string]any{"outcome": "done", "summary": "Blue."}))
		},
	}
	if st := h.run(t, Config{}, "Pick a color"); st.End != Done {
		t.Fatalf("status: %+v", st)
	}
}

func TestContinue(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_launch", map[string]any{"app": "Dinely"}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Opened Dinely."}))
		},
		func(string, *http.Request) any {
			reqs := h.api.requests()
			blocks := lastUser(t, reqs[len(reqs)-1])
			text, _ := blocks[len(blocks)-1]["text"].(string)
			return message("tool_use", 100, use("t3", "phone_tap", map[string]any{"ref": refOf(t, text, "Orders")}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t4", finishName, map[string]any{"outcome": "done", "summary": "Opened your orders."}))
		},
	}
	if err := h.r.Continue("pixel", "Go on"); !errors.Is(err, ErrCannotContinue) {
		t.Fatalf("continue with no task: %v", err)
	}
	st := h.run(t, Config{}, "Open Dinely")
	if st.End != Done || !st.Continuable || st.TaskID == "" || st.Steps != 1 {
		t.Fatalf("status: %+v", st)
	}
	first := h.task(t)
	if err := h.r.Continue("pixel", ""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("continue with nothing: %v", err)
	}
	if err := h.r.Continue("pixel", "Now open my orders"); err != nil {
		t.Fatal(err)
	}
	if err := h.r.Continue("pixel", "Again"); !errors.Is(err, ErrBusy) {
		t.Fatalf("continue while running: %v", err)
	}
	st = h.wait(t)
	if st.End != Done || st.Summary != "Opened your orders." || st.Continues != 1 || st.Steps != 2 || st.TaskID != first.ID {
		t.Fatalf("status: %+v", st)
	}

	// One task in the history, done, with both stretches' steps and the
	// person's message between them.
	h.g.Flush()
	tasks, err := h.g.History(context.Background(), gateway.HistoryQuery{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != first.ID || tasks[0].State != gateway.TaskDone || tasks[0].Ended.IsZero() {
		t.Fatalf("history: %+v", tasks)
	}
	var tools []string
	for _, s := range tasks[0].Steps {
		tools = append(tools, s.Tool)
	}
	want := "phone_connect phone_launch phone_release person_message phone_tap phone_release"
	if got := strings.Join(tools, " "); got != want {
		t.Fatalf("steps: %s, want %s", got, want)
	}
	if o, s := released(t, gateway.TaskView{Steps: tasks[0].Steps[len(tasks[0].Steps)-1:]}); o != "done" || s != "Opened your orders." {
		t.Fatalf("released %q %q", o, s)
	}

	// The model goes on from its conversation: finish gets its answer, then
	// the person's message comes with the phone's screen.
	reqs := h.api.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[2]
	alternates(t, r)
	if msgs, _ := r.body["messages"].([]any); len(msgs) != 5 {
		t.Fatalf("messages: %s", r.raw)
	}
	blocks := lastUser(t, r)
	if len(blocks) != 2 || blocks[0]["type"] != "tool_result" || blocks[0]["tool_use_id"] != "t2" {
		t.Fatalf("blocks: %v", blocks)
	}
	text, _ := blocks[1]["text"].(string)
	if !strings.Contains(text, "The person has continued it") || !strings.Contains(text, "<message>\nNow open my orders\n</message>") ||
		!strings.Contains(text, "settled · Pixel 9") {
		t.Fatalf("message: %s", text)
	}
	if r.header.Get("Authorization") != "Bearer "+testKey {
		t.Fatalf("no key: %v", r.header)
	}
}

func TestContinueLimits(t *testing.T) {
	// The limits count anew: one step a stretch here. The model asks for
	// two steps at once; the limit stops the task before the second.
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_observe", nil), use("t2", "phone_observe", nil))
		},
		func(string, *http.Request) any { return message("tool_use", 100, use("t3", "phone_observe", nil)) },
	}
	st := h.run(t, Config{MaxSteps: 1}, "Look around")
	if st.End != StepLimit || st.Steps != 1 || !st.Continuable {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", "Look once more"); err != nil {
		t.Fatal(err)
	}
	st = h.wait(t)
	if st.End != StepLimit || st.Steps != 2 || st.Continues != 1 {
		t.Fatalf("status: %+v", st)
	}
	// Both calls are answered before the message: the first with its
	// result, the one the limit cut off as not run.
	reqs := h.api.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[1]
	alternates(t, r)
	blocks := lastUser(t, r)
	if len(blocks) != 3 || blocks[0]["tool_use_id"] != "t1" || blocks[1]["tool_use_id"] != "t2" || blocks[2]["type"] != "text" ||
		!strings.Contains(r.raw, "error: not_run") || blocks[1]["is_error"] != true {
		t.Fatalf("blocks: %v", blocks)
	}
}

func TestContinueAfterStop(t *testing.T) {
	// Stopped while the model thought: that request was never answered, so
	// the continuing one adds to it.
	h := newHarness(t)
	asked := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any { return message("tool_use", 100, use("t1", "phone_observe", nil)) },
		func(_ string, r *http.Request) any {
			close(asked)
			<-r.Context().Done()
			return nil
		},
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Done."}))
		},
	}
	if err := h.r.Start(Config{}, "pixel", "Look around"); err != nil {
		t.Fatal(err)
	}
	<-asked
	// A message typed just before Stop waits for Continue.
	if _, err := h.r.Steer("pixel", "Check the cart too"); err != nil {
		t.Fatal(err)
	}
	h.g.Stop("pixel")
	h.r.Stop("pixel")
	st := h.wait(t)
	if st.End != Stopped || !st.Continuable || len(st.Pending) != 1 {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", ""); err != nil {
		t.Fatal(err)
	}
	if st = h.wait(t); st.End != Done {
		t.Fatalf("status: %+v", st)
	}
	reqs := h.api.requests()
	r := reqs[len(reqs)-1]
	alternates(t, r)
	if !strings.Contains(r.raw, "Check the cart too") {
		t.Fatalf("request: %s", r.raw)
	}
	if tv := h.task(t); tv.State != gateway.TaskDone {
		t.Fatalf("task: %+v", tv)
	}
}

func TestContinueOpenAI(t *testing.T) {
	h := newHarness(t)
	toolCall := func(id, name string, args map[string]any) any {
		a, _ := json.Marshal(args)
		return map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}},
				"finish_reason": "tool_calls"}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20},
		}
	}
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return toolCall("c1", finishName, map[string]any{"outcome": "done", "summary": "Looked."})
		},
		func(string, *http.Request) any {
			return toolCall("c2", finishName, map[string]any{"outcome": "done", "summary": "Looked again."})
		},
	}
	cfg := Config{Provider: OpenAI, Model: "gpt-6", BaseURL: anthropicURL + "/v1"}
	if st := h.run(t, cfg, "Look around"); st.End != Done {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", "Look again"); err != nil {
		t.Fatal(err)
	}
	if st := h.wait(t); st.End != Done || st.Summary != "Looked again." {
		t.Fatalf("status: %+v", st)
	}
	msgs, _ := h.api.requests()[1].body["messages"].([]any)
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.(map[string]any)["role"].(string))
	}
	if got := strings.Join(roles, " "); got != "system user assistant tool user" {
		t.Fatalf("roles: %s", got)
	}
	if tm := msgs[3].(map[string]any); tm["tool_call_id"] != "c1" {
		t.Fatalf("tool message: %v", tm)
	}
}

func TestCannotContinue(t *testing.T) {
	// A task that never reached the model has nothing to continue.
	h := newHarness(t)
	delete(h.keys, Anthropic)
	if st := h.run(t, Config{}, "Look around"); st.End != NoKey || st.Continuable {
		t.Fatalf("status: %+v", st)
	}
	if err := h.r.Continue("pixel", "Go on"); !errors.Is(err, ErrCannotContinue) {
		t.Fatalf("continue: %v", err)
	}
}

func TestElapsed(t *testing.T) {
	t0 := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	s := Status{Started: t0, Resumed: t0.Add(time.Hour), Worked: 2 * time.Minute}
	if got := s.Elapsed(t0.Add(time.Hour + time.Minute)); got != 3*time.Minute {
		t.Fatalf("running: %v", got)
	}
	s.Ended = t0.Add(time.Hour + 30*time.Second)
	if got := s.Elapsed(t0.Add(2 * time.Hour)); got != 150*time.Second {
		t.Fatalf("ended: %v", got)
	}
}
