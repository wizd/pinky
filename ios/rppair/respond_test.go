package rppair

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/opack"
)

// phoneSide is what the phone does when the person picks a pairable host
// in Settings: the handshake as a host, then pair-setup with the PIN the
// person types.
func phoneSide(conn net.Conn, id Identity, pin string, info map[string]any) (Identity, error) {
	s := newSession(conn)
	err := s.writePlain("request", map[string]any{
		"handshake": map[string]any{"_0": map[string]any{
			"hostOptions":         map[string]any{"attemptPairVerify": false},
			"wireProtocolVersion": 26,
		}},
	})
	if err != nil {
		return id, err
	}
	m, err := s.read()
	if err != nil {
		return id, err
	}
	h, err := child(m, "plain", "_0", "response", "_1", "handshake", "_0")
	if err != nil {
		return id, err
	}
	if o, _ := h["deviceOptions"].(map[string]any); o["allowsPairSetup"] != true {
		return id, errors.New("no pair-setup offered")
	}
	return s.setupWith(id, pin, info)
}

func TestRespond(t *testing.T) {
	host, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	phoneID, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	irk := bytes.Repeat([]byte{7}, 16)
	phoneIRK := bytes.Repeat([]byte{9}, 16)
	info := map[string]any{
		"accountID":          phoneID.Identifier,
		"altIRK":             phoneIRK,
		"model":              "iPhone14,4",
		"name":               "Ann's iPhone",
		"remotepairing_udid": "00008110-0000000000000000",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, tc := range []struct {
		name  string
		typed string
	}{{"right PIN", "123456"}, {"wrong PIN", "654321"}} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			type res struct {
				id  Identity
				err error
			}
			ph := make(chan res, 1)
			go func() {
				got, err := phoneSide(b, phoneID, tc.typed, info)
				ph <- res{got, err}
				b.Close()
			}()
			var shown string
			got, phone, err := respond(ctx, a, host, Host{Name: "Pinky", Model: "Mac17,7", AltIRK: irk}, "123456", func(p string) { shown = p })
			a.Close()
			pr := <-ph
			if shown != "123456" {
				t.Errorf("shown PIN %q", shown)
			}
			if tc.typed != shown {
				if !errors.Is(err, ErrWrongPIN) {
					t.Errorf("a wrong PIN: %v; want ErrWrongPIN", err)
				}
				if pr.err == nil {
					t.Error("the phone paired with a wrong PIN")
				}
				return
			}
			if err != nil || pr.err != nil {
				t.Fatalf("host %v, phone %v", err, pr.err)
			}
			if phone.UDID != "00008110-0000000000000000" || phone.Name != "Ann's iPhone" || phone.Model != "iPhone14,4" || phone.AccountID != phoneID.Identifier {
				t.Errorf("phone = %+v", phone)
			}
			if !bytes.Equal(phone.AltIRK, phoneIRK) || !bytes.Equal(got.AltIRK, phoneIRK) {
				t.Errorf("the phone's altIRK: %x, %x", phone.AltIRK, got.AltIRK)
			}
			if got.Identifier != host.Identifier {
				t.Errorf("identity changed: %q", got.Identifier)
			}
			// The phone took the host's altIRK from M6.
			if !bytes.Equal(pr.id.AltIRK, irk) {
				t.Errorf("the phone got altIRK %x", pr.id.AltIRK)
			}
		})
	}
}

func TestRespondRefusesVerify(t *testing.T) {
	host, _ := NewIdentity()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		s := newSession(b)
		s.writePlain("request", map[string]any{"handshake": map[string]any{"_0": map[string]any{
			"hostOptions": map[string]any{"attemptPairVerify": true},
		}}})
	}()
	if _, _, err := Respond(context.Background(), a, host, Host{}, nil); err == nil {
		t.Error("answered a pair-verify")
	}
}

func TestAuthTag(t *testing.T) {
	// idevice's test vector (peer_device.rs).
	irk, err := base64.StdEncoding.DecodeString("Mgp6ZGPzXM2ku9br46vsiw==")
	if err != nil {
		t.Fatal(err)
	}
	if got := base64.StdEncoding.EncodeToString(AuthTag(irk, "2BE6E510-0325-4365-923E-B14C6F57DB3A")); got != "kXjlTr2l" {
		t.Errorf("authTag = %q", got)
	}
	// SipHash-2-4's own: key 00…0f, message 00…0e.
	var key [16]byte
	for i := range key {
		key[i] = byte(i)
	}
	msg := make([]byte, 15)
	for i := range msg {
		msg[i] = byte(i)
	}
	if got := siphash24(key, msg); got != 0xa129ca6149be45e5 {
		t.Errorf("siphash24 = %x", got)
	}
	txt := Host{Name: "Pinky", Model: "Mac17,7", AltIRK: irk}.TXT("2BE6E510-0325-4365-923E-B14C6F57DB3A")
	if txt["authTag"] != "kXjlTr2l" || txt["identifier"] != "2BE6E510-0325-4365-923E-B14C6F57DB3A" || txt["model"] != "Mac17,7" {
		t.Errorf("TXT = %v", txt)
	}
}

func TestOpackDecode(t *testing.T) {
	in := map[string]any{"name": "Ann's iPhone", "altIRK": bytes.Repeat([]byte{1}, 16), "long": string(bytes.Repeat([]byte("x"), 200))}
	b, err := opack.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	v, err := opackDecode(b)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	if m["name"] != in["name"] || !bytes.Equal(m["altIRK"].([]byte), in["altIRK"].([]byte)) || m["long"] != in["long"] {
		t.Errorf("decoded %v", m)
	}

	// A dictionary with no count, ended by 0x03: a number, booleans, an
	// array and a reference to the first string.
	raw := []byte{0xef,
		0x41, 'a', 0x0a, // "a": 2
		0x41, 'b', 0x30, 0xff, // "b": -1
		0x41, 'c', 0xd2, 0x01, 0x02, // "c": [true, false]
		0x41, 'd', 0xa0, // "d": the first string, "a"
		0x03}
	v, err = opackDecode(raw)
	if err != nil {
		t.Fatal(err)
	}
	m = v.(map[string]any)
	if m["a"] != int64(2) || m["b"] != int64(-1) || m["d"] != "a" {
		t.Errorf("decoded %v", m)
	}
	if c, _ := m["c"].([]any); len(c) != 2 || c[0] != true || c[1] != false {
		t.Errorf("c = %v", m["c"])
	}
	if _, err := opackDecode([]byte{0xe1, 0x41}); err == nil {
		t.Error("a truncated dictionary decoded")
	}
}
