package gateway

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/DIYgod/pinky/internal/screen"
)

func TestCollectViewportOrdersAndFilters(t *testing.T) {
	s := &screen.Screen{
		Width: 1000, Height: 2000,
		Elements: []screen.Element{
			{Role: screen.RoleText, Label: "lower", Bounds: screen.Rect{X: 10, Y: 700, W: 200, H: 50}},
			{Role: screen.RoleText, Label: "outside", Bounds: screen.Rect{X: 10, Y: 1900, W: 200, H: 50}},
			{Role: screen.RoleText, Label: "  upper\n title ", Bounds: screen.Rect{X: 10, Y: 300, W: 200, H: 50}},
			{Role: screen.RoleText, Label: "safe\x00\x1b\u202e text", Bounds: screen.Rect{X: 10, Y: 360, W: 200, H: 50}},
			{Role: screen.RoleText, Label: "upper title", Bounds: screen.Rect{X: 12, Y: 302, W: 196, H: 46}},
			{Role: screen.RoleText, Label: "", Bounds: screen.Rect{X: 10, Y: 100, W: 200, H: 50}},
			{Role: screen.RoleText, Label: "hidden", Bounds: screen.Rect{X: 10, Y: 500, W: 200, H: 50}, Hidden: true},
			{Role: screen.RoleText, Label: "right", Bounds: screen.Rect{X: 500, Y: 300, W: 200, H: 50}},
		},
	}
	got := viewportTexts(collectViewport(s, screen.Rect{X: 0, Y: 200, W: 1000, H: 800}))
	want := []string{"upper title", "right", "safe text", "lower"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestCollectListViewportOmitsContainerAndStaticLines(t *testing.T) {
	list := screen.Element{Ref: 1, Role: screen.RoleList, Label: "Feed", Bounds: screen.Rect{X: 0, Y: 100, W: 1000, H: 1000}}
	first := &screen.Screen{Elements: []screen.Element{
		list,
		{Role: screen.RoleText, Label: "Filters", Bounds: screen.Rect{X: 0, Y: 100, W: 1000, H: 80}},
		{Role: screen.RoleText, Label: "first item", Bounds: screen.Rect{X: 0, Y: 200, W: 1000, H: 80}},
	}}
	second := &screen.Screen{Elements: []screen.Element{
		list,
		{Role: screen.RoleText, Label: "Filters", Bounds: screen.Rect{X: 0, Y: 100, W: 1000, H: 80}},
		{Role: screen.RoleText, Label: "second item", Bounds: screen.Rect{X: 0, Y: 200, W: 1000, H: 80}},
	}}
	a := collectListViewport(first, list)
	b := filterPinned(a, collectListViewport(second, list))
	if got, want := viewportTexts(a), []string{"Filters", "first item"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first = %#v, want %#v", got, want)
	}
	if got, want := viewportTexts(b), []string{"second item"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second = %#v, want %#v", got, want)
	}
}

func TestFilterPinnedDoesNotModifyItsInput(t *testing.T) {
	previous := lines("Pinned controls", "first item")
	current := lines("Pinned controls", "second item")
	before := append([]viewportLine(nil), current...)
	got := filterPinned(previous, current)
	if want := []string{"second item"}; !reflect.DeepEqual(viewportTexts(got), want) {
		t.Fatalf("filtered = %#v, want %#v", viewportTexts(got), want)
	}
	if !reflect.DeepEqual(current, before) {
		t.Fatalf("filterPinned modified its input: got %#v, want %#v", current, before)
	}
}

func TestPinnedLinesReturnsOnlyStationaryContent(t *testing.T) {
	previous := []viewportLine{
		{Text: "Pinned controls", Bounds: screen.Rect{X: 0, Y: 20, W: 100, H: 40}},
		{Text: "moving item", Bounds: screen.Rect{X: 0, Y: 200, W: 100, H: 40}},
	}
	current := []viewportLine{
		{Text: "Pinned controls", Bounds: screen.Rect{X: 0, Y: 20, W: 100, H: 40}},
		{Text: "moving item", Bounds: screen.Rect{X: 0, Y: 80, W: 100, H: 40}},
	}
	if got, want := viewportTexts(pinnedLines(previous, current)), []string{"Pinned controls"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pinned = %#v, want %#v", got, want)
	}
}

func TestAppendViewportMergesContiguousOverlap(t *testing.T) {
	previous := lines("alpha", "beta", "gamma", "delta")
	current := lines("gamma", "delta", "epsilon", "zeta")
	fresh, overlap := appendViewport(previous, current)
	if overlap != 2 {
		t.Fatalf("overlap = %d, want 2", overlap)
	}
	if got, want := viewportTexts(fresh), []string{"epsilon", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh = %#v, want %#v", got, want)
	}
}

func TestAppendViewportKeepsLegitimateRepeatedText(t *testing.T) {
	previous := lines("same headphones", "red cover", "same headphones", "blue cover")
	current := lines("same headphones", "blue cover", "new controller", "same headphones")
	fresh, overlap := appendViewport(previous, current)
	if overlap != 2 {
		t.Fatalf("overlap = %d, want 2", overlap)
	}
	if got, want := viewportTexts(fresh), []string{"new controller", "same headphones"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh = %#v, want %#v", got, want)
	}
}

func TestAppendViewportFallsBackWithoutUsefulAnchor(t *testing.T) {
	fresh, overlap := appendViewport(lines("old"), lines("new"))
	if overlap != 0 || !reflect.DeepEqual(viewportTexts(fresh), []string{"new"}) {
		t.Fatalf("fresh=%#v overlap=%d", viewportTexts(fresh), overlap)
	}
}

func TestAppendViewportDoesNotMergeOnOnlyOneShortBoundaryLine(t *testing.T) {
	fresh, overlap := appendViewport(lines("same"), lines("same", "new"))
	if overlap != 0 || !reflect.DeepEqual(viewportTexts(fresh), []string{"same", "new"}) {
		t.Fatalf("fresh=%#v overlap=%d", viewportTexts(fresh), overlap)
	}
}

func TestAppendViewportMergesOneUniqueLongBoundaryLine(t *testing.T) {
	shared := "a unique article title long enough to identify one row"
	fresh, overlap := appendViewport(lines(shared), lines(shared, "new article"))
	if overlap != 1 || !reflect.DeepEqual(viewportTexts(fresh), []string{"new article"}) {
		t.Fatalf("fresh=%#v overlap=%d", viewportTexts(fresh), overlap)
	}
}

func TestUnseenPrefixDetectsPrependedContent(t *testing.T) {
	previous := lines("b long title", "c long title", "d long title")
	if !unseenPrefix(previous, lines("new long title", "b long title", "c long title", "d long title", "e long title")) {
		t.Fatal("new line before the overlap was not detected")
	}
	if unseenPrefix(previous, lines("b long title", "c long title", "d long title", "e long title")) {
		t.Fatal("ordinary suffix-only growth was treated as prepended content")
	}
}

func TestUnseenPrefixAcceptsALongAccessibilityFragment(t *testing.T) {
	long := "a detailed article title that a platform may expose by itself when the rest of the row leaves the viewport"
	previous := lines(long+" author name time", "next long title", "third long title")
	current := lines(long, "next long title", "third long title", "new long title")
	if unseenPrefix(previous, current) {
		t.Fatal("a long fragment of the previous row was treated as new content")
	}
}

// rows lays texts out as a list's rows from y, h pixels each.
func rows(y, h int, texts ...string) []viewportLine {
	out := lines(texts...)
	for i := range out {
		out[i].Bounds = screen.Rect{X: 48, Y: y + i*h, W: 1224, H: h}
	}
	return out
}

// Above a strong shared run, lines that are not content a swipe could have
// skipped do not make a gap; the same lines above a weak run still do.
func TestUnseenAboveExcusesWhatIsNotSkippedContent(t *testing.T) {
	area := screen.Rect{X: 0, Y: 600, W: 1320, H: 2268}
	previous := rows(600, 150, "Wi-Fi, Home", "Bluetooth, On", "Cellular", "Battery", "General", "Accessibility", "Camera")
	ocr := viewportLine{Text: "1", Role: screen.RoleText, Source: screen.SourceOCR, Bounds: screen.Rect{X: 1120, Y: 790, W: 60, H: 70}}
	cut := viewportLine{Text: "Batte", Role: screen.RoleText, Source: screen.SourceTree, Bounds: screen.Rect{X: 48, Y: 760, W: 1224, H: 140}}
	title := viewportLine{Text: "Settings", Role: screen.RoleText, Source: screen.SourceTree, Bounds: screen.Rect{X: 48, Y: 760, W: 400, H: 140}}
	band := viewportLine{Text: "Personal Hotspot, Off", Role: screen.RoleButton, Source: screen.SourceTree, Bounds: screen.Rect{X: 48, Y: 600, W: 1224, H: 150}}
	seen := []viewportLine{{Text: "Settings", Role: screen.RoleText, Bounds: screen.Rect{X: 48, Y: 290, W: 600, H: 120}}}
	gc := gapContext{area: area, seen: seen}
	for name, line := range map[string]viewportLine{
		"an OCR line": ocr, "a cut-off row": cut, "a title seen outside the list": title, "a row in the top band": band,
	} {
		strong := append([]viewportLine{line}, rows(900, 150, "General", "Accessibility", "Camera")...)
		if got := unseenAbove(previous, strong, gc); len(got) != 0 {
			t.Errorf("%s above a strong run counted as unseen: %#v", name, viewportTexts(got))
		}
		weak := append([]viewportLine{line}, rows(900, 150, "Accessibility", "Camera")...)
		if got := unseenAbove(previous, weak, gc); len(got) != 1 {
			t.Errorf("%s above a weak run did not count: %#v", name, viewportTexts(got))
		}
	}
}

// A strong run does not hide rows that were not on the last screen below
// the top band: content added above the shared rows, or a swipe that
// skipped part of the list, is still a gap.
func TestUnseenAboveKeepsRealGapsAboveAStrongRun(t *testing.T) {
	area := screen.Rect{X: 0, Y: 600, W: 1320, H: 2268}
	previous := rows(600, 150, "Wi-Fi, Home", "Bluetooth, On", "Cellular", "Battery", "General", "Accessibility", "Camera")
	current := append(rows(600, 150, "Personal Hotspot, Off", "VPN, Not Connected", "Screen Time"),
		rows(1050, 150, "General", "Accessibility", "Camera", "Control Center")...)
	got := unseenAbove(previous, current, gapContext{area: area})
	if want := []string{"VPN, Not Connected", "Screen Time"}; !reflect.DeepEqual(viewportTexts(got), want) {
		t.Fatalf("unseen = %#v, want %#v", viewportTexts(got), want)
	}
	// Without the list's place, no line is in a top band.
	if got := unseenAbove(previous, current, gapContext{}); len(got) != 3 {
		t.Fatalf("unseen without an area = %#v", viewportTexts(got))
	}
}

// Elements on one row read left to right whichever of them lies a pixel
// higher, so the row reads the same on every screen.
func TestCollectViewportKeepsARowInOrder(t *testing.T) {
	for _, badgeY := range []int{2614, 2616} {
		s := &screen.Screen{Width: 1320, Height: 2868, Elements: []screen.Element{
			{Role: screen.RoleText, Label: "1", Source: screen.SourceOCR, Bounds: screen.Rect{X: 1116, Y: badgeY, W: 65, H: 73}},
			{Role: screen.RoleButton, Label: "General", Bounds: screen.Rect{X: 48, Y: 2572, W: 1224, H: 156}},
			{Role: screen.RoleButton, Label: "Accessibility", Bounds: screen.Rect{X: 48, Y: 2728, W: 1224, H: 140}},
		}}
		got := viewportTexts(collectViewport(s, screen.Rect{W: 1320, H: 2868}))
		if want := []string{"General", "1", "Accessibility"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("badge at %d: lines = %#v, want %#v", badgeY, got, want)
		}
	}
}

func TestSameViewportStopsOnlyForSameOrder(t *testing.T) {
	a := lines("one", "two")
	if !sameViewport(a, lines("ONE", "two")) {
		t.Fatal("case-insensitive equal viewport not detected")
	}
	if sameViewport(a, lines("two", "one")) {
		t.Fatal("reordered content counted as the same viewport")
	}
}

func TestEndMarkerIsExplicitAndLast(t *testing.T) {
	for _, text := range []string{"没有更多了", "No more results", "You've reached the end"} {
		if !endMarker(lines(text)) {
			t.Errorf("%q not recognised", text)
		}
	}
	if !endMarker(lines("—— 已经到底了 ——")) {
		t.Fatal("a decorated end marker was not recognised")
	}
	if endMarker(lines("Recommended for you")) {
		t.Fatal("an ordinary recommendation was treated as the end")
	}
	if endMarker(lines("No more worries")) {
		t.Fatal("an ordinary item was treated as the end")
	}
	if endMarker(lines("No more results", "first item")) {
		t.Fatal("a marker above the last row was treated as the end")
	}
	if !endMarker(lines("first item", "No more results")) {
		t.Fatal("a marker on the last row was not recognised")
	}
}

func TestAppendViewportKeepsOneHundredItemsInOrder(t *testing.T) {
	all := make([]string, 100)
	for i := range all {
		all[i] = fmt.Sprintf("item %03d", i)
	}
	var got []string
	var previous []viewportLine
	pages := 0
	for start := 0; start < len(all); start += 5 {
		end := min(start+7, len(all))
		if start > 0 && start+5 < len(all) {
			end = start + 7
		}
		current := lines(all[start:end]...)
		fresh, _ := appendViewport(previous, current)
		got = append(got, viewportTexts(fresh)...)
		previous = current
		pages++
	}
	if pages != 20 {
		t.Fatalf("pages = %d, want 20", pages)
	}
	if !reflect.DeepEqual(got, all) {
		t.Fatalf("assembled %d items, want 100 in order: %#v", len(got), got)
	}
}

func TestNewViewportContentPreservesMultiplicityAtTheEnd(t *testing.T) {
	previous := lines("same long product title", "first unique long title", "second unique long title")
	current := lines("same long product title", "second unique long title", "same long product title", "new long final title", "end of list")
	if got, want := viewportTexts(newViewportContent(previous, current)),
		[]string{"same long product title", "new long final title", "end of list"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("new content = %#v, want %#v", got, want)
	}
}

func lines(texts ...string) []viewportLine {
	out := make([]viewportLine, len(texts))
	for i, text := range texts {
		out[i] = viewportLine{Text: text, Role: screen.RoleText, Source: screen.SourceTree}
	}
	return out
}
