package wadb

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Pairing follows AOSP's adb pairing_connection and pairing_auth: a TLS
// 1.3 connection to the pairing port that Wireless debugging opens while
// its "Pair device with pairing code" dialog shows, then SPAKE2 with the
// six-digit code followed by 64 bytes exported from that TLS session (so
// that nobody in the middle can relay the exchange), and finally each side
// sends its PeerInfo encrypted with AES-128-GCM under a key derived from
// SPAKE2's: the client its adb public key, adbd its GUID. adbd then adds
// the key to adb_keys, and the key's TLS connections are let in.

// Pairing constants (pairing_connection.cpp, pairing_auth.cpp).
const (
	pairHeaderVersion = 1
	pairTypeSpake2    = 0
	pairTypePeerInfo  = 1
	peerInfoSize      = 8192
	peerInfoRSAKey    = 0
	peerInfoGUID      = 1
	exportedKeySize   = 64
	maxPairPayload    = peerInfoSize * 2
)

var (
	pairClientName = []byte("adb pair client\x00")
	pairServerName = []byte("adb pair server\x00")
	// exportLabel is BoringSSL's label with its terminating NUL, as adb
	// passes sizeof() of the C string.
	exportLabel = "adb-label\x00"
	gcmInfo     = "adb pairing_auth aes-128-gcm key"
)

// ErrWrongCode is a pairing the phone refused: most often a mistyped code
// or one from an older dialog.
var ErrWrongCode = errors.New("wadb: the phone did not accept the pairing code")

// Pair pairs key with the adbd at addr (the pairing port Wireless
// debugging shows next to the code) and returns the phone's adb GUID.
// name is how the phone lists the pairing.
func Pair(ctx context.Context, addr, code string, key *Key, name string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("wadb: no pairing code")
	}
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("wadb: pairing port: %w", err)
	}
	defer raw.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	} else {
		_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
	}
	stop := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	tc := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		// adbd shows a self-signed certificate; SPAKE2 over the exported
		// key material is what authenticates it.
		InsecureSkipVerify: true,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return &key.cert, nil
		},
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		return "", fmt.Errorf("wadb: pairing TLS: %w", err)
	}
	cs := tc.ConnectionState()
	ekm, err := cs.ExportKeyingMaterial(exportLabel, nil, exportedKeySize)
	if err != nil {
		return "", fmt.Errorf("wadb: pairing TLS: %w", err)
	}
	password := append([]byte(code), ekm...)
	sp, err := newSpake2(spakeAlice, pairClientName, pairServerName, password, nil)
	if err != nil {
		return "", err
	}
	if err := writePairPacket(tc, pairTypeSpake2, sp.msg[:]); err != nil {
		return "", err
	}
	typ, peerMsg, err := readPairPacket(tc)
	if err != nil {
		return "", err
	}
	if typ != pairTypeSpake2 {
		return "", fmt.Errorf("wadb: pairing: unexpected packet type %d", typ)
	}
	shared, err := sp.finish(peerMsg)
	if err != nil {
		return "", err
	}
	aead, err := pairCipher(shared[:])
	if err != nil {
		return "", err
	}

	var info [peerInfoSize]byte
	info[0] = peerInfoRSAKey
	pub := key.PublicKey(name)
	if len(pub) > peerInfoSize-2 {
		return "", errors.New("wadb: public key too long")
	}
	copy(info[1:], pub)
	if err := writePairPacket(tc, pairTypePeerInfo, aead.seal(info[:])); err != nil {
		return "", err
	}
	typ, enc, err := readPairPacket(tc)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || isReset(err) {
			// adbd hangs up when the codes differ: its decryption failed.
			return "", ErrWrongCode
		}
		return "", err
	}
	if typ != pairTypePeerInfo {
		return "", fmt.Errorf("wadb: pairing: unexpected packet type %d", typ)
	}
	theirs, err := aead.open(enc)
	if err != nil {
		return "", ErrWrongCode
	}
	if len(theirs) != peerInfoSize || theirs[0] != peerInfoGUID {
		return "", errors.New("wadb: pairing: the phone sent no GUID")
	}
	guid := theirs[1:]
	if i := strings.IndexByte(string(guid), 0); i >= 0 {
		guid = guid[:i]
	}
	return string(guid), nil
}

func isReset(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && strings.Contains(op.Err.Error(), "reset")
}

func writePairPacket(w io.Writer, typ byte, payload []byte) error {
	hdr := make([]byte, 6, 6+len(payload))
	hdr[0] = pairHeaderVersion
	hdr[1] = typ
	binary.BigEndian.PutUint32(hdr[2:], uint32(len(payload)))
	if _, err := w.Write(append(hdr, payload...)); err != nil {
		return fmt.Errorf("wadb: pairing: %w", err)
	}
	return nil
}

func readPairPacket(r io.Reader) (byte, []byte, error) {
	var hdr [6]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, fmt.Errorf("wadb: pairing: %w", err)
	}
	if hdr[0] != pairHeaderVersion {
		return 0, nil, fmt.Errorf("wadb: pairing: unsupported version %d", hdr[0])
	}
	n := binary.BigEndian.Uint32(hdr[2:])
	if n == 0 || n > maxPairPayload {
		return 0, nil, fmt.Errorf("wadb: pairing: bad payload size %d", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, nil, fmt.Errorf("wadb: pairing: %w", err)
	}
	return hdr[1], b, nil
}

// gcm is pairing_auth's AES-128-GCM: the key from HKDF-SHA256 over the
// SPAKE2 key, and each direction's nonce a message counter, 8 bytes
// little-endian then zeros.
type gcm struct {
	aead     cipher.AEAD
	enc, dec uint64
}

func pairCipher(secret []byte) (*gcm, error) {
	k, err := hkdf.Key(sha256.New, secret, nil, gcmInfo, 16)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &gcm{aead: aead}, nil
}

func (g *gcm) nonce(n uint64) []byte {
	b := make([]byte, g.aead.NonceSize())
	binary.LittleEndian.PutUint64(b, n)
	return b
}

func (g *gcm) seal(p []byte) []byte {
	out := g.aead.Seal(nil, g.nonce(g.enc), p, nil)
	g.enc++
	return out
}

func (g *gcm) open(c []byte) ([]byte, error) {
	out, err := g.aead.Open(nil, g.nonce(g.dec), c, nil)
	if err != nil {
		return nil, err
	}
	g.dec++
	return out, nil
}
