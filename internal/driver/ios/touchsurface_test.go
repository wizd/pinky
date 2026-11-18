package ios

import "testing"

// The touchscreen under each display, as measured on an iPhone Duo (iOS 27): the outer display (1) and phones that
// name no display keep the built-in touchscreen; the inner display (2) takes touches at 258.
func TestTouchSurface(t *testing.T) {
	for _, c := range []struct {
		id   int
		want uint64
	}{{0, 0}, {1, 0}, {2, 258}, {3, 259}} {
		if got := touchSurface(screenInfo{DisplayID: c.id}); got != c.want {
			t.Errorf("display %d: surface %d, want %d", c.id, got, c.want)
		}
	}
	t.Setenv("PINKY_IOS_TOUCH_SERVICE", "")
	if got := touchService(0); got != surfaceTouchscreen {
		t.Errorf("no surface: service %d, want the built-in %d", got, surfaceTouchscreen)
	}
	if got := touchService(258); got != 258 {
		t.Errorf("surface 258: service %d", got)
	}
}
