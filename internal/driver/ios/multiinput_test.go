package ios

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

func marks(sizes ...int) []frameMark {
	out := make([]frameMark, len(sizes))
	for i, s := range sizes {
		out[i] = frameMark{size: s}
	}
	return out
}

func TestMovedAfter(t *testing.T) {
	for _, c := range []struct {
		name          string
		before, after []frameMark
		ax            bool
		want          bool
	}{
		{"nothing came", marks(1500, 1500), nil, false, false},
		{"nothing came, ax changed", nil, nil, true, true},
		{"frames after a still stretch", nil, marks(1500), false, true},
		{"still frames go on", marks(1500, 1510), marks(1505, 1500, 1498), false, false},
		{"sizes vary after", marks(1500), marks(1500, 9000, 1500), false, true},
		{"one size, another than before", marks(1500, 1500), marks(10240, 10240), false, true},
		{"padded still frames go on", marks(10240), marks(10240, 10200), false, false},
	} {
		if got := movedAfter(c.before, c.after, c.ax); got != c.want {
			t.Errorf("%s: moved %v, want %v", c.name, got, c.want)
		}
	}
}

func TestHIDReplyError(t *testing.T) {
	for _, c := range []struct {
		name  string
		reply map[string]any
		want  string // "" for no error
	}{
		{"none", nil, ""},
		{"success", map[string]any{"messageType": "Response", "payload": map[string]any{}}, ""},
		{"error at the top", map[string]any{"error": map[string]any{"domain": "com.apple.dt.CoreDeviceError", "code": int64(1000),
			"userInfo": map[string]any{"NSLocalizedDescription": map[string]any{"string": "No main screen button service found"}}}}, "No main screen button service found"},
		{"error in the payload", map[string]any{"payload": map[string]any{"error": "no service"}}, "no service"},
		{"CoreDevice error", map[string]any{"CoreDevice.error": map[string]any{"domain": "d", "code": uint64(7)}}, "d 7"},
		{"error message type", map[string]any{"messageType": "ErrorResponse"}, "ErrorResponse"},
	} {
		err := hidReplyError(c.reply)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v, want none", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want one with %q", c.name, err, c.want)
		}
	}
}

func TestHoldAndListen(t *testing.T) {
	ctx := context.Background()
	replies := make(chan map[string]any, 2)
	replies <- map[string]any{"a": 1}
	if r, ok := holdAndListen(ctx, replies, time.Second); !ok || r["a"] != 1 {
		t.Fatalf("waiting: %v %v", r, ok)
	}
	start := time.Now()
	if _, ok := holdAndListen(ctx, nil, 20*time.Millisecond); ok {
		t.Fatal("a nil channel answered")
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("did not hold")
	}
	if _, ok := holdAndListen(ctx, replies, 0); ok {
		t.Fatal("an empty channel answered")
	}
	replies <- map[string]any{}
	if _, ok := holdAndListen(ctx, replies, -time.Second); !ok {
		t.Fatal("a waiting reply was missed after the time was up")
	}
	replies <- map[string]any{}
	replies <- map[string]any{}
	drainReplies(replies)
	if len(replies) != 0 {
		t.Fatal("not drained")
	}
}

