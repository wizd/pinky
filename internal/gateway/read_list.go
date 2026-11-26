package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

const (
	readListOutputBytes = 16 << 10
	readListCursorLife  = 15 * time.Minute
	readListMinArea     = 4 // at least a quarter of the screen
	// readListSwipeMS is how long each swipe takes: a deliberate drag
	// keeps the next screen overlapping this one, where a short fling can
	// carry several pages (browser inertia) and leave no text that proves
	// the two reads are adjacent.
	readListSwipeMS = 1000
	// readListKeep is how many lines of the last screen, and of the fixed
	// controls, a read keeps for the next screen and its continuation.
	readListKeep = 80
	// readListCursors is how many continuations a lease keeps.
	readListCursors = 8
)

// readListLanes are where across the list a swipe may run, in percent of
// its width from the left, tried in order (0 is the middle): the first
// the rules let through on the latest screen is the one sent.
var readListLanes = []int{0, 85, 15, 75, 25}

// readCursor is where a partial phone_read_list stopped. It stays in the
// gateway, on the lease, under the opaque id the continuation carries.
type readCursor struct {
	app       screen.App
	list      listID
	area      *screen.Rect
	direction string
	page      int
	previous  []viewportLine
	// raw is the whole screen the call ended on: the next call's first
	// read of it must lose every line, previous keeps only some.
	raw     []viewportLine
	pinned  []viewportLine
	expires time.Time
}

// listID is how a read finds its list again on the next screen: its path
// in the tree, which agents never see and sibling lists may share, and
// where it is.
type listID struct {
	path   string
	bounds screen.Rect
}

// ReadList implements phone_read_list: it reads one vertical list across
// several settled screens in a single call and returns one compact
// transcript with the overlap between screens removed. Every swipe is a
// phone_swipe through the action pipeline (act): it waits out any settle
// owed, is judged on the latest settled screen, settles afterwards and is
// recorded as a step. A swipe the rules would ask about ends the read
// instead of asking.
func (s *Session) ReadList(ctx context.Context, req ReadListRequest) *Result {
	if res := req.check(); res != nil {
		return res
	}
	l, res := s.acquire(req.Device)
	if res != nil {
		return res
	}
	defer s.done(l)
	g := s.g
	ctx, cancel := g.callCtx(ctx, l)
	defer cancel()
	// The agent sees none of the screens the read passes: its next result
	// shows what changed since the screen it saw last.
	g.mu.Lock()
	seen, mark := l.baseline, l.mark
	g.mu.Unlock()
	res = g.readList(ctx, l, req)
	g.mu.Lock()
	l.baseline, l.mark = seen, mark
	g.mu.Unlock()
	res = g.finish(l, res)
	if res.Status == StatusStopped || res.Status == StatusPaused || res.Status == StatusTookOver ||
		res.Error == ErrDevicePaused {
		// Stop, pause and takeover end the call with nothing more for
		// the agent, as phone_batch drops its finished steps' screens.
		res.Scan = nil
	}
	return res
}

func (req *ReadListRequest) check() *Result {
	req.Direction = strings.ToLower(strings.TrimSpace(req.Direction))
	if req.Direction != "" && req.Direction != "up" && req.Direction != "down" {
		return errResult(ErrInvalidArgument, "direction must be up or down.")
	}
	if req.Ref > 0 && req.Area != nil {
		return errResult(ErrInvalidArgument, "pass ref or scroll_area, not both.")
	}
	if req.Area != nil && (req.Area.W < 100 || req.Area.H < 200) {
		return errResult(ErrInvalidArgument, "scroll_area must be at least 100 by 200 pixels.")
	}
	if req.MaxScreens == 0 {
		req.MaxScreens = 10
	}
	if req.MaxScreens < 1 || req.MaxScreens > MaxReadScreens {
		return errResult(ErrInvalidArgument, "max_screens must be between 1 and %d.", MaxReadScreens)
	}
	if req.MaxMS == 0 {
		req.MaxMS = 30000
	}
	if req.MaxMS < 1000 || req.MaxMS > int(MaxReadTime/time.Millisecond) {
		return errResult(ErrInvalidArgument, "max_ms must be between 1000 and %d.", MaxReadTime.Milliseconds())
	}
	if req.ScrollPercent == 0 {
		req.ScrollPercent = 60
	}
	if req.ScrollPercent < 30 || req.ScrollPercent > 80 {
		return errResult(ErrInvalidArgument, "scroll_percent must be between 30 and 80.")
	}
	switch req.OCR {
	case "":
		req.OCR = "auto"
	case "auto", "all", "off":
	default:
		return errResult(ErrInvalidArgument, "ocr must be auto, all or off.")
	}
	return nil
}

// readState is a read in progress.
type readState struct {
	req      ReadListRequest
	view     *ScanView
	app      screen.App
	list     screen.Element
	id       listID
	previous []viewportLine // the last screen's lines, fixed controls left out
	raw      []viewportLine // the same with them
	full     []viewportLine // every line of the last screen, for fixed controls
	pinned   []viewportLine
}

