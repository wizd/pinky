package webmirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

type fakeStream struct {
	ch   chan driver.Packet
	keys chan struct{}
}

func (f *fakeStream) Codec() driver.Codec           { return driver.H264 }
func (f *fakeStream) Size() (int, int)              { return 640, 360 }
func (f *fakeStream) Packets() <-chan driver.Packet { return f.ch }
func (f *fakeStream) Err() error                    { return nil }
func (f *fakeStream) Close() error                  { return nil }
func (f *fakeStream) RequestKeyFrame()              { f.keys <- struct{}{} }

type fakeSource struct {
	st *fakeStream
	in *inputLog // nil: input is dropped
}

// inputLog records the input the server passes to the gateway.
type inputLog struct {
	mu    sync.Mutex
	calls []string
	got   chan struct{}
}

func newInputLog() *inputLog { return &inputLog{got: make(chan struct{}, 1024)} }

func (l *inputLog) add(c string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.calls = append(l.calls, c)
	l.mu.Unlock()
	l.got <- struct{}{}
}

// wait returns the calls once there are n, or what there is after 5 s.
func (l *inputLog) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		l.mu.Lock()
		c := append([]string(nil), l.calls...)
		l.mu.Unlock()
		if len(c) >= n {
			return c
		}
		select {
		case <-l.got:
		case <-deadline:
			return c
		}
	}
}

func (f fakeSource) Touch(id string, a driver.TouchAction, p screen.Point) error {
	f.in.add(fmt.Sprintf("%s %s %d,%d", id, a, p.X, p.Y))
	return nil
}

func (f fakeSource) Key(id string, k driver.Key) error {
	f.in.add(fmt.Sprintf("%s key %s", id, k))
	return nil
}

func (f fakeSource) Devices() []gateway.DeviceView {
	return []gateway.DeviceView{
		{Info: driver.Info{ID: "off", Name: "Old phone", State: driver.StateOffline}},
		{Info: driver.Info{ID: "p1", Name: "iPhone", Platform: driver.IOS, State: driver.StateOnline}},
	}
}

func (f fakeSource) Stream(ctx context.Context, id string, _ driver.StreamOptions) (driver.Stream, error) {
	return f.st, nil
}

func newTestServer(t *testing.T, src Source) (*Server, *httptest.Server) {
	t.Helper()
	s := New(Options{Source: src,
		OwnNames: func(context.Context) []string { return []string{"mac.tail1234.ts.net"} },
		Owner:    func(context.Context) string { return "owner@example.com" }})
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	return s, hs
}

func dial(t *testing.T, hs *httptest.Server, origin string) *websocket.Conn {
	t.Helper()
	ws, err := websocket.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+WebSocketPath, "", origin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	ws.SetDeadline(time.Now().Add(5 * time.Second))
	return ws
}

func receive(t *testing.T, ws *websocket.Conn) serverMsg {
	t.Helper()
	var s string
	if err := websocket.Message.Receive(ws, &s); err != nil {
		t.Fatal(err)
	}
	var m serverMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return m
}

func TestViewerNeedsToken(t *testing.T) {
	_, hs := newTestServer(t, fakeSource{st: &fakeStream{ch: make(chan driver.Packet)}})
	ws := dial(t, hs, hs.URL)
	websocket.JSON.Send(ws, hello{Token: "wrong"})
	if m := receive(t, ws); m.Type != "error" || m.Error != "token" {
		t.Fatalf("got %+v, want the token error", m)
	}
}

func TestViewerRefusesOtherOrigins(t *testing.T) {
	_, hs := newTestServer(t, fakeSource{st: &fakeStream{ch: make(chan driver.Packet)}})
	if _, err := websocket.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+WebSocketPath, "", "https://evil.example"); err == nil {
		t.Fatal("a page of another origin opened the WebSocket")
	}
}

func TestViewerGetsPictures(t *testing.T) {
	st := &fakeStream{ch: make(chan driver.Packet, 8), keys: make(chan struct{}, 8)}
	s, hs := newTestServer(t, fakeSource{st: st})
	ws := dial(t, hs, hs.URL)
	websocket.JSON.Send(ws, hello{Token: s.Token()})

	m := receive(t, ws)
	if m.Type != "devices" || m.Device != "p1" || len(m.Devices) != 1 || m.Devices[0].Name != "iPhone" {
		t.Fatalf("got %+v, want the one phone online", m)
	}
	sps, pps := unhex(t, h264SPSHex), unhex(t, h264PPSHex)
	now := time.Duration(time.Now().UnixNano())
	st.ch <- driver.Packet{Data: annexB([]byte{0x41, 0x01}), PTS: now} // before a key frame: dropped
	st.ch <- driver.Packet{Data: annexB(sps, pps), Config: true, PTS: now}
	st.ch <- driver.Packet{Data: annexB([]byte{0x41, 0x02}), PTS: now} // still no key frame
	st.ch <- driver.Packet{Data: annexB([]byte{0x65, 0x03}), KeyFrame: true, PTS: now, Turn: 1}
	st.ch <- driver.Packet{Data: annexB([]byte{0x41, 0x04}), PTS: now + time.Millisecond}

	if m := receive(t, ws); m.Type != "config" || m.Codec != "avc1.64001E" || m.Description == "" {
		t.Fatalf("got %+v, want the configuration", m)
	}
	for _, want := range []struct {
		flags byte
		nal   byte
	}{{1 | 1<<1, 0x65}, {0, 0x41}} {
		var b []byte
		if err := websocket.Message.Receive(ws, &b); err != nil {
			t.Fatal(err)
		}
		if b[0] != want.flags || len(b) != frameHeader+4+2 || b[frameHeader+4] != want.nal {
			t.Fatalf("picture %x, want flags %b and NAL %x", b, want.flags, want.nal)
		}
	}
	if w := s.Watchers(); len(w) != 1 || w[0] != (Watcher{Device: "p1"}) {
		t.Errorf("Watchers() = %+v, want one watching p1", w)
	}

	// The page asks for a key frame after a decoding error: the phone is
	// asked, and pictures wait for it.
	websocket.JSON.Send(ws, clientMsg{Type: "keyframe"})
	select {
	case <-st.keys:
	case <-time.After(5 * time.Second):
		t.Fatal("the phone was not asked for a key frame")
	}
	st.ch <- driver.Packet{Data: annexB([]byte{0x41, 0x05}), PTS: now + 2*time.Millisecond}
	st.ch <- driver.Packet{Data: annexB([]byte{0x65, 0x06}), KeyFrame: true, PTS: now + 3*time.Millisecond}
	var b []byte
	if err := websocket.Message.Receive(ws, &b); err != nil {
		t.Fatal(err)
	}
	if b[frameHeader+4] != 0x65 {
		t.Fatalf("after asking for a key frame got NAL %x first", b[frameHeader+4])
	}

	// A new token cuts the viewer off.
	old := s.Token()
	s.NewToken()
	if s.Token() == old {
		t.Error("NewToken kept the token")
	}
	if err := websocket.Message.Receive(ws, &b); err == nil {
		t.Fatal("the viewer stayed connected after a new token")
	}
}

