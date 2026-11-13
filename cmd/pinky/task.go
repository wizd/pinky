package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/api"
)

// pinky task hands Ask Pinky a task from a shell, through the phone_task_*
// tools: run, status, steer, cancel and list, as the tools are named. It
// is an agent like any other: an MCP client of the gateway over the
// connection `pinky mcp` uses, with a name of its own (-as), so its first
// task asks the person (New agent), and its scope and the person's rules
// apply. It is never Pinky's own session, and it adds nothing to the
// control socket.

// taskAgentName is the name `pinky task` shows in Pinky without -as.
const taskAgentName = "Pinky CLI"

// taskPoll is how long `pinky task` waits before it asks again about a
// task that waits for the person: phone_task_wait returns at once then. A
// variable for tests.
var taskPoll = 2 * time.Second

// taskWaitMS is how long each phone_task_wait waits, the most it allows.
const taskWaitMS = 45000

// Exit statuses of `pinky task`, besides 0 for a task that was done:
// exitTaskEnded for one that ended otherwise (failed, refused, a limit,
// cancelled, stopped), exitTaskError for a call Pinky refused or a task
// that could not run (unsupported, no_key, bad_key, error), and
// exitInterrupted after Ctrl-C, which cancels the task.
const (
	exitTaskEnded   = 1
	exitTaskError   = 2
	exitInterrupted = 130
)

// exitError ends the command with its code; main prints err, if any.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit status %d", e.code)
	}
	return e.err.Error()
}

func (e *exitError) Unwrap() error { return e.err }

// taskSession is how `pinky task` reaches the gateway.
var taskSession = dialTask

// stepLog writes what happens to a task on stderr: each step once, by its
// number, whether it came as a progress notification or in an answer's
// steps. phone_task_wait notifies only the steps taken while it waits, so
// an answer may list earlier ones after a later one was notified.
type stepLog struct {
	mu      sync.Mutex
	w       io.Writer
	printed map[int]bool
}

func (l *stepLog) step(n int, line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.printed[n] {
		return
	}
	if l.printed == nil {
		l.printed = map[int]bool{}
	}
	l.printed[n] = true
	fmt.Fprintf(l.w, "%d. %s\n", n, line)
}

// steps writes the steps an answer lists ("Steps:", then "4. …").
func (l *stepLog) steps(text string) {
	_, list, ok := strings.Cut(text, "\nSteps:\n")
	if !ok {
		return
	}
	for _, line := range strings.Split(list, "\n") {
		if m := stepLineRe.FindStringSubmatch(line); m != nil {
			var n int
			fmt.Sscan(m[1], &n)
			l.step(n, m[2])
		}
	}
}