func (g *Gateway) readList(ctx context.Context, l *lease, req ReadListRequest) *Result {
	start := time.Now()
	deadline := start.Add(time.Duration(req.MaxMS) * time.Millisecond)
	// A read swipes: an agent that may only watch, a phone in the
	// person's hands, an open approval or a locked phone end it here.
	if res := g.gate(l, "", true); res != nil {
		return res
	}
	st := &readState{req: req, view: &ScanView{}}
	var cursor *readCursor
	if req.Continuation != "" {
		c, res := g.takeCursor(l, req)
		if res != nil {
			return res
		}
		cursor = c
		st.req.Direction = c.direction
		st.req.Area = c.area
		st.previous, st.raw, st.pinned = c.previous, c.raw, c.pinned
		st.view.StartScreen = c.page
	}
	if st.req.Direction == "" {
		st.req.Direction = "up"
	}
	req = st.req
	view := st.view

	first, slowest, res := g.firstListScreen(ctx, l, req, cursor)
	if res != nil {
		if cursor != nil && res.outOfTime {
			// The phone did not answer in time: nothing was read, and the
			// continuation holds for another try.
			view.Stop, view.Truncated = "deadline", true
			view.Continuation = g.putCursor(l, cursor)
			view.notes = append(view.notes, fmt.Sprintf(
				"The phone did not finish reading the screen within the call's %s, so nothing was read.", human(g.opt.CallBudget)))
			return readListResult(l, view)
		}
		view.Stop = readListStop(res)
		return withScan(res, view)
	}
	st.app = first.App
	list, found, res := startList(first, req, cursor, g.policy.IsSystem(first.App))
	if res != nil {
		return withScan(res, view)
	}
	if list.Role == "" {
		if cursor != nil {
			return withScan(errResult(ErrInvalidArgument,
				"The list the continuation belongs to is not on the screen anymore. Read it again without continuation."), view)
		}
		return withScan(errResult(ErrNotFound,
			"No list was found on this screen. Pass ref with the list or scrollable element to read, or scroll_area with the part of the screen that scrolls."), view)
	}
	if found.area != nil {
		// A column of rows, or the screen's content, is read as the
		// scroll area it covers.
		st.req.Area = found.area
		req = st.req
		if found.content {
			a := *found.area
			view.notes = append(view.notes, fmt.Sprintf(
				"No list or scrollable element was found, so the read scrolled the screen's content (scroll_area x %d, y %d, w %d, h %d). Pass ref or scroll_area to read another part.",
				a.X, a.Y, a.W, a.H))
		}
	}
	if cursor != nil && !sameApp(cursor.app, first.App) {
		return withScan(errResult(ErrInvalidArgument,
			"The continuation belongs to another app. Read the list again without continuation."), view)
	}
	st.list, st.id = list, listID{path: list.Path, bounds: list.Bounds}

	if !st.take(first, true) {
		return readListResult(l, view)
	}
	g.readProgress(req, start, view)
	if view.Stop == "" && st.endsHere() {
		view.Stop, view.Complete = "end", !view.Truncated
		return readListResult(l, view)
	}

	// longest is the longest swipe so far, with its settling and read, and
	// slowest the slowest read of the screen: the next swipe starts only
	// while the call has time for one like the longest and for the reads
	// it needs (swipeNeeds), so a slow phone's read is not cut short.
	var longest time.Duration
	noNew := 0
	// unread: the last swipe went out, but the screen it led to was not
	// read in time; the continuation reads it first.
	unread := false
loop:
	for view.Stop == "" && view.Pages < req.MaxScreens {
		need := g.swipeNeeds(l, longest, slowest)
		left, bounded := budgetLeft(ctx)
		blind := false
		if (bounded && left < max(need, minBatchStep)) || time.Until(deadline) < need {
			// No time for a swipe and the read after it. On a phone whose
			// reads take a good part of the call, the swipe still goes out
			// while there is time for it, and the next call reads its
			// screen: else a continuation whose first read takes most of
			// the call would never get further.
			if !g.slowRead(slowest) || (bounded && left < minBatchStep) || time.Until(deadline) < minBatchStep {
				view.Stop, view.Truncated = "deadline", true
				break
			}
			blind = true
		}
		a := Action{Tool: "swipe", Direction: req.Direction, DurationMS: readListSwipeMS,
			list: &listSwipe{area: req.Area, travel: req.ScrollPercent}}
		if req.Area == nil {
			a.Ref = st.list.Ref
		}
		a.list.lane = g.listLane(ctx, l, a, first)
		// Each swipe starts from the read that ended the one before, or
		// for the first, from the read of the first screen, as a
		// phone_batch step does: on a slow phone another read of the same
		// screen costs as much as the swipe and its read together.
		c := Call{Device: req.Device, Intent: req.Intent, Progress: req.Progress, reuse: true, noAsk: true, unread: blind}
		began := time.Now()
		r := g.act(ctx, l, c, a)
		if !blind {
			longest = max(longest, time.Since(began))
		}
		switch {
		case r.unread:
			view.Swipes++
			unread = true
			view.Stop, view.Truncated = "deadline", true
			break loop
		case r.outOfTime && r.IsError():
			// The call ran out of time before the swipe went out.
			view.Stop, view.Truncated = "deadline", true
			break loop
		case r.outOfTime:
			// It went out, and the phone answered too slowly to read the
			// screen after it: the continuation reads that screen, and
			// tells by the overlap whether the swipe skipped anything.
			view.Swipes++
			unread = true
			view.Stop, view.Truncated = "deadline", true
			break loop
		}
		if r.IsError() || r.Status != StatusSettled {
			// Not sent (refused, an approval open, the person took
			// over), or it may have been and its result is unknown.
			view.Stop = readListStop(r)
			return withScan(r, view)
		}
		view.Swipes++
		next := r.Screen
		if r.Rule != "" {
			// A blocked app came to the front: its screen is hidden.
			view.Stop = "blocked"
			return withScan(&Result{Status: StatusBlocked, Rule: r.Rule,
				Message: "A blocked app came to the front while reading the list, so the read stopped there."}, view)
		}
		if next, res = g.listScreenAfter(ctx, l, req.OCR, next); res != nil {
			if res.outOfTime {
				unread = true
				view.Stop, view.Truncated = "deadline", true
				break
			}
			view.Stop = readListStop(res)
			return withScan(res, view)
		}
		if next.Locked {
			view.Stop = "locked"
			return withScan(statusResult(StatusLocked,
				"The phone locked while reading the list. Call phone_handoff so the person can unlock it."), view)
		}
		if appKnown(st.app) && appKnown(next.App) && !sameApp(st.app, next.App) {
			view.Stop, view.Truncated = "app_changed", true
			break
		}
		nextList, ok := st.find(next)
		if !ok {
			view.Stop, view.Truncated = "list_changed", true
			break
		}
		// The pipeline read the screen after the swipe with the OCR it adds
		// where the tree misses text, which on an iPhone is any text OCR
		// finds and no element shows. The read's own reads take OCR only
		// where the list's tree says too little: the same here, so every
		// screen of the read is read alike (GitHub issue #148).
		retry := ocrOff
		switch req.OCR {
		case "all":
			retry = ocrAll
		case "auto":
			if next, ok = autoOCR(next, nextList); ok {
				retry = ocrAuto
			}
		}
		settled := r.Settle != "timeout"
		raw := collectListViewport(next, nextList)
		outOfTime := false
		for try := 0; !settled && try < 2; try++ {
			// A live video or animation can keep the pixels moving after
			// the text is final: the list's own text holding still is
			// enough. Not when the call has no time for another read: the
			// screen as read is kept, and the continuation goes on from it.
			if left, ok := budgetLeft(ctx); ok && left < slowest {
				outOfTime = true
				break
			}
			if !sleepCtx(ctx, g.opt.PollInterval, nil) {
				break
			}
			began := time.Now()
			again, res := g.readListScreenOCR(ctx, l, retry)
			slowest = max(slowest, time.Since(began))
			if res != nil && res.outOfTime {
				outOfTime = true
				break
			}
			if res != nil {
				view.Stop = readListStop(res)
				return withScan(res, view)
			}
			againList, ok := st.find(again)
			if again.Locked || !ok || (appKnown(again.App) && !sameApp(st.app, again.App)) {
				break
			}
			againRaw := collectListViewport(again, againList)
			settled = sameViewport(raw, againRaw)
			next, nextList, raw = again, againList, againRaw
		}
		st.list, st.id = nextList, listID{path: nextList.Path, bounds: nextList.Bounds}
		before := view.Lines
		if !st.take(next, false) {
			break
		}
		if view.Lines == before {
			noNew++
		} else {
			noNew = 0
		}
		first = next
		g.readProgress(req, start, view)
		switch {
		case !settled && outOfTime:
			view.Stop, view.Truncated = "deadline", true
		case !settled:
			view.Stop, view.Truncated = "unsettled", true
		case st.endsHere():
			view.Stop, view.Complete = "end", !view.Truncated
		case noNew >= 2:
			view.Stop, view.Truncated = "no_new_content", true
		}
	}
	if view.Stop == "" {
		view.Stop, view.Truncated = "max_screens", true
	}
	if readListCanContinue(view.Stop) {
		view.Truncated = true
		view.Continuation = g.keepCursor(l, st)
	}
	if view.Stop == "deadline" && g.slowRead(slowest) {
		view.notes = append(view.notes, g.slowNote(slowest, view, cursor != nil, unread))
	} else if unread {
		view.notes = append(view.notes, "The last swipe went out, but there was no time left to read the screen it led to: the continuation reads it first.")
	}
	return readListResult(l, view)
}

