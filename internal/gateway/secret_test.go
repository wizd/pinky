package gateway

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	_ "image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// The password and code these tests type: distinct enough that any copy
// of them, anywhere, is a leak.
const (
	leakPassword = "Hunter2-zq9!"
	leakCode     = "804217"
)

// TestStaleRefTypeIsMasked types a password by the ref of a password field
// after the app left the page that had it, then again after that failure,
// by the ref of a button, and in a batch whose first step leaves the page:
// each type fails, and its step keeps neither the password nor the
// intent's copy of it. A type by a
// ref Pinky cannot place is not known to go into an ordinary field, so its
// text is masked the same way.
func TestStaleRefTypeIsMasked(t *testing.T) {
	ctx := context.Background()
	g, hd := newHookGateway(t, func(o *Options) {
		o.Policy = loadPolicy(t, t.TempDir())
		o.DataDir = t.TempDir()
	})
	var away atomic.Bool
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		if away.Load() {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleButton, Label: "Back", Source: screen.SourceTree,
				Bounds: screen.Rect{X: 100, Y: 1700, W: 800, H: 120}})
			return
		}
		sc.Elements = append(sc.Elements,
			screen.Element{Role: screen.RoleButton, Label: "Forgot password?", Source: screen.SourceTree, Bounds: screen.Rect{X: 100, Y: 1100, W: 800, H: 120}},
			screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
				States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}})
	})
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	call := Call{Intent: "Sign in with " + leakPassword}
	pw := ref(t, s.Observe(ctx, ObserveRequest{}), "Password")
	// The app left the sign-in page before the type came, and the agent
	// tries again after the first failure.
	away.Store(true)
	for i := 0; i < 2; i++ {
		if res := s.Act(ctx, call, Action{Tool: "type", Ref: pw, Text: leakPassword}); res.Error != ErrNotFound {
			t.Fatalf("typed by a gone ref: %+v", res)
		}
	}
	// A ref that is not a text field: nothing is typed, and the text may
	// be a password all the same.
	back := ref(t, s.Observe(ctx, ObserveRequest{}), "Back")
	if res := s.Act(ctx, call, Action{Tool: "type", Ref: back, Text: leakPassword}); res.Error != ErrInvalidArgument {
		t.Fatalf("typed into a button: %+v", res)
	}
	// A batch whose first step leaves the sign-in page.
	away.Store(false)
	obs := s.Observe(ctx, ObserveRequest{})
	pw = ref(t, obs, "Password")
	hd.OnInput(func(demo.Input) { away.Store(true) })
	res := s.Batch(ctx, BatchRequest{Call: call, Steps: []Action{
		{Tool: "tap", Ref: ref(t, obs, "Forgot password?")}, {Tool: "type", Ref: pw, Text: leakPassword},
	}})
	if len(res.Steps) != 2 || res.Steps[1].Error != ErrNotFound {
		t.Fatalf("batch: %+v", res)
	}

	var failed int
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_type" && strings.Contains(st.Text+st.Args, leakPassword) {
			t.Errorf("live step %s: %q · %s", st.Tool, st.Text, st.Args)
		}
		if st.Text == "Failed: Typed “"+secretMask+"”" {
			failed++
		}
	}
	if failed != 4 {
		t.Errorf("%d failed steps with the mask:\n%+v", failed, taskSteps(g, s))
	}
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	for _, task := range tasks {
		steps, err := g.store.Steps(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range steps {
			if st.Tool == "phone_type" && strings.Contains(strings.Join([]string{st.Text, st.Args, st.Intent}, "\n"), leakPassword) {
				t.Errorf("history step %s: %q · intent %q", st.Tool, st.Text, st.Intent)
			}
		}
	}
}

