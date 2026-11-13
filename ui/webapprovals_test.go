package ui

import (
	"context"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/internal/webapproval"
	"github.com/DIYgod/pinky/internal/webmirror"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// fakeMirror is a web mirror that Tailscale serves.
type fakeMirror struct{}

func (fakeMirror) Token() string                 { return "VIEWTOKEN" }
func (fakeMirror) ControlToken() string          { return "" }
func (fakeMirror) NewToken()                     {}
func (fakeMirror) Watchers() []webmirror.Watcher { return nil }
func (fakeMirror) Err() error                    { return nil }
func (fakeMirror) Tailnet(context.Context) RemoteTailnet {
	return RemoteTailnet{Name: "macbook.tail1234.ts.net", URL: "https://macbook.tail1234.ts.net:8443/"}
}

// fakeApprovals is the approvals page as Settings sees it: a phone's
// request comes once a link is made.
type fakeApprovals struct {
	mu      sync.Mutex
	links   int
	reqs    []webapproval.Request
	phones  []webapproval.Phone
	allowed []string
	devices []string
	denied  []string
}

func (f *fakeApprovals) NewLink() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links++
	return "PAIRTOKEN"
}

func (f *fakeApprovals) Requests() []webapproval.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]webapproval.Request(nil), f.reqs...)
}

func (f *fakeApprovals) Allow(id, device string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed = append(f.allowed, id)
	f.devices = append(f.devices, device)
	for i, r := range f.reqs {
		if r.ID == id {
			f.phones = append(f.phones, webapproval.Phone{ID: r.ID, Login: r.Login, Browser: r.Browser, Device: device, Added: time.Now()})
			f.reqs = append(f.reqs[:i], f.reqs[i+1:]...)
			return nil
		}
	}
	return webapproval.ErrGone
}

func (f *fakeApprovals) Deny(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.denied = append(f.denied, id)
}

func (f *fakeApprovals) Phones() []webapproval.Phone {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]webapproval.Phone(nil), f.phones...)
}

func (f *fakeApprovals) Remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phones = nil
}

func (f *fakeApprovals) request() { f.requestFrom("iPhone · Safari") }

func (f *fakeApprovals) requestFrom(browser string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, webapproval.Request{ID: "req1", Login: "alex@example.com", Browser: browser, Code: "482913", Created: time.Now()})
}

func webApprovalsApp(t *testing.T) (*testApp, *fakeApprovals) {
	t.Helper()
	ta := newTestApp(t, fakeapi.OnePhone)
	fa := &fakeApprovals{}
	ta.opt.WebMirror = fakeMirror{}
	ta.opt.WebApprovals = fa
	ta.settings.WebMirror = true
	ta.sync()
	ta.page, ta.set.sec = pageSettings, secPhones
	return ta, fa
}

