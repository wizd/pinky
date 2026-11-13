package android

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestXiaomiROMProp tells MIUI and HyperOS from other systems by their
// version properties (DIYgod/pinky#48).
func TestXiaomiROMProp(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      bool
	}{
		{"HyperOS", "Mi 10 Pro\n13\n33\nMi 10 Pro\n3e8be48e\npinky-xiaomi=OS1.0.9.0\n", true},
		{"MIUI", "Redmi Note 9\n11\n30\nRedmi Note 9\nabc\npinky-xiaomi=V125\n", true},
		{"LineageOS on a Xiaomi phone", "Mi 10 Pro\n17\n37\nMi 10 Pro\n3e8be48e\npinky-xiaomi=\n", false},
		{"Pixel", "Pixel 9\n16\n36\nPixel 9\nR5CN20ABC\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newFakeADB(t)
			f.set(func(f *fakeADB) { f.props = map[string]string{"phone": tc.out} })
			pr, err := p.deviceProps(context.Background(), adb.DeviceEntry{Serial: "phone", TransportID: "1"})
			if err != nil {
				t.Fatal(err)
			}
			if pr.XiaomiROM != tc.want {
				t.Errorf("XiaomiROM = %v, want %v", pr.XiaomiROM, tc.want)
			}
		})
	}
}

// TestDisplayInputChoice sends a background display's input through the
// companion on MIUI and HyperOS, or when asked, and refuses the display
// where the companion cannot stand in, so the task runs on the phone's
// own screen.
func TestDisplayInputChoice(t *testing.T) {
	ctx := context.Background()
	dev := func(xiaomi bool, sdk int) *Device {
		d, _ := testDevice(&fakeRunner{})
		d.props = deviceProps{XiaomiROM: xiaomi, SDK: sdk}
		return d
	}
	for _, tc := range []struct {
		name   string
		xiaomi bool
		sdk    int
		input  string
		err    string // "" for scrcpy
	}{
		{"auto on other phones", false, 34, InputAuto, ""},
		{"scrcpy when asked, on Xiaomi too", true, 34, InputScrcpy, ""},
		{"Xiaomi before Android 11", true, 29, InputAuto, "need Android 11"},
		{"Xiaomi without the app", true, 33, InputAuto, "its app is not installed"},
		{"companion asked for, without the app", false, 34, InputCompanion, "its app is not installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			via, err := dev(tc.xiaomi, tc.sdk).displayInput(ctx, tc.input)
			if via {
				t.Fatal("chose the companion")
			}
			if tc.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, driver.ErrUnsupported) || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err = %v, want ErrUnsupported with %q", err, tc.err)
			}
		})
	}
	if _, err := dev(false, 34).displayInput(ctx, "adb"); err == nil || errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("an unknown input: %v", err)
	}
}

func TestDragSegments(t *testing.T) {
	a, b, c := screen.Point{X: 100, Y: 200}, screen.Point{X: 100, Y: 800}, screen.Point{X: 500, Y: 800}
	show := func(segs []companion.Segment) string {
		var parts []string
		for _, s := range segs {
			parts = append(parts, fmt.Sprintf("%v/%v", s.Points, s.Duration))
		}
		return strings.Join(parts, " ")
	}
	for _, tc := range []struct {
		name  string
		g     driver.Drag
		first bool
		want  string
	}{
		{"hold, move, pause, move", driver.Drag{From: a, Hold: 600 * time.Millisecond, Stops: []driver.DragStop{
			{Point: b, Move: 300 * time.Millisecond, Pause: 200 * time.Millisecond},
			{Point: c, Move: 250 * time.Millisecond},
		}}, true, "[{100 200}]/600ms [{100 800}]/300ms [{100 800}]/200ms [{500 800}]/250ms"},
		{"no hold: the first move starts at From", driver.Drag{From: a, Stops: []driver.DragStop{{Point: b, Move: 300 * time.Millisecond}}},
			true, "[{100 200} {100 800}]/300ms"},
		{"no stops: a press", driver.Drag{From: a}, true, "[{100 200}]/50ms"},
		{"going on from a held finger", driver.Drag{From: b, Stops: []driver.DragStop{{Point: c}}},
			false, "[{500 800}]/1ms"},
		{"nothing left", driver.Drag{From: b}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := show(dragSegments(tc.g, tc.first)); got != tc.want {
				t.Errorf("segments %s, want %s", got, tc.want)
			}
		})
	}
}

// TestDroppedKeyNote: what the agent hears about a key that went through
// scrcpy on a phone that drops adb's input on a background display.
func TestDroppedKeyNote(t *testing.T) {
	code := func(c string) error { return &companion.Error{Code: c, Message: c} }
	cases := []struct {
		key     driver.Key
		refused error
		want    string
	}{
		{driver.KeyBack, code(companion.CodeNotFocused), noteBackUnfocused},
		{driver.KeyBack, code("action_failed"), noteKeyDropped},
		{driver.KeyEnter, code("not_found"), noteEnterNoField},
		{driver.KeyEnter, code("action_failed"), noteEnterNoField},
		{driver.KeyHome, nil, noteKeyDropped},
		{driver.KeyDelete, nil, noteKeyDropped},
	}
	for _, c := range cases {
		if got := droppedKeyNote(c.key, c.refused); got != c.want {
			t.Errorf("droppedKeyNote(%s, %v) = %q, want %q", c.key, c.refused, got, c.want)
		}
	}
}
