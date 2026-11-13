package android

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestOtherServerHoldsPhone: another adb server, on another port, holds a
// phone on a cable (GitHub issue #95): CheckHost says so with the port and
// the model but not the serial, and stops once the server Pinky uses
// lists the phone. Phones with no permission, emulators and phones on
// Wi-Fi are not held.
func TestOtherServerHoldsPhone(t *testing.T) {
	other := startFakeADB(t)
	other.set(func(f *fakeADB) {
		f.devices = []string{
			"HELD123\tdevice usb:1-1 product:k70 model:Redmi_K70 device:x transport_id:1",
			"NOPERM\tno permissions (missing udev rules?) usb:1-2 transport_id:2",
			"emulator-5554\tdevice product:sdk model:sdk_gphone64 device:x transport_id:3",
			"192.168.1.5:5555\tdevice product:x model:Pixel_9 device:x transport_id:4",
		}
	})
	_, portText, _ := net.SplitHostPort(other.ln.Addr().String())
	otherPort, _ := strconv.Atoi(portText)

	f := startFakeADB(t)
	cfg := f.config()
	asked := 0
	cfg.OtherServers = func() ([]int, error) { asked++; return []int{otherPort}, nil }
	p := NewProvider(cfg)
	held := func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if c := p.CheckHost(); c.Missing != "" || time.Now().After(deadline) {
				return c.Missing + "|" + c.Detail
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if _, err := p.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := held()
	if !strings.HasPrefix(got, hostADBOtherServer+"|Another program's adb server, on port "+portText+", holds Redmi K70 over USB") ||
		!strings.Contains(got, "adb -P "+portText+" kill-server") || strings.Contains(got, "HELD123") || strings.Contains(got, "Pixel") {
		t.Fatalf("CheckHost = %q", got)
	}
	// The next List does not look again within otherScanEvery.
	if _, err := p.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Errorf("looked for other servers %d times, want 1", asked)
	}
	// The server Pinky uses gets the phone: no word of it at once.
	f.set(func(f *fakeADB) {
		f.devices = []string{"HELD123\tdevice usb:1-1 product:k70 model:Redmi_K70 device:x transport_id:7"}
	})
	if _, err := p.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	if c := p.CheckHost(); c.Missing != "" {
		t.Errorf("the phone is Pinky's again: %+v", c)
	}
}

// TestOtherServersOff: without Config.OtherServers (androidctl, tests)
// the driver looks for no other server.
func TestOtherServersOff(t *testing.T) {
	_, p := newFakeADB(t)
	if _, err := p.List(t.Context()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if c := p.CheckHost(); c.Missing != "" {
		t.Errorf("CheckHost = %+v", c)
	}
}
