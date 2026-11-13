// Package webmirror shows a phone's live screen in a web browser, on this
// computer or, through `tailscale serve`, on the person's other devices,
// and, while the person allows it, lets the browser use the phone
// (input.go).
//
// It is off by default, the person turns it on in Settings, and it
// listens on this computer only (Addr). Other devices reach it through
// Tailscale, which the person points at it with `tailscale serve`; Pinky
// never runs that itself; Tailscale Funnel's public requests are refused.
// Every page needs one of the mirror's own tokens, never the agent token:
// one can only watch, the other can also use the phone and exists only
// while the person allows control. Both live in memory only, so a new
// start of Pinky makes new links and nothing on disk gives them away. The
// page's link carries its token after the "#", which browsers do not send
// to the server, and the page sends it as its first WebSocket message.
// Through Tailscale, control also needs the person who owns this computer
// on the tailnet.
//
// The phone's video goes to the browser as it comes from the phone,
// without decoding or encoding it again: a WebSocket carries the decoder
// configuration (codec.go) and each picture, and the page decodes them
// with WebCodecs. It shows the phone's screen as Pinky's own mirror does,
// blocked apps and one-time codes included, so the token is the key to
// all of it.
package webmirror

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// Addr is where the web mirror listens: this computer only, on the port
// after remote agents' (127.0.0.1:7465).
const Addr = "127.0.0.1:7466"

// WebSocketPath is where the page opens its WebSocket.
const WebSocketPath = "/ws"

// Source gives the phones and their live video, and takes the person's
// input: the gateway. Touch and Key are the window mirror's own, so input
// from the page pauses an agent's task as input on the mirror does; the
// page reaches nothing else of the gateway (approvals, unlocking).
type Source interface {
	Devices() []gateway.DeviceView
	Stream(ctx context.Context, deviceID string, opt driver.StreamOptions) (driver.Stream, error)
	Touch(deviceID string, a driver.TouchAction, p screen.Point) error
	Key(deviceID string, k driver.Key) error
}

// Options configure a Server.
type Options struct {
	Source Source
	Log    *slog.Logger
	// OwnNames returns this computer's own DNS names that `tailscale
	// serve` forwards requests under, such as "mac.tail1234.ts.net".
	// Requests under any other name than those and localhost's are
	// refused (DNS rebinding).
	OwnNames func(ctx context.Context) []string
	// Owner returns the tailnet login of the person who owns this
	// computer, "" when it has none (a tagged computer). Only they may
	// use the phone through Tailscale.
	Owner func(ctx context.Context) string
	// Text returns the page's words, by key, in the language that best
	// suits the browser's Accept-Language, and that language's tag.
	Text func(acceptLanguage string) (lang string, text map[string]string)
	// Approvals, when set, is served under ApprovalsPath behind the
	// mirror's own checks (Funnel, this computer's names): the page that
	// answers approvals in the browser (internal/webapproval), which does
	// its own checks besides. The mirror itself never reaches approvals.
	Approvals http.Handler
}

// ApprovalsPath is where Options.Approvals is served.
const ApprovalsPath = "/approvals"

// Server is the web mirror.
type Server struct {
	opt Options
	log *slog.Logger

	mu  sync.Mutex
	ln  net.Listener
	srv *http.Server
	// token can only watch; controlToken can also use the phone, and is
	// "" while the person does not allow that. Both in memory only.
	token        string
	controlToken string
	conns        map[*websocket.Conn]*Watcher
	open         int                 // WebSockets open, said hello or not
	pressing     map[string]*session // the one session holding a finger down, by phone
	err          error               // why it could not turn on
	// stopped is set once the mirror is turned off, so a viewer whose
	// WebSocket opened while it was stopping is not kept.
	stopped bool
}

// New returns the web mirror, off.
func New(opt Options) *Server {
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{opt: opt, log: log, token: newToken(), conns: map[*websocket.Conn]*Watcher{}, pressing: map[string]*session{}}
}

