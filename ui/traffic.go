package ui

import (
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/traffic"
)

// Read network traffic (docs/notes/traffic-capture.md): a switch per phone
// in Settings › Phones, off by default, that lets agents read the web
// requests the phone makes during their tasks (phone_traffic), through
// Pinky's own proxy. With it on, the person can open the phone's setup:
// the proxy setup link (a PAC file), Pinky's certificate, the steps on the
// phone and how to undo them, the hosts Pinky never decrypts, and on
// Android the switch that routes the phone through its cable. The gateway
// serves the setup files whenever the switch is on, so opening the setup
// changes nothing there.

// trafficSetter is the gateway's part of it.
type trafficSetter interface {
	SetTraffic(deviceID string, on bool) error
	Traffic(deviceID string) bool
}

// trafficSetuper is the gateway's part of the setup.
type trafficSetuper interface {
	TrafficSetup(deviceID string) (gateway.TrafficSetup, error)
	SetTrafficRoute(deviceID string, on bool) error
	SetTrafficBlocklist(deviceID string, hosts []string) ([]string, error)
}

// trafficEvery is how often an open setup reads the gateway again: where
// the proxy listens follows the phone's networks, which send no event.
const trafficEvery = 2 * time.Second

// trafficState is the switch's part of the client.
type trafficState struct {
	on      map[string]bool // Traffic(id), read with the devices
	busy    map[string]bool // a SetTraffic is under way
	open    map[string]bool // the person has the phone's setup open
	setup   map[string]gateway.TrafficSetup
	at      map[string]time.Time // when setup was read
	loading map[string]bool
	routing map[string]bool // a SetTrafficRoute is under way
	// newHost is what the person types into Never decrypt, bad the host
	// it refused; blocking: a SetTrafficBlocklist is under way.
	newHost  map[string]string
	bad      map[string]string
	blocking map[string]bool
}

func (s *trafficState) init() {
	if s.on == nil {
		*s = trafficState{on: map[string]bool{}, busy: map[string]bool{}, open: map[string]bool{},
			setup: map[string]gateway.TrafficSetup{}, at: map[string]time.Time{}, loading: map[string]bool{},
			routing: map[string]bool{}, newHost: map[string]string{}, bad: map[string]string{}, blocking: map[string]bool{}}
	}
}

// readTraffic asks the gateway which phones' traffic agents may read, off
// the main thread, as Devices is read.
func (a *app) readTraffic(devs []gateway.DeviceView) map[string]bool {
	ts, ok := a.api.(trafficSetter)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for _, d := range devs {
		out[d.Info.ID] = ts.Traffic(d.Info.ID)
	}
	return out
}

// setTraffics takes what readTraffic read.
func (a *app) setTraffics(on map[string]bool) {
	a.traffic.init()
	for id, v := range on {
		if !a.traffic.busy[id] {
			a.traffic.on[id] = v
			if !v {
				a.closeTrafficSetup(id)
			}
		}
	}
}

// forgetTraffic drops what the client keeps of a removed phone.
func (a *app) forgetTraffic(id string) {
	a.closeTrafficSetup(id)
	for _, m := range []map[string]bool{a.traffic.on, a.traffic.busy, a.traffic.loading, a.traffic.routing, a.traffic.blocking} {
		delete(m, id)
	}
	delete(a.traffic.setup, id)
	delete(a.traffic.at, id)
	delete(a.traffic.newHost, id)
	delete(a.traffic.bad, id)
}

// setTraffic turns reading a phone's traffic on or off.
func (a *app) setTraffic(d gateway.DeviceView, on bool) {
	ts, ok := a.api.(trafficSetter)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.traffic.busy[id] {
		return
	}
	a.traffic.init()
	a.traffic.on[id], a.traffic.busy[id] = on, true
	if !on {
		a.closeTrafficSetup(id)
	}
	go func() {
		err := ts.SetTraffic(id, on)
		now := ts.Traffic(id) // what the gateway kept, after an error too
		a.post(func() {
			a.traffic.busy[id], a.traffic.on[id] = false, now
			if err != nil {
				a.toasts = append(a.toasts, locales.TrafficChangeFailed(name, err.Error()))
			}
		})
	}()
}

// openTrafficSetup shows a phone's setup.
func (a *app) openTrafficSetup(id string) {
	a.traffic.open[id] = true
	delete(a.traffic.at, id) // read it now
}

// closeTrafficSetup hides it.
func (a *app) closeTrafficSetup(id string) {
	delete(a.traffic.open, id)
}

// trafficSetupOf reads a phone's setup off the main thread, again every
// trafficEvery while it shows.
func (a *app) trafficSetupOf(c *mg.Context, id string) (gateway.TrafficSetup, bool) {
	ts, ok := a.api.(trafficSetuper)
	if !ok {
		return gateway.TrafficSetup{}, false
	}
	s := &a.traffic
	if !s.loading[id] && time.Since(s.at[id]) >= trafficEvery {
		s.loading[id] = true
		go func() {
			st, err := ts.TrafficSetup(id)
			a.post(func() {
				s.loading[id], s.at[id] = false, time.Now()
				if err == nil {
					s.setup[id] = st
				} else {
					delete(s.setup, id)
				}
			})
		}()
	}
	c.After(trafficEvery)
	st, have := s.setup[id]
	return st, have
}

