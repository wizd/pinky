package ui

import (
	"math"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
)

// A phone's own window shows only its live mirror, with its keys under it,
// for using the phone from the computer as macOS's iPhone Mirroring does
// (issue 122), for iPhones, iPads and Android phones alike. It opens from
// the ⋯ menu under the mirror and from the phone's right-click menu; opened
// again, it comes to the front.
//
// The window shows the phone's mirror itself: the one the phone's page and
// its tile show, with its one stream from the gateway, its one decoder and
// its one bitmap, which each window's renderer uploads for itself. Input
// goes through mirrorInput, as on the page: a touch takes over and pauses
// the agent. The mirror stays open while the window shows it, also with
// the main window hidden, minimized or on another page (inWindow).
//
// The screen fills the window but for the keys under it, at its own
// aspect: the window keeps that shape, settling to it after the person
// resizes it and turning with the phone. The control banner shrinks into
// the bar of keys, beside Hand back, so that it never covers the screen nor
// moves it under the person's finger; the lock and the agent's overlay
// show on the screen as on the page. A phone that goes offline, pauses or
// waits for a step shows that in the window, which closes when the phone
// is removed. Keep on top is the person's choice, off at first.

// phoneWin is a phone's own window and what it keeps.
type phoneWin struct {
	win    *mygo.Window // nil in tests, which have no windows
	mirror *mirror      // the mirror it shows, nil while the phone is not live
	aspect float32      // the screen's aspect the window is shaped for
	last   [2]int       // the content size it last had the window take
	resize int          // counts the person's resizes, to settle after the last
	title  string
}

// The window's parts: winPad around the screen, but under it, where the
// bar of keys is winBar high; the screen's height in a new window, at most;
// and how long the window waits after the person resizes it before it
// takes the screen's shape again.
const (
	winPad     = 8
	winBar     = 44
	winKey     = 32
	winScreenH = 640
	winSettle  = 400 * time.Millisecond
)

// inWindow reports whether a phone's own window shows m.
func (a *app) inWindow(m *mirror) bool {
	pw := a.phoneWins[m.id]
	return pw != nil && pw.mirror == m
}

// openPhoneWindow opens the phone's own window, or brings it to the front.
func (a *app) openPhoneWindow(id string) {
	if pw := a.phoneWins[id]; pw != nil {
		if w := pw.win; w != nil && !w.IsDestroyed() {
			if w.IsMinimized() {
				w.Restore()
			}
			w.Show()
			w.Focus()
		}
		return
	}
	d := a.device(id)
	if d == nil {
		return
	}
	pw := &phoneWin{title: d.Info.Name, aspect: bezelOf(*d).defaultAspect()}
	if m := a.mirrors[id]; m != nil {
		pw.aspect = m.aspect(*d)
	}
	a.phoneWins[id] = pw
	if a.main == nil {
		return // tests have no windows
	}
	wa := mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint()).WorkArea
	w, h := phoneWindowSize(pw.aspect, float32(wa.Width), float32(wa.Height))
	pw.last = [2]int{w, h}
	pw.win = mygo.NewWindow(mygo.WindowOptions{
		Title: d.Info.Name, Width: w, Height: h, UseContentSize: true,
		MinWidth: 200, MinHeight: 240,
		// Where the person last put this phone's window, and its size,
		// which takes the screen's shape again if the phone turned since.
		StateKey:        "phone-" + id,
		AlwaysOnTop:     a.winTop[id],
		DisableMaximize: true,
		BackgroundColor: "light-dark(#f7f7f8, #212125)",
		Content:         mg.View(func(c *mg.Context) { a.phoneWindowView(c, id) }),
	})
	win := pw.win
	if runtime.GOOS != "darwin" {
		win.SetIcon(iconPNG())
	}
	win.OnResize(func() { a.phoneWindowResized(id, win) })
	win.OnClosed(func() {
		if pw := a.phoneWins[id]; pw != nil && pw.win == win {
			a.dropWindowMirror(pw)
			delete(a.phoneWins, id)
		}
	})
	a.post(func() { a.settlePhoneWindow(id, true) })
	win.Show()
	win.Focus()
}

// closePhoneWindow closes the phone's own window.
func (a *app) closePhoneWindow(id string) {
	pw := a.phoneWins[id]
	if pw == nil {
		return
	}
	if w := pw.win; w != nil && !w.IsDestroyed() {
		w.Close() // OnClosed lets it go
		return
	}
	a.dropWindowMirror(pw)
	delete(a.phoneWins, id)
}

