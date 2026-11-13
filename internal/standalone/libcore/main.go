// Command libcore is Pinky's Go core as a shared library (libpinkycore.so),
// which the Android app's accessibility service loads through JNI (Core.kt)
// to run tasks on the phone itself (docs/notes/android-app.md).
// scripts/androidcore.sh builds it with the Android NDK:
//
//	CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=<ndk clang> \
//		go build -buildmode=c-shared -o libpinkycore.so ./internal/standalone/libcore
//
// Elsewhere it builds as an empty program, so `go build ./...` passes.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/local"
	"github.com/DIYgod/pinky/internal/driver/android/wadb"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/standalone"
)

func main() {}

var (
	mu      sync.Mutex
	running *standalone.Core
)

// start starts the core once per process: the local driver on the
// service's companion socket, the gateway and agent, and the control
// socket for Pinky's screens. pkg is the app's package, the released app
// or the development build.
func start(dataDir, pkg, companionSocket, coreSocket, version string, logw io.Writer) error {
	mu.Lock()
	defer mu.Unlock()
	if running != nil {
		return nil
	}
	log := slog.New(slog.NewTextHandler(logw, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	app, ok := companion.AppFor(pkg)
	if !ok {
		return fmt.Errorf("not Pinky's app: %s", pkg)
	}
	app.Socket = companionSocket
	ln, err := net.Listen("unix", "@"+coreSocket)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	// Background displays: the shell connection Wireless debugging gives
	// the app, with its key in the app's private storage, only while the
	// person has the feature on (docs/notes/android-wadb.md).
	sh := &shell{}
	sh.Manager = wadb.NewManager(filepath.Join(dataDir, "wadb"), "Pinky", sh.notify)
	provider := local.NewProvider(app)
	provider.SetShell(sh.Manager)
	c, err := standalone.New(standalone.Options{
		Providers: []driver.Provider{provider},
		Device:    local.ID,
		DataDir:   dataDir,
		// Desktop Pinky's default (Settings › Privacy).
		KeepDays: 30,
		Version:  version,
		// On the phone it drives, ask before commits, messages and
		// settings changes too, not only payments.
		Preset: policy.PresetCommits,
		Gateway: gateway.Options{
			// Activity on the phone shows the steps' words only, and
			// each picture would cost a capture on the phone.
			NoScreenshots: true,
		},
		// The task claims the phone, so that Pinky on a computer and Pinky
		// on the phone never drive it at once.
		Claims:          local.NewClaims(app),
		Shell:           sh,
		BackgroundTouch: provider.BackgroundTouch,
		Log:             log,
	})
	if err != nil {
		sh.Close()
		ln.Close()
		return err
	}
	running = c
	go func() {
		if err := c.Serve(context.Background(), ln); err != nil {
			log.Error("control socket stopped", "err", err)
		}
	}()
	log.Info("core ready", "data", dataDir, "companion", companionSocket, "control", coreSocket)
	return nil
}

// shell adapts wadb's Manager to standalone.Shell.
type shell struct {
	*wadb.Manager
	changed atomic.Pointer[func()]
}

func (s *shell) notify() {
	if f := s.changed.Load(); f != nil {
		(*f)()
	}
}

func (s *shell) Watch(changed func()) { s.changed.Store(&changed) }

func (s *shell) State() standalone.ShellState {
	st := s.Manager.State()
	return standalone.ShellState{On: st.On, Status: string(st.Status), Port: st.Port, Error: st.Error}
}

func (s *shell) Pair(ctx context.Context, port int, code string) error {
	err := s.Manager.Pair(ctx, port, code)
	if errors.Is(err, wadb.ErrWrongCode) {
		return fmt.Errorf("%w: %w", standalone.ErrWrongCode, err)
	}
	return err
}
