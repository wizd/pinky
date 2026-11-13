package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
)

// fakeTasks plays a task through its states, one per version, as Pinky's
// own agent would: Wait moves to the next one, and blocks after the last
// until its caller gives up. A state that waits for the person is seen
// twice through Task before the next, as a person who takes a moment.
type fakeTasks struct {
	mu        sync.Mutex
	states    []mcp.Task
	at        int
	seen      int
	req       mcp.TaskRequest
	cancelled bool
	// steerBlock, when set, holds Steer until it is closed; steering
	// says Steer was called.
	steerBlock chan struct{}
	steering   atomic.Bool
}

func (f *fakeTasks) Run(req mcp.TaskRequest) (mcp.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.req = req
	return f.states[0], nil
}

func (f *fakeTasks) Task(_, id string) (mcp.Task, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != f.states[0].ID {
		return mcp.Task{}, false
	}
	if f.states[f.at].Waiting != "" && f.at+1 < len(f.states) {
		if f.seen++; f.seen > 1 {
			f.at++
		}
	}
	return f.states[f.at], true
}

func (f *fakeTasks) List(string) []mcp.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []mcp.Task{f.states[f.at]}
}

func (f *fakeTasks) Wait(ctx context.Context, _, _ string, version int) (mcp.Task, error) {
	f.mu.Lock()
	if version+1 < len(f.states) {
		f.at = version + 1
		t := f.states[f.at]
		f.mu.Unlock()
		return t, nil
	}
	t := f.states[f.at]
	f.mu.Unlock()
	<-ctx.Done()
	return t, ctx.Err()
}

func (f *fakeTasks) Cancel(_, _ string) (mcp.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = true
	t := f.states[f.at]
	t.Ended, t.End = time.Now(), "cancelled"
	return t, nil
}