// TestTypedSecretsAreNeverKept types a password and a code on a phone
// (refused, then asked and allowed) and on a virtual phone (allowed), by
// ref and without, taps an app's hidden passcode keypad, and fails a type
// by a stale ref; then looks for the text everywhere Pinky keeps or shows
// it: the live steps, the cards and their details, history's steps (words,
// arguments, intents, screen text) and approvals, an export of it, and
// the log. Only the agent's own results may hold it. An ordinary field
// keeps what was typed into it.
func TestTypedSecretsAreNeverKept(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	var typedCode atomic.Bool
	g, hd := newHookGateway(t, func(o *Options) {
		o.Policy = loadPolicy(t, t.TempDir())
		o.DataDir = t.TempDir()
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		for i, l := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"} {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
				States: []string{screen.StateNotInScreenshot}, Bounds: screen.Rect{X: 100 + i%3*250, Y: 1500 + i/3*100, W: 250, H: 100}})
		}
		code := screen.Element{Role: screen.RoleInput, Label: "Verification code", Source: screen.SourceTree,
			Bounds: screen.Rect{X: 100, Y: 1150, W: 800, H: 120}}
		if typedCode.Load() && typed(hd, leakCode) {
			// The field shows the code once it is typed, as Android's do.
			code.Label, code.Content = "Verification code, "+leakCode, leakCode
		}
		sc.Elements = append(sc.Elements,
			screen.Element{Role: screen.RoleInput, Label: "Email", Source: screen.SourceTree, Bounds: screen.Rect{X: 100, Y: 1000, W: 800, H: 120}},
			code,
			screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
				States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}})
	})
	pol := g.Policy()
	// With Hide codes off, nothing else masks the code in the field: the
	// masking here stands on its own.
	if err := pol.SetHideCodes(false); err != nil {
		t.Fatal(err)
	}
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	typeInto := func(label, text string) *Result {
		obs := s.Observe(ctx, ObserveRequest{})
		return s.Act(ctx, Call{Intent: "Sign in with " + text}, Action{Tool: "type", Ref: ref(t, obs, label), Text: text})
	}

	// Refused: the switch is off.
	if res := typeInto("Password", leakPassword); res.Status != StatusBlocked {
		t.Fatalf("typed with the switch off: %+v", res)
	}
	// Refused: an app's hidden passcode keypad.
	obs := s.Observe(ctx, ObserveRequest{})
	if res := tap(s, ref(t, obs, "8")); res.Status != StatusBlocked {
		t.Fatalf("tapped the hidden keypad: %+v", res)
	}
	// Failed: a ref that is gone, on a screen with a password field.
	s.Observe(ctx, ObserveRequest{})
	if res := s.Act(ctx, Call{}, Action{Tool: "type", Ref: 9999, Text: leakPassword}); !res.IsError() {
		t.Fatalf("typed by a stale ref: %+v", res)
	}

	// Asked: the card and its detail show neither the text nor the intent's
	// copy of it.
	if err := pol.SetPhoneCredentials(true); err != nil {
		t.Fatal(err)
	}
	answer(t, g, AllowOnce, func(n NeedView) {
		if strings.Contains(n.Title+n.Detail, leakPassword) || !strings.Contains(n.Detail, secretMask) {
			t.Errorf("the card shows the password: %q · %q", n.Title, n.Detail)
		}
	})
	if res := typeInto("Password", leakPassword); res.IsError() || res.Status == StatusBlocked {
		t.Fatalf("allowed typing: %+v", res)
	}
	answer(t, g, AllowOnce, nil)
	typedCode.Store(true) // once the code is typed (typed)
	res := typeInto("Verification code", leakCode)
	if res.IsError() || res.Status == StatusBlocked {
		t.Fatalf("allowed code: %+v", res)
	}
	// The agent's own result keeps the field as it reads: the agent gave
	// the code, and checks the field with it.
	if !strings.Contains(res.Screen.Render(), leakCode) {
		t.Errorf("the agent's result lost the field's text:\n%s", res.Screen.Render())
	}
	typedCode.Store(false)

	// An ordinary field keeps its text.
	if res := typeInto("Email", "sam@example.com"); res.IsError() {
		t.Fatalf("email: %+v", res)
	}

	// A virtual phone: let through, by ref and without.
	if err := pol.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	hd.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	if res := typeInto("Password", leakPassword); res.Status == StatusBlocked || res.Status == StatusPending {
		t.Fatalf("a virtual phone: %+v", res)
	}
	s.Observe(ctx, ObserveRequest{})
	if res := s.Act(ctx, Call{}, Action{Tool: "type", Text: leakPassword}); res.Status == StatusBlocked || res.Status == StatusPending {
		t.Fatalf("a virtual phone, without ref: %+v", res)
	}
	obs = s.Observe(ctx, ObserveRequest{})
	if res := tap(s, ref(t, obs, "8")); res.Status == StatusBlocked {
		t.Fatalf("a virtual phone's hidden keypad: %+v", res)
	}

	leaks := func(where, s string) {
		t.Helper()
		for _, secret := range []string{leakPassword, leakCode} {
			if strings.Contains(s, secret) {
				t.Errorf("%s holds %q:\n%s", where, secret, s)
			}
		}
	}

	// The live steps, as Activity and the phone's page show them (ui's
	// gwtext tests translate the masked words).
	steps := taskSteps(g, s)
	var masked, email, keys int
	for _, st := range steps {
		leaks("live step "+st.Tool, st.Text+"\n"+st.Args)
		if strings.Contains(st.Text, "Typed “"+secretMask+"”") {
			masked++
		}
		if strings.Contains(st.Text, "Typed “sam@example.com” into “Email”") {
			email++
		}
		if st.Tool == "phone_tap" && strings.Contains(st.Text, "“"+secretKey+"”") {
			keys++
			if !st.Target.Empty() || st.Point != (screen.Point{}) || strings.Contains(st.Args, `"ref"`) || strings.Contains(st.Args, `"x"`) {
				t.Errorf("a keypad tap says where it went: %+v", st)
			}
		}
		if st.Tool == "phone_tap" && strings.Contains(st.Text, "“8”") {
			t.Errorf("a keypad tap names its key: %+v", st)
		}
	}
	if masked < 6 || email != 1 || keys != 2 {
		t.Errorf("masked %d, email %d, keys %d:\n%+v", masked, email, keys, steps)
	}

	// History: steps, approvals, and an export.
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	for _, task := range tasks {
		stored, err := g.store.Steps(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range stored {
			leaks("history step "+st.Tool, strings.Join([]string{st.Text, st.Args, st.Intent, st.ScreenText}, "\n"))
		}
		aps, err := g.store.Approvals(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(aps) < 2 {
			t.Errorf("approvals %+v", aps)
		}
		for _, a := range aps {
			leaks("history approval", a.Title+"\n"+a.Note)
		}
	}
	dir := t.TempDir()
	if _, err := g.ExportHistory(ctx, nil, dir); err != nil {
		t.Fatal(err)
	}
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(path)
			leaks("export "+filepath.Base(path), string(b))
		}
		return nil
	})
	leaks("the log", logs.String())
}

