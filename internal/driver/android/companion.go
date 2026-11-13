package android

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// The companion app (android/companion) gives a 30 ms element tree, events,
// ACTION_SET_TEXT input and an accessibility settle signal. Its
// accessibility service is on only while an agent works on the phone: the
// phone's apps can see an accessibility service that is on, and banking
// apps refuse to sign in next to one that is not from an app store. The
// gateway says when an agent works (driver.WorkWatcher): the driver turns
// the service on as a task starts, so that the task's first read finds it
// ready, and off a few seconds after the task ends or the person takes
// over, or a minute after the agent's last call. While no agent works,
// reads do without it (uiautomator), except for unlocking the phone from
// the computer, and nothing turns it on. Callers that never call
// AgentWorking (androidctl, tests) turn it on at their first read and off
// after a minute without calls. Turning it off restores the setting
// exactly; a service someone else turned on goes off too, unless another
// client still uses it.
//
// A phone that ignores the driver's change to its accessibility settings
// (manual; vivo's OriginOS 6, GitHub issue #94) lets only the person turn
// the service on. There the driver leaves on a service the person turned
// on, until they turn it off on the phone, and stops the app's own idle
// backstop: the owner decided on 2026-11-12 that it stays on, rather than
// have the person turn it on again before every task.
//
// Pinky on the phone (on-phone mode) runs tasks of its own. While an agent
// works, the driver claims the phone on its event subscription (held), so
// that the phone's own tasks wait and its pill says a computer is using
// it; the agent's calls fail with driver.BusyError while Pinky on the phone
// holds it, and with driver.ErrStoppedOnPhone once the person pressed Stop
// there. A service that on-phone mode keeps on stays on. Apps from before
// claims (2026-11-12) work as before.
//
// Without the app, or when it is too old or does not answer, the driver
// falls back to uiautomator and the keyboard. The two never run together:
// a uiautomator dump unbinds every accessibility service.

const (
	// companionIdle is how long the service stays on without driver calls
	// while an agent works.
	companionIdle = 60 * time.Second
	// companionWarm bounds turning the service on as a task starts.
	companionWarm = 20 * time.Second
	// companionMissingRetry is how long to wait before looking for an app
	// that was not installed.
	companionMissingRetry = 5 * time.Minute
	// companionFailRetry is how long to wait before trying an app that
	// failed or did not answer.
	companionFailRetry = 2 * time.Minute
	// maxPendingEvents caps the toasts and notifications kept between two
	// observations.
	maxPendingEvents = 20
)

// companionRest is how long the service stays on without driver calls once
// no agent works: the task ended, or the person took over. The reads right
// after a task (its recording's last frame) still find it on. A variable
// for tests.
var companionRest = 5 * time.Second

// companionLink is a device's connection to its companion app.
type companionLink struct {
	mu      sync.Mutex // serializes turning the service on and off
	client  *companion.Client
	on      bool      // the service is on for the driver and the subscription runs
	checked bool      // installed was looked up
	have    bool      // the app is installed
	version string    // its version name
	retryAt time.Time // do not try again before this
	reason  string    // why the companion is not used, logged once
	// failed is why the service did not come on the last time the driver
	// tried, for CompanionState; nil once it came on.
	failed error
	// manual: the phone took the driver's last change to
	// enabled_accessibility_services without an error and did not keep it
	// (vivo's OriginOS 6, issue #94), so only the person can turn the
	// service on, in the phone's accessibility settings (CompanionManual).
	// It stays set when the service comes on without a change (the person
	// turned it on), and goes when a change of the driver's is kept. Kept
	// across restarts in Device.manuals (setManualLocked). While it is set,
	// the driver leaves a service the person turned on on.
	manual  bool
	idle    *time.Timer
	lastUse atomic.Int64 // Unix nanoseconds of the last driver call
	sub     *companion.Subscription
	stop    context.CancelFunc // ends the subscription's context
	// claimed: the subscription holds the phone for the agent
	// (FeatureClaim), so Pinky on the phone does not start a task meanwhile.
	// busy is who held it at the last try, at busyAt.
	claimed bool
	busy    *driver.BusyError
	busyAt  time.Time

	// gated: the gateway reports when an agent works (AgentWorking), and
	// the service is turned on only while one does; working: one does.
	gated, working atomic.Bool

	info *companion.Info // the app's ping while it is on

	evMu   sync.Mutex
	lastAt map[int]time.Time // arrival of the last window or content event, by display (-1: unknown)
	// windowAt is the arrival of the last window event (a new activity,
	// dialog or pane), by display (-1: unknown or dropped events).
	windowAt map[int]time.Time
	events   []screen.Event // toasts and notifications since the last Observe
	live     bool           // the subscription is delivering
	// stopped: the person ended the agent's claim on the phone (Stop on
	// the pill), so the agent's task must end; until the next one starts.
	// lost: the claim ended that way, and claimed is out of date.
	stopped, lost bool
}

