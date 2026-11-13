package standalone

import (
	"context"
	"errors"
	"time"
)

// Claims is who drives the phone (docs/notes/companion-protocol.md,
// "claim"): the core claims it while its task runs, and Pinky on a computer
// may hold it, which keeps the core from starting a task meanwhile. On
// Android it is local.Claims, over the app's companion socket.
type Claims interface {
	// Watch follows who holds the phone until ctx ends. It calls changed
	// when that may have changed, and lost when the core's claim ended
	// without Release: a computer took the phone while the service
	// restarted.
	Watch(ctx context.Context, changed, lost func())
	// Claim takes the phone for the core's task; busy when a computer
	// holds it.
	Claim(ctx context.Context, task string) (busy bool, err error)
	// Release gives the phone back.
	Release(ctx context.Context)
	// Computer reports a computer's claim: the task it named, if any, and
	// whether the person can stop it from the phone.
	Computer() (task string, stoppable, ok bool)
	// StopComputer ends a computer's claim.
	StopComputer(ctx context.Context) error
}

// ErrComputer: Pinky on a computer is using the phone, so the core starts
// no task until it is done.
var ErrComputer = errors.New("standalone: Pinky on a computer is using the phone")

// claimWait bounds a claim request; the subscription it goes over may take
// a few seconds to open after the service starts.
const claimWait = 15 * time.Second

// StopComputer ends Pinky on a computer's claim on the phone: the person
// pressed Stop on the pill while it worked. The computer hears it as a
// claim event that says stopped.
func (c *Core) StopComputer() error {
	if c.opt.Claims == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(c.ctx, claimWait)
	defer cancel()
	return c.opt.Claims.StopComputer(ctx)
}

// claimLost stops the task whose claim ended: a computer took the phone
// while the service restarted.
func (c *Core) claimLost() {
	c.log.Warn("standalone: the task lost its claim on the phone; stopping it")
	c.Stop()
}
