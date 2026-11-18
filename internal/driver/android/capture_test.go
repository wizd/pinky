package android

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
)

// The warning AOSP's screencap prints (to stderr, which the exec service
// sends along with stdout) on a phone with several physical displays when
// no -d is given, before the PNG.
const multiDisplayWarningText = "[Warning] Multiple displays were found, but no display id was specified! Defaulting to the first display found, however this default is not guaranteed to be consistent across captures. A display id should be specified.\n" +
	"A display ID can be specified with the [-d display-id] option.\n" +
	"See \"dumpsys SurfaceFlinger --display-id\" for valid display IDs.\n"

// A foldable like the vivo X Fold 6 in issue #154: its displays have
// vendor unique ids ("local:primary", "local:secondary") rather than
// AOSP's "local:<physical id>", so only the port ties display 0 to a
// SurfaceFlinger display. The inner screen is port 3.
const (
	vivoOuterID = "4630946213471563521"
	vivoInnerID = "4630946213471563523"
	vivoSF      = "Display 4630946213471563521 (HWC display 0): port=1 pnpId=VVO displayName=\"primary\"\n" +
		"Display 4630946213471563523 (HWC display 1): port=3 pnpId=VVO displayName=\"secondary\"\n"
	// `dumpsys display | grep -E 'displayId[ =]0[,}]'` while unfolded.
	vivoUnfolded = `    mBaseDisplayInfo=DisplayInfo{"内置屏幕", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS, FLAG_TRUSTED, real 2200 x 2480, largest app 2480 x 2480, smallest app 2200 x 2200, appVsyncOff 1000000, presDeadline 7333333, mode 2 120.0, defaultMode 2, modes [{id=1, width=2200, height=2480, fps=60.0, vsync=60.0, synthetic=false, alternativeRefreshRates=[120.0], supportedHdrTypes=[2, 3]}], rotation 0, state ON, committedState ON, type INTERNAL, uniqueId "local:secondary", app 2200 x 2480, density 480 (480.0 x 480.0) dpi, layerStack 0, colorMode 0, supportedColorModes [0, 7, 9], address {port=3, model=0x401cec6a7a2b7b}, deviceProductInfo DeviceProductInfo{name=, manufacturerPnpId=VVO, productId=1, modelYear=null, manufactureDate=ManufactureDate{week=27, year=2006}, connectionToSinkType=0}, removeMode 0, installOrientation ROTATION_0}
    mOverrideDisplayInfo=DisplayInfo{"内置屏幕", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS, FLAG_TRUSTED, real 2200 x 2480, largest app 2480 x 2480, smallest app 2200 x 2200, rotation 0, state ON, committedState ON, type INTERNAL, uniqueId "local:secondary", app 2200 x 2480, density 480 (480.0 x 480.0) dpi, layerStack 0, address {port=3, model=0x401cec6a7a2b7b}, removeMode 0, installOrientation ROTATION_0}
    DisplayViewport{type=INTERNAL, valid=true, isActive=true, displayId=0, uniqueId='local:secondary', physicalPort=3, orientation=0, logicalFrame=Rect(0, 0 - 2200, 2480), physicalFrame=Rect(0, 0 - 2200, 2480), deviceWidth=2200, deviceHeight=2480}
`
	// The same while folded: display 0 is the outer screen, port 1.
	vivoFolded = `    mBaseDisplayInfo=DisplayInfo{"内置屏幕", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_TRUSTED, real 1172 x 2748, rotation 0, state ON, type INTERNAL, uniqueId "local:primary", app 1172 x 2748, density 480 (480.0 x 480.0) dpi, layerStack 0, address {port=1, model=0x401cec6a7a2b7b}, removeMode 0}
    mOverrideDisplayInfo=DisplayInfo{"内置屏幕", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_TRUSTED, real 1172 x 2748, rotation 0, state ON, type INTERNAL, uniqueId "local:primary", app 1172 x 2748, density 480 (480.0 x 480.0) dpi, layerStack 0, address {port=1, model=0x401cec6a7a2b7b}, removeMode 0}
    DisplayViewport{type=INTERNAL, valid=true, isActive=true, displayId=0, uniqueId='local:primary', physicalPort=1, orientation=0, logicalFrame=Rect(0, 0 - 1172, 2748), physicalFrame=Rect(0, 0 - 1172, 2748), deviceWidth=1172, deviceHeight=2748}
`
)

