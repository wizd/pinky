package gateway

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/driver"
)

// Restarting the device service without a password
// (driver.QuietDeviceServiceRestarter).
//
// macOS's usbmuxd can lose an iPhone on Wi-Fi for up to an hour, and
// restarting it brings the phone back (internal/driver/ios/lost.go). Once
// the person allowed restarts without a password (Settings › Phones), the
// gateway restarts the service by itself when it lost a phone an agent
// uses: when the agent's call finds the phone lost, and on discovery while
// a task holds it. Restarting drops every phone of the platform for a
// second or two, also on a cable, so it waits while one of them is in the
// middle of an action, and restarts at most once every quietRestartEvery.
// Every restart is logged, and the agents of the phones it dropped are
// told in their next result.

// quietRestartEvery is how long the gateway waits after a restart of a
// platform's device service before it restarts it by itself again. A
// variable for tests.
var quietRestartEvery = 5 * time.Minute

// lostBackWait is how long an agent's call waits for its phone to come
// back after Pinky restarted the device service for it. A variable for
// tests.
var lostBackWait = 30 * time.Second

// lostLocked reports whether the device service lost the phone on Wi-Fi.
// The caller holds g.mu.
func lostLocked(d *device) bool {
	return waitingLocked(d) && d.info.Setup == driver.SetupDeviceService
}

// QuietRestart reports whether this computer can restart its device
// service without a password (supported: a Mac), and whether the person
// allowed it, for Settings › Phones. It is off until they turn it on.
func (g *Gateway) QuietRestart() (supported, allowed bool) {
	for _, p := range g.opt.Providers {
		if q, ok := p.(driver.QuietDeviceServiceRestarter); ok {
			s, a := q.QuietRestart()
			supported, allowed = supported || s, allowed || (s && a)
		}
	}
	return supported, allowed
}

// SetQuietRestart allows (on) or stops allowing restarts of the device
// service without a password, as the person chose in Settings › Phones.
// The system asks an administrator once, showing reason. It returns
// driver.ErrCanceled when the person declines.
func (g *Gateway) SetQuietRestart(ctx context.Context, on bool, reason string) error {
	done := false
	for _, p := range g.opt.Providers {
		q, ok := p.(driver.QuietDeviceServiceRestarter)
		if !ok {
			continue
		}
		if s, _ := q.QuietRestart(); !s {
			continue
		}
		if err := q.AllowQuietRestart(ctx, on, reason); err != nil {
			g.log.Warn("device service restarts without a password", "allowed", on, "err", err)
			return err
		}
		done = true
	}
	if !done {
		return fmt.Errorf("restarting the device service without a password: %w", driver.ErrUnsupported)
	}
	g.log.Info("device service restarts without a password", "allowed", on)
	if on {
		g.discoverSoon() // a phone lost now comes back
	}
	return nil
}

// restartLost restarts a platform's device service without a password
// when it lost a phone an agent uses (only, when set: that phone), the
// person allowed it, no phone of the platform is in the middle of an
// action, and the service did not restart through Pinky within
// quietRestartEvery. It reports whether it restarted the service.
func (g *Gateway) restartLost(ctx context.Context, only *device) bool {
	restarted := false
	for _, p := range g.opt.Providers {
		q, ok := p.(driver.QuietDeviceServiceRestarter)
		if !ok {
			continue
		}
		g.mu.Lock()
		var lost []*device
		busy := false
		for _, d := range g.devices {
			if d.provider != p {
				continue
			}
			if lostLocked(d) && d.lease != nil && !d.lease.ended {
				lost = append(lost, d)
			}
			busy = busy || d.inflight != nil
		}
		g.mu.Unlock()
		if only != nil && !slices.Contains(lost, only) || len(lost) == 0 {
			continue
		}
		supported, allowed := q.QuietRestart()
		allowed = allowed && supported
		now := time.Now()
		g.mu.Lock()
		for _, d := range lost {
			d.quietRestart = allowed
		}
		last := g.serviceRestarts[p.Platform()]
		soon := !last.IsZero() && now.Sub(last) < quietRestartEvery
		run := allowed && !busy && !soon
		if run {
			// Taken before it runs, so a call and discovery at the same
			// time restart it once.
			g.noteRestartLocked(p.Platform(), now)
		}
		g.mu.Unlock()
		if !run {
			if allowed && busy {
				g.log.Debug("device service lost a phone; waiting for the action in progress before restarting it", "platform", p.Platform())
			}
			continue
		}
		ids := make([]string, len(lost))
		for i, d := range lost {
			ids[i] = d.id
		}
		sort.Strings(ids)
		g.log.Info("restarting the device service without a password: it lost a phone an agent uses", "platform", p.Platform(), "devices", strings.Join(ids, ","))
		if err := q.RestartDeviceServiceQuietly(ctx); err != nil {
			g.log.Warn("restart the device service without a password", "platform", p.Platform(), "err", err)
			continue
		}
		g.mu.Lock()
		for _, d := range g.devices {
			if d.provider != p {
				continue
			}
			was := slices.Contains(lost, d)
			if was {
				d.serviceRestarted = now
			}
			if l := d.lease; l != nil && !l.ended {
				l.bgNote = joinNote(l.bgNote, restartedNote(d.name(), was))
			}
			g.wakeLocked(d)
		}
		g.mu.Unlock()
		restarted = true
		g.discoverSoon()
	}
	return restarted
}

