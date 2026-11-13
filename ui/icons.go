package ui

import (
	"embed"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/assets"
)

// The app's icons are MingCute's: the free set (icons/mingcute,
// Apache-2.0) and a few of MingCute Icons System Pro (icons/mingcute/pro,
// under the owner's license). Line icons at rest, and the same icon filled
// where a row or a tab is selected (icGridOn, …). They are drawn on one grid of sizes (iconMicro
// to iconHero) in the color of the text around them, which follows the
// theme. The keys of Android's navigation and the rotation picks keep
// glyphs of their own, drawn as the systems draw them.
//
//go:embed icons/mingcute/*.svg icons/mingcute/pro/*.svg
var mingcuteSVGs embed.FS

// mingcute returns the bundled MingCute icon of that name, as
// "settings_3_line", or "pro/eye_off_line" for one of the Pro set.
func mingcute(name string) *mg.SVG {
	b, err := mingcuteSVGs.ReadFile("icons/mingcute/" + name + ".svg")
	if err != nil {
		panic(err)
	}
	return mg.MustParseSVG(b)
}

// The sizes of icons, one grid for the app.
const (
	iconMicro = 12 // in a badge or a small round button
	iconSmall = 14 // beside a caption or a small line
	iconBody  = 16 // in a row, a button, a tab, a menu, beside body text
	iconLarge = 20 // on a key under the phone, a tile, a banner
	iconBig   = 24 // over the phone's screen, and on a small tile
	iconHero  = 28 // at the head of an empty or a blocking state
)

// icon makes a line icon from SVG path data, for the few glyphs of our
// own: 24-unit box, round caps and joins, and the stroke of MingCute's
// line icons.
func icon(body string) *mg.SVG {
	return mg.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="24" height="24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

var (
	icGrid     = mingcute("grid_line")
	icPlus     = mingcute("add_line")
	icHistory  = mingcute("history_line")
	icSettings = mingcute("settings_3_line")
	icHand     = mingcute("hand_line")
	icBack     = icon(`<path d="M17 5 7 12l10 7z"/>`)
	icHome     = icon(`<circle cx="12" cy="12" r="7"/>`)
	icRecents  = icon(`<rect x="6" y="6" width="12" height="12" rx="2"/>`)
	icMore     = mingcute("more_1_line")
	icCheck    = mingcute("check_line")
	icPerson   = mingcute("user_3_line")
	icTouchID  = mingcute("fingerprint_line")
	icLock     = mingcute("lock_line")
	icUnlock   = mingcute("pro/lock_open_line")
	icCable    = mingcute("pro/cable_line")
	icWifi     = mingcute("wifi_line")
	icWifiOff  = mingcute("wifi_off_line")
	icSearch   = mingcute("search_line")
	icPlay     = mingcute("play_line")
	icPause    = mingcute("pause_line")
	icCopy     = mingcute("copy_2_line")
	icExport   = mingcute("upload_2_line")
	icX        = mingcute("close_line")
	icChevL    = mingcute("left_line")
	icChevR    = mingcute("right_line")
	icChevU    = mingcute("up_line")
	icChevD    = mingcute("down_line")
	icAndroid  = mingcute("cellphone_line")
	icIphone   = mingcute("cellphone_2_line")
	icAlert    = mingcute("alert_line")
	icExpand   = mingcute("fullscreen_line")
	icEye      = mingcute("eye_2_line")
	icEyeOff   = mingcute("pro/eye_off_line")
	icCamera   = mingcute("camera_line")
	icMonitor  = mingcute("computer_line")
	icStop     = mingcute("stop_circle_line")
	icShield   = mingcute("shield_line")
	icSliders  = mingcute("settings_6_line")
	icInbox    = mingcute("inbox_line")
	icPlug     = mingcute("plugin_line")

	icVideo     = mingcute("video_line")
	icPrivacy   = icEyeOff
	icPrivacyOn = mingcute("pro/eye_off_fill")
	icRotate    = mingcute("rotate_to_horizontal_line")
	icLayer     = mingcute("layer_line")
	icDelete    = mingcute("delete_2_line")

	// Filled, for a selected row or tab.
	icGridOn     = mingcute("grid_fill")
	icHistoryOn  = mingcute("history_fill")
	icSettingsOn = mingcute("settings_3_fill")
	icAndroidOn  = mingcute("cellphone_fill")
	icPlugOn     = mingcute("plugin_fill")
	icShieldOn   = mingcute("shield_fill")
	icSlidersOn  = mingcute("settings_6_fill")

	logo = mg.MustParseSVG(assets.Logo)
)
