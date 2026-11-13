package rppair

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// opackDecode reads an OPACK value, Apple's compact encoding of property
// lists (go-ios only encodes it): what the phone says about itself in
// pair-setup. It knows dictionaries, arrays, strings, data, numbers,
// booleans, UUIDs, dates and references to earlier strings and data, which
// is what pairing info holds.
func opackDecode(b []byte) (any, error) {
	d := opackDecoder{b: b}
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	return v, nil
}

type opackDecoder struct {
	b    []byte
	refs []any // values a reference (0xA0…) may point back to
}

// errOpackEnd ends a container that has no count.
var errOpackEnd = errors.New("end of container")

func (d *opackDecoder) take(n int) ([]byte, error) {
	if n < 0 || n > len(d.b) {
		return nil, errors.New("OPACK: truncated")
	}
	v := d.b[:n]
	d.b = d.b[n:]
	return v, nil
}

func (d *opackDecoder) uint(n int) (uint64, error) {
	v, err := d.take(n)
	if err != nil {
		return 0, err
	}
	var x uint64
	for i := n - 1; i >= 0; i-- {
		x = x<<8 | uint64(v[i])
	}
	return x, nil
}

func (d *opackDecoder) value() (any, error) {
	t, err := d.take(1)
	if err != nil {
		return nil, err
	}
	switch c := t[0]; {
	case c == 0x01:
		return true, nil
	case c == 0x02:
		return false, nil
	case c == 0x03:
		return nil, errOpackEnd
	case c == 0x04:
		return nil, nil
	case c == 0x05:
		v, err := d.take(16)
		return d.ref(append([]byte(nil), v...), err)
	case c == 0x06:
		x, err := d.uint(8) // seconds since 2001, a float
		return d.ref(math.Float64frombits(x), err)
	case c >= 0x08 && c <= 0x2f:
		return int64(c - 0x08), nil
	case c >= 0x30 && c <= 0x33:
		n := 1 << (c - 0x30)
		x, err := d.uint(n)
		if err != nil {
			return nil, err
		}
		// Signed, little-endian, at its width.
		sh := 64 - 8*n
		return d.ref(int64(x<<sh)>>sh, nil)
	case c == 0x35:
		x, err := d.uint(4)
		return d.ref(float64(math.Float32frombits(uint32(x))), err)
	case c == 0x36:
		x, err := d.uint(8)
		return d.ref(math.Float64frombits(x), err)
	case c >= 0x40 && c <= 0x64:
		n, err := d.length(c, 0x40)
		if err != nil {
			return nil, err
		}
		v, err := d.take(n)
		return d.ref(string(v), err)
	case c >= 0x70 && c <= 0x94:
		n, err := d.length(c, 0x70)
		if err != nil {
			return nil, err
		}
		v, err := d.take(n)
		return d.ref(append([]byte(nil), v...), err)
	case c >= 0xa0 && c <= 0xc0:
		i := int(c - 0xa0)
		if c == 0xc0 {
			x, err := d.uint(1)
			if err != nil {
				return nil, err
			}
			i = int(x)
		}
		if i >= len(d.refs) {
			return nil, fmt.Errorf("OPACK: reference %d of %d", i, len(d.refs))
		}
		return d.refs[i], nil
	case c >= 0xd0 && c <= 0xdf:
		var out []any
		for n := int(c - 0xd0); c == 0xdf || len(out) < n; {
			v, err := d.value()
			if err == errOpackEnd && c == 0xdf {
				break
			}
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case c >= 0xe0 && c <= 0xef:
		out := map[string]any{}
		for n, i := int(c-0xe0), 0; c == 0xef || i < n; i++ {
			k, err := d.value()
			if err == errOpackEnd && c == 0xef {
				break
			}
			if err != nil {
				return nil, err
			}
			v, err := d.value()
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("OPACK: a key of type %T", k)
			}
			out[ks] = v
		}
		return out, nil
	default:
		return nil, fmt.Errorf("OPACK: type 0x%02x", c)
	}
}

// length is the length of a string (base 0x40) or data (base 0x70): up to
// 0x20 in the type itself, then 1, 2, 3 or 4 little-endian bytes.
func (d *opackDecoder) length(c, base byte) (int, error) {
	if c <= base+0x20 {
		return int(c - base), nil
	}
	x, err := d.uint(int(c - base - 0x20))
	return int(x), err
}

func (d *opackDecoder) ref(v any, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	d.refs = append(d.refs, v)
	return v, nil
}

// AuthTag is the authTag of a pairable host's advertisement: the first six
// bytes of SipHash-2-4 of the identifier, keyed by the host's altIRK, in
// reverse order (idevice, peer_device.rs).
func AuthTag(altIRK []byte, identifier string) []byte {
	var key [16]byte
	copy(key[:], altIRK)
	var out [8]byte
	binary.LittleEndian.PutUint64(out[:], siphash24(key, []byte(identifier)))
	return []byte{out[5], out[4], out[3], out[2], out[1], out[0]}
}

// siphash24 is SipHash-2-4 with a 16-byte key.
func siphash24(key [16]byte, m []byte) uint64 {
	k0 := binary.LittleEndian.Uint64(key[:8])
	k1 := binary.LittleEndian.Uint64(key[8:])
	v0 := k0 ^ 0x736f6d6570736575
	v1 := k1 ^ 0x646f72616e646f6d
	v2 := k0 ^ 0x6c7967656e657261
	v3 := k1 ^ 0x7465646279746573
	round := func() {
		v0 += v1
		v1 = bits.RotateLeft64(v1, 13)
		v1 ^= v0
		v0 = bits.RotateLeft64(v0, 32)
		v2 += v3
		v3 = bits.RotateLeft64(v3, 16)
		v3 ^= v2
		v0 += v3
		v3 = bits.RotateLeft64(v3, 21)
		v3 ^= v0
		v2 += v1
		v1 = bits.RotateLeft64(v1, 17)
		v1 ^= v2
		v2 = bits.RotateLeft64(v2, 32)
	}
	n := len(m)
	for len(m) >= 8 {
		w := binary.LittleEndian.Uint64(m)
		v3 ^= w
		round()
		round()
		v0 ^= w
		m = m[8:]
	}
	var last [8]byte
	copy(last[:], m)
	last[7] = byte(n)
	w := binary.LittleEndian.Uint64(last[:])
	v3 ^= w
	round()
	round()
	v0 ^= w
	v2 ^= 0xff
	round()
	round()
	round()
	round()
	return v0 ^ v1 ^ v2 ^ v3
}
