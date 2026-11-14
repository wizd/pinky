package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/DIYgod/pinky/internal/gateway"
)

// Tasks run by Pinky's own agent for other agents: phone_task_run hands
// it a whole task, which it does on its own with the model the person
// chose in Pinky, while the agent follows it with phone_task_status and
// phone_task_wait, and may steer it with phone_task_steer. The task works
// as the agent that handed it over (its approval as a new agent, its scope,
// the person's rules), so it can do no more than that agent could itself;
// approvals and Take over stay the person's alone. The person may steer and
// continue it in Pinky too, as their own task.

// TaskRequest is a task an agent hands Pinky.
type TaskRequest struct {
	Agent    gateway.AgentInfo
	Device   string // the phone's device id
	Task     string
	Apps     []string
	ReadOnly bool
}

// Task is a task an agent handed Pinky, running or ended.
type Task struct {
	ID      string
	Device  string // the phone's device id
	Task    string
	Model   string
	Started time.Time
	Ended   time.Time // zero while it runs
	// End is how it ended (pilot.End): done, failed, stopped, cancelled,
	// steps, time, cost, refused, no_key, bad_key or error.
	End     string
	Summary string // the agent's summary, or why it ended
	// How far it is, and its limits.
	Steps, MaxSteps int
	MaxTime         time.Duration
	Waited          time.Duration // the person's time, which does not count
	Elapsed         time.Duration // the time it ran, in all its stretches
	// Continues counts the times it was continued after it ended; the
	// limits count anew from the last.
	Continues int
	// Pending counts the messages it has not read yet.
	Pending        int
	Cost, MaxCost  float64 // US dollars, when Priced
	Tokens, MaxTok int64   // when not Priced
	Priced         bool
	// Waiting is set while it waits for the person: approval or handoff.
	Waiting string
	Log     []TaskStep // the newest last; Log[i] is step First+i
	First   int        // the number of Log's first step, from 1
	Version int        // counts its changes
}

// Running reports whether the task has not ended.
func (t Task) Running() bool { return t.Ended.IsZero() }

// TaskStep is a phone tool Pinky's agent called.
type TaskStep struct {
	At                         time.Time
	Tool, Intent, Status, Note string
	// From is, for a message (gateway.PersonStep), the agent that sent it;
	// empty for the person's.
	From string
}

// TaskError is why Tasks cannot do what was asked, for the agent.
type TaskError struct {
	Code    gateway.ErrorCode
	Message string
}

func (e *TaskError) Error() string { return e.Message }

// Tasks runs the tasks agents hand Pinky's own agent. Each call returns at
// once but Wait. agent is the asking agent's name: an agent sees only its
// own tasks.
type Tasks interface {
	Run(req TaskRequest) (Task, error)
	Task(agent, id string) (Task, bool)
	List(agent string) []Task
	// Wait returns once the task changed since version, ended, or ctx
	// ended.
	Wait(ctx context.Context, agent, id string, version int) (Task, error)
	Cancel(agent, id string) (Task, error)
	// Steer gives the task a message from the agent: read with its next
	// step while it runs, or carrying it on once it ended.
	Steer(agent, id, text string) (Task, error)
}

// SetTasks lets agents hand tasks to Pinky's own agent through t; nil, as
// before it is called, turns the phone_task_* tools off: they return
// unsupported (in `pinky daemon`, which has no window for the person to
// follow the task in).
func (s *Server) SetTasks(t Tasks) {
	s.mu.Lock()
	s.tasks = t
	s.mu.Unlock()
}

func (s *Server) taskRunner() Tasks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tasks
}

type taskRunIn struct {
	Task     string   `json:"task" jsonschema:"The whole task, in words, as you would give it to a person: what to do, on which app if you know, and what to report back. Pinky's agent works from these words and the screen alone."`
	Device   string   `json:"device,omitempty" jsonschema:"The phone, by device id or name. Optional when one phone is connected."`
	Apps     []string `json:"apps,omitempty" jsonschema:"Optional: the only apps the task needs, by name or id, as in phone_connect."`
	ReadOnly bool     `json:"read_only,omitempty" jsonschema:"Run the task read-only, as in phone_connect: for tasks that only look and report."`
}

