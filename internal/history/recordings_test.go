package history

import (
	"context"
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
