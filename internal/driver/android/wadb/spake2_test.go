package wadb

import (
	"bytes"
	"testing"

	"filippo.io/edwards25519"
)

func TestSpake2Agrees(t *testing.T) {
	pw := []byte("123456 and the exported key")
	a, err := newSpake2(spakeAlice, pairClientName, pairServerName, pw, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newSpake2(spakeBob, pairServerName, pairClientName, pw, nil)
	if err != nil {
		t.Fatal(err)
	}
	ka, err := a.finish(b.msg[:])
	if err != nil {
		t.Fatal(err)
	}
	kb, err := b.finish(a.msg[:])
	if err != nil {
		t.Fatal(err)
	}
	if ka != kb {
		t.Fatal("Alice and Bob derived different keys from the same password")
	}
}

func TestSpake2WrongPassword(t *testing.T) {
	a, _ := newSpake2(spakeAlice, pairClientName, pairServerName, []byte("123456"), nil)
	b, _ := newSpake2(spakeBob, pairServerName, pairClientName, []byte("654321"), nil)
	ka, _ := a.finish(b.msg[:])
	kb, _ := b.finish(a.msg[:])
	if ka == kb {
		t.Fatal("different passwords gave the same key")
	}
}

func TestSpake2Deterministic(t *testing.T) {
	// The same randomness and password give the same message: nothing else
	// feeds into it.
	seed := func(b []byte) error {
		for i := range b {
			b[i] = byte(i)
		}
		return nil
	}
	a1, _ := newSpake2(spakeAlice, pairClientName, pairServerName, []byte("pw"), seed)
	a2, _ := newSpake2(spakeAlice, pairClientName, pairServerName, []byte("pw"), seed)
	if a1.msg != a2.msg {
		t.Fatal("same inputs, different messages")
	}
	b1, _ := newSpake2(spakeBob, pairServerName, pairClientName, []byte("pw"), seed)
	if bytes.Equal(a1.msg[:], b1.msg[:]) {
		t.Fatal("Alice's and Bob's masks are the same")
	}
}

func TestSpake2BadPeerMessage(t *testing.T) {
	a, _ := newSpake2(spakeAlice, pairClientName, pairServerName, []byte("pw"), nil)
	if _, err := a.finish(make([]byte, 31)); err == nil {
		t.Fatal("a short message was taken")
	}
	// About half of all y coordinates are on no point of the curve.
	var bad []byte
	for y := byte(2); bad == nil; y++ {
		b := make([]byte, 32)
		b[0] = y
		if _, err := new(edwards25519.Point).SetBytes(b); err != nil {
			bad = b
		}
	}
	if _, err := a.finish(bad); err == nil {
		t.Fatal("a message that is not a point was taken")
	}
}
