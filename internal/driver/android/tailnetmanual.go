package android

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/tailnet"
)

// Use away from home at a tailnet address the person enters
// (driver.TailnetByHand), for a computer that reaches the tailnet without
// Tailscale's own app, such as a proxy with a Tailscale outbound. Pinky
// then cannot read `tailscale status`, so it cannot tell from Tailscale
// that an address is the phone's or that traffic to it goes through the
// tailnet. Instead:
//
//   - the address must be in Tailscale's ranges (tailnet.Contains);
//   - it must be the phone's own on its VPN, read over the cable or Wi-Fi
//     connection while the phone is still near (addrOnPhone): on the VPN
//     interface, or, when a VPN app runs Tailscale in userspace (sing-box)
//     and so shows no tailnet address there, on none of the phone's
//     interfaces while a VPN is up;
//   - what answers there must carry Pinky's mark for this phone
//     (identity.go), as any connection over the network must;
//   - a TCP probe to the phone's adb port, rather than Tailscale's view
//     of the phone, says whether the phone is reachable.
//
// adb's own connection is not encrypted: it is as private as this
// computer's route to the tailnet, which Pinky cannot see.

// SetAwayAt implements driver.TailnetByHand.
func (p *Provider) SetAwayAt(ctx context.Context, id, addr string) (driver.Away, error) {
	a, err := parseByHand(addr)
	if err != nil {
		return driver.Away{}, err
	}
	if p.ids == nil {
		// No marks to tell this phone from another at the address.
		return driver.Away{}, fmt.Errorf("android: entering a tailnet address needs Pinky's phone checks: %w", driver.ErrUnsupported)
	}
	entries, err := p.devices(ctx)
	if err != nil {
		return driver.Away{}, wrapErr(err)
	}
	c := p.conns(ctx, entries, id)
	local := c.usb
	if local == "" {
		local = c.local
	}
	if local == "" {
		return driver.Away{}, fmt.Errorf("android: %s: connect the phone by cable or Wi-Fi to set it up: %w", id, driver.ErrOffline)
	}
	e := entryOf(entries, local)

	// The address is the phone's own, on its VPN.
	out, err := p.cfg.Client.Shell(ctx, local, "ip -o addr show 2>/dev/null")
	if err != nil {
		return driver.Away{}, wrapErr(err)
	}
	if err := addrOnPhone(a, string(out)); err != nil {
		return driver.Away{}, fmt.Errorf("android: %s: %w", id, err)
	}
	// The phone carries Pinky's mark, to check what answers there.
	if err := p.markNear(ctx, e, id); err != nil {
		return driver.Away{}, err
	}
	name := id
	if pr, err := p.deviceProps(ctx, e); err == nil {
		name = pr.Name
	}

	serial := joinAddr(a.String(), AwayPort)
	was := p.listenPort(ctx, local) == AwayPort
	if err := p.listen(ctx, id, local); err != nil {
		return driver.Away{}, err
	}
	undo := func() {
		if err := p.cfg.Client.Disconnect(context.WithoutCancel(ctx), serial); err != nil && !errors.Is(err, adb.ErrDeviceNotFound) {
			p.cfg.logf("android: disconnecting %s: %v", serial, err)
		}
		if !was {
			p.undoListen(ctx, id, local)
		}
	}
	p.link.forgive(serial)
	// Whatever answered there before may not be what answers now.
	p.dropProps(serial)
	info, err := p.connectProbed(ctx, serial)
	if err != nil {
		undo()
		return driver.Away{}, err
	}
	if err := p.sameAs(ctx, serial, info, id); err != nil {
		undo()
		return driver.Away{}, err
	}
	if err := p.net.update(id, func(n *netPhone) {
		n.Name, n.Port, n.Node, n.Tailnet, n.Manual = name, AwayPort, "", a.String(), true
	}); err != nil {
		return driver.Away{}, err
	}
	p.cfg.logf("android: %s: set up for away from home at %s, entered by hand", id, serial)
	p.link.soon()
	return p.Away(ctx, id)
}

// parseByHand reads an address the person entered: an IP address in
// Tailscale's ranges, with adb's port or none.
func parseByHand(s string) (netip.Addr, error) {
	s = strings.TrimSpace(s)
	if h, port, err := net.SplitHostPort(s); err == nil && port == strconv.Itoa(AwayPort) {
		s = h
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("android: %q is not an IP address: %w", s, driver.ErrNotTailnetAddr)
	}
	a = a.Unmap()
	if !tailnet.Contains(a) {
		return netip.Addr{}, fmt.Errorf("android: %s: %w", a, driver.ErrNotTailnetAddr)
	}
	return a, nil
}

// addrOnPhone says whether a, an address in Tailscale's ranges, is the
// phone's own on its VPN, from the phone's `ip -o addr show`:
//
//   - on a VPN interface (tun<n>: Android names every VPN app's interface
//     so), it is, as with Tailscale's own app;
//   - when a VPN interface has another tailnet address, it is not: that is
//     the phone's, and a is another device's;
//   - on any other interface, it is not: a carrier's address in
//     100.64.0.0/10 is on the mobile data interface (rmnet, ccmni), and
//     traffic to it would not go through the tailnet;
//   - on none, it is, as long as a VPN is up: a VPN app that runs
//     Tailscale in userspace (sing-box's Tailscale endpoint) gives its
//     interface an address of its own and answers at the tailnet address
//     itself. Pinky's mark then tells this phone from another device.
func addrOnPhone(a netip.Addr, out string) error {
	vpn, other := parseIfAddrs(out)
	var tail []netip.Addr
	for _, v := range vpn {
		if tailnet.Contains(v) {
			tail = append(tail, v)
		}
	}
	switch {
	case slices.Contains(vpn, a):
		return nil
	case len(tail) > 0:
		return fmt.Errorf("%s is not on the phone's VPN interface (it has %s): %w", a, addrList(tail), driver.ErrAddrNotOnPhone)
	case slices.Contains(other, a):
		return fmt.Errorf("%s is on the phone's own network interface, not its VPN: %w", a, driver.ErrAddrNotOnPhone)
	case len(vpn) == 0:
		return fmt.Errorf("%s: the phone has no VPN up: %w", a, driver.ErrAddrNotOnPhone)
	}
	return nil
}

