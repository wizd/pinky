//go:build !ios

package standalone

import (
	"net/http"
	"strings"
	"testing"
	"unicode"
)

// hasHan reports whether s has Chinese characters.
func hasHan(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

// The screens name their language, and the state says the gateway's words
// in it beside the English, which stays for the agent and the logs.
func TestScreensLanguage(t *testing.T) {
	t.Cleanup(func() { setLocale("en") })
	h := newHarness(t)
	h.dev.Show("cart")
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			return call("t1", "phone_tap", map[string]any{"ref": refOf(t, last, "Place order · $12.80")})
		},
		func(string, *http.Request) map[string]any {
			return call("t2", "finish", map[string]any{"outcome": "done", "summary": "Ordered."})
		},
	}
	ctl := h.dial(t)
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe", "locale": "zh-Hans"})
	sub.read()
	if r := ctl.do(map[string]any{"cmd": "start", "task": "Order lunch", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !r.OK {
		t.Fatalf("start: %+v", r)
	}
	st := sub.until("the approval", func(s State) bool { return len(s.Needs) > 0 })
	n := st.Needs[0]
	if n.TitleShown == "" || !hasHan(n.TitleShown) || !strings.Contains(n.TitleShown, "Place order") {
		t.Fatalf("need: %q, shown %q", n.Title, n.TitleShown)
	}
	if n.Rule != "Payments" || !hasHan(n.RuleShown) {
		t.Fatalf("rule: %q, shown %q", n.Rule, n.RuleShown)
	}
	var waiting *Step
	for i, s := range st.Task.Steps {
		if strings.HasPrefix(s.Text, "Waiting for you: ") {
			waiting = &st.Task.Steps[i]
		}
	}
	if waiting == nil || !hasHan(waiting.Shown) || !hasHan(waiting.Asked) || !strings.Contains(waiting.Asked, "Place order") {
		t.Fatalf("waiting step: %+v", st.Task.Steps)
	}
	first := st.Task.Steps[0]
	if !strings.HasPrefix(first.Text, "Connected to ") || !hasHan(first.Shown) {
		t.Fatalf("first step: %+v", first)
	}
	if r := ctl.do(map[string]any{"cmd": "decide", "need": n.ID, "decision": "allow_once"}); !r.OK {
		t.Fatalf("decide: %+v", r)
	}
	st = sub.until("the end", func(s State) bool { return s.Agent != nil && !s.Agent.Running })
	for _, s := range st.Task.Steps {
		if strings.HasPrefix(s.Text, "Tapped ") && !hasHan(s.Shown) {
			t.Fatalf("tapped: %+v", s)
		}
	}
	// The history says them too.
	r := ctl.do(map[string]any{"cmd": "history", "id": st.Task.ID})
	if !r.OK || len(r.History) != 1 || len(r.History[0].Steps) == 0 || !hasHan(r.History[0].Steps[0].Shown) {
		t.Fatalf("history: %+v", r)
	}
	// In English the screens read the gateway's words as they are.
	st = *ctl.do(map[string]any{"cmd": "state", "locale": "en"}).State
	for _, s := range st.Task.Steps {
		if s.Shown != "" || s.Asked != "" {
			t.Fatalf("in English: %+v", s)
		}
	}
	t.Logf("steps: %+v", st.Task.Steps)
}
