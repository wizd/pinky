package onphone

import (
	"context"
	"errors"
	"image"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// Input is refused while Pinky's app is in front, as an action that was
// not done.
func TestGateRefusesWhilePinkyIsInFront(t *testing.T) {
	g := newGate(policy.IPhoneApp)
	if err := g.check(); err != nil {
		t.Fatal(err)
	}
	g.setUI(true)
	err := g.check()
	if !errors.Is(err, ErrOwnUI) || !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("check = %v", err)
	}
	g.setUI(false)
	if err := g.check(); err != nil {
		t.Fatal(err)
	}
}

// A read learns the app the task works in, never Pinky's or the home
// screen; it names Pinky's app where the driver could not; and another app
// read well after Pinky's app said it came to the front means it left.
func TestGateLearnsFromReads(t *testing.T) {
	g := newGate("com.example.pinky")
	read := func(id string, start time.Time) screen.App {
		s := &screen.Screen{App: screen.App{ID: id}}
		g.saw(s, start)
		return s.App
	}
	read("com.apple.calculator", time.Now())
	read(springboard, time.Now())
	for _, self := range []string{"com.example.pinky", "com.example.pinky.tunnel", "cc.diygod.pinky.standalone",
		"pinky.abcde12345"} {
		if app := read(self, time.Now()); app.ID != policy.IPhoneApp {
			t.Errorf("%s read as %+v", self, app)
		}
	}
	read("com.example.pinkyfan", time.Now())
	read("", time.Now())
	if got := g.lastApp(); got != "com.example.pinkyfan" {
		t.Fatalf("last app = %q", got)
	}
	if !policy.IsPinkyScreen(screen.App{ID: policy.IPhoneApp}) {
		t.Fatal("the policy does not hide Pinky's app")
	}

	g.setUI(true)
	if app := read("", time.Now()); app.ID != policy.IPhoneApp {
		t.Fatalf("Pinky's screen unnamed: %+v", app)
	}
	// Started before Pinky's app came to the front: the app it found is
	// old news.
	read("com.apple.Preferences", time.Now().Add(-time.Second))
	if !g.uiActive() {
		t.Fatal("an old read cleared Pinky's app")
	}
	read("com.apple.Preferences", time.Now().Add(staleUI+time.Second))
	if g.uiActive() {
		t.Fatal("a later read of another app kept Pinky's app in front")
	}
	if got := g.lastApp(); got != "com.apple.Preferences" {
		t.Fatalf("last app = %q", got)
	}
}

func TestGateWaitUI(t *testing.T) {
	g := newGate(policy.IPhoneApp)
	g.setUI(true)
	if g.waitUI(context.Background(), false, 20*time.Millisecond) {
		t.Fatal("waitUI reported a change that did not happen")
	}
	time.AfterFunc(10*time.Millisecond, func() { g.setUI(false) })
	if !g.waitUI(context.Background(), false, time.Second) {
		t.Fatal("waitUI missed the change")
	}
}

// The phone's reads take no pictures, and screenshots are refused as
// unsupported, which the gateway reports to the agent as such.
func TestGuardTakesNoPictures(t *testing.T) {
	opt := treeOnly(driver.ObserveOptions{OCR: true, OCRAll: true,
		Frame:      func(context.Context) (image.Image, error) { return nil, nil },
		MayCapture: func(*screen.Screen) bool { return true }})
	if opt.OCR || opt.OCRAll || opt.Frame != nil {
		t.Fatalf("treeOnly kept OCR or the frame: OCR %v, OCRAll %v, Frame %v", opt.OCR, opt.OCRAll, opt.Frame != nil)
	}
	// nil would let the driver take its picture at once.
	if opt.MayCapture == nil || opt.MayCapture(&screen.Screen{App: screen.App{ID: "com.apple.calculator"}}) {
		t.Fatal("treeOnly allows a picture")
	}
	d := &Guard{g: newGate("cc.example.pinky")}
	if _, err := d.Screenshot(context.Background()); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("Screenshot: %v, want ErrUnsupported", err)
	}
	if _, err := d.ScreenshotPNG(context.Background()); !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("ScreenshotPNG: %v, want ErrUnsupported", err)
	}
	var fs driver.FrameShooter = d
	if img, ok := fs.ScreenshotFromFrame(context.Background(), nil); ok || img != nil {
		t.Fatal("ScreenshotFromFrame gave a picture")
	}
}