type taskIDIn struct {
	TaskID string `json:"task_id" jsonschema:"The task_id phone_task_run returned."`
}

type taskStatusIn struct {
	TaskID    string `json:"task_id" jsonschema:"The task_id phone_task_run returned."`
	SinceStep int    `json:"since_step,omitempty" jsonschema:"Only the steps after this one, for the steps you have not seen yet; default the last 10."`
}

type taskWaitIn struct {
	TaskID    string `json:"task_id" jsonschema:"The task_id phone_task_run returned."`
	TimeoutMS int    `json:"timeout_ms,omitempty" jsonschema:"Wait at most this many milliseconds, 1000 to 45000; default 30000."`
	SinceStep int    `json:"since_step,omitempty" jsonschema:"As in phone_task_status."`
}

type taskListIn struct{}

type taskSteerIn struct {
	TaskID  string `json:"task_id" jsonschema:"The task_id phone_task_run returned."`
	Message string `json:"message" jsonschema:"What Pinky's agent should do differently or next, in words, as you would tell a person."`
}

// Defaults of phone_task_wait.
const (
	taskWaitDefault = 30 * time.Second
	taskWaitMax     = 45 * time.Second
	taskLogDefault  = 10
)

func (s *Server) addTaskTools(srv *sdk.Server, remote bool) {
	readOnly := &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolp(false)}

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_run", Title: "Hand Pinky a task",
		Description: "Hand a whole task to Pinky's own agent, which does it on the phone by itself with the model the person chose in Pinky, " +
			"instead of you driving the phone step by step: for simple tasks you need only the outcome of (set an alarm, look something up in an app). " +
			"Returns at once with a task_id; follow it with phone_task_wait until it ends, then read its summary. " +
			"The task works as you: your first connection asks the person, your scope and their rules apply, and the person sees it in Pinky and may stop it or take over. " +
			"You cannot answer its approvals; the person does. One task a phone at a time, and not on a phone you hold: release it first. " +
			"Each task has the step, time and cost limits the person set for Ask Pinky.",
		InputSchema: schema[taskRunIn](nil),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(true)}},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskRunIn) (*sdk.CallToolResult, any, error) {
			return s.taskRun(req, remote, in), nil, nil
		})

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_status", Title: "Follow a task",
		Description: "Return how a task you handed Pinky is going: running, waiting for the person, or how it ended with its summary; and its latest steps.",
		InputSchema: schema[taskStatusIn](nil), Annotations: readOnly},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskStatusIn) (*sdk.CallToolResult, any, error) {
			t, res := s.taskFind(req, remote, in.TaskID)
			if res != nil {
				return res, nil, nil
			}
			return s.taskResult(t, in.SinceStep, ""), nil, nil
		})

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_wait", Title: "Wait for a task",
		Description: "Wait until a task you handed Pinky ends or starts waiting for the person, up to timeout_ms; then return as phone_task_status does. " +
			"If it is still running, call it again. While it waits for the person (an approval, a handoff), there is nothing for you to do but wait or tell them.",
		InputSchema: schema[taskWaitIn](func(p map[string]*jsonschema.Schema) {
			bounds(p["timeout_ms"], 0, float64(taskWaitMax/time.Millisecond))
		}),
		Annotations: readOnly},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskWaitIn) (*sdk.CallToolResult, any, error) {
			return s.taskWait(ctx, req, remote, in), nil, nil
		})

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_cancel", Title: "Cancel a task",
		Description: "Stop a task you handed Pinky, at once, and free the phone. It ends as cancelled; what it already did on the phone stays done.",
		InputSchema: schema[taskIDIn](nil),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolp(false), IdempotentHint: true, OpenWorldHint: boolp(false)}},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskIDIn) (*sdk.CallToolResult, any, error) {
			tr, agent, res := s.taskAgent(req, remote)
			if res != nil {
				return res, nil, nil
			}
			t, err := tr.Cancel(agent.Name, strings.TrimSpace(in.TaskID))
			if err != nil {
				return taskNotFound(in.TaskID), nil, nil
			}
			return s.taskResult(t, len(t.Log)+t.First, ""), nil, nil
		})

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_steer", Title: "Steer a task",
		Description: "Tell Pinky's agent more about a task you handed it: a correction, or what to do next. " +
			"While the task runs, it reads the message before its next step; once it ended (done, failed, a limit, cancelled), the message carries the same task on, with the limits counting anew. " +
			"A task the person stopped cannot be carried on. The message approves nothing: risky steps still wait for the person. " +
			"Then follow the task with phone_task_wait.",
		InputSchema: schema[taskSteerIn](nil),
		Annotations: &sdk.ToolAnnotations{DestructiveHint: boolp(true), OpenWorldHint: boolp(true)}},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskSteerIn) (*sdk.CallToolResult, any, error) {
			return s.taskSteer(req, remote, in), nil, nil
		})

	sdk.AddTool(srv, &sdk.Tool{Name: "phone_task_list", Title: "List your tasks",
		Description: "List the tasks you handed Pinky in the last day, running and ended, the newest first, with their task_id.",
		InputSchema: schema[taskListIn](nil), Annotations: readOnly},
		func(ctx context.Context, req *sdk.CallToolRequest, in taskListIn) (*sdk.CallToolResult, any, error) {
			tr, agent, res := s.taskAgent(req, remote)
			if res != nil {
				return res, nil, nil
			}
			return s.taskList(tr.List(agent.Name)), nil, nil
		})
}

