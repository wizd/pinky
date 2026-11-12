package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
)

// VCorp: Pinky paired with a VCorp Super Agent account, whose cloud tasks
// then use the phones through Pinky, with the person approving here as for
// any agent. Settings › Agents pairs it with a code from the platform, shows
// whether the link is up and unpairs it.

// Vcorp pairs Pinky with a VCorp account and says how its link is. The
// client calls Status, Claim and Pair off the main thread.
type Vcorp interface {
	// Status is the pairing and the link's state.
	Status() VcorpStatus
	// Claim says which account a pairing code is for, and the platform's
	// address as Pinky will use it, without using the code up.
	Claim(ctx context.Context, origin, code string) (account, normOrigin string, err error)
	// Pair pairs with the code; Pinky links by itself from then on.
	Pair(ctx context.Context, origin, code string) error
	// Unpair forgets the pairing here; the link drops.
	Unpair() error
}

// VcorpStatus is the pairing and the link's state.
type VcorpStatus struct {
	Paired    bool
	Account   string
	Origin    string
	Name      string // the name the platform shows for this Pinky
	Connected bool
	Err       string // why the link is down, after a drop
}

// DefaultVcorpOrigin is the platform the address field suggests.
const DefaultVcorpOrigin = "https://maker.vcorp.ai"

// vcorpState is Settings › Agents › VCorp's.
type vcorpState struct {
	origin, code string // being typed
	account      string // the code's account, once checked: the confirm step
	normOrigin   string // the address it was checked at
	busy         bool
	err          string
	unpairAsk    bool // Unpair asks once more
	gen          int  // new fields after pairing or cancelling

	st      VcorpStatus // read off the main thread
	at      time.Time
	reading bool
	stGen   int // a read begun before a change is stale
}

const vcorpEvery = 2 * time.Second

// vcorpStatus returns the last status read, and reads it again off the main
// thread when it is old.
func (a *app) vcorpStatus(c *mg.Context) VcorpStatus {
	v := &a.vcorp
	if !v.reading && time.Since(v.at) >= vcorpEvery {
		v.reading = true
		gen := v.stGen
		go func() {
			st := a.opt.Vcorp.Status()
			a.post(func() {
				v.reading = false
				if gen == v.stGen {
					v.st, v.at = st, time.Now()
				}
			})
		}()
	}
	c.After(vcorpEvery)
	return v.st
}

// vcorpRefresh reads the status again at the next frame.
func (a *app) vcorpRefresh() { a.vcorp.at = time.Time{}; a.vcorp.stGen++ }

// vcorpAgents is the VCorp part of Settings › Agents.
func (a *app) vcorpAgents(c *mg.Context) {
	if a.opt.Vcorp == nil {
		return
	}
	p := pal(c)
	v := &a.vcorp
	st := a.vcorpStatus(c)
	mg.Column(c).Gap(10).Label(locales.VcorpTitle()).Children(func() {
		settingsHeading(c, locales.VcorpTitle(), locales.VcorpIntro())
		group(c).Children(func() {
			switch {
			case st.Paired:
				a.vcorpPaired(c, st)
			case v.account != "":
				a.vcorpConfirm(c)
			default:
				a.vcorpForm(c)
			}
		})
		if v.err != "" {
			mg.Text(c, v.err).FontSize(fsSmall).TextColor(p.red).Role(mg.RoleStatus)
		}
	})
}

// vcorpForm asks for the platform's address and a pairing code.
func (a *app) vcorpForm(c *mg.Context) {
	v := &a.vcorp
	groupRow(c, mg.Column(c).Gap(sp3), true).Children(func() {
		rowWords(c, mg.Column(c), locales.VcorpPairTitle(), locales.VcorpPairDesc())
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Wrap().Children(func() {
			if v.origin == "" && v.gen == 0 {
				v.origin = DefaultVcorpOrigin
			}
			o := mg.TextInput(c.Key(fmt.Sprintf("vcorp-origin-%d", v.gen)), &v.origin).Label(locales.VcorpAddress()).
				Placeholder(DefaultVcorpOrigin).Width(230)
			code := mg.TextInput(c.Key(fmt.Sprintf("vcorp-code-%d", v.gen)), &v.code).Label(locales.VcorpCode()).
				Placeholder(locales.VcorpCodePlaceholder()).Width(150)
			ready := strings.TrimSpace(v.origin) != "" && strings.TrimSpace(v.code) != ""
			next := button(c, locales.VcorpNext(), btnOpt{small: true, kind: btnPrimary}).Disabled(v.busy || !ready)
			if (next.Clicked() || code.Submitted() || o.Submitted()) && !v.busy && ready {
				a.vcorpClaim()
			}
			if v.busy {
				spinner(c)
			}
		})
	})
}

