package ios

import (
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestSwipeStopsBeforeLifting: GitHub issue #148. A swipe lifted while
// the finger still moved flung the Settings list past a whole screen, so
// rows were skipped. Swipe eases to a stop at its end and rests there
// before it lifts, so the content moves only as far as the finger did.
func TestSwipeStopsBeforeLifting(t *testing.T) {
	const w, h = 1320, 2868
	from, to := screen.Point{X: 660, Y: 2000}, screen.Point{X: 660, Y: 900}
	for _, dur := range []time.Duration{0, 300 * time.Millisecond, time.Second} {
		steps := swipeSteps(from, to, dur, w, h)
		if steps[0].P != from || steps[len(steps)-1].P != to {
			t.Fatalf("%v: from %v to %v, want %v to %v", dur, steps[0].P, steps[len(steps)-1].P, from, to)
		}
		last := steps[len(steps)-1]
		before := steps[len(steps)-2]
		if before.P != to || last.Wait < 100*time.Millisecond {
			t.Errorf("%v: the finger lifts %v after reaching %v; want it to rest at %v first", dur, last.Wait, before.P, to)
		}
		// The finger slows down on its way in: its last move is short
		// next to its average one.
		moves := 0
		for i := 1; i < len(steps); i++ {
			if steps[i].P != steps[i-1].P {
				moves++
			}
		}
		avg := (from.Y - to.Y) / moves
		lastMove := 0
		for i := len(steps) - 1; i > 0; i-- {
			if dy := steps[i-1].P.Y - steps[i].P.Y; dy != 0 {
				lastMove = dy
				break
			}
		}
		if lastMove*4 > avg {
			t.Errorf("%v: the last move is %d px against %d on average; the finger is still moving fast", dur, lastMove, avg)
		}
		var total time.Duration
		for _, s := range steps {
			total += s.Wait
		}
		want := dur
		if want == 0 {
			want = 300 * time.Millisecond
		}
		if total < want || total > want+swipeRest+20*time.Millisecond {
			t.Errorf("%v: the finger is down for %v", dur, total)
		}
	}
}

// TestEdgeSwipeFlings: a swipe from the screen's edge toward its middle is
// a system gesture that iOS takes only at speed, so it still lifts moving.
func TestEdgeSwipeFlings(t *testing.T) {
	const w, h = 1320, 2868
	for _, c := range []struct {
		name     string
		from, to screen.Point
		edge     bool
	}{
		{"home from the bottom", screen.Point{X: 660, Y: h - 1}, screen.Point{X: 660, Y: h / 2}, true},
		{"back from the left", screen.Point{X: 0, Y: 1400}, screen.Point{X: 900, Y: 1400}, true},
		{"notifications from the top", screen.Point{X: 300, Y: 10}, screen.Point{X: 300, Y: 1400}, true},
		{"from the right", screen.Point{X: w - 5, Y: 1400}, screen.Point{X: 400, Y: 1400}, true},
		{"a list", screen.Point{X: 660, Y: 2000}, screen.Point{X: 660, Y: 900}, false},
		{"toward the bottom edge", screen.Point{X: 660, Y: 900}, screen.Point{X: 660, Y: h - 1}, false},
		{"along the bottom edge", screen.Point{X: 200, Y: h - 1}, screen.Point{X: 1100, Y: h - 1}, false},
	} {
		if got := edgeSwipe(c.from, c.to, w, h); got != c.edge {
			t.Errorf("%s: edgeSwipe = %v, want %v", c.name, got, c.edge)
		}
		steps := swipeSteps(c.from, c.to, 250*time.Millisecond, w, h)
		rests := steps[len(steps)-2].P == steps[len(steps)-1].P
		if rests == c.edge {
			t.Errorf("%s: rests before lifting = %v", c.name, rests)
		}
	}
	if edgeSwipe(screen.Point{X: 0, Y: 10}, screen.Point{X: 500, Y: 500}, 0, 0) {
		t.Error("an edge swipe on a screen of unknown size")
	}
}
