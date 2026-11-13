// Package webapproval lets the person answer Pinky's approvals in a web
// browser on their phone, away from the computer: a page served next to
// the web mirror (internal/webmirror), reached through `tailscale serve`.
// docs/notes/remote-approvals.md has the design and the threat model.
//
// It is off by default. Every request must come through Tailscale from
// the person who owns this computer, and still proves nothing by that
// alone: a process on this computer can send any header to 127.0.0.1.
// What lets a browser answer is a passkey made on the person's phone,
// which the person adds by clicking Allow in Pinky's window after
// comparing a code the phone shows; every answer is a fresh challenge
// that names the approval and the decision, signed by that passkey with
// the person verified (Face ID, a fingerprint, the phone's passcode). An
// agent on this computer has no such passkey. When the person says the
// browser runs on a phone Pinky drives, its answers are refused while an
// agent uses that phone: whoever drives it could tap Allow while the
// person glances at the screen.
//
// Everything lives in memory only: the links that add a phone, the
// requests waiting for the person, the phones added and their session
// tokens. Quitting Pinky or turning the feature off forgets them.
//
// It reaches the gateway through Source, which has Needs and Decide only,
// and answers only what docs/notes/remote-approvals.md lists, with Allow
// once or Deny; the rest stays in Pinky's window.
package webapproval

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image/jpeg"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/approval"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// MaxPhones is how many phones may answer approvals at once.
const MaxPhones = 8

// Path is where the page is, under the web mirror's address.
const Path = "/approvals"

// Source is the gateway, as far as the page reaches it: the open needs,
// answering one, and the phones (to see whether an agent uses the phone a
// browser runs on).
type Source interface {
	Needs() []gateway.NeedView
	Decide(needID string, d gateway.Decision, note string) error
	Devices() []gateway.DeviceView
}

// Options configure a Server.
type Options struct {
	Source Source
	Log    *slog.Logger
	// Owner returns the tailnet login of the person who owns this
	// computer, "" when it has none (a tagged computer): only they may use
	// the page.
	Owner func(ctx context.Context) string
	// Origin returns the origin `tailscale serve` gives the web mirror to
	// the tailnet under, as "https://mac.tail1234.ts.net:8443", "" when it
	// does not. Passkeys are bound to it when a phone is added, never to
	// what a request names.
	Origin func(ctx context.Context) string
	// Proxied reports whether a request came through `tailscale serve`
	// (webmirror.Proxied).
	Proxied func(r *http.Request) bool
	// Text returns the page's words, by key, in the language that best
	// suits the browser's Accept-Language, and that language's tag.
	Text func(acceptLanguage string) (lang string, text map[string]string)
	// OnChange runs, without locks held, when a request to add a phone
	// comes or goes, or a phone is added or removed, so Settings shows it.
	OnChange func()
}

// Limits.
const (
	linkLife      = 5 * time.Minute    // an add-a-phone link, and the request it starts
	challengeLife = 2 * time.Minute    // an answer's challenge, at most; never past the approval's deadline
	sessionIdle   = 12 * time.Hour     // a phone not seen for this long is forgotten
	sessionLife   = 7 * 24 * time.Hour // and any phone after this long
	ownerLife     = 5 * time.Second    // how long the owner's login is kept
	pairRate      = 30                 // failed pair/begin calls a minute, and pair/* calls a minute per pairing
	leaseCooldown = 30 * time.Second   // after an agent's task on a browser's phone ends, its answers are still refused
	maxChallenges = 32                 // challenges open per phone
	maxBody       = 64 << 10           // a request's body
)

// Errors of Allow.
var (
	ErrGone    = errors.New("this request is gone")
	ErrTooMany = fmt.Errorf("at most %d phones can answer approvals; remove one first", MaxPhones)
	// ErrNoDevice is a phone Pinky does not know.
	ErrNoDevice = errors.New("no such phone")
)

// Request is a browser that asks to answer approvals, waiting for the
// person to compare its code and allow it in Pinky's window.
type Request struct {
	// ID names the request in Pinky's window. It is not a secret: what
	// the page holds to finish adding itself is another value.
	ID      string
	Login   string // the tailnet login `tailscale serve` named
	Browser string // read from its User-Agent, from a fixed list, as "iPhone · Safari"; "" when unknown
	Code    string // six digits the page shows too
	Created time.Time
}