func TestOwnHost(t *testing.T) {
	s := New(Options{OwnNames: func(context.Context) []string { return []string{"mac.tail1234.ts.net."} }})
	for host, want := range map[string]bool{
		"127.0.0.1:7466":           true,
		"localhost:7466":           true,
		"[::1]:7466":               true,
		"mac.tail1234.ts.net:8443": true,
		"MAC.tail1234.ts.net":      true,
		"evil.example:7466":        false,
		"mac.tail1234.ts.net.evil": false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		if got := s.ownHost(r); got != want {
			t.Errorf("ownHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestLagMeter(t *testing.T) {
	var l lagMeter
	t0 := time.Now()
	// The pictures since the last key frame, sent at once to a late viewer:
	// old, but not behind.
	for i, pts := range []time.Duration{0, 10 * time.Millisecond, 20 * time.Millisecond} {
		if d := l.behind(pts, t0.Add(time.Duration(i)*time.Microsecond)); d != 0 {
			t.Errorf("replayed picture %d behind by %v", i, d)
		}
	}
	// Live pictures, on time: no further behind than the replay's spread.
	if d := l.behind(5*time.Second, t0.Add(5*time.Second)); d > 50*time.Millisecond {
		t.Errorf("on-time picture behind by %v", d)
	}
	// A picture that waited 2 s in a queue.
	if d := l.behind(6*time.Second, t0.Add(8*time.Second)); d < 2*time.Second || d > 2050*time.Millisecond {
		t.Errorf("queued picture behind by %v, want about 2s", d)
	}
}

// Tokens live in memory: each start makes new ones, and the control token
// exists only while control is allowed.
func TestTokens(t *testing.T) {
	a, b := New(Options{}), New(Options{})
	if a.Token() == "" || a.Token() == b.Token() {
		t.Fatalf("tokens %q and %q, want two new ones", a.Token(), b.Token())
	}
	if a.ControlToken() != "" {
		t.Fatal("a control token before control was allowed")
	}
	a.SetControl(true)
	ctl := a.ControlToken()
	if ctl == "" || ctl == a.Token() {
		t.Fatalf("control token %q", ctl)
	}
	a.SetControl(true)
	if a.ControlToken() != ctl {
		t.Fatal("allowing control again changed its token")
	}
	a.SetControl(false)
	if a.ControlToken() != "" {
		t.Fatal("the control token outlived control")
	}
	if r, ok := a.roleOf(ctl); ok {
		t.Fatalf("the dropped control token still works, as %v", r)
	}
	a.SetControl(true)
	if a.ControlToken() == ctl {
		t.Fatal("allowing control again brought the old token back")
	}
}

func TestFunnelRefused(t *testing.T) {
	_, hs := newTestServer(t, fakeSource{st: &fakeStream{ch: make(chan driver.Packet)}})
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/", nil)
	req.Header.Set("Tailscale-Funnel-Request", "?1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a Funnel request got %s", res.Status)
	}
}

// WebSockets that never say hello still count: past maxConns the server
// turns new ones away.
func TestConnLimit(t *testing.T) {
	s, hs := newTestServer(t, fakeSource{st: &fakeStream{ch: make(chan driver.Packet)}})
	for range maxConns {
		dial(t, hs, hs.URL)
	}
	if _, err := websocket.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+WebSocketPath, "", hs.URL); err == nil {
		t.Fatal("a WebSocket past the limit opened")
	}
	s.mu.Lock()
	open := s.open
	s.mu.Unlock()
	if open != maxConns {
		t.Fatalf("open = %d, want %d", open, maxConns)
	}
}

func TestProxied(t *testing.T) {
	for _, tc := range []struct {
		host   string
		header string
		want   bool
	}{
		{"127.0.0.1:7466", "", false},
		{"localhost:7466", "", false},
		{"mac.tail1234.ts.net:8443", "", true},
		{"localhost:7466", "X-Forwarded-For", true},
		{"localhost", "X-Forwarded-Host", true},
		{"127.0.0.1", "Tailscale-User-Login", true},
		{"127.0.0.1", "Tailscale-Funnel-Request", true},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = tc.host
		if tc.header != "" {
			r.Header.Set(tc.header, "x")
		}
		if got := proxied(r); got != tc.want {
			t.Errorf("proxied(%s, %s) = %v, want %v", tc.host, tc.header, got, tc.want)
		}
	}
}
