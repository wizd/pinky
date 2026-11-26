package video

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Frame is a decoded picture, converted to RGBA or as its planes.
type Frame struct {
	// Image holds the picture in RGBA, scaled to fit the size the player
	// was asked for; nil with PlayerOptions.YUV. It belongs to the player:
	// it stays as it is until the next Take, after which the player may
	// write into it again (two conversions later at the earliest).
	Image *image.RGBA
	// YUV holds the picture's planes at the stream's size with
	// PlayerOptions.YUV, else nil. It belongs to the player as Image does.
	YUV *YUV
	// Width and Height are the stream's picture size, in the device's
	// pixels, whatever size Image has.
	Width, Height int
	Seq           uint64        // counts the pictures decoded and handed over
	PTS           time.Duration // the packet's PTS
	// Turn is how many quarter turns clockwise put the picture the right
	// way up (driver.Packet.Turn), from the packet it was decoded from.
	Turn     int
	Received time.Time // when its packet arrived
	Ready    time.Time // when it was converted
}

// PlayerOptions tune a Player.
type PlayerOptions struct {
	Decoder DecoderOptions
	// OnFrame is called on the player's goroutine after each new picture,
	// and once more when the stream ends. A UI wakes its window from it
	// and then calls Take on its own thread.
	OnFrame func()
	// MaxBacklog is the number of queued packets beyond which the player
	// skips to the next key frame to catch up; default 30.
	MaxBacklog int
	// YUV hands pictures over as their Y′CbCr planes (Frame.YUV) at the
	// stream's size, copied as the decoder gives them, for a UI that
	// converts and scales them as it draws them (MyGo's Bitmap.UpdateYUV):
	// the least CPU a picture can cost. SetSize then changes nothing.
	YUV bool
	// SkipRepeats hands over no picture that is the same, sample for
	// sample, as the one before, and calls no OnFrame for it. Phones go on
	// sending pictures for a while after their screen stops changing (an
	// iPhone 20 to 30 a second for 5 to 7 s); a mirror that woke for each
	// would paint as if the screen moved. A recording's player keeps them,
	// as their PTS move its playhead.
	SkipRepeats bool
	// StallAfter ends the player with ErrStalled once packets have kept
	// coming for this long, at least stallPackets of them, without a single
	// picture out of the decoder: a decoder that fails every packet or
	// gives nothing back (its GPU device lost, a reference it never got).
	// The stream itself is no help there: an iPhone sends no key frame
	// unless asked, so such a decoder would never recover, and nothing
	// would say so. A still screen sends no packets and never stalls. Zero
	// turns the watch off.
	//
	// It also ends the player with ErrStalled when the work on one packet
	// (decoding it, converting its pictures, OnFrame and OnWaiting) takes
	// this long: a decoder call that never returns (#210, d3d11va on
	// Windows, presumably) would otherwise leave the mirror frozen for
	// good, with the stream's packets piling up behind it. The player then
	// ends without waiting for that call; its goroutine and decoder are let
	// go of whenever the call returns.
	StallAfter time.Duration
	// OnWaiting is called on the player's goroutine, at most once a
	// second, while new pictures come and none has been taken for over a
	// second since one was handed over. A UI whose window should be
	// painting finds out that it stopped taking pictures, and paints
	// again; OnFrame alone tells it of each picture only once.
	OnWaiting func()
}

// ErrStalled is a player's error when its decoder gave no picture for the
// packets of PlayerOptions.StallAfter.
var ErrStalled = errors.New("the video decoder gave no picture")

// ErrHung is a player's error when its work on one packet took
// PlayerOptions.StallAfter: a decoder call, most likely, that did not
// return. It is an ErrStalled too.
var ErrHung = fmt.Errorf("%w: it stopped answering", ErrStalled)

// stallPackets is how many packets a stall needs besides its time: a few
// late packets after a pause are no stall.
const stallPackets = 30

// keyFrameRequester is a stream that can ask its source for a key frame
// (the gateway's viewers of an iPhone's stream can).
type keyFrameRequester interface{ RequestKeyFrame() }

// Stats describe a player's work so far.
type Stats struct {
	Hardware   string        // "videotoolbox", "d3d11va", "vaapi", or "" for software
	Decoded    uint64        // pictures decoded and converted, repeats aside
	Repeats    uint64        // pictures the same as the one before, not handed over (SkipRepeats)
	Shown      uint64        // pictures handed to Take
	Dropped    uint64        // pictures replaced before Take saw them
	Skipped    uint64        // packets skipped to catch up
	Idle       uint64        // pictures decoded but not converted, as nobody took any
	Errors     uint64        // packets the decoder rejected
	Decode     time.Duration // mean time from a packet's arrival to its picture out of the decoder
	Convert    time.Duration // mean time to convert a picture to RGBA, or to copy its planes
	Latency    time.Duration // mean time from a packet's arrival to its picture being ready
	FPS        float64       // pictures ready per second, over the last second or so
	OutW, OutH int           // the size pictures are converted to
}

