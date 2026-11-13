package companion

import (
	"context"
	"errors"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// Stroke is one finger's path in a gesture: it moves along Points at an
// even speed, from Start for Duration. One point is a tap, or a long press
// when it lasts long enough.
type Stroke struct {
	Points   []screen.Point
	Start    time.Duration
	Duration time.Duration
}

// Segment is one leg of a finger that does not lift between legs: it moves
// along Points in Duration, and one point rests there.
type Segment struct {
	Points   []screen.Point
	Duration time.Duration
}

// SegmentOptions say how Segments begins and ends.
type SegmentOptions struct {
	// Continue goes on with the finger an earlier Segments call left down
	// on the same display: the first point is reached from where it rests.
	// Without a finger down the app answers not_held.
	Continue bool
	// Hold leaves the finger down after the last segment. It lifts on the
	// next gesture that does not continue it, or after 30 seconds.
	Hold bool
}

// Error codes the input commands answer besides the usual ones.
const (
	// CodeOwnUI: a point lands on Pinky's own controls; nothing was done.
	CodeOwnUI = "own_ui"
	// CodeNotFocused: back on a display other than the one the phone's
	// focus is on, where Android would send it.
	CodeNotFocused = "not_focused"
	// CodeNotHeld: Continue without a finger down on the display.
	CodeNotHeld = "not_held"
	// CodeCancelled: Android cancelled the gesture, for example because the
	// person touched the screen.
	CodeCancelled = "cancelled"
)

// IsCode reports whether err is the app's answer with that error code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Gesture plays strokes as one gesture on a display through the service's
// dispatchGesture and returns once Android has played it. Displays other
// than 0 need FeatureDisplayInput. Never retried: a tap that reached the
// phone must not land twice.
func (c *Client) Gesture(ctx context.Context, display int, strokes ...Stroke) error {
	ss := make([]map[string]any, len(strokes))
	var longest time.Duration
	for i, s := range strokes {
		ss[i] = map[string]any{
			"points":      pointsParam(s.Points),
			"start_ms":    s.Start.Milliseconds(),
			"duration_ms": max(1, s.Duration.Milliseconds()),
		}
		longest = max(longest, s.Start+s.Duration)
	}
	params := map[string]any{"strokes": ss}
	if display != 0 {
		params["display"] = display
	}
	return c.callFor(ctx, longest+gestureSlack, "gesture", params, nil)
}

// Segments moves one finger through segs without lifting it between them
// and returns once Android has played them all. Never retried.
func (c *Client) Segments(ctx context.Context, display int, segs []Segment, opt SegmentOptions) error {
	ss := make([]map[string]any, len(segs))
	var total time.Duration
	for i, s := range segs {
		ss[i] = map[string]any{
			"points":      pointsParam(s.Points),
			"duration_ms": max(1, s.Duration.Milliseconds()),
		}
		total += s.Duration
	}
	params := map[string]any{"segments": ss}
	if display != 0 {
		params["display"] = display
	}
	if opt.Continue {
		params["continue"] = true
	}
	if opt.Hold {
		params["hold"] = true
	}
	return c.callFor(ctx, total+gestureSlack, "gesture", params, nil)
}

// Global presses back, home, recents, notifications or enter (the
// keyboard's action key in the focused field). On a display other than 0
// only back and enter, with FeatureDisplayInput; back answers
// CodeNotFocused unless that display has the phone's focus. Never retried.
func (c *Client) Global(ctx context.Context, display int, action string) error {
	params := map[string]any{"action": action}
	if display != 0 {
		params["display"] = display
	}
	return c.call(ctx, "global", params, nil, false)
}

// gestureSlack is how much longer than its strokes a gesture may take to
// be answered.
const gestureSlack = 10 * time.Second

func pointsParam(pts []screen.Point) [][2]int {
	out := make([][2]int, len(pts))
	for i, p := range pts {
		out[i] = [2]int{p.X, p.Y}
	}
	return out
}
