package gateway

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/screen"
)

type readListDevice struct {
	*demo.Device
	app          screen.App
	pageApps     []screen.App
	listPaths    []string
	overlay      []screen.Element
	pages        [][]string
	page         int
	lockedAt     int
	delay        time.Duration
	observeDelay time.Duration
	noList       bool
	unsettled    bool
	lineStep     int
	reverse      bool
	ocrOnly      []screen.Element
	sawOCR       bool
	afterSwipe   func()
	// failAfter makes reads fail once that many swipes went out; waits
	// counts WaitSettled calls.
	failAfter, swipes, waits int
}

func (d *readListDevice) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	if d.failAfter > 0 && d.swipes >= d.failAfter {
		return nil, errors.New("screen read failed")
	}
	if d.observeDelay > 0 {
		select {
		case <-time.After(d.observeDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := &screen.Screen{
		Width: 1080, Height: 2400,
		Orientation: "portrait",
		CapturedAt:  time.Now(),
		Locked:      d.lockedAt > 0 && d.page >= d.lockedAt,
	}
	s.App = d.app
	if d.page < len(d.pageApps) {
		s.App = d.pageApps[d.page]
	}
	if s.App.ID == "" {
		s.App = screen.App{ID: demo.Dinely, Name: "Dinely"}
	}
	if s.Locked {
		return s, nil
	}
	if !d.noList {
		path := ""
		if d.page < len(d.listPaths) {
			path = d.listPaths[d.page]
		}
		if path == "" {
			path = "read-list/test"
		}
		s.Elements = append(s.Elements, screen.Element{
			Role: screen.RoleList, Path: path, Bounds: screen.Rect{X: 0, Y: 200, W: 1080, H: 1900},
			States: []string{screen.StateScrollable},
		})
	}
	for i, text := range d.pages[d.page] {
		step := d.lineStep
		if step == 0 {
			step = 200
		}
		shift := -d.page * 5
		if d.reverse {
			shift = d.page * 5
		}
		s.Elements = append(s.Elements, screen.Element{
			Role: screen.RoleText, Label: text,
			Bounds: screen.Rect{X: 120, Y: 300 + i*step + shift, W: 700, H: 80},
		})
	}
	s.Elements = append(s.Elements, d.overlay...)
	if opt.OCR && opt.OCRAll {
		d.sawOCR = true
		s.Elements = append(s.Elements, d.ocrOnly...)
	}
	return s, nil
}

func (d *readListDevice) Swipe(ctx context.Context, from, to screen.Point, _ time.Duration) error {
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.swipes++
	if (d.reverse && from.Y < to.Y) || (!d.reverse && from.Y > to.Y) {
		if d.page+1 < len(d.pages) {
			d.page++
		}
	} else if d.page > 0 {
		d.page--
	}
	if d.afterSwipe != nil {
		f := d.afterSwipe
		d.afterSwipe = nil
		f()
	}
	return nil
}

func (d *readListDevice) WaitSettled(context.Context, time.Duration) (bool, error) {
	d.waits++
	return !d.unsettled, nil
}

func (d *readListDevice) ReadsText() bool { return true }

func TestReadListMergesOverlappingScreens(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title", "shared one long title", "shared two long title"},
			{"shared one long title", "shared two long title", "fourth long title", "fifth long title"},
			{"fourth long title", "fifth long title", "sixth long title", "seventh long title"},
			{"sixth long title", "seventh long title", "eighth long title"},
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
	if listRef == 0 {
		t.Fatal("list ref not assigned")
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 4, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Status != StatusSettled || res.Scan == nil {
		t.Fatalf("read: %+v", res)
	}
	if res.Scan.Pages != 4 || res.Scan.Swipes != 3 || res.Scan.Overlap != 6 {
		t.Fatalf("scan summary: %+v", res.Scan)
	}
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	want := []string{
		"first long title", "second long title", "shared one long title", "shared two long title",
		"fourth long title", "fifth long title",
		"sixth long title", "seventh long title", "eighth long title",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	if res.Scan.Complete || !res.Scan.Truncated || res.Scan.Stop != "max_screens" || res.Scan.Continuation == "" {
		t.Fatalf("partial result flags: %+v", res.Scan)
	}
	// Every swipe is a step of its own in the task's history.
	tasks := g.Tasks()
	swipes := 0
	for _, st := range tasks[0].Steps {
		if st.Tool == "phone_swipe" {
			swipes++
		}
	}
	if len(tasks) != 1 || swipes != 3 {
		t.Fatalf("history: %+v", tasks)
	}
}

func TestReadListDoesNotTreatAPinnedControlAsANewGap(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		overlay: []screen.Element{{
			Role: screen.RoleText, Label: "Pinned feed controls",
			Bounds: screen.Rect{X: 0, Y: 210, W: 1080, H: 60},
		}},
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
			{"third long title", "fourth long title", "fifth long title"},
			{"fourth long title", "fifth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 4, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop != "max_screens" || res.Scan.Pages != 4 {
		t.Fatalf("pinned-control scan: %+v\n%s", res, scanText(res.Scan))
	}
	got, want := strings.Fields(scanText(res.Scan)), strings.Fields(
		"screen 1\nPinned feed controls\nfirst long title\nsecond long title\nthird long title\n"+
			"screen 2\nfourth long title\n"+
			"screen 3\nfifth long title")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scan text = %q, want %q", got, want)
	}
}

func TestReadListConfirmsARecurringVideoInsteadOfStoppingUnsettled(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device:    base,
		unsettled: true,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
			{"third long title", "fourth long title", "fifth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop != "max_screens" || res.Scan.Pages != 3 {
		t.Fatalf("recurring video scan: %+v\n%s", res, scanText(res.Scan))
	}
	if got, want := strings.Fields(scanText(res.Scan)), strings.Fields(
		"screen 1\nfirst long title\nsecond long title\nthird long title\n"+
			"screen 2\nfourth long title\n"+
			"screen 3\nfifth long title"); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan text = %q, want %q", got, want)
	}
}

func TestReadListSettlesAnExplicitEndAfterAccessibilityReordering(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"toolbar text", "second long title", "third long title", "fourth long title", "已经到底了"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 2, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.IsError() || res.Scan == nil || res.Scan.Stop != "end" || !res.Scan.Complete {
		t.Fatalf("reordered end scan: %+v\n%s", res, scanText(res.Scan))
	}
	if got, want := strings.Fields(scanText(res.Scan)), strings.Fields(
		"screen 1\nfirst long title\nsecond long title\nthird long title\n"+
			"screen 2\ntoolbar text\nfourth long title\n已经到底了"); !reflect.DeepEqual(got, want) {
		t.Fatalf("scan text = %q, want %q", got, want)
	}
}

func TestReadListStopsWhenLocked(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base, lockedAt: 1,
		pages: [][]string{
			{"first long title", "second long title"},
			{"second long title", "third long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Status != StatusLocked || res.Scan == nil || res.Scan.Stop != "locked" {
		t.Fatalf("locked scan: %+v", res)
	}
	if res.Scan.Lines != 2 {
		t.Fatalf("kept %d lines, want the first screen's 2", res.Scan.Lines)
	}
}

func TestReadListExplicitEnd(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title"},
			{"first long title", "second long title", "没有更多了"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 5, MaxMS: 5000, OCR: "off",
	})
	if res.Scan == nil || res.Scan.Stop != "end" || !res.Scan.Complete || res.Scan.Continuation != "" {
		t.Fatalf("end scan: %+v", res)
	}
	if !strings.Contains(scanText(res.Scan), "没有更多了") {
		t.Fatalf("end marker missing:\n%s", scanText(res.Scan))
	}
}

func TestReadListStopsAfterTwoUnchangedScreens(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title"},
			{"first long title", "second long title"},
			{"first long title", "second long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 5, MaxMS: 5000, OCR: "off",
	})
	if res.Scan == nil || res.Scan.Stop != "no_new_content" || res.Scan.Complete || !res.Scan.Truncated {
		t.Fatalf("unchanged scan: %+v", res)
	}
	if res.Scan.Lines != 2 {
		t.Fatalf("returned %d lines, want 2", res.Scan.Lines)
	}
}

func TestReadListContinuation(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
			{"third long title", "fourth long title", "fifth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	one := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if one.Scan == nil || one.Scan.Continuation == "" {
		t.Fatalf("first call has no continuation: %+v", one)
	}
	two := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off", Continuation: one.Scan.Continuation,
	})
	if two.Error != "" || two.Scan == nil {
		t.Fatalf("continuation: %+v", two)
	}
	if got := scanText(two.Scan); !strings.Contains(got, "fifth long title") {
		t.Fatalf("continued content missing: text=%q scan=%+v", got, two.Scan)
	}
}

