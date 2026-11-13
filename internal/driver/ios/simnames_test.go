package ios

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"howett.net/plist"
)

// writePlist writes v as a binary property list, as Xcode builds
// InfoPlist.strings and InfoPlist.loctable.
func writePlist(t *testing.T, path string, v any) {
	t.Helper()
	b, err := plist.Marshal(v, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A Simulator names its apps in its language, from their bundles'
// localizations (GitHub issue #7): the Simulator's apps show their
// localized names, with the Info.plist's as aliases.
func TestParseSimAppsLocalized(t *testing.T) {
	dir := t.TempDir()
	set := filepath.Join(dir, "Devices")
	writePlist(t, filepath.Join(set, "UDID", "data", "Library", "Preferences", ".GlobalPreferences.plist"),
		map[string]any{"AppleLanguages": []string{"zh-Hans-SG", "en-US"}})
	langs := simLanguages(set, "UDID")
	if !slices.Equal(langs, []string{"zh-Hans-SG", "en-US"}) {
		t.Fatalf("languages = %q", langs)
	}

	// Calendar, as the runtime ships it: zh_CN.lproj, binary.
	cal := filepath.Join(dir, "MobileCal.app")
	writePlist(t, filepath.Join(cal, "en.lproj", "InfoPlist.strings"), map[string]string{"CFBundleDisplayName": "Calendar"})
	writePlist(t, filepath.Join(cal, "zh_CN.lproj", "InfoPlist.strings"), map[string]string{"CFBundleDisplayName": "日历", "CFBundleName": "MobileCal"})
	// WeChat: zh-Hans.lproj in the text form, UTF-16.
	wx := filepath.Join(dir, "WeChat.app")
	os.MkdirAll(filepath.Join(wx, "zh-Hans.lproj"), 0o755)
	text := "/* Localized */\n\"CFBundleDisplayName\" = \"微信\";\n\"NSCameraUsageDescription\" = \"扫码\";\n"
	u := utf16.Encode([]rune(text))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	os.WriteFile(filepath.Join(wx, "zh-Hans.lproj", "InfoPlist.strings"), b, 0o644)
	// Maps: one InfoPlist.loctable for every language.
	maps := filepath.Join(dir, "Maps.app")
	writePlist(t, filepath.Join(maps, "InfoPlist.loctable"), map[string]any{
		"en": map[string]string{"CFBundleDisplayName": "Maps"}, "zh_CN": map[string]string{"CFBundleDisplayName": "地图"},
	})
	// English only: the person's second language serves it.
	en := filepath.Join(dir, "Tool.app")
	writePlist(t, filepath.Join(en, "en.lproj", "InfoPlist.strings"), map[string]string{"CFBundleDisplayName": "Handy Tool"})
	// No localizations at all.
	none := filepath.Join(dir, "Plain.app")
	os.MkdirAll(none, 0o755)

	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	app := func(id, display, path string) string {
		return q(id) + " = { ApplicationType = User; CFBundleDisplayName = " + q(display) + "; Path = " + q(path) + "; };\n"
	}
	out := "{\n" + app("com.apple.mobilecal", "Calendar", cal) + app("com.tencent.xin", "WeChat", wx) +
		app("com.apple.Maps", "Maps", maps) + app("com.example.tool", "Tool", en) + app("com.example.plain", "Plain", none) + "}"
	apps, err := parseSimApps([]byte(out), langs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range apps {
		got[a.ID] = a.Name + "|" + strings.Join(a.Aliases, ",")
	}
	want := map[string]string{
		"com.apple.mobilecal": "日历|Calendar",
		"com.tencent.xin":     "微信|WeChat",
		"com.apple.Maps":      "地图|Maps",
		"com.example.tool":    "Handy Tool|Tool",
		"com.example.plain":   "Plain|",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %q, want %q", id, got[id], w)
		}
	}

	// In English, the same apps keep their English names.
	apps, _ = parseSimApps([]byte(out), []string{"en-GB"})
	for _, a := range apps {
		if a.ID == "com.apple.mobilecal" && (a.Name != "Calendar" || len(a.Aliases) != 0) {
			t.Errorf("English Calendar = %+v", a)
		}
	}
}

func TestPickLocalization(t *testing.T) {
	have := []string{"en", "en_GB", "zh_CN", "zh_TW", "zh_HK", "ja", "Base", "pt_PT"}
	for lang, want := range map[string]string{
		"zh-Hans-SG": "zh_CN", "zh-Hans": "zh_CN", "zh-Hant-TW": "zh_TW", "zh-Hant-HK": "zh_HK",
		"zh-HK": "zh_HK", "en-GB": "en_GB", "en-US": "en", "ja-JP": "ja", "pt-PT": "pt_PT", "fr-FR": "",
	} {
		if got := pickLocalization(lang, have); got != want {
			t.Errorf("%s: %q, want %q", lang, got, want)
		}
	}
	if got := pickLocalization("en-US", []string{"English", "Japanese"}); got != "English" {
		t.Errorf("old bundle: %q", got)
	}
}
