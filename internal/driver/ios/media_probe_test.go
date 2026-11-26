package ios

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	goios "github.com/danielpaulus/go-ios/ios"
)

// probeStream is a fake stream that showed a picture, with a viewer when
// watched, and a log in buf.
func probeStream(t *testing.T, watched bool) (*fakeStream, *bytes.Buffer) {
	t.Helper()
	f := newFakeStream(t)
	var buf bytes.Buffer
	f.m.setLog(slog.New(slog.NewTextHandler(&buf, nil)))
	if watched {
		f.m.subscribe()
	}
	f.rtp(100, false, 0, hevcParams(testHEVCSPS(1136, 2464, 0)))
	f.keyFrame()
	select {
	case <-f.m.ready:
	default:
		t.Fatal("no picture")
	}
	return f, &buf
}

// keyFrame feeds a key frame, as the phone's answer to a request.
func (f *fakeStream) keyFrame() {
	f.ts += 400
	f.rtp(100, true, f.ts, hevcNAL(19, 0xAA))
}

// srCount feeds a sender report that says the phone sent n packets.
func (f *fakeStream) srCount(n uint32) {
	b := make([]byte, 28)
	b[0], b[1] = 0x80, 200
	binary.BigEndian.PutUint16(b[2:], 6)
	binary.BigEndian.PutUint32(b[20:], n)
	f.m.onRTCP(b)
}

// ticks runs the probe from from to to, as the feedback loop does every
// 200 ms, and returns the requests it sent and the error it failed with.
func ticks(m *media, from, to time.Time) (asks int, err error) {
	for now := from; !now.After(to); now = now.Add(200 * time.Millisecond) {
		before := m.probe.requests
		err = m.probeTick(now)
		asks += m.probe.requests - before
		if err != nil {
			return asks, err
		}
	}
	return asks, nil
}

// A still screen that answers each probe with a key frame is left alone,
// and is asked less and less often: at most one key frame per probeIdle.
func TestProbeStillScreenAnswers(t *testing.T) {
	f, buf := probeStream(t, true)
	m, t0 := f.m, time.Now()
	if asks, err := ticks(m, t0, t0.Add(probeIdle-300*time.Millisecond)); asks != 0 || err != nil {
		t.Fatalf("before probeIdle: %d requests, %v", asks, err)
	}
	if asks, _ := ticks(m, t0.Add(probeIdle), t0.Add(probeIdle)); asks != 1 || m.probe.why != "idle" {
		t.Fatalf("at probeIdle: %d requests (%q); want one idle probe", asks, m.probe.why)
	}
	f.keyFrame() // the phone answers
	now := t0.Add(probeIdle + 200*time.Millisecond)
	if err := m.probeTick(now); err != nil || !m.probe.at.IsZero() || m.probe.answered != 1 {
		t.Fatalf("after the answer: %v, probe %+v", err, m.probe)
	}
	// The next probe waits twice as long; answered, the one after that
	// longer again, up to probeIdleMax.
	for i, wait := range []time.Duration{2 * probeIdle, 4 * probeIdle, probeIdleMax, probeIdleMax} {
		if asks, _ := ticks(m, now.Add(200*time.Millisecond), now.Add(wait-200*time.Millisecond)); asks != 0 {
			t.Fatalf("probe %d came before %v", i+2, wait)
		}
		now = now.Add(wait)
		if asks, _ := ticks(m, now, now); asks != 1 {
			t.Fatalf("probe %d did not come after %v", i+2, wait)
		}
		f.keyFrame()
		now = now.Add(200 * time.Millisecond)
		if err := m.probeTick(now); err != nil {
			t.Fatal(err)
		}
	}
	if m.Err() != nil || m.stopped.Load() || strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("a still screen was failed: %v\n%s", m.Err(), buf)
	}
	// The screen changes: the next probe is probeIdle after its last frame.
	f.rtp(100, true, f.ts+400, hevcNAL(1, 0xBB))
	f.ts += 400
	changed := now.Add(time.Second)
	m.lastFrame.Store(changed.UnixNano()) // as if it came then
	if asks, _ := ticks(m, changed, changed.Add(probeIdle-200*time.Millisecond)); asks != 0 {
		t.Fatalf("a probe within %v of the screen changing", probeIdle)
	}
	if asks, _ := ticks(m, changed.Add(probeIdle), changed.Add(probeIdle)); asks != 1 {
		t.Fatalf("no probe %v after the screen changed", probeIdle)
	}
}

