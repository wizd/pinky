package ios

import (
	"context"
	"fmt"
)

// Locked implements driver.LockReporter, best effort. It reads lockdown's
// PasswordProtected value, which is true while the phone needs its passcode
// to unlock (the same thing devicectl reports as passcodeRequired). A phone
// without a passcode always reads as unlocked, even on its lock screen.
// iOS 27's developer disk image does not implement CoreDevice's getlockstate.
func (d *Device) Locked(ctx context.Context) (bool, error) {
	type result struct {
		v   bool
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := d.cur().lockdownValue("PasswordProtected")
		if err != nil {
			ch <- result{err: err}
			return
		}
		b, ok := v.(bool)
		if !ok {
			ch <- result{err: fmt.Errorf("PasswordProtected is %T", v)}
			return
		}
		ch <- result{v: b}
	}()
	select {
	case r := <-ch:
		return r.v, d.gone(r.err)
	case <-ctx.Done():
		return false, ctx.Err()
	}
}