// noteRestartLocked records a restart of the platform's device service
// through Pinky. The caller holds g.mu.
func (g *Gateway) noteRestartLocked(p driver.Platform, at time.Time) {
	if g.serviceRestarts == nil {
		g.serviceRestarts = map[driver.Platform]time.Time{}
	}
	g.serviceRestarts[p] = at
}

// restartedNote tells an agent that Pinky restarted the device service:
// for its phone, which the service had lost (lost), or for another one.
func restartedNote(name string, lost bool) string {
	if lost {
		return fmt.Sprintf("This computer's device service (usbmuxd) had lost %s on Wi-Fi, so Pinky restarted it without a password, as the person allowed in Settings › Phones. "+
			"Every iPhone and iPad on this computer disconnected for a second or two.", name)
	}
	return fmt.Sprintf("Pinky restarted this computer's device service (usbmuxd), which had lost another phone on Wi-Fi, so %s disconnected for a second or two: "+
		"if your last action seemed to fail, observe before you repeat it.", name)
}

// recoverLost restarts the device service for the lease's phone when the
// service lost it (restartLost), and then waits up to lostBackWait for the
// phone to come back, so the agent's call goes on with it.
func (g *Gateway) recoverLost(ctx context.Context, l *lease) {
	g.mu.Lock()
	d := l.dev
	lost := lostLocked(d) && !l.ended
	g.mu.Unlock()
	if !lost {
		return
	}
	if !g.restartLost(ctx, d) {
		// Not allowed, or not now; one that restarted moments ago tells
		// the agent to look again (deviceServiceHelpLocked).
		return
	}
	deadline := time.Now().Add(lostBackWait)
	g.mu.Lock()
	defer g.mu.Unlock()
	for !d.online && !l.ended && !(waitingLocked(d) && !lostLocked(d)) {
		left := time.Until(deadline)
		if left <= 0 {
			return
		}
		ch := d.wake
		g.mu.Unlock()
		t := time.NewTimer(left)
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
		case <-l.ctx.Done():
		}
		t.Stop()
		g.mu.Lock()
		if ctx.Err() != nil || l.ctx.Err() != nil {
			return
		}
	}
}

// deviceServiceHelpLocked is what an agent can do about a phone the device
// service lost: wait a little when Pinky restarts the service by itself,
// else only a person at the computer can help. The caller holds g.mu.
func deviceServiceHelpLocked(d *device) string {
	if r := d.serviceRestarted; !r.IsZero() && time.Since(r) < quietRestartEvery {
		return fmt.Sprintf("Pinky restarted the device service without a password %s ago, as the person allowed in Settings › Phones, but the phone is not back yet. "+
			"It may take a minute: call phone_observe again in a little while. If it stays lost, release it with phone_release and use another phone, "+
			"or end the task saying the phone needs the device service restarted.", approval.Human(time.Since(r)))
	}
	if d.quietRestart {
		return fmt.Sprintf("The person allowed Pinky to restart the device service without a password (Settings › Phones), and Pinky does so by itself "+
			"once no iPhone or iPad on this computer is in the middle of an action, at most once every %s: call phone_observe again in a minute. "+
			"If it stays lost, release it with phone_release and use another phone, or end the task.", approval.Human(quietRestartEvery))
	}
	return deviceServiceHelp
}
