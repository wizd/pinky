package android

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/screen"
)

// How a background display's taps, swipes, drags, pinches and keys reach
// it (DisplayOptions.Input).
//
// Pinky sends them through scrcpy, as on the main display. Xiaomi's MIUI
// and HyperOS drop what adb injects into a display other than the main
// one, while text set through the companion still lands (DIYgod/pinky#48,
// HyperOS 1.0.9.0 on Android 13). There they go through the companion's
// accessibility service instead: taps, swipes, drags and pinches as
// gestures Android plays on that display (dispatchGesture with
// setDisplayId, Android 11), back as the service's global action while
// that display has the phone's focus, and enter as the focused field's
// action key.
const (
	// InputAuto picks: the companion on MIUI and HyperOS, scrcpy elsewhere.
	InputAuto = ""
	// InputScrcpy injects through scrcpy.
	InputScrcpy = "scrcpy"
	// InputCompanion plays input through the companion
	// (companion.FeatureDisplayInput).
	InputCompanion = "companion"
)

// dropsDisplayInput reports whether the phone's system is known to drop
// what adb injects into a virtual display: Xiaomi's MIUI and HyperOS.
func (d *Device) dropsDisplayInput() bool { return d.props.XiaomiROM }

// displayInput decides whether a new background display takes its input
// through the companion, and checks that the companion can: an error
// when the phone drops adb's input there and the companion cannot stand
// in, so the task runs on the phone's own screen instead.
func (d *Device) displayInput(ctx context.Context, input string) (bool, error) {
	switch input {
	case InputScrcpy:
		return false, nil
	case InputCompanion:
	case InputAuto:
		if !d.dropsDisplayInput() {
			return false, nil
		}
	default:
		return false, fmt.Errorf("android: unknown display input %q", input)
	}
	if d.props.SDK > 0 && d.props.SDK < 30 {
		return false, fmt.Errorf("android: taps on a background display through Pinky on the phone need Android 11: %w", driver.ErrUnsupported)
	}
	c := d.companionClientFor(ctx, forBackground)
	if c != nil && d.comp.has(companion.FeatureDisplayInput) {
		return true, nil
	}
	why := "it is not answering"
	if c != nil {
		why = "the version on the phone is too old, and Pinky updates it once the phone is free"
	} else {
		d.comp.mu.Lock()
		if d.comp.reason != "" {
			why = d.comp.reason
		}
		d.comp.mu.Unlock()
	}
	return false, fmt.Errorf("android: this phone ignores taps that USB debugging sends to a background display, "+
		"and Pinky on the phone cannot tap there instead (%s): %w", why, driver.ErrUnsupported)
}

// inputClient returns the companion client when the display's input goes
// through it, nil when it goes through scrcpy, and an error when it should
// go through the companion but its service does not answer.
func (b *Background) inputClient(ctx context.Context) (*companion.Client, error) {
	if !b.viaCompanion {
		return nil, nil
	}
	c := b.d.companionClientFor(ctx, forBackground)
	if c == nil || !b.d.comp.has(companion.FeatureDisplayInput) {
		return nil, fmt.Errorf("android: input on display %d goes through Pinky on the phone, whose service is not answering", b.display)
	}
	return c, nil
}

// inputErr turns the companion's refusal of input into the driver's words.
func inputErr(err error) error {
	switch {
	case err == nil:
		return nil
	case companion.IsCode(err, companion.CodeCancelled):
		return fmt.Errorf("android: the gesture was cancelled before it ended: %w", err)
	case companion.IsCode(err, companion.CodeOwnUI):
		return fmt.Errorf("android: the point is on Pinky's own controls: %w", err)
	}
	return err
}

// gestureVia plays strokes on the display through the companion.
func (b *Background) gestureVia(ctx context.Context, c *companion.Client, strokes ...companion.Stroke) error {
	return b.l.input(func() error { return inputErr(c.Gesture(ctx, b.display, strokes...)) })
}

// touchVia moves a finger through the companion: down holds it, move
// moves the held finger there and keeps it down, up lifts it there.
func (b *Background) touchVia(ctx context.Context, c *companion.Client, a driver.TouchAction, p screen.Point) error {
	seg := []companion.Segment{{Points: []screen.Point{p}, Duration: time.Millisecond}}
	var opt companion.SegmentOptions
	switch a {
	case driver.TouchDown:
		opt.Hold = true
	case driver.TouchMove:
		seg[0].Duration = touchMoveTime
		opt.Continue, opt.Hold = true, true
	case driver.TouchUp:
		opt.Continue = true
	default:
		return fmt.Errorf("android: touch action %q: %w", a, driver.ErrUnsupported)
	}
	return b.l.input(func() error { return inputErr(c.Segments(ctx, b.display, seg, opt)) })
}

// touchMoveTime is how long a finger the companion holds takes to move to
// the next point of a touch.
const touchMoveTime = 16 * time.Millisecond

