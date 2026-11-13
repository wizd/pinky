package companion

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// asJSON renders a request's params the way the app reads them.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGestureParams(t *testing.T) {
	p, c := readyClient(t)
	ctx := context.Background()
	err := c.Gesture(ctx, 7,
		Stroke{Points: []screen.Point{{X: 10, Y: 20}}, Duration: 50 * time.Millisecond},
		Stroke{Points: []screen.Point{{X: 1, Y: 2}, {X: 3, Y: 4}}, Start: 100 * time.Millisecond},
	)
	if err != nil {
		t.Fatal(err)
	}
	req := p.lastRequest("gesture")
	if got, want := asJSON(t, req["strokes"]), `[{"duration_ms":50,"points":[[10,20]],"start_ms":0},{"duration_ms":1,"points":[[1,2],[3,4]],"start_ms":100}]`; got != want {
		t.Errorf("strokes %s, want %s", got, want)
	}
	if req["display"] != float64(7) {
		t.Errorf("display %v", req["display"])
	}

	// Display 0 is the default and not sent.
	if err := c.Gesture(ctx, 0, Stroke{Points: []screen.Point{{X: 1, Y: 1}}}); err != nil {
		t.Fatal(err)
	}
	if req := p.lastRequest("gesture"); req["display"] != nil {
		t.Errorf("display %v on the phone's own screen", req["display"])
	}
}

func TestSegmentsParams(t *testing.T) {
	p, c := readyClient(t)
	ctx := context.Background()
	segs := []Segment{
		{Points: []screen.Point{{X: 5, Y: 5}}, Duration: 600 * time.Millisecond},
		{Points: []screen.Point{{X: 5, Y: 5}, {X: 50, Y: 500}}, Duration: 300 * time.Millisecond},
	}
	if err := c.Segments(ctx, 3, segs, SegmentOptions{Hold: true}); err != nil {
		t.Fatal(err)
	}
	req := p.lastRequest("gesture")
	if got, want := asJSON(t, req["segments"]), `[{"duration_ms":600,"points":[[5,5]]},{"duration_ms":300,"points":[[5,5],[50,500]]}]`; got != want {
		t.Errorf("segments %s, want %s", got, want)
	}
	if req["hold"] != true || req["continue"] != nil || req["display"] != float64(3) {
		t.Errorf("request %v", req)
	}
	if err := c.Segments(ctx, 3, segs[:1], SegmentOptions{Continue: true}); err != nil {
		t.Fatal(err)
	}
	if req := p.lastRequest("gesture"); req["continue"] != true || req["hold"] != nil {
		t.Errorf("request %v", req)
	}
}

func TestInputErrorCodes(t *testing.T) {
	p, c := readyClient(t)
	ctx := context.Background()
	p.input = func(req map[string]any) *Error {
		if req["cmd"] == "global" {
			return &Error{Code: CodeNotFocused, Message: "display 7 does not have the focus"}
		}
		return &Error{Code: CodeOwnUI, Message: "that is Pinky's own screen"}
	}
	if err := c.Global(ctx, 7, "back"); !IsCode(err, CodeNotFocused) {
		t.Fatalf("back: %v", err)
	}
	if req := p.lastRequest("global"); req["action"] != "back" || req["display"] != float64(7) {
		t.Errorf("request %v", req)
	}
	if err := c.Gesture(ctx, 0, Stroke{Points: []screen.Point{{X: 1, Y: 1}}}); !IsCode(err, CodeOwnUI) || IsCode(err, CodeNotFocused) {
		t.Fatalf("gesture: %v", err)
	}
}
