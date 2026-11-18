package android

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Screenshots over USB come from `screencap -p`, which captures one
// physical display. A phone with one display (most of them) runs it as it
// is and pays nothing more. A phone with more than one, such as a foldable
// with an inner and an outer screen, needs `-d <id>` to capture the screen
// in use: without it screencap prints a warning before the PNG and then
// captures the first display it finds, which may be the folded-away one
// and black (GitHub issue #154). The phone says it has several displays
// with that warning; the driver then finds the physical display that
// logical display 0 shows on (screenDisplayCmd) and passes its id from
// then on. Folding or unfolding the phone moves display 0 to the other
// screen and leaves the old one off, so a failed or black capture looks
// the id up again.

// screenDisplayCmd lists the physical displays SurfaceFlinger has, and
// the lines of `dumpsys display` that describe logical display 0: its
// DisplayInfo (with the unique id and address of the display it shows on)
// and its input viewport (with the physical port). Up to Android 11 the
// DisplayInfo reads `{"Built-in Screen, displayId 0", uniqueId ...`.
const screenDisplayCmd = `dumpsys SurfaceFlinger --display-id; echo --display--; dumpsys display | grep -E 'displayId[ =]0[,}"]'`

// displayRecheck is how often, at most, a capture that looks wrong looks
// the display up again.
const displayRecheck = 3 * time.Second

// capDisplay is what the driver knows about a phone's displays for
// screencap.
type capDisplay struct {
	mu    sync.Mutex
	multi bool      // the phone has more than one physical display
	id    string    // the physical display to capture; "" for screencap's default
	at    time.Time // when id was last looked up
}

// pngSig is the signature every PNG starts with.
var pngSig = []byte("\x89PNG\r\n\x1a\n")

// cutPNG returns the PNG in screencap's output and the text printed before
// it, such as the warning about several displays.
func cutPNG(out []byte) (img []byte, lead string, ok bool) {
	i := bytes.Index(out, pngSig)
	if i < 0 {
		return nil, "", false
	}
	return out[i:], string(out[:i]), true
}

// multiDisplayWarning reports whether text screencap printed says the
// phone has several displays and none was chosen. AOSP prints "[Warning]
// Multiple displays were found, but no display id was specified!" and
// points to `dumpsys SurfaceFlinger --display-id`.
func multiDisplayWarning(lead string) bool {
	l := strings.ToLower(lead)
	return strings.Contains(l, "multiple displays") || strings.Contains(l, "--display-id")
}

// screencap captures the phone's screen and returns the PNG.
func (d *Device) screencap(ctx context.Context) ([]byte, error) {
	cd := &d.capDisp
	cd.mu.Lock()
	id, multi := cd.id, cd.multi
	cd.mu.Unlock()

	img, lead, err := d.screencapOn(ctx, id)
	if err == nil && !multi && !multiDisplayWarning(lead) {
		return img, nil // one display: the usual case
	}
	if err == nil && id != "" && !blankPNG(img) {
		return img, nil
	}
	if err == nil && !multi {
		d.cfg.logf("android: %s: the phone has several displays: %q", d.info.ID, firstLine([]byte(lead)))
		cd.mu.Lock()
		cd.multi = true
		cd.mu.Unlock()
	}
	// Several displays, or a capture that failed: capture the display that
	// display 0 shows on. Look it up when it is not known yet, or when the
	// capture failed or came out black, as it does after folding or
	// unfolding.
	nid, ok := d.lookUpDisplay(ctx)
	if !ok || nid == id {
		if err != nil {
			return nil, err
		}
		return img, nil
	}
	img2, _, err2 := d.screencapOn(ctx, nid)
	if err2 != nil {
		if err == nil {
			return img, nil
		}
		return nil, err2
	}
	return img2, nil
}

// screencapOn runs screencap on a physical display, or on its default one
// for "".
func (d *Device) screencapOn(ctx context.Context, id string) (img []byte, lead string, err error) {
	cmd := "screencap -p"
	if id != "" {
		cmd += " -d " + id
	}
	out, err := d.client.Exec(ctx, d.info.ID, cmd)
	if err != nil {
		return nil, "", wrapErr(err)
	}
	img, lead, ok := cutPNG(out)
	if !ok {
		return nil, "", fmt.Errorf("android: screencap: no PNG (%d bytes: %q)", len(out), firstLine(out))
	}
	return img, lead, nil
}

// lookUpDisplay finds the physical display that display 0 shows on and
// keeps it, when the phone has more than one. It looks at most once every
// displayRecheck and otherwise returns what it found last.
func (d *Device) lookUpDisplay(ctx context.Context) (string, bool) {
	cd := &d.capDisp
	cd.mu.Lock()
	if time.Since(cd.at) < displayRecheck {
		id := cd.id
		cd.mu.Unlock()
		return id, id != ""
	}
	cd.at = time.Now()
	cd.mu.Unlock()
	out, err := d.client.Shell(ctx, d.info.ID, screenDisplayCmd)
	if err != nil {
		d.cfg.logf("android: %s: read the displays: %v", d.info.ID, err)
		return "", false
	}
	sec := splitSections(string(out))
	if len(parseSFDisplays(sec[""])) < 2 {
		return "", false // one display: screencap's default is the right one
	}
	id, ok := screenDisplay(sec[""], sec["display"])
	cd.mu.Lock()
	defer cd.mu.Unlock()
	cd.multi = true
	if !ok {
		d.cfg.logf("android: %s: cannot tell which display shows the screen from %q", d.info.ID, firstLine(out))
		return "", false
	}
	if cd.id != id {
		d.cfg.logf("android: %s: the screen is on physical display %s", d.info.ID, id)
	}
	cd.id = id
	return id, true
}

