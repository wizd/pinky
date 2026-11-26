package ios

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// shotWithText is a white screenshot with dark "text" drawn in each of the
// given rectangles.
func shotWithText(w, h int, text ...screen.Rect) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.White)
		}
	}
	for _, r := range text {
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				if (x/3+y/5)%2 == 0 {
					img.Set(x, y, color.Black)
				}
			}
		}
	}
	return img
}

func TestUncaptured(t *testing.T) {
	const w, h = 375, 812 // a screenshot at one pixel per point
	title := screen.Element{Role: screen.RoleText, Label: "Enter passcode", Bounds: screen.Rect{X: 100, Y: 100, W: 175, H: 40}, Source: screen.SourceTree}
	var keys []screen.Element
	for i, l := range []string{"1", "2", "3", "4", "5", "6"} {
		keys = append(keys, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
			Bounds: screen.Rect{X: 40 + (i%3)*100, Y: 400 + (i/3)*100, W: 80, H: 80}})
	}
	els := append([]screen.Element{title}, keys...)
	img := shotWithText(w, h, title.Bounds) // the keys are in a layer the screenshot leaves out
	got := uncaptured(els, img, nil, w, h)
	if len(got) != len(keys) || got[0] != 1 {
		t.Fatalf("uncaptured %v; want the six keys", got)
	}
	// Drawn keys show.
	drawn := shotWithText(w, h, title.Bounds, keys[0].Bounds, keys[1].Bounds, keys[2].Bounds, keys[3].Bounds)
	if got := uncaptured(els, drawn, nil, w, h); got != nil {
		t.Errorf("two blank keys were marked: %v", got)
	}
	// Text OCR read inside counts as shown.
	lines := []ocrLine{}
	for _, k := range keys {
		c := k.Bounds.Center()
		lines = append(lines, ocrLine{Text: k.Label, X: float64(c.X-5) / w, Y: float64(c.Y-5) / h, W: 10.0 / w, H: 10.0 / h})
	}
	if got := uncaptured(els, img, lines, w, h); got != nil {
		t.Errorf("keys OCR read were marked: %v", got)
	}
}

func TestCaptureMarks(t *testing.T) {
	var c captureMarks
	s := &screen.Screen{App: screen.App{ID: "sg.trust"}, Elements: []screen.Element{
		{Role: screen.RoleButton, Label: "1"}, {Role: screen.RoleButton, Label: "2"}, {Role: screen.RoleText, Label: "Enter passcode"},
	}}
	c.mark(s, []int{0, 1})
	if !s.Elements[0].Has(stateNotInScreenshot) || s.Elements[2].Has(stateNotInScreenshot) {
		t.Fatalf("marked %+v", s.Elements)
	}
	// A read without a screenshot keeps the marks.
	next := &screen.Screen{App: screen.App{ID: "sg.trust"}, Elements: []screen.Element{
		{Role: screen.RoleButton, Label: "1"}, {Role: screen.RoleText, Label: "Enter passcode"},
	}}
	c.apply(next)
	if !next.Elements[0].Has(stateNotInScreenshot) || next.Elements[1].Has(stateNotInScreenshot) {
		t.Errorf("applied %+v", next.Elements)
	}
	// Another app does not get them.
	other := &screen.Screen{App: screen.App{ID: "com.apple.Preferences"}, Elements: []screen.Element{{Role: screen.RoleButton, Label: "1"}}}
	c.apply(other)
	if other.Elements[0].Has(stateNotInScreenshot) {
		t.Error("marks carried over to another app")
	}
	// A screenshot that shows the element clears its mark.
	again := &screen.Screen{App: screen.App{ID: "sg.trust"}, Elements: []screen.Element{{Role: screen.RoleButton, Label: "1"}}}
	c.mark(again, nil)
	later := &screen.Screen{App: screen.App{ID: "sg.trust"}, Elements: []screen.Element{{Role: screen.RoleButton, Label: "1"}}}
	c.apply(later)
	if later.Elements[0].Has(stateNotInScreenshot) {
		t.Error("a mark stayed after a screenshot showed the element")
	}
}

// Calculator's keys: orange circles in square frames on black, with thin
// symbols. They show.
func TestUncapturedThinSymbols(t *testing.T) {
	const w, h = 375, 812
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var els []screen.Element
	for i := 0; i < 4; i++ {
		r := screen.Rect{X: 20 + i*90, Y: 600, W: 80, H: 80}
		els = append(els, screen.Element{Role: screen.RoleButton, Label: "Add", Bounds: r, Source: screen.SourceTree})
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				dx, dy := x-(r.X+40), y-(r.Y+40)
				switch {
				case dx*dx+dy*dy > 38*38:
					img.Set(x, y, color.Black)
				case dy >= -1 && dy <= 1 && dx > -15 && dx < 15:
					img.Set(x, y, color.White) // a minus sign, 3 pixels high
				default:
					img.Set(x, y, color.RGBA{255, 159, 10, 255})
				}
			}
		}
	}
	if got := uncaptured(els, img, nil, w, h); got != nil {
		t.Errorf("drawn keys marked: %v", got)
	}
}

