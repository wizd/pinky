package android

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The getprop output of the paired phone guidPixel, and of another phone.
const (
	propsPixel = "Pixel 9\n17\n37\nPixel\nR5CN20ABC\n"
	propsOther = "REDMI K90\n16\n36\nXiaomi\nOTHER9876\n"
	keptAddr   = "192.168.1.42:41000"
)

func (f *fakeADB) probed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.probes)
}

func (f *fakeADB) disconnected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.disconnects)
}

// keptFile reads the addresses kept in a network file.
func keptFile(t *testing.T, path string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f netFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, a := range f.Paired {
		out[a.GUID] = a.Addr
	}
	return out
}

// TestReconnectKeptAddressAfterRestart is GitHub issue #102: a paired
// phone that no longer announces itself on the network (HyperOS, screen
// off) comes back after Pinky and the adb server restarted, at the address
// it was last connected at, which the Pinky before kept in its file.
func TestReconnectKeptAddressAfterRestart(t *testing.T) {
	f := startFakeADB(t)
	path := filepath.Join(t.TempDir(), "network-phones.json")
	ctx := context.Background()

	// The first run connects the phone, found by the driver's search.
	p := newReconnectProvider(t, f, path)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.own = []string{guidPixel + "\t_adb-tls-connect._tcp\t" + keptAddr}
		f.props = map[string]string{keptAddr: propsPixel}
	})
	if _, connected := p.wifi.reconnect(ctx, entries(t, p)); !connected {
		t.Fatal("not connected")
	}
	if got := keptFile(t, path)[guidPixel]; got != keptAddr {
		t.Fatalf("kept %q, want %q", got, keptAddr)
	}

	// Pinky updates and the adb server restarts: no connections, and the
	// phone, its screen off, announces nothing.
	f.set(func(f *fakeADB) { f.devices, f.own, f.connects, f.browses = nil, nil, nil, 0 })
	p2 := newReconnectProvider(t, f, path)
	away, connected := p2.wifi.reconnect(ctx, entries(t, p2))
	if !connected || away {
		t.Errorf("reconnect = away %v, connected %v", away, connected)
	}
	if _, connects := f.counts(); !slices.Equal(connects, []string{keptAddr}) {
		t.Errorf("connects %q", connects)
	}
	if got := p2.wifi.guidAt(keptAddr); got != guidPixel {
		t.Errorf("guidAt = %q", got)
	}
	infos, err := p2.List(ctx)
	if err != nil || len(infos) != 1 || infos[0].ID != "R5CN20ABC" {
		t.Errorf("listed %+v %v, want the phone as R5CN20ABC", infos, err)
	}
	// Connected now: the next pass does not try again.
	if away, connected := p2.wifi.reconnect(ctx, entries(t, p2)); away || connected {
		t.Errorf("second pass = away %v, connected %v", away, connected)
	}
	if _, connects := f.counts(); len(connects) != 1 {
		t.Errorf("connected again: %q", connects)
	}
}

// TestReconnectKeptAddressUnreachable: a kept address where nothing
// answers gets a TCP probe only, never an adb connect (which would wait
// for the system's TCP timeout and hold up the server), and is not held
// against the phone: it is probed again at the next pass.
func TestReconnectKeptAddressUnreachable(t *testing.T) {
	f, p := newReconnectFake(t)
	ctx := context.Background()
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.unreachable = map[string]bool{keptAddr: true}
	})
	for i := range 2 {
		if away, connected := p.wifi.reconnect(ctx, nil); !away || connected {
			t.Errorf("pass %d = away %v, connected %v", i, away, connected)
		}
	}
	if probes := f.probed(); !slices.Equal(probes, []string{keptAddr, keptAddr}) {
		t.Errorf("probes %q", probes)
	}
	if _, connects := f.counts(); len(connects) != 0 {
		t.Errorf("adb connects %q", connects)
	}
	// It answers again (the person woke the phone, or it never stopped):
	// connected.
	f.set(func(f *fakeADB) {
		f.unreachable = nil
		f.props = map[string]string{keptAddr: propsPixel}
	})
	if _, connected := p.wifi.reconnect(ctx, nil); !connected {
		t.Error("not connected once the address answers")
	}
}

// TestReconnectKeptAddressOtherDevice: a kept address that another device
// took over (the router gave the phone's address to another phone this
// computer trusts) is not taken for the phone: that connection is dropped
// and the address forgotten.
func TestReconnectKeptAddressOtherDevice(t *testing.T) {
	f := startFakeADB(t)
	path := filepath.Join(t.TempDir(), "network-phones.json")
	p := newReconnectProvider(t, f, path)
	ctx := context.Background()
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.props = map[string]string{keptAddr: propsOther}
	})
	if away, connected := p.wifi.reconnect(ctx, nil); !away || connected {
		t.Errorf("reconnect = away %v, connected %v", away, connected)
	}
	if d := f.disconnected(); !slices.Equal(d, []string{keptAddr}) {
		t.Errorf("disconnects %q", d)
	}
	if got := p.wifi.guidAt(keptAddr); got != "" {
		t.Errorf("the other device is taken for %q", got)
	}
	if got, ok := keptFile(t, path)[guidPixel]; ok {
		t.Errorf("kept %q after another device answered there", got)
	}
	// Not tried again.
	p.wifi.reconnect(ctx, nil)
	if probes := f.probed(); len(probes) != 1 {
		t.Errorf("probes %q", probes)
	}
}

