package history

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Recording is a task's screen recording: one MP4 file in the store's
// recordings folder.
type Recording struct {
	TaskID  string
	Path    string    // absolute
	Started time.Time // the wall time of the file's start
	Length  time.Duration
	Codec   string
	Width   int
	Height  int
	Size    int64
}

// ErrNoRecording means the task has no recording, or its file is gone.
var ErrNoRecording = errors.New("no recording")

// RecordingPath returns where a task's recording goes, by the month it
// started in.
func (s *Store) RecordingPath(taskID string, started time.Time) (string, error) {
	if taskID == "" || strings.ContainsAny(taskID, `/\.`) {
		return "", errors.New("bad task id")
	}
	return filepath.Join(s.recDir, started.Format("2006-01"), taskID+".mp4"), nil
}

// SaveRecording inserts or updates a task's recording.
func (s *Store) SaveRecording(ctx context.Context, r Recording) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rel, err := filepath.Rel(s.dir, r.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return errors.New("the recording is outside the history folder")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO recordings (task_id, path, started_at, length_ms, codec, width, height, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET path=excluded.path, started_at=excluded.started_at, length_ms=excluded.length_ms,
			codec=excluded.codec, width=excluded.width, height=excluded.height, size=excluded.size`,
		r.TaskID, filepath.ToSlash(rel), ms(r.Started), r.Length.Milliseconds(), r.Codec, r.Width, r.Height, r.Size)
	return err
}

// RecordingTemp is where a new file for a recording is written before
// ReplaceRecording puts it in the recording's place.
func RecordingTemp(path string) string { return path + recordingTempSuffix }

const recordingTempSuffix = ".part"

// ReplaceRecording puts the file at RecordingTemp(r.Path) in the place of
// the task's recording at r.Path, and updates its row with r's length,
// codec, size and bytes, as when the recording was made smaller. When the
// task's recording is gone or is another file (the task was deleted while
// the new file was written), it removes the new file and reports false.
// Deleting and pruning wait for it, so a deleted task never gets its file
// back.
func (s *Store) ReplaceRecording(ctx context.Context, r Recording) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp := RecordingTemp(r.Path)
	rel, err := filepath.Rel(s.dir, r.Path)
	if err != nil || strings.HasPrefix(rel, "..") {
		os.Remove(tmp)
		return false, errors.New("the recording is outside the history folder")
	}
	var cur string
	err = s.db.QueryRowContext(ctx, `SELECT path FROM recordings WHERE task_id = ?`, r.TaskID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && cur != filepath.ToSlash(rel)) {
		os.Remove(tmp)
		return false, nil
	}
	if err != nil {
		os.Remove(tmp)
		return false, err
	}
	if err := os.Rename(tmp, r.Path); err != nil {
		// On Windows a replay that has the file open keeps it in place.
		os.Remove(tmp)
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE recordings SET length_ms = ?, codec = ?, width = ?, height = ?, size = ? WHERE task_id = ?`,
		r.Length.Milliseconds(), r.Codec, r.Width, r.Height, r.Size, r.TaskID)
	return true, err
}

// RemoveRecordingTemps removes the new files of recordings that were being
// replaced when Pinky last quit.
func (s *Store) RemoveRecordingTemps() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	err := filepath.WalkDir(s.recDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, recordingTempSuffix) {
			if err := os.Remove(path); err != nil {
				errs = append(errs, err)
			}
		}
		return nil
	})
	return errors.Join(append(errs, err)...)
}

// Recording returns a task's recording, or ErrNoRecording.
func (s *Store) Recording(ctx context.Context, taskID string) (Recording, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := Recording{TaskID: taskID}
	var rel string
	var started, length int64
	err := s.db.QueryRowContext(ctx, `SELECT path, started_at, length_ms, codec, width, height, size FROM recordings WHERE task_id = ?`, taskID).
		Scan(&rel, &started, &length, &r.Codec, &r.Width, &r.Height, &r.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNoRecording
	}
	if err != nil {
		return Recording{}, err
	}
	if strings.Contains(rel, "..") {
		return Recording{}, ErrNoRecording
	}
	r.Path = filepath.Join(s.dir, filepath.FromSlash(rel))
	if _, err := os.Stat(r.Path); err != nil {
		return Recording{}, ErrNoRecording
	}
	r.Started, r.Length = fromMS(started), time.Duration(length)*time.Millisecond
	return r, nil
}

// SetDeviceBackground saves whether agents work on a phone's background
// display.
func (s *Store) SetDeviceBackground(ctx context.Context, deviceID string, on bool) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := 0
	if on {
		v = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_settings (device_id, background) VALUES (?, ?)
		ON CONFLICT(device_id) DO UPDATE SET background=excluded.background`, deviceID, v)
	return err
}

// BackgroundDevices returns the phones the person chose for, whether their
// agents work on a background display (true) or on the main screen (false).
// Phones without a choice are left out.
func (s *Store) BackgroundDevices(ctx context.Context) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, background FROM device_settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var v int
		if err := rows.Scan(&id, &v); err != nil {
			return nil, err
		}
		out[id] = v != 0
	}
	return out, rows.Err()
}

// SetDeviceConnection saves how Pinky connects to a phone; an empty way
// removes the choice (the driver's own rule applies).
func (s *Store) SetDeviceConnection(ctx context.Context, deviceID, way string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if way == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM device_connections WHERE device_id = ?`, deviceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_connections (device_id, way) VALUES (?, ?)
		ON CONFLICT(device_id) DO UPDATE SET way=excluded.way`, deviceID, way)
	return err
}

// DeviceConnections returns how the person chose to connect each phone, by
// phone. Phones without a choice are left out.
func (s *Store) DeviceConnections(ctx context.Context) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, way FROM device_connections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, way string
		if err := rows.Scan(&id, &way); err != nil {
			return nil, err
		}
		out[id] = way
	}
	return out, rows.Err()
}