// companionUse is what a caller wants the companion for.
type companionUse int

const (
	// forAgent: an agent's read or input. While no agent works, the
	// service stays off and the caller does without it.
	forAgent companionUse = iota
	// forBackground: a background display, which has no uiautomator to
	// fall back on; it exists only for a task. An installed app is tried
	// again at once instead of after the wait that follows a failure.
	forBackground
	// forUnlock: the person unlocking the phone from the computer, on its
	// lock screen, which reads the PIN pad several times: the service
	// comes on for it and goes off a few seconds after.
	forUnlock
)

// companionClient returns the companion client with its service on, or nil
// when the driver should do without it. It turns the service on at the
// first call after the device opened or went idle, while an agent works.
func (d *Device) companionClient(ctx context.Context) *companion.Client {
	return d.companionClientFor(ctx, forAgent)
}

// companionClientFor is companionClient for a use.
func (d *Device) companionClientFor(ctx context.Context, use companionUse) *companion.Client {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.on {
		k.evMu.Lock()
		live := k.live
		k.evMu.Unlock()
		if live {
			return k.client
		}
		// The subscription ended: the service stopped or was turned off.
		d.companionOffLocked("its event stream ended")
	}
	if use == forAgent && k.gated.Load() && !k.working.Load() {
		return nil // no agent works on the phone: the service stays off
	}
	if time.Now().Before(k.retryAt) && !(use == forBackground && k.checked && k.have) {
		return nil
	}
	if err := d.companionOnLocked(ctx); err != nil {
		wait := companionFailRetry
		if errors.Is(err, errCompanionMissing) {
			wait = companionMissingRetry
		}
		d.companionUnusableLocked(wait, err.Error())
		return nil
	}
	return k.client
}

// errCompanionMissing: the app is not installed.
var errCompanionMissing = errors.New("its app is not installed")

// companionOnLocked turns the service on, checks that it answers and
// subscribes to its events. A failure to turn it on is kept for
// CompanionState.
func (d *Device) companionOnLocked(ctx context.Context) error {
	k := &d.comp
	if k.client == nil {
		k.client = d.newCompanion()
	}
	if !k.checked {
		version, ok, err := k.client.Installed(ctx)
		if err != nil {
			return fmt.Errorf("cannot tell whether it is installed: %w", err)
		}
		k.checked, k.have, k.version = true, ok, version
	}
	if !k.have {
		k.checked = false // look again after the wait
		return fmt.Errorf("%s: %w", k.client.App().Package, errCompanionMissing)
	}
	start := time.Now()
	if err := k.client.Enable(ctx); err != nil {
		if ctx.Err() == nil {
			k.failed = err
		}
		if settingIgnored(err) {
			if !k.manual {
				d.cfg.logf("android: %s: the phone did not keep Pinky's change to its accessibility settings; only the person can turn the companion's service on", d.info.ID)
			}
			d.setManualLocked(true)
		}
		if se := (*driver.CompanionStartError)(nil); errors.As(err, &se) {
			return err // it says so, with what the phone showed
		}
		return fmt.Errorf("its service did not start: %w", err)
	}
	if k.client.EnabledForTask() {
		d.setManualLocked(false) // the phone kept the driver's change this time
	}
	info, err := k.client.Ping(ctx)
	if err == nil && info.Protocol != companion.ProtocolVersion {
		err = fmt.Errorf("it speaks protocol %d, the driver %d", info.Protocol, companion.ProtocolVersion)
	}
	if err == nil {
		err = d.subscribeLocked(info)
	}
	if err != nil {
		d.disableLocked()
		if ctx.Err() == nil {
			k.failed = err
		}
		return fmt.Errorf("version %s: %w", k.version, err)
	}
	if k.manual {
		// The person turned it on, on a phone where only they can: it
		// stays on after the task, also once Pinky is gone.
		d.keepOn(ctx, k.client)
	}
	k.on = true
	k.info = info
	k.reason = ""
	k.failed = nil
	d.used()
	if k.idle == nil {
		k.idle = time.AfterFunc(d.companionLimit(), d.companionIdleCheck)
	} else {
		k.idle.Reset(d.companionLimit())
	}
	d.cfg.logf("android: %s: companion %s on in %v", d.info.ID, info.Version, time.Since(start).Round(time.Millisecond))
	return nil
}

