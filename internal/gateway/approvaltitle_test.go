package gateway

import (
	"testing"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestApprovalTitlesAsk: an approval with nothing more particular to ask
// says the action in the imperative, as its other titles do ("Pay: …?",
// "Open …?"), since it is what the Needs you card, Pinky's phone apps and
// the waiting agent read. History keeps describe's past tense for the same
// action.
func TestApprovalTitlesAsk(t *testing.T) {
	at := func(x, y int) *screen.Point { return &screen.Point{X: x, Y: y} }
	mapView := &screen.Element{Label: "Map"}
	for _, c := range []struct {
		a           Action
		target      *screen.Element
		title, step string
	}{
		{a: Action{Tool: "key", Key: "back"}, title: "Press back?", step: "Pressed back"},
		{a: Action{Tool: "key", Key: "forward_delete"}, title: "Press forward delete?", step: "Pressed forward delete"},
		{a: Action{Tool: "tap", Point: at(540, 1200)}, title: "Tap at 540,1200?", step: "Tapped at 540,1200"},
		{a: Action{Tool: "tap", Point: at(540, 1200), LongPress: true}, title: "Long-press at 540,1200?", step: "Long-pressed at 540,1200"},
		{a: Action{Tool: "swipe", Direction: "up"}, title: "Swipe up?", step: "Swiped up"},
		{a: Action{Tool: "swipe", From: at(540, 1800), To: at(540, 600)}, title: "Swipe from 540,1800 to 540,600?", step: "Swiped from 540,1800 to 540,600"},
		{a: Action{Tool: "type", Text: "chicken rice"}, title: "Type “chicken rice”?", step: "Typed “chicken rice”"},
		{a: Action{Tool: "type", Text: "chicken rice", Submit: true}, title: "Type “chicken rice” and submit?", step: "Typed “chicken rice” and submitted"},
		{a: Action{Tool: "pinch", Scale: 2}, title: "Zoom in?", step: "Zoomed in"},
		{a: Action{Tool: "pinch", Scale: 0.5}, target: mapView, title: "Zoom out on “Map”?", step: "Zoomed out on “Map”"},
		{a: Action{Tool: "pinch", Rotation: 45}, target: mapView, title: "Turn “Map” with two fingers?", step: "Turned “Map” with two fingers"},
		{a: Action{Tool: "pinch", Rotation: 45}, title: "Turn with two fingers?", step: "Turned with two fingers"},
		{a: Action{Tool: "drag", From: at(200, 2000), To: at(540, 1700)}, title: "Drag from 200,2000 to 540,1700?", step: "Dragged from 200,2000 to 540,1700"},
		{a: Action{Tool: "drag"}, title: "Drag?", step: "Dragged"},
		// Something labelled under the finger was asked about by name
		// already, and still is.
		{a: Action{Tool: "tap"}, target: &screen.Element{Label: "Delete"}, title: "Tap “Delete”?", step: "Tapped “Delete”"},
	} {
		p := &prep{target: c.target}
		p.desc = describe(c.a, p)
		if got := approvalTitle(c.a, p, policy.Verdict{}); got != c.title {
			t.Errorf("%s: the title %q, want %q", c.step, got, c.title)
		}
		if p.desc != c.step {
			t.Errorf("%s: history says %q", c.step, p.desc)
		}
	}
}
