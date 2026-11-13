package wadb

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// Key is the app's adb key: an RSA key the phone's adbd learns when the
// person pairs, and which proves it is Pinky on every later connection,
// as a computer's ~/.android/adbkey does. It lives in the app's private
// storage only.
type Key struct {
	priv *rsa.PrivateKey
	cert tls.Certificate
}

// keyFile is the key's file name in the key directory.
const keyFile = "adbkey.pem"

// keyBits is the key size adbd expects: its key format holds a 2048-bit
// modulus only.
const keyBits = 2048

// LoadKey reads the key in dir, creating it when there is none.
func LoadKey(dir string) (*Key, error) {
	path := filepath.Join(dir, keyFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		priv, err := rsa.GenerateKey(rand.Reader, keyBits)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		der := x509.MarshalPKCS1PrivateKey(priv)
		data := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return nil, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return nil, err
		}
		return NewKey(priv)
	}
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "RSA PRIVATE KEY" {
		return nil, fmt.Errorf("wadb: %s is not an RSA key", path)
	}
	priv, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("wadb: %s: %w", path, err)
	}
	return NewKey(priv)
}

// RemoveKey deletes the key in dir, so that the phone no longer knows the
// next one: the person pairs again.
func RemoveKey(dir string) error {
	err := os.Remove(filepath.Join(dir, keyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// NewKey wraps an RSA key with the self-signed certificate it shows in TLS.
func NewKey(priv *rsa.PrivateKey) (*Key, error) {
	if priv.N.BitLen() != keyBits {
		return nil, fmt.Errorf("wadb: the key has %d bits, adb needs %d", priv.N.BitLen(), keyBits)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Pinky"},
		NotBefore:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2120, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, err
	}
	return &Key{priv: priv, cert: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}}, nil
}

// PublicKey is the key in adb's public key format, followed by a space
// and name: what adbd keeps in /data/misc/adb/adb_keys, and what Android's
// Wireless debugging lists as the paired device's name.
func (k *Key) PublicKey(name string) string {
	return base64.StdEncoding.EncodeToString(adbPublicKey(&k.priv.PublicKey)) + " " + name
}

// adbPublicKey encodes pub as AOSP's RSAPublicKey (libcrypto_utils,
// android_pubkey.cpp): the modulus' length in 32-bit words, n0inv =
// -1/n mod 2^32, the modulus and R² mod n (R = 2^2048), both
// little-endian, and the exponent.
func adbPublicKey(pub *rsa.PublicKey) []byte {
	const words = keyBits / 32
	out := make([]byte, 4+4+words*4*2+4)
	binary.LittleEndian.PutUint32(out[0:], words)

	r32 := new(big.Int).Lsh(big.NewInt(1), 32)
	n0 := new(big.Int).Mod(pub.N, r32)
	n0inv := new(big.Int).ModInverse(n0, r32)
	n0inv.Sub(r32, n0inv)
	binary.LittleEndian.PutUint32(out[4:], uint32(n0inv.Uint64()))

	putLE(out[8:8+words*4], pub.N)
	rr := new(big.Int).Lsh(big.NewInt(1), keyBits*2)
	rr.Mod(rr, pub.N)
	putLE(out[8+words*4:8+words*8], rr)
	binary.LittleEndian.PutUint32(out[8+words*8:], uint32(pub.E))
	return out
}

// putLE writes n into b little-endian, zero-padded.
func putLE(b []byte, n *big.Int) {
	be := n.Bytes()
	for i, c := range be {
		b[len(be)-1-i] = c
	}
}
