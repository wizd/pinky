package android

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/tailnet"
)

func TestParseByHand(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"100.101.2.2", "100.101.2.2"},
		{" 100.101.2.2 ", "100.101.2.2"},
		{"100.101.2.2:5555", "100.101.2.2"},
		{"::ffff:100.101.2.2", "100.101.2.2"},
		{"100.64.0.1", "100.64.0.1"},
		{"100.127.255.254", "100.127.255.254"},
		{"fd7a:115c:a1e0::2", "fd7a:115c:a1e0::2"},
		{"[fd7a:115c:a1e0::2]", "fd7a:115c:a1e0::2"},
		{"[fd7a:115c:a1e0::2]:5555", "fd7a:115c:a1e0::2"},
	} {
		a, err := parseByHand(c.in)
		if err != nil || a.String() != c.want {
			t.Errorf("parseByHand(%q) = %v, %v; want %s", c.in, a, err, c.want)
		}
	}
	// Outside Tailscale's ranges, or no address at all.
	for _, in := range []string{
		"", "192.168.1.42", "10.0.0.2", "100.63.255.255", "100.128.0.1", "8.8.8.8",
		"fd7a:115c:a1e1::2", "fe80::1%tun0", "fd00::1", "pixel-9", "pixel-9.tail1234.ts.net",
		"100.101.2.2:41000", "100.101.2.2/32",
	} {
		if a, err := parseByHand(in); !errors.Is(err, driver.ErrNotTailnetAddr) {
			t.Errorf("parseByHand(%q) = %v, %v; want ErrNotTailnetAddr", in, a, err)
		}
	}
}

func TestParseVPNAddrs(t *testing.T) {
	out := "1: lo    inet 127.0.0.1/8 scope host lo\n" +
		"20: rmnet_data0    inet 100.72.9.9/30 scope global rmnet_data0\n" + // a carrier's CGNAT address
		"30: wlan0    inet 192.168.1.42/24 brd 192.168.1.255 scope global wlan0\n" +
		"31: tun0    inet 100.101.2.2/32 scope global tun0\n" +
		"31: tun0    inet6 fd7a:115c:a1e0::2/128 scope global\n" +
		"32: tun1@NONE    inet 100.101.7.7/32 scope global tun1\n"
	got := parseVPNAddrs(out)
	want := []netip.Addr{netip.MustParseAddr("100.101.2.2"), netip.MustParseAddr("fd7a:115c:a1e0::2"), netip.MustParseAddr("100.101.7.7")}
	if !slices.Equal(got, want) {
		t.Fatalf("parseVPNAddrs = %v, want %v", got, want)
	}
	if got := parseVPNAddrs("20: rmnet_data0    inet 100.72.9.9/30 scope global rmnet_data0\n"); len(got) != 0 {
		t.Fatalf("a carrier's address counted as a VPN's: %v", got)
	}
}

// TestAddrOnPhone: which tailnet addresses count as the phone's own.
func TestAddrOnPhone(t *testing.T) {
	const (
		lo      = "1: lo    inet 127.0.0.1/8 scope host lo\n"
		wlan    = "30: wlan0    inet 192.168.1.42/24 brd 192.168.1.255 scope global wlan0\n"
		ts      = "31: tun0    inet 100.101.2.2/32 scope global tun0\n"
		singBox = "31: tun0    inet 172.19.0.1/30 scope global tun0\n" // a VPN app's own address
		carrier = "20: rmnet_data0    inet 100.101.2.2/30 scope global rmnet_data0\n"
	)
	for _, c := range []struct {
		what, out string
		ok        bool
	}{
		{"on Tailscale's interface", lo + wlan + ts, true},
		{"another device's, with Tailscale's interface up", lo + wlan + strings.ReplaceAll(ts, "100.101.2.2", "100.101.4.4"), false},
		{"on no interface, Tailscale in userspace", lo + wlan + singBox, true},
		{"on no interface, no VPN up", lo + wlan, false},
		{"a carrier's", lo + wlan + carrier, false},
		{"a carrier's, with a VPN up", lo + carrier + singBox, false},
	} {
		err := addrOnPhone(netip.MustParseAddr("100.101.2.2"), c.out)
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, driver.ErrAddrNotOnPhone)) {
			t.Errorf("%s: %v", c.what, err)
		}
	}
}

// markFake stands in for the mark files on the phone and on another
// device that may answer at the phone's tailnet address.
type markFake struct {
	mu       sync.Mutex
	marks    map[string][]byte // by device: "phone", "other"
	impostor bool              // another device answers at tailSerial
}

