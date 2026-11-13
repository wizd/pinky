package onphone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
	"github.com/DIYgod/pinky/internal/standalone"
)

// model is a Messages API whose turns a test scripts: each gets the text
// the request ends with and returns the tool to call; the last repeats.
type model struct {
	mu    sync.Mutex
	n     int
	turns []func(last string, r *http.Request) map[string]any
}

func (m *model) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	data, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(data, &body)
	last := ""
	if n := len(body.Messages); n > 0 {
		last = textOf(body.Messages[n-1].Content)
	}
	m.mu.Lock()
	turn := m.turns[min(m.n, len(m.turns)-1)]
	m.n++
	m.mu.Unlock()
	use := turn(last, r)
	if use == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "msg_1", "type": "message", "role": "assistant",
		"model": "fake", "stop_reason": "tool_use", "content": []any{use},
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 10}})
}

// textOf is a message's text: its last tool result's, or its text.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &blocks)
	out := ""
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			for _, c := range b.Content {
				if c.Text != "" {
					out = c.Text
				}
			}
		case "text":
			if out == "" {
				out = b.Text
			}
		}
	}
	return out
}

func call(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

var refRe = regexp.MustCompile(`\[(\d+)\] \w+ "([^"]*)"`)

func refOf(t *testing.T, text, label string) int {
	t.Helper()
	for _, m := range refRe.FindAllStringSubmatch(text, -1) {
		if m[2] == label {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	t.Errorf("%q not in:\n%s", label, text)
	return 0
}

// guarded is Guard over a demo phone.
type guarded struct {
	*demo.Device
	g *gate
}

func (d *guarded) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	start := time.Now()
	s, err := d.Device.Observe(ctx, opt)
	if err == nil {
		d.g.saw(s, start)
	}
	return s, err
}

func (d *guarded) Tap(ctx context.Context, p screen.Point, opt driver.TapOptions) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Tap(ctx, p, opt)
}

func (d *guarded) Key(ctx context.Context, k driver.Key) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Key(ctx, k)
}

func (d *guarded) Launch(ctx context.Context, app string) error {
	if err := d.g.check(); err != nil {
		return err
	}
	return d.Device.Launch(ctx, app)
}

// person is the demo phone past the guard, and the person who opens
// Pinky's app on it. Pinky's app reports leaving the screen a moment after
// the core presses home or opens another app.
type person struct {
	dev  *demo.Device
	core *Core

	mu       sync.Mutex
	keys     []driver.Key
	launched []string
	resume   string // the screen the task's app comes back to
}

func (p *person) Key(ctx context.Context, k driver.Key) error {
	p.mu.Lock()
	p.keys = append(p.keys, k)
	p.mu.Unlock()
	err := p.dev.Key(ctx, k)
	time.AfterFunc(20*time.Millisecond, func() { p.core.SetUI(false) })
	return err
}

func (p *person) LaunchID(ctx context.Context, id string) error {
	p.mu.Lock()
	p.launched = append(p.launched, id)
	scr := p.resume
	p.mu.Unlock()
	p.dev.Show(scr)
	time.AfterFunc(20*time.Millisecond, func() { p.core.SetUI(false) })
	return nil
}

