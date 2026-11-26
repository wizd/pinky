package history

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DIYgod/pinky/internal/screen"
)

// TestOpenScrubsTypedSecrets: history from before scrubbedVersion, with a
// password typed on a virtual phone, one refused on a phone, a code, and a
// key of a hidden passcode keypad, is masked once when opened; other steps
// keep their words, and history written since is left as it is.
func TestOpenScrubsTypedSecrets(t *testing.T) {
	const pw, code = "hunter2-Secret", "804217"
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	if err := s.SaveTask(ctx, Task{ID: "t1", DeviceID: "d1", Agent: "Claude Code", Title: "Sign in", State: "done", Started: now}); err != nil {
		t.Fatal(err)
	}
	at := screen.Rect{X: 10, Y: 20, W: 30, H: 40}
	steps := []Step{
		{Index: 0, Tool: "phone_type", Args: `{"intent":"Sign in with ` + pw + `","ref":12,"text":"` + pw + `"}`, Intent: "Sign in with " + pw,
			Text: "Typed “" + pw + "” into “Password”", ScreenText: `[12] input "Password, ` + pw + `"`, Target: at},
		{Index: 1, Tool: "phone_type", Args: `{"ref":12,"text":"` + pw + `"}`,
			Text: "Blocked by “credentials”: Typed “" + pw + "” into “Sign-in field”", Target: at},
		{Index: 2, Tool: "phone_type", Args: `{"ref":13,"text":"` + code + `"}`, Text: "Typed “" + code + "” into “Verification code”", Target: at},
		{Index: 3, Tool: "phone_tap", Args: `{"ref":30}`, Text: "Blocked by “credentials”: Tapped “8”", Target: at, Point: screen.Point{X: 25, Y: 40}},
		{Index: 4, Tool: "phone_type", Args: `{"ref":14,"text":"sam@example.com"}`, Text: "Typed “sam@example.com” into “Email”", Target: at},
		{Index: 5, Tool: "phone_tap", Args: `{"ref":15}`, Text: "Tapped “Sign in”", Target: at},
	}
	for _, st := range steps {
		st.TaskID, st.Started = "t1", now
		if err := s.SaveStep(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveApproval(ctx, Approval{ID: "a1", TaskID: "t1", StepIndex: 0, Kind: "action", Rule: "credentials",
		Title: "Type “" + pw + "” into “Password”?", Created: now}); err != nil {
		t.Fatal(err)
	}
	// As written before scrubbedVersion.
	if _, err := s.db.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Steps(ctx, "t1")
	if err != nil || len(got) != len(steps) {
		t.Fatalf("steps %+v %v", got, err)
	}
	for _, st := range got[:4] {
		all := strings.Join([]string{st.Text, st.Args, st.Intent, st.ScreenText}, "\n")
		if strings.Contains(all, pw) || strings.Contains(all, code) || st.Tool == "phone_tap" && strings.Contains(st.Args, `"ref"`) {
			t.Errorf("step %d kept the secret or the key:\n%s", st.Index, all)
		}
		if !st.Target.Empty() || st.Point != (screen.Point{}) {
			t.Errorf("step %d kept where it went: %+v", st.Index, st)
		}
	}
	for i, want := range []string{
		"Typed “" + SecretMask + "” into “Password”",
		"Blocked by “credentials”: Typed “" + SecretMask + "” into “Sign-in field”",
		"Typed “" + SecretMask + "” into “Verification code”",
		"Blocked by “credentials”: Tapped “" + secretKey + "”",
		"Typed “sam@example.com” into “Email”",
		"Tapped “Sign in”",
	} {
		if got[i].Text != want {
			t.Errorf("step %d: %q, want %q", i, got[i].Text, want)
		}
	}
	if got[0].Intent != "Sign in with "+SecretMask || !strings.Contains(got[0].ScreenText, `"Password, `+SecretMask+`"`) {
		t.Errorf("step 0: %+v", got[0])
	}
	for _, st := range got[4:] {
		if st.Target != at {
			t.Errorf("an ordinary step lost its target: %+v", st)
		}
	}
	aps, err := s.Approvals(ctx, "t1")
	if err != nil || len(aps) != 1 || aps[0].Title != "Type “"+SecretMask+"” into “Password”?" {
		t.Fatalf("approvals %+v %v", aps, err)
	}

	// Once only: what is written since stays.
	if err := s.SaveStep(ctx, Step{TaskID: "t1", Index: 6, Tool: "phone_type", Args: `{"text":"` + pw + `"}`,
		Text: "Typed “" + pw + "” into “Password”", Started: now}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ = s.Steps(ctx, "t1")
	if len(got) != 7 || !strings.Contains(got[6].Text, pw) {
		t.Errorf("scrubbed again: %+v", got)
	}
}

// TestOpenClearsScrubbedSecrets: the database's files hold none of the
// masked passwords, not even in the space SQLite freed when it rewrote the
// steps, whether this Pinky masks them or an earlier one (version 8) did.
func TestOpenClearsScrubbedSecrets(t *testing.T) {
	const pw = "hunter2-Secret"
	ctx := context.Background()
	dir := t.TempDir()
	inFiles := func() (n int) {
		files, err := filepath.Glob(filepath.Join(dir, "pinky.db*"))
		if err != nil || len(files) == 0 {
			t.Fatal(files, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "-shm") {
				continue // the log's index: no text
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			n += bytes.Count(b, []byte(pw))
		}
		return n
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	for i := range 50 {
		p := fmt.Sprintf("%s-%d", pw, i)
		if err := s.SaveStep(ctx, Step{TaskID: "t1", Index: i, Tool: "phone_type", Args: `{"text":"` + p + `"}`, Intent: "Sign in with " + p,
			Text: "Typed “" + p + "” into “Password”", ScreenText: `[12] input "Password, ` + p + `"`, Started: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("PRAGMA user_version = 7"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if inFiles() == 0 {
		t.Fatal("the steps are not in the files to begin with")
	}

	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	if n := inFiles(); n != 0 {
		t.Errorf("%d left in the files once opened", n)
	}
	s.Close()
	if n := inFiles(); n != 0 {
		t.Errorf("%d left in the files once closed", n)
	}

	// As version 8 left it: the steps masked, the old text in freed space.
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if _, err := s.db.Exec(`UPDATE steps SET screen_text = ? WHERE idx = ?`, strings.Repeat(fmt.Sprintf("%s-%d ", pw, i), 20), i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`UPDATE steps SET screen_text = ''; PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if inFiles() == 0 {
		t.Fatal("no text is left in freed space to begin with")
	}
	if s, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := inFiles(); n != 0 {
		t.Errorf("%d left in the files of a version 8 history", n)
	}
	if got, err := s.Steps(ctx, "t1"); err != nil || len(got) != 50 {
		t.Errorf("%d steps %v", len(got), err)
	}
}
