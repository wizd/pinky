package gateway

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// settingsRow is a row of iOS Settings' home page as the iOS driver lists
// it: the cell's accessibility element (a button, or a switch), labelled
// with the row's title and value ("Wi-Fi, Home"), over the whole cell. The
// icon and the chevron are images without labels, which a read leaves out.
type settingsRow struct {
	label string
	role  string
	h     float64 // points
	badge string  // a badge only OCR reads, at the right of the row
	gap   bool    // a new section starts above the row
}

var settingsHome = []settingsRow{
	{label: "Apple Account, Sign in to access your iCloud data, the App Store, Apple Services and more.", role: screen.RoleButton, h: 80},
	{label: "Airplane Mode", role: screen.RoleSwitch, gap: true},
	{label: "Wi-Fi, Home"}, {label: "Bluetooth, On"}, {label: "Cellular"},
	{label: "Personal Hotspot, Off"}, {label: "Battery"}, {label: "VPN, Not Connected"},
	{label: "General", badge: "1", gap: true}, {label: "Accessibility"}, {label: "Action Button"},
	{label: "Camera"}, {label: "Control Center"}, {label: "Display & Brightness"},
	{label: "Home Screen & App Library"}, {label: "Search"}, {label: "Siri"},
	{label: "StandBy"}, {label: "Wallpaper"},
	{label: "Notifications", gap: true}, {label: "Sounds & Haptics"}, {label: "Focus"}, {label: "Screen Time"},
	{label: "Face ID & Passcode", gap: true}, {label: "Emergency SOS"}, {label: "Privacy & Security"},
	{label: "App Store", gap: true}, {label: "Game Center"}, {label: "iCloud"}, {label: "Wallet & Apple Pay"},
	{label: "Apps", gap: true},
}

// settingsDevice is an iPhone Pro Max (440 by 956 points, 1320 by 2868
// pixels: 3 pixels a point) on iOS 27 showing Settings' home page, read
// the way the WebDriverAgent path reads it (wdaScreenElements):
//
//   - frames in points, rounded to pixels edge by edge (pixelRect), so a
//     scroll offset with a fraction moves a row's edges unevenly;
//   - no element for the list around the rows (GitHub issue #148);
//   - a navigation bar whose large title and search field collapse into an
//     inline title as the page scrolls, and rows that slide under it and
//     the status bar cut to their uncovered part or left out (unoccluded);
//   - with OCR, the text OCR finds that no element shows (ocr.Extras): a
//     badge and the visible piece of a row behind the translucent bar.
//
// A swipe scrolls by the finger's travel less the touch slop, times jump.
type settingsDevice struct {
	*demo.Device
	off    float64 // scroll offset, points
	jump   float64
	swipes int
}

const (
	settingsScale  = 3
	settingsWPt    = 440
	settingsHPt    = 956
	settingsW      = 1320
	settingsH      = 2868
	settingsTop    = 214 // where the first row starts at the top, points
	settingsStatus = 50  // status bar height, points
	settingsSlop   = 8   // points of a drag before the list moves
)

func (d *settingsDevice) rowTops() ([]float64, float64) {
	tops := make([]float64, len(settingsHome))
	y := float64(settingsTop)
	for i, r := range settingsHome {
		if r.gap {
			y += 35
		}
		tops[i] = y
		y += rowHeight(r)
	}
	return tops, y + 35 // the content's bottom
}

func rowHeight(r settingsRow) float64 {
	if r.h > 0 {
		return r.h
	}
	return 52
}

