package android

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/ocr"
	"github.com/DIYgod/pinky/internal/screen"
)

// restlessPhone answers the driver's shell commands for a phone whose
// screen may never stop changing, as a rewards page with live counters
// (GitHub issue #174): uiautomator then prints its idle error and no tree.
type restlessPhone struct {
	mu      sync.Mutex
	still   bool   // the screen stops changing: uiautomator dumps
	focus   string // the focused activity, "package/.Activity"
	dumps   int    // uiautomator runs
	fileDmp bool   // the phone cannot dump to /dev/tty
}

func (p *restlessPhone) set(fn func(p *restlessPhone)) {
	p.mu.Lock()
	fn(p)
	p.mu.Unlock()
}

func (p *restlessPhone) dumpCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dumps
}

func (p *restlessPhone) run(_, svc string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	idle := "ERROR: could not get idle state.\n"
	switch {
	case svc == "exec:uiautomator dump /dev/tty":
		p.dumps++
		switch {
		case p.fileDmp:
			return "UI hierchary dumped to: /dev/tty\n", true // and nothing came
		case p.still:
			return fixtureText.Replace(settingsFixture), true
		}
		return idle, true
	case strings.HasPrefix(svc, "exec:uiautomator dump /data/local/tmp/"):
		p.dumps++
		if p.still {
			return fixtureText.Replace(settingsFixture), true
		}
		return idle, true // what grep let through; there is no file to cat
	case svc == "shell:wm size":
		return "Physical size: 1080x2340\n", true
	case strings.HasPrefix(svc, "shell:dumpsys window displays | grep -E ' cur="):
		return "    cur=1080x2340 app=1080x2214 rng=1080x1017-2214x2151\n", true
	case svc == "shell:"+restlessCmd:
		return "showing=false\n--ime--\n  isVisible=false\n--focus--\n  mFocusedApp=ActivityRecord{5d2a1c9 u0 " + p.focus + " t87}\n", true
	case strings.HasPrefix(svc, "shell:"+keyguardCmd):
		return "showing=false\n--ime--\n", true
	case strings.HasPrefix(svc, "exec:screencap -p"):
		return string(grayPNG(1080, 2340)), true
	}
	return "", false
}

