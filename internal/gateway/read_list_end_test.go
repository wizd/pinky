package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// A web page's screen can show more lines than a read keeps for the next
// screen. Swiped at its end, the page does not move: the read must see
// that screen as the one before, not take the lines it did not keep as new
// ones that share nothing with it (a gap).
func TestReadListEndsOnAScreenWithMoreLinesThanItKeeps(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	page := func(first int) []string {
		var lines []string
		for n := first; n < first+readListKeep+4; n++ {
			lines = append(lines, fmt.Sprintf("Line %03d", n))
		}
		return lines
	}
	dev := &readListDevice{Device: base, lineStep: 20, pages: [][]string{page(1), page(43)}}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	listRef := 0
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 6, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop != "no_new_content" {
		t.Fatalf("read: %+v", res.Scan)
	}
	text := scanText(res.Scan)
	for n := 1; n < 43+readListKeep+4; n++ {
		if c := strings.Count(text, fmt.Sprintf("Line %03d", n)); c != 1 {
			t.Fatalf("Line %03d read %d times:\n%s", n, c, text)
		}
	}
}

// Read back up the page, the next screen overlaps this one at its top: the
// lines a read keeps of a screen that shows more must be those, or the
// overlap is lost and rows above it come twice.
func TestReadListBackwardKeepsTheTopOfATallScreen(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	page := func(first int) []string {
		var lines []string
		for n := first; n < first+readListKeep+4; n++ {
			lines = append(lines, fmt.Sprintf("Line %03d", n))
		}
		return lines
	}
	dev := &readListDevice{Device: base, reverse: true, lineStep: 20, pages: [][]string{page(101), page(51), page(1)}}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read backwards")
	listRef := 0
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, Direction: "down", MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop == "gap" || res.Scan.Pages != 3 {
		t.Fatalf("read: %+v", res.Scan)
	}
	text := scanText(res.Scan)
	for n := 1; n < 101+readListKeep+4; n++ {
		if c := strings.Count(text, fmt.Sprintf("Line %03d", n)); c != 1 {
			t.Fatalf("Line %03d read %d times:\n%s", n, c, text)
		}
	}
}

// A continuation starts on the screen the last call ended on, which did
// not move: every line of it, not only those kept for the overlap, must
// stay out, or the lines past them look new, share nothing with the kept
// ones, and the continuation stops at a gap before its first swipe.
func TestReadListContinuesFromAScreenWithMoreLinesThanItKeeps(t *testing.T) {
	page := func(first int) []string {
		var lines []string
		for n := first; n < first+readListKeep+4; n++ {
			lines = append(lines, fmt.Sprintf("Line %03d", n))
		}
		return lines
	}
	for _, tc := range []struct {
		direction string
		reverse   bool
		pages     [][]string
	}{
		{"up", false, [][]string{page(1), page(43), page(85), page(127)}},
		{"down", true, [][]string{page(127), page(85), page(43), page(1)}},
	} {
		t.Run(tc.direction, func(t *testing.T) {
			base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
			dev := &readListDevice{Device: base, reverse: tc.reverse, lineStep: 20, pages: tc.pages}
			g := newDevGateway(t, base, dev)
			s, first := connect(t, g, "Read a list")
			listRef := 0
			for _, e := range first.Screen.Elements {
				if e.Role == screen.RoleList {
					listRef = e.Ref
				}
			}
			one := s.ReadList(context.Background(), ReadListRequest{
				Ref: listRef, Direction: tc.direction, MaxScreens: 2, MaxMS: 5000, OCR: "off",
			})
			if one.IsError() || one.Scan == nil || one.Scan.Continuation == "" {
				t.Fatalf("first read: %+v", one.Scan)
			}
			two := s.ReadList(context.Background(), ReadListRequest{
				Continuation: one.Scan.Continuation, MaxScreens: 2, MaxMS: 5000, OCR: "off",
			})
			if two.IsError() || two.Scan == nil || two.Scan.Stop == "gap" || two.Scan.Pages != 2 {
				t.Fatalf("continuation: %+v\n%s", two.Scan, scanText(two.Scan))
			}
			text := scanText(one.Scan) + scanText(two.Scan)
			for n := 1; n < 127+readListKeep+4; n++ {
				if c := strings.Count(text, fmt.Sprintf("Line %03d", n)); c != 1 {
					t.Fatalf("Line %03d read %d times:\n%s", n, c, text)
				}
			}
		})
	}
}
