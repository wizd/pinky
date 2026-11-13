package ui

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mg "github.com/egoist/mygo/ui"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/pilot"
	"github.com/DIYgod/pinky/internal/schedule"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// schedFakePilot is fakePilot with scheduled tasks.
type schedFakePilot struct {
	*fakePilot
	smu       sync.Mutex
	scheduled []pilot.Scheduled
}

func (f *schedFakePilot) StartScheduled(cfg pilot.Config, device, task string, s pilot.Scheduled) error {
	if err := f.Start(cfg, device, task); err != nil {
		return err
	}
	f.mu.Lock()
	st := f.status[device]
	st.Scheduled = &s
	f.status[device] = st
	f.mu.Unlock()
	f.smu.Lock()
	f.scheduled = append(f.scheduled, s)
	f.smu.Unlock()
	return nil
}

// end ends the device's task as the agent would.
func (f *schedFakePilot) end(device string, end pilot.End, summary string, met *bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.status[device]
	s.Ended, s.End, s.Summary, s.Met = time.Now(), end, summary, met
	f.status[device] = s
}

func (f *schedFakePilot) running(device string) bool {
	s, ok := f.Status(device)
	return ok && s.Running()
}

// newSchedApp is newAskApp with scheduled tasks in a temporary file.
func newSchedApp(t *testing.T) (*testApp, *schedFakePilot, *mg.Tester, string) {
	t.Helper()
	ta, fp, tt := newAskApp(t, true, true)
	sp := &schedFakePilot{fakePilot: fp}
	ta.app.opt.Pilot = sp
	st, err := schedule.Open(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	ta.app.opt.Schedules = st
	ta.settings.AskPinky = true
	ta.startSchedules()
	t.Cleanup(ta.stopSchedules)
	schedPoll = 10 * time.Millisecond
	t.Cleanup(func() { schedPoll = 500 * time.Millisecond })
	return ta, sp, tt, ta.devices[0].Info.ID
}

// The Schedule… button takes the typed task into the dialog; saved, the
// schedule shows under Tasks with its next time.
func TestScheduleDialog(t *testing.T) {
	ta, _, tt, id := newSchedApp(t)
	// Tall enough that the Tasks section's rows sit below the Ask box.
	tt.SetSize(1280, 1100)
	ta.ask.draft = "Check the weather"
	frameUntil(t, ta, tt, locales.SchedButton())
	if err := tt.Click(locales.SchedButton()); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, ta, tt, locales.SchedTitleNew())
	if ta.sched.task != "Check the weather" || ta.sched.device != id {
		t.Fatalf("dialog: %+v", ta.sched)
	}
	frameUntil(t, ta, tt, locales.SchedDesktopNote())

	// A bad time says why, in the preview, and Save refuses.
	ta.sched.times = "25:00"
	frameUntil(t, ta, tt, locales.SchedErrTime())
	if err := tt.Click(locales.SettingsSave()); err != nil {
		t.Fatal(err)
	}
	if !ta.sched.open || len(ta.opt.Schedules.List()) != 0 {
		t.Fatal("saved a bad time")
	}

	ta.sched.times = "08:00, 20:00"
	ta.sched.cond = "it will rain"
	ta.sched.err = ""
	frameUntil(t, ta, tt, locales.SchedCooldown())
	if err := tt.Click(locales.SettingsSave()); err != nil {
		t.Fatal(err)
	}
	list := ta.opt.Schedules.List()
	if ta.sched.open || len(list) != 1 {
		t.Fatalf("not saved: open %v, %d schedules, err %q", ta.sched.open, len(list), ta.sched.err)
	}
	s := list[0]
	if s.Task != "Check the weather" || s.Device != id || s.Condition != "it will rain" || len(s.Times) != 2 {
		t.Errorf("saved %+v", s)
	}
	if ta.ask.draft != "" {
		t.Error("the Ask box kept the scheduled task")
	}
	frameUntil(t, ta, tt, locales.SchedSection())
	if _, ok := tt.Access(locales.SchedOpen("Check the weather")); !ok {
		t.Fatalf("no row: %q", tt.Texts())
	}

	// Opened again: Pause, then Delete.
	if err := tt.Click(locales.SchedOpen("Check the weather")); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, ta, tt, locales.SchedTitleEdit())
	frameUntil(t, ta, tt, locales.SchedNoRuns())
	if err := tt.Click(locales.SchedPause()); err != nil {
		t.Fatal(err)
	}
	if got, _ := ta.opt.Schedules.Get(s.ID); !got.Paused {
		t.Error("not paused")
	}
	if err := tt.Click(locales.SchedDelete()); err != nil {
		t.Fatal(err)
	}
	ta.sync()
	if len(ta.opt.Schedules.List()) != 0 || ta.sched.open {
		t.Error("not deleted")
	}
}