// vcorpConfirm shows the code's account before pairing.
func (a *app) vcorpConfirm(c *mg.Context) {
	v := &a.vcorp
	groupRow(c, mg.Column(c).Gap(sp3), true).Children(func() {
		rowWords(c, mg.Column(c), locales.VcorpConfirmTitle(v.account), locales.VcorpConfirmDesc(v.normOrigin))
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Children(func() {
			if button(c, locales.VcorpPair(), btnOpt{small: true, kind: btnPrimary}).Disabled(v.busy).Clicked() && !v.busy {
				a.vcorpPair()
			}
			if button(c, locales.SettingsCancel(), btnOpt{small: true}).Disabled(v.busy).Clicked() && !v.busy {
				v.account, v.normOrigin, v.code, v.err = "", "", "", ""
				v.gen++
			}
			if v.busy {
				spinner(c)
			}
		})
	})
}

// vcorpPaired shows the account, the link and Unpair.
func (a *app) vcorpPaired(c *mg.Context, st VcorpStatus) {
	p := pal(c)
	v := &a.vcorp
	settingRow(c, locales.VcorpAccount(), st.Origin, false, func() {
		mg.Text(c, st.Account).FontSize(fsSmall).TextColor(p.ink2).Selectable().SingleLine().MaxWidth(260)
	})
	desc := locales.VcorpLinkedDesc()
	if !st.Connected {
		desc = locales.VcorpConnectingDesc()
		if st.Err != "" {
			desc = locales.VcorpDownDesc(st.Err)
		}
	}
	settingRow(c, locales.VcorpLink(), desc, false, func() {
		dot, word := p.amber, locales.VcorpConnecting()
		if st.Connected {
			dot, word = p.green, locales.VcorpLinked()
		}
		mg.Row(c).Gap(6).AlignItems(mg.Center).Shrink(0).Children(func() {
			mg.Column(c).Width(8).Height(8).Radius(4).Background(dot)
			mg.Text(c, word).FontSize(fsSmall).TextColor(p.ink2)
		})
	})
	settingRow(c, locales.VcorpUnpairTitle(), locales.VcorpUnpairDesc(), true, func() {
		mg.Row(c).Gap(sp2).AlignItems(mg.Center).Shrink(0).Children(func() {
			if !v.unpairAsk {
				if button(c, locales.VcorpUnpair(), btnOpt{small: true}).Clicked() {
					v.unpairAsk = true
				}
				return
			}
			if button(c, locales.VcorpUnpairConfirm(), btnOpt{small: true, kind: btnRed}).Clicked() {
				a.vcorpUnpair()
			}
			if button(c, locales.SettingsCancel(), btnOpt{small: true}).Clicked() {
				v.unpairAsk = false
			}
		})
	})
}

func (a *app) vcorpClaim() {
	v := &a.vcorp
	origin, code := strings.TrimSpace(v.origin), strings.TrimSpace(v.code)
	v.busy, v.err = true, ""
	vc := a.opt.Vcorp
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		account, norm, err := vc.Claim(ctx, origin, code)
		a.post(func() {
			v.busy = false
			if err != nil {
				v.err = locales.VcorpErrClaim(err.Error())
				return
			}
			v.account, v.normOrigin, v.code = account, norm, code
		})
	}()
}

func (a *app) vcorpPair() {
	v := &a.vcorp
	origin, code := v.normOrigin, v.code
	v.busy, v.err = true, ""
	vc := a.opt.Vcorp
	go func() {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		defer cancel()
		err := vc.Pair(ctx, origin, code)
		a.post(func() {
			v.busy = false
			if err != nil {
				v.err = locales.VcorpErrPair(err.Error())
				return
			}
			account := v.account
			v.account, v.normOrigin, v.code = "", "", ""
			v.gen++
			a.vcorpRefresh()
			a.toasts = append(a.toasts, locales.VcorpPairedToast(account))
		})
	}()
}

func (a *app) vcorpUnpair() {
	v := &a.vcorp
	v.unpairAsk, v.err = false, ""
	vc := a.opt.Vcorp
	go func() {
		err := vc.Unpair()
		a.post(func() {
			if err != nil {
				v.err = locales.VcorpErrUnpair(err.Error())
				return
			}
			v.st = VcorpStatus{}
			a.vcorpRefresh()
			a.toasts = append(a.toasts, locales.VcorpUnpairedToast())
		})
	}()
}
