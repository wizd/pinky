package standalone

import (
	"net/http"
	"strings"
	"testing"
)

// A question about the screen with what the person shared: the start
// carries them to the model's first message, a picture larger than the
// socket's old 1 MB line included; a picture of another type is refused
// before the phone is claimed.
func TestStartWithExtra(t *testing.T) {
	h := newHarness(t)
	h.dev.Show("cart")
	first := make(chan string, 1)
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			first <- last
			return call("t1", "finish", map[string]any{"outcome": "done", "summary": "Your cart holds a chicken rice bowl."})
		},
	}
	ctl := h.dial(t)
	big := make([]byte, 3<<20)
	if r := ctl.do(map[string]any{"cmd": "start", "task": "What's in it?", "key": "sk-test", "base_url": h.url, "model": "fake",
		"images": []map[string]any{{"mime": "image/svg+xml", "data": []byte("<svg/>")}}}); r.OK || r.Code != CodeShared {
		t.Fatalf("an SVG: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "start", "task": "What's in it?", "key": "sk-test", "base_url": h.url, "model": "fake",
		"screen": true, "shared": "https://example.com/menu",
		"images": []map[string]any{{"mime": "image/jpeg", "data": big}}}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	msg := <-first
	for _, want := range []string{"<task>\nWhat's in it?\n</task>", "Place order", "what their phone shows right now",
		"<shared>\nhttps://example.com/menu\n</shared>", "shared a picture"} {
		if !strings.Contains(msg, want) {
			t.Errorf("first message lacks %q:\n%s", want, msg)
		}
	}
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	st := sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	if st.Agent.Task != "What's in it?" || st.Agent.Summary != "Your cart holds a chicken rice bowl." {
		t.Fatalf("agent: %+v", st.Agent)
	}
}
