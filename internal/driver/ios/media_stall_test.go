package ios

import (
	"strings"
	"testing"
	"time"
)

// A phone that numbers its packets anew, far behind the last number, must
// not freeze the picture: every packet would otherwise be dropped as late
// while video and RTCP keep arriving. The stream follows the new numbers
// from the second packet in a row, as a gap, and keeps dropping a packet
// that is only a little late.
func TestTrackFollowsNumbersAnew(t *testing.T) {
	now := time.Now()
	m := &media{started: now}
	feed := func(seq uint16) (bool, bool) { return m.track(rtpPacket{Seq: seq}, now) }
	for s := uint16(40000); s < 40010; s++ {
		if ok, gap := feed(s); !ok || gap {
			t.Fatalf("in order %d: ok %v gap %v", s, ok, gap)
		}
	}
	// A little late, and a duplicate: dropped, as before.
	if ok, _ := feed(39995); ok {
		t.Fatal("a late packet was taken")
	}
	if ok, _ := feed(40009); ok {
		t.Fatal("a duplicate was taken")
	}
	if ok, gap := feed(40010); !ok || gap {
		t.Fatalf("the next packet after late ones: ok %v gap %v", ok, gap)
	}
	// The phone starts again at 20000, far behind: the first is dropped, the
	// second taken as a gap (the frame it belongs to is bad, a key frame is
	// asked for).
	if ok, _ := feed(20000); ok {
		t.Fatal("the first packet of new numbers was taken")
	}
	if ok, gap := feed(20001); !ok || !gap {
		t.Fatalf("the second packet of new numbers: ok %v gap %v", ok, gap)
	}
	for s := uint16(20002); s < 20010; s++ {
		if ok, gap := feed(s); !ok || gap {
			t.Fatalf("new numbers %d: ok %v gap %v", s, ok, gap)
		}
	}
	if st := m.snapshot(); st.Resyncs != 1 || st.Lost != 0 {
		t.Fatalf("stats %+v: want one resync and nothing lost", st)
	}
	// The receiver report counts from the new numbers.
	if f, l, ext := rrLoss(t, m.receiverReport()); f != 0 || l != 0 || ext != 20009 {
		t.Fatalf("after the resync: fraction %d, lost %d, highest %d", f, l, ext)
	}
	// Two far-behind packets that are not in a row are both dropped.
	if ok, _ := feed(10000); ok {
		t.Fatal("a far packet was taken")
	}
	if ok, _ := feed(20010); !ok {
		t.Fatal("the in-order packet after a far one was dropped")
	}
	if ok, _ := feed(10001); ok {
		t.Fatal("a far packet after an in-order one was taken")
	}
}

// A stream that showed a picture and has waited for a key frame for
// keyWaitLimit while the phone went on sending video stalled; a phone
// that sends nothing meanwhile (a still or dark screen), a short wait, or
// a stream that never showed a picture (restartMedia's readyWait covers
// that) did not.
func TestKeyWaitStalls(t *testing.T) {
	m := &media{started: time.Now()}
	m.stats.Packets = 500
	m.isReady = true
	m.lastRTP.Store(time.Now().UnixNano())
	long := time.Now().Add(-keyWaitLimit - time.Second)
	if err := m.keyStalled(time.Now().Add(-time.Second), 100); err != nil {
		t.Fatalf("a short wait stalled: %v", err)
	}
	if err := m.keyStalled(long, 480); err != nil {
		t.Fatalf("a few packets stalled: %v", err)
	}
	err := m.keyStalled(long, 100)
	if err == nil || !strings.Contains(err.Error(), "no key frame") {
		t.Fatalf("frames without a key frame for %v: %v", keyWaitLimit, err)
	}
	// No video for over a second: the screen stopped, nothing stalled.
	m.lastRTP.Store(time.Now().Add(-2 * time.Second).UnixNano())
	if err := m.keyStalled(long, 100); err != nil {
		t.Fatalf("a still screen stalled: %v", err)
	}
	m.lastRTP.Store(time.Now().UnixNano())
	m.isReady = false
	if err := m.keyStalled(long, 100); err != nil {
		t.Fatalf("a stream before its first picture stalled: %v", err)
	}
}
