//go:build !darwin && !windows

package h264enc

import (
	"fmt"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// Pinky encodes H.264 with macOS's VideoToolbox on macOS and with FFmpeg's
// encoders on Windows; other systems have no encoder Pinky can call without
// cgo yet.

// Encoder is never made here: New fails.
type Encoder struct{ w, h int }

// Name is the encoder's name for the log; this system has none.
func (e *Encoder) Name() string { return "none" }

// Size is the size of the pictures the encoder takes.
func (e *Encoder) Size() (w, h int) { return e.w, e.h }

// New fails: this system has no encoder.
func New(w, h int, o Options) (*Encoder, error) { return nil, Available() }

// Encode fails: this system has no encoder.
func (e *Encoder) Encode(y, uv []byte, pts time.Duration, forceKey bool) ([]Frame, error) {
	return nil, Available()
}

// PacketTimes is nil.
func (e *Encoder) PacketTimes() []time.Duration { return nil }

// Flush returns nothing.
func (e *Encoder) Flush() ([]Frame, error) { return nil, nil }

// Close does nothing.
func (e *Encoder) Close() {}

// Available says that this system has no H.264 encoder Pinky can call.
func Available() error {
	return fmt.Errorf("encoding H.264 needs VideoToolbox on macOS or FFmpeg's encoders on Windows, and this system has none Pinky can call: %w", driver.ErrUnsupported)
}
