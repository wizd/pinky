package webapproval

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/webmirror"
)

const (
	host  = "mac.tail1234.ts.net:8443"
	owner = "person@example.com"
	// iPhone Safari's User-Agent.
	safariUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
)

// newGateway is the real gateway on a demo phone, as the gateway's own
// tests make it.
func newGateway(t *testing.T, timeout time.Duration) *gateway.Gateway {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pol := policy.Default()
	if err := pol.SetPreset(policy.PresetCommits); err != nil {
		t.Fatal(err)
	}
	g, err := gateway.New(gateway.Options{
		Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)}, Policy: pol, TrustNewAgents: true,
		ApprovalTimeout: timeout, MaxBlock: 200 * time.Millisecond, SettleMax: 200 * time.Millisecond,
		PollInterval: 5 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond, NoScreenshots: true, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

// ask has an agent ask the person a question and returns its approval id
// and a channel with the agent's answer.
func ask(t *testing.T, g *gateway.Gateway) (string, <-chan string) {
	t.Helper()
	ctx := context.Background()
	s := g.OpenSession(gateway.AgentInfo{Name: "Claude Code", Client: "test"})
	t.Cleanup(s.Close)
	if res := s.Connect(ctx, gateway.ConnectRequest{Task: "Lunch"}); res.IsError() {
		t.Fatalf("connect: %+v", res)
	}
	res := s.RequestApproval(ctx, gateway.ApprovalRequest{Summary: "May I order lunch?"})
	if res.Status != gateway.StatusPending {
		t.Fatalf("request: %+v", res)
	}
	out := make(chan string, 1)
	go func() {
		for {
			r := s.RequestApproval(ctx, gateway.ApprovalRequest{Summary: "May I order lunch?", ApprovalID: res.ApprovalID})
			if r.Status != gateway.StatusPending {
				out <- r.Decision
				return
			}
		}
	}()
	return res.ApprovalID, out
}

// harness is the page as the web mirror serves it, through a fake
// `tailscale serve`, and a phone with a software passkey.
type harness struct {
	t   *testing.T
	s   *Server
	srv *httptest.Server
	// What the requests carry, as `tailscale serve` would set them.
	login   string
	proxied bool
	host    string // the Host requests name, and the origin the browser puts in the client data
	ua      string
	key     *ecdsa.PrivateKey
	credID  []byte
	session string
	uv      bool
	counter uint32 // the passkey's sign counter
	device  string // the phone the person says the browser runs on
	// tsOrigin is where `tailscale serve` gives the mirror.
	tsOrigin string
}

func newHarness(t *testing.T, src Source) *harness {
	h := &harness{t: t, login: owner, proxied: true, uv: true, credID: []byte("cred-1"), host: host, ua: safariUA,
		tsOrigin: "https://" + host}
	h.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.s = New(Options{Source: src, Owner: func(context.Context) string { return owner },
		Origin: func(context.Context) string { return h.tsOrigin }, Proxied: webmirror.Proxied})
	h.s.Set(true)
	m := webmirror.New(webmirror.Options{Source: nil, OwnNames: func(context.Context) []string { return []string{"mac.tail1234.ts.net"} },
		Approvals: h.s.Handler()})
	h.srv = httptest.NewServer(m.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) post(path string, body any, out any) int {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", h.srv.URL+Path+"/api/"+path, bytes.NewReader(b))
	h.headers(req)
	req.Header.Set("Origin", "https://"+h.host)
	if h.session != "" {
		req.Header.Set("Authorization", "Bearer "+h.session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		json.Unmarshal(data, out)
	}
	return resp.StatusCode
}

func (h *harness) headers(req *http.Request) {
	req.Host = h.host
	req.Header.Set("User-Agent", h.ua)
	if h.proxied {
		req.Header.Set("X-Forwarded-For", "100.64.0.2")
	}
	if h.login != "" {
		req.Header.Set("Tailscale-User-Login", h.login)
	}
}

func (h *harness) clientData(typ string, ch []byte) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": b64.EncodeToString(ch), "origin": "https://" + h.host})
	return b
}

func (h *harness) authData() []byte {
	rp := sha256.Sum256([]byte("mac.tail1234.ts.net"))
	flags := byte(flagUserPresent)
	if h.uv {
		flags |= flagUserVerified
	}
	return binary.BigEndian.AppendUint32(append(rp[:], flags), h.counter)
}

// pair adds the harness's phone, the person allowing it in Pinky's window.
func (h *harness) pair() {
	h.t.Helper()
	var begun pairBeginResp
	if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun); c != 200 {
		h.t.Fatalf("begin: %d", c)
	}
	var fin pairFinishResp
	if c := h.post("pair/finish", h.finishReq(begun), &fin); c != 200 || len(fin.Code) != 6 {
		h.t.Fatalf("finish: %d %+v", c, fin)
	}
	reqs := h.s.Requests()
	if len(reqs) != 1 || reqs[0].Code != fin.Code || reqs[0].Login != owner || reqs[0].Browser != "iPhone · Safari" ||
		reqs[0].ID == begun.ID || fin.Poll == "" {
		h.t.Fatalf("requests %+v, code %s", reqs, fin.Code)
	}
	var st pairStatusResp
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "waiting" {
		h.t.Fatalf("status before Allow: %+v", st)
	}
	if err := h.s.Allow(reqs[0].ID, h.device); err != nil {
		h.t.Fatal(err)
	}
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "allowed" || st.Session == "" {
		h.t.Fatalf("status after Allow: %+v", st)
	}
	h.session = st.Session
	// Once.
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "gone" {
		h.t.Fatalf("status again: %+v", st)
	}
}

func (h *harness) finishReq(begun pairBeginResp) pairFinishReq {
	ch, _ := b64.DecodeString(begun.Challenge)
	spki, _ := x509.MarshalPKIXPublicKey(&h.key.PublicKey)
	return pairFinishReq{ID: begun.ID, CredentialID: b64.EncodeToString(h.credID), PublicKey: b64.EncodeToString(spki),
		Alg: algES256, ClientData: b64.EncodeToString(h.clientData("webauthn.create", ch)),
		AuthData: b64.EncodeToString(h.authData())}
}

// sign makes the assertion the phone's passkey would for a challenge.
func (h *harness) sign(ch challengeResp) decideReq {
	v, _ := b64.DecodeString(ch.Challenge)
	cd := h.clientData("webauthn.get", v)
	ad := h.authData()
	sum := sha256.Sum256(cd)
	d := sha256.Sum256(append(append([]byte(nil), ad...), sum[:]...))
	sig, _ := ecdsa.SignASN1(rand.Reader, h.key, d[:])
	return decideReq{Challenge: ch.Challenge, CredentialID: b64.EncodeToString(h.credID), ClientData: b64.EncodeToString(cd),
		AuthData: b64.EncodeToString(ad), Signature: b64.EncodeToString(sig)}
}

func (h *harness) challenge(id, d string) challengeResp {
	h.t.Helper()
	var ch challengeResp
	if c := h.post("challenge", challengeReq{ID: id, Decision: d}, &ch); c != 200 {
		h.t.Fatalf("challenge: %d", c)
	}
	return ch
}

func TestAnswerFromThePhone(t *testing.T) {
	for _, tc := range []struct{ decision, want string }{{"allow", "allow"}, {"deny", "deny"}} {
		t.Run(tc.decision, func(t *testing.T) {
			g := newGateway(t, 30*time.Second)
			h := newHarness(t, g)
			h.pair()
			id, answer := ask(t, g)

			var needs needsResp
			if c := h.post("needs", nil, &needs); c != 200 || len(needs.Needs) != 1 {
				t.Fatalf("needs: %d %+v", c, needs)
			}
			n := needs.Needs[0]
			if n.ID != id || !n.Answerable || n.Title == "" || n.Agent != "Claude Code" || n.Phone == "" || n.Deadline <= needs.Now {
				t.Fatalf("card %+v", n)
			}
			var res decideResp
			if c := h.post("decide", h.sign(h.challenge(id, tc.decision)), &res); c != 200 || res.Result != "done" {
				t.Fatalf("decide: %d %+v", c, res)
			}
			select {
			case got := <-answer:
				if got != tc.want {
					t.Fatalf("agent heard %q", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the agent heard nothing")
			}
		})
	}
}

func TestRefused(t *testing.T) {
	g := newGateway(t, 30*time.Second)

	t.Run("not through Tailscale", func(t *testing.T) {
		h := newHarness(t, g)
		h.proxied, h.login, h.host = false, "", "127.0.0.1:7466"
		if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, nil); c != http.StatusForbidden {
			t.Fatalf("got %d", c)
		}
		req, _ := http.NewRequest("GET", h.srv.URL+Path, nil)
		h.headers(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("page: %d", resp.StatusCode)
		}
	})
	t.Run("another tailnet user", func(t *testing.T) {
		h := newHarness(t, g)
		h.login = "someone@example.com"
		if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, nil); c != http.StatusForbidden {
			t.Fatalf("got %d", c)
		}
	})
	t.Run("off", func(t *testing.T) {
		h := newHarness(t, g)
		link := h.s.NewLink()
		h.s.Set(false)
		if c := h.post("pair/begin", pairBeginReq{Link: link}, nil); c != http.StatusNotFound {
			t.Fatalf("got %d", c)
		}
	})
	t.Run("Funnel", func(t *testing.T) {
		h := newHarness(t, g)
		req, _ := http.NewRequest("GET", h.srv.URL+Path, nil)
		h.headers(req)
		req.Header.Set("Tailscale-Funnel-Request", "?1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("got %d", resp.StatusCode)
		}
	})
	t.Run("link used twice or made up", func(t *testing.T) {
		h := newHarness(t, g)
		link := h.s.NewLink()
		if c := h.post("pair/begin", pairBeginReq{Link: link}, nil); c != 200 {
			t.Fatalf("first: %d", c)
		}
		if c := h.post("pair/begin", pairBeginReq{Link: link}, nil); c != http.StatusForbidden {
			t.Fatalf("again: %d", c)
		}
		if c := h.post("pair/begin", pairBeginReq{Link: "AAAA"}, nil); c != http.StatusForbidden {
			t.Fatalf("made up: %d", c)
		}
	})
	t.Run("passkey without user verification", func(t *testing.T) {
		h := newHarness(t, g)
		h.uv = false
		var begun pairBeginResp
		h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
		if c := h.post("pair/finish", h.finishReq(begun), nil); c != http.StatusForbidden || len(h.s.Requests()) != 0 {
			t.Fatalf("got %d, requests %+v", c, h.s.Requests())
		}
	})
	t.Run("turned down in Pinky's window", func(t *testing.T) {
		h := newHarness(t, g)
		var begun pairBeginResp
		h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
		var fin pairFinishResp
		h.post("pair/finish", h.finishReq(begun), &fin)
		h.s.Deny(h.s.Requests()[0].ID)
		var st pairStatusResp
		if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "denied" || st.Session != "" || len(h.s.Phones()) != 0 {
			t.Fatalf("status %+v, phones %+v", st, h.s.Phones())
		}
	})
	t.Run("no session", func(t *testing.T) {
		h := newHarness(t, g)
		if c := h.post("needs", nil, nil); c != http.StatusUnauthorized {
			t.Fatalf("got %d", c)
		}
		h.session = "AAAAAAAAAAAAAAAAAAAAAAAAAA"
		if c := h.post("needs", nil, nil); c != http.StatusUnauthorized {
			t.Fatalf("made up: %d", c)
		}
	})
	t.Run("another origin", func(t *testing.T) {
		h := newHarness(t, g)
		b, _ := json.Marshal(pairBeginReq{Link: h.s.NewLink()})
		req, _ := http.NewRequest("POST", h.srv.URL+Path+"/api/pair/begin", bytes.NewReader(b))
		h.headers(req)
		req.Header.Set("Origin", "https://evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("got %d", resp.StatusCode)
		}
	})
}

func TestAnswersThatDoNotCount(t *testing.T) {
	g := newGateway(t, 30*time.Second)
	h := newHarness(t, g)
	h.pair()
	id, answer := ask(t, g)

	// Without user verification: a tap on the page by whoever drives the
	// phone, without Face ID.
	h.uv = false
	if c := h.post("decide", h.sign(h.challenge(id, "allow")), nil); c != http.StatusForbidden {
		t.Fatalf("no UV: %d", c)
	}
	h.uv = true

	// Another key: a passkey the person never added.
	ch := h.challenge(id, "allow")
	good := h.key
	h.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if c := h.post("decide", h.sign(ch), nil); c != http.StatusForbidden {
		t.Fatalf("other key: %d", c)
	}
	h.key = good
	// That challenge went with the failed try.
	if c := h.post("decide", h.sign(ch), nil); c != http.StatusForbidden {
		t.Fatalf("used challenge: %d", c)
	}

	// Another phone's session cannot use this phone's challenge.
	other := newHarness(t, g)
	other.s = h.s
	other.srv = h.srv
	other.credID = []byte("cred-2")
	other.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	other.pair()
	ch = h.challenge(id, "allow")
	if c := other.post("decide", other.sign(ch), nil); c != http.StatusForbidden {
		t.Fatalf("other phone: %d", c)
	}

	// A challenge for Deny answers Deny, whatever the page says.
	var res decideResp
	if c := h.post("decide", h.sign(h.challenge(id, "deny")), &res); c != 200 || res.Result != "done" {
		t.Fatalf("decide: %d %+v", c, res)
	}
	if got := <-answer; got != "deny" {
		t.Fatalf("agent heard %q", got)
	}

	// Removed in Settings: the session no longer works.
	h.s.Remove(h.s.Phones()[0].ID)
	if c := h.post("needs", nil, nil); c != http.StatusUnauthorized {
		t.Fatalf("after Remove: %d", c)
	}
}

func TestTwoPlacesAnswer(t *testing.T) {
	g := newGateway(t, 30*time.Second)
	h := newHarness(t, g)
	h.pair()
	id, answer := ask(t, g)
	ch := h.challenge(id, "allow")
	// The person answers in Pinky's window first.
	if err := g.Decide(id, gateway.Deny, ""); err != nil {
		t.Fatal(err)
	}
	var res decideResp
	if c := h.post("decide", h.sign(ch), &res); c != 200 || res.Result != "gone" || res.Late {
		t.Fatalf("decide: %d %+v", c, res)
	}
	if got := <-answer; got != "deny" {
		t.Fatalf("agent heard %q", got)
	}
}

func TestTimeoutUnchanged(t *testing.T) {
	g := newGateway(t, time.Second)
	h := newHarness(t, g)
	h.pair()
	id, answer := ask(t, g)
	ch := h.challenge(id, "allow")
	if ch.Expires > time.Now().Add(time.Second).UnixMilli()+50 {
		t.Fatalf("challenge outlives the approval: %d", ch.Expires)
	}
	if got := <-answer; got != "deny" {
		t.Fatalf("agent heard %q", got)
	}
	var res decideResp
	if c := h.post("decide", h.sign(ch), &res); c != 200 || res.Result != "gone" || !res.Late {
		t.Fatalf("decide: %d %+v", c, res)
	}
}

func TestAnswerable(t *testing.T) {
	for name, n := range map[string]gateway.NeedView{
		"action":  {Kind: gateway.NeedApproval, Approval: approval.KindAction, DeviceID: "pixel", Rule: "Payments"},
		"request": {Kind: gateway.NeedApproval, Approval: approval.KindRequest, DeviceID: "pixel", Rule: "Agent request"},
	} {
		if !Answerable(n) {
			t.Errorf("%s not answerable", name)
		}
	}
	for name, n := range map[string]gateway.NeedView{
		// By its kind, whatever its rule says.
		"new agent":         {Kind: gateway.NeedApproval, Approval: approval.KindAgent, DeviceID: "pixel", Rule: "Payments"},
		"no kind":           {Kind: gateway.NeedApproval, DeviceID: "pixel", Rule: "Payments"},
		"unblock":           {Kind: gateway.NeedApproval, Approval: approval.KindAction, DeviceID: "pixel", Rule: "Finance apps", Unblock: true},
		"no phone":          {Kind: gateway.NeedApproval, Approval: approval.KindAction, Rule: "Payments"},
		"handoff":           {Kind: gateway.NeedHandoff, Approval: approval.KindHandoff, DeviceID: "pixel"},
		"handoff as action": {Kind: gateway.NeedHandoff, Approval: approval.KindAction, DeviceID: "pixel"},
		"unlock":            {Kind: gateway.NeedUnlock, DeviceID: "pixel"},
		"setup":             {Kind: gateway.NeedSetup, DeviceID: "pixel"},
	} {
		if Answerable(n) {
			t.Errorf("%s answerable", name)
		}
	}
}

// fakeSource holds cards and phones; answering is counted, or panics
// when no answer may come.
type fakeSource struct {
	mu       sync.Mutex
	needs    []gateway.NeedView
	devices  []gateway.DeviceView
	answers  []string
	noAnswer bool
}

func (f *fakeSource) Needs() []gateway.NeedView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gateway.NeedView(nil), f.needs...)
}

