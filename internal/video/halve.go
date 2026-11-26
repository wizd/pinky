package video

import "encoding/binary"

// Halve writes src at half its size into dst, reusing dst's planes: in
// src's layout and colors, half as wide and high, rounded up. Each sample
// of a plane is the rounded average of the 2×2 of src's it covers, an odd
// last row or column counting twice, as MyGo halves a bitmap it shows much
// smaller. A UI shows a picture of video at half its size or less from its
// halvings (ui's mirror): a renderer that takes one sample of the picture
// a pixel would skip most of it, and fine text breaks up.
//
// It reads src's planes in rows of its strides and writes dst's in rows as
// long as their samples. For a phone's screen, 1206×2622, it takes about
// a third of a millisecond (docs/notes/client.md).
func Halve(dst, src *YUV) {
	dst.layout(src.NV12, (src.Width+1)/2, (src.Height+1)/2)
	dst.Matrix, dst.FullRange = src.Matrix, src.FullRange
	halvePlane(dst.Planes[0], dst.Strides[0], src.Planes[0], src.Strides[0], src.Width, src.Height, halveRow)
	cw, ch := (src.Width+1)/2, (src.Height+1)/2
	if src.NV12 {
		// Pairs of Cb and Cr, two bytes a sample.
		halvePlane(dst.Planes[1], dst.Strides[1], src.Planes[1], src.Strides[1], 2*cw, ch, halvePairRow)
		return
	}
	halvePlane(dst.Planes[1], dst.Strides[1], src.Planes[1], src.Strides[1], cw, ch, halveRow)
	halvePlane(dst.Planes[2], dst.Strides[2], src.Planes[2], src.Strides[2], cw, ch, halveRow)
}

// halvePlane halves the h rows of n bytes of a plane, in rows of stride
// bytes, into dst, whose rows of out bytes hold just their samples, with
// row: two rows of src, the second the first again for an odd last one,
// into one of dst.
func halvePlane(dst []byte, out int, src []byte, stride, n, h int, row func(out, r0, r1 []byte)) {
	if n == 0 {
		return
	}
	for y := range (h + 1) / 2 {
		r0 := src[2*y*stride:][:n]
		r1 := r0
		if 2*y+1 < h {
			r1 = src[(2*y+1)*stride:][:n]
		}
		row(dst[y*out:][:out], r0, r1)
	}
}

// halveRow writes into out the averages of the 2×2 bytes of r0 and r1, two
// rows of a plane of a byte a sample, as long as each other: out is half
// as long, rounded up, and the last of an odd row counts twice.
func halveRow(out, r0, r1 []byte) {
	// Eight averages at a time, of 16 bytes of each row, in 64-bit words.
	for len(r0) >= 16 && len(r1) >= 16 && len(out) >= 8 {
		lo := avg2x2(binary.LittleEndian.Uint64(r0), binary.LittleEndian.Uint64(r1))
		hi := avg2x2(binary.LittleEndian.Uint64(r0[8:]), binary.LittleEndian.Uint64(r1[8:]))
		binary.LittleEndian.PutUint64(out, lo|hi<<32)
		r0, r1, out = r0[16:], r1[16:], out[8:]
	}
	last := len(r0) - 1
	for i := range out {
		a, b := 2*i, min(2*i+1, last)
		out[i] = byte((uint(r0[a]) + uint(r0[b]) + uint(r1[a]) + uint(r1[b]) + 2) >> 2)
	}
}

// halvePairRow is halveRow for NV12's chroma, pairs of bytes, Cb and Cr:
// each pair of out averages the 2×2 pairs it covers, Cb with Cb and Cr
// with Cr.
func halvePairRow(out, r0, r1 []byte) {
	// Four pairs at a time, of eight pairs of each row.
	for len(r0) >= 16 && len(r1) >= 16 && len(out) >= 8 {
		lo := avgPairs(binary.LittleEndian.Uint64(r0), binary.LittleEndian.Uint64(r1))
		hi := avgPairs(binary.LittleEndian.Uint64(r0[8:]), binary.LittleEndian.Uint64(r1[8:]))
		binary.LittleEndian.PutUint64(out, lo|hi<<32)
		r0, r1, out = r0[16:], r1[16:], out[8:]
	}
	last := len(r0)/2 - 1 // the last pair
	for i := range len(out) / 2 {
		a, b := 4*i, 2*min(2*i+1, last)
		out[2*i] = byte((uint(r0[a]) + uint(r0[b]) + uint(r1[a]) + uint(r1[b]) + 2) >> 2)
		out[2*i+1] = byte((uint(r0[a+1]) + uint(r0[b+1]) + uint(r1[a+1]) + uint(r1[b+1]) + 2) >> 2)
	}
}

// lanes picks the even bytes of a 64-bit word, each into a 16-bit lane of
// its own, where the sum of a few bytes does not carry into the next.
const lanes = 0x00FF00FF00FF00FF

// avg2x2 returns the rounded averages of the 2×2 bytes of a and b, the
// same eight bytes of two rows, little-endian: four bytes, the low half of
// the result.
func avg2x2(a, b uint64) uint64 {
	// Each lane sums a byte pair of each row, and 2 to round: at most
	// 4×255+2, ten bits.
	s := a&lanes + a>>8&lanes + b&lanes + b>>8&lanes + 0x0002000200020002
	// A quarter of each lane: shifted, a lane's low byte takes its bits
	// 2 to 9; the next lane's bits shifted in land above them, masked off.
	s = s >> 2 & lanes
	// The four bytes, at bits 0, 16, 32 and 48, packed together.
	s = (s | s>>8) & 0x0000FFFF0000FFFF
	return (s | s>>16) & 0xFFFFFFFF
}

// avgPairs is avg2x2 for four pairs of Cb and Cr of two rows: the two pairs
// of the result, the low half, average the 2×2 pairs each covers.
func avgPairs(a, b uint64) uint64 {
	// The Cb, and the Cr, of the four pairs of both rows, a lane each.
	cb := a&lanes + b&lanes
	cr := a>>8&lanes + b>>8&lanes
	// Lanes 0 and 2 take in the next: the sums of two pairs of two rows,
	// at most 4×255. Lanes 1 and 3 go unused.
	cb += cb >> 16
	cr += cr >> 16
	const round = 0x0000000200000002
	cb = (cb + round) >> 2 & 0x000000FF000000FF
	cr = (cr + round) >> 2 & 0x000000FF000000FF
	// Cb and Cr at bits 0 and 8, and 32 and 40, packed together.
	s := cb | cr<<8
	return (s | s>>16) & 0xFFFFFFFF
}
