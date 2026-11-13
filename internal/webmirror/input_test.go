package webmirror

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

func TestToPicture(t *testing.T) {
	// A 100×200 stream picture (portrait as the phone sends it).
	for _, tc := range []struct {
		x, y float64
		turn int
		want screen.Point
	}{
		{0, 0, 0, screen.Point{X: 0, Y: 0}},
		{0.5, 0.25, 0, screen.Point{X: 50, Y: 50}},
		{1, 1, 0, screen.Point{X: 99, Y: 199}}, // clamped inside
		// Turned once clockwise for the page: the page's top-left corner
		// is the picture's bottom-left.
		{0, 0, 1, screen.Point{X: 0, Y: 199}},
		{1, 0, 1, screen.Point{X: 0, Y: 0}},
		{0, 1, 1, screen.Point{X: 99, Y: 199}},
		{0, 0, 2, screen.Point{X: 99, Y: 199}},
		{0, 0, 3, screen.Point{X: 99, Y: 0}},
		{1, 1, 3, screen.Point{X: 0, Y: 199}},
		{-3, 7, 0, screen.Point{X: 0, Y: 199}}, // outside the picture
	} {
		if got := toPicture(tc.x, tc.y, 100, 200, tc.turn); got != tc.want {
			t.Errorf("toPicture(%v, %v, turn %d) = %v, want %v", tc.x, tc.y, tc.turn, got, tc.want)
		}
	}
}

// controlConn connects with the control token and reads up to the phone's
// configuration, so touches have a picture size.
func controlConn(t *testing.T, s *Server, st *fakeStream, dial func() *websocket.Conn) *websocket.Conn {
	t.Helper()
	tok := s.ControlToken()
	if tok == "" {
		t.Fatal("no control token")
	}
	ws := dial()
	websocket.JSON.Send(ws, hello{Token: tok})
	if m := receive(t, ws); m.Type != "devices" || !m.Control {
		t.Fatalf("got %+v, want devices with control", m)
	}
	st.ch <- driver.Packet{Data: annexB(unhex(t, h264SPSHex), unhex(t, h264PPSHex)), Config: true}
	if m := receive(t, ws); m.Type != "config" {
		t.Fatalf("got %+v, want config", m)
	}
	return ws
}

func touchMsg(a string, x, y float64) clientMsg {
	return clientMsg{Type: "touch", A: a, X: x, Y: y}
}

func TestControlTouches(t *testing.T) {
	in := newInputLog()
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st, in: in})
	s.SetControl(true)
	ws := controlConn(t, s, st, func() *websocket.Conn { return dial(t, hs, hs.URL) })

	// A move or an up without a down is dropped; a drag goes through in
	// order, its moves maybe merged; then Back.
	websocket.JSON.Send(ws, touchMsg("move", 0.1, 0.1))
	websocket.JSON.Send(ws, touchMsg("up", 0.1, 0.1))
	websocket.JSON.Send(ws, touchMsg("down", 0.5, 0.5))
	for i := 1; i <= 20; i++ {
		websocket.JSON.Send(ws, touchMsg("move", 0.5, 0.5+float64(i)/100))
	}
	websocket.JSON.Send(ws, touchMsg("up", 0.5, 0.7))
	websocket.JSON.Send(ws, clientMsg{Type: "key", Key: "back"})
	websocket.JSON.Send(ws, clientMsg{Type: "key", Key: "power"}) // not a page key

	calls := in.wait(t, 3)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && calls[len(calls)-1] != "p1 key back"; {
		calls = in.wait(t, len(calls)+1)
	}
	if calls[0] != "p1 down 320,180" {
		t.Errorf("first call %q, want the down at the middle of 640×360", calls[0])
	}
	if got := calls[len(calls)-2:]; got[0] != "p1 up 320,251" || got[1] != "p1 key back" {
		t.Errorf("last calls %q, want the up then Back", got)
	}
	for _, c := range calls[1 : len(calls)-2] {
		if !strings.HasPrefix(c, "p1 move ") {
			t.Errorf("between down and up: %q", c)
		}
	}
}

func TestDisconnectLiftsFinger(t *testing.T) {
	in := newInputLog()
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st, in: in})
	s.SetControl(true)
	ws := controlConn(t, s, st, func() *websocket.Conn { return dial(t, hs, hs.URL) })
	websocket.JSON.Send(ws, touchMsg("down", 0.25, 0.5))
	websocket.JSON.Send(ws, touchMsg("move", 0.5, 0.5))
	in.wait(t, 2)
	ws.Close()
	calls := in.wait(t, 3)
	if len(calls) != 3 || calls[2] != "p1 up 320,180" {
		t.Fatalf("calls %q, want the finger lifted where it was", calls)
	}

	// The phone's finger is free again for the next page.
	ws2 := controlConn(t, s, st, func() *websocket.Conn { return dial(t, hs, hs.URL) })
	websocket.JSON.Send(ws2, touchMsg("down", 0, 0))
	if calls := in.wait(t, 4); len(calls) != 4 || calls[3] != "p1 down 0,0" {
		t.Fatalf("calls %q, want the second page's down", calls)
	}
}