// TestKeypadTapCardSaysNoKey taps a key of an app's hidden passcode keypad
// on a virtual phone, with every action asking: the card's title names no
// key and the card shows no place, and neither do the step and history.
func TestKeypadTapCardSaysNoKey(t *testing.T) {
	ctx := context.Background()
	g, hd := newHookGateway(t, func(o *Options) {
		o.Policy = loadPolicy(t, t.TempDir())
		o.DataDir = t.TempDir()
	})
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		for i, l := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "FaceID", "0", "Delete"} {
			sc.Elements = append(sc.Elements, screen.Element{Role: screen.RoleButton, Label: l, Source: screen.SourceTree,
				States: []string{screen.StateNotInScreenshot}, Bounds: screen.Rect{X: 100 + i%3*250, Y: 1500 + i/3*100, W: 250, H: 100}})
		}
	})
	pol := g.Policy()
	if err := pol.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	hd.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	if err := pol.SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	var card atomic.Value
	answer(t, g, AllowOnce, func(n NeedView) { card.Store(n) })
	obs := s.Observe(ctx, ObserveRequest{})
	if res := tap(s, ref(t, obs, "8")); res.IsError() || res.Status == StatusBlocked {
		t.Fatalf("asked keypad tap: %+v", res)
	}
	n, _ := card.Load().(NeedView)
	if n.Title != "Tap “"+secretKey+"”?" || !n.Target.Empty() || n.Point != (screen.Point{}) {
		t.Errorf("the card names or shows the key: %q, target %+v, point %+v", n.Title, n.Target, n.Point)
	}
	for _, st := range taskSteps(g, s) {
		if st.Tool == "phone_tap" && (strings.Contains(st.Text, "“8”") || !st.Target.Empty() || st.Point != (screen.Point{})) {
			t.Errorf("the step names or shows the key: %+v", st)
		}
	}
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	var approvals int
	for _, task := range tasks {
		aps, err := g.store.Approvals(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range aps {
			approvals++
			if strings.Contains(a.Title, "“8”") {
				t.Errorf("history approval: %q", a.Title)
			}
		}
	}
	if approvals != 1 {
		t.Errorf("%d approvals", approvals)
	}
}

