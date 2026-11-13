package ios

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// Input on a phone with several displays (the iPhone Duo, GitHub issue #32
// and the report of 2026-11-12). Touches go to dtuhidd's virtual
// touchscreen (surfaceTouchscreen, 257), whose reports carry a position on
// "the panel" and nothing that names a display; the phone decides which
// display that is. Reported: folded, on the outer display, taps worked
// and Home did nothing; unfolded, on the inner display, the picture
// followed the phone but no touch or key did anything, and Pinky said
// each one worked. The HID services never acknowledge a report, so the
// only sign of what an input did is the picture.
//
// So on such a phone Pinky watches the picture after each input
// (inputEcho): whether it changed within echoWait, logged with the
// display shown, the stream's display id, the HID service and the panel
// point. When the last stillLimit inputs on the display in use changed
// nothing, whatever earlier ones did, a touch returns an error that says so
// (the gateway answers "uncertain" with it) instead of a silent success.
// Home listens for the phone's answer to the button and, when the phone
// refused it or the picture did not change, goes home with a swipe up
// from the bottom edge, as on any iPhone without a Home button; when the
// swipe changes nothing either, Home returns an error (uncertain), not a
// success.

const (
	// echoWait is how long the picture is watched after an input.
	echoWait = 1200 * time.Millisecond
	// echoBefore is how far back "before the input" reaches.
	echoBefore = time.Second
	// stillLimit is how many inputs in a row may change nothing on the
	// display in use before touches report it.
	stillLimit = 2
	// homeReplyWait is how long Home waits for the phone's answer.
	homeReplyWait = 300 * time.Millisecond
	// homeEchoWait is how long Home waits for the picture to change before
	// it swipes.
	homeEchoWait = time.Second
)

// echoCount is what inputs on one display did.
type echoCount struct {
	Sent  int // inputs watched
	Moved int // inputs after which the picture changed
	Still int // inputs in a row after which it did not
}

// movedAfter reports whether the picture changed after an input: the
// accessibility daemon reported a change, the frames after it vary in size
// (a still screen encodes to frames of one size, see quietSince), or
// frames came after it while none came for echoBefore before it (the
// phone stops sending frames a few seconds after the screen stops moving).
func movedAfter(before, after []frameMark, axChanged bool) bool {
	if axChanged {
		return true
	}
	if len(after) == 0 {
		return false
	}
	if len(before) == 0 {
		return true
	}
	lo, hi := after[0].size, after[0].size
	for _, f := range after[1:] {
		lo, hi = min(lo, f.size), max(hi, f.size)
	}
	if hi-lo > max(64, lo/50) {
		return true
	}
	// A frame of another size than the still ones before the input.
	b := before[len(before)-1].size
	return hi-b > max(64, b/50) || b-lo > max(64, b/50)
}

// pictureMoved reports whether the picture of m changed since t.
func (d *Device) pictureMoved(m *media, t time.Time) bool {
	if m == nil {
		return false
	}
	d.svcMu.Lock()
	ax := d.ax
	d.svcMu.Unlock()
	axChanged := ax != nil && ax.lastChange().After(t)
	all := m.framesSince(t.Add(-echoBefore))
	var before, after []frameMark
	for _, f := range all {
		if f.at.After(t) {
			after = append(after, f)
		} else {
			before = append(before, f)
		}
	}
	return movedAfter(before, after, axChanged)
}

// multi reports whether the phone has several displays of its own.
func (d *Device) multi() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.multiLocked()
}

// inputTarget describes where input goes now, for the log: the display the
// stream shows (its key), the id the stream asked for, and the displays.
func (d *Device) inputTarget() (shown string, streamOn int, displays string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	shown = d.streamShows
	if shown == "" {
		shown = d.want
	}
	return shown, d.streamOn, describeDisplays(d.dinfo.Displays)
}

// unseenError is inputUnseen's error: the input was sent, but the ones
// before it on the display in use changed nothing.
type unseenError struct {
	still   int    // inputs in a row that changed nothing
	moved   int    // inputs on the display that ever changed the picture
	display string // the display's key
	stream  int    // the stream's display id
}

func (e *unseenError) Error() string {
	if e.moved == 0 {
		return fmt.Sprintf("the screen did not change after the last %d touches on the display in use (%s, streamed as display %d). On a phone with two screens Pinky's touches may reach only one of them (on iPhone Duo, the outer display): fold the phone to use the outer display, or call phone_handoff", e.still, e.display, e.stream)
	}
	return fmt.Sprintf("the screen did not change after the last %d touches on the display in use (%s, streamed as display %d), though earlier ones there did. They may have missed, or the phone may have stopped taking Pinky's touches: observe the screen before touching it again, and call phone_handoff if touches keep changing nothing", e.still, e.display, e.stream)
}

// inputUnseen returns an error when the last stillLimit inputs on the
// display shown changed nothing, nil otherwise or on a phone with one
// display. It judges the last inputs only: an earlier one that changed the
// picture does not vouch for the ones after it (GitHub issue #106: on an
// iPhone Duo a tap that changed the picture was followed by ones that did
// nothing, each reported as done).
func (d *Device) inputUnseen() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.multiLocked() {
		return nil
	}
	key := d.streamShows
	if key == "" {
		key = d.want
	}
	c := d.echo[key]
	if c == nil || c.Still < stillLimit {
		return nil
	}
	return &unseenError{still: c.Still, moved: c.Moved, display: key, stream: d.streamOn}
}