func pxRect(x, y, w, h float64) screen.Rect {
	x0, y0 := int(math.Round(x*settingsScale)), int(math.Round(y*settingsScale))
	x1, y1 := int(math.Round((x+w)*settingsScale)), int(math.Round((y+h)*settingsScale))
	x0, y0, x1, y1 = max(x0, 0), max(y0, 0), min(x1, settingsW), min(y1, settingsH)
	if x1 <= x0 || y1 <= y0 {
		return screen.Rect{}
	}
	return screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

func (d *settingsDevice) Observe(_ context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	s := &screen.Screen{
		Width: settingsW, Height: settingsH, Orientation: "portrait", CapturedAt: time.Now(),
		App: screen.App{ID: "com.apple.Preferences", Name: "Settings"},
	}
	off := d.off
	// The navigation bar: the large title and the search field fold into
	// it as the page scrolls up, leaving the inline title.
	barBottom := math.Max(94, 198-off)
	bar := pxRect(0, settingsStatus, settingsWPt, barBottom-settingsStatus)
	status := pxRect(0, 0, settingsWPt, settingsStatus)
	if off < 60 {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: "Settings",
			Source: screen.SourceTree, Bounds: pxRect(16, math.Max(settingsStatus, 98-off), 200, 41)})
	} else {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: "Settings",
			Source: screen.SourceTree, Bounds: pxRect(settingsWPt/2-32, 61, 65, 22)})
	}
	if off < 30 {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleInput, Label: "Search",
			States: []string{screen.StateEditable}, Source: screen.SourceTree, Bounds: pxRect(16, 150-off, settingsWPt-32, 36)})
	}
	tops, _ := d.rowTops()
	var extras []screen.Element
	for i, r := range settingsHome {
		top := tops[i] - off
		h := rowHeight(r)
		if top+h <= 0 || top >= settingsHPt {
			continue // WDA: not visible
		}
		b := pxRect(16, top, settingsWPt-32, h)
		role := r.role
		if role == "" {
			role = screen.RoleButton
		}
		// unoccluded: a row whose middle is under a bar keeps its part
		// below the bar when a quarter of it is left, else it goes.
		full := b
		for _, o := range []screen.Rect{status, bar} {
			if !o.Contains(b.Center()) {
				continue
			}
			below := screen.Rect{X: b.X, Y: o.Y + o.H, W: b.W, H: b.Y + b.H - (o.Y + o.H)}
			if float64(below.H) < 8*settingsScale || 4*below.W*below.H < full.W*full.H {
				b = screen.Rect{}
				break
			}
			b = below
		}
		if b.Empty() {
			// OCR still reads the row through the translucent bar: the
			// part of its title the bar's edge leaves.
			if top+h > settingsStatus+4 {
				title, _, _ := strings.Cut(r.label, ",")
				extras = append(extras, screen.Element{Role: screen.RoleText, Label: title[:min(len(title), 6)],
					Source: screen.SourceOCR, Bounds: pxRect(60, math.Max(top+12, settingsStatus+2), 80, 20.4)})
			}
			continue
		}
		s.Elements = append(s.Elements, screen.Element{Role: role, Label: r.label, Bounds: b, Source: screen.SourceTree})
		if r.badge != "" {
			// OCR's box is its own: its middle is the row's give or take
			// a pixel, depending on where the row lies.
			extras = append(extras, screen.Element{Role: screen.RoleText, Label: r.badge,
				Source: screen.SourceOCR, Bounds: pxRect(settingsWPt-68, top+13.7, 21.6, 24.3)})
		}
	}
	if opt.OCR {
		s.Elements = append(s.Elements, extras...)
	}
	return s, nil
}

func (d *settingsDevice) Swipe(_ context.Context, from, to screen.Point, _ time.Duration) error {
	d.swipes++
	_, bottom := d.rowTops()
	move := (float64(from.Y-to.Y)/settingsScale - math.Copysign(settingsSlop, float64(from.Y-to.Y))) * d.jump
	d.off = math.Max(0, math.Min(d.off+move, bottom-settingsHPt))
	return nil
}

func (d *settingsDevice) WaitSettled(context.Context, time.Duration) (bool, error) { return true, nil }

func (d *settingsDevice) ReadsText() bool { return true }

func readSettings(t *testing.T, jump float64, ocr string) (*Result, *settingsDevice) {
	t.Helper()
	base := demo.NewDevice("iphone", "iPhone Pro Max", driver.IOS)
	dev := &settingsDevice{Device: base, jump: jump}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read Settings")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 10, MaxMS: 20000, OCR: ocr})
	if res.Error != "" || res.Scan == nil {
		t.Fatalf("read: %+v", res)
	}
	return res, dev
}

func settingsLabels() []string {
	out := make([]string, len(settingsHome))
	for i, r := range settingsHome {
		out[i] = r.label
	}
	return out
}

// GitHub issue #148: on Settings' home page an iPhone read stopped with a
// gap after the first screen, though the two screens shared six rows. The
// screens after a swipe came from the action pipeline's read, which adds
// the text OCR finds that no element shows (General's badge) where the
// first screen had none: a line above part of the shared rows that the
// last screen did not have. The whole page reads now, each row once.
func TestReadListReadsIOSSettingsHomeWithoutGap(t *testing.T) {
	res, dev := readSettings(t, 1, "auto")
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	if !reflect.DeepEqual(got, settingsLabels()) {
		t.Fatalf("stop %q (%s)\nlines = %#v\nwant    %#v", res.Scan.Stop, res.Message, got, settingsLabels())
	}
	if res.Scan.Stop == "gap" || res.Scan.Pages < 3 || dev.swipes < 2 || res.Scan.Overlap < 6 {
		t.Fatalf("scan: %+v\n%s", res.Scan, res.Message)
	}
}