func TestCutPNG(t *testing.T) {
	pic := testPNG(t, 1, false)
	for _, c := range []struct {
		name, out, lead string
		ok              bool
	}{
		{"plain", string(pic), "", true},
		{"warning first", multiDisplayWarningText + string(pic), multiDisplayWarningText, true},
		{"no PNG", "screencap: no display\n", "", false},
		{"empty", "", "", false},
		{"signature cut short", multiDisplayWarningText + "\x89PNG\r\n", "", false},
	} {
		img, lead, ok := cutPNG([]byte(c.out))
		if ok != c.ok || lead != c.lead || (ok && !bytes.Equal(img, pic)) {
			t.Errorf("%s: cutPNG = %d bytes, %q, %v; want the PNG, %q, %v", c.name, len(img), lead, ok, c.lead, c.ok)
		}
	}
	if !multiDisplayWarning(multiDisplayWarningText) || multiDisplayWarning("") {
		t.Error("multiDisplayWarning misreads the warning")
	}
}

func TestScreenDisplay(t *testing.T) {
	// A Pixel Fold on Android 14: AOSP unique ids hold the physical id.
	const pixelFoldSF = "Display 4619827259835644672 (HWC display 0): port=0 pnpId=GGL displayName=\"EXTERNAL\"\n" +
		"Display 4619827551948147201 (HWC display 1): port=1 pnpId=GGL displayName=\"INTERNAL\"\n"
	const pixelFoldOpen = `    mBaseDisplayInfo=DisplayInfo{"Built-in Screen", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS, FLAG_TRUSTED, real 2208 x 1840, rotation 0, state ON, type INTERNAL, uniqueId "local:4619827551948147201", app 2208 x 1840, density 420 (420.0 x 420.0) dpi, layerStack 0, address {port=1, model=0x401cecd21d5101}, removeMode 0}
    mOverrideDisplayInfo=DisplayInfo{"Built-in Screen", displayId 0, displayGroupId 0, FLAG_SECURE, FLAG_SUPPORTS_PROTECTED_BUFFERS, FLAG_TRUSTED, real 2208 x 1840, rotation 0, state ON, type INTERNAL, uniqueId "local:4619827551948147201", app 2208 x 1840, density 420 (420.0 x 420.0) dpi, layerStack 0, address {port=1, model=0x401cecd21d5101}, removeMode 0}
    DisplayViewport{type=INTERNAL, valid=true, isActive=true, displayId=0, uniqueId='local:4619827551948147201', physicalPort=1, orientation=0, logicalFrame=Rect(0, 0 - 2208, 1840), physicalFrame=Rect(0, 0 - 2208, 1840), deviceWidth=2208, deviceHeight=1840}
`
	// Android 10: the older DisplayInfo layout, name first.
	const android10SF = "Display 19260578298818433 (HWC display 0): port=129 pnpId=QCM displayName=\"\"\n" +
		"Display 19260578298818434 (HWC display 1): port=130 pnpId=QCM displayName=\"\"\n"
	const android10 = `    mBaseDisplayInfo=DisplayInfo{"Built-in Screen, displayId 0", uniqueId "local:19260578298818434", app 1080 x 2340, real 1080 x 2340, largest app 2340 x 2268, smallest app 1080 x 1008, mode 1, defaultMode 1, modes [{id=1, width=1080, height=2340, fps=60.0}], colorMode 0, rotation 0, density 440 (409.432 x 406.5) dpi, layerStack 0, appVsyncOff 1000000, presDeadline 16666666, type BUILT_IN, address {port=130}, state ON, FLAG_SECURE, removeMode 0}
`
	for _, c := range []struct {
		name, sf, display, want string
	}{
		{"vivo unfolded, by port", vivoSF, vivoUnfolded, vivoInnerID},
		{"vivo folded, by port", vivoSF, vivoFolded, vivoOuterID},
		{"Pixel Fold open, by unique id", pixelFoldSF, pixelFoldOpen, "4619827551948147201"},
		{"Android 10", android10SF, android10, "19260578298818434"},
		{
			"viewports only",
			vivoSF,
			"  DisplayViewport{type=INTERNAL, valid=true, isActive=false, displayId=0, uniqueId='local:primary', physicalPort=1, orientation=0}\n" +
				"  DisplayViewport{type=INTERNAL, valid=true, isActive=true, displayId=0, uniqueId='local:secondary', physicalPort=3, orientation=0}\n",
			vivoInnerID,
		},
		{"nothing about display 0", vivoSF, "", ""},
		{"unknown port", vivoSF, `    mOverrideDisplayInfo=DisplayInfo{"x", displayId 0, uniqueId "local:tertiary", address {port=7}}`, ""},
	} {
		got, ok := screenDisplay(c.sf, c.display)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("%s: screenDisplay = %q, %v; want %q", c.name, got, ok, c.want)
		}
	}
	if ds := parseSFDisplays(vivoSF); len(ds) != 2 || ds[1].id != vivoInnerID || ds[1].port != 3 {
		t.Errorf("parseSFDisplays = %+v", ds)
	}
}