// TestReconnectKeptAddressUnsure: a connection at a kept address that does
// not tell which phone it is (its shell does not answer) is dropped, and
// the address kept for another try.
func TestReconnectKeptAddressUnsure(t *testing.T) {
	f, p := newReconnectFake(t)
	keptVerify = 300 * time.Millisecond
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) { f.known = []string{guidPixel} }) // no props: the shell fails
	if _, connected := p.wifi.reconnect(context.Background(), nil); connected {
		t.Error("connected without knowing the phone")
	}
	if d := f.disconnected(); !slices.Equal(d, []string{keptAddr}) {
		t.Errorf("disconnects %q", d)
	}
	if got := p.net.pairedAddr(guidPixel); got != keptAddr {
		t.Errorf("kept %q", got)
	}
}

// TestReconnectKeptHiddenSerial: a phone whose shell reads no serial is
// told by the mark Pinky left on it.
func TestReconnectKeptHiddenSerial(t *testing.T) {
	const hidden = "Pixel 9\n17\n37\nPixel\n\npinky-boot=\npinky-aid=9774d56d682e549c\n"
	for _, tc := range []struct {
		name string
		same bool
	}{{"its mark", true}, {"no mark", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newReconnectFake(t)
			p.ids = newIdentity(p)
			mark := []byte(strings.Repeat("m", markLen))
			if _, err := p.ids.store.set("R5CN20ABC", mark); err != nil {
				t.Fatal(err)
			}
			p.ids.readMark = func(context.Context, string, string) ([]byte, bool, error) {
				if tc.same {
					return mark, true, nil
				}
				return nil, false, nil
			}
			p.keepPairedAddr(guidPixel, keptAddr)
			f.set(func(f *fakeADB) {
				f.known = []string{guidPixel}
				f.props = map[string]string{keptAddr: hidden}
			})
			if _, connected := p.wifi.reconnect(context.Background(), nil); connected != tc.same {
				t.Errorf("connected = %v", connected)
			}
		})
	}
}

// TestReconnectKeptAndSearchOnce: when the phone both answers at its kept
// address and shows up on the network, it is connected once.
func TestReconnectKeptAndSearchOnce(t *testing.T) {
	f, p := newReconnectFake(t)
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.own = []string{guidPixel + "\t_adb-tls-connect._tcp\t" + keptAddr}
		f.props = map[string]string{keptAddr: propsPixel}
	})
	if _, connected := p.wifi.reconnect(context.Background(), nil); !connected {
		t.Fatal("not connected")
	}
	if _, connects := f.counts(); len(connects) != 1 {
		t.Errorf("connects %q", connects)
	}
	devs := entries(t, p)
	if len(devs) != 1 {
		t.Errorf("devices %+v", devs)
	}
}

// TestReconnectKeptAddressMoved: Wireless debugging came back on another
// port and the phone announces it: the search connects the new port, and
// the new one is kept in place of the old.
func TestReconnectKeptAddressMoved(t *testing.T) {
	const moved = "192.168.1.42:42000"
	f, p := newReconnectFake(t)
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.unreachable = map[string]bool{keptAddr: true}
		f.own = []string{guidPixel + "\t_adb-tls-connect._tcp\t" + moved}
	})
	if _, connected := p.wifi.reconnect(context.Background(), nil); !connected {
		t.Fatal("not connected")
	}
	if _, connects := f.counts(); !slices.Equal(connects, []string{moved}) {
		t.Errorf("connects %q", connects)
	}
	if got := p.net.pairedAddr(guidPixel); got != moved {
		t.Errorf("kept %q, want %q", got, moved)
	}
}

// TestReconnectKeepsNamedAddress: the address of a phone connected by its
// service name, as the server's discovery lists it, is kept, also for a
// connection the server made by itself.
func TestReconnectKeepsNamedAddress(t *testing.T) {
	f, p := newReconnectFake(t)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel, guidOther}
		f.services = []string{
			guidPixel + "\t_adb-tls-connect._tcp.\t" + keptAddr,
			guidOther + "\t_adb-tls-connect._tcp.\t192.168.1.7:43210",
		}
		f.devices = []string{guidOther + connectSuffix + "\tdevice product:x model:sdk device:x transport_id:3"}
	})
	if _, connected := p.wifi.reconnect(context.Background(), entries(t, p)); !connected {
		t.Fatal("not connected")
	}
	if got := p.net.pairedAddr(guidPixel); got != keptAddr {
		t.Errorf("connected by name: kept %q", got)
	}
	if got := p.net.pairedAddr(guidOther); got != "192.168.1.7:43210" {
		t.Errorf("connected by the server: kept %q", got)
	}
}

