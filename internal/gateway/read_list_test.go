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
	// lineRole is the role of the list's lines, text by default; froms
	// are where the swipes started.
	lineRole   string
	froms      []screen.Point
	afterSwipe func()
	// failAfter makes reads fail once that many swipes went out; waits
	// counts WaitSettled calls.
	failAfter, swipes, waits int
	// ragged gives the lines differing widths, as a web page's text has;
	// clock makes reads with OCR find the status bar's clock, as an
	// iPhone's do; reads counts the reads other than those that check a
	// finger's spot with all OCR text (OCRAll).
	ragged, clock bool
	reads         int
	// still keeps each page's lines where the page before had its own:
	// a swipe that moved the list by whole rows.
	still bool
}

func (d *readListDevice) Observe(ctx context.Context, opt driver.ObserveOptions) (*screen.Screen, error) {
	if !opt.OCRAll {
		d.reads++
	}
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
		if d.still {
			shift = 0
		}
		role := d.lineRole
		if role == "" {
			role = screen.RoleText
		}
		w := 700
		if d.ragged {
			w = 400 + (i%3)*200
		}
		s.Elements = append(s.Elements, screen.Element{
			Role: role, Label: text,
			Bounds: screen.Rect{X: 120, Y: 300 + i*step + shift, W: w, H: 80},
		})
	}
	s.Elements = append(s.Elements, d.overlay...)
	if opt.OCR && d.clock {
		s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: "9:41", Source: screen.SourceOCR,
			Bounds: screen.Rect{X: 80, Y: 30, W: 110, H: 50}})
	}
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
	d.froms = append(d.froms, from)
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
	// The continuation reads the screen the first call ended on again, and
	// finds the next screen's overlap with it.
	if got := scanText(two.Scan); strings.Contains(got, "third long title") || strings.Contains(got, "fourth long title") {
		t.Fatalf("continuation repeats lines already read: text=%q scan=%+v", got, two.Scan)
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

// GitHub issue #148: iOS 27 lists Settings' rows but no list or scrollable
// element around them. With no ref or scroll_area, the read takes the
// column of rows as the area to scroll, swipes inside it and reads on.
func TestReadListFallsBackToAColumnOfRows(t *testing.T) {
	base := demo.NewDevice("iphone", "iPhone", driver.IOS)
	rows := []string{
		"Apple Account, iCloud", "Airplane Mode", "Wi-Fi, Home", "Bluetooth, On", "Cellular", "Personal Hotspot",
		"Battery", "VPN", "General", "Accessibility", "Action Button", "Camera",
		"Control Center", "Display & Brightness", "Home Screen & App Library", "Search", "Siri", "StandBy",
	}
	dev := &readListDevice{
		Device:   base,
		app:      screen.App{ID: "com.apple.Preferences", Name: "Ajustes"},
		noList:   true,
		lineRole: screen.RoleButton,
		lineStep: 140,
		pages:    [][]string{rows[0:8], rows[5:13], rows[10:18]},
	}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read Settings")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.Error != "" || res.Scan == nil || res.Scan.Pages != 3 || res.Scan.Swipes != 2 {
		t.Fatalf("read: %+v", res)
	}
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	if !reflect.DeepEqual(got, rows) {
		t.Fatalf("lines = %#v, want %#v", got, rows)
	}
	// The swipes run inside the rows' column, upwards.
	for _, p := range dev.froms {
		if p.X < 120 || p.X >= 820 || p.Y < 300 || p.Y > 300+8*140 {
			t.Errorf("a swipe started at %v, outside the rows", p)
		}
	}
	// A continuation reads on in the same column.
	if res.Scan.Continuation == "" {
		t.Fatalf("no continuation: %+v", res.Scan)
	}
}

// The fallbacks take only a column of like rows, or a page of text rows:
// a screen without a list whose elements are few, or a grid of buttons
// (icons), still has no list to read.
func TestReadListFindsNoColumnWithoutRows(t *testing.T) {
	grid := make([]screen.Element, 0, 24)
	for i := range 24 {
		grid = append(grid, screen.Element{Role: screen.RoleButton, Label: fmt.Sprintf("App %d", i),
			Bounds: screen.Rect{X: 60 + (i%4)*250, Y: 300 + (i/4)*300, W: 200, H: 220}})
	}
	for name, c := range map[string]struct {
		lines   []string
		overlay []screen.Element
	}{
		"few rows":       {lines: []string{"first long title", "second long title", "third long title"}},
		"a grid":         {overlay: grid},
		"nothing at all": {},
	} {
		t.Run(name, func(t *testing.T) {
			base := demo.NewDevice("iphone", "iPhone", driver.IOS)
			dev := &readListDevice{Device: base, noList: true, overlay: c.overlay, pages: [][]string{c.lines}}
			g := newDevGateway(t, base, dev)
			s, _ := connect(t, g, "Read")
			res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 2, MaxMS: 5000, OCR: "off"})
			if res.Error != ErrNotFound || dev.swipes != 0 {
				t.Fatalf("read without a list: %+v, %d swipes", res, dev.swipes)
			}
		})
	}
}

// webPage is a page of text as a web view on an iPhone through
// WebDriverAgent lists it: lines of differing widths, and no element for
// the view or its scrolling.
func webPage() [][]string {
	lines := webLines()
	return [][]string{lines[0:8], lines[4:12], lines[8:14]}
}

func webLines() []string {
	lines := make([]string, 14)
	for i := range lines {
		lines[i] = fmt.Sprintf("Bill item %d, paid at the corner shop", i+1)
	}
	return lines
}

