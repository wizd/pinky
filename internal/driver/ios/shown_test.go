package ios

import "testing"

func TestShownInPicture(t *testing.T) {
	const w, h, scale = 1179, 2556, 3.0
	// An OCR line whose box, in pixels, is x, y, bw, bh.
	line := func(text string, x, y, bw, bh int) ocrLine {
		return ocrLine{Text: text, X: float64(x) / w, Y: float64(y) / h, W: float64(bw) / w, H: float64(bh) / h}
	}
	el := func(label string, x, y, fw, fh float64, visible bool) raxElement {
		return raxElement{Object: &axpObject{PID: 2}, Label: label, Frame: axpRect{X: x, Y: y, W: fw, H: fh},
			HasFrame: true, IsElement: true, Visible: visible}
	}
	app := raxElement{Object: &axpObject{PID: 1}, IsElement: true, Visible: true}
	// As read on an iPhone 14 Pro: the code's frame, in points, about 29
	// points under where the picture shows it, on one line with its name.
	code := el("1032472281927", 181, 380, 93, 20, false)
	lines := []ocrLine{
		line("唯一码：1032472281927", 400, 1060, 410, 50),
		line("实收配件：镜头前后盖 遮光罩", 400, 1140, 520, 50),
	}
	for _, c := range []struct {
		name  string
		e     raxElement
		lines []ocrLine
		want  bool
	}{
		{"the picture shows it", code, lines, true},
		{"OCR misread two characters", code, []ocrLine{line("唯一码：1032472281G2Z", 400, 1060, 410, 50)}, true},
		{"no picture", code, nil, false},
		{"the picture shows other text there", code, []ocrLine{line("唯一码：1110AIR3S59GH", 400, 1060, 410, 50)}, false},
		{"the text is elsewhere on the screen", code, []ocrLine{line("唯一码：1032472281927", 400, 2200, 410, 50)}, false},
		{"a label too short to vouch for", el("复制", 280, 380, 30, 20, false), []ocrLine{line("复制", 880, 1090, 60, 40)}, false},
		{"no frame", raxElement{Object: &axpObject{PID: 2}, Label: "1032472281927", IsElement: true}, lines, false},
	} {
		out, n := shownInPicture([]raxElement{app, c.e}, scale, w, h, c.lines)
		if got := out[1].Visible; got != c.want || (n == 1) != c.want {
			t.Errorf("%s: visible %v (%d set), want %v", c.name, got, n, c.want)
		}
	}
	// The caller's elements are left as they were, and one the phone calls
	// visible stays so.
	in := []raxElement{app, code, el("申请时间：2025月03月22日", 181, 300, 200, 20, true)}
	out, n := shownInPicture(in, scale, w, h, lines)
	if n != 1 || in[1].Visible || !out[1].Visible || !out[2].Visible {
		t.Errorf("set %d; the caller's %v, the result's %v and %v", n, in[1].Visible, out[1].Visible, out[2].Visible)
	}
}

func TestCommonRun(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1032472281927", "唯一码1032472281927", 13},
		{"1032472281927", "唯一码1032472281g2z", 10},
		{"abc", "xyz", 0},
		{"", "abc", 0},
	} {
		if got := commonRun([]rune(c.a), []rune(c.b)); got != c.want {
			t.Errorf("commonRun(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
