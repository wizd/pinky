package traffic

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
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

// testProxy is a proxy on 127.0.0.1 that takes the test's own connections
// as the phone "pixel", and sends every host to upstream.
type testProxy struct {
	*Proxy
	addr  string
	store *Store
	ca    *CA
	quiet atomic.Bool
	// allow is the address the proxy takes as the phone's.
	allow atomic.Pointer[netip.Addr]
}

func newTestProxy(t *testing.T, upstream *httptest.Server) *testProxy {
	t.Helper()
	return newTestProxyWith(t, upstream, limits{})
}

func newTestProxyWith(t *testing.T, upstream *httptest.Server, lim limits) *testProxy {
	t.Helper()
	ca, err := NewCA()
	if err != nil {
		t.Fatal(err)
	}
	tp := &testProxy{store: NewStore(0, 0), ca: ca}
	loop := netip.MustParseAddr("127.0.0.1")
	tp.allow.Store(&loop)
	up := ""
	var tr http.RoundTripper
	if upstream != nil {
		up = upstream.Listener.Addr().String()
		pool := x509.NewCertPool()
		if upstream.Certificate() != nil {
			pool.AddCert(upstream.Certificate())
		}
		dial := func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, up)
		}
		// httptest's certificate is for example.com.
		tr = &http.Transport{DialContext: dial, TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"}, DisableCompression: true}
	}
	tp.Proxy = &Proxy{CA: ca, Store: tp.store, Transport: tr,
		Client: func(a netip.Addr, _ bool) (string, bool) { return "pixel", a == *tp.allow.Load() },
		Record: func(string) bool { return !tp.quiet.Load() },
		limits: lim,
	}
	// The test's servers are on this computer, which the proxy's own dial
	// refuses (TestProxyRefusesThisComputer).
	tp.Proxy.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if up != "" {
			addr = up
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	addr, err := tp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tp.addr = addr
	t.Cleanup(tp.Close)
	return tp
}