// slowRead reports whether reading the screen once took a tenth of the
// call's budget or more (4.5 s of 45 s): an iPhone through WebDriverAgent
// over Wi-Fi took 20–40 s in a field test.
func (g *Gateway) slowRead(d time.Duration) bool {
	return g.opt.CallBudget > 0 && d*10 >= g.opt.CallBudget
}

// swipeNeeds is how much time a read's next swipe needs: as long as its
// longest swipe so far, with settling and the read after it, and at least
// the slowest read of the call for each read it takes, so a phone whose
// reads take most of the call is not cut off in the middle of one. Besides
// the read after it, a swipe reads the screen it starts from when it
// cannot start from the last read (the swipe before did not settle), and
// reads it again with OCR, on phones that read text, when that read has
// none (ocrForVerdict).
func (g *Gateway) swipeNeeds(l *lease, longest, slowest time.Duration) time.Duration {
	g.mu.Lock()
	after := l.after
	if time.Since(l.afterAt) >= reuseAge {
		after = nil
	}
	g.mu.Unlock()
	reads := slowest
	if after == nil {
		reads += slowest
	}
	if !hasOCR(after) && g.readsText(l.dev) {
		reads += slowest
	}
	return max(longest, reads)
}

// slowNote says why a read on a slow phone stopped at its deadline, and
// what the continuation does.
func (g *Gateway) slowNote(slowest time.Duration, view *ScanView, continued, unread bool) string {
	took := fmt.Sprintf("Reading the screen took up to %s of the call's %s", human(slowest), human(g.opt.CallBudget))
	switch {
	case unread:
		return took + ", so the last swipe went out without time to read the screen it led to: the continuation reads it first, a screen or two per call."
	case continued && view.Pages == 0 && view.Swipes == 0:
		return took + ", which left no time for a swipe, so this call read nothing new. The phone answers too slowly for phone_read_list now: " +
			"pass continuation to try again, or scroll with phone_swipe and read with phone_observe."
	}
	return took + ", so the call fit fewer screens."
}

