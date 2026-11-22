package traffic

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/secret"
)

func TestBlocked(t *testing.T) {
	for _, c := range []struct {
		host string
		want bool
	}{
		{"www.paypal.com", true},
		{"paypal.com.", true},
		{"mapi.alipay.com", true},
		{"pay.weixin.qq.com", true},
		{"weixin.qq.com", false},
		{"my.1password.com", true},
		{"notpaypal.com", false},
		{"api.example.com", false},
		{"bank.example.test", true}, // the person's own
		{"x.bank.example.test", true},
	} {
		if got := Blocked(c.host, []string{"bank.example.test"}); got != c.want {
			t.Errorf("Blocked(%q) = %v, want %v", c.host, got, c.want)
		}
	}
	got := CleanHosts([]string{" https://Bank.Example/login ", "*.shop.test", ".shop.test", "a b", "", "host.test:8443", "bank.example"})
	if strings.Join(got, ",") != "bank.example,shop.test,host.test" {
		t.Errorf("CleanHosts = %q", got)
	}
}

// A blocked host is tunnelled untouched and kept only as its host, even
// for a phone that trusts Pinky's certificate; over plain HTTP too.
func TestProxyTunnelsBlockedHosts(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("from the bank"))
	}))
	defer upstream.Close()
	tp := newTestProxy(t, upstream)
	// The phone trusts Pinky's CA and the server's own certificate: had
	// the proxy decrypted, the certificate would be Pinky's.
	pool := tp.ca.Pool()
	pool.AddCert(upstream.Certificate())
	c := tp.phone(pool, "example.com")
	resp, err := c.Get("https://www.paypal.com/v1/login?token=abc")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "from the bank" || resp.TLS.PeerCertificates[0].Issuer.CommonName == tp.ca.Name() {
		t.Fatalf("decrypted a blocked host: %q, issuer %s", body, resp.TLS.PeerCertificates[0].Issuer.CommonName)
	}
	waitFor(t, "the tunnel", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if !r.Blocked || r.Decrypted || r.URL != "https://www.paypal.com" || r.Host != "www.paypal.com" ||
		r.Sent != 0 || r.Received != 0 || len(r.Headers) != 0 || r.Note != BlockedNote {
		t.Fatalf("blocked request %+v", r)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Write([]byte("ok"))
	}))
	defer plain.Close()
	tp2 := newTestProxy(t, plain)
	resp, err = tp2.phone(nil, "").Post("http://paypal.com/pay?card=4111", "text/plain", strings.NewReader("card=4111111111111111"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, "the plain request", func() bool { return len(tp2.store.Requests(Query{Device: "pixel"})) == 1 })
	r = tp2.store.Requests(Query{Device: "pixel"})[0]
	if !r.Blocked || r.URL != "http://paypal.com" || len(r.Headers) != 0 || r.Sent != 0 {
		t.Fatalf("plain blocked request %+v", r)
	}
	if b, _ := tp2.store.Bodies("pixel", r.ID, BodyMax); len(b.Request) != 0 {
		t.Fatalf("kept a blocked body: %q", b.Request)
	}
}

// The setup listener hands out the certificate and the PAC file, and
// proxies nothing.
func TestSetupListener(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the setup listener proxied a request")
	}))
	defer upstream.Close()
	tp := newTestProxy(t, nil)
	tp.Stop(tp.addr)
	at, err := tp.ListenSetup("127.0.0.1:0", "192.168.1.5:9277")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + at + PACPath)
	if err != nil {
		t.Fatal(err)
	}
	pac, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/x-ns-proxy-autoconfig" ||
		!strings.Contains(string(pac), `return "PROXY 192.168.1.5:9277; DIRECT";`) || !strings.Contains(string(pac), "FindProxyForURL") {
		t.Fatalf("PAC %s:\n%s", resp.Header.Get("Content-Type"), pac)
	}
	resp, err = http.Get("http://" + at + CertPath)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(der, tp.ca.CertDER()) {
		t.Fatal("no certificate")
	}
	u, _ := url.Parse("http://" + at)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	resp, err = c.Get(upstream.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("proxied through the setup listener: %d", resp.StatusCode)
	}
	if _, err := c.Get("https://example.test/"); err == nil {
		t.Fatal("CONNECT through the setup listener")
	}
	if n, _ := tp.store.Len(); n != 0 {
		t.Fatalf("kept %d requests", n)
	}
	// The proxy's own address serves a PAC file naming itself.
	addr, err := tp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get("http://" + addr + PACPath)
	if err != nil {
		t.Fatal(err)
	}
	pac, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(pac), "PROXY "+addr+"; DIRECT") {
		t.Fatalf("PAC at the proxy:\n%s", pac)
	}
}

func TestLoadCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	a, err := LoadCA(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, CAKeyFile))
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) || a.KeyPlace != KeyInFile {
		t.Fatalf("key file: %v %v %s", st.Mode(), err, a.KeyPlace)
	}
	if life := time.Until(a.Expires()); life < 364*24*time.Hour || life > 366*24*time.Hour {
		t.Errorf("lasts %v, want a year", life)
	}
	b, err := LoadCA(dir, nil)
	if err != nil || !bytes.Equal(a.CertDER(), b.CertDER()) || b.Renewed {
		t.Fatalf("a second load made another authority: %v", err)
	}
	if !strings.HasPrefix(a.Name(), "Pinky Traffic CA ") || len(a.Fingerprint()) != 95 {
		t.Errorf("name %q fingerprint %q", a.Name(), a.Fingerprint())
	}
	leaf, err := a.Leaf("Api.Example.test.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "api.example.test", Roots: a.Pool()}); err != nil {
		t.Errorf("leaf: %v", err)
	}
	if leaf.Leaf.NotAfter.After(a.Expires()) {
		t.Error("a leaf outlives its CA")
	}
	if ip, _ := a.Leaf("10.0.0.2"); len(ip.Leaf.IPAddresses) != 1 {
		t.Error("an address's certificate has no IP")
	}
	os.WriteFile(filepath.Join(dir, CAKeyFile), []byte("junk"), 0o600)
	if c, err := LoadCA(dir, nil); err != nil || bytes.Equal(c.CertDER(), a.CertDER()) || !c.Renewed {
		t.Errorf("a broken key: %v", err)
	}
}

