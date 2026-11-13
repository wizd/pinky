package standalone

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/schedule"
)

// setNow moves the schedules' clock for the test.
func setNow(t *testing.T, at time.Time) {
	schedNow = func() time.Time { return at }
	t.Cleanup(func() { schedNow = time.Now })
}

// A schedule on the phone: saved and previewed in the phone's zone, skipped
// while the phone is locked, and run unattended when the alarm fires: a
// payment is denied without asking, and the person hears how it went.
func TestScheduleOnPhone(t *testing.T) {
	h := newHarness(t)
	h.dev.Show("cart")
	shanghai, _ := time.LoadLocation("Asia/Shanghai")
	setNow(t, time.Date(2026, 11, 13, 9, 0, 0, 0, shanghai))
	ctl := h.dial(t)

	daily := map[string]any{"task": "Order lunch", "repeat": "daily", "times": []string{"12:00"}}
	bad := map[string]any{"task": "Order lunch", "repeat": "daily", "times": []string{"25:00"}}
	if r := ctl.do(map[string]any{"cmd": "schedule_preview", "schedule": bad, "zone": "Asia/Shanghai"}); r.OK || r.Code != CodeSchedTime {
		t.Fatalf("preview of a bad time: %+v", r)
	}
	r := ctl.do(map[string]any{"cmd": "schedule_preview", "schedule": daily, "zone": "Asia/Shanghai"})
	noon := time.Date(2026, 11, 13, 12, 0, 0, 0, shanghai)
	if !r.OK || len(r.Upcoming) != 3 || r.Upcoming[0] != noon.UnixMilli() {
		t.Fatalf("preview: %+v", r)
	}
	r = ctl.do(map[string]any{"cmd": "schedule_save", "schedule": daily, "zone": "Asia/Shanghai"})
	if !r.OK || len(r.Schedules) != 1 || r.NextMS != noon.UnixMilli() {
		t.Fatalf("save: %+v", r)
	}
	id := r.Schedules[0].ID

	// Locked at noon: skipped, and the person hears of it.
	setNow(t, noon)
	if r := ctl.do(map[string]any{"cmd": "schedule_due", "locked": true, "key": "sk-test", "base_url": h.url,
		"model": "fake"}); !r.OK || r.NextMS != noon.AddDate(0, 0, 1).UnixMilli() {
		t.Fatalf("due while locked: %+v", r)
	}
	st := ctl.do(map[string]any{"cmd": "state"}).State
	if len(st.Notices) != 1 || st.Notices[0].End != schedule.Skipped || st.Notices[0].Reason != schedule.SkipLocked {
		t.Fatalf("notices: %+v", st.Notices)
	}
	if r := ctl.do(map[string]any{"cmd": "notice_seen", "notice": st.Notices[0].ID}); !r.OK {
		t.Fatal(r)
	}
	if st := ctl.do(map[string]any{"cmd": "state"}).State; len(st.Notices) != 0 {
		t.Fatalf("notice not taken: %+v", st.Notices)
	}
	// Pinky on the phone turned off: skipped too.
	setNow(t, noon.Add(time.Hour))
	ctl.do(map[string]any{"cmd": "schedule_save", "schedule": map[string]any{"id": id, "task": "Order lunch", "repeat": "daily",
		"times": []string{"12:00", "23:00"}}})
	setNow(t, noon.Add(11*time.Hour+time.Minute))
	ctl.do(map[string]any{"cmd": "schedule_due", "off": true, "key": "sk-test"})
	st = ctl.do(map[string]any{"cmd": "state"}).State
	if len(st.Notices) != 1 || st.Notices[0].Reason != schedule.SkipOff {
		t.Fatalf("notices while off: %+v", st.Notices)
	}
	ctl.do(map[string]any{"cmd": "notice_seen", "notice": st.Notices[0].ID})
	ctl.do(map[string]any{"cmd": "schedule_save", "schedule": map[string]any{"id": id, "task": "Order lunch", "repeat": "daily",
		"times": []string{"12:00"}}})

	// The next day, free: it runs unattended, and its payment is denied.
	h.api.turns = []func(string, *http.Request) map[string]any{
		func(last string, _ *http.Request) map[string]any {
			return call("t1", "phone_tap", map[string]any{"ref": refOf(t, last, "Place order · $12.80")})
		},
		func(last string, _ *http.Request) map[string]any {
			if !strings.HasPrefix(last, "approval_denied") {
				t.Errorf("after the payment:\n%s", last)
			}
			return call("t2", "finish", map[string]any{"outcome": "failed", "summary": "Skipped paying."})
		},
	}
	sub := h.dial(t)
	sub.send(map[string]any{"cmd": "subscribe"})
	sub.read()
	setNow(t, noon.AddDate(0, 0, 1))
	if r := ctl.do(map[string]any{"cmd": "schedule_due", "key": "sk-test", "base_url": h.url, "model": "fake"}); !r.OK {
		t.Fatalf("due: %+v", r)
	}
	// The run may end before the subscription sees it running; the ended
	// task still says a schedule started it.
	ended := sub.until("the notice", func(s State) bool { return len(s.Notices) > 0 })
	if a := ended.Agent; a == nil || !a.Scheduled || !a.Unattended {
		t.Errorf("agent: %+v", a)
	}
	if n := ended.Notices[0]; n.End != "failed" || n.Summary != "Skipped paying." || n.Task != "Order lunch" {
		t.Fatalf("notice: %+v", n)
	}
	eventually(t, "the key is gone with the run", func() bool { return h.key() == "" })
	s, _ := h.core.sched.Get(id)
	if len(s.Runs) != 3 || s.Runs[0].End != "failed" || s.Runs[1].Reason != schedule.SkipOff || s.Runs[2].Reason != schedule.SkipLocked {
		t.Fatalf("runs: %+v", s.Runs)
	}

	// Run now: the person is there, so no notice.
	ctl.do(map[string]any{"cmd": "notice_seen", "notice": ended.Notices[0].ID})
	h.api.mu.Lock()
	h.api.n, h.api.turns = 0, []func(string, *http.Request) map[string]any{
		func(string, *http.Request) map[string]any {
			return call("t1", "finish", map[string]any{"outcome": "done", "summary": "Looked."})
		},
	}
	h.api.mu.Unlock()
	if r := ctl.do(map[string]any{"cmd": "schedule_run", "id": id, "key": "sk-test", "base_url": h.url, "model": "fake"}); !r.OK {
		t.Fatalf("run now: %+v", r)
	}
	eventually(t, "run now to end", func() bool {
		s, _ := h.core.sched.Get(id)
		return len(s.Runs) == 4 && s.Runs[0].End == "done"
	})
	if s, _ := h.core.sched.Get(id); !s.Runs[0].Manual || len(h.core.pendingNotices()) != 0 {
		t.Errorf("run now: %+v, %d notices", s.Runs[0], len(h.core.pendingNotices()))
	}

	if r := ctl.do(map[string]any{"cmd": "schedule_pause", "id": id, "paused": true}); !r.OK || r.NextMS != 0 {
		t.Fatalf("pause: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "schedule_delete", "id": id}); !r.OK {
		t.Fatalf("delete: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "schedule_run", "id": id}); r.OK || r.Code != CodeSchedNotFound {
		t.Fatalf("run a deleted one: %+v", r)
	}
	if r := ctl.do(map[string]any{"cmd": "schedules"}); !r.OK || len(r.Schedules) != 0 {
		t.Fatalf("schedules: %+v", r)
	}
}

// A locked phone runs a scheduled task only on a background display that is
// set up: paired, with Wireless debugging's port, on a phone that has one.
func TestScheduleBackgroundReady(t *testing.T) {
	for status, want := range map[string]bool{"off": false, "unpaired": false, "no_port": false,
		"ready": true, "connected": true, "refused": false, "failed": false} {
		if got := shellReady(status); got != want {
			t.Errorf("shellReady(%q) = %v", status, got)
		}
	}
	h := newHarness(t)
	if h.core.backgroundReady("pixel") {
		t.Error("ready without a shell")
	}
	sh := &fakeShell{st: ShellState{On: true, Status: "connected"}}
	h = newHarness(t, func(o *Options) { o.Shell = sh })
	// The demo phone has no background displays.
	if h.core.backgroundReady("pixel") {
		t.Error("ready on a phone without background displays")
	}
}