func (f *fakeTasks) Steer(_, _, _ string) (mcp.Task, error) {
	if f.steerBlock != nil {
		f.steering.Store(true)
		<-f.steerBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[f.at], nil
}

// taskStates is a task on the demo phone that takes two steps, waiting
// for the person between them when wait is set, and ends as end.
func taskStates(end, wait string) []mcp.Task {
	base := mcp.Task{ID: "t0123456789ab", Device: "pixel", Task: "Turn on Airplane mode", Model: "test-model",
		Started: time.Now(), MaxSteps: 100, MaxTime: 20 * time.Minute, MaxTok: 1000, First: 1}
	step := func(n int, intent string) mcp.Task {
		t := base
		t.Steps = n
		for i := 1; i <= n; i++ {
			t.Log = append(t.Log, mcp.TaskStep{Tool: "phone_tap", Status: "settled", Intent: []string{"Open Settings", intent}[min(i-1, 1)]})
		}
		return t
	}
	states := []mcp.Task{base, step(1, "")}
	if wait != "" {
		w := step(1, "")
		w.Waiting = wait
		states = append(states, w)
	}
	last := step(2, "Turn on Airplane mode")
	last.Ended, last.End, last.Summary = time.Now(), end, "Airplane mode is on."
	states = append(states, last)
	for i := range states {
		states[i].Version = i
	}
	return states
}

// taskHarness serves the MCP tools of a gateway with the demo phone and
// the fake tasks; nil tasks leaves the phone_task_* tools off, as in
// `pinky daemon`.
func taskHarness(t *testing.T, tasks mcp.Tasks) *mcp.Server {
	t.Helper()
	dev := demo.NewDevice("pixel", "Pixel 9", driver.Android)
	g, err := gateway.New(gateway.Options{Providers: []driver.Provider{demo.NewProvider(driver.Android, dev)},
		TrustNewAgents: true, NoScreenshots: true, DiscoverEvery: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for len(g.Devices()) == 0 || g.Devices()[0].Status == gateway.DeviceOffline {
		if time.Now().After(deadline) {
			t.Fatal("the demo phone did not come online")
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv := mcp.New(g, "test")
	if tasks != nil {
		srv.SetTasks(tasks)
	}
	return srv
}

// taskConnect connects `pinky task`'s client, named name, to srv.
func taskConnect(t *testing.T, srv *mcp.Server, name string, steps *stepLog) *sdk.ClientSession {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	go srv.Serve(context.Background(), st)
	cs, err := taskClient(name, steps).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// taskBuffer is a bytes.Buffer that the client's notification handler may
// write from another goroutine while the test reads it. (e2e_test.go's
// syncBuffer is not built on Windows.)
type taskBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *taskBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *taskBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return -1
}

// TestTaskRun: run hands the task over as the agent "Pinky CLI", prints
// the steps and the person's wait on stderr, once, and only the status
// line and summary on stdout; how the task ended gives the exit status.
func TestTaskRun(t *testing.T) {
	saved := taskPoll
	taskPoll = 10 * time.Millisecond
	t.Cleanup(func() { taskPoll = saved })
	for _, c := range []struct {
		end  string
		code int
	}{{"done", 0}, {"failed", exitTaskEnded}, {"bad_key", exitTaskError}} {
		t.Run(c.end, func(t *testing.T) {
			tasks := &fakeTasks{states: taskStates(c.end, "approval")}
			var buf taskBuffer
			log := &stepLog{w: &buf}
			cs := taskConnect(t, taskHarness(t, tasks), taskAgentName, log)
			var out bytes.Buffer
			err := task(context.Background(), cs, taskOptions{sub: "run", text: "Turn on Airplane mode", apps: []string{"Settings"}, readOnly: true}, &out, log)
			if got := exitCode(err); got != c.code {
				t.Fatalf("exit status %d (%v), want %d", got, err, c.code)
			}
			if tasks.req.Agent.Name != "Pinky CLI" || tasks.req.Device != "pixel" || !tasks.req.ReadOnly ||
				len(tasks.req.Apps) != 1 || tasks.req.Apps[0] != "Settings" {
				t.Errorf("handed over %+v", tasks.req)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 || !strings.HasPrefix(lines[0], c.end+" · task_id: t0123456789ab") {
				t.Errorf("stdout:\n%s", out.String())
			}
			if c.end == "done" && !strings.Contains(lines[len(lines)-1], "Airplane mode is on.") {
				t.Errorf("no summary on stdout:\n%s", out.String())
			}
			l := buf.String()
			if !strings.Contains(l, "1. phone_tap settled — Open Settings") || !strings.Contains(l, "2. phone_tap settled — Turn on Airplane mode") {
				t.Errorf("steps on stderr:\n%s", l)
			}
			if n := strings.Count(l, "Waiting for you in Pinky: approval"); n != 1 {
				t.Errorf("the wait was told %d times:\n%s", n, l)
			}
		})
	}
}

// TestTaskDetach: run -detach prints only the task_id on stdout.
func TestTaskDetach(t *testing.T) {
	var buf taskBuffer
	log := &stepLog{w: &buf}
	cs := taskConnect(t, taskHarness(t, &fakeTasks{states: taskStates("done", "")}), taskAgentName, log)
	var out bytes.Buffer
	if err := task(context.Background(), cs, taskOptions{sub: "run", text: "Turn on Airplane mode", detach: true}, &out, log); err != nil {
		t.Fatal(err)
	}
	if out.String() != "t0123456789ab\n" {
		t.Errorf("stdout %q", out.String())
	}
}

// TestTaskInterrupt: Ctrl-C while the task runs cancels it, and exits 130.
func TestTaskInterrupt(t *testing.T) {
	states := taskStates("done", "")
	tasks := &fakeTasks{states: states[:2]} // it never ends by itself
	var buf taskBuffer
	log := &stepLog{w: &buf}
	cs := taskConnect(t, taskHarness(t, tasks), taskAgentName, log)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for !strings.Contains(buf.String(), "1. phone_tap") {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	var out bytes.Buffer
	err := task(ctx, cs, taskOptions{sub: "run", text: "Turn on Airplane mode"}, &out, log)
	if got := exitCode(err); got != exitInterrupted {
		t.Fatalf("exit status %d (%v), want %d", got, err, exitInterrupted)
	}
	tasks.mu.Lock()
	cancelled := tasks.cancelled
	tasks.mu.Unlock()
	if !cancelled || !strings.Contains(buf.String(), "Cancelled task t0123456789ab.") {
		t.Errorf("not cancelled; stderr:\n%s", buf.String())
	}
}

// TestTaskAs: -as names the agent; a client calling itself Pinky shows
// as "Pinky (external)", never as Pinky's own agent.
func TestTaskAs(t *testing.T) {
	for name, want := range map[string]string{"Nightly tests": "Nightly tests", "Pinky": "Pinky (external)"} {
		tasks := &fakeTasks{states: taskStates("done", "")}
		var buf taskBuffer
		log := &stepLog{w: &buf}
		cs := taskConnect(t, taskHarness(t, tasks), name, log)
		if err := task(context.Background(), cs, taskOptions{sub: "run", text: "Turn on Airplane mode", detach: true}, &bytes.Buffer{}, log); err != nil {
			t.Fatal(err)
		}
		if tasks.req.Agent.Name != want {
			t.Errorf("-as %q: the agent is %q, want %q", name, tasks.req.Agent.Name, want)
		}
	}
}

// TestTaskOnce: status, cancel and list print Pinky's answer; without
// Pinky's window (no tasks) a call is refused with exit status 2.
func TestTaskOnce(t *testing.T) {
	tasks := &fakeTasks{states: taskStates("done", "")}
	var buf taskBuffer
	log := &stepLog{w: &buf}
	cs := taskConnect(t, taskHarness(t, tasks), taskAgentName, log)
	for _, o := range []taskOptions{{sub: "status", id: "t0123456789ab"}, {sub: "list"}, {sub: "cancel", id: "t0123456789ab"}} {
		var out bytes.Buffer
		if err := task(context.Background(), cs, o, &out, log); err != nil {
			t.Fatalf("%s: %v", o.sub, err)
		}
		if !strings.Contains(out.String(), "t0123456789ab") {
			t.Errorf("%s: stdout:\n%s", o.sub, out.String())
		}
	}
	if err := task(context.Background(), cs, taskOptions{sub: "status", id: "t-nope"}, &bytes.Buffer{}, log); exitCode(err) != exitTaskError {
		t.Errorf("an unknown task: %v", err)
	}

	cs = taskConnect(t, taskHarness(t, nil), taskAgentName, log)
	err := task(context.Background(), cs, taskOptions{sub: "run", text: "Turn on Airplane mode"}, &bytes.Buffer{}, log)
	if exitCode(err) != exitTaskError || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("without Pinky's window: %v", err)
	}
}

// TestTaskInterruptSteer: Ctrl-C while steer's message is on its way
// cancels the task, whose task_id is known, and exits 130.
func TestTaskInterruptSteer(t *testing.T) {
	tasks := &fakeTasks{states: taskStates("done", "")[:2], steerBlock: make(chan struct{})}
	var buf taskBuffer
	log := &stepLog{w: &buf}
	cs := taskConnect(t, taskHarness(t, tasks), taskAgentName, log)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for !tasks.steering.Load() {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		time.Sleep(20 * time.Millisecond)
		close(tasks.steerBlock)
	}()
	err := task(ctx, cs, taskOptions{sub: "steer", id: "t0123456789ab", text: "Use the quick settings instead"}, &bytes.Buffer{}, log)
	if got := exitCode(err); got != exitInterrupted {
		t.Fatalf("exit status %d (%v), want %d", got, err, exitInterrupted)
	}
	tasks.mu.Lock()
	cancelled := tasks.cancelled
	tasks.mu.Unlock()
	if !cancelled {
		t.Errorf("the task was not cancelled; stderr:\n%s", buf.String())
	}
}

// TestStepLog: each step is written once, also when an answer lists an
// earlier step after a later one was notified.
func TestStepLog(t *testing.T) {
	var buf bytes.Buffer
	log := &stepLog{w: &buf}
	log.step(2, "phone_tap settled — Open Wi-Fi")
	log.steps("running · task_id: t1 · Pixel 9 · steps: 2\nPinky's agent is working on it.\nSteps:\n1. phone_launch settled — Open Settings\n2. phone_tap settled — Open Wi-Fi")
	want := "2. phone_tap settled — Open Wi-Fi\n1. phone_launch settled — Open Settings\n"
	if buf.String() != want {
		t.Errorf("wrote %q, want %q", buf.String(), want)
	}
}

// TestTaskSummaryLines: a summary of several lines reaches stdout whole,
// without the task's words or its steps.
func TestTaskSummaryLines(t *testing.T) {
	states := taskStates("done", "")
	states[len(states)-1].Summary = "Found three alarms:\n7:00\n7:30"
	var buf taskBuffer
	log := &stepLog{w: &buf}
	cs := taskConnect(t, taskHarness(t, &fakeTasks{states: states}), taskAgentName, log)
	var out bytes.Buffer
	if err := task(context.Background(), cs, taskOptions{sub: "run", text: "List my alarms"}, &out, log); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, "Found three alarms:\n7:00\n7:30\n") || strings.Contains(o, "Task: ") || strings.Contains(o, "Steps:") {
		t.Errorf("stdout:\n%s", o)
	}
}

// TestTaskUsage: wrong flags and arguments exit 2, before connecting.
func TestTaskUsage(t *testing.T) {
	saved := taskSession
	taskSession = func(context.Context, string, *stepLog) (*sdk.ClientSession, error) {
		t.Fatal("connected for a usage error")
		return nil, nil
	}
	t.Cleanup(func() { taskSession = saved })
	for _, args := range [][]string{{}, {"nope"}, {"run"}, {"run", "-unknown", "x"}, {"run", "-read-only=maybe", "x"},
		{"status"}, {"steer", "t1"}, {"list", "x"}} {
		err := runTask(args)
		var ee *exitError
		if !errors.Is(err, flag.ErrHelp) && !(errors.As(err, &ee) && ee.code == exitTaskError) {
			t.Errorf("%q: %v, want exit status 2", args, err)
		}
	}
}

// TestTaskMessage: the message ends at the task's own line, also when the
// summary holds a "Steps:" heading or task words of its own.
func TestTaskMessage(t *testing.T) {
	summary := "Pinky's agent did the task. Its summary: Procedure:\nSteps:\n1. Open Settings\nTask: “a note”\n2. Read Wi-Fi"
	tail := "\nTask: “Read Wi-Fi”. test-model: 2 of 100 steps."
	for name, text := range map[string]string{
		"no steps":   "done · task_id: t1 · Pixel 9 · steps: 2\n" + summary + tail,
		"with steps": "done · task_id: t1 · Pixel 9 · steps: 2\n" + summary + tail + "\nSteps:\n2. phone_tap settled",
	} {
		if got := messageOf(text); got != summary {
			t.Errorf("%s: %q, want %q", name, got, summary)
		}
	}
}
