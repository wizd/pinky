package pilot

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
)

// agent is an MCP client of the harness's server, an agent on the
// computer that hands Pinky tasks.
type agent struct {
	cs *sdk.ClientSession
}

func (h *harness) agent(t *testing.T, name string, cfg Config, on bool) *agent {
	t.Helper()
	srv := h.r.opt.Server
	srv.SetTasks(h.r.ForAgents(func() (Config, bool) { return cfg, on }))
	st, ct := sdk.NewInMemoryTransports()
	go srv.Serve(context.Background(), st)
	cs, err := sdk.NewClient(&sdk.Implementation{Name: name, Version: "1.0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return &agent{cs: cs}
}

func (a *agent) call(t *testing.T, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := a.cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*sdk.TextContent).Text, res.IsError
}

var taskIDRe = regexp.MustCompile(`task_id: (t[0-9a-f]+)`)

func taskID(t *testing.T, text string) string {
	t.Helper()
	m := taskIDRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no task_id in:\n%s", text)
	}
	return m[1]
}

// waitEnd calls phone_task_wait until the task is no longer running or
// waiting.
func (a *agent) waitEnd(t *testing.T, id string) string {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		text, _ := a.call(t, "phone_task_wait", map[string]any{"task_id": id, "timeout_ms": 2000})
		if s := firstWord(text); s != "running" && s != "waiting_for_person" {
			return text
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the task did not end")
	return ""
}

func firstWord(s string) string {
	w, _, _ := strings.Cut(s, " ")
	return strings.TrimSpace(w)
}

func TestAgentHandsATask(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.MaxBlock = 50 * time.Millisecond })
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "<task>\nAdd my usual lunch to the cart\n</task>") || !strings.Contains(last, "agent Claude Code") {
				t.Errorf("first message:\n%s", last)
			}
			return message("tool_use", 1000, say("Opening Dinely."), use("t1", "phone_launch", map[string]any{"app": "Dinely", "intent": "Open Dinely"}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("t2", "phone_tap", map[string]any{"ref": refOf(t, last, "Orders")}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("t3", "phone_tap", map[string]any{"ref": refOf(t, last, "Reorder · Chicken rice bowl")}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 1000, use("t4", finishName, map[string]any{"outcome": "done", "summary": "Added the chicken rice bowl to the cart."}))
		},
	}
	a := h.agent(t, "claude-code", Config{}, true)
	text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Add my usual lunch to the cart"})
	if isErr || firstWord(text) != "running" || !strings.Contains(text, "Pixel 9") || !strings.Contains(text, "claude-opus-5-5") {
		t.Fatalf("run:\n%s", text)
	}
	id := taskID(t, text)
	if st, _ := h.r.Status("pixel"); st.By != "Claude Code" || st.ID != id {
		t.Fatalf("the Ask box's status: %+v", st)
	}
	// Claude Code is new: its task waits for the person, as its own
	// phone_connect would, and it cannot answer.
	text, _ = a.call(t, "phone_task_wait", map[string]any{"task_id": id, "timeout_ms": 5000})
	if !strings.HasPrefix(text, "waiting_for_person · task_id: "+id) || !strings.Contains(text, "waiting: approval") {
		t.Fatalf("wait:\n%s", text)
	}
	needs := h.g.Needs()
	if len(needs) != 1 || needs[0].Rule != "New agent" || !strings.Contains(needs[0].Title, "Claude Code") {
		t.Fatalf("needs: %+v", needs)
	}
	h.g.Decide(needs[0].ID, gateway.AllowOnce, "")

	text = a.waitEnd(t, id)
	if !strings.HasPrefix(text, "done · task_id: "+id) || !strings.Contains(text, "Added the chicken rice bowl to the cart.") ||
		!strings.Contains(text, "1. phone_launch settled — Open Dinely (Opening Dinely.)") || !strings.Contains(text, "3. phone_tap settled") {
		t.Fatalf("end:\n%s", text)
	}
	// Activity shows it as Claude Code's task, done.
	tv := h.task(t)
	if tv.Agent != "Claude Code" || tv.State != gateway.TaskDone {
		t.Fatalf("task: %+v", tv)
	}
	text, _ = a.call(t, "phone_task_status", map[string]any{"task_id": id, "since_step": 2})
	if strings.Contains(text, "2. phone_tap") || !strings.Contains(text, "3. phone_tap") {
		t.Fatalf("status since step 2:\n%s", text)
	}
	text, _ = a.call(t, "phone_task_list", nil)
	if !strings.Contains(text, id+" · done · Pixel 9") {
		t.Fatalf("list:\n%s", text)
	}

	// Another agent sees none of it.
	other := h.agent(t, "codex", Config{}, true)
	if text, isErr := other.call(t, "phone_task_status", map[string]any{"task_id": id}); !isErr || !strings.HasPrefix(text, "error: not_found") {
		t.Fatalf("another agent's status:\n%s", text)
	}
	if text, _ := other.call(t, "phone_task_list", nil); strings.Contains(text, id) {
		t.Fatalf("another agent's list:\n%s", text)
	}
}