// Phone is a browser that may answer approvals.
type Phone struct {
	ID      string
	Login   string
	Browser string
	// Device is the id of the phone connected to Pinky that this browser
	// runs on, as the person said when they allowed it; "" when it runs
	// on another device. Its answers are refused while an agent uses that
	// phone.
	Device   string
	Added    time.Time
	LastSeen time.Time
}

// Server is the approvals page.
type Server struct {
	opt Options
	log *slog.Logger

	ownerMu  sync.Mutex
	owner    string
	ownerAt  time.Time
	pairHits map[string][]time.Time // by pairing, "begin" for failed begins, "unknown" for unknown pairings

	mu         sync.Mutex
	on         bool
	link       string // the one add-a-phone link's token, "" when none
	linkExp    time.Time
	seq        int                   // numbers requests
	pairs      map[string]*pairing   // by the page's secret
	phones     map[[32]byte]*phone   // by the session token's hash
	challenges map[string]*challenge // by the challenge, base64url
}

type pairState int

const (
	pairBegun   pairState = iota // the page is making a passkey
	pairWaiting                  // the person compares the code
	pairAllowed                  // added; the page picks up its session
	pairDenied
)

type pairing struct {
	Request
	state     pairState
	poll      string // the second secret pair/status needs, given with the code
	challenge []byte
	expires   time.Time
	origin    string // where the passkey must be made and used
	rpID      string
	key       *ecdsa.PublicKey
	credID    []byte
	counter   uint32
	session   string // set once allowed, until the page picks it up
}

type phone struct {
	Phone
	key     *ecdsa.PublicKey
	credID  []byte
	origin  string
	rpID    string
	counter uint32 // the passkey's sign counter, 0 when it keeps none
}

type challenge struct {
	phone    [32]byte
	need     string
	decision gateway.Decision
	lease    uint64 // the browser's phone's LeaseGen when the challenge was issued
	value    []byte
	expires  time.Time
	deadline time.Time // the approval's
}

// New returns the approvals page, off.
func New(opt Options) *Server {
	log := opt.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{opt: opt, log: log, pairs: map[string]*pairing{}, phones: map[[32]byte]*phone{},
		challenges: map[string]*challenge{}}
}

// Set turns the page on or off. Off forgets every phone, link and
// request.
func (s *Server) Set(on bool) {
	s.mu.Lock()
	changed := on != s.on
	s.on = on
	if !on {
		s.link, s.linkExp = "", time.Time{}
		clear(s.pairs)
		clear(s.phones)
		clear(s.challenges)
	}
	s.mu.Unlock()
	if changed {
		s.log.Info("approvals in the browser", "on", on)
		s.changed()
	}
}

// On reports whether the page is on.
func (s *Server) On() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

func (s *Server) changed() {
	if s.opt.OnChange != nil {
		s.opt.OnChange()
	}
}

// NewLink makes the token of a link that adds a phone, good once and for
// a few minutes. It replaces an earlier one, and turns down the requests
// still waiting, so only one browser at a time asks. "" while off.
func (s *Server) NewLink() string {
	s.mu.Lock()
	if !s.on {
		s.mu.Unlock()
		return ""
	}
	dropped := false
	for k, p := range s.pairs {
		if p.state == pairBegun || p.state == pairWaiting {
			delete(s.pairs, k)
			dropped = true
		}
	}
	s.link, s.linkExp = rand.Text(), time.Now().Add(linkLife)
	link := s.link
	s.mu.Unlock()
	if dropped {
		s.changed()
	}
	return link
}