func TestViewTokenCannotTouch(t *testing.T) {
	in := newInputLog()
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st, in: in})
	s.SetControl(true)
	ws := dial(t, hs, hs.URL)
	websocket.JSON.Send(ws, hello{Token: s.Token()})
	if m := receive(t, ws); m.Type != "devices" || m.Control {
		t.Fatalf("got %+v, want devices without control", m)
	}
	websocket.JSON.Send(ws, touchMsg("down", 0.5, 0.5))
	var b []byte
	if err := websocket.Message.Receive(ws, &b); err == nil {
		t.Fatal("a page that may only watch stayed connected after a touch")
	}
	time.Sleep(200 * time.Millisecond)
	in.mu.Lock()
	calls := in.calls
	in.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("calls %q from a page that may only watch", calls)
	}
}

func TestControlOff(t *testing.T) {
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st})

	// Turning control off cuts off the pages that use the phone, and
	// their token no longer works.
	s.SetControl(true)
	tok := s.ControlToken()
	ws := controlConn(t, s, st, func() *websocket.Conn { return dial(t, hs, hs.URL) })
	if w := s.Watchers(); len(w) != 1 || !w[0].Control {
		t.Errorf("Watchers() = %+v, want one that can use the phone", w)
	}
	s.SetControl(false)
	var b []byte
	if err := websocket.Message.Receive(ws, &b); err == nil {
		t.Fatal("a page kept control after control was turned off")
	}
	ws = dial(t, hs, hs.URL)
	websocket.JSON.Send(ws, hello{Token: tok})
	if m := receive(t, ws); m.Type != "error" || m.Error != "token" {
		t.Fatalf("got %+v, want the old control token refused", m)
	}
}

// Through Tailscale, control needs the person who owns this computer,
// whom `tailscale serve` names in Tailscale-User-Login (a public Funnel
// link or a tagged device has none), whatever Host the request names.
func TestControlThroughTailnetNeedsOwner(t *testing.T) {
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st})
	s.SetControl(true)
	for _, tc := range []struct {
		host    string
		headers map[string]string
		want    string
	}{
		{"mac.tail1234.ts.net:8443", nil, "error"},
		{"mac.tail1234.ts.net:8443", map[string]string{"Tailscale-User-Login": "someone@example.com"}, "error"},
		{"mac.tail1234.ts.net:8443", map[string]string{"Tailscale-User-Login": "Owner@example.com"}, "devices"},
		// A Host of the client's choosing, through `tailscale serve`.
		{"localhost:7466", map[string]string{"X-Forwarded-For": "100.101.2.2"}, "error"},
		{"localhost:7466", map[string]string{"X-Forwarded-For": "100.101.2.2", "Tailscale-User-Login": "owner@example.com"}, "devices"},
		{"localhost:7466", nil, "devices"},
	} {
		cfg, err := websocket.NewConfig("ws://"+tc.host+WebSocketPath, "http://"+tc.host)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range tc.headers {
			cfg.Header.Set(k, v)
		}
		conn, err := net.Dial("tcp", strings.TrimPrefix(hs.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		ws, err := websocket.NewClient(cfg, conn)
		if err != nil {
			t.Fatal(err)
		}
		ws.SetDeadline(time.Now().Add(5 * time.Second))
		websocket.JSON.Send(ws, hello{Token: s.ControlToken()})
		if m := receive(t, ws); m.Type != tc.want || (m.Type == "error" && m.Error != "tailnet_user") {
			t.Errorf("%s %v: got %+v, want %s", tc.host, tc.headers, m, tc.want)
		}
		ws.Close()
	}
}

// While the gateway is busy, a page cannot pile up input: downs and keys
// past maxQueue are dropped, and keys pass at keyRate after a burst.
func TestInputLimits(t *testing.T) {
	s := New(Options{})
	ss := &session{s: s, device: "p1", w: 100, h: 200, keys: keyBurst, keysAt: time.Now(), done: make(chan struct{})}
	ss.cond = sync.NewCond(&ss.mu) // no run: the gateway never takes the queue
	for range 3 * maxQueue {
		ss.touch(driver.TouchDown, 0.5, 0.5, 0)
		ss.touch(driver.TouchMove, 0.6, 0.6, 0)
		ss.touch(driver.TouchMove, 0.7, 0.7, 0)
		ss.touch(driver.TouchUp, 0.7, 0.7, 0)
	}
	if n := len(ss.queue); n > maxQueue+3 {
		t.Fatalf("queue of %d, want at most about %d", n, maxQueue)
	}
	if ss.down {
		t.Fatal("a dropped down left the finger down")
	}

	ss = &session{s: s, device: "p1", w: 100, h: 200, done: make(chan struct{})}
	ss.cond = sync.NewCond(&ss.mu)
	now := time.Now()
	ss.keys, ss.keysAt = keyBurst, now
	n := 0
	for range 20 {
		if ss.takeKeyLocked(now) {
			n++
		}
	}
	if n != keyBurst {
		t.Fatalf("%d keys at once, want %d", n, keyBurst)
	}
	if !ss.takeKeyLocked(now.Add(time.Second / keyRate)) {
		t.Fatal("no key after waiting for one")
	}
}