// Set turns the web mirror on or off. Turning it off ends every viewer's
// connection.
func (s *Server) Set(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on == (s.srv != nil) && s.err == nil {
		return nil
	}
	s.err = nil
	if !on {
		s.stopLocked()
		return nil
	}
	ln, err := net.Listen("tcp", Addr)
	if err != nil {
		s.err = fmt.Errorf("listen on %s: %w", Addr, err)
		return s.err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.ln, s.srv, s.stopped = ln, srv, false
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Warn("web mirror stopped", "err", err)
		}
	}()
	s.log.Info("web mirror on", "addr", Addr)
	return nil
}

// On reports whether the web mirror is on.
func (s *Server) On() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Err says why the web mirror could not turn on, nil when it is on or off
// as asked.
func (s *Server) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// URL is the page's address on this computer.
func URL() string { return "http://" + Addr + "/" }

// Close turns the web mirror off.
func (s *Server) Close() { s.Set(false) }

func (s *Server) stopLocked() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	s.srv.Shutdown(ctx)
	s.stopped = true
	s.closeConnsLocked(false)
	s.ln, s.srv = nil, nil
	s.log.Info("web mirror off")
}

// closeConnsLocked ends the WebSockets that may use the phone, or every
// one, which Shutdown leaves alone.
func (s *Server) closeConnsLocked(controlOnly bool) {
	for c, w := range s.conns {
		if !controlOnly || w.Control {
			c.Close()
		}
	}
}

// SetControl lets the page use the phone, or stops it. Turning it on makes
// a new control token; turning it off drops it and cuts off the browsers
// that use the phone.
func (s *Server) SetControl(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if on == (s.controlToken != "") {
		return
	}
	if on {
		s.controlToken = newToken()
	} else {
		s.controlToken = ""
		s.closeConnsLocked(true)
	}
	s.log.Info("web mirror: control from the browser", "on", on)
}

func (s *Server) controlAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controlToken != ""
}

// Watcher is a browser on the mirror.
type Watcher struct {
	Device  string // the phone it shows
	Control bool   // it may use the phone
	// Login is the tailnet login of the person `tailscale serve` named,
	// "" on this computer or for a tagged device.
	Login string
}

// Watchers are the browsers that watch now.
func (s *Server) Watchers() []Watcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Watcher, 0, len(s.conns))
	for _, w := range s.conns {
		if w.Device != "" {
			out = append(out, *w)
		}
	}
	return out
}

// Token returns the token that can only watch, ControlToken the one that
// can also use the phone: "" while the person does not allow that.
func (s *Server) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

func (s *Server) ControlToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controlToken
}

// NewToken replaces both tokens: every link stops working, and the
// browsers that watch or use the phone with them are cut off.
func (s *Server) NewToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = newToken()
	if s.controlToken != "" {
		s.controlToken = newToken()
	}
	s.closeConnsLocked(false)
}

// newToken returns a random token of 26 base32 letters (130 bits).
func newToken() string { return rand.Text() }

// roleOf says what a hello's token lets the page do. Both tokens are
// compared in full, in constant time.
func (s *Server) roleOf(tok string) (r role, ok bool) {
	s.mu.Lock()
	view, ctl := s.token, s.controlToken
	s.mu.Unlock()
	isView := view != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(view)) == 1
	isCtl := ctl != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(ctl)) == 1
	switch {
	case isCtl:
		return roleControl, true
	case isView:
		return roleView, true
	}
	return roleView, false
}