// taskAgent returns the tasks and the asking agent, or the result that
// says why it may not hand Pinky tasks.
func (s *Server) taskAgent(req *sdk.CallToolRequest, remote bool) (Tasks, gateway.AgentInfo, *sdk.CallToolResult) {
	s.mu.Lock()
	tr := s.tasks
	_, builtin := s.builtin[req.Session]
	agent := s.agentLocked(req, remote)
	s.mu.Unlock()
	switch {
	case builtin:
		return nil, agent, render(&gateway.Result{Error: gateway.ErrUnsupported, Message: "Pinky's own agent hands no tasks to itself."})
	case tr == nil:
		return nil, agent, render(&gateway.Result{Error: gateway.ErrUnsupported,
			Message: "Pinky's own agent runs tasks only while Pinky's window is open, so that the person can follow them. Drive the phone with phone_connect and the other phone_* tools instead."})
	}
	return tr, agent, nil
}

func (s *Server) taskFind(req *sdk.CallToolRequest, remote bool, id string) (Task, *sdk.CallToolResult) {
	tr, agent, res := s.taskAgent(req, remote)
	if res != nil {
		return Task{}, res
	}
	t, ok := tr.Task(agent.Name, strings.TrimSpace(id))
	if !ok {
		return Task{}, taskNotFound(id)
	}
	return t, nil
}

func taskNotFound(id string) *sdk.CallToolResult {
	return render(&gateway.Result{Error: gateway.ErrNotFound,
		Message: fmt.Sprintf("You handed Pinky no task %q in the last day. phone_task_list lists yours.", strings.TrimSpace(id))})
}

