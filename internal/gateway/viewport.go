package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/screen"
)

// viewportLine is one readable line in a scrolling viewport. Bounds are
// kept for ordering only; the collection and merge code deliberately do
// not use position to decide whether two pages overlap, because the same
// content moves up or down as the list scrolls.
type viewportLine struct {
	Text   string
	Role   string
	Source screen.Source
	Bounds screen.Rect
}

// collectViewport returns the labelled content whose centres are inside
// area, in visual reading order. Repeated labels stay repeated: two
// products may legitimately have the same title. Hidden elements and
// labels that are empty after whitespace normalization are omitted.
func collectViewport(s *screen.Screen, area screen.Rect) []viewportLine {
	return collectViewportIn(s, area, 0)
}

// collectListViewport collects a list but omits the list container itself,
// whose label is a control title rather than scrolled content.
func collectListViewport(s *screen.Screen, list screen.Element) []viewportLine {
	return collectViewportIn(s, list.Bounds, list.Ref)
}

func collectViewportIn(s *screen.Screen, area screen.Rect, excludeRef int) []viewportLine {
	if s == nil || area.Empty() {
		return nil
	}
	lines := make([]viewportLine, 0, len(s.Elements))
	for _, e := range s.Elements {
		text := cleanViewportText(e.Label)
		if text == "" || (excludeRef != 0 && e.Ref == excludeRef) || e.Hidden ||
			e.Bounds.Empty() || !area.Contains(e.Bounds.Center()) {
			continue
		}
		line := viewportLine{Text: text, Role: e.Role, Source: e.Source, Bounds: e.Bounds}
		// A few platform trees repeat the same visible node at exactly the
		// same place. Keep equal labels elsewhere, but discard those exact
		// duplicates before they affect overlap detection.
		duplicate := false
		for i := len(lines) - 1; i >= 0 && len(lines)-i <= 4; i-- {
			prev := lines[i]
			if prev.Text == line.Text && nearlySameRect(prev.Bounds, line.Bounds) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			lines = append(lines, line)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i].Bounds.Center(), lines[j].Bounds.Center()
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		if a.X != b.X {
			return a.X < b.X
		}
		return lines[i].Text < lines[j].Text
	})
	return lines
}

// filterPinned removes lines that stayed at the same place between two
// viewports. Sticky bars and floating controls would otherwise interrupt the
// text sequence that identifies the actual overlap.
func filterPinned(previous, current []viewportLine) []viewportLine {
	out := make([]viewportLine, 0, len(current))
	for _, line := range current {
		pinned := false
		for _, old := range previous {
			if viewportKey(old) == viewportKey(line) && nearlySameRect(old.Bounds, line.Bounds) {
				pinned = true
				break
			}
		}
		if !pinned {
			out = append(out, line)
		}
	}
	return out
}

// pinnedLines returns previous lines that stayed at the same place and kept
// the same content in current. A caller can retain this small set across
// further captures even when a changing list container no longer exposes it.
func pinnedLines(previous, current []viewportLine) []viewportLine {
	out := make([]viewportLine, 0, len(previous))
	for _, line := range previous {
		for _, now := range current {
			if viewportKey(line) == viewportKey(now) && nearlySameRect(line.Bounds, now.Bounds) {
				out = append(out, line)
				break
			}
		}
	}
	return out
}

// appendViewport returns the content on current that follows the adjacent
// overlap with previous, and the number of overlapping lines. A contiguous
// match is required so equal text elsewhere in either page does not erase a
// legitimate repeated item. The caller compares the returned slice with an
// empty slice to decide whether scrolling exposed new content.
func appendViewport(previous, current []viewportLine) (fresh []viewportLine, overlap int) {
	if len(previous) == 0 || len(current) == 0 {
		return append([]viewportLine(nil), current...), 0
	}
	prevKeys, curKeys := viewportKeys(previous), viewportKeys(current)
	if sameKeys(prevKeys, curKeys) {
		return nil, len(current)
	}

	bestLen, bestAt := 0, 0
	for j := 0; j < len(current); j++ {
		for i := 0; i < len(previous); i++ {
			if prevKeys[i] != curKeys[j] {
				continue
			}
			n, chars := 0, 0
			for i+n < len(previous) && j+n < len(current) &&
				prevKeys[i+n] == curKeys[j+n] {
				chars += utf8.RuneCountInString(current[j+n].Text)
				n++
			}
			if !usefulOverlap(n, chars, curKeys[j], prevKeys, curKeys) {
				continue
			}
			if n > bestLen || (n == bestLen && chars > overlapChars(current[bestAt:bestAt+bestLen])) {
				bestLen, bestAt = n, j
			}
		}
	}
	if bestLen == 0 {
		return append([]viewportLine(nil), current...), 0
	}
	return append([]viewportLine(nil), current[bestAt+bestLen:]...), bestLen
}

// unseenPrefix reports whether a forward viewport match starts after one or
// more lines that were not present in the previous viewport. Such lines may
// be newly prepended content, so silently appending only what follows the
// overlap could lose them.
func unseenPrefix(previous, current []viewportLine) bool {
	if len(previous) == 0 || len(current) == 0 {
		return false
	}
	prevKeys, curKeys := viewportKeys(previous), viewportKeys(current)
	bestLen, bestAt := 0, 0
	for j := 0; j < len(current); j++ {
		for i := 0; i < len(previous); i++ {
			if prevKeys[i] != curKeys[j] {
				continue
			}
			n, chars := 0, 0
			for i+n < len(previous) && j+n < len(current) && prevKeys[i+n] == curKeys[j+n] {
				chars += utf8.RuneCountInString(current[j+n].Text)
				n++
			}
			if usefulOverlap(n, chars, curKeys[j], prevKeys, curKeys) && n > bestLen {
				bestLen, bestAt = n, j
			}
		}
	}
	if bestLen == 0 || bestAt == 0 {
		return false
	}
	counts := map[string]int{}
	for _, key := range prevKeys {
		counts[key]++
	}
	for i, key := range curKeys[:bestAt] {
		if counts[key] == 0 {
			// Accessibility trees often shorten a row's label when part of it
			// scrolls near an edge. A long fragment already contained in a
			// previous line is not newly prepended content.
			if !longFragmentOfPrevious(current[i], previous) {
				return true
			}
			continue
		}
		counts[key]--
	}
	return false
}

