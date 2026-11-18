package ui

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/egoist/mygo"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/ui/fakeapi"
)

// recDirAPI is the fake API with the gateway's check of a recordings
// folder: one in own is refused.
type recDirAPI struct {
	*fakeapi.API
	own string
}

func (r recDirAPI) CheckRecordingsDir(dir string) error {
	if rel, err := filepath.Rel(r.own, dir); err == nil && filepath.IsLocal(rel) {
		return gateway.ErrRecordingsDirInside
	}
	return nil
}

// TestChooseRecordingsFolderRefused checks that Settings › Privacy does not
// keep a recordings folder the gateway would refuse: it says why, and the
// setting stays as it was.
func TestChooseRecordingsFolderRefused(t *testing.T) {
	ta := newTestApp(t, fakeapi.OnePhone)
	own := filepath.Join(t.TempDir(), "recordings")
	ta.app.api = recDirAPI{API: ta.api, own: own}
	var reported []Settings
	ta.opt.OnSettings = func(_, s Settings) { reported = append(reported, s) }
	pick := ""
	ta.openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{pick}, nil }
	choose := func(dir string) {
		t.Helper()
		pick = dir
		ta.chooseRecordingsFolder(ta.settings.RecordingsDir)
		ta.waitFor(t, "the folder picker to close", func() bool { return !ta.set.picking })
	}

	choose(filepath.Join(own, "mine"))
	if ta.settings.RecordingsDir != "" || len(reported) != 0 {
		t.Errorf("a refused folder was kept: %q, reported %v", ta.settings.RecordingsDir, reported)
	}
	if !slices.Contains(ta.toasts, locales.SettingsPrivacyRecordFolderInside()) {
		t.Errorf("toasts %q", ta.toasts)
	}

	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	choose(elsewhere)
	if ta.settings.RecordingsDir != elsewhere || len(reported) != 1 || reported[0].RecordingsDir != elsewhere {
		t.Errorf("a good folder was not kept: %q, reported %v", ta.settings.RecordingsDir, reported)
	}
}
