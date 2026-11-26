package ios

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/DIYgod/pinky/internal/driver"
)

// quietProvider is a provider whose rule lives in a folder of the test,
// which runs neither sudo nor osascript: sudo answers with sudoOut and
// sudoErr, and the administrator's commands go to admin.
type quietProvider struct {
	*Provider
	rule    string // the rule's file
	sudo    [][]string
	sudoOut string
	sudoErr error
	admin   []string // commands, then reasons
	adminFn func(command string) error
}

func newQuietProvider(t *testing.T) *quietProvider {
	t.Helper()
	// With slashes on Windows too: the rule's path goes into a shell command.
	q := &quietProvider{Provider: NewProvider(), rule: filepath.ToSlash(filepath.Join(t.TempDir(), "sudoers.d", "pinky-usbmuxd"))}
	q.quiet = quietRestart{path: q.rule, anyOS: true, userFn: func() (string, error) { return "diygod", nil },
		sudoFn: func(_ context.Context, args ...string) ([]byte, error) {
			q.sudo = append(q.sudo, args)
			return []byte(q.sudoOut), q.sudoErr
		}}
	q.restartFn = func(_ context.Context, command, reason string) error {
		q.admin = append(q.admin, command, reason)
		if q.adminFn != nil {
			return q.adminFn(command)
		}
		return nil
	}
	return q
}

func (q *quietProvider) install(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(q.rule), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(q.rule, []byte("rule"), 0o440); err != nil {
		t.Fatal(err)
	}
}

// TestSudoersRule: the rule lets the person's account run exactly
// `/usr/bin/killall usbmuxd` as root without a password, and nothing
// else; an account name sudoers would read as more than a name is refused.
func TestSudoersRule(t *testing.T) {
	got, err := sudoersRule("diygod")
	if err != nil {
		t.Fatal(err)
	}
	want := "# Installed by Pinky (Settings > Phones): lets diygod restart usbmuxd,\n" +
		"# which can lose an iPhone on Wi-Fi, without a password. Turning the setting off removes it.\n" +
		"diygod ALL = (root) NOPASSWD: /usr/bin/killall usbmuxd\n"
	if got != want {
		t.Fatalf("the rule:\n%s\nwant:\n%s", got, want)
	}
	for _, name := range []string{"", "two words", "-x", ".hidden", "a,b", "a:b", "ALL\nroot", "%admin", "a=b", "a#b", "a'b", `a\b`, "a!", "ALL", "ADMIN", "OPS_2"} {
		if r, err := sudoersRule(name); err == nil {
			t.Errorf("sudoersRule(%q) = %q, want an error", name, r)
		}
	}
	for _, name := range []string{"diygod", "john.doe", "a_b-c", "_mbsetupuser", "user1", "Admin", "ADMIN.x"} {
		if _, err := sudoersRule(name); err != nil {
			t.Errorf("sudoersRule(%q): %v", name, err)
		}
	}
}