// firstListScreen reads the screen the read starts from, under the action
// lock and once any settle an earlier action owes is waited out, so it
// never shows the screen from before that action took effect. It returns
// how long the read took, and leaves the read for the first swipe to start
// from (Call.reuse).
//
// With ocr "auto" it reads once with the text OCR finds and the tree
// misses, as the pipeline reads the screen after each swipe, and drops
// that text again where the tree says enough about the list (autoOCR).
// Reading the tree first and again with OCR where it says too little cost
// two reads, which take 20–40 s each on an iPhone through WebDriverAgent
// over Wi-Fi.
func (g *Gateway) firstListScreen(ctx context.Context, l *lease, req ReadListRequest, cursor *readCursor) (*screen.Screen, time.Duration, *Result) {
	g.readyScreen(ctx, l)
	d := l.dev
	if !lockAct(ctx, d) {
		if ctx.Err() != nil {
			return nil, 0, errResult(ErrInternal, "The call was cancelled. Nothing was done.")
		}
		return nil, 0, busyOutOfTime(g.opt.CallBudget)
	}
	defer d.act.Unlock()
	if res := g.gate(l, "", true); res != nil {
		return nil, 0, res
	}
	g.settleOwed(ctx, d)
	mode := ocrAuto
	switch req.OCR {
	case "off":
		mode = ocrOff
	case "all":
		mode = ocrAll
	}
	began := time.Now()
	s, raw, res := g.readListRaw(ctx, l, mode)
	took := time.Since(began)
	if res != nil {
		return nil, took, res
	}
	if req.OCR == "auto" {
		if list, _, _ := startList(s, req, cursor, g.policy.IsSystem(s.App)); list.Role != "" {
			s, _ = autoOCR(s, list)
		}
	}
	g.mu.Lock()
	if !d.settleDue.After(time.Now()) {
		l.after, l.afterAt = raw, time.Now()
	}
	g.mu.Unlock()
	return s, took, nil
}

// readListScreen reads the screen as the agent may see it, with the OCR
// that ocr asks for: none for auto, which reads again only where the tree
// says too little (viewportNeedsOCR).
func (g *Gateway) readListScreen(ctx context.Context, l *lease, ocr string) (*screen.Screen, *Result) {
	mode := ocrOff
	if ocr == "all" {
		mode = ocrAll
	}
	return g.readListScreenOCR(ctx, l, mode)
}

func (g *Gateway) readListScreenOCR(ctx context.Context, l *lease, mode readOCR) (*screen.Screen, *Result) {
	view, _, res := g.readListRaw(ctx, l, mode)
	return view, res
}

// readListRaw reads the screen, and returns it as the agent may see it and
// as the driver read it, which the action pipeline judges swipes against.
func (g *Gateway) readListRaw(ctx context.Context, l *lease, mode readOCR) (view, raw *screen.Screen, res *Result) {
	if res := g.gate(l, "", false); res != nil {
		return nil, nil, res
	}
	raw, err := g.observeOCR(ctx, l.dev, mode)
	if err != nil {
		return nil, nil, g.readError(l, err)
	}
	if raw.Locked {
		return nil, nil, statusResult(StatusLocked, "The phone is locked. Call phone_handoff so the person can unlock it.")
	}
	view, rule := g.agentView(l, raw)
	if rule != "" {
		return nil, nil, &Result{Status: StatusBlocked, Rule: rule,
			Message: "The foreground app is blocked by the person's rules, so the list is hidden."}
	}
	return view, raw, nil
}

// listScreenAfter is the screen a swipe settled on, as the pipeline read
// it (with OCR where the tree says little), read again when the agent
// asked for all OCR text, or without OCR text when it asked for none.
func (g *Gateway) listScreenAfter(ctx context.Context, l *lease, ocr string, next *screen.Screen) (*screen.Screen, *Result) {
	switch {
	case next == nil:
		return g.readListScreen(ctx, l, ocr)
	case ocr == "all":
		return g.readListScreenOCR(ctx, l, ocrAll)
	case ocr == "off":
		cp := *next
		cp.Elements = make([]screen.Element, 0, len(next.Elements))
		for _, e := range next.Elements {
			if e.Source != screen.SourceOCR {
				cp.Elements = append(cp.Elements, e)
			}
		}
		return &cp, nil
	}
	return next, nil
}

// autoOCR is s as ocr "auto" reads it for list: without OCR's lines when
// the tree says enough about the list (viewportNeedsOCR), as the read's
// first screen is read. It reports whether the OCR lines stay.
func autoOCR(s *screen.Screen, list screen.Element) (*screen.Screen, bool) {
	tree := *s
	tree.Elements = make([]screen.Element, 0, len(s.Elements))
	for _, e := range s.Elements {
		if e.Source != screen.SourceOCR {
			tree.Elements = append(tree.Elements, e)
		}
	}
	if len(tree.Elements) == len(s.Elements) {
		return s, false
	}
	if viewportNeedsOCR(&tree, list) {
		return s, true
	}
	return &tree, false
}

// listLane picks where across the list the next swipe runs: the first lane
// whose start the rules let through on the latest screen. The pipeline
// judges the swipe again on its own read, with OCR, before it is sent;
// this only keeps a button in the middle of a feed from ending the read
// when the swipe can start beside it.
func (g *Gateway) listLane(ctx context.Context, l *lease, a Action, cur *screen.Screen) int {
	for _, lane := range readListLanes {
		a.list.lane = lane
		p, res := g.prepare(ctx, l, a, cur)
		if res != nil {
			return 0
		}
		if v := g.evaluate(p, cur); v.Decision == policy.Allow {
			return lane
		}
	}
	return 0
}

