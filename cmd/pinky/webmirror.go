package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/tailnet"
	"github.com/DIYgod/pinky/internal/webapproval"
	"github.com/DIYgod/pinky/internal/webmirror"
	"github.com/DIYgod/pinky/ui"
)

// newWebMirror returns the web mirror of the gateway's phones, off until
// Settings › Phones › Web mirror turns it on. Its tokens are its own, in
// memory only: one can only watch, the other can also use the phone while
// the person allows control.
//
// It also serves the page that answers approvals in the browser
// (internal/webapproval), off until Settings turns it on; that page
// reaches the gateway's Needs and Decide, never the mirror's Source.
func newWebMirror(g *gateway.Gateway, log *slog.Logger) *webMirror {
	approvals := webapproval.New(webapproval.Options{
		Source:  g,
		Log:     log,
		Owner:   tailnetOwner,
		Origin:  tailnetMirrorOrigin,
		Proxied: webmirror.Proxied,
		Text:    webApprovalsText,
	})
	return &webMirror{Server: webmirror.New(webmirror.Options{
		Source:    g,
		Log:       log,
		OwnNames:  tailnetNames,
		Owner:     tailnetOwner,
		Text:      webMirrorText,
		Approvals: approvals.Handler(),
	}), Approvals: approvals}
}

// tailnetOwner is the tailnet login of the person this computer is signed
// in as, "" when it is tagged or Tailscale is not running.
func tailnetOwner(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	st, _ := tailnet.Read(ctx)
	if !st.Running() {
		return ""
	}
	return st.Owner
}

// tailnetMirrorOrigin is the origin `tailscale serve` gives the web mirror
// to the tailnet under, as "https://mac.tail1234.ts.net:8443", "" when it
// gives none. Passkeys that answer approvals are bound to it.
func tailnetMirrorOrigin(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	s, err := tailnet.ReadServe(ctx)
	if err != nil {
		return ""
	}
	return s.Forwarding(webmirror.Addr)
}

// webMirror is the web mirror as Settings sees it (ui.WebMirror).
type webMirror struct {
	*webmirror.Server
	// Approvals answers approvals in the browser (ui.WebApprovals).
	Approvals *webapproval.Server
}

// Tailnet says where `tailscale serve` gives the web mirror to the tailnet.
// It only reads Tailscale's state.
func (w *webMirror) Tailnet(ctx context.Context) ui.RemoteTailnet {
	st, _ := tailnet.Read(ctx)
	if !st.Running() || st.Self.DNSName == "" {
		return ui.RemoteTailnet{}
	}
	out := ui.RemoteTailnet{Name: st.Self.DNSName}
	if s, err := tailnet.ReadServe(ctx); err == nil {
		if u := s.Forwarding(webmirror.Addr); u != "" {
			out.URL = u + "/"
		} else {
			out.ServePort = webmirror.ServePort(s)
		}
	}
	return out
}

// webMirrorText is the page's words in the language of the browser's
// Accept-Language that suits best, as "zh-CN,zh;q=0.9,en;q=0.8".
func webMirrorText(acceptLanguage string) (string, map[string]string) {
	want := ""
	if first, _, _ := strings.Cut(acceptLanguage, ","); first != "" {
		want, _, _ = strings.Cut(strings.TrimSpace(first), ";")
	}
	m := locales.In(want)
	_, lang, ok := locales.Catalog.Message(want, "webMirrorPageTitle")
	if !ok || lang == "" {
		lang = "en"
	}
	return lang, map[string]string{
		"title":        m.WebMirrorPageTitle(),
		"phone":        m.WebMirrorPagePhone(),
		"connecting":   m.WebMirrorPageConnecting(),
		"reconnecting": m.WebMirrorPageReconnecting(),
		"noToken":      m.WebMirrorPageNoToken(),
		"badToken":     m.WebMirrorPageBadToken(),
		"insecure":     m.WebMirrorPageInsecure(),
		"noWebCodecs":  m.WebMirrorPageNoWebCodecs(),
		"unsupported":  m.WebMirrorPageUnsupported("{codec}"),
		"noPhone":      m.WebMirrorPageNoPhone(),
		"ended":        m.WebMirrorPageEnded(),
		"streamFailed": m.WebMirrorPageStreamFailed("{detail}"),
		"back":         m.WebMirrorPageBack(),
		"home":         m.WebMirrorPageHome(),
		"fullScreen":   m.WebMirrorPageFullScreen(),
		"inControl":    m.WebMirrorPageInControl(),
		"controlOff":   m.WebMirrorPageControlOff(),
		"tailnetUser":  m.WebMirrorPageTailnetUser(),
	}
}

