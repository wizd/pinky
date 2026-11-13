package webmirror

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/video"
)

// Browsers decode the phone's stream with WebCodecs' VideoDecoder, which
// takes a codec string and, for H.264 and HEVC, a decoder configuration
// record (avcC, hvcC) as its description; each chunk is then a picture's
// NAL units with four-byte lengths in place of Annex B's start codes.
// converter turns the phone's packets into that: the configuration from
// the parameter sets, which may come in their own packet or ahead of a key
// frame, and the pictures without parameter sets or delimiters.

// DecoderConfig is what a browser configures its VideoDecoder with.
type DecoderConfig struct {
	Codec       string // "hvc1.1.6.L120.90", "avc1.640028"
	Description []byte // hvcC or avcC
	Width       int
	Height      int
}

// converter turns one stream's packets into decoder configurations and
// chunks. It keeps the parameter sets it saw last.
type converter struct {
	codec driver.Codec
	vps   [][]byte
	sps   [][]byte
	pps   [][]byte
	key   []byte // the parameter sets the current configuration is for
}

// NAL unit types, as in internal/video.
const (
	h264SPS = 7
	h264PPS = 8
	h264AUD = 9
	hevcVPS = 32
	hevcSPS = 33
	hevcPPS = 34
	hevcAUD = 35
)

// errNoConfig is convert's error for a picture before any parameter sets.
var errNoConfig = errors.New("no parameter sets yet")

// convert returns a new decoder configuration when p carries parameter
// sets other than the current ones, and the chunk of the picture p holds,
// nil when it holds none (a configuration packet).
func (c *converter) convert(p driver.Packet) (cfg *DecoderConfig, chunk []byte, err error) {
	var ps [3][][]byte // vps, sps, pps
	var out bytes.Buffer
	for _, nal := range video.SplitNALs(p.Data) {
		if len(nal) == 0 {
			continue
		}
		switch t := video.NALType(c.codec, nal); {
		case c.codec == driver.HEVC && t == hevcVPS:
			ps[0] = append(ps[0], nal)
		case c.codec == driver.HEVC && t == hevcSPS, c.codec == driver.H264 && t == h264SPS:
			ps[1] = append(ps[1], nal)
		case c.codec == driver.HEVC && t == hevcPPS, c.codec == driver.H264 && t == h264PPS:
			ps[2] = append(ps[2], nal)
		case c.codec == driver.HEVC && t == hevcAUD, c.codec == driver.H264 && t == h264AUD:
		default:
			var n [4]byte
			binary.BigEndian.PutUint32(n[:], uint32(len(nal)))
			out.Write(n[:])
			out.Write(nal)
		}
	}
	if len(ps[1]) > 0 {
		if len(ps[0]) > 0 || c.codec != driver.HEVC {
			c.vps = ps[0]
		}
		c.sps = ps[1]
		if len(ps[2]) > 0 {
			c.pps = ps[2]
		}
		key := joinNALs(c.vps, c.sps, c.pps)
		if !bytes.Equal(key, c.key) {
			d, err := c.config()
			if err != nil {
				return nil, nil, err
			}
			c.key, cfg = key, d
		}
	} else if len(ps[2]) > 0 && len(c.sps) > 0 {
		c.pps = ps[2]
		if key := joinNALs(c.vps, c.sps, c.pps); !bytes.Equal(key, c.key) {
			d, err := c.config()
			if err != nil {
				return nil, nil, err
			}
			c.key, cfg = key, d
		}
	}
	if out.Len() == 0 {
		return cfg, nil, nil
	}
	if c.key == nil {
		return nil, nil, errNoConfig
	}
	return cfg, out.Bytes(), nil
}

// config is the decoder configuration of the current parameter sets.
func (c *converter) config() (*DecoderConfig, error) {
	if len(c.sps) == 0 || len(c.pps) == 0 {
		return nil, errors.New("parameter sets without a picture parameter set")
	}
	var buf bytes.Buffer
	switch c.codec {
	case driver.HEVC:
		if len(c.vps) == 0 {
			return nil, errors.New("HEVC parameter sets without a video parameter set")
		}
		sps, err := hevc.ParseSPSNALUnit(c.sps[0])
		if err != nil {
			return nil, fmt.Errorf("read the HEVC sequence parameter set: %w", err)
		}
		rec, err := hevc.CreateHEVCDecConfRec(c.vps, c.sps, c.pps, true, true, true, true)
		if err != nil {
			return nil, err
		}
		if err := rec.Encode(&buf); err != nil {
			return nil, err
		}
		w, h := sps.ImageSize()
		return &DecoderConfig{Codec: hevc.CodecString("hvc1", sps), Description: buf.Bytes(), Width: int(w), Height: int(h)}, nil
	case driver.H264:
		sps, err := avc.ParseSPSNALUnit(c.sps[0], false)
		if err != nil {
			return nil, fmt.Errorf("read the H.264 sequence parameter set: %w", err)
		}
		rec, err := avc.CreateAVCDecConfRec(c.sps, c.pps, true)
		if err != nil {
			return nil, err
		}
		if err := rec.Encode(&buf); err != nil {
			return nil, err
		}
		return &DecoderConfig{Codec: avc.CodecString("avc1", sps), Description: buf.Bytes(), Width: int(sps.Width), Height: int(sps.Height)}, nil
	}
	return nil, fmt.Errorf("codec %q is not supported", c.codec)
}

func joinNALs(sets ...[][]byte) []byte {
	var b bytes.Buffer
	for _, set := range sets {
		for _, nal := range set {
			b.Write([]byte{0, 0, 0, 1})
			b.Write(nal)
		}
		b.WriteByte(0xff) // between sets
	}
	return b.Bytes()
}

// Frame header flags, the first byte of a binary message: bit 0 a key
// frame, bits 1 and 2 the quarter turns clockwise that put the picture
// the right way up (driver.Packet.Turn).
const (
	flagKey      = 1
	turnShift    = 1
	frameHeader  = 9 // flags, then the presentation time in microseconds
	maxTurnValue = 3
)

// frameMessage is the binary WebSocket message of a chunk: a flags byte,
// the presentation time in microseconds (big-endian, 8 bytes), then the
// chunk.
func frameMessage(p driver.Packet, chunk []byte) []byte {
	b := make([]byte, frameHeader+len(chunk))
	var f byte
	if p.KeyFrame {
		f |= flagKey
	}
	f |= byte(p.Turn&maxTurnValue) << turnShift
	b[0] = f
	binary.BigEndian.PutUint64(b[1:9], uint64(p.PTS.Microseconds()))
	copy(b[frameHeader:], chunk)
	return b
}
