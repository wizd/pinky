package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/webapproval"
)

// WebApprovals lets the person answer approvals in a browser on their
// phone, through the web mirror and Tailscale (internal/webapproval). A
// phone is added only here: its browser opens a link made here, makes a
// passkey and shows a code, and the person allows it here after comparing
// the code. Everything is kept in memory only.
type WebApprovals interface {
	// NewLink makes the token of the link that adds a phone, good once
	// and for a few minutes.
	NewLink() string
	// Requests are the browsers waiting for the person to allow them.
	Requests() []webapproval.Request
	// Allow adds a request's browser; device is the phone connected to
	// Pinky it runs on, "" for another device.
	Allow(id, device string) error
	Deny(id string)
	// Phones are the browsers that may answer approvals.
	Phones() []webapproval.Phone
	Remove(id string)
}

// webApprovalsLinkLife is how long the link that adds a phone works.
const webApprovalsLinkLife = 5 * time.Minute

// webApprovalsSettings is the Answer approvals in the browser part of
// Settings › Phones › Web mirror; tn says where Tailscale serves the
// mirror.
func (a *app) webApprovalsSettings(c *mg.Context, tn RemoteTailnet) {
	wa := a.opt.WebApprovals
	if wa == nil {
		return
	}
	p := pal(c)
	st := &a.settings
	a.toggleRow(c, "web-approvals", locales.WebApprovalsToggle(), locales.WebApprovalsDesc(), false, &st.WebApprovals)
	if !st.WebApprovals {
		return
	}
	if tn.URL == "" {
		mg.Text(c, locales.WebApprovalsNeedsTailnet()).FontSize(fsSmall).TextColor(p.ink2).Margin(0, sp4, sp2, sp4)
		return
	}
	r := &a.webMirror
	reqs := wa.Requests()
	if len(reqs) > 0 || time.Since(r.approvalLinkAt) >= webApprovalsLinkLife {
		// Used, or too old to work.
		r.approvalLink = ""
	}
	settingRow(c.Key("wa-add"), locales.WebApprovalsAdd(), locales.WebApprovalsAddDesc(), false, func() {
		if button(c.Key("wa-new"), locales.WebApprovalsAddButton(), btnOpt{small: true}).Clicked() {
			r.approvalLink, r.approvalLinkAt = wa.NewLink(), time.Now()
		}
	})
	if r.approvalLink != "" {
		// The token is a secret: never a key, and only after "#" on screen.
		link := tn.URL + "approvals#pair=" + r.approvalLink
		settingRow(c.Key("wa-link"), locales.WebApprovalsLink(), locales.WebApprovalsLinkDesc(), false, func() {
			mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
				mg.Text(c, tn.URL+"approvals#…").Font("monospace").FontSize(12.5)
				if button(c.Key("wa-copy"), "", btnOpt{small: true, icon: icCopy, label: locales.WebApprovalsCopyLink()}).Clicked() {
					a.copy(c, link, locales.WebMirrorCopiedLink())
				}
			})
		})
	}
	picks := a.webApprovalsPicks()
	for _, q := range reqs {
		a.webApprovalsRequest(c, wa, q, picks)
	}
	phones := wa.Phones()
	if len(phones) == 0 {
		settingRow(c.Key("wa-phones"), locales.WebApprovalsPhones(), locales.WebApprovalsNoPhones(), false, nil)
	}
	for _, ph := range phones {
		kc := c.Key("wa-phone-" + ph.ID)
		desc := locales.WebApprovalsPhone(webApprovalsBrowser(ph.Browser), ph.Login)
		if ph.Device != "" {
			name := ph.Device
			if d := a.device(ph.Device); d != nil {
				name = d.Info.Name
			}
			desc = locales.WebApprovalsPhoneOn(webApprovalsBrowser(ph.Browser), ph.Login, name)
		}
		settingRow(kc, locales.WebApprovalsPhones(), desc, false, func() {
			if button(kc.Key("remove"), locales.WebApprovalsRemove(), btnOpt{small: true}).Clicked() {
				wa.Remove(ph.ID)
				c.Toast(locales.WebApprovalsRemoved())
			}
		})
	}
	// Requests come from the phone.
	c.After(time.Second)
}

// webApprovalsPick is a choice in the menu of the phone a browser runs on.
type webApprovalsPick struct {
	label    string
	device   string // "" for another device
	platform driver.Platform
}

// webApprovalsOther is what Settings keeps when the person says a browser
// runs on another device; a device id is never empty.
const webApprovalsOther = "\x00other"

// webApprovalsPicks are the phones connected to Pinky, then Another
// device; labels are told apart when two phones share a name.
func (a *app) webApprovalsPicks() []webApprovalsPick {
	var out []webApprovalsPick
	seen := map[string]int{}
	for _, d := range a.devices {
		label := d.Info.Name
		if label == "" {
			label = d.Info.ID
		}
		if seen[label]++; seen[label] > 1 {
			label = fmt.Sprintf("%s (%d)", label, seen[label])
		}
		out = append(out, webApprovalsPick{label: label, device: d.Info.ID, platform: d.Info.Platform})
	}
	return append(out, webApprovalsPick{label: locales.WebApprovalsOther()})
}

