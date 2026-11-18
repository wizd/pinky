package traffic

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/secret"
)

// The certificate authority's files, in the folder LoadCA keeps them in
// (Pinky's data folder). The key file is there only where Pinky keeps the
// key in no keychain (Linux, Windows), readable by the person's user only.
const (
	CACertFile = "traffic-ca.crt"
	CAKeyFile  = "traffic-ca.key"
)

// Where a CA's private key is kept (CA.KeyPlace).
const (
	KeyInKeychain = "keychain"
	KeyInFile     = "file"
	KeyInMemory   = "memory"
)

// How long certificates last. A CA lasts a year and is replaced
// RenewBefore its end; leaves last a day, never past their CA, and are
// made again once less than leafRenew of it is left.
const (
	caLife      = 365 * 24 * time.Hour
	RenewBefore = 30 * 24 * time.Hour
	leafLife    = 24 * time.Hour
	leafRenew   = time.Hour
	// leafKeep bounds the certificates kept for hosts; the cache starts
	// over past it.
	leafKeep = 1024
)

// CA is the certificate authority Pinky makes for this computer, which
// signs a certificate for each host the proxy decrypts. Phones decrypt
// only once the person installs and trusts its certificate.
type CA struct {
	cert *x509.Certificate
	key  crypto.Signer
	der  []byte
	// Renewed: LoadCA made this one because the one before was about to
	// end; the phones have to install it again.
	Renewed bool
	// KeyPlace is where its private key is kept: KeyInKeychain,
	// KeyInFile or KeyInMemory.
	KeyPlace string

	mu      sync.Mutex
	leafKey *ecdsa.PrivateKey
	leaves  map[string]*tls.Certificate
}

// NewCA makes a new certificate authority, in memory.
func NewCA() (*CA, error) { return newCAFor(caLife) }

func newCAFor(life time.Duration) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	// The name carries a few random letters, so two computers' authorities
	// can be told apart in the phone's list of certificates.
	tag := fmt.Sprintf("%06X", new(big.Int).And(serial, big.NewInt(0xFFFFFF)).Int64())
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "Pinky Traffic CA " + tag, Organization: []string{"Pinky"}},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(life),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		// Only for servers' certificates: not code signing, not mail.
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	return newCA(der, key)
}

func newCA(der []byte, key crypto.Signer) (*CA, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA {
		return nil, errors.New("traffic: the certificate is not a certificate authority")
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, der: der, leafKey: leafKey, leaves: map[string]*tls.Certificate{}, KeyPlace: KeyInMemory}, nil
}

// Expires returns when the authority ends.
func (c *CA) Expires() time.Time { return c.cert.NotAfter }

// NeedsRenewal reports whether the authority ends within RenewBefore.
func (c *CA) NeedsRenewal(now time.Time) bool { return !now.Before(c.cert.NotAfter.Add(-RenewBefore)) }

// LoadCA reads the certificate authority from dir, or makes one when there
// is none, it does not match its key, or it ends within RenewBefore (a
// renewed one has Renewed set, and has to be installed on the phones
// again). The certificate is kept in dir as CACertFile.
//
// keys, when not nil, keeps the private key (the macOS keychain, under
// secret.TrafficCAKeyAccount). A key an earlier Pinky left in dir as
// CAKeyFile moves into keys, and the file goes. Without keys the key is
// kept in dir as CAKeyFile, readable by this user only.
func LoadCA(dir string, keys secret.Store) (*CA, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	certPath, keyPath := filepath.Join(dir, CACertFile), filepath.Join(dir, CAKeyFile)
	certPEM, err := os.ReadFile(certPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("traffic: read the certificate authority: %w", err)
	}
	had := err == nil
	if had {
		keyPEM, place, err := readKey(ctx, keyPath, keys)
		switch {
		case err == nil:
			ca, err := parseCA(certPEM, keyPEM)
			if err == nil && !ca.NeedsRenewal(time.Now()) {
				ca.KeyPlace = place
				if place == KeyInFile && keys != nil {
					// An earlier Pinky kept it in a file: move it.
					if err := keys.Save(secret.TrafficCAKeyAccount, string(keyPEM)); err != nil {
						return nil, fmt.Errorf("traffic: move the certificate authority's key into the keychain: %w", err)
					}
					ca.KeyPlace = KeyInKeychain
					if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
						return nil, fmt.Errorf("traffic: remove the old key file: %w", err)
					}
				}
				return ca, nil
			}
		case !errors.Is(err, os.ErrNotExist) && !errors.Is(err, secret.ErrNotFound):
			return nil, fmt.Errorf("traffic: read the certificate authority's key: %w", err)
		}
	}
	ca, err := NewCA()
	if err != nil {
		return nil, err
	}
	ca.Renewed = had
	keyDER, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if keys != nil {
		if err := keys.Save(secret.TrafficCAKeyAccount, string(keyPEM)); err != nil {
			return nil, fmt.Errorf("traffic: keep the certificate authority's key in the keychain: %w", err)
		}
		ca.KeyPlace = KeyInKeychain
		if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else {
		if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
			return nil, err
		}
		ca.KeyPlace = KeyInFile
	}
	if err := writeFile(certPath, ca.CertPEM(), 0o600); err != nil {
		return nil, err
	}
	return ca, nil
}

// readKey reads the CA's key from keys, or from the file an earlier Pinky
// (or one without keys) wrote.
func readKey(ctx context.Context, keyPath string, keys secret.Store) ([]byte, string, error) {
	if keys != nil {
		k, err := secret.Get(ctx, keys, secret.TrafficCAKeyAccount)
		if err == nil {
			return []byte(k), KeyInKeychain, nil
		}
		if !errors.Is(err, secret.ErrNotFound) {
			return nil, "", err
		}
	}
	b, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, "", err
	}
	return b, KeyInFile, nil
}

func parseCA(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, errors.New("not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("the key cannot sign")
	}
	ca, err := newCA(cb.Bytes, signer)
	if err != nil {
		return nil, err
	}
	pub, ok := ca.cert.PublicKey.(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(signer.Public()) {
		return nil, errors.New("the key is not the certificate's")
	}
	return ca, nil
}

// writeFile writes a file through a temporary one, with perm from the
// start.
func writeFile(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// CertDER returns the authority's certificate, DER-encoded, as phones
// install it.
func (c *CA) CertDER() []byte { return c.der }

// CertPEM returns the authority's certificate, PEM-encoded.
func (c *CA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der})
}

// Name returns the authority's name, as phones list it.
func (c *CA) Name() string { return c.cert.Subject.CommonName }

// Fingerprint returns the certificate's SHA-256 fingerprint, as phones
// show it: "AB:CD:…".
func (c *CA) Fingerprint() string {
	sum := sha256.Sum256(c.der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

// Pool returns a pool that trusts the authority, for tests and checks.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Leaf returns a certificate for host, signed by the authority.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("traffic: no host to make a certificate for")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaves[host]; ok && time.Now().Add(leafRenew).Before(l.Leaf.NotAfter) {
		return l, nil
	}
	serial, err := serialNumber()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	end := now.Add(leafLife)
	if end.After(c.cert.NotAfter) {
		end = c.cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     end,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, c.leafKey.Public(), c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	out := &tls.Certificate{Certificate: [][]byte{der, c.der}, PrivateKey: c.leafKey, Leaf: leaf}
	if len(c.leaves) >= leafKeep {
		c.leaves = map[string]*tls.Certificate{}
	}
	c.leaves[host] = out
	return out, nil
}

func serialNumber() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}
