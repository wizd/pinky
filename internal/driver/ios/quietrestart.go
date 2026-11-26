package ios

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"strings"

	"github.com/DIYgod/pinky/internal/driver"
)

// Restarting usbmuxd without a password (driver.QuietDeviceServiceRestarter).
//
// usbmuxd runs as root, so quitting it takes an administrator. Asking for
// the password each time leaves an agent nobody watches stuck with a phone
// usbmuxd lost (lost.go). So the person may allow it once, in Settings ›
// Phones: Pinky then installs a sudoers rule, through macOS's request for an
// administrator's password, that lets their account run exactly
// `/usr/bin/killall usbmuxd` as root without a password, and nothing else.
// RestartDeviceService then runs `sudo -n /usr/bin/killall usbmuxd`, and
// asks for the password as before only when the rule is not there or sudo
// refuses it. Turning the setting off removes the rule, asking once again.
//
// The rule's file is the setting: the provider reads whether it is there,
// so the switch shows what the system does, also after the person removed
// the file by hand, and nothing else needs saving.
//
// The root shell command that installs the rule writes its content itself:
// no file the person's account can write is copied into /etc/sudoers.d, so
// a program running as the person cannot swap in a wider rule between the
// check and the move. It writes a temporary file there whose name holds a
// dot (sudo reads no such file), with mode 0440 and owned by root, checks
// it with `visudo -cf`, and only then moves it into place: a file sudo
// cannot parse would break sudo for everyone.

const (
	// sudoersPath is the rule's file.
	sudoersPath = "/etc/sudoers.d/pinky-usbmuxd"
	// killallPath and killallArg are the one command the rule allows.
	killallPath = "/usr/bin/killall"
	killallArg  = "usbmuxd"
	// restartCommand is the command, as the shell runs it.
	restartCommand = killallPath + " " + killallArg
)

// quietRestart is how the provider restarts usbmuxd without a password.
// Its zero value is the system's.
type quietRestart struct {
	// For tests: the rule's file, the person's account name, how sudo runs,
	// and on any system.
	path   string
	userFn func() (string, error)
	sudoFn func(ctx context.Context, args ...string) ([]byte, error)
	anyOS  bool
}

func (q *quietRestart) file() string {
	if q.path != "" {
		return q.path
	}
	return sudoersPath
}

func (q *quietRestart) supported() bool { return runtime.GOOS == "darwin" || q.anyOS }

// installed reports whether the rule's file is there. /etc/sudoers.d is
// readable by everyone, the file only by root: its being there is enough.
func (q *quietRestart) installed() bool {
	if !q.supported() {
		return false
	}
	_, err := os.Stat(q.file())
	return err == nil
}