// newCompanion returns a client for the build of the companion this Pinky
// carries.
func (d *Device) newCompanion() *companion.Client {
	return companion.NewFor(d.companionRunner(), companionApp())
}

// companionRunner runs the companion's adb commands.
func (d *Device) companionRunner() companion.Runner {
	if d.compRun != nil {
		return d.compRun
	}
	return adbRunner{c: d.client, serial: d.info.ID}
}

// used notes a driver call; a minute without one turns the companion off.
func (d *Device) used() { d.comp.lastUse.Store(time.Now().UnixNano()) }

// companionLimit is how long the service stays on without driver calls.
func (d *Device) companionLimit() time.Duration {
	if k := &d.comp; k.gated.Load() && !k.working.Load() {
		return companionRest
	}
	return companionIdle
}

// companionIdleCheck runs when the idle timer fires: it turns the companion
// off after a minute without driver calls (a few seconds once no agent
// works), or waits for the rest of it.
func (d *Device) companionIdleCheck() {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.on {
		return
	}
	limit := d.companionLimit()
	if since := time.Since(time.Unix(0, k.lastUse.Load())); since < limit {
		k.idle.Reset(limit - since)
		return
	}
	why := "a minute without driver calls"
	if limit == companionRest {
		why = "no agent works on the phone"
	}
	d.companionOffLocked(why)
}

// AgentWorking implements driver.WorkWatcher. When an agent starts working
// it turns the service on in the background, so that the task's first
// read finds it ready; when the agent stops, it turns it off a few seconds
// after the last call. The first call also turns off a service that was
// left on with nobody using it.
func (d *Device) AgentWorking(on bool) {
	k := &d.comp
	first := !k.gated.Swap(true)
	if k.working.Swap(on) == on && !first {
		return
	}
	if on {
		// A new stretch of work: a stop on the phone belonged to the last.
		k.evMu.Lock()
		k.stopped = false
		k.evMu.Unlock()
		go d.warmCompanion()
		// The task's first read may need OCR: load its models meanwhile.
		ocrWarm()
	} else {
		go d.restCompanion(first)
	}
}

// warmCompanion turns the service on as an agent starts working.
func (d *Device) warmCompanion() {
	ctx, cancel := context.WithTimeout(context.Background(), companionWarm)
	defer cancel()
	if d.comp.working.Load() {
		_ = d.held(ctx) // a busy phone is the agent's first call's to report
	}
}

// restCompanion shortens the service's time without calls once no agent
// works and, at the first call, turns off a service left on before.
func (d *Device) restCompanion(first bool) {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.working.Load() {
		return
	}
	switch {
	case k.on:
		d.releaseLocked()
		if k.idle != nil {
			k.idle.Reset(companionRest) // measured from the last call
		}
	case first:
		d.quietLocked()
	}
}

// claimRetry is how long a busy phone is reported busy before the driver
// asks the app again. A variable for tests.
var claimRetry = 2 * time.Second