// Handler serves the page and its WebSocket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.servePage)
	ws := websocket.Server{Handshake: s.handshake, Handler: s.serveViewer}
	mux.HandleFunc("GET "+WebSocketPath, func(w http.ResponseWriter, r *http.Request) {
		if !s.enter() {
			http.Error(w, "Too many browsers on Pinky's web mirror.", http.StatusServiceUnavailable)
			return
		}
		defer s.leave()
		ws.ServeHTTP(w, r)
	})
	if s.opt.Approvals != nil {
		mux.Handle(ApprovalsPath, s.opt.Approvals)
		mux.Handle(ApprovalsPath+"/", s.opt.Approvals)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Tailscale-Funnel-Request") != "" {
			// Tailscale Funnel: anyone on the internet.
			http.Error(w, "Pinky's web mirror is not for Tailscale Funnel.", http.StatusForbidden)
			return
		}
		if !s.ownHost(r) {
			http.Error(w, "Pinky's web mirror answers only at this computer's own names.", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ownHost reports whether r names this computer: localhost, a loopback
// address, or one of its tailnet names, which `tailscale serve` forwards
// requests under. A page whose DNS name was rebound to 127.0.0.1 names
// another host, and is refused.
func (s *Server) ownHost(r *http.Request) bool {
	name := r.Host
	if h, _, err := net.SplitHostPort(name); err == nil {
		name = h
	}
	name = strings.TrimSuffix(name, ".")
	if loopbackName(name) {
		return true
	}
	if s.opt.OwnNames == nil || name == "" {
		return false
	}
	for _, own := range s.opt.OwnNames(r.Context()) {
		if own = strings.TrimSuffix(own, "."); own != "" && strings.EqualFold(name, own) {
			return true
		}
	}
	return false
}

// proxied reports whether r came through a proxy, as `tailscale serve`
// forwards a request from another device: it names another host than
// this computer's loopback, or carries a header a proxy adds. A request
// with a Host of the client's choosing still shows the headers `tailscale
// serve` adds (X-Forwarded-*, Tailscale-*).
func proxied(r *http.Request) bool { return Proxied(r) }

// Proxied reports whether r came through a proxy such as `tailscale
// serve` (proxied). A process on this computer can fake it.
func Proxied(r *http.Request) bool {
	if !loopbackName(hostName(r.Host)) {
		return true
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded",
		"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-Funnel-Request"} {
		if r.Header.Get(h) != "" {
			return true
		}
	}
	return false
}

// ownerLogin reports whether login is the tailnet login of the person who
// owns this computer.
func (s *Server) ownerLogin(ctx context.Context, login string) bool {
	if login == "" || s.opt.Owner == nil {
		return false
	}
	owner := s.opt.Owner(ctx)
	return owner != "" && strings.EqualFold(owner, login)
}

// enter counts a WebSocket in, unless maxConns are open; leave counts it
// out.
func (s *Server) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open >= maxConns {
		return false
	}
	s.open++
	return true
}

func (s *Server) leave() {
	s.mu.Lock()
	s.open--
	s.mu.Unlock()
}

func loopbackName(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// handshake takes WebSockets from the page itself only: the Origin must
// name the host the request does.
func (s *Server) handshake(cfg *websocket.Config, r *http.Request) error {
	o, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || o.Host == "" || !strings.EqualFold(o.Host, r.Host) {
		return errors.New("cross-origin WebSocket")
	}
	cfg.Origin = o
	return nil
}

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("page").Parse(pageHTML))

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	lang, text := "en", map[string]string{}
	if s.opt.Text != nil {
		lang, text = s.opt.Text(r.Header.Get("Accept-Language"))
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; frame-ancestors 'none'")
	page.Execute(w, struct {
		Lang string
		Text map[string]string
	}{lang, text})
}

// Messages between the page and the server. The page's first one is a
// hello with the token; text messages from the server are JSON, binary
// ones pictures (frameMessage).
type hello struct {
	Token  string `json:"token"`
	Device string `json:"device"`
}

type clientMsg struct {
	Type string `json:"type"` // "keyframe", "touch", "key", "ping"
	// A touch: its phase ("down", "move", "up"), where it is from 0 to 1
	// across the upright picture the page shows, and the quarter turns the
	// page turned the stream's picture by to show it.
	A    string  `json:"a,omitempty"`
	X    float64 `json:"x,omitempty"`
	Y    float64 `json:"y,omitempty"`
	Turn int     `json:"turn,omitempty"`
	Key  string  `json:"key,omitempty"` // "back", "home"
}

type deviceInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Model    string `json:"model,omitempty"` // for the bezel: an iPad's corners differ
}