// capturePhone is a fake phone for screencap: what screencap prints for
// each display ("" its default), and what the display dumps say.
type capturePhone struct {
	mu      sync.Mutex
	shots   map[string]string
	sf      string
	display string
	cmds    []string
}

func (p *capturePhone) run(_, service string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case strings.HasPrefix(service, "exec:screencap -p"):
		p.cmds = append(p.cmds, service)
		id := strings.TrimPrefix(strings.TrimPrefix(service, "exec:screencap -p"), " -d ")
		out, ok := p.shots[id]
		if !ok {
			return "Invalid physical display ID\n", true
		}
		return out, true
	case service == "shell:"+screenDisplayCmd:
		p.cmds = append(p.cmds, service)
		return p.sf + "--display--\n" + p.display, true
	}
	return "", false
}

func (p *capturePhone) set(fn func(p *capturePhone)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
	p.cmds = nil
}

func (p *capturePhone) commands() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cmds...)
}

func newCaptureDevice(t *testing.T, p *capturePhone) *Device {
	t.Helper()
	f := startFakeADB(t)
	f.set(func(f *fakeADB) {
		f.props = map[string]string{"phone": ""}
		f.run = p.run
	})
	return newDevice(f.config(), driver.Info{ID: "phone", Connection: "usb"}, deviceProps{SDK: 34})
}