// acting notes a driver call for an agent and checks that the agent may act
// on the phone (held).
func (d *Device) acting(ctx context.Context) error {
	d.used()
	return d.held(ctx)
}

// held makes sure the phone is the agent's while one works on it: it
// claims the phone on the event subscription, so that Pinky on the phone
// waits and shows that a computer is using it. It returns a
// *driver.BusyError while Pinky on the phone (or another computer) holds
// it, and driver.ErrStoppedOnPhone once the person stopped the task there.
// Without an app that takes claims, or while no agent works (the person
// unlocking the phone from the computer), it lets everything through, as
// before claims.
func (d *Device) held(ctx context.Context) error {
	k := &d.comp
	if k.gated.Load() && !k.working.Load() {
		return nil
	}
	if k.stoppedOnPhone() {
		return driver.ErrStoppedOnPhone
	}
	if d.companionClient(ctx) == nil {
		return nil // without the app: as before
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return d.claimLocked(ctx)
}

// stoppedOnPhone reports whether the person stopped the agent's task on
// the phone.
func (k *companionLink) stoppedOnPhone() bool {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	return k.stopped
}

// claimLocked claims the phone on the subscription unless it holds it
// already. A busy phone is reported for claimRetry before the app is asked
// again. When the claim gets no answer the driver goes on without it: the
// claim is cooperative, and the subscription's own end frees the phone.
func (d *Device) claimLocked(ctx context.Context) error {
	k := &d.comp
	k.evMu.Lock()
	if k.lost {
		k.claimed, k.lost = false, false
	}
	k.evMu.Unlock()
	if !k.on || k.claimed || k.info == nil || !k.info.Has(companion.FeatureClaim) {
		return nil
	}
	if k.busy != nil && time.Since(k.busyAt) < claimRetry {
		return k.busy
	}
	k.evMu.Lock()
	sub := k.sub
	k.evMu.Unlock()
	if sub == nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := sub.Claim(cctx, companion.ByComputer, "")
	var ce *companion.Error
	switch {
	case err == nil:
		if k.busy != nil {
			d.cfg.logf("android: %s: the phone is free again; the agent holds it", d.info.ID)
		}
		k.claimed, k.busy = true, nil
		return nil
	case errors.As(err, &ce) && ce.Code == companion.CodeBusy:
		if k.busy == nil {
			d.cfg.logf("android: %s: the phone is busy: %s", d.info.ID, ce.Message)
		}
		k.busy, k.busyAt = &driver.BusyError{By: ce.By, Task: ce.Task}, time.Now()
		return k.busy
	default:
		if ctx.Err() == nil {
			d.cfg.logf("android: %s: claiming the phone: %v; going on without a claim", d.info.ID, err)
		}
		return nil
	}
}

// releaseLocked gives the phone back as the agent stops working, so that
// Pinky on the phone may start a task before the service rests.
func (d *Device) releaseLocked() {
	k := &d.comp
	k.busy = nil
	if !k.claimed {
		return
	}
	k.claimed = false
	k.evMu.Lock()
	sub := k.sub
	k.evMu.Unlock()
	if sub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := sub.Release(ctx); err != nil {
		d.cfg.logf("android: %s: giving the phone back: %v", d.info.ID, err)
	}
}

// quietLocked turns the service off when it is on although no agent works
// and no client uses it: Pinky until 2026-11-08 kept it on for good once
// installed. One that another client uses, or that the phone does not let
// the computer turn off, stays on, and so does one the person turned on on
// a phone where only they can (manual).
func (d *Device) quietLocked() {
	k := &d.comp
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if time.Now().Before(k.retryAt) {
		return
	}
	if k.client == nil {
		k.client = d.newCompanion()
	}
	listed, err := k.client.Enabled(ctx)
	if err != nil || !listed {
		return
	}
	if why := companionKept(ctx, k.client); why != "" {
		d.cfg.logf("android: %s: %s; leaving its service on", d.info.ID, why)
		return
	}
	if k.manual {
		// Only the person can turn it on here, and they did: it stays on.
		d.keepOn(ctx, k.client)
		k.client.Close()
		d.cfg.logf("android: %s: leaving on the companion's service, which the person turned on on a phone where only they can", d.info.ID)
		return
	}
	if err := k.client.ForceDisable(ctx); err != nil {
		d.cfg.logf("android: %s: turning off the companion's service, which was on with no agent working: %v", d.info.ID, err)
		return
	}
	d.cfg.logf("android: %s: turned off the companion's service, which was on with no agent working; Pinky turns it on only while an agent works", d.info.ID)
}

// closeCompanion turns the companion off for good, when the device closes.
func (d *Device) closeCompanion() {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.on {
		d.companionOffLocked("the device closed")
	} else if k.client != nil {
		k.client.Close()
	}
	k.retryAt = time.Now().Add(100 * 365 * 24 * time.Hour)
}

// subscribeLocked starts the event subscription, which outlives the call
// that started it. With an app that takes claims it asks for claim events
// too: the app then counts the subscription as a claim only once the
// driver claims on it (claimLocked), and says when the person stops it.
func (d *Device) subscribeLocked(info *companion.Info) error {
	k := &d.comp
	ctx, stop := context.WithCancel(context.Background())
	kinds := []companion.EventKind{companion.EventWindow, companion.EventWindows, companion.EventContent,
		companion.EventToast, companion.EventNotification}
	if info.Has(companion.FeatureClaim) {
		kinds = append(kinds, companion.EventClaim)
	}
	sub, err := k.client.Events(ctx, kinds...)
	if err != nil {
		stop()
		return fmt.Errorf("subscribe to events: %w", err)
	}
	k.stop = stop
	k.evMu.Lock()
	k.sub = sub // readCompanionEvents reads it under evMu
	k.live = true
	k.lost = false
	k.events = nil
	k.lastAt = map[int]time.Time{}
	k.windowAt = map[int]time.Time{}
	k.evMu.Unlock()
	go d.readCompanionEvents(sub)
	return nil
}

func (d *Device) readCompanionEvents(sub *companion.Subscription) {
	k := &d.comp
	for ev := range sub.C() {
		now := time.Now()
		k.evMu.Lock()
		switch ev.Kind {
		case companion.EventWindow, companion.EventWindows, companion.EventContent:
			k.lastAt[ev.Display] = now
		case companion.EventDropped:
			k.lastAt[-1] = now
			k.windowAt[-1] = now
		}
		if ev.Kind == companion.EventWindow {
			k.windowAt[ev.Display] = now
		}
		if ev.Kind == companion.EventClaim && ev.Stopped {
			k.lost = true
			if !k.stopped {
				k.stopped = true
				d.cfg.logf("android: %s: the person stopped the agent's task on the phone", d.info.ID)
			}
		}
		if se, ok := ev.ScreenEvent(); ok {
			k.events = append(k.events, se)
			if len(k.events) > maxPendingEvents {
				k.events = k.events[len(k.events)-maxPendingEvents:]
			}
		}
		k.evMu.Unlock()
	}
	k.evMu.Lock()
	if k.sub == sub {
		k.live = false
	}
	k.evMu.Unlock()
}

// takeEvents returns the toasts and notifications since the last call.
func (k *companionLink) takeEvents() []screen.Event {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	ev := k.events
	k.events = nil
	return ev
}

// lastChange is the arrival of the last window or content event on a
// display, or of one whose display is not known; ok is false when no
// subscription is delivering. Apps from before round 3 send no display,
// which reads as 0, the main display.
func (k *companionLink) lastChange(display int) (time.Time, bool) {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	last := k.lastAt[display]
	if u := k.lastAt[-1]; u.After(last) {
		last = u
	}
	return last, k.live
}

// windowSince reports whether a window event (another activity, a dialog)
// arrived on a display, or one whose display is not known, after t; ok is
// false when no subscription is delivering.
func (k *companionLink) windowSince(display int, t time.Time) (changed, ok bool) {
	k.evMu.Lock()
	defer k.evMu.Unlock()
	if !k.live {
		return false, false
	}
	return k.windowAt[display].After(t) || k.windowAt[-1].After(t), true
}

// displays reports whether the app on the phone takes displays other than
// the main one for set_text (Android 11 and later, apps from round 3).
func (k *companionLink) displays() bool {
	return k.has(companion.FeatureDisplays)
}

// has reports whether the app on the phone is on and has a feature.
func (k *companionLink) has(feature string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.on && k.info != nil && k.info.Has(feature)
}

// settleCond is the companion's settle condition for a display, or false
// when no subscription is delivering.
func (d *Device) settleCond(display int) (quietCond, bool) {
	if _, live := d.comp.lastChange(display); !live {
		return quietCond{}, false
	}
	return quietCond{
		last: func() time.Time {
			last, live := d.comp.lastChange(display)
			if !live {
				return time.Time{} // the subscription ended: video alone
			}
			return last
		},
		quiet: companion.DefaultQuiet,
	}, true
}

// companionUnusableLocked records why the companion is not used and when
// to try again, logging each new reason once.
func (d *Device) companionUnusableLocked(wait time.Duration, reason string) {
	k := &d.comp
	k.retryAt = time.Now().Add(wait)
	if reason != k.reason {
		k.reason = reason
		d.cfg.logf("android: %s: not using the companion app: %s; using uiautomator", d.info.ID, reason)
	}
}

// companionFailed turns the companion off after a call failed, so that
// uiautomator can run, and waits before trying it again.
func (d *Device) companionFailed(err error) {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.on {
		d.companionOffLocked(fmt.Sprintf("a request failed: %v", err))
	}
	d.companionUnusableLocked(companionFailRetry, fmt.Sprintf("a request failed: %v", err))
}

// companionOff turns the companion off, if it is on.
func (d *Device) companionOff(why string) {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.on {
		d.companionOffLocked(why)
	}
}

func (d *Device) companionOffLocked(why string) {
	k := &d.comp
	d.disableLocked()
	k.on = false
	k.info = nil
	d.cfg.logf("android: %s: companion off: %s", d.info.ID, why)
}

// disableLocked ends the subscription and turns the service off: exactly
// as Enable found the setting when the driver turned it on, or by removing
// only Pinky's entry when it was on already (an older Pinky kept it on for
// good, or another client turned it on for its reads). One that another
// client still uses stays on: several drivers (two Pinky processes, Pinky
// and androidctl) may share it, and the last to leave turns it off. On a
// phone where only the person can turn it on (manual), one they turned on
// stays on too.
func (d *Device) disableLocked() {
	k := &d.comp
	if k.idle != nil {
		k.idle.Stop()
	}
	if k.sub != nil {
		k.sub.Close()
	}
	if k.stop != nil {
		k.stop()
		k.stop = nil
	}
	k.evMu.Lock()
	k.sub = nil // readCompanionEvents reads it under evMu
	k.live = false
	k.evMu.Unlock()
	k.claimed, k.busy = false, nil // the claim ended with the subscription
	if k.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var err error
	switch why := companionKept(ctx, k.client); {
	case why != "":
		d.cfg.logf("android: %s: %s; leaving its service on", d.info.ID, why)
	case k.client.EnabledForTask():
		err = k.client.Disable(ctx)
	case k.manual:
		d.cfg.logf("android: %s: leaving on the companion's service, which the person turned on on a phone where only they can", d.info.ID)
	default:
		err = k.client.ForceDisable(ctx)
	}
	if err != nil {
		d.cfg.logf("android: %s: turning the companion off: %v", d.info.ID, err)
	}
	k.client.Close()
}

// companionKept says why the service stays on although the driver is done
// with it, or "": on-phone mode is on (Pinky on the phone keeps it on, and
// turning it off would turn that mode off too), or another client uses it.
func companionKept(ctx context.Context, c *companion.Client) string {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	info, err := c.Ping(pctx)
	cancel()
	switch {
	case err != nil:
		return ""
	case info.OnPhone:
		return "Pinky on the phone keeps the companion on (on-phone mode)"
	case companionPeers(ctx, c):
		return "another client is using the companion"
	}
	return ""
}

// companionPeers asks the app whether clients other than this one's
// request connection are connected. The event subscription was just
// closed, and the app may count it for a moment, so it asks a few times.
// When the app does not answer, there is nobody to cut off.
func companionPeers(ctx context.Context, c *companion.Client) bool {
	for i := 0; i < 3; i++ {
		if i > 0 {
			if sleepCtx(ctx, 100*time.Millisecond) != nil {
				return false
			}
		}
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		info, err := c.Ping(pctx)
		cancel()
		if err != nil || info.Connections <= 1 {
			return false
		}
	}
	return true
}

// companionInUse reports whether the driver has the service on.
func (d *Device) companionInUse() bool {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.on
}

// companionFailure is why the service did not come on the last time the
// driver tried; nil when it did, or before any try.
func (d *Device) companionFailure() error {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.failed
}

// companionManual reports whether only the person can turn the service on:
// the phone did not keep the driver's last change to its accessibility
// settings, and nothing else kept the service off since.
func (d *Device) companionManual() bool {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.manual && (k.failed == nil || settingIgnored(k.failed))
}

// companionManualPhone reports whether the phone is one where only the
// person can turn the service on, whether it is on now or not.
func (d *Device) companionManualPhone() bool {
	k := &d.comp
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.manual
}

// useManuals keeps whether the phone ignores the driver's change to its
// accessibility settings in s, and reads what s knows of it, as the device
// opens: before the first AgentWorking(false), so that quietLocked leaves
// on a service the person turned on.
func (d *Device) useManuals(s *manualStore) {
	d.manuals = s
	d.comp.manual = s.has(d.Info().ID)
}

// setManualLocked notes whether the phone ignores the driver's change to
// its accessibility settings, and keeps it across Pinky's restarts.
func (d *Device) setManualLocked(manual bool) {
	d.comp.manual = manual
	d.manuals.set(d.Info().ID, manual)
}

// keepOn stops the app's idle backstop, which would turn the service off a
// minute after the last computer left, on a phone where only the person
// can turn it on: what they turned on stays on until they turn it off. The
// app forgets it when the service goes off, so it is said each time the
// driver finds the service on. It needs the service to answer.
func (d *Device) keepOn(ctx context.Context, c *companion.Client) {
	if err := c.SetIdleDisable(ctx, 0); err != nil {
		d.cfg.logf("android: %s: keeping on the companion's service, which the person turned on: %v", d.info.ID, err)
	}
}

// settingIgnored reports whether err says the phone took the change to
// enabled_accessibility_services and did not keep it.
func settingIgnored(err error) bool {
	var se *driver.CompanionStartError
	return errors.As(err, &se) && se.Reason == driver.CompanionStartSettingIgnored
}

// companionManualWords say, for an agent, why the service is off on a
// phone where only the person can turn it on.
const companionManualWords = "this phone does not let Pinky turn on Pinky's accessibility service over USB debugging; " +
	"the person can turn on Pinky in the phone's accessibility settings by hand"

var _ driver.WorkWatcher = (*Device)(nil)

// observeCompanion reads the screen from the companion, with the lock and
// keyboard state from the window manager as the uiautomator path has them.
// The two dumpsys queries run as separate shells, in parallel with the
// tree: in one shell they took about 85 ms, the tree about 30.
func (d *Device) observeCompanion(ctx context.Context, c *companion.Client) (*screen.Screen, textScan, error) {
	run := func(cmd string) <-chan string {
		ch := make(chan string, 1)
		go func() {
			out, err := d.client.Shell(ctx, d.info.ID, cmd)
			if err != nil {
				d.cfg.logf("android: %s: read state: %v", d.info.ID, err)
			}
			ch <- string(out)
		}()
		return ch
	}
	keyguardc, imec := run(keyguardCmd), run(imeWindowCmd)
	t, err := c.Dump(ctx, companion.DumpOptions{})
	keyguard, ime := <-keyguardc, <-imec
	if err != nil {
		return nil, textScan{}, err
	}
	read := t
	s := t.Screen(companion.ScreenOptions{})
	s.CapturedAt = c.HostTime(t.Time)
	if locked, ok := parseKeyguard(keyguard); ok {
		s.Locked = locked
	}
	if imeWindowRE.MatchString(ime) {
		s.Keyboard = parseIMEWindowVisible(ime)
	}
	if emptyWithKeyboard(s) {
		// The keyboard covers the app with a screen of its own (Gboard's
		// stylus tutorial, setup or a full-screen panel), whose nodes a
		// normal read leaves out: read them too.
		if t2, err := c.Dump(ctx, companion.DumpOptions{IME: true}); err == nil {
			if s2 := t2.Screen(companion.ScreenOptions{Keyboard: true}); len(s2.Elements) > 0 {
				s2.CapturedAt = c.HostTime(t2.Time)
				s2.Locked, s2.Keyboard = s.Locked, s.Keyboard
				s, read = s2, t2
			}
		}
	}
	// Pinky on the phone's own pill and approval card, which on-phone mode
	// shows while the agent holds the phone, lie over the app: a tap there
	// lands on them.
	s.Covered = append(s.Covered, read.OverlaysOf(c.App().Package)...)
	s.App.Name = d.companionAppName(s.App)
	s.Events = d.comp.takeEvents()
	return s, scanCompanion(read), nil
}

// companionAppName names the foreground app from the driver's app list,
// as the uiautomator path does, so headers stay the same across the two.
// The companion's own name may be a window title on the first tree after
// an app opens. The list loads in the background the first time.
func (d *Device) companionAppName(app screen.App) string {
	if name := d.appName(app.ID); name != "" {
		return name
	}
	d.appsMu.Lock()
	loaded := d.apps != nil
	d.appsMu.Unlock()
	if !loaded && d.appsLoading.CompareAndSwap(false, true) {
		go func() {
			defer d.appsLoading.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = d.cachedApps(ctx, 0)
		}()
	}
	return app.Name
}

// refusedText reports whether the companion refused a set_text request
// before changing anything, so typing it another way cannot duplicate it.
func refusedText(err error) bool {
	var ce *companion.Error
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case "not_found", "not_editable", "disabled", "unsupported", "action_failed":
		return true
	}
	return false
}

