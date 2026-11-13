package rppair

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"

	"github.com/danielpaulus/go-ios/ios/opack"
	"github.com/tadglines/go-pkgs/crypto/srp"
	"golang.org/x/crypto/chacha20poly1305"
)

// LockdownService is the lockdown service that speaks this control channel
// to a host the phone already trusts through lockdown (USB or Wi-Fi
// usbmuxd). Pair-setup on it uses the fixed PIN "000000" and shows nothing on
// the phone (idevice tools/src/remotepairing_lockdown.rs).
const LockdownService = "com.apple.dt.remotepairingdeviced.lockdown"

// lockdownPIN is the pair-setup code on LockdownService.
const lockdownPIN = "000000"

// Setup pairs id with the phone over conn, a LockdownService stream: hello,
// then pair-setup with the lockdown PIN. It returns id with the phone's
// altIRK when the phone sent one. A phone that already knows id passes
// pair-verify and is left as it is.
func Setup(ctx context.Context, conn io.ReadWriteCloser, id Identity) (Identity, error) {
	s := newSession(conn)
	defer s.deadline(ctx)()
	if err := s.hello(); err != nil {
		return id, fmt.Errorf("hello: %w", err)
	}
	err := s.verify(id)
	if err == nil {
		return id, nil
	}
	// The phone answers an identity it does not know with a pairing error
	// (go-ios moves on to pair-setup after any failed pair-verify too).
	var te tlvError
	if !errors.As(err, &te) {
		return id, fmt.Errorf("pair-verify: %w", err)
	}
	return s.setup(id)
}

// newSRP is SRP-6a as pair-setup uses it: the 3072-bit group, SHA-512 and
// x = H(salt | H("Pair-Setup:" | password)).
func newSRP() (*srp.SRP, error) {
	return srp.NewSRP("rfc5054.3072", sha512.New, func(salt, password []byte) []byte {
		inner := sha512.Sum512([]byte("Pair-Setup:" + string(password)))
		h := sha512.New()
		h.Write(salt)
		h.Write(inner[:])
		return h.Sum(nil)
	})
}

func (s *Session) setup(id Identity) (Identity, error) {
	return s.setupWith(id, lockdownPIN, nil)
}

// setupWith is pair-setup as the host with pin, sending info (OPACK) about
// this host in M5; nil sends Pinky's.
func (s *Session) setupWith(id Identity, pin string, info map[string]any) (Identity, error) {
	m1 := tlv{}
	m1.add(tMethod, []byte{0})
	m1.add(tState, []byte{stateM1})
	if err := s.writePairing("setupManualPairing", m1.bytes(), true); err != nil {
		return id, err
	}
	m2, err := s.readPairing()
	if err != nil {
		return id, fmt.Errorf("M2: %w", err)
	}
	if e := tlvRead(m2, tError); len(e) > 0 {
		return id, fmt.Errorf("M2: %w", tlvError(e[0]))
	}
	salt, devPub := tlvRead(m2, tSalt), tlvRead(m2, tPublicKey)
	if len(salt) == 0 || len(devPub) == 0 {
		return id, errors.New("M2: no salt or public key")
	}

	srpc, err := newSRP()
	if err != nil {
		return id, err
	}
	cs := srpc.NewClientSession([]byte("Pair-Setup"), []byte(pin))
	key, err := cs.ComputeKey(salt, devPub)
	if err != nil {
		return id, fmt.Errorf("SRP: %w", err)
	}
	m3 := tlv{}
	m3.add(tState, []byte{stateM3})
	m3.add(tPublicKey, cs.GetA())
	m3.add(tProof, cs.ComputeAuthenticator())
	if err := s.writePairing("setupManualPairing", m3.bytes(), false); err != nil {
		return id, err
	}
	m4, err := s.readPairing()
	if err != nil {
		return id, fmt.Errorf("M4: %w", err)
	}
	if e := tlvRead(m4, tError); len(e) > 0 {
		return id, fmt.Errorf("M4: %w", tlvError(e[0]))
	}
	if !cs.VerifyServerAuthenticator(tlvRead(m4, tProof)) {
		return id, errors.New("M4: the phone's SRP proof does not match")
	}

	sign, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Controller-Sign-Salt"), "Pair-Setup-Controller-Sign-Info", 32)
	if err != nil {
		return id, err
	}
	var signed bytes.Buffer
	signed.Write(sign)
	signed.WriteString(id.Identifier)
	signed.Write(id.PublicKey())
	if info == nil {
		info = map[string]any{
			"accountID":                   id.Identifier,
			"altIRK":                      make([]byte, 16),
			"btAddr":                      "00:00:00:00:00:00",
			"mac":                         make([]byte, 6),
			"model":                       "Pinky",
			"name":                        hostName,
			"remotepairing_serial_number": id.Identifier,
		}
	}
	enc, err := opack.Encode(info)
	if err != nil {
		return id, err
	}
	inner := tlv{}
	inner.add(tIdentifier, []byte(id.Identifier))
	inner.add(tPublicKey, id.PublicKey())
	inner.add(tSignature, ed25519.Sign(id.PrivateKey, signed.Bytes()))
	inner.add(tInfo, enc)
	ek, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Encrypt-Salt"), "Pair-Setup-Encrypt-Info", 32)
	if err != nil {
		return id, err
	}
	aead, err := chacha20poly1305.New(ek)
	if err != nil {
		return id, err
	}
	m5 := tlv{}
	m5.add(tState, []byte{stateM5})
	m5.add(tEncryptedData, aead.Seal(nil, nonce("PS-Msg05"), inner.bytes(), nil))
	if err := s.writePairing("setupManualPairing", m5.bytes(), false); err != nil {
		return id, err
	}
	m6, err := s.readPairing()
	if err != nil {
		return id, fmt.Errorf("M6: %w", err)
	}
	if e := tlvRead(m6, tError); len(e) > 0 {
		return id, fmt.Errorf("M6: %w", tlvError(e[0]))
	}
	plain, err := aead.Open(nil, nonce("PS-Msg06"), tlvRead(m6, tEncryptedData), nil)
	if err != nil {
		return id, fmt.Errorf("M6: %w", err)
	}
	// The phone's own info (OPACK) carries its altIRK; Pinky does not need
	// it, so it is kept only when it is easy to find.
	if irk := opackData(tlvRead(plain, tInfo), "altIRK"); len(irk) == 16 {
		id.AltIRK = irk
	}
	return id, nil
}

// opackData finds the data value of key in an OPACK dictionary by its
// encoding: the key as a short string, then data of up to 32 bytes. Enough
// for the altIRK; nil when it is laid out otherwise.
func opackData(b []byte, key string) []byte {
	if len(key) > 0x20 {
		return nil
	}
	k := append([]byte{byte(0x40 + len(key))}, key...)
	i := bytes.Index(b, k)
	if i < 0 || i+len(k) >= len(b) {
		return nil
	}
	rest := b[i+len(k):]
	t := rest[0]
	if t < 0x70 || t > 0x90 {
		return nil
	}
	n := int(t - 0x70)
	if len(rest) < 1+n {
		return nil
	}
	return rest[1 : 1+n]
}