func (s *Server) taskSteer(req *sdk.CallToolRequest, remote bool, in taskSteerIn) *sdk.CallToolResult {
	t, res := s.taskFind(req, remote, in.TaskID)
	if res != nil {
		return res
	}
	if strings.TrimSpace(in.Message) == "" {
		return render(&gateway.Result{Error: gateway.ErrInvalidArgument, Message: "message is required: what Pinky's agent should do, in words."})
	}
	tr, agent, _ := s.taskAgent(req, remote)
	running := t.Running()
	t, err := tr.Steer(agent.Name, t.ID, in.Message)
	if err != nil {
		if te, ok := err.(*TaskError); ok {
			return render(&gateway.Result{Error: te.Code, Message: te.Message})
		}
		return render(&gateway.Result{Error: gateway.ErrInternal, Message: err.Error()})
	}
	lead := "Pinky's agent reads your message before its next step. Follow the task with phone_task_wait."
	if !running {
		lead = "Pinky's agent carries the task on with your message, within " + taskLimits(t) + " from now. Follow it with phone_task_wait."
	}
	return s.taskResult(t, len(t.Log)+t.First, lead)
}

func (s *Server) taskRun(req *sdk.CallToolRequest, remote bool, in taskRunIn) *sdk.CallToolResult {
	tr, agent, res := s.taskAgent(req, remote)
	if res != nil {
		return res
	}
	if strings.TrimSpace(in.Task) == "" {
		return render(&gateway.Result{Error: gateway.ErrInvalidArgument, Message: "task is required: the whole task, in words."})
	}
	device, res := s.taskDevice(strings.TrimSpace(in.Device))
	if res != nil {
		return res
	}
	t, err := tr.Run(TaskRequest{Agent: agent, Device: device, Task: in.Task, Apps: in.Apps, ReadOnly: in.ReadOnly})
	if err != nil {
		if te, ok := err.(*TaskError); ok {
			return render(&gateway.Result{Error: te.Code, Message: te.Message})
		}
		return render(&gateway.Result{Error: gateway.ErrInternal, Message: err.Error()})
	}
	return s.taskResult(t, 0, fmt.Sprintf("Pinky's own agent started “%s” on %s with %s, within %s. "+
		"The person sees it in Pinky. Follow it with phone_task_wait.", oneLine(t.Task, 120), s.phoneName(t.Device), t.Model, taskLimits(t)))
}

// taskDevice resolves the phone of phone_task_run to its device id: by id,
// name, or the start of its id; with none, the one phone online.
func (s *Server) taskDevice(ref string) (string, *sdk.CallToolResult) {
	var devs []gateway.DeviceView
	if s.g != nil {
		devs = s.g.Devices()
	}
	var list []string
	for _, d := range devs {
		list = append(list, fmt.Sprintf("%s (device %q, %s)", d.Info.Name, d.Info.ID, d.Status))
	}
	listed := strings.Join(list, ", ")
	if listed == "" {
		listed = "none"
	}
	if ref != "" {
		for _, match := range []func(d gateway.DeviceView) bool{
			func(d gateway.DeviceView) bool { return d.Info.ID == ref },
			func(d gateway.DeviceView) bool { return strings.EqualFold(d.Info.Name, ref) },
			func(d gateway.DeviceView) bool {
				return len(ref) >= 4 && strings.HasPrefix(strings.ToLower(d.Info.ID), strings.ToLower(ref))
			},
		} {
			var found []string
			for _, d := range devs {
				if match(d) {
					found = append(found, d.Info.ID)
				}
			}
			if len(found) == 1 {
				return found[0], nil
			}
		}
		return "", render(&gateway.Result{Error: gateway.ErrInvalidArgument, Message: fmt.Sprintf("No phone matches %q. Connected phones: %s.", ref, listed)})
	}
	var online []string
	for _, d := range devs {
		if d.Status != gateway.DeviceOffline {
			online = append(online, d.Info.ID)
		}
	}
	switch len(online) {
	case 0:
		return "", render(&gateway.Result{Error: gateway.ErrDeviceOffline, Message: "No phone is connected to Pinky. Ask the person to connect one."})
	case 1:
		return online[0], nil
	}
	return "", render(&gateway.Result{Error: gateway.ErrInvalidArgument, Message: fmt.Sprintf("Several phones are connected: %s. Pass device.", listed)})
}

func (s *Server) phoneName(id string) string {
	if s.g != nil {
		for _, d := range s.g.Devices() {
			if d.Info.ID == id && d.Info.Name != "" {
				return d.Info.Name
			}
		}
	}
	return id
}

