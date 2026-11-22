package traffic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Proxy is the HTTP proxy a phone sends its web requests through while an
// agent's task runs. Plain HTTP it passes on and keeps. HTTPS arrives as
// CONNECT: the proxy answers the TLS handshake with a certificate its CA
// signs for the host, reads the requests inside and passes them on over a
// new TLS connection that checks the server's own certificate. A phone
// that does not trust the CA, or an app that pins its certificate, refuses
// the handshake; the proxy then passes that host's connections through
// untouched for a while, as a tunnel it lists as not decrypted.
//
// It speaks HTTP/1.1 to the phone (it offers only http/1.1 in the
// handshake), and passes no WebSocket connections yet.
//
// It connects to no address on this computer (loopback, its own
// interfaces), and none that is unspecified, link-local (cloud metadata at
// 169.254.169.254 included) or multicast, whatever name led there: it
// checks the address it actually dials, so a name that resolves to such an
// address is refused too.
//
// It takes connections only from the addresses Client accepts, and serves
// the CA's certificate to them at CertPath, for the person to install, and
// a proxy auto-config file at PACPath. ListenSetup listens for those two
// files only, never proxying.
//
// Hosts Blocked names are never decrypted: their connections pass through
// as tunnels kept only as their host, marked blocked. The CONNECT host,
// the server name in the TLS handshake (SNI) and the Host of each request
// inside are all checked: an app that looks names up itself connects to an
// address and names the host only in the handshake.
type Proxy struct {
	CA    *CA
	Store *Store
	// Client says which phone a connection from addr comes from, and
	// whether the proxy takes it at all; setup is true for a listener that
	// serves only the setup files (ListenSetup). It is asked for each
	// connection and each request on it. nil takes none.
	Client func(addr netip.Addr, setup bool) (device string, ok bool)
	// Record says whether to keep a phone's requests now: false while the
	// person holds the phone. The requests still go through. nil keeps all.
	Record func(device string) bool
	// Now gives the time a request starts; nil is time.Now. The gateway
	// orders requests against when the person held the phone, so it
	// passes a clock that never gives the same time twice.
	Now func() time.Time
	// Transport sends requests on; nil connects directly and checks the
	// servers' certificates.
	Transport http.RoundTripper
	// Dial connects the tunnels, and the requests when Transport is nil;
	// nil dials directly, refusing the addresses above. A Dial set here
	// makes no such check (tests).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Blocked says whether a phone's host is one Pinky never decrypts or
	// records beyond its host; nil blocks only BlockedHosts.
	Blocked func(device, host string) bool
	Log     *slog.Logger

	once sync.Once
	tr   http.RoundTripper

	mu        sync.Mutex
	listeners map[string]net.Listener
	// setupOnly: the listeners that serve only the setup files, to the
	// proxy address their PAC file names.
	setupOnly map[string]string
	conns     map[net.Conn]string // open connections, by the listener's address
	pinned    map[string]time.Time
	// via: CONNECT hosts (mostly addresses) inside whose connections an
	// app named a blocked host, to that host, for a while.
	via      map[string]viaHost
	rejected int
	loggedAt time.Time // when a refused connection was last logged
	nconns   int       // the phones' open connections

	// limits replace idleTimeout, responseTimeout and maxConns where not
	// zero; tests set them before Listen.
	limits limits

	ownMu sync.Mutex
	own   map[netip.Addr]bool // this computer's addresses
	ownAt time.Time
}

type limits struct {
	idle, response time.Duration
	conns          int
}

func (p *Proxy) idle() time.Duration {
	if p.limits.idle > 0 {
		return p.limits.idle
	}
	return idleTimeout
}

func (p *Proxy) maxConns() int {
	if p.limits.conns > 0 {
		return p.limits.conns
	}
	return maxConns
}

func (p *Proxy) responseTimeout() time.Duration {
	if p.limits.response > 0 {
		return p.limits.response
	}
	return responseTimeout
}

type viaHost struct {
	host  string
	until time.Time
}

// Paths the proxy serves itself, to a phone that opens its address in a
// browser.
const (
	CertPath = "/pinky-ca.crt"
	pemPath  = "/pinky-ca.pem"
	// PACPath is the proxy auto-config file: the proxy, or a direct
	// connection when the proxy does not answer.
	PACPath = "/proxy.pac"
)

