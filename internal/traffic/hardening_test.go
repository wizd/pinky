package traffic

import (
	"bufio"
	"context"
	"crypto/x509"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The proxy's own dial reaches nothing on this computer, whatever the
// request names it by, nor link-local or multicast addresses.
func TestProxyRefusesThisComputer(t *testing.T) {
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "a service on this computer")
	}))
	defer local.Close()
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{CA: ca, Store: NewStore(0, 0), Client: func(netip.Addr, bool) (string, bool) { return "pixel", true }}
	addr, err := p.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	port := strings.TrimPrefix(local.URL, "http://127.0.0.1:")
	u, _ := url.Parse("http://" + addr)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
	for _, host := range []string{"127.0.0.1", "localhost", "localhost.", "127.1", "0.0.0.0", "[::1]"} {
		resp, err := c.Get("http://" + host + ":" + port + "/")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(body), "a service on this computer") {
			t.Errorf("%s: %d %q", host, resp.StatusCode, body)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the local service was reached %d times", n)
	}
	for _, a := range []string{"127.0.0.1", "::1", "0.0.0.0", "0.1.2.3", "169.254.169.254", "fe80::1", "224.0.0.251",
		"ff02::1", "255.255.255.255"} {
		if !p.refusedAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s is not refused", a)
		}
	}
	for _, a := range []string{"93.184.216.34", "192.168.1.20", "2606:4700::1111"} {
		if p.refusedAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s is refused", a)
		}
	}
	// This computer's own addresses on its networks too.
	ifs, _ := net.InterfaceAddrs()
	for _, a := range ifs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			ip, _ := netip.AddrFromSlice(n.IP)
			if !p.refusedAddr(ip.Unmap()) {
				t.Errorf("this computer's %s is not refused", ip)
			}
		}
	}
}

// sniProxy is a proxy whose blocked host is example.com (the name in
// httptest's certificate), with every connection sent to upstream.
func sniProxy(t *testing.T, upstream *httptest.Server) *testProxy {
	tp := newTestProxy(t, upstream)
	tp.Blocked = func(_, host string) bool { return Blocked(host, []string{"example.com"}) }
	return tp
}

// An app that connects to an address, or to another name, and names a
// blocked host only in the TLS handshake is not decrypted: the handshake
// goes on to the server untouched, and only the host is kept.
func TestProxyBlocksByServerName(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "balance 100")
	}))
	defer upstream.Close()
	tp := sniProxy(t, upstream)
	real := x509.NewCertPool()
	real.AddCert(upstream.Certificate())
	// The app trusts only the server's own certificate: it works only
	// when the proxy does not decrypt.
	for _, target := range []string{"https://192.0.2.10/", "https://harmless.example/"} {
		resp, err := tp.phone(real, "example.com").Get(target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != "balance 100" {
			t.Fatalf("%s: body %q", target, body)
		}
	}
	waitFor(t, "the requests", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 2 })
	for _, r := range tp.store.Requests(Query{Device: "pixel"}) {
		if !r.Blocked || r.Decrypted || r.Host != "example.com" || r.URL != "https://example.com" || len(r.Headers) != 0 {
			t.Fatalf("request %+v", r)
		}
	}
}

