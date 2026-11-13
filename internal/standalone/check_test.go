package standalone

import (
	"net/http"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/pilot"
)

func TestCheckAndUsageThroughTheControlSocket(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(_ string, r *http.Request) map[string]any {
			if r.Header.Get("X-Api-Key") != "sk-check" {
				t.Errorf("key: %v", r.Header)
			}
			return call("c1", "ready", map[string]any{"word": "ready"})
		},
	}
	ctl := h.dial(t)
	if r := ctl.do(map[string]any{"cmd": "usage"}); !r.OK || r.Usage == nil || len(*r.Usage) != 0 {
		t.Fatalf("usage before: %+v", r)
	}
	// Nothing to send without a key; the service is the person's own.
	r := ctl.do(map[string]any{"cmd": "check", "service": pilot.CustomAnthropic, "base_url": h.url, "model": "fake"})
	if !r.OK || r.Check == nil || r.Check.OK || r.Check.Problem != pilot.CheckNoKey {
		t.Fatalf("check without a key: %+v", r)
	}
	r = ctl.do(map[string]any{"cmd": "check", "service": pilot.CustomAnthropic, "base_url": h.url, "model": "fake", "key": "sk-check"})
	if !r.OK || r.Check == nil || !r.Check.OK {
		t.Fatalf("check: %+v", r)
	}
	// A check's key is not kept for tasks.
	if k := h.key(); k != "" {
		t.Errorf("the core kept the key %q", k)
	}
	// What the check used is counted, for today.
	today := time.Now().Format(time.DateOnly)
	r = ctl.do(map[string]any{"cmd": "usage", "since": today})
	if !r.OK || r.Usage == nil || len(*r.Usage) != 1 {
		t.Fatalf("usage: %+v", r)
	}
	if u := (*r.Usage)[0]; u.Day != today || u.Service != pilot.CustomAnthropic || u.Model != "fake" || u.Requests != 1 ||
		u.Input != 100 || u.Output != 10 {
		t.Errorf("row: %+v", u)
	}
	if r := ctl.do(map[string]any{"cmd": "usage", "since": "2999-01-01"}); !r.OK || r.Usage == nil || len(*r.Usage) != 0 {
		t.Errorf("usage of later days: %+v", r)
	}
}

// A task on a server that needs no key (Ollama) starts and continues
// without one; the state names the service and its key, and both
// stretches count in usage.
func TestKeyOptionalTaskContinuesAndCounts(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(_ string, r *http.Request) map[string]any {
			if r.Header.Get("Authorization") != "" {
				t.Errorf("a key was sent: %v", r.Header)
			}
			return call("t1", "finish", map[string]any{"outcome": "done", "summary": "Opened it."})
		},
	}
	ctl := h.dial(t)
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Open Clock", "service": "ollama", "base_url": h.url + "/v1",
		"model": "qwen"}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	st := sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if a := st.Agent; a.End != "done" || !a.Continuable || a.Service != "ollama" || a.KeyName != "ollama" || !a.KeyOptional ||
		a.Provider != pilot.OpenAI {
		t.Fatalf("agent: %+v", a)
	}
	if r := ctl.do(map[string]any{"cmd": "continue", "text": "Now the timer"}); !r.OK {
		t.Fatalf("continue without a key: %+v", r)
	}
	sub.until("the second end", func(s State) bool { return s.Agent != nil && !s.Agent.Running && s.Agent.Continues == 1 })
	r := ctl.do(map[string]any{"cmd": "usage"})
	if !r.OK || r.Usage == nil || len(*r.Usage) != 1 {
		t.Fatalf("usage: %+v", r)
	}
	if u := (*r.Usage)[0]; u.Service != "ollama" || u.Model != "qwen" || u.Requests != 2 || u.Input != 200 || u.Output != 20 {
		t.Errorf("row: %+v", u)
	}
}
