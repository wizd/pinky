package rppair

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
	"howett.net/plist"
)

// Identity is the host side of a RemotePairing pairing: the identifier the
// phone knows the host by and the host's Ed25519 key.
type Identity struct {
	Identifier string
	PrivateKey ed25519.PrivateKey
	// AltIRK is the phone's altIRK from pair-setup, when known.
	AltIRK []byte
}

// NewIdentity makes a fresh identity for pair-setup.
func NewIdentity() (Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	return Identity{Identifier: strings.ToUpper(uuid.NewString()), PrivateKey: priv}, nil
}

// PublicKey is the identity's Ed25519 public key.
func (id Identity) PublicKey() ed25519.PublicKey {
	return id.PrivateKey.Public().(ed25519.PublicKey)
}

// file is the pairing file as idevice, StikDebug and SideStore keep it
// (rp_pairing_file.plist). go-ios's selfIdentity.plist uses camelCase keys
// for the same values.
type file struct {
	Identifier string `plist:"identifier"`
	PublicKey  []byte `plist:"public_key"`
	PrivateKey []byte `plist:"private_key"` // the 32-byte seed
	AltIRK     []byte `plist:"alt_irk,omitempty"`

	GoIOSPublicKey  []byte `plist:"publicKey,omitempty"`
	GoIOSPrivateKey []byte `plist:"privateKey,omitempty"`
}

// ParseIdentity reads a pairing file: idevice's rp_pairing_file.plist or
// go-ios's selfIdentity.plist, XML or binary.
func ParseIdentity(b []byte) (Identity, error) {
	var f file
	if _, err := plist.Unmarshal(b, &f); err != nil {
		return Identity{}, fmt.Errorf("pairing file: %w", err)
	}
	seed := f.PrivateKey
	if len(seed) == 0 {
		seed = f.GoIOSPrivateKey
	}
	if len(seed) == ed25519.PrivateKeySize {
		seed = seed[:ed25519.SeedSize]
	}
	if len(seed) != ed25519.SeedSize {
		return Identity{}, fmt.Errorf("pairing file: private key of %d bytes", len(seed))
	}
	if f.Identifier == "" {
		return Identity{}, errors.New("pairing file: no identifier")
	}
	id := Identity{Identifier: f.Identifier, PrivateKey: ed25519.NewKeyFromSeed(seed), AltIRK: f.AltIRK}
	pub := f.PublicKey
	if len(pub) == 0 {
		pub = f.GoIOSPublicKey
	}
	if len(pub) > 0 && !id.PublicKey().Equal(ed25519.PublicKey(pub)) {
		return Identity{}, errors.New("pairing file: public key does not match the private key")
	}
	return id, nil
}

// LoadIdentity reads a pairing file from path.
func LoadIdentity(path string) (Identity, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	return ParseIdentity(b)
}

// Marshal writes the identity as idevice's rp_pairing_file.plist (XML).
func (id Identity) Marshal() ([]byte, error) {
	return plist.MarshalIndent(file{
		Identifier: id.Identifier,
		PublicKey:  id.PublicKey(),
		PrivateKey: id.PrivateKey.Seed(),
		AltIRK:     id.AltIRK,
	}, plist.XMLFormat, "\t")
}
