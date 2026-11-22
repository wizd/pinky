package ios

import (
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// A read that remoteAXService fails for goes without the inspector walk
// when the last read it answered showed web content, and with it again
// once a read shows none.
func TestLastReadWeb(t *testing.T) {
	d := &Device{}
	if _, web := d.lastReadWeb(); web {
		t.Fatal("a phone not read yet counts as showing web content")
	}
	app := screen.App{Name: "Alipay", ID: "com.alipay.iphoneclient"}
	page := []raxElement{{Label: "Alipay"}, {Label: "row", Web: true}}
	d.noteReadWeb(app, hasWeb(page))
	if got, web := d.lastReadWeb(); !web || got != app {
		t.Fatalf("after a page: app %+v, web %v", got, web)
	}
	native := []raxElement{{Label: "Settings"}, {Label: "General"}}
	d.noteReadWeb(screen.App{Name: "Settings", ID: "com.apple.Preferences"}, hasWeb(native))
	if _, web := d.lastReadWeb(); web {
		t.Fatal("a native screen still counts as web content")
	}
}
