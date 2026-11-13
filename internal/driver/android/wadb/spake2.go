package wadb

import (
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"

	"filippo.io/edwards25519"
)

// SPAKE2 over edwards25519 as BoringSSL implements it
// (crypto/curve25519/spake25519.cc), which adbd uses to pair: a client and
// a server that know the same password derive the same 64-byte key, and
// one that does not learns nothing it could test offline.
//
// BoringSSL's details, which a compatible peer must follow:
//
//   - The private scalar is a random 64-byte value reduced mod l, then
//     multiplied by 8 (the cofactor), so that later products clear any
//     small-order component of the peer's point.
//   - The mask is h·M for Alice and h·N for Bob, where h is SHA-512 of the
//     password reduced mod l, plus a multiple of l that makes it a multiple
//     of 8 ("the password scalar hack"). That puts the mask in the
//     prime-order subgroup: it equals (h/8 mod l)·(8M).
//   - The key is SHA-512 over each of the names, the two messages (Alice's
//     first), the shared point and the password's SHA-512, each preceded by
//     its length as 8 bytes, little-endian.

// The points M and N, from BoringSSL: SHA-256 of "edwards25519 point
// generation seed (M)" (or N), hashed again until it decodes as a point.
var (
	spakeM = mustPoint("5ada7e4bf6ddd9adb6626d32131c6b5c51a1e347a3478f53cfcf441b88eed12e")
	spakeN = mustPoint("10e3df0ae37d8e7a99b5fe74b44672103dbddcbd06af680d71329a11693bc778")
)

func mustPoint(h string) *edwards25519.Point {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		panic(err)
	}
	return p
}

// spakeRole is which side of the exchange this is.
type spakeRole int

const (
	spakeAlice spakeRole = iota // the pairing client (adb pair)
	spakeBob                    // the pairing server (adbd)
)

// spake2 is one side's state.
type spake2 struct {
	role             spakeRole
	myName, peerName []byte
	x                *edwards25519.Scalar // the private scalar before the factor 8
	pwScalar         *edwards25519.Scalar // h/8 mod l, see above
	pwHash           [64]byte
	msg              [32]byte
}

// eighth is 8⁻¹ mod l.
var eighth = func() *edwards25519.Scalar {
	var b [32]byte
	b[0] = 8
	s, err := edwards25519.NewScalar().SetCanonicalBytes(b[:])
	if err != nil {
		panic(err)
	}
	return edwards25519.NewScalar().Invert(s)
}()

// newSpake2 starts an exchange for password and returns the side's state;
// its message is in msg. random supplies the private scalar's 64 bytes
// (crypto/rand when nil).
func newSpake2(role spakeRole, myName, peerName, password []byte, random func([]byte) error) (*spake2, error) {
	if random == nil {
		random = func(b []byte) error {
			_, err := rand.Read(b)
			return err
		}
	}
	var seed [64]byte
	if err := random(seed[:]); err != nil {
		return nil, err
	}
	x, err := edwards25519.NewScalar().SetUniformBytes(seed[:])
	if err != nil {
		return nil, err
	}
	s := &spake2{role: role, myName: myName, peerName: peerName, x: x}
	s.pwHash = sha512.Sum512(password)
	h, err := edwards25519.NewScalar().SetUniformBytes(s.pwHash[:])
	if err != nil {
		return nil, err
	}
	s.pwScalar = edwards25519.NewScalar().Multiply(h, eighth)

	// P = 8x·B, P* = P + mask.
	eight := edwards25519.NewScalar().Add(x, x)
	eight.Add(eight, eight)
	eight.Add(eight, eight)
	p := new(edwards25519.Point).ScalarBaseMult(eight)
	pstar := new(edwards25519.Point).Add(p, s.mask(role))
	copy(s.msg[:], pstar.Bytes())
	return s, nil
}

// mask is the mask of the given role's message: (h/8)·(8M) for Alice,
// (h/8)·(8N) for Bob.
func (s *spake2) mask(role spakeRole) *edwards25519.Point {
	base := spakeM
	if role == spakeBob {
		base = spakeN
	}
	clean := new(edwards25519.Point).MultByCofactor(base)
	return new(edwards25519.Point).ScalarMult(s.pwScalar, clean)
}

// errBadPeerMessage is a SPAKE2 message that is not a point.
var errBadPeerMessage = errors.New("wadb: the peer's pairing message is not a curve point")

// finish takes the peer's message and returns the shared key.
func (s *spake2) finish(peerMsg []byte) ([64]byte, error) {
	var key [64]byte
	if len(peerMsg) != 32 {
		return key, errBadPeerMessage
	}
	qstar, err := new(edwards25519.Point).SetBytes(peerMsg)
	if err != nil {
		return key, errBadPeerMessage
	}
	peer := spakeBob
	if s.role == spakeBob {
		peer = spakeAlice
	}
	q := new(edwards25519.Point).Subtract(qstar, s.mask(peer))
	// 8x·Q = x·(8Q): the same point as BoringSSL's for every Q, small-order
	// components included.
	shared := new(edwards25519.Point).ScalarMult(s.x, new(edwards25519.Point).MultByCofactor(q))

	h := sha512.New()
	if s.role == spakeAlice {
		writeLP(h, s.myName)
		writeLP(h, s.peerName)
		writeLP(h, s.msg[:])
		writeLP(h, peerMsg)
	} else {
		writeLP(h, s.peerName)
		writeLP(h, s.myName)
		writeLP(h, peerMsg)
		writeLP(h, s.msg[:])
	}
	writeLP(h, shared.Bytes())
	writeLP(h, s.pwHash[:])
	copy(key[:], h.Sum(nil))
	return key, nil
}

// writeLP writes b's length as 8 bytes, little-endian, then b.
func writeLP(h hash.Hash, b []byte) {
	var n [8]byte
	binary.LittleEndian.PutUint64(n[:], uint64(len(b)))
	h.Write(n[:])
	h.Write(b)
}