// phone is an HTTP client set up as a phone that trusts roots and uses the
// proxy.
func (tp *testProxy) phone(roots *x509.CertPool, serverName string) *http.Client {
	u, _ := url.Parse("http://" + tp.addr)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy:             http.ProxyURL(u),
		TLSClientConfig:   &tls.Config{RootCAs: roots, ServerName: serverName},
		DisableKeepAlives: true,
	}}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProxyDecryptsHTTPS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Set-Cookie", "session=s3cr3t")
		zw := gzip.NewWriter(w)
		zw.Write([]byte(`{"ok":true,"token":"tok-123","echo":` + string(body) + `}`))
		zw.Close()
	}))
	defer upstream.Close()
	tp := newTestProxy(t, upstream)
	c := tp.phone(tp.ca.Pool(), "")
	req, _ := http.NewRequest("POST", "https://api.example.test/v1/login?access_token=abc&page=1", strings.NewReader(`{"user":"amy","password":"hunter2"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer abcdefghijklmnop")
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	// The phone saw a certificate for the host, signed by Pinky's CA.
	if resp.TLS == nil || resp.TLS.PeerCertificates[0].Subject.CommonName != "api.example.test" ||
		resp.TLS.PeerCertificates[0].Issuer.CommonName != tp.ca.Name() {
		t.Fatalf("certificate: %+v", resp.TLS)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	resp.Body.Close()
	if !strings.Contains(string(got), `"password":"hunter2"`) {
		t.Fatalf("the app did not get the server's answer: %s", got)
	}
	waitFor(t, "the request", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if !r.Decrypted || r.Method != "POST" || r.Status != 200 || r.Host != "api.example.test" ||
		r.URL != "https://api.example.test/v1/login?access_token=abc&page=1" || r.Client != netip.MustParseAddr("127.0.0.1") {
		t.Fatalf("request %+v", r)
	}
	if r.Sent != int64(len(`{"user":"amy","password":"hunter2"}`)) || r.Received == 0 {
		t.Errorf("sizes %d %d", r.Sent, r.Received)
	}
	b, err := tp.store.Bodies("pixel", r.ID, BodyMax)
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Request) != `{"user":"amy","password":"hunter2"}` || !strings.Contains(string(b.Response), `"token":"tok-123"`) {
		t.Fatalf("bodies %q %q", b.Request, b.Response)
	}
	// What reaches an agent is redacted.
	reqText, _ := RedactBody(b.RequestType, b.Request)
	respText, _ := RedactBody(b.ResponseType, b.Response)
	if strings.Contains(reqText, "hunter2") || strings.Contains(respText, "tok-123") || !strings.Contains(reqText, `"user":"amy"`) {
		t.Errorf("redacted bodies: %s / %s", reqText, respText)
	}
	for _, h := range RedactHeaders(append(r.Headers, r.ResponseHeaders...)) {
		if strings.Contains(h.Value, "abcdefghijklmnop") || strings.Contains(h.Value, "s3cr3t") {
			t.Errorf("header %s: %s", h.Name, h.Value)
		}
	}
	if u := RedactURL(r.URL); strings.Contains(u, "abc&") {
		t.Errorf("url %s", u)
	}
	if _, err := tp.store.Bodies("other", r.ID, BodyMax); err != ErrGone {
		t.Errorf("another phone's bodies: %v", err)
	}
}

func TestProxyPlainHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Connection") != "" {
			t.Error("a hop-by-hop header went on")
		}
		w.Write([]byte("hello " + r.URL.Path))
	}))
	defer upstream.Close()
	tp := newTestProxy(t, nil)
	c := tp.phone(nil, "")
	resp, err := c.Get(upstream.URL + "/items?id=7")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello /items" {
		t.Fatalf("body %q", body)
	}
	waitFor(t, "the request", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if r.URL != upstream.URL+"/items?id=7" || r.Status != 200 || !r.Decrypted || r.Received != int64(len(body)) {
		t.Fatalf("request %+v", r)
	}
	// While the person holds the phone its requests go through and are not
	// kept.
	tp.quiet.Store(true)
	resp, err = c.Get(upstream.URL + "/private")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if n := len(tp.store.Requests(Query{Device: "pixel"})); n != 1 {
		t.Fatalf("%d requests kept while quiet", n)
	}
}

func TestProxyPassesPinnedHostsThrough(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("from the server"))
	}))
	defer upstream.Close()
	tp := newTestProxy(t, upstream)
	// A phone (or app) that trusts only the server's real certificate.
	pool := x509.NewCertPool()
	pool.AddCert(upstream.Certificate())
	c := tp.phone(pool, "example.com")
	if _, err := c.Get("https://example.com/"); err == nil {
		t.Fatal("the app accepted Pinky's certificate though it trusts only the server's")
	}
	waitFor(t, "the refused handshake", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if r.Decrypted || !r.Failed || r.Method != "CONNECT" || r.URL != "https://example.com:443" || !strings.Contains(r.Note, "refused") {
		t.Fatalf("refused handshake: %+v", r)
	}
	// The next connection to the host goes through untouched, so the app
	// works.
	resp, err := c.Get("https://example.com/")
	if err != nil {
		t.Fatalf("passed through: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "from the server" {
		t.Fatalf("body %q", body)
	}
	waitFor(t, "the tunnel", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 2 })
	r = tp.store.Requests(Query{Device: "pixel"})[1]
	if r.Decrypted || r.Failed || r.Method != "CONNECT" || r.Sent == 0 || r.Received == 0 || !strings.Contains(r.Note, "passed through") {
		t.Fatalf("tunnel: %+v", r)
	}
	if got := tp.Pinned(); len(got) != 1 || got[0] != "example.com" {
		t.Errorf("pinned %v", got)
	}
}

func TestProxyRefusesOtherAddresses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request from another address went through")
	}))
	defer upstream.Close()
	tp := newTestProxy(t, nil)
	other := netip.MustParseAddr("192.168.1.50")
	tp.allow.Store(&other)
	if _, err := tp.phone(nil, "").Get(upstream.URL); err == nil {
		t.Fatal("the proxy took a connection from an address it should not")
	}
	waitFor(t, "the refusal", func() bool { return tp.Rejected() == 1 })
	if n, _ := tp.store.Len(); n != 0 {
		t.Errorf("%d requests kept", n)
	}
	// Nothing at all without Client.
	bare := &Proxy{CA: tp.ca, Store: tp.store}
	addr, err := bare.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	u, _ := url.Parse("http://" + addr)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	if _, err := c.Get(upstream.URL); err == nil {
		t.Fatal("the proxy took a connection without Client")
	}
}

func TestProxyServesTheCertificate(t *testing.T) {
	tp := newTestProxy(t, nil)
	tp.pin("bank.example.test")
	resp, err := http.Get("http://" + tp.addr + CertPath)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(der, tp.ca.CertDER()) || resp.Header.Get("Content-Type") != "application/x-x509-ca-cert" {
		t.Fatalf("certificate: %s, %d bytes", resp.Header.Get("Content-Type"), len(der))
	}
	if len(tp.Pinned()) != 0 {
		t.Error("serving the certificate kept the pinned hosts")
	}
	// Through the proxy too, as a phone set to use it would ask.
	resp, err = tp.phone(nil, "").Get("http://" + tp.addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), CertPath) || !strings.Contains(string(page), tp.ca.Fingerprint()) {
		t.Fatalf("page:\n%s", page)
	}
	if n, _ := tp.store.Len(); n != 0 {
		t.Errorf("the proxy kept its own page as a request")
	}
}

func TestProxyStop(t *testing.T) {
	tp := newTestProxy(t, nil)
	tp.Stop(tp.addr)
	if len(tp.Addrs()) != 0 {
		t.Fatal("still listening")
	}
	if c, err := net.DialTimeout("tcp", tp.addr, time.Second); err == nil {
		c.Close()
		t.Fatal("still takes connections")
	}
}

func TestStoreBounds(t *testing.T) {
	s := NewStore(3, 0)
	start := time.Now()
	for i := range 5 {
		s.Add(Request{Device: "pixel", Start: start.Add(time.Duration(i) * time.Second), URL: "https://example.com/"}, Bodies{})
	}
	if n, _ := s.Len(); n != 3 || s.Dropped("pixel") != 2 {
		t.Fatalf("held %d, dropped %d", n, s.Dropped("pixel"))
	}
	rs := s.Requests(Query{Device: "pixel"})
	if rs[0].ID != "3" || rs[2].ID != "5" {
		t.Errorf("kept %s…%s, want the newest", rs[0].ID, rs[2].ID)
	}
	// Bytes: each body is cut to BodyMax; the total stays under the bound.
	s = NewStore(0, 3*BodyMax)
	big := bytes.Repeat([]byte("x"), 3*BodyMax)
	for range 4 {
		s.Add(Request{Device: "pixel", Start: time.Now()}, Bodies{Request: big})
	}
	n, size := s.Len()
	if size > 3*BodyMax || n != 2 {
		t.Fatalf("held %d requests, %d bytes", n, size)
	}
	b, _ := s.Bodies("pixel", s.Requests(Query{Device: "pixel"})[0].ID, BodyMax)
	if len(b.Request) != BodyMax || !b.RequestCut {
		t.Errorf("body of %d bytes, cut %v", len(b.Request), b.RequestCut)
	}
	// Dropping a phone's requests forgets them.
	s.Add(Request{Device: "ipad"}, Bodies{})
	s.Drop("pixel")
	if n, _ := s.Len(); n != 1 || len(s.Requests(Query{Device: "pixel"})) != 0 || s.Dropped("pixel") != 0 {
		t.Errorf("after Drop: %d", n)
	}
	// The window.
	q := Query{Device: "ipad", From: time.Now().Add(time.Hour)}
	if len(s.Requests(q)) != 0 {
		t.Error("a request before From")
	}
}