// take adds the lines of screen s that the read has not seen. It reports
// false when the read ends with s: past the output limit, or past a gap.
func (st *readState) take(s *screen.Screen, first bool) bool {
	view, req := st.view, st.req
	raw := collectListViewport(s, st.list)
	full := collectViewport(s, screen.Rect{W: s.Width, H: s.Height})
	if !first {
		st.pinned = mergePinnedLines(st.pinned, pinnedLines(st.full, full))
	}
	current := raw
	if anchors := append(append([]viewportLine(nil), st.pinned...), st.raw...); len(anchors) > 0 {
		current = filterPinned(anchors, raw)
	}
	var fresh []viewportLine
	overlap := 0
	switch {
	case len(st.previous) == 0:
		fresh = current
	case req.Direction == "down":
		fresh, overlap = appendViewportBackward(st.previous, current)
	default:
		fresh, overlap = appendViewport(st.previous, current)
	}
	if !first || view.StartScreen > 0 {
		// Adjacent screens must share text: without it, or with lines
		// above the shared part that the last screen did not have, the
		// swipe may have skipped content.
		var unseen []viewportLine
		if len(st.previous) > 0 && len(current) > 0 && overlap > 0 && req.Direction != "down" {
			unseen = unseenAbove(st.previous, current, gapContext{area: st.list.Bounds, seen: st.outside()})
		}
		gap := len(st.previous) > 0 && len(current) > 0 && (overlap == 0 || len(unseen) > 0)
		switch {
		case gap && overlap > 0 && req.Direction != "down" && endMarker(current):
			fresh = newViewportContent(st.previous, current)
		case gap:
			view.Stop, view.Truncated = "gap", true
			view.gap = gapNote(view.StartScreen+view.Pages+1, overlap, unseen)
			return false
		}
	}
	page := view.StartScreen + view.Pages + 1
	if first && view.StartScreen > 0 && len(fresh) == 0 {
		// A continuation that starts where the last call ended adds no
		// screen of its own.
		page--
	}
	if !appendScanLines(view, page, fresh, req.Direction) {
		view.Truncated = true
		view.Stop = "output_limit"
		if view.Lines == 0 {
			view.Stop = "page_too_large"
		}
		return false
	}
	if !(first && view.StartScreen > 0 && len(fresh) == 0) {
		view.Pages++
	}
	view.Overlap += overlap
	if len(current) > 0 || len(raw) == 0 {
		// Every line of a screen that did not move stays at its place, so
		// filterPinned leaves none: the screen before is still the one to
		// find the next screen's overlap with. A continuation's first read
		// of the screen the last call ended on is such a screen; forgetting
		// it took the whole next screen as new.
		// The next screen overlaps this one at its bottom when the read
		// goes forward and at its top when it goes back: keep that end.
		st.previous = tail(current, readListKeep)
		if req.Direction == "down" && len(current) > readListKeep {
			st.previous = current[:readListKeep]
		}
	}
	// The whole last screen: the next one is filtered against it, and a
	// screen that did not move (the end of a page that shows more lines
	// than readListKeep) must lose every line.
	st.raw, st.full = raw, full
	return true
}

// outside returns the last screen's lines outside the list, and its fixed
// controls: what a gap judgment may count as seen though the list did not
// show it (gapContext.seen).
func (st *readState) outside() []viewportLine {
	out := append([]viewportLine(nil), st.pinned...)
	area := st.list.Bounds
	for _, line := range st.full {
		if area.Empty() || !area.Contains(line.Bounds.Center()) {
			out = append(out, line)
		}
	}
	return out
}

// gapNote says why screen page did not follow on from the one before, for
// the result's message: a read stopped at a gap is otherwise hard to tell
// from a list whose screens share nothing.
func gapNote(page, overlap int, unseen []viewportLine) string {
	if overlap == 0 || len(unseen) == 0 {
		return fmt.Sprintf("Screen %d shared no run of lines with the one before, so the swipe may have skipped content.", page)
	}
	quoted := make([]string, 0, 3)
	for _, line := range unseen[:min(len(unseen), 3)] {
		text, _ := truncateRunesMarked(line.Text, 40)
		quoted = append(quoted, fmt.Sprintf("%q", text))
	}
	if len(unseen) > 3 {
		quoted = append(quoted, "…")
	}
	verb := "were"
	if len(unseen) == 1 {
		verb = "was"
	}
	return fmt.Sprintf("Screen %d shared %d line%s with the one before, but %d line%s above them (%s) %s not on it, so content may have been added or skipped there.",
		page, overlap, plural(overlap), len(unseen), plural(len(unseen)), strings.Join(quoted, ", "), verb)
}

// endsHere reports whether the list's last row, at the end being read
// toward, says the list ends there. Only lists read downwards (the finger
// moving up) have such rows: "No more results" at the bottom says nothing
// about the top.
func (st *readState) endsHere() bool {
	return st.req.Direction != "down" && endMarker(st.previous)
}

// find finds the read's list on s: by its ref when the request named one
// and it is still that list, else where it was.
func (st *readState) find(s *screen.Screen) (screen.Element, bool) {
	if st.req.Area != nil {
		e, res := listOn(s, 0, nil, st.req.Area)
		return e, res == nil
	}
	e := findList(s, st.id)
	return e, e.Role != ""
}

func cursorList(c *readCursor) *listID {
	if c == nil || c.area != nil {
		return nil
	}
	return &c.list
}