// Check skips a run when the phone is not free for it.
func TestScheduleCheck(t *testing.T) {
	ta, sp, _, id := newSchedApp(t)
	h := schedHost{ta.app}
	// Check asks the main thread, which sync stands in for.
	checkOn := func(device string) string {
		var got string
		done := make(chan struct{})
		go func() { got = h.Check(schedule.Schedule{Device: device}); close(done) }()
		for {
			select {
			case <-done:
				return got
			default:
				ta.sync()
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	check := func() string { return checkOn(id) }
	if got := check(); got != "" {
		t.Errorf("idle phone: %q", got)
	}
	if got := checkOn("gone"); got != schedule.SkipNoPhone {
		t.Errorf("no phone: %q", got)
	}
	_ = sp.Start(pilot.Config{}, id, "Open Clock")
	if got := check(); got != schedule.SkipBusy {
		t.Errorf("Ask Pinky busy: %q", got)
	}
	ta.settings.AskPinky = false
	if got := check(); got != schedule.SkipOff {
		t.Errorf("Ask Pinky off: %q", got)
	}
}

// Run passes the condition and the last results to the agent and copies
// how the task ended; Report notifies in the language shown.
func TestScheduleRun(t *testing.T) {
	ta, sp, _, id := newSchedApp(t)
	h := schedHost{ta.app}
	due := time.Now().Add(-time.Minute)
	s, err := ta.opt.Schedules.Save(schedule.Schedule{Task: "Check the weather", Device: id, Repeat: schedule.Daily,
		Times: []string{"08:00"}, Condition: "it will rain"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.Runs = []schedule.Run{{Due: due.Add(-24 * time.Hour), End: "done", Summary: "Sunny."}}
	out := make(chan schedule.Run, 1)
	go func() { out <- h.Run(context.Background(), s, schedule.Run{Due: due, End: schedule.Running}) }()
	ta.waitFor(t, "the scheduled task", func() bool { return sp.running(id) })
	sp.smu.Lock()
	got := sp.scheduled[0]
	sp.smu.Unlock()
	if !got.Unattended || got.Condition != "it will rain" || got.Last == nil || got.Last.Summary != "Sunny." || got.Zone == nil {
		t.Errorf("scheduled: %+v", got)
	}
	met := true
	sp.end(id, pilot.Done, "Rain from 3 pm.", &met)
	var r schedule.Run
	ta.waitFor(t, "the run to end", func() bool {
		select {
		case r = <-out:
			return true
		default:
			return false
		}
	})
	if r.End != string(pilot.Done) || r.Summary != "Rain from 3 pm." || r.Met == nil || !*r.Met {
		t.Errorf("run = %+v", r)
	}
	title, detail := ta.runText(s, r)
	if !strings.Contains(title, locales.SchedMet()) || detail == "" {
		t.Errorf("runText = %q, %q", title, detail)
	}
	// A skipped run says why.
	if _, d := ta.runText(s, schedule.Run{End: schedule.Skipped, Reason: schedule.SkipLocked}); d != locales.SchedSkipLocked(ta.phoneName(id)) {
		t.Errorf("locked: %q", d)
	}
	// With the window in front, a toast.
	ta.focused = true
	ta.notifySchedule(s, r)
	if n := len(ta.toasts); n == 0 || !strings.Contains(ta.toasts[n-1], locales.SchedNotifyTitle("Check the weather")) {
		t.Errorf("toasts = %q", ta.toasts)
	}
}

// payAPI is the fake with one approval Pinky's own agent asks for.
type payAPI struct {
	*fakeapi.API
	mu      sync.Mutex
	needs   []gateway.NeedView
	decided map[string]string
}

func (p *payAPI) Needs() []gateway.NeedView {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []gateway.NeedView
	for _, n := range p.needs {
		if _, ok := p.decided[n.ID]; !ok {
			out = append(out, n)
		}
	}
	return out
}

func (p *payAPI) Decide(id string, d gateway.Decision, note string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.decided[id] = string(d) + ":" + note
	return nil
}

// A run nobody watches never pays: a payment approval is denied at once,
// others wait for the person.
func TestSchedulePayment(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	pa := &payAPI{API: ta.api, decided: map[string]string{}, needs: []gateway.NeedView{
		{ID: "pay", Kind: gateway.NeedApproval, Agent: mcp.BuiltinName, DeviceID: "p1", Rule: "Payments"},
		{ID: "send", Kind: gateway.NeedApproval, Agent: mcp.BuiltinName, DeviceID: "p1", Rule: "Send messages"},
		{ID: "other", Kind: gateway.NeedApproval, Agent: "Claude Code", DeviceID: "p1", Rule: "Payments"},
	}}
	ta.app.api = pa
	h := schedHost{ta.app}
	denied := map[string]bool{}
	h.denyPayments("p1", denied)
	h.denyPayments("p1", denied)
	if len(pa.decided) != 1 || pa.decided["pay"] != string(gateway.Deny)+":"+locales.SchedPaymentDenied() {
		t.Errorf("decided %v", pa.decided)
	}
}
