package ios

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Resting: letting go of an idle phone (GitHub issue #96).
//
// An open iPhone keeps sessions on the phone that cost it something while
// nobody uses them: the CoreDevice tunnel, remoteAXService's session (iOS
// 27; while a client is connected the phone keeps its accessibility
// machinery up for it, and serves no other client, such as the iPhone
// app's own tasks), the inspector's session (axAuditDaemon, which follows
// apps coming to the front), and the pasteboard and screenshot services.
// A person reported an iPhone 16 Pro on iOS 27 over Wi-Fi that stuttered
// while scrolling until they paused Pinky's connection to it.
//
// So once a phone has had no task (driver.WorkWatcher), no view of its
// screen (the mirror, a recording) and no call from Pinky for restIdle,
// the device closes all of that and rests; the device itself stays open,
// and the gateway goes on showing the phone online. The next call that
// needs the phone (an agent's connect and first read, the mirror opening,
// the checkup, the app list) wakes it first with a new tunnel, as when the
// phone is opened; nothing of it asks the person for anything. Lockdown
// reads (the lock state, battery and storage) go through usbmuxd and do
// not wake it. The helper's runner is not stopped (rest_wda.go).
//
// A wake that fails reports driver.ErrOffline, so the gateway drops the
// phone and opens it again the usual way.

// restIdle is how long a phone goes unused before it rests. It is longer
// than streamIdle, so the stream has stopped by then, and than
// switchBackAfter (the gateway's 30 s back from the helper); and it lets
// an agent that releases the phone and connects again within a minute or
// so, as agents often do between steps of one job, find it awake. Waking
// costs what opening the phone costs: a tunnel (about a second on a
// cable, one to three over Wi-Fi).
const restIdle = 90 * time.Second

// wakeTimeout bounds a wake: the gateway's connectWait.
const wakeTimeout = 20 * time.Second

// AgentWorking implements driver.WorkWatcher: while an agent works on the
// phone it never rests.
func (d *Device) AgentWorking(on bool) {
	d.working.Store(on)
	if on {
		d.markUsed()
	}
}

// markUsed records a use of the phone's tunnel.
func (d *Device) markUsed() { d.usedAt.Store(time.Now().UnixNano()) }

// usedAtTime is when the tunnel was last used, or the zero time.
func (d *Device) usedAtTime() time.Time { return unixTime(d.usedAt.Load()) }

func unixTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// cur returns the link the device has now, without waking it: for
// lockdown, which goes through usbmuxd whether or not the tunnel is up.
func (d *Device) cur() *link {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	return d.l
}

// Resting reports whether the device let go of the phone's sessions.
func (d *Device) Resting() bool {
	d.lmu.Lock()
	defer d.lmu.Unlock()
	return d.resting
}

// link returns the device's link, waking the phone first when it rests,
// and counts as a use. A wake that failed leaves the closed link, whose
// calls then fail; gone reports them as driver.ErrOffline.
func (d *Device) link() *link {
	d.markUsed()
	d.lmu.Lock()
	l, resting := d.l, d.resting
	d.lmu.Unlock()
	if !resting {
		return l
	}
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()
	if nl, err := d.wakeUp(ctx); err == nil {
		return nl
	}
	return d.cur()
}

// wakeUp opens a new tunnel to a resting phone. Callers may hold svcMu,
// restartMu or inputMu: it takes none of them.
func (d *Device) wakeUp(ctx context.Context) (*link, error) {
	d.wakeMu.Lock()
	defer d.wakeMu.Unlock()
	d.lmu.Lock()
	l, resting, lost := d.l, d.resting, d.wakeLost
	d.lmu.Unlock()
	if !resting {
		return l, nil
	}
	if lost != nil {
		return nil, lost
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("device closed: %w", driver.ErrOffline)
	}
	start := time.Now()
	dial := d.redial
	if dial == nil {
		dial = func(ctx context.Context) (*link, error) {
			return dialLink(ctx, d.info.ID, d.info.Connection == "wifi")
		}
	}
	nl, err := dial(ctx)
	if err != nil {
		// The gateway drops the phone and opens it again (gone).
		lost := fmt.Errorf("wake the phone: %v: %w", err, driver.ErrOffline)
		d.lmu.Lock()
		d.wakeLost = lost
		d.lmu.Unlock()
		d.logger().Warn("iphone: could not wake the phone", "device", d.info.ID, "after", time.Since(start).Round(time.Millisecond), "err", err)
		return nil, lost
	}
	d.lmu.Lock()
	d.l, d.resting = nl, false
	d.lmu.Unlock()
	d.markUsed()
	d.logger().Info("iphone: awake", "device", d.info.ID, "in", time.Since(start).Round(time.Millisecond))
	if d.reconnect != nil {
		go d.reconnect()
	}
	return nl, nil
}

// reconnectCore connects again, in the background, what a CoreDevice
// session connects as it opens (run): the display info service, and
// remoteAXService and the pasteboard, whose first requests are slow.
func (d *Device) reconnectCore() {
	c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.infoSvc()
	d.raxReady(c)
	if pb := d.pasteboard(); pb != nil {
		pb.connect()
	}
}

// reconnectAX connects the inspector again, in the background, as
// OpenAccessibility does.
func (d *Device) reconnectAX() {
	c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d.axReady(c)
}

// restIfIdle lets go of the phone's sessions when it has been unused for
// restIdle (see above). It never waits for a lock another call holds: such
// a call is a use. It reports whether the device now rests.
func (d *Device) restIfIdle(now time.Time) bool {
	if d.working.Load() || !d.wakeMu.TryLock() {
		return false
	}
	defer d.wakeMu.Unlock()
	if !d.restartMu.TryLock() {
		return false
	}
	defer d.restartMu.Unlock()
	if !d.inputMu.TryLock() {
		return false
	}
	defer d.inputMu.Unlock()
	d.mu.Lock()
	idle := !d.closed && d.m == nil && d.hid == nil && d.watchers == 0 &&
		idleSince(now, restIdle, d.watchedAt, d.lastInputAt(), d.usedAtTime(), d.openedAt)
	d.mu.Unlock()
	if !idle {
		return false
	}
	d.lmu.Lock()
	l := d.l
	ok := !d.resting && l != nil && l.tunnel != nil && !l.tunnelOnly
	d.lmu.Unlock()
	if !ok || !d.svcMu.TryLock() {
		return false
	}
	var closers []func()
	for _, s := range []*cdService{d.info2, d.shot} {
		if s != nil {
			closers = append(closers, func() { s.Close() })
		}
	}
	if d.inst != nil {
		closers = append(closers, d.inst.Close)
	}
	if d.ax != nil {
		ax := d.ax
		closers = append(closers, func() { ax.Close() })
	}
	if d.rax != nil {
		closers = append(closers, d.rax.Close)
	}
	if d.pb != nil {
		pb := d.pb
		closers = append(closers, func() { pb.Close() })
	}
	d.info2, d.shot, d.inst, d.ax, d.rax, d.pb = nil, nil, nil, nil, nil, nil
	d.raxErr = nil
	d.lmu.Lock()
	d.resting, d.wakeLost = true, nil
	d.lmu.Unlock()
	d.svcMu.Unlock()
	d.logs.stopAll()
	for _, c := range closers {
		c()
	}
	l.Close()
	d.logger().Info("iphone: resting", "device", d.info.ID, "idle", restIdle)
	return true
}

// restLoop rests the device once it is idle, until it closes. An open
// CoreDevice session checks in idleLoop instead.
func (d *Device) restLoop() {
	t := time.NewTicker(idleCheck)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			d.restIfIdle(time.Now())
		case <-d.done:
			return
		}
	}
}

func (d *Device) logger() *slog.Logger { return logTo(d.log) }

// AgentWorking implements driver.WorkWatcher for the inspector path.
func (a *AXDevice) AgentWorking(on bool) { a.d.AgentWorking(on) }

// Resting reports whether the device let go of the phone's sessions.
func (a *AXDevice) Resting() bool { return a.d.Resting() }

var (
	_ driver.WorkWatcher = (*Device)(nil)
	_ driver.WorkWatcher = (*AXDevice)(nil)
)
