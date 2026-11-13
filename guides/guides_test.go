package guides

import (
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"unicode/utf8"
)

func TestGuidesRead(t *testing.T) {
	if err := Check(); err != nil {
		t.Fatal(err)
	}
	all := All()
	if len(all) < 10 {
		t.Fatalf("%d guides, want the first ten or more", len(all))
	}
	total := 0
	for _, g := range all {
		n := utf8.RuneCountInString(g.Text())
		total += n
		t.Logf("%-12s %4d characters", g.ID, n)
	}
	t.Logf("%d guides, %d characters in all, %d on average", len(all), total, total/len(all))
}

// A guide describes the app. Rules, approvals, the tools and ways around
// them are Pinky's, and stay out of every guide (CONTRIBUTING.md).
var ruleWords = regexp.MustCompile(`(?i)\b(ignore|bypass|get around|work ?around|captcha|approv\w*|phone_\w+|you must|instruction\w*|always|never|password is|pin is)\b|验证码|密码是`)

func TestGuidesHoldNoRules(t *testing.T) {
	for _, g := range All() {
		if m := ruleWords.FindString(g.Body); m != "" {
			t.Errorf("guide %s says %q: describe the app, leave the rules to Pinky", g.ID, m)
		}
		for _, line := range strings.Split(g.Body, "\n") {
			if !strings.HasPrefix(line, "- ") {
				t.Errorf("guide %s: %q is not a list item", g.ID, line)
			}
		}
	}
}

func TestFor(t *testing.T) {
	for _, c := range []struct{ id, name, want string }{
		{"com.tencent.mm", "", "wechat"},
		{"com.tencent.xin", "WeChat", "wechat"},
		{"com.apple.mobiletimer", "", "clock"},
		{"com.google.android.apps.maps", "Maps", "google-maps"},
		{"", "微信", "wechat"},
		{"", "maps", "apple-maps"},
		// An id Pinky has no guide for: no guess from the name, which an
		// app of another maker may share.
		{"com.example.maps", "Maps", ""},
		{"", "Nothing", ""},
	} {
		g, ok := For(c.id, c.name)
		if got := map[bool]string{true: g.ID}[ok]; got != c.want {
			t.Errorf("For(%q, %q) = %q, want %q", c.id, c.name, got, c.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	long := "- " + strings.Repeat("x", MaxBody)
	for name, c := range map[string]struct{ id, data string }{
		"bad id":         {"WeChat", "---\napp: A\nandroid: [a.b]\n---\n- x\n"},
		"no front":       {"a", "- x\n"},
		"no end":         {"a", "---\napp: A\n- x\n"},
		"no app":         {"a", "---\nandroid: [a.b]\n---\n- x\n"},
		"no ids":         {"a", "---\napp: A\n---\n- x\n"},
		"no body":        {"a", "---\napp: A\nandroid: [a.b]\n---\n"},
		"long body":      {"a", "---\napp: A\nandroid: [a.b]\n---\n" + long},
		"unknown field":  {"a", "---\napp: A\nandroid: [a.b]\nrisk: high\n---\n- x\n"},
		"id with spaces": {"a", "---\napp: A\nandroid: [a b]\n---\n- x\n"},
	} {
		if _, err := Parse(c.id, []byte(c.data)); err == nil {
			t.Errorf("%s: Parse took it", name)
		}
	}
}

func TestBuildRefusesSharedIDs(t *testing.T) {
	fsys := fstest.MapFS{
		"a.md": {Data: []byte("---\napp: A\nandroid: [x.y]\n---\n- a\n")},
		"b.md": {Data: []byte("---\napp: B\nandroid: [x.y]\n---\n- b\n")},
	}
	if _, err := build(fsys); err == nil || !strings.Contains(err.Error(), "x.y") {
		t.Errorf("build = %v, want the shared id", err)
	}
}
