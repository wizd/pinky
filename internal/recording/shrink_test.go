package recording

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

func needShrink(t *testing.T) {
	t.Helper()
	if err := ShrinkAvailable(); err != nil {
		t.Skipf("this computer cannot shrink recordings: %v", err)
	}
}

// decoded is a recording's pictures, decoded.
type decoded struct {
	at  []time.Duration
	pic []*image.RGBA
}

// decodeAll decodes every picture of the recording at path.
func decodeAll(t *testing.T, path string) decoded {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idx, err := readIndex(f)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := video.NewDecoder(idx.codec, video.DecoderOptions{Software: true})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()
	var out decoded
	receive := func() {
		for {
			pic, ok, err := dec.Receive()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				return
			}
			img := image.NewRGBA(image.Rect(0, 0, pic.Width, pic.Height))
			if err := dec.Convert(img); err != nil {
				t.Fatal(err)
			}
			out.at = append(out.at, pic.PTS)
			out.pic = append(out.pic, img)
		}
	}
	if err := dec.Send(driver.Packet{Data: annexB(idx.params), Config: true}); err != nil {
		t.Fatal(err)
	}
	for _, s := range idx.samples {
		buf := make([]byte, s.size)
		if _, err := f.ReadAt(buf, s.off); err != nil {
			t.Fatal(err)
		}
		cfg, pic := idx.split(buf)
		if len(cfg) > 0 {
			dec.Send(driver.Packet{Data: annexB(cfg), Config: true})
		}
		if err := dec.Send(driver.Packet{Data: annexB(pic), KeyFrame: s.key, PTS: fromTicks(s.t)}); err != nil {
			t.Fatal(err)
		}
		receive()
	}
	return out
}

// diff is the mean difference of two pictures' RGB samples, after scaling
// b to a's size by nearest neighbour.
func diff(a, b *image.RGBA) float64 {
	aw, ah := a.Bounds().Dx(), a.Bounds().Dy()
	bw, bh := b.Bounds().Dx(), b.Bounds().Dy()
	var sum, n float64
	for y := range ah {
		for x := range aw {
			pa := a.Pix[y*a.Stride+4*x:]
			pb := b.Pix[(y*bh/ah)*b.Stride+4*(x*bw/aw):]
			for c := range 3 {
				d := float64(pa[c]) - float64(pb[c])
				if d < 0 {
					d = -d
				}
				sum += d
				n++
			}
		}
	}
	return sum / n
}

func TestShrink(t *testing.T) {
	needShrink(t)
	for _, tc := range []struct {
		name  string
		codec driver.Codec
	}{{"testsrc.h264", driver.H264}, {"testsrc.hevc", driver.HEVC}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// 30 pictures at 30 a second: a burst, as when a screen scrolls.
			src := write(t, dir, tc.codec, packets(t, tc.name, tc.codec), 30)
			dst := filepath.Join(dir, "small.mp4")
			o := ShrinkOptions{LongSide: 64, FPS: 10, BitRate: 200_000, KeyEvery: 100}
			if err := Shrink(context.Background(), src, dst, o); err != nil {
				t.Fatal(err)
			}
			in, err := Probe(src)
			if err != nil {
				t.Fatal(err)
			}
			info, err := Probe(dst)
			if err != nil {
				t.Fatal(err)
			}
			if info.Codec != driver.H264 || info.Width != 64 || info.Height != 42 {
				t.Errorf("shrunk to %+v, want H.264 at 64x42", info)
			}
			if d := info.Length - in.Length; d < -time.Millisecond || d > time.Millisecond {
				t.Errorf("length %v, want %v", info.Length, in.Length)
			}
			// At most 10 a second, and the last picture of the burst kept.
			if info.Frames < 9 || info.Frames > 11 {
				t.Errorf("%d pictures in a second, want about 10", info.Frames)
			}
			orig, small := decodeAll(t, src), decodeAll(t, dst)
			if len(small.pic) != info.Frames {
				t.Fatalf("decoded %d of %d pictures", len(small.pic), info.Frames)
			}
			// The last may come sooner: the recording ends before its slot.
			for i := 1; i < len(small.at)-1; i++ {
				if gap := small.at[i] - small.at[i-1]; gap < 100*time.Millisecond-time.Millisecond {
					t.Errorf("pictures %d and %d only %v apart", i-1, i, gap)
				}
			}
			last, first := small.pic[len(small.pic)-1], orig.pic[0]
			if dl, df := diff(last, orig.pic[len(orig.pic)-1]), diff(last, first); dl >= df/2 {
				t.Errorf("the last picture is not the burst's last: %.1f from it, %.1f from the first", dl, df)
			}
		})
	}
}

func TestShrinkKeepsTurnsAndSizeChanges(t *testing.T) {
	needShrink(t)
	dir := t.TempDir()
	// The phone turns after a second: the picture changes size and the
	// turn changes, as an iPhone on iOS 27 sends it.
	path := filepath.Join(dir, "t.mp4")
	w, err := Create(path, driver.H264)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Duration(0)
	for i, part := range [][]driver.Packet{packets(t, "testsrc.h264", driver.H264)[:12], packets(t, "rotated.h264", driver.H264)} {
		for _, p := range part {
			p.Turn = i
			if err := w.Write(p, at); err != nil {
				t.Fatal(err)
			}
			if !p.Config {
				at += 100 * time.Millisecond
			}
		}
	}
	if err := w.Close(at); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "small.mp4")
	if err := Shrink(context.Background(), path, dst, ShrinkOptions{LongSide: 48, FPS: 10, BitRate: 200_000, KeyEvery: 100}); err != nil {
		t.Fatal(err)
	}
	small := decodeAll(t, dst)
	sizes := map[image.Point]bool{}
	for _, p := range small.pic {
		sizes[p.Bounds().Size()] = true
	}
	if !sizes[image.Pt(48, 32)] || !sizes[image.Pt(32, 48)] || len(sizes) != 2 {
		t.Errorf("sizes %v, want 48x32 then 32x48", sizes)
	}
	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idx, err := readIndex(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.turnAt(idx.samples[0].t); got != 0 {
		t.Errorf("turn at the start %d, want 0", got)
	}
	if got := idx.turnAt(idx.samples[len(idx.samples)-1].t); got != 1 {
		t.Errorf("turn at the end %d, want 1", got)
	}
}

func TestShrinkCancelled(t *testing.T) {
	needShrink(t)
	dir := t.TempDir()
	src := write(t, dir, driver.H264, packets(t, "testsrc.h264", driver.H264), 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dst := filepath.Join(dir, "small.mp4")
	if err := Shrink(ctx, src, dst, Smaller); err == nil {
		t.Fatal("a cancelled shrink succeeded")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("a cancelled shrink left its file: %v", err)
	}
}

func TestFitSize(t *testing.T) {
	for _, tc := range []struct{ w, h, long, ww, wh int }{
		{1080, 2340, 1280, 590, 1280},
		{2340, 1080, 1280, 1280, 590},
		{1136, 2464, 1280, 590, 1280},
		{720, 1280, 1280, 720, 1280},
		{563, 1219, 1280, 562, 1218}, // smaller: kept, in even numbers
	} {
		if w, h := fitSize(tc.w, tc.h, tc.long); w != tc.ww || h != tc.wh {
			t.Errorf("fitSize(%d, %d, %d) = %dx%d, want %dx%d", tc.w, tc.h, tc.long, w, h, tc.ww, tc.wh)
		}
	}
}
