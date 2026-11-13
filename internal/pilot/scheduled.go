package pilot

import (
	"fmt"
	"strings"
	"time"
)

// Scheduled is how a scheduled task differs from one the person starts
// (internal/schedule, docs/notes/schedules.md).
type Scheduled struct {
	// Unattended says nobody pressed Run now: the person is likely away
	// from the phone and the computer. The agent never pays, and does not
	// hand the phone to the person, but ends the task saying what it
	// needed.
	Unattended bool
	// Condition, when set, is when the person wants to hear of the result
	// ("it will rain"): the agent says in finish whether it holds
	// (Status.Met).
	Condition string
	// Last is the last time the task ended done, and LastMet the last time
	// its condition held, for the agent to compare with ("cheaper than
	// last time").
	Last, LastMet *Earlier
	// Zone is the person's time zone, for the time the agent is told; nil
	// is the computer's. Go on Android knows no local zone.
	Zone *time.Location
}

// Earlier is an earlier run of a scheduled task.
type Earlier struct {
	At      time.Time
	Summary string
}

// StartScheduled starts a scheduled task on a phone, as Start does.
func (r *Runner) StartScheduled(cfg Config, device, task string, s Scheduled) error {
	_, err := r.start(cfg, Request{Device: device, Task: task, Scheduled: &s})
	return err
}

// conditionMet is finish's argument that says whether a scheduled task's
// condition holds.
const conditionMet = "condition_met"

// finishFor is the finish tool of a task: with condition_met when it has a
// condition.
func finishFor(s *Scheduled) tool {
	if s == nil || s.Condition == "" {
		return finishTool
	}
	t := finishTool
	props := map[string]any{}
	for k, v := range finishTool.schema["properties"].(map[string]any) {
		props[k] = v
	}
	props[conditionMet] = map[string]any{"type": "boolean",
		"description": "Whether the person's condition holds, from what you found on the phone. The person hears of the result only when it does. false when you could not tell."}
	t.schema = map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []any{"outcome", "summary", conditionMet},
	}
	return t
}

// scheduledPrompt is what the system prompt adds for a scheduled task.
func scheduledPrompt(s *Scheduled) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nThis task was scheduled: the person set it to run at set times, and Pinky started it at one of them.")
	if s.Unattended {
		b.WriteString(` Nobody is watching it: the person is likely away from the phone and the computer, and reads finish's summary later in a notification.
- Never pay, buy, order, subscribe, tip, donate or move money, and never confirm a payment, even when the task seems to ask for it. If the task needs one of these, finish with failed and say in the summary that you skipped that step and why.
- You cannot hand the phone to the person (there is no phone_handoff). When the phone needs them (a sign-in, an unlock, a code, a choice only they can make) or is locked, in use or paused, finish with failed and say what it needs.
- phone_request_approval reaches the person as a notification, but they may not answer in time; no answer is a denial. After a denial, finish with failed and say which step you skipped.
- Leave the phone as you found it where you can: do not leave a half-written message or an open form.`)
	}
	if s.Condition != "" {
		b.WriteString(`
- The person wants to hear of the result only when their condition (in the first message) holds. Find out what the task asks, then decide whether the condition holds and say so in finish's condition_met; write the summary either way, with what you found. When the condition compares with an earlier result ("cheaper than last time", "anything new"), compare with the earlier results in the first message.`)
	}
	return b.String()
}

// scheduledMessage is what the first message adds for a scheduled task:
// its condition and earlier results.
func scheduledMessage(s *Scheduled) string {
	if s == nil || s.Condition == "" && s.Last == nil {
		return ""
	}
	var b strings.Builder
	if s.Condition != "" {
		fmt.Fprintf(&b, "Tell the person only if, in their words:\n<condition>\n%s\n</condition>\n\n", strings.TrimSpace(s.Condition))
	}
	earlier := func(what string, e *Earlier) {
		fmt.Fprintf(&b, "%s (%s):\n<earlier>\n%s\n</earlier>\n\n", what, e.At.In(zone(s)).Format("Monday 2 January 2006, 15:04"), strings.TrimSpace(e.Summary))
	}
	if s.Last != nil {
		earlier("The last time this task ran, you found", s.Last)
	}
	if s.LastMet != nil && s.Condition != "" && (s.Last == nil || !s.LastMet.At.Equal(s.Last.At)) {
		earlier("The last time the condition held, you found", s.LastMet)
	}
	return b.String()
}

// zone is the time zone the agent is told the time in.
func zone(s *Scheduled) *time.Location {
	if s != nil && s.Zone != nil {
		return s.Zone
	}
	return time.Local
}

// leftOutFor reports whether a task's model goes without a tool: an
// unattended task has nobody to hand the phone to.
func leftOutFor(s *Scheduled, name string) bool {
	return leftOut[name] || s != nil && s.Unattended && name == "phone_handoff"
}

// attendLocked makes a scheduled task the person continued (Continue) one
// they watch: the run that ended was the schedule's, recorded and told as
// such, and what follows is theirs. Its condition was answered by then,
// and a task nobody watched gets the rules of one they watch, which the
// model hears with the continue message. Its tools stay as they were, for
// the conversation and its prompt cache, so it still has no phone_handoff.
// A person's Steer during the run changes none of this: the run stays
// unattended until it ends. The caller holds ru.mu.
func (ru *run) attendLocked() {
	s := ru.st.Scheduled
	if s == nil {
		return
	}
	c := *s
	if c.Unattended {
		ru.attended = attendedNote
	}
	c.Unattended, c.Condition = false, ""
	ru.st.Scheduled, ru.st.Met = &c, nil
}

// attendedNote is what the continue message adds for a scheduled task
// that nobody watched.
const attendedNote = `This task was scheduled and ran with nobody watching. The person has continued it themselves, so they are watching now: the rules for a task nobody watches no longer hold, and a payment or another risky step goes to them for approval as in any task. Its condition was answered by the run that ended: give condition_met as false. You still have no phone_handoff: when the phone needs them (a sign-in, a code, an unlock), finish with failed and say what it needs; they can continue the task again once it is done.`
