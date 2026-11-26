package ios

import (
	"context"
	"encoding/binary"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// mjpegServer streams frames the way WebDriverAgent's MJPEG server does,
// one every 30 ms from the moment a client connects: pick returns the
// frame to send that long after the connection opened, or nil to send
// none.
func mjpegServer(t *testing.T, pick func(since time.Duration) []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		opened := time.Now()
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=BoundaryString")
		w.(http.Flusher).Flush()
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		for {
			if b := pick(time.Since(opened)); b != nil {
				fmt.Fprintf(w, "--BoundaryString\r\nContent-type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(b))
				w.Write(b)
				w.(http.Flusher).Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A gap in the frames is no stillness: WDA's MJPEG server sends frames of
// a still screen too, and leaves them out only when it cannot take a
// picture, as while an app turns the screen. The same frame on both sides
// of a gap starts the quiet stretch again, once a few frames set the
// stream's pace.
func TestWDASettleAfterGap(t *testing.T) {
	srv := httptest.NewServer(&fakeWDA{}) // no GET /rotation
	defer srv.Close()
	still := jpegBytes(t, 8, 12, color.White)
	const gapEnds = 800 * time.Millisecond
	mj := mjpegServer(t, func(since time.Duration) []byte {
		if since > 150*time.Millisecond && since < gapEnds {
			return nil
		}
		return still
	})
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL)}
	start := time.Now()
	ok, err := d.WaitSettled(context.Background(), 3*time.Second)
	if took := time.Since(start); err != nil || !ok || took < gapEnds+settleQuiet {
		t.Fatalf("settled %v (%v) after %v, want true %v after the gap", ok, err, took, settleQuiet)
	}
}

// Over Wi-Fi the frames may come only 3 or 4 a second (WDA's frames are
// large, the tunnel slow): a wait of settleQuiet between them is then the
// stream's pace, no gap, and a still screen settles as it did before gaps
// counted.
func TestWDASettleSlowStream(t *testing.T) {
	srv := httptest.NewServer(&fakeWDA{}) // no GET /rotation
	defer srv.Close()
	still := jpegBytes(t, 8, 12, color.White)
	var sent atomic.Int64
	sent.Store(-1)
	mj := mjpegServer(t, func(since time.Duration) []byte {
		n := int64(since / (300 * time.Millisecond)) // a frame every 300 ms
		if sent.Swap(n) == n {
			return nil
		}
		return still
	})
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL)}
	start := time.Now()
	ok, err := d.WaitSettled(context.Background(), 3*time.Second)
	if took := time.Since(start); err != nil || !ok || took >= 1500*time.Millisecond {
		t.Fatalf("still frames 300 ms apart: settled %v (%v) after %v, want true within 1.5 s", ok, err, took)
	}
}

// GET /rotation's z is -1 when WDA does not know which way the app in
// front is turned. That says nothing, rather than upright: still frames
// turned for landscape count.
func TestWDASettleUnknownRotation(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotation" {
			fmt.Fprint(w, `{"value":{"x":-1,"y":-1,"z":-1}}`)
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	landscape := withEXIF(t, jpegBytes(t, 8, 12, color.White), 6, binary.BigEndian)
	mj := mjpegServer(t, func(time.Duration) []byte { return landscape })
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL)}
	start := time.Now()
	ok, err := d.WaitSettled(context.Background(), 2*time.Second)
	if took := time.Since(start); err != nil || !ok || took >= time.Second {
		t.Fatalf("landscape frames, rotation unknown: settled %v (%v) after %v, want true once still", ok, err, took)
	}
}

// Settings opened over a game that runs in landscape, on an iPad standing
// upright (field report, iPadOS 26, 2026-11-26): for a while the frames
// still show the game, still and turned its way, while GET /rotation
// already says Settings' portrait. The screen counts as settled once the
// frames are Settings' own and still; when frames and app agree, as soon
// as the frames are still.
func TestWDASettleWaitsForTheTurn(t *testing.T) {
	var z atomic.Int32 // GET /rotation's answer
	f := &fakeWDA{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotation" {
			fmt.Fprintf(w, `{"value":{"x":0,"y":0,"z":%d}}`, z.Load())
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	// The panel as it is, 8×12, tagged as turned for landscape: the game;
	// tagged up: Settings.
	game := withEXIF(t, jpegBytes(t, 8, 12, color.Black), 6, binary.BigEndian)
	settings := withEXIF(t, jpegBytes(t, 8, 12, color.White), 1, binary.BigEndian)
	const turnsAt = 900 * time.Millisecond
	mj := mjpegServer(t, func(since time.Duration) []byte {
		if since < turnsAt {
			return game
		}
		return settings
	})
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL)}
	ctx := context.Background()

	start := time.Now()
	ok, err := d.WaitSettled(ctx, 3*time.Second)
	if took := time.Since(start); err != nil || !ok || took < turnsAt+settleQuiet {
		t.Fatalf("settled %v (%v) after %v, want true once Settings' frames were still for %v", ok, err, took, settleQuiet)
	}
	// A limit that ends before the frames turn: not settled.
	if ok, err := d.WaitSettled(ctx, 700*time.Millisecond); err != nil || ok {
		t.Fatalf("frames of the game under Settings: settled %v (%v), want false at the limit", ok, err)
	}
	// The game in front, in landscape: its still frames are the screen.
	z.Store(90)
	start = time.Now()
	ok, err = d.WaitSettled(ctx, 3*time.Second)
	if took := time.Since(start); err != nil || !ok || took >= turnsAt {
		t.Fatalf("landscape app, landscape frames: settled %v (%v) after %v", ok, err, took)
	}
}

// A frame without an EXIF orientation tag does not say which way it is
// turned: the panel as it is and an upright picture look the same. Such
// frames count once still, under an app in landscape too, as before
// frameFits: a WebDriverAgent that sent the panel untagged would otherwise
// leave every settle in a landscape app waiting for the limit. A frame
// tagged up under an app in landscape is still another app's picture.
func TestWDASettleUntaggedFrames(t *testing.T) {
	f := &fakeWDA{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rotation" {
			fmt.Fprint(w, `{"value":{"x":0,"y":0,"z":90}}`) // landscape
			return
		}
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	panel := jpegBytes(t, 8, 12, color.White) // the panel as it is, 8×12
	var frame atomic.Value
	frame.Store(panel)
	mj := mjpegServer(t, func(time.Duration) []byte { return frame.Load().([]byte) })
	d := &WDADevice{c: newWDAURL(srv.URL, mj.URL)}
	ctx := context.Background()

	start := time.Now()
	ok, err := d.WaitSettled(ctx, 2*time.Second)
	if took := time.Since(start); err != nil || !ok || took >= time.Second {
		t.Fatalf("untagged frames under a landscape app: settled %v (%v) after %v, want true once still", ok, err, took)
	}
	frame.Store(withEXIF(t, panel, 1, binary.BigEndian))
	if ok, err := d.WaitSettled(ctx, 700*time.Millisecond); err != nil || ok {
		t.Fatalf("frames tagged up under a landscape app: settled %v (%v), want false at the limit", ok, err)
	}
}
