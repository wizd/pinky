package gateway

import (
	"context"
	"os"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/recording"
)

// recordTask records a task of n pictures and returns its id once its
// recording is saved.
func recordTask(t *testing.T, g *Gateway, v *videoDev, n int) string {
	t.Helper()
	s, _ := connect(t, g, "Lunch")
	id := taskID(t, g, s)
	st := newStreamer(t, v.open(t))
	st.config()
	st.pictures(n, true)
	mustStatus(t, s.Release(context.Background(), ReleaseRequest{}), StatusSettled)
	g.Flush()
	return id
}

func TestSmallerRecordings(t *testing.T) {
	if err := recording.ShrinkAvailable(); err != nil {
		t.Skipf("this computer cannot shrink recordings: %v", err)
	}
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	g.SetSmallerRecordings(true)
	id := recordTask(t, g, v, 30)
	g.shrinks.wait()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	// 30 pictures 2 ms apart: a burst, of which the last is kept.
	if info.Codec != driver.H264 || info.Frames >= 30 || info.Frames == 0 {
		t.Errorf("recording %+v, want fewer pictures in H.264", info)
	}
	row, err := g.store.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if row.Size != fi.Size() {
		t.Errorf("history says %d bytes, the file has %d", row.Size, fi.Size())
	}
	if _, err := os.Stat(rec.Path + ".part"); !os.IsNotExist(err) {
		t.Errorf("the new file is left next to the recording: %v", err)
	}
}

func TestOriginalRecordings(t *testing.T) {
	ctx := context.Background()
	v := newVideoDev("pixel")
	g := newVideoGateway(t, v)
	g.SetRecording(true)
	g.SetSmallerRecordings(false)
	id := recordTask(t, g, v, 30)
	g.shrinks.wait()
	rec, err := g.Recording(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	info, err := recording.Probe(rec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Frames != 30 {
		t.Errorf("recorded %d pictures, want all 30 as the phone sent them", info.Frames)
	}
}
