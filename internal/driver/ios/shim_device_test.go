//go:build device

package ios

import (
	"errors"
	"io"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/springboard"
)

// TestDeviceShimDesktop checks the desktop path (usbmuxd, not tunnelOnly)
// of the calls shim.go routes: the app list with the names the home screen
// shows, the lock state, and a remoteAXService session. The app list and
// the icon layout, which shim.go now asks SpringBoard for itself, are
// compared with what go-ios's own clients read. It sends no input and
// opens no Device (so TestMain presses nothing), which lets it run on a
// locked phone. It logs counts, never app names.
//
//	PINKY_DEVICE=<udid> scripts/devlock.sh <udid> go test -tags device -count=1 -run '^TestDeviceShimDesktop$' -v ./internal/driver/ios/
//
// With PINKY_EXPECT_LOCKED=1 (or 0) it also checks the lock state it reads.
func TestDeviceShimDesktop(t *testing.T) {
	id := os.Getenv("PINKY_DEVICE")
	if id == "" {
		t.Skip("PINKY_DEVICE not set")
	}
	ctx := ctxFor(t, 2*time.Minute)
	l, err := dialLink(ctx, id, false)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.tunnelOnly {
		t.Fatal("a link through usbmuxd reads as tunnelOnly")
	}

	t.Run("IconLayout", func(t *testing.T) {
		sb, err := springboard.NewClient(l.base)
		if err != nil {
			t.Fatal(err)
		}
		want, err := sb.GetIconLayout("2")
		sb.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err := l.iconLayout(func(io.Closer) {})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Error(`the icon layout differs from go-ios's GetIconLayout("2")`)
		}
		wantNames := map[string]string{}
		collectIconNames(want, wantNames, 0)
		names, err := homeScreenNames(l, homeScreenWait)
		if err != nil {
			t.Fatal(err)
		}
		if len(names) == 0 {
			t.Fatal("no names on the home screen")
		}
		if !reflect.DeepEqual(names, wantNames) {
			t.Fatalf("homeScreenNames read %d names, go-ios's layout %d, and they differ", len(names), len(wantNames))
		}
		t.Logf("%d names on the home screen, the same as go-ios's", len(names))
	})

	t.Run("Apps", func(t *testing.T) {
		got, err := browseApps(l)
		if err != nil {
			t.Fatal(err)
		}
		// What browseApps read before shim.go: go-ios's own clients.
		conn, err := installationproxy.New(l.base)
		if err != nil {
			t.Fatal(err)
		}
		infos, err := conn.BrowseAllApps()
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		sb, err := springboard.NewClient(l.base)
		if err != nil {
			t.Fatal(err)
		}
		layout, err := sb.GetIconLayout("2")
		sb.Close()
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]string{}
		collectIconNames(layout, names, 0)
		want := readAppInfos(infos, names)
		byID := func(apps []driver.App) []driver.App {
			s := slices.Clone(apps)
			sort.Slice(s, func(i, j int) bool { return s[i].ID < s[j].ID })
			return s
		}
		if !reflect.DeepEqual(byID(got.apps), byID(want.apps)) ||
			!reflect.DeepEqual(got.byPath, want.byPath) ||
			!reflect.DeepEqual(got.schemes, want.schemes) {
			t.Errorf("browseApps read %d apps, go-ios's clients %d, and the lists differ", len(got.apps), len(want.apps))
		}
		ids := map[string]bool{}
		for _, a := range got.apps {
			ids[a.ID] = true
		}
		for _, id := range []string{"com.apple.Preferences", "com.apple.calculator", "com.apple.mobiletimer"} {
			if !ids[id] {
				t.Errorf("%s missing from %d apps", id, len(got.apps))
			}
		}
		t.Logf("%d apps, %d names from the home screen", len(got.apps), len(names))
	})

	t.Run("Locked", func(t *testing.T) {
		locked, err := (&Device{l: l}).Locked(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("locked: %v", locked)
		switch os.Getenv("PINKY_EXPECT_LOCKED") {
		case "1":
			if !locked {
				t.Error("the phone reads as unlocked, PINKY_EXPECT_LOCKED=1")
			}
		case "0":
			if locked {
				t.Error("the phone reads as locked, PINKY_EXPECT_LOCKED=0")
			}
		}
	})

	t.Run("RemoteAX", func(t *testing.T) {
		start := time.Now()
		s, err := dialRAX(ctx, l.entry, raxAnswerWait)
		took := time.Since(start).Round(time.Millisecond)
		if errors.Is(err, errRAXTimeout) {
			if want := "remote accessibility: no answer within " + raxAnswerWait.String(); err.Error() != want {
				t.Errorf("a timeout reads %q, want %q", err, want)
			}
		}
		if err != nil {
			t.Fatalf("remoteAXService after %v: %v", took, err)
		}
		s.Close()
		t.Logf("remoteAXService answered in %v", took)
	})
}
