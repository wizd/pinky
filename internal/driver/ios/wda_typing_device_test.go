//go:build device

package ios

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// TestWDATypingProbe measures typing through WebDriverAgent in Settings'
// search field: key by key (/wda/keys) at the frequencies PINKY_FREQS lists
// (0 is the session's own, wdaTypingFrequency; 60 is XCTest's default),
// and pasting (/wda/setPasteboard, then Command-V
// through /wda/element/0/keyboardInput). It saves the pasteboard first and
// puts it back last, and logs only lengths and timings, never what the
// pasteboard held. PINKY_PASTEPROBE=1, and either PINKY_WDA_URL (a
// Simulator) or PINKY_DEVICE (a phone, opened through the helper):
//
//	PINKY_PASTEPROBE=1 PINKY_DEVICE=<udid> scripts/devlock.sh <udid> go test -tags device -count=1 -v -run TestWDATypingProbe ./internal/driver/ios/
func TestWDATypingProbe(t *testing.T) {
	if os.Getenv("PINKY_PASTEPROBE") == "" {
		t.Skip("PINKY_PASTEPROBE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var d *WDADevice
	if u := os.Getenv("PINKY_WDA_URL"); u != "" {
		var err error
		if d, err = OpenWDAURL(ctx, u, os.Getenv("PINKY_MJPEG_URL")); err != nil {
			t.Fatal(err)
		}
	} else {
		id := os.Getenv("PINKY_DEVICE")
		p := deviceProvider(t, id)
		if err := p.SetConnection(id, "wda"); err != nil {
			t.Fatal(err)
		}
		dev, err := p.Open(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var ok bool
		if d, ok = dev.(*WDADevice); !ok {
			dev.Close()
			t.Fatalf("opened %T, not the helper", dev)
		}
	}
	defer d.Close()
	c := d.c
	if st, err := c.status(ctx); err == nil {
		t.Logf("WDA %s, iOS %s", st.Build.Version, st.OS.Version)
	}

	// The pasteboard first: what reading it costs, and whether iOS asks.
	t0 := time.Now()
	var saved string
	var gerr error
	seen := probeAlerts(ctx, c, func() { saved, gerr = probeGetPB(ctx, c) })
	t.Logf("getPasteboard: %v, %d bytes, err %v, alerts %q", time.Since(t0).Round(time.Millisecond), len(saved), gerr, seen)
	t0 = time.Now()
	var gerr2 error
	seen = probeAlerts(ctx, c, func() { _, gerr2 = probeGetPB(ctx, c) })
	t.Logf("getPasteboard again: %v, err %v, alerts %q", time.Since(t0).Round(time.Millisecond), gerr2, seen)
	defer func() {
		if gerr != nil {
			return
		}
		r0 := time.Now()
		err := probeSetPB(context.WithoutCancel(ctx), c, saved)
		back, berr := probeGetPB(context.WithoutCancel(ctx), c)
		t.Logf("restored the pasteboard: %v, err %v; reads the same %v (err %v)", time.Since(r0).Round(time.Millisecond), err, back == saved, berr)
	}()

	if err := d.Key(ctx, driver.KeyHome); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := d.Launch(ctx, "com.apple.Preferences"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	s, err := d.Observe(ctx, driver.ObserveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var field screen.Element
	for _, e := range s.Elements {
		if e.Role == screen.RoleInput {
			field = e
			break
		}
	}
	if field.Bounds.Empty() {
		t.Fatal("no search field in Settings")
	}
	if err := d.Tap(ctx, field.Bounds.Center(), driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	defer func() {
		bctx := context.WithoutCancel(ctx)
		d.Type(bctx, "", driver.TypeOptions{})
		time.Sleep(400 * time.Millisecond)
		if s, err := d.Observe(bctx, driver.ObserveOptions{}); err == nil {
			if e, ok := closeSearchButton(s); ok {
				d.Tap(bctx, e.Bounds.Center(), driver.TapOptions{})
			}
		}
	}()

	texts := []string{
		"clock",
		"the quick brown fox jumps over the lazy dog 0123456789",
		"你好世界，蓝牙和无线局域网",
		"café ✓ 😀 naïve",
		"Meet me at 7:30 near the north gate, then we walk to 世纪公园 together & grab coffee ☕️ — ok? (reply by 8pm!)",
	}
	if os.Getenv("PINKY_LONG") != "" {
		texts = append(texts, strings.Repeat(texts[len(texts)-1]+" ", 3))
	}
	value := func() string {
		v, _ := probeActiveValue(ctx, c)
		return v
	}
	clear := func() {
		if err := c.clearFocused(ctx); err != nil {
			t.Fatalf("clear: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	keysAt := func(freq int) {
		if freq > 0 {
			sid, _ := c.session(ctx)
			if err := c.call(ctx, c.hc, http.MethodPost, "/session/"+sid+"/appium/settings", map[string]any{"settings": map[string]any{"maxTypingFrequency": freq}}, nil); err != nil {
				t.Fatalf("maxTypingFrequency %d: %v", freq, err)
			}
		}
		for _, text := range texts {
			clear()
			k0 := time.Now()
			err := c.keys(ctx, text)
			dt := time.Since(k0)
			time.Sleep(300 * time.Millisecond)
			v := value()
			t.Logf("keys (frequency %d): %d characters in %v, err %v, field same %v (%d characters)", freq, len([]rune(text)), dt.Round(time.Millisecond), err, v == text, len([]rune(v)))
		}
	}
	freqs := []int{0, 600}
	if f := os.Getenv("PINKY_FREQS"); f != "" {
		freqs = nil
		for _, x := range strings.Split(f, ",") {
			n, _ := strconv.Atoi(x)
			freqs = append(freqs, n)
		}
	}
	for _, f := range freqs {
		keysAt(f)
	}
	if os.Getenv("PINKY_NOPASTE") != "" {
		return
	}
	sid, _ := c.session(ctx)
	c.call(ctx, c.hc, http.MethodPost, "/session/"+sid+"/appium/settings", map[string]any{"settings": map[string]any{"maxTypingFrequency": wdaTypingFrequency}}, nil)

	// Paste.
	for _, text := range texts {
		clear()
		p0 := time.Now()
		if err := probeSetPB(ctx, c, text); err != nil {
			t.Fatalf("setPasteboard: %v", err)
		}
		set := time.Since(p0)
		k0 := time.Now()
		var err error
		seen := probeAlerts(ctx, c, func() {
			err = c.do(ctx, c.long, http.MethodPost, "/wda/element/0/keyboardInput", map[string]any{"keys": []any{map[string]any{"key": "v", "modifierFlags": 1 << 4}}}, nil)
		})
		cmdv := time.Since(k0)
		if len(seen) > 0 {
			t.Logf("alerts during Command-V: %q", seen)
		}
		if err != nil {
			t.Errorf("Command-V: %v", err)
			continue
		}
		var v string
		w0 := time.Now()
		for time.Since(w0) < 2*time.Second {
			if v = value(); v == text {
				break
			}
			time.Sleep(40 * time.Millisecond)
		}
		t.Logf("paste: %d characters, set %v, Command-V %v, field same %v after %v (%d characters)", len([]rune(text)), set.Round(time.Millisecond), cmdv.Round(time.Millisecond), v == text, time.Since(w0).Round(time.Millisecond), len([]rune(v)))
	}
}

// probeAlerts runs f while asking WDA every 250 ms whether an alert shows,
// and returns whether one did and if its text speaks of pasting.
func probeAlerts(ctx context.Context, c *wdaClient, f func()) []string {
	done := make(chan struct{})
	var seen []string
	go func() {
		defer close(done)
		f()
	}()
	for {
		select {
		case <-done:
			return seen
		case <-time.After(250 * time.Millisecond):
		}
		var text string
		if err := c.do(ctx, c.hc, http.MethodGet, "/alert/text", nil, &text); err == nil && text != "" {
			kind := "alert"
			if strings.Contains(strings.ToLower(text), "paste") || strings.Contains(text, "粘贴") {
				kind = "paste alert"
			}
			seen = append(seen, kind)
		}
	}
}

func probeGetPB(ctx context.Context, c *wdaClient) (string, error) {
	var b64 string
	if err := c.do(ctx, c.long, http.MethodPost, "/wda/getPasteboard", map[string]any{"contentType": "plaintext"}, &b64); err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	return string(b), err
}

func probeSetPB(ctx context.Context, c *wdaClient, s string) error {
	return c.do(ctx, c.hc, http.MethodPost, "/wda/setPasteboard", map[string]any{"contentType": "plaintext", "content": base64.StdEncoding.EncodeToString([]byte(s))}, nil)
}

func probeActiveValue(ctx context.Context, c *wdaClient) (string, error) {
	var el map[string]string
	if err := c.do(ctx, c.hc, http.MethodGet, "/element/active", nil, &el); err != nil {
		return "", err
	}
	id := el["ELEMENT"]
	if id == "" {
		id = el["element-6066-11e4-a52e-4f735466cecf"]
	}
	var v any
	if err := c.do(ctx, c.hc, http.MethodGet, "/element/"+id+"/attribute/value", nil, &v); err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

func closeSearchButton(s *screen.Screen) (screen.Element, bool) {
	for _, e := range s.Elements {
		if e.Label == "Cancel" || e.Label == "close" {
			return e, true
		}
	}
	return screen.Element{}, false
}
