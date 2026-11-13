package policy

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// The screens of Pinky's Android app hold the person's approvals, Stop and
// rules: they are blocked, no task can lift that, and the agent can neither
// open them nor act on them, nor stop or uninstall the app. Pressing home
// still works, so the agent can leave. Other apps are not blocked by it.
func TestPinkyScreenBlocked(t *testing.T) {
	e := Default()
	for _, pkg := range []string{"app.pinky.companion", "app.pinky.companion.dev"} {
		pinky := screen.App{ID: pkg, Name: "Pinky"}
		if rule, blocked := e.Blocked(pinky); !blocked || rule != RulePinkyApps {
			t.Fatalf("Blocked(%s) = %q, %v", pkg, rule, blocked)
		}
		if rule, blocked := e.Blocked(screen.App{ID: pkg + "@10"}); !blocked || rule != RulePinkyApps {
			t.Fatalf("Blocked(%s in a work profile) = %q, %v", pkg, rule, blocked)
		}

		for _, in := range []Input{
			{Op: OpLaunch, Launch: pinky},
			{Op: OpLaunch, Launch: pinky, AllowBlocked: true},
			{Op: OpTap, App: pinky, Target: &screen.Element{Role: screen.RoleButton, Label: "Allow once"}},
			{Op: OpTap, App: pinky, AllowBlocked: true},
			{Op: OpKey, Key: "back", App: pinky},
			{Op: OpStop, Launch: pinky},
			{Op: OpUninstall, Launch: pinky},
		} {
			if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RulePinkyApps {
				t.Errorf("%s %s: %+v", pkg, in.Op, v)
			}
		}
		if v := e.Evaluate(Input{Op: OpKey, Key: "home", App: pinky}); v.Decision == Deny {
			t.Errorf("home on %s: %+v", pkg, v)
		}
	}
	for _, other := range []string{"app.pinky.companionx", "app.pinky.standalone", "com.android.settings"} {
		if _, blocked := e.Blocked(screen.App{ID: other}); blocked {
			t.Errorf("%s is blocked", other)
		}
	}
}

// Pinky's iPhone app is blocked the same way, under each id it gets: its
// own builds, desktop Pinky's builds with the person's team, and SideStore's.
// Other apps whose ids merely start alike are not, nor are ids under
// "pinky." without a team id after it.
func TestIPhoneAppBlocked(t *testing.T) {
	e := Default()
	for _, id := range []string{"cc.diygod.pinky", "cc.diygod.pinky.standalone", "cc.diygod.pinky.standalone.tunnel",
		"cc.diygod.pinky.ABCDE12345", "pinky.abcde12345", "pinky.abcde12345.activity", "PINKY.ABCDE12345.Tunnel",
		"CC.DIYGOD.PINKY.APP"} {
		app := screen.App{ID: id, Name: "Pinky"}
		if !IsPinkyScreen(app) || !IsPinkyApp(app) {
			t.Errorf("%s: IsPinkyScreen %v, IsPinkyApp %v", id, IsPinkyScreen(app), IsPinkyApp(app))
		}
		if rule, blocked := e.Blocked(app); !blocked || rule != RulePinkyApps {
			t.Errorf("Blocked(%s) = %q, %v", id, rule, blocked)
		}
		for _, in := range []Input{
			{Op: OpLaunch, Launch: app, AllowBlocked: true},
			{Op: OpTap, App: app, AllowBlocked: true},
			{Op: OpUninstall, Launch: app},
		} {
			if v := e.Evaluate(in); v.Decision != Deny || v.Rule != RulePinkyApps {
				t.Errorf("%s %s: %+v", id, in.Op, v)
			}
		}
	}
	for _, id := range []string{"cc.diygod.pinkyclone", "com.example.pinky", "pinky.notes", "pinky.app.reader",
		"pinky.abcde1234", "pinky.abcde123456", "pinky.abcde-1234.app"} {
		if _, blocked := e.Blocked(screen.App{ID: id}); blocked {
			t.Errorf("%s is blocked", id)
		}
	}
}

// XCTest runners are Pinky's apps, which agents cannot uninstall or stop,
// but their screens are not blocked, even under ids that would otherwise
// read as Pinky's iPhone app: Pinky Helper built with the person's team is
// pinky.<team id>.WebDriverAgentRunner.xctrunner.
func TestXCTestRunnerNotBlocked(t *testing.T) {
	e := Default()
	for _, id := range []string{"pinky.abcde12345.WebDriverAgentRunner.xctrunner", "cc.diygod.pinky.helper.xctrunner",
		"com.example.WebDriverAgentRunner.xctrunner"} {
		app := screen.App{ID: id, Name: "Pinky Helper"}
		if IsPinkyScreen(app) {
			t.Errorf("IsPinkyScreen(%s)", id)
		}
		if !IsPinkyApp(app) {
			t.Errorf("IsPinkyApp(%s) = false", id)
		}
		if rule, blocked := e.Blocked(app); blocked {
			t.Errorf("Blocked(%s) = %q", id, rule)
		}
		if v := e.Evaluate(Input{Op: OpTap, App: app}); v.Rule == RulePinkyApps {
			t.Errorf("tap on %s: %+v", id, v)
		}
		for _, op := range []Op{OpUninstall, OpStop} {
			if v := e.Evaluate(Input{Op: op, Launch: app}); v.Decision != Deny || v.Rule != RulePinkyApps {
				t.Errorf("%s %s: %+v", op, id, v)
			}
		}
	}
}
