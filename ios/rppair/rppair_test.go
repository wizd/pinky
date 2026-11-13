package rppair

import (
	"bufio"
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/opack"
	"github.com/tadglines/go-pkgs/crypto/srp"
	"golang.org/x/crypto/chacha20poly1305"
)

// phone plays the device side of the control channel well enough for these
// tests: the handshake, pair-verify, pair-setup with the lockdown PIN and
// createListener.
type phone struct {
	t      *testing.T
	conn   net.Conn
	r      *bufio.Reader
	seq    uint64
	hosts  map[string]ed25519.PublicKey // pairings it keeps
	irk    []byte
	port   int
	secret []byte

	srp *srp.ServerSession
	key []byte // pair-setup session key
}

func newPhone(t *testing.T, conn net.Conn) *phone {
	irk := make([]byte, 16)
	rand.Read(irk)
	return &phone{t: t, conn: conn, r: bufio.NewReader(conn), hosts: map[string]ed25519.PublicKey{}, irk: irk, port: 54321}
}

func (p *phone) send(message map[string]any) {
	body, _ := json.Marshal(map[string]any{"message": message, "originatedBy": "device", "sequenceNumber": p.seq})
	p.seq++
	if err := writeFrame(p.conn, body); err != nil {
		p.t.Logf("phone write: %v", err)
	}
}

func (p *phone) sendPairing(data []byte) {
	p.send(map[string]any{"plain": map[string]any{"_0": map[string]any{"event": map[string]any{"_0": map[string]any{
		"pairingData": map[string]any{"_0": map[string]any{"data": data, "kind": "x", "startNewSession": false}},
	}}}}})
}