// openPinky opens Pinky's app over what the phone shows.
func (p *person) openPinky(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	p.resume = p.dev.Current()
	p.mu.Unlock()
	if err := p.dev.Launch(context.Background(), policy.IPhoneApp); err != nil {
		t.Fatal(err)
	}
	p.core.SetUI(true)
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type harness struct {
	core   *Core
	dev    *demo.Device
	person *person
	api    *model
	url    string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessLog(t, quiet)
}

// newHarnessLog is newHarness with the core logging to log.
func newHarnessLog(t *testing.T, log *slog.Logger) *harness {
	t.Helper()
	dev := demo.NewDevice("iphone", "iPhone", driver.IOS)
	dev.AddApp(policy.IPhoneApp, "Pinky")
	h := &harness{dev: dev, api: &model{}}
	srv := httptest.NewServer(h.api)
	h.url = srv.URL
	c, err := New(Options{
		ID:      "iphone",
		Self:    policy.IPhoneApp,
		Version: "test",
		Gateway: gateway.Options{SettleMax: 200 * time.Millisecond, PollInterval: 5 * time.Millisecond,
			MaxBlock: 50 * time.Millisecond, DiscoverEvery: 20 * time.Millisecond},
		Pilot: pilot.Options{HTTPClient: srv.Client()},
		Log:   log,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.Close()
		srv.Close()
	})
	h.core = c
	pol := c.Gateway().Policy()
	for cat, e := range demo.PolicyApps() {
		pol.AddApps(cat, e)
	}
	h.person = &person{dev: dev, core: c}
	if o := h.do(t, map[string]any{"cmd": "state"}); o.State == nil || o.State.Device.Ready || o.Reason != ReasonConnecting {
		t.Fatalf("before it connected: %+v", o)
	}
	c.connect(&guarded{Device: dev, g: c.gate}, h.person)
	h.until(t, "the phone", func(s standalone.State) bool { return s.Device.Ready })
	return h
}

type out struct {
	OK      bool                  `json:"ok"`
	Error   string                `json:"error"`
	Code    string                `json:"code"`
	Rev     uint64                `json:"rev"`
	State   *standalone.State     `json:"state"`
	Reason  string                `json:"reason"`
	History []standalone.PastTask `json:"history"`
	More    bool                  `json:"more"`
	Info    *Info                 `json:"info"`
	ID      string                `json:"id"`

	Memories []standalone.MemoryState `json:"memories"`
}

func (h *harness) do(t *testing.T, req map[string]any) out {
	t.Helper()
	data, _ := json.Marshal(req)
	var o out
	if err := json.Unmarshal(h.core.Call(data), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

// until waits through "wait" for a state that passes ok.
func (h *harness) until(t *testing.T, what string, ok func(standalone.State) bool) standalone.State {
	t.Helper()
	rev := uint64(0)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		o := h.do(t, map[string]any{"cmd": "wait", "rev": rev, "timeout_ms": 1000})
		if !o.OK || o.State == nil {
			t.Fatalf("waiting for %s: %+v", what, o)
		}
		if ok(*o.State) {
			return *o.State
		}
		rev = o.Rev
	}
	t.Fatalf("no %s", what)
	return standalone.State{}
}

// A task on this iPhone asks before it pays. The person answers in
// Pinky's app, where the agent can do nothing; the core goes back to the
// shop before the approved tap, which lands there.
func TestApprovalInPinkysApp(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		req  map[string]any
		code string
	}{
		{map[string]any{"cmd": "start", "task": "Order lunch", "base_url": h.url}, "no_key"},
		{map[string]any{"cmd": "start", "task": " ", "key": "sk-test"}, "no_task"},
		{map[string]any{"cmd": "start", "task": "Order lunch", "key": "sk-test", "service": "openai-compatible"}, "no_model"},
		{map[string]any{"cmd": "decide", "need": "nothing", "decision": "allow_once"}, "gone"},
	} {
		if o := h.do(t, tc.req); o.OK || o.Code != tc.code {
			t.Errorf("%v: %+v", tc.req, o)
		}
	}

	h.dev.Show("cart")
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			return call("t1", "phone_tap", map[string]any{"ref": refOf(t, last, "Place order · $12.80")})
		},
		func(last string, _ *http.Request) map[string]any {
			if !strings.HasPrefix(last, "settled") {
				t.Errorf("after the approval:\n%s", last)
			}
			return call("t2", "phone_launch", map[string]any{"app": "Pinky"})
		},
		func(last string, _ *http.Request) map[string]any {
			if !strings.HasPrefix(last, "blocked_by_policy") {
				t.Errorf("opening Pinky:\n%s", last)
			}
			return call("t3", "finish", map[string]any{"outcome": "done", "summary": "Ordered."})
		},
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Order lunch", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !o.OK {
		t.Fatalf("start: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Again", "key": "sk-test", "model": "fake"}); o.Code != "busy" {
		t.Fatalf("a second start: %+v", o)
	}
	st := h.until(t, "the approval", func(s standalone.State) bool { return len(s.Needs) > 0 })
	n := st.Needs[0]
	if n.Kind != "approval" || n.Rule != "Payments" {
		t.Fatalf("need: %+v", n)
	}

	h.person.openPinky(t)
	dev, err := h.core.prov.Open(context.Background(), "iphone")
	if err != nil {
		t.Fatal(err)
	}
	if err := dev.Tap(context.Background(), screen.Point{X: 10, Y: 10}, driver.TapOptions{}); !errors.Is(err, ErrOwnUI) {
		t.Fatalf("a tap on Pinky's app: %v", err)
	}
	if o := h.do(t, map[string]any{"cmd": "decide", "need": n.ID, "decision": "allow_once"}); !o.OK {
		t.Fatalf("decide: %+v", o)
	}
	h.person.mu.Lock()
	launched := slices.Clone(h.person.launched)
	h.person.mu.Unlock()
	if !slices.Equal(launched, []string{demo.Dinely}) || h.core.gate.uiActive() {
		t.Fatalf("back to the shop: launched %v, Pinky in front %v", launched, h.core.gate.uiActive())
	}

	st = h.until(t, "the end", func(s standalone.State) bool { return s.Agent != nil && !s.Agent.Running })
	if st.Agent.End != "done" || st.Agent.Summary != "Ordered." || len(st.Needs) != 0 {
		t.Fatalf("agent: %+v", st.Agent)
	}
	if h.dev.Current() != "done" {
		t.Fatalf("the order was not placed: the phone shows %q", h.dev.Current())
	}

	o := h.do(t, map[string]any{"cmd": "history"})
	if !o.OK || len(o.History) != 1 || o.History[0].Title != "Order lunch" || o.History[0].Outcome != "done" ||
		o.History[0].Agent == nil || o.History[0].Agent.Summary != "Ordered." || o.More {
		t.Fatalf("history: %+v", o)
	}
	o = h.do(t, map[string]any{"cmd": "history", "id": o.History[0].ID})
	if !o.OK || len(o.History) != 1 || len(o.History[0].Steps) == 0 {
		t.Fatalf("a task of the history: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "history", "id": "nope"}); o.OK || o.Code != "gone" {
		t.Fatalf("a task the history does not have: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "info"}); !o.OK || o.Info == nil || !o.Info.Connected {
		t.Fatalf("info: %+v", o)
	}
}

// A task started in Pinky's app leaves it first: the agent's first call
// finds the phone as the person left it.
func TestStartLeavesPinkysApp(t *testing.T) {
	h := newHarness(t)
	h.person.openPinky(t)
	var uiAtFirstCall bool
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			uiAtFirstCall = h.core.gate.uiActive()
			return call("t1", "finish", map[string]any{"outcome": "done", "summary": "Nothing to do."})
		},
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Look", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !o.OK {
		t.Fatalf("start: %+v", o)
	}
	h.person.mu.Lock()
	keys := slices.Clone(h.person.keys)
	h.person.mu.Unlock()
	if !slices.Equal(keys, []driver.Key{driver.KeyHome}) {
		t.Fatalf("keys pressed: %v", keys)
	}
	h.until(t, "the end", func(s standalone.State) bool { return s.Agent != nil && !s.Agent.Running })
	if uiAtFirstCall {
		t.Fatal("the agent started while Pinky's app was in front")
	}
}

