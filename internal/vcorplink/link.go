package vcorplink

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/websocket"
)

// Phone is a phone as the link reports it to the platform.
type Phone struct {
	ID       string // the gateway's id (UDID, adb serial); never sent
	Name     string
	Platform string // ios, android
	Online   bool
}

// Host is the Pinky the link relays to: in the gateway's own process, or
// over the per-user socket from `pinky vcorp run`.
type Host interface {
	// Phones lists the phones Pinky knows.
	Phones(ctx context.Context) ([]Phone, error)
	// Connect returns a transport to Pinky's MCP server, for one session.
	Connect(ctx context.Context) (sdk.Transport, error)
}

// Tools are the MCP tools a cloud task may call: all of Pinky's phone tools.
var Tools = []string{
	"phone_connect", "phone_release", "phone_observe", "phone_tap", "phone_type", "phone_swipe",
	"phone_pinch", "phone_drag", "phone_key", "phone_launch", "phone_apps", "phone_batch",
	"phone_wait_for", "phone_files", "phone_app", "phone_logs", "phone_device",
	"phone_request_approval", "phone_handoff",
}

// ErrRevoked: the person removed this Pinky on the platform's page, or
// unpaired it; the link stops and the pairing is void.
var ErrRevoked = errors.New("the platform removed this pairing")

const (
	readTimeout  = 90 * time.Second // the platform pings every 25 s
	callTimeout  = 7 * time.Minute  // under the platform's 8 minutes
	sessionIdle  = 30 * time.Minute // a task that went quiet without job_end
	phonesEvery  = 5 * time.Second
	clientName   = "vcorp-cloud"
	clientTitle  = "VCorp (cloud)" // the agent name the person approves in Pinky
	maxTextBytes = 256 << 10
)

// Link keeps one connection to the platform.
type Link struct {
	Cfg      *Config
	Host     Host
	Version  string // Pinky's version
	Platform string // windows, macos, linux
	Log      *slog.Logger
	// OnState hears when the link signs in to the platform (connected) and
	// when it drops, with why; nil hears nothing. Settings shows it.
	OnState func(connected bool, err error)

	wmu sync.Mutex
	ws  *websocket.Conn

	mu       sync.Mutex
	phones   map[string]Phone // platform id -> phone
	reported string           // the phone list last sent, as JSON
	sessions map[string]*jobSession
}

type jobSession struct {
	cs   *sdk.ClientSession
	mu   sync.Mutex // one call at a time per task
	last time.Time
}

type inMsg struct {
	Type   string          `json:"type"`
	Nonce  string          `json:"nonce"`
	ID     string          `json:"id"`
	JobID  string          `json:"jobId"`
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params"`
	To     *struct {
		Device string `json:"device"`
	} `json:"to"`
	Account string `json:"account"`
}

type wirePhone struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Online   bool   `json:"online"`
}

