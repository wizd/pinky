package companion

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// settingKey is the only setting Pinky changes on a phone, and only its own
// entry in it.
const settingKey = "enabled_accessibility_services"

// startWait is how long Enable and TurnOn wait for the service to answer;
// tests shorten it.
var startWait = 10 * time.Second

// restoreState remembers the setting as Enable found it.
type restoreState struct {
	raw   string   // exactly as read; "" when unset
	unset bool     // the key did not exist
	list  []string // its components
}

// IgnoredDetail is the Detail of the error Enable and TurnOn return on a
// phone that takes their change to enabled_accessibility_services without
// an error and does not keep Pinky's entry: vivo's OriginOS 6 (issue #94).
const IgnoredDetail = "the phone took the change to " + settingKey + " without an error but did not keep Pinky's entry in it, " +
	"so it does not let Pinky turn the service on over USB debugging; the person can turn on Pinky in the phone's accessibility settings by hand"

// settingIgnored is the error of Enable and TurnOn when the phone did not
// keep their change.
func settingIgnored() error {
	return &driver.CompanionStartError{Reason: driver.CompanionStartSettingIgnored, Detail: IgnoredDetail}
}

// Enable turns the companion's accessibility service on for a task and
// waits until it answers. It reads enabled_accessibility_services, adds
// only Pinky's own service and keeps every other entry. If the service was
// already on (the person enabled it, or an earlier run did not clean up),
// the setting is left alone and Disable will not turn it off.
//
// It reads the setting back after changing it: a phone that did not keep
// Pinky's entry (vivo's OriginOS 6 takes the change without an error and
// keeps nothing) fails at once with a *driver.CompanionStartError whose
// Reason is driver.CompanionStartSettingIgnored. If the service does not
// answer within 10 seconds, Enable restores the setting itself and
// returns the error.
func (c *Client) Enable(ctx context.Context) error {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	wrote := false
	var err error
	if c.restore == nil {
		raw, unset, rerr := c.readSetting(ctx)
		if rerr != nil {
			return rerr
		}
		list := parseServices(raw)
		if !slices.ContainsFunc(list, c.isOurs) {
			st := &restoreState{raw: raw, unset: unset, list: list}
			if err := c.writeSetting(ctx, c.withOurs(list), false); err != nil {
				return err
			}
			c.restore = st
			wrote = true
			err = c.kept(ctx)
		}
	}
	if err == nil {
		err = c.waitReady(ctx, startWait)
	}
	if err != nil && wrote {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if rerr := c.restoreLocked(rctx); rerr != nil {
			return fmt.Errorf("%w (and restoring %s failed: %v)", err, settingKey, rerr)
		}
	}
	return err
}

// Disable closes the client's connections and, if Enable turned the
// service on, removes Pinky's entry again. When nothing else changed in the
// meantime the setting goes back to exactly what Enable found (deleted if
// it did not exist); otherwise only Pinky's entry is removed and other
// changes are kept.
func (c *Client) Disable(ctx context.Context) error {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	c.mu.Lock()
	c.dropLocked()
	c.mu.Unlock()
	return c.restoreLocked(ctx)
}

// restoreLocked undoes Enable's change; c.setMu must be held.
func (c *Client) restoreLocked(ctx context.Context) error {
	st := c.restore
	if st == nil {
		return nil
	}
	raw, unset, err := c.readSetting(ctx)
	if err != nil {
		return err
	}
	list := parseServices(raw)
	rest := slices.DeleteFunc(slices.Clone(list), c.isOurs)
	switch {
	case sameComponents(rest, st.list):
		// Android may have rewritten the list in another order or form;
		// put back the exact original text.
		if raw != st.raw || unset != st.unset {
			err = c.writeSetting(ctx, st.raw, st.unset)
		}
	case len(rest) != len(list):
		err = c.writeSetting(ctx, strings.Join(rest, ":"), false)
	}
	if err != nil {
		return err
	}
	c.restore = nil
	return nil
}

// TurnOn turns the service on for good, when the person asks for it (the
// client's Install button): it adds only Pinky's entry to
// enabled_accessibility_services, keeps every other entry, waits until
// the service answers, and stops the app's idle backstop (idle_disable_ms
// 0), which would otherwise turn the service off after a minute without a
// connection. Unlike Enable, a later Disable leaves it on. Like Enable, it
// fails at once on a phone that does not keep its change.
func (c *Client) TurnOn(ctx context.Context) error {
	if err := c.turnOn(ctx); err != nil {
		return err
	}
	return c.SetIdleDisable(ctx, 0)
}

func (c *Client) turnOn(ctx context.Context) error {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	c.restore = nil
	raw, _, err := c.readSetting(ctx)
	if err != nil {
		return err
	}
	list := parseServices(raw)
	if !slices.ContainsFunc(list, c.isOurs) {
		if err := c.writeSetting(ctx, c.withOurs(list), false); err != nil {
			return err
		}
		if err := c.kept(ctx); err != nil {
			return err
		}
	}
	return c.waitReady(ctx, startWait)
}

