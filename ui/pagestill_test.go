package ui

import (
	"os/exec"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/video"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// TestPageStill verifies when a phone's page holds a still picture instead
// of keeping a live stream open.
func TestPageStill(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	locked := gateway.DeviceView{Info: driver.Info{ID: "iphone", Platform: driver.IOS, State: driver.StateOnline}, Status: gateway.DeviceLocked}
	idle := locked
	idle.Status = gateway.DeviceIdle
	android := locked
	android.Info.Platform = driver.Android
	working := &gateway.TaskView{ID: "t1", State: gateway.TaskWorking}
	for _, tc := range []struct {
		name string
		d    gateway.DeviceView
		t    *gateway.TaskView
		want bool
	}{
		{"locked iPhone", locked, nil, true},
		{"unlocked iPhone", idle, nil, false},
		{"locked iPhone with a task", locked, working, false},
		{"locked Android phone", android, nil, false},
	} {
		if got := ta.pageStill(tc.d, tc.t); got != tc.want {
			t.Errorf("%s: pageStill = %v, want %v", tc.name, got, tc.want)
		}
	}

	ta.unlocks["iphone"] = true
	if ta.pageStill(locked, nil) {
		t.Error("pageStill = true while computer unlock is pending, want false")
	}
	delete(ta.unlocks, "iphone")

	m := &mirror{a: ta.app, id: "iphone", pointerOn: true, lookedAt: time.Now()}
	ta.mirrors["iphone"] = m
	if ta.pageStill(locked, nil) {
		t.Error("pageStill = true while pointer is on screen, want false")
	}
	m.pointerOn = false
	if ta.pageStill(locked, nil) {
		t.Error("pageStill = true right after pointer left, want false")
	}
	m.lookedAt = time.Now().Add(-pageLookFor - time.Second)
	if !ta.pageStill(locked, nil) {
		t.Error("pageStill = false after pageLookFor elapsed, want true")
	}
}

// TestUnseenOpensNoStream verifies that hidden window builds don't open or
// keep streams running, and reopen them once shown again.
func TestUnseenOpensNoStream(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs the ffmpeg command")
	}
	if err := video.Load(); err != nil {
		t.Skip(err)
	}
	const iphone = "iphone-17"
	ta := newTestApp(t, fakeapi.ThreePhones)
	ta.sync()
	ta.app.videoErr = nil
	t.Cleanup(ta.closeMirrors)
	streaming := func() bool {
		for _, d := range ta.api.Devices() {
			if d.Info.ID == iphone {
				return d.Streaming
			}
		}
		return false
	}
	if err := ta.api.Stop(iphone); err != nil {
		t.Fatal(err)
	}
	ta.selectPhone(iphone)
	tt := mg.NewTester(ta.view, 1280, 820)
	ta.frameUntil(t, tt, "iPhone 17's page live", func() bool {
		m := ta.mirrors[iphone]
		return m != nil && m.state == "playing" && m.bitmap != nil && streaming()
	})

	tt.SetWindowHidden(true)
	ta.frameUntil(t, tt, "the stream let go", func() bool { return !streaming() })
	for range 5 {
		tt.Frame()
		ta.sync()
	}
	if streaming() {
		t.Fatal("hidden window build reopened the stream")
	}
	if m := ta.mirrors[iphone]; m == nil || m.state != "still" || m.player != nil {
		t.Fatalf("mirror while hidden = %+v, want still with nil player", m)
	}

	tt.SetWindowHidden(false)
	ta.frameUntil(t, tt, "iPhone 17's page live again", func() bool {
		m := ta.mirrors[iphone]
		return m != nil && m.state == "playing" && streaming()
	})
}

// TestPagePointerBuildsAgain verifies that hover enter/leave on the mirror
// triggers a rebuild so pageStill schedules its timeout after the pointer leaves.
func TestPagePointerBuildsAgain(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	posts := 0
	ta.app.postHook = func(func()) { posts++ }
	d := gateway.DeviceView{Info: driver.Info{ID: "iphone", Platform: driver.IOS, State: driver.StateOnline}, Status: gateway.DeviceLocked}
	m := &mirror{a: ta.app, id: "iphone", state: "still"}
	ta.mirrors["iphone"] = m
	tt := mg.NewTester(func(c *mg.Context) {
		mg.Box(c).Size(200, 400).Children(func() { ta.mirrorView(c, m, d, nil, 0, onPage) })
	}, 400, 600)
	tt.Frame()
	if m.pointerOn || posts != 0 {
		t.Fatalf("initial state: pointerOn=%v, posts=%d; want false, 0", m.pointerOn, posts)
	}

	tt.Move(100, 200)
	tt.Frame()
	if !m.pointerOn || posts == 0 {
		t.Fatalf("after hover enter: pointerOn=%v, posts=%d; want true, >0", m.pointerOn, posts)
	}
	if ta.pageStill(d, nil) {
		t.Error("pageStill = true while pointer is on screen, want false")
	}

	posts = 0
	tt.Move(300, 500)
	tt.Frame()
	if m.pointerOn || posts == 0 {
		t.Fatalf("after hover leave: pointerOn=%v, posts=%d; want false, >0", m.pointerOn, posts)
	}
	if ta.pageStill(d, nil) || !m.lookDue {
		t.Errorf("after hover leave: pageStill=%v, lookDue=%v; want false, true", ta.pageStill(d, nil), m.lookDue)
	}
}
