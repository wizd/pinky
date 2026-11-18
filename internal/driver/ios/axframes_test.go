package ios

import (
	"math"
	"testing"
)

func raxApp(w, h float64) raxElement {
	return raxElement{Frame: axpRect{W: w, H: h}, HasFrame: true}
}

// The scale of a walk's frames. A one-screen phone or iPad keeps the scale
// read from the app's width, as before raxScale; the iPhone Duo's inner
// display (measured 2026-11-11: displayId 2, bounds 2007x2853, rot90,
// pointScale 3) takes it from the display when the app's frame comes in the
// display's own frame, not turned.
func TestRaxScale(t *testing.T) {
	// The screens the right way up, as observeRAX gets them (upright).
	mini := screenInfo{Width: 1125, Height: 2436, Scale: 3, Orientation: "rot0"}      // iPhone 13 mini
	miniLand := screenInfo{Width: 2436, Height: 1125, Scale: 3, Orientation: "rot90"} // turned to landscape
	ipad := screenInfo{Width: 2048, Height: 2732, Scale: 2, Orientation: "rot0", DisplayID: 1}
	duo := screenInfo{Width: 2853, Height: 2007, Scale: 3, Orientation: "rot90", DisplayID: 2}
	for _, c := range []struct {
		name string
		app  raxElement
		si   screenInfo
		want float64
	}{
		{"iPhone upright", raxApp(375, 812), mini, 1125.0 / 375},
		{"iPhone upright, displayId 1", raxApp(375, 812), withID(mini, 1), 1125.0 / 375},
		{"iPhone landscape", raxApp(812, 375), miniLand, 2436.0 / 812},
		{"iPhone landscape, displayId 1", raxApp(812, 375), withID(miniLand, 1), 2436.0 / 812},
		// An iPhone app on an iPad: its frame is the iPhone's, smaller than the screen.
		{"iPhone app on an iPad", raxApp(375, 667), ipad, 2048.0 / 375},
		{"iPhone app on an iPad, landscape", raxApp(667, 375), screenInfo{Width: 2732, Height: 2048, Scale: 2, Orientation: "rot90"}, 2732.0 / 667},
		// A frame turned from the screen on one display is still read by its width, as before.
		{"one screen, frame not turned", raxApp(375, 812), miniLand, 2436.0 / 375},
		{"no app frame", raxElement{}, mini, 3},
		{"Duo inner, frame in the display's frame", raxApp(669, 951), duo, 3},
		{"Duo inner, frame the right way up", raxApp(951, 669), duo, 3},
		{"Duo inner, no app frame", raxElement{}, duo, 3},
	} {
		if got := raxScale(c.app, c.si); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: scale %v, want %v", c.name, got, c.want)
		}
	}
}

func withID(si screenInfo, id int) screenInfo {
	si.DisplayID = id
	return si
}

func rect(x, y, w, h float64) raxElement {
	return raxElement{Frame: axpRect{X: x, Y: y, W: w, H: h}, HasFrame: true}
}

// Frames are turned only on a second display. A one-screen phone in
// landscape whose elements all sit in the left part and run past the bottom
// edge (a scrolled list in a split view) fits the turned frame too, and
// keeps its frames as they came.
func TestTurnRaxFrames(t *testing.T) {
	split := []raxElement{
		rect(0, 0, 812, 375),   // the app: 812x375 points in landscape
		rect(0, 20, 320, 44),   // the list's rows, in its left part
		rect(0, 300, 320, 44),  //
		rect(0, 620, 320, 44),  // scrolled past the bottom edge
		rect(0, -200, 320, 44), // and past the top
	}
	for _, si := range []screenInfo{
		{Width: 2436, Height: 1125, Scale: 3, Orientation: "rot90"},
		{Width: 2436, Height: 1125, Scale: 3, Orientation: "rot270", DisplayID: 1},
		{Width: 2048, Height: 2732, Scale: 2, Orientation: "rot0", DisplayID: 1},
		{Width: 2732, Height: 2048, Scale: 2, Orientation: "rot90"},
	} {
		got := turnRaxFrames(split, si, raxScale(split[0], si))
		for i := range split {
			if got[i].Frame != split[i].Frame {
				t.Errorf("%s display %d: element %d moved to %+v from %+v", si.Orientation, si.DisplayID, i, got[i].Frame, split[i].Frame)
			}
		}
	}

	// The Duo's inner display in landscape: SpringBoard's icons come in the
	// display's own frame, 669x951 points (Files at y 816), and turn into
	// the interface's 951x669.
	duo := screenInfo{Width: 2853, Height: 2007, Scale: 3, Orientation: "rot90", DisplayID: 2}
	sb := []raxElement{rect(0, 0, 466, 678), rect(500, 816, 80, 90), rect(40, 60, 80, 90)}
	got := turnRaxFrames(sb, duo, 3)
	if got[0].Frame != sb[0].Frame {
		t.Errorf("Duo: the app's own frame moved to %+v", got[0].Frame)
	}
	for i, want := range []axpRect{{}, {X: 45, Y: 500, W: 90, H: 80}, {X: 801, Y: 40, W: 90, H: 80}} {
		if i > 0 && got[i].Frame != want {
			t.Errorf("Duo: element %d at %+v, want %+v", i, got[i].Frame, want)
		}
	}
	if sb[1].Frame.Y != 816 {
		t.Errorf("Duo: the walk's own elements changed: %+v", sb[1].Frame)
	}

	// An app on the inner display whose frames are the right way up stays.
	app := []raxElement{rect(0, 0, 951, 669), rect(700, 500, 200, 120), rect(10, 10, 40, 40)}
	got = turnRaxFrames(app, duo, 3)
	for i := range app {
		if got[i].Frame != app[i].Frame {
			t.Errorf("Duo app: element %d moved to %+v", i, got[i].Frame)
		}
	}
}
