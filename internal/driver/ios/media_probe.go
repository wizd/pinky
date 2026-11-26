package ios

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// A display stream can break without a sign: an iPhone 11 on iOS 27.2.1
// froze on the grey dim of a permission alert (#204, #206, #207) while the
// phone's RTCP sender reports kept coming and input kept working, so the
// stream's other checks (videoSilence, keyStalled) saw nothing wrong. A
// still screen looks the same from here: the phone sends frames only while
// the screen changes. But a working phone answers a key frame request
// within about 70 ms, still screen or not. So while someone watches, a
// stream that gave no frame for a while is asked for one (a probe), and so
// is one that gave none soon after input; one that gives none even then is
// broken, and fails, and the next view or input starts it again. A probe
// costs the phone one key frame, so on a still screen they come further
// and further apart.
var (
	probeIdle    = 5 * time.Second         // no frame for this long while someone watches: probe
	probeIdleMax = 30 * time.Second        // the longest wait between probes of a still screen
	probeInput   = 1500 * time.Millisecond // no frame for this long after input: probe (after echoWait, whose frames a probe's would spoil)
	probeWait    = 2 * time.Second         // no frame for this long after a probe: broken
)

// probeAsks is how many key frame requests a probe sends, keyRepeat apart.
const (
	probeAsks = 3
	keyRepeat = 600 * time.Millisecond
)

// probeHoldBase is how old a stream must be before a probe may fail it, and
// probeYoung how old a failed stream may have been for the next one to
// wait longer: a phone whose streams keep breaking is restarted at most
// every 10 s, then 20, 40, 80 and 160 s.
const (
	probeHoldBase = 10 * time.Second
	probeYoung    = 2 * time.Minute
)

// probeHold is how old a stream must be before a probe may fail it, after
// strikes streams in a row that a probe failed young.
func probeHold(strikes int) time.Duration {
	return probeHoldBase << min(strikes, 4)
}

// probeError is the error of a stream a probe found broken.
type probeError struct {
	age  time.Duration // how long the stream had run
	idle time.Duration // since its last frame
	why  string
}

func (e *probeError) Error() string {
	return fmt.Sprintf("the video stream stopped: no picture from the phone for %v, even when asked for one (%s)",
		e.idle.Round(time.Second), e.why)
}

// What a probe found in one step.
type probeAct int

const (
	probeNothing probeAct = iota
	probeAsk              // ask for a key frame
	probeBroken           // no frame came: the stream is broken
)

// probeView is what a probe step looks at.
type probeView struct {
	now       time.Time
	ready     bool      // the stream showed a picture
	watched   bool      // someone takes its packets
	frames    int       // frames so far
	lastFrame time.Time // when the last came
	input     time.Time // the last input sent to the phone
	hold      time.Time // no failing before then (probeHold)
}

// probe is the state of a stream's probes, owned by its feedback loop.
type probe struct {
	idle     time.Duration // the wait before the next idle probe
	at       time.Time     // when the running probe started; zero: none runs
	why      string        // what started it: "idle" or "input"
	frames   int           // the frames counted then
	asks     int           // requests it sent
	askedAt  time.Time
	doneAt   time.Time // when the last probe ended
	inputFor time.Time // the input the last input probe was for
	sent     int       // probes started
	requests int       // key frame requests all probes sent
	answered int       // probes a frame answered
	dark     int       // probes not answered while the screen was off
}

// step advances the probe to v.now.
func (p *probe) step(v probeView) probeAct {
	if p.idle == 0 {
		p.idle = probeIdle
	}
	if !v.ready {
		// Before the first picture, readyWait decides.
		p.at = time.Time{}
		return probeNothing
	}
	if !p.at.IsZero() {
		if v.frames > p.frames {
			p.answered++
			p.pass(v.now, p.why == "idle")
			return probeNothing
		}
		if v.now.Sub(p.at) >= probeWait && !v.now.Before(v.hold) {
			return probeBroken
		}
		if p.asks < probeAsks && v.now.Sub(p.askedAt) >= keyRepeat {
			p.asks++
			p.requests++
			p.askedAt = v.now
			return probeAsk
		}
		return probeNothing
	}
	if v.lastFrame.After(p.doneAt) {
		p.idle = probeIdle // the screen changed since the last probe
	}
	quiet := v.lastFrame
	if p.doneAt.After(quiet) {
		quiet = p.doneAt
	}
	switch {
	case v.input.After(v.lastFrame) && v.input.After(p.inputFor) && v.now.Sub(v.input) >= probeInput:
		p.why, p.inputFor = "input", v.input
	case v.watched && v.now.Sub(quiet) >= p.idle && v.now.Sub(v.input) >= probeInput:
		p.why = "idle"
	default:
		return probeNothing
	}
	p.at, p.frames, p.asks, p.askedAt = v.now, v.frames, 1, v.now
	p.sent++
	p.requests++
	return probeAsk
}

