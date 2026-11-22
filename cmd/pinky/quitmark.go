package main

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/internal/api"
)

// Quitting Pinky is how the person stops every agent, so a quit they chose
// lasts until they open Pinky again (issue #142). A shim that runs when
// Pinky quits waits for it (shimconn.go, awaitReopen), but agents start new
// shims all the time: a new session, a subagent, a client restarting its
// MCP servers. Those used to start Pinky in the background again
// (launchGateway). Now the app leaves a marker in the data folder when the
// person quits it, and while it is there `pinky mcp` and `pinky task` do
// not start Pinky:
//
//   - written when the person quits: the tray's or menu bar's Quit, the
//     app's own Quit menu and its shortcut, the Dock's Quit, and `pinky
//     quit` (the app's or the daemon's);
//   - not written when Pinky restarts for an update, crashes, is stopped by
//     a signal, or quits because the system ends the session (logout,
//     restart, shutdown);
//   - removed when the person opens Pinky (the app started without
//     --background), and whenever a gateway with real phones starts, so it
//     never outlives the next Pinky that runs.
//
// --fake neither reads nor removes it.

// quitMarkerName is the marker's name in the data folder.
const quitMarkerName = "quit-by-person"

func quitMarker(dirs api.Dirs) string { return filepath.Join(dirs.Data, quitMarkerName) }

// markQuit notes that the person quit Pinky on purpose.
func markQuit(dirs api.Dirs) error {
	if err := dirs.Ensure(); err != nil {
		return err
	}
	return os.WriteFile(quitMarker(dirs), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}

// clearQuit removes the marker: Pinky runs again.
func clearQuit(dirs api.Dirs) error {
	if err := os.Remove(quitMarker(dirs)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// quitByPerson reports whether the person quit Pinky and has not opened it
// since.
func quitByPerson(dirs api.Dirs) bool {
	_, err := os.Stat(quitMarker(dirs))
	return err == nil
}

// errPersonQuit is why `pinky mcp` and `pinky task` do not start Pinky:
// the person quit it. Agents read it, so it stays English.
var errPersonQuit = errors.New("Pinky is not running: the person quit it, and agents do not start it again until " +
	"the person opens Pinky. Ask them to open Pinky, then try again")

// appStarted removes the marker when the person opened the app, that is
// when it starts without --background. Pinky has no login item of its own;
// one the person adds starts it so too.
func appStarted(dirs api.Dirs, background, fake bool, log *slog.Logger) {
	if !background {
		gatewayUp(dirs, fake, log)
	}
}

// gatewayUp removes the marker once a gateway runs, unless it has fake
// phones.
func gatewayUp(dirs api.Dirs, fake bool, log *slog.Logger) {
	if fake {
		return
	}
	if err := clearQuit(dirs); err != nil && log != nil {
		log.Warn("remove the quit marker", "err", err)
	}
}

// quitIntent tells a quit the person chose from one the system or a signal
// made. The app's quit listener and its signal channel fill it in; the app
// reads it once the client has quit.
type quitIntent struct {
	mu      sync.Mutex
	started bool // a quit started
	system  bool // the system ends the session, or a signal stopped Pinky
}

// watch listens to the app's quits. Call it before the client runs.
func (q *quitIntent) watch() {
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) { q.before(e.SessionEnding || sessionEnding()) })
	mygo.App.OnQuitCanceled(q.canceled)
}

// before notes a quit that starts; ending is true when the system ends
// the session.
func (q *quitIntent) before(ending bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.started = true
	if ending {
		q.system = true
	}
}

func (q *quitIntent) canceled() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.started, q.system = false, false
}

// signalled notes that a signal stopped Pinky, as at logout on Linux.
func (q *quitIntent) signalled() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.system = true
}

// earlySignal quits for a SIGINT or SIGTERM that came while the app was
// starting. signals hears them from before ui.Run, but MyGo only once its
// event loop starts: one in between reached signals alone, and Pinky went
// on running as if it had not come. Call it once the window is up; should
// MyGo have heard the signal too, the second quit does nothing.
func (q *quitIntent) earlySignal(signals <-chan os.Signal, quit func()) {
	select {
	case <-signals:
		q.signalled()
		quit()
	default:
	}
}

// byPerson reports whether the person quit Pinky.
func (q *quitIntent) byPerson() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.started && !q.system
}

// afterQuit leaves the marker when the person quit the app: the client
// quit without an error, not to restart for an update, and neither the
// system nor a signal made it quit.
func afterQuit(dirs api.Dirs, q *quitIntent, restarting, fake bool, runErr error, log *slog.Logger) {
	if fake || restarting || runErr != nil || !q.byPerson() {
		return
	}
	if err := markQuit(dirs); err != nil {
		log.Warn("mark the quit", "err", err)
		return
	}
	log.Info("the person quit Pinky: agents do not start it again until it is opened")
}