// Run connects, and connects again after a drop with backoff, until ctx
// ends or the platform revokes the pairing.
func (l *Link) Run(ctx context.Context) error {
	if l.Log == nil {
		l.Log = slog.Default()
	}
	l.mu.Lock()
	if l.sessions == nil {
		l.sessions = map[string]*jobSession{}
	}
	l.mu.Unlock()
	go l.sweep(ctx)
	defer l.closeSessions()
	wait := time.Second
	for {
		started := time.Now()
		err := l.once(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		l.state(false, err)
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if time.Since(started) > time.Minute {
			wait = time.Second
		}
		l.Log.Warn("vcorp: disconnected", "err", err, "retry", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func wsURL(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil {
		return "", err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/ext/ws"
	return u.String(), nil
}

func (l *Link) once(ctx context.Context) error {
	target, err := wsURL(l.Cfg.Origin)
	if err != nil {
		return err
	}
	wc, err := websocket.NewConfig(target, l.Cfg.Origin)
	if err != nil {
		return err
	}
	wc.Dialer = &net.Dialer{Timeout: 15 * time.Second}
	ws, err := websocket.DialConfig(wc)
	if err != nil {
		return err
	}
	ws.MaxPayloadBytes = 8 << 20
	l.wmu.Lock()
	l.ws = ws
	l.wmu.Unlock()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-cctx.Done()
		ws.Close()
	}()
	welcomed := false
	for {
		ws.SetReadDeadline(time.Now().Add(readTimeout))
		var raw string
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			if !welcomed {
				return fmt.Errorf("closed before welcome (removed on the platform, or a wrong key?): %w", err)
			}
			return err
		}
		var m inMsg
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue
		}
		switch m.Type {
		case "challenge":
			if err := l.hello(ctx, m.Nonce); err != nil {
				return err
			}
		case "welcome":
			welcomed = true
			l.state(true, nil)
			l.Log.Info("vcorp: connected", "account", m.Account, "origin", l.Cfg.Origin)
			go l.watchPhones(cctx)
		case "ping":
			l.send(map[string]any{"type": "pong"})
		case "probe":
			l.send(map[string]any{"type": "res", "id": m.ID, "ok": true, "data": map[string]any{}})
		case "cmd":
			go l.command(ctx, m)
		case "job_end":
			l.endJob(m.JobID)
		case "revoked":
			return ErrRevoked
		}
	}
}

func (l *Link) state(connected bool, err error) {
	if l.OnState != nil {
		l.OnState(connected, err)
	}
}

func (l *Link) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	l.wmu.Lock()
	defer l.wmu.Unlock()
	if l.ws == nil {
		return net.ErrClosed
	}
	l.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return websocket.Message.Send(l.ws, string(b))
}

func (l *Link) hello(ctx context.Context, nonce string) error {
	sig, err := Sign(l.Cfg.Key, l.Cfg.Origin, l.Cfg.DeviceID, nonce)
	if err != nil {
		return err
	}
	list, _ := l.listPhones(ctx)
	l.mu.Lock()
	b, _ := json.Marshal(list)
	l.reported = string(b)
	l.mu.Unlock()
	return l.send(map[string]any{
		"type": "hello", "deviceId": l.Cfg.DeviceID, "sig": sig, "kind": "pinky",
		"version": l.Version, "name": l.Cfg.Name, "platform": l.Platform, "devices": list,
	})
}

// listPhones asks Pinky for its phones and remembers which platform id is
// which phone.
func (l *Link) listPhones(ctx context.Context) ([]wirePhone, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	phones, err := l.Host.Phones(cctx)
	if err != nil {
		return nil, err
	}
	out := make([]wirePhone, 0, len(phones))
	m := map[string]Phone{}
	for _, p := range phones {
		id := PhoneID(l.Cfg.DeviceID, p.ID)
		m[id] = p
		out = append(out, wirePhone{Kind: "mobile", ID: id, Name: p.Name, Platform: p.Platform, Online: p.Online})
	}
	slices.SortFunc(out, func(a, b wirePhone) int { return strings.Compare(a.ID, b.ID) })
	l.mu.Lock()
	l.phones = m
	l.mu.Unlock()
	return out, nil
}

// watchPhones tells the platform when a phone comes, goes or changes.
func (l *Link) watchPhones(ctx context.Context) {
	t := time.NewTicker(phonesEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		list, err := l.listPhones(ctx)
		if err != nil {
			l.Log.Debug("vcorp: list phones", "err", err)
			continue
		}
		b, _ := json.Marshal(list)
		l.mu.Lock()
		same := string(b) == l.reported
		l.reported = string(b)
		l.mu.Unlock()
		if !same {
			l.send(map[string]any{"type": "config", "devices": list})
		}
	}
}

type cmdParams struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args"`
}

