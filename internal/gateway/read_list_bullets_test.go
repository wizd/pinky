package gateway

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// A web page's bulleted list gives each row a "•" of its own. A swipe that
// moves the list by whole rows leaves a bullet where the last screen had
// one: that is not a fixed control, and taking it for one broke the run
// that proves two screens adjacent.
func TestReadListKeepsBulletsThatLandWhereOthersWere(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	page := func(first int) []string {
		var lines []string
		for n := first; n < first+20; n++ {
			lines = append(lines, "•", fmt.Sprintf("Row %03d", n))
		}
		return lines
	}
	dev := &readListDevice{
		Device: base, still: true, lineStep: 40,
		pages: [][]string{page(1), page(11), page(21), page(31)},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	listRef := 0
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 4, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop == "gap" || res.Scan.Pages != 4 {
		t.Fatalf("read: %+v\n%s", res.Scan, scanText(res.Scan))
	}
	text := scanText(res.Scan)
	for n := 1; n <= 50; n++ {
		if c := strings.Count(text, fmt.Sprintf("Row %03d", n)); c != 1 {
			t.Fatalf("Row %03d read %d times:\n%s", n, c, text)
		}
	}
}

// A screen that did not move keeps every line where it was, bullets too:
// they all stay out, so the read sees no new content rather than bullets
// it cannot place.
func TestFilterPinnedLeavesOutBulletsOfAScreenThatDidNotMove(t *testing.T) {
	var screen1 []viewportLine
	for i, text := range []string{"•", "Row 001", "•", "Row 002", "•", "Row 003"} {
		screen1 = append(screen1, viewportLine{Text: text, Bounds: screen.Rect{X: 100, Y: 300 + i/2*50, W: 200, H: 40}})
	}
	if got := filterPinned(screen1, screen1); len(got) != 0 {
		t.Fatalf("unmoved screen kept %q", viewportTexts(got))
	}
	moved := append([]viewportLine(nil), screen1...)
	moved[1].Text, moved[3].Text, moved[5].Text = "Row 002", "Row 003", "Row 004"
	if got, want := viewportTexts(filterPinned(screen1, moved)), viewportTexts(moved); !reflect.DeepEqual(got, want) {
		t.Fatalf("moved screen = %q, want %q", got, want)
	}
}