// Timeouts and bounds. Tests set Proxy.limits to shorten the first three.
const (
	// idleTimeout: a phone's connection that sends nothing for this long,
	// between requests or within a body, is closed.
	idleTimeout = 2 * time.Minute
	// responseTimeout is how long a server may take to start its answer.
	responseTimeout = 2 * time.Minute
	// maxConns bounds the phones' open connections, all listeners together.
	maxConns         = 512
	handshakeTimeout = 15 * time.Second
	// headerMax bounds a request's line and headers.
	headerMax = 1 << 20
	// helloMax bounds the handshake bytes kept until the server name is
	// known.
	helloMax = 64 << 10
	// logEvery spaces the log lines about refused connections.
	logEvery = time.Minute
	// pinnedFor is how long a host whose handshake failed passes through.
	pinnedFor = 10 * time.Minute
	pinnedMax = 4096
)

// Listen starts taking connections at addr ("192.168.1.5:9277", or
// "127.0.0.1:0" for any free port) and returns the address it listens at.
// Listening there already is not an error.
func (p *Proxy) Listen(addr string) (string, error) { return p.listen(addr, "") }

// ListenSetup starts serving only the setup files at addr: the CA's
// certificate and a PAC file that names proxy ("192.168.1.5:9277"). It
// proxies nothing and keeps nothing; it takes only the addresses Client
// accepts.
func (p *Proxy) ListenSetup(addr, proxy string) (string, error) { return p.listen(addr, proxy) }

func (p *Proxy) listen(addr, setupFor string) (string, error) {
	p.mu.Lock()
	if _, ok := p.listeners[addr]; ok {
		p.mu.Unlock()
		return addr, nil
	}
	p.mu.Unlock()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	at := ln.Addr().String()
	p.mu.Lock()
	if p.listeners == nil {
		p.listeners = map[string]net.Listener{}
		p.setupOnly = map[string]string{}
		p.conns = map[net.Conn]string{}
	}
	p.listeners[at] = ln
	if setupFor != "" {
		p.setupOnly[at] = setupFor
	}
	p.mu.Unlock()
	go p.accept(ln, at)
	return at, nil
}

// Stop stops listening at addr, and ends its open connections.
func (p *Proxy) Stop(addr string) {
	p.mu.Lock()
	ln := p.listeners[addr]
	delete(p.listeners, addr)
	delete(p.setupOnly, addr)
	var open []net.Conn
	for c, at := range p.conns {
		if at == addr {
			open = append(open, c)
		}
	}
	p.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	for _, c := range open {
		c.Close()
	}
}

// Addrs returns where the proxy listens.
func (p *Proxy) Addrs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.listeners))
	for a := range p.listeners {
		out = append(out, a)
	}
	return out
}

// Close stops listening everywhere.
func (p *Proxy) Close() {
	for _, a := range p.Addrs() {
		p.Stop(a)
	}
}

// Rejected returns how many connections came from addresses Client did not
// take.
func (p *Proxy) Rejected() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rejected
}

func (p *Proxy) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (p *Proxy) accept(ln net.Listener, at string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if _, ok := p.listeners[at]; !ok {
			p.mu.Unlock()
			c.Close()
			return
		}
		if p.nconns >= p.maxConns() {
			p.mu.Unlock()
			c.Close()
			p.refusedLog("too many connections", remoteAddr(c))
			continue
		}
		p.conns[c] = at
		p.nconns++
		p.mu.Unlock()
		go func() {
			defer func() {
				p.mu.Lock()
				delete(p.conns, c)
				p.nconns--
				p.mu.Unlock()
				c.Close()
			}()
			p.serve(c, at)
		}()
	}
}

// refusedLog logs a refused connection, at most once per logEvery.
func (p *Proxy) refusedLog(why string, from netip.Addr) {
	p.mu.Lock()
	p.rejected++
	n := p.rejected
	now := time.Now()
	due := now.Sub(p.loggedAt) >= logEvery
	if due {
		p.loggedAt = now
	}
	p.mu.Unlock()
	if due {
		p.log().Info("traffic: refused a connection", "why", why, "from", from, "refused so far", n)
	}
}

// conn is who a connection came from.
type conn struct {
	device string
	client netip.Addr
	setup  bool // it came to a setup listener
}

// take says whether the proxy takes a connection from addr now.
func (p *Proxy) take(addr netip.Addr, setup bool) (conn, bool) {
	if p.Client == nil {
		return conn{}, false
	}
	dev, ok := p.Client(addr.Unmap(), setup)
	return conn{device: dev, client: addr.Unmap(), setup: setup}, ok
}