// webApprovalsText is the approvals page's words, as webMirrorText.
func webApprovalsText(acceptLanguage string) (string, map[string]string) {
	want := ""
	if first, _, _ := strings.Cut(acceptLanguage, ","); first != "" {
		want, _, _ = strings.Cut(strings.TrimSpace(first), ";")
	}
	m := locales.In(want)
	_, lang, ok := locales.Catalog.Message(want, "webApprovalsPageTitle")
	if !ok || lang == "" {
		lang = "en"
	}
	return lang, map[string]string{
		"title":         m.WebApprovalsPageTitle(),
		"tailnet":       m.WebApprovalsPageTailnet(),
		"notAdded":      m.WebApprovalsPageNotAdded(),
		"noPasskeys":    m.WebApprovalsPageNoPasskeys(),
		"addTitle":      m.WebApprovalsPageAddTitle(),
		"addBody":       m.WebApprovalsPageAddBody(),
		"addButton":     m.WebApprovalsPageAddButton(),
		"linkUsed":      m.WebApprovalsPageLinkUsed(),
		"passkeyFailed": m.WebApprovalsPagePasskeyFailed("{detail}"),
		"compareTitle":  m.WebApprovalsPageCompareTitle(),
		"compare":       m.WebApprovalsPageCompare(),
		"turnedDown":    m.WebApprovalsPageTurnedDown(),
		"added":         m.WebApprovalsPageAdded(),
		"none":          m.WebApprovalsPageNone(),
		"removed":       m.WebApprovalsPageRemoved(),
		"allow":         m.WebApprovalsPageAllow(),
		"deny":          m.WebApprovalsPageDeny(),
		"confirmAllow":  m.WebApprovalsPageConfirmAllow(),
		"confirmDeny":   m.WebApprovalsPageConfirmDeny(),
		"onComputer":    m.WebApprovalsPageOnComputer(),
		"left":          m.WebApprovalsPageLeft("{seconds}"),
		"timedOut":      m.WebApprovalsPageTimedOut(),
		"already":       m.WebApprovalsPageAlready(),
		"late":          m.WebApprovalsPageLate(),
		"verifyFailed":  m.WebApprovalsPageVerifyFailed(),
		"notAnswered":   m.WebApprovalsPageNotAnswered(),
		"allowed":       m.WebApprovalsPageAllowed(),
		"denied":        m.WebApprovalsPageDenied(),
		"rule":          m.WebApprovalsPageRule("{rule}"),
		"busy":          m.WebApprovalsPageBusy(),
		"slowDown":      m.WebApprovalsPageSlowDown(),
	}
}

// printMirrorLinks prints the web mirror's links, which are secret: with
// one, anyone watches the phones, or uses them. Their keys go only to a
// terminal, never to a pipe or a file a program reads.
func printMirrorLinks(f *os.File, m *webmirror.Server) {
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		fmt.Fprintf(f, "Web mirror at %s (its link with the key is printed to a terminal only)\n", webmirror.URL())
		return
	}
	fmt.Fprintf(f, "Web mirror at %s#%s\n", webmirror.URL(), m.Token())
	if ctl := m.ControlToken(); ctl != "" {
		fmt.Fprintf(f, "Web mirror with control at %s#%s\n", webmirror.URL(), ctl)
	}
	fmt.Fprintln(f, "Keep these links secret: anyone with one can watch the phones, or use them.")
}
