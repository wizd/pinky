// Package h264enc encodes NV12 pictures to H.264 with the encoder the
// computer has, called without cgo: VideoToolbox on macOS, and FFmpeg's
// OpenH264 or Media Foundation encoder on Windows. Other systems have none
// yet, and Available says so.
//
// The iPhone driver encodes the picture of the iPhones below iOS 27 with
// it (the mirror and the recorder take H.264 or HEVC only), and the
// recorder makes recordings smaller with it (internal/recording).
package h264enc

// Frame is one picture as the encoder produced it.
type Frame struct {
	Data     []byte // Annex B
	Config   []byte // SPS and PPS, Annex B; only with key frames
	KeyFrame bool
}

// Options set an encoder up. The encoder is Main profile with no B-frames,
// and gives each picture back as soon as it can.
type Options struct {
	// FPS is about how many pictures come a second; rate control spreads
	// the bit rate over them.
	FPS int
	// BitRate is the average bit rate, in bits per second.
	BitRate int
	// KeyEvery is the most pictures from one key frame to the next; zero
	// keeps the live picture's interval (encoder_darwin.go,
	// encoder_windows.go).
	KeyEvery int
	// Offline lets the encoder take its time for a better picture at the
	// bit rate, where nobody watches it live.
	Offline bool
}
