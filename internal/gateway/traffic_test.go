package gateway

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/fake"
	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/traffic"
)

// addrProvider is the fake provider with the phone on a network: its
// address is 127.0.0.1, so the test's own HTTP client is the phone.
type addrProvider struct {
	*fake.Provider
	mu    sync.Mutex
	addrs []netip.Addr
}

func (p *addrProvider) PhoneAddrs(context.Context, string) ([]netip.Addr, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.addrs, nil
}

func (p *addrProvider) setAddrs(a ...netip.Addr) {
	p.mu.Lock()
	p.addrs = a
	p.mu.Unlock()
}

// routeProvider can also route the phone's proxy, keeping its setting.
type routeProvider struct {
	*addrProvider
	mu      sync.Mutex
	setting string
	routes  int
	away    bool // the phone cannot be reached
}

func (p *routeProvider) ProxySetting(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.setting, nil
}

func (p *routeProvider) RouteProxy(_ context.Context, _ string, port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setting = "127.0.0.1:" + strconv.Itoa(port)
	p.routes++
	return nil
}

func (p *routeProvider) UnrouteProxy(_ context.Context, _ string, port int, prev string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.away {
		return driver.ErrUnsupported
	}
	if p.setting == "127.0.0.1:"+strconv.Itoa(port) {
		p.setting = prev
	}
	return nil
}

func (p *routeProvider) now() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.setting
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

var loopback = netip.MustParseAddr("127.0.0.1")

func trafficOptions(t *testing.T, p driver.Provider, dir string) Options {
	return Options{
		Providers: []driver.Provider{p}, Policy: testPolicy(t), TrustNewAgents: true, DataDir: dir,
		ApprovalTimeout: 3 * time.Second, MaxBlock: 2 * time.Second, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true,
		TrafficPort: freePort(t), TrafficSetupPort: freePort(t),
		// The test's servers are on this computer, which the proxy's own
		// dial refuses.
		TrafficDial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		TrafficInterfaces: func() ([]netip.Prefix, error) {
			return []netip.Prefix{netip.MustParsePrefix("127.0.0.1/8"), netip.MustParsePrefix("192.168.1.5/24")}, nil
		},
	}
}

