package rppair

import (
	"bytes"
	"fmt"
)

// TLV8 item types of the pairing messages (HomeKit pairing).
const (
	tMethod        = 0x00
	tIdentifier    = 0x01
	tSalt          = 0x02
	tPublicKey     = 0x03
	tProof         = 0x04
	tEncryptedData = 0x05
	tState         = 0x06
	tError         = 0x07
	tSignature     = 0x0a
	tInfo          = 0x11
)

// Pairing states.
const (
	stateM1 = 0x01
	stateM2 = 0x02
	stateM3 = 0x03
	stateM4 = 0x04
	stateM5 = 0x05
	stateM6 = 0x06
)

// tlv builds a TLV8 message. Values longer than 255 bytes are split into
// items of the same type.
type tlv struct{ b bytes.Buffer }

func (t *tlv) add(typ byte, v []byte) {
	for {
		n := min(len(v), 255)
		t.b.WriteByte(typ)
		t.b.WriteByte(byte(n))
		t.b.Write(v[:n])
		v = v[n:]
		if len(v) == 0 {
			return
		}
	}
}

func (t *tlv) bytes() []byte { return t.b.Bytes() }

// tlvRead returns the value of type typ, its fragments joined; nil when
// there is none.
func tlvRead(b []byte, typ byte) []byte {
	var out []byte
	for len(b) >= 2 {
		t, n := b[0], int(b[1])
		b = b[2:]
		if n > len(b) {
			n = len(b)
		}
		if t == typ {
			out = append(out, b[:n]...)
		}
		b = b[n:]
	}
	return out
}

// tlvError is a pairing error code.
type tlvError byte

var tlvErrorNames = [...]string{"reserved", "unknown", "authentication", "backoff", "unknown peer", "max peers", "max tries"}

func (e tlvError) Error() string {
	if int(e) < len(tlvErrorNames) {
		if e == 4 {
			return ErrUnknownPeer.Error()
		}
		return "pairing error: " + tlvErrorNames[e]
	}
	return fmt.Sprintf("pairing error %d", byte(e))
}

// Is lets errors.Is(err, ErrUnknownPeer) see code 4.
func (e tlvError) Is(target error) bool { return e == 4 && target == ErrUnknownPeer }