// A phone that stops sending video while its sender reports go on (the
// alert of #204) gets no frame even when asked: the stream fails, and the
// log says no RTP came while the phone counted packets it sent.
func TestProbeNoRTPFails(t *testing.T) {
	f, buf := probeStream(t, true)
	m, t0 := f.m, time.Now()
	f.srCount(40)
	f.srCount(90) // sent, never came
	asks, err := ticks(m, t0, t0.Add(probeIdle+probeWait))
	var pe *probeError
	if !errors.As(err, &pe) || asks != probeAsks {
		t.Fatalf("after %d requests: %v; want the stream broken after %d", asks, err, probeAsks)
	}
	m.fail(err)
	if !m.stopped.Load() || !errors.As(m.Err(), &pe) {
		t.Fatalf("the stream did not fail: %v", m.Err())
	}
	got := buf.String()
	t.Logf("the log:\n%s", got)
	for _, want := range []string{
		"level=WARN", "the video stream stopped showing pictures", "why=idle", "requests=3", "rtp_since_frame=0",
		"sender_reports=2", "sr_packets=90", "sr_sent_since_frame=90", "frames=1", "payload_types=100×2", "codec=hevc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s", want)
		}
	}
}

// RTP that gives no frame (another payload type, fragments without their
// start) counts as no picture: the probe fails the stream, and the log
// says packets came.
func TestProbeUnreadableRTPFails(t *testing.T) {
	f, buf := probeStream(t, true)
	m, t0 := f.m, time.Now()
	fu := []byte{hevcFU << 1, 1, 0x01, 0xEE} // a middle fragment
	for i := range 30 {
		f.rtp(127, i%3 == 2, f.ts+uint32(400*(i/3+1)), fu)
	}
	_, err := ticks(m, t0, t0.Add(probeIdle+probeWait))
	var pe *probeError
	if !errors.As(err, &pe) {
		t.Fatalf("the stream was not found broken: %v", err)
	}
	got := buf.String()
	for _, want := range []string{"rtp_since_frame=30", "payload_types=\"100×2 127×30\"", "last_pt=127", "frames=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
}

// Input that no frame follows is probed probeInput later, watched or not;
// a frame after the input, or no input, starts no probe on a stream
// nobody watches.
func TestProbeAfterInput(t *testing.T) {
	f, _ := probeStream(t, false)
	m, t0 := f.m, time.Now()
	var in atomic.Int64
	m.setProbe(&in, 0, nil)
	if asks, _ := ticks(m, t0, t0.Add(time.Minute)); asks != 0 {
		t.Fatalf("%d requests on an unwatched stream without input", asks)
	}
	// Input the screen answers: no probe.
	in.Store(time.Now().Add(-time.Millisecond).UnixNano())
	f.rtp(100, true, f.ts+400, hevcNAL(1, 0xBB))
	f.ts += 400
	now := time.Now()
	if asks, _ := ticks(m, now, now.Add(5*time.Second)); asks != 0 {
		t.Fatalf("%d requests after input the screen answered", asks)
	}
	// Input with no frame after it.
	at := time.Now().Add(time.Millisecond)
	in.Store(at.UnixNano())
	if asks, _ := ticks(m, at, at.Add(probeInput-200*time.Millisecond)); asks != 0 {
		t.Fatal("a probe before probeInput")
	}
	if asks, _ := ticks(m, at.Add(probeInput), at.Add(probeInput)); asks != 1 || m.probe.why != "input" {
		t.Fatalf("%d requests (%q) probeInput after input; want an input probe", asks, m.probe.why)
	}
	// Unanswered, the stream is broken.
	_, err := ticks(m, at.Add(probeInput+200*time.Millisecond), at.Add(probeInput+probeWait))
	var pe *probeError
	if !errors.As(err, &pe) || pe.why != "input" {
		t.Fatalf("an unanswered input probe: %v", err)
	}
}

// A probe fails no stream younger than its hold, nor one whose phone says
// its screen is off.
func TestProbeHoldAndDarkScreen(t *testing.T) {
	f, buf := probeStream(t, true)
	m, t0 := f.m, time.Now()
	dark := true
	m.setProbe(new(atomic.Int64), 20*time.Second, func() bool { return dark })
	if _, err := ticks(m, t0, t0.Add(19*time.Second)); err != nil {
		t.Fatalf("failed within its hold: %v", err)
	}
	if _, err := ticks(m, t0.Add(19200*time.Millisecond), t0.Add(2*time.Minute)); err != nil {
		t.Fatalf("failed while the screen is off: %v", err)
	}
	if m.probe.dark == 0 || !strings.Contains(buf.String(), "while its screen is off") {
		t.Fatalf("dark probes %d, log:\n%s", m.probe.dark, buf)
	}
	dark = false
	now := t0.Add(2 * time.Minute)
	if _, err := ticks(m, now, now.Add(probeIdleMax+probeWait+time.Second)); err == nil {
		t.Fatal("not failed once the screen is on")
	}
}

// A stream a probe fails ends its view, as any failed stream does: the
// mirror opens the stream again (ui: TestMirrorLogsItsLife), and the next
// view starts a new one, which may be failed by a probe only after a
// longer hold while streams keep breaking young.
func TestProbeFailureRestarts(t *testing.T) {
	var mu sync.Mutex
	var started []*media
	d := &Device{l: &link{}, done: make(chan struct{})}
	d.startFn = func(context.Context, *link, int) (*media, error) {
		f, _ := probeStream(t, false)
		mu.Lock()
		started = append(started, f.m)
		mu.Unlock()
		return f.m, nil
	}
	d.hidFn = func(goios.DeviceEntry) (*hidSession, error) { return &hidSession{}, nil }
	holdOf := func(m *media) time.Duration { return unixTime(m.probeHold.Load()).Sub(m.started) }

	view, err := d.Stream(context.Background(), driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := started[0]
	if h := holdOf(first); h != probeHoldBase {
		t.Fatalf("the first stream's hold %v; want %v", h, probeHoldBase)
	}
	now := first.started.Add(probeHoldBase)
	_, perr := ticks(first, now, now.Add(probeIdle+probeWait))
	if perr == nil {
		t.Fatal("the probe did not find the stream broken")
	}
	first.fail(perr)
	if _, ok := <-view.Packets(); ok {
		// The key frame the view started at; then the end.
		for range view.Packets() {
		}
	}
	var pe *probeError
	if !errors.As(view.Err(), &pe) {
		t.Fatalf("the view ended with %v; want the probe's error", view.Err())
	}
	view.Close()

	// The mirror opens it again: a new stream, with a longer hold.
	view, err = d.Stream(context.Background(), driver.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	mu.Lock()
	n := len(started)
	mu.Unlock()
	if n != 2 || !first.stopped.Load() {
		t.Fatalf("%d streams started, the first stopped %v", n, first.stopped.Load())
	}
	if h := holdOf(started[1]); h != 2*probeHoldBase {
		t.Fatalf("the second stream's hold %v; want %v", h, 2*probeHoldBase)
	}
	d.mu.Lock()
	cur := d.m
	d.mu.Unlock()
	if cur != started[1] {
		t.Fatal("the device does not run the new stream")
	}
}

func TestProbeHoldGrows(t *testing.T) {
	for i, want := range []time.Duration{10, 20, 40, 80, 160, 160} {
		if got := probeHold(i); got != want*time.Second {
			t.Errorf("probeHold(%d) = %v; want %v", i, got, want*time.Second)
		}
	}
}