// lockedBuffer is a log that tests read while the core writes it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The log says that a task started and how it ended, without the task's
// words, the key or the model's summary, which may tell what the screen
// showed.
func TestTaskLog(t *testing.T) {
	var log lockedBuffer
	h := newHarnessLog(t, slog.New(slog.NewTextHandler(&log, nil)))
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			return call("t1", "finish", map[string]any{"outcome": "done", "summary": "Read the secret plan."})
		},
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Find my secret plan", "key": "sk-test-key",
		"base_url": h.url, "model": "fake"}); !o.OK {
		t.Fatalf("start: %+v", o)
	}
	h.until(t, "the end", func(s standalone.State) bool { return s.Agent != nil && !s.Agent.Running })
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(log.String(), `msg="task ended"`) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got := log.String()
	for _, want := range []string{`msg="task started"`, "model=fake", `msg="task ended"`, "end=done"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
	for _, secret := range []string{"secret", "sk-test-key"} {
		if strings.Contains(got, secret) {
			t.Errorf("the log tells %q:\n%s", secret, got)
		}
	}
}

// wait answers at once when the state moved on, else at its timeout; the
// phone going away is a change.
func TestWait(t *testing.T) {
	h := newHarness(t)
	o := h.do(t, map[string]any{"cmd": "state"})
	start := time.Now()
	w := h.do(t, map[string]any{"cmd": "wait", "rev": o.Rev, "timeout_ms": 100})
	if w.Rev != o.Rev || time.Since(start) < 90*time.Millisecond {
		t.Fatalf("wait without a change: rev %d → %d after %v", o.Rev, w.Rev, time.Since(start))
	}
	h.core.Disconnected(driver.StateNeedsSetup, ReasonVPNOff, "Turn on the VPN.")
	st := h.until(t, "the phone offline", func(s standalone.State) bool { return !s.Device.Ready })
	if st.Device.Detail != "Turn on the VPN." {
		t.Fatalf("device: %+v", st.Device)
	}
	if o := h.do(t, map[string]any{"cmd": "state"}); o.Reason != ReasonVPNOff {
		t.Fatalf("reason: %+v", o)
	}
}

