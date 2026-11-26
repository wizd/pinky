package policy

import (
	"strings"

	"github.com/DIYgod/pinky/internal/screen"
)

// The phone's own passcode is never typed, whatever the person allows:
// with File.PhoneCredentials on, typing into a password field on a phone
// asks the person, but never where the field may take the passcode that
// unlocks the phone. That is the lock screen, the system's own prompts for
// the passcode (iOS's "Enter iPhone Passcode" over an app, Android's
// "Confirm your PIN") and the settings that change it. Pinky's unlock from
// the computer (gateway.Unlock) enters a PIN the person saved, never one
// an agent gives.

// deviceLockApps are the apps that ask for the phone's own passcode, by id.
// They are here as well as in their categories (system, settings), which
// the person's apps.yaml may change.
var deviceLockApps = map[string]bool{
	"com.apple.springboard":        true, // the lock screen and its alerts
	"com.apple.CoreAuthUI":         true, // Face ID's passcode fallback over an app
	"com.apple.Preferences":        true, // Face ID & Passcode
	"com.android.systemui":         true, // the keyguard and the biometric prompt
	"com.android.settings":         true, // Confirm your PIN
	"com.samsung.android.settings": true,
	"com.google.android.gms":       true, // Google's screen-lock check
	"com.android.keyguard":         true,
	"com.miui.securitycenter":      true,
}

// phoneLockWords name the phone's own passcode on its prompts. They are
// matched in lower case, with spaces taken out, anywhere on the screen.
var phoneLockWords = []string{
	"iphonepasscode", "ipadpasscode", "devicepasscode", "phonepasscode", "screenlock", "lockscreen",
	"devicepin", "phonepin", "devicepassword", "confirmyourpin", "confirmyourpassword",
	"confirmyourpattern", "useyourpin", "useyourpassword", "enteryourpin",
	"iphone密码", "ipad密码", "锁屏密码", "设备密码", "手机密码", "解锁密码", "屏幕锁定",
	"iphone密碼", "ipad密碼", "鎖定螢幕", "裝置密碼", "解鎖密碼",
	"iphoneのパスコード", "ipadのパスコード", "画面ロック", "デバイスのパスコード",
}

// PhoneLockScreen reports whether typing a password on this screen of a
// phone could enter the phone's own passcode: the phone is locked, the app
// in front is one that asks for it or cannot be told (deviceLockApps, the
// system and settings categories, an app without id or name), or the
// screen names it (phoneLockWords). cats are the app's categories.
func PhoneLockScreen(in Input, cats []string) bool {
	if in.Screen != nil && in.Screen.Locked {
		return true
	}
	app := in.App
	id, name := strings.TrimSpace(app.ID), strings.TrimSpace(app.Name)
	if id == "" && name == "" {
		return true
	}
	if deviceLockApps[id] || contains(cats, CatSystem) || contains(cats, CatSettings) {
		return true
	}
	if namesPhoneLock(in.Target) {
		return true
	}
	if in.Screen != nil {
		for i := range in.Screen.Elements {
			if namesPhoneLock(&in.Screen.Elements[i]) {
				return true
			}
		}
	}
	return false
}

// namesPhoneLock reports whether an element's text names the phone's own
// passcode.
func namesPhoneLock(e *screen.Element) bool {
	if e == nil {
		return false
	}
	for _, s := range []string{e.Label, e.Content} {
		if s == "" {
			continue
		}
		s = strings.ToLower(strings.Join(strings.Fields(s), ""))
		for _, w := range phoneLockWords {
			if strings.Contains(s, w) {
				return true
			}
		}
	}
	return false
}
