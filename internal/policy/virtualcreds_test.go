package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestVirtualCredentials: the credentials rule is lifted only for a
// virtual phone while VirtualCredentials is on, off by default; the switch
// is saved in the rules file and survives a restart.
func TestVirtualCredentials(t *testing.T) {
	dir := t.TempDir()
	rules, apps := filepath.Join(dir, "rules.yaml"), filepath.Join(dir, "apps.yaml")
	e, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	app := screen.App{ID: "com.example.shop", Name: "Shop"}
	pw := Input{Op: OpType, App: app, Text: "hunter2", Target: &screen.Element{Role: screen.RoleInput, States: []string{screen.StatePassword}}}
	otp := Input{Op: OpType, App: app, Text: "123456", Target: &screen.Element{Role: screen.RoleInput, Label: "Verification code"}}
	refused := func(in Input) bool { return e.Evaluate(in).Rule == RuleCredentials }
	virtual := func(in Input) Input { in.Virtual = true; return in }

	if e.VirtualCredentials() || e.CredentialTypingAllowed(true) {
		t.Fatal("on by default")
	}
	if !refused(pw) || !refused(virtual(pw)) || !refused(virtual(otp)) {
		t.Fatal("credentials typed with the switch off")
	}
	if v := e.Evaluate(virtual(pw)); !strings.Contains(v.Reason, "Settings › Safety") {
		t.Errorf("a virtual phone's refusal does not say where to allow it: %q", v.Reason)
	}
	if v := e.Evaluate(pw); strings.Contains(v.Reason, "virtual phone") {
		t.Errorf("a phone's refusal names virtual phones: %q", v.Reason)
	}

	if err := e.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	if refused(virtual(pw)) || refused(virtual(otp)) || !e.CredentialTypingAllowed(true) {
		t.Fatal("a virtual phone still refused with the switch on")
	}
	if !refused(pw) || !refused(otp) || e.CredentialTypingAllowed(false) {
		t.Fatal("a phone allowed credentials")
	}

	b, err := os.ReadFile(rules)
	if err != nil || !strings.Contains(string(b), "virtual_credentials: true") {
		t.Fatalf("not saved: %v\n%s", err, b)
	}
	e2, err := Load(rules, apps)
	if err != nil {
		t.Fatal(err)
	}
	if !e2.VirtualCredentials() {
		t.Fatal("lost after a restart")
	}
	if err := e2.SetVirtualCredentials(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rules); strings.Contains(string(b), "\nvirtual_credentials:") {
		t.Errorf("off is written out:\n%s", b)
	}
}
