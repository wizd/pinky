package history

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestPrune checks what retention deletes: a finished task older than the
// cutoff, with the screenshot only it showed, and a recording; a task the
// caller asked to keep; a newer task; and a screenshot a newer task still
// shows. A cancelled context deletes nothing.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	old := time.Now().Add(-40 * 24 * time.Hour).Truncate(time.Millisecond)
	recent := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	shared := saveShot(t, s)
	onlyOld := saveShot(t, s)
	keptShot := saveShot(t, s)

	saveEnded(t, s, "old", old)
	saveEnded(t, s, "kept", old)
	saveEnded(t, s, "new", recent)
	saveStepShot(t, s, "old", 0, onlyOld)
	saveStepShot(t, s, "old", 1, shared)
	saveStepShot(t, s, "new", 0, shared)
	saveStepShot(t, s, "kept", 0, keptShot)
	recPath := saveRecFile(t, s, "old", old)

	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	out, err := s.Prune(ctx, cutoff, []string{"kept"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tasks != 1 || out.Steps != 2 || out.Media != 1 || out.Recordings != 1 {
		t.Fatalf("pruned %+v, want 1 task, 2 steps, 1 screenshot, 1 recording", out)
	}
	if _, ok, err := s.Task(ctx, "old"); err != nil || ok {
		t.Fatalf("old task still there: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Task(ctx, "kept"); err != nil || !ok {
		t.Fatalf("kept task: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Task(ctx, "new"); err != nil || !ok {
		t.Fatalf("new task: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(recPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old recording: %v", err)
	}
	if _, err := s.MediaPath(ctx, onlyOld); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("screenshot only the old task showed: %v", err)
	}
	if _, err := s.MediaPath(ctx, shared); err != nil {
		t.Fatalf("screenshot a newer task still shows: %v", err)
	}
	if _, err := s.MediaPath(ctx, keptShot); err != nil {
		t.Fatalf("screenshot of a task retention was told to keep: %v", err)
	}

	gone, err := s.Prune(context.Background(), cutoff, []string{"kept"})
	if err != nil || gone.Tasks != 0 {
		t.Fatalf("second prune: %+v %v", gone, err)
	}
	// Already cancelled: the read fails before anything is deleted.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Prune(cancelCtx, time.Now().Add(time.Hour), nil); err == nil {
		t.Fatal("prune with a cancelled context succeeded")
	}
	if _, ok, err := s.Task(ctx, "new"); err != nil || !ok {
		t.Fatalf("a failed prune removed a task: ok=%v err=%v", ok, err)
	}
}

// TestDeleteTasks checks deleting one task from Activity: the task goes
// with its steps, approvals, the screenshot only it showed, its recording
// and its session; a screenshot another task's step shows stays, and so
// does a running task the caller keeps, even when asked for.
func TestDeleteTasks(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	shared := saveShot(t, s)
	onlyGone := saveShot(t, s)
	runningShot := saveShot(t, s)
	onlyGoneFile, err := s.MediaPath(ctx, onlyGone)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SaveSession(ctx, Session{ID: "sess", Agent: "a", Started: at.Add(-time.Minute), Ended: at}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTask(ctx, Task{ID: "gone", SessionID: "sess", Title: "gone", State: "done",
		Started: at.Add(-time.Minute), Ended: at}); err != nil {
		t.Fatal(err)
	}
	saveEnded(t, s, "other", at)
	if err := s.SaveTask(ctx, Task{ID: "running", Title: "running", State: "working", Started: at}); err != nil {
		t.Fatal(err)
	}
	saveStepShot(t, s, "gone", 0, onlyGone)
	saveStepShot(t, s, "gone", 1, shared)
	saveStepShot(t, s, "other", 0, shared)
	saveStepShot(t, s, "running", 0, runningShot)
	if err := s.SaveApproval(ctx, Approval{ID: "ap", TaskID: "gone", StepIndex: 1, Kind: "approval", Title: "Pay?",
		Decision: "allow_once", Created: at, Decided: at}); err != nil {
		t.Fatal(err)
	}
	recPath := saveRecFile(t, s, "gone", at)
	runningRec := saveRecFile(t, s, "running", at)

	out, err := s.DeleteTasks(ctx, []string{"gone", "running", "missing"}, []string{"running"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Tasks != 1 || out.Steps != 2 || out.Approvals != 1 || out.Media != 1 || out.Recordings != 1 || out.Sessions != 1 {
		t.Fatalf("deleted %+v, want 1 task, 2 steps, 1 approval, 1 screenshot, 1 recording, 1 session", out)
	}
	if _, ok, err := s.Task(ctx, "gone"); err != nil || ok {
		t.Fatalf("deleted task still there: ok=%v err=%v", ok, err)
	}
	if steps, err := s.Steps(ctx, "gone"); err != nil || len(steps) != 0 {
		t.Fatalf("deleted task's steps: %v %v", steps, err)
	}
	if aps, err := s.Approvals(ctx, "gone"); err != nil || len(aps) != 0 {
		t.Fatalf("deleted task's approvals: %v %v", aps, err)
	}
	if _, err := s.Recording(ctx, "gone"); !errors.Is(err, ErrNoRecording) {
		t.Fatalf("deleted task's recording: %v", err)
	}
	if _, err := os.Stat(recPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted task's recording file: %v", err)
	}
	if _, err := s.MediaPath(ctx, onlyGone); !errors.Is(err, ErrNoMedia) {
		t.Fatalf("screenshot only the deleted task showed: %v", err)
	}
	if _, err := os.Stat(onlyGoneFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("screenshot file only the deleted task showed: %v", err)
	}
	if _, err := s.MediaPath(ctx, shared); err != nil {
		t.Fatalf("screenshot another task still shows: %v", err)
	}
	var sessions int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE id = 'sess'`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("the deleted task's session: %d %v", sessions, err)
	}

	// The running task is untouched, with its steps, screenshot and recording.
	if _, ok, err := s.Task(ctx, "running"); err != nil || !ok {
		t.Fatalf("running task: ok=%v err=%v", ok, err)
	}
	if steps, err := s.Steps(ctx, "running"); err != nil || len(steps) != 1 {
		t.Fatalf("running task's steps: %v %v", steps, err)
	}
	if _, err := s.MediaPath(ctx, runningShot); err != nil {
		t.Fatalf("running task's screenshot: %v", err)
	}
	if _, err := os.Stat(runningRec); err != nil {
		t.Fatalf("running task's recording: %v", err)
	}
	if _, ok, err := s.Task(ctx, "other"); err != nil || !ok {
		t.Fatalf("other task: ok=%v err=%v", ok, err)
	}

	// Again, and with nothing to delete: no error, nothing deleted.
	for _, ids := range [][]string{{"gone"}, nil, {"running"}} {
		if again, err := s.DeleteTasks(ctx, ids, []string{"running"}); err != nil || again != (Pruned{}) {
			t.Fatalf("delete %v again: %+v %v", ids, again, err)
		}
	}
}

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func saveShot(t *testing.T, s *Store) string {
	t.Helper()
	m, err := s.SaveScreenshot(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func saveEnded(t *testing.T, s *Store, id string, at time.Time) {
	t.Helper()
	err := s.SaveTask(context.Background(), Task{
		ID: id, Title: id, State: "done", Started: at.Add(-time.Minute), Ended: at,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveStepShot(t *testing.T, s *Store, task string, index int, media string) {
	t.Helper()
	err := s.SaveStep(context.Background(), Step{
		TaskID: task, Index: index, Tool: "phone_tap", Text: "Tapped",
		Status: "settled", Started: time.Now(), Before: media,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func saveRecFile(t *testing.T, s *Store, task string, at time.Time) string {
	t.Helper()
	path, err := s.RecordingPath(task, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a real recording"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = s.SaveRecording(context.Background(), Recording{
		TaskID: task, Path: path, Started: at, Length: time.Second, Codec: "h264", Width: 8, Height: 8, Size: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// readOnly makes dir read-only until the test ends, so the files in it
// cannot be removed, as on a read-only disk. It skips the test where that
// does not stop removing: on Windows, and for root.
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("no read-only folders this way")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

func writable(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestDeleteTasksRemovesWhatItCan checks that a file that cannot be removed
// leaves no other of the same deletion behind: the task's screenshot goes
// though its recording cannot.
func TestDeleteTasksRemovesWhatItCan(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour)
	saveEnded(t, s, "t1", at)
	shot := saveShot(t, s)
	saveStepShot(t, s, "t1", 0, shot)
	shotFile, err := s.MediaPath(ctx, shot)
	if err != nil {
		t.Fatal(err)
	}
	rec := saveRecFile(t, s, "t1", at)
	readOnly(t, filepath.Dir(rec))

	if _, err := s.DeleteTasks(ctx, []string{"t1"}, nil); err == nil {
		t.Error("DeleteTasks reported no error for the recording it could not remove")
	}
	if _, err := os.Stat(shotFile); !os.IsNotExist(err) {
		t.Errorf("the screenshot is still there: %v", err)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Errorf("the recording in the read-only folder is gone: %v", err)
	}
}

// TestEndUnfinished checks what a start ends of a Pinky that was killed: a
// task without an end, at the end of its last step, or when it started
// without a step; and a session without an end, when its last task ended.
// What had ended stays as it was.
func TestEndUnfinished(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(s.SaveSession(ctx, Session{ID: "killed", Started: at}))
	save(s.SaveSession(ctx, Session{ID: "idle", Started: at}))
	save(s.SaveSession(ctx, Session{ID: "closed", Started: at, Ended: at.Add(time.Minute), EndReason: "disconnected"}))
	save(s.SaveTask(ctx, Task{ID: "steps", SessionID: "killed", State: "working", Started: at}))
	save(s.SaveStep(ctx, Step{TaskID: "steps", Index: 0, Started: at.Add(time.Second), Elapsed: time.Second}))
	save(s.SaveStep(ctx, Step{TaskID: "steps", Index: 1, Started: at.Add(10 * time.Second), Elapsed: 2 * time.Second}))
	save(s.SaveTask(ctx, Task{ID: "nosteps", SessionID: "killed", State: "waiting", Started: at.Add(time.Second)}))
	save(s.SaveTask(ctx, Task{ID: "done", SessionID: "closed", State: "done", Outcome: "done", Started: at, Ended: at.Add(time.Minute)}))

	n, err := s.EndUnfinished(ctx, "failed", "gateway stopped", "disconnected", "withdrawn")
	if err != nil || n != 2 {
		t.Fatalf("ended %d tasks, want 2: %v", n, err)
	}
	for id, want := range map[string]Task{
		"steps":   {State: "failed", Outcome: "gateway stopped", Ended: at.Add(12 * time.Second)},
		"nosteps": {State: "failed", Outcome: "gateway stopped", Ended: at.Add(time.Second)},
		"done":    {State: "done", Outcome: "done", Ended: at.Add(time.Minute)},
	} {
		got, ok, err := s.Task(ctx, id)
		if err != nil || !ok {
			t.Fatalf("task %s: ok=%v err=%v", id, ok, err)
		}
		if got.State != want.State || got.Outcome != want.Outcome || !got.Ended.Equal(want.Ended) {
			t.Errorf("task %s: %s, %q, ended %v; want %s, %q, ended %v", id, got.State, got.Outcome, got.Ended,
				want.State, want.Outcome, want.Ended)
		}
	}
	for id, want := range map[string]time.Time{
		"killed": at.Add(12 * time.Second), "idle": at, "closed": at.Add(time.Minute),
	} {
		var ended int64
		var reason string
		if err := s.db.QueryRow(`SELECT ended_at, end_reason FROM sessions WHERE id = ?`, id).Scan(&ended, &reason); err != nil {
			t.Fatal(err)
		}
		if ended != want.UnixMilli() || reason != "disconnected" {
			t.Errorf("session %s: ended %v (%s), want %v", id, time.UnixMilli(ended), reason, want)
		}
	}
	// Nothing is left for a second start.
	if n, err := s.EndUnfinished(ctx, "failed", "gateway stopped", "disconnected", "withdrawn"); err != nil || n != 0 {
		t.Fatalf("a second start ended %d tasks: %v", n, err)
	}
}

// TestEndUnfinishedWithdrawsApprovals checks the approvals a Pinky that was
// killed left open: the start takes them back and closes their steps, and
// leaves an answered one as it is.
func TestEndUnfinishedWithdrawsApprovals(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(s.SaveSession(ctx, Session{ID: "killed", Started: at}))
	save(s.SaveTask(ctx, Task{ID: "asked", SessionID: "killed", State: "waiting", Started: at}))
	save(s.SaveStep(ctx, Step{TaskID: "asked", Index: 0, Status: "settled", Started: at.Add(time.Second), Elapsed: time.Second}))
	save(s.SaveApproval(ctx, Approval{ID: "answered", TaskID: "asked", StepIndex: 0, Decision: "allow",
		Created: at.Add(time.Second), Decided: at.Add(2 * time.Second), Waited: time.Second}))
	save(s.SaveStep(ctx, Step{TaskID: "asked", Index: 1, Status: "approval_pending", Started: at.Add(10 * time.Second)}))
	save(s.SaveApproval(ctx, Approval{ID: "open", TaskID: "asked", StepIndex: 1, Created: at.Add(10 * time.Second)}))

	if n, err := s.EndUnfinished(ctx, "failed", "gateway stopped", "disconnected", "withdrawn"); err != nil || n != 1 {
		t.Fatalf("ended %d tasks, want 1: %v", n, err)
	}
	steps, err := s.Steps(ctx, "asked")
	if err != nil || len(steps) != 2 {
		t.Fatalf("%d steps: %v", len(steps), err)
	}
	if steps[0].Status != "settled" || steps[1].Status != "withdrawn" {
		t.Errorf("steps: %q, %q; want settled, withdrawn", steps[0].Status, steps[1].Status)
	}
	aps, err := s.Approvals(ctx, "asked")
	if err != nil || len(aps) != 2 {
		t.Fatalf("%d approvals: %v", len(aps), err)
	}
	if a := aps[0]; a.Decision != "allow" || !a.Decided.Equal(at.Add(2*time.Second)) {
		t.Errorf("the answered approval: %q, decided %v", a.Decision, a.Decided)
	}
	if a := aps[1]; a.Decision != "withdrawn" || !a.Decided.Equal(at.Add(10*time.Second)) {
		t.Errorf("the open approval: %q, decided %v; want withdrawn when it was asked", a.Decision, a.Decided)
	}
}

// TestEndAllowedSteps checks the steps a Pinky that was killed left
// waiting: the one the person had allowed, which was under way, becomes
// unsure; one the person denied, one nobody answered and one that finished
// stay as they are.
func TestEndAllowedSteps(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	save := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	save(s.SaveSession(ctx, Session{ID: "killed", Started: at}))
	save(s.SaveTask(ctx, Task{ID: "pay", SessionID: "killed", State: "working", Started: at}))
	for i, c := range []struct{ status, decision string }{
		{"settled", "allow_once"},
		{"approval_pending", "deny"},
		{"approval_pending", ""},
		{"approval_pending", "allow_task"},
	} {
		save(s.SaveStep(ctx, Step{TaskID: "pay", Index: i, Status: c.status, Started: at.Add(time.Duration(i) * time.Second)}))
		save(s.SaveApproval(ctx, Approval{ID: "ap" + strconv.Itoa(i), TaskID: "pay", StepIndex: i, Decision: c.decision,
			Created: at.Add(time.Duration(i) * time.Second)}))
	}

	n, err := s.EndAllowedSteps(ctx, "approval_pending", "uncertain", "allow_once", "allow_task")
	if err != nil || n != 1 {
		t.Fatalf("%d steps changed, want 1: %v", n, err)
	}
	steps, err := s.Steps(ctx, "pay")
	if err != nil || len(steps) != 4 {
		t.Fatalf("%d steps: %v", len(steps), err)
	}
	for i, want := range []string{"settled", "approval_pending", "approval_pending", "uncertain"} {
		if steps[i].Status != want {
			t.Errorf("step %d: %q, want %q", i, steps[i].Status, want)
		}
	}
}