// A handshake that names another host than the CONNECT's name is refused.
func TestProxyRefusesAnotherServerName(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	tp := newTestProxy(t, upstream)
	if _, err := tp.phone(tp.ca.Pool(), "other.example").Get("https://harmless.example/"); err == nil {
		t.Fatal("decrypted a handshake for another host")
	}
	waitFor(t, "the refusal", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if !r.Failed || r.Decrypted || !strings.Contains(r.Note, "another host") {
		t.Fatalf("request %+v", r)
	}
	if len(tp.Pinned()) != 0 {
		t.Fatal("a refused name pinned the host")
	}
}

// An app that names a blocked host only in the Host of a request inside
// gets that request through, kept as its host only, and the address's
// next connections pass through untouched.
func TestProxyBlocksByHostInside(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "balance 100")
	}))
	defer upstream.Close()
	tp := sniProxy(t, upstream)
	req, _ := http.NewRequest(http.MethodGet, "https://192.0.2.10/account?id=7", nil)
	req.Host = "example.com"
	resp, err := tp.phone(tp.ca.Pool(), "").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, "the request", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if !r.Blocked || r.Host != "example.com" || strings.Contains(r.URL, "account") || len(r.Headers) != 0 {
		t.Fatalf("request %+v", r)
	}
	if b, _ := tp.store.Bodies("pixel", r.ID, BodyMax); len(b.Response) != 0 {
		t.Fatalf("kept a body: %q", b.Response)
	}
	// The next connection to the address is not decrypted.
	_, _ = tp.phone(tp.ca.Pool(), "").Get("https://192.0.2.10/")
	waitFor(t, "the tunnel", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 2 })
	r = tp.store.Requests(Query{Device: "pixel"})[1]
	if !r.Blocked || r.Host != "example.com" || r.Method != http.MethodConnect {
		t.Fatalf("next connection %+v", r)
	}
}

// A request that ends after its phone's requests were dropped (the task
// ended meanwhile) is not kept.
func TestProxyKeepsNothingAfterTheDrop(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		io.WriteString(w, "late")
	}))
	defer upstream.Close()
	tp := newTestProxy(t, nil)
	done := make(chan error, 1)
	go func() {
		resp, err := tp.phone(nil, "").Get(upstream.URL + "/slow")
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	<-started
	tp.store.Drop("pixel")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n, _ := tp.store.Len(); n != 0 {
		t.Fatalf("%d requests kept after the drop", n)
	}
	gen := tp.store.Gen("pixel")
	if _, ok := tp.store.AddSince(gen, Request{Device: "pixel"}, Bodies{}); !ok {
		t.Fatal("a request of this generation was not kept")
	}
}

// Headers past headerMax are refused.
func TestProxyRefusesHugeHeaders(t *testing.T) {
	tp := newTestProxy(t, nil)
	c, err := net.Dial("tcp", tp.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		fmt.Fprintf(c, "GET http://example.org/ HTTP/1.1\r\nHost: example.org\r\n")
		line := "X-Big: " + strings.Repeat("a", 8000) + "\r\n"
		for i := 0; i < headerMax/len(line)+10; i++ {
			if _, err := io.WriteString(c, line); err != nil {
				return
			}
		}
	}()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A body that stops coming closes the connection after idleTimeout.
func TestProxyClosesAStalledBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
	}))
	defer upstream.Close()
	tp := newTestProxyWith(t, nil, limits{idle: 200 * time.Millisecond})
	c, err := net.Dial("tcp", tp.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "POST %s/up HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\n\r\n0123456789", upstream.URL, upstream.Listener.Addr())
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, _ = io.ReadAll(c)
	if time.Since(start) > 3*time.Second {
		t.Fatal("a stalled body kept the connection open")
	}
}

// A server that does not start its answer in responseTimeout fails the
// request.
func TestProxyResponseTimeout(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer upstream.Close()
	defer close(release)
	tp := newTestProxyWith(t, nil, limits{response: 200 * time.Millisecond})
	start := time.Now()
	resp, err := tp.phone(nil, "").Get(upstream.URL + "/never")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || time.Since(start) > 3*time.Second {
		t.Fatalf("status %d after %s", resp.StatusCode, time.Since(start))
	}
}

