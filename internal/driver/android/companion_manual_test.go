package android

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/companion/companiontest"
)

// ignoringRunner takes settings changes without an error and keeps nothing
// of them, as vivo's OriginOS 6 does with the accessibility setting
// (GitHub issue #94).
type ignoringRunner struct {
	*quietPhone
	tries atomic.Int32 // settings put and delete it ignored
}

func (r *ignoringRunner) Shell(ctx context.Context, cmd string) (string, error) {
	if strings.HasPrefix(cmd, "settings put") || strings.HasPrefix(cmd, "settings delete") {
		r.tries.Add(1)
		return "", nil
	}
	return r.quietPhone.Shell(ctx, cmd)
}

// fast fails the test when what took longer than a moment: the service's
// own wait is 10 s.
func fast(t *testing.T, what string, start time.Time) {
	t.Helper()
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("%s took %v: it waited for a service that cannot come on", what, took)
	}
}

// TestCompanionManual: on a phone that ignores Pinky's change to its
// accessibility settings, the driver says so at once and remembers it for
// the phone: its state, its checkup item, Turn on and the agent's
// background display all say that only the person can turn the service
// on, until a change of Pinky's is kept.
func TestCompanionManual(t *testing.T) {
	p := newQuietPhone(t)
	d, logs := quietDevice(t, p, "")
	r := &ignoringRunner{quietPhone: p}
	d.compRun = r
	ctx := context.Background()
	if st, _ := d.CompanionState(ctx); st != driver.CompanionReady {
		t.Fatalf("state before any try = %q, want ready", st)
	}

	// A task starts: the service does not come on, without the 10 s wait.
	start := time.Now()
	d.AgentWorking(true)
	if c := d.companionClient(ctx); c != nil {
		t.Fatal("used a companion whose service could not come on")
	}
	fast(t, "the task's first read", start)
	waitFor(t, "the warm-up's try", func() bool { return d.companionManual() })
	if r.tries.Load() == 0 {
		t.Fatal("the driver did not try to turn the service on")
	}
	if st, err := d.CompanionState(ctx); err != nil || st != driver.CompanionManual {
		t.Errorf("state = %q, %v; want manual", st, err)
	}
	ck := d.checkCompanion(ctx)
	if ck.Status != driver.CheckFail || ck.Fix != driver.FixTurnOnCompanionByHand || !strings.Contains(ck.Detail, "did not keep Pinky's entry") {
		t.Errorf("checkup item = %+v", ck)
	}

	// The agent's background display says why it cannot be read, in words
	// the agent can pass on.
	b := &Background{d: d, display: 2, closed: make(chan struct{})}
	start = time.Now()
	_, err := b.Observe(ctx, driver.ObserveOptions{})
	fast(t, "reading the background display", start)
	if !errors.Is(err, driver.ErrUnsupported) || !strings.Contains(err.Error(), "does not let Pinky turn on") ||
		!strings.Contains(err.Error(), "accessibility settings by hand") {
		t.Errorf("background display: %v", err)
	}

	// Turn on (SetUpCompanion) says the same.
	start = time.Now()
	_, err = d.SetUpCompanion(ctx, false)
	fast(t, "Turn on", start)
	var se *driver.CompanionStartError
	if !errors.As(err, &se) || se.Reason != driver.CompanionStartSettingIgnored {
		t.Errorf("SetUpCompanion = %v, want the change the phone did not keep", err)
	}
	if n := countLogs(logs(), "did not keep Pinky's change"); n != 1 {
		t.Errorf("logged the ignored change %d times, want once: %q", n, logs())
	}

	// The person turns the service on in the phone's settings: Pinky uses
	// it, and the phone stays one where only the person can.
	p.mu.Lock()
	p.setting, p.unset = p.app.Component(), false
	p.mu.Unlock()
	if st, _ := d.CompanionState(ctx); st != driver.CompanionManualOn {
		t.Errorf("state with the service on by hand = %q, want manual_on", st)
	}
	if c := d.companionClientFor(ctx, forBackground); c == nil {
		t.Fatal("did not use the service the person turned on")
	}
	d.companionOff("the task ended")
	// It goes off again (the app's own backstop turns it off).
	p.mu.Lock()
	p.setting, p.unset = "", true
	p.mu.Unlock()
	p.dropAll()
	if st, _ := d.CompanionState(ctx); st != driver.CompanionManual {
		t.Errorf("state once it is off again = %q, want manual", st)
	}

	// Another reason keeps it off: the state says that one.
	d.comp.mu.Lock()
	d.comp.failed = &driver.CompanionStartError{Reason: driver.CompanionStartUIAutomation, Detail: "x"}
	d.comp.mu.Unlock()
	if st, _ := d.CompanionState(ctx); st != driver.CompanionOff {
		t.Errorf("state after another failure = %q, want off", st)
	}

	// The phone keeps Pinky's change again: Turn on works, and the phone is
	// one like any other.
	d.compRun = p
	d.comp.client = nil
	d.AgentWorking(false)
	if _, err := d.SetUpCompanion(ctx, false); err != nil {
		t.Fatalf("SetUpCompanion once the phone keeps the change: %v", err)
	}
	if d.companionManual() {
		t.Error("still manual after a change the phone kept")
	}
	waitFor(t, "the service is off after Turn on", func() bool { return !d.companionInUse() && !p.listed() })
	if st, _ := d.CompanionState(ctx); st != driver.CompanionReady {
		t.Errorf("state = %q, want ready", st)
	}
}