func (s *Server) taskWait(ctx context.Context, req *sdk.CallToolRequest, remote bool, in taskWaitIn) *sdk.CallToolResult {
	t, res := s.taskFind(req, remote, in.TaskID)
	if res != nil {
		return res
	}
	tr, agent, _ := s.taskAgent(req, remote)
	timeout := taskWaitDefault
	if in.TimeoutMS > 0 {
		timeout = min(max(time.Duration(in.TimeoutMS)*time.Millisecond, time.Second), taskWaitMax)
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	report := progress(ctx, req)
	// The number of the first step not yet notified. A step's line is in
	// the log once the step is done, while Steps counts a phone tool as it
	// starts, and messages the task read are steps of the log too: each
	// new line of the log is notified under its own number, the one the
	// answer's Steps list gives it.
	unsent := t.First + len(t.Log)
	// It waits through the steps, and returns at the end or once the
	// person is asked: what the agent may want to tell them about.
	for t.Running() && t.Waiting == "" && wctx.Err() == nil {
		next, err := tr.Wait(wctx, agent.Name, t.ID, t.Version)
		if err != nil {
			break
		}
		t = next
		if report == nil {
			continue
		}
		for i := max(unsent-t.First, 0); i < len(t.Log); i++ {
			report(float64(t.First+i), float64(t.MaxSteps), stepLine(t.Log[i]))
		}
		unsent = max(unsent, t.First+len(t.Log))
	}
	if ctx.Err() != nil {
		return render(&gateway.Result{Error: gateway.ErrInternal, Message: "The call was cancelled; the task goes on."})
	}
	return s.taskResult(t, in.SinceStep, "")
}

// taskStatus is a task's status word: running, waiting_for_person, or how
// it ended.
func taskStatus(t Task) string {
	switch {
	case t.Running() && t.Waiting != "":
		return "waiting_for_person"
	case t.Running():
		return "running"
	case t.End == "steps" || t.End == "time" || t.End == "cost":
		return "limit_reached"
	}
	return t.End
}

// taskResult renders a task: its status line, what it means, and its
// steps after since (0: the last few).
func (s *Server) taskResult(t Task, since int, lead string) *sdk.CallToolResult {
	var b strings.Builder
	parts := []string{taskStatus(t), "task_id: " + t.ID, s.phoneName(t.Device)}
	if t.End == "steps" || t.End == "time" || t.End == "cost" {
		parts = append(parts, "limit: "+t.End)
	}
	if t.Waiting != "" && t.Running() {
		parts = append(parts, "waiting: "+t.Waiting)
	}
	parts = append(parts, fmt.Sprintf("steps: %d", t.Steps))
	b.WriteString(strings.Join(parts, " · ") + "\n")
	if lead != "" {
		b.WriteString(lead + "\n")
	}
	b.WriteString(taskMessage(t) + "\n")
	if lead == "" {
		fmt.Fprintf(&b, "Task: “%s”. %s.\n", oneLine(t.Task, 200), taskUsage(t))
	}
	first := since + 1
	if since <= 0 {
		first = t.First + len(t.Log) - taskLogDefault
	}
	first = max(first, t.First)
	if n := t.First + len(t.Log) - first; n > 0 {
		b.WriteString("Steps:\n")
		for i := first - t.First; i < len(t.Log); i++ {
			fmt.Fprintf(&b, "%d. %s\n", t.First+i, stepLine(t.Log[i]))
		}
	}
	isErr := !t.Running() && t.End != "done" && t.End != "failed" && t.End != "cancelled" && t.End != "stopped"
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.TrimRight(b.String(), "\n")}}, IsError: isErr}
}