// With ocr "all" every screen has OCR's badge, at a centre a pixel above
// or below the row's: the row must read the same on every screen.
func TestReadListReadsIOSSettingsHomeWithOCR(t *testing.T) {
	res, _ := readSettings(t, 1, "all")
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	var want []string
	for _, r := range settingsHome {
		want = append(want, r.label)
		if r.badge != "" {
			want = append(want, r.badge)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stop %q (%s)\nlines = %#v\nwant    %#v", res.Scan.Stop, res.Message, got, want)
	}
}

// ocr "auto" reads the screens after a swipe as it reads the first: OCR's
// lines go where the list's tree says enough and stay where it does not.
func TestAutoOCRReadsEveryScreenAlike(t *testing.T) {
	list := screen.Element{Role: screen.RoleList, Bounds: screen.Rect{X: 0, Y: 600, W: 1320, H: 2268}}
	badge := screen.Element{Role: screen.RoleText, Label: "1", Source: screen.SourceOCR, Bounds: screen.Rect{X: 1116, Y: 700, W: 65, H: 73}}
	rich := &screen.Screen{Width: 1320, Height: 2868}
	for i, label := range []string{"General", "Accessibility", "Action Button", "Camera"} {
		rich.Elements = append(rich.Elements, screen.Element{Role: screen.RoleButton, Label: label,
			Source: screen.SourceTree, Bounds: screen.Rect{X: 48, Y: 650 + i*156, W: 1224, H: 156}})
	}
	rich.Elements = append(rich.Elements, badge)
	got, kept := autoOCR(rich, list)
	if kept || len(got.Elements) != 4 || len(rich.Elements) != 5 {
		t.Fatalf("rich tree: kept=%v, %d elements (input %d)", kept, len(got.Elements), len(rich.Elements))
	}
	sparse := &screen.Screen{Width: 1320, Height: 2868, Elements: []screen.Element{
		{Role: screen.RoleImage, Source: screen.SourceTree, Bounds: screen.Rect{X: 0, Y: 600, W: 1320, H: 1200}},
		{Role: screen.RoleImage, Source: screen.SourceTree, Bounds: screen.Rect{X: 0, Y: 1800, W: 1320, H: 1000}},
		{Role: screen.RoleText, Label: "A picture caption read by OCR", Source: screen.SourceOCR, Bounds: screen.Rect{X: 48, Y: 900, W: 900, H: 60}},
	}}
	if got, kept := autoOCR(sparse, list); !kept || got != sparse {
		t.Fatalf("sparse tree: kept=%v, %d elements", kept, len(got.Elements))
	}
}

// A gap above a strong run of shared lines says how many lines the
// screens shared and which lines above them were new.
func TestReadListGapSaysWhy(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"a long title", "b long title", "c long title", "d long title"},
			{"a long title", "new long title", "b long title", "c long title", "d long title", "e long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	listRef := 0
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Scan == nil || res.Scan.Stop != "gap" {
		t.Fatalf("read: %+v", res)
	}
	want := `Screen 2 shared 3 lines with the one before, but 1 line above them ("new long title") was not on it`
	if !strings.Contains(res.Message, want) {
		t.Fatalf("message: %s\nwant it to say: %s", res.Message, want)
	}
}

// A swipe that carries the page past rows the read never saw is still a
// gap, and the message says why.
func TestReadListIOSSettingsJumpIsAGap(t *testing.T) {
	res, _ := readSettings(t, 2.5, "auto")
	if res.Scan.Stop != "gap" || !res.Scan.Truncated || res.Scan.Complete || res.Scan.Continuation != "" {
		t.Fatalf("scan: %+v\n%s", res.Scan, res.Message)
	}
	if !strings.Contains(res.Message, "Screen 2 shared no run of lines") {
		t.Fatalf("message: %s", res.Message)
	}
	got := scanText(res.Scan)
	if strings.Contains(got, "Notifications") || strings.Contains(got, "Apps\n") {
		t.Fatalf("content after a gap was accepted:\n%s", got)
	}
}
