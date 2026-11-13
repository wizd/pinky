package rppair

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/danielpaulus/go-ios/ios/opack"
	"github.com/tadglines/go-pkgs/crypto/srp"
	"golang.org/x/crypto/chacha20poly1305"
)

// Pairing from the phone itself (iOS 27 and later). The phone pairs with a
// host on the network from Settings › Privacy & Security › Developer Mode ›
// "Pair with …": the host advertises PairableService over Bonjour, the phone
// connects to it and runs the RemotePairing conversation with the roles
// flipped. The phone says the handshake and drives pair-setup as the host
// would; the host answers as a device would, makes up the PIN and shows it,
// and the person types it on the phone (idevice,
// idevice/src/remote_pairing/responder.rs). The identity that results is the
// one Setup makes from a computer: Dial and Verify take it as it is.

// PairableService is the Bonjour service type a host the phone may pair
// with advertises.
const PairableService = "_remotepairing-pairable-host._tcp"

// Host is what a pairable host tells the phone about itself. iOS treats it
// as a computer, so Model is a Mac's.
type Host struct {
	Name  string
	Model string
	// AltIRK is the host's 16-byte key for Bonjour: the authTag in its
	// advertisement is made with it (AuthTag), and the phone gets it in M6.
	AltIRK []byte
}

// TXT is the TXT record of the host's advertisement, whose service name is
// the identity's Identifier.
func (h Host) TXT(identifier string) map[string]string {
	return map[string]string{
		"name":       h.Name,
		"identifier": identifier,
		"authTag":    base64.StdEncoding.EncodeToString(AuthTag(h.AltIRK, identifier)),
		"model":      h.Model,
		"flags":      "1",
		"ver":        fmt.Sprint(respondWireVersion),
		"minVer":     "17",
	}
}

// respondWireVersion is the wire protocol version a host answers with:
// what iOS 27 speaks (responder.rs).
const respondWireVersion = 26

// Phone is what the phone said about itself in pair-setup.
type Phone struct {
	UDID      string // remotepairing_udid
	Name      string
	Model     string
	AccountID string
	AltIRK    []byte
}

// ErrWrongPIN is the phone's proof not matching: the person typed another
// PIN than the one shown.
var ErrWrongPIN = errors.New("the PIN typed on the phone does not match")

// Respond answers a phone that connected to the host's advertised port:
// the handshake, then pair-setup with a PIN it makes up and hands to show
// before the phone asks for it. It returns id, which the phone now trusts,
// with the phone's altIRK, and what the phone said about itself.
func Respond(ctx context.Context, conn io.ReadWriteCloser, id Identity, host Host, show func(pin string)) (Identity, Phone, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return id, Phone{}, err
	}
	return respond(ctx, conn, id, host, fmt.Sprintf("%06d", n.Int64()), show)
}

func respond(ctx context.Context, conn io.ReadWriteCloser, id Identity, host Host, pin string, show func(string)) (Identity, Phone, error) {
	s := newSession(conn)
	s.origin = "device"
	defer s.deadline(ctx)()
	if err := s.answerHello(host); err != nil {
		return id, Phone{}, fmt.Errorf("handshake: %w", err)
	}
	phone, err := s.answerSetup(id, host, pin, show)
	if err != nil {
		return id, Phone{}, fmt.Errorf("pair-setup: %w", err)
	}
	id.AltIRK = phone.AltIRK
	return id, phone, nil
}

