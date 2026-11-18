package android

import (
	"os"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Pinduoduo's search box, as uiautomator dumps it, reads as a search box,
// so a read-only session may type into it (GitHub issue #156); without
// the search button and the headings beside it, the same field does not.
func TestPinduoduoSearchBox(t *testing.T) {
	raw, err := os.ReadFile("testdata/search/pinduoduo-search.xml")
	if err != nil {
		t.Fatal(err)
	}
	read := func(dump string) (*screen.Screen, *screen.Element) {
		tree, err := parseUIDump([]byte(dump))
		if err != nil {
			t.Fatal(err)
		}
		s := &screen.Screen{App: screen.App{Name: "拼多多", ID: tree.Package()}, Width: 1080, Height: 2400, Keyboard: true,
			Elements: convertTree(tree, 1080, 2400)}
		f, ok := s.Focused()
		if !ok {
			t.Fatalf("no focused field in\n%s", strings.Join(lines(s.Elements), "\n"))
		}
		return s, &f
	}

	s, box := read(string(raw))
	if box.Role != screen.RoleInput || box.Label != "羽绒服女冬季" || policy.IsSearchField(box) {
		t.Fatalf("the box reads as %q (%s); the fixture no longer shows issue #156", box.Label, box.Role)
	}
	if !policy.IsSearchBox(box, s) {
		t.Errorf("Pinduoduo's search box is not a search box:\n%s", strings.Join(lines(s.Elements), "\n"))
	}
	e := policy.Default()
	if err := e.SetPreset(policy.PresetPayments); err != nil {
		t.Fatal(err)
	}
	in := policy.Input{Op: policy.OpType, App: s.App, Text: "保温杯", Target: box, Screen: s, ReadOnly: true, ReadOnlyAccess: true}
	if v := e.Evaluate(in); v.Decision != policy.Allow {
		t.Errorf("read-only typing into Pinduoduo's search box: %+v", v)
	}

	bare := strings.NewReplacer(`text="搜索"`, `text=""`, `text="历史搜索"`, `text="最近"`, `text="搜索发现"`, `text="推荐"`).Replace(string(raw))
	s, box = read(bare)
	if policy.IsSearchBox(box, s) {
		t.Errorf("a field with no search button or heading reads as a search box:\n%s", strings.Join(lines(s.Elements), "\n"))
	}
}
