package ios

import (
	"math"
	"os"
	"strconv"
)

// Frames on a foldable. On an iPhone Duo (iPhone19,4, iOS 27.3) unfolded and
// turned to landscape, measured on 2026-11-11, remoteAXService does not
// describe the inner display the way it does a one-screen iPhone:
//
//   - An app's own frame is the display's frame, not turned: 669x951 points
//     while its elements are laid out in the 951x669 points of the interface
//     the right way up. Scaling by the app's width (2853/669) put every
//     element 1.42 times too far from the top left corner.
//   - SpringBoard's frame is the outer display's (466x678), and its icons are
//     in the inner display's own frame, not turned: Files at y 816 of 951.
//
// raxScale and turnRaxFrames read both cases from the frames themselves, and
// only on a second display (displayId 2 or more, as touchSurface): every
// other screen keeps the scale from the app's width and its frames as they
// come, so that a one-screen phone whose elements happen to fit the turned
// frame (a scrolled list in a split view, in landscape) is not misread.

// near reports whether a is within 3% of b.
func near(a, b float64) bool { return b != 0 && math.Abs(a-b)/math.Abs(b) < 0.03 }

// raxScale returns the pixels per point of a walk's frames on the screen si
// describes the right way up.
func raxScale(app raxElement, si screenInfo) float64 {
	if si.DisplayID < 2 {
		if app.HasFrame && app.Frame.W > 0 {
			return float64(si.Width) / app.Frame.W
		}
		return si.Scale
	}
	points := si.Scale
	w, h := float64(si.Width), float64(si.Height)
	if !app.HasFrame || app.Frame.W <= 0 || app.Frame.H <= 0 || w <= 0 || h <= 0 {
		return points
	}
	ratio := app.Frame.W / app.Frame.H
	upright := w / app.Frame.W
	switch {
	case near(ratio, w/h) && (points <= 0 || near(upright, points) || !near(h/app.Frame.W, points)):
		// The app's frame is the interface's, as on every one-screen phone
		// (and an iPhone app on an iPad, whose frame is smaller than the
		// screen: that is why the frame, not the point scale, decides).
		return upright
	case near(ratio, h/w) && (points <= 0 || near(h/app.Frame.W, points)):
		return h / app.Frame.W // the frame is the display's, not turned
	case points > 0:
		return points
	}
	return upright
}

// turnRaxFrames returns the elements with their frames turned into the
// interface's, when they are in the display's own frame instead: the
// elements reach past the interface's bottom edge yet fit the display's
// frame, which the interface is a quarter turn from. Otherwise, and on any
// display but a second one, it returns els as they are.
func turnRaxFrames(els []raxElement, si screenInfo, scale float64) []raxElement {
	q := si.turn()
	if si.DisplayID < 2 || !q.swaps() || scale <= 0 || len(els) < 2 {
		return els
	}
	w, h := float64(si.Width)/scale, float64(si.Height)/scale // the interface, in points
	var maxX, maxY float64
	for _, e := range els[1:] {
		if !e.HasFrame || e.Frame.W <= 0 || e.Frame.H <= 0 {
			continue
		}
		maxX = math.Max(maxX, e.Frame.X+e.Frame.W)
		maxY = math.Max(maxY, e.Frame.Y+e.Frame.H)
	}
	if maxY <= h*1.04 || maxY > w*1.04 || maxX > h*1.04 {
		return els
	}
	// The display's frame is h wide and w tall, in points.
	out := make([]raxElement, len(els))
	copy(out, els)
	for i := range out {
		if i == 0 || !out[i].HasFrame {
			continue
		}
		out[i].Frame = turnRect(out[i].Frame, h, w, q)
	}
	return out
}

// turnRect returns where rectangle r of a w×h frame lands when the frame
// turns q clockwise.
func turnRect(r axpRect, w, h float64, q quarter) axpRect {
	x0, y0 := turnPointF(r.X, r.Y, w, h, q)
	x1, y1 := turnPointF(r.X+r.W, r.Y+r.H, w, h, q)
	return axpRect{X: math.Min(x0, x1), Y: math.Min(y0, y1), W: math.Abs(x1 - x0), H: math.Abs(y1 - y0)}
}

func turnPointF(x, y, w, h float64, q quarter) (float64, float64) {
	switch q & 3 {
	case 1:
		return h - y, x
	case 2:
		return w - x, h - y
	case 3:
		return y, w - x
	}
	return x, y
}

// touchTurnOverride is PINKY_IOS_TOUCH_TURN, a quarter turn 0 to 3 that
// replaces the touch mapping's own, for measuring a phone whose touches land
// wrong. Unset in normal use.
func touchTurnOverride() (quarter, bool) {
	v, err := strconv.Atoi(os.Getenv("PINKY_IOS_TOUCH_TURN"))
	if err != nil || v < 0 || v > 3 {
		return 0, false
	}
	return quarter(v), true
}

// touchService is the HID service touches go to: surface (0 for the
// built-in touchscreen), or PINKY_IOS_TOUCH_SERVICE when set, for measuring
// a phone with more than one touchscreen.
func touchService(surface uint64) uint64 {
	if v, err := strconv.ParseUint(os.Getenv("PINKY_IOS_TOUCH_SERVICE"), 0, 64); err == nil && v != 0 {
		return v
	}
	if surface != 0 {
		return surface
	}
	return surfaceTouchscreen
}

// touchSurface is the touchscreen over the display in use. Each display of
// a phone with two (iPhone Duo) has its own, at service 256 plus the
// display's id: measured on 2026-11-11 on an iPhone Duo, iOS 27, unfolded
// in landscape, its inner display (displayId 2) takes touches at 258 and
// its outer one (displayId 1) at 257, the built-in id. Touches sent to 257
// while the inner display is in use reach no app. The inner panel's
// 0..65535 range is in that display's own frame turned as touchTurn says,
// like any other, so only the service differs. Phones with one display name
// no displayId (0) and keep 257; the display in use is always one of the
// phone's own (chooseDisplay), so an id of 2 or more means a second screen.
func touchSurface(si screenInfo) uint64 {
	if si.DisplayID >= 2 {
		return 256 + uint64(si.DisplayID)
	}
	return 0
}
