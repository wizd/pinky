package webmirror

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/DIYgod/pinky/internal/driver"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func annexB(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = append(b, 0, 0, 0, 1)
		b = append(b, n...)
	}
	return b
}

// Parameter sets from mp4ff's own tests.
const (
	h264SPSHex = "6764001eacd940a02ff9610000030001000003003c8f162d96"
	h264PPSHex = "68ebecb22c"
	hevcVPSHex = "40010c01ffff016000000300900000030000030078959809"
	hevcSPSHex = "420101016000000300900000030000030078a0021c801e0596566924caf01680800001f480003a9804"
	hevcPPSHex = "4401c172b46240"
)

func TestConvertH264(t *testing.T) {
	sps, pps := unhex(t, h264SPSHex), unhex(t, h264PPSHex)
	c := &converter{codec: driver.H264}

	// A picture before any configuration cannot be decoded.
	if _, _, err := c.convert(driver.Packet{Data: annexB([]byte{0x41, 0x9a})}); !errors.Is(err, errNoConfig) {
		t.Fatalf("picture first: err %v, want errNoConfig", err)
	}

	cfg, chunk, err := c.convert(driver.Packet{Data: annexB(sps, pps), Config: true})
	if err != nil {
		t.Fatal(err)
	}
	if chunk != nil {
		t.Errorf("configuration packet gave a chunk of %d bytes", len(chunk))
	}
	if cfg == nil || cfg.Codec != "avc1.64001E" {
		t.Fatalf("config %+v, want codec avc1.64001E", cfg)
	}
	if cfg.Width != 640 || cfg.Height != 360 {
		t.Errorf("size %dx%d, want 640x360", cfg.Width, cfg.Height)
	}
	rec, err := avc.DecodeAVCDecConfRec(cfg.Description)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.SPSnalus) != 1 || !bytes.Equal(rec.SPSnalus[0], sps) || len(rec.PPSnalus) != 1 || !bytes.Equal(rec.PPSnalus[0], pps) {
		t.Errorf("avcC holds %x / %x", rec.SPSnalus, rec.PPSnalus)
	}

	// A key frame with the same parameter sets again, an access unit
	// delimiter and two slices: no new configuration, and only the slices,
	// each after its length.
	idr1, idr2 := []byte{0x65, 0x88, 0x84}, []byte{0x65, 0x01}
	cfg, chunk, err = c.convert(driver.Packet{Data: annexB([]byte{0x09, 0xf0}, sps, pps, idr1, idr2), KeyFrame: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Errorf("same parameter sets gave a new configuration %+v", cfg)
	}
	want := lengthPrefixed(idr1, idr2)
	if !bytes.Equal(chunk, want) {
		t.Errorf("chunk %x, want %x", chunk, want)
	}
}

func TestConvertHEVC(t *testing.T) {
	vps, sps, pps := unhex(t, hevcVPSHex), unhex(t, hevcSPSHex), unhex(t, hevcPPSHex)
	c := &converter{codec: driver.HEVC}
	slice := []byte{0x26, 0x01, 0xaf}
	cfg, chunk, err := c.convert(driver.Packet{Data: annexB([]byte{0x46, 0x01, 0x10}, vps, sps, pps, slice), KeyFrame: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || cfg.Codec != "hvc1.1.6.L120.90" {
		t.Fatalf("config %+v, want codec hvc1.1.6.L120.90", cfg)
	}
	if cfg.Width != 1080 || cfg.Height != 1920 {
		t.Errorf("size %dx%d, want 1080x1920", cfg.Width, cfg.Height)
	}
	rec, err := hevc.DecodeHEVCDecConfRec(cfg.Description)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		t    hevc.NaluType
		want []byte
	}{{hevc.NALU_VPS, vps}, {hevc.NALU_SPS, sps}, {hevc.NALU_PPS, pps}} {
		if got := rec.GetNalusForType(tc.t); len(got) != 1 || !bytes.Equal(got[0], tc.want) {
			t.Errorf("hvcC %v: %x", tc.t, got)
		}
	}
	if want := lengthPrefixed(slice); !bytes.Equal(chunk, want) {
		t.Errorf("chunk %x, want %x", chunk, want)
	}

	// New parameter sets (the phone turned, or changed its size) give a
	// new configuration.
	pps2 := []byte{0x44, 0x01, 0xc0}
	cfg, _, err = c.convert(driver.Packet{Data: annexB(vps, sps, pps2), Config: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("changed parameter sets gave no configuration")
	}
	rec2 := mustHEVC(t, cfg)
	if got := rec2.GetNalusForType(hevc.NALU_PPS); len(got) != 1 || !bytes.Equal(got[0], pps2) {
		t.Errorf("new hvcC PPS %x", got)
	}
}

func mustHEVC(t *testing.T, cfg *DecoderConfig) hevc.DecConfRec {
	t.Helper()
	rec, err := hevc.DecodeHEVCDecConfRec(cfg.Description)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func lengthPrefixed(nals ...[]byte) []byte {
	var b []byte
	for _, n := range nals {
		b = binary.BigEndian.AppendUint32(b, uint32(len(n)))
		b = append(b, n...)
	}
	return b
}

func TestFrameMessage(t *testing.T) {
	m := frameMessage(driver.Packet{KeyFrame: true, Turn: 3, PTS: 1500 * time.Millisecond}, []byte{7, 8})
	if m[0] != 1|3<<1 {
		t.Errorf("flags %08b", m[0])
	}
	if us := binary.BigEndian.Uint64(m[1:9]); us != 1_500_000 {
		t.Errorf("pts %d µs", us)
	}
	if !bytes.Equal(m[9:], []byte{7, 8}) {
		t.Errorf("chunk %x", m[9:])
	}
}
