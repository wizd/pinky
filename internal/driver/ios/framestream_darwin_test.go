package ios

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/h264enc"
)

func testPicture(w, h, shift int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x + shift), uint8(y), uint8(x ^ y), 255})
		}
	}
	return img
}

// TestFrameStream runs frames through startFrameStream: packets in order,
// a configuration before the first key frame, unchanged frames skipped.
func TestFrameStream(t *testing.T) {
	if err := h264enc.Available(); err != nil {
		t.Skip(err)
	}
	src := &fakeFrames{}
	for i := 0; i < 5; i++ {
		key := uint64(i / 2) // pairs of equal frames
		pic := testPicture(160, 320, int(key)*16)
		src.frames = append(src.frames, frame{key: key + 1, decode: func() (image.Image, error) { return pic, nil }})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := startFrameStream(ctx, func(context.Context) (frameSource, error) { return src, nil }, 160, 320)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := s.Size(); w != 160 || h != 320 {
		t.Fatalf("size %dx%d", w, h)
	}
	var kinds []string
	timeout := time.After(5 * time.Second)
	want := 4
read:
	for len(kinds) < want+1 {
		if len(kinds) == want {
			timeout = time.After(300 * time.Millisecond) // nothing more should come
		}
		select {
		case p, ok := <-s.Packets():
			if !ok {
				break read
			}
			switch {
			case p.Config:
				kinds = append(kinds, "config")
			case p.KeyFrame:
				kinds = append(kinds, "key")
			default:
				kinds = append(kinds, "delta")
			}
		case <-timeout:
			break read
		}
	}
	// Five frames, three distinct: config, key, delta, delta, and nothing
	// for the repeats.
	if got := strings.Join(kinds, ","); got != "config,key,delta,delta" {
		t.Fatalf("packets %s", got)
	}
	s.Close()
	if err := s.Err(); err != nil {
		t.Fatalf("err %v", err)
	}
	if _, ok := <-s.Packets(); ok {
		t.Fatal("packets after Close")
	}
}

type fakeFrames struct {
	frames []frame
	i      int
}

func (f *fakeFrames) next(ctx context.Context) (frame, error) {
	if f.i >= len(f.frames) {
		<-ctx.Done() // a source that ends ends the stream
		return frame{}, ctx.Err()
	}
	f.i++
	return f.frames[f.i-1], nil
}

func (f *fakeFrames) Close() error { return nil }