// typeCompanion writes text with the companion into the focused field of
// a display, or else into the field under the last tap there (tap
// returns it), and presses Enter with enter when Submit was asked and the
// companion could not submit. It returns done=false, with the companion's
// refusal, when the caller should type another way. An app with
// FeatureSetTextFallback reads the field back: text that reads otherwise
// is an error, and is not submitted.
func (d *Device) typeCompanion(ctx context.Context, c *companion.Client, text string, append, submit bool, display int, tap func() (screen.Point, bool), enter func() error) (done bool, refused, err error) {
	o := companion.SetTextOptions{Append: append, Submit: submit, Display: display}
	res, err := c.SetText(ctx, companion.Target{}, text, o)
	if refusedText(err) {
		if p, ok := tap(); ok {
			res, err = c.SetText(ctx, companion.Target{Point: &p}, text, o)
		}
	}
	switch {
	case err == nil:
		if res.Method != "" && res.Method != companion.MethodSetText {
			d.cfg.logf("android: %s: the companion wrote the text by %s", d.info.ID, res.Method)
		}
		if d.comp.has(companion.FeatureSetTextFallback) {
			switch {
			case res.Password:
				d.setInputNote(noteHidden)
			case !res.Verified:
				return true, nil, typedOtherwise(res.Text, text, append, submit, "")
			}
		}
		if submit && !res.Submitted {
			return true, nil, enter()
		}
		return true, nil, nil
	case refusedText(err):
		d.cfg.logf("android: %s: the companion could not set the text (%v); typing it instead", d.info.ID, err)
		return false, err, nil
	default:
		// The request may have reached the phone; typing the text again
		// could enter it twice, so report it instead.
		d.companionFailed(err)
		return true, nil, fmt.Errorf("android: set text through the companion: %w", err)
	}
}

// recentTap returns the last tap's point if it was in the last minute.
func (d *Device) recentTap() (screen.Point, bool) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.tapAt.IsZero() || time.Since(d.tapAt) > time.Minute {
		return screen.Point{}, false
	}
	return d.tapPoint, true
}
