package android

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
)

// proxyPhone answers the commands of the traffic route, keeping the
// phone's http_proxy setting.
type proxyPhone struct {
	mu       sync.Mutex
	setting  string // as `settings get` prints it
	reverses []string
	cmds     []string
}

func (ph *proxyPhone) run(serial, service string) (string, bool) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.cmds = append(ph.cmds, service)
	switch {
	case strings.HasPrefix(service, "shell:ip -o addr show"):
		return phoneAddrs, true
	case service == "shell:settings get global http_proxy":
		return ph.setting + "\n", true
	case strings.HasPrefix(service, "shell:settings put global http_proxy "):
		ph.setting = strings.Trim(strings.TrimPrefix(service, "shell:settings put global http_proxy "), "'")
		return "", true
	case strings.HasPrefix(service, "reverse:forward:"):
		ph.reverses = append(ph.reverses, strings.TrimPrefix(service, "reverse:forward:"))
		return "OKAY", true
	case strings.HasPrefix(service, "reverse:killforward:"):
		remote := strings.TrimPrefix(service, "reverse:killforward:")
		ph.reverses = slices.DeleteFunc(ph.reverses, func(r string) bool { return strings.HasPrefix(r, remote+";") })
		return "OKAY", true
	}
	return "", false
}

func (ph *proxyPhone) state() (string, []string) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	return ph.setting, slices.Clone(ph.reverses)
}

func newProxyFake(t *testing.T, setting string) (*proxyPhone, *Provider) {
	f := startFakeADB(t)
	ph := &proxyPhone{setting: setting}
	f.set(func(f *fakeADB) {
		f.devices = []string{usbLine}
		f.props = map[string]string{piHW: phoneProps}
		f.run = ph.run
	})
	return ph, NewProvider(f.config())
}

func TestPhoneAddrsForTraffic(t *testing.T) {
	_, p := newProxyFake(t, "null")
	got, err := p.PhoneAddrs(context.Background(), piHW)
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr(wifiAddr), netip.MustParseAddr(tailAddr), netip.MustParseAddr("fd7a:115c:a1e0::2")}
	if !slices.Equal(got, want) {
		t.Fatalf("addresses %v", got)
	}
	if _, err := p.PhoneAddrs(context.Background(), "nobody"); err == nil {
		t.Error("a phone that is not connected has addresses")
	}
}

// The route sets the proxy and the reverse port, and taking it away puts
// back what the person had: nothing, or their own proxy.
func TestRouteProxyRestores(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct{ before, prev, after string }{
		{"null", "", ":0"},
		{"10.0.0.8:3128", "10.0.0.8:3128", "10.0.0.8:3128"},
	} {
		ph, p := newProxyFake(t, c.before)
		prev, err := p.ProxySetting(ctx, piHW)
		if err != nil || prev != c.prev {
			t.Fatalf("setting %q, %v", prev, err)
		}
		if err := p.RouteProxy(ctx, piHW, 9277); err != nil {
			t.Fatal(err)
		}
		if s, rev := ph.state(); s != "127.0.0.1:9277" || !slices.Equal(rev, []string{"tcp:9277;tcp:9277"}) {
			t.Fatalf("routed: %q %v", s, rev)
		}
		if err := p.UnrouteProxy(ctx, piHW, 9277, prev); err != nil {
			t.Fatal(err)
		}
		if s, rev := ph.state(); s != c.after || len(rev) != 0 {
			t.Fatalf("restored: %q %v", s, rev)
		}
	}
	// A setting someone changed since stays as they made it.
	ph, p := newProxyFake(t, "null")
	if err := p.RouteProxy(ctx, piHW, 9277); err != nil {
		t.Fatal(err)
	}
	ph.mu.Lock()
	ph.setting = "10.0.0.9:8080"
	ph.mu.Unlock()
	if err := p.UnrouteProxy(ctx, piHW, 9277, ""); err != nil {
		t.Fatal(err)
	}
	if s, _ := ph.state(); s != "10.0.0.9:8080" {
		t.Fatalf("someone's own setting was overwritten: %q", s)
	}
}