// TestReconnectKeptStops: a phone removed from Pinky, paused, or no longer
// paired with the adb server is not tried at its kept address; removing
// or unpairing it forgets the address.
func TestReconnectKeptStops(t *testing.T) {
	ctx := context.Background()
	t.Run("removed", func(t *testing.T) {
		f, p := newReconnectFake(t)
		p.keepPairedAddr(guidPixel, keptAddr)
		f.set(func(f *fakeADB) { f.known = []string{guidPixel} })
		p.Forget("R5CN20ABC")
		if err := p.Disconnect(ctx, "R5CN20ABC"); err != nil {
			t.Fatal(err)
		}
		if away, connected := p.wifi.reconnect(ctx, nil); away || connected {
			t.Errorf("reconnect = away %v, connected %v", away, connected)
		}
		if probes := f.probed(); len(probes) != 0 {
			t.Errorf("probed %q", probes)
		}
		if got := p.net.pairedAddr(guidPixel); got != "" {
			t.Errorf("kept %q", got)
		}
	})
	t.Run("paused", func(t *testing.T) {
		f, p := newReconnectFake(t)
		p.keepPairedAddr(guidPixel, keptAddr)
		f.set(func(f *fakeADB) { f.known = []string{guidPixel} })
		p.SetPaused("R5CN20ABC", true)
		p.wifi.reconnect(ctx, nil)
		if probes := f.probed(); len(probes) != 0 {
			t.Errorf("probed %q", probes)
		}
		// Resuming keeps the address.
		if got := p.net.pairedAddr(guidPixel); got != keptAddr {
			t.Errorf("kept %q", got)
		}
	})
	t.Run("unpaired", func(t *testing.T) {
		f, p := newReconnectFake(t)
		p.keepPairedAddr(guidPixel, keptAddr)
		f.set(func(f *fakeADB) { f.known = []string{guidOther} })
		p.wifi.reconnect(ctx, nil)
		if probes := f.probed(); len(probes) != 0 {
			t.Errorf("probed %q", probes)
		}
		if got := p.net.pairedAddr(guidPixel); got != "" {
			t.Errorf("kept %q", got)
		}
	})
}

// TestReconnectKeptSchedule: while a phone with a kept address stays away,
// passes back off to a minute rather than two.
func TestReconnectKeptSchedule(t *testing.T) {
	f, p := newReconnectFake(t)
	p.keepPairedAddr(guidPixel, keptAddr)
	f.set(func(f *fakeADB) {
		f.known = []string{guidPixel}
		f.unreachable = map[string]bool{keptAddr: true}
	})
	r := p.wifi
	var mu sync.Mutex
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var got []time.Duration
	for range 6 {
		r.mu.Lock()
		r.running = true
		r.mu.Unlock()
		r.pass(nil)
		r.mu.Lock()
		w := r.next.Sub(now)
		r.mu.Unlock()
		got = append(got, w)
		mu.Lock()
		now = now.Add(w)
		mu.Unlock()
	}
	want := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute, time.Minute}
	if !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

// TestNetStorePaired: the paired phones' addresses share the network file
// with the phones moved to Wi-Fi, and neither drops the other's.
func TestNetStorePaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "network-phones.json")
	s := &netStore{path: path, logf: t.Logf}
	if err := s.setPairedAddr(guidPixel, keptAddr); err != nil {
		t.Fatal(err)
	}
	if err := s.update("FA6C20303860", func(n *netPhone) { n.LAN, n.Port = "192.168.1.9", AwayPort }); err != nil {
		t.Fatal(err)
	}
	if err := s.setPairedAddr(guidPixel, "192.168.1.42:42000"); err != nil {
		t.Fatal(err)
	}
	if err := s.setPairedAddr(guidOther, "not an address"); err != nil {
		t.Fatal(err)
	}
	s2 := &netStore{path: path, logf: t.Logf}
	if got := s2.pairedAddr(guidPixel); got != "192.168.1.42:42000" {
		t.Errorf("pairedAddr = %q", got)
	}
	if got := s2.pairedAddr(guidOther); got != "" {
		t.Errorf("kept %q", got)
	}
	if n, ok := s2.get("FA6C20303860"); !ok || n.LAN != "192.168.1.9" {
		t.Errorf("get = %+v %v", n, ok)
	}
	// Forgetting the moved phone keeps the paired one's address, and the
	// other way round.
	if err := s2.update("FA6C20303860", func(n *netPhone) { *n = netPhone{} }); err != nil {
		t.Fatal(err)
	}
	if got := (&netStore{path: path}).pairedAddr(guidPixel); got == "" {
		t.Error("the paired phone's address went with the moved phone")
	}
	if err := s2.setPairedAddr(guidPixel, ""); err != nil {
		t.Fatal(err)
	}
	if got := keptFile(t, path); len(got) != 0 {
		t.Errorf("kept %v", got)
	}
}