// Requests are the browsers waiting for the person to allow them, oldest
// first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	var out []Request
	for _, p := range s.pairs {
		if p.state == pairWaiting {
			out = append(out, p.Request)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *Server) pairByIDLocked(id string) *pairing {
	for _, p := range s.pairs {
		if p.ID == id {
			return p
		}
	}
	return nil
}

// Allow adds the browser of a request; Pinky's window calls it when the
// person clicks Allow, with the phone connected to Pinky the browser runs
// on ("" for another device; ErrNoDevice for a phone Pinky does not know).
// It is the only way a phone is added.
func (s *Server) Allow(id, device string) error {
	if device != "" && !s.knownDevice(device) {
		return ErrNoDevice
	}
	s.mu.Lock()
	now := time.Now()
	s.sweepLocked(now)
	p := s.pairByIDLocked(id)
	if p == nil || p.state != pairWaiting {
		s.mu.Unlock()
		return ErrGone
	}
	if len(s.phones) >= MaxPhones {
		s.mu.Unlock()
		return ErrTooMany
	}
	tok := rand.Text()
	ph := &phone{Phone: Phone{ID: p.ID, Login: p.Login, Browser: p.Browser, Device: device, Added: now, LastSeen: now},
		key: p.key, credID: p.credID, origin: p.origin, rpID: p.rpID, counter: p.counter}
	s.phones[sha256.Sum256([]byte(tok))] = ph
	// The page picks its session up within a few seconds; the request's
	// own time may be nearly over.
	p.state, p.session, p.expires = pairAllowed, tok, now.Add(linkLife)
	s.mu.Unlock()
	s.log.Info("approvals in the browser: phone added", "login", p.Login, "browser", p.Browser, "device", device)
	s.changed()
	return nil
}

// Deny turns a request down.
func (s *Server) Deny(id string) {
	s.mu.Lock()
	p := s.pairByIDLocked(id)
	if p != nil && p.state == pairWaiting {
		p.state = pairDenied
	}
	s.mu.Unlock()
	if p != nil {
		s.changed()
	}
}

// Phones are the browsers that may answer approvals, oldest first.
func (s *Server) Phones() []Phone {
	s.mu.Lock()
	gone := s.expireLocked(time.Now())
	var out []Phone
	for _, p := range s.phones {
		out = append(out, p.Phone)
	}
	s.mu.Unlock()
	if gone {
		s.changed()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Added.Before(out[j].Added) })
	return out
}

// Remove takes a phone's right to answer away.
func (s *Server) Remove(id string) {
	s.mu.Lock()
	found := false
	for h, p := range s.phones {
		if p.ID == id {
			s.dropLocked(h)
			found = true
		}
	}
	s.mu.Unlock()
	if found {
		s.changed()
	}
}

func (s *Server) dropLocked(h [32]byte) {
	delete(s.phones, h)
	for k, c := range s.challenges {
		if c.phone == h {
			delete(s.challenges, k)
		}
	}
}

// expireLocked forgets phones not seen for sessionIdle, or added more
// than sessionLife ago, and reports whether it forgot any.
func (s *Server) expireLocked(now time.Time) bool {
	gone := false
	for h, p := range s.phones {
		if now.Sub(p.LastSeen) > sessionIdle || now.Sub(p.Added) > sessionLife {
			s.dropLocked(h)
			gone = true
		}
	}
	return gone
}

// sweepLocked forgets expired links, requests and challenges.
func (s *Server) sweepLocked(now time.Time) {
	if s.link != "" && now.After(s.linkExp) {
		s.link = ""
	}
	for id, p := range s.pairs {
		if now.After(p.expires) {
			delete(s.pairs, id)
		}
	}
	for k, c := range s.challenges {
		if now.After(c.expires) {
			delete(s.challenges, k)
		}
	}
}

// Answerable reports whether the page may answer a need: an Ask action's
// approval or an agent's own question, on a phone. Not an agent's first
// connection (it needs a scope), opening a blocked app, handoffs,
// unlocking or setup.
func Answerable(n gateway.NeedView) bool {
	return n.Kind == gateway.NeedApproval && (n.Approval == approval.KindAction || n.Approval == approval.KindRequest) &&
		n.DeviceID != "" && !n.Unblock
}

// knownDevice reports whether a phone is connected to Pinky, or was.
func (s *Server) knownDevice(id string) bool {
	for _, d := range s.opt.Source.Devices() {
		if d.Info.ID == id {
			return true
		}
	}
	return false
}

// lease says whether an agent uses the phone the browser runs on, or did
// within leaseCooldown, and the phone's LeaseGen. Then whoever drives it
// could open the page and tap Allow, and a glance at the phone could pass
// Face ID, so its answers are refused. A phone Pinky no longer knows is
// busy too.
func (s *Server) lease(p *phone) (gen uint64, busy bool) {
	if p.Device == "" {
		return 0, false
	}
	for _, d := range s.opt.Source.Devices() {
		if d.Info.ID == p.Device {
			ended := !d.LeaseEnded.IsZero() && time.Since(d.LeaseEnded) < leaseCooldown
			return d.LeaseGen, d.TaskID != "" || ended
		}
	}
	return 0, true
}

func (s *Server) busy(p *phone) bool {
	_, b := s.lease(p)
	return b
}