func (f *fakeSource) Devices() []gateway.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gateway.DeviceView(nil), f.devices...)
}

func (f *fakeSource) Decide(id string, d gateway.Decision, _ string) error {
	if f.noAnswer {
		panic("the page answered what it may not")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers = append(f.answers, id+":"+string(d))
	return nil
}

func (f *fakeSource) setTask(device, task string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devices {
		if f.devices[i].Info.ID == device {
			// As the gateway: a task starting or ending changes LeaseGen.
			if was := f.devices[i].TaskID; was != task {
				f.devices[i].LeaseGen++
				if task == "" {
					f.devices[i].LeaseEnded = time.Now()
				}
			}
			f.devices[i].TaskID = task
		}
	}
}

// endedAgo moves the end of a phone's last task back.
func (f *fakeSource) endedAgo(device string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.devices {
		if f.devices[i].Info.ID == device {
			f.devices[i].LeaseEnded = time.Now().Add(-d)
		}
	}
}

func payment(id string) gateway.NeedView {
	return gateway.NeedView{ID: id, Kind: gateway.NeedApproval, Approval: approval.KindAction, DeviceID: "pixel", Rule: "Payments",
		Title: "Pay $12.80?", Deadline: time.Now().Add(time.Minute), Screenshot: image.NewRGBA(image.Rect(0, 0, 90, 160)),
		Target: screen.Rect{X: 10, Y: 20, W: 30, H: 40}, Point: screen.Point{X: 25, Y: 40}}
}

func (h *harness) shot(id string) int {
	h.t.Helper()
	b, _ := json.Marshal(idReq{ID: id})
	req, _ := http.NewRequest("POST", h.srv.URL+Path+"/api/shot", bytes.NewReader(b))
	h.headers(req)
	req.Header.Set("Origin", "https://"+h.host)
	req.Header.Set("Authorization", "Bearer "+h.session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 && (resp.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store")) {
		h.t.Fatalf("shot: %v", resp.Header)
	}
	return resp.StatusCode
}

// Cards the page may not answer are listed without their screenshot, and
// refused.
func TestCardsAndComputerOnly(t *testing.T) {
	newAgent := payment("ap_1")
	newAgent.Approval, newAgent.Rule, newAgent.Title = approval.KindAgent, "New agent", "Let Codex use your phones?"
	unblock := payment("ap_2")
	unblock.Rule, unblock.Unblock, unblock.Title = "Finance apps", true, "Open Bank?"
	src := &fakeSource{noAnswer: true, needs: []gateway.NeedView{newAgent, unblock, payment("ap_3"),
		{ID: "unlock:pixel", Kind: gateway.NeedUnlock, DeviceID: "pixel"}}}
	h := newHarness(t, src)
	h.pair()
	var needs needsResp
	h.post("needs", nil, &needs)
	if len(needs.Needs) != 3 || needs.Needs[0].Answerable || needs.Needs[1].Answerable || !needs.Needs[2].Answerable {
		t.Fatalf("needs %+v", needs.Needs)
	}
	if needs.Needs[0].Shot != nil || needs.Needs[1].Shot != nil || needs.Needs[1].Target != nil {
		t.Fatalf("a screenshot of what the page may not answer: %+v", needs.Needs[:2])
	}
	n := needs.Needs[2]
	if n.Shot == nil || n.Shot.W != 90 || n.Target == nil || n.Target.W != 30 || len(n.Points) != 1 {
		t.Fatalf("card %+v", n)
	}
	for _, id := range []string{"ap_1", "ap_2"} {
		var e apiErr
		if c := h.post("challenge", challengeReq{ID: id, Decision: "allow"}, &e); c != http.StatusForbidden || e.Error != "computer" {
			t.Fatalf("%s: %d %+v", id, c, e)
		}
		if c := h.shot(id); c != http.StatusNotFound {
			t.Fatalf("shot of %s: %d", id, c)
		}
	}
	if c := h.shot("ap_3"); c != 200 {
		t.Fatalf("shot: %d", c)
	}
}

// A browser on a phone an agent uses answers nothing, and sees nothing,
// while the agent uses it and for leaseCooldown after: whoever drives the
// phone could tap Allow while the person glances at it, then let go.
func TestBusyPhone(t *testing.T) {
	t.Run("gateway", func(t *testing.T) {
		g := newGateway(t, 30*time.Second)
		h := newHarness(t, g)
		h.device = "pixel"
		h.pair()
		id, _ := ask(t, g) // the agent holds the Pixel
		var needs needsResp
		if h.post("needs", nil, &needs); !needs.Busy || len(needs.Needs) != 0 {
			t.Fatalf("needs %+v", needs)
		}
		var e apiErr
		if c := h.post("challenge", challengeReq{ID: id, Decision: "allow"}, &e); c != http.StatusForbidden || e.Error != "busy" {
			t.Fatalf("challenge: %d %+v", c, e)
		}
		// Another device answers.
		other := newHarness(t, g)
		other.s, other.srv, other.credID = h.s, h.srv, []byte("cred-2")
		other.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		other.pair()
		needs = needsResp{}
		if other.post("needs", nil, &needs); needs.Busy || len(needs.Needs) != 1 || !needs.Needs[0].Answerable {
			t.Fatalf("other device's needs %+v", needs)
		}
		var res decideResp
		if c := other.post("decide", other.sign(other.challenge(id, "deny")), &res); c != 200 || res.Result != "done" {
			t.Fatalf("other device: %d %+v", c, res)
		}
	})
	t.Run("the gateway counts leases", func(t *testing.T) {
		g := newGateway(t, 30*time.Second)
		pixel := func() gateway.DeviceView {
			for _, d := range g.Devices() {
				if d.Info.ID == "pixel" {
					return d
				}
			}
			t.Fatal("no pixel")
			return gateway.DeviceView{}
		}
		deadline := time.Now().Add(5 * time.Second)
		for pixel().Info.State != driver.StateOnline && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		before := pixel()
		s := g.OpenSession(gateway.AgentInfo{Name: "Claude Code", Client: "test"})
		if res := s.Connect(context.Background(), gateway.ConnectRequest{Task: "Lunch"}); res.IsError() {
			t.Fatalf("connect: %+v", res)
		}
		during := pixel()
		s.Close()
		after := pixel()
		if during.TaskID == "" || during.LeaseGen == before.LeaseGen || after.TaskID != "" || after.LeaseGen == during.LeaseGen ||
			after.LeaseEnded.IsZero() || time.Since(after.LeaseEnded) > time.Minute {
			t.Fatalf("before %+v, during %+v, after %+v", before, during, after)
		}
		// A browser on the Pixel answers nothing right after.
		h := newHarness(t, g)
		h.device = "pixel"
		h.pair()
		var needs needsResp
		if h.post("needs", nil, &needs); !needs.Busy {
			t.Fatalf("needs right after the task %+v", needs)
		}
	})

	phones := func() *fakeSource {
		return &fakeSource{needs: []gateway.NeedView{payment("ap_1")},
			devices: []gateway.DeviceView{{Info: driver.Info{ID: "pixel"}}, {Info: driver.Info{ID: "iphone"}}}}
	}
	pairOn := func(t *testing.T, src *fakeSource, device string) *harness {
		h := newHarness(t, src)
		h.device = device
		h.pair()
		if ph := h.s.Phones(); len(ph) != 1 || ph[0].Device != device {
			t.Fatalf("phones %+v", ph)
		}
		return h
	}
	refused := func(t *testing.T, h *harness, src *fakeSource, ch challengeResp) {
		t.Helper()
		var e apiErr
		if c := h.post("decide", h.sign(ch), &e); c != http.StatusForbidden || e.Error != "busy" || len(src.answers) != 0 {
			t.Fatalf("decide: %d %+v, answers %v", c, e, src.answers)
		}
	}
	t.Run("an agent starts after the tap", func(t *testing.T) {
		src := phones()
		h := pairOn(t, src, "iphone")
		ch := h.challenge("ap_1", "allow")
		src.setTask("iphone", "task-1")
		refused(t, h, src, ch)
		var needs needsResp
		if h.post("needs", nil, &needs); !needs.Busy || len(needs.Needs) != 0 {
			t.Fatalf("needs %+v", needs)
		}
		if c := h.shot("ap_1"); c != http.StatusForbidden {
			t.Fatalf("shot while busy: %d", c)
		}
	})
	t.Run("the task ends between the tap and the answer", func(t *testing.T) {
		// The agent taps Allow, the passkey sheet passes Face ID while the
		// person looks, and the agent lets go of the phone at once.
		src := phones()
		h := pairOn(t, src, "iphone")
		ch := h.challenge("ap_1", "allow")
		src.setTask("iphone", "task-1")
		src.setTask("iphone", "")
		src.endedAgo("iphone", 2*leaseCooldown) // even long ago: a lease came and went since the tap
		refused(t, h, src, ch)
	})
	t.Run("cooldown", func(t *testing.T) {
		src := phones()
		h := pairOn(t, src, "iphone")
		src.setTask("iphone", "task-1")
		src.setTask("iphone", "")
		var e apiErr
		if c := h.post("challenge", challengeReq{ID: "ap_1", Decision: "allow"}, &e); c != http.StatusForbidden || e.Error != "busy" {
			t.Fatalf("challenge right after the task: %d %+v", c, e)
		}
		src.endedAgo("iphone", leaseCooldown+time.Second)
		var res decideResp
		if c := h.post("decide", h.sign(h.challenge("ap_1", "allow")), &res); c != 200 || res.Result != "done" || len(src.answers) != 1 {
			t.Fatalf("after the cooldown: %d %+v %v", c, res, src.answers)
		}
	})
	t.Run("an unknown phone", func(t *testing.T) {
		src := phones()
		h := newHarness(t, src)
		var begun pairBeginResp
		h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
		h.post("pair/finish", h.finishReq(begun), nil)
		if err := h.s.Allow(h.s.Requests()[0].ID, "made-up"); !errors.Is(err, ErrNoDevice) || len(h.s.Phones()) != 0 {
			t.Fatalf("allowed on a made-up phone: %v", err)
		}
		// A phone Pinky forgets afterwards is busy, not free.
		h = pairOn(t, src, "iphone")
		src.mu.Lock()
		src.devices = src.devices[:1]
		src.mu.Unlock()
		var e apiErr
		if c := h.post("challenge", challengeReq{ID: "ap_1", Decision: "allow"}, &e); c != http.StatusForbidden || e.Error != "busy" {
			t.Fatalf("challenge on a forgotten phone: %d %+v", c, e)
		}
	})
}

// Passkeys are bound to where `tailscale serve` gives the mirror, stored
// when the phone is added; never to what a request names.
func TestOriginFromTailscale(t *testing.T) {
	src := &fakeSource{needs: []gateway.NeedView{payment("ap_1")}}
	t.Run("not served", func(t *testing.T) {
		h := newHarness(t, src)
		h.tsOrigin = ""
		var e apiErr
		if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &e); c != http.StatusForbidden || e.Error != "tailnet" {
			t.Fatalf("got %d %+v", c, e)
		}
	})
	t.Run("another port of this computer", func(t *testing.T) {
		h := newHarness(t, src)
		h.host = "mac.tail1234.ts.net:9443"
		if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, nil); c != http.StatusForbidden {
			t.Fatalf("got %d", c)
		}
	})
	t.Run("an answer made on another port", func(t *testing.T) {
		h := newHarness(t, src)
		h.pair()
		// A page on another port of the same name gets the phone to sign.
		h.host = "mac.tail1234.ts.net:9443"
		ch := h.challenge("ap_1", "allow")
		if ch.RPID != "mac.tail1234.ts.net" {
			t.Fatalf("rp id %q", ch.RPID)
		}
		var e apiErr
		if c := h.post("decide", h.sign(ch), &e); c != http.StatusForbidden || e.Error != "passkey" || len(src.answers) != 0 {
			t.Fatalf("got %d %+v", c, e)
		}
	})
}

