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

// ErrRecordingsDirInside means the folder chosen for recordings is Pinky's
// own recordings folder or one in it, which deleting all activity removes
// whole.
var ErrRecordingsDirInside = errors.New("the recordings folder is in Pinky's own recordings folder")

// CheckRecordingsDir reports why dir cannot be the folder new recordings go
// to, and nil when it can; "" is the history folder's own and always can.
// Symbolic links count: a path that leads into Pinky's own recordings
// folder is refused like the folder itself (ErrRecordingsDirInside).
func (s *Store) CheckRecordingsDir(dir string) error {
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return errors.New("the recordings folder is not an absolute path")
	}
	dir = filepath.Clean(dir)
	if within(s.recDir, dir) || within(resolvePath(s.recDir), resolvePath(dir)) {
		return ErrRecordingsDirInside
	}
	return nil
}

// within reports whether path is dir or in it; both are clean and
// absolute.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolvePath returns path with its symbolic links followed. For a path
// that is not there yet, it follows those of the nearest folder above it
// that is, and keeps the rest as it is.
func resolvePath(path string) string {
	rest := ""
	for p := path; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		up := filepath.Dir(p)
		if up == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = up
	}
}

// SetRecordingsDir sets the folder new recordings go to, an absolute path;
// "" is the history folder's own. Recordings made before stay where they
// are, and the store keeps finding them there: each row keeps its file's
// place. While dir is not a folder (a disk that is not plugged in), new
// recordings go to the history folder. It refuses what CheckRecordingsDir
// refuses.
func (s *Store) SetRecordingsDir(dir string) error {
	if err := s.CheckRecordingsDir(dir); err != nil {
		return err
	}
	if dir != "" {
		dir = filepath.Clean(dir)
	}
	s.userRecDir.Store(&dir)
	return nil
}

// newRecordingsDir is the folder new recordings go to now.
func (s *Store) newRecordingsDir() string {
	if p := s.userRecDir.Load(); p != nil && *p != "" {
		if fi, err := os.Stat(*p); err == nil && fi.IsDir() {
			return *p
		}
	}
	return s.recDir
}

// RecordingPath returns where a task's recording goes, by the month it
// started in.
func (s *Store) RecordingPath(taskID string, started time.Time) (string, error) {
	return recordingIn(s.newRecordingsDir(), taskID, started)
}

// HistoryRecordingPath returns where a task's recording goes in the history
// folder, for a recording the chosen folder did not take (it cannot be
// written there).
func (s *Store) HistoryRecordingPath(taskID string, started time.Time) (string, error) {
	return recordingIn(s.recDir, taskID, started)
}

func recordingIn(dir, taskID string, started time.Time) (string, error) {
	if taskID == "" || strings.ContainsAny(taskID, `/\.`) {
		return "", errors.New("bad task id")
	}
	return filepath.Join(dir, started.Format("2006-01"), taskID+".mp4"), nil
}

// rowPath is how a recording's row keeps the file at path: relative to the
// history folder, with slashes, when the file is in its recordings folder,
// as rows always did; else its absolute path, in a folder the person chose,
// whose recordings go one by one when they are deleted.
func (s *Store) rowPath(path string) (string, error) {
	if rel, err := filepath.Rel(s.recDir, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join(filepath.Base(s.recDir), rel)), nil
	}
	if _, ok := recordingFile(s.dir, path); !ok {
		return "", errors.New("the recording is not an MP4 file at an absolute path")
	}
	return path, nil
}

// recordingFile returns the file a row's path names, and false for a path
// that cannot be a recording's: one that leaves the history folder, or an
// absolute one that is not a clean path to an MP4 file.
func recordingFile(dir, row string) (string, bool) {
	if filepath.IsAbs(row) {
		return row, filepath.Clean(row) == row && strings.HasSuffix(row, ".mp4")
	}
	if strings.Contains(row, "..") {
		return "", false
	}
	return filepath.Join(dir, filepath.FromSlash(row)), true
}

