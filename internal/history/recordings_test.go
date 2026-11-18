package history

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceRecording(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)

	small := []byte("smaller")
	if err := os.WriteFile(RecordingTemp(path), small, 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ReplaceRecording(ctx, Recording{TaskID: "t1", Path: path, Length: 2 * time.Second, Codec: "h264",
		Width: 4, Height: 6, Size: int64(len(small))})
	if err != nil || !ok {
		t.Fatalf("ReplaceRecording: %v, %v", ok, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(small) {
		t.Fatalf("the file holds %q, %v", got, err)
	}
	r, err := s.Recording(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Length != 2*time.Second || r.Codec != "h264" || r.Width != 4 || r.Height != 6 || r.Size != int64(len(small)) ||
		!r.Started.Equal(at) {
		t.Errorf("row %+v", r)
	}
	if _, err := os.Stat(RecordingTemp(path)); !os.IsNotExist(err) {
		t.Errorf("the new file is still there: %v", err)
	}
}

func TestReplaceRecordingOfADeletedTask(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	if err := os.WriteFile(RecordingTemp(path), []byte("smaller"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTasks(ctx, []string{"t1"}, nil); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ReplaceRecording(ctx, Recording{TaskID: "t1", Path: path, Codec: "h264"})
	if err != nil || ok {
		t.Fatalf("ReplaceRecording of a deleted task: %v, %v", ok, err)
	}
	for _, p := range []string{path, RecordingTemp(path)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s is back: %v", filepath.Base(p), err)
		}
	}
}

func TestRemoveRecordingTemps(t *testing.T) {
	s := openStore(t)
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	if err := os.WriteFile(RecordingTemp(path), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRecordingTemps(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(RecordingTemp(path)); !os.IsNotExist(err) {
		t.Errorf("the unfinished file is still there: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the recording is gone: %v", err)
	}
}

func TestRecordingsDir(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	at := time.Now()
	saveEnded(t, s, "before", at)
	before := saveRecFile(t, s, "before", at)

	dir := t.TempDir()
	if err := s.SetRecordingsDir(dir); err != nil {
		t.Fatal(err)
	}
	saveEnded(t, s, "after", at)
	after := saveRecFile(t, s, "after", at)
	if want := filepath.Join(dir, at.Format("2006-01"), "after.mp4"); after != want {
		t.Fatalf("a new recording goes to %s, want %s", after, want)
	}
	// The one made before stays where it is, and both are found after the
	// folder changes back.
	if err := s.SetRecordingsDir(""); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"before": before, "after": after} {
		r, err := s.Recording(ctx, id)
		if err != nil || r.Path != want {
			t.Errorf("Recording(%s) = %s, %v; want %s", id, r.Path, err, want)
		}
	}
	if _, err := s.DeleteTasks(ctx, []string{"after"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(after); !os.IsNotExist(err) {
		t.Errorf("deleting the task left its recording: %v", err)
	}
	if err := s.SetRecordingsDir("relative"); err == nil {
		t.Error("a relative folder was taken")
	}
}

func TestRecordingsDirNotThere(t *testing.T) {
	s := openStore(t)
	if err := s.SetRecordingsDir(filepath.Join(t.TempDir(), "unplugged")); err != nil {
		t.Fatal(err)
	}
	path, err := s.RecordingPath("t1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rel, err := filepath.Rel(s.Dir(), path); err != nil || rel != filepath.Join("recordings", time.Now().Format("2006-01"), "t1.mp4") {
		t.Errorf("with the folder not there, a recording goes to %s", path)
	}
}

func TestWipeKeepsTheRecordingsFolder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	dir := t.TempDir()
	other := filepath.Join(dir, "mine.txt")
	if err := os.WriteFile(other, []byte("the person's own file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordingsDir(dir); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	if err := os.WriteFile(RecordingTemp(path), []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRecordingTemps(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(RecordingTemp(path)); !os.IsNotExist(err) {
		t.Errorf("the unfinished file is still there: %v", err)
	}
	if err := s.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Wipe left the recording: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Wipe removed the folder's other files: %v", err)
	}
}

func TestRecordingsDirUnplugged(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.Mkdir(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordingsDir(disk); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)

	// The disk goes away; the task is deleted meanwhile.
	away := disk + "-away"
	if err := os.Rename(disk, away); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTasks(ctx, []string{"t1"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx, time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	// Back again: the next prune removes the recording.
	if err := os.Rename(away, disk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the recording should still be on the disk until a prune: %v", err)
	}
	if _, err := s.Prune(ctx, time.Now().Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the recording of the deleted task is still there: %v", err)
	}
	if left, err := s.strings(ctx, `SELECT path FROM recording_removals`); err != nil || len(left) != 0 {
		t.Errorf("recording_removals holds %v, %v", left, err)
	}
}

func TestRecordingsDirInHistoryFolder(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	mine := filepath.Join(s.Dir(), "mine")
	if err := os.Mkdir(mine, 0o700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(mine, "notes.txt")
	if err := os.WriteFile(other, []byte("the person's own file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordingsDir(mine); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	if err := s.Wipe(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Wipe left the recording: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("Wipe removed the folder's other files: %v", err)
	}
}

func TestReplaceRecordingInRecordingsDir(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.SetRecordingsDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	// The folder changes back before the recording is made smaller.
	if err := s.SetRecordingsDir(""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(RecordingTemp(path), []byte("smaller"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ReplaceRecording(ctx, Recording{TaskID: "t1", Path: path, Codec: "h264", Size: 7})
	if err != nil || !ok {
		t.Fatalf("ReplaceRecording: %v, %v", ok, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "smaller" {
		t.Errorf("the file holds %q, %v", got, err)
	}
}

func TestRecordingsDirNotInPinkysRecordings(t *testing.T) {
	s := openStore(t)
	if err := s.SetRecordingsDir(filepath.Join(s.Dir(), "recordings", "mine")); err == nil {
		t.Error("a folder in Pinky's own recordings folder was taken")
	}
}

// pending returns the recordings waiting in recording_removals.
func pending(t *testing.T, s *Store) []string {
	t.Helper()
	left, err := s.strings(context.Background(), `SELECT path FROM recording_removals`)
	if err != nil {
		t.Fatal(err)
	}
	return left
}

// TestRecordingsDirReadOnly checks that a recording in a chosen folder that
// is there but cannot be removed (a disk mounted read-only) is not
// forgotten: it waits until it can be removed.
func TestRecordingsDirReadOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if err := s.SetRecordingsDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	readOnly(t, filepath.Dir(path))

	if _, err := s.DeleteTasks(ctx, []string{"t1"}, nil); err == nil {
		t.Error("DeleteTasks reported no error for the recording it could not remove")
	}
	if left := pending(t, s); len(left) != 1 || left[0] != path {
		t.Fatalf("recording_removals holds %v, want the recording", left)
	}
	// Still read-only at the next pass: it keeps waiting.
	if err := s.RemovePendingRecordings(ctx); err == nil {
		t.Error("RemovePendingRecordings reported no error for the recording it could not remove")
	}
	if left := pending(t, s); len(left) != 1 {
		t.Fatalf("recording_removals holds %v after a failed removal", left)
	}
	writable(t, filepath.Dir(path))
	if err := s.RemovePendingRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the recording of the deleted task is still there: %v", err)
	}
	if left := pending(t, s); len(left) != 0 {
		t.Errorf("recording_removals holds %v", left)
	}
}

// TestRecordingsDirBackReadOnly checks that a recording that waited for its
// folder keeps waiting when the folder comes back read-only.
func TestRecordingsDirBackReadOnly(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	disk := filepath.Join(t.TempDir(), "disk")
	if err := os.Mkdir(disk, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordingsDir(disk); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	saveEnded(t, s, "t1", at)
	path := saveRecFile(t, s, "t1", at)
	away := disk + "-away"
	if err := os.Rename(disk, away); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTasks(ctx, []string{"t1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, disk); err != nil {
		t.Fatal(err)
	}
	readOnly(t, filepath.Dir(path))
	if err := s.RemovePendingRecordings(ctx); err == nil {
		t.Error("RemovePendingRecordings reported no error for the recording it could not remove")
	}
	if left := pending(t, s); len(left) != 1 || left[0] != path {
		t.Fatalf("recording_removals holds %v, want the recording still waiting", left)
	}
	writable(t, filepath.Dir(path))
	if err := s.RemovePendingRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the recording of the deleted task is still there: %v", err)
	}
	if left := pending(t, s); len(left) != 0 {
		t.Errorf("recording_removals holds %v", left)
	}
}

func TestCheckRecordingsDirFollowsLinks(t *testing.T) {
	s := openStore(t)
	own := filepath.Join(s.Dir(), "recordings")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(own)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(own, link); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	for _, dir := range []string{
		own,
		filepath.Join(own, "sub"),
		link,                               // a link to it
		filepath.Join(link, "not", "yet"),  // a folder not made yet, through the link
		filepath.Join(resolved, "2026-11"), // by its resolved path (/private/var for /var on macOS)
	} {
		if err := s.CheckRecordingsDir(dir); !errors.Is(err, ErrRecordingsDirInside) {
			t.Errorf("CheckRecordingsDir(%s) = %v, want ErrRecordingsDirInside", dir, err)
		}
		if err := s.SetRecordingsDir(dir); err == nil {
			t.Errorf("SetRecordingsDir(%s) took it", dir)
		}
	}
	for _, dir := range []string{"", s.Dir(), own + "-mine", filepath.Join(t.TempDir(), "elsewhere")} {
		if err := s.CheckRecordingsDir(dir); err != nil {
			t.Errorf("CheckRecordingsDir(%s) = %v", dir, err)
		}
	}
}
