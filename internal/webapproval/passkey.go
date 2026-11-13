package webapproval

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// The passkey check, as WebAuthn defines it for an assertion
// (navigator.credentials.get) and for what Pinky needs of a new passkey.
// Only ES256 (ECDSA with P-256 and SHA-256) is asked for and taken. No
// attestation is checked: a new passkey is trusted because the person
// allows it in Pinky's window after comparing a code, not because of who
// made the authenticator.

// COSE algorithm ES256.
const algES256 = -7

// Authenticator data flags.
const (
	flagUserPresent  = 0x01
	flagUserVerified = 0x04
)

var b64 = base64.RawURLEncoding

// clientData is the part of WebAuthn's client data Pinky checks.
type clientData struct {
	Type        string `json:"type"`
	Challenge   string `json:"challenge"`
	Origin      string `json:"origin"`
	CrossOrigin bool   `json:"crossOrigin"`
}

var (
	errClientData = errors.New("passkey: client data does not match")
	errAuthData   = errors.New("passkey: authenticator data does not match")
	errNoUV       = errors.New("passkey: the person was not verified")
	errSignature  = errors.New("passkey: bad signature")
	errKey        = errors.New("passkey: not a P-256 key")
)

// checkClientData checks that the browser made the client data for this
// ceremony, challenge and origin.
func checkClientData(raw []byte, typ string, challenge []byte, origin string) error {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return errClientData
	}
	got, err := b64.DecodeString(cd.Challenge)
	if err != nil || cd.Type != typ || cd.Origin != origin || cd.CrossOrigin || !bytes.Equal(got, challenge) {
		return errClientData
	}
	return nil
}

// checkAuthData checks the authenticator data's RP id hash and that the
// person was present and verified (Face ID, a fingerprint, the phone's
// passcode).
func checkAuthData(ad []byte, rpID string) error {
	if len(ad) < 37 {
		return errAuthData
	}
	want := sha256.Sum256([]byte(rpID))
	if !bytes.Equal(ad[:32], want[:]) {
		return errAuthData
	}
	if ad[32]&flagUserPresent == 0 || ad[32]&flagUserVerified == 0 {
		return errNoUV
	}
	return nil
}

// parseKey reads a passkey's public key as getPublicKey() gives it
// (SubjectPublicKeyInfo, DER).
func parseKey(spki []byte) (*ecdsa.PublicKey, error) {
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, errKey
	}
	ek, ok := k.(*ecdsa.PublicKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errKey
	}
	return ek, nil
}

// assertion is what navigator.credentials.get returns.
type assertion struct {
	ClientData []byte
	AuthData   []byte
	Signature  []byte
}

// verify checks an assertion made by key for challenge, at origin and
// rpID, with the person verified.
func (a assertion) verify(key *ecdsa.PublicKey, challenge []byte, origin, rpID string) error {
	if err := checkClientData(a.ClientData, "webauthn.get", challenge, origin); err != nil {
		return err
	}
	if err := checkAuthData(a.AuthData, rpID); err != nil {
		return err
	}
	h := sha256.Sum256(a.ClientData)
	msg := append(append([]byte(nil), a.AuthData...), h[:]...)
	d := sha256.Sum256(msg)
	if !ecdsa.VerifyASN1(key, d[:], a.Signature) {
		return errSignature
	}
	return nil
}
