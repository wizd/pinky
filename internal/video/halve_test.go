package video

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

// planeSizes are the sizes in bytes of the rows of a picture's planes, and
// their numbers of rows: NV12's chroma two bytes a sample.
func planeSizes(nv12 bool, w, h int) [3][2]int {
	cw, ch := (w+1)/2, (h+1)/2
	if nv12 {
		return [3][2]int{{w, h}, {2 * cw, ch}}
	}
	return [3][2]int{{w, h}, {cw, ch}, {cw, ch}}
}

// paddedYUV is a w×h picture in rows pad bytes longer than their samples,
// padded with 0xEE, the last row of each plane without its padding, as
// decoders leave pictures; fill gives each sample.
func paddedYUV(nv12 bool, w, h, pad int, fill func(plane, x, y int) byte) *YUV {
	p := &YUV{NV12: nv12, Width: w, Height: h, Matrix: BT709, FullRange: true}
	for i, s := range planeSizes(nv12, w, h) {
		if s[0] == 0 {
			continue
		}
		p.Strides[i] = s[0] + pad
		p.Planes[i] = bytes.Repeat([]byte{0xEE}, p.Strides[i]*(s[1]-1)+s[0])
		for y := range s[1] {
			for x := range s[0] {
				p.Planes[i][y*p.Strides[i]+x] = fill(i, x, y)
			}
		}
	}
	return p
}

// halveRef is Halve as it reads, one sample at a time.
func halveRef(src *YUV) *YUV {
	dst := &YUV{Matrix: src.Matrix, FullRange: src.FullRange}
	dst.layout(src.NV12, (src.Width+1)/2, (src.Height+1)/2)
	for i, s := range planeSizes(src.NV12, src.Width, src.Height) {
		step := 1 // bytes a sample
		if src.NV12 && i == 1 {
			step = 2
		}
		w, h := s[0]/step, s[1]
		at := func(x, y, c int) int {
			return int(src.Planes[i][min(y, h-1)*src.Strides[i]+min(x, w-1)*step+c])
		}
		for y := range (h + 1) / 2 {
			for x := range (w + 1) / 2 {
				for c := range step {
					sum := at(2*x, 2*y, c) + at(2*x+1, 2*y, c) + at(2*x, 2*y+1, c) + at(2*x+1, 2*y+1, c)
					dst.Planes[i][y*dst.Strides[i]+x*step+c] = byte((sum + 2) / 4)
				}
			}
		}
	}
	return dst
}

// TestHalveSamples: Halve averages each 2×2 of each plane, rounded, an
// odd last row or column counting twice, NV12's Cb with Cb and Cr with Cr,
// and leaves out the padding of rows longer than their samples.
func TestHalveSamples(t *testing.T) {
	type planes [3][]byte
	for _, tc := range []struct {
		name string
		nv12 bool
		w, h int
		in   planes // rows of samples, each row's bytes in turn
		want planes
	}{
		{"I420 4×2", false, 4, 2,
			planes{{0, 4, 8, 12, 2, 6, 10, 14}, {10, 20}, {200, 101}},
			planes{{3, 11}, {15}, {151}}},
		{"NV12 3×3", true, 3, 3,
			planes{{1, 2, 3, 4, 5, 6, 7, 8, 9}, {10, 100, 20, 200, 30, 50, 40, 60}},
			planes{{3, 5, 8, 9}, {25, 103}}},
		{"rounding", false, 10, 2,
			planes{{1, 1, 1, 1, 255, 255, 0, 0, 0, 1, 1, 2, 2, 2, 255, 255, 0, 1, 1, 1}, {7, 8, 9, 10, 3}, {0, 1, 1, 1, 254}},
			planes{{1, 2, 255, 0, 1}, {8, 10, 3}, {1, 1, 254}}},
		{"I420 18×2, eight at a time", false, 18, 2,
			planes{
				append(steps(18, 0, 10), steps(18, 1, 10)...),
				{0, 255, 3, 4, 5, 6, 7, 8, 9},
				{255, 255, 254, 254, 0, 0, 1, 2, 3},
			},
			planes{{6, 26, 46, 66, 86, 106, 126, 146, 166}, {128, 4, 6, 8, 9}, {255, 254, 0, 2, 3}}},
		{"NV12 16×2, four pairs at a time", true, 16, 2,
			planes{
				append(bytes.Repeat([]byte{255}, 16), bytes.Repeat([]byte{254}, 16)...),
				{0, 250, 20, 230, 40, 210, 60, 190, 80, 170, 100, 150, 120, 130, 140, 110},
			},
			planes{bytes.Repeat([]byte{255}, 8), {10, 240, 50, 200, 90, 160, 130, 120}}},
	} {
		for _, pad := range []int{0, 6} {
			sizes := planeSizes(tc.nv12, tc.w, tc.h)
			src := paddedYUV(tc.nv12, tc.w, tc.h, pad, func(i, x, y int) byte { return tc.in[i][y*sizes[i][0]+x] })
			var dst YUV
			Halve(&dst, src)
			w, h := (tc.w+1)/2, (tc.h+1)/2
			if dst.NV12 != tc.nv12 || dst.Width != w || dst.Height != h || dst.Matrix != BT709 || !dst.FullRange {
				t.Errorf("%s, padded %d: a %v×%v picture, NV12 %v, matrix %v, full range %v; want %d×%d, NV12 %v, BT.709, full range",
					tc.name, pad, dst.Width, dst.Height, dst.NV12, dst.Matrix, dst.FullRange, w, h, tc.nv12)
			}
			for i, want := range tc.want {
				if !bytes.Equal(dst.Planes[i], want) {
					t.Errorf("%s, padded %d: plane %d is %v, want %v", tc.name, pad, i, dst.Planes[i], want)
				}
			}
		}
	}
}