// SaveRecording inserts or updates a task's recording.
func (s *Store) SaveRecording(ctx context.Context, r Recording) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rel, err := s.rowPath(r.Path)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO recordings (task_id, path, started_at, length_ms, codec, width, height, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET path=excluded.path, started_at=excluded.started_at, length_ms=excluded.length_ms,
			codec=excluded.codec, width=excluded.width, height=excluded.height, size=excluded.size`,
		r.TaskID, rel, ms(r.Started), r.Length.Milliseconds(), r.Codec, r.Width, r.Height, r.Size)
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
	rel, err := s.rowPath(r.Path)
	if err != nil {
		os.Remove(tmp)
		return false, err
	}
	var cur string
	err = s.db.QueryRowContext(ctx, `SELECT path FROM recordings WHERE task_id = ?`, r.TaskID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && cur != rel) {
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
// replaced when Pinky last quit: any in the history folder's recordings,
// and those next to recordings in folders the person chose, which may hold
// other files.
func (s *Store) RemoveRecordingTemps() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	paths, err := s.strings(context.Background(), `SELECT path FROM recordings`)
	if err != nil {
		errs = append(errs, err)
	}
	for _, row := range paths {
		if f, ok := recordingFile(s.dir, row); ok && filepath.IsAbs(row) {
			if err := os.Remove(RecordingTemp(f)); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	err = filepath.WalkDir(s.recDir, func(path string, d os.DirEntry, err error) error {
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
	f, ok := recordingFile(s.dir, rel)
	if !ok {
		return Recording{}, ErrNoRecording
	}
	r.Path = f
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

// dropRecordingFiles returns the files of the recordings rows names, which
// tx deletes, to remove once it commits. A file in a chosen folder that is
// not there now (a disk that is not plugged in) waits in recording_removals
// instead, for removePending to remove when the folder is back: removing
// it now would find nothing, and nothing would remove it later. One that
// is there but cannot be removed joins it after the commit (removeDropped).
func (s *Store) dropRecordingFiles(ctx context.Context, tx *sql.Tx, rows []string) ([]string, error) {
	var files []string
	for _, row := range rows {
		f, ok := recordingFile(s.dir, row)
		if !ok {
			continue
		}
		if filepath.IsAbs(row) {
			if _, err := os.Stat(filepath.Dir(f)); err != nil {
				if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO recording_removals (path) VALUES (?)`, f); err != nil {
					return nil, err
				}
				continue
			}
		}
		files = append(files, f)
	}
	return files, nil
}

// RemovePendingRecordings removes the recordings of deleted tasks that
// waited for their folder (a disk that was not plugged in), now that it is
// back. Pruning and deleting tasks do it too.
func (s *Store) RemovePendingRecordings(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removePending(ctx)
}

// removePending removes the recordings in recording_removals whose folder
// is back. The caller holds s.mu for writing.
func (s *Store) removePending(ctx context.Context) error {
	paths, err := s.strings(ctx, `SELECT path FROM recording_removals`)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range paths {
		if _, err := os.Stat(filepath.Dir(p)); err != nil {
			continue // still not there
		}
		// A file that cannot be removed yet (a read-only disk) keeps its
		// row, to try again at the next pass.
		if _, err := removeEach([]string{p, RecordingTemp(p)}); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM recording_removals WHERE path = ?`, p); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// removeDropped removes the files of rows that are gone, as removeFiles
// does. A recording in a folder the person chose that cannot be removed
// now (a disk mounted read-only) waits in recording_removals, as one whose
// folder is missing does, for removePending to try again: nothing else
// would find it later. The caller holds s.mu for writing.
func (s *Store) removeDropped(ctx context.Context, files []string) error {
	failed, err := removeEach(files)
	errs := []error{err}
	for _, f := range failed {
		if within(s.recDir, f) || within(s.mediaDir, f) || !strings.HasSuffix(f, ".mp4") {
			continue // not a recording in a chosen folder
		}
		if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO recording_removals (path) VALUES (?)`, f); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
