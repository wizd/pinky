// Package rppair speaks RemotePairing to an iPhone over TCP: the protocol
// remotepairingd serves to network hosts on port 49152, and on the phone's own
// loopback when a packet tunnel reflects 10.7.0.1 back into the phone (see
// ios/core). It verifies a pairing, asks the phone for a TCP tunnel
// listener and opens the TLS-PSK stream that carries the CoreDevice tunnel.
// Setup creates the pairing in the first place, over lockdown from a computer
// the phone already trusts.
//
// The control channel is the one go-ios speaks over RemoteXPC
// (ios/tunnel/untrusted.go), framed for TCP: "RPPairing", a big-endian 16-bit
// length and a JSON envelope, with binary values in base64.
package rppair

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/danielpaulus/go-ios/ios/tunnel/tlspsk"
	"golang.org/x/crypto/chacha20poly1305"
)

// Port is where remotepairingd listens.
const Port = 49152

// magic starts every control channel frame.
const magic = "RPPairing"

// wireProtocolVersion is what go-ios and pymobiledevice3 announce.
const wireProtocolVersion = 19

// Session is a verified control channel to a phone. Keep it open while the
// tunnel it set up is in use.
type Session struct {
	conn io.ReadWriteCloser
	addr string // host:port it was dialed at; "" when given a stream
	r    *bufio.Reader
	seq  uint64 // sequence number of the next frame we send
	// origin is who we are in the conversation, "host" unless we answer a
	// phone that pairs with us (Respond): "device".
	origin string

	secret         []byte // X25519 shared secret from pair-verify; the TLS PSK
	client, server cipher.AEAD
	encSeq         uint64 // nonce counter of the encrypted stream

	// Peer is what the phone said about itself in the handshake
	// (peerDeviceInfo), for diagnostics.
	Peer map[string]any
}

// Dial connects to addr (host:port), says hello and verifies the pairing of
// id. A phone that does not know id fails with an error wrapping
// ErrUnknownPeer.
func Dial(ctx context.Context, addr string, id Identity) (*Session, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	s, err := Verify(ctx, conn, id)
	if err != nil {
		conn.Close()
		return nil, err
	}
	s.addr = addr
	return s, nil
}

// Verify runs the hello and pair-verify on conn.
func Verify(ctx context.Context, conn io.ReadWriteCloser, id Identity) (*Session, error) {
	s := newSession(conn)
	defer s.deadline(ctx)()
	if err := s.hello(); err != nil {
		return nil, fmt.Errorf("hello: %w", err)
	}
	if err := s.verify(id); err != nil {
		return nil, fmt.Errorf("pair-verify: %w", err)
	}
	return s, nil
}

func newSession(conn io.ReadWriteCloser) *Session {
	return &Session{conn: conn, r: bufio.NewReader(conn)}
}

// deadline applies ctx's deadline to the connection when it takes one, and
// returns what clears it.
func (s *Session) deadline(ctx context.Context) func() {
	dc, ok := s.conn.(interface{ SetDeadline(time.Time) error })
	dl, has := ctx.Deadline()
	if !ok || !has {
		return func() {}
	}
	dc.SetDeadline(dl)
	return func() { dc.SetDeadline(time.Time{}) }
}

// Close closes the control channel.
func (s *Session) Close() error { return s.conn.Close() }

// SharedSecret is the pair-verify secret, the TLS pre-shared key.
func (s *Session) SharedSecret() []byte { return s.secret }

// Tunnel asks the phone for a TCP tunnel listener, dials it with dial and
// returns the TLS-PSK stream on it. The stream starts with the CDTunnel
// exchange (Pinky's internal/driver/ios does that part).
func (s *Session) Tunnel(ctx context.Context, dial func(ctx context.Context, port int) (net.Conn, error)) (net.Conn, error) {
	clear := s.deadline(ctx)
	port, err := s.CreateListener()
	clear()
	if err != nil {
		return nil, fmt.Errorf("create listener: %w", err)
	}
	c, err := dial(ctx, port)
	if err != nil {
		return nil, fmt.Errorf("dial tunnel port %d: %w", port, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	tc, err := tlspsk.Client(c, s.secret)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("TLS-PSK: %w", err)
	}
	c.SetDeadline(time.Time{})
	return tc, nil
}

// DialTunnel is Tunnel to the host Dial connected to.
func (s *Session) DialTunnel(ctx context.Context) (net.Conn, error) {
	host, _, err := net.SplitHostPort(s.addr)
	if err != nil {
		return nil, fmt.Errorf("session address %q: %w", s.addr, err)
	}
	return s.Tunnel(ctx, func(ctx context.Context, port int) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	})
}

