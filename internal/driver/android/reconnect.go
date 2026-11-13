package android

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// Reconnecting paired phones over Wi-Fi (Config.ReconnectWiFi). The adb
// server can connect by itself to the phones it paired with when their
// connect service shows up on the network, but Pinky starts it with that
// turned off (adb.ServerOptions.NoAutoConnect), so that a phone is
// connected only when Pinky asks: List connects the paired phones the
// server's discovery lists by their service name, which gives them the
// serials the server's auto-connect would. The server's discovery sees
// nothing after the computer's network changes until it restarts, so a
// phone whose connection dropped (it slept, or left the network for a
// while) would stay away; List looks for the paired phones the server
// does not list with the driver's own search, and connects to them by
// address.
//
// Some phones stop announcing their connect service while their screen is
// off (a REDMI K90 on HyperOS 4, GitHub issue #102), so after the adb
// server or Pinky restarted they would stay away until the person woke
// them. Their Wireless debugging still takes connections at the address it
// had, so the driver keeps the address each paired phone was last
// connected at (netStore.pairedAddr, in Config.NetworkFile) and, while
// the phone is away, tries it in the same pass as the search: a quick TCP
// probe, then adb connect, and the connection is kept only when the phone
// at that address is the paired one (keptPhone).

// reconnectSearch is how long a pass searches the network: a dozing phone
// may take five seconds to answer.
var reconnectSearch = 6 * time.Second

// keptVerify is how long a connection at a kept address may take to come
// online and tell which phone it is.
var keptVerify = 10 * time.Second

const (
	reconnectSoon  = 5 * time.Second // the wait after a phone came or went, or one was connected
	reconnectFirst = 10 * time.Second
	reconnectMax   = 2 * time.Minute // the longest wait while a paired phone stays away
	reconnectIdle  = time.Minute     // the wait while every paired phone is connected
	// reconnectKeptMax is the longest wait while a paired phone with a kept
	// address stays away: trying an address is a TCP probe, so a phone that
	// stopped announcing itself comes back within a minute of answering.
	reconnectKeptMax = time.Minute

	// A phone that refused a connection (it no longer trusts this
	// computer, say) is tried again at the same address after these waits,
	// tripled each time. The adb server (platform-tools 37) can crash when
	// a TLS handshake fails, taking every other tool's connections down, so
	// a phone it quit on is left alone for the longest wait at once.
	refusedFirst = 5 * time.Minute
	refusedMax   = 6 * time.Hour
)

// connectSuffix ends the serials of the server's own Wi-Fi connections,
// "adb-<serial>-<suffix>._adb-tls-connect._tcp".
const connectSuffix = "." + adb.ServiceConnect

type reconnector struct {
	p     *Provider
	now   func() time.Time
	probe func(ctx context.Context, addr string) error // tests replace it

	mu      sync.Mutex
	running bool
	next    time.Time         // no pass starts before then
	wait    time.Duration     // the wait after a pass that left a phone away
	online  string            // the serials in use at the last List
	byAddr  map[string]string // the addresses the driver connected to, with their guids
	noted   map[string]string // the last thing logged about a guid
	refused map[string]refusal
	// keptAway: the last pass left away a phone with a kept address.
	keptAway bool
}

// keptTry is a pass's try of a paired phone's kept address, which the
// search's connection to the same phone waits for: whichever connects the
// phone first, the other does not connect it again.
type keptTry struct {
	mu   sync.Mutex // held while either connects the phone
	done bool       // the phone was connected, under mu
	won  atomic.Bool
	wait chan struct{} // closed once the try is over
}

// refusal is a phone's last failed connection, by guid.
type refusal struct {
	addr  string
	count int
	until time.Time // no new try at addr before then
}

func newReconnector(p *Provider) *reconnector {
	return &reconnector{p: p, now: time.Now, probe: probe, byAddr: map[string]string{}, noted: map[string]string{},
		refused: map[string]refusal{}}
}

