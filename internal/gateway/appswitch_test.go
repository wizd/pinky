package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/screen"
)

// noSwitchGrace counts an app that came to the front by itself at once,
// without the grace a late link gets. It turns the grace off rather than
// making it tiny: Windows' clock is coarse enough that the read can come
// at the same instant as the input it follows.
func noSwitchGrace(o *Options) { o.SwitchGrace = -1 }

const switchedWords = "and none of your calls opened it"

// inDinely connects and opens Dinely, as the agent's own doing.
func inDinely(t *testing.T, g *Gateway) *Session {
	t.Helper()
	s, _ := connect(t, g, "Order lunch")
	mustStatus(t, s.Act(context.Background(), Call{}, Action{Tool: "launch", App: "Dinely"}), StatusSettled)
	return s
}

// GitHub issue #181: the person opened another app on an iPhone mid-task,
// and the agent's next read came back settled with no word about it.
func TestAppSwitchNoteOnObserve(t *testing.T) {
	g, dev := newTestGateway(t, noSwitchGrace)
	s := inDinely(t, g)
	dev.Show("chats") // the person, on the phone itself

	res := s.Observe(context.Background(), ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if res.Screen.App.ID != demo.Chatter {
		t.Fatalf("app %+v", res.Screen.App)
	}
	if !strings.Contains(res.Message, "Chatter is in front now instead of Dinely, "+switchedWords) ||
		!strings.Contains(res.Message, "phone_handoff") {
		t.Fatalf("message %q", res.Message)
	}

	// Said once: the agent has seen Chatter now.
	res = s.Observe(context.Background(), ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("said again: %q", res.Message)
	}
}

// An action aimed at the app the agent saw is not sent to another app that
// came to the front meanwhile; the next one, aimed at what it shows, is.
func TestAppSwitchStaleAction(t *testing.T) {
	g, dev := newTestGateway(t, noSwitchGrace)
	s := inDinely(t, g)
	before := len(dev.Inputs())
	dev.Show("chats")

	res := s.Act(context.Background(), Call{}, Action{Tool: "tap", Point: &screen.Point{X: 500, Y: 500}})
	mustStatus(t, res, StatusStale)
	if !strings.Contains(res.Message, switchedWords) || !strings.Contains(res.Message, "Nothing was done") {
		t.Fatalf("message %q", res.Message)
	}
	if res.Screen == nil || res.Screen.App.ID != demo.Chatter {
		t.Fatalf("screen %+v", res.Screen)
	}
	if n := len(dev.Inputs()); n != before {
		t.Fatalf("input sent: %+v", dev.Inputs()[before:])
	}

	res = tap(s, ref(t, res, "Sam"))
	mustStatus(t, res, StatusSettled)
	if dev.Current() != "chat" {
		t.Fatalf("on %q", dev.Current())
	}
}

// Launching an app and pressing home aim at no screen, and go through.
func TestAppSwitchLaunchAndHomeGo(t *testing.T) {
	for _, a := range []Action{{Tool: "launch", App: "Dinely"}, {Tool: "key", Key: "home"}} {
		g, dev := newTestGateway(t, noSwitchGrace)
		s := inDinely(t, g)
		dev.Show("chats")
		res := s.Act(context.Background(), Call{}, a)
		mustStatus(t, res, StatusSettled)
		if strings.Contains(res.Message, switchedWords) {
			t.Fatalf("%s: %q", a.Tool, res.Message)
		}
	}
}

// An app that comes to the front soon after the agent's action, as a link
// may open its app late, counts as the action's doing.
func TestAppSwitchGraceAfterAction(t *testing.T) {
	g, dev := newTestGateway(t) // the default grace, 5 s
	s := inDinely(t, g)
	dev.Show("chats")
	res := s.Observe(context.Background(), ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note within the grace: %q", res.Message)
	}
}

// The agent's own actions, and the calls that may change the screen, are
// never taken for the app coming by itself.
func TestAppSwitchOwnDoing(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, noSwitchGrace)
	s := inDinely(t, g)

	// Home, then the app's icon: each result shows the app it opened.
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"}), StatusSettled)
	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	res = tap(s, ref(t, res, "Chatter"))
	mustStatus(t, res, StatusSettled)
	if dev.Current() != "chats" {
		t.Fatalf("on %q", dev.Current())
	}
	res = s.Observe(ctx, ObserveRequest{})
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("own tap taken for a switch: %q", res.Message)
	}

	// A batch whose first step opens an app runs its next step there.
	res = s.Batch(ctx, BatchRequest{Steps: []Action{{Tool: "launch", App: "Dinely"}, {Tool: "swipe", Direction: "up"}}})
	mustStatus(t, res, StatusSettled)
	if len(res.Steps) != 2 {
		t.Fatalf("steps %+v", res.Steps)
	}
}

// What the person left in front when they hand back is no surprise: the
// agent was paused while they used the phone.
func TestAppSwitchAfterHandBack(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, noSwitchGrace)
	s := inDinely(t, g)
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"}), StatusTookOver)
	dev.Show("chats")
	res := s.Observe(ctx, ObserveRequest{})
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note while the person is in control: %q", res.Message)
	}
	dev.Show("settings")
	if err := g.HandBack("pixel", ""); err != nil {
		t.Fatal(err)
	}
	res = s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note after hand back: %q", res.Message)
	}
	mustStatus(t, s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"}), StatusSettled)
}

// An unlock on the phone shows whatever app the person left: a locked
// screen is no app that came by itself.
func TestAppSwitchAcrossLock(t *testing.T) {
	ctx := context.Background()
	g, dev := newTestGateway(t, noSwitchGrace)
	s := inDinely(t, g)
	dev.SetLocked(true)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusLocked)
	dev.SetLocked(false)
	dev.Show("chats")
	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note after a lock: %q", res.Message)
	}
}

// A screen that names no app, such as the Android Assistant's overlay over
// the whole screen, is no app that came by itself: it gives no note and
// does not hold back an action, either way. It used to read
// " is in front now instead of Dinely" and refuse the action.
func TestAppSwitchUnnamedScreen(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v, noSwitchGrace)
	s := inDinely(t, g)
	v.setNoApp(true)

	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note for a screen with no app: %q", res.Message)
	}
	before := len(v.Inputs())
	res = s.Act(ctx, Call{}, Action{Tool: "swipe", Direction: "up"})
	mustStatus(t, res, StatusSettled)
	if len(v.Inputs()) == before {
		t.Fatal("swipe not sent")
	}

	// Back from the screen with no app: no note either.
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	v.setNoApp(false)
	res = s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, switchedWords) {
		t.Fatalf("note after a screen with no app: %q", res.Message)
	}
}