// answerHello reads the phone's handshake request and answers it as a
// device that allows pair-setup.
func (s *Session) answerHello(host Host) error {
	m, err := s.read()
	if err != nil {
		return err
	}
	h, err := child(m, "plain", "_0", "request", "_0", "handshake", "_0")
	if err != nil {
		return fmt.Errorf("%w (got %s)", err, brief(m))
	}
	if o, ok := h["hostOptions"].(map[string]any); ok && o["attemptPairVerify"] == true {
		// A phone that already paired with this host verifies with the
		// pairing it has; the core dials the phone for that, so here it is
		// a pairing this host no longer holds.
		return errors.New("the phone asked to verify a pairing; this host only sets one up")
	}
	return s.write(map[string]any{"plain": map[string]any{"_0": map[string]any{
		"response": map[string]any{
			"forRequestIdentifier": 0,
			"_1": map[string]any{"handshake": map[string]any{"_0": map[string]any{
				"wireProtocolVersion":                 respondWireVersion,
				"minimumSupportedWireProtocolVersion": 8,
				"deviceOptions": map[string]any{
					"allowsPairSetup":                 true,
					"allowsPinlessPairing":            false,
					"allowsIncomingTunnelConnections": false,
					"allowsUpgradeOfLockdownPairings": false,
					"allowsSharingSensitiveInfo":      false,
				},
				"peerDeviceInfo": map[string]any{
					"udid":                           "",
					"identifier":                     "",
					"deviceKVSIncludesSensitiveInfo": false,
					"name":                           host.Name,
					"model":                          host.Model,
				},
			}}},
		},
	}}})
}

// answerSetup is pair-setup as the accessory, M1 to M6.
func (s *Session) answerSetup(id Identity, host Host, pin string, show func(string)) (Phone, error) {
	m1, err := s.readPairing()
	if err != nil {
		return Phone{}, fmt.Errorf("M1: %w", err)
	}
	if st := tlvRead(m1, tState); len(st) != 1 || st[0] != stateM1 {
		return Phone{}, fmt.Errorf("M1: state %v", st)
	}

	srps, err := newSRP()
	if err != nil {
		return Phone{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return Phone{}, err
	}
	verifier := srpVerifier(srps, salt, pin)
	ss := srpServer(srps, salt, verifier)
	if show != nil {
		show(pin)
	}
	m2 := tlv{}
	m2.add(tState, []byte{stateM2})
	m2.add(tSalt, salt)
	m2.add(tPublicKey, ss.GetB())
	if err := s.writePairing("setupManualPairing", m2.bytes(), false); err != nil {
		return Phone{}, err
	}

	// The phone waits for the person to type the PIN before M3.
	m3, err := s.readPairing()
	if err != nil {
		return Phone{}, fmt.Errorf("M3: %w", err)
	}
	if e := tlvRead(m3, tError); len(e) > 0 {
		return Phone{}, fmt.Errorf("M3: %w", tlvError(e[0]))
	}
	key, err := ss.ComputeKey(tlvRead(m3, tPublicKey))
	if err != nil {
		return Phone{}, fmt.Errorf("M3: %w", err)
	}
	proof := tlvRead(m3, tProof)
	if !ss.VerifyClientAuthenticator(proof) {
		bad := tlv{}
		bad.add(tState, []byte{stateM4})
		bad.add(tError, []byte{2}) // authentication
		s.writePairing("setupManualPairing", bad.bytes(), false)
		return Phone{}, ErrWrongPIN
	}
	m4 := tlv{}
	m4.add(tState, []byte{stateM4})
	m4.add(tProof, ss.ComputeAuthenticator(proof))
	if err := s.writePairing("setupManualPairing", m4.bytes(), false); err != nil {
		return Phone{}, err
	}

	ek, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Encrypt-Salt"), "Pair-Setup-Encrypt-Info", 32)
	if err != nil {
		return Phone{}, err
	}
	aead, err := chacha20poly1305.New(ek)
	if err != nil {
		return Phone{}, err
	}
	m5, err := s.readPairing()
	if err != nil {
		return Phone{}, fmt.Errorf("M5: %w", err)
	}
	if e := tlvRead(m5, tError); len(e) > 0 {
		return Phone{}, fmt.Errorf("M5: %w", tlvError(e[0]))
	}
	inner, err := aead.Open(nil, nonce("PS-Msg05"), tlvRead(m5, tEncryptedData), nil)
	if err != nil {
		return Phone{}, fmt.Errorf("M5: %w", err)
	}
	phone, err := phoneOf(inner, key)
	if err != nil {
		return Phone{}, fmt.Errorf("M5: %w", err)
	}

	sign, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Accessory-Sign-Salt"), "Pair-Setup-Accessory-Sign-Info", 32)
	if err != nil {
		return Phone{}, err
	}
	var signed bytes.Buffer
	signed.Write(sign)
	signed.WriteString(id.Identifier)
	signed.Write(id.PublicKey())
	info, err := opack.Encode(map[string]any{
		"accountID":                   id.Identifier,
		"altIRK":                      host.AltIRK,
		"btAddr":                      "11:22:33:44:55:66",
		"mac":                         []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66},
		"model":                       host.Model,
		"name":                        host.Name,
		"remotepairing_serial_number": "AAAAAAAAAAAA",
		"remotepairing_udid":          "",
	})
	if err != nil {
		return Phone{}, err
	}
	mine := tlv{}
	mine.add(tIdentifier, []byte(id.Identifier))
	mine.add(tPublicKey, id.PublicKey())
	mine.add(tSignature, ed25519.Sign(id.PrivateKey, signed.Bytes()))
	mine.add(tInfo, info)
	m6 := tlv{}
	m6.add(tEncryptedData, aead.Seal(nil, nonce("PS-Msg06"), mine.bytes(), nil))
	m6.add(tState, []byte{stateM6})
	if err := s.writePairing("setupManualPairing", m6.bytes(), false); err != nil {
		return Phone{}, err
	}
	return phone, nil
}