// noteEcho counts what an input on display key did.
func (d *Device) noteEcho(key string, moved bool) echoCount {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.echo == nil {
		d.echo = map[string]*echoCount{}
	}
	c := d.echo[key]
	if c == nil {
		c = &echoCount{}
		d.echo[key] = c
	}
	c.Sent++
	if moved {
		c.Moved++
		c.Still = 0
	} else {
		c.Still++
	}
	return *c
}

// watchEcho, on a phone with several displays, watches in the background
// whether the picture changed after an input sent at t, counts it for the
// display shown and logs it. what names the input; x, y is where it
// touched the panel (for a key, both 0).
func (d *Device) watchEcho(what string, t time.Time, x, y uint16) {
	if !d.multi() {
		return
	}
	d.mu.Lock()
	m := d.m
	d.mu.Unlock()
	shown, on, displays := d.inputTarget()
	go func() {
		select {
		case <-time.After(time.Until(t.Add(echoWait))):
		case <-d.done:
			return
		}
		moved := d.pictureMoved(m, t)
		c := d.noteEcho(shown, moved)
		d.log.Info("iphone: input on a phone with several displays", "device", d.info.ID, "input", what,
			"moved", moved, "shown", shown, "stream_display", on, "touch_service", surfaceTouchscreen,
			"panel", fmt.Sprintf("%d,%d", x, y), "sent", c.Sent, "changed", c.Moved, "still_in_a_row", c.Still,
			"displays", displays)
	}()
}

// homeMulti is Home on a phone with several displays: the Home button,
// listening for the phone's answer, then, when the phone refused it or the
// picture did not change within homeEchoWait, a swipe up from the bottom
// edge. The swipe is told to the agent (TakeInputNote).
func (d *Device) homeMulti(ctx context.Context) error {
	d.inputMu.Lock()
	h, err := d.waitInput(ctx)
	if err != nil {
		d.inputMu.Unlock()
		return err
	}
	d.mu.Lock()
	m := d.m
	d.mu.Unlock()
	t := time.Now()
	ans, err := h.buttonListen(ctx, consumerPage, usageHome, 60*time.Millisecond, homeReplyWait)
	d.inputMu.Unlock()
	if err != nil {
		return err
	}
	moved := ans.Err == nil && d.waitMoved(ctx, m, t, homeEchoWait)
	shown, on, displays := d.inputTarget()
	d.log.Info("iphone: Home on a phone with several displays", "device", d.info.ID, "moved", moved,
		"replied", ans.Replied, "reply", ans.Reply, "refused", errString(ans.Err),
		"shown", shown, "stream_display", on, "displays", displays)
	if moved {
		d.noteEcho(shown, true)
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	why := "the screen did not change after the Home button"
	if ans.Err != nil {
		why = fmt.Sprintf("the phone refused the Home button (%v)", ans.Err)
	}
	// The same swipe as the one that brings up the passcode pad
	// (power.go's swipeUp), which works on the phones tried: from the last
	// row of the screen, fast, lifted while still moving.
	s := d.freshDisplay(ctx).upright()
	from := screen.Point{X: s.Width / 2, Y: s.Height - 1}
	to := screen.Point{X: s.Width / 2, Y: s.Height * 45 / 100}
	t = time.Now()
	serr := d.Swipe(ctx, from, to, 250*time.Millisecond)
	if ue := (*unseenError)(nil); errors.As(serr, &ue) {
		// The swipe was sent; what the touches before it did is not
		// Home's answer, the picture after it is.
		serr = nil
	}
	swiped := serr == nil && d.waitMoved(ctx, m, t, homeEchoWait)
	if serr == nil && !swiped && ctx.Err() != nil {
		serr = ctx.Err()
	}
	d.log.Info("iphone: Home by a swipe up", "device", d.info.ID, "why", why, "moved", swiped,
		"swipe_error", errString(serr), "shown", shown)
	note, err := homeSwipeOutcome(why, serr, swiped)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.inputNote = note
	d.mu.Unlock()
	return nil
}

// homeSwipeOutcome is what Home on a phone with several displays returns
// after the Home button did nothing (why says what) and Pinky swiped up
// from the bottom edge: the swipe's error, or whether the picture changed
// after it. A swipe that changed nothing either is an error, which the
// gateway answers as "uncertain" with it: the phone may have taken neither
// (as on an iPhone Duo folded with an app open, GitHub issue #106), or the
// home screen showed already. A swipe that changed the picture leaves a
// note for the agent (TakeInputNote), since the change may not be the home
// screen.
func homeSwipeOutcome(why string, swipeErr error, swiped bool) (note string, err error) {
	if swipeErr != nil {
		return "", fmt.Errorf("home: %s; then the swipe up from the bottom edge: %w", why, swipeErr)
	}
	if !swiped {
		return "", fmt.Errorf("home: %s, and not after a swipe up from the bottom edge either, so the phone may have taken neither (if the home screen showed already, there was nothing to change). On a phone with two screens, Pinky's input may reach only one of them: observe the screen, and if an app still shows, call phone_handoff", why)
	}
	return fmt.Sprintf("Home: %s, so Pinky swiped up from the bottom edge instead and the screen changed. Check that the home screen shows.", why), nil
}

// waitMoved waits up to limit for the picture of m to change after t.
func (d *Device) waitMoved(ctx context.Context, m *media, t time.Time, limit time.Duration) bool {
	end := t.Add(limit)
	for {
		if d.pictureMoved(m, t) {
			return true
		}
		if time.Now().After(end) || sleepCtx(ctx, 50*time.Millisecond) != nil {
			return false
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
