//go:build ios

// Command core is Pinky on the iPhone as a C library for the iPhone app
// (go build -buildmode=c-archive; see ../build.sh). The app's packet-tunnel
// extension hosts it, since iOS keeps a VPN extension running while other
// apps are in front; a build without the extension (a free Apple ID cannot
// sign one) hosts it in the app.
//
// It connects to the phone it runs on through its own loopback: a packet
// tunnel (Pinky's or LocalDevVPN's) turns packets for 10.7.0.1 back to the
// phone, where remotepairingd answers as it would a computer on the
// network. Over that it pair-verifies with the pairing the app holds, opens
// a TLS-PSK tunnel and hands it to Pinky's iOS driver, which ios/onphone's
// core drives.
package main

/*
#include <stdlib.h>
#include <os/log.h>
#include <mach/mach.h>
#include <ifaddrs.h>
#include <net/if.h>
#include <netinet/in.h>
#include <arpa/inet.h>

static void pinky_log(int level, const char *s) {
	os_log_with_type(OS_LOG_DEFAULT, level ? OS_LOG_TYPE_ERROR : OS_LOG_TYPE_DEFAULT, "%{public}s", s);
}

// pinky_tunnel_index is the index of the interface holding an IPv4 address
// in 10.7.0.0/16, the tunnel's own (10.7.0.2 in Pinky's reflector, 10.7.1.1
// or 10.7.0.2 in LocalDevVPN's), or 0. getifaddrs, because the sandbox
// denies the routing sysctl Go's net.Interfaces reads.
static unsigned int pinky_tunnel_index(void) {
	struct ifaddrs *list, *i;
	unsigned int idx = 0;
	if (getifaddrs(&list) != 0) return 0;
	for (i = list; i; i = i->ifa_next) {
		if (!i->ifa_addr || i->ifa_addr->sa_family != AF_INET) continue;
		uint32_t a = ntohl(((struct sockaddr_in *)i->ifa_addr)->sin_addr.s_addr);
		if ((a >> 16) == ((10u << 8) | 7u)) { idx = if_nametoindex(i->ifa_name); break; }
	}
	freeifaddrs(list);
	return idx;
}

static unsigned long long pinky_footprint(void) {
	task_vm_info_data_t info;
	mach_msg_type_number_t count = TASK_VM_INFO_COUNT;
	if (task_info(mach_task_self(), TASK_VM_INFO, (task_info_t)&info, &count) != KERN_SUCCESS) return 0;
	return info.phys_footprint;
}
*/
import "C"

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/ios"
	"github.com/DIYgod/pinky/ios/onphone"
	"github.com/DIYgod/pinky/ios/rppair"
)

func main() {}

// config is PinkyStart's JSON.
type config struct {
	Pairing   string `json:"pairing"`   // the pairing file, base64; empty until the person pairs
	UDID      string `json:"udid"`      // the phone's
	Addr      string `json:"addr"`      // remotepairingd through the reflector, "10.7.0.1:<port>"
	Interface string `json:"interface"` // "tunnel" binds to the tunnel interface; "" routes, then binds if that fails
	Self      string `json:"self"`      // the app's bundle id
	Host      string `json:"host"`      // "extension" or "app", for the log
	DataDir   string `json:"data_dir"`  // history and rules
	Version   string `json:"version"`
	MemoryMB  int    `json:"memory_mb"`   // Go's soft memory limit
	Heartbeat int    `json:"heartbeat_s"` // seconds between memory lines in the log; 0 for none
}

var (
	mu   sync.Mutex
	core *onphone.Core
	cfg  config
	log  *slog.Logger
	// linked is the core having a pairing to connect with.
	linked bool
)

func logLine(level int, s string) {
	cs := C.CString(s)
	C.pinky_log(C.int(level), cs)
	C.free(unsafe.Pointer(cs))
}

// osLog writes slog's lines to the unified log, with the prefix pinky.
type osLog struct{}