func (q *quietRestart) userName() (string, error) {
	if q.userFn != nil {
		return q.userFn()
	}
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

// sudo runs sudo with args, never asking for a password (-n), with sudo's
// messages in English so they can be told from the command's own.
func (q *quietRestart) sudo(ctx context.Context, args ...string) ([]byte, error) {
	if q.sudoFn != nil {
		return q.sudoFn(ctx, args...)
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/sudo", append([]string{"-n"}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	return cmd.CombinedOutput()
}

// accountName matches the macOS account names a sudoers rule can name
// without quoting: letters, digits, '_', '.' and '-', not starting with
// '-' or '.'.
var accountName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// sudoersAlias matches what sudoers reads as an alias or the word ALL, not
// an account: upper-case letters, digits and '_', starting with a letter.
// "ALL ALL = …" would let every account restart usbmuxd, and an alias of
// that name in another file could stand for anyone.
var sudoersAlias = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// sudoersRule is the rule's content for the account named user: only
// `/usr/bin/killall usbmuxd`, as root, without a password.
func sudoersRule(user string) (string, error) {
	if !accountName.MatchString(user) || sudoersAlias.MatchString(user) || len(user) > 255 {
		return "", fmt.Errorf("the account name %q cannot go into a sudoers rule", user)
	}
	return "# Installed by Pinky (Settings > Phones): lets " + user + " restart usbmuxd,\n" +
		"# which can lose an iPhone on Wi-Fi, without a password. Turning the setting off removes it.\n" +
		user + " ALL = (root) NOPASSWD: " + restartCommand + "\n", nil
}

// installScript is the root shell command that installs rule at path.
func installScript(rule, path string) string {
	dir := path[:strings.LastIndex(path, "/")]
	tmp := dir + "/.pinky-usbmuxd.XXXXXX" // a dot: sudo does not read it
	lines := strings.Split(strings.TrimSuffix(rule, "\n"), "\n")
	quoted := make([]string, len(lines))
	for i, l := range lines {
		quoted[i] = shellQuote(l)
	}
	return "umask 077; /bin/mkdir -p -m 0755 " + shellQuote(dir) +
		" && t=$(/usr/bin/mktemp " + shellQuote(tmp) + ")" +
		" && { /usr/bin/printf '%s\\n' " + strings.Join(quoted, " ") + ` > "$t"` +
		` && /bin/chmod 0440 "$t" && /usr/sbin/chown root:wheel "$t"` +
		` && /usr/sbin/visudo -cf "$t" && /bin/mv -f "$t" ` + shellQuote(path) + "; }" +
		` || { s=$?; /bin/rm -f "$t"; exit $s; }`
}

// removeScript is the root shell command that removes the rule at path.
func removeScript(path string) string { return "/bin/rm -f " + shellQuote(path) }

// shellQuote quotes s for the shell.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// errNeedsPassword is sudo refusing to run the command without a password:
// the rule is not there, or does not allow it.
var errNeedsPassword = errors.New("sudo needs a password to restart usbmuxd")

// QuietRestart implements driver.QuietDeviceServiceRestarter: on a Mac,
// allowed while the rule's file is there.
func (p *Provider) QuietRestart() (supported, allowed bool) {
	return p.quiet.supported(), p.quiet.installed()
}

// AllowQuietRestart installs the rule (on) or removes it, through macOS's
// request for an administrator's password, with reason in it.
func (p *Provider) AllowQuietRestart(ctx context.Context, on bool, reason string) error {
	if !p.quiet.supported() {
		return fmt.Errorf("restarting the device service without a password: %w", driver.ErrUnsupported)
	}
	script := removeScript(p.quiet.file())
	if on {
		name, err := p.quiet.userName()
		if err != nil {
			return fmt.Errorf("restarting the device service without a password: %v", err)
		}
		rule, err := sudoersRule(name)
		if err != nil {
			return fmt.Errorf("restarting the device service without a password: %v", err)
		}
		script = installScript(rule, p.quiet.file())
	}
	if err := p.runAdmin(ctx, script, reason); err != nil {
		return fmt.Errorf("restarting the device service without a password: %w", err)
	}
	if on && !p.quiet.installed() {
		return fmt.Errorf("restarting the device service without a password: the rule is not in %s", p.quiet.file())
	}
	p.logger().Info("iphone: restarting usbmuxd without a password", "allowed", on, "rule", p.quiet.file())
	return nil
}

// RestartDeviceServiceQuietly implements
// driver.QuietDeviceServiceRestarter: it quits usbmuxd through the rule,
// and fails without asking anyone when the rule is not there.
func (p *Provider) RestartDeviceServiceQuietly(ctx context.Context) error {
	if !p.quiet.installed() {
		return fmt.Errorf("restarting the device service: %w", errNeedsPassword)
	}
	if err := p.restartQuietly(ctx); err != nil {
		return err
	}
	p.afterRestart()
	return nil
}

// restartQuietly runs `sudo -n /usr/bin/killall usbmuxd`. It returns
// errNeedsPassword when sudo refuses to without a password, and another
// error when killall failed.
func (p *Provider) restartQuietly(ctx context.Context) error {
	out, err := p.quiet.sudo(ctx, killallPath, killallArg)
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.HasPrefix(msg, "sudo:") || strings.Contains(msg, "\nsudo:") {
		// sudo's own refusal ("sudo: a password is required"), not
		// killall's.
		return fmt.Errorf("restarting the device service: %w: %s", errNeedsPassword, msg)
	}
	if msg != "" {
		return fmt.Errorf("restarting the device service: %v: %s", err, msg)
	}
	return fmt.Errorf("restarting the device service: %v", err)
}

// runAdmin runs a shell command as root through the system's request for
// an administrator's password.
func (p *Provider) runAdmin(ctx context.Context, command, reason string) error {
	run := p.restartFn
	if run == nil {
		run = runAsAdmin
	}
	return run(ctx, command, reason)
}

var _ driver.QuietDeviceServiceRestarter = (*Provider)(nil)
