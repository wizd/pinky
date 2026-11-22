package gateway

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// restlessDev is a demo phone showing a page that never stops changing
// (GitHub issue #174): its reads come without a tree (Unsettled), or fail
// as uiautomator's did before the Android driver read such screens.
type restlessDev struct {
	*demo.Device
	mu        sync.Mutex
	unsettled bool  // reads come back Unsettled, without elements
	fail      error // reads fail with this
	clear     bool  // the next key ends the failures: it left the page
	keys      []driver.Key
}

func (d *restlessDev) set(fn func(d *restlessDev)) {
	d.mu.Lock()
	fn(d)
	d.mu.Unlock()
}

func (d *restlessDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	unsettled, fail := d.unsettled, d.fail
	d.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !unsettled {
		return s, err
	}
	cp := *s
	cp.Elements, cp.Unsettled = nil, true
	return &cp, nil
}

func (d *restlessDev) Key(ctx context.Context, k driver.Key) error {
	d.mu.Lock()
	d.keys = append(d.keys, k)
	if d.clear {
		d.fail, d.clear = nil, false
	}
	d.mu.Unlock()
	return d.Device.Key(ctx, k)
}

func (d *restlessDev) pressed() []driver.Key {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]driver.Key(nil), d.keys...)
}

// A read of a screen that kept changing says so, and keys work on it.
func TestUnsettledScreen(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &restlessDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 200 * time.Millisecond })
	s, _ := connect(t, g, "Collect the daily coins")
	dev.set(func(d *restlessDev) { d.unsettled = true })

	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "kept changing") || !strings.Contains(res.Message, "phone_key") {
		t.Errorf("observe message = %q", res.Message)
	}
	if res.Screen == nil || !res.Screen.Unsettled || !strings.Contains(res.Screen.Header(), "still changing") {
		t.Errorf("observe screen = %+v", res.Screen)
	}

	for _, k := range []string{"back", "recents", "home"} {
		start := time.Now()
		res = s.Act(ctx, Call{}, Action{Tool: "key", Key: k})
		mustStatus(t, res, StatusSettled)
		if !strings.Contains(res.Message, "kept changing") {
			t.Errorf("%s: message = %q", k, res.Message)
		}
		// No wait for a tree that will not come (awaitTree).
		if took := time.Since(start); took > treeWait {
			t.Errorf("%s took %v", k, took)
		}
	}
	if got := dev.pressed(); len(got) != 3 {
		t.Errorf("keys pressed = %v", got)
	}

	// Once the page holds still, reads have their tree and say nothing of it.
	dev.set(func(d *restlessDev) { d.unsettled = false })
	res = s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "kept changing") || res.Screen.Unsettled {
		t.Errorf("settled observe: %q, %+v", res.Message, res.Screen)
	}
}

// A screenshot of a screen that kept changing comes with the read that
// says so (the reporter's second try in GitHub issue #174).
func TestUnsettledScreenshot(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &restlessDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 200 * time.Millisecond })
	s, _ := connect(t, g, "Collect the daily coins")
	base.Show("calc")
	dev.set(func(d *restlessDev) { d.unsettled = true })

	res := s.Observe(ctx, ObserveRequest{Screenshot: true})
	mustStatus(t, res, StatusSettled)
	if res.Screenshot == nil {
		t.Fatalf("no screenshot: %q", res.Message)
	}
	if b := res.Screenshot.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Errorf("screenshot bounds = %v", b)
	}
	if !strings.Contains(res.Message, "kept changing") || strings.Contains(res.Message, "No screenshot") || strings.Contains(res.Message, "screenshot failed") {
		t.Errorf("message = %q", res.Message)
	}
	if res.Screen == nil || !res.Screen.Unsettled {
		t.Errorf("screen = %+v", res.Screen)
	}
}

// Home, the way out of a screen Pinky cannot read, is pressed although the
// read before it failed; other actions still report the failed read and do
// nothing.
func TestHomeWhenTheReadFails(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &restlessDev{Device: base}
	g := newDevGateway(t, base, dev, func(o *Options) { o.SettleMax = 200 * time.Millisecond })
	s, _ := connect(t, g, "Collect the daily coins")
	base.Show("calc")
	readErr := errors.New("android: uiautomator dump: the screen never stopped changing (could not get idle state)")
	dev.set(func(d *restlessDev) { d.fail = readErr })

	res := s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"})
	if !res.IsError() || !strings.Contains(res.Message, "never stopped changing") {
		t.Fatalf("back with a failing read: %+v", res)
	}
	if got := dev.pressed(); len(got) != 0 {
		t.Fatalf("back was pressed without a read: %v", got)
	}

	// The read after Home works again: the launcher is still.
	dev.set(func(d *restlessDev) { d.clear = true })
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	mustStatus(t, res, StatusSettled)
	if got := dev.pressed(); len(got) != 1 || got[0] != driver.KeyHome {
		t.Fatalf("keys pressed = %v", got)
	}
	if res.Screen == nil || res.Screen.App.Name != "Pixel Launcher" {
		t.Errorf("after home: %+v", res.Screen)
	}

	// The read after Home fails too: the key went out, and the result says
	// the screen could not be read.
	base.Show("calc")
	dev.set(func(d *restlessDev) { d.fail = readErr })
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	if res.Status != StatusUncertain || !strings.Contains(res.Message, "could not be read afterwards") {
		t.Errorf("home with no read at all: %+v", res)
	}
	if got := dev.pressed(); len(got) != 2 {
		t.Errorf("keys pressed = %v", got)
	}

	// Gone is gone: no key without the phone.
	dev.set(func(d *restlessDev) { d.fail = driver.ErrOffline })
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "home"})
	if !res.IsError() {
		t.Errorf("home on an offline phone: %+v", res)
	}
	if got := dev.pressed(); len(got) != 2 {
		t.Errorf("keys pressed on an offline phone = %v", got)
	}
}