// listOn finds the list a read starts on: the scroll area, the element ref
// names, the list the continuation stopped in, or else the largest list on
// the screen.
func listOn(s *screen.Screen, ref int, id *listID, area *screen.Rect) (screen.Element, *Result) {
	if area != nil {
		if area.X < 0 || area.Y < 0 || area.W < 100 || area.H < 200 ||
			area.X >= s.Width || area.Y >= s.Height ||
			area.W > s.Width-area.X || area.H > s.Height-area.Y {
			return screen.Element{}, errResult(ErrInvalidArgument,
				"scroll_area must be at least 100 by 200 pixels and lie inside the %dx%d screen.", s.Width, s.Height)
		}
		return screen.Element{Role: screen.RoleList, Bounds: *area}, nil
	}
	if ref > 0 {
		e, ok := s.Find(ref)
		if !ok || !readableList(e) {
			return screen.Element{}, nil
		}
		if id != nil && !sameListPlace(e.Bounds, id.bounds) {
			return screen.Element{}, nil
		}
		return e, nil
	}
	if id != nil {
		return findList(s, *id), nil
	}
	return largestList(s), nil
}

// findList finds the list id names on s: one in the same place, preferring
// one with the same path. A list that moved or changed size is another
// list as far as a read can tell.
func findList(s *screen.Screen, id listID) screen.Element {
	var found screen.Element
	for _, e := range s.Elements {
		if !readableList(e) || !sameListPlace(e.Bounds, id.bounds) {
			continue
		}
		if e.Path == id.path {
			return e
		}
		if found.Role == "" {
			found = e
		}
	}
	if found.Role != "" && id.path != "" && found.Path != "" {
		// Another list where this one was (a tab switched): not this one.
		return screen.Element{}
	}
	return found
}

// sameListPlace reports whether a list is where it was: within a twentieth
// of its size on every side, as a list whose toolbar hides on scrolling
// grows by its height.
func sameListPlace(a, b screen.Rect) bool {
	tolX, tolY := max(b.W/20, 8), max(b.H/10, 8)
	abs := func(n int) int { return max(n, -n) }
	return abs(a.X-b.X) <= tolX && abs(a.W-b.W) <= tolX && abs(a.Y-b.Y) <= tolY && abs(a.Y+a.H-b.Y-b.H) <= tolY
}

func largestList(s *screen.Screen) screen.Element {
	if s == nil {
		return screen.Element{}
	}
	var candidates []screen.Element
	for _, e := range s.Elements {
		if readableList(e) {
			candidates = append(candidates, e)
		}
	}
	if len(candidates) == 0 {
		return screen.Element{}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i].Bounds, candidates[j].Bounds
		return a.W*a.H > b.W*b.H
	})
	largest := candidates[0]
	if largest.Bounds.W*largest.Bounds.H*readListMinArea < s.Width*s.Height {
		return screen.Element{}
	}
	return largest
}

// listGuess is where a read found its list when no element marks one: the
// area to read as a scroll_area, and whether it is the screen's content
// (contentArea) rather than a column of rows.
type listGuess struct {
	area    *screen.Rect
	content bool
}

// startList finds the list a read starts on (listOn). With no ref, scroll
// area or continuation, on a screen with no list or scrollable element
// big enough, it falls back to a column of rows (rowColumn), and then,
// outside launchers and system UI (system), to the screen's content
// (contentArea), returned as the area to read as a scroll_area: iOS 27
// lists Settings' rows but no element for the list that holds them
// (GitHub issue #148), and a web view through WebDriverAgent shows its
// page's text but no element for the view or its scrolling.
func startList(s *screen.Screen, req ReadListRequest, cursor *readCursor, system bool) (screen.Element, listGuess, *Result) {
	list, res := listOn(s, req.Ref, cursorList(cursor), req.Area)
	if res != nil || list.Role != "" || req.Ref > 0 || req.Area != nil || cursor != nil {
		return list, listGuess{}, res
	}
	if r, ok := rowColumn(s); ok {
		return screen.Element{Role: screen.RoleList, Bounds: r}, listGuess{area: &r}, nil
	}
	if system {
		return list, listGuess{}, nil
	}
	if r, ok := contentArea(s); ok {
		return screen.Element{Role: screen.RoleList, Bounds: r}, listGuess{area: &r, content: true}, nil
	}
	return list, listGuess{}, nil
}

// contentArea is the part of a screen a read scrolls when nothing on it
// marks a list: the screen's width between its top and bottom eighths,
// where the status, navigation, tab and tool bars lie, less the keyboard
// and any system bar the driver reports over it. A web view on an iPhone
// through WebDriverAgent lists its page's text and links but no element
// for the view or its scrolling, and lines of differing width make no
// column of rows (a bank's bill page in a field test). It counts only when
// at least readListMinRows rows of text or links lie in it, from the tree
// or OCR, spread over half its height or more: a page of text, not a grid
// of buttons, a dialog, a short form or an empty screen.
func contentArea(s *screen.Screen) (screen.Rect, bool) {
	if s == nil || s.Width <= 0 || s.Height <= 0 {
		return screen.Rect{}, false
	}
	area, _ := clearPart(s, screen.Rect{X: 0, Y: s.Height / 8, W: s.Width, H: s.Height * 3 / 4})
	if area.W < 100 || area.H < 200 || area.W*area.H*readListMinArea < s.Width*s.Height {
		return screen.Rect{}, false
	}
	var lines []screen.Rect
	for _, e := range s.Elements {
		b := e.Bounds
		if e.Hidden || b.Empty() || strings.TrimSpace(e.Label) == "" || !area.Contains(b.Center()) {
			continue
		}
		if e.Source == screen.SourceOCR || e.Role == screen.RoleText || e.Role == screen.RoleLink {
			lines = append(lines, b)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].Center().Y < lines[j].Center().Y })
	// One row each: a label and its value side by side count once.
	var rows []screen.Rect
	for _, b := range lines {
		if n := len(rows); n > 0 {
			last := rows[n-1]
			if b.Center().Y-last.Center().Y < min(b.H, last.H)/2 {
				continue
			}
		}
		rows = append(rows, b)
	}
	if len(rows) < readListMinRows {
		return screen.Rect{}, false
	}
	if spread := rows[len(rows)-1].Center().Y - rows[0].Center().Y; spread*2 < area.H {
		return screen.Rect{}, false
	}
	return area, true
}

