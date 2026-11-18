package ui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/driver"
)

// TestMirrorMediaInUse: a mirror whose iPhone refused the stream while an
// app uses the camera or microphone says so in the person's words.
func TestMirrorMediaInUse(t *testing.T) {
	err := fmt.Errorf("open the video stream: %w", &driver.SetupError{Setup: driver.SetupMediaInUse, Detail: "in use",
		Err: errors.New("start display stream (display 1): the device microphone or camera is currently in use")})
	if got, want := mirrorError(err), locales.MirrorNoPicture(locales.MediaInUseTitle()); got != want {
		t.Fatalf("mirrorError = %q; want %q", got, want)
	}
}
