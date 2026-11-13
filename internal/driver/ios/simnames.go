package ios

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf16"

	"howett.net/plist"
)

// Naming a Simulator's apps as its home screen does. simctl lists each app
// with its Info.plist's own names ("Calendar", "WeChat"), while the home
// screen shows the app's localized name in the Simulator's language ("日历",
// "微信"), from the app's <language>.lproj/InfoPlist.strings or its
// InfoPlist.loctable. Both live in the app's bundle on the Mac, so Pinky
// reads them there (GitHub issue #7).

// simLanguages reads a Simulator's preferred languages (AppleLanguages,
// "zh-Hans-SG") from its global preferences in the device set; nil when it
// cannot.
func simLanguages(setDir, udid string) []string {
	if setDir == "" || udid == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(setDir, udid, "data", "Library", "Preferences", ".GlobalPreferences.plist"))
	if err != nil {
		return nil
	}
	var prefs struct {
		AppleLanguages []string `plist:"AppleLanguages"`
	}
	if _, err := plist.Unmarshal(b, &prefs); err != nil {
		return nil
	}
	return prefs.AppleLanguages
}

// localizedAppName returns the name an app's bundle gives itself in the
// first of langs it is localized for, as iOS chooses: "" when it has no
// such localization or names itself there as it does in its Info.plist.
func localizedAppName(bundle string, langs []string) string {
	if bundle == "" || len(langs) == 0 {
		return ""
	}
	entries, err := os.ReadDir(bundle)
	if err != nil {
		return ""
	}
	var lprojs []string
	hasLoctable := false
	for _, e := range entries {
		switch n := e.Name(); {
		case e.IsDir() && strings.HasSuffix(n, ".lproj"):
			lprojs = append(lprojs, strings.TrimSuffix(n, ".lproj"))
		case n == "InfoPlist.loctable":
			hasLoctable = true
		}
	}
	var table map[string]map[string]any
	if hasLoctable {
		if b, err := os.ReadFile(filepath.Join(bundle, "InfoPlist.loctable")); err == nil {
			if _, err := plist.Unmarshal(b, &table); err != nil {
				table = nil
			}
		}
	}
	var keys []string
	for k := range table {
		keys = append(keys, k)
	}
	for _, lang := range langs {
		// The app's own localizations decide, in the person's order: the
		// first language it has wins, also when it names nothing there.
		k, l := pickLocalization(lang, keys), pickLocalization(lang, lprojs)
		if k == "" && l == "" {
			continue
		}
		if n := bundleName(table[k]); k != "" && n != "" {
			return n
		}
		if l != "" {
			return bundleName(readStrings(filepath.Join(bundle, l+".lproj", "InfoPlist.strings")))
		}
		return ""
	}
	return ""
}

// bundleName is the display name, else the name, of an InfoPlist table.
func bundleName(m map[string]any) string {
	for _, k := range []string{"CFBundleDisplayName", "CFBundleName"} {
		if s, _ := m[k].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// pickLocalization finds the localization of have that serves a language
// ("zh-Hans-SG"): the language itself, then without its region, then
// without its script, in the names bundles use for them ("zh_CN" for
// Simplified Chinese, "English" for English in old bundles); "" for none.
func pickLocalization(lang string, have []string) string {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "-")) }
	byName := map[string]string{}
	for _, h := range have {
		byName[norm(h)] = h
	}
	for _, c := range localizationCandidates(norm(lang)) {
		if h, ok := byName[c]; ok {
			return h
		}
	}
	return ""
}

// localizationCandidates lists the names a language's localization may
// have, best first; lang is lower case with dashes.
func localizationCandidates(lang string) []string {
	parts := strings.Split(lang, "-")
	var out []string
	add := func(s ...string) {
		for _, x := range s {
			if x != "" && !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	add(lang)
	if parts[0] == "zh" {
		script := ""
		region := ""
		for _, p := range parts[1:] {
			switch {
			case len(p) == 4:
				script = p
			case len(p) == 2:
				region = p
			}
		}
		if script == "" {
			script = "hans"
			if region == "tw" || region == "hk" || region == "mo" {
				script = "hant"
			}
		}
		if script == "hant" {
			if region == "hk" || region == "mo" {
				add("zh-hant-hk", "zh-hk")
			}
			add("zh-hant", "zh-tw", "zh-hk")
		} else {
			add("zh-hans", "zh-cn")
		}
		return out
	}
	for i := len(parts) - 1; i >= 1; i-- {
		add(strings.Join(parts[:i], "-"))
	}
	legacy := map[string]string{"en": "english", "ja": "japanese", "fr": "french", "de": "german", "es": "spanish", "it": "italian", "nl": "dutch"}
	add(legacy[parts[0]])
	return out
}

// readStrings reads a .strings file: a property list (binary or XML, as
// Xcode builds them) or the text form, `"key" = "value";`, in UTF-8 or
// UTF-16.
func readStrings(path string) map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]any
	if _, err := plist.Unmarshal(b, &m); err == nil {
		return m
	}
	text := decodeText(b)
	if _, err := plist.Unmarshal([]byte("{"+text+"\n}"), &m); err == nil {
		return m
	}
	return nil
}

// decodeText turns a text file's bytes into UTF-8, from UTF-16 when it
// starts with a byte order mark.
func decodeText(b []byte) string {
	var order binary.ByteOrder
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		order = binary.LittleEndian
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		order = binary.BigEndian
	default:
		return string(bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF}))
	}
	b = b[2:]
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = order.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}