// setTrafficRoute turns routing an Android phone through its cable on or
// off.
func (a *app) setTrafficRoute(d gateway.DeviceView, on bool) {
	ts, ok := a.api.(trafficSetuper)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.traffic.routing[id] {
		return
	}
	a.traffic.routing[id] = true
	if st, have := a.traffic.setup[id]; have {
		st.Route = on
		a.traffic.setup[id] = st
	}
	go func() {
		err := ts.SetTrafficRoute(id, on)
		st, serr := ts.TrafficSetup(id)
		a.post(func() {
			a.traffic.routing[id] = false
			if serr == nil {
				a.traffic.setup[id], a.traffic.at[id] = st, time.Now()
			}
			if err != nil {
				a.toasts = append(a.toasts, locales.TrafficRouteChangeFailed(name, err.Error()))
			}
		})
	}()
}

// setTrafficBlocklist saves the hosts Pinky never decrypts for a phone.
func (a *app) setTrafficBlocklist(d gateway.DeviceView, hosts []string) {
	ts, ok := a.api.(trafficSetuper)
	id, name := d.Info.ID, d.Info.Name
	if !ok || a.traffic.blocking[id] {
		return
	}
	a.traffic.blocking[id] = true
	if st, have := a.traffic.setup[id]; have {
		st.Blocklist = hosts
		a.traffic.setup[id] = st
	}
	go func() {
		_, err := ts.SetTrafficBlocklist(id, hosts)
		st, serr := ts.TrafficSetup(id)
		a.post(func() {
			a.traffic.blocking[id] = false
			if serr == nil {
				a.traffic.setup[id], a.traffic.at[id] = st, time.Now()
			}
			if err != nil {
				a.toasts = append(a.toasts, locales.TrafficBlockFailed(name, err.Error()))
			}
		})
	}()
}

// blockTypedHost adds what the person typed to Never decrypt.
func (a *app) blockTypedHost(d gateway.DeviceView, list []string) {
	id := d.Info.ID
	typed := strings.TrimSpace(a.traffic.newHost[id])
	if typed == "" {
		return
	}
	h := traffic.CleanHost(typed)
	if h == "" || !strings.Contains(h, ".") {
		a.traffic.bad[id] = typed
		return
	}
	delete(a.traffic.bad, id)
	a.traffic.newHost[id] = ""
	if slices.Contains(list, h) {
		return
	}
	a.setTrafficBlocklist(d, append(slices.Clone(list), h))
}

// trafficRow is the "Read network traffic" switch with its words, and,
// with it on, the phone's setup.
func (a *app) trafficRow(c *mg.Context, d gateway.DeviceView, last bool) {
	if _, ok := a.api.(trafficSetter); !ok {
		return
	}
	a.traffic.init()
	id := d.Info.ID
	on := a.traffic.on[id]
	if a.toggleRowFor(c, "traffic-"+id, locales.TrafficTitle(), locales.TrafficDesc(), last && !on, &on) {
		a.setTraffic(d, on)
	}
	if !on {
		return
	}
	if _, ok := a.api.(trafficSetuper); !ok {
		return
	}
	open := a.traffic.open[id]
	settingRow(c, locales.TrafficSetupTitle(), locales.TrafficSetupDesc(), last && !open, func() {
		label := locales.TrafficSetupShow()
		if open {
			label = locales.TrafficSetupHide()
		}
		if button(c.Key("traffic-setup-"+id), label, btnOpt{small: true}).Clicked() {
			if open {
				a.closeTrafficSetup(id)
			} else {
				a.openTrafficSetup(id)
			}
		}
	})
	if open {
		a.trafficSetupRows(c, d, last)
	}
}

