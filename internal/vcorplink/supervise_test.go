package vcorplink

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"golang.org/x/net/websocket"
)

// fakePlatform speaks the platform's side of the socket: a challenge, a
// signed hello checked against the paired key, welcome.
type fakePlatform struct {
	t      *testing.T
	origin string
	keys   map[string]map[string]string // deviceId -> public JWK

	mu     sync.Mutex
	conns  map[*websocket.Conn]string // live, signed-in connections -> deviceId
	hellos int
	phones int
	revoke bool
}

func (p *fakePlatform) handle(ws *websocket.Conn) {
	nonce := "n" + time.Now().Format("150405.000000000")
	websocket.JSON.Send(ws, map[string]any{"type": "challenge", "nonce": nonce, "v": 1})
	var m struct {
		Type, DeviceID, Sig string
		Devices             []map[string]any
	}
	if websocket.JSON.Receive(ws, &m) != nil || m.Type != "hello" || !p.verify(m.DeviceID, nonce, m.Sig) {
		ws.Close()
		return
	}
	p.mu.Lock()
	p.hellos++
	p.phones = len(m.Devices)
	p.conns[ws] = m.DeviceID
	revoke := p.revoke
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.conns, ws)
		p.mu.Unlock()
	}()
	websocket.JSON.Send(ws, map[string]any{"type": "welcome", "account": "tester"})
	if revoke {
		websocket.JSON.Send(ws, map[string]any{"type": "revoked"})
		ws.Close()
		return
	}
	for {
		var x map[string]any
		if websocket.JSON.Receive(ws, &x) != nil {
			return
		}
	}
}

func (p *fakePlatform) verify(id, nonce, sig string) bool {
	jwk := p.keys[id]
	if jwk == nil {
		return false
	}
	dec := func(s string) *big.Int {
		b, _ := base64.RawURLEncoding.DecodeString(s)
		return new(big.Int).SetBytes(b)
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: dec(jwk["x"]), Y: dec(jwk["y"])}
	raw, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || len(raw) != 64 {
		return false
	}
	h := sha256.Sum256([]byte(helloText(p.origin, id, nonce)))
	return ecdsa.Verify(pub, h[:], new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}

func (p *fakePlatform) live() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting: %s", what)
}

func TestSupervise(t *testing.T) {
	defer func(d time.Duration) { superviseEvery = d }(superviseEvery)
	superviseEvery = 50 * time.Millisecond

	p := &fakePlatform{t: t, keys: map[string]map[string]string{}, conns: map[*websocket.Conn]string{}}
	srv := httptest.NewServer(websocket.Server{Handler: p.handle})
	defer srv.Close()
	p.origin = srv.URL

	providers, _ := demo.World()
	g, err := gateway.New(gateway.Options{Providers: providers, Policy: policy.Default(), TrustNewAgents: true})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	host := &GatewayHost{G: g, MCP: mcp.New(g, "test")}

	file := filepath.Join(t.TempDir(), "vcorp.json")
	pair := func(id string) {
		key, err := NewKey()
		if err != nil {
			t.Fatal(err)
		}
		jwk, _ := PublicJWK(key)
		p.mu.Lock()
		p.keys[id] = jwk
		p.mu.Unlock()
		if err := Save(file, &Config{Origin: srv.URL, DeviceID: id, Account: "tester", Key: key, Name: "test"}); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, file, func(c *Config) *Link { return &Link{Cfg: c, Host: host, Version: "test", Platform: "test"} }, nil)
	}()

	time.Sleep(200 * time.Millisecond)
	if p.live() != 0 {
		t.Fatal("connected without a pairing")
	}

	pair("pk_one")
	waitUntil(t, "connects once paired", func() bool { return p.live() == 1 })
	p.mu.Lock()
	if p.phones == 0 {
		t.Error("the hello reported no phones")
	}
	p.mu.Unlock()

	os.Remove(file)
	waitUntil(t, "disconnects once unpaired", func() bool { return p.live() == 0 })

	pair("pk_two")
	waitUntil(t, "connects with the new pairing", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, id := range p.conns {
			if id == "pk_two" {
				return true
			}
		}
		return false
	})

	// Paired anew while linked: the old connection goes, the new one comes.
	p.mu.Lock()
	p.revoke = true
	p.mu.Unlock()
	pair("pk_three")
	waitUntil(t, "a revoked pairing is forgotten", func() bool {
		_, err := os.Stat(file)
		return os.IsNotExist(err)
	})
	p.mu.Lock()
	hellos := p.hellos
	p.mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	p.mu.Lock()
	if p.hellos != hellos {
		t.Errorf("connected again after the platform removed it (%d hellos, then %d)", hellos, p.hellos)
	}
	p.mu.Unlock()

	cancel()
	<-done
}
