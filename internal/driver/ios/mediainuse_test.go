package ios

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
	goios "github.com/danielpaulus/go-ios/ios"
)

// refusedStart is the phone's answer to a stream start while an app uses
// the camera or microphone, as a new iPhone 15 Plus on iOS 27 gave it.
func refusedStart() error {
	ce := &CoreDeviceError{Code: errMediaInUse, Domain: "com.apple.dt.CoreDeviceError", Desc: "The device microphone or camera is currently in use."}
	return fmt.Errorf("start display stream (display 1): an app is using the camera or microphone, and iOS does not allow mirroring then: %w", ce)
}

// fakeSession is a CoreDevice session that counts what the provider does
// with it.
type fakeSession struct {
	mu       sync.Mutex
	fail     error // prepare's error
	prepared int
	ran      bool
	closed   bool
}

// openedDevice is the device a fakeSession opens as.
type openedDevice struct{ driver.Device }

func (f *fakeSession) prepare(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	return f.fail
}

func (f *fakeSession) opened() driver.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = true
	return openedDevice{}
}

func (f *fakeSession) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// sessionProvider is a provider with one iPhone on iOS 27 on a cable,
// whose CoreDevice session is s.
func sessionProvider(t *testing.T, s *fakeSession) *Provider {
	p := NewProvider()
	p.listFn = func() (goios.DeviceList, error) {
		return goios.DeviceList{DeviceList: []goios.DeviceEntry{entry("phone", 1, "USB")}}, nil
	}
	p.inspectFn = func(_ context.Context, e goios.DeviceEntry) driver.Info {
		return driver.Info{ID: e.Properties.SerialNumber, Platform: driver.IOS, State: driver.StateOnline, Backend: backendCoreDevice, OSVersion: "27.2"}
	}
	p.dialFn = func(context.Context, driver.Info) (coreSession, error) { return s, nil }
	t.Cleanup(func() { p.Close() })
	return p
}

func listOne(t *testing.T, p *Provider) driver.Info {
	t.Helper()
	got, err := p.List(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("List = %+v, %v; want one phone", got, err)
	}
	return got[0]
}

// TestOpenCoreDeviceStartsNoStream: opening a phone prepares its session
// and starts nothing more; the phone stays online.
func TestOpenCoreDeviceStartsNoStream(t *testing.T) {
	f := &fakeSession{}
	p := sessionProvider(t, f)
	listOne(t, p)
	dev, err := p.Open(context.Background(), "phone")
	if err != nil || dev == nil {
		t.Fatalf("Open = %v, %v", dev, err)
	}
	if f.prepared != 1 || !f.ran || f.closed {
		t.Fatalf("prepared %d, ran %v, closed %v; want one prepare and the session running", f.prepared, f.ran, f.closed)
	}
	if got := listOne(t, p); got.State != driver.StateOnline {
		t.Fatalf("after the open: %+v", got)
	}

	// A session that cannot read the displays closes, and Open fails.
	g := &fakeSession{fail: errors.New("display info: no answer")}
	p = sessionProvider(t, g)
	listOne(t, p)
	if _, err := p.Open(context.Background(), "phone"); err == nil || !g.closed || g.ran {
		t.Fatalf("Open = %v, closed %v, ran %v; want the error and the session closed", err, g.closed, g.ran)
	}
}