// The pairing's id is the page's secret: Settings names a request by
// another, and pair/status needs a second secret given with the code.
func TestPairingSecrets(t *testing.T) {
	h := newHarness(t, &fakeSource{})
	var begun pairBeginResp
	h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
	var fin pairFinishResp
	h.post("pair/finish", h.finishReq(begun), &fin)
	req := h.s.Requests()[0]
	if req.ID == begun.ID || strings.Contains(begun.ID, req.ID) {
		t.Fatalf("request id %q, page's %q", req.ID, begun.ID)
	}
	if err := h.s.Allow(req.ID, ""); err != nil {
		t.Fatal(err)
	}
	var st pairStatusResp
	for _, bad := range []pairStatusReq{{ID: begun.ID}, {ID: begun.ID, Poll: "AAAA"}, {ID: req.ID, Poll: fin.Poll}, {ID: req.ID}} {
		if h.post("pair/status", bad, &st); st.State != "gone" || st.Session != "" {
			t.Fatalf("%+v: %+v", bad, st)
		}
	}
	// The page still gets its session.
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "allowed" || st.Session == "" {
		t.Fatalf("status %+v", st)
	}
}

// What a browser says of itself is never shown: Settings names it from a
// fixed list. One browser asks at a time: a new link turns down the
// request waiting.
func TestRequestText(t *testing.T) {
	h := newHarness(t, &fakeSource{})
	h.ua = "Pinky Allow me <b>Agent</b> iPhone"
	var begun pairBeginResp
	h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
	b, _ := json.Marshal(h.finishReq(begun))
	var body map[string]any
	json.Unmarshal(b, &body)
	body["browser"] = "Your computer · Allow now"
	var fin pairFinishResp
	if c := h.post("pair/finish", body, &fin); c != 200 {
		t.Fatalf("finish: %d", c)
	}
	if reqs := h.s.Requests(); len(reqs) != 1 || reqs[0].Browser != "iPhone" {
		t.Fatalf("requests %+v", reqs)
	}
	h.s.NewLink()
	var st pairStatusResp
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "gone" || len(h.s.Requests()) != 0 {
		t.Fatalf("after a new link: %+v %+v", st, h.s.Requests())
	}
	for ua, want := range map[string]string{
		safariUA: "iPhone · Safari",
		"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Mobile Safari/537.36":                      "Android · Chrome",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0 Mobile/15E148 Safari/604.1": "iPhone · Chrome",
		"anything": "",
	} {
		if got := browserName(ua); got != want {
			t.Errorf("%q: %q", ua, got)
		}
	}
}