// TestAgentTaskWaitNotifiesEachStep: while phone_task_wait waits, it
// notifies each step as the step's own line, under the number the
// answer's Steps list gives it, a message the task read included.
func TestAgentTaskWaitNotifiesEachStep(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	var a *agent
	var id string
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_launch", map[string]any{"app": "Dinely"}))
		},
		func(last string, _ *http.Request) any {
			if _, isErr := a.call(t, "phone_task_steer", map[string]any{"task_id": id, "message": "Mind the cart"}); isErr {
				t.Error("steer refused")
			}
			return message("tool_use", 100, use("t2", "phone_tap", map[string]any{"ref": refOf(t, last, "Orders")}))
		},
		func(last string, _ *http.Request) any {
			return message("tool_use", 100, use("t3", "phone_tap", map[string]any{"ref": refOf(t, last, "Reorder · Chicken rice bowl")}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t4", finishName, map[string]any{"outcome": "done", "summary": "Reordered."}))
		},
	}
	a = h.agent(t, "claude-code", Config{}, true)
	var mu sync.Mutex
	notified := map[int]string{}
	st, ct := sdk.NewInMemoryTransports()
	go h.r.opt.Server.Serve(context.Background(), st)
	waiter, err := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1.0"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			mu.Lock()
			notified[int(req.Params.Progress)] = req.Params.Message
			mu.Unlock()
		},
	}).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { waiter.Close() })

	release := make(chan struct{})
	first := h.api.turns[0]
	h.api.turns[0] = func(last string, r *http.Request) any { <-release; return first(last, r) }
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Reorder lunch"})
	id = taskID(t, text)
	go func() { time.Sleep(100 * time.Millisecond); close(release) }()
	for deadline := time.Now().Add(15 * time.Second); firstWord(text) != "done"; {
		if time.Now().After(deadline) {
			t.Fatalf("the task did not end:\n%s", text)
		}
		p := &sdk.CallToolParams{Name: "phone_task_wait", Arguments: map[string]any{"task_id": id, "timeout_ms": 5000}}
		p.SetProgressToken("wait-" + id)
		res, err := waiter.CallTool(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		text = res.Content[0].(*sdk.TextContent).Text
	}
	answer := map[int]string{}
	for _, line := range strings.Split(text, "\n") {
		var n int
		if num, rest, ok := strings.Cut(line, ". "); ok {
			if _, err := fmt.Sscan(num, &n); err == nil && n > 0 {
				answer[n] = rest
			}
		}
	}
	if len(answer) != 4 || !strings.Contains(text, "your message: “Mind the cart”") || !strings.HasPrefix(answer[4], "phone_tap settled") {
		t.Fatalf("the answer's steps:\n%s", text)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notified) == 0 {
		t.Fatal("no step was notified")
	}
	for n, msg := range notified {
		if answer[n] != msg {
			t.Errorf("step %d was notified as %q; the answer has %q", n, msg, answer[n])
		}
	}
}

func TestAgentTaskKeepsTheAgentsScope(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	// The person let Claude Code use Calendar alone: Pinky, working for
	// it, may not open Dinely either.
	h.g.SetAgentScope("Claude Code", gateway.Scope{Mode: gateway.ScopeApps, Apps: []string{"Calendar"}})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_launch", map[string]any{"app": "Dinely"}))
		},
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "error: scope_denied") && !strings.Contains(last, "blocked_by_policy") {
				t.Errorf("Dinely opened:\n%s", last)
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "failed", "summary": "Not allowed to open Dinely."}))
		},
	}
	a := h.agent(t, "claude-code", Config{}, true)
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Order lunch", "device": "Pixel 9"})
	text = a.waitEnd(t, taskID(t, text))
	if !strings.HasPrefix(text, "failed") || !strings.Contains(text, "Not allowed to open Dinely.") {
		t.Fatalf("end:\n%s", text)
	}
}

