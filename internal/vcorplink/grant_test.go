package vcorplink

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"golang.org/x/net/websocket"
)

type cmdResp struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// grantPlatform speaks the platform side and lets tests drive cmd frames
// and observe hello/config messages.
type grantPlatform struct {
	t      *testing.T
	origin string
	keys   map[string]map[string]string

	mu      sync.Mutex
	ws      *websocket.Conn
	hellos  int
	grantOK *bool
	configs []map[string]any
	phones  []wirePhone
	pending map[string]chan cmdResp
}

func newGrantPlatform(t *testing.T) *grantPlatform {
	return &grantPlatform{
		t:       t,
		keys:    map[string]map[string]string{},
		pending: map[string]chan cmdResp{},
	}
}

func (p *grantPlatform) handle(ws *websocket.Conn) {
	nonce := "n" + time.Now().Format("150405.000000000")
	websocket.JSON.Send(ws, map[string]any{"type": "challenge", "nonce": nonce, "v": 1})
	var m struct {
		Type     string      `json:"type"`
		DeviceID string      `json:"deviceId"`
		Sig      string      `json:"sig"`
		Devices  []wirePhone `json:"devices"`
		GrantOK  *bool       `json:"grantOk"`
	}
	if websocket.JSON.Receive(ws, &m) != nil || m.Type != "hello" {
		ws.Close()
		return
	}
	p.mu.Lock()
	p.hellos++
	p.ws = ws
	p.phones = m.Devices
	p.grantOK = m.GrantOK
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		if p.ws == ws {
			p.ws = nil
		}
		p.mu.Unlock()
	}()
	websocket.JSON.Send(ws, map[string]any{"type": "welcome", "account": "owner"})
	for {
		var raw string
		if websocket.Message.Receive(ws, &raw) != nil {
			return
		}
		var env map[string]any
		if json.Unmarshal([]byte(raw), &env) != nil {
			continue
		}
		switch env["type"] {
		case "config":
			p.mu.Lock()
			p.configs = append(p.configs, env)
			if v, ok := env["grantOk"].(bool); ok {
				b := v
				p.grantOK = &b
			}
			p.mu.Unlock()
		case "res":
			var r cmdResp
			if json.Unmarshal([]byte(raw), &r) == nil {
				p.mu.Lock()
				ch := p.pending[r.ID]
				delete(p.pending, r.ID)
				p.mu.Unlock()
				if ch != nil {
					ch <- r
				}
			}
		}
	}
}

func (p *grantPlatform) call(t *testing.T, id, jobID string, grant *jobGrant, phoneID, cmd string, params any) cmdResp {
	t.Helper()
	ch := make(chan cmdResp, 1)
	p.mu.Lock()
	ws := p.ws
	p.pending[id] = ch
	p.mu.Unlock()
	if ws == nil {
		t.Fatal("platform has no connected websocket")
	}
	msg := map[string]any{
		"type":   "cmd",
		"id":     id,
		"jobId":  jobID,
		"cmd":    cmd,
		"params": params,
		"to":     map[string]string{"device": phoneID},
	}
	if grant != nil {
		msg["job"] = map[string]any{"grant": grant}
	}
	if err := websocket.JSON.Send(ws, msg); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for cmd %s", id)
		return cmdResp{}
	}
}