// Settings adds a phone only on the person's Allow, after showing the
// code the phone shows; the link's token goes only to the clipboard.
func TestWebApprovalsSettings(t *testing.T) {
	ta, fa := webApprovalsApp(t)
	tt := mg.NewTester(ta.view, 1280, 2600)
	ta.waitFor(t, "the switch", func() bool { tt.Frame(); return hasText(tt, "Answer approvals in the browser") })
	if hasText(tt, "Add a phone") {
		t.Fatal("Add a phone shows while the switch is off")
	}
	ta.settings.WebApprovals = true
	ta.waitFor(t, "Add a phone", func() bool { tt.Frame(); return hasText(tt, "Make a link") })
	if err := tt.Click("Make a link"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the link", func() bool { tt.Frame(); return hasText(tt, "Link for your phone") })
	if hasText(tt, "PAIRTOKEN") {
		t.Fatal("the link's token is on screen")
	}
	if err := tt.Click("Copy the link for your phone"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "https://macbook.tail1234.ts.net:8443/approvals#pair=PAIRTOKEN" {
		t.Fatalf("copied %q", got)
	}

	fa.request()
	ta.waitFor(t, "the request", func() bool { tt.Frame(); return hasText(tt, "A browser asks to answer approvals") })
	if !hasText(tt, "iPhone · Safari · alex@example.com. Allow it only if your phone shows this code:") || !hasText(tt, "482 913") ||
		hasText(tt, "Link for your phone") {
		t.Fatalf("request: %q", tt.Texts())
	}
	// No Allow until the person says which phone the browser is on.
	if slices.Contains(tt.Texts(), "Allow") || !hasText(tt, "Which phone is this browser on?") {
		t.Fatalf("Allow before the phone is chosen: %q", tt.Texts())
	}
	if err := tt.Click("Choose…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// The menu lists Choose…, the phones and Another device: the first
	// phone.
	tt.Key(0, mg.KeyDown)
	tt.Key(0, mg.KeyEnter)
	ta.waitFor(t, "Allow", func() bool { tt.Frame(); return slices.Contains(tt.Texts(), "Allow") })
	if len(fa.allowed) != 0 {
		t.Fatal("allowed by itself")
	}
	// Kept by the phone's id, not its label in the menu.
	if got := ta.webMirror.approvalPick["req1"]; got != ta.devices[0].Info.ID {
		t.Fatalf("kept %q", got)
	}
	if hasText(tt, "This browser says it runs on") {
		t.Fatal("a warning for a phone connected to Pinky")
	}
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the phone", func() bool {
		tt.Frame()
		return hasText(tt, "iPhone · Safari · alex@example.com · on Pixel 9") && !hasText(tt, "A browser asks to answer approvals")
	})
	if len(fa.allowed) != 1 || fa.allowed[0] != "req1" || fa.devices[0] != ta.devices[0].Info.ID || ta.devices[0].Info.Name != "Pixel 9" {
		t.Fatalf("allowed %v on %v", fa.allowed, fa.devices)
	}
	if err := tt.Click("Remove"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "no phone", func() bool { tt.Frame(); return hasText(tt, "None yet.") })
}

// A browser that says it runs on an Android phone, said to be on another
// device while an Android phone is connected: Settings warns that it may
// be that phone, whose agent could then answer.
func TestWebApprovalsOtherDevice(t *testing.T) {
	ta, fa := webApprovalsApp(t)
	ta.settings.WebApprovals = true
	fa.requestFrom("Android · Chrome")
	tt := mg.NewTester(ta.view, 1280, 2600)
	ta.waitFor(t, "the request", func() bool { tt.Frame(); return hasText(tt, "Which phone is this browser on?") })
	if err := tt.Click("Choose…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// Choose…, Pixel 9, Another device.
	tt.Key(0, mg.KeyDown)
	tt.Key(0, mg.KeyDown)
	tt.Key(0, mg.KeyEnter)
	ta.waitFor(t, "the warning", func() bool {
		tt.Frame()
		return hasText(tt, "This browser says it runs on an Android phone, and an Android phone is connected to Pinky.")
	})
	if got := ta.webMirror.approvalPick["req1"]; got != webApprovalsOther {
		t.Fatalf("kept %q", got)
	}
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the phone", func() bool { tt.Frame(); return len(fa.Phones()) == 1 })
	if fa.devices[0] != "" {
		t.Fatalf("allowed on %q", fa.devices[0])
	}
}

// TestWebApprovalsScreenshots writes Settings › Phones › Web mirror with a
// request to answer approvals, in each language and theme, to the folder
// PINKY_SCREENSHOTS names.
func TestWebApprovalsScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	for _, lang := range testLanguages() {
		for _, dark := range []bool{false, true} {
			ta, fa := webApprovalsApp(t)
			ta.settings.WebApprovals = true
			fa.request()
			tt := mg.NewTester(ta.view, 1280, 2000)
			inLanguage(t, lang, tt)
			tt.SetScale(2)
			tt.SetDark(dark)
			ta.waitFor(t, "the request", func() bool { tt.Frame(); return len(tt.Texts()) > 0 && ta.webMirror.tn.URL != "" })
			tt.Frame()
			tt.Frame()
			theme := map[bool]string{false: "light", true: "dark"}[dark]
			f, err := os.Create(filepath.Join(dir, "web-approvals-settings-"+theme+"-"+langShort(lang)+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
