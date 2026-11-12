package ui

import (
	"context"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"testing"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/ui/fakeapi"
)

// fakeVcorp is a platform that knows one code.
type fakeVcorp struct {
	mu     sync.Mutex
	st     VcorpStatus
	claims []string
	paired string
}

func (f *fakeVcorp) Status() VcorpStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeVcorp) Claim(_ context.Context, origin, code string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims = append(f.claims, origin+" "+code)
	if code != "ABC123" {
		return "", "", errors.New("unknown or expired code")
	}
	return "tester@example.com", "https://maker.vcorp.ai", nil
}

func (f *fakeVcorp) Pair(_ context.Context, origin, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paired = origin + " " + code
	f.st = VcorpStatus{Paired: true, Account: "tester@example.com", Origin: origin, Name: "Pinky · test", Connected: true}
	return nil
}

func (f *fakeVcorp) Unpair() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.st = VcorpStatus{}
	return nil
}

func vcorpApp(t *testing.T) (*testApp, *mg.Tester, *fakeVcorp) {
	t.Helper()
	ta := newTestApp(t, fakeapi.OnePhone)
	f := &fakeVcorp{}
	ta.opt.Vcorp = f
	ta.sync()
	ta.page, ta.set.sec = pageSettings, secAgents
	return ta, mg.NewTester(ta.view, 1280, 1400), f
}

// Settings › Agents pairs with a code: a wrong code says why, a right one
// shows its account first, and Pair pairs. Paired, it shows the account and
// the link, and Unpair asks once more.
func TestVcorpPairing(t *testing.T) {
	ta, tt, f := vcorpApp(t)
	tt.Frame()
	if !hasText(tt, "Pair with an account") {
		t.Fatalf("no pairing form: %q", tt.Texts())
	}
	if ta.vcorp.origin != DefaultVcorpOrigin {
		t.Errorf("address starts as %q", ta.vcorp.origin)
	}

	ta.vcorp.code = "WRONG"
	if err := tt.Click("Next"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the error", func() bool { tt.Frame(); return hasText(tt, "Couldn't check the code: unknown or expired code") })

	ta.vcorp.code = " ABC123 "
	if err := tt.Click("Next"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "the account", func() bool { tt.Frame(); return hasText(tt, "Pair with tester@example.com?") })
	if hasText(tt, "Couldn't check the code: unknown or expired code") {
		t.Error("the old error stays")
	}
	if err := tt.Click("Pair"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "paired", func() bool { tt.Frame(); return hasText(tt, "Cloud tasks can ask to use your phones.") })
	f.mu.Lock()
	if f.paired != "https://maker.vcorp.ai ABC123" {
		t.Errorf("paired with %q", f.paired)
	}
	if f.claims[1] != DefaultVcorpOrigin+" ABC123" {
		t.Errorf("claimed %q", f.claims)
	}
	f.mu.Unlock()
	if !hasText(tt, "tester@example.com") || !hasText(tt, "Cloud tasks can ask to use your phones.") {
		t.Errorf("paired: %q", tt.Texts())
	}

	if err := tt.Click("Unpair"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.Status().Paired == false {
		t.Fatal("unpaired without asking")
	}
	if err := tt.Click("Unpair now"); err != nil {
		t.Fatal(err)
	}
	ta.waitFor(t, "unpaired", func() bool { tt.Frame(); return hasText(tt, "Pair with an account") })
}

// The link's state while it is down says why.
func TestVcorpDown(t *testing.T) {
	ta, tt, f := vcorpApp(t)
	f.st = VcorpStatus{Paired: true, Account: "a@b.c", Origin: "https://maker.vcorp.ai", Err: "dial tcp: timeout"}
	tt.Frame()
	ta.waitFor(t, "the state", func() bool { tt.Frame(); return hasText(tt, "Not connected, trying again: dial tcp: timeout") })
	if !hasText(tt, "Connecting…") {
		t.Errorf("down: %q", tt.Texts())
	}
}

// TestVcorpScreenshots draws the form, the confirm step and the paired
// state, in light and dark, in English and Chinese, into
// $PINKY_SCREENSHOTS.
func TestVcorpScreenshots(t *testing.T) {
	dir := os.Getenv("PINKY_SCREENSHOTS")
	if dir == "" {
		t.Skip("set PINKY_SCREENSHOTS to a folder to write the screenshots")
	}
	ta, tt, f := vcorpApp(t)
	shoot := func(name string) {
		t.Helper()
		for _, dark := range []bool{false, true} {
			st := mg.NewTester(ta.view, 1280, 1400)
			st.SetScale(2)
			st.SetDark(dark)
			st.Frame()
			st.Frame()
			file := name + "-light.png"
			if dark {
				file = name + "-dark.png"
			}
			out, err := os.Create(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(out, st.Image()); err != nil {
				t.Fatal(err)
			}
			out.Close()
		}
	}
	for _, lang := range []string{"en", "zh-Hans"} {
		inLanguage(t, lang, tt)
		f.Unpair()
		ta.vcorp = vcorpState{}
		tt.Frame()
		shoot("vcorp-form-" + lang)
		ta.vcorp.account, ta.vcorp.normOrigin, ta.vcorp.code = "tester@example.com", "https://maker.vcorp.ai", "ABC123"
		shoot("vcorp-confirm-" + lang)
		ta.vcorp.account = ""
		f.Pair(context.Background(), "https://maker.vcorp.ai", "ABC123")
		ta.vcorpRefresh()
		ta.waitFor(t, "paired", func() bool { tt.Frame(); return ta.vcorp.st.Paired })
		shoot("vcorp-paired-" + lang)
	}
}