// Sessions end when unused, and after a week anyway.
func TestSessionExpiry(t *testing.T) {
	for name, age := range map[string]func(p *phone){
		"idle": func(p *phone) { p.LastSeen = time.Now().Add(-sessionIdle - time.Minute) },
		"old":  func(p *phone) { p.Added = time.Now().Add(-sessionLife - time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, &fakeSource{})
			h.pair()
			if c := h.post("needs", nil, nil); c != 200 {
				t.Fatalf("needs: %d", c)
			}
			h.s.mu.Lock()
			for _, p := range h.s.phones {
				age(p)
			}
			h.s.mu.Unlock()
			if c := h.post("needs", nil, nil); c != http.StatusUnauthorized || len(h.s.Phones()) != 0 {
				t.Fatalf("got %d, phones %+v", c, h.s.Phones())
			}
		})
	}
}

// Calls without the link are limited, and pair/status per pairing, so a
// flood of made-up calls cannot keep the real page out.
func TestPairRateLimit(t *testing.T) {
	h := newHarness(t, &fakeSource{})
	for i := range pairRate {
		if c := h.post("pair/begin", pairBeginReq{Link: fmt.Sprint("made-up-", i)}, nil); c != http.StatusForbidden {
			t.Fatalf("try %d: %d", i, c)
		}
	}
	var e apiErr
	if c := h.post("pair/begin", pairBeginReq{Link: "made-up"}, &e); c != http.StatusTooManyRequests || e.Error != "slow_down" {
		t.Fatalf("got %d %+v", c, e)
	}
	for range 2 * pairRate {
		h.post("pair/status", pairStatusReq{ID: "made-up", Poll: "x"}, nil)
	}
	if c := h.post("pair/status", pairStatusReq{ID: "made-up", Poll: "x"}, &e); c != http.StatusTooManyRequests {
		t.Fatalf("made-up status: %d", c)
	}
	// The real page still gets through.
	var begun pairBeginResp
	if c := h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun); c != 200 {
		t.Fatalf("begin: %d", c)
	}
	var fin pairFinishResp
	if c := h.post("pair/finish", h.finishReq(begun), &fin); c != 200 {
		t.Fatalf("finish: %d", c)
	}
	if err := h.s.Allow(h.s.Requests()[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	var st pairStatusResp
	if c := h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); c != 200 || st.State != "allowed" {
		t.Fatalf("status: %d %+v", c, st)
	}
}