// steps returns n bytes from start, step apart.
func steps(n, start, step int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(start + i*step)
	}
	return b
}

// TestHalveMatchesReference: Halve, which averages eight samples at a time
// in 64-bit words, gives what halveRef does, sample for sample, for
// pictures of every width up to 70 and of odd and even heights, in rows of
// their samples or longer, and for a phone's screen.
func TestHalveMatchesReference(t *testing.T) {
	r := rand.New(rand.NewPCG(233, 1))
	random := func(int, int, int) byte { return byte(r.IntN(256)) }
	type size struct{ w, h, pad int }
	var sizes []size
	for w := 1; w <= 70; w++ {
		for _, h := range []int{1, 2, 3, 4, 5, 8, 9} {
			for _, pad := range []int{0, 2, 16} {
				sizes = append(sizes, size{w, h, pad})
			}
		}
	}
	sizes = append(sizes, size{1206, 2622, 0}, size{1205, 2621, 64}, size{1080, 2400, 0})
	var dst YUV // reused, as a mirror reuses its planes
	for _, s := range sizes {
		for _, nv12 := range []bool{true, false} {
			src := paddedYUV(nv12, s.w, s.h, s.pad, random)
			Halve(&dst, src)
			if want := halveRef(src); !dst.Equal(want) {
				t.Fatalf("NV12 %v, %d×%d in rows %d bytes longer: Halve gives %+v, want %+v", nv12, s.w, s.h, s.pad, dst, want)
			}
		}
	}
}

// TestHalveReusesPlanes: halving a picture of a size it halved before
// allocates nothing.
func TestHalveReusesPlanes(t *testing.T) {
	src := paddedYUV(true, 1206, 2622, 0, func(int, int, int) byte { return 128 })
	var half, quarter YUV
	Halve(&half, src)
	Halve(&quarter, &half)
	if n := testing.AllocsPerRun(3, func() { Halve(&half, src); Halve(&quarter, &half) }); n != 0 {
		t.Errorf("halving again allocates %v times", n)
	}
	if quarter.Width != 302 || quarter.Height != 656 {
		t.Errorf("1206×2622 halved twice is %d×%d, want 302×656", quarter.Width, quarter.Height)
	}
}

// BenchmarkHalve halves a phone's screen, 1206×2622 as an iPhone 17 Pro
// sends it, once and twice (the second from the first, as a mirror does),
// in both layouts; reference is halveRef's one sample at a time.
func BenchmarkHalve(b *testing.B) {
	r := rand.New(rand.NewPCG(233, 2))
	for _, nv12 := range []bool{true, false} {
		layout := map[bool]string{true: "NV12", false: "I420"}[nv12]
		src := paddedYUV(nv12, 1206, 2622, 0, func(int, int, int) byte { return byte(r.IntN(256)) })
		for _, times := range []int{1, 2} {
			b.Run(fmt.Sprintf("%s/%d", layout, times), func(b *testing.B) {
				var l [2]YUV
				for b.Loop() {
					Halve(&l[0], src)
					if times == 2 {
						Halve(&l[1], &l[0])
					}
				}
			})
		}
		b.Run(layout+"/reference", func(b *testing.B) {
			for b.Loop() {
				halveRef(src)
			}
		})
	}
}
