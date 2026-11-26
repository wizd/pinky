package android

import (
	"context"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

const emulatorMediaPlayerDump = ` 0 registered handlers:

Decoder infos by media types:
=============================

Media type 'video/hevc':
  Decoder "c2.android.hevc.decoder" supports
    attributes: 0x4: [
      encoder: 0,
      vendor: 0,
      software-only: 1,
      hw-accelerated: 0 ]

Encoder infos by media types:
=============================

Media type 'video/avc':
  Encoder "c2.android.avc.encoder" supports
    aliases: [
      "OMX.google.h264.encoder" ]
    attributes: 0x5: [
      encoder: 1,
      vendor: 0,
      software-only: 1,
      hw-accelerated: 0 ]
    owner: "codec2::software"
    rank: 512

Media type 'video/hevc':
  Encoder "c2.android.hevc.encoder" supports
    aliases: []
    attributes: 0x5: [
      encoder: 1,
      vendor: 0,
      software-only: 1,
      hw-accelerated: 0 ]
    owner: "codec2::software"
    rank: 512
    details: AMessage(what = 0x00000000) = {
        string size-range = "2x2-512x512"
      }

 No media recorder client
`

const hardwareHEVCMediaPlayerDump = `Encoder infos by media types:
=============================

Media type 'video/hevc':
  Encoder "c2.android.hevc.encoder" supports
    attributes: 0x5: [
      encoder: 1,
      vendor: 0,
      software-only: 1,
      hw-accelerated: 0 ]
  Encoder "c2.qti.hevc.encoder" supports
    aliases: [
      "OMX.qcom.video.encoder.hevc" ]
    attributes: 0x7: [
      encoder: 1,
      vendor: 1,
      software-only: 0,
      hw-accelerated: 1 ]

 No media recorder client
`

const legacyOMXHardwareDump = `Encoder infos by media types:
=============================

Media type 'video/hevc':
  Encoder "OMX.qcom.video.encoder.hevc" supports
    aliases: []
`

const legacyOMXSoftwareDump = `Encoder infos by media types:
=============================

Media type 'video/hevc':
  Encoder "OMX.google.hevc.encoder" supports
    aliases: []
`

const noHEVCMediaPlayerDump = `Encoder infos by media types:
=============================

Media type 'video/avc':
  Encoder "c2.qti.avc.encoder" supports
    attributes: 0x7: [
      encoder: 1,
      vendor: 1,
      software-only: 0,
      hw-accelerated: 1 ]
`

func TestHasHardwareHEVCEncoder(t *testing.T) {
	for name, tc := range map[string]struct {
		dump   string
		hw, ok bool
	}{
		"emulator software only": {emulatorMediaPlayerDump, false, true},
		"qti hardware + sw":      {hardwareHEVCMediaPlayerDump, true, true},
		"legacy OMX hardware":    {legacyOMXHardwareDump, true, true},
		"legacy OMX software":    {legacyOMXSoftwareDump, false, true},
		"no hevc section":        {noHEVCMediaPlayerDump, false, true},
		"empty output":           {"", false, false},
		"permission denied":      {"Permission Denial: can't dump media.player\n", false, false},
	} {
		hw, ok := hasHardwareHEVCEncoder(tc.dump)
		if hw != tc.hw || ok != tc.ok {
			t.Errorf("%s: got (%v, %v), want (%v, %v)", name, hw, ok, tc.hw, tc.ok)
		}
	}
}

func TestCheckHEVCEncoder(t *testing.T) {
	ctx := context.Background()

	t.Run("software only falls back to H264 once", func(t *testing.T) {
		f := startFakeADB(t)
		f.props = map[string]string{"127.0.0.1:5555": phoneProps}
		calls := 0
		f.run = func(_, service string) (string, bool) {
			if service == "shell:dumpsys media.player" {
				calls++
				return emulatorMediaPlayerDump, true
			}
			return "", false
		}
		cfg := f.config()
		d := newDevice(cfg, driver.Info{ID: "127.0.0.1:5555", Connection: "wifi"}, deviceProps{})
		d.checkHEVCEncoder(ctx)
		d.checkHEVCEncoder(ctx)
		if calls != 1 {
			t.Fatalf("dumpsys media.player called %d times, want 1", calls)
		}
		o := d.baseOptions()
		if o.VideoCodec != "" {
			t.Fatalf("VideoCodec = %q, want empty (H.264)", o.VideoCodec)
		}
		if o.VideoBitRate != wifiBitRate || o.MaxFPS != wifiMaxFPS {
			t.Fatalf("wifi rate/fps changed: %+v", o)
		}
	})

	t.Run("hardware encoder keeps H265", func(t *testing.T) {
		f := startFakeADB(t)
		f.props = map[string]string{localWiFi: phoneProps}
		calls := 0
		f.run = func(_, service string) (string, bool) {
			if service == "shell:dumpsys media.player" {
				calls++
				return hardwareHEVCMediaPlayerDump, true
			}
			return "", false
		}
		cfg := f.config()
		d := newDevice(cfg, driver.Info{ID: localWiFi, Connection: "wifi"}, deviceProps{})
		d.checkHEVCEncoder(ctx)
		d.checkHEVCEncoder(ctx)
		if calls != 1 {
			t.Fatalf("dumpsys media.player called %d times, want 1", calls)
		}
		if o := d.baseOptions(); o.VideoCodec != "h265" {
			t.Fatalf("VideoCodec = %q, want h265", o.VideoCodec)
		}
	})

	t.Run("unreadable table keeps H265 and checks once", func(t *testing.T) {
		f := startFakeADB(t)
		f.props = map[string]string{localWiFi: phoneProps}
		calls := 0
		f.run = func(_, service string) (string, bool) {
			if service == "shell:dumpsys media.player" {
				calls++
				return "Can't find service: media.player\n", true
			}
			return "", false
		}
		cfg := f.config()
		d := newDevice(cfg, driver.Info{ID: localWiFi, Connection: "wifi"}, deviceProps{})
		d.checkHEVCEncoder(ctx)
		d.checkHEVCEncoder(ctx)
		if calls != 1 {
			t.Fatalf("dumpsys media.player called %d times, want 1", calls)
		}
		if o := d.baseOptions(); o.VideoCodec != "h265" {
			t.Fatalf("VideoCodec = %q, want h265", o.VideoCodec)
		}
	})

	t.Run("explicit VideoCodec skips check", func(t *testing.T) {
		f := startFakeADB(t)
		f.props = map[string]string{"127.0.0.1:5555": phoneProps}
		calls := 0
		f.run = func(_, _ string) (string, bool) {
			calls++
			return emulatorMediaPlayerDump, true
		}
		cfg := f.config()
		cfg.VideoCodec = "h265"
		d := newDevice(cfg, driver.Info{ID: "127.0.0.1:5555", Connection: "wifi"}, deviceProps{})
		d.checkHEVCEncoder(ctx)
		if calls != 0 {
			t.Fatalf("dumpsys called %d times when VideoCodec is set", calls)
		}
		if o := d.baseOptions(); o.VideoCodec != "h265" {
			t.Fatalf("VideoCodec = %q, want explicit h265", o.VideoCodec)
		}
	})

	t.Run("usb skips check", func(t *testing.T) {
		f := startFakeADB(t)
		f.props = map[string]string{piHW: phoneProps}
		calls := 0
		f.run = func(_, _ string) (string, bool) {
			calls++
			return emulatorMediaPlayerDump, true
		}
		cfg := f.config()
		d := newDevice(cfg, driver.Info{ID: piHW, Connection: "usb"}, deviceProps{})
		d.checkHEVCEncoder(ctx)
		if calls != 0 {
			t.Fatalf("dumpsys called %d times on USB", calls)
		}
	})
}
