package ios

import (
	"context"
	"encoding/binary"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// Over Wi-Fi, OCR reads a frame of WDA's MJPEG server instead of the
// full-size screenshot; when no frame comes, the screenshot, and frames are
// not asked for again for frameRetry. Over a cable, always the screenshot.
func TestWDAOCRPicture(t *testing.T) {
	f := &fakeWDA{app: `{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}`}
	srv := httptest.NewServer(f)
	defer srv.Close()
	frame := jpegBytes(t, 10, 20, color.White)
	var frames, mute atomic.Int32
	mj := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		frames.Add(1)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=BoundaryString")
		if mute.Load() != 0 {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprintf(w, "--BoundaryString\r\nContent-type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
		w.Write(frame)
	}))
	defer mj.Close()
	defer func(w time.Duration) { frameWait = w }(frameWait)
	frameWait = 200 * time.Millisecond

	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL), width: 100, height: 200, scale: 1,
		info: driver.Info{Connection: "wifi"}}
	ctx := context.Background()
	shots := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := f.shots
		f.shots = 0
		return n
	}
	pic, from, err := d.ocrPicture(ctx)
	if err != nil || from != "mjpeg" || len(pic) == 0 || shots() != 0 || frames.Load() != 1 {
		t.Fatalf("wifi: from %q, %d bytes, %v; %d screenshots", from, len(pic), err, shots())
	}
	if _, err := d.Observe(ctx, driver.ObserveOptions{OCR: true}); err != nil || shots() != 0 {
		t.Fatalf("observe over wifi: %v; %d screenshots", err, shots())
	}

	// The MJPEG server sends nothing: the screenshot, then only it.
	mute.Store(1)
	frames.Store(0)
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || shots() != 1 || frames.Load() != 1 {
		t.Fatalf("no frame: from %q", from)
	}
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || frames.Load() != 1 {
		t.Fatalf("within frameRetry: from %q, %d frames asked", from, frames.Load())
	}
	mute.Store(0)
	d.framesAfter.Store(time.Now().Add(-time.Second).UnixNano())
	if _, from, _ := d.ocrPicture(ctx); from != "mjpeg" {
		t.Fatalf("after frameRetry: from %q", from)
	}

	// Over a cable, the screenshot.
	d.info.Connection = "usb"
	frames.Store(0)
	if _, from, _ := d.ocrPicture(ctx); from != "screenshot" || frames.Load() != 0 {
		t.Fatalf("usb: from %q, %d frames", from, frames.Load())
	}
}

// The field report of 2026-11-26 (an iPad on iPadOS 26 over Wi-Fi): a read
// of Settings, opened over a game that runs in landscape, gave Settings'
// elements with OCR lines of the game's notice, from a picture that still
// showed the game. OCR's lines are placed by their share of the picture,
// so a picture turned the other way than the tree is another screen than
// the tree's, and would put every line in the wrong place: the read leaves
// its text out.
func TestWDAOCRPictureTurnedAway(t *testing.T) {
	srv := httptest.NewServer(&fakeWDA{app: `{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}`})
	defer srv.Close()
	var turned atomic.Bool
	// The panel as it is, 8×12, tagged as turned for landscape: the game.
	game := withEXIF(t, jpegBytes(t, 8, 12, color.Black), 6, binary.BigEndian)
	settings := jpegBytes(t, 8, 12, color.White)
	mj := mjpegServer(t, func(time.Duration) []byte {
		if turned.Load() {
			return game
		}
		return settings
	})
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL), width: 100, height: 200, scale: 1,
		info: driver.Info{Connection: "wifi"},
		recognize: func([]byte) ([]ocrLine, error) {
			return []ocrLine{{Text: "Update notice", Confidence: 1, X: 0.1, Y: 0.5, W: 0.5, H: 0.05}}, nil
		}}
	read := func() *screen.Screen {
		t.Helper()
		s, err := d.Observe(context.Background(), driver.ObserveOptions{OCR: true})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	notice := func(s *screen.Screen) bool {
		for _, e := range s.Elements {
			if e.Source == screen.SourceOCR && e.Label == "Update notice" {
				return true
			}
		}
		return false
	}

	// A picture of the tree's screen: its text is read.
	if s := read(); !notice(s) {
		t.Fatalf("portrait picture, portrait tree: %s", s.Render())
	}
	// The landscape game's picture under Settings' portrait tree.
	turned.Store(true)
	if s := read(); notice(s) || s.App.ID != "com.apple.Preferences" || len(s.Elements) != 1 {
		t.Fatalf("landscape picture, portrait tree: %s", s.Render())
	}
}