// CreateListener asks the phone to open a TCP tunnel listener keyed to the
// shared secret and returns its port.
func (s *Session) CreateListener() (int, error) {
	err := s.writeEncrypted(map[string]any{
		"request": map[string]any{"_0": map[string]any{
			"createListener": map[string]any{
				"key":                   s.secret, // base64 in JSON
				"transportProtocolType": "tcp",
			},
		}},
	})
	if err != nil {
		return 0, err
	}
	res, err := s.readEncrypted()
	if err != nil {
		return 0, err
	}
	cl, err := child(res, "response", "_1", "createListener")
	if err != nil {
		return 0, fmt.Errorf("%w (reply %s)", err, brief(res))
	}
	port, ok := cl["port"].(float64)
	if !ok || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("no port in reply %s", brief(res))
	}
	return int(port), nil
}

func (s *Session) hello() error {
	err := s.writePlain("request", map[string]any{
		"handshake": map[string]any{"_0": map[string]any{
			"hostOptions":         map[string]any{"attemptPairVerify": true},
			"wireProtocolVersion": wireProtocolVersion,
		}},
	})
	if err != nil {
		return err
	}
	m, err := s.read()
	if err != nil {
		return err
	}
	if h, err := child(m, "plain", "_0", "response", "_1", "handshake", "_0"); err == nil {
		if p, ok := h["peerDeviceInfo"].(map[string]any); ok {
			s.Peer = p
		}
	}
	return nil
}

// ErrUnknownPeer is the phone saying it has no pairing with this identity.
var ErrUnknownPeer = errors.New("the phone does not know this host")

func (s *Session) verify(id Identity) error {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	start := tlv{}
	start.add(tState, []byte{stateM1})
	start.add(tPublicKey, key.PublicKey().Bytes())
	if err := s.writePairing("verifyManualPairing", start.bytes(), true); err != nil {
		return err
	}
	m2, err := s.readPairing()
	if err != nil {
		return err
	}
	devPub := tlvRead(m2, tPublicKey)
	if len(devPub) == 0 {
		s.writeEvent(map[string]any{"pairVerifyFailed": map[string]any{}})
		if e := tlvRead(m2, tError); len(e) > 0 {
			return tlvError(e[0])
		}
		return errors.New("no public key from the phone")
	}
	dp, err := ecdh.X25519().NewPublicKey(devPub)
	if err != nil {
		return err
	}
	secret, err := key.ECDH(dp)
	if err != nil {
		return err
	}
	k, err := hkdf.Key(sha512.New, secret, []byte("Pair-Verify-Encrypt-Salt"), "Pair-Verify-Encrypt-Info", 32)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.New(k)
	if err != nil {
		return err
	}
	var signed bytes.Buffer
	signed.Write(key.PublicKey().Bytes())
	signed.WriteString(id.Identifier)
	signed.Write(devPub)
	inner := tlv{}
	inner.add(tSignature, ed25519.Sign(id.PrivateKey, signed.Bytes()))
	inner.add(tIdentifier, []byte(id.Identifier))
	m3 := tlv{}
	m3.add(tState, []byte{stateM3})
	m3.add(tEncryptedData, aead.Seal(nil, nonce("PV-Msg03"), inner.bytes(), nil))
	if err := s.writePairing("verifyManualPairing", m3.bytes(), false); err != nil {
		return err
	}
	m4, err := s.readPairing()
	if err != nil {
		return err
	}
	if e := tlvRead(m4, tError); len(e) > 0 {
		s.writeEvent(map[string]any{"pairVerifyFailed": map[string]any{}})
		return tlvError(e[0])
	}
	s.secret = secret
	ck, err := hkdf.Key(sha512.New, secret, nil, "ClientEncrypt-main", 32)
	if err != nil {
		return err
	}
	sk, err := hkdf.Key(sha512.New, secret, nil, "ServerEncrypt-main", 32)
	if err != nil {
		return err
	}
	if s.client, err = chacha20poly1305.New(ck); err != nil {
		return err
	}
	if s.server, err = chacha20poly1305.New(sk); err != nil {
		return err
	}
	return nil
}

// nonce is a 12-byte ChaCha20-Poly1305 nonce ending in label.
func nonce(label string) []byte {
	n := make([]byte, 12)
	copy(n[4:], label)
	return n
}