// readListMinRows is how many rows a column needs to be read as a list.
const readListMinRows = 6

// rowColumn finds a column of rows on a screen that marks no list: at
// least readListMinRows elements from the tree, each at least half the
// screen wide and at most an eighth of it high, that share their left
// edge and width and lie one below another, together covering at least a
// quarter of the screen, as a settings page's or a table's rows do. It
// returns the box around them. Text read by OCR, fields, images and tabs
// do not count, so a home screen, a form or a tab bar is never a column.
func rowColumn(s *screen.Screen) (screen.Rect, bool) {
	if s == nil || s.Width <= 0 || s.Height <= 0 {
		return screen.Rect{}, false
	}
	var rows []screen.Rect
	for _, e := range s.Elements {
		b := e.Bounds
		if e.Source == screen.SourceOCR || e.Hidden || b.Empty() || b.W*2 < s.Width || b.H*8 > s.Height {
			continue
		}
		switch e.Role {
		case screen.RoleButton, screen.RoleText, screen.RoleLink, screen.RoleSwitch, screen.RoleCheckbox, screen.RoleOther:
			rows = append(rows, b)
		}
	}
	tol := max(s.Width/40, 4)
	near := func(a, b int) bool { return max(a-b, b-a) <= tol }
	var best []screen.Rect
	for _, r := range rows {
		var col []screen.Rect
		for _, o := range rows {
			if near(o.X, r.X) && near(o.W, r.W) {
				col = append(col, o)
			}
		}
		// One row each: a cell and an element of the same size inside it
		// count once.
		sort.SliceStable(col, func(i, j int) bool { return col[i].Y < col[j].Y })
		var distinct []screen.Rect
		for _, o := range col {
			if n := len(distinct); n > 0 && o.Y < distinct[n-1].Y+distinct[n-1].H/2 {
				continue
			}
			distinct = append(distinct, o)
		}
		if len(distinct) > len(best) {
			best = distinct
		}
	}
	if len(best) < readListMinRows {
		return screen.Rect{}, false
	}
	box := best[0]
	for _, o := range best[1:] {
		x0, y0 := min(box.X, o.X), min(box.Y, o.Y)
		x1, y1 := max(box.X+box.W, o.X+o.W), max(box.Y+box.H, o.Y+o.H)
		box = screen.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
	}
	box.X, box.Y = max(box.X, 0), max(box.Y, 0)
	box.W, box.H = min(box.W, s.Width-box.X), min(box.H, s.Height-box.Y)
	if box.W*box.H*readListMinArea < s.Width*s.Height {
		return screen.Rect{}, false
	}
	return box, true
}

func readableList(e screen.Element) bool {
	if e.Bounds.Empty() {
		return false
	}
	return e.Role == screen.RoleList || e.Has(screen.StateScrollable)
}

// viewportNeedsOCR reports whether the tree says too little about what the
// list shows: few or short labels, or large unlabelled elements (images,
// web views).
func viewportNeedsOCR(s *screen.Screen, list screen.Element) bool {
	area := list.Bounds
	if s == nil || area.Empty() {
		return false
	}
	lines, chars, unlabelled := 0, 0, 0
	for _, e := range s.Elements {
		if list.Ref != 0 && e.Ref == list.Ref {
			continue
		}
		if e.Bounds.Empty() || !area.Contains(e.Bounds.Center()) {
			continue
		}
		if strings.TrimSpace(e.Label) != "" {
			lines++
			chars += utf8.RuneCountInString(e.Label)
		} else if e.Bounds.W*e.Bounds.H*8 >= area.W*area.H {
			unlabelled++
		}
	}
	return lines < 3 || chars < 24 || unlabelled >= 2
}

func mergePinnedLines(existing, found []viewportLine) []viewportLine {
	out := append([]viewportLine(nil), existing...)
	for _, line := range found {
		duplicate := false
		for _, old := range out {
			if viewportKey(old) == viewportKey(line) && nearlySameRect(old.Bounds, line.Bounds) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, line)
		}
	}
	return tail(out, readListKeep)
}

func tail(lines []viewportLine, n int) []viewportLine {
	if len(lines) > n {
		return lines[len(lines)-n:]
	}
	return lines
}

func appendScanLines(view *ScanView, page int, lines []viewportLine, direction string) bool {
	if len(lines) == 0 {
		return true
	}
	out := make([]string, 0, len(lines))
	bytes := view.OutputBytes
	clippedAny := false
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" {
			continue
		}
		text, clipped := truncateRunesMarked(text, 1000)
		clippedAny = clippedAny || clipped
		if bytes+len(text)+3 > readListOutputBytes {
			return false
		}
		out = append(out, text)
		bytes += len(text) + 3
	}
	view.OutputBytes = bytes
	if clippedAny {
		view.Truncated = true
	}
	if len(out) > 0 {
		section := ScanPage{Number: page, Lines: out}
		if direction == "down" {
			view.Sections = append([]ScanPage{section}, view.Sections...)
		} else {
			view.Sections = append(view.Sections, section)
		}
		view.Lines += len(out)
	}
	return true
}

