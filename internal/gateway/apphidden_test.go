package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// hiddenDev is a demo phone whose foreground app the phone keeps from
// accessibility, as ColorOS 17 does with WeChat (GitHub issue #191): the
// driver names the app from the window manager and reads its text by OCR
// alone (screen.Screen.AppHidden).
type hiddenDev struct {
	*demo.Device
	mu     sync.Mutex
	hidden bool
}

func (d *hiddenDev) set(hidden bool) {
	d.mu.Lock()
	d.hidden = hidden
	d.mu.Unlock()
}

func (d *hiddenDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	hidden := d.hidden
	d.mu.Unlock()
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !hidden {
		return s, err
	}
	cp := *s
	cp.Elements, cp.AppHidden = nil, true
	// Lines OCR read from a picture, more of them than the tree has: a
	// page whose tree would be loading, were the app not hidden.
	for i := range 10 {
		cp.Elements = append(cp.Elements, screen.Element{Role: screen.RoleText, Label: fmt.Sprintf("Line %d", i),
			Bounds: screen.Rect{X: 40, Y: 300 + 100*i, W: 600, H: 60}, Source: screen.SourceOCR})
	}
	return &cp, nil
}

// A read of an app the phone hides from accessibility tells the agent why
// it has only OCR text; screenshots follow the app's rules as for any
// named app, so a blocked one stays hidden; actions do not wait for a tree
// that will not come.
func TestAppHiddenFromAccessibility(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &hiddenDev{Device: base}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read the chat list")
	base.Show("calc")
	dev.set(true)

	res := s.Observe(ctx, ObserveRequest{Screenshot: true})
	mustStatus(t, res, StatusSettled)
	if !strings.Contains(res.Message, "did not give this app's window to Pinky's accessibility service") {
		t.Errorf("observe message = %q", res.Message)
	}
	if res.Screen == nil || !res.Screen.AppHidden || res.Screen.App.ID != demo.Calc ||
		!strings.Contains(res.Screen.Header(), "app hidden from accessibility") {
		t.Errorf("observe screen = %+v", res.Screen)
	}
	if res.Screenshot == nil {
		t.Errorf("no screenshot of an app the rules allow: %q", res.Message)
	}

	start := time.Now()
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"})
	mustStatus(t, res, StatusSettled)
	if took := time.Since(start); took > treeWait {
		t.Errorf("back took %v: it waited for a tree", took)
	}

	// A blocked app is hidden from the agent as ever, and so is the note.
	base.Show("wallet") // finance apps are blocked by default
	res = s.Observe(ctx, ObserveRequest{Screenshot: true})
	if res.Screenshot != nil {
		t.Error("a screenshot of a blocked app")
	}
	if res.Rule == "" || res.Screen == nil || res.Screen.AppHidden || len(res.Screen.Elements) != 0 ||
		strings.Contains(res.Message, "accessibility service") {
		t.Errorf("blocked app: rule %q, message %q, screen %+v", res.Rule, res.Message, res.Screen)
	}

	// Once the phone gives the app's window again, the note is gone.
	base.Show("calc")
	dev.set(false)
	res = s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Message, "accessibility service") || res.Screen.AppHidden {
		t.Errorf("read with the app's window: %q, %+v", res.Message, res.Screen)
	}
}
