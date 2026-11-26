package video

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// A decoder stalls only when packets kept coming for the time and in the
// number asked for without a picture; a picture starts the count again.
func TestStallWatch(t *testing.T) {
	start := time.Now()
	var s stallWatch
	for i := range stallPackets - 1 {
		s.packet(start.Add(time.Duration(i) * 100 * time.Millisecond))
	}
	if s.stalled(start.Add(time.Minute), 3*time.Second) {
		t.Fatal("a few packets after a long still screen stalled")
	}
	s.packet(start.Add(3 * time.Second))
	if s.stalled(start.Add(2*time.Second), 3*time.Second) {
		t.Fatal("stalled before its time")
	}
	if !s.stalled(start.Add(3*time.Second), 3*time.Second) {
		t.Fatal("no stall after enough packets and time")
	}
	s.last = errors.New("bad slice")
	if err := s.err(start.Add(3 * time.Second)); !errors.Is(err, ErrStalled) {
		t.Fatalf("the error %v is not ErrStalled", err)
	}
	s.picture()
	if s.stalled(start.Add(time.Hour), time.Second) {
		t.Fatal("stalled after a picture")
	}
}

// keyStream is a gatedStream that counts requests for a key frame.
type keyStream struct {
	gatedStream
	asked atomic.Int64
}

func (s *keyStream) RequestKeyFrame() { s.asked.Add(1) }

// A decoder that gives no picture for packet after packet ends the player
// with ErrStalled, so the mirror can open the stream again, and asks the
// stream for a key frame meanwhile.
func TestPlayerStalls(t *testing.T) {
	if err := Load(); err != nil {
		if errors.Is(err, ErrNotFound) {
			t.Skip(err)
		}
		t.Fatal(err)
	}
	s := &keyStream{gatedStream: gatedStream{codec: driver.H264, w: 320, h: 640, packets: make(chan driver.Packet, 4)}}
	p, err := NewPlayer(s, PlayerOptions{YUV: true, StallAfter: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// P slices that refer to pictures the decoder never got.
	junk := []byte{0, 0, 0, 1, 0x41, 0x9a, 0x22, 0x4c, 0x10, 0x0f, 0xff, 0x00, 0x13, 0x37}
	go func() {
		defer close(s.packets)
		for i := range 400 {
			select {
			case s.packets <- driver.Packet{Data: junk, PTS: time.Duration(i) * 33 * time.Millisecond}:
			case <-p.Done():
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	select {
	case <-p.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("the player did not stop: %+v", p.Stats())
	}
	if err := p.Err(); !errors.Is(err, ErrStalled) {
		t.Fatalf("the player ended with %v; stats %+v", err, p.Stats())
	}
	t.Logf("%v; stats %+v; asked for %d key frames", p.Err(), p.Stats(), s.asked.Load())
}

// A player whose picture nobody takes while new ones come calls OnWaiting,
// at most once a second, and does not count that as a stall.
func TestPlayerOnWaiting(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := StartTestStream(ctx, TestStreamOptions{Width: 320, Height: 640, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	var waited atomic.Int64
	start := time.Now()
	p, err := NewPlayer(s, PlayerOptions{YUV: true, StallAfter: time.Second, OnWaiting: func() { waited.Add(1) }})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	for waited.Load() < 3 {
		select {
		case <-ctx.Done():
			t.Fatalf("OnWaiting came %d times: %+v", waited.Load(), p.Stats())
		case <-p.Done():
			t.Fatalf("the stream ended: %v", p.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	p.Close()
	took := time.Since(start)
	if n := waited.Load(); n > int64(took/time.Second)+1 {
		t.Fatalf("OnWaiting came %d times in %v", n, took)
	}
	if err := p.Err(); errors.Is(err, ErrStalled) {
		t.Fatalf("a player nobody took from stalled: %v", err)
	}
}

// A player whose work on one packet never ends (a decoder call that hung,
// here an OnFrame that blocks) ends with ErrStalled after StallAfter, and
// Close returns without waiting for it (#210).
func TestPlayerHangs(t *testing.T) {
	needFFmpeg(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := StartTestStream(ctx, TestStreamOptions{Width: 320, Height: 640, FPS: 30})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var hung atomic.Bool
	p, err := NewPlayer(s, PlayerOptions{YUV: true, StallAfter: 300 * time.Millisecond, OnFrame: func() {
		if hung.CompareAndSwap(false, true) {
			<-release
		}
	}})
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	defer close(release)
	start := time.Now()
	select {
	case <-p.Done():
	case <-ctx.Done():
		t.Fatalf("the player did not end: %+v", p.Stats())
	}
	if err := p.Err(); !errors.Is(err, ErrHung) || !errors.Is(err, ErrStalled) {
		t.Fatalf("the player ended with %v", err)
	}
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the hung goroutine")
	}
	t.Logf("%v, %v after the first picture", p.Err(), time.Since(start).Round(time.Millisecond))
}

// A still screen sends nothing: however long the player waits for a
// packet, it does not count as hung.
func TestPlayerWaitIsNoHang(t *testing.T) {
	needFFmpeg(t)
	s := &gatedStream{codec: driver.H264, w: 320, h: 640, packets: make(chan driver.Packet)}
	p, err := NewPlayer(s, PlayerOptions{YUV: true, StallAfter: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	select {
	case <-p.Done():
		t.Fatalf("a player waiting for packets ended: %v", p.Err())
	case <-time.After(400 * time.Millisecond):
	}
	close(s.packets)
	<-p.Done()
	if err := p.Err(); err != io.EOF {
		t.Fatalf("the player ended with %v, not io.EOF", err)
	}
}