// idleConn closes a phone's connection that sends nothing for idleTimeout:
// each read waits that long at most. Tunnels turn it off.
type idleConn struct {
	net.Conn
	idle time.Duration
	off  bool
	wait time.Duration // instead of idle, when not 0
}

func (c *idleConn) Read(b []byte) (int, error) {
	if !c.off {
		w := c.wait
		if w == 0 {
			w = c.idle
		}
		_ = c.Conn.SetReadDeadline(time.Now().Add(w))
	}
	return c.Conn.Read(b)
}

func (c *idleConn) CloseWrite() error {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// headerLimit lets at most n bytes through until it is reset; it bounds a
// request's line and headers, and is lifted for the body.
type headerLimit struct {
	r io.Reader
	n int64 // < 0: no limit
}

func (l *headerLimit) Read(b []byte) (int, error) {
	if l.n < 0 {
		return l.r.Read(b)
	}
	if l.n == 0 {
		return 0, errHeaderTooLarge
	}
	if int64(len(b)) > l.n {
		b = b[:l.n]
	}
	n, err := l.r.Read(b)
	l.n -= int64(n)
	return n, err
}

var errHeaderTooLarge = errors.New("traffic: request headers too large")

// readRequest reads one request whose line and headers are at most
// headerMax bytes, and lifts the limit for its body.
func readRequest(br *bufio.Reader, lim *headerLimit) (*http.Request, error) {
	lim.n = headerMax
	req, err := http.ReadRequest(br)
	lim.n = -1
	return req, err
}

func remoteAddr(c net.Conn) netip.Addr {
	if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

func (p *Proxy) serve(c net.Conn, at string) {
	p.mu.Lock()
	setupFor, setupOnly := p.setupOnly[at]
	p.mu.Unlock()
	pac := at
	if setupOnly {
		pac = setupFor
	}
	cx, ok := p.take(remoteAddr(c), setupOnly)
	if !ok {
		p.refusedLog("not a phone Pinky captures now", remoteAddr(c))
		return
	}
	ic := &idleConn{Conn: c, idle: p.idle()}
	lim := &headerLimit{r: ic, n: -1}
	br := bufio.NewReader(lim)
	for {
		req, err := readRequest(br, lim)
		if err != nil {
			if errors.Is(err, errHeaderTooLarge) {
				writeError(c, http.StatusRequestHeaderFieldsTooLarge, "The request's headers are too large for Pinky's proxy.")
			}
			return
		}
		if _, ok := p.take(cx.client, setupOnly); !ok {
			return
		}
		switch {
		case setupOnly && (req.Method == http.MethodConnect || (req.URL.IsAbs() && !p.isSelf(hostPort(req.URL.Host, "80")))):
			// Outside a task this address hands out the setup files only.
			writeError(c, http.StatusForbidden, "Pinky's proxy runs only during an agent's task; this address serves only Pinky's setup files.")
			return
		case req.Method == http.MethodConnect:
			p.connect(ic, br, req, cx)
			return
		case !req.URL.IsAbs() || p.isSelf(req.URL.Host):
			if !p.page(c, req, pac) {
				return
			}
		default:
			if req.URL.Host == "" {
				req.URL.Host = req.Host
			}
			if p.isSelf(hostPort(req.URL.Host, "80")) {
				if !p.page(c, req, pac) {
					return
				}
				continue
			}
			if !p.forward(c, req, cx, "", false) {
				return
			}
		}
	}
}

// hostPort adds port to a host without one.
func hostPort(hp, port string) string {
	if _, _, err := net.SplitHostPort(hp); err == nil {
		return hp
	}
	return net.JoinHostPort(strings.Trim(hp, "[]"), port)
}

// isSelf reports whether host:port is where the proxy listens, which it
// never passes requests on to.
func (p *Proxy) isSelf(hp string) bool {
	if hp == "" {
		return false
	}
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for a := range p.listeners {
		lh, lp, _ := net.SplitHostPort(a)
		if lp != port {
			continue
		}
		if lh == host || host == "localhost" || host == "127.0.0.1" || host == "::1" {
			return true
		}
	}
	return false
}

// page answers a request to the proxy itself: the CA's certificate, the
// PAC file naming the proxy at pac, or a page that links to the
// certificate. It returns whether the connection goes on.
func (p *Proxy) page(w io.Writer, req *http.Request, pac string) bool {
	if req.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(req.Body, 1<<16))
	}
	status, ctype, body := http.StatusOK, "text/html; charset=utf-8", []byte(nil)
	header := http.Header{}
	switch req.URL.Path {
	case CertPath:
		ctype, body = "application/x-x509-ca-cert", p.CA.CertDER()
		header.Set("Content-Disposition", `attachment; filename="pinky-ca.crt"`)
		// The person is installing the certificate: hosts that refused the
		// old trust get another chance.
		p.mu.Lock()
		p.pinned = nil
		p.mu.Unlock()
	case PACPath:
		ctype, body = "application/x-ns-proxy-autoconfig", []byte(PAC(pac))
	case pemPath:
		ctype, body = "application/x-pem-file", p.CA.CertPEM()
		header.Set("Content-Disposition", `attachment; filename="pinky-ca.pem"`)
	case "/", "":
		body = []byte(fmt.Sprintf(caPage, html.EscapeString(p.CA.Name()), CertPath, html.EscapeString(p.CA.Fingerprint())))
	default:
		status, ctype, body = http.StatusNotFound, "text/plain; charset=utf-8", []byte("Not found\n")
	}
	header.Set("Content-Type", ctype)
	header.Set("Cache-Control", "no-store")
	resp := &http.Response{StatusCode: status, ProtoMajor: 1, ProtoMinor: 1, Header: header,
		ContentLength: int64(len(body)), Body: io.NopCloser(bytes.NewReader(body)), Close: req.Close, Request: req}
	if req.Method == http.MethodHead {
		resp.Body = http.NoBody
	}
	return resp.Write(w) == nil && !req.Close
}