func readListResult(l *lease, view *ScanView) *Result {
	res := &Result{Status: StatusSettled, Device: l.dev.id, DeviceName: l.dev.name(), Scan: view}
	state := "Read"
	if view.Truncated {
		state = "Read part of"
	}
	res.Message = fmt.Sprintf("%s the list: %d screen%s, %d new line%s, %d overlapping line%s; stopped at %s.",
		state, view.Pages, plural(view.Pages), view.Lines, plural(view.Lines),
		view.Overlap, plural(view.Overlap), strings.ReplaceAll(view.Stop, "_", " "))
	if view.gap != "" {
		res.Message += " " + view.gap
	}
	for _, note := range view.notes {
		res.Message += " " + note
	}
	if view.Continuation != "" {
		res.Message += " Pass continuation to read on from here."
	}
	if view.Swipes > 0 {
		res.Message += " The list stays where the read ended: observe before acting on refs."
	}
	return res
}

func (g *Gateway) readProgress(req ReadListRequest, start time.Time, view *ScanView) {
	if req.Progress != nil {
		req.Progress(time.Since(start).Seconds(), float64(req.MaxMS)/1000,
			fmt.Sprintf("Read %d screen%s", view.Pages, plural(view.Pages)))
	}
}

func appKnown(app screen.App) bool {
	return app.ID != "" || app.Name != ""
}

// withScan adds what was read so far to a result that ended the read.
func withScan(res *Result, view *ScanView) *Result {
	if view.Pages > 0 && (res.Error != "" || res.Status != StatusSettled) {
		view.Truncated = true
	}
	res.Scan = view
	return res
}

func readListStop(res *Result) string {
	switch {
	case res.Status == StatusStopped:
		return "stopped"
	case res.Status == StatusTookOver || res.Status == StatusPaused:
		return "took_over"
	case res.Status == StatusPending:
		return "approval_open"
	case res.Status == StatusBlocked:
		return "blocked"
	case res.Status == StatusLocked:
		return "locked"
	case res.Status == StatusUncertain:
		return "uncertain"
	case res.Error == ErrScopeDenied:
		return "scope"
	case res.Error == ErrDeviceOffline:
		return "offline"
	case res.Error == ErrNotFound:
		return "list_changed"
	default:
		return "failed"
	}
}

func readListCanContinue(stop string) bool {
	switch stop {
	case "deadline", "max_screens", "output_limit", "unsettled":
		return true
	}
	return false
}

// keepCursor keeps where the read stopped on the lease and returns the
// opaque continuation that names it.
func (g *Gateway) keepCursor(l *lease, st *readState) string {
	return g.putCursor(l, &readCursor{app: st.app, list: st.id, area: st.req.Area, direction: st.req.Direction,
		page:     st.view.StartScreen + st.view.Pages,
		previous: append([]viewportLine(nil), st.previous...),
		raw:      append([]viewportLine(nil), st.raw...),
		pinned:   append([]viewportLine(nil), st.pinned...)})
}

// putCursor keeps c on the lease for readListCursorLife under a new opaque
// id, and returns the id.
func (g *Gateway) putCursor(l *lease, c *readCursor) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := "rl_" + base64.RawURLEncoding.EncodeToString(b[:])
	c.expires = time.Now().Add(readListCursorLife)
	g.mu.Lock()
	defer g.mu.Unlock()
	if l.readLists == nil {
		l.readLists = map[string]*readCursor{}
	}
	now := time.Now()
	for k, old := range l.readLists {
		if now.After(old.expires) {
			delete(l.readLists, k)
		}
	}
	for len(l.readLists) >= readListCursors {
		var oldest string
		for k, old := range l.readLists {
			if oldest == "" || old.expires.Before(l.readLists[oldest].expires) {
				oldest = k
			}
		}
		delete(l.readLists, oldest)
	}
	l.readLists[id] = c
	return id
}

// takeCursor finds the continuation on the lease and uses it up. One from
// another task or phone is on another lease, so it is not found. A call
// whose direction, area or ref does not go with it leaves it there: the
// error says what to pass, and the agent's next call still finds it.
func (g *Gateway) takeCursor(l *lease, req ReadListRequest) (*readCursor, *Result) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := l.readLists[req.Continuation]
	if !ok || time.Now().After(c.expires) {
		delete(l.readLists, req.Continuation)
		return nil, errResult(ErrInvalidArgument,
			"continuation is unknown or has expired: it holds for 15 minutes, on the task and phone it came from. Read the list again without it.")
	}
	if req.Direction != "" && req.Direction != c.direction {
		return nil, errResult(ErrInvalidArgument, "continuation reads %s; leave direction out or pass %s.", c.direction, c.direction)
	}
	if req.Area != nil && (c.area == nil || *req.Area != *c.area) {
		return nil, errResult(ErrInvalidArgument, "continuation belongs to another scroll_area.")
	}
	if req.Ref > 0 && c.area != nil {
		return nil, errResult(ErrInvalidArgument, "continuation belongs to a scroll_area; leave ref out.")
	}
	delete(l.readLists, req.Continuation)
	return c, nil
}

func truncateRunesMarked(s string, max int) (string, bool) {
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	r := []rune(s)
	return string(r[:max-1]) + "…", true
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// noAskMessage answers a phone_read_list swipe the rules would ask the
// person about (Call.noAsk).
func noAskMessage(v policy.Verdict) string {
	if v.Rule == policy.RuleEvery {
		return "The person asks to approve every action, and phone_read_list does not ask, so the read stopped before its next swipe. " +
			"Scroll with phone_swipe instead, which asks them each time."
	}
	return fmt.Sprintf("The next swipe would start where the rule %q asks the person first, and phone_read_list does not ask, so the read stopped before it. "+
		"Swipe with phone_swipe (it asks them, or start it elsewhere with from and to), then call phone_read_list again.", v.Rule)
}

func scanText(scan *ScanView) string {
	var b strings.Builder
	for _, page := range scan.Sections {
		fmt.Fprintf(&b, "screen %d\n", page.Number)
		for _, line := range page.Lines {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