// TestHideTyped: history's screen text shows a code or password field's
// content as the mask, and its screenshots cover such fields; other fields
// keep their text.
func TestHideTyped(t *testing.T) {
	s := &screen.Screen{Width: 100, Height: 200, Elements: []screen.Element{
		{Ref: 1, Role: screen.RoleInput, Label: "Verification code, " + leakCode, Content: leakCode, Bounds: screen.Rect{X: 10, Y: 50, W: 80, H: 20}},
		{Ref: 2, Role: screen.RoleInput, Label: "sam@example.com", Content: "sam@example.com", Bounds: screen.Rect{X: 10, Y: 100, W: 80, H: 20}},
		{Ref: 3, Role: screen.RoleInput, Label: "Password", Bounds: screen.Rect{X: 10, Y: 150, W: 80, H: 20}, States: []string{screen.StatePassword}},
		// A password field of Android that shows its text.
		{Ref: 4, Role: screen.RoleInput, Label: leakPassword, Content: leakPassword, Hint: "Password", Bounds: screen.Rect{X: 10, Y: 10, W: 80, H: 20}},
		// What OCR read there and did not match to the field, as it misread it;
		// with the name beside it; and elsewhere.
		{Role: screen.RoleText, Label: "Hunter2-zq8!", Source: screen.SourceOCR, Bounds: screen.Rect{X: 12, Y: 12, W: 60, H: 14}},
		{Role: screen.RoleText, Label: "Code " + leakCode, Source: screen.SourceOCR, Bounds: screen.Rect{X: 0, Y: 52, W: 18, H: 14}},
		{Role: screen.RoleText, Label: "Forgot it?", Source: screen.SourceOCR, Bounds: screen.Rect{X: 12, Y: 180, W: 60, H: 14}},
	}}
	got := body(hideTyped(s))
	if strings.Contains(got, leakPassword) || strings.Contains(got, "zq8") {
		t.Errorf("a shown password is in history's screen text:\n%s", got)
	}
	if !strings.Contains(got, "Forgot it?") {
		t.Errorf("hid OCR text outside the fields:\n%s", got)
	}
	if strings.Contains(got, leakCode) || !strings.Contains(got, `"Verification code, `+secretMask+`"`) || !strings.Contains(got, "sam@example.com") {
		t.Fatalf("history's screen text:\n%s", got)
	}
	if !strings.Contains(body(s), leakCode) {
		t.Fatal("hideTyped changed the screen it was given")
	}

	g, _ := newTestGateway(t, func(o *Options) { o.DataDir = t.TempDir(); o.NoScreenshots = false })
	white := image.NewRGBA(image.Rect(0, 0, 100, 200))
	draw.Draw(white, white.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	p := &bgShot{img: white, state: shotKept, reads: []*screen.Screen{s}}
	g.mu.Lock()
	id := g.writeShotLocked(p)
	g.mu.Unlock()
	g.Flush()
	if id == "" {
		t.Fatal("not kept")
	}
	path, err := g.store.MediaPath(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	gray := func(x, y int) bool {
		r, gg, b, _ := img.At(x, y).RGBA()
		return r>>8 < 200 && gg>>8 < 200 && b>>8 < 200
	}
	if !gray(50, 60) {
		t.Error("the code field is not covered")
	}
	if !gray(50, 20) {
		t.Error("the shown password is not covered")
	}
	if gray(50, 110) || gray(50, 160) {
		t.Error("covered a field with no secret text in it")
	}
}

// TestBatchIntentIsMasked runs batches whose intent names what a step
// types. Every step keeps and shows the intent without the password: the
// tap before the type and the one after it, a step's own intent, and a
// batch that reaches the password field only on its next screen, typing
// there without ref. An email typed by ref into an ordinary field stays in
// the intent.
func TestBatchIntentIsMasked(t *testing.T) {
	ctx := context.Background()
	g, hd := newHookGateway(t, func(o *Options) {
		o.Policy = loadPolicy(t, t.TempDir())
		o.DataDir = t.TempDir()
	})
	var nextScreen atomic.Int64 // taps after which the password page shows; 0 for the sign-in page
	taps := func() int64 {
		var n int64
		for _, in := range hd.Inputs() {
			if in.Kind == "tap" {
				n++
			}
		}
		return n
	}
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		if n := nextScreen.Load(); n > 0 {
			// The email page, then, after a tap on Next, the password page
			// with its field focused.
			if taps() < n {
				sc.Elements = append(sc.Elements,
					screen.Element{Role: screen.RoleButton, Label: "Next", Source: screen.SourceTree, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}})
				return
			}
			sc.Elements = append(sc.Elements,
				screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
					States: []string{screen.StatePassword, screen.StateFocused}, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}},
				screen.Element{Role: screen.RoleButton, Label: "Sign in", Source: screen.SourceTree, Bounds: screen.Rect{X: 100, Y: 1500, W: 800, H: 120}})
			return
		}
		sc.Elements = append(sc.Elements,
			screen.Element{Role: screen.RoleInput, Label: "Email", Source: screen.SourceTree, Bounds: screen.Rect{X: 100, Y: 1000, W: 800, H: 120}},
			screen.Element{Role: screen.RoleInput, Label: "Password", Source: screen.SourceTree,
				States: []string{screen.StatePassword}, Bounds: screen.Rect{X: 100, Y: 1300, W: 800, H: 120}})
	})
	if err := g.Policy().SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	hd.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	batch := func(intent string, steps ...Action) {
		t.Helper()
		if res := s.Batch(ctx, BatchRequest{Call: Call{Intent: intent}, Steps: steps}); res.IsError() || len(res.Steps) != len(steps) {
			t.Fatalf("batch %q: %+v", intent, res)
		}
	}
	const (
		withPassword = "Sign in with " + leakPassword
		masked       = "Sign in with " + secretMask
		mine         = "Sign in as sam@example.com"
	)
	obs := s.Observe(ctx, ObserveRequest{})
	pw, email := ref(t, obs, "Password"), ref(t, obs, "Email")
	batch(withPassword, Action{Tool: "tap", Ref: pw}, Action{Tool: "type", Ref: pw, Text: leakPassword},
		Action{Tool: "tap", Ref: email, Intent: "Done with " + leakPassword})
	batch(mine, Action{Tool: "type", Ref: email, Text: "sam@example.com"}, Action{Tool: "tap", Ref: pw})
	// The password field is on the next screen, which the agent has not read.
	nextScreen.Store(taps() + 1)
	obs = s.Observe(ctx, ObserveRequest{})
	batch(withPassword, Action{Tool: "tap", Ref: ref(t, obs, "Next")}, Action{Tool: "type", Text: leakPassword},
		Action{Tool: "key", Key: "enter"})

	want := []string{masked, masked, "Done with " + secretMask, mine, mine, masked, masked, masked}
	check := func(where string, intents []string) {
		t.Helper()
		if len(intents) != len(want) {
			t.Fatalf("%s: intents %q", where, intents)
		}
		for i := range want {
			if intents[i] != want[i] {
				t.Errorf("%s: step %d's intent %q, want %q", where, i+1, intents[i], want[i])
			}
		}
	}
	var live []string
	for _, st := range taskSteps(g, s) {
		if strings.Contains(st.Text+st.Args, leakPassword) {
			t.Errorf("live step %s: %q · %s", st.Tool, st.Text, st.Args)
		}
		if i := strings.Index(st.Args, `"intent":"`); i >= 0 {
			rest := st.Args[i+len(`"intent":"`):]
			live = append(live, rest[:strings.Index(rest, `"`)])
		}
	}
	check("live", live)
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	var stored []string
	for _, task := range tasks {
		steps, err := g.store.Steps(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range steps {
			if strings.Contains(strings.Join([]string{st.Text, st.Args, st.Intent, st.ScreenText}, "\n"), leakPassword) {
				t.Errorf("history step %s: intent %q, args %s", st.Tool, st.Intent, st.Args)
			}
			if st.Intent != "" {
				stored = append(stored, st.Intent)
			}
		}
	}
	check("history", stored)
}