// srpVerifier is v = g^x for pair-setup's x (newSRP) of salt and pin.
func srpVerifier(srps *srp.SRP, salt []byte, pin string) []byte {
	x := new(big.Int).SetBytes(srps.KeyDerivationFunc(salt, []byte(pin)))
	return new(big.Int).Exp(srps.Group.Generator, x, srps.Group.Prime).Bytes()
}

// srpServer is a server session whose B takes all 384 bytes of the group:
// the phone reads B at that length (responder.rs draws b again too).
func srpServer(srps *srp.SRP, salt, verifier []byte) *srp.ServerSession {
	for {
		if ss := srps.NewServerSession([]byte("Pair-Setup"), salt, verifier); len(ss.GetB()) == 384 {
			return ss
		}
	}
}

// phoneOf checks the phone's M5 identity, signed with its long-term key
// over the controller's sign key, its identifier and that key, and reads
// its info.
func phoneOf(inner, key []byte) (Phone, error) {
	ident, pub, sig := tlvRead(inner, tIdentifier), tlvRead(inner, tPublicKey), tlvRead(inner, tSignature)
	if len(ident) == 0 || len(pub) != ed25519.PublicKeySize || len(sig) == 0 {
		return Phone{}, errors.New("no identifier, key or signature")
	}
	sign, err := hkdf.Key(sha512.New, key, []byte("Pair-Setup-Controller-Sign-Salt"), "Pair-Setup-Controller-Sign-Info", 32)
	if err != nil {
		return Phone{}, err
	}
	var signed bytes.Buffer
	signed.Write(sign)
	signed.Write(ident)
	signed.Write(pub)
	if !ed25519.Verify(pub, signed.Bytes(), sig) {
		return Phone{}, errors.New("the phone's signature does not match")
	}
	p := Phone{AccountID: string(ident)}
	info, err := opackDecode(tlvRead(inner, tInfo))
	if err != nil {
		// What the phone is called is only shown; the pairing stands.
		return p, nil
	}
	m, _ := info.(map[string]any)
	str := func(k string) string { s, _ := m[k].(string); return s }
	p.UDID, p.Name, p.Model = str("remotepairing_udid"), str("name"), str("model")
	if a := str("accountID"); a != "" {
		p.AccountID = a
	}
	if irk, ok := m["altIRK"].([]byte); ok && len(irk) == 16 {
		p.AltIRK = irk
	}
	return p, nil
}