// dragVia plays a drag through the companion, as segments of one finger
// that does not lift: a rest for Hold, a move to each stop at an even
// speed and a rest for its Pause. With replan the finger stays down at
// g.Stops[at] while replan runs, then goes on (driver.DragReplanner).
func (b *Background) dragVia(ctx context.Context, c *companion.Client, g driver.Drag, at int, replan func(context.Context) []driver.DragStop) error {
	first, rest := g, []driver.DragStop(nil)
	if replan != nil && at >= 0 && at < len(g.Stops) {
		first.Stops, rest = g.Stops[:at+1], g.Stops[at+1:]
	} else {
		replan = nil
	}
	return b.l.input(func() error {
		if err := c.Segments(ctx, b.display, dragSegments(first, true), companion.SegmentOptions{Hold: replan != nil}); err != nil {
			return inputErr(err)
		}
		if replan == nil {
			return nil
		}
		if stops := replan(ctx); stops != nil {
			rest = stops
		}
		segs := dragSegments(driver.Drag{From: first.To(), Stops: rest}, false)
		if err := ctx.Err(); err != nil {
			segs = nil
		}
		if len(segs) == 0 {
			segs = []companion.Segment{{Points: []screen.Point{first.To()}, Duration: time.Millisecond}}
		}
		err := c.Segments(context.WithoutCancel(ctx), b.display, segs, companion.SegmentOptions{Continue: true})
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return inputErr(err)
	})
}

// dragSegments is g as the companion's segments. The first segment starts
// at g.From when first is set; later ones go on from where the one before
// ends, so each names only where it goes.
func dragSegments(g driver.Drag, first bool) []companion.Segment {
	var out []companion.Segment
	start := first
	if g.Hold > 0 || (first && len(g.Stops) == 0) {
		out = append(out, companion.Segment{Points: []screen.Point{g.From}, Duration: max(g.Hold, tapHold)})
		start = false
	}
	for _, s := range g.Stops {
		pts := []screen.Point{s.Point}
		if start {
			pts = []screen.Point{g.From, s.Point}
			start = false
		}
		out = append(out, companion.Segment{Points: pts, Duration: max(s.Move, time.Millisecond)})
		if s.Pause > 0 {
			out = append(out, companion.Segment{Points: []screen.Point{s.Point}, Duration: s.Pause})
		}
	}
	return out
}

// pinchVia plays a pinch through the companion as one gesture of two
// strokes along g.
func (b *Background) pinchVia(ctx context.Context, c *companion.Client, g driver.Pinch) error {
	const n = 24
	dur := g.Duration
	if dur <= 0 {
		dur = defaultSwipe
	}
	s1 := companion.Stroke{Duration: dur}
	s2 := companion.Stroke{Duration: dur}
	for i := 0; i <= n; i++ {
		p, q := g.At(float64(i) / n)
		s1.Points = append(s1.Points, p)
		s2.Points = append(s2.Points, q)
	}
	return b.gestureVia(ctx, c, s1, s2)
}

// keyVia presses back or enter through the companion; other keys, back
// while another display has the phone's focus and enter where the
// companion finds no field go through scrcpy. A phone that drops adb's
// input ignores those, so the agent gets a note that the key may not have
// arrived (driver.InputNoter).
func (b *Background) keyVia(ctx context.Context, c *companion.Client, k driver.Key, code int32) error {
	action := ""
	switch k {
	case driver.KeyBack:
		action = "back"
	case driver.KeyEnter:
		action = "enter"
	}
	return b.l.input(func() error {
		var refused error
		if action != "" {
			err := c.Global(ctx, b.display, action)
			var ce *companion.Error
			if !errors.As(err, &ce) || ce.Code == companion.CodeOwnUI {
				return inputErr(err) // done, or refused for good, or the connection failed
			}
			refused = err
			b.d.cfg.logf("android: %s: %s on display %d through the companion: %v; sending it through scrcpy", b.d.info.ID, action, b.display, err)
		}
		if b.d.dropsDisplayInput() {
			b.d.setInputNote(droppedKeyNote(k, refused))
		}
		return b.l.key(code, 0)
	})
}

// Notes for the agent about a key that went through scrcpy to a background
// display of a phone that drops what adb injects there (driver.InputNoter).
const (
	noteBackUnfocused = "Back may not have reached the app: the phone's own screen was used last and has the focus, and this phone drops keys sent to the background display you work on. If the screen did not change, tap the app's own back button."
	noteEnterNoField  = "Enter may not have reached the app: no text field took it, and this phone drops keys sent to the background display you work on. If the screen did not change, tap the field first, or tap the app's own button."
	noteKeyDropped    = "The key may not have reached the app: this phone drops keys sent to the background display you work on. If the screen did not change, use what is on the screen instead."
)

// droppedKeyNote is the note for key k that went through scrcpy on such a
// phone. refused is the companion's answer for back or enter, nil for a
// key the companion does not press.
func droppedKeyNote(k driver.Key, refused error) string {
	switch {
	case k == driver.KeyBack && companion.IsCode(refused, companion.CodeNotFocused):
		return noteBackUnfocused
	case k == driver.KeyEnter && refused != nil:
		return noteEnterNoField
	}
	return noteKeyDropped
}