func TestAgentCancelsATask(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	asked := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			return message("tool_use", 100, use("t1", "phone_observe", nil))
		},
		func(_ string, r *http.Request) any {
			close(asked)
			<-r.Context().Done()
			return nil
		},
	}
	a := h.agent(t, "claude-code", Config{}, true)
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Look around"})
	id := taskID(t, text)
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Something else"}); !isErr || !strings.HasPrefix(text, "error: device_busy") {
		t.Fatalf("a second task:\n%s", text)
	}
	<-asked
	text, _ = a.call(t, "phone_task_cancel", map[string]any{"task_id": id})
	if !strings.Contains(text, "task_id: "+id) {
		t.Fatalf("cancel:\n%s", text)
	}
	text = a.waitEnd(t, id)
	if !strings.HasPrefix(text, "cancelled") {
		t.Fatalf("end:\n%s", text)
	}
	tv := h.task(t)
	if o, s := released(t, tv); o != "cancelled" || !strings.Contains(s, "Claude Code") {
		t.Fatalf("released %q %q: %+v", o, s, tv)
	}
	// The phone is free again.
	if d := h.g.Devices()[0]; d.TaskID != "" {
		t.Fatalf("the phone still runs %s", d.TaskID)
	}
}

func TestAgentTaskUnavailable(t *testing.T) {
	h := newHarness(t)
	srv := h.r.opt.Server
	st, ct := sdk.NewInMemoryTransports()
	go srv.Serve(context.Background(), st)
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "1.0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	a := &agent{cs: cs}
	// No runner, as in `pinky daemon`.
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Look"}); !isErr || !strings.HasPrefix(text, "error: unsupported") {
		t.Fatalf("without a runner:\n%s", text)
	}
	// Ask Pinky turned off.
	h.agent(t, "x", Config{}, false)
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Look"}); !isErr || !strings.Contains(text, "turned Ask Pinky off") {
		t.Fatalf("Ask Pinky off:\n%s", text)
	}
	// No key.
	h.agent(t, "x", Config{}, true)
	delete(h.keys, Anthropic)
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Look"}); !isErr || !strings.Contains(text, "no API key") {
		t.Fatalf("no key:\n%s", text)
	}
	// No model: a custom OpenAI-compatible service has no default one.
	h.agent(t, "x", Config{Service: CustomOpenAI}, true)
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Look"}); !isErr || !strings.Contains(text, "no model") {
		t.Fatalf("no model:\n%s", text)
	}
	// No such phone.
	h.agent(t, "x", Config{}, true)
	h.keys[Anthropic] = testKey
	if text, isErr := a.call(t, "phone_task_run", map[string]any{"task": "Look", "device": "iPhone"}); !isErr || !strings.Contains(text, "No phone matches") {
		t.Fatalf("no such phone:\n%s", text)
	}
}