// Handler serves the page and its API under Path. The web mirror serves
// it behind its own checks (Tailscale Funnel, this computer's names).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+Path, s.servePage)
	mux.HandleFunc("POST "+Path+"/api/pair/begin", s.pairBegin)
	mux.HandleFunc("POST "+Path+"/api/pair/finish", s.pairFinish)
	mux.HandleFunc("POST "+Path+"/api/pair/status", s.pairStatus)
	mux.HandleFunc("POST "+Path+"/api/needs", s.authed(s.needs))
	mux.HandleFunc("POST "+Path+"/api/shot", s.authed(s.shot))
	mux.HandleFunc("POST "+Path+"/api/challenge", s.authed(s.challenge))
	mux.HandleFunc("POST "+Path+"/api/decide", s.authed(s.decide))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		if !s.On() {
			http.NotFound(w, r)
			return
		}
		// Through Tailscale, from the person who owns this computer. A
		// local process can fake both, so this is never enough alone.
		if s.opt.Proxied == nil || !s.opt.Proxied(r) || !s.ownerLogin(r) {
			if r.Method == http.MethodGet {
				s.writePage(w, r, http.StatusForbidden, "tailnet")
				return
			}
			apiError(w, http.StatusForbidden, "tailnet")
			return
		}
		if r.Method == http.MethodPost && !sameOrigin(r) {
			apiError(w, http.StatusForbidden, "origin")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ownerLogin reports whether the request names the owner. The owner's
// login is read from Tailscale at most every few seconds.
func (s *Server) ownerLogin(r *http.Request) bool {
	login := r.Header.Get("Tailscale-User-Login")
	if login == "" || s.opt.Owner == nil {
		return false
	}
	s.ownerMu.Lock()
	owner, fresh := s.owner, time.Since(s.ownerAt) < ownerLife
	s.ownerMu.Unlock()
	if !fresh {
		owner = s.opt.Owner(r.Context())
		s.ownerMu.Lock()
		s.owner, s.ownerAt = owner, time.Now()
		s.ownerMu.Unlock()
	}
	return owner != "" && strings.EqualFold(owner, login)
}

// limit counts a pair/* call under key and reports whether it may go
// on: at most pairRate a minute per key. pair/finish and pair/status count
// per pairing, so calls with made-up ids cannot starve the real page;
// pair/begin counts only calls without the right link.
func (s *Server) limit(key string) bool {
	now := time.Now()
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	if s.pairHits == nil {
		s.pairHits = map[string][]time.Time{}
	}
	for k, hits := range s.pairHits {
		keep := hits[:0]
		for _, t := range hits {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		if len(keep) == 0 {
			delete(s.pairHits, k)
		} else {
			s.pairHits[k] = keep
		}
	}
	if len(s.pairHits[key]) >= pairRate {
		return false
	}
	s.pairHits[key] = append(s.pairHits[key], now)
	return true
}

// pairKey is the key pair/finish and pair/status count under.
func (s *Server) pairKey(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pairs[id]; ok && id != "" {
		return "pair:" + id
	}
	return "unknown"
}

// tailnetOrigin is the origin `tailscale serve` gives the page under, and
// its host name, the passkeys' relying party; "" when it gives none or
// the request did not come to it.
func (s *Server) tailnetOrigin(r *http.Request) (origin, rpID string) {
	if s.opt.Origin == nil {
		return "", ""
	}
	u, err := url.Parse(s.opt.Origin(r.Context()))
	if err != nil || u.Scheme != "https" || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return "", ""
	}
	return "https://" + strings.ToLower(u.Host), strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
}

func sameOrigin(r *http.Request) bool {
	o, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && o.Host != "" && strings.EqualFold(o.Host, r.Host)
}

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("page").Parse(pageHTML))

func (s *Server) servePage(w http.ResponseWriter, r *http.Request) {
	s.writePage(w, r, http.StatusOK, "")
}

// writePage writes the page; fail names an error it shows at once.
func (s *Server) writePage(w http.ResponseWriter, r *http.Request, status int, fail string) {
	lang, text := "en", map[string]string{}
	if s.opt.Text != nil {
		lang, text = s.opt.Text(r.Header.Get("Accept-Language"))
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src blob:; frame-ancestors 'none'")
	w.WriteHeader(status)
	page.Execute(w, struct {
		Lang string
		Text map[string]string
		Fail string
	}{lang, text, fail})
}

type apiErr struct {
	Error string `json:"error"`
}

func apiError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, apiErr{code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody)).Decode(v)
}