// A web page with no list or scrollable element, and lines too unlike to
// make a column of rows (a bank's bill page in a field test answered
// not_found): the read scrolls the screen's content and says so.
func TestReadListReadsAWebPageWithoutAList(t *testing.T) {
	base := demo.NewDevice("iphone", "iPhone", driver.IOS)
	dev := &readListDevice{Device: base, noList: true, ragged: true, lineStep: 180, pages: webPage()}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read a bill")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 3, MaxMS: 5000, OCR: "off"})
	if res.IsError() || res.Scan == nil || res.Scan.Pages != 3 || res.Scan.Swipes != 2 {
		t.Fatalf("web page: %+v", res)
	}
	var got []string
	for _, page := range res.Scan.Sections {
		got = append(got, page.Lines...)
	}
	if want := webLines(); !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
	if !strings.Contains(res.Message, "scrolled the screen's content") {
		t.Fatalf("the message does not say where the read scrolled: %s", res.Message)
	}
	// The swipes run between the bars at the top and bottom eighths.
	for _, p := range dev.froms {
		if p.Y < 2400/8 || p.Y > 2400*7/8 {
			t.Errorf("a swipe started at %v, outside the screen's content", p)
		}
	}
}

// The home screen and other system UI are never read as a page, however
// much text they show.
func TestReadListLeavesTheHomeScreenAlone(t *testing.T) {
	base := demo.NewDevice("iphone", "iPhone", driver.IOS)
	dev := &readListDevice{Device: base, noList: true, ragged: true, lineStep: 180, pages: webPage(),
		app: screen.App{ID: "com.apple.springboard", Name: "Home Screen"}}
	g := newDevGateway(t, base, dev)
	s, _ := connect(t, g, "Read")
	res := s.ReadList(context.Background(), ReadListRequest{MaxScreens: 2, MaxMS: 5000, OCR: "off"})
	if res.Error != ErrNotFound || dev.swipes != 0 {
		t.Fatalf("home screen: %+v, %d swipes", res, dev.swipes)
	}
}

func TestContentArea(t *testing.T) {
	page := func(n, top int) *screen.Screen {
		s := &screen.Screen{Width: 1080, Height: 2400}
		for i := range n {
			s.Elements = append(s.Elements, screen.Element{Role: screen.RoleText, Label: fmt.Sprintf("line %d", i),
				Bounds: screen.Rect{X: 60, Y: top + i*200, W: 300 + (i%4)*150, H: 60}})
		}
		return s
	}
	if got, ok := contentArea(page(8, 400)); !ok || got != (screen.Rect{X: 0, Y: 300, W: 1080, H: 1800}) {
		t.Fatalf("a page of text: %v, %v", got, ok)
	}
	// The keyboard takes the bottom of the screen: the content ends above it.
	kb := page(8, 400)
	kb.Keyboard, kb.KeyboardFrame = true, screen.Rect{X: 0, Y: 1600, W: 1080, H: 800}
	if got, ok := contentArea(kb); !ok || got != (screen.Rect{X: 0, Y: 300, W: 1080, H: 1300}) {
		t.Fatalf("above the keyboard: %v, %v", got, ok)
	}
	// Five lines, or lines bunched at the top (a short form), are no page.
	if _, ok := contentArea(page(5, 400)); ok {
		t.Fatal("five lines read as a page")
	}
	bunched := page(8, 400)
	for i := range bunched.Elements {
		bunched.Elements[i].Bounds.Y = 400 + i*100
	}
	if _, ok := contentArea(bunched); ok {
		t.Fatal("lines over less than half the screen read as a page")
	}
	// Buttons are not text: a grid of them is no page.
	buttons := page(8, 400)
	for i := range buttons.Elements {
		buttons.Elements[i].Role = screen.RoleButton
	}
	if _, ok := contentArea(buttons); ok {
		t.Fatal("buttons read as a page")
	}
}

// rowColumn takes the box around the rows, a cell and the label of the
// same size inside it counting once.
func TestRowColumn(t *testing.T) {
	s := &screen.Screen{Width: 1320, Height: 2868}
	for i := range 10 {
		b := screen.Rect{X: 48, Y: 600 + i*150, W: 1224, H: 130}
		s.Elements = append(s.Elements,
			screen.Element{Role: screen.RoleButton, Label: fmt.Sprintf("Row %d", i), Bounds: b},
			screen.Element{Role: screen.RoleText, Label: fmt.Sprintf("Row %d", i), Bounds: screen.Rect{X: 50, Y: b.Y + 2, W: 1220, H: 126}},
			screen.Element{Role: screen.RoleImage, Bounds: screen.Rect{X: 60, Y: b.Y + 20, W: 90, H: 90}})
	}
	// The navigation bar's title and the search field are not rows.
	s.Elements = append(s.Elements,
		screen.Element{Role: screen.RoleText, Label: "Ajustes", Bounds: screen.Rect{X: 48, Y: 250, W: 400, H: 120}},
		screen.Element{Role: screen.RoleInput, Label: "Buscar", Bounds: screen.Rect{X: 48, Y: 420, W: 1224, H: 110}},
		screen.Element{Role: screen.RoleText, Label: "row read by OCR", Source: screen.SourceOCR, Bounds: screen.Rect{X: 48, Y: 2200, W: 1224, H: 130}})
	got, ok := rowColumn(s)
	want := screen.Rect{X: 48, Y: 600, W: 1224, H: 9*150 + 130}
	if !ok || got != want {
		t.Fatalf("rowColumn = %v, %v; want %v", got, ok, want)
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
