package gateway

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
)

// quietProvider is a restartingProvider the person can allow to restart
// the device service without a password.
type quietProvider struct {
	*restartingProvider
	allowed atomic.Bool
	quiet   atomic.Int32 // restarts without a password
	allows  atomic.Int32
	failing atomic.Bool
}

func (p *quietProvider) QuietRestart() (bool, bool) { return true, p.allowed.Load() }

func (p *quietProvider) AllowQuietRestart(_ context.Context, on bool, reason string) error {
	p.allows.Add(1)
	p.reason.Store(reason)
	p.allowed.Store(on)
	return nil
}

func (p *quietProvider) RestartDeviceServiceQuietly(context.Context) error {
	if !p.allowed.Load() {
		return errors.New("not allowed")
	}
	p.quiet.Add(1)
	if p.failing.Load() {
		return errors.New("killall failed")
	}
	p.lost.Store(false)
	return nil
}

// lostGateway starts a gateway whose iPhone the device service lost on
// Wi-Fi: Pinky used it before, in an earlier run.
func lostGateway(t *testing.T, allowed bool) (*Gateway, *quietProvider) {
	t.Helper()
	dir := t.TempDir()
	iphone := demo.NewDevice("iphone", "DIYgod's iPhone", driver.IOS)
	first := &restartingProvider{airProvider: newAirProvider(iphone)}
	g, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{first}; o.DataDir = dir })
	waitFor(t, "the iPhone online", func() bool { v, _ := viewOf(g, "iphone"); return v.Status == DeviceIdle })
	g.Flush()
	g.Close()

	p := &quietProvider{restartingProvider: &restartingProvider{airProvider: newAirProvider(iphone)}}
	p.lost.Store(true)
	p.allowed.Store(allowed)
	g2, _ := newTestGateway(t, func(o *Options) { o.Providers = []driver.Provider{p}; o.DataDir = dir })
	g2.SetWiFi(true)
	waitFor(t, "the iPhone lost", func() bool { v, _ := viewOf(g2, "iphone"); return v.StatusLine == "Lost on Wi-Fi" })
	return g2, p
}

// TestQuietRestartOffByDefault: until the person allows restarts without
// a password, Pinky never restarts the device service by itself, and an
// agent is told that only a person at the computer can, and where the
// person can let Pinky do it.
func TestQuietRestartOffByDefault(t *testing.T) {
	g, p := lostGateway(t, false)
	if supported, allowed := g.QuietRestart(); !supported || allowed {
		t.Fatalf("QuietRestart() = %v, %v; want supported, not allowed", supported, allowed)
	}
	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Check the weather"})
	if res.Status != StatusNeedsSetup || res.Setup != driver.SetupDeviceService {
		t.Fatalf("connecting: %+v", res)
	}
	for _, want := range []string{"Only a person at this computer", "Restart device service without a password in Settings › Phones"} {
		if !strings.Contains(res.Message, want) {
			t.Errorf("the message lacks %q: %s", want, res.Message)
		}
	}
	g.Refresh(context.Background())
	if n := p.quiet.Load() + p.restarts.Load(); n != 0 {
		t.Fatalf("%d restarts without the person", n)
	}
}