// The pairing: the page sends the link's token (begin), makes a passkey
// and sends its public key (finish), then asks until the person allowed or
// denied it in Pinky's window (status). Begin gives the page a secret
// that finish needs; finish gives it a second one that status needs, so
// only the page that made the passkey picks up the session.

type pairBeginReq struct {
	Link string `json:"link"`
}

type pairBeginResp struct {
	ID        string `json:"id"` // a secret, the page's own
	Challenge string `json:"challenge"`
	RPID      string `json:"rpId"`
	User      string `json:"user"`
}

func (s *Server) pairBegin(w http.ResponseWriter, r *http.Request) {
	var req pairBeginReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	origin, rp := s.tailnetOrigin(r)
	if origin == "" {
		apiError(w, http.StatusForbidden, "tailnet")
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.sweepLocked(now)
	if s.link == "" || req.Link == "" || !constEq(req.Link, s.link) {
		s.mu.Unlock()
		if !s.limit("begin") {
			apiError(w, http.StatusTooManyRequests, "slow_down")
			return
		}
		apiError(w, http.StatusForbidden, "link")
		return
	}
	s.link = "" // once
	s.seq++
	ch := random(32)
	secret := rand.Text()
	p := &pairing{Request: Request{ID: fmt.Sprintf("req-%d", s.seq), Login: r.Header.Get("Tailscale-User-Login"), Created: now},
		challenge: ch, expires: now.Add(linkLife), origin: origin, rpID: rp}
	s.pairs[secret] = p
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, pairBeginResp{ID: secret, Challenge: b64.EncodeToString(ch), RPID: rp,
		User: b64.EncodeToString(random(16))})
}

type pairFinishReq struct {
	ID           string `json:"id"`
	CredentialID string `json:"credentialId"`
	PublicKey    string `json:"publicKey"` // SPKI, base64url
	Alg          int    `json:"alg"`
	ClientData   string `json:"clientData"`
	AuthData     string `json:"authData"`
}

type pairFinishResp struct {
	Code string `json:"code"`
	Poll string `json:"poll"` // what pair/status needs besides the id
}

func (s *Server) pairFinish(w http.ResponseWriter, r *http.Request) {
	var req pairFinishReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.limit(s.pairKey(req.ID)) {
		apiError(w, http.StatusTooManyRequests, "slow_down")
		return
	}
	cred, e1 := b64.DecodeString(req.CredentialID)
	spki, e2 := b64.DecodeString(req.PublicKey)
	cd, e3 := b64.DecodeString(req.ClientData)
	ad, e4 := b64.DecodeString(req.AuthData)
	if errors.Join(e1, e2, e3, e4) != nil || len(cred) == 0 || len(cred) > 1024 {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	s.mu.Lock()
	s.sweepLocked(time.Now())
	p, ok := s.pairs[req.ID]
	if !ok || req.ID == "" || p.state != pairBegun || !strings.EqualFold(p.Login, r.Header.Get("Tailscale-User-Login")) {
		s.mu.Unlock()
		apiError(w, http.StatusForbidden, "gone")
		return
	}
	ch, origin, rp := p.challenge, p.origin, p.rpID
	s.mu.Unlock()

	key, err := parseKey(spki)
	if err == nil && req.Alg != algES256 {
		err = errKey
	}
	if err == nil {
		err = checkClientData(cd, "webauthn.create", ch, origin)
	}
	if err == nil {
		err = checkAuthData(ad, rp)
	}
	if err != nil {
		s.log.Warn("approvals in the browser: a passkey was refused", "err", err, "login", r.Header.Get("Tailscale-User-Login"))
		s.mu.Lock()
		delete(s.pairs, req.ID)
		s.mu.Unlock()
		apiError(w, http.StatusForbidden, "passkey")
		return
	}
	s.mu.Lock()
	if s.pairs[req.ID] != p || p.state != pairBegun {
		s.mu.Unlock()
		apiError(w, http.StatusForbidden, "gone")
		return
	}
	p.state, p.key, p.credID, p.counter = pairWaiting, key, cred, signCount(ad)
	p.Browser = browserName(r.UserAgent())
	p.Code = code()
	p.poll = rand.Text()
	resp := pairFinishResp{Code: p.Code, Poll: p.poll}
	s.mu.Unlock()
	s.log.Info("approvals in the browser: a browser asks to be added", "login", p.Login, "browser", p.Browser)
	s.changed()
	writeJSON(w, http.StatusOK, resp)
}

type pairStatusReq struct {
	ID   string `json:"id"`
	Poll string `json:"poll"`
}

type pairStatusResp struct {
	State   string `json:"state"` // "waiting", "allowed", "denied", "gone"
	Session string `json:"session,omitempty"`
}

func (s *Server) pairStatus(w http.ResponseWriter, r *http.Request) {
	var req pairStatusReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if !s.limit(s.pairKey(req.ID)) {
		apiError(w, http.StatusTooManyRequests, "slow_down")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(time.Now())
	p, ok := s.pairs[req.ID]
	if !ok || req.ID == "" || p.poll == "" || !constEq(req.Poll, p.poll) || !strings.EqualFold(p.Login, r.Header.Get("Tailscale-User-Login")) {
		writeJSON(w, http.StatusOK, pairStatusResp{State: "gone"})
		return
	}
	switch p.state {
	case pairAllowed:
		// The session token goes to the page once.
		delete(s.pairs, req.ID)
		writeJSON(w, http.StatusOK, pairStatusResp{State: "allowed", Session: p.session})
	case pairDenied:
		delete(s.pairs, req.ID)
		writeJSON(w, http.StatusOK, pairStatusResp{State: "denied"})
	default:
		writeJSON(w, http.StatusOK, pairStatusResp{State: "waiting"})
	}
}

// authed runs h for a request with an added phone's session token.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request, [32]byte, *phone)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || tok == "" {
			apiError(w, http.StatusUnauthorized, "session")
			return
		}
		hash := sha256.Sum256([]byte(tok))
		now := time.Now()
		s.mu.Lock()
		gone := s.expireLocked(now)
		p, ok := s.phones[hash]
		if ok {
			p.LastSeen = now
		}
		s.mu.Unlock()
		if gone {
			s.changed()
		}
		if !ok {
			apiError(w, http.StatusUnauthorized, "session")
			return
		}
		h(w, r, hash, p)
	}
}