func (m *markFake) whose(serial string) string {
	if m.impostor && serial == tailSerial {
		return "other"
	}
	return "phone"
}

// newByHandFake is newNetFake with Pinky's phone checks on, Tailscale not
// installed on the computer, and the phone on a cable.
func newByHandFake(t *testing.T) (*fakeADB, *fakePhone, *markFake, *Provider) {
	oldLook, oldUp := netLook, upWait
	netLook, upWait = 300*time.Millisecond, 1500*time.Millisecond
	t.Cleanup(func() { netLook, upWait = oldLook, oldUp })
	f := startFakeADB(t)
	ph := &fakePhone{f: f, addrs: phoneAddrs}
	f.set(func(f *fakeADB) {
		f.props = map[string]string{piHW: phoneProps, localWiFi: phoneProps, tailSerial: phoneProps, lanSerial: phoneProps}
		f.connectOK = map[string]bool{}
		f.run = ph.run
		f.devices = []string{usbLine}
	})
	cfg := f.config()
	cfg.Tailnet = func(context.Context) (tailnet.Status, error) {
		return tailnet.Status{State: tailnet.NotInstalled}, tailnet.ErrNotInstalled
	}
	cfg.NetworkFile = filepath.Join(t.TempDir(), "network-phones.json")
	cfg.VerifyWiFi = true
	p := NewProvider(cfg)
	p.link.probe = func(ctx context.Context, serial string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.probes = append(f.probes, serial)
		switch {
		case f.unreachable[serial]:
			return errors.New("dial tcp " + serial + ": i/o timeout")
		case f.connectOK != nil && !f.connectOK[serial]:
			return connectErr(serial, errors.New("dial tcp "+serial+": connect: connection refused"))
		}
		return nil
	}
	m := &markFake{marks: map[string][]byte{"other": bytes.Repeat([]byte{7}, markLen)}}
	p.ids.readMark = func(_ context.Context, serial, _ string) ([]byte, bool, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		b, ok := m.marks[m.whose(serial)]
		return slices.Clone(b), ok, nil
	}
	p.ids.writeMark = func(_ context.Context, serial, _ string, mark []byte) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.marks[m.whose(serial)] = slices.Clone(mark)
		return nil
	}
	p.UseOverWiFi(piHW)
	return f, ph, m, p
}

func (f *fakeADB) connectedTo(serial string) bool {
	return slices.Contains(f.connected(), serial)
}