type serverMsg struct {
	Type    string       `json:"type"` // "devices", "config", "state", "error", "ended"
	Devices []deviceInfo `json:"devices,omitempty"`
	Device  string       `json:"device,omitempty"`
	Control bool         `json:"control,omitempty"` // the page may use the phone
	Status  string       `json:"status,omitempty"`  // the phone's gateway.DeviceStatus

	Codec       string `json:"codec,omitempty"`
	Description string `json:"description,omitempty"` // base64
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	Error       string `json:"error,omitempty"` // "token", "control_off", "tailnet_user", "no_phone", "stream"
	Detail      string `json:"detail,omitempty"`
}

// Viewer limits.
const (
	helloTimeout = 10 * time.Second
	writeTimeout = 10 * time.Second
	// idleTimeout is how long a page may send nothing after its hello; the
	// page pings every 20 s (a hidden tab's timers may run once a minute).
	idleTimeout = 90 * time.Second
	// maxConns is how many WebSockets may be open at once, hello or not.
	maxConns = 16
	// maxLag is how far the pictures sent may fall behind the phone before
	// the viewer skips to the next key frame.
	maxLag = 1500 * time.Millisecond
	// statusEvery is how often a page that can use the phone hears of its
	// state.
	statusEvery = time.Second
)

// status is the phone's gateway.DeviceStatus, "" when it is gone.
func (s *Server) status(device string) string {
	for _, d := range s.opt.Source.Devices() {
		if d.Info.ID == device {
			return string(d.Status)
		}
	}
	return ""
}

func (s *Server) serveViewer(ws *websocket.Conn) {
	defer ws.Close()
	ws.MaxPayloadBytes = 4 << 10
	r := ws.Request()
	ws.SetReadDeadline(time.Now().Add(helloTimeout))
	var h hello
	if err := websocket.JSON.Receive(ws, &h); err != nil {
		return
	}
	rl, ok := s.roleOf(h.Token)
	if !ok {
		send(ws, serverMsg{Type: "error", Error: "token"})
		return
	}
	// `tailscale serve` names the tailnet's signed-in person here, and
	// drops a copy the client sent; a tagged device or a Funnel link has
	// none.
	who := r.Header.Get("Tailscale-User-Login")
	if rl == roleControl {
		if !s.controlAllowed() {
			send(ws, serverMsg{Type: "error", Error: "control_off"})
			return
		}
		// Through Tailscale, only the person who owns this computer.
		if proxied(r) && !s.ownerLogin(r.Context(), who) {
			send(ws, serverMsg{Type: "error", Error: "tailnet_user"})
			return
		}
	}
	w := &Watcher{Control: rl == roleControl, Login: who}
	if !s.addConn(ws, w) {
		return
	}
	defer s.removeConn(ws)
	s.log.Info("web mirror: a browser joined", "role", rl, "from", r.RemoteAddr, "tailscale_user", who)
	defer s.log.Info("web mirror: a browser left", "role", rl, "from", r.RemoteAddr, "tailscale_user", who)

	devs, id := s.devices(h.Device)
	if err := send(ws, serverMsg{Type: "devices", Devices: devs, Device: id, Control: rl == roleControl}); err != nil {
		return
	}
	if id == "" {
		send(ws, serverMsg{Type: "error", Error: "no_phone"})
		return
	}
	s.mu.Lock()
	w.Device = id
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := s.opt.Source.Stream(ctx, id, driver.StreamOptions{})
	if err != nil {
		send(ws, serverMsg{Type: "error", Error: "stream", Detail: err.Error()})
		return
	}
	defer st.Close()
	var ss *session
	if rl == roleControl {
		ss = newSession(s, id)
		defer ss.close()
	}
	keyFrames := make(chan struct{}, 1)
	go func() {
		defer cancel()
		for {
			var m clientMsg
			ws.SetReadDeadline(time.Now().Add(idleTimeout))
			if err := websocket.JSON.Receive(ws, &m); err != nil {
				return
			}
			switch m.Type {
			case "keyframe":
				select {
				case keyFrames <- struct{}{}:
				default:
				}
			case "touch", "key":
				if ss == nil || !s.controlAllowed() {
					return // a page that may only watch, or control was turned off
				}
				if m.Type == "key" {
					ss.key(m.Key)
				} else {
					ss.touch(driver.TouchAction(m.A), m.X, m.Y, m.Turn)
				}
			}
		}
	}()
	err = s.pump(ctx, ws, st, keyFrames, id, ss)
	if err == nil {
		send(ws, serverMsg{Type: "ended"})
	}
}

