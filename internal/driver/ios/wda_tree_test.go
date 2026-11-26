package ios

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

// treeTimes sets the tree's waits for a test.
func treeTimes(t *testing.T, wait, slow time.Duration) {
	t.Helper()
	w, s, f := wdaTreeWait, wdaTreeSlow, wdaSlowFor
	wdaTreeWait, wdaTreeSlow, wdaSlowFor = wait, slow, time.Minute
	t.Cleanup(func() { wdaTreeWait, wdaTreeSlow, wdaSlowFor = w, s, f })
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 100, 200))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (f *fakeWDA) sourcesSeen() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sourcesIn
}

const ethena = `{"bundleId":"com.ethena.app","name":"Ethena","pid":8}`

// The field report of 2026-11-22: in a long React Native list, WDA's tree
// stopped coming, and every read failed after the call's whole budget. Now
// a read that waited wdaTreeWait answers with the app and OCR's text, the
// next reads of that app ask for less and send no new tree request while
// the phone still builds the last one, and the app's full tree comes back
// once its time as a slow app is over.
func TestWDAStalledTree(t *testing.T) {
	treeTimes(t, 300*time.Millisecond, 5*time.Second)
	hold := make(chan struct{})
	f := &fakeWDA{app: ethena, sourceHold: hold, png: pngBytes(t)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1,
		recognize: func([]byte) ([]ocrLine, error) {
			return []ocrLine{{Text: "Total balance", Confidence: 1, X: 0.1, Y: 0.2, W: 0.5, H: 0.05}}, nil
		}}
	released := false
	release := func() {
		if !released {
			close(hold)
			released = true
		}
	}
	defer d.trees.close()
	defer release()
	ctx := context.Background()

	ocrOnly := func(what string, s *screen.Screen, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if !s.TreeTooSlow || s.App.ID != "com.ethena.app" || len(s.Elements) != 1 ||
			s.Elements[0].Label != "Total balance" || s.Elements[0].Source != screen.SourceOCR {
			t.Fatalf("%s: %s", what, s.Render())
		}
		if !strings.Contains(s.Header(), "element tree too slow: OCR text only") {
			t.Fatalf("%s: header %q", what, s.Header())
		}
	}

	// The first read waits for the tree, then reads the picture's text.
	start := time.Now()
	s, err := d.Observe(ctx, driver.ObserveOptions{})
	ocrOnly("first read", s, err)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("first read took %v", took)
	}
	if l := d.trees.level("com.ethena.app", time.Now()); l != treeShallow {
		t.Fatalf("after one stalled tree: level %v", l)
	}

	// The next waits for the tree in flight rather than ask again, and
	// then the app gets no tree at all.
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	ocrOnly("second read", s, err)
	if n := f.sourcesSeen(); n != 1 {
		t.Fatalf("%d tree requests while the first was held", n)
	}
	if l := d.trees.level("com.ethena.app", time.Now()); l != treeNone {
		t.Fatalf("after a stalled shallow tree: level %v", l)
	}

	// Without a tree, the read asks the phone for none, and answers at
	// once.
	release()
	start = time.Now()
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	ocrOnly("third read", s, err)
	if took := time.Since(start); took > wdaTreeWait {
		t.Fatalf("a read without the tree took %v", took)
	}
	if n := f.sourcesSeen(); n != 1 {
		t.Fatalf("%d tree requests for an app read without one", n)
	}
	// Another app is read in full.
	if l := d.trees.level("com.apple.Preferences", time.Now()); l != treeFull {
		t.Fatalf("another app: level %v", l)
	}

	// Its time as a slow app over, the app's tree is read in full again.
	d.trees.mu.Lock()
	for app, s := range d.trees.slow {
		s.until = time.Now()
		d.trees.slow[app] = s
	}
	d.trees.mu.Unlock()
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s.TreeTooSlow || s.Partial || len(s.Elements) != 1 || s.Elements[0].Label != "Pay" {
		t.Fatalf("after the slow time: %v\n%s", err, s.Render())
	}
}

// An app whose full tree came, but slowly, gets a shallower one next: the
// read says it is partial. Other apps keep the full depth.
func TestWDASlowTreeReadsShallow(t *testing.T) {
	treeTimes(t, 5*time.Second, 100*time.Millisecond)
	f := &fakeWDA{app: ethena, sourceDelay: 150 * time.Millisecond}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1}
	defer d.trees.close()
	ctx := context.Background()
	depths := func() []int {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []int
		for _, b := range f.bodies["POST /appium/settings"] {
			var set struct {
				Settings map[string]any `json:"settings"`
			}
			json.Unmarshal([]byte(b), &set)
			if v, ok := set.Settings["snapshotMaxDepth"].(float64); ok {
				out = append(out, int(v))
			}
		}
		return out
	}

	s, err := d.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s.Partial || s.TreeTooSlow || len(s.Elements) != 1 {
		t.Fatalf("first read: %v\n%s", err, s.Render())
	}
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	if err != nil || !s.Partial || s.TreeTooSlow || len(s.Elements) != 1 {
		t.Fatalf("second read: %v\n%s", err, s.Render())
	}
	if got := depths(); len(got) != 2 || got[0] != wdaDepth || got[1] != wdaShallowDepth {
		t.Fatalf("snapshot depths %v", got)
	}
	// Another app: the full depth again.
	f.mu.Lock()
	f.app = `{"bundleId":"com.apple.Preferences","name":"Settings","pid":9}`
	f.mu.Unlock()
	s, err = d.Observe(ctx, driver.ObserveOptions{})
	if err != nil || s.Partial {
		t.Fatalf("another app: %v\n%s", err, s.Render())
	}
	if got := depths(); len(got) != 3 || got[2] != wdaDepth {
		t.Fatalf("snapshot depths %v", got)
	}
}