func (osLog) Write(p []byte) (int, error) {
	s := strings.TrimRight(string(p), "\n")
	level := 0
	if strings.Contains(s, "level=ERROR") {
		level = 1
	}
	logLine(level, "pinky "+s)
	return len(p), nil
}

func fail(err error) *C.char {
	b, _ := json.Marshal(map[string]any{"ok": false, "error": err.Error()})
	return C.CString(string(b))
}

// PinkyStart starts the core and its connection to the phone. It returns
// {"ok":true} or {"ok":false,"error":...}; free the result with PinkyFree.
// A second call while the core runs changes nothing, unless the core had
// no pairing and the call brings one (the person just paired on the
// phone, ios/core/pair.go): then it connects with it.
//
//export PinkyStart
func PinkyStart(js *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()
	var c config
	if err := json.Unmarshal([]byte(C.GoString(js)), &c); err != nil {
		return fail(err)
	}
	var id *rppair.Identity
	if c.Pairing != "" {
		raw, err := base64.StdEncoding.DecodeString(c.Pairing)
		if err == nil {
			var i rppair.Identity
			i, err = rppair.ParseIdentity(raw)
			id = &i
		}
		if err != nil {
			return fail(fmt.Errorf("pairing: %w", err))
		}
	}
	if core != nil {
		if id != nil && !linked {
			linked = true
			if c.UDID != "" {
				cfg.UDID = c.UDID
			}
			log.Info("paired on the phone")
			go connectLoop(*id)
		}
		return C.CString(`{"ok":true}`)
	}
	if c.Addr == "" {
		c.Addr = "10.7.0.1:49152"
	}
	if c.MemoryMB > 0 {
		debug.SetMemoryLimit(int64(c.MemoryMB) << 20)
	}
	cfg = c
	log = slog.New(slog.NewTextHandler(osLog{}, &slog.HandlerOptions{Level: slog.LevelInfo}))
	k, err := onphone.New(onphone.Options{ID: c.UDID, Self: c.Self, DataDir: c.DataDir, Version: c.Version, Log: log})
	if err != nil {
		return fail(err)
	}
	core = k
	if id == nil {
		core.Disconnected(driver.StateNeedsSetup, onphone.ReasonNotPaired, "Pinky is not paired with this iPhone yet.")
	} else {
		linked = true
		go connectLoop(*id)
	}
	if c.Heartbeat > 0 {
		go heartbeat(time.Duration(c.Heartbeat) * time.Second)
	}
	log.Info("started", "host", c.Host, "go", runtime.Version(), "version", c.Version)
	return C.CString(`{"ok":true}`)
}

func current() *onphone.Core {
	mu.Lock()
	defer mu.Unlock()
	return core
}

// PinkyCall answers one request (see ios/onphone). It may block: wait
// until the state changes, start and decide while Pinky's app leaves the
// screen. Call it off the main thread.
//
//export PinkyCall
func PinkyCall(js *C.char) *C.char {
	k := current()
	if k == nil {
		return fail(errors.New("Pinky's core is not running"))
	}
	return C.CString(string(k.Call([]byte(C.GoString(js)))))
}

// PinkyStop stops the task at once (the Live Activity's Stop).
//
//export PinkyStop
func PinkyStop() {
	if k := current(); k != nil {
		k.Stop()
	}
}

// PinkyUIActive records whether Pinky's own app is in front.
//
//export PinkyUIActive
func PinkyUIActive(on C.int) {
	if k := current(); k != nil {
		k.SetUI(on != 0)
	}
}

// PinkyComputer records that Pinky's app, in test mode, hosts
// WebDriverAgent for Pinky on a computer (ios/onphone, claims.go).
//
//export PinkyComputer
func PinkyComputer() {
	if k := current(); k != nil {
		k.ComputerSeen()
	}
}

// PinkyFree frees a string PinkyStart or PinkyCall returned.
//
//export PinkyFree
func PinkyFree(p *C.char) { C.free(unsafe.Pointer(p)) }