// Player decodes a live stream on a goroutine of its own and keeps the
// newest picture for the UI, so the mirror always shows the latest one and
// never queues behind. It triple-buffers: the picture the UI shows, the
// newest one waiting for it, and the one being converted.
type Player struct {
	stream driver.Stream
	opt    PlayerOptions

	mu       sync.Mutex
	wantW    int // the size to fit pictures into, in pixels; 0 = the stream's
	wantH    int
	front    *Frame // returned by the last Take
	pending  *Frame // the newest picture, not yet taken
	spare    *Frame // free for the decoder
	ready    bool   // pending holds a picture Take has not returned
	err      error
	stats    Stats
	sumDec   time.Duration
	sumConv  time.Duration
	sumLat   time.Duration
	fpsCount int
	fpsStart time.Time
	waitFrom time.Time // when pictures started waiting for Take: the first since the last Take came
	last     *Frame    // the picture handed over last; the player's goroutine's only
	waitedAt time.Time // when OnWaiting was last called; the player's goroutine's only

	// busy is when the player's goroutine started on its current packet,
	// in unix nanoseconds; zero while it waits for one. stage says which
	// part of that work it is in (stageNames). watch reads both.
	busy  atomic.Int64
	stage atomic.Int32

	done      chan struct{}
	endOnce   sync.Once
	closeOnce sync.Once
}

// The parts of a packet's work, for the error of a player that hung in one.
const (
	stageNone int32 = iota
	stageFlush
	stageSend
	stageReceive
	stageConvert
	stageNotify
)

var stageNames = [...]string{"on a packet", "flushing the decoder", "sending a packet to the decoder",
	"taking a picture from the decoder", "converting a picture", "telling the UI of a picture"}

// NewPlayer starts decoding stream. The player owns the stream and closes
// it with Close.
func NewPlayer(stream driver.Stream, opt PlayerOptions) (*Player, error) {
	dec, err := NewDecoder(stream.Codec(), opt.Decoder)
	if err != nil {
		return nil, err
	}
	if opt.MaxBacklog <= 0 {
		opt.MaxBacklog = 30
	}
	p := &Player{stream: stream, opt: opt, done: make(chan struct{}), fpsStart: time.Now()}
	go p.run(dec)
	if opt.StallAfter > 0 {
		go p.watch()
	}
	return p, nil
}

// SetSize sets the size, in pixels, to fit pictures into, keeping their
// aspect ratio; pictures are never scaled up. Zero means the stream's own
// size. A mirror sets the pixel size it shows the phone at, so the GPU
// uploads no more than it draws.
func (p *Player) SetSize(w, h int) {
	p.mu.Lock()
	p.wantW, p.wantH = max(w, 0), max(h, 0)
	p.mu.Unlock()
}

// Take returns the newest picture decoded since the last Take, or nil.
// The picture it returned before may be written again from now on.
func (p *Player) Take() *Frame {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ready {
		return nil
	}
	p.front, p.pending = p.pending, p.front
	p.ready = false
	p.stats.Shown++
	return p.front
}

// Pending reports whether a picture waits for Take.
func (p *Player) Pending() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

// Done is closed when the stream ended or failed.
func (p *Player) Done() <-chan struct{} { return p.done }

// Err returns why the stream ended: nil while it plays, io.EOF at its end.
func (p *Player) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Stats returns the player's counters.
func (p *Player) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stats
	if n := time.Duration(s.Decoded); n > 0 {
		s.Decode = p.sumDec / n
		s.Convert = p.sumConv / n
		s.Latency = p.sumLat / n
	}
	return s
}

// Close stops decoding, closes the stream and waits for the decoder, unless
// the player ended as its decoder hung (StallAfter).
func (p *Player) Close() {
	p.closeOnce.Do(func() {
		p.stream.Close()
		<-p.done
	})
}

// end records why the player ended and closes Done, the first time it is
// called: from run once the stream ended, or from watch once run hung.
func (p *Player) end(err error) {
	p.endOnce.Do(func() {
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		if p.opt.OnFrame != nil {
			p.opt.OnFrame()
		}
		close(p.done)
	})
}

// at notes the part of a packet's work run goes on to.
func (p *Player) at(stage int32) { p.stage.Store(stage) }

