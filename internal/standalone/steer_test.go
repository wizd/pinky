package standalone

import (
	"net/http"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/gateway"
)

func TestSteerAndContinueThroughTheControlSocket(t *testing.T) {
	h := newHarness(t)
	asked, goOn := make(chan struct{}), make(chan struct{})
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			close(asked)
			<-goOn
			return call("t1", "phone_observe", nil)
		},
		func(string, *http.Request) map[string]any {
			return call("t2", "finish", map[string]any{"outcome": "done", "summary": "Picked blue."})
		},
		func(string, *http.Request) map[string]any {
			return call("t3", "finish", map[string]any{"outcome": "done", "summary": "Picked green."})
		},
	}
	ctl := h.dial(t)
	if r := ctl.do(map[string]any{"cmd": "steer", "text": "Hello"}); r.OK || r.Code != CodeNotRunning {
		t.Fatalf("steer with no task: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "continue", "text": "Go on", "key": "sk-test"}); r.OK || r.Code != CodeCannotContinue {
		t.Fatalf("continue with no task: %+v", r)
	}
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Pick a color", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	<-asked

	// The person writes while the model thinks: one message they edit, one
	// they take back.
	r := ctl.do(map[string]any{"cmd": "steer", "text": "Use the red one"})
	if !r.OK || r.ID == "" {
		t.Fatalf("steer: %+v", r)
	}
	first := r.ID
	if r := ctl.do(map[string]any{"cmd": "steer", "id": first, "text": "Use the blue one"}); !r.OK {
		t.Fatalf("edit: %+v", r)
	}
	second := ctl.do(map[string]any{"cmd": "steer", "text": "Never mind"}).ID
	if r := ctl.do(map[string]any{"cmd": "withdraw", "id": second}); !r.OK {
		t.Fatalf("withdraw: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "withdraw", "id": second}); r.OK || r.Code != CodeNoMessage {
		t.Fatalf("withdrawn twice: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "steer", "text": "  "}); r.OK || r.Code != CodeEmpty {
		t.Fatalf("empty: %+v", r)
	}
	st := sub.until("the message waits", func(s State) bool {
		return s.Agent != nil && len(s.Agent.Pending) == 1 && s.Agent.Pending[0].Text == "Use the blue one"
	})
	if m := st.Agent.Pending[0]; m.ID != first || m.MS == 0 {
		t.Fatalf("pending: %+v", m)
	}
	close(goOn)

	st = sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if a := st.Agent; a.End != "done" || len(a.Pending) != 0 || !a.Continuable || a.Continues != 0 || a.Provider != "anthropic" {
		t.Fatalf("agent: %+v", a)
	}
	taskID := st.Task.ID
	said := false
	for _, s := range st.Task.Steps {
		said = said || s.Text == "You said: “Use the blue one”"
	}
	if !said {
		t.Fatalf("steps: %+v", st.Task.Steps)
	}
	if r := ctl.do(map[string]any{"cmd": "withdraw", "id": first}); r.OK || r.Code != CodeRead {
		t.Fatalf("withdraw once read: %+v", r)
	}
	eventually(t, "the key is gone with the task", func() bool { return h.key() == "" })

	// The person's step in their language.
	if st := ctl.do(map[string]any{"cmd": "state", "locale": "zh-Hans"}).State; !hasShown(st, "你说：“Use the blue one”") {
		t.Errorf("shown: %+v", st.Task.Steps)
	}
	ctl.do(map[string]any{"cmd": "state", "locale": "en"})

	// Continue needs the key again, and goes on with the same task.
	if r := ctl.do(map[string]any{"cmd": "continue", "text": "Now green"}); r.OK || r.Code != CodeNoKey {
		t.Fatalf("continue without a key: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "continue", "key": "sk-test"}); r.OK || r.Code != CodeEmpty {
		t.Fatalf("continue with nothing: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "continue", "text": "Now green", "key": "sk-test"}); !r.OK {
		t.Fatalf("continue: %+v", r)
	}
	st = sub.until("the second end", func(s State) bool {
		return s.Agent != nil && !s.Agent.Running && s.Agent.Continues == 1
	})
	if a := st.Agent; a.End != "done" || a.Summary != "Picked green." || a.Steps != 1 || !a.Continuable {
		t.Fatalf("agent: %+v", a)
	}
	if st.Task.ID != taskID || st.Task.State != string(gateway.TaskDone) {
		t.Fatalf("task: %+v", st.Task)
	}
	var texts []string
	for _, s := range st.Task.Steps {
		texts = append(texts, s.Text)
	}
	if got := strings.Join(texts, "\n"); !strings.Contains(got, "You said: “Now green”") {
		t.Fatalf("steps:\n%s", got)
	}
	eventually(t, "the key is gone again", func() bool { return h.key() == "" })
	h.core.G.Flush()
	r = ctl.do(map[string]any{"cmd": "history"})
	if !r.OK || len(r.History) != 1 || r.History[0].ID != taskID {
		t.Fatalf("history: %+v", r)
	}
}

func hasShown(st *State, want string) bool {
	if st == nil || st.Task == nil {
		return false
	}
	for _, s := range st.Task.Steps {
		if s.Shown == want {
			return true
		}
	}
	return false
}