// kick starts a pass in the background when one is due. A phone coming or
// going makes the next pass come soon.
func (r *reconnector) kick(entries []adb.DeviceEntry) {
	now := r.now()
	online := inUseSerials(entries)
	r.mu.Lock()
	if online != r.online {
		r.online = online
		r.wait = 0
		if soon := now.Add(reconnectSoon); r.next.After(soon) {
			r.next = soon
		}
	}
	if r.running || now.Before(r.next) {
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go r.pass(entries)
}

func (r *reconnector) pass(entries []adb.DeviceEntry) {
	ctx, cancel := context.WithTimeout(context.Background(), reconnectSearch+keptVerify+20*time.Second)
	defer cancel()
	away, connected := r.reconnect(ctx, entries)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = false
	now := r.now()
	switch {
	case connected:
		r.wait = 0
		r.next = now.Add(reconnectSoon)
	case away:
		most := reconnectMax
		if r.keptAway {
			most = reconnectKeptMax
		}
		r.wait = min(max(2*r.wait, reconnectFirst), most)
		r.next = now.Add(r.wait)
	default:
		r.wait = 0
		r.next = now.Add(reconnectIdle)
	}
}

// reconnect connects the paired phones that are on the network but not
// connected. It reports whether a paired phone is still away, and whether
// it connected one.
func (r *reconnector) reconnect(ctx context.Context, entries []adb.DeviceEntry) (away, connected bool) {
	p := r.p
	guids, err := p.cfg.KnownHosts()
	if err != nil {
		r.note("", "android: reading the paired phones: "+err.Error())
		return false, false
	}
	// The addresses kept for phones the server no longer has paired are of
	// no use.
	if err := p.net.keepPaired(func(g string) bool { return slices.Contains(guids, g) }); err != nil {
		p.cfg.logf("android: forgetting the addresses of phones no longer paired: %v", err)
	}
	want := r.missing(guids, entries)
	// Phones the person removed from Pinky stay away until they add them
	// back (Forget), and those Pinky may not use (Limit) away for good.
	want = slices.DeleteFunc(want, func(g string) bool { return p.forgot(guidSerial(g)) || !p.mayUseID(guidSerial(g)) })
	want = slices.DeleteFunc(want, func(g string) bool { return r.onWiFi(ctx, g, entries) })
	if len(guids) == 0 {
		r.setKeptAway(false)
		return false, false
	}
	// A phone the server's discovery lists is connected by its service
	// name, as the server's own auto-connect does (Pinky starts the server
	// with it off, so that only Pinky decides what to connect): the
	// connection gets the serial the server would give it.
	listed, _ := p.cfg.Client.MDNSServices(ctx)
	r.keepNamed(guids, want, entries, listed)
	if len(want) == 0 {
		r.setKeptAway(false)
		return false, false
	}
	// The phones' kept addresses are tried while the search runs.
	tries := r.tryKept(ctx, want, entries)
	missed := map[string]bool{}
	rest := want[:0]
	for _, g := range want {
		if serviceAddr(listed, g) == "" {
			rest = append(rest, g)
			continue
		}
		name := g + connectSuffix
		if r.holdBack(g, name) {
			missed[g] = true
			continue
		}
		ok, took := r.exclusive(tries[g], func() bool {
			r.note(g, "android: "+g+" is on the network and the adb server lists it; connecting it by its name")
			got, err := r.connect(ctx, g, name, serviceAddr(listed, g))
			if err != nil {
				return false
			}
			if got == name {
				r.connectedNamed(g)
			} else {
				r.remember(got, g)
			}
			r.p.keepPairedAddr(g, serviceAddr(listed, g))
			return true
		})
		switch {
		case took:
		case ok:
			connected = true
		default:
			missed[g] = true
		}
	}
	want = rest
	if len(want) > 0 {
		if b, err := p.browse(adb.ServiceConnect); err != nil {
			for _, g := range want {
				missed[g] = true
			}
		} else {
			found := r.search(ctx, b, want, func(g string) bool { return tries[g] != nil && tries[g].won.Load() })
			if err := b.Err(); err != nil {
				r.note("", "android: searching the network for paired phones: "+err.Error())
			}
			b.Close()
			for _, g := range want {
				addr := found[g]
				if addr == "" {
					missed[g] = true
					continue
				}
				if serialInUse(entries, addr) {
					r.remember(addr, g) // connected by address already, by someone else
					r.p.keepPairedAddr(g, addr)
					continue
				}
				if r.holdBack(g, addr) {
					missed[g] = true
					continue
				}
				ok, took := r.exclusive(tries[g], func() bool {
					r.note(g, "android: "+g+" is on the network at "+addr+", but the adb server has not connected it (its discovery stops working after the computer's network changes); connecting")
					if _, err := r.connect(ctx, g, addr, addr); err != nil {
						return false
					}
					r.remember(addr, g)
					r.p.keepPairedAddr(g, addr)
					return true
				})
				switch {
				case took:
				case ok:
					connected = true
				default:
					missed[g] = true
				}
			}
		}
	}
	keptAway := false
	for g, t := range tries {
		<-t.wait
		if t.won.Load() {
			connected = true
			delete(missed, g)
		} else if missed[g] {
			keptAway = true
		}
	}
	r.setKeptAway(keptAway)
	return len(missed) > 0, connected
}

func (r *reconnector) setKeptAway(v bool) {
	r.mu.Lock()
	r.keptAway = v
	r.mu.Unlock()
}

// keepNamed keeps the addresses of the paired phones that have a
// connection named after them, as the server's discovery lists them: a
// connection the server made by itself (one Pinky did not start), or one
// made before Pinky kept addresses.
func (r *reconnector) keepNamed(guids, want []string, entries []adb.DeviceEntry, listed []adb.MDNSService) {
	for _, e := range entries {
		g, ok := strings.CutSuffix(e.Serial, connectSuffix)
		if !ok || e.State != adb.StateDevice || !slices.Contains(guids, g) || slices.Contains(want, g) {
			continue
		}
		if addr := serviceAddr(listed, g); addr != "" {
			r.p.keepPairedAddr(g, addr)
		}
	}
}

// tryKept starts a try of the kept address of each phone in want that has
// one, and returns the tries by guid. A try probes the address with TCP
// first: adb connect at an address nothing answers waits for the system's
// TCP timeout (75 s on macOS) and holds up the server's other requests.
func (r *reconnector) tryKept(ctx context.Context, want []string, entries []adb.DeviceEntry) map[string]*keptTry {
	tries := map[string]*keptTry{}
	for _, g := range want {
		addr := r.p.net.pairedAddr(g)
		// An address in use is another device's (onWiFi found no
		// connection of this phone's).
		if addr == "" || serialInUse(entries, addr) || r.holdBack(g, addr) {
			continue
		}
		t := &keptTry{wait: make(chan struct{})}
		tries[g] = t
		go func() {
			defer close(t.wait)
			if err := r.probe(ctx, addr); err != nil {
				r.note(g, "android: "+g+" does not answer at "+addr+", the address it was last connected at: "+err.Error())
				return
			}
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.done {
				return
			}
			r.note(g, "android: "+g+" answers at "+addr+", the address it was last connected at, though nothing on the network announces it; connecting")
			if r.keptPhone(ctx, g, addr) {
				t.done = true
				t.won.Store(true)
			}
		}()
	}
	return tries
}

// exclusive connects a phone with connect unless its kept address try
// connected it already, and keeps that try from connecting it again. It
// reports whether connect connected the phone, and whether the try had.
func (r *reconnector) exclusive(t *keptTry, connect func() bool) (ok, took bool) {
	if t == nil {
		return connect(), false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return false, true
	}
	t.done = connect()
	return t.done, false
}

// keptPhone connects the paired phone guid at its kept address and keeps
// the connection when the phone there is that phone; another device that
// took the address over is disconnected, and the address forgotten.
func (r *reconnector) keptPhone(ctx context.Context, guid, addr string) bool {
	p := r.p
	got, err := r.connect(ctx, guid, addr, addr)
	if err != nil {
		return false
	}
	same, sure := r.isPhone(ctx, guid, got)
	if same {
		r.remember(got, guid)
		p.keepPairedAddr(guid, got)
		p.cfg.logf("android: connected %s at %s, the address it was last connected at", guid, got)
		return true
	}
	if err := p.cfg.Client.Disconnect(ctx, got); err != nil && !errors.Is(err, adb.ErrDeviceNotFound) {
		p.cfg.logf("android: disconnecting %s: %v", got, err)
	}
	p.dropProps(got)
	if sure {
		p.cfg.logf("android: %s is no longer at %s, where another device answers; Pinky forgets that address", guid, got)
		if err := p.net.setPairedAddr(guid, ""); err != nil {
			p.cfg.logf("android: forgetting %s's address: %v", guid, err)
		}
	} else {
		p.cfg.logf("android: connected to %s for %s, but could not tell which phone answers there; trying again later", got, guid)
	}
	return false
}

// isPhone reports whether the device connected at serial is the paired
// phone guid: its hardware serial is the one in the guid, or, for a phone
// whose shell reads no serial, it has the mark Pinky left on the phone
// (identity.go). sure is false when it could not tell, as the connection
// did not come online.
func (r *reconnector) isPhone(ctx context.Context, guid, serial string) (same, sure bool) {
	p := r.p
	want := guidSerial(guid)
	if want == "" {
		return false, true
	}
	ctx, cancel := context.WithTimeout(ctx, keptVerify)
	defer cancel()
	var e adb.DeviceEntry
	for {
		entries, err := p.cfg.Client.Devices(ctx)
		if err == nil {
			i := slices.IndexFunc(entries, func(e adb.DeviceEntry) bool { return e.Serial == serial })
			if i >= 0 && entries[i].State == adb.StateDevice {
				e = entries[i]
				break
			}
		}
		if sleepCtx(ctx, 250*time.Millisecond) != nil {
			return false, false
		}
	}
	pr, err := p.deviceProps(ctx, e)
	if err != nil {
		return false, false
	}
	if pr.HardwareSerial != "" {
		return pr.HardwareSerial == want, true
	}
	if p.ids == nil {
		// Nothing else tells this phone from another.
		return false, true
	}
	v, err := p.ids.check(ctx, serial, want)
	if err != nil {
		return false, false
	}
	return v == idSame, true
}

// connect has the adb server connect to the paired phone guid by target,
// its service name or its address addr, and returns what it connected to. A
// failure is the phone's (refuse), unless the server cannot reach the
// local network while Pinky can: then the driver restarts a server it
// started (offnet.go) and connects to addr through the new one
// (afterRestart), and otherwise leaves the phone to be tried again at the
// next pass, as it is not the phone that refused.
func (r *reconnector) connect(ctx context.Context, guid, target, addr string) (string, error) {
	p := r.p
	err := p.cfg.Client.Connect(ctx, target)
	if err == nil {
		p.server.reached()
		return target, nil
	}
	if !p.serverCutOff(ctx, addr, err) {
		r.refuse(ctx, guid, target, err)
		return "", err
	}
	if !p.serverOffNetwork(ctx, addr) {
		return "", err
	}
	err = p.afterRestart(ctx, addr, func() error { return p.cfg.Client.Connect(ctx, addr) }, func(error) bool { return true })
	if err != nil {
		p.cfg.logf("android: connecting to %s at %s through the new adb server: %v; trying again at the next pass", guid, addr, err)
		return "", err
	}
	return addr, nil
}

// onWiFi reports whether a paired phone is connected on the computer's
// network already in a way that missing does not see: at an address that
// neither the server's discovery nor this driver connected, such as adb
// tcpip's port 5555, another tool's connection, one that Pinky made before
// it restarted, or the phone's other address (IPv6 rather than IPv4). One
// more connection would be the same phone again. A cable's and Tailscale's
// do not count: the server's discovery would connect the phone at home all
// the same.
func (r *reconnector) onWiFi(ctx context.Context, guid string, entries []adb.DeviceEntry) bool {
	serial := guidSerial(guid)
	if serial == "" {
		return false
	}
	for _, e := range entries {
		if e.State != adb.StateDevice || !e.Wireless() || viaTailnet(e.Serial) {
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pr, err := r.p.deviceProps(pctx, e)
		cancel()
		if err == nil && r.p.phoneID(e, pr) == serial {
			return true
		}
	}
	return false
}

// guidAt returns the guid of the paired phone this driver connected at an
// address, "" for an address it did not connect.
func (r *reconnector) guidAt(addr string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byAddr[addr]
}

// holdBack reports whether a phone refused a connection at this address
// too recently to try again.
func (r *reconnector) holdBack(guid, addr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.refused[guid]
	return ok && f.addr == addr && r.now().Before(f.until)
}

// refuse notes a failed connection and when to try that address again.
func (r *reconnector) refuse(ctx context.Context, guid, addr string, err error) {
	if r.p.server.justRestarted(time.Now()) {
		// The server went, or came just now, because Pinky restarted it
		// (offnet.go): not the phone's doing.
		r.p.cfg.logf("android: connecting to %s at %s while Pinky restarts the adb server: %v; trying again at the next pass", guid, addr, err)
		return
	}
	_, verr := r.p.cfg.Client.Version(ctx)
	quit := errors.Is(verr, adb.ErrServerNotRunning)
	r.mu.Lock()
	f := r.refused[guid]
	if f.addr != addr {
		f = refusal{addr: addr}
	}
	f.count++
	wait := refusedFirst
	for i := 1; i < f.count && wait < refusedMax; i++ {
		wait *= 3
	}
	if quit || wait > refusedMax {
		wait = refusedMax
	}
	f.until = r.now().Add(wait)
	r.refused[guid] = f
	r.mu.Unlock()
	if quit {
		r.p.cfg.logf("android: the adb server quit while connecting to %s at %s (%v); Pinky leaves that phone alone for %v", guid, addr, err, wait)
		return
	}
	r.p.cfg.logf("android: connecting to %s at %s: %v; trying again in %v", guid, addr, err, wait)
}

// missing returns the paired phones' guids that have no connection in use:
// none named after them by the server, and none at an address the driver
// connected them at.
func (r *reconnector) missing(guids []string, entries []adb.DeviceEntry) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	present := map[string]bool{}
	for _, e := range entries {
		if !inUse(e.State) {
			continue
		}
		if g, ok := strings.CutSuffix(e.Serial, connectSuffix); ok {
			present[g] = true
		}
		if g, ok := r.byAddr[e.Serial]; ok {
			present[g] = true
		}
	}
	var out []string
	for _, g := range guids {
		if !present[g] && !slices.Contains(out, g) {
			out = append(out, g)
		}
	}
	return out
}

// search waits up to reconnectSearch for the phones' connect services and
// returns their addresses by guid. It stops waiting for a phone once done
// says it was connected another way.
func (r *reconnector) search(ctx context.Context, b Browse, want []string, done func(guid string) bool) map[string]string {
	found := map[string]string{}
	deadline := time.Now().Add(reconnectSearch)
	for {
		svcs := b.Services()
		left := 0
		for _, g := range want {
			if a := serviceAddr(svcs, g); a != "" {
				found[g] = a
			} else if !done(g) {
				left++
			}
		}
		if left == 0 || !time.Now().Before(deadline) {
			return found
		}
		if sleepCtx(ctx, 200*time.Millisecond) != nil {
			return found
		}
	}
}

// keepPairedAddr keeps the address a paired phone was connected at just
// now, for the reconnector to try when the phone does not show up on the
// network.
func (p *Provider) keepPairedAddr(guid, addr string) {
	if p.wifi == nil || guid == "" || !isAddr(addr) {
		return
	}
	if err := p.net.setPairedAddr(guid, addr); err != nil {
		p.cfg.logf("android: keeping %s's address %s: %v", guid, addr, err)
	}
}

// connectedNamed forgets a phone's failed connections once it connected by
// its name.
func (r *reconnector) connectedNamed(guid string) {
	r.mu.Lock()
	delete(r.refused, guid)
	r.mu.Unlock()
}

func (r *reconnector) remember(addr, guid string) {
	r.mu.Lock()
	r.byAddr[addr] = guid
	delete(r.refused, guid)
	r.mu.Unlock()
}

// drop forgets a connection by address once it is gone.
func (r *reconnector) drop(addr string) {
	r.mu.Lock()
	delete(r.byAddr, addr)
	r.mu.Unlock()
}

// note logs a message about a guid ("" for the search) when it differs
// from the last one.
func (r *reconnector) note(guid, msg string) {
	r.mu.Lock()
	same := r.noted[guid] == msg
	r.noted[guid] = msg
	r.mu.Unlock()
	if !same {
		r.p.cfg.logf("%s", msg)
	}
}

// serviceAddr returns the address of a phone's connect service.
func serviceAddr(svcs []adb.MDNSService, guid string) string {
	for _, s := range svcs {
		if s.Type == adb.ServiceConnect && s.Instance == guid {
			return s.Addr
		}
	}
	return ""
}

// inUse reports whether a connection in this state is there or on its way.
func inUse(state string) bool {
	return state == adb.StateDevice || state == adb.StateConnecting || state == adb.StateAuthorizing
}

func serialInUse(entries []adb.DeviceEntry, serial string) bool {
	for _, e := range entries {
		if e.Serial == serial && inUse(e.State) {
			return true
		}
	}
	return false
}

func inUseSerials(entries []adb.DeviceEntry) string {
	var s []string
	for _, e := range entries {
		if inUse(e.State) {
			s = append(s, e.Serial)
		}
	}
	sort.Strings(s)
	return strings.Join(s, " ")
}
