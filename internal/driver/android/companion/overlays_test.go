package companion

import (
	"slices"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

// Pinky's own pill and card, known by title or package, cover what lies
// under them, clipped to the display; other overlays and windows do not.
func TestOverlaysOf(t *testing.T) {
	pkg := Release.Package
	tr := &Tree{
		Display: Display{Width: 1080, Height: 2400},
		Windows: []Window{
			{ID: 8, Type: WindowAccessibilityOverlay, Title: pkg, Bounds: Bounds{186, 149, 894, 270}},
			{ID: 9, Type: WindowAccessibilityOverlay, Package: pkg, Bounds: Bounds{31, 1847, 1048, 2306},
				Region: []Bounds{{31, 1847, 1048, 2500}}},
			{ID: 10, Type: WindowAccessibilityOverlay, Title: "com.example.other", Bounds: Bounds{0, 0, 100, 100}},
			{ID: 11, Type: "application", Package: pkg, Bounds: Bounds{0, 0, 1080, 2400}},
		},
	}
	got := tr.OverlaysOf(pkg)
	want := []screen.Rect{{X: 186, Y: 149, W: 708, H: 121}, {X: 31, Y: 1847, W: 1017, H: 553}}
	if !slices.Equal(got, want) {
		t.Errorf("OverlaysOf = %v, want %v", got, want)
	}
	if got := tr.OverlaysOf("com.example.none"); len(got) != 0 {
		t.Errorf("OverlaysOf another app = %v", got)
	}
}
