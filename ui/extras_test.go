package ui

import (
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// The gateway's step texts, as the client words them.
func TestStepDisplay(t *testing.T) {
	cases := []struct {
		state      gateway.TaskState
		text, stat string
		want, wsub string
		wantKind   stepKind
	}{
		{gateway.TaskWaiting, "Waiting for you: Tapped “Pay”", "approval_pending", "Waiting for you: tap “Pay”", "", stepWait},
		{gateway.TaskWorking, "Waiting for you: Tapped “Pay”", "approval_pending", "You answered: tap “Pay”", "", stepYou},
		{gateway.TaskDone, "Waiting for you: Tapped “Pay”", "approval_denied", "You denied: tap “Pay”", "", stepYou},
		{gateway.TaskPaused, "Waiting for you: Tapped “Pay”", "user_took_over", "Tap “Pay”", "Not done: you took over", stepYou},
		{gateway.TaskStopped, "Waiting for you: Typed “hi”", "stopped", "Type “hi”", "Not done: stopped", stepDone},
		{gateway.TaskWaiting, "Asked you to take over: Sign in, please", "approval_pending", "Asked you to take over: Sign in, please", "", stepWait},
		{gateway.TaskDone, "Asked you to take over: Sign in, please", "settled", "You handed it back", "Asked: “Sign in, please”", stepYou},
		{gateway.TaskDone, "Asked you to take over: Sign in, please", "approval_denied", "You declined", "Asked: “Sign in, please”", stepYou},
		{gateway.TaskDone, "Asked you: “Keep the receipt?”", "settled", "You agreed: “Keep the receipt?”", "", stepYou},
		{gateway.TaskDone, "Tapped “Send”", "uncertain", "Tapped “Send”", "May have happened", stepDone},
		{gateway.TaskDone, "Tapped “Send”", "scope_denied", "Tapped “Send”", "Failed: scope denied", stepDone},
	}
	for _, c := range cases {
		tk := gateway.TaskView{State: c.state, Steps: []gateway.StepView{{Text: "Opened Dinely", Status: "settled"}, {Text: c.text, Status: c.stat}}}
		text, sub, kind := stepDisplay(tk, 1)
		if text != c.want || sub != c.wsub || kind != c.wantKind {
			t.Errorf("%q (%s, task %s) = %q / %q / %v, want %q / %q / %v", c.text, c.stat, c.state, text, sub, kind, c.want, c.wsub, c.wantKind)
		}
	}
}

// Step screenshots no build asked for during mediaKeep let go of their
// pixels; the ones still shown stay, and the sweep comes again for them.
func TestSweepMedia(t *testing.T) {
	ta := newTestApp(t, fakeapi.ManyPhones)
	ta.mediaCache["old"] = &mediaEntry{usedAt: time.Now().Add(-mediaKeep)}
	ta.mediaCache["new"] = &mediaEntry{usedAt: time.Now()}
	ta.mediaSweep = true
	ta.sweepMedia()
	if _, ok := ta.mediaCache["old"]; ok {
		t.Error("a screenshot unused for mediaKeep stayed loaded")
	}
	if _, ok := ta.mediaCache["new"]; !ok {
		t.Error("a screenshot in use was let go")
	}
	if !ta.mediaSweep {
		t.Error("no sweep due for the screenshot that stayed")
	}
	ta.mediaCache["new"].usedAt = time.Now().Add(-mediaKeep)
	ta.sweepMedia()
	if len(ta.mediaCache) != 0 || ta.mediaSweep {
		t.Errorf("after the last went: %d loaded, sweep due %v", len(ta.mediaCache), ta.mediaSweep)
	}
}