func TestReadListAutoSelectsTheLargestList(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 2, MaxMS: 5000, OCR: "off"})
	if res.Error != "" || res.Scan == nil || res.Scan.Pages != 2 {
		t.Fatalf("auto-selected list: %+v", res)
	}
}

func TestReadListAssemblesOneHundredItems(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	pages := make([][]string, 0, 20)
	all := make([]string, 100)
	for i := range all {
		all[i] = fmt.Sprintf("product %03d", i)
	}
	for start := 0; start < len(all); start += 5 {
		end := min(start+7, len(all))
		pages = append(pages, append([]string(nil), all[start:end]...))
	}
	dev := &readListDevice{Device: base, pages: pages}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{
		MaxScreens: 20, MaxMS: 5000, OCR: "off", ScrollPercent: 60,
	})
	if res.Error != "" || res.Scan == nil {
		t.Fatalf("read: %+v", res)
	}
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	if !reflect.DeepEqual(got, all) {
		t.Fatalf("assembled %d items, want 100 in order: %+v", len(got), res.Scan)
	}
	if res.Scan.Stop != "max_screens" || res.Scan.Pages != 20 || res.Scan.Swipes != 19 {
		t.Fatalf("scan summary: %+v", res.Scan)
	}
}

func TestReadListRefusesBlockedApp(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		app:    screen.App{ID: "com.google.android.apps.walletnfcrel", Name: "Google Wallet"},
		pages:  [][]string{{"balance", "card"}},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 2, MaxMS: 5000})
	if res.Status != StatusBlocked || res.Rule == "" || res.Scan == nil || res.Scan.Stop != "blocked" {
		t.Fatalf("blocked scan: %+v", res)
	}
	if strings.Contains(scanText(res.Scan), "balance") {
		t.Fatalf("blocked content leaked:\n%s", scanText(res.Scan))
	}
}

