package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every bundled MingCute icon, of the free set and of Pro, is one the app
// shows: the folders hold the icons in use and no others
// (icons/mingcute/README.md).
func TestBundledIconsAreUsed(t *testing.T) {
	var names []string
	for _, dir := range []string{"", "pro/"} {
		files, err := mingcuteSVGs.ReadDir(strings.TrimSuffix("icons/mingcute/"+dir, "/"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if !f.IsDir() {
				names = append(names, dir+strings.TrimSuffix(f.Name(), ".svg"))
			}
		}
	}
	srcs, _ := filepath.Glob("*.go")
	var code strings.Builder
	for _, src := range srcs {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		code.Write(b)
	}
	for _, name := range names {
		if !strings.Contains(code.String(), `mingcute("`+name+`")`) {
			t.Errorf("icons/mingcute/%s.svg is bundled but not shown: remove it", name)
		}
	}
}