// serve answers until the host closes the stream.
func (p *phone) serve() {
	var verifyKey *ecdh.PrivateKey
	var hostPub []byte
	var encSeq uint64
	for {
		body, err := readFrame(p.r)
		if err != nil {
			return
		}
		var env struct {
			Message        map[string]any `json:"message"`
			SequenceNumber uint64         `json:"sequenceNumber"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			p.t.Errorf("phone: bad frame: %v", err)
			return
		}
		if enc, err := child(env.Message, "streamEncrypted"); err == nil {
			p.encrypted(enc, encSeq)
			encSeq++
			continue
		}
		if _, err := child(env.Message, "plain", "_0", "request", "_0", "handshake"); err == nil {
			p.send(map[string]any{"plain": map[string]any{"_0": map[string]any{"response": map[string]any{"_1": map[string]any{
				"handshake": map[string]any{"_0": map[string]any{"peerDeviceInfo": map[string]any{"name": "Fake iPhone"}}},
			}}}}})
			continue
		}
		ev, err := child(env.Message, "plain", "_0", "event", "_0")
		if err != nil {
			p.t.Errorf("phone: unexpected %s", brief(env.Message))
			return
		}
		pd, err := child(ev, "pairingData", "_0")
		if err != nil {
			continue // pairVerifyFailed and other events
		}
		data, _ := bytesOf(pd["data"])
		kind, _ := pd["kind"].(string)
		state := tlvRead(data, tState)
		switch {
		case kind == "verifyManualPairing" && state[0] == stateM1:
			verifyKey, _ = ecdh.X25519().GenerateKey(rand.Reader)
			hostPub = tlvRead(data, tPublicKey)
			m := tlv{}
			m.add(tState, []byte{stateM2})
			m.add(tPublicKey, verifyKey.PublicKey().Bytes())
			p.sendPairing(m.bytes())
		case kind == "verifyManualPairing" && state[0] == stateM3:
			hp, _ := ecdh.X25519().NewPublicKey(hostPub)
			secret, _ := verifyKey.ECDH(hp)
			k, _ := hkdf.Key(sha512.New, secret, []byte("Pair-Verify-Encrypt-Salt"), "Pair-Verify-Encrypt-Info", 32)
			aead, _ := chacha20poly1305.New(k)
			m := tlv{}
			m.add(tState, []byte{stateM4})
			inner, err := aead.Open(nil, nonce("PV-Msg03"), tlvRead(data, tEncryptedData), nil)
			id := string(tlvRead(inner, tIdentifier))
			var signed bytes.Buffer
			signed.Write(hostPub)
			signed.WriteString(id)
			signed.Write(verifyKey.PublicKey().Bytes())
			pub, known := p.hosts[id]
			if err != nil || !known || !ed25519.Verify(pub, signed.Bytes(), tlvRead(inner, tSignature)) {
				m.add(tError, []byte{2}) // authentication, as the phone answers strangers
			} else {
				p.secret = secret
			}
			p.sendPairing(m.bytes())
		case kind == "setupManualPairing" && state[0] == stateM1:
			s, _ := newSRP()
			salt, verifier, _ := s.ComputeVerifier([]byte(lockdownPIN))
			p.srp = s.NewServerSession([]byte("Pair-Setup"), salt, verifier)
			m := tlv{}
			m.add(tState, []byte{stateM2})
			m.add(tSalt, salt)
			m.add(tPublicKey, p.srp.GetB())
			p.sendPairing(m.bytes())
		case kind == "setupManualPairing" && state[0] == stateM3:
			key, err := p.srp.ComputeKey(tlvRead(data, tPublicKey))
			m := tlv{}
			m.add(tState, []byte{stateM4})
			proof := tlvRead(data, tProof)
			if err != nil || !p.srp.VerifyClientAuthenticator(proof) {
				m.add(tError, []byte{2})
			} else {
				p.key = key
				m.add(tProof, p.srp.ComputeAuthenticator(proof))
			}
			p.sendPairing(m.bytes())
		case kind == "setupManualPairing" && state[0] == stateM5:
			ek, _ := hkdf.Key(sha512.New, p.key, []byte("Pair-Setup-Encrypt-Salt"), "Pair-Setup-Encrypt-Info", 32)
			aead, _ := chacha20poly1305.New(ek)
			inner, err := aead.Open(nil, nonce("PS-Msg05"), tlvRead(data, tEncryptedData), nil)
			if err != nil {
				p.t.Errorf("phone: M5: %v", err)
				return
			}
			id := string(tlvRead(inner, tIdentifier))
			pub := ed25519.PublicKey(tlvRead(inner, tPublicKey))
			sign, _ := hkdf.Key(sha512.New, p.key, []byte("Pair-Setup-Controller-Sign-Salt"), "Pair-Setup-Controller-Sign-Info", 32)
			signed := append(append(append([]byte{}, sign...), id...), pub...)
			if !ed25519.Verify(pub, signed, tlvRead(inner, tSignature)) {
				p.t.Errorf("phone: M5 signature does not verify")
				return
			}
			p.hosts[id] = pub
			info, _ := opack.Encode(map[string]any{"altIRK": p.irk, "name": "Fake iPhone"})
			reply := tlv{}
			reply.add(tIdentifier, []byte("FAKE-DEVICE"))
			reply.add(tInfo, info)
			m := tlv{}
			m.add(tState, []byte{stateM6})
			m.add(tEncryptedData, aead.Seal(nil, nonce("PS-Msg06"), reply.bytes(), nil))
			p.sendPairing(m.bytes())
		default:
			p.t.Errorf("phone: unexpected %s state %v", kind, state)
			return
		}
	}
}

func (p *phone) encrypted(enc map[string]any, seq uint64) {
	ck, _ := hkdf.Key(sha512.New, p.secret, nil, "ClientEncrypt-main", 32)
	sk, _ := hkdf.Key(sha512.New, p.secret, nil, "ServerEncrypt-main", 32)
	var client, server cipher.AEAD
	client, _ = chacha20poly1305.New(ck)
	server, _ = chacha20poly1305.New(sk)
	n := make([]byte, 12)
	binary.LittleEndian.PutUint64(n, seq)
	sealed, _ := bytesOf(enc["_0"])
	plain, err := client.Open(nil, n, sealed, nil)
	if err != nil {
		p.t.Errorf("phone: decrypt request %d: %v", seq, err)
		return
	}
	var req map[string]any
	json.Unmarshal(plain, &req)
	cl, err := child(req, "request", "_0", "createListener")
	if err != nil {
		p.t.Errorf("phone: unexpected request %s", brief(req))
		return
	}
	if key, _ := bytesOf(cl["key"]); !bytes.Equal(key, p.secret) {
		p.t.Errorf("phone: createListener key is not the shared secret")
	}
	out, _ := json.Marshal(map[string]any{"response": map[string]any{"_1": map[string]any{
		"createListener": map[string]any{"port": p.port, "devicePublicKey": "x"},
	}}})
	p.send(map[string]any{"streamEncrypted": map[string]any{"_0": server.Seal(nil, n, out, nil)}})
}

func pipe(t *testing.T) (net.Conn, *phone) {
	host, dev := net.Pipe()
	p := newPhone(t, dev)
	go p.serve()
	t.Cleanup(func() { host.Close(); dev.Close() })
	return host, p
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestVerifyUnknownHost(t *testing.T) {
	conn, _ := pipe(t)
	id, _ := NewIdentity()
	if _, err := Verify(ctx(t), conn, id); err == nil {
		t.Fatal("a phone that does not know the host verified it")
	}
}

func TestSetupThenVerifyAndListen(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	conn, p := pipe(t)
	got, err := Setup(ctx(t), conn, id)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !bytes.Equal(got.AltIRK, p.irk) {
		t.Errorf("AltIRK = %x, want the phone's %x", got.AltIRK, p.irk)
	}
	if _, ok := p.hosts[id.Identifier]; !ok {
		t.Fatal("the phone did not keep the pairing")
	}

	// A fresh control channel to the same phone now verifies.
	host, dev := net.Pipe()
	t.Cleanup(func() { host.Close(); dev.Close() })
	p2 := newPhone(t, dev)
	p2.hosts = p.hosts
	go p2.serve()
	s, err := Verify(ctx(t), host, got)
	if err != nil {
		t.Fatalf("Verify after Setup: %v", err)
	}
	if s.Peer["name"] != "Fake iPhone" {
		t.Errorf("Peer = %v", s.Peer)
	}
	if !bytes.Equal(s.SharedSecret(), p2.secret) {
		t.Fatal("host and phone disagree on the shared secret")
	}
	for range 2 { // the nonce counter moves on between requests
		port, err := s.CreateListener()
		if err != nil {
			t.Fatalf("CreateListener: %v", err)
		}
		if port != p2.port {
			t.Fatalf("port = %d, want %d", port, p2.port)
		}
	}

	// Setup with a known identity leaves the pairing as it is.
	host3, dev3 := net.Pipe()
	t.Cleanup(func() { host3.Close(); dev3.Close() })
	p3 := newPhone(t, dev3)
	p3.hosts = p.hosts
	go p3.serve()
	if _, err := Setup(ctx(t), host3, got); err != nil {
		t.Fatalf("Setup of a known host: %v", err)
	}
}

func TestIdentityRoundTrip(t *testing.T) {
	id, _ := NewIdentity()
	id.AltIRK = bytes.Repeat([]byte{7}, 16)
	b, err := id.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseIdentity(b)
	if err != nil {
		t.Fatal(err)
	}
	if back.Identifier != id.Identifier || !back.PrivateKey.Equal(id.PrivateKey) || !bytes.Equal(back.AltIRK, id.AltIRK) {
		t.Fatalf("round trip changed the identity: %+v", back)
	}
	if !bytes.Contains(b, []byte("<key>private_key</key>")) || !bytes.Contains(b, []byte("<key>public_key</key>")) {
		t.Fatalf("not idevice's key names:\n%s", b)
	}
}

func TestTLV(t *testing.T) {
	long := bytes.Repeat([]byte{1}, 600)
	m := tlv{}
	m.add(tState, []byte{stateM3})
	m.add(tPublicKey, long)
	if got := tlvRead(m.bytes(), tPublicKey); !bytes.Equal(got, long) {
		t.Fatalf("fragmented value came back as %d bytes", len(got))
	}
	if !errors.Is(tlvError(4), ErrUnknownPeer) || errors.Is(tlvError(2), ErrUnknownPeer) {
		t.Fatal("tlvError and ErrUnknownPeer disagree")
	}
}
