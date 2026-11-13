package ui

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/webmirror"
)

// WebMirror shows the phones' screens in a web browser: on this computer
// at webmirror.URL, and on the person's other devices through `tailscale
// serve`, which the person runs. Each link carries one of the mirror's
// tokens, kept in memory only: Token's can only watch, ControlToken's can
// also use the phone, and is "" unless Settings.WebMirrorControl is on.
type WebMirror interface {
	Token() string
	ControlToken() string
	// NewToken replaces both tokens, cutting off every browser.
	NewToken()
	// Watchers are the browsers that watch now.
	Watchers() []webmirror.Watcher
	// Err says why the mirror could not turn on.
	Err() error
	// Tailnet says where `tailscale serve` gives the mirror to the
	// tailnet; URL is the page without its token.
	Tailnet(ctx context.Context) RemoteTailnet
}

// webMirrorState is Settings › Phones › Web mirror's.
type webMirrorState struct {
	tn     RemoteTailnet
	tnAt   time.Time
	tnBusy bool
	// approvalLink is the token of the link that adds a phone to answer
	// approvals (webapprovals.go), made at approvalLinkAt; in memory only.
	approvalLink   string
	approvalLinkAt time.Time
	// approvalPick is, by request, the phone the person said the
	// browser runs on: a menu label (webApprovalsPicks).
	approvalPick map[string]string
}

// webMirrorServe is the command that has Tailscale give the web mirror to
// the tailnet over HTTPS at https, a port nothing else is served on.
func webMirrorServe(https string) string {
	_, port, _ := net.SplitHostPort(webmirror.Addr)
	return "tailscale serve --bg --https=" + https + " " + port
}

// webMirrorTailnet returns what Tailscale last said about the web mirror,
// and reads it again off the main thread when it is old.
func (a *app) webMirrorTailnet(c *mg.Context, wm WebMirror) RemoteTailnet {
	r := &a.webMirror
	if !r.tnBusy && time.Since(r.tnAt) >= tailnetEveryRemote {
		r.tnBusy = true
		go func() {
			ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
			defer cancel()
			tn := wm.Tailnet(ctx)
			a.post(func() { r.tn, r.tnAt, r.tnBusy = tn, time.Now(), false })
		}()
	}
	c.After(tailnetEveryRemote) // also shows how many watch
	return r.tn
}

// webMirrorSettings is the Web mirror part of Settings › Phones.
func (a *app) webMirrorSettings(c *mg.Context) {
	wm := a.opt.WebMirror
	if wm == nil {
		return
	}
	p := pal(c)
	st := &a.settings
	mg.Column(c).Gap(10).Label(locales.WebMirrorTitle()).Children(func() {
		settingsHeading(c, locales.WebMirrorTitle(), locales.WebMirrorIntro())
		group(c).Children(func() {
			a.toggleRow(c, "web-mirror", locales.WebMirrorToggle(), "", !st.WebMirror, &st.WebMirror)
			if !st.WebMirror {
				return
			}
			if err := wm.Err(); err != nil {
				mg.Text(c, locales.WebMirrorFailed(err.Error())).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus).Margin(0, sp4, sp2, sp4)
			}
			mg.Row(c).Gap(sp2).AlignItems(mg.Start).Padding(10, sp3).Radius(rControl).Background(p.amberTint).Border(1, p.amberLine).Margin(0, sp4, sp2, sp4).
				Children(func() {
					mg.Icon(c, icAlert).FontSize(iconBody).TextColor(p.amber)
					mg.Text(c, locales.WebMirrorWarning()).FontSize(fsSmall).LineHeight(1.4).Grow(1).MinWidth(0)
				})
			tn := a.webMirrorTailnet(c, wm)
			a.webMirrorLinks(c.Key("view"), tn, wm.Token(), locales.WebMirrorLink(), locales.WebMirrorTailnetLink(), locales.WebMirrorLinkDesc(), locales.WebMirrorTailnetLinkDesc())
			if tn.URL == "" && tn.Name != "" && tn.ServePort != "" {
				url := "https://" + tn.Name + ":" + tn.ServePort + "/"
				a.commandBlock(c, locales.WebMirrorTailnetTitle(), locales.WebMirrorTailnetHint(url), webMirrorServe(tn.ServePort),
					locales.WebMirrorTailnetCommandLabel(), locales.RemoteCopyCommand(), false)
			}
			a.toggleRow(c, "web-mirror-control", locales.WebMirrorControlToggle(), locales.WebMirrorControlDesc(), false, &st.WebMirrorControl)
			if st.WebMirrorControl {
				mg.Row(c).Gap(sp2).AlignItems(mg.Start).Padding(10, sp3).Radius(rControl).Background(p.amberTint).Border(1, p.amberLine).Margin(0, sp4, sp2, sp4).
					Children(func() {
						mg.Icon(c, icAlert).FontSize(iconBody).TextColor(p.amber)
						mg.Text(c, locales.WebMirrorControlWarning()).FontSize(fsSmall).LineHeight(1.4).Grow(1).MinWidth(0)
					})
				a.webMirrorLinks(c.Key("control"), tn, wm.ControlToken(), locales.WebMirrorControlLink(), locales.WebMirrorControlTailnetLink(),
					locales.WebMirrorControlLinkDesc(), locales.WebMirrorControlTailnetLinkDesc())
			}
			a.webApprovalsSettings(c, tn)
			settingRow(c, locales.WebMirrorNewLink(), locales.WebMirrorNewLinkDesc(), true, func() {
				mg.Row(c).Gap(sp3).AlignItems(mg.Center).Children(func() {
					watching := webMirrorWatching(wm.Watchers(), "", false)
					if watching == "" {
						watching = locales.WebMirrorNoViewers()
					}
					mg.Text(c, watching).FontSize(fsSmall).TextColor(p.ink2)
					if button(c, locales.WebMirrorNewLinkButton(), btnOpt{small: true}).Clicked() {
						wm.NewToken()
						c.Toast(locales.WebMirrorNewLinkDone())
					}
				})
			})
		})
	})
}