// connectLoop keeps the phone connected: it connects, hands the Device to
// the core, waits for the tunnel to close, and tries again, waiting longer
// after each failure.
func connectLoop(id rppair.Identity) {
	wait := time.Second
	for {
		start := time.Now()
		d, err := connect(id)
		if err != nil {
			reason, state := onphone.ReasonConnecting, driver.StateOffline
			switch {
			case errors.Is(err, errNoTunnel):
				reason, state = onphone.ReasonVPNOff, driver.StateNeedsSetup
			case errors.Is(err, rppair.ErrUnknownPeer):
				reason, state = onphone.ReasonUnpaired, driver.StateNeedsSetup
			}
			log.Warn("connect", "err", err, "retry", wait)
			core.Disconnected(state, reason, err.Error())
			time.Sleep(wait)
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = time.Second
		log.Info("connected", "in", time.Since(start).Round(time.Millisecond), "services", len(d.Services()))
		core.Connected(d)
		<-d.Done()
		log.Info("tunnel closed")
		core.Disconnected(driver.StateOffline, onphone.ReasonConnecting, "Connecting to this iPhone…")
	}
}

// bound is the interface index dials bind to, 0 for none. The extension
// binds to the tunnel interface from the start, since its own sockets skip
// its tunnel; the app follows the routes and binds only if that fails.
var bound int

var errNoTunnel = errors.New("no interface has an address in 10.7.0.0/16: is the VPN on?")

func tunnelInterface() (int, error) {
	if i := int(C.pinky_tunnel_index()); i != 0 {
		return i, nil
	}
	return 0, errNoTunnel
}

func dialTCP(ctx context.Context, addr string, ifIndex int) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	if ifIndex != 0 {
		d.Control = func(network, address string, rc syscall.RawConn) error {
			var serr error
			if err := rc.Control(func(fd uintptr) { serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex) }); err != nil {
				return err
			}
			return serr
		}
	}
	return d.DialContext(ctx, "tcp", addr)
}

func connect(id rppair.Identity) (*ios.Device, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, _, _ := net.SplitHostPort(cfg.Addr)
	dial := func(ctx context.Context, addr string) (net.Conn, error) { return dialTCP(ctx, addr, bound) }
	if cfg.Interface != "" && bound == 0 {
		idx, err := tunnelInterface()
		if err != nil {
			return nil, err
		}
		bound = idx
	}
	conn, err := dial(ctx, cfg.Addr)
	if err != nil && bound == 0 {
		idx, ierr := tunnelInterface()
		if ierr != nil {
			return nil, fmt.Errorf("remotepairingd: %v: %w", err, ierr)
		}
		c, err2 := dialTCP(ctx, cfg.Addr, idx)
		if err2 != nil {
			return nil, fmt.Errorf("remotepairingd: %w; bound to the tunnel: %v", err, err2)
		}
		log.Info("dial: routed failed; bound to the tunnel works", "err", err, "interface", idx)
		bound, conn, err = idx, c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("remotepairingd: %w", err)
	}
	s, err := rppair.Verify(ctx, conn, id)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("pair-verify: %w", err)
	}
	t, err := s.Tunnel(ctx, func(ctx context.Context, port int) (net.Conn, error) {
		return dial(ctx, net.JoinHostPort(host, fmt.Sprint(port)))
	})
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("tunnel: %w", err)
	}
	model, _ := s.Peer["model"].(string)
	info := driver.Info{ID: cfg.UDID, Platform: driver.IOS, Name: "This iPhone", Model: model, Connection: "loopback",
		Backend: "remotepairing"}
	d, err := ios.OpenOverTunnel(ctx, info, t, log)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("driver: %w", err)
	}
	// The pairing session must stay open while its tunnel lives.
	go func() { <-d.Done(); s.Close() }()
	return d, nil
}

func heartbeat(every time.Duration) {
	var ms runtime.MemStats
	for range time.Tick(every) {
		runtime.ReadMemStats(&ms)
		log.Info("heartbeat", "host", cfg.Host, "footprint_mb", uint64(C.pinky_footprint())>>20,
			"heap_mb", ms.HeapInuse>>20, "goroutines", runtime.NumGoroutine())
	}
}
