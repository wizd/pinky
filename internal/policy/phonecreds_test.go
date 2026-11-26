package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestPhoneCredentials: on a phone, typing into a password or code field
// is refused while PhoneCredentials is off (the default), and asks under
// the credentials rule while it is on; Always allow lifts that ask in one
// app. The phone's own passcode stays refused, read-only mode still
// refuses, and virtual phones go by VirtualCredentials as before.
func TestPhoneCredentials(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	app := screen.App{ID: "com.example.shop", Name: "Shop"}
	field := func(label string, states ...string) *screen.Element {
		return &screen.Element{Role: screen.RoleInput, Label: label, States: states}
	}
	pw := Input{Op: OpType, App: app, Text: "hunter2", Target: field("Password", screen.StatePassword)}
	otp := Input{Op: OpType, App: app, Text: "123456", Target: field("Verification code")}
	virtual := func(in Input) Input { in.Virtual = true; return in }

	// Off by default.
	if e.PhoneCredentials() {
		t.Fatal("on by default")
	}
	for _, in := range []Input{pw, otp, virtual(pw)} {
		if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RuleCredentials {
			t.Fatalf("off: %+v", v)
		}
	}

	// On: a phone asks, a virtual phone is refused as before.
	if err := e.SetPhoneCredentials(true); err != nil {
		t.Fatal(err)
	}
	for _, in := range []Input{pw, otp} {
		v := e.Evaluate(in)
		if v.Decision != Ask || v.Rule != RuleCredentials || len(v.Asks) == 0 || v.Asks[0] != RuleCredentials {
			t.Fatalf("on, a phone: %+v", v)
		}
		if !strings.Contains(v.Reason, "Shop") {
			t.Errorf("the ask does not name the app: %q", v.Reason)
		}
	}
	if v := e.Evaluate(virtual(pw)); v.Decision != Deny || v.Rule != RuleCredentials {
		t.Fatalf("on, a virtual phone without VirtualCredentials: %+v", v)
	}
	if e.CredentialTypingAllowed(false) {
		t.Fatal("the hidden keypad and unknown focus are lifted on a phone")
	}
	if v := e.Evaluate(Input{Op: OpType, App: app, Text: "hi", Target: field("Name")}); v.Rule == RuleCredentials {
		t.Fatalf("an ordinary field: %+v", v)
	}

	// The phone's own passcode: never.
	withApp := func(id, name string) Input { in := pw; in.App = screen.App{ID: id, Name: name}; return in }
	withField := func(label string) Input { in := pw; in.Target = field(label, screen.StatePassword); return in }
	locked, prompt := pw, pw
	locked.Screen = &screen.Screen{App: app, Locked: true}
	prompt.Screen = &screen.Screen{App: app, Elements: []screen.Element{{Role: screen.RoleText, Label: "Enter iPhone Passcode"}}}
	lock := map[string]Input{
		"locked":      locked,
		"springboard": withApp("com.apple.springboard", "SpringBoard"),
		"face id":     withApp("com.apple.CoreAuthUI", "Face ID"),
		"settings":    withApp("com.android.settings", "Settings"),
		"systemui":    withApp("com.android.systemui", "System UI"),
		"no app":      withApp("", ""),
		"prompt":      prompt,
		"field":       withField("Confirm your PIN"),
		"chinese":     withField("输入锁屏密码"),
	}
	for name, in := range lock {
		v := e.Evaluate(in)
		if v.Decision != Deny || v.Rule != RuleCredentials || !strings.Contains(v.Reason, "phone's own passcode") {
			t.Errorf("%s: %+v", name, v)
		}
	}

	// Always allow lifts the ask in that app only, never the passcode.
	if _, err := e.Exempt([]string{RuleCredentials}, app); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(pw); v.Decision != Allow {
		t.Fatalf("always allowed: %+v", v)
	}
	other := pw
	other.App = screen.App{ID: "com.example.other", Name: "Other"}
	if v := e.Evaluate(other); v.Decision != Ask {
		t.Fatalf("another app: %+v", v)
	}
	for name, in := range lock {
		if in.App == app {
			if v := e.Evaluate(in); v.Decision != Deny {
				t.Errorf("%s, always allowed: %+v", name, v)
			}
		}
	}

	// Read-only mode refuses it, as any typing outside search fields.
	ro := pw
	ro.ReadOnly = true
	if v := e.Evaluate(ro); v.Decision != Deny || v.Rule != RuleReadOnly {
		t.Fatalf("read-only: %+v", v)
	}

	// Virtual phones as before: VirtualCredentials lets them type without
	// asking.
	if err := e.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	if v := e.Evaluate(virtual(other)); v.Decision != Allow {
		t.Fatalf("a virtual phone with VirtualCredentials: %+v", v)
	}

	// Saved, and off takes it back, the exemption notwithstanding.
	b, err := os.ReadFile(rules)
	if err != nil || !strings.Contains(string(b), "phone_credentials: true") {
		t.Fatalf("not saved: %v\n%s", err, b)
	}
	e2, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if !e2.PhoneCredentials() {
		t.Fatal("lost after a restart")
	}
	if err := e2.SetPhoneCredentials(false); err != nil {
		t.Fatal(err)
	}
	if v := e2.Evaluate(pw); v.Decision != Deny || v.Rule != RuleCredentials {
		t.Fatalf("off again, always allowed: %+v", v)
	}
	if b, _ := os.ReadFile(rules); strings.Contains(string(b), "\nphone_credentials:") {
		t.Errorf("off is written out:\n%s", b)
	}
}
