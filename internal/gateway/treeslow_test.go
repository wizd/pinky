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

// slowTreeDev is a demo iPhone whose element tree the phone gives too
// slowly, as WebDriverAgent did in a long React Native list (field report
// of 2026-11-22): the driver answers with the app and OCR's text alone
// (screen.Screen.TreeTooSlow).
type slowTreeDev struct {
	*demo.Device
	mu   sync.Mutex
	slow bool
}

func (d *slowTreeDev) set(slow bool) {
	d.mu.Lock()
	d.slow = slow
	d.mu.Unlock()
}

func (d *slowTreeDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	d.mu.Lock()
	slow := d.slow
	d.mu.Unlock()
	s, err := d.Device.Observe(ctx, opt)
	if err != nil || !slow {
		return s, err
	}
	cp := *s
	cp.Elements, cp.TreeTooSlow = nil, true
	for i := range 10 {
		cp.Elements = append(cp.Elements, screen.Element{Role: screen.RoleText, Label: fmt.Sprintf("Row %d", i),
			Bounds: screen.Rect{X: 40, Y: 300 + 100*i, W: 600, H: 60}, Source: screen.SourceOCR})
	}
	return &cp, nil
}

// A read without the tree tells the agent why it has no refs; an action
// whose result is read that way is settled, says it reached the phone, and
// does not wait for a tree that will not come soon.
func TestTreeTooSlow(t *testing.T) {
	ctx := context.Background()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android) // the gateway reads every platform alike
	dev := &slowTreeDev{Device: base}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read the list")
	base.Show("calc")
	dev.set(true)

	res := s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if !strings.HasPrefix(res.Message, treeTooSlowMessage) {
		t.Errorf("observe message = %q", res.Message)
	}
	if res.Screen == nil || !res.Screen.TreeTooSlow || !strings.Contains(res.Screen.Header(), "element tree too slow") {
		t.Errorf("observe screen = %+v", res.Screen)
	}

	start := time.Now()
	res = s.Act(ctx, Call{}, Action{Tool: "key", Key: "back"})
	mustStatus(t, res, StatusSettled)
	if took := time.Since(start); took > treeWait {
		t.Errorf("back took %v: it waited for a tree", took)
	}
	if !strings.HasPrefix(res.Message, "The action was delivered to the phone") ||
		!strings.Contains(res.Message, "did not give this screen's elements in time") {
		t.Errorf("action message = %q", res.Message)
	}

	// With the tree back, so are the refs, without the note.
	dev.set(false)
	res = s.Observe(ctx, ObserveRequest{})
	mustStatus(t, res, StatusSettled)
	if res.Message != "" || res.Screen.TreeTooSlow {
		t.Errorf("read with the tree: %q, %+v", res.Message, res.Screen)
	}
}