// TestSetAwayAt sets a phone up at a tailnet address entered by hand,
// with no Tailscale on the computer.
func TestSetAwayAt(t *testing.T) {
	f, ph, _, p := newByHandFake(t)
	ctx := context.Background()
	if a, err := p.Away(ctx, piHW); err != nil || a.Missing != driver.AwayNoTailscaleHere || a.Addr != tailAddr {
		t.Fatalf("before: %+v, %v", a, err)
	}
	// The usual way needs Tailscale here.
	if _, err := p.SetAway(ctx, piHW, true); err == nil {
		t.Fatal("set up without Tailscale on the computer")
	}
	a, err := p.SetAwayAt(ctx, piHW, tailAddr)
	if err != nil || !a.On || !a.Manual || !a.Listening || a.Addr != tailAddr || a.Port != AwayPort || a.Missing != "" {
		t.Fatalf("on: %+v, %v", a, err)
	}
	if r := ph.restarted(); !slices.Equal(r, []string{"tcpip:5555"}) {
		t.Fatalf("restarts = %q", r)
	}
	if !slices.Contains(f.tried(), tailSerial) || !f.connectedTo(tailSerial) {
		t.Fatalf("probes %q, connects %q", f.tried(), f.connected())
	}
	if n, ok := p.net.get(piHW); !ok || !n.Manual || n.Tailnet != tailAddr || n.Node != "" || n.Name != "Pixel 9" {
		t.Fatalf("kept %+v", n)
	}
	// On again keeps the address entered by hand.
	if a, err := p.SetAway(ctx, piHW, true); err != nil || !a.Manual || a.Addr != tailAddr {
		t.Fatalf("on again: %+v, %v", a, err)
	}
	// Off forgets it.
	if a, err := p.SetAway(ctx, piHW, false); err != nil || a.On || a.Manual {
		t.Fatalf("off: %+v, %v", a, err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("still kept")
	}
}

// TestSetAwayAtRange: an address outside Tailscale's ranges is refused
// before anything on the phone changes.
func TestSetAwayAtRange(t *testing.T) {
	_, ph, _, p := newByHandFake(t)
	for _, addr := range []string{wifiAddr, "10.0.0.2", "100.128.0.1", "fd00::1", "pixel-9"} {
		if _, err := p.SetAwayAt(context.Background(), piHW, addr); !errors.Is(err, driver.ErrNotTailnetAddr) {
			t.Fatalf("%s: %v, want ErrNotTailnetAddr", addr, err)
		}
	}
	if r := ph.restarted(); len(r) != 0 {
		t.Fatalf("restarts = %q", r)
	}
}

// TestSetAwayAtNotOnPhone: the address must be on the phone's own VPN
// interface, not another device's on the tailnet nor a carrier's.
func TestSetAwayAtNotOnPhone(t *testing.T) {
	f, ph, _, p := newByHandFake(t)
	ctx := context.Background()
	if _, err := p.SetAwayAt(ctx, piHW, "100.101.4.4"); !errors.Is(err, driver.ErrAddrNotOnPhone) {
		t.Fatalf("another device's address: %v", err)
	}
	// The phone has the address, but on mobile data: a carrier's.
	ph.mu.Lock()
	ph.addrs = "20: rmnet_data0    inet " + tailAddr + "/30 scope global rmnet_data0\n" +
		"30: wlan0    inet " + wifiAddr + "/24 scope global wlan0\n"
	ph.mu.Unlock()
	if _, err := p.SetAwayAt(ctx, piHW, tailAddr); !errors.Is(err, driver.ErrAddrNotOnPhone) {
		t.Fatalf("a carrier's address: %v", err)
	}
	if r := ph.restarted(); len(r) != 0 {
		t.Fatalf("restarts = %q", r)
	}
	if len(f.tried()) != 0 {
		t.Fatalf("probed %q", f.tried())
	}
	// Not connected: nothing to read the interfaces over.
	f.set(func(f *fakeADB) { f.devices = nil })
	if _, err := p.SetAwayAt(ctx, piHW, tailAddr); !errors.Is(err, driver.ErrOffline) {
		t.Fatalf("not connected: %v", err)
	}
}

// TestSetAwayAtIdentity: a device that answers at the address under the
// phone's serial, without Pinky's mark, is not taken for the phone.
func TestSetAwayAtIdentity(t *testing.T) {
	f, ph, m, p := newByHandFake(t)
	ctx := context.Background()
	m.mu.Lock()
	m.impostor = true
	m.mu.Unlock()
	if _, err := p.SetAwayAt(ctx, piHW, tailAddr); !errors.Is(err, driver.ErrNotThePhone) {
		t.Fatalf("impostor: %v", err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept the impostor's address")
	}
	f.mu.Lock()
	disc := slices.Clone(f.disconnects)
	f.mu.Unlock()
	if !slices.Contains(disc, tailSerial) {
		t.Fatalf("left the impostor connected: disconnects %q", disc)
	}
	// adb listened only for this: it stops again.
	if r := ph.restarted(); len(r) != 2 || r[1] != "usb:"+piHW {
		t.Fatalf("restarts = %q", r)
	}
	// One that says it is another phone is refused too.
	m.mu.Lock()
	m.impostor = false
	m.mu.Unlock()
	f.set(func(f *fakeADB) { f.props[tailSerial] = "Galaxy\n16\n36\nGalaxy\nOTHER123\n" })
	if _, err := p.SetAwayAt(ctx, piHW, tailAddr); !errors.Is(err, driver.ErrNotThePhone) {
		t.Fatalf("another phone: %v", err)
	}
}

// TestSetAwayAtUserspace: a VPN app that runs Tailscale in userspace
// (sing-box) shows its own address on tun0, not the tailnet's. The phone
// answers at the tailnet address with Pinky's mark, so it is set up; what
// answers there without the mark is not.
func TestSetAwayAtUserspace(t *testing.T) {
	f, ph, m, p := newByHandFake(t)
	ph.mu.Lock()
	ph.addrs = "30: wlan0    inet " + wifiAddr + "/24 scope global wlan0\n" +
		"31: tun0    inet 172.19.0.1/30 scope global tun0\n"
	ph.mu.Unlock()
	ctx := context.Background()
	m.mu.Lock()
	m.impostor = true
	m.mu.Unlock()
	if _, err := p.SetAwayAt(ctx, piHW, tailAddr); !errors.Is(err, driver.ErrNotThePhone) {
		t.Fatalf("another device there: %v", err)
	}
	m.mu.Lock()
	m.impostor = false
	m.mu.Unlock()
	a, err := p.SetAwayAt(ctx, piHW, tailAddr)
	if err != nil || !a.On || !a.Manual || a.Addr != tailAddr || a.Missing != "" {
		t.Fatalf("on: %+v, %v", a, err)
	}
	if !f.connectedTo(tailSerial) {
		t.Fatalf("connects %q", f.connected())
	}
}

// TestSetAwayAtUnreachable: without Tailscale's status, a TCP probe says
// whether this computer reaches the phone, before the adb server is asked
// to connect (it would wait for the system's own timeout).
func TestSetAwayAtUnreachable(t *testing.T) {
	f, ph, _, p := newByHandFake(t)
	f.set(func(f *fakeADB) { f.unreachable = map[string]bool{tailSerial: true} })
	if _, err := p.SetAwayAt(context.Background(), piHW, tailAddr); !errors.Is(err, driver.ErrAddrUnreachable) {
		t.Fatalf("unreachable: %v", err)
	}
	if !slices.Contains(f.tried(), tailSerial) || f.connectedTo(tailSerial) {
		t.Fatalf("probes %q, connects %q", f.tried(), f.connected())
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept an address this computer does not reach")
	}
	if r := ph.restarted(); len(r) != 2 || r[1] != "usb:"+piHW {
		t.Fatalf("restarts = %q", r)
	}
}

// TestSetAwayAtNoADB: something takes the connection at the address, as a
// proxy does before its own way to the tailnet fails, but no adb comes up
// there. The phone is on its cable all along, so this computer does not
// reach it at that address.
func TestSetAwayAtNoADB(t *testing.T) {
	f, _, _, p := newByHandFake(t)
	f.set(func(f *fakeADB) {
		f.connectOK[tailSerial] = true
		f.connectsOff = true
	})
	if _, err := p.SetAwayAt(context.Background(), piHW, tailAddr); !errors.Is(err, driver.ErrAddrUnreachable) {
		t.Fatalf("no adb there: %v", err)
	}
	if _, kept := p.net.get(piHW); kept {
		t.Fatal("kept an address where no adb came up")
	}
}

// TestSetAwayAtNoADBSlowList: as TestSetAwayAtNoADB, with the time running
// out while the adb server lists its devices rather than between two
// listings, as it can on a busy computer.
func TestSetAwayAtNoADBSlowList(t *testing.T) {
	f, _, _, p := newByHandFake(t)
	f.set(func(f *fakeADB) {
		f.connectOK[tailSerial] = true
		f.connectsOff = true
		f.slowDevices = 2 * upWait
	})
	if _, err := p.SetAwayAt(context.Background(), piHW, tailAddr); !errors.Is(err, driver.ErrAddrUnreachable) {
		t.Fatalf("no adb there, time out mid-listing: %v", err)
	}
}

// TestByHandReconnect: the reconnector reaches a phone set up by hand
// without Tailscale here, once a probe gets through.
func TestByHandReconnect(t *testing.T) {
	f, _, _, p := newByHandFake(t)
	ctx := context.Background()
	now := time.Now()
	p.link.now = func() time.Time { return now }
	f.set(func(f *fakeADB) { f.devices = nil })
	p.net.update(piHW, func(n *netPhone) { n.Tailnet, n.Manual = tailAddr, true })
	f.set(func(f *fakeADB) { f.unreachable = map[string]bool{tailSerial: true} })
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); !missing || len(f.tried()) != 1 || f.connectedTo(tailSerial) {
		t.Fatalf("unreachable: missing %v, tried %q, connects %q", missing, f.tried(), f.connected())
	}
	if setup, _ := p.link.why(netPhone{ID: piHW, Tailnet: tailAddr, Manual: true}); setup != driver.SetupNetworkAway {
		t.Fatalf("why: %s", setup)
	}
	now = now.Add(netFirst + time.Second)
	f.set(func(f *fakeADB) {
		delete(f.unreachable, tailSerial)
		f.connectOK[tailSerial] = true
	})
	if missing := p.link.reconnect(ctx, p.net.list(), entries(t, p)); missing || !f.connectedTo(tailSerial) {
		t.Fatalf("reachable: missing %v, connects %q", missing, f.connected())
	}
	a, err := p.Away(ctx, piHW)
	if err != nil || !a.On || !a.Manual || !a.Listening || a.Missing != "" {
		t.Fatalf("away: %+v, %v", a, err)
	}
}
