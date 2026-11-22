package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
)

// quitDirs points Pinky's folders at a new temporary home.
func quitDirs(t *testing.T) api.Dirs {
	t.Helper()
	t.Setenv(api.HomeEnv, t.TempDir())
	d, err := api.DefaultDirs()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestQuitMarker(t *testing.T) {
	d := quitDirs(t)
	if quitByPerson(d) {
		t.Fatal("marked before any quit")
	}
	if err := markQuit(d); err != nil {
		t.Fatal(err)
	}
	if !quitByPerson(d) {
		t.Fatal("not marked after markQuit")
	}
	if err := clearQuit(d); err != nil || quitByPerson(d) {
		t.Fatalf("clearQuit: %v, still marked %v", err, quitByPerson(d))
	}
	if err := clearQuit(d); err != nil {
		t.Fatalf("clearQuit without a marker: %v", err)
	}
}

// The marker is left when the person quits the app, and not when it quits
// to restart for an update, because the system ends the session, after a
// signal, after an error, or with fake phones.
func TestAfterQuit(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name       string
		quit       func(*quitIntent)
		restarting bool
		fake       bool
		err        error
		want       bool
	}{
		{name: "the person quits", quit: func(q *quitIntent) { q.before(false) }, want: true},
		{name: "an update restarts it", quit: func(q *quitIntent) { q.before(false) }, restarting: true},
		{name: "the session ends", quit: func(q *quitIntent) { q.before(true) }},
		{name: "a signal", quit: func(q *quitIntent) { q.before(false); q.signalled() }},
		{name: "the client failed", quit: func(q *quitIntent) {}, err: errors.New("no display")},
		{name: "an error after a quit", quit: func(q *quitIntent) { q.before(false) }, err: errors.New("boom")},
		{name: "no quit", quit: func(q *quitIntent) {}},
		{name: "a canceled quit", quit: func(q *quitIntent) { q.before(false); q.canceled() }},
		{name: "fake phones", quit: func(q *quitIntent) { q.before(false) }, fake: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := quitDirs(t)
			var q quitIntent
			c.quit(&q)
			afterQuit(d, &q, c.restarting, c.fake, c.err, log)
			if got := quitByPerson(d); got != c.want {
				t.Errorf("marked %v, want %v", got, c.want)
			}
		})
	}
}

// A signal that came while the app was starting, before MyGo listened for
// signals, quits it once the window is up, as a signal and not the person.
func TestEarlySignalQuits(t *testing.T) {
	var q quitIntent
	quits := 0
	signals := make(chan os.Signal, 1)
	q.earlySignal(signals, func() { quits++ })
	if quits != 0 {
		t.Fatalf("quit %d times without a signal", quits)
	}
	signals <- os.Interrupt
	q.earlySignal(signals, func() { quits++ })
	q.before(false) // the quit it asked for starts
	if quits != 1 {
		t.Fatalf("quit %d times after a signal, want 1", quits)
	}
	if q.byPerson() {
		t.Error("a signal's quit taken for the person's")
	}
}

// Outside a quit Apple event (Cmd-Q, a Quit menu item, `pinky quit`), the
// system does not end the session.
func TestSessionEndingOutsideAQuitEvent(t *testing.T) {
	if sessionEnding() {
		t.Fatal("sessionEnding without a quit event")
	}
}

// While the marker is there, launchGateway starts nothing.
func TestLaunchGatewayRefusesAfterAQuit(t *testing.T) {
	d := quitDirs(t)
	if err := markQuit(d); err != nil {
		t.Fatal(err)
	}
	conn, err := launchGateway(d, false)
	if conn != nil {
		conn.Close()
	}
	if !errors.Is(err, errPersonQuit) {
		t.Fatalf("launchGateway: %v, want errPersonQuit", err)
	}
	// startDaemon opens the daemon's log before it starts anything.
	if _, err := os.Stat(d.DaemonLog()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("something was started: %v", err)
	}
}

// Opening the app removes the marker; the app started in the background
// for an agent, or with fake phones, does not.
func TestOpeningPinkyClearsTheQuit(t *testing.T) {
	d := quitDirs(t)
	if err := markQuit(d); err != nil {
		t.Fatal(err)
	}
	appStarted(d, true, false, nil)
	if !quitByPerson(d) {
		t.Fatal("started in the background, the app removed the marker")
	}
	appStarted(d, false, true, nil)
	if !quitByPerson(d) {
		t.Fatal("with fake phones, the app removed the marker")
	}
	appStarted(d, false, false, nil)
	if quitByPerson(d) {
		t.Fatal("opened by the person, the app left the marker")
	}
	// Any gateway with real phones removes it too.
	markQuit(d)
	gatewayUp(d, false, nil)
	if quitByPerson(d) {
		t.Fatal("a gateway started and left the marker")
	}
}

// Started after the person quit Pinky, the shim starts the agent's session
// with Pinky's tools, answers every call that Pinky is not running, and
// starts nothing.
func TestQuitShimStartsTheSessionWithoutPinky(t *testing.T) {
	d := quitDirs(t)
	if err := markQuit(d); err != nil {
		t.Fatal(err)
	}
	agentIn, shimIn := io.Pipe()
	shimOut, agentOut := io.Pipe()
	s := newShimSession(d, "token", agentOut)
	s.poll = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runWithoutPinky(s, agentIn) }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := sdk.NewClient(&sdk.Implementation{Name: "claude-code", Version: "test"}, nil)
	cs, err := cl.Connect(ctx, &sdk.IOTransport{Reader: shimOut, Writer: shimIn}, nil)
	if err != nil {
		t.Fatalf("the session did not start: %v", err)
	}
	if v := cs.InitializeResult().ServerInfo.Version; v != version {
		t.Errorf("server version %q, want %q", v, version)
	}
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "phone_connect") {
		t.Fatalf("tools: %v", names)
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "phone_connect", Arguments: map[string]any{"task": "After a quit"}})
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	if len(res.Content) == 1 {
		if tc, ok := res.Content[0].(*sdk.TextContent); ok {
			text = tc.Text
		}
	}
	if !res.IsError || text != notRunning {
		t.Fatalf("a call after the quit: %v %q", res.IsError, text)
	}
	if err := cs.Ping(ctx, nil); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, err := os.Stat(d.DaemonLog()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the shim started something: %v", err)
	}

	// The agent ends its session: the shim ends.
	cs.Close()
	shimIn.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shim: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the shim did not end with the session")
	}
}
