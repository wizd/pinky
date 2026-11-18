package ios

import (
	"context"
	"errors"

	"github.com/DIYgod/pinky/internal/driver"
)

// An app on the phone that uses the camera or microphone (a call or
// FaceTime, Camera, Voice Memos, a screen recording with the microphone,
// dictation, a Mac using it as Continuity Camera) keeps iOS from mirroring
// the screen: the display service refuses the stream with errMediaInUse
// until the app lets go. And the other way round: while Pinky's stream
// runs, iOS takes the camera and microphone from apps (idle.go).
//
// So Pinky starts the stream only when something needs it, a view of the
// screen or input (live), never when it opens the phone and never in the
// background to see whether the camera is free again: a start that lands
// in a short pause of the person's own use (dictation between two
// sentences, Camera switching modes) would take the camera and microphone
// from them. A start refused for this fails that view or input with
// driver.SetupMediaInUse and mediaInUseDetail; the next view or input tries
// again, no sooner than restartBackoff later.

// mediaInUseDetail says what the phone waits for, in English for agents
// and the log; the client says it in the person's language.
const mediaInUseDetail = "An app on the iPhone is using the camera or microphone (a call or FaceTime, Camera, Voice Memos, " +
	"a screen recording with the microphone, dictation, or a Mac using it as Continuity Camera), and iOS does not allow " +
	"mirroring the screen or input from the computer then. Ask the person to end the call or close that app: the green " +
	"or orange dot at the top of the screen shows it is in use, and Control Center shows which app. Then try again."

// mediaInUse reports whether err is the phone refusing the stream because
// an app uses the camera or microphone.
func mediaInUse(err error) bool {
	var ce *CoreDeviceError
	return errors.As(err, &ce) && ce.Code == errMediaInUse
}

// mediaInUseStep returns err as the driver.SetupMediaInUse step when the
// phone refused the stream because an app uses the camera or microphone,
// and err itself otherwise.
func mediaInUseStep(err error) error {
	if !mediaInUse(err) {
		return err
	}
	var se *driver.SetupError
	if errors.As(err, &se) {
		return err
	}
	return &driver.SetupError{Setup: driver.SetupMediaInUse, Detail: mediaInUseDetail, Err: err}
}

// coreSession is a CoreDevice session from its tunnel on: *Device, or a
// fake in tests.
type coreSession interface {
	// prepare reads what the session needs before it is used (the phone's
	// displays); it starts no stream.
	prepare(ctx context.Context) error
	// opened starts what runs beside the session and returns the device.
	opened() driver.Device
	Close() error
}

// openCoreDevice opens a phone over CoreDevice: its tunnel and services,
// without the display stream, which the first view or input starts (live).
func (p *Provider) openCoreDevice(ctx context.Context, info driver.Info) (driver.Device, error) {
	dial := p.dialFn
	if dial == nil {
		dial = func(ctx context.Context, info driver.Info) (coreSession, error) {
			return dialDevice(ctx, info, p.logger())
		}
	}
	s, err := dial(ctx, info)
	if err != nil {
		return nil, err
	}
	if err := s.prepare(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s.opened(), nil
}