// TestScreenOfFrame: the screen sits at 1:1 in the top-left corner of the
// video's picture, so OCR reads the frame cropped to the screen, in the
// screenshot's pixels; a smaller picture is not used.
func TestScreenOfFrame(t *testing.T) {
	f := image.NewRGBA(image.Rect(0, 0, 1136, 2464))
	f.Set(10, 20, color.RGBA{R: 255, A: 255})
	got, ok := screenOfFrame(f, screenInfo{Width: 1125, Height: 2436})
	if !ok || got.Bounds() != image.Rect(0, 0, 1125, 2436) {
		t.Fatalf("screenOfFrame = %v, %v", got.Bounds(), ok)
	}
	if r, _, _, _ := got.At(10, 20).RGBA(); r>>8 != 255 {
		t.Fatal("the crop moved the picture")
	}
	if _, ok := screenOfFrame(image.NewRGBA(image.Rect(0, 0, 568, 1232)), screenInfo{Width: 1125, Height: 2436}); ok {
		t.Fatal("a picture smaller than the screen was used")
	}
	if _, ok := screenOfFrame(f, screenInfo{}); ok {
		t.Fatal("used without a screen size")
	}
}

// TestOCRPictureFromVideo: over Wi-Fi, OCR reads a frame of the live video
// when the gateway offers one, encoded for Vision.
func TestOCRPictureFromVideo(t *testing.T) {
	d := &Device{info: driver.Info{ID: "ipad", Connection: "wifi"}, scr: screenInfo{Width: 1668, Height: 2388}}
	frame := image.NewRGBA(image.Rect(0, 0, 1680, 2400))
	opt := driver.ObserveOptions{OCR: true, Frame: func(context.Context) (image.Image, error) { return frame, nil }}
	data, pic, from, err := d.ocrPicture(context.Background(), opt)
	if err != nil || from != "video" || pic.Bounds().Dx() != 1668 {
		t.Fatalf("ocrPicture = %v, %v, %v", from, pic.Bounds(), err)
	}
	if img, err := jpeg.Decode(bytes.NewReader(data)); err != nil || img.Bounds().Dy() != 2388 {
		t.Fatalf("the picture for Vision: %v, %v", img.Bounds(), err)
	}
}

// picture is a w x h picture of one color, with "text" (a dark pattern)
// drawn in each of the given rectangles.
func picture(w, h int, bg color.RGBA, text ...screen.Rect) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bg.R, bg.G, bg.B, bg.A
	}
	for _, r := range text {
		for y := r.Y; y < r.Y+r.H; y++ {
			for x := r.X; x < r.X+r.W; x++ {
				if (x/3+y/5)%2 == 0 {
					img.SetRGBA(x, y, color.RGBA{A: 255})
				}
			}
		}
	}
	return img
}