// watch ends the player once run took StallAfter over one packet.
func (p *Player) watch() {
	t := time.NewTicker(max(p.opt.StallAfter/4, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case now := <-t.C:
			b := p.busy.Load()
			if b == 0 {
				continue
			}
			if took := now.Sub(time.Unix(0, b)); took >= p.opt.StallAfter {
				p.mu.Lock()
				hw, decoded := p.stats.Hardware, p.stats.Decoded
				p.mu.Unlock()
				switch {
				case p.opt.Decoder.Software || (hw == "" && decoded > 0):
					hw = "software"
				case hw == "":
					hw = "not known yet" // no picture so far
				}
				p.end(fmt.Errorf("%w: stuck %s for %v (decoder: %s)", ErrHung,
					stageNames[p.stage.Load()], took.Round(100*time.Millisecond), hw))
				return
			}
		}
	}
}

func (p *Player) run(dec *Decoder) {
	notify := func() {
		if p.opt.OnFrame != nil {
			p.at(stageNotify)
			p.opt.OnFrame()
		}
	}
	packets := p.stream.Packets()
	skipping := false
	var (
		stall   stallWatch
		stalled error
		askedAt time.Time
	)
	// failed counts a packet the decoder rejected or a picture it could not
	// give, and asks the stream for a key frame to start again from: an
	// iPhone sends none by itself.
	failed := func(err error) {
		p.mu.Lock()
		p.stats.Errors++
		p.mu.Unlock()
		stall.last = err
		if kr, ok := p.stream.(keyFrameRequester); ok && time.Since(askedAt) >= 500*time.Millisecond {
			askedAt = time.Now()
			kr.RequestKeyFrame()
		}
	}
	for {
		p.busy.Store(0)
		pkt, ok := <-packets
		if !ok {
			break
		}
		select {
		case <-p.done:
			// watch ended the player while this goroutine hung.
			dec.Close()
			return
		default:
		}
		received := time.Now()
		p.at(stageNone)
		p.busy.Store(received.UnixNano())
		turn := pkt.Turn
		// Behind by more than the backlog: drop everything up to the next
		// key frame, which the decoder can start from.
		if !skipping && len(packets) > p.opt.MaxBacklog {
			skipping = true
		}
		if skipping {
			if !pkt.KeyFrame && !pkt.Config {
				p.mu.Lock()
				p.stats.Skipped++
				p.mu.Unlock()
				continue
			}
			if pkt.KeyFrame {
				skipping = false
				p.at(stageFlush)
				dec.Flush()
			}
		}
		if !pkt.Config {
			stall.packet(received)
		}
		p.at(stageSend)
		if err := dec.Send(pkt); err != nil {
			failed(err)
		} else {
			for {
				p.at(stageReceive)
				pic, ok, err := dec.Receive()
				if err != nil {
					failed(err)
					break
				}
				if !ok {
					break
				}
				decoded := time.Now()
				p.at(stageConvert)
				shown, err := p.convert(dec, pic, turn, received, decoded)
				if err != nil {
					stall.last = err // counted by convert
					continue
				}
				stall.picture()
				if shown {
					notify()
				}
				p.at(stageNotify)
				p.waiting()
			}
		}
		if now := time.Now(); p.opt.StallAfter > 0 && stall.stalled(now, p.opt.StallAfter) {
			stalled = stall.err(now)
			break
		}
	}
	p.busy.Store(0)
	err := stalled
	if err == nil {
		err = p.stream.Err()
	}
	if err == nil {
		err = io.EOF
	}
	dec.Close()
	p.end(err)
}