// needView is a card on the page: what the panel shows.
type needView struct {
	ID         string         `json:"id"`
	Phone      string         `json:"phone"`
	Agent      string         `json:"agent"`
	Title      string         `json:"title"`
	Detail     string         `json:"detail,omitempty"`
	Rule       string         `json:"rule,omitempty"`
	App        string         `json:"app,omitempty"`
	Created    int64          `json:"created"`  // ms since the epoch
	Deadline   int64          `json:"deadline"` // ms since the epoch
	Answerable bool           `json:"answerable"`
	Shot       *shotView      `json:"shot,omitempty"`
	Target     *screen.Rect   `json:"target,omitempty"`
	Points     []screen.Point `json:"points,omitempty"` // the finger's: Point, the path, To
	Points2    []screen.Point `json:"points2,omitempty"`
}

type shotView struct {
	W int `json:"w"`
	H int `json:"h"`
}

type needsResp struct {
	Now   int64      `json:"now"`
	Busy  bool       `json:"busy,omitempty"` // an agent uses the phone this browser runs on
	Needs []needView `json:"needs"`
}

func (s *Server) needs(w http.ResponseWriter, r *http.Request, _ [32]byte, p *phone) {
	out := needsResp{Now: time.Now().UnixMilli(), Busy: s.busy(p), Needs: []needView{}}
	if out.Busy {
		// Nothing at all: whoever drives the phone would see it.
		writeJSON(w, http.StatusOK, out)
		return
	}
	for _, n := range s.opt.Source.Needs() {
		if n.Kind != gateway.NeedApproval {
			continue
		}
		v := needView{ID: n.ID, Phone: n.DeviceName, Agent: n.Agent, Title: n.Title, Detail: n.Detail, Rule: n.Rule, App: n.App,
			Created: n.Created.UnixMilli(), Deadline: n.Deadline.UnixMilli(), Answerable: Answerable(n)}
		// The screenshot only of what the page may answer.
		if n.Screenshot != nil && Answerable(n) {
			b := n.Screenshot.Bounds()
			v.Shot = &shotView{W: b.Dx(), H: b.Dy()}
			if n.Target.W > 0 && n.Target.H > 0 {
				t := n.Target
				v.Target = &t
			}
			v.Points = fingerPoints(n.Point, n.Path, n.To)
			v.Points2 = fingerPoints(n.Point2, nil, n.To2)
		}
		out.Needs = append(out.Needs, v)
	}
	writeJSON(w, http.StatusOK, out)
}

