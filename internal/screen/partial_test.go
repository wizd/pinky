package screen

import (
	"strings"
	"testing"
)

func TestPartial(t *testing.T) {
	list := func(labels ...string) *Screen {
		s := &Screen{Width: 100, Height: 200, App: App{ID: "app"}, Orientation: "portrait"}
		for i, l := range labels {
			s.Elements = append(s.Elements, Element{Ref: i + 1, Role: "text", Label: l})
		}
		return s
	}
	whole, part := list("a", "b", "c"), list("a")
	part.Partial = true
	if h := whole.Header(); strings.Contains(h, "partial") {
		t.Errorf("a whole screen's header says partial: %q", h)
	}
	if h := part.Header(); !strings.HasSuffix(h, " · partial") {
		t.Errorf("header %q; want it to end with partial", h)
	}
	// What a partial read lacks is not removed, and what the read after
	// it has again is not added: both are sent whole.
	if lines, full := Diff(whole, part); !full {
		t.Errorf("whole to partial sent as a diff: %q", lines)
	}
	if lines, full := Diff(part, whole); !full {
		t.Errorf("partial to whole sent as a diff: %q", lines)
	}
	if _, full := Diff(whole, list("a", "b", "c")); full {
		t.Error("two whole screens of one app sent whole")
	}
}
