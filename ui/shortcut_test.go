package ui

import (
	"runtime"
	"testing"
)

// The sidebar and the flyout write the keys the stop shortcut has: the
// app's own where it takes them itself, the desktop's where the desktop
// binds them, and none when there is no shortcut or the desktop bound none.
func TestShortcutLabel(t *testing.T) {
	own := "Ctrl+Alt+."
	if runtime.GOOS == "darwin" {
		own = "⌥⌘."
	}
	for _, tc := range []struct {
		name string
		a    *app
		want string
	}{
		{"no shortcut", &app{}, ""},
		{"the app takes the keys, or the desktop has not answered", &app{shortcut: "CmdOrCtrl+Alt+."}, own},
		{"the desktop bound keys", &app{shortcut: "CmdOrCtrl+Alt+.", keysTold: true, keys: "Meta+F9"}, "Meta+F9"},
		{"the desktop bound none", &app{shortcut: "CmdOrCtrl+Alt+.", keysTold: true}, ""},
	} {
		if got := tc.a.shortcutLabel(); got != tc.want {
			t.Errorf("%s: the label is %q, want %q", tc.name, got, tc.want)
		}
	}
}
