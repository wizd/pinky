//go:build ios

package main

import "C"

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/DIYgod/pinky/ios/rppair"
)

// Pairing from the phone itself, iOS 27 and later (rppair.Respond): the app
// listens, advertises the port as a pairable host over Bonjour (the core
// cannot: multicast needs an entitlement the system's Bonjour does not), and
// the person picks it in Settings › Privacy & Security › Developer Mode and
// types the PIN the app shows. The app runs this, not the extension: it
// needs no tunnel, only Wi-Fi, and the app stays alive meanwhile with its
// silent audio.

// pairRun is a pairing that listens.
type pairRun struct {
	ln     net.Listener
	events chan map[string]any
	cancel context.CancelFunc
}

var (
	pairMu  sync.Mutex
	pairing *pairRun
)

// pairStart is PinkyPairStart's JSON.
type pairStart struct {
	Name string `json:"name"` // what the phone lists the app as
}

// PinkyPairStart starts listening for the phone: {"ok":true,"port":…,
// "identifier":…,"txt":{…}}, the service's name and its TXT record, which
// the app advertises as rppair.PairableService. A second call stops the
// first pairing.
//
//export PinkyPairStart
func PinkyPairStart(js *C.char) *C.char {
	var p pairStart
	if err := json.Unmarshal([]byte(C.GoString(js)), &p); err != nil {
		return fail(err)
	}
	if p.Name == "" {
		p.Name = "Pinky"
	}
	id, err := rppair.NewIdentity()
	if err != nil {
		return fail(err)
	}
	irk := make([]byte, 16)
	if _, err := rand.Read(irk); err != nil {
		return fail(err)
	}
	host := rppair.Host{Name: p.Name, Model: "Mac17,7", AltIRK: irk}
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	run := &pairRun{ln: ln, events: make(chan map[string]any, 8), cancel: cancel}
	pairMu.Lock()
	if pairing != nil {
		pairing.stop()
	}
	pairing = run
	pairMu.Unlock()
	go run.serve(ctx, id, host)
	b, _ := json.Marshal(map[string]any{
		"ok": true, "port": ln.Addr().(*net.TCPAddr).Port,
		"identifier": id.Identifier, "txt": host.TXT(id.Identifier),
	})
	return C.CString(string(b))
}

// PinkyPairNext waits for what happens next in the pairing: {"pin":…} to
// show, {"error":…} for a try that failed (the app keeps listening),
// {"paired":true,"udid":…,"pairing":…,"name":…}, the pairing to keep, which
// ends it, or {"stopped":true}. Call it off the main thread.
//
//export PinkyPairNext
func PinkyPairNext() *C.char {
	pairMu.Lock()
	run := pairing
	pairMu.Unlock()
	if run == nil {
		return C.CString(`{"stopped":true}`)
	}
	ev, ok := <-run.events
	if !ok {
		return C.CString(`{"stopped":true}`)
	}
	b, _ := json.Marshal(ev)
	return C.CString(string(b))
}

// PinkyPairStop stops listening.
//
//export PinkyPairStop
func PinkyPairStop() {
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairing != nil {
		pairing.stop()
		pairing = nil
	}
}

func (r *pairRun) stop() {
	r.cancel()
	r.ln.Close()
}

// serve answers each phone that connects until one pairs.
func (r *pairRun) serve(ctx context.Context, id rppair.Identity, host rppair.Host) {
	defer close(r.events)
	send := func(ev map[string]any) {
		select {
		case r.events <- ev:
		case <-ctx.Done():
		}
	}
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return // stopped
		}
		// The person has a few minutes to type the PIN.
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		stop := context.AfterFunc(cctx, func() { conn.Close() })
		got, phone, err := rppair.Respond(cctx, conn, id, host, func(pin string) { send(map[string]any{"pin": pin}) })
		stop()
		cancel()
		conn.Close()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			why := err.Error()
			if errors.Is(err, rppair.ErrWrongPIN) {
				why = "wrong_pin"
			}
			send(map[string]any{"error": why})
			continue
		}
		raw, err := got.Marshal()
		if err != nil {
			send(map[string]any{"error": err.Error()})
			continue
		}
		send(map[string]any{
			"paired": true, "udid": phone.UDID, "name": phone.Name,
			"pairing": base64.StdEncoding.EncodeToString(raw),
		})
		r.ln.Close()
		return
	}
}
