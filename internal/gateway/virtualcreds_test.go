package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestVirtualCredentials: typing into a password field and tapping an
// app's own passcode keypad are refused on a phone whatever the switch
// says, and on a virtual phone until the person turns on
// VirtualCredentials (Settings › Safety).
func TestVirtualCredentials(t *testing.T) {
	g, dev := newTestGateway(t)
	labels := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"}
	var els []screen.Element
	for i, l := range labels {
		els = append(els, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
			States: []string{screen.StateNotInScreenshot}, Bounds: screen.Rect{X: 100 + i%3*250, Y: 1300 + i/3*160, W: 250, H: 160}})
	}
	els = append(els, screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
		States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 600, W: 800, H: 120}})
	dev.SetOverlay(els)
	s, _ := connect(t, g, "Sign in to the test account")
	ctx := context.Background()
	refused := func(res *Result) bool { return res.Status == StatusBlocked && res.Rule == policy.RuleCredentials }
	typePassword := func() *Result {
		obs := s.Observe(ctx, ObserveRequest{})
		return s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: "hunter2"})
	}
	tapKey := func() *Result {
		obs := s.Observe(ctx, ObserveRequest{})
		return tap(s, ref(t, obs, "5"))
	}
	// No field reports focus, and the screen has a password field.
	typeUnfocused := func() *Result {
		s.Observe(ctx, ObserveRequest{})
		return s.Act(ctx, Call{}, Action{Tool: "type", Text: "hunter2"})
	}

	// A phone: refused, switch or not.
	if err := g.Policy().SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); !refused(res) {
		t.Fatalf("typed into a phone's password field: %+v", res)
	}
	if res := tapKey(); !refused(res) {
		t.Fatalf("tapped a phone's hidden keypad: %+v", res)
	}
	if res := typeUnfocused(); !refused(res) {
		t.Fatalf("typed without ref beside a phone's password field: %+v", res)
	}

	// A virtual phone, with the switch off: refused.
	dev.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	if err := g.Policy().SetVirtualCredentials(false); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); !refused(res) {
		t.Fatalf("typed with the switch off: %+v", res)
	}
	if res := tapKey(); !refused(res) {
		t.Fatalf("tapped the keypad with the switch off: %+v", res)
	}
	if res := typeUnfocused(); !refused(res) {
		t.Fatalf("typed without ref with the switch off: %+v", res)
	}

	// Turned on: both go through.
	if err := g.Policy().SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); refused(res) || res.Status == StatusBlocked {
		t.Fatalf("a virtual phone's password field, switch on: %+v", res)
	}
	if res := tapKey(); refused(res) || res.Status == StatusBlocked {
		t.Fatalf("a virtual phone's hidden keypad, switch on: %+v", res)
	}
	if res := typeUnfocused(); refused(res) {
		t.Fatalf("typing without ref on a virtual phone, switch on: %+v", res)
	}
}