// PAC returns a proxy auto-config file that sends the phone's web
// requests to proxy, and connects directly when the proxy does not answer
// (outside an agent's task, or with Pinky not running). Names without a
// dot and .local ones always go directly.
func PAC(proxy string) string {
	return `function FindProxyForURL(url, host) {
  if (isPlainHostName(host) || dnsDomainIs(host, ".local")) {
    return "DIRECT";
  }
  return "PROXY ` + proxy + `; DIRECT";
}
`
}

const caPage = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Pinky</title>
<style>body{font:17px -apple-system,system-ui,sans-serif;margin:2em auto;max-width:34em;padding:0 1em;line-height:1.5}a.b{display:inline-block;padding:.7em 1.2em;border-radius:.6em;background:#e8457a;color:#fff;text-decoration:none}code{word-break:break-all;font-size:13px}</style>
</head><body>
<h1>Pinky</h1>
<p>Install this certificate so that Pinky can read the web requests this phone makes during an agent's task. Pinky's window on the computer shows the steps.</p>
<p><a class="b" href="%[2]s">Download “%[1]s”</a></p>
<p>SHA-256: <code>%[3]s</code></p>
</body></html>
`

// forward passes one request on and its response back, and keeps both.
// target is where a decrypted request goes ("host:443"); "" for plain
// HTTP. It returns whether the connection goes on.
func (p *Proxy) forward(w io.Writer, req *http.Request, cx conn, target string, decrypted bool) bool {
	start := p.now()
	gen := p.gen(cx.device)
	keep := p.Record == nil || p.Record(cx.device)
	req.RequestURI = ""
	if decrypted {
		req.URL.Scheme, req.URL.Host = "https", target
	}
	host := req.URL.Hostname()
	if decrypted && req.Host != "" {
		host = hostOnly(req.Host)
	}
	if p.blocked(cx.device, host) {
		// Passed on, kept only as its host.
		if keep {
			scheme := "http://"
			if decrypted {
				scheme = "https://"
			}
			p.keep(true, gen, blockedRequest(cx, start, scheme+host, host), Bodies{})
		}
		keep = false
	}
	r := Request{Device: cx.device, Client: cx.client, Start: start, Method: req.Method, Host: host, Decrypted: true}
	r.URL = displayURL(req, decrypted)
	r.Headers = headerList(req.Header)
	if req.Header.Get("Upgrade") != "" {
		r.End, r.Failed, r.Note = time.Now(), true, "a WebSocket or other upgrade, which Pinky's proxy does not pass on yet"
		writeError(w, http.StatusNotImplemented, "Pinky's proxy does not pass WebSocket connections on yet.")
		p.keep(keep, gen, r, Bodies{})
		return false
	}
	if strings.EqualFold(req.Header.Get("Expect"), "100-continue") {
		// The app waits for this before it sends the body.
		if _, err := io.WriteString(w, "HTTP/1.1 100 Continue\r\n\r\n"); err != nil {
			return false
		}
		req.Header.Del("Expect")
	}
	clientClose := req.Close
	removeHop(req.Header)
	reqBody := &capture{}
	if req.Body != nil && req.Body != http.NoBody {
		req.Body = &teeBody{ReadCloser: req.Body, c: reqBody}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := p.transport().RoundTrip(req.WithContext(ctx))
	if err != nil {
		r.End, r.Failed, r.Note = time.Now(), true, shortErr(err)
		r.Sent = reqBody.n
		writeError(w, http.StatusBadGateway, "Pinky's proxy could not reach the server: "+shortErr(err))
		p.keep(keep, gen, r, Bodies{Request: reqBody.bytes(), RequestCut: reqBody.cut(), RequestType: req.Header.Get("Content-Type"),
			RequestEncoding: req.Header.Get("Content-Encoding")})
		return false
	}
	defer resp.Body.Close()
	removeHop(resp.Header)
	r.Status = resp.StatusCode
	r.ResponseHeaders = headerList(resp.Header)
	respBody := &capture{}
	resp.Body = &teeBody{ReadCloser: resp.Body, c: respBody}
	resp.Proto, resp.ProtoMajor, resp.ProtoMinor = "HTTP/1.1", 1, 1
	resp.TransferEncoding = nil
	if resp.ContentLength < 0 && bodyAllowed(req.Method, resp.StatusCode) {
		resp.TransferEncoding = []string{"chunked"}
	}
	resp.Close = clientClose
	werr := resp.Write(w)
	r.End, r.Sent, r.Received = time.Now(), reqBody.n, respBody.n
	if werr != nil {
		r.Failed, r.Note = true, "the connection to the phone broke: "+shortErr(werr)
	}
	p.keep(keep, gen, r, Bodies{
		Request: reqBody.bytes(), RequestCut: reqBody.cut(), RequestType: req.Header.Get("Content-Type"), RequestEncoding: req.Header.Get("Content-Encoding"),
		Response: respBody.bytes(), ResponseCut: respBody.cut(), ResponseType: resp.Header.Get("Content-Type"), ResponseEncoding: resp.Header.Get("Content-Encoding"),
	})
	return werr == nil && !clientClose
}

// blocked reports whether Pinky never decrypts or records a phone's host.
func (p *Proxy) blocked(device, host string) bool {
	if p.Blocked != nil {
		return p.Blocked(device, host)
	}
	return Blocked(host, nil)
}

// BlockedNote is the note of a blocked host's request.
const BlockedNote = "blocked: Pinky never decrypts or records this host"

// blockedRequest is all Pinky keeps of a request to a blocked host. It is
// kept as the connection opens, not as it ends, so that it shows at once
// and in order: a blocked tunnel can stay open for minutes. Its End is
// that moment too; the store keeps repeated ones as one row
// (Request.Count).
func blockedRequest(cx conn, start time.Time, url, host string) Request {
	return Request{Device: cx.device, Client: cx.client, Start: start, End: start, Method: http.MethodConnect,
		URL: url, Host: host, Blocked: true, Note: BlockedNote}
}

// keep stores a request that started when the phone's store was at
// generation gen: one that ends after the phone's requests were dropped
// (its task ended meanwhile) is not kept.
func (p *Proxy) keep(keep bool, gen int, r Request, b Bodies) {
	if keep && p.Store != nil {
		p.Store.AddSince(gen, r, b)
	}
}

// now is Now, or time.Now.
func (p *Proxy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// gen is a phone's store generation now.
func (p *Proxy) gen(device string) int {
	if p.Store == nil {
		return 0
	}
	return p.Store.Gen(device)
}

// displayURL is the URL as the app asked for it: the Host header's name,
// without the default port.
func displayURL(req *http.Request, decrypted bool) string {
	u := *req.URL
	if decrypted && req.Host != "" {
		h, port, err := net.SplitHostPort(req.Host)
		if err != nil {
			h, port = req.Host, ""
		}
		if port == "" || port == "443" {
			u.Host = h
		} else {
			u.Host = req.Host
		}
	}
	if u.Scheme == "https" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	} else {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	}
	return u.String()
}

func hostOnly(hp string) string {
	if h, _, err := net.SplitHostPort(hp); err == nil {
		return h
	}
	return strings.Trim(hp, "[]")
}

func bodyAllowed(method string, status int) bool {
	return method != http.MethodHead && status >= 200 && status != http.StatusNoContent && status != http.StatusNotModified
}

func writeError(w io.Writer, status int, msg string) {
	msg += "\n"
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(msg), msg)
}

func shortErr(err error) string {
	s := err.Error()
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// Hop-by-hop headers, which a proxy does not pass on.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func removeHop(h http.Header) {
	for _, f := range h.Values("Connection") {
		for _, name := range strings.Split(f, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopHeaders {
		h.Del(name)
	}
}

func headerList(h http.Header) []Header {
	var out []Header
	for name, vs := range h {
		for _, v := range vs {
			out = append(out, Header{Name: name, Value: v})
		}
	}
	sortHeaders(out)
	return out
}

func sortHeaders(hs []Header) {
	for i := 1; i < len(hs); i++ {
		for j := i; j > 0 && hs[j].Name < hs[j-1].Name; j-- {
			hs[j], hs[j-1] = hs[j-1], hs[j]
		}
	}
}

// capture keeps the first BodyMax bytes of a body, and counts them all.
type capture struct {
	buf bytes.Buffer
	n   int64
}

func (c *capture) Write(b []byte) {
	c.n += int64(len(b))
	if room := BodyMax + 1 - c.buf.Len(); room > 0 {
		if len(b) > room {
			b = b[:room]
		}
		c.buf.Write(b)
	}
}

func (c *capture) bytes() []byte {
	b := c.buf.Bytes()
	if len(b) > BodyMax {
		b = b[:BodyMax]
	}
	return bytes.Clone(b)
}

func (c *capture) cut() bool { return c.buf.Len() > BodyMax }

type teeBody struct {
	io.ReadCloser
	c *capture
}

func (t *teeBody) Read(b []byte) (int, error) {
	n, err := t.ReadCloser.Read(b)
	if n > 0 {
		t.c.Write(b[:n])
	}
	return n, err
}

func (p *Proxy) transport() http.RoundTripper {
	if p.Transport != nil {
		return p.Transport
	}
	p.once.Do(func() {
		p.tr = &http.Transport{
			Proxy:                  nil, // never through another proxy, nor back through this one
			DialContext:            p.dial,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           100,
			IdleConnTimeout:        90 * time.Second,
			TLSHandshakeTimeout:    handshakeTimeout,
			ResponseHeaderTimeout:  p.responseTimeout(),
			MaxResponseHeaderBytes: headerMax,
			// Pass the app's own Accept-Encoding on, and the body as the
			// server sent it.
			DisableCompression: true,
		}
	})
	return p.tr
}

// errRefusedAddr: the proxy does not connect there.
var errRefusedAddr = errors.New("Pinky's proxy does not connect to this computer, or to link-local or multicast addresses")

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if p.isSelf(addr) {
		return nil, errors.New("the request points back at Pinky's proxy")
	}
	if p.Dial != nil {
		return p.Dial(ctx, network, addr)
	}
	// The address is checked as dialled, after the name is looked up, so
	// that no name (localhost., 127.1, one that resolves to this computer
	// or changes its answer) reaches it.
	d := net.Dialer{Timeout: handshakeTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		if p.refusedAddr(ap.Addr()) {
			return errRefusedAddr
		}
		return nil
	}}
	return d.DialContext(ctx, network, addr)
}

// refusedAddr reports whether the proxy never connects to ip: this
// computer (loopback, unspecified, the addresses of its interfaces),
// link-local (cloud metadata at 169.254.169.254 included), multicast and
// broadcast. Other addresses on the phone's network stay reachable: the
// phone reaches them by itself anyway, and apps use them (a printer, a
// NAS, a TV).
func (p *Proxy) refusedAddr(ip netip.Addr) bool {
	ip = ip.WithZone("").Unmap()
	switch {
	case !ip.IsValid(), ip.IsLoopback(), ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return true
	case ip.Is4() && (ip.As4()[0] == 0 || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})):
		return true
	}
	return p.ownAddr(ip)
}

// ownAddr reports whether ip is one of this computer's addresses, read
// again every few seconds.
func (p *Proxy) ownAddr(ip netip.Addr) bool {
	p.ownMu.Lock()
	defer p.ownMu.Unlock()
	if p.own == nil || time.Since(p.ownAt) > 10*time.Second {
		own := map[netip.Addr]bool{}
		if addrs, err := net.InterfaceAddrs(); err == nil {
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					if x, ok := netip.AddrFromSlice(n.IP); ok {
						own[x.Unmap()] = true
					}
				}
			}
		}
		p.own, p.ownAt = own, time.Now()
	}
	return p.own[ip]
}

// connect answers CONNECT: it decrypts the connection when the phone
// accepts the CA's certificate for the host, and passes it through
// untouched otherwise. A blocked host, whether the CONNECT, the TLS server
// name or a request inside names it, is not decrypted.
func (p *Proxy) connect(c *idleConn, br *bufio.Reader, req *http.Request, cx conn) {
	target := hostPort(req.Host, "443")
	host, port, _ := net.SplitHostPort(target)
	if p.isSelf(target) {
		writeError(c, http.StatusForbidden, "That is Pinky's proxy itself.")
		return
	}
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	start := p.now()
	gen := p.gen(cx.device)
	keep := p.Record == nil || p.Record(cx.device)
	if name := p.blockedName(cx.device, host); name != "" {
		p.keep(keep, gen, blockedRequest(cx, start, "https://"+name, name), Bodies{})
		p.tunnel(c, br, target, name, cx, start, false, "", gen)
		return
	}
	if p.isPinned(host) {
		p.tunnel(c, br, target, host, cx, start, keep, "passed through: this host refused Pinky's certificate earlier", gen)
		return
	}
	c.wait = handshakeTimeout
	first, err := br.Peek(1)
	if err != nil {
		return
	}
	if first[0] != 0x16 { // not a TLS handshake
		c.wait = 0
		p.tunnel(c, br, target, host, cx, start, keep, "passed through: not TLS", gen)
		return
	}
	// The handshake's bytes are kept until its server name is known, so
	// that a blocked one still passes through untouched: nothing has been
	// answered yet, and they are sent on as they came.
	hc := &helloConn{Conn: c, r: br, keeping: true}
	var sni, why string
	tc := tls.Server(hc, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = strings.ToLower(strings.TrimSuffix(h.ServerName, "."))
			switch {
			case sni != "" && p.blocked(cx.device, sni):
				why = "blocked"
				hc.muted = true
				return nil, errBlockedName
			case sni != "" && net.ParseIP(host) == nil && sni != strings.ToLower(strings.TrimSuffix(host, ".")):
				why = "mismatch"
				return nil, errOtherName
			}
			hc.keeping = false
			hc.kept = nil
			return nil, nil
		},
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := sni
			if name == "" {
				name = host
			}
			return p.CA.Leaf(name)
		},
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS12,
	})
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err = tc.HandshakeContext(ctx)
	cancel()
	switch why {
	case "blocked":
		c.wait = 0
		if hc.over {
			return // too much to send on as it came
		}
		p.keep(keep, gen, blockedRequest(cx, start, "https://"+sni, sni), Bodies{})
		p.tunnel(c, io.MultiReader(bytes.NewReader(hc.kept), br), target, sni, cx, start, false, "", gen)
		return
	case "mismatch":
		p.keep(keep, gen, Request{Device: cx.device, Client: cx.client, Start: start, End: time.Now(), Method: http.MethodConnect,
			URL: "https://" + target, Host: host, Failed: true,
			Note: "refused: the app's handshake named another host (" + sni + ") than the one it connected to"}, Bodies{})
		return
	}
	c.wait = 0
	refused := func(why string) {
		p.pin(host)
		p.keep(keep, gen, Request{Device: cx.device, Client: cx.client, Start: start, End: time.Now(), Method: http.MethodConnect,
			URL: "https://" + target, Host: host, Failed: true, Note: why}, Bodies{})
	}
	if err != nil {
		refused("the phone refused Pinky's certificate (it is not trusted there, or the app pins its own); this host passes through without decryption for a while")
		return
	}
	name := host
	if sni != "" {
		name = sni
	}
	upstream := net.JoinHostPort(name, port)
	lim := &headerLimit{r: tc, n: -1}
	tbr := bufio.NewReader(lim)
	served := 0
	for {
		r, err := readRequest(tbr, lim)
		if err != nil {
			if errors.Is(err, errHeaderTooLarge) {
				writeError(tc, http.StatusRequestHeaderFieldsTooLarge, "The request's headers are too large for Pinky's proxy.")
			} else if served == 0 && !errors.Is(err, net.ErrClosed) && !isTimeout(err) {
				// An app that checks the certificate itself after the
				// handshake hangs up without a request.
				refused("the app hung up after the handshake, as one that pins its certificate does; this host passes through without decryption for a while")
			}
			return
		}
		if _, ok := p.take(cx.client, cx.setup); !ok {
			return
		}
		served++
		if h := hostOnly(r.Host); h != "" && p.blocked(cx.device, h) {
			// Only inside did the app name a blocked host: this request
			// is passed on and kept as its host only, the connection
			// ends, and the next ones to this address pass through
			// untouched.
			p.blockVia(host, h)
			p.forward(tc, r, cx, upstream, true)
			return
		}
		if !p.forward(tc, r, cx, upstream, true) {
			return
		}
	}
}

var (
	errBlockedName = errors.New("traffic: the handshake names a blocked host")
	errOtherName   = errors.New("traffic: the handshake names another host than the CONNECT")
)

// helloConn keeps the bytes the TLS handshake reads, up to helloMax, and
// sends nothing to the phone while muted.
type helloConn struct {
	net.Conn
	r       io.Reader
	keeping bool
	over    bool // more than helloMax came
	kept    []byte
	muted   bool
}

func (h *helloConn) Read(b []byte) (int, error) {
	n, err := h.r.Read(b)
	if h.keeping && n > 0 {
		if len(h.kept)+n > helloMax {
			h.over, h.keeping, h.kept = true, false, nil
		} else {
			h.kept = append(h.kept, b[:n]...)
		}
	}
	return n, err
}

func (h *helloConn) Write(b []byte) (int, error) {
	if h.muted {
		return len(b), nil
	}
	return h.Conn.Write(b)
}

// blockedName returns the blocked host a CONNECT host stands for: itself
// when it is blocked, or the blocked host an app named inside an earlier
// connection to it (blockVia); "" when none.
func (p *Proxy) blockedName(device, host string) string {
	if p.blocked(device, host) {
		return host
	}
	p.mu.Lock()
	v, ok := p.via[strings.ToLower(host)]
	p.mu.Unlock()
	if ok && time.Now().Before(v.until) && p.blocked(device, v.host) {
		return v.host
	}
	return ""
}

// blockVia notes that connections to the CONNECT host addr carry the
// blocked host, for pinnedFor.
func (p *Proxy) blockVia(addr, host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.via == nil || len(p.via) >= pinnedMax {
		p.via = map[string]viaHost{}
	}
	p.via[strings.ToLower(addr)] = viaHost{host: strings.ToLower(host), until: time.Now().Add(pinnedFor)}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func (p *Proxy) pin(host string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pinned == nil || len(p.pinned) >= pinnedMax {
		p.pinned = map[string]time.Time{}
	}
	p.pinned[strings.ToLower(host)] = time.Now().Add(pinnedFor)
}

func (p *Proxy) isPinned(host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	until, ok := p.pinned[strings.ToLower(host)]
	return ok && time.Now().Before(until)
}

// Pinned lists the hosts that pass through now.
func (p *Proxy) Pinned() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for h, until := range p.pinned {
		if time.Now().Before(until) {
			out = append(out, h)
		}
	}
	return out
}

// tunnel passes a connection through untouched, and keeps it as one
// request that was not decrypted.
func (p *Proxy) tunnel(c net.Conn, br io.Reader, target, host string, cx conn, start time.Time, keep bool, note string, gen int) {
	if ic, ok := c.(*idleConn); ok {
		// A tunnel may rightly stay quiet for long (a push connection).
		ic.off = true
		_ = ic.SetReadDeadline(time.Time{})
	}
	r := Request{Device: cx.device, Client: cx.client, Start: start, Method: http.MethodConnect,
		URL: "https://" + target, Host: host, Note: note}
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	up, err := p.dial(ctx, "tcp", target)
	cancel()
	if err != nil {
		r.End, r.Failed, r.Note = time.Now(), true, shortErr(err)
		p.keep(keep, gen, r, Bodies{})
		return
	}
	defer up.Close()
	var sent, recv int64
	done := make(chan struct{}, 2)
	go func() {
		sent, _ = io.Copy(up, br)
		closeWrite(up)
		done <- struct{}{}
	}()
	go func() {
		recv, _ = io.Copy(c, up)
		closeWrite(c)
		done <- struct{}{}
	}()
	<-done
	// One side is done; give the other a moment to finish.
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_ = up.SetDeadline(time.Now().Add(5 * time.Second))
	<-done
	r.End, r.Sent, r.Received = time.Now(), sent, recv
	p.keep(keep, gen, r, Bodies{})
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// Port returns the port of an address Listen returned.
func Port(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}
