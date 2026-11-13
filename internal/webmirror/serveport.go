package webmirror

import (
	"net/url"

	"github.com/DIYgod/pinky/internal/tailnet"
)

// servePorts are the HTTPS ports Settings may have `tailscale serve` give
// the web mirror to the tailnet at, in order: not 443, which remote agents
// use, and never one the person's own `tailscale serve` already uses, which
// the command would take over.
var servePorts = []string{"8443", "10000", "9443", "7443", "4443"}

// ServePort returns the first of servePorts that s does not serve on, ""
// when it serves on all of them.
func ServePort(s tailnet.Serve) string {
	used := map[string]bool{}
	for _, w := range s.Web {
		u, err := url.Parse(w.URL)
		if err != nil {
			continue
		}
		port := u.Port()
		if port == "" {
			port = map[string]string{"https": "443", "http": "80"}[u.Scheme]
		}
		used[port] = true
	}
	for _, p := range servePorts {
		if !used[p] {
			return p
		}
	}
	return ""
}