func grayPNG(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// restlessDevice opens a device on a fake adb server that plays p. No agent
// works on it, so the companion stays off unless the test says otherwise.
func restlessDevice(t *testing.T, p *restlessPhone) *Device {
	t.Helper()
	f := startFakeADB(t)
	f.set(func(f *fakeADB) {
		f.props = map[string]string{"phone": pixelProps}
		f.run = p.run
	})
	cfg := Config{Client: &adb.Client{Addr: f.ln.Addr().String()}, Logf: t.Logf}
	cfg.defaults()
	d := newDevice(cfg, driver.Info{ID: "phone", Connection: "usb"}, deviceProps{})
	d.listApps = func(context.Context) ([]scrcpy.App, error) {
		return []scrcpy.App{{Package: "com.example.rewards", Name: "Rewards"}, {Package: "com.android.settings", Name: "Settings"}}, nil
	}
	d.comp.gated.Store(true)
	t.Cleanup(d.closeCompanion)
	return d
}

// TestObserveRestless: a screen that never stops changing reads without its
// tree, saying so, with what the window manager knows; reads of the same
// window then skip uiautomator's 10 s wait, until another window is in
// front or the time is up; once the screen holds still, the tree is back.
func TestObserveRestless(t *testing.T) {
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	p := &restlessPhone{focus: "com.example.rewards/.TaskActivity"}
	d := restlessDevice(t, p)
	ctx := context.Background()

	s, err := d.Observe(ctx, driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatalf("observe on a screen that never stops changing: %v", err)
	}
	if !s.Unsettled || s.App != (screen.App{ID: "com.example.rewards", Name: "Rewards"}) || s.Locked || s.Keyboard {
		t.Fatalf("screen = %+v", s)
	}
	if s.Width != 1080 || s.Height != 2340 || s.Orientation != "portrait" {
		t.Errorf("size = %dx%d %s", s.Width, s.Height, s.Orientation)
	}
	if !strings.Contains(s.Header(), "still changing") {
		t.Errorf("header %q does not say the screen kept changing", s.Header())
	}
	// No recognizer on this computer: the whole screen is marked as text
	// the read misses.
	if len(s.Elements) != 1 || !s.Elements[0].Has(stateNoOCR) || s.Elements[0].Bounds != (screen.Rect{W: 1080, H: 2340}) {
		t.Errorf("elements = %+v", s.Elements)
	}
	if n := p.dumpCount(); n != 1 {
		t.Fatalf("uiautomator ran %d times, want 1", n)
	}

	// The same window: no second 10 s wait.
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil || !s.Unsettled {
		t.Fatalf("second read = %+v, %v", s, err)
	}
	if n := p.dumpCount(); n != 1 {
		t.Errorf("uiautomator ran again on the same restless window (%d runs)", n)
	}

	// Another window came to the front, and holds still: its tree is read.
	p.set(func(p *restlessPhone) { p.focus, p.still = "com.android.settings/.Settings", true })
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil || s.Unsettled || len(s.Elements) == 0 {
		t.Fatalf("read of a still window = %+v, %v", s, err)
	}
	if n := p.dumpCount(); n != 2 {
		t.Errorf("uiautomator runs = %d, want 2", n)
	}
	if _, ok := d.stillRestless(); ok {
		t.Error("a read with a tree left the restless window noted")
	}

	// The time is up: uiautomator is tried again on the same window.
	defer func(v time.Duration) { restlessSkip = v }(restlessSkip)
	restlessSkip = 50 * time.Millisecond
	p.set(func(p *restlessPhone) { p.focus, p.still = "com.example.rewards/.TaskActivity", false })
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil || !s.Unsettled {
		t.Fatalf("read = %+v, %v", s, err)
	}
	time.Sleep(60 * time.Millisecond)
	p.set(func(p *restlessPhone) { p.still = true })
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil || s.Unsettled {
		t.Fatalf("read after the screen stopped = %+v, %v", s, err)
	}
	if n := p.dumpCount(); n != 4 {
		t.Errorf("uiautomator runs = %d, want 4", n)
	}
}

// TestObserveRestlessOCR: with a recognizer, the read of a screen that never
// stops changing has the text OCR reads from a screenshot, all of it.
func TestObserveRestlessOCR(t *testing.T) {
	w, h := 1080, 2340
	at := func(s string, x, y int) ocr.Line {
		return ocr.Line{Text: s, Confidence: 0.9, X: float64(x-50) / float64(w), Y: float64(y-20) / float64(h), W: 100.0 / float64(w), H: 40.0 / float64(h)}
	}
	st := &ocrStub{lines: []ocr.Line{at("Daily tasks", 300, 400), at("Claim 50 coins", 540, 1200)}}
	stubOCR(t, st)
	p := &restlessPhone{focus: "com.example.rewards/.TaskActivity"}
	d := restlessDevice(t, p)
	s, stats, err := d.ObserveWithText(context.Background(), driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Unsettled || stats.From != "screenshot" || stats.Added != 2 {
		t.Fatalf("screen %+v, stats %+v", s, stats)
	}
	got := ocrLabels(s)
	if len(got) != 2 || got[0] != "Daily tasks" || got[1] != "Claim 50 coins" {
		t.Errorf("OCR labels = %q", got)
	}
}

// TestObserveRestlessDumpFile: a phone that dumps to a file reports the idle
// error too, which the file dump used to drop.
func TestObserveRestlessDumpFile(t *testing.T) {
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	p := &restlessPhone{focus: "com.example.rewards/.TaskActivity", fileDmp: true}
	d := restlessDevice(t, p)
	s, err := d.Observe(context.Background(), driver.ObserveOptions{})
	if err != nil || !s.Unsettled {
		t.Fatalf("read = %+v, %v", s, err)
	}
	if !d.dumpFile {
		t.Error("the driver did not move to dumping to a file")
	}
}

// TestObserveRestlessCompanion: when uiautomator finds the screen never
// still, a companion that failed a moment ago is tried again at once: it
// reads the tree without waiting for the screen to stop changing.
func TestObserveRestlessCompanion(t *testing.T) {
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	qp := newQuietPhone(t)
	t.Setenv("PINKY_COMPANION_APK", "/nonexistent/pinky-android.apk")
	p := &restlessPhone{focus: "com.example.rewards/.TaskActivity"}
	d := restlessDevice(t, p)
	d.compRun = qp
	d.AgentWorking(true)
	ctx := context.Background()

	// The companion's first read fails: it is set aside for minutes.
	var fail atomic.Bool
	fail.Store(true)
	qp.mu.Lock()
	qp.reply = func(req map[string]any) (any, *companion.Error) {
		if req["cmd"] == "tree" && fail.Swap(false) {
			return nil, &companion.Error{Code: "internal", Message: "test failure"}
		}
		return nil, nil
	}
	qp.mu.Unlock()
	waitFor(t, "the companion's warm-up", func() bool { return d.companionInUse() })
	s, err := d.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if s.Unsettled {
		t.Fatalf("read without a tree although the companion could read it: %+v", s)
	}
	if n := p.dumpCount(); n != 1 {
		t.Errorf("uiautomator runs = %d, want 1", n)
	}
	if !d.companionInUse() {
		t.Error("the companion is not on after the restless read")
	}
}
