package ios

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
	"howett.net/plist"
)

// PhoneAddrs implements driver.NetAddresser for Pinky's traffic capture
// (docs/notes/traffic-capture.md): the address usbmuxd lists an iPhone
// on Wi-Fi at (its NetworkAddress), which go-ios's list leaves out. A
// phone listed on its cable only has none: the person sets its Wi-Fi
// proxy, and Pinky knows its address once the phone is listed on Wi-Fi
// too (Use over Wi-Fi in Settings › Phones).
func (p *Provider) PhoneAddrs(ctx context.Context, id string) ([]netip.Addr, error) {
	raw, err := readMuxList()
	if err != nil {
		return nil, fmt.Errorf("usbmuxd: %w", err)
	}
	return muxAddrs(raw, id)
}

var _ driver.NetAddresser = (*Provider)(nil)

// readMuxList is usbmuxd's device list, as it sent it. Tests replace it.
var readMuxList = func() ([]byte, error) {
	c, err := goios.NewUsbMuxConnectionSimple()
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err := c.Send(goios.NewReadDevices()); err != nil {
		return nil, err
	}
	m, err := c.ReadMessage()
	if err != nil {
		return nil, err
	}
	return m.Payload, nil
}

// muxAddrs reads a phone's network addresses from usbmuxd's device list.
func muxAddrs(raw []byte, udid string) ([]netip.Addr, error) {
	var list struct {
		DeviceList []struct {
			Properties struct {
				SerialNumber   string
				ConnectionType string
				NetworkAddress []byte
			}
		}
	}
	if _, err := plist.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("usbmuxd: the device list: %w", err)
	}
	var out []netip.Addr
	for _, e := range list.DeviceList {
		pr := e.Properties
		if pr.SerialNumber != udid || pr.ConnectionType != "Network" {
			continue
		}
		if a, ok := sockaddrAddr(pr.NetworkAddress); ok && !containsAddr(out, a) {
			out = append(out, a)
		}
	}
	return out, nil
}

// sockaddrAddr reads a BSD sockaddr as usbmuxd sends it: length, family,
// then the port and the address. Only IPv4 (AF_INET, 2) and IPv6
// (AF_INET6, 30 on Apple's systems, 10 on Linux, 23 on Windows).
func sockaddrAddr(b []byte) (netip.Addr, bool) {
	if len(b) < 8 {
		return netip.Addr{}, false
	}
	switch b[1] {
	case 2:
		a := netip.AddrFrom4([4]byte(b[4:8]))
		return a, a.IsValid() && !a.IsUnspecified()
	case 30, 10, 23:
		if len(b) < 24 {
			return netip.Addr{}, false
		}
		a := netip.AddrFrom16([16]byte(b[8:24]))
		return a, !a.IsUnspecified() && !a.IsLinkLocalUnicast()
	}
	return netip.Addr{}, false
}

func containsAddr(list []netip.Addr, a netip.Addr) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}