// setWindowOnTop keeps the phone's own window on top of other apps' windows
// or lets it go behind them. The choice lasts while Pinky runs.
func (a *app) setWindowOnTop(id string, on bool) {
	a.winTop[id] = on
	if pw := a.phoneWins[id]; pw != nil && pw.win != nil && !pw.win.IsDestroyed() {
		pw.win.SetAlwaysOnTop(on)
	}
}

// dropWindowMirror lets the window's mirror go: closed now while the main
// window, hidden or minimized, shows no mirror; otherwise its next build
// keeps it or lets it go (sweepMirrors).
func (a *app) dropWindowMirror(pw *phoneWin) {
	m := pw.mirror
	pw.mirror = nil
	if m == nil {
		return
	}
	if a.main != nil && a.main.IsVisible() && !a.main.IsMinimized() {
		a.main.Invalidate()
		return
	}
	if a.mirrors[m.id] == m {
		m.close()
		delete(a.mirrors, m.id)
	}
}

// phoneWindowSize is a new window's content size for a screen of aspect
// (width over height) on a display whose work area is maxW × maxH: the
// screen winScreenH high at most, and no more than three quarters of the
// display's height or half its width, as a tablet turned sideways would.
func phoneWindowSize(aspect, maxW, maxH float32) (int, int) {
	if aspect <= 0 {
		aspect = 9.0 / 20
	}
	sh := float32(winScreenH)
	if maxH > 0 {
		sh = min(sh, 0.75*maxH-winPad-winBar)
	}
	sw := sh * aspect
	if maxW > 0 && sw > 0.5*maxW {
		sw = 0.5 * maxW
		sh = sw / aspect
	}
	return int(sw + 2*winPad + 0.5), int(sh + winPad + winBar + 0.5)
}

// fitPhoneWindow returns the content size closest to cw × ch that shows a
// screen of aspect whole, with nothing beside it: the width kept when
// byWidth, else the height.
func fitPhoneWindow(cw, ch int, aspect float32, byWidth bool) (int, int) {
	if byWidth {
		sw := max(float32(cw-2*winPad), 1)
		return cw, int(sw/aspect + winPad + winBar + 0.5)
	}
	sh := max(float32(ch-winPad-winBar), 1)
	return int(sh*aspect + 2*winPad + 0.5), ch
}

// turnPhoneWindow returns the content size for a screen of aspect in a
// window of cw × ch shaped for another: the screen keeps its area, so a
// phone that turns swaps the window's width and height, within maxW ×
// maxH.
func turnPhoneWindow(cw, ch int, aspect, maxW, maxH float32) (int, int) {
	sw, sh := float32(cw-2*winPad), float32(ch-winPad-winBar)
	area := max(sw*sh, 1)
	nw := float32(math.Sqrt(float64(area * aspect)))
	nh := nw / aspect
	if maxW > 0 && maxH > 0 {
		if s := min((0.9*maxW-2*winPad)/nw, (0.9*maxH-winPad-winBar)/nh); s < 1 {
			nw, nh = nw*s, nh*s
		}
	}
	return int(nw + 2*winPad + 0.5), int(nh + winPad + winBar + 0.5)
}

// phoneWindowResized has the window take the screen's shape a moment after
// the person stops resizing it.
func (a *app) phoneWindowResized(id string, win *mygo.Window) {
	pw := a.phoneWins[id]
	if pw == nil || pw.win != win {
		return
	}
	pw.resize++
	seq := pw.resize
	time.AfterFunc(winSettle, func() {
		a.post(func() {
			if pw := a.phoneWins[id]; pw != nil && pw.resize == seq {
				a.settlePhoneWindow(id, false)
			}
		})
	})
}

// settlePhoneWindow gives the window the screen's shape, keeping the side
// the person changed more since it last had it; at first, the width.
func (a *app) settlePhoneWindow(id string, first bool) {
	pw := a.phoneWins[id]
	if pw == nil || pw.win == nil || pw.win.IsDestroyed() || pw.win.IsFullScreen() || pw.win.IsMaximized() {
		return
	}
	cw, ch := pw.win.ContentSize()
	byWidth := first || absInt(cw-pw.last[0]) >= absInt(ch-pw.last[1])
	w, h := fitPhoneWindow(cw, ch, pw.aspect, byWidth)
	pw.last = [2]int{w, h}
	if absInt(w-cw) > 1 || absInt(h-ch) > 1 {
		pw.win.SetContentSize(w, h)
	}
}