// physDisplay is one line of `dumpsys SurfaceFlinger --display-id`.
type physDisplay struct {
	id   string
	port int // -1 when not given
}

// sfDisplayIDRE matches `dumpsys SurfaceFlinger --display-id` lines:
// "Display 4619827259835644672 (HWC display 0): port=0 pnpId=QCM
// displayName="...""; Android 10 has no port.
var (
	sfDisplayIDRE = regexp.MustCompile(`(?m)^\s*Display (\d+) \(HWC display \d+\)(?::\s*port=(\d+))?`)
	uniqueIDRE    = regexp.MustCompile(`uniqueId[ =]["']local:([^"']+)["']`)
	portRE        = regexp.MustCompile(`(?:address \{port=|physicalPort=)(\d+)`)
)

// parseSFDisplays returns the physical displays from `dumpsys
// SurfaceFlinger --display-id`.
func parseSFDisplays(out string) []physDisplay {
	var ds []physDisplay
	for _, m := range sfDisplayIDRE.FindAllStringSubmatch(out, -1) {
		p := -1
		if m[2] != "" {
			p, _ = strconv.Atoi(m[2])
		}
		ds = append(ds, physDisplay{id: m[1], port: p})
	}
	return ds
}

// parseScreenAddress returns what `dumpsys display`'s lines about logical
// display 0 say about the physical display it shows on: the part of its
// unique id after "local:" (the physical display id on AOSP, a vendor's
// own name such as "secondary" on some phones) and its port, -1 if none.
// DisplayInfo lines ("displayId 0, ... uniqueId "local:...", ...
// address {port=3, model=0x...}") come before viewports
// ("DisplayViewport{... displayId=0, uniqueId='local:...',
// physicalPort=3, ...}"); the current (override) info comes first.
func parseScreenAddress(out string) (unique string, port int) {
	port = -1
	lines := strings.Split(out, "\n")
	// Rank the lines: the current DisplayInfo, then any DisplayInfo, then
	// active viewports, then the rest.
	// Lines about other displays rank -1.
	rank := func(l string) int {
		info := strings.Contains(l, "displayId 0,") || strings.Contains(l, `displayId 0"`)
		switch {
		case info && strings.Contains(l, "mOverrideDisplayInfo"):
			return 0
		case info:
			return 1
		case !strings.Contains(l, "displayId=0,"):
			return -1
		case strings.Contains(l, "isActive=true"):
			return 2
		case strings.Contains(l, "isActive=false"):
			return 4
		}
		return 3
	}
	for r := 0; r <= 4 && (unique == "" || port < 0); r++ {
		for _, l := range lines {
			if rank(l) != r {
				continue
			}
			if m := uniqueIDRE.FindStringSubmatch(l); m != nil && unique == "" {
				unique = m[1]
			}
			if m := portRE.FindStringSubmatch(l); m != nil && port < 0 {
				port, _ = strconv.Atoi(m[1])
			}
		}
	}
	return unique, port
}

// screenDisplay returns the id of the physical display that logical
// display 0 shows on, for `screencap -d`: the SurfaceFlinger display
// whose id the unique id holds, or else the one on the same port.
func screenDisplay(sf, display string) (string, bool) {
	ds := parseSFDisplays(sf)
	unique, port := parseScreenAddress(display)
	for _, p := range ds {
		if unique != "" && p.id == unique {
			return p.id, true
		}
	}
	if port >= 0 {
		for _, p := range ds {
			if p.port == port {
				return p.id, true
			}
		}
	}
	return "", false
}

// blankPNG reports whether a PNG is all black, as a capture of a screen
// that is off is. It decodes only PNGs small for their size: a screen
// with anything on it does not compress that well.
func blankPNG(img []byte) bool {
	if len(img) < 24 || string(img[12:16]) != "IHDR" {
		return false
	}
	w, h := binary.BigEndian.Uint32(img[16:20]), binary.BigEndian.Uint32(img[20:24])
	if w == 0 || h == 0 || uint64(len(img)) > uint64(w)*uint64(h)/64+1024 {
		return false
	}
	m, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		return false
	}
	b := m.Bounds()
	const steps = 32
	for y := 0; y < steps; y++ {
		for x := 0; x < steps; x++ {
			r, g, bl, _ := m.At(b.Min.X+x*b.Dx()/steps, b.Min.Y+y*b.Dy()/steps).RGBA()
			if r > 0x0800 || g > 0x0800 || bl > 0x0800 {
				return false
			}
		}
	}
	return true
}