func TestAgentTaskSteerAndContinue(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	var a *agent
	var id string
	lastText := func() string {
		reqs := h.api.requests()
		blocks := lastUser(t, reqs[len(reqs)-1])
		text, _ := blocks[len(blocks)-1]["text"].(string)
		return text
	}
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any {
			// The agent and the person both write while the model thinks.
			if text, isErr := a.call(t, "phone_task_steer", map[string]any{"task_id": id, "message": "Look at the cart too"}); isErr ||
				!strings.Contains(text, "reads your message before its next step") {
				t.Errorf("steer:\n%s", text)
			}
			if _, err := h.r.Steer("pixel", "Skip the drinks"); err != nil {
				t.Error(err)
			}
			return message("tool_use", 100, use("t1", "phone_observe", nil))
		},
		func(string, *http.Request) any {
			text := lastText()
			if !strings.Contains(text, "<message from=\"agent\">\nLook at the cart too\n</message>") ||
				!strings.Contains(text, "<message from=\"person\">\nSkip the drinks\n</message>") || !strings.Contains(text, "agent Claude Code") {
				t.Errorf("messages:\n%s", text)
			}
			return message("tool_use", 100, use("t2", finishName, map[string]any{"outcome": "done", "summary": "Looked."}))
		},
		func(string, *http.Request) any {
			// The person continues the agent's task.
			if text := lastText(); !strings.Contains(text, "The person has continued it") {
				t.Errorf("continued by the person:\n%s", text)
			}
			return message("tool_use", 100, use("t3", finishName, map[string]any{"outcome": "done", "summary": "Looked again."}))
		},
		func(string, *http.Request) any {
			// Then the agent does.
			if text := lastText(); !strings.Contains(text, "The person's agent Claude Code, which handed you the task, has continued it") ||
				!strings.Contains(text, "<message from=\"agent\">\nNow check the orders\n</message>") {
				t.Errorf("continued by the agent:\n%s", text)
			}
			return message("tool_use", 100, use("t4", finishName, map[string]any{"outcome": "done", "summary": "Checked the orders."}))
		},
	}
	a = h.agent(t, "claude-code", Config{}, true)
	// The first turn waits for the task's id.
	release := make(chan struct{})
	first := h.api.turns[0]
	h.api.turns[0] = func(last string, r *http.Request) any { <-release; return first(last, r) }
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Look around"})
	id = taskID(t, text)
	close(release)
	if text = a.waitEnd(t, id); !strings.HasPrefix(text, "done") || !strings.Contains(text, "your message: “Look at the cart too”") ||
		!strings.Contains(text, "the person's message in Pinky: “Skip the drinks”") {
		t.Fatalf("end:\n%s", text)
	}
	gid := h.task(t).ID

	// The person continues it in Pinky: it stays the agent's task, under
	// its name, and the agent sees it run again.
	if err := h.r.Continue("pixel", "Look again"); err != nil {
		t.Fatal(err)
	}
	if text = a.waitEnd(t, id); !strings.HasPrefix(text, "done") || !strings.Contains(text, "Looked again.") {
		t.Fatalf("after the person's Continue:\n%s", text)
	}
	if tv := h.task(t); tv.ID != gid || tv.Agent != "Claude Code" {
		t.Fatalf("task: %+v", tv)
	}

	// The agent carries its ended task on.
	if text, isErr := a.call(t, "phone_task_steer", map[string]any{"task_id": id, "message": "Now check the orders"}); isErr ||
		!strings.Contains(text, "carries the task on") {
		t.Fatalf("continue:\n%s", text)
	}
	if text = a.waitEnd(t, id); !strings.Contains(text, "Checked the orders.") || !strings.Contains(text, "continued 2 times") {
		t.Fatalf("after the agent's Continue:\n%s", text)
	}
	tv := h.task(t)
	if tv.ID != gid || tv.Agent != "Claude Code" {
		t.Fatalf("task: %+v", tv)
	}
	said := strings.Join(personSteps(tv), "\n")
	for _, want := range []string{"Claude Code said: “Look at the cart too”", "You said: “Skip the drinks”", "You said: “Look again”",
		"Claude Code said: “Now check the orders”"} {
		if !strings.Contains(said, want) {
			t.Fatalf("Activity's messages lack %q:\n%s", want, said)
		}
	}
}

func TestAgentCannotContinueAStoppedTask(t *testing.T) {
	h := newHarness(t, func(o *gateway.Options) { o.TrustNewAgents = true })
	asked := make(chan struct{})
	h.api.turns = []func(string, *http.Request) any{
		func(string, *http.Request) any { return message("tool_use", 100, use("t1", "phone_observe", nil)) },
		func(_ string, r *http.Request) any {
			close(asked)
			<-r.Context().Done()
			return nil
		},
	}
	a := h.agent(t, "claude-code", Config{}, true)
	text, _ := a.call(t, "phone_task_run", map[string]any{"task": "Look around"})
	id := taskID(t, text)
	<-asked
	h.g.Stop("pixel")
	h.r.Stop("pixel")
	if text = a.waitEnd(t, id); !strings.HasPrefix(text, "stopped") {
		t.Fatalf("end:\n%s", text)
	}
	if text, isErr := a.call(t, "phone_task_steer", map[string]any{"task_id": id, "message": "Go on"}); !isErr ||
		!strings.Contains(text, "person stopped this task") {
		t.Fatalf("continue a stopped task:\n%s", text)
	}
	// The person may.
	if st, _ := h.r.Status("pixel"); !st.Continuable {
		t.Fatalf("status: %+v", st)
	}
}
