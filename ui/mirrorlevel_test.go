package ui

import (
	"bytes"
	"image"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// lumaYUV is a w×h picture in NV12, in grays luma gives.
func lumaYUV(w, h int, luma func(x, y int) byte) *video.YUV {
	cw, ch := (w+1)/2, (h+1)/2
	p := &video.YUV{NV12: true, Width: w, Height: h, Strides: [3]int{w, 2 * cw, 0}, FullRange: true}
	p.Planes[0] = make([]byte, w*h)
	for y := range h {
		for x := range w {
			p.Planes[0][y*w+x] = luma(x, y)
		}
	}
	p.Planes[1] = bytes.Repeat([]byte{128}, 2*cw*ch)
	return p
}

// checker is the finest detail a picture holds: squares of a pixel, black
// and white.
func checker(x, y int) byte { return byte(255 * ((x + y) & 1)) }

// yuvMirror is stillMirror for a picture of video, which the mirror shows
// as it shows its stream's.
func yuvMirror(ta *testApp, id string, pic *video.YUV) *mirror {
	m := &mirror{a: ta.app, id: id, state: "playing", bitmap: showPlanes(nil, pic),
		frame: &video.Frame{YUV: pic, Width: pic.Width, Height: pic.Height}, halves: halvings{pic: pic, n: 1},
		picW: pic.Width, picH: pic.Height, used: ta.buildSeq, retry: time.Now().Add(time.Hour)}
	ta.mirrors[id] = m
	return m
}

// TestMirrorLevel: a surface halves the picture as often as leaves it no
// smaller than the pixels it shows on, counted in the screen's pixels, for
// the picture the right way up: not at all down to half its size, once
// from half, twice from a quarter.
func TestMirrorLevel(t *testing.T) {
	for _, tc := range []struct {
		name          string
		w, h, turn    int
		bw, bh, scale float32
		want          int
	}{
		{"window at its narrowest", 1206, 2622, 0, 184, 400, 1, 2}, // 6.6 times smaller
		{"window on Retina", 1206, 2622, 0, 184, 400, 2, 1},        // 3.3
		{"page", 1206, 2622, 0, 276, 600, 1, 2},                    // 4.4
		{"page on Retina", 1206, 2622, 0, 276, 600, 2, 1},          // 2.2
		{"large page on Retina", 1206, 2622, 0, 640, 1391, 2, 0},   // 0.94
		{"tile", 1206, 2622, 0, 158, 342, 1, 2},                    // 7.7
		{"tile of a tablet", 2560, 1600, 0, 158, 99, 1, 4},         // 16.2
		{"exactly half", 1000, 2000, 0, 500, 1000, 1, 1},
		{"a little larger than half", 1000, 2000, 0, 501, 1002, 1, 0},
		{"exactly a quarter", 1000, 2000, 0, 250, 500, 1, 2},
		{"a wide box: the height decides", 1000, 2000, 0, 900, 500, 1, 2},
		{"turned the right way up", 2622, 1206, 1, 184, 400, 1, 2},
		{"turned three times", 2622, 1206, 3, 184, 400, 1, 2},
		{"turned twice, the same shape", 1206, 2622, 2, 184, 400, 1, 2},
		{"sideways, not turned", 2622, 1206, 0, 184, 400, 1, 3}, // 14.3
		{"a tiny box", 1206, 2622, 0, 4, 8, 1, mirrorHalvings},
		{"no box yet", 1206, 2622, 0, 0, 0, 1, 0},
		{"no picture", 0, 0, 0, 184, 400, 1, 0},
		{"no scale", 1206, 2622, 0, 184, 400, 0, 0},
	} {
		if got := mirrorLevel(tc.w, tc.h, tc.turn, tc.bw, tc.bh, tc.scale); got != tc.want {
			t.Errorf("%s: %d×%d turned %d in %v×%v at %v× halves %d times, want %d", tc.name, tc.w, tc.h, tc.turn, tc.bw, tc.bh, tc.scale, got, tc.want)
		}
	}
}

// TestMirrorHalvesOnce: a mirror halves a picture once for each level a
// surface shows, each from the one above, and shares it between the
// surfaces that show it; it makes no level no surface shows, nothing again
// without a new picture, and halves a new picture into planes the level's
// bitmap does not show, which MyGo reads until the bitmap's next picture.
func TestMirrorHalvesOnce(t *testing.T) {
	pic := lumaYUV(1206, 2622, checker)
	m := &mirror{bitmap: showPlanes(nil, pic), frame: &video.Frame{YUV: pic, Width: 1206, Height: 2622}, halves: halvings{pic: pic, n: 1}}
	window := mg.Rect{W: 184, H: 400}
	bm, src := m.halves.pictureFor(m.bitmap, m.turn, window, 1)
	if w, h := bm.Size(); w != 302 || h != 656 || src != (mg.Rect{W: 301.5, H: 655.5}) {
		t.Fatalf("the window shows a bitmap of %d×%d, its part %v; want 302×656, 301.5×655.5", w, h, src)
	}
	if l := m.halves.levels; l[0].pic != 1 || l[1].pic != 1 || l[2].pic != 0 || l[0].bitmap != nil {
		t.Fatalf("pictures halved once, twice and three times: %d, %d, %d; the first shown: %v", l[0].pic, l[1].pic, l[2].pic, l[0].bitmap != nil)
	}
	if got := m.halves.levels[1].planes[m.halves.levels[1].cur].Planes[0][:4]; !bytes.Equal(got, []byte{128, 128, 128, 128}) {
		t.Errorf("the checkerboard halved twice is %v, want 128s", got)
	}
	// Marked, the planes tell whether they are made again.
	for k := range 2 {
		l := &m.halves.levels[k]
		l.planes[l.cur].Planes[0][0] = 7
	}
	if again, _ := m.halves.pictureFor(m.bitmap, m.turn, mg.Rect{W: 180, H: 391}, 1); again != bm {
		t.Error("two surfaces that show the picture halved twice show different bitmaps")
	}
	if once, _ := m.halves.pictureFor(m.bitmap, m.turn, window, 2); once == nil || once == bm || once == m.bitmap {
		t.Fatal("the window on Retina does not show the picture halved once")
	}
	if full, src := m.halves.pictureFor(m.bitmap, m.turn, mg.Rect{W: 603, H: 1311}, 2); full != m.bitmap || src != (mg.Rect{}) {
		t.Errorf("a box as large as the picture shows %p, part %v; want the picture's own bitmap %p", full, src, m.bitmap)
	}
	for k := range 2 {
		if l := &m.halves.levels[k]; l.planes[l.cur].Planes[0][0] != 7 {
			t.Errorf("the picture halved %d times was made again for the same picture", k+1)
		}
	}

	// A new picture: drawn small, it is halved into the planes the
	// bitmaps do not show, which keep the picture before.
	var kept [2][]byte
	for k := range 2 {
		l := &m.halves.levels[k]
		kept[k] = bytes.Clone(l.planes[l.shows].Planes[0])
	}
	next := lumaYUV(1206, 2622, func(x, y int) byte { return 200 })
	m.frame = &video.Frame{YUV: next, Width: 1206, Height: 2622}
	m.bitmap = showPlanes(m.bitmap, next)
	m.halves.next(next)
	m.halves.halved(2)
	for k := range 2 {
		if l := &m.halves.levels[k]; l.cur == l.shows || !bytes.Equal(l.planes[l.shows].Planes[0], kept[k]) {
			t.Errorf("the new picture halved %d times went into the planes its bitmap shows", k+1)
		}
	}
	if bm, _ := m.halves.pictureFor(m.bitmap, m.turn, window, 1); bm != m.halves.levels[1].bitmap || m.halves.levels[1].shown != 2 || m.halves.levels[1].shows != m.halves.levels[1].cur {
		t.Error("the window does not show the new picture halved twice")
	}
	if l := &m.halves.levels[0]; l.shown != 1 || !bytes.Equal(l.planes[l.shows].Planes[0], kept[0]) {
		t.Error("the bitmap of the picture halved once, which no surface drew since, does not keep the picture before")
	}
	if got := m.halves.levels[1].planes[m.halves.levels[1].cur].Planes[0][:2]; !bytes.Equal(got, []byte{200, 200}) {
		t.Errorf("the new picture halved twice is %v, want 200s", got)
	}

	// Pictures of another size, as when the phone turns: the planes keep
	// up, the size each bitmap shows with them.
	turned := lumaYUV(2622, 1206, checker)
	m.frame, m.turn = &video.Frame{YUV: turned, Width: 2622, Height: 1206, Turn: 1}, 1
	m.bitmap = showPlanes(m.bitmap, turned)
	m.halves.next(turned)
	if bm, src := m.halves.pictureFor(m.bitmap, m.turn, window, 1); src != (mg.Rect{W: 655.5, H: 301.5}) {
		w, h := bm.Size()
		t.Errorf("the turned picture shows as a bitmap of %d×%d, its part %v; want 656×302, 655.5×301.5", w, h, src)
	}
}

// TestMirrorSmallShowsAverage: a picture of the finest detail, squares of a
// pixel of black and white, shows as an even gray in the phone's own window
// at its narrowest, over six times smaller than the picture, and on its
// page on a Retina screen, over twice smaller: each surface shows the
// picture halved as often as its pixels need (#233). One sample of the
// picture a pixel showed bands of black, white and gray.
func TestMirrorSmallShowsAverage(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	ta.sync()
	if err := ta.api.StopAll(); err != nil { // no step outlined on the mirror
		t.Fatal(err)
	}
	ta.waitFor(t, "the task to stop", func() bool { return !ta.anyRunning() })
	pic := lumaYUV(1206, 2622, checker)
	m := yuvMirror(ta, "pixel-9", pic)
	ta.openPhoneWindow("pixel-9")
	wt := windowTester(ta, "pixel-9", winMinW, 460)
	tt := mg.NewTester(ta.view, 1280, 820)
	tt.SetScale(2)
	wt.Frame()
	tt.Frame()
	if pw := ta.phoneWins["pixel-9"]; ta.mirrors["pixel-9"] != m || pw == nil || pw.mirror != m {
		t.Fatalf("the page and the window do not show the test's mirror: page's %p, window %+v, test's %p", ta.mirrors["pixel-9"], pw, m)
	}
	for _, s := range []struct {
		name  string
		tt    *mg.Tester
		scale float32
		level int
	}{
		{"the window", wt, 1, 2},
		{"the page", tt, 2, 1},
	} {
		scr, ok := s.tt.Find(locales.MirrorScreenLabel("Pixel 9"))
		if !ok {
			t.Fatalf("%s: no screen: %q", s.name, s.tt.Texts())
		}
		// Not a whole number of times smaller, where the samples would all
		// fall between four pixels of the picture and show gray anyway.
		if k := mirrorLevel(pic.Width, pic.Height, 0, scr.W, scr.H, s.scale); k != s.level {
			t.Fatalf("%s: a screen of %v at %v× halves the picture %d times, want %d", s.name, scr, s.scale, k, s.level)
		}
		lo, hi := grayRange(s.tt.Image(), scr, s.scale)
		if lo < 124 || hi > 132 {
			t.Errorf("%s: the checkerboard shows from %d to %d, want an even gray of 128", s.name, lo, hi)
		}
	}
	for k, want := range []uint64{1, 1, 0} {
		if l := m.halves.levels[k]; l.pic != want || l.shown != want {
			t.Errorf("the picture halved %d times: made for picture %d, shown for %d; want %d", k+1, l.pic, l.shown, want)
		}
	}
}

// grayRange returns the darkest and the lightest of the red, green and
// blue of img, a capture at scale pixels a DIP, in the middle of r, away
// from its rounded corners.
func grayRange(img *image.RGBA, r mg.Rect, scale float32) (lo, hi int) {
	lo, hi = 255, 0
	x0, x1 := int((r.X+0.15*r.W)*scale), int((r.X+0.85*r.W)*scale)
	y0, y1 := int((r.Y+0.15*r.H)*scale), int((r.Y+0.85*r.H)*scale)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			for _, v := range img.Pix[img.PixOffset(x, y):][:3] {
				lo, hi = min(lo, int(v)), max(hi, int(v))
			}
		}
	}
	return lo, hi
}