// Allowed near the end of the request's time, the page still picks its
// session up: no phone is left added that no page holds.
func TestAllowLate(t *testing.T) {
	h := newHarness(t, &fakeSource{})
	var begun pairBeginResp
	h.post("pair/begin", pairBeginReq{Link: h.s.NewLink()}, &begun)
	var fin pairFinishResp
	h.post("pair/finish", h.finishReq(begun), &fin)
	h.s.mu.Lock()
	h.s.pairs[begun.ID].expires = time.Now().Add(20 * time.Millisecond)
	h.s.mu.Unlock()
	if err := h.s.Allow(h.s.Requests()[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	var st pairStatusResp
	if h.post("pair/status", pairStatusReq{ID: begun.ID, Poll: fin.Poll}, &st); st.State != "allowed" || st.Session == "" {
		t.Fatalf("status %+v", st)
	}
}

// A passkey that keeps a sign counter must count up: one that went back
// was copied.
func TestSignCounter(t *testing.T) {
	src := &fakeSource{needs: []gateway.NeedView{payment("ap_1")}}
	h := newHarness(t, src)
	h.counter = 5
	h.pair()
	if c := h.post("decide", h.sign(h.challenge("ap_1", "allow")), nil); c != http.StatusForbidden {
		t.Fatalf("same counter: %d", c)
	}
	h.counter = 7
	var res decideResp
	if c := h.post("decide", h.sign(h.challenge("ap_1", "allow")), &res); c != 200 || res.Result != "done" {
		t.Fatalf("counted up: %d %+v", c, res)
	}
	h.counter = 6
	if c := h.post("decide", h.sign(h.challenge("ap_1", "allow")), nil); c != http.StatusForbidden {
		t.Fatalf("went back: %d", c)
	}
	// A passkey without a counter (Apple's) stays at 0.
	h2 := newHarness(t, src)
	h2.pair()
	for range 2 {
		if c := h2.post("decide", h2.sign(h2.challenge("ap_1", "allow")), &res); c != 200 {
			t.Fatalf("no counter: %d", c)
		}
	}
}

func TestPageText(t *testing.T) {
	h := newHarness(t, &fakeSource{})
	h.s.opt.Text = func(string) (string, map[string]string) {
		return "ja", map[string]string{"title": "Pinky の承認"}
	}
	req, _ := http.NewRequest("GET", h.srv.URL+Path, nil)
	h.headers(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `<html lang="ja">`) || !strings.Contains(string(body), "Pinky の承認") ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("page: %d %s", resp.StatusCode, body[:200])
	}
}