// Touches are reported after stillLimit in a row changed nothing on the
// display in use, on phones with several displays only; a change resets
// the run but does not vouch for the inputs after it (issue #106).
func TestInputUnseen(t *testing.T) {
	d := &Device{two: true, streamShows: "u inner", streamOn: 2}
	for i := 0; i < stillLimit; i++ {
		if err := d.inputUnseen(); err != nil {
			t.Fatalf("after %d: %v", i, err)
		}
		d.noteEcho("u inner", false)
	}
	err := d.inputUnseen()
	if err == nil || !strings.Contains(err.Error(), "u inner") || !strings.Contains(err.Error(), "display 2") ||
		!strings.Contains(err.Error(), "fold the phone") {
		t.Fatalf("got %v", err)
	}
	// Another display (folded) starts afresh.
	d.streamShows = "u outer"
	if err := d.inputUnseen(); err != nil {
		t.Fatalf("outer: %v", err)
	}
	// A change ends the run...
	d.streamShows = "u inner"
	d.noteEcho("u inner", true)
	if err := d.inputUnseen(); err != nil {
		t.Fatalf("right after a change: %v", err)
	}
	// ...and one still input after it is not reported: a single tap may
	// rightly change nothing.
	d.noteEcho("u inner", false)
	if err := d.inputUnseen(); err != nil {
		t.Fatalf("one still after a change: %v", err)
	}
	// A run of still inputs after a change is (the iPhone Duo of #106: the
	// first tap on Calendar changed the picture, the ones after it nothing).
	for i := 1; i < stillLimit; i++ {
		d.noteEcho("u inner", false)
	}
	err = d.inputUnseen()
	if err == nil || !strings.Contains(err.Error(), "earlier ones there did") || !strings.Contains(err.Error(), "u inner") {
		t.Fatalf("still after a change: %v", err)
	}
	if ue := (*unseenError)(nil); !errors.As(err, &ue) || ue.still != stillLimit || ue.moved != 1 {
		t.Fatalf("error %#v", err)
	}
	if c := *d.echo["u inner"]; c.Sent != 2*stillLimit+1 || c.Moved != 1 || c.Still != stillLimit {
		t.Fatalf("counts %+v", c)
	}
	// The touch that comes next changes the picture: the run is over.
	d.noteEcho("u inner", true)
	if err := d.inputUnseen(); err != nil {
		t.Fatalf("after another change: %v", err)
	}
	// One display: never.
	one := &Device{streamShows: "k"}
	for i := 0; i < 3*stillLimit; i++ {
		one.noteEcho("k", false)
	}
	if err := one.inputUnseen(); err != nil {
		t.Fatalf("one display: %v", err)
	}
}

// Home on a phone with several displays, after the Home button did
// nothing: a swipe that changed nothing either is an error (the gateway
// answers "uncertain"), never a success (issue #106: folded, with Calendar
// open, both did nothing and the agent was told the action settled).
func TestHomeSwipeOutcome(t *testing.T) {
	why := "the screen did not change after the Home button"

	note, err := homeSwipeOutcome(why, nil, false)
	if err == nil || note != "" {
		t.Fatalf("still after the swipe: note %q, error %v", note, err)
	}
	for _, want := range []string{why, "swipe up from the bottom edge", "home screen showed already", "phone_handoff"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// Not one of the errors the gateway takes for "nothing was done".
	for _, e := range []error{driver.ErrUnsupported, driver.ErrNotFound, driver.ErrLocked, driver.ErrOffline} {
		if errors.Is(err, e) {
			t.Errorf("error is %v", e)
		}
	}

	note, err = homeSwipeOutcome(why, nil, true)
	if err != nil || !strings.Contains(note, why) || !strings.Contains(note, "Check that the home screen shows") {
		t.Fatalf("moved after the swipe: note %q, error %v", note, err)
	}

	refused := "the phone refused the Home button (No main screen button service found)"
	sent := errors.New("touch connection closed")
	note, err = homeSwipeOutcome(refused, sent, false)
	if note != "" || !errors.Is(err, sent) || !strings.Contains(err.Error(), refused) {
		t.Fatalf("swipe failed: note %q, error %v", note, err)
	}
	note, err = homeSwipeOutcome(why, context.DeadlineExceeded, false)
	if note != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("time ran out: note %q, error %v", note, err)
	}
}

// The verdict on the touches before Home's swipe is told apart from the
// swipe's own errors, so Home judges the swipe by the picture alone.
func TestUnseenErrorIsTold(t *testing.T) {
	d := &Device{two: true, streamShows: "u outer", streamOn: 1}
	for i := 0; i < stillLimit; i++ {
		d.noteEcho("u outer", false)
	}
	err := fmt.Errorf("swipe: %w", d.inputUnseen())
	if ue := (*unseenError)(nil); !errors.As(err, &ue) || ue.display != "u outer" || ue.stream != 1 || ue.moved != 0 {
		t.Fatalf("%v", err)
	}
	if ue := (*unseenError)(nil); errors.As(errors.New("touch connection closed"), &ue) {
		t.Fatal("another error taken for the verdict")
	}
}

func TestPictureMovedSince(t *testing.T) {
	d := &Device{}
	m := &media{}
	now := time.Now()
	m.frameLog = []frameMark{
		{at: now.Add(-500 * time.Millisecond), size: 1500},
		{at: now.Add(-100 * time.Millisecond), size: 1500},
	}
	if d.pictureMoved(m, now) {
		t.Fatal("moved without frames after")
	}
	m.frameLog = append(m.frameLog, frameMark{at: now.Add(100 * time.Millisecond), size: 12000})
	if !d.pictureMoved(m, now) {
		t.Fatal("a frame of another size after did not count")
	}
	if d.pictureMoved(nil, now) {
		t.Fatal("no stream moved")
	}
}