// TestUncapturedLaunchWithBanner: after phone_launch on an iPhone 13 mini
// (375 x 812 points, 1080 x 2340 pixels), with a notification banner
// coming in, every element of the app was marked not_in_screenshot. The
// read's picture is begun as soon as the read may take it, and its tree
// comes seconds later: the picture can show the app's launch screen, one
// flat color, and the tree the app as it came up. A picture begun before
// the tree was in is checked again on one taken after it.
func TestUncapturedLaunchWithBanner(t *testing.T) {
	const w, h = 1080, 2340
	scale := raxScale(raxElement{Frame: axpRect{0, 0, 375, 812}, HasFrame: true}, screenInfo{Width: w, Height: h, Scale: 3})
	el := func(id uint64, typ int, label string, f axpRect) raxElement {
		return raxElement{Object: &axpObject{PID: 5, ID: id}, Parent: 0, Type: typ, Label: label, Frame: f,
			HasFrame: true, IsElement: true, Visible: true, Enabled: true}
	}
	root := raxElement{Object: &axpObject{PID: 5, ID: 100, IsApp: true}, Parent: -1, Label: "Clock", Frame: axpRect{0, 0, 375, 812}, HasFrame: true, Type: 1}
	tree := []raxElement{
		root,
		el(1, xcButton, "Edit", axpRect{16, 50, 40, 40}), // under the banner
		el(2, xcStaticText, "World Clock", axpRect{16, 100, 250, 41}),
		el(3, xcStaticText, "Cupertino", axpRect{16, 170, 200, 30}),
		el(4, xcStaticText, "Tokyo", axpRect{16, 250, 200, 30}),
		el(5, xcStaticText, "London", axpRect{16, 330, 200, 30}),
		el(6, xcButton, "Add", axpRect{320, 100, 40, 40}),
	}
	d := &Device{apps: &appCatalog{apps: []driver.App{{ID: "com.apple.MobileSMS", Name: "Messages"}}, at: time.Now()}}
	ban := sbOverlay{key: [2]uint64{37, 600}, els: []raxElement{
		sbEl(600, -1, 0, traitButton, "Messages, Anna, Lunch?", axpRect{8, 10, 359, 80}),
	}}
	_, covered, _, notes := d.overlayEvents(context.Background(), []sbOverlay{ban}, scale, w, h)
	app, _, _ := raxScreenElements(tree, scale, w, h, "com.apple.mobiletimer", covered...)
	// Notifications come first, as observeRAX lists them.
	els := append(append([]screen.Element(nil), notes...), app...)
	if len(notes) != 1 || len(app) != 5 {
		t.Fatalf("notes %+v, app %+v; want the banner and the five elements it does not cover", notes, app)
	}
	var drawn []screen.Rect
	for _, e := range els {
		drawn = append(drawn, e.Bounds)
	}
	white := color.RGBA{255, 255, 255, 255}
	launchScreen := picture(w, h, white, notes[0].Bounds) // the banner over the launch screen
	appShown := picture(w, h, white, drawn...)

	// The launch screen shows none of the app: on it alone every element
	// looks blank, as on the phone.
	if got := uncaptured(els, launchScreen, nil, w, h); len(got) != len(app) {
		t.Fatalf("on the launch screen uncaptured = %v; want the app's %d elements", got, len(app))
	}
	// Taken before the tree was in, it is checked on a new picture, which
	// shows the app.
	took := 0
	again := func() image.Image { took++; return appShown }
	if got := uncapturedNow(els, launchScreen, nil, w, h, true, again); got != nil || took != 1 {
		t.Fatalf("uncapturedNow = %v after %d new pictures; want none after one", got, took)
	}
	// A picture taken after the tree is trusted as it is, and a picture
	// that shows everything needs no other.
	if got := uncapturedNow(els, launchScreen, nil, w, h, false, again); len(got) != len(app) || took != 1 {
		t.Errorf("a picture taken after the tree: %v, %d new pictures", got, took)
	}
	if got := uncapturedNow(els, appShown, nil, w, h, true, again); got != nil || took != 1 {
		t.Errorf("a picture showing everything: %v, %d new pictures", got, took)
	}
	// Without a new picture, the first one stands.
	if got := uncapturedNow(els, launchScreen, nil, w, h, true, func() image.Image { return nil }); len(got) != len(app) {
		t.Errorf("no new picture: %v", got)
	}

	// A banner the picture missed (it came later) is not marked: banners
	// are in every screenshot.
	for _, i := range uncaptured(els, picture(w, h, white), nil, w, h) {
		if els[i].ID == notes[0].ID {
			t.Errorf("the banner was marked")
		}
	}

	// What the app draws where screenshots do not reach stays marked on
	// the new picture too: a passcode keypad, with the banner over it.
	keys := []raxElement{root}
	for i := range 6 {
		keys = append(keys, el(uint64(10+i), xcButton, itoa(i+1), axpRect{X: 40 + float64(i%3)*100, Y: 400 + float64(i/3)*100, W: 80, H: 80}))
	}
	pad, _, _ := raxScreenElements(keys, scale, w, h, "sg.trust", covered...)
	padEls := append(append([]screen.Element(nil), notes...), pad...)
	bannerOnly := func() image.Image { return picture(w, h, white, notes[0].Bounds) }
	if got := uncapturedNow(padEls, bannerOnly(), nil, w, h, true, bannerOnly); len(got) != len(pad) || padEls[got[0]].Label != "1" {
		t.Errorf("hidden keypad: %v; want its %d keys", got, len(pad))
	}
}

// pictureNow takes the video's frame over Wi-Fi, as OCR's picture does.
func TestPictureNow(t *testing.T) {
	d := &Device{info: driver.Info{ID: "iphone", Connection: "wifi"}, scr: screenInfo{Width: 1080, Height: 2340}}
	frame := image.NewRGBA(image.Rect(0, 0, 1088, 2352))
	opt := driver.ObserveOptions{OCR: true, Frame: func(context.Context) (image.Image, error) { return frame, nil }}
	if img := d.pictureNow(context.Background(), opt); img == nil || img.Bounds().Dx() != 1080 || img.Bounds().Dy() != 2340 {
		t.Fatalf("pictureNow = %v", img)
	}
}
