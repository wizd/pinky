package standalone

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/pilot"
)

// A task suggests a note, the person keeps it through the control socket
// and changes the notes there; the next task reads them, and the file in
// the data folder keeps them.
func TestMemoryThroughTheControlSocket(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir = dir })
	var (
		firstsMu sync.Mutex
		firsts   []string // the first message of each later task
	)
	firstOf := func(i int) string {
		var out string
		eventually(t, "the task's request", func() bool {
			firstsMu.Lock()
			defer firstsMu.Unlock()
			if len(firsts) > i {
				out = firsts[i]
				return true
			}
			return false
		})
		return out
	}
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			if strings.Contains(last, "<notes>") {
				t.Errorf("notes before any:\n%s", last)
			}
			return call("r1", "remember", map[string]any{"kind": "said", "text": "Likes oat milk in coffee",
				"quote": "I like oat milk in coffee"})
		},
		func(last string, _ *http.Request) map[string]any {
			return call("f1", "finish", map[string]any{"outcome": "done", "summary": "Done."})
		},
	}
	ctl := h.dial(t)
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	start := map[string]any{"cmd": "start", "task": "Open Clock. I like oat milk in coffee", "key": "sk-test",
		"base_url": h.url, "model": "fake"}
	if r := ctl.do(start); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	st := sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if s := st.Agent.Suggested; len(s) != 1 || s[0].Text != "Likes oat milk in coffee" || s[0].Kind != "said" || s[0].Kept {
		t.Fatalf("suggested: %+v", st.Agent.Suggested)
	}
	if r := ctl.do(map[string]any{"cmd": "keep", "index": 1}); r.OK || r.Code != CodeMemoryGone {
		t.Fatalf("keep of no suggestion: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "keep", "index": 0}); !r.OK {
		t.Fatalf("keep: %+v", r)
	}
	sub.until("kept", func(s State) bool { return s.Agent != nil && len(s.Agent.Suggested) == 1 && s.Agent.Suggested[0].Kept })
	if r := ctl.do(map[string]any{"cmd": "drop", "index": 0}); r.OK || r.Code != CodeMemoryGone {
		t.Fatalf("drop of a kept one: %+v", r)
	}

	// The notes: add, refuse, edit, delete.
	r := ctl.do(map[string]any{"cmd": "memory_add", "text": "Prefers window seats"})
	if !r.OK || r.Memories == nil || len(*r.Memories) != 2 || (*r.Memories)[0].Text != "Prefers window seats" {
		t.Fatalf("memory_add: %+v", r)
	}
	id := (*r.Memories)[0].ID
	if r := ctl.do(map[string]any{"cmd": "memory_add", "text": "My card is 4111 1111 1111 1111"}); r.OK || r.Code != CodeMemorySensitive {
		t.Fatalf("memory_add of a card number: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "memory_add", "text": strings.Repeat("x", pilot.MaxMemoryChars+1)}); r.OK || r.Code != CodeMemoryLong {
		t.Fatalf("memory_add of a long note: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "memory_edit", "id": id, "text": "Prefers aisle seats"}); !r.OK || (*r.Memories)[0].Text != "Prefers aisle seats" || (*r.Memories)[0].UpdatedMS == 0 {
		t.Fatalf("memory_edit: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "memory_edit", "id": "nope", "text": "x"}); r.OK || r.Code != CodeMemoryGone {
		t.Fatalf("memory_edit of no note: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "memory_delete", "id": id}); !r.OK || len(*r.Memories) != 1 {
		t.Fatalf("memory_delete: %+v", r)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "memory.json")); err != nil || !strings.Contains(string(data), "Likes oat milk in coffee") {
		t.Fatalf("memory.json: %s, %v", data, err)
	}

	// The next task reads them; with no_memory, it does not.
	h.api.mu.Lock()
	h.api.n = 0
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			firstsMu.Lock()
			firsts = append(firsts, last)
			firstsMu.Unlock()
			return call("f2", "finish", map[string]any{"outcome": "done", "summary": "Done."})
		},
	}
	h.api.mu.Unlock()
	ctl.do(map[string]any{"cmd": "dismiss"})
	start["task"] = "Open Clock"
	if r := ctl.do(start); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	if second := firstOf(0); !strings.Contains(second, "<notes>\n- Likes oat milk in coffee\n</notes>") {
		t.Fatalf("the second task's first message:\n%s", second)
	}
	eventually(t, "the second task's end", func() bool {
		st := h.core.State()
		return st.Agent != nil && !st.Agent.Running
	})
	ctl.do(map[string]any{"cmd": "dismiss"})
	start["no_memory"] = true
	if r := ctl.do(start); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	if third := firstOf(1); strings.Contains(third, "<notes>") {
		t.Fatalf("a task without memory read the notes:\n%s", third)
	}

	if r := ctl.do(map[string]any{"cmd": "memory_delete_all"}); !r.OK || r.Memories == nil || len(*r.Memories) != 0 {
		t.Fatalf("memory_delete_all: %+v", r)
	}
}