// fingerPoints lists where a finger goes: none when the action has no
// position.
func fingerPoints(p screen.Point, path []screen.Point, to screen.Point) []screen.Point {
	if len(path) > 0 {
		return path
	}
	if p == (screen.Point{}) {
		return nil
	}
	if to == (screen.Point{}) || to == p {
		return []screen.Point{p}
	}
	return []screen.Point{p, to}
}

type idReq struct {
	ID string `json:"id"`
}

// shot sends the screenshot of an approval the page may answer, as JPEG.
func (s *Server) shot(w http.ResponseWriter, r *http.Request, _ [32]byte, p *phone) {
	var req idReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if s.busy(p) {
		apiError(w, http.StatusForbidden, "busy")
		return
	}
	n, ok := s.need(req.ID)
	if !ok || n.Screenshot == nil || !Answerable(n) {
		apiError(w, http.StatusNotFound, "gone")
		return
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, n.Screenshot, &jpeg.Options{Quality: 80}); err != nil {
		apiError(w, http.StatusInternalServerError, "shot")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(buf.Bytes())
}

func (s *Server) need(id string) (gateway.NeedView, bool) {
	for _, n := range s.opt.Source.Needs() {
		if n.ID == id && n.Kind == gateway.NeedApproval {
			return n, true
		}
	}
	return gateway.NeedView{}, false
}

type challengeReq struct {
	ID       string `json:"id"`
	Decision string `json:"decision"` // "allow" or "deny"
}

type challengeResp struct {
	Challenge    string `json:"challenge"`
	CredentialID string `json:"credentialId"`
	RPID         string `json:"rpId"`
	Expires      int64  `json:"expires"` // ms since the epoch
}

// decisionOf maps the page's two answers to the gateway's.
func decisionOf(s string) (gateway.Decision, bool) {
	switch s {
	case "allow":
		return gateway.AllowOnce, true
	case "deny":
		return gateway.Deny, true
	}
	return "", false
}

// challenge issues the challenge that the phone's passkey signs to give
// one answer to one approval.
func (s *Server) challenge(w http.ResponseWriter, r *http.Request, hash [32]byte, p *phone) {
	var req challengeReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	d, ok := decisionOf(req.Decision)
	if !ok {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	n, ok := s.need(req.ID)
	if !ok {
		apiError(w, http.StatusNotFound, "gone")
		return
	}
	if !Answerable(n) {
		apiError(w, http.StatusForbidden, "computer")
		return
	}
	gen, busy := s.lease(p)
	if busy {
		apiError(w, http.StatusForbidden, "busy")
		return
	}
	now := time.Now()
	exp := now.Add(challengeLife)
	if n.Deadline.Before(exp) {
		exp = n.Deadline
	}
	v := random(32)
	k := b64.EncodeToString(v)
	s.mu.Lock()
	s.sweepLocked(now)
	if _, still := s.phones[hash]; !still {
		s.mu.Unlock()
		apiError(w, http.StatusUnauthorized, "session")
		return
	}
	mine := 0
	var oldest string
	for ck, c := range s.challenges {
		if c.phone == hash {
			mine++
			if oldest == "" || c.expires.Before(s.challenges[oldest].expires) {
				oldest = ck
			}
		}
	}
	if mine >= maxChallenges {
		delete(s.challenges, oldest)
	}
	s.challenges[k] = &challenge{phone: hash, need: n.ID, decision: d, lease: gen, value: v, expires: exp, deadline: n.Deadline}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, challengeResp{Challenge: k, CredentialID: b64.EncodeToString(p.credID), RPID: p.rpID,
		Expires: exp.UnixMilli()})
}

type decideReq struct {
	Challenge    string `json:"challenge"`
	CredentialID string `json:"credentialId"`
	ClientData   string `json:"clientData"`
	AuthData     string `json:"authData"`
	Signature    string `json:"signature"`
}

type decideResp struct {
	// Result is "done", or "gone": already answered elsewhere, or the time
	// ran out (Late says which).
	Result string `json:"result"`
	Late   bool   `json:"late,omitempty"`
}

// decide checks the passkey's signature over the challenge and gives the
// answer to the gateway, as the panel does.
func (s *Server) decide(w http.ResponseWriter, r *http.Request, hash [32]byte, p *phone) {
	var req decideReq
	if readJSON(r, &req) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	cred, e1 := b64.DecodeString(req.CredentialID)
	cd, e2 := b64.DecodeString(req.ClientData)
	ad, e3 := b64.DecodeString(req.AuthData)
	sig, e4 := b64.DecodeString(req.Signature)
	if errors.Join(e1, e2, e3, e4) != nil {
		apiError(w, http.StatusBadRequest, "bad_request")
		return
	}
	now := time.Now()
	s.mu.Lock()
	c, ok := s.challenges[req.Challenge]
	if ok {
		delete(s.challenges, req.Challenge) // once, whatever comes of it
	}
	s.mu.Unlock()
	if !ok || c.phone != hash {
		apiError(w, http.StatusForbidden, "challenge")
		return
	}
	if now.After(c.expires) {
		writeJSON(w, http.StatusOK, decideResp{Result: "gone", Late: !now.Before(c.deadline)})
		return
	}
	if !bytes.Equal(cred, p.credID) {
		apiError(w, http.StatusForbidden, "passkey")
		return
	}
	// The origin and RP id stored when the phone was added, never what
	// this request names.
	if err := (assertion{ClientData: cd, AuthData: ad, Signature: sig}).verify(p.key, c.value, p.origin, p.rpID); err != nil {
		s.log.Warn("approvals in the browser: an answer was refused", "err", err, "login", p.Login, "browser", p.Browser)
		apiError(w, http.StatusForbidden, "passkey")
		return
	}
	// Still added (Remove, the switch, expiry may have come meanwhile),
	// and a sign counter that went back means a copied passkey.
	s.mu.Lock()
	if s.phones[hash] != p {
		s.mu.Unlock()
		apiError(w, http.StatusUnauthorized, "session")
		return
	}
	n := signCount(ad)
	if (n != 0 || p.counter != 0) && n <= p.counter {
		s.mu.Unlock()
		s.log.Warn("approvals in the browser: a passkey's counter went back", "login", p.Login, "browser", p.Browser)
		apiError(w, http.StatusForbidden, "passkey")
		return
	}
	p.counter = n
	s.mu.Unlock()
	// No agent on the browser's phone now, within leaseCooldown, or at any
	// time since the challenge (a task that started and ended between the
	// tap and now changed LeaseGen). This is read just before Decide, not
	// under one lock with it: a task that starts in between did not tap.
	if gen, busy := s.lease(p); busy || gen != c.lease {
		apiError(w, http.StatusForbidden, "busy")
		return
	}
	// Check again what the approval is: the same id stays the same
	// approval, but answer only what the page may.
	nv, ok := s.need(c.need)
	if !ok {
		writeJSON(w, http.StatusOK, decideResp{Result: "gone", Late: !time.Now().Before(c.deadline)})
		return
	}
	if !Answerable(nv) {
		apiError(w, http.StatusForbidden, "computer")
		return
	}
	if err := s.opt.Source.Decide(c.need, c.decision, ""); err != nil {
		writeJSON(w, http.StatusOK, decideResp{Result: "gone", Late: !time.Now().Before(c.deadline)})
		return
	}
	s.log.Info("approvals in the browser: answered", "approval", c.need, "decision", c.decision,
		"login", p.Login, "browser", p.Browser, "title", nv.Title)
	writeJSON(w, http.StatusOK, decideResp{Result: "done"})
}

// signCount is the authenticator data's sign counter.
func signCount(ad []byte) uint32 {
	if len(ad) < 37 {
		return 0
	}
	return binary.BigEndian.Uint32(ad[33:37])
}

// browserName names a browser from its User-Agent, from a fixed list
// only: what a page sends is never shown as it is.
func browserName(ua string) string {
	dev := ""
	switch {
	case strings.Contains(ua, "iPad"):
		dev = "iPad"
	case strings.Contains(ua, "iPhone"):
		dev = "iPhone"
	case strings.Contains(ua, "Android"):
		dev = "Android"
	case strings.Contains(ua, "Mac OS X"):
		dev = "Mac"
	case strings.Contains(ua, "Windows"):
		dev = "Windows"
	case strings.Contains(ua, "Linux"):
		dev = "Linux"
	}
	br := ""
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgA/") || strings.Contains(ua, "EdgiOS/"):
		br = "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		br = "Firefox"
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		br = "Chrome"
	case strings.Contains(ua, "Safari/"):
		br = "Safari"
	}
	switch {
	case dev != "" && br != "":
		return dev + " · " + br
	case dev != "":
		return dev
	}
	return br
}

func random(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// code is six random digits.
func code() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
	return fmt.Sprintf("%06d", n.Int64())
}

func constEq(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return ha == hb
}