// The phones' connections are bounded, and refusals are logged sparingly.
func TestProxyBoundsConnections(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	var lines atomic.Int32
	var take atomic.Bool
	take.Store(true)
	tp := &Proxy{CA: ca, Store: NewStore(0, 0), Log: slog.New(countHandler{&lines}), limits: limits{conns: 2},
		Client: func(netip.Addr, bool) (string, bool) { return "pixel", take.Load() }}
	addr, err := tp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tp.Close()
	var open []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		open = append(open, c)
	}
	waitFor(t, "the connections", func() bool { tp.mu.Lock(); defer tp.mu.Unlock(); return tp.nconns == 2 })
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || isTimeout(err) {
		t.Fatalf("the third connection stayed open: %v", err)
	}

	for _, c := range open {
		c.Close()
	}
	waitFor(t, "the connections to close", func() bool { tp.mu.Lock(); defer tp.mu.Unlock(); return tp.nconns == 0 })
	tp.mu.Lock()
	tp.loggedAt = time.Time{}
	tp.mu.Unlock()
	base := lines.Load()
	take.Store(false)
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			c.Read(make([]byte, 1))
			c.Close()
		}
	}
	if n := lines.Load() - base; n != 1 || tp.Rejected() != 4 {
		t.Fatalf("%d log lines for %d refusals", n, tp.Rejected())
	}
}

type countHandler struct{ n *atomic.Int32 }

func (h countHandler) Enabled(context.Context, slog.Level) bool  { return true }
func (h countHandler) Handle(context.Context, slog.Record) error { h.n.Add(1); return nil }
func (h countHandler) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h countHandler) WithGroup(string) slog.Handler             { return h }

// The CA signs only servers' certificates, and leaves last a day.
func TestCertificateLives(t *testing.T) {
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	if len(ca.cert.ExtKeyUsage) != 1 || ca.cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("CA usage %v", ca.cert.ExtKeyUsage)
	}
	leaf, err := ca.Leaf("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if life := leaf.Leaf.NotAfter.Sub(time.Now()); life > 25*time.Hour || life < 23*time.Hour {
		t.Fatalf("leaf lasts %s", life)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: ca.Pool()}); err != nil {
		t.Fatal(err)
	}
	// A cached leaf near its end is made again.
	leaf.Leaf.NotAfter = time.Now().Add(30 * time.Minute)
	again, err := ca.Leaf("example.com")
	if err != nil {
		t.Fatal(err)
	}
	if again == leaf {
		t.Fatal("a leaf near its end was reused")
	}
}

// Secret fields are redacted in JSON cut short, in JSON sent with another
// type, and whole when they hold an object or a list; URLs in headers
// are redacted as URLs.
func TestRedactHarder(t *testing.T) {
	cut := `{"user":"amy","code":12,"password":"hunter2","credentials":{"key":"k1","list":["a"]},"tokens":["t1","t2"],"note":"ok","refresh_token":"abc`
	text, _ := RedactBody("application/json", []byte(cut))
	for _, leak := range []string{"hunter2", "k1", "t1", "abc"} {
		if strings.Contains(text, leak) {
			t.Errorf("cut JSON leaks %q: %s", leak, text)
		}
	}
	for _, kept := range []string{`"user":"amy"`, `"code":12`, `"note":"ok"`} {
		if !strings.Contains(text, kept) {
			t.Errorf("cut JSON lost %s: %s", kept, text)
		}
	}
	text, _ = RedactBody("text/plain", []byte(`{"tokens":["t1"],"password":"hunter2","user":"amy"}`))
	if text != `{"password":"[redacted]","tokens":"[redacted]","user":"amy"}` {
		t.Errorf("JSON as text: %s", text)
	}
	text, _ = RedactBody("application/octet-stream", []byte(`[{"secret":"s1"}]`))
	if text != `[{"secret":"[redacted]"}]` {
		t.Errorf("JSON as bytes: %s", text)
	}
	hs := RedactHeaders([]Header{{"Location", "https://app.example/cb?access_token=abc&state=1"}, {"Referer", "https://u:pw@app.example/x?sig=zz"}})
	if hs[0].Value != "https://app.example/cb?access_token=[redacted]&state=1" || strings.Contains(hs[1].Value, "zz") || strings.Contains(hs[1].Value, "pw") {
		t.Errorf("headers %+v", hs)
	}
}
