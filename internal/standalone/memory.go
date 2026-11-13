package standalone

import (
	"errors"

	"github.com/DIYgod/pinky/internal/pilot"
)

// The notes the person keeps for Pinky on this phone (pilot.Memories):
// memory.json in DataDir, never shared with the computer or another
// phone. Tasks read them unless StartRequest.NoMemory, and the person
// keeps or drops each note a task suggests (AgentState.Suggested).

// MemoryState is a note as the screens show it.
type MemoryState struct {
	ID   string `json:"id"`
	Kind string `json:"kind"` // pilot.Said or pilot.Learned
	Text string `json:"text"`
	App  string `json:"app,omitempty"` // for learned: the app it is about
	// CreatedMS and UpdatedMS are Unix milliseconds; UpdatedMS is 0 for a
	// note never edited.
	CreatedMS int64 `json:"created_ms"`
	UpdatedMS int64 `json:"updated_ms,omitempty"`
}

// SuggestionState is a note the task suggested, until the person decides.
type SuggestionState struct {
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	App     string `json:"app,omitempty"`
	Kept    bool   `json:"kept,omitempty"`
	Dropped bool   `json:"dropped,omitempty"`
}

// The codes of the notes' errors.
const (
	CodeMemorySensitive = "memory_sensitive" // pilot.ErrSensitive
	CodeMemoryLong      = "memory_long"      // pilot.ErrMemoryLong: more than pilot.MaxMemoryChars
	CodeMemoryFull      = "memory_full"      // pilot.ErrMemoryFull: pilot.MaxMemories already
	CodeMemoryEmpty     = "memory_empty"     // pilot.ErrMemoryEmpty
	CodeMemoryGone      = "memory_gone"      // pilot.ErrMemoryGone: no such note, or decided already
)

// MemoryCode is the code of a notes command's error, or "".
func MemoryCode(err error) string {
	switch {
	case errors.Is(err, pilot.ErrSensitive):
		return CodeMemorySensitive
	case errors.Is(err, pilot.ErrMemoryLong):
		return CodeMemoryLong
	case errors.Is(err, pilot.ErrMemoryFull):
		return CodeMemoryFull
	case errors.Is(err, pilot.ErrMemoryEmpty):
		return CodeMemoryEmpty
	case errors.Is(err, pilot.ErrMemoryGone):
		return CodeMemoryGone
	}
	return ""
}

func suggestionStates(list []pilot.Suggestion) []SuggestionState {
	if len(list) == 0 {
		return nil
	}
	out := make([]SuggestionState, len(list))
	for i, s := range list {
		out[i] = SuggestionState{Kind: string(s.Kind), Text: s.Text, App: s.App, Kept: s.Kept != nil, Dropped: s.Dropped}
	}
	return out
}

// Memories returns the notes, newest first.
func (c *Core) Memories() []MemoryState {
	list := c.mem.List()
	out := make([]MemoryState, 0, len(list))
	for _, m := range list {
		s := MemoryState{ID: m.ID, Kind: string(m.Kind), Text: m.Text, App: m.App, CreatedMS: m.Created.UnixMilli()}
		if !m.Updated.IsZero() {
			s.UpdatedMS = m.Updated.UnixMilli()
		}
		out = append(out, s)
	}
	return out
}

// AddMemory keeps a note the person wrote.
func (c *Core) AddMemory(text string) error {
	_, err := c.mem.Add(pilot.Said, text, "")
	return err
}

// EditMemory changes a note's words.
func (c *Core) EditMemory(id, text string) error { return c.mem.Edit(id, text) }

// DeleteMemory forgets a note.
func (c *Core) DeleteMemory(id string) error { return c.mem.Delete(id) }

// DeleteAllMemories forgets every note.
func (c *Core) DeleteAllMemories() error { return c.mem.DeleteAll() }

// Keep keeps the note the task suggested at index i of
// AgentState.Suggested; Drop does not.
func (c *Core) Keep(i int) error {
	d, ok := c.device()
	if !ok {
		return pilot.ErrMemoryGone
	}
	return c.pilot.Keep(d.Info.ID, i)
}

// Drop answers no to the note the task suggested at index i.
func (c *Core) Drop(i int) error {
	d, ok := c.device()
	if !ok {
		return pilot.ErrMemoryGone
	}
	return c.pilot.Drop(d.Info.ID, i)
}
