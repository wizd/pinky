package mcp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// recordedTasks keeps the phone of the task it was handed last.
type recordedTasks struct {
	Tasks
	device string
}

func (r *recordedTasks) Run(req TaskRequest) (Task, error) {
	r.device = req.Device
	return Task{ID: "t1", Device: req.Device, Task: req.Task}, nil
}

// listedPhones are phones listed but never opened: one that is online
// keeps connecting. The first list is empty, as the gateway waits for the
// phones of the one it starts with.
type listedPhones struct {
	infos []driver.Info
	later atomic.Bool
}

func (p *listedPhones) Platform() driver.Platform { return driver.IOS }
func (p *listedPhones) List(context.Context) ([]driver.Info, error) {
	if !p.later.Swap(true) {
		return nil, nil
	}
	return p.infos, nil
}
func (p *listedPhones) Open(ctx context.Context, _ string) (driver.Device, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestTaskRunPicksThePhoneOfConnect: without a device, phone_task_run
// takes the phone phone_connect would: the one online, whatever else is
// listed, or the one that waits for a step. A phone found on Wi-Fi may not
// be the person's: it is never the one, and agents do not hear of it.
func TestTaskRunPicksThePhoneOfConnect(t *testing.T) {
	found := driver.Info{ID: "found", Platform: driver.IOS, Name: "Someone's iPhone", Connection: "wifi",
		State: driver.StateNeedsSetup, Setup: driver.SetupConfirmWiFi}
	waiting := driver.Info{ID: "waiting", Platform: driver.IOS, Name: "Ada's iPhone", Connection: "usb",
		State: driver.StateNeedsSetup}
	waiting2 := waiting
	waiting2.ID, waiting2.Name = "waiting2", "Ada's iPad"
	offline := driver.Info{ID: "offline", Platform: driver.IOS, Name: "Old iPhone", Connection: "wifi", State: driver.StateOffline}
	connecting := driver.Info{ID: "connecting", Platform: driver.IOS, Name: "New iPhone", Connection: "usb", State: driver.StateOnline}
	for _, c := range []struct {
		name   string
		online bool // Pixel 9 is online too
		listed []driver.Info
		want   string // the phone
		err    string // or the error
	}{
		{"one online, one found", true, []driver.Info{found}, "pixel", ""},
		{"one online, one that waits", true, []driver.Info{waiting}, "pixel", ""},
		{"one online, one offline", true, []driver.Info{offline}, "pixel", ""},
		{"one online, one connecting", true, []driver.Info{connecting}, "pixel", ""},
		{"only one connecting", false, []driver.Info{connecting}, "", "error: device_offline"},
		{"one that waits, one found", false, []driver.Info{waiting, found}, "waiting", ""},
		{"only one found", false, []driver.Info{found}, "", "error: device_offline"},
		{"two that wait, one found", false, []driver.Info{waiting, waiting2, found}, "", "error: invalid_argument"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, func(o *gateway.Options) {
				if !c.online {
					o.Providers = nil
				}
				o.Providers = append(o.Providers, &listedPhones{infos: c.listed})
			})
			n := len(c.listed)
			if c.online {
				n++
			}
			listed := func() bool {
				devs := h.g.Devices()
				for _, d := range devs {
					if d.Info.ID == "pixel" && d.Status != gateway.DeviceIdle ||
						d.Info.ID == "connecting" && d.Status != gateway.DeviceConnecting {
						return false
					}
				}
				return len(devs) == n
			}
			for deadline := time.Now().Add(3 * time.Second); !listed(); time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("devices: %+v", h.g.Devices())
				}
			}
			var ran recordedTasks
			h.srv.SetTasks(&ran)
			cl := h.connect(t, "claude-code")
			text, isErr := cl.call(t, "phone_task_run", map[string]any{"task": "Look around"})
			if ran.device != c.want || isErr != (c.err != "") || !strings.HasPrefix(text, c.err) {
				t.Errorf("phone_task_run on %q:\n%s", ran.device, text)
			}
			if strings.Contains(text, found.Name) {
				t.Errorf("phone_task_run names a phone found on Wi-Fi:\n%s", text)
			}
			// phone_connect agrees: it names the phone, or fails alike.
			text, _ = cl.call(t, "phone_connect", map[string]any{"task": "Look around"})
			switch {
			case c.err != "" && !strings.HasPrefix(text, c.err):
				t.Errorf("phone_connect:\n%s", text)
			case c.want != "":
				name := map[string]string{"pixel": "Pixel 9", "waiting": waiting.Name}[c.want]
				if !strings.Contains(text, name) {
					t.Errorf("phone_connect names no %s:\n%s", name, text)
				}
			}
			if strings.Contains(text, found.Name) {
				t.Errorf("phone_connect names a phone found on Wi-Fi:\n%s", text)
			}

			// A name that matches none.
			text, _ = cl.call(t, "phone_task_run", map[string]any{"task": "Look around", "device": "no such phone"})
			if !strings.HasPrefix(text, "error: invalid_argument") || strings.Contains(text, found.Name) {
				t.Errorf("no such phone:\n%s", text)
			}
		})
	}
}