func countLogs(logs []string, s string) int {
	n := 0
	for _, l := range logs {
		if strings.Contains(l, s) {
			n++
		}
	}
	return n
}

// idleConfig records what the app was told about its idle backstop.
type idleConfig struct {
	mu   sync.Mutex
	sets []float64 // idle_disable_ms of each config request
}

func (c *idleConfig) watch(p *quietPhone) {
	p.mu.Lock()
	p.reply = func(req map[string]any) (any, *companion.Error) {
		if req["cmd"] == "config" {
			if v, ok := req["idle_disable_ms"].(float64); ok {
				c.mu.Lock()
				c.sets = append(c.sets, v)
				c.mu.Unlock()
			}
		}
		return nil, nil
	}
	p.mu.Unlock()
}

// keptOn reports whether the app was told to keep its service on, and
// forgets it.
func (c *idleConfig) keptOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	on := len(c.sets) > 0 && c.sets[len(c.sets)-1] == 0
	c.sets = nil
	return on
}

// TestCompanionManualStaysOn: on a phone that ignores Pinky's change to its
// accessibility settings (GitHub issue #94), a service the person turned on
// by hand stays on after a task, after an update and across a restart of
// Pinky, and the app's idle backstop is stopped, until a change of Pinky's
// is kept again; the phone is remembered in the driver's file.
func TestCompanionManualStaysOn(t *testing.T) {
	release := []byte("Pinky's release certificate")
	apk := companiontest.APK(t, companion.Release.Package, "2026.1108.9", 480, release)
	file := filepath.Join(t.TempDir(), "companion-manual.json")
	p := newQuietPhone(t)
	p.signer = companion.SignerName(release)
	var idle idleConfig
	idle.watch(p)
	r := &ignoringRunner{quietPhone: p}
	d, logs := quietDevice(t, p, "")
	d.compRun = r
	d.useManuals(&manualStore{path: file, logf: t.Logf})
	ctx := context.Background()

	// A task finds that the phone ignores Pinky's change.
	d.AgentWorking(false)
	d.AgentWorking(true)
	waitFor(t, "the warm-up's try", func() bool { return d.companionManual() })
	if !(&manualStore{path: file, logf: t.Logf}).has("phone") {
		t.Fatal("the phone is not kept in the file")
	}
	d.AgentWorking(false)

	// The person turns the service on in the phone's settings; the next
	// task uses it.
	p.mu.Lock()
	p.setting, p.unset = p.app.Component(), false
	p.mu.Unlock()
	d.comp.mu.Lock()
	d.comp.retryAt = time.Time{}
	d.comp.mu.Unlock()
	d.AgentWorking(true)
	waitFor(t, "the service is on for the task", func() bool { return d.companionInUse() })
	if !idle.keptOn() {
		t.Error("the app's idle backstop was not stopped")
	}
	if st, err := d.CompanionState(ctx); err != nil || st != driver.CompanionManualOn {
		t.Errorf("state during the task = %q, %v; want manual_on", st, err)
	}

	// The task ends: the driver lets go of it, and it stays on.
	tries := r.tries.Load()
	d.AgentWorking(false)
	waitFor(t, "the driver let go of the service", func() bool { return !d.companionInUse() })
	time.Sleep(100 * time.Millisecond)
	if !p.listed() || r.tries.Load() != tries {
		t.Errorf("after the task: listed %v, %d tries to change the setting", p.listed(), r.tries.Load()-tries)
	}
	if st, err := d.CompanionState(ctx); err != nil || st != driver.CompanionManualOn {
		t.Errorf("state after the task = %q, %v; want manual_on", st, err)
	}
	if ck := d.checkCompanion(ctx); ck.Status != driver.CheckPass || !strings.Contains(ck.Detail, "stays on until they turn it off") {
		t.Errorf("checkup item = %+v", ck)
	}
	idle.keptOn()

	// An update in place leaves it on.
	t.Setenv("PINKY_COMPANION_APK", apk)
	res, err := d.UpdateCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Updated || res.Replaced || !res.ServiceOn {
		t.Errorf("update = %+v", res)
	}
	if !p.listed() || r.tries.Load() != tries {
		t.Errorf("after the update: listed %v, %d tries to change the setting", p.listed(), r.tries.Load()-tries)
	}
	if !idle.keptOn() {
		t.Error("the updated app's idle backstop was not stopped")
	}
	if n := countLogs(logs(), "leaving on the updated companion's service"); n != 1 {
		t.Errorf("logged leaving it on after the update %d times: %q", n, logs())
	}

	// Pinky restarts: the new device knows the phone from the file before
	// anything else, and leaves the service on.
	d.closeCompanion()
	d2, logs2 := quietDevice(t, p, apk)
	d2.compRun = r
	d2.useManuals(&manualStore{path: file, logf: t.Logf})
	d2.AgentWorking(false)
	waitFor(t, "the restarted driver looked at the service", func() bool {
		return countLogs(logs2(), "leaving on the companion's service") == 1
	})
	if !p.listed() || r.tries.Load() != tries {
		t.Errorf("after the restart: listed %v, %d tries to change the setting", p.listed(), r.tries.Load()-tries)
	}
	if !idle.keptOn() {
		t.Error("the idle backstop was not stopped after the restart")
	}
	if st, _ := d2.CompanionState(ctx); st != driver.CompanionManualOn {
		t.Errorf("state after the restart = %q, want manual_on", st)
	}

	// The person turns it off, and the phone keeps Pinky's change again
	// (an update of the phone's system): the phone is one like any other,
	// in memory and in the file.
	p.mu.Lock()
	p.setting, p.unset = "", true
	p.mu.Unlock()
	p.dropAll()
	d2.compRun = p
	d2.comp.client = nil
	d2.AgentWorking(true)
	waitFor(t, "the service is on for the task", func() bool { return d2.companionInUse() })
	if d2.companionManualPhone() || (&manualStore{path: file, logf: t.Logf}).has("phone") {
		t.Error("still manual after a change the phone kept")
	}
	d2.AgentWorking(false)
	waitFor(t, "the service is off after the task", func() bool { return !d2.companionInUse() && !p.listed() })
	if st, _ := d2.CompanionState(ctx); st != driver.CompanionReady {
		t.Errorf("state = %q, want ready", st)
	}
}

// TestCompanionNotManualGoesOff: on other phones, a service left on still
// goes off, and the file knows nothing of them.
func TestCompanionNotManualGoesOff(t *testing.T) {
	file := filepath.Join(t.TempDir(), "companion-manual.json")
	if err := os.WriteFile(file, []byte(`{"phones": ["another-phone"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newQuietPhone(t)
	p.setting, p.unset = p.app.Component(), false
	var idle idleConfig
	idle.watch(p)
	d, _ := quietDevice(t, p, "")
	d.useManuals(&manualStore{path: file, logf: t.Logf})
	d.AgentWorking(false)
	waitFor(t, "the service left on is off", func() bool { return !p.listed() })
	if idle.keptOn() {
		t.Error("stopped the idle backstop of a phone Pinky can turn the service off on")
	}
	if st, _ := d.CompanionState(context.Background()); st != driver.CompanionReady {
		t.Errorf("state = %q, want ready", st)
	}
	s := &manualStore{path: file, logf: t.Logf}
	if s.has("phone") || !s.has("another-phone") {
		t.Errorf("the file: phone %v, another-phone %v", s.has("phone"), s.has("another-phone"))
	}
}