// The field report of 2026-11-26 (an iPad on iPadOS 26 over Wi-Fi): after
// Home, the header said "app: DockFolderViewService · keyboard shown" over
// the home screen. WDA named a view service of the home screen's Dock as
// the app in front, and OCR's one-character lines on the home screen, such
// as a calendar widget's weekdays, pass for a keyboard's keys. The read
// names the home screen, whose tree says whether a keyboard shows.
func TestWDAHomeScreenViewService(t *testing.T) {
	var app, tree atomic.Value // activeAppInfo's answer ("" fails it), /source's tree
	f := &fakeWDA{png: jpegBytes(t, 10, 20, color.White)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/wda/activeAppInfo":
			a := app.Load().(string)
			if a == "" {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"value":{"error":"unknown error","message":"no app"}}`)
				return
			}
			fmt.Fprintf(w, `{"value":%s}`, a)
		case strings.HasSuffix(r.URL.Path, "/source"):
			fmt.Fprintf(w, `{"value":%s}`, tree.Load().(string))
		default:
			f.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	// A calendar widget's weekday row, low on the screen and across it.
	var week []ocrLine
	for i, day := range []string{"日", "一", "二", "三", "四", "五", "六"} {
		week = append(week, ocrLine{Text: day, Confidence: 1, X: 0.1 + 0.12*float64(i), Y: 0.7, W: 0.04, H: 0.02})
	}
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1,
		recognize: func([]byte) ([]ocrLine, error) { return week, nil }}
	defer d.trees.close()
	read := func(what string) *screen.Screen {
		t.Helper()
		s, err := d.Observe(context.Background(), driver.ObserveOptions{OCR: true})
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		return s
	}
	home := screen.App{Name: "Home Screen", ID: springBoard}
	root := func(label, children string) string {
		return `{"type":"Application","label":"` + label +
			`","rect":{"x":0,"y":0,"width":100,"height":200},"isVisible":"1","children":[` + children + `]}`
	}

	tree.Store(root("DockFolderViewService", ""))
	for _, c := range []struct{ what, app string }{
		{"named by WDA", `{"bundleId":"com.apple.DockFolderViewService","name":"","pid":90}`},
		{"named by its tree", ""}, // WDA did not answer
	} {
		app.Store(c.app)
		if s := read(c.what); s.App != home || s.Keyboard || len(s.Elements) != len(week) {
			t.Errorf("%s: %s", c.what, s.Render())
		}
	}
	// The home screen's keyboard, as Spotlight's search shows it, is in its
	// tree, and OCR's text of its keys is left out.
	app.Store(`{"bundleId":"com.apple.springboard","name":"","pid":60}`)
	tree.Store(root("", `{"type":"Keyboard","rect":{"x":0,"y":120,"width":100,"height":80},"isVisible":"1"}`))
	if s := read("Spotlight"); s.App != home || !s.Keyboard || len(s.Elements) != 0 {
		t.Fatalf("Spotlight: %s", s.Render())
	}
	// In an app, such lines are a keyboard's keys that its tree may lack.
	app.Store(`{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}`)
	tree.Store(root("Settings", ""))
	if s := read("Settings"); !s.Keyboard {
		t.Fatalf("Settings: %s", s.Render())
	}
}