// trafficSetupRows are the open setup: the proxy setup link, the
// certificate, the steps on the phone and how to undo them, Never
// decrypt and, on Android, the cable route.
func (a *app) trafficSetupRows(c *mg.Context, d gateway.DeviceView, last bool) {
	p := pal(c)
	id := d.Info.ID
	st, have := a.trafficSetupOf(c, id)
	android := d.Info.Platform == driver.Android
	if st.PACURL != "" {
		a.copyRow(c, locales.TrafficPACURL(), st.PACURL, st.PACURL, locales.TrafficCopyPACURL(), locales.TrafficCopiedPACURL(), false)
	}
	if st.CertURL != "" {
		a.copyRow(c, locales.TrafficCertificate(), st.CertURL, st.CertURL, locales.TrafficCopyCertURL(), locales.TrafficCopiedCertURL(), false)
	}
	groupRow(c, mg.Column(c).Gap(sp2), false).Children(func() {
		switch {
		case !have:
			mg.Text(c, locales.TrafficSetupReading()).FontSize(fsSmall).TextColor(p.ink2)
		case st.Problem != "":
			mg.Text(c, trafficProblem(st, d.Info.Name)).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		case st.PACURL == "" && !st.Routed:
			mg.Text(c, locales.TrafficSetupWaiting()).FontSize(fsSmall).TextColor(p.ink2)
		}
		if st.CARenewed {
			mg.Text(c, locales.TrafficCARenewed()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.amber).Role(mg.RoleStatus)
		}
		if st.CAName != "" {
			info := locales.TrafficCAInfo(st.CAName, st.Fingerprint)
			if !st.CAExpires.IsZero() {
				info += " " + locales.TrafficCAExpires(st.CAExpires.Local().Format("2006-01-02"))
			}
			mg.Text(c, info).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2).Selectable()
		}
		steps := locales.TrafficStepsIOS()
		if android {
			steps = locales.TrafficStepsAndroid()
		}
		mg.Text(c, steps).FontSize(fsSmall).LineHeight(1.5)
		if android {
			mg.Text(c, locales.TrafficAndroidAppsWarning()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		}
		mg.Text(c, locales.TrafficOutsideTasks()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		remove := locales.TrafficRemoveIOS()
		if android {
			remove = locales.TrafficRemoveAndroid()
		}
		mg.Text(c, remove).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
	})
	a.trafficBlockRows(c, d, st, last && !(android && st.CanRoute))
	if android && st.CanRoute {
		route := st.Route
		desc := locales.TrafficRouteDesc(strconv.Itoa(trafficPortOf(st))) + " " + locales.TrafficRouteStranded()
		if a.toggleRowFor(c, "traffic-route-"+id, locales.TrafficRouteTitle(), desc, last, &route) {
			a.setTrafficRoute(d, route)
		}
	}
}

// trafficBlockRows are Never decrypt: the hosts the person added, and a
// field to add one.
func (a *app) trafficBlockRows(c *mg.Context, d gateway.DeviceView, st gateway.TrafficSetup, last bool) {
	p := pal(c)
	id := d.Info.ID
	busy := a.traffic.blocking[id]
	groupRow(c, mg.Column(c).Gap(sp2), last).Children(func() {
		mg.Text(c, locales.TrafficBlockTitle()).FontWeight(fwMedium)
		mg.Text(c, locales.TrafficBlockDesc()).FontSize(fsSmall).LineHeight(1.4).TextColor(p.ink2)
		for _, h := range st.Blocklist {
			h := h
			mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
				mg.Text(c, h).Grow(1).MinWidth(0).SingleLine().Selectable()
				if iconButton(c, icX, locales.TrafficBlockRemove(h), locales.TrafficBlockRemove(h), busy).Clicked() {
					a.setTrafficBlocklist(d, slices.DeleteFunc(slices.Clone(st.Blocklist), func(x string) bool { return x == h }))
				}
			})
		}
		mg.Row(c).Gap(8).AlignItems(mg.Center).Children(func() {
			typed := a.traffic.newHost[id]
			in := mg.TextInput(c.Key("traffic-block-"+id), &typed).Placeholder("bank.example.com").Label(locales.TrafficBlockLabel()).Grow(1)
			a.traffic.newHost[id] = typed
			add := button(c.Key("traffic-block-add-"+id), locales.TrafficBlockAdd(), btnOpt{small: true}).Disabled(busy)
			if !busy && (in.Submitted() || add.Clicked()) {
				a.blockTypedHost(d, st.Blocklist)
			}
		})
		if bad := a.traffic.bad[id]; bad != "" {
			mg.Text(c, locales.TrafficBlockInvalid(bad)).FontSize(fsSmall).TextColor(p.amber).Role(mg.RoleStatus)
		}
	})
}

// trafficPortOf is the port the phone's proxy is set to over its cable,
// or else the port the proxy listens at, from its addresses.
func trafficPortOf(st gateway.TrafficSetup) int {
	if st.Routed && st.RoutePort > 0 {
		return st.RoutePort
	}
	for _, addr := range st.Proxies {
		if _, port, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				return n
			}
		}
	}
	return gateway.TrafficPort
}

// trafficProblem says a TrafficSetup.Problem in the person's words.
func trafficProblem(st gateway.TrafficSetup, phone string) string {
	switch st.Problem {
	case gateway.TrafficListenFailed:
		host, port, err := net.SplitHostPort(st.ProblemAddr)
		if err != nil {
			host, port = st.ProblemAddr, strconv.Itoa(gateway.TrafficPort)
		}
		return locales.TrafficProblemListenFailed(port, host, phone)
	case gateway.TrafficRouteListenFailed:
		return locales.TrafficProblemRouteListenFailed(phone)
	case gateway.TrafficNoCA:
		return locales.TrafficProblemNoCA()
	case gateway.TrafficRouteFailed:
		return locales.TrafficProblemRouteFailed(phone)
	case gateway.TrafficRouteBusy:
		return locales.TrafficProblemRouteBusy(phone)
	}
	return locales.TrafficProblemNoAddress(phone)
}
