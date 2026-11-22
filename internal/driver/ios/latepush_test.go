package ios

import (
	"testing"
	"time"
)

func TestLatePush(t *testing.T) {
	now := time.Now()
	input := now.Add(-8 * time.Second) // past raxPushTrust: the trust rule has given up
	app := &axpObject{PID: 100}
	els := []raxElement{{Object: app}, {Object: &axpObject{PID: 100, ID: 2}, Label: "a"}}
	push := func(start, at time.Time, pid int) *raxTree {
		return &raxTree{els: els, start: start, at: at, first: true, appPID: pid}
	}
	for _, c := range []struct {
		name string
		t    *raxTree
		app  *axpObject
		want bool
	}{
		{"after the input, a few seconds old", push(input.Add(1300*time.Millisecond), input.Add(1300*time.Millisecond), 100), app, true},
		{"its second tree came later", push(input.Add(1300*time.Millisecond), input.Add(4*time.Second), 100), app, true},
		{"none", nil, app, false},
		{"no frontmost app", push(input.Add(2*time.Second), input.Add(2*time.Second), 100), nil, false},
		{"another app's", push(input.Add(2*time.Second), input.Add(2*time.Second), 7), app, false},
		{"from before the input", push(input.Add(-time.Second), input.Add(-time.Second), 100), app, false},
		{"too soon after the input", push(input.Add(300*time.Millisecond), input.Add(300*time.Millisecond), 100), app, false},
		{"empty", &raxTree{start: input.Add(2 * time.Second), at: input.Add(2 * time.Second), appPID: 100}, app, false},
	} {
		if got := latePush(c.t, c.app, input, now); got != c.want {
			t.Errorf("%s: latePush = %v, want %v", c.name, got, c.want)
		}
	}
	old := now.Add(-time.Minute)
	if latePush(push(old.Add(2*time.Second), old.Add(2*time.Second), 100), app, old, now) {
		t.Error("a push from a minute ago answers for a walk")
	}
}
