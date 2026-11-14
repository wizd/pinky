package h264enc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testNV12 is a picture of gradients that move with shift, in NV12.
func testNV12(w, h, shift int) (y, uv []byte) {
	y = make([]byte, w*h)
	uv = make([]byte, w*h/2)
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			y[r*w+c] = byte(16 + (c+shift+r)%220)
		}
	}
	for r := 0; r < h/2; r++ {
		for c := 0; c < w/2; c++ {
			uv[r*w+2*c] = byte(64 + (c+shift)%128)
			uv[r*w+2*c+1] = byte(64 + r%128)
		}
	}
	return y, uv
}

func TestH264Encoder(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	const w, h = 320, 640
	e, err := New(w, h, Options{FPS: 15, BitRate: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	var stream bytes.Buffer
	for i := 0; i < 6; i++ {
		y, uv := testNV12(w, h, i*8)
		out, err := e.Encode(y, uv, time.Duration(i)*66*time.Millisecond, i == 0 || i == 4)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(out) != 1 {
			t.Fatalf("frame %d: %d outputs", i, len(out))
		}
		f := out[0]
		wantKey := i == 0 || i == 4
		if f.KeyFrame != wantKey || (wantKey && len(f.Config) == 0) || len(f.Data) == 0 {
			t.Fatalf("frame %d: key %v config %d data %d", i, f.KeyFrame, len(f.Config), len(f.Data))
		}
		if wantKey {
			stream.Write(f.Config)
			if f.Config[4]&0x1f != 7 { // SPS first
				t.Fatalf("frame %d: config starts with NAL %d", i, f.Config[4]&0x1f)
			}
		}
		stream.Write(f.Data)
	}
	probe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe to check the stream")
	}
	file := filepath.Join(t.TempDir(), "t.h264")
	os.WriteFile(file, stream.Bytes(), 0o600)
	out, err := exec.Command(probe, "-v", "error", "-count_frames", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,width,height,nb_read_frames", "-of", "csv=p=0", file).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "h264,Main,320,640,6" {
		t.Fatalf("ffprobe: %v %q", err, out)
	}
}
