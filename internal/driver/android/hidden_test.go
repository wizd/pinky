package android

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/companion"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/ocr"
	"github.com/DIYgod/pinky/internal/screen"
)

// Apps the phone hides from accessibility (GitHub issue #191): on a OnePlus
// Ace 6T with ColorOS 17, WeChat's window is in front (`dumpsys window`
// has mFocusedApp com.tencent.mm/.ui.LauncherUI), but the companion's
// tree lists no application window, only the status bar and ColorOS's
// smart sidebar.

// treeServer answers the companion's tree command with a tree for each
// display, as the app on the phone does behind its adb forward.
type treeServer struct {
	ln    net.Listener
	mu    sync.Mutex
	trees map[int]*companion.Tree
}

func newTreeServer(t *testing.T) *treeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ts := &treeServer{ln: ln, trees: map[int]*companion.Tree{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go ts.handle(c)
		}
	}()
	return ts
}

func (ts *treeServer) set(display int, t *companion.Tree) {
	ts.mu.Lock()
	ts.trees[display] = t
	ts.mu.Unlock()
}

func (ts *treeServer) handle(c net.Conn) {
	defer c.Close()
	rd := bufio.NewReader(c)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID      int64  `json:"id"`
			Cmd     string `json:"cmd"`
			Display int    `json:"display"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		resp := map[string]any{"id": req.ID, "ok": false, "error": map[string]string{"code": "unsupported", "message": req.Cmd}}
		ts.mu.Lock()
		tree := ts.trees[req.Display]
		ts.mu.Unlock()
		if req.Cmd == "tree" && tree != nil {
			resp = map[string]any{"id": req.ID, "ok": true, "result": tree}
		}
		b, _ := json.Marshal(resp)
		if _, err := c.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

// forwardRunner reaches the tree server as the companion client's adb
// forward.
type forwardRunner struct{ addr string }

func (r forwardRunner) Shell(context.Context, string) (string, error)   { return "", nil }
func (r forwardRunner) Forward(context.Context, string) (string, error) { return r.addr, nil }
func (r forwardRunner) RemoveForward(context.Context, string) error     { return nil }
func (r forwardRunner) Install(context.Context, string) error           { return nil }

// hiddenPhone answers the driver's shell commands: the lock, the keyboard
// and each display's focused activity.
type hiddenPhone struct {
	mu      sync.Mutex
	locked  bool
	focused string // focusedAppsCmd's output
	asked   int    // focusedAppsCmd runs
}

func (p *hiddenPhone) run(_, svc string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch svc {
	case "shell:" + keyguardCmd:
		if p.locked {
			return "showing=true\n", true
		}
		return "showing=false\n", true
	case "shell:" + imeWindowCmd:
		return "  isVisible=false\n", true
	case "shell:" + focusedAppsCmd:
		p.asked++
		return p.focused, true
	}
	if strings.HasPrefix(svc, "exec:screencap -p") {
		return string(grayPNG(scrW, scrH)), true
	}
	return "", false
}

func (p *hiddenPhone) askedFocus() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asked
}

// hiddenDevice opens a device on a fake adb server that plays p, with its
// companion on and reading trees from ts.
func hiddenDevice(t *testing.T, p *hiddenPhone, ts *treeServer) *Device {
	t.Helper()
	f := startFakeADB(t)
	f.set(func(f *fakeADB) {
		f.props = map[string]string{"phone": pixelProps}
		f.run = p.run
	})
	cfg := Config{Client: &adb.Client{Addr: f.ln.Addr().String()}, Logf: t.Logf}
	cfg.defaults()
	d := newDevice(cfg, driver.Info{ID: "phone", Connection: "usb"}, deviceProps{SDK: 37})
	d.listApps = func(context.Context) ([]scrcpy.App, error) {
		return []scrcpy.App{{Package: "com.tencent.mm", Name: "WeChat"}, {Package: "com.android.settings", Name: "Settings"}}, nil
	}
	if _, err := d.cachedApps(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	c := companion.New(forwardRunner{addr: ts.ln.Addr().String()})
	d.comp.client, d.comp.on, d.comp.live = c, true, true
	t.Cleanup(func() { c.Close() })
	return d
}

// colorOSTree is what the companion read with WeChat in front on ColorOS
// 17: the status bar and the smart sidebar's handle on the right edge, and
// no application window. app adds an application window of that package.
func colorOSTree(app string) *companion.Tree {
	t := &companion.Tree{Display: companion.Display{Width: scrW, Height: scrH, DensityDPI: 440}}
	t.Windows = append(t.Windows,
		companion.Window{ID: 1, Type: companion.WindowSystem, Package: "com.android.systemui",
			Bounds: companion.Bounds{0, 0, scrW, 100}, Root: node("android.widget.FrameLayout", 0, 0, scrW, 100)},
		companion.Window{ID: 2, Type: companion.WindowSystem, Package: "com.coloros.smartsidebar",
			Bounds: companion.Bounds{scrW - 40, 800, scrW, 1200},
			Root:   &companion.Node{Class: "android.view.View", Desc: "Smart Sidebar", Clickable: true, Bounds: companion.Bounds{scrW - 40, 800, scrW, 1200}}},
	)
	if app != "" {
		t.Windows = append(t.Windows, companion.Window{ID: 3, Type: companion.WindowApplication, Package: app, Focused: true, Active: true,
			Bounds: companion.Bounds{0, 0, scrW, scrH},
			Root:   node("android.widget.FrameLayout", 0, 0, scrW, scrH, text("Network & internet", 40, 300, 1040, 400))})
	}
	return t
}

// wechatLines are what OCR reads on WeChat's chat list: the clock in the
// status bar, two lines of the app, and the sidebar's handle.
var wechatLines = []ocr.Line{
	line("12:30", 100, 50),
	line("Chats", 540, 200),
	line("File Transfer", 300, 600),
	line("Tools", scrW-20, 1000),
}

const wechatFocus = "  Display: mDisplayId=0 rootTasks=[...]\n" +
	"    mFocusedApp=ActivityRecord{8c1e2f4 u0 com.tencent.mm/.ui.LauncherUI t412}\n"

// TestHiddenAppOwnScreen: on the phone's own screen, a tree without the
// app's window names the app from the window manager and says it is
// hidden, and a read with OCR has the app's text from a screenshot, but
// none of the status bar's or the sidebar's.
func TestHiddenAppOwnScreen(t *testing.T) {
	stub := &ocrStub{lines: wechatLines}
	stubOCR(t, stub)
	ts := newTreeServer(t)
	ts.set(0, colorOSTree(""))
	p := &hiddenPhone{focused: wechatFocus}
	d := hiddenDevice(t, p, ts)

	s, st, err := d.ObserveWithText(context.Background(), driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.App != (screen.App{ID: "com.tencent.mm", Name: "WeChat"}) || !s.AppHidden || s.Unsettled {
		t.Fatalf("screen = %+v", s)
	}
	if h := s.Header(); !strings.Contains(h, "app: WeChat (com.tencent.mm)") || !strings.Contains(h, "app hidden from accessibility") {
		t.Errorf("header = %q", h)
	}
	if st.Areas != 1 || st.From != "screenshot" {
		t.Errorf("stats = %+v", st)
	}
	got := ocrLabels(s)
	if len(got) != 2 || got[0] != "Chats" || got[1] != "File Transfer" {
		t.Errorf("OCR labels = %q, want the app's two lines", got)
	}
	// The sidebar the tree has is still listed.
	found := false
	for _, e := range s.Elements {
		found = found || (e.Source == screen.SourceTree && e.Label == "Smart Sidebar")
	}
	if !found {
		t.Errorf("elements = %+v, want the sidebar", s.Elements)
	}

	// A computer without a recognizer marks the whole screen.
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	s, err = d.Observe(context.Background(), driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	marked := 0
	for _, e := range s.Elements {
		if e.Has(stateNoOCR) {
			marked++
			if e.Bounds != (screen.Rect{W: scrW, H: scrH}) {
				t.Errorf("no_ocr area = %+v", e.Bounds)
			}
		}
	}
	if marked != 1 || !s.AppHidden {
		t.Errorf("no_ocr areas = %d, screen = %+v", marked, s)
	}
}

// TestHiddenAppOnlyWithoutAppWindow: a tree with an application window
// reads as before, without asking the window manager, and a locked phone
// names no app whatever the window manager has focused.
func TestHiddenAppOnlyWithoutAppWindow(t *testing.T) {
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	ts := newTreeServer(t)
	ts.set(0, colorOSTree("com.android.settings"))
	p := &hiddenPhone{focused: wechatFocus}
	d := hiddenDevice(t, p, ts)
	ctx := context.Background()

	s, err := d.Observe(ctx, driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.App.ID != "com.android.settings" || s.AppHidden || strings.Contains(s.Header(), "hidden from accessibility") {
		t.Errorf("screen with the app's window = %+v", s)
	}
	if n := p.askedFocus(); n != 0 {
		t.Errorf("asked the window manager %d times with the app's window in the tree", n)
	}

	ts.set(0, colorOSTree(""))
	p.mu.Lock()
	p.locked = true
	p.mu.Unlock()
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s.App.ID != "" || s.AppHidden || !s.Locked {
		t.Errorf("locked screen = %+v", s)
	}

	// The window manager has no focused app either: nothing to name.
	p.mu.Lock()
	p.locked, p.focused = false, "  Display: mDisplayId=0\n    mFocusedApp=null\n"
	p.mu.Unlock()
	if s, err = d.Observe(ctx, driver.ObserveOptions{}); err != nil {
		t.Fatal(err)
	}
	if s.App.ID != "" || s.AppHidden {
		t.Errorf("screen without a focused app = %+v", s)
	}
}

// TestHiddenAppBackground: on a background display, the app focused on
// that display is named, not the one on the phone's own screen, and OCR
// reads the display's frame.
func TestHiddenAppBackground(t *testing.T) {
	stub := &ocrStub{lines: wechatLines}
	stubOCR(t, stub)
	ts := newTreeServer(t)
	ts.set(7, colorOSTree(""))
	p := &hiddenPhone{focused: "  Display: mDisplayId=0 rootTasks=[...]\n" +
		"    mFocusedApp=ActivityRecord{1a2b3c u0 com.android.launcher/.Launcher t1}\n" +
		"  Display: mDisplayId=7 rootTasks=[...]\n" +
		"    mFocusedApp=ActivityRecord{8c1e2f4 u0 com.tencent.mm/.ui.LauncherUI t412}\n"}
	d := hiddenDevice(t, p, ts)
	b := &Background{d: d, display: 7, closed: make(chan struct{})}

	s, st, err := b.ObserveWithText(context.Background(), driver.ObserveOptions{OCR: true, Frame: frameOf(scrW, scrH)})
	if err != nil {
		t.Fatal(err)
	}
	if s.App != (screen.App{ID: "com.tencent.mm", Name: "WeChat"}) || !s.AppHidden {
		t.Fatalf("screen = %+v", s)
	}
	if st.From != "video" {
		t.Errorf("stats = %+v", st)
	}
	got := ocrLabels(s)
	if len(got) != 2 || got[0] != "Chats" || got[1] != "File Transfer" {
		t.Errorf("OCR labels = %q", got)
	}

	// No recognizer: the display is marked.
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	if s, err = b.Observe(context.Background(), driver.ObserveOptions{OCR: true}); err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, e := range s.Elements {
		marked = marked || (e.Has(stateNoOCR) && e.Bounds == screen.Rect{W: scrW, H: scrH})
	}
	if !marked {
		t.Errorf("elements = %+v, want the display marked no_ocr", s.Elements)
	}
}

// TestHiddenAppNotUnderFocusedOverlay: the Assistant's overlay (on an
// emulator with Android 15, after a long press of the power key) is a
// focused window over the whole screen that Android gives accessibility
// services with no application type, and it hides the launcher's window.
// The person is looking at the overlay, which the tree has: no app is
// hidden there, and the window manager is not asked.
func TestHiddenAppNotUnderFocusedOverlay(t *testing.T) {
	stubOCR(t, &ocrStub{supported: driver.ErrUnsupported})
	ts := newTreeServer(t)
	tr := &companion.Tree{Display: companion.Display{Width: scrW, Height: scrH, DensityDPI: 440}}
	tr.Windows = append(tr.Windows, companion.Window{ID: 9, Type: "unknown", Package: "com.google.android.googlequicksearchbox",
		Focused: true, Active: true, Bounds: companion.Bounds{0, 0, scrW, scrH},
		Root: node("android.widget.FrameLayout", 0, 0, scrW, scrH, text("Hi, how can I help?", 40, 1900, 1040, 2000))})
	ts.set(0, tr)
	p := &hiddenPhone{focused: "  Display: mDisplayId=0\n    mFocusedApp=ActivityRecord{c47f4c1 u0 com.android.launcher/.Launcher t1}\n"}
	d := hiddenDevice(t, p, ts)

	s, err := d.Observe(context.Background(), driver.ObserveOptions{OCR: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.AppHidden || s.App.ID != "" || strings.Contains(s.Header(), "hidden from accessibility") {
		t.Errorf("screen under the Assistant's overlay = %+v", s)
	}
	if n := p.askedFocus(); n != 0 {
		t.Errorf("asked the window manager %d times with a focused overlay in the tree", n)
	}
	found := false
	for _, e := range s.Elements {
		found = found || e.Label == "Hi, how can I help?"
	}
	if !found {
		t.Errorf("elements = %+v, want the overlay's text", s.Elements)
	}
}

// TestHiddenAppWithKeyboard: a hidden app with the keyboard up and no
// elements is not an incomplete tree to fall back from: the companion stays
// on.
func TestHiddenAppWithKeyboard(t *testing.T) {
	s := &screen.Screen{App: screen.App{ID: "com.tencent.mm"}, Keyboard: true, AppHidden: true}
	if emptyWithKeyboard(s) {
		t.Error("a hidden app's empty tree counts as a failed read")
	}
	s.AppHidden = false
	if !emptyWithKeyboard(s) {
		t.Error("an app's empty tree with the keyboard up no longer counts as a failed read")
	}
}

func TestParseFocusedAppOn(t *testing.T) {
	out := "  Display: mDisplayId=0 rootTasks=[...]\n" +
		"    mFocusedApp=ActivityRecord{8c1e2f4 u0 com.tencent.mm/.ui.LauncherUI t412}\n" +
		"  Display: mDisplayId=12 rootTasks=[...]\n" +
		"    mFocusedApp=null\n" +
		"  Display: mDisplayId=13 rootTasks=[...]\n" +
		"    mFocusedApp=ActivityRecord{2 u0 com.android.calculator2/.Calculator t9}\n"
	for display, want := range map[int]string{0: "com.tencent.mm", 12: "", 13: "com.android.calculator2", 1: ""} {
		if got := parseFocusedAppOn(out, display); got != want {
			t.Errorf("display %d: %q, want %q", display, got, want)
		}
	}
	// Without display lines, the output is the main display's.
	one := "    mFocusedApp=ActivityRecord{8c1e2f4 u0 com.tencent.mm/.ui.LauncherUI t412}\n"
	if got := parseFocusedAppOn(one, 0); got != "com.tencent.mm" {
		t.Errorf("main display without sections: %q", got)
	}
	if got := parseFocusedAppOn(one, 3); got != "" {
		t.Errorf("display 3 without sections: %q", got)
	}
}

// TestScanReadHidden: the area of a hidden app is the whole screen but for
// the small windows over it; a window that spans most of the screen is a
// see-through overlay and hides nothing of it.
func TestScanReadHidden(t *testing.T) {
	tr := colorOSTree("")
	tr.Windows = append(tr.Windows, companion.Window{ID: 9, Type: companion.WindowAccessibilityOverlay, Bounds: companion.Bounds{0, 0, scrW, scrH}})
	s := &screen.Screen{Width: scrW, Height: scrH, App: screen.App{ID: "com.tencent.mm"}, AppHidden: true}
	sc := scanRead(s, tr)
	if len(sc.areas) != 1 {
		t.Fatalf("areas = %+v", sc.areas)
	}
	a := sc.areas[0]
	if a.rect != (screen.Rect{W: scrW, H: scrH}) || a.kind != "window" || a.path != "hidden:com.tencent.mm" {
		t.Errorf("area = %+v", a)
	}
	for p, want := range map[screen.Point]bool{
		{X: 540, Y: 1500}:      true,  // the app
		{X: scrW - 20, Y: 900}: false, // the sidebar's handle
		{X: 540, Y: 50}:        false, // the status bar
	} {
		if got := a.holds(p); got != want {
			t.Errorf("holds(%v) = %v, want %v", p, got, want)
		}
	}
	// Not hidden: as scanCompanion.
	s.AppHidden = false
	if sc := scanRead(s, tr); len(sc.areas) != 0 {
		t.Errorf("areas of a screen that is not hidden = %+v", sc.areas)
	}
	if sc := scanRead(&screen.Screen{AppHidden: true}, nil); len(sc.areas) != 0 {
		t.Errorf("areas without a tree = %+v", sc.areas)
	}
}