// convert writes the picture into the spare buffer and publishes it.
// While nobody takes pictures (a window out of sight paints nothing), it
// keeps the one waiting and converts nothing: decoding goes on, as later
// pictures depend on earlier ones. Nobody takes them when the picture
// waiting has waited for over a second, not when Take has not been called
// for a while: a mirror of a still screen calls it only when OnFrame woke it.
// It reports whether it handed over a new picture, and the error of a
// picture it could not convert.
func (p *Player) convert(dec *Decoder, pic Picture, turn int, received, decoded time.Time) (bool, error) {
	p.mu.Lock()
	if p.ready && time.Since(p.pending.Ready) > time.Second {
		p.stats.Idle++
		p.mu.Unlock()
		return false, nil
	}
	w, h := fitSize(pic.Width, pic.Height, p.wantW, p.wantH)
	f := p.spare
	p.spare = nil
	p.mu.Unlock()
	var err error
	if p.opt.YUV {
		if f == nil || f.YUV == nil {
			f = &Frame{YUV: &YUV{}}
		}
		err = dec.ConvertYUV(f.YUV)
		w, h = f.YUV.Width, f.YUV.Height
	} else {
		if f == nil || f.Image == nil || f.Image.Rect.Dx() != w || f.Image.Rect.Dy() != h {
			f = &Frame{Image: image.NewRGBA(image.Rect(0, 0, w, h))}
		}
		err = dec.Convert(f.Image)
	}
	if err != nil {
		p.mu.Lock()
		p.stats.Errors++
		p.spare = f
		p.mu.Unlock()
		return false, err
	}
	now := time.Now()
	f.Width, f.Height = pic.Width, pic.Height
	f.Turn = turn & 3
	f.PTS, f.Received, f.Ready = pic.PTS, received, now
	// The last picture stays as it is until this one is handed over: the
	// UI only reads it, and the player writes only the spare.
	if p.opt.SkipRepeats && samePicture(f, p.last) {
		p.mu.Lock()
		p.stats.Repeats++
		p.spare = f
		p.mu.Unlock()
		return false, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.last = f
	p.stats.Decoded++
	f.Seq = p.stats.Decoded
	p.stats.Hardware = dec.Hardware()
	p.stats.OutW, p.stats.OutH = w, h
	p.sumDec += decoded.Sub(received)
	p.sumConv += now.Sub(decoded)
	p.sumLat += now.Sub(received)
	p.fpsCount++
	if el := now.Sub(p.fpsStart); el >= time.Second {
		p.stats.FPS = float64(p.fpsCount) / el.Seconds()
		p.fpsCount, p.fpsStart = 0, now
	}
	if p.ready {
		p.stats.Dropped++ // the UI had not taken the last one
	} else {
		p.waitFrom = now
	}
	// The old pending picture, taken or not, becomes the spare.
	p.spare, p.pending = p.pending, f
	p.ready = true
	return true, nil
}

// waiting calls OnWaiting, at most once a second, when pictures have waited
// for Take for over a second while new ones come. A UI that stopped
// painting though it was told of each picture (OnFrame) would otherwise
// never hear of them again: it is told only once per picture.
func (p *Player) waiting() {
	if p.opt.OnWaiting == nil || time.Since(p.waitedAt) < time.Second {
		return
	}
	p.mu.Lock()
	late := p.ready && time.Since(p.waitFrom) > time.Second
	p.mu.Unlock()
	if late {
		p.waitedAt = time.Now()
		p.opt.OnWaiting()
	}
}

// stallWatch counts the packets sent to the decoder since its last
// picture, for PlayerOptions.StallAfter.
type stallWatch struct {
	since   time.Time // when the first of them came
	packets int
	last    error // the last error the decoder or the conversion gave
}

func (s *stallWatch) packet(at time.Time) {
	if s.packets == 0 {
		s.since = at
	}
	s.packets++
}

// picture notes a picture out of the decoder: it works.
func (s *stallWatch) picture() { *s = stallWatch{} }

func (s *stallWatch) stalled(now time.Time, after time.Duration) bool {
	return s.packets >= stallPackets && now.Sub(s.since) >= after
}

func (s *stallWatch) err(now time.Time) error {
	took := now.Sub(s.since).Round(100 * time.Millisecond)
	if s.last != nil {
		return fmt.Errorf("%w for %d packets in %v: %v", ErrStalled, s.packets, took, s.last)
	}
	return fmt.Errorf("%w for %d packets in %v", ErrStalled, s.packets, took)
}

// samePicture reports whether a and b show the same picture the same way.
func samePicture(a, b *Frame) bool {
	if a == nil || b == nil || a.Width != b.Width || a.Height != b.Height || a.Turn != b.Turn {
		return false
	}
	if a.YUV != nil || b.YUV != nil {
		return a.YUV.Equal(b.YUV)
	}
	return a.Image != nil && b.Image != nil && a.Image.Rect == b.Image.Rect &&
		a.Image.Stride == b.Image.Stride && bytes.Equal(a.Image.Pix, b.Image.Pix)
}

// fitSize returns the size to convert a w×h picture to so it fits inside
// maxW×maxH without growing; even, as chroma subsampling prefers.
func fitSize(w, h, maxW, maxH int) (int, int) {
	if maxW <= 0 || maxH <= 0 || (w <= maxW && h <= maxH) {
		return w, h
	}
	sw := float64(maxW) / float64(w)
	sh := float64(maxH) / float64(h)
	s := min(sw, sh)
	ow := max(2, int(float64(w)*s+0.5)&^1)
	oh := max(2, int(float64(h)*s+0.5)&^1)
	return ow, oh
}