// turnedPhoneWindow reshapes the window for a screen that turned.
func (a *app) turnedPhoneWindow(id string) {
	pw := a.phoneWins[id]
	if pw == nil || pw.win == nil || pw.win.IsDestroyed() || pw.win.IsFullScreen() {
		return
	}
	cw, ch := pw.win.ContentSize()
	x, y := pw.win.Position()
	wa := mygo.Screen.DisplayNearestPoint(mygo.Point{X: x + cw/2, Y: y + ch/2}).WorkArea
	w, h := turnPhoneWindow(cw, ch, pw.aspect, float32(wa.Width), float32(wa.Height))
	pw.last = [2]int{w, h}
	pw.win.SetContentSize(w, h)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// windowLive reports whether the phone's window shows its live screen: the
// phone is open and its own, neither paused nor offline, nor waiting for a
// step, nor moving between connections. Its page then shows the mirror too
// (phoneCenter).
func (a *app) windowLive(d gateway.DeviceView) bool {
	return !d.Paused && d.Status != gateway.DeviceConnecting && d.Status != gateway.DeviceOffline &&
		d.Info.State == driver.StateOnline && !a.helperRunning(d.Info.ID) && !lockedOnWiFi(d) &&
		!foundOnWiFi(d) && !lostOnWiFi(d) && !changedOnWiFi(d)
}

// phoneWindowView builds a phone's own window: its screen, and the bar of
// keys under it.
func (a *app) phoneWindowView(c *mg.Context, id string) {
	p := applyTheme(c)
	pw := a.phoneWins[id]
	if pw == nil {
		return
	}
	d := a.device(id)
	if d == nil {
		// The phone was removed: its window goes with it.
		a.post(func() { a.closePhoneWindow(id) })
		return
	}
	for _, msg := range a.toasts {
		c.Toast(msg)
	}
	if c.Shortcut(mg.Cmd, mg.KeyW) {
		a.post(func() { a.closePhoneWindow(id) })
	}
	if pw.title != d.Info.Name {
		pw.title = d.Info.Name
		if pw.win != nil {
			pw.win.SetTitle(d.Info.Name)
		}
	}
	live := a.windowLive(*d)
	if !live && pw.mirror != nil {
		a.dropWindowMirror(pw)
	}
	w, h := c.Size()
	mg.Column(c).Fill().Background(p.bg).Children(func() {
		mg.Box(c).Grow(1).MinHeight(0).Padding(winPad, winPad, 0, winPad).Center().Children(func() {
			room := [2]float32{max(w-2*winPad, 1), max(h-winPad-winBar, 1)}
			if live {
				a.phoneWindowScreen(c, pw, *d, room)
			} else {
				a.phoneWindowState(c, *d, room)
			}
		})
		a.phoneWindowBar(c, *d, live, w)
	})
}

// phoneWindowScreen is the phone's live screen in its window, as large as
// room allows at its aspect.
func (a *app) phoneWindowScreen(c *mg.Context, pw *phoneWin, d gateway.DeviceView, room [2]float32) {
	p := pal(c)
	id := d.Info.ID
	m := a.mirrorFor(id)
	pw.mirror = m
	t := a.taskOf(id)
	// The window shows the phone's own screen. While an agent works on a
	// background display, its steps are on that display, not here.
	shown := t
	if a.backgroundTask(d, t) {
		shown = nil
	}
	_, inControl := a.controlFor(d, t)
	aspect := m.aspect(d)
	if m.picW > 0 && math.Abs(float64(aspect-pw.aspect)) > 0.02 && !m.down && !m.scrolling {
		// The phone turned, or its picture came with another shape than
		// the window was made for: the window takes the new shape, not
		// under a finger that is down.
		pw.aspect = aspect
		a.post(func() { a.turnedPhoneWindow(id) })
	}
	_, _, sw, sh := fitBezel(aspect, room[0], room[1], 0)
	_, inner := bezelRadius(bezelOf(d), sw, sh, 0)
	fig := mg.Box(c.Key("win-phone-"+id)).Size(sw, sh).Shrink(0).Radius(inner).Background(p.phone)
	if inControl {
		fig.Shadow(0, 0, 0, 2, p.ink2) // the ring the phone's page shows while the person holds it
	}
	fig.Label(locales.PhoneScreenLabel(d.Info.Name)).Children(func() {
		a.mirrorView(c, m, d, shown, inner, onWindow)
	})
}

// phoneWindowState is what the window shows while the phone has no live
// screen: offline, paused, connecting or waiting for a step, in the phone's
// shape, with Show in Pinky, which opens its page.
func (a *app) phoneWindowState(c *mg.Context, d gateway.DeviceView, room [2]float32) {
	p := pal(c)
	ic, title := icAlert, locales.PhoneNeedsSetup(d.Info.Name)
	connecting := false
	switch {
	case d.Paused:
		ic, title = icPause, locales.PhonePausedTitle(d.Info.Name)
	case d.Status == gateway.DeviceConnecting || a.helperRunning(d.Info.ID):
		connecting, title = true, locales.PhoneConnectingTitle(d.Info.Name)
	case d.Status == gateway.DeviceOffline || d.Info.State == driver.StateOffline:
		ic, title = offlineIcon(d), locales.PhoneOffline(d.Info.Name)
	case lockedOnWiFi(d):
		ic, title = icLock, locales.PhoneLockedWiFiTitle(d.Info.Name)
	}
	_, _, sw, sh := fitBezel(bezelOf(d).defaultAspect(), room[0], room[1], 0)
	_, inner := bezelRadius(bezelOf(d), sw, sh, 0)
	mg.Column(c).Size(sw, sh).Shrink(0).Radius(inner).Border(1, p.line2).Background(p.bg2).
		Center().Gap(sp3).Padding(sp4).Role(mg.RoleStatus).Children(func() {
		if connecting {
			spinner(c)
		} else {
			mg.Icon(c, ic).FontSize(iconHero).TextColor(p.ink3)
		}
		mg.Text(c, title).FontSize(fsBody).FontWeight(fwSemibold).TextAlign(mg.Center).LineHeight(1.4)
		if button(c, locales.PhoneWindowShowInPinky(), btnOpt{small: true}).Clicked() {
			a.selectPhone(d.Info.ID)
			a.showMain()
		}
	})
}

// phoneWindowBar is the bar under the phone's screen in its window, width
// wide: the phone's keys, the control banner made small while the person
// holds the phone, with Hand back, and Keep on top.
func (a *app) phoneWindowBar(c *mg.Context, d gateway.DeviceView, live bool, width float32) {
	p := pal(c)
	id := d.Info.ID
	mg.Row(c.Key("win-bar-"+id)).Height(winBar).Shrink(0).Padding(0, winPad-2).Gap(2).AlignItems(mg.Center).Children(func() {
		var want controlText
		if live {
			a.phoneKeys(c, d, winKey, "")
			want, _ = a.controlFor(d, a.taskOf(id))
		}
		if want != (controlText{}) {
			// The banner's bold start says who has the phone, where the bar
			// has room for it whole; all its words are its tooltip and its
			// label.
			words := strings.TrimSpace(want.bold + want.rest)
			bold := strings.TrimSpace(want.bold)
			keys := float32(1)
			if d.Info.Platform != driver.IOS {
				keys = 3
			}
			room := width - 2*(winPad-2) - (keys+1)*(winKey+2) - buttonWidth(c, locales.PhoneHandBack(), true) - sp1 - iconBody - 6 - 2*2
			tw, _ := c.MeasureText(0, mg.Span{Text: bold, Size: fsSmall, Weight: fwSemibold})
			mg.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(mg.Center).Margin(0, 0, 0, sp1).
				Role(mg.RoleStatus).Label(words).Tooltip(words).Children(func() {
				mg.Icon(c, icHand).FontSize(iconBody).TextColor(p.ink2).Shrink(0)
				if tw <= room {
					mg.Text(c, bold).FontSize(fsSmall).FontWeight(fwSemibold).SingleLine().MinWidth(0)
				}
			})
			if button(c.Key("win-hand-"+id), locales.PhoneHandBack(), btnOpt{kind: btnPrimary, small: true}).Clicked() {
				a.handBack(id)
			}
		} else {
			mg.Spacer(c)
		}
		on := a.winTop[id]
		label, ic := locales.PhoneWindowOnTop(), icPin
		if on {
			label, ic = locales.PhoneWindowOnTopOff(), icPinOn
		}
		b := mg.ButtonBase(c.Key("win-top-"+id)).Size(winKey, winKey-4).Radius(rControl).Center().Label(label).Tooltip(label)
		col := p.ink2
		if b.Hovered() {
			b.Background(p.bg3)
			col = p.ink
		}
		if on {
			col = p.ink
		}
		if b.Clicked() {
			a.setWindowOnTop(id, !on)
		}
		b.TextColor(col).Children(func() { mg.Icon(c, ic).FontSize(iconBody) })
	})
}