func webApprovalsBrowser(b string) string {
	if b == "" {
		return locales.WebApprovalsUnknownBrowser()
	}
	return b
}

// webApprovalsOtherWarning is the warning when the person says a browser
// runs on another device but it says it runs on an iPhone or an Android
// phone and one is connected: it may be that phone after all.
func webApprovalsOtherWarning(browser string, picks []webApprovalsPick) string {
	has := func(pl driver.Platform) bool {
		for _, pk := range picks {
			if pk.device != "" && pk.platform == pl {
				return true
			}
		}
		return false
	}
	switch {
	case strings.HasPrefix(browser, "iPhone") && has(driver.IOS):
		return locales.WebApprovalsOtherIPhone()
	case strings.HasPrefix(browser, "Android") && has(driver.Android):
		return locales.WebApprovalsOtherAndroid()
	}
	return ""
}

// webApprovalsRequest is a browser asking to answer approvals: the code
// to compare, large, which phone the browser runs on, and Deny and Allow.
// Allow shows once the person says which phone. The choice is kept by
// device id, never by its label.
func (a *app) webApprovalsRequest(c *mg.Context, wa WebApprovals, q webapproval.Request, picks []webApprovalsPick) {
	p := pal(c)
	r := &a.webMirror
	if r.approvalPick == nil {
		r.approvalPick = map[string]string{}
	}
	code := q.Code
	if len(code) == 6 {
		code = code[:3] + " " + code[3:]
	}
	// The kept choice, if that phone is still connected.
	chosen, picked := r.approvalPick[q.ID]
	var pick *webApprovalsPick
	for i := range picks {
		if picked && (picks[i].device == chosen || picks[i].device == "" && chosen == webApprovalsOther) {
			pick = &picks[i]
		}
	}
	if picked && pick == nil {
		// That phone went away: ask again.
		delete(r.approvalPick, q.ID)
	}
	kc := c.Key("wa-req-" + q.ID)
	mg.Row(kc).Gap(sp2).AlignItems(mg.Start).Padding(12, sp3).Radius(rControl).Background(p.amberTint).Border(1, p.amberLine).
		Margin(0, sp4, sp2, sp4).Role(mg.RoleStatus).Children(func() {
		mg.Icon(kc, icAlert).FontSize(iconBody).TextColor(p.amber).Margin(2, 0, 0, 0)
		mg.Column(kc).Gap(6).Grow(1).MinWidth(0).Children(func() {
			mg.Text(kc, locales.WebApprovalsRequest()).FontWeight(fwMedium)
			mg.Text(kc, locales.WebApprovalsRequestDesc(webApprovalsBrowser(q.Browser), q.Login)).FontSize(fsSmall).LineHeight(1.4)
			mg.Text(kc, code).Font("monospace").FontSize(28).FontWeight(fwSemibold).TextColor(p.ink)
			mg.Row(kc).Gap(8).AlignItems(mg.Center).Wrap().Children(func() {
				mg.Text(kc, locales.WebApprovalsWhich()).FontSize(fsSmall).FontWeight(fwMedium)
				labels := []string{locales.WebApprovalsChoose()}
				for _, pk := range picks {
					labels = append(labels, pk.label)
				}
				sel := labels[0]
				if pick != nil {
					sel = pick.label
				}
				if mg.Select(kc.Key("which"), &sel, labels).Label(locales.WebApprovalsWhich()).Changed() {
					delete(r.approvalPick, q.ID)
					if i := slices.Index(labels, sel); i > 0 {
						dev := picks[i-1].device
						if dev == "" {
							dev = webApprovalsOther
						}
						r.approvalPick[q.ID] = dev
					}
				}
			})
			mg.Text(kc, locales.WebApprovalsWhichDesc()).FontSize(fsCaption).TextColor(p.ink2).LineHeight(1.4)
			if pick != nil && pick.device == "" {
				if warn := webApprovalsOtherWarning(q.Browser, picks); warn != "" {
					mg.Text(kc, warn).FontSize(fsSmall).TextColor(p.amber).LineHeight(1.4)
				}
			}
			mg.Row(kc).Gap(8).Justify(mg.End).Children(func() {
				if button(kc.Key("deny"), locales.WebApprovalsDeny(), btnOpt{small: true}).Clicked() {
					wa.Deny(q.ID)
					delete(r.approvalPick, q.ID)
				}
				if pick == nil {
					return
				}
				if button(kc.Key("allow"), locales.WebApprovalsAllow(), btnOpt{small: true, kind: btnPrimary}).Clicked() {
					err := wa.Allow(q.ID, pick.device)
					delete(r.approvalPick, q.ID)
					switch {
					case errors.Is(err, webapproval.ErrTooMany):
						c.Toast(locales.WebApprovalsTooMany(webapproval.MaxPhones))
					case errors.Is(err, webapproval.ErrNoDevice):
						c.Toast(locales.WebApprovalsNoDevice())
					}
				}
			})
		})
	})
}
