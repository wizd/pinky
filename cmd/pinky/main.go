// Command pinky is the Pinky gateway, desktop app and MCP shim in one
// binary.
//
//	pinky            desktop app (gateway + client)
//	pinky daemon     gateway only, no UI
//	pinky mcp        stdio MCP server for agents; forwards to the gateway
//	pinky devices    list connected phones
//	pinky helper     install the WebDriverAgent helper on an iPhone
//	pinky token      print the agent token, for agents that connect over HTTP
//	pinky quit       quit the running Pinky
//	pinky update     install a newer version of Pinky
//	pinky version    print the version
//
// Add --fake to daemon, mcp or devices to use scripted fake phones instead
// of real ones.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/driver/ios"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

func main() {
	ios.PinkyVersion = version // the iPhone app kit of this release
	cmd, args := commandOf(os.Args[1:])
	err := dispatch(cmd, args)
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, errUnknownCommand):
		usage()
	case errors.Is(err, flag.ErrHelp):
		// The command printed its usage because the command line asked
		// for it (-h, --help, help).
	default:
		var ee *exitError
		if errors.As(err, &ee) {
			// A usage error that printed itself (taskUsageError) has
			// nothing left to say.
			if ee.err != nil {
				fmt.Fprintf(os.Stderr, "pinky %s: %v\n", cmd, ee.err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "pinky %s: %v\n", cmd, err)
		}
	}
	os.Exit(exitCode(err))
}

// exitCode is how a command ends: 0 when it did what it was asked,
// which includes printing its usage because the command line asked for
// help, as the commands spelled as a flag (-h, --help, help) do; 2 for a
// command Pinky does not know, and for a usage error that ends through
// exitError; 1 for another failure.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, errUnknownCommand):
		return 2
	}
	return 1
}

// errUnknownCommand says the command line named a command Pinky does not
// know: main prints the usage and quits 2.
var errUnknownCommand = errors.New("unknown command")

// dispatch runs the command the command line names, with its flags.
func dispatch(cmd string, args []string) error {
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("pinky", version)
		return nil
	case "help", "--help", "-h", "-help":
		usage()
		return nil
	case "app":
		return runApp(args)
	case "daemon":
		return runDaemon(args)
	case "mcp":
		return runMCP(args)
	case "devices":
		return runDevices(args)
	case "task":
		return runTask(args)
	case "companion":
		return runCompanion(args)
	case "helper":
		return runHelper(args)
	case "needs":
		return runNeeds(args)
	case "decide":
		return runDecide(args)
	case "token":
		return runToken(args)
	case "vcorp":
		return runVcorp(args)
	case "quit":
		return runQuit(args)
	case "update":
		return runUpdate(args)
	case "update-helper":
		// Not for people: the app starts it from a copy of itself to
		// install an update on Windows.
		return runUpdateHelper(args)
	}
	return errUnknownCommand
}

// commandOf splits the command line into the command and its flags. The
// first argument names the command unless it is a flag, which belongs to
// the app; the commands spelled as one (--version, -help) are the
// exception.
func commandOf(args []string) (cmd string, rest []string) {
	if len(args) > 0 && (!strings.HasPrefix(args[0], "-") || isCommandFlag(args[0])) {
		return args[0], args[1:]
	}
	return "app", args
}

// isCommandFlag reports whether a first argument that looks like a flag is
// really a command, such as --version. dispatch has a case for every one
// of them; one it lacks would print the usage and quit 2, as an unknown
// command does.
func isCommandFlag(arg string) bool {
	switch arg {
	case "--version", "-v", "help", "--help", "-h", "-help":
		return true
	}
	return false
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: pinkyctl [command] [flags]

pinkyctl is the command that Homebrew's cask and the Linux packages
install; Pinky's own executable takes the same commands.

Commands:
  (none)    start the desktop app (gateway and window); same as "app"
  daemon    run the gateway without UI
  mcp       run the MCP server on stdio (agents launch this); starts the
            gateway when none is running, unless you quit Pinky
  devices   list connected phones
  task run|status|steer|cancel|list
            hand Ask Pinky a task from a shell and follow it, as an agent
            named "Pinky CLI" or -as name ("pinkyctl task help")
  companion install [phone]
            install or update Pinky on the phone, Pinky's app for Android phones
  helper install [--team ID] [--bundle-id ID] [--local-signing]
                 [--helper-only] [phone]
            build Pinky on the iPhone, Pinky's app for the iPhone, with
            WebDriverAgent in it, with Xcode and install it on the phone
            (--helper-only: the older Pinky Helper); run it again when its
            signature runs out (macOS)
  token     print the agent token, which agents connecting over HTTP send
  quit      quit the running Pinky (the app or the daemon); agents do not
            start it again until you open it
  update [--check]
            say whether a newer Pinky is out and install it (while Pinky
            is not running; the app updates itself)
  version   print the version

Flags:
  --fake    use scripted fake phones (app, daemon, mcp, devices)
  --http addr
            daemon: also serve MCP over HTTP at addr (127.0.0.1:7465)

Environment:
  PINKY_HOME         keep config, data and the socket under this folder
  PINKY_UPDATE_FEED  read updates from this feed (latest.json) instead
`)
}

// runQuit asks the running gateway, the app's or the daemon's, to quit, and
// waits until its socket goes away.
func runQuit(args []string) error {
	fs := flag.NewFlagSet("pinky quit", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	ctl, err := api.DialControl(dirs)
	if err != nil {
		fmt.Println("Pinky is not running.")
		return nil
	}
	err = ctl.Call("quit", nil, nil)
	ctl.Close()
	if err != nil {
		return err
	}
	// Quit on purpose: agents do not start it again until it is opened
	// (quitmark.go). The app notes it too; a daemon does not.
	if err := markQuit(dirs); err != nil {
		fmt.Fprintf(os.Stderr, "pinky quit: %v\n", err)
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		c, err := api.DialControl(dirs)
		if err != nil {
			return nil
		}
		c.Close()
	}
	return errors.New("Pinky is still running after 15 s")
}

// runToken prints the agent token, creating it when missing. Agents that
// connect over HTTP send it as a bearer token; `pinky mcp` reads it itself.
func runToken(args []string) error {
	fs := flag.NewFlagSet("pinky token", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	if err := dirs.Ensure(); err != nil {
		return err
	}
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		return err
	}
	fmt.Println(tok)
	return nil
}