// withOurs is the setting's value for list with Pinky's entry added.
func (c *Client) withOurs(list []string) string {
	return strings.Join(append(slices.Clone(list), c.app.Component()), ":")
}

// kept reads the setting back after Pinky added its entry, and returns
// settingIgnored when the entry is not there: some phones take the change
// without an error and keep nothing of it (vivo's OriginOS 6, issue #94),
// and waiting for a service that cannot come on would only end in a
// timeout and a probe that blames the service.
func (c *Client) kept(ctx context.Context) error {
	raw, _, err := c.readSetting(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(parseServices(raw), c.isOurs) {
		return settingIgnored()
	}
	return nil
}

// KeepEnabled makes Disable leave the service on: another client turned
// it on for good while this one had it on for a task.
func (c *Client) KeepEnabled() {
	c.setMu.Lock()
	c.restore = nil
	c.setMu.Unlock()
}

// EnabledForTask reports whether Enable turned the service on, so that
// Disable will turn it off again.
func (c *Client) EnabledForTask() bool {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	return c.restore != nil
}

// ForceDisable removes Pinky's entry from enabled_accessibility_services
// even when this client did not add it, for cleaning up after a crashed
// run. Other entries stay; the key is deleted when nothing else is left.
func (c *Client) ForceDisable(ctx context.Context) error {
	c.setMu.Lock()
	defer c.setMu.Unlock()
	c.mu.Lock()
	c.dropLocked()
	c.mu.Unlock()
	c.restore = nil
	raw, _, err := c.readSetting(ctx)
	if err != nil {
		return err
	}
	list := parseServices(raw)
	rest := slices.DeleteFunc(slices.Clone(list), c.isOurs)
	if len(rest) == len(list) {
		return nil
	}
	return c.writeSetting(ctx, strings.Join(rest, ":"), len(rest) == 0)
}

// Enabled reports whether Pinky's service is listed in
// enabled_accessibility_services.
func (c *Client) Enabled(ctx context.Context) (bool, error) {
	raw, _, err := c.readSetting(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(parseServices(raw), c.isOurs), nil
}

func (c *Client) readSetting(ctx context.Context) (raw string, unset bool, err error) {
	out, err := c.run.Shell(ctx, "settings get secure "+settingKey)
	if err != nil {
		return "", false, fmt.Errorf("read %s: %w", settingKey, err)
	}
	raw = strings.TrimRight(out, "\r\n")
	// `settings get` prints "null" for a key that does not exist.
	if raw == "null" {
		return "", true, nil
	}
	return raw, false, nil
}

func (c *Client) writeSetting(ctx context.Context, value string, unset bool) error {
	cmd := "settings put secure " + settingKey + " " + shellQuote(value)
	if unset {
		cmd = "settings delete secure " + settingKey
	}
	if _, err := c.run.Shell(ctx, cmd); err != nil {
		return fmt.Errorf("write %s: %w", settingKey, err)
	}
	return nil
}

// waitReady pings until the service answers. Android binds the service a
// moment after the setting changes. When it has not answered after max,
// the error is a *driver.CompanionStartError that says what the phone
// shows; it does not wrap context.DeadlineExceeded, which callers take for
// their own time running out.
func (c *Client) waitReady(ctx context.Context, max time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	for {
		pctx, pcancel := context.WithTimeout(wctx, 2*time.Second)
		_, err := c.Ping(pctx)
		pcancel()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrNotRunning) && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		select {
		case <-wctx.Done():
			if ctx.Err() != nil {
				return fmt.Errorf("companion did not start: %w", ctx.Err())
			}
			return c.startError(ctx, max)
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// parseServices splits the setting's colon-separated component list.
func parseServices(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ":") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// sameComponents reports whether two lists name the same components,
// ignoring order and the short or full form of class names.
func sameComponents(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	na := make([]string, len(a))
	nb := make([]string, len(b))
	for i := range a {
		na[i] = normalizeComponent(a[i])
		nb[i] = normalizeComponent(b[i])
	}
	slices.Sort(na)
	slices.Sort(nb)
	return slices.Equal(na, nb)
}

// normalizeComponent expands "pkg/.Cls" to "pkg/pkg.Cls".
func normalizeComponent(component string) string {
	pkg, cls, ok := strings.Cut(component, "/")
	if ok && strings.HasPrefix(cls, ".") {
		return pkg + "/" + pkg + cls
	}
	return component
}

// isOurs reports whether a flattened component name is the service of the
// client's build, in either the short (".Service") or the full form.
func (c *Client) isOurs(component string) bool {
	return normalizeComponent(component) == c.app.Package+"/"+ServiceClass
}