// webMirrorLinks are the rows of a link to the mirror with token: on this
// computer, with Open and Copy, and through the tailnet while `tailscale
// serve` gives the mirror to it. c is keyed apart for each kind of link;
// the token, a secret, is never a key.
func (a *app) webMirrorLinks(c *mg.Context, tn RemoteTailnet, token, localTitle, tailnetTitle, localDesc, tailnetDesc string) {
	if token == "" {
		return
	}
	local := webmirror.URL()
	settingRow(c, localTitle, localDesc, false, func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Text(c, local+"#…").Font("monospace").FontSize(12.5)
			if button(c.Key("open"), locales.WebMirrorOpen(), btnOpt{small: true}).Clicked() {
				openURL(c, local+"#"+token, func(error) {})
			}
			if button(c.Key("copy"), "", btnOpt{small: true, icon: icCopy, label: locales.WebMirrorCopyLink()}).Clicked() {
				a.copy(c, local+"#"+token, locales.WebMirrorCopiedLink())
			}
		})
	})
	if tn.URL == "" {
		return
	}
	settingRow(c, tailnetTitle, tailnetDesc, false, func() {
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			mg.Text(c, tn.URL+"#…").Font("monospace").FontSize(12.5)
			if button(c.Key("copy-tn"), "", btnOpt{small: true, icon: icCopy, label: locales.WebMirrorCopyTailnetLink()}).Clicked() {
				a.copy(c, tn.URL+"#"+token, locales.WebMirrorCopiedLink())
			}
		})
	})
}

// webMirrorWatching says how many browsers watch, those of the phone
// device ("" for all), how many of them can use it, and with who, who
// they are on the tailnet: "" when none watch.
func webMirrorWatching(ws []webmirror.Watcher, device string, who bool) string {
	n, ctl := 0, 0
	var logins []string
	for _, w := range ws {
		if device != "" && w.Device != device {
			continue
		}
		n++
		if w.Control {
			ctl++
		}
		if who && w.Login != "" && !slices.Contains(logins, w.Login) {
			logins = append(logins, w.Login)
		}
	}
	if n == 0 {
		return ""
	}
	out := locales.WebMirrorViewers(n)
	if ctl > 0 {
		out += " · " + locales.WebMirrorControllers(ctl)
	}
	if len(logins) > 0 {
		out += " · " + strings.Join(logins, ", ")
	}
	return out
}

// webMirrorBadge shows, on a phone's row of the sidebar, that browsers
// watch it through the web mirror: an eye, or a hand when one can use the
// phone, with how many; who they are in its tooltip.
func (a *app) webMirrorBadge(c *mg.Context, device string) {
	wm := a.opt.WebMirror
	if wm == nil || !a.settings.WebMirror {
		return
	}
	ws := wm.Watchers()
	text := webMirrorWatching(ws, device, true)
	if text == "" {
		return
	}
	n, ic := 0, icEye
	for _, w := range ws {
		if w.Device == device {
			n++
			if w.Control {
				ic = icHand
			}
		}
	}
	p := pal(c)
	mg.Row(c).Gap(3).AlignItems(mg.Center).Padding(1, 6).Radius(99).Background(p.amberTint).TextColor(p.amber).
		Tooltip(text).Label(text).Children(func() {
		mg.Icon(c, ic).FontSize(iconMicro)
		mg.Text(c, strconv.Itoa(n)).FontSize(fsCaption).FontWeight(fwMedium)
	})
}