// The person's messages: steer while the task runs, then continue, which
// leaves Pinky's app first as start does.
func TestSteerAndContinue(t *testing.T) {
	h := newHarness(t)
	asked, goOn := make(chan struct{}), make(chan struct{})
	var uiAtContinue bool
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			close(asked)
			<-goOn
			return call("t1", "phone_observe", nil)
		},
		func(string, *http.Request) map[string]any {
			return call("t2", "finish", map[string]any{"outcome": "done", "summary": "Looked."})
		},
		func(string, *http.Request) map[string]any {
			uiAtContinue = h.core.gate.uiActive()
			return call("t3", "finish", map[string]any{"outcome": "done", "summary": "Looked again."})
		},
	}
	if o := h.do(t, map[string]any{"cmd": "steer", "text": "Hi"}); o.OK || o.Code != standalone.CodeNotRunning {
		t.Fatalf("steer with no task: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "start", "task": "Look", "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !o.OK {
		t.Fatalf("start: %+v", o)
	}
	<-asked
	o := h.do(t, map[string]any{"cmd": "steer", "text": "Look at the top"})
	if !o.OK || o.ID == "" {
		t.Fatalf("steer: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "steer", "id": o.ID, "text": "Look at the bottom"}); !o.OK {
		t.Fatalf("edit: %+v", o)
	}
	extra := h.do(t, map[string]any{"cmd": "steer", "text": "Never mind"}).ID
	if o := h.do(t, map[string]any{"cmd": "withdraw", "id": extra}); !o.OK {
		t.Fatalf("withdraw: %+v", o)
	}
	h.until(t, "the message waits", func(s standalone.State) bool {
		return s.Agent != nil && len(s.Agent.Pending) == 1 && s.Agent.Pending[0].Text == "Look at the bottom"
	})
	close(goOn)
	st := h.until(t, "the end", func(s standalone.State) bool { return s.Agent != nil && !s.Agent.Running })
	if !st.Agent.Continuable || len(st.Agent.Pending) != 0 {
		t.Fatalf("agent: %+v", st.Agent)
	}

	h.person.openPinky(t)
	if o := h.do(t, map[string]any{"cmd": "continue", "text": "Again"}); o.OK || o.Code != "no_key" {
		t.Fatalf("continue without a key: %+v", o)
	}
	if o := h.do(t, map[string]any{"cmd": "continue", "text": "Again", "key": "sk-test"}); !o.OK {
		t.Fatalf("continue: %+v", o)
	}
	st = h.until(t, "the second end", func(s standalone.State) bool {
		return s.Agent != nil && !s.Agent.Running && s.Agent.Continues == 1
	})
	if st.Agent.Summary != "Looked again." {
		t.Fatalf("agent: %+v", st.Agent)
	}
	if uiAtContinue {
		t.Fatal("the agent went on while Pinky's app was in front")
	}
}
