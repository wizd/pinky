package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// claimDev is a demo phone whose reads and taps fail as Pinky for
// Android's companion makes them fail while Pinky on the phone holds the
// phone (busy) or after the person pressed Stop on the phone (stopped).
type claimDev struct {
	*demo.Device
	mu      sync.Mutex
	busy    *driver.BusyError
	stopped bool
	taps    int
}

func (d *claimDev) claimErr() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return fmt.Errorf("companion: %w", driver.ErrStoppedOnPhone)
	}
	if d.busy != nil {
		return fmt.Errorf("companion: %w", d.busy)
	}
	return nil
}

func (d *claimDev) set(busy *driver.BusyError, stopped bool) {
	d.mu.Lock()
	d.busy, d.stopped = busy, stopped
	d.mu.Unlock()
}

func (d *claimDev) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	if err := d.claimErr(); err != nil {
		return nil, err
	}
	return d.Device.Observe(ctx, opt)
}

func (d *claimDev) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	if err := d.claimErr(); err != nil {
		return err
	}
	d.mu.Lock()
	d.taps++
	d.mu.Unlock()
	return d.Device.Tap(ctx, p, opt)
}

func claimGateway(t *testing.T) (*Gateway, *claimDev) {
	t.Helper()
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &claimDev{Device: base}
	return newDevGateway(t, base, dev), dev
}

func statusLineOf(t *testing.T, g *Gateway) (DeviceStatus, string) {
	t.Helper()
	ds := g.Devices()
	if len(ds) != 1 {
		t.Fatalf("devices: %+v", ds)
	}
	return ds[0].Status, ds[0].StatusLine
}

// While Pinky on the phone runs a task, the agent's calls return
// device_busy naming that task, nothing is done, and the phone shows busy
// until a read goes through again.
func TestBusyOnPhone(t *testing.T) {
	ctx := context.Background()
	g, dev := claimGateway(t)
	s, first := connect(t, g, "Order lunch")

	dev.set(&driver.BusyError{By: "phone", Task: "Water the plants"}, false)
	res := s.Observe(ctx, ObserveRequest{})
	if res.Error != ErrDeviceBusy || !strings.Contains(res.Message, "Pinky on the phone") || !strings.Contains(res.Message, "Water the plants") || !strings.Contains(res.Message, "Nothing was done") {
		t.Fatalf("a read while the phone is busy: %q %q", res.Error, res.Message)
	}
	if st, line := statusLineOf(t, g); st != DeviceWorking || line != busyLinePhone {
		t.Fatalf("a busy phone shows %q %q", st, line)
	}

	res = tap(s, ref(t, first, "Dinely"))
	if res.Error != ErrDeviceBusy {
		t.Fatalf("a tap while the phone is busy: %q %q %q", res.Status, res.Error, res.Message)
	}
	dev.mu.Lock()
	taps := dev.taps
	dev.mu.Unlock()
	if taps != 0 {
		t.Fatalf("%d taps went to a busy phone", taps)
	}

	dev.set(&driver.BusyError{By: "computer"}, false)
	if res := s.Observe(ctx, ObserveRequest{}); res.Error != ErrDeviceBusy || !strings.Contains(res.Message, "another computer") {
		t.Fatalf("a read while another computer holds the phone: %q %q", res.Error, res.Message)
	}
	if _, line := statusLineOf(t, g); line != busyLineComputer {
		t.Fatalf("a phone another computer holds shows %q", line)
	}

	dev.set(nil, false)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusSettled)
	if st, line := statusLineOf(t, g); st != DeviceWorking || line != "Claude Code · Order lunch" {
		t.Fatalf("once the phone is free it shows %q %q", st, line)
	}
}

// A phone busy when the task starts says so at phone_connect, and the
// busy line goes with the task.
func TestBusyAtConnect(t *testing.T) {
	ctx := context.Background()
	g, dev := claimGateway(t)
	dev.set(&driver.BusyError{By: "phone"}, false)
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(ctx, ConnectRequest{Task: "Order lunch"})
	if res.Error != ErrDeviceBusy {
		t.Fatalf("connect to a busy phone: %q %q %q", res.Status, res.Error, res.Message)
	}
	if _, line := statusLineOf(t, g); line != busyLinePhone {
		t.Fatalf("a busy phone shows %q", line)
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{}), StatusSettled)
	if st, line := statusLineOf(t, g); st != DeviceIdle || line != "Idle" {
		t.Fatalf("after the task the phone shows %q %q", st, line)
	}
}

// Stop on the phone's pill ends the agent's task as stopped by the
// person, as Stop in Pinky does: the call says so, the task reads
// stopped, and the session may not take the phone again.
func TestStoppedOnPhone(t *testing.T) {
	ctx := context.Background()
	g, dev := claimGateway(t)
	s, first := connect(t, g, "Order lunch")
	dev.set(nil, true)
	res := tap(s, ref(t, first, "Dinely"))
	mustStatus(t, res, StatusStopped)
	if !strings.Contains(res.Message, "on the phone") {
		t.Fatalf("stopped on the phone says %q", res.Message)
	}
	var found bool
	for _, tv := range g.Tasks() {
		if tv.Title == "Order lunch" {
			found = true
			if tv.State != TaskStopped {
				t.Fatalf("the task reads %q", tv.State)
			}
		}
	}
	if !found {
		t.Fatal("the task is gone from Tasks")
	}
	dev.set(nil, false)
	mustStatus(t, s.Observe(ctx, ObserveRequest{}), StatusStopped)
	if res := s.Connect(ctx, ConnectRequest{Task: "Order lunch"}); res.Status != StatusStopped {
		t.Fatalf("connect after a stop on the phone: %q %q %q", res.Status, res.Error, res.Message)
	}
}