func longFragmentOfPrevious(line viewportLine, previous []viewportLine) bool {
	text := viewportKeyText(line)
	if utf8.RuneCountInString(text) < 24 {
		return false
	}
	for _, old := range previous {
		oldText := viewportKeyText(old)
		if len(text) < len(oldText) && strings.Contains(oldText, text) {
			return true
		}
	}
	return false
}

// newViewportContent returns current content that is not already represented
// in previous. It is for a final viewport with an explicit end marker, where
// accessibility grouping can reorder an overlapping row. Equal text is
// subtracted by multiplicity, so a legitimate repeated item is kept when
// current contains one more occurrence than previous.
func newViewportContent(previous, current []viewportLine) []viewportLine {
	counts := map[string]int{}
	for _, line := range previous {
		counts[viewportKey(line)]++
	}
	out := make([]viewportLine, 0, len(current))
	for _, line := range current {
		key := viewportKey(line)
		if counts[key] > 0 {
			counts[key]--
			continue
		}
		if longFragmentOfPrevious(line, previous) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// appendViewportBackward is appendViewport for a list read towards its top.
// The current viewport shows content above previous, so the useful overlap is
// a prefix of previous matched by a suffix of current. The newly exposed
// content is the part of current before that suffix.
func appendViewportBackward(previous, current []viewportLine) (fresh []viewportLine, overlap int) {
	if len(previous) == 0 || len(current) == 0 {
		return append([]viewportLine(nil), current...), 0
	}
	prevKeys, curKeys := viewportKeys(previous), viewportKeys(current)
	if sameKeys(prevKeys, curKeys) {
		return nil, len(current)
	}
	bestLen, bestAt := 0, len(current)
	for i := 0; i < len(previous); i++ {
		for j := len(current) - 1; j >= 0; j-- {
			if prevKeys[i] != curKeys[j] {
				continue
			}
			n, chars := 0, 0
			for i+n < len(previous) && j+n < len(current) &&
				prevKeys[i+n] == curKeys[j+n] {
				chars += utf8.RuneCountInString(current[j+n].Text)
				n++
			}
			if j+n != len(current) || !usefulOverlap(n, chars, curKeys[j], prevKeys, curKeys) {
				continue
			}
			if n > bestLen || (n == bestLen && chars > overlapChars(current[bestAt:bestAt+bestLen])) {
				bestLen, bestAt = n, j
			}
		}
	}
	if bestLen == 0 {
		return append([]viewportLine(nil), current...), 0
	}
	return append([]viewportLine(nil), current[:bestAt]...), bestLen
}

// viewportTexts returns the line labels for compact output.
func viewportTexts(lines []viewportLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Text)
	}
	return out
}

// viewportKeys returns the normalized comparison key of each line.
func viewportKeys(lines []viewportLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, viewportKey(line))
	}
	return out
}

// endMarker reports whether the last of lines, the list's last row in
// reading order, is a common explicit end-of-list line. Only the last row
// counts: "No more results" in the middle of a screen, or at the end the
// read moves away from, does not end the list. It is conservative on
// purpose: missing an end is harmless, claiming one the app did not show
// is not.
func endMarker(lines []viewportLine) bool {
	if len(lines) == 0 {
		return false
	}
	text := strings.TrimSpace(strings.ToLower(lines[len(lines)-1].Text))
	text = strings.TrimSpace(strings.Trim(text, "-–—_.·"))
	switch text {
	case "没有更多了", "没有更多", "到底了", "已经到底了", "已经到底",
		"没有更多内容了", "暂无更多", "已显示全部",
		"no more results", "no more items", "no more content", "end of list",
		"you've reached the end", "you reached the end":
		return true
	}
	return false
}

func viewportKey(line viewportLine) string {
	sum := sha256.Sum256([]byte(viewportKeyText(line)))
	return hex.EncodeToString(sum[:8])
}

func viewportKeyText(line viewportLine) string {
	return strings.ToLower(cleanViewportText(line.Text))
}

func cleanViewportText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func sameViewport(a, b []viewportLine) bool {
	return sameKeys(viewportKeys(a), viewportKeys(b))
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func usefulOverlap(lines, chars int, key string, previous, current []string) bool {
	if lines >= 2 {
		return true
	}
	return lines == 1 && chars >= 24 && countKey(previous, key) == 1 && countKey(current, key) == 1
}

func countKey(keys []string, key string) int {
	n := 0
	for _, candidate := range keys {
		if candidate == key {
			n++
		}
	}
	return n
}

func overlapChars(lines []viewportLine) int {
	n := 0
	for _, line := range lines {
		n += utf8.RuneCountInString(line.Text)
	}
	return n
}

func nearlySameRect(a, b screen.Rect) bool {
	const tolerance = 4
	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	return abs(a.X-b.X) <= tolerance && abs(a.Y-b.Y) <= tolerance &&
		abs(a.W-b.W) <= tolerance && abs(a.H-b.H) <= tolerance
}