func TestGrantEnforcementAndIsolation(t *testing.T) {
	p := newGrantPlatform(t)
	srv := httptest.NewServer(websocket.Server{Handler: p.handle})
	defer srv.Close()
	p.origin = srv.URL

	providers, _ := demo.World()
	pol := policy.Default()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	dataDir := t.TempDir()
	g, err := gateway.New(gateway.Options{
		Providers:      providers,
		Policy:         pol,
		DataDir:        dataDir,
		TrustNewAgents: true,
		MaxBlock:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	host := &GatewayHost{G: g, MCP: mcp.New(g, "test")}

	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Origin: srv.URL, DeviceID: "pk_test", Account: "owner", Key: key, Name: "Pinky · test", GrantOK: false}
	link := &Link{Cfg: cfg, Host: host, Version: "test", Platform: "test"}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- link.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitUntil(t, "connected with phones", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.ws != nil && len(p.phones) > 0 && p.grantOK != nil
	})
	p.mu.Lock()
	if *p.grantOK {
		t.Fatal("hello reported grantOk=true when config had GrantOK=false")
	}
	phoneID := p.phones[0].ID
	p.mu.Unlock()

	alice := &jobGrant{ID: "gr_1", For: "alice"}

	// 1. Grant task rejected with GRANT_OFF while switch is off.
	res := p.call(t, "c1", "job_g1", alice, phoneID, "phone", map[string]any{
		"tool": "phone_connect", "args": map[string]any{"task": "Check screen"},
	})
	if res.OK || res.Error == nil || res.Error.Code != "GRANT_OFF" {
		t.Fatalf("grant while off: %+v", res)
	}

	// 2. Malformed grant rejected with CLOUD_DENIED.
	res = p.call(t, "c2", "job_bad", &jobGrant{ID: "gr_1", For: "   "}, phoneID, "phone", map[string]any{
		"tool": "phone_connect", "args": map[string]any{"task": "Bad"},
	})
	if res.OK || res.Error == nil || res.Error.Code != "CLOUD_DENIED" {
		t.Fatalf("bad grant: %+v", res)
	}

	// 3. Turn GrantOK on: sends config frame without reconnecting.
	link.SetGrantOK(true)
	waitUntil(t, "config grantOk=true", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.grantOK != nil && *p.grantOK
	})
	p.mu.Lock()
	if p.hellos != 1 {
		t.Errorf("SetGrantOK reconnected: hellos=%d", p.hellos)
	}
	p.mu.Unlock()

	// 4. Non-phone command and blocked tools return GRANT_SCOPE.
	res = p.call(t, "c3", "job_g1", alice, phoneID, "desktop", map[string]any{})
	if res.OK || res.Error == nil || res.Error.Code != "GRANT_SCOPE" {
		t.Fatalf("non-phone grant cmd: %+v", res)
	}
	for _, tool := range []string{"phone_files", "phone_app", "phone_logs", "phone_device"} {
		res = p.call(t, "scope_"+tool, "job_g1", alice, phoneID, "phone", map[string]any{"tool": tool, "args": map[string]any{}})
		if res.OK || res.Error == nil || res.Error.Code != "GRANT_SCOPE" {
			t.Fatalf("tool %s: %+v (want GRANT_SCOPE)", tool, res)
		}
	}

	// 5. Screen tools work under isolated identity "VCorp · alice", and save/export are stripped.
	res = p.call(t, "c4", "job_g1", alice, phoneID, "phone", map[string]any{
		"tool": "phone_connect", "args": map[string]any{"task": "Inspect apps"},
	})
	if !res.OK {
		t.Fatalf("grant phone_connect failed: %+v", res)
	}
	// Verify the gateway sees "VCorp · alice", not "VCorp (cloud)".
	tasks := g.Tasks()
	foundAlice := false
	for _, tk := range tasks {
		if tk.Title == "Inspect apps" {
			if tk.Agent != "VCorp · alice" {
				t.Fatalf("task agent = %q, want %q", tk.Agent, "VCorp · alice")
			}
			foundAlice = true
		}
	}
	if !foundAlice {
		t.Fatalf("task not found in gateway: %+v", tasks)
	}

	// phone_observe with save must strip save and write no file to dataDir/files.
	res = p.call(t, "c5", "job_g1", alice, phoneID, "phone", map[string]any{
		"tool": "phone_observe", "args": map[string]any{"save": "leak.png"},
	})
	if !res.OK {
		t.Fatalf("grant phone_observe failed: %+v", res)
	}
	if entries, _ := os.ReadDir(filepath.Join(dataDir, "files")); len(entries) != 0 {
		t.Fatalf("phone_observe wrote files to owner disk: %+v", entries)
	}

	// 6. Mid-session identity switch on same jobId is refused.
	res = p.call(t, "c6", "job_g1", nil, phoneID, "phone", map[string]any{
		"tool": "phone_observe", "args": map[string]any{},
	})
	if res.OK || res.Error == nil || res.Error.Code != "CLOUD_DENIED" {
		t.Fatalf("identity switch to owner: %+v", res)
	}
	res = p.call(t, "c7", "job_g1", &jobGrant{ID: "gr_1", For: "bob"}, phoneID, "phone", map[string]any{
		"tool": "phone_observe", "args": map[string]any{},
	})
	if res.OK || res.Error == nil || res.Error.Code != "CLOUD_DENIED" {
		t.Fatalf("identity switch to bob: %+v", res)
	}

	// 7. Owner's global Always allow exemption does NOT apply to VCorp · alice,
	// and her approval card never offers Always allow.
	if _, err := g.Policy().Exempt([]string{"Payments", "Commits"}, screen.App{Name: "Dinely", ID: demo.Dinely}); err != nil {
		t.Fatal(err)
	}
	findRef := func(r cmdResp, label string) int {
		t.Helper()
		var data map[string]any
		_ = json.Unmarshal(r.Data, &data)
		text, _ := data["text"].(string)
		for _, line := range strings.Split(text, "\n") {
			if !strings.Contains(line, label) {
				continue
			}
			i := strings.IndexByte(line, '[')
			j := strings.IndexByte(line, ']')
			if i >= 0 && j > i+1 {
				if n, err := strconv.Atoi(line[i+1 : j]); err == nil {
					return n
				}
			}
		}
		t.Fatalf("label %q not found in screen:\n%s", label, text)
		return 0
	}
	tapLabel := func(id string, prev cmdResp, label string) cmdResp {
		t.Helper()
		return p.call(t, id, "job_g1", alice, phoneID, "phone", map[string]any{
			"tool": "phone_tap", "args": map[string]any{"ref": findRef(prev, label)},
		})
	}
	rLaunch := p.call(t, "c8a", "job_g1", alice, phoneID, "phone", map[string]any{
		"tool": "phone_launch", "args": map[string]any{"app": "Dinely"},
	})
	if !rLaunch.OK {
		t.Fatalf("launch Dinely: %+v", rLaunch)
	}
	rOrders := tapLabel("c8b", rLaunch, "Orders")
	rReorder := tapLabel("c8c", rOrders, "Reorder · Chicken rice bowl")
	rCart := tapLabel("c8d", rReorder, "Cart · 1")
	rPay := tapLabel("c8e", rCart, "Place order · $12.80")
	if !rPay.OK {
		t.Fatalf("tap Pay call error: %+v", rPay)
	}
	var payData map[string]any
	_ = json.Unmarshal(rPay.Data, &payData)
	if txt, _ := payData["text"].(string); !strings.Contains(txt, "approval_pending") {
		t.Fatalf("expected approval_pending for grant agent despite owner's Always allow, got:\n%s", txt)
	}
	needs := g.Needs()
	if len(needs) != 1 || needs[0].Agent != "VCorp · alice" || needs[0].AlwaysAllow || !needs[0].AllowTask {
		t.Fatalf("unexpected grant need: %+v", needs)
	}
	if err := g.Decide(needs[0].ID, gateway.Deny, "no"); err != nil {
		t.Fatal(err)
	}

	// 8. Turning GrantOK off immediately releases active grant sessions and sends config grantOk=false.
	link.SetGrantOK(false)
	waitUntil(t, "config grantOk=false", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.grantOK != nil && !*p.grantOK
	})
	waitUntil(t, "grant session released", func() bool {
		for _, d := range g.Devices() {
			if d.TaskID != "" {
				return false
			}
		}
		return true
	})

	// 9. Owner task still works with GrantOK=false and can use phone_files.
	res = p.call(t, "c9", "job_owner", nil, phoneID, "phone", map[string]any{
		"tool": "phone_connect", "args": map[string]any{"task": "Owner task"},
	})
	if !res.OK {
		t.Fatalf("owner connect after grant off: %+v", res)
	}
	res = p.call(t, "c10", "job_owner", nil, phoneID, "phone", map[string]any{
		"tool": "phone_files", "args": map[string]any{"action": "list"},
	})
	if !res.OK {
		t.Fatalf("owner phone_files: %+v", res)
	}
}

func TestSuperviseGrantToggleNoReconnect(t *testing.T) {
	defer func(d time.Duration) { superviseEvery = d }(superviseEvery)
	superviseEvery = 40 * time.Millisecond

	p := newGrantPlatform(t)
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

	key, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "vcorp.json")
	cfg := &Config{Origin: srv.URL, DeviceID: "pk_sup", Account: "owner", Key: key, Name: "test", GrantOK: false}
	if err := Save(file, cfg); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Supervise(ctx, file, func(c *Config) *Link { return &Link{Cfg: c, Host: host, Version: "test", Platform: "test"} }, nil)
	}()

	waitUntil(t, "connected with grantOk=false", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.ws != nil && p.grantOK != nil && !*p.grantOK
	})

	// Flip GrantOK in vcorp.json: Supervise must update live without reconnecting.
	cfg.GrantOK = true
	if err := Save(file, cfg); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "grantOk updated to true", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.grantOK != nil && *p.grantOK
	})
	p.mu.Lock()
	if p.hellos != 1 {
		t.Errorf("GrantOK change caused reconnect: hellos=%d, want 1", p.hellos)
	}
	p.mu.Unlock()

	cancel()
	<-done
}