// taskMessage says what a task's state means for the agent.
func taskMessage(t Task) string {
	if t.Running() {
		switch t.Waiting {
		case "approval":
			return "Pinky's agent waits for the person to decide on an approval in Pinky; it goes on once they answer. You cannot answer it: wait, or tell the person."
		case "handoff":
			return "Pinky's agent asked the person to do something on the phone, and goes on once they hand it back. Wait, or tell the person."
		}
		return "Pinky's agent is working on it. Call phone_task_wait to wait for its end."
	}
	sum := strings.TrimSpace(t.Summary)
	switch t.End {
	case "done":
		return "Pinky's agent did the task. Its summary: " + nonEmpty(sum, "(none)")
	case "failed":
		return "Pinky's agent could not do the task. Its summary: " + nonEmpty(sum, "(none)")
	case "stopped":
		return "The person stopped the task in Pinky. Do not start it again unless they ask you to."
	case "cancelled":
		return "You cancelled the task."
	case "steps", "time", "cost":
		return "The task reached the limit the person set for Ask Pinky in Pinky's Settings › Agents, and ended with what it had done. " + sum
	case "refused":
		return "The model declined this task."
	case "no_key":
		return "Pinky has no API key for its model. Ask the person to add one in Pinky's Settings › Agents › Ask Pinky, or drive the phone with the phone_* tools yourself."
	case "bad_key":
		return "The model's API did not accept Pinky's key: " + nonEmpty(sum, "no reason given") + ". Ask the person to fix it in Pinky's Settings › Agents › Ask Pinky."
	}
	return "The task failed: " + nonEmpty(sum, "no reason given") + "."
}

// taskUsage is how far a task went against its limits.
func taskUsage(t Task) string {
	mins := max(int((t.Elapsed - t.Waited).Minutes()), 0)
	if t.Continues > 0 {
		// The limits count from the last Continue; the totals do not.
		used := fmt.Sprintf("%d tokens", t.Tokens)
		if t.Priced {
			used = fmt.Sprintf("US$%.2f", t.Cost)
		}
		return fmt.Sprintf("%s: %d steps, %d minutes and %s in all; continued %d times, each time within %s",
			t.Model, t.Steps, mins, used, t.Continues, taskLimits(t))
	}
	cost := fmt.Sprintf("%d of %d tokens", t.Tokens, t.MaxTok)
	if t.Priced {
		cost = fmt.Sprintf("US$%.2f of US$%.2f", t.Cost, t.MaxCost)
	}
	return fmt.Sprintf("%s: %d of %d steps, %d of %d minutes, %s", t.Model, t.Steps, t.MaxSteps, mins, int(t.MaxTime.Minutes()), cost)
}

// taskLimits names a task's limits.
func taskLimits(t Task) string {
	cost := fmt.Sprintf("%d tokens", t.MaxTok)
	if t.Priced {
		cost = fmt.Sprintf("US$%.2f", t.MaxCost)
	}
	return fmt.Sprintf("%d steps, %d minutes and %s", t.MaxSteps, int(t.MaxTime.Minutes()), cost)
}

// stepLine is a step on a line: tool, status, and why.
func stepLine(st TaskStep) string {
	if st.Tool == gateway.PersonStep {
		if st.From != "" {
			return "your message: “" + st.Note + "”"
		}
		return "the person's message in Pinky: “" + st.Note + "”"
	}
	line := st.Tool + " " + st.Status
	if st.Intent != "" {
		line += " — " + st.Intent
	}
	if st.Note != "" {
		line += " (" + st.Note + ")"
	}
	return line
}

func (s *Server) taskList(ts []Task) *sdk.CallToolResult {
	if len(ts) == 0 {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "settled\nYou handed Pinky no tasks in the last day."}}}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "settled · tasks: %d\n", len(ts))
	for _, t := range ts {
		fmt.Fprintf(&b, "%s · %s · %s · steps: %d · started %s · “%s”\n", t.ID, taskStatus(t), s.phoneName(t.Device), t.Steps,
			t.Started.Format("15:04"), oneLine(t.Task, 80))
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.TrimRight(b.String(), "\n")}}}
}

// oneLine puts words on one line of at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}
