package gateway

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestPhoneCredentials: on a phone, typing into a password field is
// refused until the person turns on PhoneCredentials (Settings › Safety);
// then it asks, and Always allow keeps it for that app. The phone's own
// passcode, an app's hidden keypad and typing without ref beside a
// password field stay refused, and turning the switch off refuses again.
func TestPhoneCredentials(t *testing.T) {
	ctx := context.Background()
	var otherApp, lockWords, authUI atomic.Bool
	g, hd := newHookGateway(t, func(o *Options) { o.Policy = loadPolicy(t, t.TempDir()) })
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		switch {
		case authUI.Load():
			sc.App = screen.App{ID: "com.apple.CoreAuthUI", Name: "Face ID"}
		case otherApp.Load():
			sc.App = screen.App{ID: "com.othereats.app", Name: "Other Eats"}
		}
		for i, l := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"} {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
				States: []string{screen.StateNotInScreenshot}, Bounds: screen.Rect{X: 100 + i%3*250, Y: 1500 + i/3*100, W: 250, H: 100}})
		}
		sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
			States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}})
		if lockWords.Load() {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleText, Label: "Enter iPhone Passcode", Source: screen.SourceTree,
				Bounds: screen.Rect{X: 100, Y: 1200, W: 800, H: 80}})
		}
	})
	pol := g.Policy()
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	refused := func(res *Result) bool { return res.Status == StatusBlocked && res.Rule == policy.RuleCredentials }
	typePassword := func() *Result {
		obs := s.Observe(ctx, ObserveRequest{})
		return s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: "hunter2"})
	}
	went := func(res *Result) bool {
		return !res.IsError() && (res.Status == StatusSettled || res.Status == StatusUncertain)
	}

	// Off by default: refused, and nothing asks.
	if pol.PhoneCredentials() {
		t.Fatal("on by default")
	}
	if res := typePassword(); !refused(res) || strings.Contains(res.Message, "virtual") {
		t.Fatalf("typed into a phone's password field by default: %+v", res)
	}
	if len(g.Needs()) != 0 {
		t.Fatalf("asked with the switch off: %+v", g.Needs())
	}

	// On: it asks, under the credentials rule, and Always allow keeps it
	// for Dinely.
	if err := pol.SetPhoneCredentials(true); err != nil {
		t.Fatal(err)
	}
	answer(t, g, AllowAlways, func(n NeedView) {
		if n.Rule != policy.RuleCredentials || n.App != "Dinely" || !n.AlwaysAllow || strings.Contains(n.Title, "hunter2") {
			t.Errorf("need %+v", n)
		}
	})
	if res := typePassword(); !went(res) {
		t.Fatalf("allowed typing did not go through: %+v", res)
	}
	if x := g.Exemptions(); len(x) != 1 || x[0].Rule != policy.RuleCredentials || x[0].AppID != demo.Dinely {
		t.Fatalf("exemptions %+v", x)
	}
	g.opt.MaxBlock = 50 * time.Millisecond
	if res := typePassword(); !went(res) || len(g.Needs()) != 0 {
		t.Fatalf("always allowed in Dinely, still asked: %+v %+v", res, g.Needs())
	}

	// Another app asks again.
	otherApp.Store(true)
	res := typePassword()
	mustStatus(t, res, StatusPending)
	if n := g.Needs()[0]; n.Rule != policy.RuleCredentials || n.App != "Other Eats" {
		t.Fatalf("other app: %+v", n)
	}
	g.Decide(res.ApprovalID, Deny, "")
	obs := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, s.Act(ctx, Call{ApprovalID: res.ApprovalID}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: "hunter2"}), StatusDenied)
	otherApp.Store(false)

	// An app's hidden passcode keypad, and typing without ref beside a
	// password field, stay refused on a phone.
	obs = s.Observe(ctx, ObserveRequest{})
	if res := tap(s, ref(t, obs, "5")); !refused(res) {
		t.Fatalf("tapped a phone's hidden keypad: %+v", res)
	}
	s.Observe(ctx, ObserveRequest{})
	if res := s.Act(ctx, Call{}, Action{Tool: "type", Text: "hunter2"}); !refused(res) {
		t.Fatalf("typed without ref beside a phone's password field: %+v", res)
	}

	// The phone's own passcode: never, the exemption notwithstanding.
	lockWords.Store(true)
	if res := typePassword(); !refused(res) || !strings.Contains(res.Message, "phone's own passcode") {
		t.Fatalf("typed where the screen asks for the iPhone's passcode: %+v", res)
	}
	lockWords.Store(false)
	authUI.Store(true)
	if res := typePassword(); !refused(res) {
		t.Fatalf("typed into the system's passcode prompt: %+v", res)
	}
	authUI.Store(false)

	// Off again: refused, the exemption notwithstanding.
	if err := pol.SetPhoneCredentials(false); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); !refused(res) {
		t.Fatalf("typed with the switch off again: %+v", res)
	}
}

// TestPhoneCredentialsLeaveVirtualPhones: PhoneCredentials does not change
// a virtual phone, which goes by VirtualCredentials alone.
func TestPhoneCredentialsLeaveVirtualPhones(t *testing.T) {
	g, dev := newTestGateway(t)
	dev.SetOverlay([]screen.Element{{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
		States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 600, W: 800, H: 120}}})
	dev.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	s, _ := connect(t, g, "Sign in to the test account")
	ctx := context.Background()
	typePassword := func() *Result {
		obs := s.Observe(ctx, ObserveRequest{})
		return s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "Password"), Text: "hunter2"})
	}
	if err := g.Policy().SetPhoneCredentials(true); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); res.Status != StatusBlocked || res.Rule != policy.RuleCredentials {
		t.Fatalf("a virtual phone with only PhoneCredentials on: %+v", res)
	}
	if err := g.Policy().SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	if res := typePassword(); res.Status == StatusBlocked || res.Status == StatusPending {
		t.Fatalf("a virtual phone with VirtualCredentials on: %+v", res)
	}
}
