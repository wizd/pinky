package android

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// Pinky's traffic capture (docs/notes/traffic-capture.md): the phone's
// addresses, for the proxy to take its connections only, and the route
// through the phone's adb connection that the person may turn on.

var (
	_ driver.NetAddresser = (*Provider)(nil)
	_ driver.ProxyRouter  = (*Provider)(nil)
)

// serial returns a connection to the phone, its cable's first.
func (p *Provider) serial(ctx context.Context, id string) (string, error) {
	entries, err := p.devices(ctx)
	if err != nil {
		return "", wrapErr(err)
	}
	s := p.conns(ctx, entries, id).any()
	if s == "" {
		return "", fmt.Errorf("android: %s is not connected", id)
	}
	return s, nil
}

// PhoneAddrs returns the phone's Wi-Fi and Tailscale addresses.
func (p *Provider) PhoneAddrs(ctx context.Context, id string) ([]netip.Addr, error) {
	s, err := p.serial(ctx, id)
	if err != nil {
		return nil, err
	}
	wifi, tail, err := p.phoneAddrs(ctx, s)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	if wifi.IsValid() {
		out = append(out, wifi)
	}
	return append(out, tail...), nil
}

// ProxySetting reads the phone's global HTTP proxy.
func (p *Provider) ProxySetting(ctx context.Context, id string) (string, error) {
	s, err := p.serial(ctx, id)
	if err != nil {
		return "", err
	}
	return p.proxySetting(ctx, s)
}

func (p *Provider) proxySetting(ctx context.Context, serial string) (string, error) {
	out, err := p.cfg.Client.Shell(ctx, serial, "settings get global http_proxy")
	if err != nil {
		return "", wrapErr(err)
	}
	v := strings.TrimSpace(string(out))
	if v == "null" || v == ":0" {
		v = ""
	}
	return v, nil
}

// pinkyProxy is the setting RouteProxy makes.
func pinkyProxy(port int) string { return "127.0.0.1:" + strconv.Itoa(port) }

// RouteProxy has the phone's 127.0.0.1:port reach the computer's
// 127.0.0.1:port over adb, and sets the phone's HTTP proxy to it.
func (p *Provider) RouteProxy(ctx context.Context, id string, port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("android: no port %d", port)
	}
	s, err := p.serial(ctx, id)
	if err != nil {
		return err
	}
	tcp := "tcp:" + strconv.Itoa(port)
	if err := p.cfg.Client.Reverse(ctx, s, tcp, tcp); err != nil {
		return fmt.Errorf("android: %s: adb reverse: %w", id, wrapErr(err))
	}
	if _, err := p.cfg.Client.Shell(ctx, s, "settings put global http_proxy "+pinkyProxy(port)); err != nil {
		return fmt.Errorf("android: %s: set the proxy: %w", id, wrapErr(err))
	}
	return nil
}

// UnrouteProxy puts the phone's HTTP proxy back to prev, unless something
// else changed it since, and removes the reverse port.
func (p *Provider) UnrouteProxy(ctx context.Context, id string, port int, prev string) error {
	s, err := p.serial(ctx, id)
	if err != nil {
		return err
	}
	var errs []error
	cur, err := p.proxySetting(ctx, s)
	switch {
	case err != nil:
		errs = append(errs, err)
	case cur == pinkyProxy(port):
		v := prev
		if v == "" {
			// ":0" clears the setting; deleting it needs a restart to apply.
			v = ":0"
		}
		if _, err := p.cfg.Client.Shell(ctx, s, "settings put global http_proxy '"+strings.ReplaceAll(v, "'", "")+"'"); err != nil {
			errs = append(errs, fmt.Errorf("android: %s: put the proxy back: %w", id, wrapErr(err)))
		}
	}
	if err := p.cfg.Client.RemoveReverse(ctx, s, "tcp:"+strconv.Itoa(port)); err != nil {
		p.cfg.logf("android: %s: remove the reverse port: %v", id, err)
	}
	return errors.Join(errs...)
}
