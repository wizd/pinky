// Command bgcheck tries the core's background display on the phone it runs
// on (docs/notes/android-wadb.md): it connects to the phone's own adbd with
// a key Wireless debugging already paired (wadbctl pair), opens a virtual
// display through the local driver, starts an app there, reads it, taps it,
// counts its video and closes it. Run it on the phone (adb push, then adb
// shell, whose uid Pinky's service accepts) while the service is on.
//
//	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bgcheck ./internal/driver/android/local/cmd/bgcheck
//	adb shell /data/local/tmp/bgcheck -key /data/local/tmp/wadb -port 41889
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/local"
	"github.com/DIYgod/pinky/internal/driver/android/wadb"
	"github.com/DIYgod/pinky/internal/screen"
)

var (
	keyDir = flag.String("key", "", "directory holding the paired key (adbkey.pem)")
	port   = flag.Int("port", 0, "Wireless debugging's port")
	app    = flag.String("app", "com.android.settings", "app to start on the display")
	dev    = flag.Bool("dev", false, "use the development build of Pinky's app")
)

func main() {
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "bgcheck:", err)
		os.Exit(1)
	}
}

func step(name string, t0 time.Time) {
	fmt.Printf("%-28s %v\n", name, time.Since(t0).Round(time.Millisecond))
}

func run(ctx context.Context) error {
	if *keyDir == "" || *port == 0 {
		return errors.New("-key and -port are needed")
	}
	pinky := companion.Release
	if *dev {
		pinky = companion.Dev
	}
	m := wadb.NewManager(*keyDir, "Pinky", nil)
	if err := m.SetPort(*port); err != nil {
		return err
	}
	// The key was paired by wadbctl: mark the manager paired and on.
	if err := m.MarkPaired(); err != nil {
		return err
	}
	defer m.Close()
	p := local.NewProvider(pinky)
	p.SetShell(m)
	t0 := time.Now()
	dd, err := p.Open(ctx, local.ID)
	if err != nil {
		return err
	}
	defer dd.Close()
	step("open the phone", t0)
	d := dd.(*local.Device)
	if !d.BackgroundSupported() {
		return errors.New("background displays are not supported here (Android 13+, the service's display input)")
	}
	t0 = time.Now()
	bd, err := d.OpenBackground(ctx, driver.BackgroundOptions{App: *app})
	if err != nil {
		return fmt.Errorf("open background: %w", err)
	}
	defer bd.Close()
	b := bd.(*local.Background)
	step(fmt.Sprintf("open display %d + app", b.DisplayID()), t0)
	fmt.Println("shell:", m.State().Status)

	st, err := bd.Stream(ctx, driver.StreamOptions{})
	if err != nil {
		return err
	}
	defer st.Close()

	var s *screen.Screen
	t0 = time.Now()
	for range 20 {
		if s, err = bd.Observe(ctx, driver.ObserveOptions{}); err != nil {
			return fmt.Errorf("observe: %w", err)
		}
		if s.App.ID == *app && len(s.Elements) > 0 {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	step(fmt.Sprintf("observe: %s, %d elements", s.App.ID, len(s.Elements)), t0)
	if s.App.ID != *app {
		return fmt.Errorf("the display shows %q, not %s", s.App.ID, *app)
	}
	main, err := d.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("main screen still shows %s\n", main.App.ID)

	// Tap the middle of the first labelled element below the top fifth,
	// then go back.
	for _, e := range s.Elements {
		if e.Label != "" && e.Bounds.Y > s.Height/5 {
			t0 = time.Now()
			c := screen.Point{X: e.Bounds.X + e.Bounds.W/2, Y: e.Bounds.Y + e.Bounds.H/2}
			if err := bd.Tap(ctx, c, driver.TapOptions{}); err != nil {
				return fmt.Errorf("tap %q: %w", e.Label, err)
			}
			step(fmt.Sprintf("tap %q", e.Label), t0)
			break
		}
	}
	time.Sleep(time.Second)
	t0 = time.Now()
	if err := bd.Key(ctx, driver.KeyBack); err != nil {
		return fmt.Errorf("back: %w", err)
	}
	step("back", t0)
	if err := bd.Type(ctx, "", driver.TypeOptions{}); err != nil {
		fmt.Println("type with nothing focused:", err)
	}

	// Count the video for three seconds.
	frames, cfg, bytes := 0, 0, 0
	deadline := time.After(3 * time.Second)
loop:
	for {
		select {
		case pk, ok := <-st.Packets():
			if !ok {
				return fmt.Errorf("video ended: %v", st.Err())
			}
			frames++
			bytes += len(pk.Data)
			if pk.Config {
				cfg++
			}
		case <-deadline:
			break loop
		}
	}
	w, h := st.Size()
	fmt.Printf("video: %v %dx%d, %d packets (%d config), %d bytes in 3s\n", st.Codec(), w, h, frames, cfg, bytes)
	if cfg == 0 {
		return errors.New("no configuration packet")
	}
	t0 = time.Now()
	if err := bd.Close(); err != nil {
		return err
	}
	step("close display", t0)
	select {
	case <-b.Done():
	case <-time.After(5 * time.Second):
		return errors.New("the display did not end")
	}
	return nil
}
