// Package traffic is Pinky's own capture of a phone's web requests
// (docs/notes/traffic-capture.md): an HTTP proxy on the computer (Proxy)
// that the phone sends its HTTP and HTTPS through, which decrypts HTTPS
// with a certificate authority Pinky makes for this computer (CA), and
// keeps what it saw in memory (Store) for the gateway's phone_traffic
// tool.
//
// Nothing here changes the phone. Everything captured is untrusted data,
// as screen text is, and passes through Redact* before it reaches an
// agent.
package traffic

import (
	"errors"
	"net/netip"
	"time"
)

// Header is one header line.
type Header struct {
	Name, Value string
}

// Request is one HTTP request the proxy passed on.
type Request struct {
	ID     string // the store's id, stable while the store holds it
	Device string // the phone it came from
	Client netip.Addr
	Start  time.Time
	End    time.Time
	Method string
	// URL is the full URL of a request the proxy read (plain HTTP, or HTTPS
	// it decrypted). For a connection it passed through without reading it
	// is only "https://host:port", with Decrypted false.
	URL       string
	Host      string
	Decrypted bool
	// Blocked: a host Pinky never decrypts or records beyond its host
	// (Blocked); such a request has only its host, start and end.
	Blocked bool
	Status  int // the response's status code; 0 without a response
	Headers []Header
	// ResponseHeaders are the response's headers.
	ResponseHeaders []Header
	Sent, Received  int64 // body bytes, or a tunnel's bytes each way
	Failed          bool
	Note            string // why it failed, or why it was not decrypted
	// Count is how many connections a blocked row stands for: the store
	// keeps blocked connections to one host that follow each other within
	// BlockedGap as one row (an app that keeps reconnecting to a blocked
	// host), from Start to Last. 0 and 1 both mean one.
	Count int
	// Last is when the latest of a blocked row's connections started;
	// zero for a single one.
	Last time.Time
}

// Connections returns how many connections or requests r stands for.
func (r Request) Connections() int { return max(r.Count, 1) }

// Latest returns when the latest of r's connections started: Last, or
// Start for a single one.
func (r Request) Latest() time.Time {
	if r.Last.After(r.Start) {
		return r.Last
	}
	return r.Start
}

// Bodies are one request's bodies as the store kept them: the first
// BodyMax bytes of each, as they went over the wire (compressed when the
// app asked for it).
type Bodies struct {
	Request, Response         []byte
	RequestCut, ResponseCut   bool
	RequestType, ResponseType string // Content-Type
	// RequestEncoding and ResponseEncoding are the Content-Encoding.
	RequestEncoding, ResponseEncoding string
}

// ErrGone is returned for a request the store no longer holds.
var ErrGone = errors.New("Pinky no longer holds this request")

// Query selects a phone's requests.
type Query struct {
	Device string
	// From and To bound when a request started; zero means unbounded.
	From, To time.Time
}

// Wanted reports whether r matches q. A blocked row that stands for
// several connections matches when one of them may have started between
// From and To: its span from Start to Last meets them.
func (q Query) Wanted(r Request) bool {
	if r.Device != q.Device {
		return false
	}
	if !q.From.IsZero() && r.Latest().Before(q.From) {
		return false
	}
	if !q.To.IsZero() && r.Start.After(q.To) {
		return false
	}
	return true
}
