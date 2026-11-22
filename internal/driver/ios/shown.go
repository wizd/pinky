package ios

import (
	"math"
)

// Elements the phone calls not visible that the picture shows.
//
// raxScreenElements leaves out what IsVisible says is not on screen. In a
// web view that scrolls, that attribute can lag behind the page: on an
// iPhone 14 Pro, in a mini-program's long list, the text of a row that had
// just scrolled in came with its frame on the screen, where a screenshot
// showed it, and IsVisible false (9 of 60 reads after a swipe, 26 texts;
// the same texts read as visible once the list stood still). An agent got
// rows without the one text it was after, in a read that was complete.
//
// The picture decides for those: an element that is not visible by the
// phone's word, but whose label OCR read close to its frame, is shown.
// Only its being shown comes from the picture; its label stays the tree's,
// since OCR misreads characters. A read without a picture changes nothing.

const (
	// shownMinLabel is the shortest label, in letters and digits, that
	// the picture can vouch for: two or three characters turn up by
	// chance near any frame.
	shownMinLabel = 4
	// shownAlike is the share of a label that must stand, in order and
	// unbroken, in an OCR line: OCR reads most of a label right and adds
	// its neighbours on the line ("唯一码：1032472281927").
	shownAlike = 0.7
	// shownNear is how far, in points, the line's middle may lie above or
	// below the frame's, and its box to the side of it. Frames of web
	// content were seen about 29 points off the picture on that page.
	shownNear = 50
)

// shownInPicture returns els with Visible set on those the phone calls not
// visible whose label lines shows near their frame, and how many. scale is
// pixels per point of the w x h screen the lines are of.
func shownInPicture(els []raxElement, scale float64, w, h int, lines []ocrLine) ([]raxElement, int) {
	if len(lines) == 0 || scale <= 0 {
		return els, 0
	}
	normed := make([]string, len(lines))
	for i, l := range lines {
		normed[i] = norm(l.Text)
	}
	near := int(math.Round(shownNear * scale))
	var out []raxElement
	n := 0
	for i, e := range els {
		if i == 0 || e.Visible || !e.IsElement || !e.HasFrame {
			continue
		}
		label := []rune(norm(e.Label))
		if len(label) < shownMinLabel {
			continue
		}
		r := pixelRect(e.Frame, scale, w, h)
		if r.Empty() {
			continue
		}
		for j, l := range lines {
			lr := ocrRect(l, w, h)
			if dy := lr.Center().Y - r.Center().Y; dy > near || dy < -near {
				continue
			}
			if lr.X > r.X+r.W+near || lr.X+lr.W < r.X-near {
				continue
			}
			if float64(commonRun(label, []rune(normed[j]))) >= shownAlike*float64(len(label)) {
				if out == nil {
					out = append([]raxElement(nil), els...)
				}
				out[i].Visible = true
				n++
				break
			}
		}
	}
	if out == nil {
		return els, 0
	}
	return out, n
}

// commonRun is the length of the longest run of characters a and b share.
func commonRun(a, b []rune) int {
	best := 0
	prev := make([]int, len(b)+1)
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				best = max(best, cur[j])
			}
		}
		prev = cur
	}
	return best
}
