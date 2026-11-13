package standalone

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/screen"
)

// Background displays on the phone itself (docs/notes/android-wadb.md):
// with the person's yes, the app holds the shell user's adb connection that
// Wireless debugging gives it, and tasks run on a virtual display the
// floating window shows. These are the control commands for it:
//
//	{"cmd":"shell","op":"on"|"off"}                    → {"ok":true}  turn the feature on or off
//	{"cmd":"shell","op":"port","port":N}               → {"ok":true}  Wireless debugging's port (0: gone)
//	{"cmd":"shell","op":"pair","port":N,"code":"..."}  → {"ok":true}  pair with the code and pairing port
//	{"cmd":"shell","op":"forget"}                      → {"ok":true}  drop the key
//	{"cmd":"video"}                                    → a reply line, then the display's video (video)
//	{"cmd":"takeover"}                                 → {"ok":true}  the person takes the task over
//	{"cmd":"handback","note":"..."}                    → {"ok":true}  and gives it back
//	{"cmd":"touch","action":"down","x":0,"y":0}        → {"ok":true}  the person's finger in the floating window
//
// State.Shell says where the feature stands, State.Background whether
// tasks use a background display.

// Shell is the app's adb connection to its own phone: package wadb's
// Manager on Android, adapted by libcore.
type Shell interface {
	State() ShellState
	SetOn(on bool) error
	SetPort(port int) error
	Pair(ctx context.Context, port int, code string) error
	Forget() error
	// Watch registers the function to call after each change of State.
	Watch(changed func())
}

// ShellState is the feature's state for the screens: Status is one of
// wadb's (off, unpaired, no_port, ready, connected, refused, failed).
type ShellState struct {
	On     bool   `json:"on"`
	Status string `json:"status"`
	Port   int    `json:"port,omitempty"`
	Error  string `json:"error,omitempty"`
}

// The codes of the background display's errors.
const (
	CodeNoShell   = "no_shell"   // the core has no Shell: not on Android
	CodeNoDisplay = "no_display" // no background display is open now; try again
	CodeOwnScreen = "own_screen" // the task works on the phone's own screen to its end
	CodeWrongCode = "wrong_code" // the pairing code was wrong
)

// ErrNoShell is a shell command to a core without one.
var ErrNoShell = errors.New("standalone: background displays are not available here")

// ErrWrongCode is a pairing with a wrong code; the Shell wraps it.
var ErrWrongCode = errors.New("standalone: wrong pairing code")

// pairWait bounds a pairing.
const pairWait = 30 * time.Second

func (c *Core) shell(r request) error {
	sh := c.opt.Shell
	if sh == nil {
		return ErrNoShell
	}
	switch r.Op {
	case "on", "off":
		return sh.SetOn(r.Op == "on")
	case "port":
		return sh.SetPort(r.Port)
	case "pair":
		ctx, cancel := context.WithTimeout(c.ctx, pairWait)
		defer cancel()
		return sh.Pair(ctx, r.Port, r.Code)
	case "forget":
		return sh.Forget()
	}
	return fmt.Errorf("unknown shell op %q", r.Op)
}

// deviceID is the phone tasks run on, or "" when there is none.
func (c *Core) deviceID() string {
	if d, ok := c.device(); ok {
		return d.Info.ID
	}
	return c.opt.Device
}

// TakeOver puts the person in control of the task: the agent pauses.
func (c *Core) TakeOver() error { return c.G.TakeOver(c.deviceID()) }

// HandBack gives the task back to the agent, with the person's note.
func (c *Core) HandBack(note string) error { return c.G.HandBack(c.deviceID(), note) }

// Touch is the person's finger on the floating window, at a point of the
// video. The first touch takes the task over.
func (c *Core) Touch(action driver.TouchAction, p screen.Point) error {
	if c.opt.BackgroundTouch == nil {
		return ErrNoShell
	}
	if action == driver.TouchDown {
		if err := c.TakeOver(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return c.opt.BackgroundTouch(ctx, action, p)
}

// videoInfo is the reply that starts a video connection.
type videoInfo struct {
	Codec  string `json:"codec"` // "h264" or "h265"
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Frame header flags, in the top bits of the presentation time.
const (
	frameConfig   = uint64(1) << 63
	frameKeyFrame = uint64(1) << 62
)

// video answers {"cmd":"video"}: a reply line with the codec and size, and
// then, until either side closes, the background display's video as
// frames, each a 12-byte header and the data. The header is the
// presentation time in microseconds (big-endian, 8 bytes, with frameConfig
// and frameKeyFrame in its top bits) and the data's length (big-endian, 4
// bytes). It starts with a configuration packet.
func (c *Core) video(ctx context.Context, w io.Writer, enc *json.Encoder) {
	s, err := c.G.AgentStream(ctx, c.deviceID(), driver.StreamOptions{})
	if err != nil {
		code := CodeNoDisplay
		if errors.Is(err, gateway.ErrOwnScreen) {
			code = CodeOwnScreen
		}
		_ = enc.Encode(reply{Error: err.Error(), Code: code})
		return
	}
	defer s.Close()
	codec := "h264"
	if s.Codec() == driver.HEVC {
		codec = "h265"
	}
	vw, vh := s.Size()
	if err := enc.Encode(reply{OK: true, Video: &videoInfo{Codec: codec, Width: vw, Height: vh}}); err != nil {
		return
	}
	if err := writeFrames(ctx, w, s.Packets()); err != nil {
		c.log.Info("standalone: video connection ended", "err", err)
	}
}

func writeFrames(ctx context.Context, w io.Writer, packets <-chan driver.Packet) error {
	var hdr [12]byte
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case p, ok := <-packets:
			if !ok {
				return io.EOF
			}
			pts := uint64(p.PTS.Microseconds()) &^ (frameConfig | frameKeyFrame)
			if p.Config {
				pts |= frameConfig
			}
			if p.KeyFrame {
				pts |= frameKeyFrame
			}
			binary.BigEndian.PutUint64(hdr[:8], pts)
			binary.BigEndian.PutUint32(hdr[8:], uint32(len(p.Data)))
			if _, err := w.Write(hdr[:]); err != nil {
				return err
			}
			if _, err := w.Write(p.Data); err != nil {
				return err
			}
		}
	}
}