type cmdError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (l *Link) command(ctx context.Context, m inMsg) {
	phoneID := ""
	if m.To != nil {
		phoneID = m.To.Device
	}
	reply := func(ok bool, data any, e *cmdError) {
		r := map[string]any{"type": "res", "id": m.ID, "ok": ok, "from": map[string]string{"device": phoneID, "kind": "mobile"}}
		if data != nil {
			r["data"] = data
		}
		if e != nil {
			r["error"] = e
		}
		if err := l.send(r); err != nil {
			l.Log.Warn("vcorp: send result", "err", err)
		}
	}
	if m.Cmd != "phone" {
		reply(false, nil, &cmdError{"CLOUD_DENIED", "Pinky only takes phone commands, not " + m.Cmd})
		return
	}
	var p cmdParams
	if err := json.Unmarshal(m.Params, &p); err != nil || p.Tool == "" {
		reply(false, nil, &cmdError{"BAD_PARAMS", `phone takes {"tool":"phone_…","args":{…}}`})
		return
	}
	if !slices.Contains(Tools, p.Tool) {
		reply(false, nil, &cmdError{"CLOUD_DENIED", "not a Pinky phone tool: " + p.Tool})
		return
	}
	l.mu.Lock()
	phone, ok := l.phones[phoneID]
	l.mu.Unlock()
	if !ok {
		reply(false, nil, &cmdError{"NO_DEVICE", "Pinky does not have this phone right now"})
		return
	}
	if m.JobID == "" {
		reply(false, nil, &cmdError{"BAD_PARAMS", "no task id"})
		return
	}
	if p.Args == nil {
		p.Args = map[string]any{}
	}
	// The platform chose the phone; the task cannot point elsewhere.
	p.Args["device"] = phone.ID
	s, err := l.session(ctx, m.JobID)
	if err != nil {
		reply(false, nil, &cmdError{"PINKY_OFFLINE", "could not reach Pinky on the computer: " + err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	res, err := s.cs.CallTool(cctx, &sdk.CallToolParams{Name: p.Tool, Arguments: p.Args})
	s.last = time.Now()
	if err != nil {
		// The session may be gone (Pinky restarted): drop it, so the next
		// call opens a new one, which continues the task.
		l.dropSession(m.JobID, s)
		reply(false, nil, &cmdError{"UNCERTAIN", "Pinky did not answer (" + err.Error() + "). The action may or may not have happened: observe before doing anything else."})
		return
	}
	data := resultData(res)
	if res.IsError {
		reply(false, data, &cmdError{"PHONE_ERROR", data["text"].(string)})
		return
	}
	reply(true, data, nil)
}

// resultData turns a tool result into what the platform passes on: the
// text, and a screenshot as a data URL.
func resultData(res *sdk.CallToolResult) map[string]any {
	var text []string
	data := map[string]any{}
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			text = append(text, c.Text)
		case *sdk.ImageContent:
			if _, has := data["dataUrl"]; !has {
				mime := c.MIMEType
				if mime == "" {
					mime = "image/jpeg"
				}
				data["dataUrl"] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(c.Data)
			}
		}
	}
	t := strings.Join(text, "\n")
	if len(t) > maxTextBytes {
		t = t[:maxTextBytes] + "\n… (cut)"
	}
	data["text"] = t
	return data
}

func (l *Link) session(ctx context.Context, jobID string) (*jobSession, error) {
	l.mu.Lock()
	s := l.sessions[jobID]
	l.mu.Unlock()
	if s != nil {
		return s, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	t, err := l.Host.Connect(cctx)
	if err != nil {
		return nil, err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: clientName, Title: clientTitle, Version: l.Version}, nil)
	// The session outlives this command, so it is not bound to cctx.
	cs, err := client.Connect(context.WithoutCancel(ctx), t, nil)
	if err != nil {
		return nil, err
	}
	s = &jobSession{cs: cs, last: time.Now()}
	l.mu.Lock()
	if had := l.sessions[jobID]; had != nil {
		l.mu.Unlock()
		cs.Close()
		return had, nil
	}
	l.sessions[jobID] = s
	l.mu.Unlock()
	return s, nil
}

func (l *Link) dropSession(jobID string, s *jobSession) {
	l.mu.Lock()
	if l.sessions[jobID] == s {
		delete(l.sessions, jobID)
	}
	l.mu.Unlock()
	go s.cs.Close()
}

// endJob ends the task's MCP session; Pinky ends its task and frees the
// phone.
func (l *Link) endJob(jobID string) {
	l.mu.Lock()
	s := l.sessions[jobID]
	delete(l.sessions, jobID)
	l.mu.Unlock()
	if s == nil {
		return
	}
	l.Log.Info("vcorp: task ended", "job", jobID)
	go func() {
		// Closing alone would keep the phone a minute for a reconnect
		// that never comes: the task is over, so release it now. A task
		// that released already gets an error here, which is fine.
		s.mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		s.cs.CallTool(ctx, &sdk.CallToolParams{Name: "phone_release", Arguments: map[string]any{
			"outcome": "partial", "summary": "The cloud task ended without releasing the phone."}})
		cancel()
		s.mu.Unlock()
		s.cs.Close()
	}()
}

func (l *Link) sweep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		l.mu.Lock()
		for id, s := range l.sessions {
			if s.mu.TryLock() {
				idle := time.Since(s.last) > sessionIdle
				s.mu.Unlock()
				if idle {
					delete(l.sessions, id)
					go s.cs.Close()
				}
			}
		}
		l.mu.Unlock()
	}
}

func (l *Link) closeSessions() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, s := range l.sessions {
		delete(l.sessions, id)
		s.cs.Close()
	}
}