func newTrafficGateway(t *testing.T) (*Gateway, *addrProvider) {
	t.Helper()
	np := &addrProvider{Provider: fake.NewProvider(fake.NewDevice("pixel", "Pixel 9"))}
	g, err := New(trafficOptions(t, np, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g, np
}

// phoneClient is the phone, with its HTTP proxy set to Pinky's.
func phoneClient(proxy string) *http.Client {
	u, _ := url.Parse("http://" + proxy)
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
}

// The person turns reading on per phone; the proxy listens for it while
// its task runs; the first read asks, Allow for this task covers the rest;
// requests from while the person held the phone are left out; credentials
// are redacted; history keeps no request; the task's end drops them.
func TestPhoneTraffic(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"code":0,"token":"t-1","name":"Amy"}`)
	}))
	defer upstream.Close()
	g, np := newTrafficGateway(t)

	s, _ := connect(t, g, "Find the app's API")

	// Off by default, and nothing listens.
	res := s.Traffic(ctx, TrafficRequest{})
	mustStatus(t, res, StatusBlocked)
	if res.Rule != RuleTraffic || !strings.Contains(res.Message, "Settings › Phones") {
		t.Fatalf("off: %+v", res)
	}
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	if !g.Traffic("pixel") {
		t.Fatal("Traffic(pixel) is off after SetTraffic")
	}
	// On, but Pinky knows no address of the phone.
	waitUntil(t, "the capture to look", func() bool {
		st, _ := g.TrafficSetup("pixel")
		return st.Problem == TrafficNoAddress
	})
	if res := s.Traffic(ctx, TrafficRequest{}); res.Error != ErrUnsupported || !strings.Contains(res.Message, "no address") {
		t.Fatalf("no address: %+v", res)
	}

	// On the network: the proxy listens on this computer's address there,
	// and only for the phone's address.
	np.setAddrs(loopback)
	g.cap.mu.Lock()
	clear(g.cap.addrs)
	g.cap.mu.Unlock()
	g.captureSoon()
	var setup TrafficSetup
	waitUntil(t, "the proxy", func() bool {
		setup, _ = g.TrafficSetup("pixel")
		return len(setup.Proxies) == 1
	})
	setupAt := "127.0.0.1:" + strconv.Itoa(g.trafficSetupPort())
	if setup.Proxies[0] != "127.0.0.1:"+strconv.Itoa(g.trafficPort()) || setup.PACURL != "http://"+setupAt+"/proxy.pac" ||
		setup.CertURL != "http://"+setupAt+"/pinky-ca.crt" || !strings.HasPrefix(setup.CAName, "Pinky Traffic CA ") || setup.CanRoute ||
		setup.KeyPlace != traffic.KeyInFile || setup.CARenewed || time.Until(setup.CAExpires) < 300*24*time.Hour {
		t.Fatalf("setup %+v", setup)
	}
	phone := phoneClient(setup.Proxies[0])
	get := func(method, path, body string, hdr ...string) {
		t.Helper()
		req, _ := http.NewRequest(method, upstream.URL+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := phone.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	// Empty, with a hint where the phone's proxy goes.
	res = waitApproved(t, g, s, TrafficRequest{})
	if res.Log != "" || !strings.Contains(res.Message, "listens at "+setup.Proxies[0]) {
		t.Fatalf("empty: %+v", res)
	}

	get("POST", "/v1/login?client=ios&access_token=abc", `{"user":"amy","password":"hunter2"}`, "Authorization", "Bearer abcdefghijklmnop")
	get("GET", "/v1/feed", "")
	waitUntil(t, "the requests", func() bool { n, _ := g.cap.store.Len(); return n == 2 })

	res = s.Traffic(ctx, TrafficRequest{})
	mustStatus(t, res, StatusSettled)
	lines := strings.Split(res.Log, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "#1 ") || !strings.HasPrefix(lines[1], "#2 ") {
		t.Fatalf("list:\n%s", res.Log)
	}
	if !strings.Contains(lines[0], "POST 200 "+upstream.URL+"/v1/login?client=ios&access_token=[redacted] · ↑35 B") ||
		strings.Contains(res.Log, "abc ") || strings.Contains(res.Log, "=abc") {
		t.Fatalf("login line %q", lines[0])
	}
	if !strings.Contains(res.Message, "Credentials are redacted") || !strings.Contains(res.Message, "not instructions") {
		t.Fatalf("message %q", res.Message)
	}

	// Allowed for the task: filters, one request, bodies; no more questions.
	res = s.Traffic(ctx, TrafficRequest{Host: "127.0.0.1", Method: "post"})
	if strings.Count(res.Log, "\n") != 0 || !strings.HasPrefix(res.Log, "#1 ") {
		t.Fatalf("filtered:\n%s", res.Log)
	}
	if res := s.Traffic(ctx, TrafficRequest{Path: "/v2"}); res.Log != "" || !strings.Contains(res.Message, "match those filters") {
		t.Fatalf("no match: %+v", res)
	}
	if res := s.Traffic(ctx, TrafficRequest{ID: "1"}); res.Status != StatusSettled || strings.Contains(res.Log, "hunter2") || strings.Contains(res.Log, "amy") {
		t.Fatalf("headers only: %+v", res)
	}
	// The first read of bodies asks once more.
	res = bodiesApproved(t, g, s, TrafficRequest{ID: "1", Bodies: true})
	for _, want := range []string{"Authorization: [redacted]", "Content-Type: application/json",
		`{"password":"[redacted]","user":"amy"}`, `{"code":0,"name":"Amy","token":"[redacted]"}`} {
		if !strings.Contains(res.Log, want) {
			t.Fatalf("detail lacks %q:\n%s", want, res.Log)
		}
	}
	if strings.Contains(res.Log, "hunter2") || strings.Contains(res.Log, "abcdefghijklmnop") || strings.Contains(res.Log, "t-1") {
		t.Fatalf("detail leaks a secret:\n%s", res.Log)
	}
	if res := s.Traffic(ctx, TrafficRequest{ID: "9"}); res.Error != ErrNotFound {
		t.Fatalf("a request Pinky does not hold: %+v", res)
	}
	mustStatus(t, s.Traffic(ctx, TrafficRequest{ID: "2", Bodies: true}), StatusSettled)
	if len(g.Needs()) != 0 {
		t.Fatalf("asked again: %+v", g.Needs())
	}

	// While the person holds the phone, nothing it does is kept. On
	// Windows' clock the hand back and the next request start in the same
	// tick; that request is still the agent's.
	windowsClock(t)
	if err := g.TakeOver("pixel"); err != nil {
		t.Fatal(err)
	}
	get("POST", "/v1/password", `{"password":"new-secret"}`)
	if err := g.HandBack("pixel", ""); err != nil {
		t.Fatal(err)
	}
	get("GET", "/v1/me", "")
	waitUntil(t, "the request after", func() bool { n, _ := g.cap.store.Len(); return n == 3 })
	res = s.Traffic(ctx, TrafficRequest{Since: "1h"})
	for res.Status == StatusTookOver || res.Status == StatusPaused {
		res = s.Traffic(ctx, TrafficRequest{Since: "1h"})
	}
	mustStatus(t, res, StatusSettled)
	if strings.Contains(res.Log, "/v1/password") || !strings.Contains(res.Log, "/v1/me") {
		t.Fatalf("after a takeover:\n%s", res.Log)
	}
	if !strings.Contains(res.Message, "Left out: 1 request made while the person held the phone") {
		t.Fatalf("message %q", res.Message)
	}

	// History keeps what was read, never the requests.
	var texts []string
	for _, st := range taskSteps(g, s) {
		if st.Tool != "phone_traffic" {
			continue
		}
		texts = append(texts, st.Text)
		if strings.Contains(st.Text, "127.0.0.1") || strings.Contains(st.Text, "login") {
			t.Fatalf("history keeps a request: %+v", st)
		}
	}
	if texts[0] != "Blocked by “Network traffic”: read the network traffic of Pixel 9" ||
		!contains(texts, "Read 2 network requests of Pixel 9") || !contains(texts, "Read network request 1 of Pixel 9") ||
		!contains(texts, "Waiting for you: Read the request and response bodies in the network traffic of Pixel 9") {
		t.Fatalf("steps %q", texts)
	}

	// Turning it off takes back what the person allowed, and forgets the
	// requests.
	if err := g.SetTraffic("pixel", false); err != nil {
		t.Fatal(err)
	}
	if n, _ := g.cap.store.Len(); n != 0 {
		t.Fatalf("%d requests kept after the switch went off", n)
	}
	mustStatus(t, s.Traffic(ctx, TrafficRequest{}), StatusBlocked)
	waitUntil(t, "the proxy to stop", func() bool {
		st, _ := g.TrafficSetup("pixel")
		return len(st.Proxies) == 0 && st.PACURL == ""
	})
	for _, at := range []string{setup.Proxies[0], setupAt} {
		if c, err := net.DialTimeout("tcp", at, time.Second); err == nil {
			c.Close()
			t.Fatalf("%s still listens with the switch off", at)
		}
	}
}

// bodiesApproved reads bodies, allowing them for the task when asked.
func bodiesApproved(t *testing.T, g *Gateway, s *Session, req TrafficRequest) *Result {
	t.Helper()
	done := make(chan *Result, 1)
	go func() { done <- s.Traffic(context.Background(), req) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != RuleTraffic || n.Title != "Read the request and response bodies in the network traffic of Pixel 9?" || !n.AllowTask || n.AlwaysAllow {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusSettled)
	return res
}

// A first read that asks for bodies asks once, for both.
func TestTrafficFirstReadWithBodies(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer upstream.Close()
	g, np := newTrafficGateway(t)
	np.setAddrs(loopback)
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Look at requests")
	var setup TrafficSetup
	waitUntil(t, "the proxy", func() bool { setup, _ = g.TrafficSetup("pixel"); return len(setup.Proxies) == 1 })
	resp, err := phoneClient(setup.Proxies[0]).Get(upstream.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitUntil(t, "the request", func() bool { n, _ := g.cap.store.Len(); return n == 1 })
	done := make(chan *Result, 1)
	go func() { done <- s.Traffic(ctx, TrafficRequest{ID: "1", Bodies: true}) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Title != "Read the network traffic of Pixel 9 with request and response bodies?" {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	if res := <-done; res.Status != StatusSettled || !strings.Contains(res.Log, "Response body:\nok") {
		t.Fatalf("bodies: %+v", res)
	}
	mustStatus(t, s.Traffic(ctx, TrafficRequest{}), StatusSettled)
	if len(g.Needs()) != 0 {
		t.Fatalf("asked again: %+v", g.Needs())
	}
}

// Hosts the person blocks pass through undecrypted and are kept only as
// their host; the list is saved per phone.
func TestTrafficBlocklist(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "secret balance") }))
	defer upstream.Close()
	dir := t.TempDir()
	np := &addrProvider{Provider: fake.NewProvider(fake.NewDevice("pixel", "Pixel 9"))}
	g, err := New(trafficOptions(t, np, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	np.setAddrs(loopback)
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	got, err := g.SetTrafficBlocklist("pixel", []string{"https://127.0.0.1/login", "*.bank.example", "", "bank.example"})
	if err != nil || strings.Join(got, ",") != "127.0.0.1,bank.example" {
		t.Fatalf("SetTrafficBlocklist = %q, %v", got, err)
	}
	s, _ := connect(t, g, "Look at requests")
	var setup TrafficSetup
	waitUntil(t, "the proxy", func() bool { setup, _ = g.TrafficSetup("pixel"); return len(setup.Proxies) == 1 })
	if strings.Join(setup.Blocklist, ",") != "127.0.0.1,bank.example" {
		t.Fatalf("setup blocklist %q", setup.Blocklist)
	}
	// An app that keeps calling the blocked host leaves one line.
	for range 3 {
		resp, err := phoneClient(setup.Proxies[0]).Get(upstream.URL + "/balance?account=1")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	waitUntil(t, "the requests", func() bool {
		rs := g.cap.store.Requests(traffic.Query{Device: "pixel"})
		return len(rs) == 1 && rs[0].Connections() == 3
	})
	res := waitApproved(t, g, s, TrafficRequest{})
	if !strings.Contains(res.Log, " · not decrypted (blocked) · 3 connections, the last at ") || strings.Contains(res.Log, "balance") ||
		strings.Contains(res.Log, "↑") || strings.Count(res.Log, "\n") != 0 || !strings.Contains(res.Message, "Blocked: 3 connections") {
		t.Fatalf("list: %+v", res)
	}
	res = s.Traffic(ctx, TrafficRequest{ID: "1"})
	if res.Status != StatusSettled || !strings.Contains(res.Message, "only its host") || !strings.Contains(res.Message, "3 connections") ||
		strings.Contains(res.Log, "headers") {
		t.Fatalf("detail: %+v", res)
	}
	g.Close()

	g2, err := New(trafficOptions(t, np, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer g2.Close()
	waitUntil(t, "the phone", func() bool { _, err := g2.TrafficSetup("pixel"); return err == nil })
	if st, _ := g2.TrafficSetup("pixel"); strings.Join(st.Blocklist, ",") != "127.0.0.1,bank.example" {
		t.Fatalf("saved blocklist %q", st.Blocklist)
	}
	if _, err := g2.SetTrafficBlocklist("pixel", nil); err != nil {
		t.Fatal(err)
	}
	if st, _ := g2.TrafficSetup("pixel"); len(st.Blocklist) != 0 {
		t.Fatalf("cleared blocklist %q", st.Blocklist)
	}
}

// waitApproved reads the traffic, allowing it for the task when asked
// (after a refusal first: there is no Always allow).
func waitApproved(t *testing.T, g *Gateway, s *Session, req TrafficRequest) *Result {
	t.Helper()
	done := make(chan *Result, 1)
	go func() { done <- s.Traffic(context.Background(), req) }()
	n := waitNeedKind(t, g, NeedApproval)
	if n.Rule != RuleTraffic || n.Title != "Read the network traffic of Pixel 9?" || !n.AllowTask || n.AlwaysAllow {
		t.Fatalf("approval %+v", n)
	}
	if err := g.Decide(n.ID, Deny, ""); err != nil {
		t.Fatal(err)
	}
	mustStatus(t, <-done, StatusDenied)
	go func() { done <- s.Traffic(context.Background(), req) }()
	n = waitNeedKind(t, g, NeedApproval)
	if err := g.Decide(n.ID, AllowTask, ""); err != nil {
		t.Fatal(err)
	}
	res := <-done
	mustStatus(t, res, StatusSettled)
	return res
}

// The task's end drops its requests and stops the proxy; the setup
// listener stays for the PAC file and the certificate, and proxies
// nothing.
func TestTrafficDroppedAtTaskEnd(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer upstream.Close()
	g, np := newTrafficGateway(t)
	np.setAddrs(loopback)
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Look at requests")
	var setup TrafficSetup
	waitUntil(t, "the proxy", func() bool { setup, _ = g.TrafficSetup("pixel"); return len(setup.Proxies) == 1 })
	resp, err := phoneClient(setup.Proxies[0]).Get(upstream.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitUntil(t, "the request", func() bool { n, _ := g.cap.store.Len(); return n == 1 })
	mustStatus(t, s.Release(ctx, ReleaseRequest{Outcome: "done"}), StatusSettled)
	if n, _ := g.cap.store.Len(); n != 0 {
		t.Fatalf("%d requests kept after the task", n)
	}
	proxy := setup.Proxies[0]
	waitUntil(t, "the proxy to stop", func() bool { setup, _ = g.TrafficSetup("pixel"); return len(setup.Proxies) == 0 })
	// The proxy's port is closed, so a phone set to the PAC file connects
	// directly.
	if c, err := net.DialTimeout("tcp", proxy, time.Second); err == nil {
		c.Close()
		t.Fatal("the proxy listens outside a task")
	}
	if setup.PACURL == "" {
		t.Fatalf("no PAC file outside a task: %+v", setup)
	}
	resp, err = http.Get(setup.PACURL)
	if err != nil {
		t.Fatal(err)
	}
	pac, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(pac), `"PROXY `+proxy+`; DIRECT"`) {
		t.Fatalf("PAC file:\n%s", pac)
	}
	resp, err = http.Get(setup.CertURL)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(der) == 0 || resp.Header.Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("certificate %q, %d bytes", resp.Header.Get("Content-Type"), len(der))
	}
	u, _ := url.Parse(setup.PACURL)
	resp, err = phoneClient(u.Host).Get(upstream.URL + "/b")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the setup listener proxied: %d", resp.StatusCode)
	}
	if n, _ := g.cap.store.Len(); n != 0 {
		t.Fatalf("%d requests kept outside a task", n)
	}
}

func newRouteGateway(t *testing.T, dir string) (*Gateway, *routeProvider) {
	t.Helper()
	rp := &routeProvider{addrProvider: &addrProvider{Provider: fake.NewProvider(fake.NewDevice("pixel", "Pixel 9"))}, setting: "10.0.0.8:3128"}
	g, err := New(trafficOptions(t, rp, dir))
	if err != nil {
		t.Fatal(err)
	}
	return g, rp
}

// The person's route switch points the Android phone's proxy at Pinky for
// each task, and the setting goes back when the task ends and when Pinky
// stops.
func TestTrafficRoute(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	g, rp := newRouteGateway(t, dir)
	defer g.Close()
	if err := g.SetTrafficRoute("pixel", true); err != ErrTrafficOff {
		t.Fatalf("route with reading off: %v", err)
	}
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	if err := g.SetTrafficRoute("pixel", true); err != nil {
		t.Fatal(err)
	}
	if rp.now() != "10.0.0.8:3128" {
		t.Fatalf("the phone changed outside a task: %q", rp.now())
	}
	s, _ := connect(t, g, "Look at requests")
	port := g.trafficPort()
	waitUntil(t, "the route", func() bool { return rp.now() == "127.0.0.1:"+strconv.Itoa(port) })
	if st, _ := g.TrafficSetup("pixel"); !st.CanRoute || !st.Route || !st.Routed {
		t.Fatalf("setup %+v", st)
	}
	if rs, _ := g.store.TrafficRoutes(ctx); len(rs) != 1 || rs[0].Prev != "10.0.0.8:3128" {
		t.Fatalf("saved routes %+v", rs)
	}
	// Over the cable the phone's requests come from 127.0.0.1.
	if id, ok := g.trafficClient(loopback, false); !ok || id != "pixel" {
		t.Fatalf("loopback is %q %v", id, ok)
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{Outcome: "done"}), StatusSettled)
	waitUntil(t, "the setting back", func() bool { return rp.now() == "10.0.0.8:3128" })
	waitUntil(t, "the saved route gone", func() bool { rs, _ := g.store.TrafficRoutes(ctx); return len(rs) == 0 })
	if _, ok := g.trafficClient(loopback, false); ok {
		t.Fatal("loopback still taken after the task")
	}

	// Pinky stopping in a task puts it back too.
	connect(t, g, "Again")
	waitUntil(t, "the route", func() bool { return rp.now() == "127.0.0.1:"+strconv.Itoa(port) })
	g.Close()
	if rp.now() != "10.0.0.8:3128" {
		t.Fatalf("after Close: %q", rp.now())
	}
}

// A Pinky that crashed with a phone routed puts its setting back at the
// next start, once it reaches the phone.
func TestTrafficRouteRestoredAfterCrash(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := history.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTrafficRoute(ctx, history.TrafficRoute{DeviceID: "pixel", Prev: "", Port: 9277}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	rp := &routeProvider{addrProvider: &addrProvider{Provider: fake.NewProvider(fake.NewDevice("pixel", "Pixel 9"))},
		setting: "127.0.0.1:9277", away: true}
	g, err := New(trafficOptions(t, rp, dir))
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	time.Sleep(100 * time.Millisecond)
	if rp.now() != "127.0.0.1:9277" {
		t.Fatal("changed a phone it cannot reach")
	}
	rp.mu.Lock()
	rp.away = false
	rp.mu.Unlock()
	g.captureSoon()
	waitUntil(t, "the setting back", func() bool { return rp.now() == "" })
	waitUntil(t, "the saved route gone", func() bool { rs, _ := g.store.TrafficRoutes(ctx); return len(rs) == 0 })
}

// The switch is saved per phone.
func TestTrafficSaved(t *testing.T) {
	dir := t.TempDir()
	g, err := New(Options{Policy: testPolicy(t), DataDir: dir, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.devices["pixel"] = &device{id: "pixel"}
	g.mu.Unlock()
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	g.Close()
	g, err = New(Options{Policy: testPolicy(t), DataDir: dir, NoScreenshots: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if !g.Traffic("pixel") {
		t.Fatal("the switch was not kept")
	}
}

func TestListenAddr(t *testing.T) {
	locals := []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/8"),
		netip.MustParsePrefix("192.168.1.5/24"),
		netip.MustParsePrefix("100.90.1.2/32"),
		netip.MustParsePrefix("fe80::1/64"),
	}
	for _, c := range []struct{ phone, want string }{
		{"192.168.1.42", "192.168.1.5"},
		{"100.101.2.2", "100.90.1.2"},
		{"10.0.0.7", ""},
		{"fe80::2", ""},
		{"fd7a:115c:a1e0::2", ""},
		// A carrier's NAT uses the range too: not a tailnet peer.
		{"100.70.0.9", ""},
	} {
		peers := map[netip.Addr]bool{netip.MustParseAddr("100.101.2.2"): true}
		got, ok := listenAddr(netip.MustParseAddr(c.phone), locals, peers)
		if (c.want == "" && ok) || (c.want != "" && got.String() != c.want) {
			t.Errorf("listenAddr(%s) = %v %v, want %q", c.phone, got, ok, c.want)
		}
	}
}

func TestStepAt(t *testing.T) {
	t0 := time.Now()
	steps := []StepView{
		{Index: 1, Tool: "phone_launch", Text: "Opened Example", At: t0},
		{Index: 2, Tool: "phone_traffic", Text: "Read 3 network requests of Pixel", At: t0.Add(time.Second)},
		{Index: 3, Tool: "phone_tap", Text: "Tapped “Log in”", At: t0.Add(2 * time.Second)},
	}
	for _, c := range []struct {
		at   time.Duration
		want int
	}{{-time.Second, 0}, {500 * time.Millisecond, 1}, {1500 * time.Millisecond, 1}, {3 * time.Second, 3}} {
		if got, _ := stepAt(steps, t0.Add(c.at)); got != c.want {
			t.Errorf("stepAt(+%v) = %d, want %d", c.at, got, c.want)
		}
	}
}

// Pinky's own setting is never the one put back: a phone still pointing
// at Pinky (an earlier route that was never undone) gets the setting
// noted then, or none.
func TestTrafficRouteNeverRestoresPinky(t *testing.T) {
	ctx := context.Background()
	for _, noted := range []string{"", "10.0.0.8:3128"} {
		dir := t.TempDir()
		rp := &routeProvider{addrProvider: &addrProvider{Provider: fake.NewProvider(fake.NewDevice("pixel", "Pixel 9"))}}
		opt := trafficOptions(t, rp, dir)
		pinky := "127.0.0.1:" + strconv.Itoa(opt.TrafficPort)
		rp.setting = pinky
		if noted != "" {
			// An earlier route whose undoing failed: the phone was away.
			st, err := history.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.SaveTrafficRoute(ctx, history.TrafficRoute{DeviceID: "pixel", Prev: noted, Port: opt.TrafficPort}); err != nil {
				t.Fatal(err)
			}
			st.Close()
			rp.away = true
		}
		g, err := New(opt)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.SetTraffic("pixel", true); err != nil {
			t.Fatal(err)
		}
		if err := g.SetTrafficRoute("pixel", true); err != nil {
			t.Fatal(err)
		}
		s, _ := connect(t, g, "Look at requests")
		waitUntil(t, "the route", func() bool { st, _ := g.TrafficSetup("pixel"); return st.Routed })
		if rs, _ := g.store.TrafficRoutes(ctx); len(rs) != 1 || rs[0].Prev != noted {
			t.Fatalf("noted %q: saved routes %+v", noted, rs)
		}
		rp.mu.Lock()
		rp.away = false
		rp.mu.Unlock()
		mustStatus(t, s.Release(ctx, ReleaseRequest{Outcome: "done"}), StatusSettled)
		waitUntil(t, "the setting back", func() bool { return rp.now() == noted })
		g.Close()
	}
}

// Outside its task a phone gets only the setup files: the proxy takes
// only a phone whose task runs.
func TestTrafficProxyOnlyDuringTheTask(t *testing.T) {
	ctx := context.Background()
	g, np := newTrafficGateway(t)
	np.setAddrs(loopback)
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the setup listener", func() bool { st, _ := g.TrafficSetup("pixel"); return st.PACURL != "" })
	if _, ok := g.trafficClient(loopback, true); !ok {
		t.Fatal("the setup files are not served to the phone")
	}
	if _, ok := g.trafficClient(loopback, false); ok {
		t.Fatal("the proxy takes a phone outside its task")
	}
	s, _ := connect(t, g, "Look at requests")
	waitUntil(t, "the proxy", func() bool { _, ok := g.trafficClient(loopback, false); return ok })
	mustStatus(t, s.Release(ctx, ReleaseRequest{Outcome: "done"}), StatusSettled)
	waitUntil(t, "the proxy to stop taking it", func() bool { _, ok := g.trafficClient(loopback, false); return !ok })
}

// The path filter matches what the agent sees: redacted values cannot be
// guessed a letter at a time.
func TestTrafficPathFilterIsRedacted(t *testing.T) {
	now := time.Now()
	reqs := []traffic.Request{{ID: "1", Device: "pixel", Start: now, End: now, Method: "GET", Host: "api.example", Decrypted: true,
		URL: "https://api.example/v1/me?access_token=abc123&page=2"}}
	for path, want := range map[string]bool{
		"access_token=a":          false,
		"abc":                     false,
		"access_token=[redacted]": true,
		"page=2":                  true,
		"/v1/me":                  true,
	} {
		res := trafficList(reqs, TrafficRequest{Path: path}, 50, nil, now.Add(-time.Minute), now, 0)
		if got := strings.Contains(res.Log, "api.example"); got != want {
			t.Errorf("path %q matched %v: %+v", path, got, res)
		}
	}
}

// Another program holding the proxy's port on this computer's loopback
// (Warp does) does not stop the cable route: it takes another free port,
// sets the phone's proxy to that one, and puts the setting back from it.
func TestTrafficRoutePortTaken(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer upstream.Close()
	g, rp := newRouteGateway(t, t.TempDir())
	defer g.Close()
	port := g.trafficPort()
	other, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	if err := g.SetTrafficRoute("pixel", true); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Look at requests")
	var st TrafficSetup
	waitUntil(t, "the route", func() bool { st, _ = g.TrafficSetup("pixel"); return st.Routed })
	if st.RoutePort == 0 || st.RoutePort == port || st.Problem != "" || rp.now() != "127.0.0.1:"+strconv.Itoa(st.RoutePort) {
		t.Fatalf("setup %+v, phone %q", st, rp.now())
	}
	if rs, _ := g.store.TrafficRoutes(ctx); len(rs) != 1 || rs[0].Port != st.RoutePort {
		t.Fatalf("saved routes %+v", rs)
	}
	// The phone's requests arrive at that port and are kept.
	resp, err := phoneClient(net.JoinHostPort("127.0.0.1", strconv.Itoa(st.RoutePort))).Get(upstream.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitUntil(t, "the request", func() bool { n, _ := g.cap.store.Len(); return n == 1 })
	// It stays on that port for the task.
	g.captureSoon()
	time.Sleep(50 * time.Millisecond)
	rp.mu.Lock()
	routes := rp.routes
	rp.mu.Unlock()
	if again, _ := g.TrafficSetup("pixel"); again.RoutePort != st.RoutePort || routes != 1 {
		t.Fatalf("routed again: %+v, %d routes", again, routes)
	}
	mustStatus(t, s.Release(ctx, ReleaseRequest{Outcome: "done"}), StatusSettled)
	waitUntil(t, "the setting back", func() bool { return rp.now() == "10.0.0.8:3128" })
	waitUntil(t, "the listener gone", func() bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(st.RoutePort)), time.Second)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
}

// A port another program holds at the address the phone reaches is named,
// in the setup and in phone_traffic's answer, as the Wi-Fi proxy's.
func TestTrafficListenFailedNamesThePort(t *testing.T) {
	ctx := context.Background()
	g, np := newTrafficGateway(t)
	at := net.JoinHostPort("127.0.0.1", strconv.Itoa(g.trafficSetupPort()))
	other, err := net.Listen("tcp", at)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	np.setAddrs(loopback)
	if err := g.SetTraffic("pixel", true); err != nil {
		t.Fatal(err)
	}
	var st TrafficSetup
	waitUntil(t, "the problem", func() bool { st, _ = g.TrafficSetup("pixel"); return st.Problem == TrafficListenFailed })
	if st.ProblemAddr != at || st.PACURL != "" {
		t.Fatalf("setup %+v", st)
	}
	s, _ := connect(t, g, "Look at requests")
	res := s.Traffic(ctx, TrafficRequest{})
	if res.Error != ErrUnsupported || !strings.Contains(res.Message, "uses "+at) || !strings.Contains(res.Message, "Wi-Fi proxy") ||
		!strings.Contains(res.Message, "cable route") {
		t.Fatalf("result %+v", res)
	}
	// Once the program lets go, Pinky listens there by itself.
	other.Close()
	g.captureSoon()
	waitUntil(t, "the setup listener", func() bool { st, _ = g.TrafficSetup("pixel"); return st.PACURL != "" })
	if st.Problem != "" || st.ProblemAddr != "" {
		t.Fatalf("setup after %+v", st)
	}
}
