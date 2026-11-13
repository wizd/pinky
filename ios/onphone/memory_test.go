package onphone

import (
	"net/http"
	"testing"

	"github.com/DIYgod/pinky/internal/standalone"
)

// The iPhone's screens keep a suggested note and change the notes through
// Call, as Android's do through the control socket.
func TestMemoryThroughCall(t *testing.T) {
	h := newHarness(t)
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			return call("r1", "remember", map[string]any{"kind": "said", "text": "Likes oat milk in coffee",
				"quote": "I like oat milk in coffee"})
		},
		func(string, *http.Request) map[string]any {
			return call("f1", "finish", map[string]any{"outcome": "done", "summary": "Done."})
		},
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Look. I like oat milk in coffee", "key": "sk-test",
		"base_url": h.url, "model": "fake"}); !o.OK {
		t.Fatalf("start: %+v", o)
	}
	st := h.until(t, "the end", func(s standalone.State) bool { return s.Agent != nil && !s.Agent.Running })
	if s := st.Agent.Suggested; len(s) != 1 || s[0].Text != "Likes oat milk in coffee" {
		t.Fatalf("suggested: %+v", st.Agent.Suggested)
	}
	if o := h.do(t, map[string]any{"cmd": "drop", "index": 0}); !o.OK {
		t.Fatalf("drop: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "keep", "index": 0}); o.OK || o.Code != standalone.CodeMemoryGone {
		t.Fatalf("keep of a dropped one: %+v", o)
	}
	h.until(t, "dropped", func(s standalone.State) bool {
		return s.Agent != nil && len(s.Agent.Suggested) == 1 && s.Agent.Suggested[0].Dropped
	})
	if o := h.do(t, map[string]any{"cmd": "memories"}); !o.OK || len(o.Memories) != 0 {
		t.Fatalf("memories: %+v", o)
	}
	o := h.do(t, map[string]any{"cmd": "memory_add", "text": "Prefers window seats"})
	if !o.OK || len(o.Memories) != 1 {
		t.Fatalf("memory_add: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "memory_add", "text": "Email me at someone@example.com"}); o.OK || o.Code != standalone.CodeMemorySensitive {
		t.Fatalf("memory_add of an email: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "memory_edit", "id": o.Memories[0].ID, "text": "Prefers aisle seats"}); !o.OK || o.Memories[0].Text != "Prefers aisle seats" {
		t.Fatalf("memory_edit: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "memory_delete_all"}); !o.OK || len(o.Memories) != 0 {
		t.Fatalf("memory_delete_all: %+v", o)
	}
}