func hostName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}

// pump sends the stream's pictures to the page until either ends. It
// returns nil when the stream ended.
func (s *Server) pump(ctx context.Context, ws *websocket.Conn, st driver.Stream, keyFrames <-chan struct{}, device string, ss *session) error {
	conv := &converter{codec: st.Codec()}
	needKey := true
	var lag lagMeter
	// The phone's state, for a page that can use it: it shows when the
	// person's input paused an agent's task.
	var status string
	var tick <-chan time.Time
	if ss != nil {
		t := time.NewTicker(statusEvery)
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			if now := s.status(device); now != status {
				status = now
				if err := send(ws, serverMsg{Type: "state", Status: now}); err != nil {
					return err
				}
			}
		case <-keyFrames:
			// The page could not decode a picture: start again at a key
			// frame.
			needKey = true
			requestKeyFrame(st)
		case p, ok := <-st.Packets():
			if !ok {
				return nil
			}
			cfg, chunk, err := conv.convert(p)
			if err != nil {
				if !errors.Is(err, errNoConfig) {
					s.log.Warn("web mirror: cannot pass a picture on", "err", err)
				}
				continue
			}
			if cfg != nil {
				if ss != nil {
					ss.setSize(cfg.Width, cfg.Height)
				}
				if err := send(ws, serverMsg{Type: "config", Codec: cfg.Codec, Width: cfg.Width, Height: cfg.Height,
					Description: base64.StdEncoding.EncodeToString(cfg.Description)}); err != nil {
					return err
				}
				needKey = true
			}
			if chunk == nil {
				continue
			}
			behind := lag.behind(p.PTS, time.Now()) > maxLag
			if needKey || behind {
				if !p.KeyFrame || behind {
					if !needKey {
						requestKeyFrame(st)
					}
					needKey = true
					continue
				}
				needKey = false
			}
			ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := websocket.Message.Send(ws, frameMessage(p, chunk)); err != nil {
				return err
			}
		}
	}
}

// lagMeter tells how far behind the phone the pictures are: by how much a
// picture's arrival here, less its time on the phone, exceeds the least
// that difference has been. Pictures queued for a slow browser arrive here
// late; the pictures since the last key frame that a viewer gets at once
// when it joins are older still, but come before any newer one and so
// never look late.
type lagMeter struct {
	base time.Time
	min  time.Duration
	set  bool
}

func (l *lagMeter) behind(pts time.Duration, now time.Time) time.Duration {
	if !l.set {
		l.base, l.set = now, true
		l.min = now.Sub(l.base) - pts
		return 0
	}
	d := now.Sub(l.base) - pts
	if d < l.min {
		l.min = d
	}
	return d - l.min
}

// requestKeyFrame asks the phone for a key frame, when its stream can.
func requestKeyFrame(st driver.Stream) {
	if kr, ok := st.(interface{ RequestKeyFrame() }); ok {
		kr.RequestKeyFrame()
	}
}

// devices lists the phones online and picks the one to show: want when it
// is online, else the first.
func (s *Server) devices(want string) ([]deviceInfo, string) {
	var out []deviceInfo
	pick := ""
	for _, d := range s.opt.Source.Devices() {
		if d.Info.State != driver.StateOnline {
			continue
		}
		out = append(out, deviceInfo{ID: d.Info.ID, Name: d.Info.Name, Platform: string(d.Info.Platform), Model: d.Info.Model})
		if pick == "" || d.Info.ID == want {
			pick = d.Info.ID
		}
	}
	return out, pick
}

func (s *Server) addConn(ws *websocket.Conn, w *Watcher) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || (w.Control && s.controlToken == "") {
		return false
	}
	s.conns[ws] = w
	return true
}

func (s *Server) removeConn(ws *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, ws)
}

func send(ws *websocket.Conn, m serverMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	ws.SetWriteDeadline(time.Now().Add(writeTimeout))
	return websocket.Message.Send(ws, string(b))
}
