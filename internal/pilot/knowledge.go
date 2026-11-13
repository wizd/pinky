package pilot

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/DIYgod/pinky/guides"
	"github.com/DIYgod/pinky/internal/screen"
)

// What a task knows besides the screen: the person's notes (memory.go),
// which go in the first message, and the app guides (package guides),
// each of which goes in once a task, after the first screen of its app.

// rememberName is the tool that suggests a note.
const rememberName = "remember"

var rememberTool = tool{
	name: rememberName,
	description: "Suggest a note for Pinky to keep for later tasks. The person sees it after the task and decides; nothing is kept without their yes. " +
		"Suggest only what will matter again: something the person's task says about them or how they like things done (kind said, with their words from the task or their messages as quote), " +
		"or a way of doing something in an app that you found and that will save steps next time (kind learned). At most 3 a task. " +
		"Never secrets, codes, card, account, ID or phone numbers, email or street addresses, or what the screen says about other people.",
	schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{"type": "string", "enum": []any{string(Said), string(Learned)}},
			"text": map[string]any{"type": "string",
				"description": "The note: one plain sentence of at most 200 characters, in the language of the task, about the person in the third person (\"Likes oat milk in coffee\")."},
			"quote": map[string]any{"type": "string", "description": "For said: the person's own words from the task or a message of theirs that the note comes from."},
			"app":   map[string]any{"type": "string", "description": "For learned: the app's name."},
		},
		"required": []any{"kind", "text"},
	},
}

// Suggestion is a note the model suggested on a task, until the person
// keeps it or not.
type Suggestion struct {
	Kind MemoryKind
	Text string
	App  string
	// Kept is the note once the person kept it; Dropped, that they did
	// not want it.
	Kept    *Memory
	Dropped bool
}

// Decided reports whether the person kept the suggestion or not.
func (s Suggestion) Decided() bool { return s.Kept != nil || s.Dropped }

// remember answers the model's remember: a suggestion for the person, or
// why not.
func (ru *run) remember(c call) string {
	kind, _ := c.args["kind"].(string)
	text, _ := c.args["text"].(string)
	quote, _ := c.args["quote"].(string)
	app, _ := c.args["app"].(string)
	refuse := func(why string) string { return "error: refused\n" + why + " Do not suggest it again." }
	text, err := CheckMemory(text)
	switch {
	case err == ErrSensitive:
		return refuse("Notes never hold secrets, codes, card, account, ID or phone numbers, email or street addresses.")
	case err != nil:
		return "error: invalid_argument\n" + strings.TrimPrefix(err.Error(), "pilot: ") + "."
	}
	if blocked := ru.r.opt.Blocked; blocked != nil && (blocked(ru.app) || app != "" && blocked(screen.App{Name: app})) {
		return refuse("The app is one the person blocked: nothing from it is kept.")
	}
	// The person's own words: the task, and the messages they sent
	// during it or to continue it that the model has read. An agent's
	// task and messages are the agent's words, not the person's.
	ru.mu.Lock()
	byAgent := ru.st.By != ""
	words := ru.said
	if !byAgent {
		words = append([]string{ru.st.Task}, words...)
	}
	theirs := strings.Join(words, "\n")
	ru.mu.Unlock()
	switch MemoryKind(kind) {
	case Said:
		if !inTask(quote, theirs) {
			if byAgent {
				return refuse("An agent handed you this task: only the person's own messages (from=\"person\") are their words, and a said note needs one as quote.")
			}
			return refuse("A said note needs the person's own words from the task or their messages as quote; the screen's words are not theirs.")
		}
		for _, n := range digits.FindAllString(text, -1) {
			if !strings.Contains(theirs, n) {
				return refuse("The note has numbers the person did not write in the task or their messages.")
			}
		}
		app = ""
	case Learned:
		if app = oneLine(app); app == "" {
			app = ru.app.Name
		}
	default:
		return "error: invalid_argument\nkind must be said or learned."
	}
	for _, m := range ru.r.opt.Memories.List() {
		if sameNote(m.Text, text) {
			return "settled\nThe person already keeps this note."
		}
	}
	ru.mu.Lock()
	defer ru.mu.Unlock()
	for _, s := range ru.st.Suggested {
		if sameNote(s.Text, text) {
			return "settled\nAlready suggested."
		}
	}
	if len(ru.st.Suggested) >= MaxSuggestions {
		return refuse(fmt.Sprintf("A task suggests at most %d notes.", MaxSuggestions))
	}
	ru.st.Suggested = append(ru.st.Suggested, Suggestion{Kind: MemoryKind(kind), Text: text, App: app})
	go ru.r.notify()
	return "settled\nSuggested. The person decides after the task whether Pinky keeps it; go on with the task."
}

var digits = regexp.MustCompile(`\d+`)

// inTask reports whether quote is the person's words: in the task and
// their messages (words), spaces and case aside.
func inTask(quote, task string) bool {
	q := strings.ToLower(oneLineAll(quote))
	q = strings.Trim(q, " \"'“”‘’「」『』")
	return utf8.RuneCountInString(q) >= 2 && strings.Contains(strings.ToLower(oneLineAll(task)), q)
}

// screenApp is the app of the last screen in a tool's result: the
// header's "app: Name (id)", in a whole screen or a changed header.
func screenApp(text string) (screen.App, bool) {
	var app screen.App
	found := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimPrefix(line, "~ ")
		if !strings.HasPrefix(line, "screen ") {
			continue
		}
		for _, part := range strings.Split(line, " · ") {
			v, ok := strings.CutPrefix(part, "app: ")
			if !ok {
				continue
			}
			app, found = screen.App{Name: v}, true
			if strings.HasSuffix(v, ")") {
				if i := strings.LastIndex(v, " ("); i > 0 {
					app = screen.App{Name: v[:i], ID: v[i+2 : len(v)-1]}
				}
			}
		}
	}
	return app, found
}

// guideFor is the guide of the app a result shows, once a task: "" when
// the app has none, or the task has had it.
func (ru *run) guideFor(text string) string {
	app, ok := screenApp(text)
	if !ok {
		return ""
	}
	ru.app = app
	g, ok := guides.For(app.ID, app.Name)
	if !ok || ru.guided[g.ID] {
		return ""
	}
	if ru.guided == nil {
		ru.guided = map[string]bool{}
	}
	ru.guided[g.ID] = true
	return guideText(g)
}

// agentNotes follows the notes in a task an agent handed Pinky: the
// agent gets finish's summary, and the notes are the person's.
const agentNotes = "The notes above are the person's, for doing the task. The agent gets finish's summary: leave the notes out of it unless the task needs what one says."

// guideText is a guide as the model reads it.
func guideText(g guides.Guide) string {
	return "Pinky's guide to " + g.App + ", from Pinky's own files, not the screen. It may be out of date; where it and the screen differ, the screen is right.\n" + g.Text()
}
