package ios

import (
	"context"
	"sync"
	"time"
)

// Reading WebDriverAgent's tree when it is slow. A field report (iPhone 13
// mini on Wi-Fi, iOS 27.0.1, 2026-11-22): in a React Native list that had
// been swiped down eight times, every call failed after the full 45 s, each
// on GET /source timing out, until another app came to the front. Each
// read sent a new /source while the phone still built the last one, so the
// reads queued up on the phone, and every read waited the whole 30 s the
// request had before it gave up, leaving nothing for the action and the
// read after it.
//
// Now:
//
//   - One tree request at a time (tree). A read that finds one in flight
//     waits for it, and takes its answer when it began after the last
//     input; otherwise it asks again once the phone is free. A request
//     whose reader gave up runs on, so the next read knows the phone is
//     still busy instead of queueing another tree behind it.
//   - A read waits wdaTreeWait for the tree at most, and less near the end
//     of its context, leaving wdaOCRReserve for a picture. Then it answers
//     without the tree, with the text OCR reads from the picture
//     (screen.Screen.TreeTooSlow), instead of failing.
//   - Pinky remembers an app whose tree did not come in time or took
//     wdaTreeSlow or more, for wdaSlowFor: its next reads ask for a
//     shallower tree (wdaShallowDepth, the read says "partial"), and if
//     that is slow too, for none (OCR only). Reads of other apps are as
//     before.

var (
	// wdaTreeWait is the longest a read waits for the tree.
	wdaTreeWait = 15 * time.Second
	// wdaTreeSlow is how long a tree may take before its app's next reads
	// ask for less.
	wdaTreeSlow = 8 * time.Second
	// wdaSlowFor is how long an app stays slow after its tree was.
	wdaSlowFor = 2 * time.Minute
	// wdaTreeMax bounds a tree request that no read waits for any more.
	wdaTreeMax = 2 * time.Minute
)

const (
	// wdaShallowDepth is the snapshot depth for an app whose full tree is
	// too slow: half of wdaDepth. Not measured on the app of the report.
	wdaShallowDepth = 30
	// wdaOCRReserve is what a read leaves of its context, after the tree,
	// for the picture and its OCR: an MJPEG frame comes within frameWait
	// (2.5 s), and OCR takes about a second.
	wdaOCRReserve = 5 * time.Second
)

// treeLevel is how much of an app's tree a read asks for.
type treeLevel int

const (
	treeFull    treeLevel = iota // wdaDepth
	treeShallow                  // wdaShallowDepth
	treeNone                     // no tree: OCR only
)

func (l treeLevel) depth() int {
	if l == treeShallow {
		return wdaShallowDepth
	}
	return wdaDepth
}

func (l treeLevel) String() string {
	switch l {
	case treeShallow:
		return "shallow"
	case treeNone:
		return "none"
	}
	return "full"
}

// wdaTrees are a device's tree requests and the apps whose trees are slow.
type wdaTrees struct {
	mu       sync.Mutex
	inflight *treeCall
	slow     map[string]slowTree // by bundle id
	lastApp  string              // the app of the last read that named one
	ctx      context.Context     // the requests' own, ended by close
	cancel   context.CancelFunc
}

// slowTree is an app whose tree was slow: reads ask for level until until.
type slowTree struct {
	level treeLevel
	until time.Time
}

// treeCall is one GET /source.
type treeCall struct {
	depth   int
	started time.Time
	done    chan struct{}
	root    *wdaNode // read only once done is closed
	err     error
}

// level is how a read of app's screen asks for its tree now.
func (t *wdaTrees) level(app string, now time.Time) treeLevel {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.slow[app]; ok && now.Before(s.until) {
		return s.level
	}
	return treeFull
}

// anySlow reports whether some app's tree is slow now: a read then learns
// the app in front before it asks for the tree.
func (t *wdaTrees) anySlow(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for app, s := range t.slow {
		if now.Before(s.until) {
			return true
		}
		delete(t.slow, app)
	}
	return false
}

// slower notes that app's tree was too slow at level: its reads ask for
// less from now, for wdaSlowFor. It returns the level they ask for.
func (t *wdaTrees) slower(app string, at treeLevel, now time.Time) treeLevel {
	if app == "" {
		return treeFull
	}
	next := min(at+1, treeNone)
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.slow[app]; ok && now.Before(s.until) && s.level > next {
		next = s.level
	}
	if t.slow == nil {
		t.slow = map[string]slowTree{}
	}
	t.slow[app] = slowTree{level: next, until: now.Add(wdaSlowFor)}
	return next
}

// sawApp records the app a read named.
func (t *wdaTrees) sawApp(app string) {
	if app == "" {
		return
	}
	t.mu.Lock()
	t.lastApp = app
	t.mu.Unlock()
}

// last is the app the last read named.
func (t *wdaTrees) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastApp
}

// bg is the context tree requests run with. The caller holds t.mu.
func (t *wdaTrees) bg() context.Context {
	if t.ctx == nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
	return t.ctx
}

// close ends the requests in flight, for Close.
func (t *wdaTrees) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel == nil {
		t.ctx, t.cancel = context.WithCancel(context.Background())
	}
	t.cancel()
}

// tree reads the page source at depth, through the one tree request the
// device has in flight at a time: it joins the request in flight when that
// asks for the same depth and began after the last input, and otherwise
// waits for it to end before it asks. The request does not end with ctx:
// the phone goes on building the tree whether or not Pinky waits, and the
// next read waits for it rather than queueing another behind it.
func (d *WDADevice) tree(ctx context.Context, depth int) (*wdaNode, error) {
	t := &d.trees
	for {
		t.mu.Lock()
		c, mine := t.inflight, false
		if c == nil {
			c = &treeCall{depth: depth, started: time.Now(), done: make(chan struct{})}
			t.inflight, mine = c, true
			go d.runTree(t.bg(), c)
		}
		t.mu.Unlock()
		join := mine || c.depth == depth && c.started.UnixNano() >= d.c.inputAt.Load()
		select {
		case <-c.done:
			if join {
				return c.root, c.err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// runTree performs one tree request.
func (d *WDADevice) runTree(ctx context.Context, c *treeCall) {
	ctx, cancel := context.WithTimeout(ctx, wdaTreeMax)
	defer cancel()
	if err := d.c.setDepth(ctx, c.depth); err != nil {
		c.err = err
	} else {
		c.root, c.err = d.sourceNow(ctx)
	}
	d.trees.mu.Lock()
	if d.trees.inflight == c {
		d.trees.inflight = nil
	}
	d.trees.mu.Unlock()
	close(c.done)
}

// treeBy is when a read with ctx stops waiting for the tree: after
// wdaTreeWait, and wdaOCRReserve before ctx ends, or half way there when
// ctx has less than twice that left.
func treeBy(ctx context.Context, now time.Time) time.Time {
	by := now.Add(wdaTreeWait)
	if dl, ok := ctx.Deadline(); ok {
		reserve := min(wdaOCRReserve, dl.Sub(now)/2)
		if end := dl.Add(-reserve); end.Before(by) {
			by = end
		}
	}
	return by
}
