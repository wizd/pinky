package wadb

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// testManager is a manager in a fresh directory that uses the shared client
// key instead of making one.
func testManager(t *testing.T) (*Manager, *atomic.Int32) {
	t.Helper()
	cli, _ := keys(t)
	var n atomic.Int32
	m := NewManager(t.TempDir(), "pinky@test", func() { n.Add(1) })
	m.key = cli
	return m, &n
}

func ctx10(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestManagerOffAndUnpaired(t *testing.T) {
	m, changes := testManager(t)
	if s := m.State(); s.Status != StatusOff || s.On {
		t.Fatalf("new manager: %+v", s)
	}
	if _, err := m.Client(ctx10(t)); !errors.Is(err, ErrOff) {
		t.Fatalf("client while off: %v", err)
	}
	if err := m.SetOn(true); err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s.Status != StatusUnpaired || !s.On {
		t.Fatalf("on, not paired: %+v", s)
	}
	if _, err := m.Client(ctx10(t)); !errors.Is(err, ErrUnpaired) {
		t.Fatalf("client before pairing: %v", err)
	}
	if changes.Load() == 0 {
		t.Fatal("no change was reported")
	}
	// The setting outlives the manager.
	if !NewManager(m.dir, "x", nil).On() {
		t.Fatal("On was not saved")
	}
}

func TestManagerPairConnectAndOff(t *testing.T) {
	m, _ := testManager(t)
	cli, srv := keys(t)
	pairAddr, _ := fakePairServer(t, "135790", srv)
	if err := m.Pair(ctx10(t), portOf(t, pairAddr), "135790"); err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s.Status != StatusNoPort || !s.On {
		t.Fatalf("paired, no port: %+v", s)
	}
	if again := NewManager(m.dir, "x", nil); !again.cfg.Paired || again.cfg.GUID != "adb-FAKE-guid" {
		t.Fatalf("pairing not saved: %+v", again.cfg)
	}
	if _, err := m.Client(ctx10(t)); !errors.Is(err, ErrNoPort) {
		t.Fatalf("client without a port: %v", err)
	}

	addr := fakeAdbd(t, srv, cli, 4096)
	if err := m.SetPort(portOf(t, addr)); err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s.Status != StatusReady {
		t.Fatalf("with a port: %+v", s)
	}
	cl, err := m.Client(ctx10(t))
	if err != nil {
		t.Fatal(err)
	}
	if cl.Direct == nil {
		t.Fatal("client has no Direct")
	}
	if s := m.State(); s.Status != StatusConnected {
		t.Fatalf("after Client: %+v", s)
	}
	c := m.conn
	// The connection is reused, not dialled again (fakeAdbd takes one).
	if _, err := m.Client(ctx10(t)); err != nil || m.conn != c {
		t.Fatalf("second Client: %v", err)
	}

	// Off drops the shell at once.
	if err := m.SetOn(false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("turning off left the connection open")
	}
	if s := m.State(); s.Status != StatusOff {
		t.Fatalf("after off: %+v", s)
	}
	if _, err := m.Client(ctx10(t)); !errors.Is(err, ErrOff) {
		t.Fatalf("client after off: %v", err)
	}
}

func TestManagerRefusedAndForget(t *testing.T) {
	m, _ := testManager(t)
	_, srv := keys(t)
	m.cfg = config{On: true, Paired: true}
	// The phone forgot the key: it trusts only its own.
	addr := fakeAdbd(t, srv, srv, 4096)
	if err := m.SetPort(portOf(t, addr)); err != nil {
		t.Fatal(err)
	}
	_, err := m.Client(ctx10(t))
	if !errors.Is(err, ErrUnauthorized) || !errors.Is(err, ErrUnpaired) {
		t.Fatalf("client with a forgotten key: %v", err)
	}
	if s := m.State(); s.Status != StatusRefused || s.Error == "" {
		t.Fatalf("refused: %+v", s)
	}
	if err := m.Forget(); err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s.Status != StatusUnpaired || s.Error != "" {
		t.Fatalf("after Forget: %+v", s)
	}
	if m.key != nil {
		t.Fatal("Forget kept the key")
	}
}

func TestManagerNotesADroppedConnection(t *testing.T) {
	m, _ := testManager(t)
	cli, srv := keys(t)
	m.cfg = config{On: true, Paired: true}
	if err := m.SetPort(portOf(t, fakeAdbd(t, srv, cli, 4096))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Client(ctx10(t)); err != nil {
		t.Fatal(err)
	}
	gone := errors.New("wireless debugging went off")
	m.conn.fail(gone)
	deadline := time.Now().Add(5 * time.Second)
	for m.State().Status != StatusFailed {
		if time.Now().After(deadline) {
			t.Fatalf("drop not noted: %+v", m.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if s := m.State(); s.Error != gone.Error() {
		t.Fatalf("error %q", s.Error)
	}
	// Losing the port (the app saw the service go) clears the old error.
	if err := m.SetPort(0); err != nil {
		t.Fatal(err)
	}
	if s := m.State(); s.Status != StatusNoPort || s.Error != "" {
		t.Fatalf("no port: %+v", s)
	}
}