func (s *Session) writePairing(kind string, data []byte, newSession bool) error {
	return s.writeEvent(map[string]any{
		"pairingData": map[string]any{"_0": map[string]any{
			"data":            data,
			"kind":            kind,
			"sendingHost":     hostName,
			"startNewSession": newSession,
		}},
	})
}

// hostName is what Pinky calls itself to the phone.
const hostName = "Pinky standalone"

// readPairing reads the next pairing message. Other events before it (the
// phone waiting for consent) are skipped; a rejection is an error.
func (s *Session) readPairing() ([]byte, error) {
	for {
		m, err := s.read()
		if err != nil {
			return nil, err
		}
		ev, err := child(m, "plain", "_0", "event", "_0")
		if err != nil {
			return nil, fmt.Errorf("%w (got %s)", err, brief(m))
		}
		if pd, err := child(ev, "pairingData", "_0"); err == nil {
			return bytesOf(pd["data"])
		}
		if _, ok := ev["pairingRejectedWithError"]; ok {
			return nil, fmt.Errorf("pairing rejected: %s", brief(ev))
		}
		// awaitingUserConsent and the like: wait for the next message.
	}
}

func (s *Session) writeEvent(e map[string]any) error {
	return s.writePlain("event", e)
}

func (s *Session) writePlain(kind string, body map[string]any) error {
	return s.write(map[string]any{"plain": map[string]any{"_0": map[string]any{kind: map[string]any{"_0": body}}}})
}

func (s *Session) writeEncrypted(body map[string]any) error {
	plain, err := json.Marshal(body)
	if err != nil {
		return err
	}
	n := make([]byte, 12)
	binary.LittleEndian.PutUint64(n, s.encSeq)
	sealed := s.client.Seal(nil, n, plain, nil)
	return s.write(map[string]any{"streamEncrypted": map[string]any{"_0": sealed}})
}

// readEncrypted reads the reply to the last writeEncrypted: it uses the same
// nonce, and the counter moves on after it.
func (s *Session) readEncrypted() (map[string]any, error) {
	m, err := s.read()
	if err != nil {
		return nil, err
	}
	enc, err := child(m, "streamEncrypted")
	if err != nil {
		return nil, fmt.Errorf("%w (got %s)", err, brief(m))
	}
	sealed, err := bytesOf(enc["_0"])
	if err != nil {
		return nil, err
	}
	n := make([]byte, 12)
	binary.LittleEndian.PutUint64(n, s.encSeq)
	s.encSeq++
	plain, err := s.server.Open(nil, n, sealed, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Session) write(message map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"message":        message,
		"originatedBy":   cmp.Or(s.origin, "host"),
		"sequenceNumber": s.seq,
	})
	if err != nil {
		return err
	}
	s.seq++
	return writeFrame(s.conn, body)
}

func (s *Session) read() (map[string]any, error) {
	body, err := readFrame(s.r)
	if err != nil {
		return nil, err
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}
	m, ok := env["message"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("frame without a message: %s", brief(env))
	}
	return m, nil
}

func writeFrame(w io.Writer, body []byte) error {
	if len(body) > 0xffff {
		return fmt.Errorf("frame of %d bytes is too long", len(body))
	}
	buf := make([]byte, 0, len(magic)+2+len(body))
	buf = append(buf, magic...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(body)))
	buf = append(buf, body...)
	_, err := w.Write(buf)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	hdr := make([]byte, len(magic)+2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if string(hdr[:len(magic)]) != magic {
		return nil, fmt.Errorf("bad frame magic %q", hdr[:len(magic)])
	}
	body := make([]byte, binary.BigEndian.Uint16(hdr[len(magic):]))
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func child(m map[string]any, keys ...string) (map[string]any, error) {
	for _, k := range keys {
		c, ok := m[k].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("no %q in message", k)
		}
		m = c
	}
	return m, nil
}

// bytesOf reads a binary value: base64 in JSON.
func bytesOf(v any) ([]byte, error) {
	switch x := v.(type) {
	case string:
		return base64.StdEncoding.DecodeString(x)
	case []byte:
		return x, nil
	}
	return nil, fmt.Errorf("binary value of type %T", v)
}

// brief is a short rendering of a message for errors.
func brief(v any) string {
	b, _ := json.Marshal(v)
	if len(b) > 300 {
		b = append(b[:300], "…"...)
	}
	return string(b)
}