// TestNoStreamUntilNeeded: an open device starts the display stream only
// for a view or input. One refused because an app uses the camera or
// microphone fails that view or input with the media_in_use step, and
// nothing starts the stream again on its own: the next view or input
// does, no sooner than restartBackoff later.
func TestNoStreamUntilNeeded(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	d := &Device{l: &link{}, done: make(chan struct{})}
	d.startFn = func(context.Context, *link, int) (*media, error) {
		mu.Lock()
		defer mu.Unlock()
		starts++
		return nil, refusedStart()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return starts
	}
	if d.Capturing() {
		t.Fatal("capturing before anything needed the stream")
	}
	stopped := d.stopIfIdle(time.Now().Add(2 * streamIdle))
	if stopped || count() != 0 {
		t.Fatalf("the idle check stopped %v and started %d streams; want neither", stopped, count())
	}

	_, err := d.Stream(context.Background(), driver.StreamOptions{})
	var se *driver.SetupError
	if !errors.As(err, &se) || se.Setup != driver.SetupMediaInUse || se.Detail != mediaInUseDetail || !mediaInUse(err) {
		t.Fatalf("Stream = %v; want the media_in_use step", err)
	}
	if count() != 1 {
		t.Fatalf("%d starts for one view; want 1", count())
	}
	d.mu.Lock()
	watchers, nv := d.watchers, d.noVideo
	d.mu.Unlock()
	if watchers != 0 || nv != nil {
		t.Fatalf("watchers %d, noVideo %v; want no view counted and no missing picture", watchers, nv)
	}

	// Input right after says the same without asking the phone again.
	err = d.Tap(context.Background(), screen.Point{X: 10, Y: 10}, driver.TapOptions{})
	if !errors.As(err, &se) || se.Setup != driver.SetupMediaInUse {
		t.Fatalf("Tap = %v; want the media_in_use step", err)
	}
	if count() != 1 {
		t.Fatalf("%d starts within restartBackoff; want 1", count())
	}

	// After the backoff, the next view asks again.
	d.restartMu.Lock()
	d.restartFailedAt = d.restartFailedAt.Add(-restartBackoff)
	d.restartMu.Unlock()
	if _, err := d.Stream(context.Background(), driver.StreamOptions{}); !mediaInUse(err) || count() != 2 {
		t.Fatalf("Stream after the backoff = %v with %d starts; want a second refused start", err, count())
	}
}

func TestMediaInUseStep(t *testing.T) {
	if err := mediaInUseStep(nil); err != nil {
		t.Fatalf("mediaInUseStep(nil) = %v", err)
	}
	other := errors.New("tunnel: the iPhone does not answer")
	if err := mediaInUseStep(other); err != other {
		t.Fatalf("mediaInUseStep(other) = %v; want it unchanged", err)
	}
	err := mediaInUseStep(refusedStart())
	var se *driver.SetupError
	if !errors.As(err, &se) || se.Setup != driver.SetupMediaInUse || !mediaInUse(err) {
		t.Fatalf("mediaInUseStep(refused) = %v", err)
	}
	if again := mediaInUseStep(fmt.Errorf("video stream: %w", err)); !errors.As(again, &se) || errors.Unwrap(se) == nil {
		t.Fatalf("wrapped twice: %v", again)
	}
}

// TestMediaInUseIsNoMissingPicture: a phone that refused one display and
// then the stream because an app holds the camera or microphone does not
// fail its checkup for sending no picture.
func TestMediaInUseIsNoMissingPicture(t *testing.T) {
	p := NewProvider()
	refused := errors.New("display 1 refused")
	nv := (*noVideoError)(nil).with(noVideoTry{Display: 1, Err: refused}, refused, nil)
	err := nv.add(refusedStart(), 2, nil)
	if !mediaInUse(err) {
		t.Fatalf("mediaInUse(%v) = false", err)
	}
	p.noteOpen("phone", err)
	if got := p.OpenChecks("phone"); len(got) != 0 {
		t.Fatalf("OpenChecks while in use = %+v; want none", got)
	}
	other := (*noVideoError)(nil).with(noVideoTry{Display: 1, Err: refused}, refused, nil)
	p.noteOpen("phone", other)
	if got := p.OpenChecks("phone"); len(got) != 1 {
		t.Fatalf("OpenChecks with no picture = %+v; want the screen item", got)
	}

	// Nor does the device's own checkup, after a view the phone refused.
	d := &Device{l: &link{}, done: make(chan struct{})}
	d.startFn = func(context.Context, *link, int) (*media, error) { return nil, err }
	if _, _, e := d.live(context.Background()); !mediaInUse(e) {
		t.Fatalf("live = %v", e)
	}
	d.mu.Lock()
	got := d.noVideo
	d.mu.Unlock()
	if got != nil {
		t.Fatalf("noVideo = %v after a refused start; want none", got)
	}
}