// A read joins the tree request in flight, unless an input went out after
// it began: that tree may show the screen from before the input.
func TestWDATreeJoinsOnlyAfterInput(t *testing.T) {
	hold := make(chan struct{})
	f := &fakeWDA{app: ethena, sourceHold: hold}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1}
	defer d.trees.close()
	ctx := context.Background()
	short := func() error {
		c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		_, err := d.tree(c, wdaDepth)
		return err
	}
	if err := short(); err == nil {
		t.Fatal("a held tree came")
	}
	// No input since: the next read takes the tree in flight.
	got := make(chan error, 1)
	go func() { _, err := d.tree(ctx, wdaDepth); got <- err }()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	f.sourceHold = nil
	f.mu.Unlock()
	close(hold)
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	if n := f.sourcesSeen(); n != 1 {
		t.Fatalf("%d tree requests, want the one in flight", n)
	}

	// A tap after the tree began: the read asks again once it ended.
	hold = make(chan struct{})
	f.mu.Lock()
	f.sourceHold = hold
	f.mu.Unlock()
	if err := short(); err == nil {
		t.Fatal("a held tree came")
	}
	if err := d.Tap(ctx, screen.Point{X: 10, Y: 10}, driver.TapOptions{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := d.tree(ctx, wdaDepth); got <- err }()
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	f.sourceHold = nil
	f.mu.Unlock()
	close(hold)
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	if n := f.sourcesSeen(); n != 3 {
		t.Fatalf("%d tree requests, want a new one after the tap", n)
	}
}

// Back in an app read without its tree swipes from the edge at once,
// without asking for the tree that would find the back button.
func TestWDABackWithoutTree(t *testing.T) {
	f := &fakeWDA{app: ethena}
	srv := httptest.NewServer(f)
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1}
	defer d.trees.close()
	d.trees.sawApp("com.ethena.app")
	d.trees.slower("com.ethena.app", treeShallow, time.Now())
	if err := d.Key(context.Background(), driver.KeyBack); err != nil {
		t.Fatal(err)
	}
	if n := f.sourcesSeen(); n != 0 {
		t.Fatalf("%d tree requests", n)
	}
	f.mu.Lock()
	acts := f.bodies["POST /actions"]
	f.mu.Unlock()
	if len(acts) != 1 {
		t.Fatalf("actions %v", acts)
	}
}

// A read waits for the tree wdaTreeWait at most, and leaves wdaOCRReserve
// of its context for the picture, or half of what is left when that is
// short.
func TestTreeBy(t *testing.T) {
	now := time.Now()
	in := func(left time.Duration) time.Duration {
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(left))
		defer cancel()
		return treeBy(ctx, now).Sub(now)
	}
	if got := treeBy(context.Background(), now).Sub(now); got != wdaTreeWait {
		t.Errorf("no deadline: %v", got)
	}
	if got := in(time.Minute); got != wdaTreeWait {
		t.Errorf("a minute left: %v", got)
	}
	if got := in(12 * time.Second); got != 12*time.Second-wdaOCRReserve {
		t.Errorf("12 s left: %v", got)
	}
	if got := in(4 * time.Second); got != 2*time.Second {
		t.Errorf("4 s left: %v", got)
	}
}

// The field report of 2026-11-26 (an iPad on iPadOS 26 over Wi-Fi): while
// the screen stayed the same, the refs of Settings' elements changed from
// one read to the next and back ([97], [145], [97]). Refs are keyed by the
// elements' paths, which began with the app WDA named in a request of its
// own (/wda/activeAppInfo): while apps switch its answer may name another
// app than the tree's, and one that failed named none. The paths now begin
// with the tree's own app, so the same tree gives the same refs.
func TestWDAPathsFromTheTree(t *testing.T) {
	f := &fakeWDA{}
	answers := []string{
		`{"value":{"bundleId":"com.apple.Preferences","name":"Settings","pid":8}}`,
		"", // the request fails
		`{"value":{"bundleId":"com.example.game","name":"Game","pid":9}}`,
	}
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/wda/activeAppInfo" {
			f.ServeHTTP(w, r)
			return
		}
		a := answers[int(n.Add(1)-1)%len(answers)]
		if a == "" {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"value":{"error":"unknown error","message":"no app"}}`)
			return
		}
		fmt.Fprint(w, a)
	}))
	defer srv.Close()
	d := &WDADevice{c: newWDAURL(srv.URL, ""), width: 100, height: 200, scale: 1}
	defer d.trees.close()
	refs := screen.NewRefs()
	var pay []int
	for i := range answers {
		s, err := d.Observe(context.Background(), driver.ObserveOptions{})
		if err != nil {
			t.Fatalf("read %d: %v", i+1, err)
		}
		refs.Assign(s)
		for _, e := range s.Elements {
			if e.Label == "Pay" {
				pay = append(pay, e.Ref)
			}
		}
	}
	if len(pay) != len(answers) || pay[1] != pay[0] || pay[2] != pay[0] {
		t.Fatalf("the same tree read %d times: Pay's refs %v", len(answers), pay)
	}
}