// TestInstallScript runs the root command that installs the rule, with
// chown, visudo and mv's place in a folder of the test: the file gets the
// exact rule and mode 0440, is checked before it is moved into place, and
// one visudo refuses never gets there and leaves nothing behind.
func TestInstallScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell command")
	}
	rule, err := sudoersRule("diygod")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	checked := filepath.Join(dir, "checked")
	// chown as root:wheel cannot run in a test; visudo copies what it
	// checks, with its mode, so the test sees it, and refuses when told.
	fake := map[string]string{
		"chown":  "#!/bin/sh\n[ \"$1\" = root:wheel ] || exit 9\n",
		"visudo": "#!/bin/sh\n[ \"$1\" = -cf ] || exit 9\nls -l \"$2\" | cut -c1-10 > " + shellQuote(checked+".mode") + "\ncat \"$2\" > " + shellQuote(checked) + "\n[ ! -e " + shellQuote(dir+"/refuse") + " ]\n",
	}
	for name, body := range fake {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, "sudoers.d", "pinky-usbmuxd")
	script := installScript(rule, target)
	for _, want := range []string{"/usr/sbin/chown root:wheel", "/bin/chmod 0440", "/usr/sbin/visudo -cf", "/bin/mv -f", "mktemp", ".pinky-usbmuxd.XXXXXX"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q: %s", want, script)
		}
	}
	if strings.Index(script, "visudo") > strings.Index(script, "/bin/mv") {
		t.Errorf("the script moves the rule into place before visudo checks it: %s", script)
	}
	run := func() error {
		s := strings.NewReplacer("/usr/sbin/chown", filepath.Join(bin, "chown"), "/usr/sbin/visudo", filepath.Join(bin, "visudo")).Replace(script)
		out, err := exec.Command("/bin/sh", "-c", s).CombinedOutput()
		if err != nil {
			return errors.New(err.Error() + ": " + string(out))
		}
		return nil
	}

	// visudo refuses: nothing in place, no temporary file left.
	if err := os.WriteFile(filepath.Join(dir, "refuse"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("the script succeeded though visudo refused the rule")
	}
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a rule visudo refused was moved into place")
	}
	if left, _ := os.ReadDir(filepath.Dir(target)); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}

	os.Remove(filepath.Join(dir, "refuse"))
	if err := run(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != rule {
		t.Fatalf("the installed rule:\n%s\nwant:\n%s", got, rule)
	}
	if c, _ := os.ReadFile(checked); string(c) != rule {
		t.Fatalf("visudo checked:\n%s", c)
	}
	if m, _ := os.ReadFile(checked + ".mode"); strings.TrimSpace(string(m)) != "-r--r-----" {
		t.Fatalf("the mode visudo saw: %q, want -r--r-----", m)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o440 {
		t.Fatalf("the rule's mode is %v, want 0440", fi.Mode().Perm())
	}
	if left, _ := os.ReadDir(filepath.Dir(target)); len(left) != 1 {
		t.Fatalf("in the folder: %v", left)
	}
	if s := removeScript(target); s != "/bin/rm -f '"+target+"'" {
		t.Fatalf("removeScript = %s", s)
	}
}

// TestQuietRestartOffByDefault: until the person allows it, no restart
// goes without a password, and nothing runs sudo.
func TestQuietRestartOffByDefault(t *testing.T) {
	q := newQuietProvider(t)
	if supported, allowed := q.QuietRestart(); !supported || allowed {
		t.Fatalf("QuietRestart() = %v, %v; want supported, not allowed", supported, allowed)
	}
	if err := q.RestartDeviceServiceQuietly(context.Background()); !errors.Is(err, errNeedsPassword) {
		t.Fatalf("a quiet restart without the rule: %v", err)
	}
	if err := q.RestartDeviceService(context.Background(), "why"); err != nil {
		t.Fatal(err)
	}
	if len(q.sudo) != 0 {
		t.Fatalf("ran sudo %v without the rule", q.sudo)
	}
	if !slices.Equal(q.admin, []string{"/usr/bin/killall usbmuxd", "why"}) {
		t.Fatalf("asked an administrator for %q", q.admin)
	}
	if runtime.GOOS != "darwin" {
		if supported, allowed := NewProvider().QuietRestart(); supported || allowed {
			t.Fatalf("on %s: QuietRestart() = %v, %v", runtime.GOOS, supported, allowed)
		}
	}
}

// TestRestartWithoutPassword: with the rule, a restart runs
// `sudo -n /usr/bin/killall usbmuxd` and asks nobody; when sudo wants a
// password after all, it asks as before; when killall fails, it says so.
func TestRestartWithoutPassword(t *testing.T) {
	ctx := context.Background()
	q := newQuietProvider(t)
	q.install(t)
	if _, allowed := q.QuietRestart(); !allowed {
		t.Fatal("not allowed with the rule in place")
	}
	q.known["iphone"] = known{}
	if err := q.RestartDeviceService(ctx, "why"); err != nil {
		t.Fatal(err)
	}
	if len(q.sudo) != 1 || !slices.Equal(q.sudo[0], []string{"/usr/bin/killall", "usbmuxd"}) || len(q.admin) != 0 {
		t.Fatalf("sudo %v, administrator %v", q.sudo, q.admin)
	}
	if len(q.known) != 0 {
		t.Fatal("the phones listed before the restart are still known")
	}
	if err := q.RestartDeviceServiceQuietly(ctx); err != nil || len(q.sudo) != 2 || len(q.admin) != 0 {
		t.Fatalf("quietly: %v; sudo %v, administrator %v", err, q.sudo, q.admin)
	}

	// The rule is there but sudo wants a password (it was changed, or
	// sudo does not read the folder): the system's request, as before.
	q.sudoOut, q.sudoErr = "sudo: a password is required\n", errors.New("exit status 1")
	if err := q.RestartDeviceService(ctx, "why"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(q.admin, []string{"/usr/bin/killall usbmuxd", "why"}) {
		t.Fatalf("no fallback to the password: administrator %v", q.admin)
	}
	if err := q.RestartDeviceServiceQuietly(ctx); !errors.Is(err, errNeedsPassword) || len(q.admin) != 2 {
		t.Fatalf("quietly, sudo wanting a password: %v, administrator %v", err, q.admin)
	}

	// killall failed: an error, and no request for the password.
	q.sudoOut = "No matching processes were found\n"
	err := q.RestartDeviceService(ctx, "why")
	if err == nil || errors.Is(err, errNeedsPassword) || !strings.Contains(err.Error(), "No matching processes") || len(q.admin) != 2 {
		t.Fatalf("killall failing: %v, administrator %v", err, q.admin)
	}

	// The person declines the password: canceled.
	q.sudoOut = "sudo: a password is required\n"
	q.adminFn = func(string) error { return driver.ErrCanceled }
	if err := q.RestartDeviceService(ctx, "why"); !errors.Is(err, driver.ErrCanceled) {
		t.Fatalf("declined: %v", err)
	}
}

// TestAllowQuietRestart: turning it on installs the rule through the
// system's request for an administrator, with the person's reason, and
// off removes it the same way; declining changes nothing.
func TestAllowQuietRestart(t *testing.T) {
	ctx := context.Background()
	q := newQuietProvider(t)
	q.adminFn = func(command string) error {
		if strings.HasPrefix(command, "/bin/rm") {
			return os.Remove(q.rule)
		}
		q.install(t)
		return nil
	}
	if err := q.AllowQuietRestart(ctx, true, "allow"); err != nil {
		t.Fatal(err)
	}
	rule, _ := sudoersRule("diygod")
	if len(q.admin) != 2 || q.admin[0] != installScript(rule, q.rule) || q.admin[1] != "allow" {
		t.Fatalf("asked an administrator for %q", q.admin)
	}
	if _, allowed := q.QuietRestart(); !allowed {
		t.Fatal("not allowed after allowing")
	}
	if err := q.AllowQuietRestart(ctx, false, "stop"); err != nil {
		t.Fatal(err)
	}
	if len(q.admin) != 4 || q.admin[2] != removeScript(q.rule) || q.admin[3] != "stop" {
		t.Fatalf("asked an administrator for %q", q.admin)
	}
	if _, allowed := q.QuietRestart(); allowed {
		t.Fatal("still allowed after turning it off")
	}

	q.adminFn = func(string) error { return driver.ErrCanceled }
	if err := q.AllowQuietRestart(ctx, true, "allow"); !errors.Is(err, driver.ErrCanceled) {
		t.Fatalf("declined: %v", err)
	}
	if _, allowed := q.QuietRestart(); allowed {
		t.Fatal("allowed though the person declined")
	}
	// The command ran but no rule is there: an error, not allowed.
	q.adminFn = nil
	if err := q.AllowQuietRestart(ctx, true, "allow"); err == nil {
		t.Fatal("no error though the rule is not there")
	}
	q.quiet.userFn = func() (string, error) { return "two words", nil }
	n := len(q.admin)
	if err := q.AllowQuietRestart(ctx, true, "allow"); err == nil || len(q.admin) != n {
		t.Fatalf("an account name sudoers cannot take: %v, administrator %v", err, q.admin[n:])
	}
}