// pass ends the running probe without failing the stream; still says the
// screen stays as it was, and the next idle probe waits longer.
func (p *probe) pass(now time.Time, still bool) {
	p.at, p.doneAt = time.Time{}, now
	if still {
		p.idle = min(2*p.idle, probeIdleMax)
	}
}

// probeTick runs one step of the probe, and returns why the stream is
// broken when it is.
func (m *media) probeTick(now time.Time) error {
	switch m.probe.step(m.probeView(now)) {
	case probeAsk:
		m.requestKeyFrame()
	case probeBroken:
		if m.screenDark() {
			// A dark screen may well send nothing; leave it alone.
			if m.probe.dark == 0 {
				logTo(m.log.Load()).Info("iphone: no picture from the phone even when asked, while its screen is off; leaving the stream")
			}
			m.probe.dark++
			m.probe.pass(now, true)
			return nil
		}
		return m.probeFailed(now)
	}
	return nil
}

func (m *media) probeView(now time.Time) probeView {
	m.mu.Lock()
	v := probeView{now: now, ready: m.isReady, watched: len(m.subs) > 0, frames: m.stats.Frames}
	m.mu.Unlock()
	v.lastFrame = unixTime(m.lastFrame.Load())
	if in := m.input.Load(); in != nil {
		v.input = unixTime(in.Load())
	}
	v.hold = unixTime(m.probeHold.Load())
	return v
}

// screenDark reports whether the phone says its screen is off (setScreen).
func (m *media) screenDark() bool {
	if f := m.dark.Load(); f != nil {
		return (*f)()
	}
	return false
}

// setProbe has the stream's probes see input from in (unix nanos), fail
// the stream no sooner than hold after it started, and ask dark whether the
// screen is off before they do.
func (m *media) setProbe(in *atomic.Int64, hold time.Duration, dark func() bool) {
	m.input.Store(in)
	m.probeHold.Store(m.started.Add(hold).UnixNano())
	if dark != nil {
		m.dark.Store(&dark)
	}
}

// probeFailed logs what the stream got, to tell a phone that stopped
// sending video from one whose video does not arrive or cannot be read,
// and returns the stream's error.
//
// rtp_since_frame counts the RTP packets since the last frame: none means
// the phone sent no video, some that it sent what gave no frame (the
// payload types, unreadable payloads and access units say more).
// sr_sent_since_frame is what the phone's sender reports say it sent since
// then: more than came means packets were lost on the way.
func (m *media) probeFailed(now time.Time) error {
	last := unixTime(m.lastFrame.Load())
	age := now.Sub(m.started)
	m.mu.Lock()
	s, w, at := m.stats, m.wire, m.atFrame
	srSent, srs := m.srSent, m.srCount
	m.mu.Unlock()
	try := m.noVideoTry()
	ago := func(n int64) any {
		if n == 0 {
			return "never"
		}
		return now.Sub(time.Unix(0, n)).Round(100 * time.Millisecond)
	}
	p := m.probe
	args := []any{
		"display", m.display, "why", p.why, "age", age.Round(time.Second), "since_frame", now.Sub(last).Round(100 * time.Millisecond),
		"requests", p.asks, "probes", p.sent, "answered", p.answered,
		"rtp_since_frame", s.Packets - at.packets, "units_since_frame", w.Units - at.units,
		"since_rtp", ago(m.lastRTP.Load()), "since_rtcp", ago(m.lastRTCP.Load()), "rtcp", m.rtcpIn.Load(),
		"sender_reports", srs, "sr_packets", srSent, "sr_sent_since_frame", int64(srSent) - int64(at.srSent),
		"packets", s.Packets, "bytes", s.Bytes, "frames", s.Frames, "key_frames", s.KeyFrames,
		"payload_types", countList(w.PayloadTypes), "last_pt", m.lastPT.Load(), "ssrc", m.lastSSRC.Load(), "device_ssrc", m.ans.DeviceSSRC,
		"codec", w.Codec, "unreadable", w.Unreadable, "not_rtp", w.NotRTP, "waited_key", w.WaitedKey,
		"lost", s.Lost, "dropped", s.Dropped, "resyncs", s.Resyncs, "firs", s.FIRs,
		"wire", try.Wire,
	}
	logTo(m.log.Load()).Warn("iphone: the video stream stopped showing pictures: none even when asked for a key frame; starting it again", args...)
	return &probeError{age: age, idle: now.Sub(last), why: p.why}
}

// screenOffNow asks the phone whether its screen is off, briefly: the
// probe's answer to a stream that sends nothing (setProbe).
func (d *Device) screenOffNow() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	on, err := d.ScreenOn(ctx)
	return err == nil && !on
}