// testPNG returns a 200×200 PNG: noise, or black as a screen that is off.
func testPNG(t *testing.T, seed uint64, black bool) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 200, 200))
	r := rand.New(rand.NewPCG(seed, 2))
	for y := range 200 {
		for x := range 200 {
			c := color.RGBA{A: 255}
			if !black {
				c.R, c.G, c.B = uint8(r.Uint32()), uint8(r.Uint32()), uint8(r.Uint32())
			}
			m.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestBlankPNG(t *testing.T) {
	if !blankPNG(testPNG(t, 1, true)) {
		t.Error("a black PNG is not blank")
	}
	if blankPNG(testPNG(t, 1, false)) {
		t.Error("a PNG of noise is blank")
	}
}

// A phone with one display runs plain `screencap -p` and nothing else,
// every time.
func TestScreencapOneDisplay(t *testing.T) {
	ctx := context.Background()
	pic := testPNG(t, 1, false)
	p := &capturePhone{shots: map[string]string{"": string(pic)}}
	d := newCaptureDevice(t, p)
	for range 3 {
		got, err := d.screencap(ctx)
		if err != nil || !bytes.Equal(got, pic) {
			t.Fatalf("screencap = %d bytes, %v", len(got), err)
		}
	}
	want := []string{"exec:screencap -p", "exec:screencap -p", "exec:screencap -p"}
	if got := p.commands(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %q, want %q", got, want)
	}
	if _, err := d.Screenshot(ctx); err != nil {
		t.Errorf("Screenshot: %v", err)
	}
	// No PNG at all is still an error.
	p.set(func(p *capturePhone) { p.shots[""] = "screencap: capture failed\n" })
	if _, err := d.screencap(ctx); err == nil || !strings.Contains(err.Error(), "no PNG") {
		t.Errorf("screencap without a PNG: %v", err)
	}
}

// GitHub issue #154: a vivo X Fold 6 prints a warning before the PNG and
// captures its outer screen, black while unfolded, by default.
func TestScreencapFoldable(t *testing.T) {
	ctx := context.Background()
	inner, outer, black := string(testPNG(t, 1, false)), string(testPNG(t, 2, false)), string(testPNG(t, 1, true))
	p := &capturePhone{
		sf:      vivoSF,
		display: vivoUnfolded,
		shots: map[string]string{
			"":          multiDisplayWarningText + black,
			vivoOuterID: black,
			vivoInnerID: inner,
		},
	}
	d := newCaptureDevice(t, p)
	img, err := d.Screenshot(ctx)
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if img.Bounds().Dx() != 200 {
		t.Errorf("Screenshot = %v", img.Bounds())
	}
	got, err := d.screencap(ctx)
	if err != nil || string(got) != inner {
		t.Fatalf("screencap = %d bytes, %v; want the inner screen", len(got), err)
	}
	cmds := p.commands()
	want := []string{"exec:screencap -p", "shell:" + screenDisplayCmd, "exec:screencap -p -d " + vivoInnerID, "exec:screencap -p -d " + vivoInnerID}
	if strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %q, want %q", cmds, want)
	}

	// Folded: display 0 moves to the outer screen and the inner one goes
	// black. The black capture looks the display up again (once the last
	// look is displayRecheck old).
	p.set(func(p *capturePhone) {
		p.display = vivoFolded
		p.shots[vivoOuterID] = outer
		p.shots[vivoInnerID] = black
	})
	d.capDisp.mu.Lock()
	d.capDisp.at = time.Now().Add(-displayRecheck)
	d.capDisp.mu.Unlock()
	got, err = d.screencap(ctx)
	if err != nil || string(got) != outer {
		t.Fatalf("folded: screencap = %d bytes, %v; want the outer screen", len(got), err)
	}
	want = []string{"exec:screencap -p -d " + vivoInnerID, "shell:" + screenDisplayCmd, "exec:screencap -p -d " + vivoOuterID}
	if cmds := p.commands(); strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Errorf("folded: commands = %q, want %q", cmds, want)
	}

	// A screen that really is black looks again at most once every
	// displayRecheck.
	p.set(func(p *capturePhone) { p.shots[vivoOuterID] = black })
	for range 3 {
		if got, err := d.screencap(ctx); err != nil || string(got) != black {
			t.Fatalf("black screen: screencap = %d bytes, %v", len(got), err)
		}
	}
	want = []string{"exec:screencap -p -d " + vivoOuterID, "exec:screencap -p -d " + vivoOuterID, "exec:screencap -p -d " + vivoOuterID}
	if cmds := p.commands(); strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Errorf("black screen: commands = %q, want %q", cmds, want)
	}
}

// A phone with several displays whose dumps say nothing usable keeps
// screencap's default display, with the warning cut off.
func TestScreencapUnknownDisplay(t *testing.T) {
	ctx := context.Background()
	pic := testPNG(t, 1, false)
	p := &capturePhone{sf: vivoSF, shots: map[string]string{"": multiDisplayWarningText + string(pic)}}
	d := newCaptureDevice(t, p)
	for range 2 {
		got, err := d.screencap(ctx)
		if err != nil || !bytes.Equal(got, pic) {
			t.Fatalf("screencap = %d bytes, %v", len(got), err)
		}
	}
	want := []string{"exec:screencap -p", "shell:" + screenDisplayCmd, "exec:screencap -p"}
	if cmds := p.commands(); strings.Join(cmds, "|") != strings.Join(want, "|") {
		t.Errorf("commands = %q, want %q", cmds, want)
	}
}
