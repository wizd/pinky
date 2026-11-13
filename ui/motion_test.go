package ui

import (
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"
)

// withMotion turns the app's motion on for the rest of the test, which
// the other tests run without (stillMotion, set in TestMain).
func withMotion(t *testing.T) {
	t.Helper()
	stillMotion = false
	t.Cleanup(func() { stillMotion = true })
}

// redAt reports how red the picture is at x, y: 1 for the box's red over
// white, 0 for white.
func redAt(tt *mg.Tester, x, y int) float64 {
	c := tt.Image().RGBAAt(x, y)
	return 1 - float64(c.G)/255
}

// motionView shows a red box once *on, with the transition tr.
func motionView(on *bool, tr func() mg.ElementTransition) func(c *mg.Context) {
	return func(c *mg.Context) {
		c.Root().Background(mg.RGB(255, 255, 255))
		mg.Column(c).Fill().Children(func() {
			if *on {
				mg.Box(c.Key("box")).Size(80, 80).Background(mg.RGB(255, 0, 0)).Transition(tr())
			}
		})
	}
}

func TestSwapInFadesIn(t *testing.T) {
	withMotion(t)
	on := false
	tt := mg.NewTester(motionView(&on, swapIn), 200, 200)
	on = true
	tt.Frame()
	if r := redAt(tt, 40, 40); r > 0.5 {
		t.Fatalf("the box shows whole at once (red %.2f), not fading in", r)
	}
	tt.Advance(motionShort + 50*time.Millisecond)
	if r := redAt(tt, 40, 40); r < 0.99 {
		t.Fatalf("the box is not whole after the fade (red %.2f)", r)
	}
}

func TestStillMotionShowsAtOnce(t *testing.T) {
	for name, tr := range map[string]func() mg.ElementTransition{"swapIn": swapIn, "cardIn": cardIn, "rowIn": rowIn, "bannerIn": bannerIn} {
		on := false
		tt := mg.NewTester(motionView(&on, tr), 200, 200)
		on = true
		tt.Frame()
		if r := redAt(tt, 40, 40); r < 0.99 {
			t.Errorf("%s: the box is not whole at once in tests (red %.2f)", name, r)
		}
	}
}

// Under the system's reduced motion an arriving card fades in its place:
// it never shows away from it.
func TestCardInStaysPutWithReducedMotion(t *testing.T) {
	withMotion(t)
	// Coming from 6 points above, the box leaves its last rows white
	// early on; in place, its bottom row is as red as its top.
	moved := func(reduce bool) bool {
		on := false
		tt := mg.NewTesterWith(motionView(&on, cardIn), 200, 200, mg.TesterOptions{Preferences: mg.Preferences{ReduceMotion: reduce}})
		on = true
		tt.Frame()
		tt.Advance(30 * time.Millisecond)
		return redAt(tt, 40, 78) < redAt(tt, 40, 2)-0.05
	}
	if !moved(false) {
		t.Fatal("the card does not come down to its place with motion on")
	}
	if moved(true) {
		t.Fatal("the card moves under reduced motion")
	}
}
