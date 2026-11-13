package ui

import (
	"time"

	mg "github.com/egoist/mygo/ui"
)

// The app's motion: short, calm changes, easing out, that show what
// changed. A page, a Settings section or a step of Add phone fades in
// where the last one was; a card or a row arrives and leaves; a fill
// follows the pointer. Nothing bounces or loops for decoration, and
// nothing waits: what arrives takes clicks from its first frame, so an
// approval's buttons and Stop answer as soon as they show.
//
// While the system asks for less motion (Preferences.ReduceMotion), MyGo
// shows moves at once and only fades, in place.
const (
	motionQuick = 120 * time.Millisecond // a fill under the pointer
	motionShort = 160 * time.Millisecond // a page, a section, a step
	motionMove  = 200 * time.Millisecond // a card or a row coming and going
)

// stillMotion shows every change at once. Tests set it: their clock
// stands still until they move it, which would leave what arrives
// halfway in their pictures and checks.
var stillMotion bool

// motionFor returns d, or the negative duration that shows a change at
// once while stillMotion is set.
func motionFor(d time.Duration) time.Duration {
	if stillMotion {
		return -1
	}
	return d
}

// fillFade fades e's fill and border as hover and press change them.
func fillFade(e mg.Element) mg.Element {
	return e.Transition(mg.ElementTransition{Colors: true, Duration: motionFor(motionQuick)})
}

// swapIn fades in content that takes the place of other content, rising
// a little: a page, a Settings section, a step of Add phone. Key e by
// what it shows. What it replaces goes at once, so the two never show
// together.
func swapIn() mg.ElementTransition {
	return mg.ElementTransition{
		Colors:       true,
		Duration:     motionFor(motionShort),
		Enter:        &mg.Motion{Y: 4},
		ExitDuration: -1,
	}
}

// cardIn is a card that arrives in a list, as an approval in Needs you:
// it fades in, coming down a little to its place, while the cards after
// it slide down to make room; it collapses as it goes, and they close up.
func cardIn() mg.ElementTransition {
	return mg.ElementTransition{
		Position: true,
		Duration: motionFor(motionMove),
		Enter:    &mg.Motion{Y: -6},
		Exit:     &mg.Motion{Collapse: true},
	}
}

// rowIn is a row that joins or leaves a list, as a step of a task or a
// phone in the sidebar: it grows in and collapses out, fading, and the
// rows after it move with it.
func rowIn() mg.ElementTransition {
	return mg.ElementTransition{
		Position: true,
		Duration: motionFor(motionMove),
		Enter:    &mg.Motion{Collapse: true},
		Exit:     &mg.Motion{Collapse: true},
	}
}

// listRow is rowIn for a row whose fill also follows the pointer, as a
// phone's in the sidebar.
func listRow() mg.ElementTransition {
	t := rowIn()
	t.Colors = true
	return t
}

// slide moves e to its new place, as a section under a list that grew.
func slide() mg.ElementTransition {
	return mg.ElementTransition{Position: true, Duration: motionFor(motionMove)}
}

// bannerIn is a banner over the page that comes and goes, as You're in
// control above the phone: it fades in, coming down a little.
func bannerIn() mg.ElementTransition {
	return mg.ElementTransition{
		Colors:       true,
		Duration:     motionFor(motionMove),
		Enter:        &mg.Motion{Y: -6},
		ExitDuration: -1,
	}
}

// colorTo moves e's color under key to col over a short fade, as a
// status dot changing from working to waiting.
func colorTo(e mg.Element, key any, col mg.Color) mg.Color {
	if stillMotion {
		return col
	}
	return e.AnimateColor(key, col, mg.Animation{Duration: motionMove, Oklab: true})
}

// textSwap fades a short text between its old and new words, as a
// status line does when the status changes.
func textSwap(e mg.Element) mg.Element {
	if stillMotion {
		return e
	}
	return e.ContentTransition(mg.ContentTransition{Animation: &mg.Animation{Duration: motionMove}})
}
