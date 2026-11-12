// Package vcorplink connects Pinky to a VCorp Super Agent account, so the
// account's cloud tasks can use the phones on this computer.
//
// Pinky dials out to the platform over a WebSocket (wss://<origin>/api/ext/ws),
// as the vcorp-chrome extension does: it pairs once with an 8-character code
// from the platform's web page and an ECDSA P-256 key that never leaves this
// computer, then answers each connection's challenge with a signature. The
// platform sends phone_* tool calls; Pinky runs them through its own MCP
// server, one MCP session per cloud task, so every call goes through the
// gateway's lease, policy and approval like any other agent's. Approvals
// are still answered in the Pinky app, never from the cloud.
package vcorplink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Config is what pairing leaves on this computer, in the config folder next
// to the agent token, readable by this user only.
type Config struct {
	Origin   string `json:"origin"`   // https://maker.example.com
	DeviceID string `json:"deviceId"` // the platform's id for this Pinky
	Account  string `json:"account"`  // the account it is paired with, for display
	Key      string `json:"key"`      // PKCS#8, base64
	Name     string `json:"name"`     // shown on the platform
}

// File is where the config lives in a config folder.
func File(configDir string) string { return filepath.Join(configDir, "vcorp.json") }

// Load reads the config. A missing file is fs.ErrNotExist: not paired.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Origin == "" || c.DeviceID == "" || c.Key == "" {
		return nil, fmt.Errorf("%s: incomplete", path)
	}
	return &c, nil
}

// Save writes the config, readable by this user only.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove forgets the pairing. Not paired is not an error.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// NormOrigin turns what the person typed into scheme://host[:port].
func NormOrigin(s string) (string, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a web address: %q", s)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("the address must start with https://: %q", s)
	}
	if u.Scheme == "http" && !isLocal(u.Hostname()) {
		return "", fmt.Errorf("only https is allowed for %s", u.Host)
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLocal(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// NewKey makes a P-256 key and returns it as PKCS#8 base64.
func NewKey() (string, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

func parseKey(s string) (*ecdsa.PrivateKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key")
	}
	return ec, nil
}

// PublicJWK is the public half as the platform stores it: kty, crv, x, y.
func PublicJWK(key string) (map[string]string, error) {
	k, err := parseKey(key)
	if err != nil {
		return nil, err
	}
	pub, err := k.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	b := pub.Bytes() // 0x04 || X || Y
	if len(b) != 65 {
		return nil, errors.New("unexpected public key length")
	}
	enc := base64.RawURLEncoding.EncodeToString
	return map[string]string{"kty": "EC", "crv": "P-256", "x": enc(b[1:33]), "y": enc(b[33:])}, nil
}

// helloText is what the platform checks the signature against; the same
// text as vcorp-chrome's, so a signature is bound to one site and one
// connection.
func helloText(origin, deviceID, nonce string) string {
	return "vcorp-cloud-v1\n" + origin + "\n" + deviceID + "\n" + nonce
}

// Sign signs the challenge: ECDSA-SHA256, r||s (IEEE P1363), base64url.
func Sign(key, origin, deviceID, nonce string) (string, error) {
	k, err := parseKey(key)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256([]byte(helloText(origin, deviceID, nonce)))
	r, s, err := ecdsa.Sign(rand.Reader, k, h[:])
	if err != nil {
		return "", err
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// PhoneID is the id the platform knows a phone by. It is derived from the
// phone's own id (a UDID or adb serial) and this pairing, so the platform
// never learns the phone's serial and two pairings never share ids.
func PhoneID(deviceID, phone string) string {
	h := sha256.Sum256([]byte("pinky-vcorp\n" + deviceID + "\n" + phone))
	return "ph_" + hex.EncodeToString(h[:8])
}