// TestQuietRestartForAgent: once the person allowed it, a phone the device
// service lost comes back by itself for the agent that connects: Pinky
// restarts the service without a password, waits for the phone, and says
// so in the result. It does not restart again within quietRestartEvery,
// and tells the agent when it did.
func TestQuietRestartForAgent(t *testing.T) {
	g, p := lostGateway(t, false)
	// A phone nobody uses is not worth dropping every other one.
	g.Refresh(context.Background())
	if p.quiet.Load() != 0 {
		t.Fatal("restarted for a phone no agent uses")
	}
	if err := g.SetQuietRestart(context.Background(), true, "Allow it."); err != nil {
		t.Fatal(err)
	}
	if _, allowed := g.QuietRestart(); !allowed || p.allows.Load() != 1 || p.reason.Load() != "Allow it." {
		t.Fatalf("allowed %v, %d allows, reason %v", allowed, p.allows.Load(), p.reason.Load())
	}
	g.Refresh(context.Background())
	if p.quiet.Load() != 0 {
		t.Fatal("restarted for a phone no agent uses")
	}

	s := g.OpenSession(agent)
	defer s.Close()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Check the weather"})
	if res.IsError() || res.Status != StatusSettled {
		t.Fatalf("connecting: %+v", res)
	}
	if p.quiet.Load() != 1 || p.restarts.Load() != 0 {
		t.Fatalf("%d quiet restarts, %d with a password", p.quiet.Load(), p.restarts.Load())
	}
	if !strings.Contains(res.Message, "had lost DIYgod's iPhone on Wi-Fi, so Pinky restarted it without a password") {
		t.Errorf("the result does not say Pinky restarted the service: %s", res.Message)
	}

	// Lost again a minute later: no second restart so soon.
	p.lost.Store(true)
	waitFor(t, "the iPhone lost again", func() bool { v, _ := viewOf(g, "iphone"); return v.StatusLine == "Lost on Wi-Fi" })
	g.Refresh(context.Background())
	obs := s.Observe(context.Background(), ObserveRequest{})
	if p.quiet.Load() != 1 {
		t.Fatalf("restarted again within %v: %d restarts", quietRestartEvery, p.quiet.Load())
	}
	if obs.Status != StatusNeedsSetup || !strings.Contains(obs.Message, "Pinky restarted the device service without a password") ||
		!strings.Contains(obs.Message, "phone_release") {
		t.Fatalf("observing the lost phone: %+v", obs)
	}

	// Once quietRestartEvery passed, it restarts again, by itself, while
	// the task holds the phone.
	g.mu.Lock()
	g.serviceRestarts[driver.IOS] = time.Now().Add(-quietRestartEvery)
	g.mu.Unlock()
	g.Refresh(context.Background())
	if p.quiet.Load() != 2 {
		t.Fatalf("%d restarts, want 2", p.quiet.Load())
	}
	waitFor(t, "the iPhone back", func() bool {
		v, _ := viewOf(g, "iphone")
		return v.Status != DeviceNeedsSetup && v.StatusLine != "Lost on Wi-Fi"
	})
	obs = s.Observe(context.Background(), ObserveRequest{})
	if obs.IsError() || !strings.Contains(obs.Message, "Pinky restarted it without a password") {
		t.Fatalf("observing after the restart: %+v", obs)
	}
}

// TestQuietRestartWaitsForActions: Pinky does not restart the device
// service while a phone of the platform is in the middle of an action,
// since every one of them drops; a failed restart counts for the limit.
func TestQuietRestartWaitsForActions(t *testing.T) {
	g, p := lostGateway(t, true)
	s := g.OpenSession(agent)
	defer s.Close()

	// The connection restarts the service; make it lost again with
	// another phone of the platform busy.
	g.mu.Lock()
	other := &device{id: "ipad", provider: p, wake: make(chan struct{}), inflight: func() {}}
	g.devices["ipad"] = other
	g.mu.Unlock()
	res := s.Connect(context.Background(), ConnectRequest{Task: "Check the weather"})
	if p.quiet.Load() != 0 || res.Status != StatusNeedsSetup {
		t.Fatalf("restarted while another phone acted: %d restarts, %+v", p.quiet.Load(), res)
	}
	if !strings.Contains(res.Message, "Pinky does so by itself once no iPhone or iPad on this computer is in the middle of an action") {
		t.Errorf("the message: %s", res.Message)
	}

	g.mu.Lock()
	other.inflight = nil
	g.mu.Unlock()
	p.failing.Store(true)
	g.Refresh(context.Background())
	if p.quiet.Load() != 1 {
		t.Fatalf("%d restarts once the action ended, want 1", p.quiet.Load())
	}
	g.Refresh(context.Background())
	if p.quiet.Load() != 1 {
		t.Fatalf("a failed restart was tried again at once: %d restarts", p.quiet.Load())
	}
	g.mu.Lock()
	delete(g.devices, "ipad")
	g.mu.Unlock()
}

// TestManualRestartCountsForLimit: a restart the person asked for counts
// for the limit, so Pinky does not restart the service by itself right
// after it.
func TestManualRestartCountsForLimit(t *testing.T) {
	g, p := lostGateway(t, true)
	if err := g.RestartDeviceService(context.Background(), "iphone", "why"); err != nil {
		t.Fatal(err)
	}
	p.lost.Store(true)
	waitFor(t, "the iPhone lost again", func() bool { v, _ := viewOf(g, "iphone"); return v.StatusLine == "Lost on Wi-Fi" })
	s := g.OpenSession(agent)
	defer s.Close()
	if res := s.Connect(context.Background(), ConnectRequest{Task: "Check the weather"}); res.Status != StatusNeedsSetup {
		t.Fatalf("connecting: %+v", res)
	}
	if p.quiet.Load() != 0 || p.restarts.Load() != 1 {
		t.Fatalf("%d quiet restarts, %d asked for", p.quiet.Load(), p.restarts.Load())
	}
}