// An earlier Pinky's key file moves into the keychain, keeping the CA the
// phones trust; later loads read it from there.
func TestLoadCAMovesTheKeyIntoTheKeychain(t *testing.T) {
	dir := t.TempDir()
	old, err := LoadCA(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	keys := &secret.Memory{}
	ca, err := LoadCA(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ca.CertDER(), old.CertDER()) || ca.KeyPlace != KeyInKeychain || ca.Renewed {
		t.Fatalf("migrated: %s renewed %v", ca.KeyPlace, ca.Renewed)
	}
	if _, err := os.Stat(filepath.Join(dir, CAKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the key file stayed: %v", err)
	}
	k, _ := keys.Get(context.Background(), secret.TrafficCAKeyAccount)
	if blk, _ := pem.Decode([]byte(k)); blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("keychain holds %q", k)
	}
	again, err := LoadCA(dir, keys)
	if err != nil || !bytes.Equal(again.CertDER(), old.CertDER()) {
		t.Fatalf("from the keychain: %v", err)
	}
	// A new install keeps no key file at all.
	dir2 := t.TempDir()
	if _, err := LoadCA(dir2, &secret.Memory{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir2, CAKeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a key file next to the keychain: %v", err)
	}
}

// A CA within RenewBefore of its end is replaced, and says so.
func TestLoadCARenews(t *testing.T) {
	dir := t.TempDir()
	short, err := newCAFor(RenewBefore - 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(short.key)
	writeFile(filepath.Join(dir, CAKeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	writeFile(filepath.Join(dir, CACertFile), short.CertPEM(), 0o600)
	if !short.NeedsRenewal(time.Now()) {
		t.Fatal("NeedsRenewal is false")
	}
	ca, err := LoadCA(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ca.CertDER(), short.CertDER()) || !ca.Renewed || ca.NeedsRenewal(time.Now()) {
		t.Fatalf("not renewed: renewed %v", ca.Renewed)
	}
}

// Blocked connections to one host that follow each other closely are one
// row with a count and the span they took; another host, another phone,
// a gap past BlockedGap or a new task starts a new row.
func TestStoreCoalescesBlockedRows(t *testing.T) {
	s := NewStore(0, 0)
	t0 := time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC)
	blocked := func(dev, host string, at time.Time) string {
		return s.Add(Request{Device: dev, Start: at, End: at, Method: http.MethodConnect, URL: "https://" + host, Host: host,
			Blocked: true, Note: BlockedNote}, Bodies{})
	}
	first := blocked("pixel", "collect.alipay.com", t0)
	for i := 1; i < 70; i++ {
		if id := blocked("pixel", "collect.alipay.com", t0.Add(time.Duration(i)*3*time.Second)); id != first {
			t.Fatalf("connection %d got its own row %s", i, id)
		}
	}
	// A decrypted request in between does not split the run.
	s.Add(Request{Device: "pixel", Start: t0.Add(10 * time.Second), Method: http.MethodGet, URL: "https://example.com/", Host: "example.com", Decrypted: true}, Bodies{})
	other := blocked("pixel", "www.alipay.com", t0.Add(time.Second))
	ipad := blocked("ipad", "collect.alipay.com", t0.Add(time.Second))
	// One that comes in a little out of order joins too and moves Start.
	if id := blocked("pixel", "collect.alipay.com", t0.Add(-time.Second)); id != first {
		t.Fatalf("an earlier connection got its own row %s", id)
	}
	if other == first || ipad == first {
		t.Fatal("another host or phone joined the row")
	}
	last := t0.Add(69 * 3 * time.Second)
	rs := s.Requests(Query{Device: "pixel"})
	if len(rs) != 3 {
		t.Fatalf("%d rows: %+v", len(rs), rs)
	}
	r := rs[0]
	if r.ID != first || r.Connections() != 71 || !r.Start.Equal(t0.Add(-time.Second)) || !r.Latest().Equal(last) || !r.End.Equal(last) {
		t.Fatalf("row %+v", r)
	}
	if r.URL != "https://collect.alipay.com" || len(r.Headers) != 0 || r.Sent != 0 || r.Received != 0 || r.Note != BlockedNote {
		t.Fatalf("the row holds more than the host: %+v", r)
	}
	// It matches a window that holds only its later connections.
	if got := s.Requests(Query{Device: "pixel", From: last.Add(-time.Second), To: last.Add(time.Second)}); len(got) != 1 || got[0].ID != first {
		t.Fatalf("window after its start: %+v", got)
	}
	if got := s.Requests(Query{Device: "pixel", From: last.Add(time.Second)}); len(got) != 0 {
		t.Fatalf("window after its end: %+v", got)
	}
	// After a quiet gap past BlockedGap, a new row.
	if id := blocked("pixel", "collect.alipay.com", last.Add(BlockedGap+time.Second)); id == first {
		t.Fatal("a connection after a long gap joined the old row")
	}
	// A new task (the phone's requests dropped) starts afresh.
	gen := s.Gen("pixel")
	s.Drop("pixel")
	if _, ok := s.AddSince(gen, Request{Device: "pixel", Start: last, Blocked: true, URL: "https://collect.alipay.com"}, Bodies{}); ok {
		t.Fatal("kept a blocked connection from before the drop")
	}
	if id := blocked("pixel", "collect.alipay.com", last); id == first {
		t.Fatal("a new task's connection joined the old task's row")
	}
}

// A blocked tunnel shows as soon as it opens, before it closes: a page
// can keep one open for minutes.
func TestProxyKeepsBlockedTunnelWhenItOpens(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer upstream.Close()
	tp := newTestProxy(t, upstream)
	c, err := net.Dial("tcp", tp.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "CONNECT www.paypal.com:443 HTTP/1.1\r\nHost: www.paypal.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT: %v %v", resp, err)
	}
	// The tunnel stays open: the phone has sent nothing yet.
	waitFor(t, "the open tunnel", func() bool { return len(tp.store.Requests(Query{Device: "pixel"})) == 1 })
	r := tp.store.Requests(Query{Device: "pixel"})[0]
	if !r.Blocked || r.Host != "www.paypal.com" || r.End.IsZero() {
		t.Fatalf("open tunnel %+v", r)
	}
}