func (l *stepLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func runTask(args []string) error {
	if len(args) == 0 {
		taskUsage(os.Stderr)
		return flag.ErrHelp
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("pinky task "+sub, flag.ContinueOnError)
	as := fs.String("as", taskAgentName, "the agent name Pinky shows, and asks you to allow the first time")
	var o taskOptions
	switch sub {
	case "run":
		fs.StringVar(&o.device, "device", "", "the phone, by id or name; optional with one phone online")
		fs.Func("apps", "the only apps the task needs, by name or id, comma-separated", func(s string) error {
			for _, a := range strings.Split(s, ",") {
				if a = strings.TrimSpace(a); a != "" {
					o.apps = append(o.apps, a)
				}
			}
			return nil
		})
		fs.BoolVar(&o.readOnly, "read-only", false, "run the task read-only, for tasks that only look and report")
		fs.BoolVar(&o.detach, "detach", false, "print the task_id once the task starts, and do not wait for it")
	case "status", "steer", "cancel", "list":
	case "help", "-h", "-help", "--help":
		taskUsage(os.Stdout)
		return nil
	default:
		taskUsage(os.Stderr)
		return flag.ErrHelp
	}
	fs.Usage = func() { taskUsage(fs.Output()) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{code: exitTaskError} // flag printed why
	}
	rest := fs.Args()
	o.sub = sub
	switch sub {
	case "run":
		o.text = strings.TrimSpace(strings.Join(rest, " "))
		if o.text == "" {
			return taskUsageError(fs, "run needs the task, in words")
		}
	case "status", "cancel":
		if len(rest) != 1 {
			return taskUsageError(fs, sub+" needs one task_id")
		}
		o.id = rest[0]
	case "steer":
		if len(rest) < 2 {
			return taskUsageError(fs, "steer needs a task_id and the message")
		}
		o.id, o.text = rest[0], strings.TrimSpace(strings.Join(rest[1:], " "))
	case "list":
		if len(rest) != 0 {
			return taskUsageError(fs, "list takes no arguments")
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := &stepLog{w: os.Stderr}
	cs, err := taskSession(ctx, *as, log)
	if err != nil {
		return &exitError{exitTaskError, err}
	}
	defer cs.Close()
	return task(ctx, cs, o, os.Stdout, log)
}

func taskUsageError(fs *flag.FlagSet, msg string) error {
	fmt.Fprintf(fs.Output(), "pinky task: %s\n\n", msg)
	taskUsage(fs.Output())
	return flag.ErrHelp
}

func taskUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  pinkyctl task run [-device phone] [-apps a,b] [-read-only] [-detach] [-as name] "<task>"
  pinkyctl task status [-as name] <task_id>
  pinkyctl task steer [-as name] <task_id> "<message>"
  pinkyctl task cancel [-as name] <task_id>
  pinkyctl task list [-as name]

Hands Ask Pinky a task, as the phone_task_* tools do: Pinky does it on the
phone with the model and limits set in Settings > Agents > Ask Pinky, while
Pinky's window is open. run and steer wait for the task to end, with its
steps on stderr, then print its status line and summary on stdout (run
-detach prints only the task_id). Ctrl-C cancels the task.

pinky task is an agent named "Pinky CLI", or the -as name: the first task
under a name asks you in Pinky, and its scope in Settings > Agents applies.

Exit status: 0 when the task was done (or the call worked, for status,
cancel and list); 1 when it ended otherwise (failed, refused, a limit,
cancelled, stopped); 2 for usage errors, a call Pinky refused, and a task
that could not run (unsupported, no_key, bad_key, error); 130 after Ctrl-C.
`)
}

// taskOptions is what `pinky task` was asked to do.
type taskOptions struct {
	sub      string // run, status, steer, cancel or list
	id       string // the task_id, but for run and list
	text     string // run's task, steer's message
	device   string
	apps     []string
	readOnly bool
	detach   bool
}

// dialTask connects to the gateway's MCP server as the agent named name,
// starting Pinky when it does not run, as `pinky mcp` does (launchGateway).
// Step notifications go to steps.
func dialTask(ctx context.Context, name string, steps *stepLog) (*sdk.ClientSession, error) {
	dirs, err := api.DefaultDirs()
	if err != nil {
		return nil, err
	}
	conn, err := api.Dial(dirs)
	if err != nil {
		if conn, err = launchGateway(dirs, false); err != nil {
			return nil, err
		}
	}
	tok, err := api.EnsureToken(dirs.TokenFile())
	if err != nil {
		conn.Close()
		return nil, err
	}
	br, err := greetMCP(conn, tok)
	if err != nil {
		conn.Close()
		return nil, err
	}
	t := &sdk.IOTransport{Reader: connReader{br, conn}, Writer: conn}
	return taskClient(name, steps).Connect(ctx, t, nil)
}

// taskClient is the MCP client `pinky task` connects with. Pinky shows
// its title, name, as the agent's name (mcp.DisplayName, which shows an
// outside client calling itself Pinky as "Pinky (external)"). It prints
// each step the gateway notifies on steps.
func taskClient(name string, steps *stepLog) *sdk.Client {
	return sdk.NewClient(&sdk.Implementation{Name: "pinky-task", Title: name, Version: version}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			steps.step(int(req.Params.Progress), req.Params.Message)
		},
	})
}

// connReader reads the session through the greeting's reader and closes
// the connection.
type connReader struct {
	*bufio.Reader
	conn net.Conn
}

func (r connReader) Close() error { return r.conn.Close() }

// task does what o asks over the session: the outcome on out, what happens
// meanwhile on log.
func task(ctx context.Context, cs *sdk.ClientSession, o taskOptions, out io.Writer, log *stepLog) error {
	var text string
	var err error
	switch o.sub {
	case "list":
		return taskOnce(ctx, cs, out, "phone_task_list", map[string]any{})
	case "status":
		return taskOnce(ctx, cs, out, "phone_task_status", map[string]any{"task_id": o.id})
	case "cancel":
		return taskOnce(ctx, cs, out, "phone_task_cancel", map[string]any{"task_id": o.id})
	case "steer":
		text, err = taskCall(ctx, cs, &sdk.CallToolParams{Name: "phone_task_steer",
			Arguments: map[string]any{"task_id": o.id, "message": o.text}})
	default:
		args := map[string]any{"task": o.text}
		if o.device != "" {
			args["device"] = o.device
		}
		if len(o.apps) > 0 {
			args["apps"] = o.apps
		}
		if o.readOnly {
			args["read_only"] = true
		}
		text, err = taskCall(ctx, cs, &sdk.CallToolParams{Name: "phone_task_run", Arguments: args})
	}
	id := o.id
	if id == "" {
		id = taskIDOf(text)
	}
	if ctx.Err() != nil {
		// Interrupted while the task started or took the message: it may
		// run on, so cancel it when its task_id is known.
		if id != "" {
			return taskCancel(cs, id, log)
		}
		return &exitError{exitInterrupted, errors.New("interrupted")}
	}
	if err != nil {
		return err
	}
	if id == "" {
		return &exitError{exitTaskError, fmt.Errorf("no task_id in Pinky's answer:\n%s", text)}
	}
	fmt.Fprintln(log, firstLine(text))
	log.steps(text)
	if o.detach {
		fmt.Fprintln(out, id)
		return nil
	}
	return taskWait(ctx, cs, id, stepsOf(text), out, log)
}

// taskOnce makes one call and prints its answer.
func taskOnce(ctx context.Context, cs *sdk.ClientSession, out io.Writer, tool string, args map[string]any) error {
	text, err := taskCall(ctx, cs, &sdk.CallToolParams{Name: tool, Arguments: args})
	if ctx.Err() != nil {
		return &exitError{exitInterrupted, errors.New("interrupted")}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, text)
	return nil
}

// taskWait waits for the task to end, its steps after since on log, and
// prints its status line and summary on out. Ctrl-C (ctx) cancels it.
func taskWait(ctx context.Context, cs *sdk.ClientSession, id string, since int, out io.Writer, log *stepLog) error {
	waiting := ""
	for {
		p := &sdk.CallToolParams{Name: "phone_task_wait",
			Arguments: map[string]any{"task_id": id, "timeout_ms": taskWaitMS, "since_step": since}}
		p.SetProgressToken(id)
		text, err := taskCall(ctx, cs, p)
		if ctx.Err() != nil {
			return taskCancel(cs, id, log)
		}
		if err != nil {
			return err
		}
		log.steps(text)
		since = max(since, stepsOf(text))
		switch status := statusOf(text); status {
		case "running":
			waiting = ""
		case "waiting_for_person":
			if w := waitingOf(text); w != waiting {
				waiting = w
				fmt.Fprintf(log, "Waiting for you in Pinky: %s\n", w)
			}
			// phone_task_wait returns at once while the person is asked.
			select {
			case <-ctx.Done():
				return taskCancel(cs, id, log)
			case <-time.After(taskPoll):
			}
		default:
			fmt.Fprintln(out, firstLine(text))
			if msg := messageOf(text); msg != "" {
				fmt.Fprintln(out, msg)
			}
			switch status {
			case "done":
				return nil
			case "failed", "refused", "limit_reached", "cancelled", "stopped":
				return &exitError{code: exitTaskEnded}
			}
			return &exitError{code: exitTaskError}
		}
	}
}

// taskCancel cancels a task after Ctrl-C, with a short timeout, so that a
// script interrupted leaves none running.
func taskCancel(cs *sdk.ClientSession, id string, log io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := taskCall(ctx, cs, &sdk.CallToolParams{Name: "phone_task_cancel",
		Arguments: map[string]any{"task_id": id}}); err != nil {
		return &exitError{exitInterrupted, fmt.Errorf("interrupted; could not cancel task %s: %w", id, err)}
	}
	fmt.Fprintf(log, "Cancelled task %s.\n", id)
	return &exitError{code: exitInterrupted}
}

// taskCall calls a tool and returns its text. An answer that is an error
// ("error: unsupported · …") is returned as one, with exitTaskError.
func taskCall(ctx context.Context, cs *sdk.ClientSession, p *sdk.CallToolParams) (string, error) {
	res, err := cs.CallTool(ctx, p)
	if err != nil {
		return "", &exitError{exitTaskError, err}
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if strings.HasPrefix(text, "error: ") {
		return "", &exitError{exitTaskError, errors.New(text)}
	}
	return text, nil
}

var (
	taskIDRe   = regexp.MustCompile(`task_id: (\S+)`)
	stepsRe    = regexp.MustCompile(`· steps: (\d+)`)
	waitingRe  = regexp.MustCompile(`· waiting: (\S+)`)
	stepLineRe = regexp.MustCompile(`^(\d+)\. (.*)$`)
)

// The first line of a task's answer: its status, task_id, phone, what it
// waits for, and its steps (internal/mcp, taskResult).

func taskIDOf(text string) string {
	if m := taskIDRe.FindStringSubmatch(firstLine(text)); m != nil {
		return m[1]
	}
	return ""
}

func stepsOf(text string) int {
	n := 0
	if m := stepsRe.FindStringSubmatch(firstLine(text)); m != nil {
		fmt.Sscan(m[1], &n)
	}
	return n
}

func waitingOf(text string) string {
	if m := waitingRe.FindStringSubmatch(firstLine(text)); m != nil {
		return m[1]
	}
	return "the person"
}

func statusOf(text string) string {
	s, _, _ := strings.Cut(firstLine(text), " ")
	return s
}

func firstLine(text string) string {
	l, _, _ := strings.Cut(text, "\n")
	return l
}

// messageOf is what an ended task's answer says after its status line:
// how it ended, with the summary, which may take several lines. The task's
// words follow it on one line, "Task: “…”", then its steps, if any
// (internal/mcp, taskResult); a summary may hold such words itself, so
// the task's line is the last one so worded that ends the answer or comes
// right before its steps.
func messageOf(text string) string {
	lines := strings.Split(text, "\n")[1:]
	end := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "Task: “") && (i == len(lines)-1 || lines[i+1] == "Steps:") {
			end = i
			break
		}
	}
	return strings.TrimSpace(strings.Join(lines[:end], "\n"))
}