func TestReadListKeepsAnAllowedBlockedAppOutOfHistory(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		app:    screen.App{ID: wallet, Name: "Google Wallet"},
		pages: [][]string{
			{"Visa •••• 4242", "Account balance", "Recent payments"},
			{"Account balance", "Recent payments", "Mastercard •••• 5555"},
		},
	}
	g := newDevGateway(t, base, dev, func(o *Options) { o.DataDir = t.TempDir() })
	s, _ := connect(t, g, "Read the wallet")
	l, acquireRes := s.acquire("")
	if acquireRes != nil {
		t.Fatalf("acquire: %+v", acquireRes)
	}
	g.mu.Lock()
	l.unblocked = append(l.unblocked, dev.app)
	g.mu.Unlock()
	s.done(l)

	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 2, MaxMS: 5000, OCR: "off"})
	if res.IsError() || res.Scan == nil || !strings.Contains(scanText(res.Scan), "Mastercard •••• 5555") {
		t.Fatalf("allowed blocked app read: %+v\n%s", res, scanText(res.Scan))
	}
	s.Release(context.Background(), ReleaseRequest{})
	g.Flush()
	tasks, err := g.History(context.Background(), HistoryQuery{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("history: %+v %v", tasks, err)
	}
	steps, err := g.store.Steps(context.Background(), tasks[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	swipes := 0
	for _, st := range steps {
		if st.Tool != "phone_swipe" {
			continue
		}
		swipes++
		if st.ScreenText != "" {
			t.Fatalf("allowed blocked app text reached history: %+v", st)
		}
	}
	if swipes != 1 {
		t.Fatalf("%d swipe steps, want 1: %+v", swipes, steps)
	}
}

func TestReadListStopsBeforeProtectedControl(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages:  [][]string{{"first long title", "second long title"}, {"second long title", "third long title"}},
		// A slider that runs along the list: a swipe that scrolls the
		// list may slide it.
		overlay: []screen.Element{{
			Role: screen.RoleOther, Label: "Slide to pay $12.80",
			Bounds: screen.Rect{X: 0, Y: 800, W: 1080, H: 1200},
		}},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if res.Status != StatusBlocked || res.Rule != "Payments" || res.Scan == nil || res.Scan.Stop != "blocked" {
		t.Fatalf("protected control: %+v", res)
	}
}

func TestReadListStopsWhenTheAppChanges(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pageApps: []screen.App{
			{ID: demo.Dinely, Name: "Dinely"},
			{ID: demo.Chatter, Name: "Chatter"},
		},
		pages: [][]string{{"first long title"}, {"message one", "message two"}},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Status != StatusSettled || res.Scan == nil || res.Scan.Stop != "app_changed" ||
		!res.Scan.Truncated || res.Scan.Continuation != "" {
		t.Fatalf("app change: %+v", res)
	}
}

func TestReadListStopsWhenTheListChanges(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device:    base,
		listPaths: []string{"list/a", "list/b"},
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Status != StatusSettled || res.Scan == nil || res.Scan.Stop != "list_changed" ||
		!res.Scan.Truncated || res.Scan.Continuation != "" {
		t.Fatalf("list change: %+v", res)
	}
}

func TestReadListStopsOnGapInsteadOfGuessing(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title"},
			{"unrelated third title", "unrelated fourth title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Scan == nil || res.Scan.Stop != "gap" || !res.Scan.Truncated || res.Scan.Continuation != "" {
		t.Fatalf("gap scan: %+v", res)
	}
	got := scanText(res.Scan)
	if strings.Contains(got, "unrelated") {
		t.Fatalf("content after a gap was accepted:\n%s", got)
	}
}

func TestReadListValidatesArguments(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g := newDevGateway(t, base, base)
	s, _ := connect(t, g, "Read a list")
	for _, req := range []ReadListRequest{
		{MaxScreens: 31},
		{MaxMS: 999},
		{Direction: "left"},
		{OCR: "pixels"},
		{ScrollPercent: 90},
		{Continuation: strings.Repeat("x", 8193)},
	} {
		res := s.ReadList(context.Background(), req)
		if res.Error != ErrInvalidArgument {
			t.Errorf("%+v accepted: %+v", req, res)
		}
	}
}

func TestReadListDoesNotLoopOnAnOversizedPage(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	long := strings.Repeat("x", 3000)
	dev := &readListDevice{
		Device: base, noList: true, lineStep: 60,
		pages: [][]string{append(make([]string, 26), "没有更多了")},
	}
	for i := 0; i < 26; i++ {
		dev.pages[0][i] = fmt.Sprintf("%02d ", i) + long
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{
		Area:       &screen.Rect{X: 0, Y: 0, W: 1080, H: 2400},
		MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if res.Scan == nil || res.Scan.Stop != "page_too_large" || res.Scan.Complete ||
		res.Scan.Continuation != "" || !res.Scan.Truncated {
		t.Fatalf("oversized page: %+v", res)
	}
}

func TestReadListContinuationMakesProgressAfterOutputLimit(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	long := strings.Repeat("x", 500)
	pages := make([][]string, 7)
	for i := range pages {
		var lines []string
		if i > 0 {
			lines = append(lines, fmt.Sprintf("shared one %02d ", i)+long, fmt.Sprintf("shared two %02d ", i)+long)
		}
		for j := 0; j < 4; j++ {
			lines = append(lines, fmt.Sprintf("page %02d line %d ", i+1, j)+long)
		}
		if i+1 < len(pages) {
			lines = append(lines, fmt.Sprintf("shared one %02d ", i+1)+long, fmt.Sprintf("shared two %02d ", i+1)+long)
		}
		pages[i] = lines
	}
	dev := &readListDevice{Device: base, noList: true, pages: pages}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a large list")
	area := &screen.Rect{X: 0, Y: 0, W: 1080, H: 2400}
	one := s.ReadList(context.Background(), ReadListRequest{
		Area: area, MaxScreens: 7, MaxMS: 5000, OCR: "off",
	})
	if one.Scan == nil || one.Scan.Stop != "output_limit" || one.Scan.Continuation == "" {
		t.Fatalf("first part: %+v", one)
	}
	two := s.ReadList(context.Background(), ReadListRequest{
		MaxScreens: 7, MaxMS: 5000, OCR: "off", Continuation: one.Scan.Continuation,
	})
	if two.Error != "" || two.Scan == nil || !strings.Contains(scanText(two.Scan), "page 07") {
		t.Fatalf("continued part: %+v\n%s", two, scanText(two.Scan))
	}
	if one.Scan.Continuation == two.Scan.Continuation {
		t.Fatal("continuation did not advance")
	}
}

func TestReadListDoesNotCallAClippedPageComplete(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	long := strings.Repeat("x", 2000)
	dev := &readListDevice{
		Device: base, noList: true,
		pages: [][]string{{long, "没有更多了"}},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{
		Area:       &screen.Rect{X: 0, Y: 0, W: 1080, H: 2400},
		MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if res.Scan == nil || res.Scan.Stop != "end" || res.Scan.Complete || !res.Scan.Truncated {
		t.Fatalf("clipped page: %+v", res)
	}
}

func TestReadListReturnsPreviousPagesAtDeadline(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base, delay: 1200 * time.Millisecond,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
			{"third long title", "fourth long title", "fifth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 10, MaxMS: 1000, OCR: "off",
	})
	if res.Error != "" || res.Status != StatusSettled || res.Scan == nil {
		t.Fatalf("deadline scan: %+v", res)
	}
	// The swipe that outlasted max_ms was not cancelled: it settled and
	// was read, and no further swipe started.
	if res.Scan.Stop != "deadline" || !res.Scan.Truncated || res.Scan.Swipes != 1 || res.Scan.Pages != 2 ||
		res.Scan.Continuation == "" {
		t.Fatalf("deadline details: %+v", res.Scan)
	}
}

func TestReadListStartsNoSwipeAfterMaxMS(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base, observeDelay: 1200 * time.Millisecond,
		pages: [][]string{{"first long title", "second long title"}},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a list")
	res := s.ReadList(context.Background(), ReadListRequest{
		MaxScreens: 2, MaxMS: 1000, OCR: "off",
	})
	if res.Error != "" || res.Status != StatusSettled || res.Scan == nil || res.Scan.Stop != "deadline" ||
		!res.Scan.Truncated || res.Scan.Pages != 1 || res.Scan.Swipes != 0 || res.Scan.Continuation == "" {
		t.Fatalf("first read deadline: %+v", res)
	}
}

func TestReadListUsesExplicitScrollAreaWithoutListElement(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		noList: true,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a web page")
	area := &screen.Rect{X: 0, Y: 200, W: 1080, H: 1900}
	res := s.ReadList(context.Background(), ReadListRequest{
		Area: area, MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if res.Error != "" || res.Scan == nil || res.Scan.Pages != 2 {
		t.Fatalf("scroll area: %+v", res)
	}
	if got := scanText(res.Scan); !strings.Contains(got, "fourth long title") {
		t.Fatalf("scroll area content:\n%s", got)
	}
}

func TestReadListReadsBackwardsWithoutLoss(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base, reverse: true,
	}
	// The device starts at the bottom view, then pages move toward the top.
	dev.pages = [][]string{
		{"item g", "item h"},
		{"item e", "item f", "item g", "item h"},
		{"item c", "item d", "item e", "item f"},
		{"item a", "item b", "item c", "item d"},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read backwards")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, Direction: "down", MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Error != "" || res.Scan == nil || res.Scan.Continuation == "" {
		t.Fatalf("read backwards: %+v", res)
	}
	more := s.ReadList(context.Background(), ReadListRequest{
		MaxScreens: 2, MaxMS: 5000, OCR: "off", Continuation: res.Scan.Continuation,
	})
	if more.Error != "" || more.Scan == nil {
		t.Fatalf("continue backwards without direction: %+v", more)
	}
	got := scanText(more.Scan) + "\n" + scanText(res.Scan)
	want := []string{"item a", "item b", "item c", "item d", "item e", "item f", "item g", "item h"}
	at := -1
	for _, item := range want {
		next := strings.Index(got, item)
		if next <= at {
			t.Fatalf("%q missing or out of order: scan=%+v\n%s", item, res.Scan, got)
		}
		at = next
	}
	if res.Scan.Overlap == 0 {
		t.Fatalf("backward scan: %+v", res.Scan)
	}
}

func TestReadListChecksOCROnlyProtectedSwipeTarget(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages:  [][]string{{"first long title", "second long title"}, {"second long title", "third long title"}},
		overlay: []screen.Element{{
			Role:   screen.RoleOther,
			Bounds: screen.Rect{X: 0, Y: 800, W: 1080, H: 1200},
		}},
		ocrOnly: []screen.Element{{
			Role: screen.RoleText, Label: "Slide to pay $12.80", Source: screen.SourceOCR,
			Bounds: screen.Rect{X: 0, Y: 850, W: 1080, H: 1100},
		}},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if !dev.sawOCR {
		t.Fatalf("the pixel-only swipe target was not checked: %+v", res)
	}
	if res.Status != StatusBlocked || res.Rule != "Payments" || res.Scan == nil || res.Scan.Stop != "blocked" {
		t.Fatalf("protected OCR target: %+v", res)
	}
}

func TestReadListChecksOCRWhereTheSwipeStartsAndSwipesBesideIt(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages:  [][]string{{"first long title", "second long title"}, {"second long title", "third long title"}},
		ocrOnly: []screen.Element{{
			Role: screen.RoleText, Label: "Slide to pay $12.80", Source: screen.SourceOCR,
			Bounds: screen.Rect{X: 850, Y: 1600, W: 180, H: 300},
		}},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 2, MaxMS: 5000, OCR: "off",
	})
	if !dev.sawOCR {
		t.Fatalf("the empty tree lane was not checked with OCR: %+v", res)
	}
	if res.Status != StatusSettled || res.Scan == nil || res.Scan.Swipes != 1 {
		t.Fatalf("a swipe clear of the pixel-only control: %+v", res)
	}
}

func TestReadListStopsWhenThePersonTakesOverAfterSwipe(t *testing.T) {
	base := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	dev := &readListDevice{
		Device: base,
		pages: [][]string{
			{"first long title", "second long title", "third long title"},
			{"second long title", "third long title", "fourth long title"},
		},
	}
	g := newDevGateway(t, base, dev)
	s, first := connect(t, g, "Read a list")
	var listRef int
	for _, e := range first.Screen.Elements {
		if e.Role == screen.RoleList {
			listRef = e.Ref
		}
	}
	var takeoverErr error
	dev.afterSwipe = func() { takeoverErr = g.TakeOver("pixel") }
	res := s.ReadList(context.Background(), ReadListRequest{
		Ref: listRef, MaxScreens: 3, MaxMS: 5000, OCR: "off",
	})
	if res.Status != StatusTookOver || res.Scan != nil {
		t.Fatalf("takeover: %+v, trigger error: %v", res, takeoverErr)
	}
}