// parseVPNAddrs is the addresses on the phone's VPN interfaces, from
// parseIfAddrs.
func parseVPNAddrs(out string) []netip.Addr {
	vpn, _ := parseIfAddrs(out)
	return vpn
}

// parseIfAddrs reads `ip -o addr show` (parsePhoneAddrs has an example)
// for the addresses on VPN interfaces (tun<n>) and on all the others.
func parseIfAddrs(out string) (vpn, other []netip.Addr) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || (f[2] != "inet" && f[2] != "inet6") {
			continue
		}
		pfx, err := netip.ParsePrefix(f[3])
		if err != nil {
			continue
		}
		a := pfx.Addr().Unmap()
		name, _, _ := strings.Cut(f[1], "@")
		list := &other
		if strings.HasPrefix(name, "tun") {
			list = &vpn
		}
		if !slices.Contains(*list, a) {
			*list = append(*list, a)
		}
	}
	return vpn, other
}

func addrList(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return "no VPN address"
	}
	s := make([]string, len(addrs))
	for i, a := range addrs {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}

// markNear makes sure the phone carries Pinky's mark before it is reached
// at an address entered by hand: a cable or this computer marks it; a
// connection over Wi-Fi must pass the usual check, which marks a phone
// the person said is theirs the first time.
func (p *Provider) markNear(ctx context.Context, e adb.DeviceEntry, id string) error {
	if trusted(e) {
		return p.ids.mark(ctx, e.Serial, id)
	}
	v, err := p.ids.verify(ctx, e, id)
	if err != nil {
		return err
	}
	if v != idSame {
		return fmt.Errorf("android: %s at %s is not checked as the phone Pinky set up; plug it in, or say it is yours: %w", id, e.Serial, driver.ErrNotThePhone)
	}
	return nil
}

// sameAs checks that what answers at serial is the phone id: it says it
// is, and it serves the mark Pinky left on that phone. It never marks.
func (p *Provider) sameAs(ctx context.Context, serial string, info driver.Info, id string) error {
	if info.State != driver.StateOnline {
		return fmt.Errorf("android: %s answers, but is %s: %w", serial, info.State, driver.ErrNotThePhone)
	}
	if info.ID != id {
		return fmt.Errorf("android: %s answers as %s, not %s: %w", serial, info.ID, id, driver.ErrNotThePhone)
	}
	rec, ok := p.ids.store.get(id)
	if !ok {
		return fmt.Errorf("android: %s has no mark to check: %w", id, driver.ErrNotThePhone)
	}
	host, err := p.ids.store.host()
	if err != nil {
		return err
	}
	mark, _, err := p.ids.readMark(ctx, serial, markDir+markName+host)
	if err != nil {
		return fmt.Errorf("android: reading Pinky's mark at %s: %w", serial, wrapErr(err))
	}
	if !rec.matches(mark) {
		p.cfg.logf("android: %s at %s lacks the mark Pinky left on that phone: not set up there", id, serial)
		return fmt.Errorf("android: %s lacks the mark Pinky left on %s: %w", serial, id, driver.ErrNotThePhone)
	}
	return nil
}

// connectProbed connects to a phone whose adb is restarting to listen on
// the network, as connectWhenUp, but asks the adb server to connect only
// once a TCP probe got through: the server waits for the system's own
// timeout at an address nothing answers. Without Tailscale's status here
// the probe is what says whether this computer reaches the phone.
func (p *Provider) connectProbed(ctx context.Context, serial string) (driver.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, upWait)
	defer cancel()
	for {
		err := p.link.probe(ctx, serial)
		if err == nil {
			if err = p.connectAddr(ctx, serial); err == nil {
				info, err := p.waitConnected(ctx, serial, false)
				if errors.Is(err, driver.ErrOffline) {
					// Something took the connection, as a proxy does
					// before its own way to the tailnet fails, but no adb
					// came up there: the phone is near, not reached.
					return driver.Info{}, fmt.Errorf("%v: %w", err, driver.ErrAddrUnreachable)
				}
				return info, err
			}
			err = connectErr(serial, err)
		}
		if sleepCtx(ctx, time.Second) != nil {
			if errors.Is(err, driver.ErrNotListening) {
				return driver.Info{}, err
			}
			return driver.Info{}, fmt.Errorf("android: %s: %v: %w", serial, err, driver.ErrAddrUnreachable)
		}
	}
}

// byHand connects a phone at a tailnet address entered by hand once a
// probe finds it there.
func (t *netLink) byHand(ctx context.Context, n netPhone) bool {
	serial := n.tailSerial()
	if t.holdBack(serial) {
		return false
	}
	if err := t.connect(ctx, serial); err != nil {
		t.fail(n.ID, serial, err, "at the tailnet address entered by hand")
		return false
	}
	t.ok(n.ID+"/tail", serial, "android: connected to "+n.ID+" at the tailnet address entered by hand, "+serial)
	return true
}

var _ driver.TailnetByHand = (*Provider)(nil)
