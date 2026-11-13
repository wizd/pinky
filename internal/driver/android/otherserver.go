package android

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
)

// A phone that another adb server holds (GitHub issue #95, 2026-11-12). A
// phone's USB interface goes to one adb server (adb.OtherServers): with a
// second server running on another port, which some tools start for
// themselves, a phone on a cable may be held by that one, and the server
// Pinky uses never lists it. Its log says "Could not open interface:
// e00002c5" (macOS: another process has the interface) again and again.
// The checkup said only that the phone was not connected, and pointed at
// the cable and USB debugging.
//
// So List has the driver look now and then (otherScanEvery) for other adb
// servers on this computer (Config.OtherServers), and ask each, with
// host:devices-l alone, which phones it has on a cable. One that holds a
// phone the server Pinky uses does not list is what CheckHost reports
// (adb_other_server), with the server's port and the phone's model. Pinky
// leaves that server running, as it does every server another program
// started: stopping it would take the phones away from that program.

// otherScanEvery is how often List looks for other adb servers: ps takes
// some 40 ms on a Mac with a thousand processes, and a server holding a
// phone stays until someone stops it.
const otherScanEvery = 30 * time.Second

// otherAskWait is how long another server may take to list its devices.
const otherAskWait = 2 * time.Second

// heldPhone is a phone on a cable that another adb server holds.
type heldPhone struct {
	port   int
	serial string // never in Detail: people paste it into public issues
	model  string // as adb reports it, with spaces; "" when it does not
}

// otherServers keeps what the last look at the other adb servers found.
type otherServers struct {
	mu      sync.Mutex
	running bool
	next    time.Time
	listed  map[string]bool // the phones the server Pinky uses lists, by serial and id
	held    []heldPhone
}

// kickOthers records the phones that List found, drops the held phones
// among them, and starts a look at the other adb servers when one is due.
// It returns at once.
func (p *Provider) kickOthers(entries []adb.DeviceEntry, infos []driver.Info) {
	if p.cfg.OtherServers == nil || !p.cfg.Client.Local() {
		return
	}
	listed := map[string]bool{}
	for _, e := range entries {
		if e.State == adb.StateDevice || e.State == adb.StateUnauthorized || e.State == adb.StateAuthorizing {
			listed[e.Serial] = true
		}
	}
	for _, info := range infos {
		if info.State == driver.StateOnline || info.State == driver.StateUnauthorized {
			listed[info.ID] = true
		}
	}
	o := &p.others
	now := time.Now()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.listed = listed
	o.held = p.stillHeld(o.held, listed)
	if o.running || now.Before(o.next) {
		return
	}
	o.running, o.next = true, now.Add(otherScanEvery)
	go p.scanOthers()
}

// stillHeld keeps the held phones that the server Pinky uses does not
// list and that the driver may use.
func (p *Provider) stillHeld(held []heldPhone, listed map[string]bool) []heldPhone {
	var out []heldPhone
	for _, h := range held {
		if !listed[h.serial] && p.mayUseID(h.serial) && !p.forgot(h.serial) {
			out = append(out, h)
		}
	}
	return out
}

// scanOthers looks at the other adb servers, and records the phones on a
// cable they hold.
func (p *Provider) scanOthers() {
	held, err := p.findHeld()
	o := &p.others
	o.mu.Lock()
	defer o.mu.Unlock()
	o.running = false
	if err != nil {
		p.cfg.logf("android: looking for other adb servers: %v", err)
		return // what the last look found stands
	}
	held = p.stillHeld(held, o.listed)
	for _, h := range held {
		if !slices.Contains(o.held, h) {
			p.cfg.infof("android: another program's adb server, on port %d, holds %s over USB, which the adb server Pinky uses cannot reach; Pinky leaves it running", h.port, nonEmptyModel(h.model))
		}
	}
	o.held = held
}

// findHeld asks each other adb server for its devices (host:devices-l,
// nothing else), and returns those on a cable: their USB interface is that
// server's.
func (p *Provider) findHeld() ([]heldPhone, error) {
	ports, err := p.cfg.OtherServers()
	if err != nil {
		return nil, err
	}
	var held []heldPhone
	for _, port := range ports {
		c := &adb.Client{Addr: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
		ctx, cancel := context.WithTimeout(context.Background(), otherAskWait)
		entries, err := c.Devices(ctx)
		cancel()
		if err != nil {
			p.cfg.logf("android: asking the adb server on port %d for its devices: %v", port, err)
			continue
		}
		for _, e := range entries {
			if e.USB == "" || e.State == adb.StateNoPerm || emulatorSerial(e.Serial) {
				continue // no permission for it: the server does not hold it
			}
			held = append(held, heldPhone{port: port, serial: e.Serial, model: strings.ReplaceAll(e.Model, "_", " ")})
		}
	}
	return held, nil
}

// checkOthers is CheckHost's answer about other adb servers: the first
// that holds a phone the server Pinky uses does not list. Its Detail
// begins "Another program's adb server, on port <port>," which the client
// reads the port from.
func (p *Provider) checkOthers() driver.HostCheck {
	o := &p.others
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.held) == 0 {
		return driver.HostCheck{}
	}
	port := o.held[0].port
	var models []string
	for _, h := range o.held {
		if h.port == port {
			models = append(models, nonEmptyModel(h.model))
		}
	}
	phones := strings.Join(models, " and ")
	return driver.HostCheck{Missing: hostADBOtherServer,
		Detail: fmt.Sprintf("Another program's adb server, on port %d, holds %s over USB, so the adb server Pinky uses cannot reach it. Quit the program that started it, or, once you are sure nothing else uses it, run `adb -P %d kill-server`: Pinky then gets the phone.", port, phones, port)}
}

func nonEmptyModel(model string) string {
	if model == "" {
		return "a phone"
	}
	return model
}
