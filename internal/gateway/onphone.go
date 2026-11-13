package gateway

import (
	"errors"
	"fmt"

	"github.com/DIYgod/pinky/internal/driver"
)

// Pinky on the phone can run tasks on the phone by itself (on-phone mode),
// and only one of the computer and the phone uses the phone at a time
// (docs/notes/android-app.md, "Two modes on one phone"). The driver claims
// the phone while an agent works on it and says when it cannot:
//
//   - driver.BusyError while Pinky on the phone (or another computer) holds
//     it. Nothing was done; the agent gets device_busy, and the phone shows
//     busy in Pinky until a read goes through again or the task ends.
//   - driver.ErrStoppedOnPhone once the person pressed Stop on the phone's
//     pill. The task ends as stopped by the person, as Stop in Pinky ends it.

// Busy lines in the sidebar, the tiles and the menu bar (ui/gwtext.go
// translates them).
const (
	busyLinePhone    = "Busy · Pinky on the phone is running a task"
	busyLineComputer = "Busy · Pinky on another computer is using it"
)

// onPhoneResult turns the driver's claim errors into the agent's result,
// or returns nil for any other error. Call it without g.mu.
func (g *Gateway) onPhoneResult(l *lease, err error) *Result {
	if errors.Is(err, driver.ErrStoppedOnPhone) {
		g.endLease(l, OutcomeStopped, "")
		return stoppedOnPhoneResult(l.dev.name())
	}
	var be *driver.BusyError
	if errors.As(err, &be) {
		g.setBusy(l.dev, be)
		return busyResult(l.dev.name(), be)
	}
	if errors.Is(err, driver.ErrBusy) {
		be = &driver.BusyError{By: "phone"}
		g.setBusy(l.dev, be)
		return busyResult(l.dev.name(), be)
	}
	return nil
}

func stoppedOnPhoneResult(name string) *Result {
	return statusResult(StatusStopped, "The person stopped this task on %s itself, with Stop in Pinky on the phone; the lease is gone. End the task.", name)
}

// busyResult is what an agent gets while another Pinky holds the phone.
func busyResult(name string, be *driver.BusyError) *Result {
	who := "Pinky on the phone is running a task of its own"
	if be.By != "phone" {
		who = "Pinky on another computer is using it"
	}
	if be.Task != "" {
		who += fmt.Sprintf(" (“%s”)", be.Task)
	}
	return errResult(ErrDeviceBusy, "%s is busy: %s, and only one of them uses the phone at a time. Nothing was done. "+
		"Try again in a while, or tell the person: they can stop that task on the phone, or let it finish.", name, who)
}

// setBusy records who holds the phone while an agent's task waits for it,
// nil once a read went through.
func (g *Gateway) setBusy(d *device, be *driver.BusyError) {
	g.mu.Lock()
	changed := (d.busy == nil) != (be == nil) || d.busy != nil && be != nil && *d.busy != *be
	d.busy = be
	g.mu.Unlock()
	if changed {
		g.emit(Event{Kind: EventDevices, DeviceID: d.id})
	}
}

// busyLineLocked is the status line of a phone whose task waits for
// another Pinky, "" when it does not.
func busyLineLocked(d *device) string {
	switch {
	case d.busy == nil || d.lease == nil:
		return ""
	case d.busy.By == "phone":
		return busyLinePhone
	}
	return busyLineComputer
}
