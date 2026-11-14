package recording

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/h264enc"
	"github.com/DIYgod/pinky/internal/video"
)

// ShrinkOptions say how small Shrink makes a recording.
type ShrinkOptions struct {
	// LongSide is the most pixels of the picture's longer side; a smaller
	// picture keeps its size.
	LongSide int
	// FPS is the most pictures a second. The last picture of a quicker
	// burst is always kept, so the screen a step settled on stays in.
	FPS int
	// BitRate is the H.264 bit rate, in bits per second.
	BitRate int
	// KeyEvery is the most pictures from one key frame to the next, which
	// is about how far a seek decodes ahead.
	KeyEvery int
}

// Smaller is what the Smaller recording quality makes of a recording:
// text in the phone's apps stays readable at about a seventh of the size
// of the phone's own stream (docs/notes/recording-size.md).
var Smaller = ShrinkOptions{LongSide: 1280, FPS: 10, BitRate: 800_000, KeyEvery: 100}

// ShrinkAvailable reports whether this computer can shrink recordings:
// it needs an H.264 encoder (internal/h264enc) and FFmpeg's decoders.
func ShrinkAvailable() error {
	if err := h264enc.Available(); err != nil {
		return err
	}
	return video.Load()
}

// Shrink encodes the recording at src again into a new one at dst, smaller
// as o says, in H.264: the same length, the pictures at the times they
// were shown, and the turns. It works as fast as the computer decodes and
// encodes, and leaves src as it is; dst is removed when it fails.
func Shrink(ctx context.Context, src, dst string, o ShrinkOptions) (err error) {
	if o.LongSide <= 0 || o.FPS <= 0 || o.BitRate <= 0 {
		return errors.New("recording: shrink options not set")
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	idx, err := readIndex(f)
	if err != nil {
		return err
	}
	if len(idx.samples) == 0 {
		return errors.New("recording: nothing to shrink")
	}
	var length time.Duration
	for _, s := range idx.samples {
		length = max(length, fromTicks(s.t+uint64(s.dur)))
	}

	dec, err := video.NewDecoder(idx.codec, video.DecoderOptions{Smooth: true, Threads: 2, ConvertThreads: 1})
	if err != nil {
		return err
	}
	defer dec.Close()
	w, err := Create(dst, driver.H264)
	if err != nil {
		return err
	}
	sh := &shrinker{o: o, w: w, interval: time.Second / time.Duration(o.FPS), turns: idx}
	defer func() {
		if err == nil {
			err = w.Close(length)
		} else {
			w.Close(length)
		}
		if sh.enc != nil {
			sh.enc.Close()
		}
		if err != nil {
			os.Remove(dst)
		}
	}()

	if len(idx.params) > 0 {
		if err := dec.Send(driver.Packet{Data: annexB(idx.params), Config: true}); err != nil {
			return err
		}
	}
	for i, s := range idx.samples {
		if i%32 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		buf := make([]byte, s.size)
		if _, err := f.ReadAt(buf, s.off); err != nil {
			return err
		}
		cfg, pic := idx.split(buf)
		if len(cfg) > 0 {
			if err := dec.Send(driver.Packet{Data: annexB(cfg), Config: true}); err != nil {
				return err
			}
		}
		if len(pic) == 0 {
			continue
		}
		if err := dec.Send(driver.Packet{Data: annexB(pic), KeyFrame: s.key, PTS: fromTicks(s.t)}); err != nil {
			// A damaged picture: the decoder picks up again at the
			// next key frame, as a mirror does.
			dec.Flush()
			continue
		}
		if err := sh.receive(dec); err != nil {
			return err
		}
	}
	if err := sh.finish(length); err != nil {
		return err
	}
	if !w.Started() {
		return errors.New("recording: no picture could be decoded")
	}
	return nil
}

// shrinker takes the decoded pictures of a recording, keeps at most FPS a
// second, and encodes them smaller.
type shrinker struct {
	o        ShrinkOptions
	w        *Writer
	interval time.Duration
	turns    *index

	enc     *h264enc.Encoder
	cur     video.YUV // the picture just decoded, scaled
	pend    video.YUV // the newest picture not encoded yet
	pendAt  time.Duration
	pending bool
	emitted bool
	last    time.Duration // when the last encoded picture is shown
}

// receive takes every picture the decoder holds.
func (sh *shrinker) receive(dec *video.Decoder) error {
	for {
		pic, ok, err := dec.Receive()
		if err != nil {
			dec.Flush()
			return nil // as with a damaged packet
		}
		if !ok {
			return nil
		}
		w, h := fitSize(pic.Width, pic.Height, sh.o.LongSide)
		if err := dec.ScaleNV12(&sh.cur, w, h); err != nil {
			return err
		}
		if err := sh.add(pic.PTS); err != nil {
			return err
		}
	}
}

// add decides about the picture in cur, shown at t. A picture is encoded
// at its own time when the last one is at least an interval older; one
// that comes sooner waits, and only the newest waiting picture is encoded,
// at the end of the interval: until a newer picture came, it was what the
// screen showed then.
func (sh *shrinker) add(t time.Duration) error {
	next := sh.last + sh.interval
	if sh.pending && t >= next {
		if err := sh.encode(&sh.pend, next); err != nil {
			return err
		}
		sh.pending = false
		next = sh.last + sh.interval
	}
	if !sh.emitted || t >= next {
		return sh.encode(&sh.cur, t)
	}
	sh.cur, sh.pend = sh.pend, sh.cur
	sh.pendAt, sh.pending = t, true
	return nil
}

// finish encodes the waiting picture and what the encoder still holds.
func (sh *shrinker) finish(length time.Duration) error {
	if sh.pending {
		at := sh.last + sh.interval
		if at >= length {
			at = sh.pendAt
		}
		if err := sh.encode(&sh.pend, at); err != nil {
			return err
		}
		sh.pending = false
	}
	return sh.flush()
}

// encode encodes a picture shown at t, starting an encoder for its size.
func (sh *shrinker) encode(p *video.YUV, t time.Duration) error {
	if sh.enc != nil {
		if w, h := sh.enc.Size(); w != p.Width || h != p.Height {
			if err := sh.flush(); err != nil { // the phone turned
				return err
			}
			sh.enc.Close()
			sh.enc = nil
		}
	}
	forceKey := false
	if sh.enc == nil {
		enc, err := h264enc.New(p.Width, p.Height, h264enc.Options{FPS: sh.o.FPS, BitRate: sh.o.BitRate,
			KeyEvery: sh.o.KeyEvery, Offline: true})
		if err != nil {
			return err
		}
		sh.enc, forceKey = enc, true
	}
	sh.emitted, sh.last = true, t
	out, err := sh.enc.Encode(p.Planes[0], p.Planes[1], t, forceKey)
	if err != nil {
		return err
	}
	return sh.write(out, t)
}

// flush writes the pictures the encoder holds back (Media Foundation's
// does, VideoToolbox's does not).
func (sh *shrinker) flush() error {
	if sh.enc == nil {
		return nil
	}
	out, err := sh.enc.Flush()
	if err != nil {
		return err
	}
	return sh.write(out, sh.last)
}

// write adds encoded pictures to the recording, each at the time of its
// own picture where the encoder says which that was, else at t.
func (sh *shrinker) write(out []h264enc.Frame, t time.Duration) error {
	times := sh.enc.PacketTimes()
	for i, e := range out {
		at := t
		if i < len(times) {
			at = times[i]
		}
		turn := sh.turns.turnAt(ticks(at))
		if e.KeyFrame && len(e.Config) > 0 {
			if err := sh.w.Write(driver.Packet{Data: e.Config, Config: true, PTS: at, Turn: turn}, at); err != nil {
				return err
			}
		}
		if err := sh.w.Write(driver.Packet{Data: e.Data, KeyFrame: e.KeyFrame, PTS: at, Turn: turn}, at); err != nil {
			return err
		}
	}
	return nil
}

// fitSize is the size of a w×h picture whose longer side is at most long
// pixels, keeping its shape, in even numbers as 4:2:0 needs.
func fitSize(w, h, long int) (int, int) {
	if m := max(w, h); m > long {
		w, h = (w*long+m/2)/m, (h*long+m/2)/m
	}
	return max(2, w&^1), max(2, h&^1)
}