// typed reports whether the phone was given text to type.
func typed(hd *hookDev, text string) bool {
	for _, in := range hd.Inputs() {
		if in.Kind == "type" && in.Text == text {
			return true
		}
	}
	return false
}

// TestTypedCodeFieldIsNamedMasked types a code on a virtual phone, then
// taps the field, which now names the code in its label ("Verification
// code, 804217", as iOS reads it), once without asking and once with every
// action asking: the steps, the card and history name the field with the
// mask, not the code.
func TestTypedCodeFieldIsNamedMasked(t *testing.T) {
	ctx := context.Background()
	g, hd := newHookGateway(t, func(o *Options) {
		o.Policy = loadPolicy(t, t.TempDir())
		o.DataDir = t.TempDir()
	})
	hd.setObserve(func(sc *screen.Screen) {
		if sc.App.ID != demo.Dinely {
			return
		}
		code := screen.Element{Role: screen.RoleInput, Label: "Verification code", Source: screen.SourceTree,
			Bounds: screen.Rect{X: 100, Y: 1150, W: 800, H: 120}}
		if typed(hd, leakCode) {
			code.Label, code.Content = "Verification code, "+leakCode, leakCode
		}
		sc.Elements = append(sc.Elements, code)
	})
	pol := g.Policy()
	if err := pol.SetHideCodes(false); err != nil {
		t.Fatal(err)
	}
	if err := pol.SetVirtualCredentials(true); err != nil {
		t.Fatal(err)
	}
	hd.SetVirtual(true)
	for deadline := time.Now().Add(3 * time.Second); !g.Devices()[0].Info.Virtual; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the phone did not become virtual")
		}
	}
	s, _ := connect(t, g, "Sign in to the test account")
	if res := s.Act(ctx, Call{}, Action{Tool: "launch", App: "Dinely"}); res.IsError() {
		t.Fatalf("launch: %+v", res)
	}
	obs := s.Observe(ctx, ObserveRequest{})
	if res := s.Act(ctx, Call{}, Action{Tool: "type", Ref: ref(t, obs, "Verification code"), Text: leakCode}); res.IsError() || res.Status == StatusBlocked {
		t.Fatalf("code: %+v", res)
	}
	field := "Verification code, " + secretMask
	obs = s.Observe(ctx, ObserveRequest{})
	// The agent quotes the field as it read it; its own result keeps it so.
	quoted := Call{Intent: "Tap Verification code, " + leakCode}
	res := s.Act(ctx, quoted, Action{Tool: "tap", Ref: ref(t, obs, "Verification code, "+leakCode)})
	if res.IsError() {
		t.Fatalf("tap: %+v", res)
	}
	if !strings.Contains(res.Screen.Render(), leakCode) {
		t.Errorf("the agent's result lost the field's text:\n%s", res.Screen.Render())
	}
	// With every action asking, the card names the field too.
	if err := pol.SetPreset(policy.PresetEvery); err != nil {
		t.Fatal(err)
	}
	var card atomic.Value
	answer(t, g, AllowOnce, func(n NeedView) { card.Store(n) })
	obs = s.Observe(ctx, ObserveRequest{})
	if res := s.Act(ctx, quoted, Action{Tool: "tap", Ref: ref(t, obs, "Verification code, "+leakCode)}); res.IsError() || res.Status == StatusBlocked {
		t.Fatalf("asked tap: %+v", res)
	}
	if n, _ := card.Load().(NeedView); n.Title != "Tap “"+field+"”?" || strings.Contains(n.Detail, leakCode) || !strings.Contains(n.Detail, "Tap Verification code, "+secretMask) {
		t.Errorf("the card: %q · %q", n.Title, n.Detail)
	}

	var taps int
	for _, st := range taskSteps(g, s) {
		if strings.Contains(st.Text+st.Args, leakCode) {
			t.Errorf("live step %s: %q", st.Tool, st.Text)
		}
		if st.Tool == "phone_tap" && strings.Contains(st.Text, "Tapped “"+field+"”") {
			taps++
		}
	}
	if taps != 2 {
		t.Errorf("%d taps name the field with the mask:\n%+v", taps, taskSteps(g, s))
	}
	s.Release(ctx, ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(ctx, HistoryQuery{})
	if err != nil || len(tasks) == 0 {
		t.Fatalf("history: %v %v", tasks, err)
	}
	for _, task := range tasks {
		stored, err := g.store.Steps(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range stored {
			if strings.Contains(strings.Join([]string{st.Text, st.Args, st.Intent, st.ScreenText}, "\n"), leakCode) {
				t.Errorf("history step %s: %q", st.Tool, st.Text)
			}
		}
		aps, err := g.store.Approvals(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(aps) != 1 {
			t.Errorf("approvals %+v", aps)
		}
		for _, a := range aps {
			if strings.Contains(a.Title+a.Note, leakCode) || !strings.Contains(a.Title, field) {
				t.Errorf("history approval: %q", a.Title)
			}
		}
	}
}
