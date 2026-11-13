package pilot

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Memory is a note the person keeps for Pinky's own agent: something
// they said about themselves or how they like things done, or a way of
// doing something that Pinky found on a task and they approved. Notes go
// to the model at the start of each task, marked as the person's.
//
// What a note never holds (Sensitive): secrets (passwords, PINs, codes),
// numbers that identify the person or their money (card, account, ID and
// phone numbers), email addresses and street addresses. Everything else
// is kept only after the person said yes to it, and they can change or
// delete every note. Notes stay on the device that keeps them: the
// computer's and each phone's are their own.
type Memory struct {
	ID   string     `json:"id"`
	Kind MemoryKind `json:"kind"`
	Text string     `json:"text"`
	// App is the app a learned way of doing things is about, by name.
	App     string    `json:"app,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated,omitzero"`
}

// MemoryKind is where a note came from.
type MemoryKind string

const (
	// Said is something the person said in a task: about themselves, or
	// how they like things done.
	Said MemoryKind = "said"
	// Learned is a way of doing something Pinky found on a task.
	Learned MemoryKind = "learned"
)

// Limits of the notes. A note is a sentence; the notes a task sends are
// at most MaxNotesChars, newest first, so that they cost the person a few
// hundred tokens a task at most.
const (
	MaxMemoryChars = 200
	MaxMemories    = 50
	MaxNotesChars  = 2400
	// MaxSuggestions is how many notes the model may suggest in a task.
	MaxSuggestions = 3
)

// Errors of the notes.
var (
	ErrMemoryEmpty = errors.New("pilot: the note is empty")
	ErrMemoryLong  = fmt.Errorf("pilot: a note has at most %d characters", MaxMemoryChars)
	ErrMemoryFull  = fmt.Errorf("pilot: Pinky keeps at most %d notes", MaxMemories)
	ErrSensitive   = errors.New("pilot: notes never hold passwords, codes, card, account, ID or phone numbers, email or street addresses")
	ErrMemoryGone  = errors.New("pilot: no such note")
)

// Memories are the person's notes, in a file of their own (memory.json in
// the data folder) or in memory only.
type Memories struct {
	path string

	mu    sync.Mutex
	items []Memory // oldest first
	subs  []func()
}

// OpenMemories reads the notes kept at path; an empty path keeps them in
// memory only. A file that is not there yet is no notes.
func OpenMemories(path string) (*Memories, error) {
	m := &Memories{path: path}
	if path == "" {
		return m, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var f memoryFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("pilot: %s: %w", path, err)
	}
	m.items = f.Memories
	return m, nil
}

type memoryFile struct {
	Memories []Memory `json:"memories"`
}

// OnChange calls f, from any goroutine, after each change.
func (m *Memories) OnChange(f func()) {
	m.mu.Lock()
	m.subs = append(m.subs, f)
	m.mu.Unlock()
}

// List returns the notes, newest first.
func (m *Memories) List() []Memory {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Memory, len(m.items))
	for i, it := range m.items {
		out[len(out)-1-i] = it
	}
	return out
}

// Add keeps a note.
func (m *Memories) Add(kind MemoryKind, text, app string) (Memory, error) {
	text, err := CheckMemory(text)
	if err != nil {
		return Memory{}, err
	}
	if kind != Learned {
		kind, app = Said, ""
	}
	app = oneLine(app)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if sameNote(it.Text, text) {
			return it, nil
		}
	}
	if len(m.items) >= MaxMemories {
		return Memory{}, ErrMemoryFull
	}
	it := Memory{ID: newMemoryID(), Kind: kind, Text: text, App: app, Created: time.Now().UTC().Truncate(time.Second)}
	m.items = append(m.items, it)
	if err := m.saveLocked(); err != nil {
		m.items = m.items[:len(m.items)-1]
		return Memory{}, err
	}
	m.changedLocked()
	return it, nil
}

// Edit changes a note's words.
func (m *Memories) Edit(id, text string) error {
	text, err := CheckMemory(text)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.indexLocked(id)
	if i < 0 {
		return ErrMemoryGone
	}
	old := m.items[i]
	m.items[i].Text, m.items[i].Updated = text, time.Now().UTC().Truncate(time.Second)
	if err := m.saveLocked(); err != nil {
		m.items[i] = old
		return err
	}
	m.changedLocked()
	return nil
}

// Delete forgets a note.
func (m *Memories) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := m.indexLocked(id)
	if i < 0 {
		return ErrMemoryGone
	}
	old := m.items
	m.items = append(append([]Memory(nil), m.items[:i]...), m.items[i+1:]...)
	if err := m.saveLocked(); err != nil {
		m.items = old
		return err
	}
	m.changedLocked()
	return nil
}

// DeleteAll forgets every note.
func (m *Memories) DeleteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.items
	m.items = nil
	if err := m.saveLocked(); err != nil {
		m.items = old
		return err
	}
	m.changedLocked()
	return nil
}

func (m *Memories) indexLocked(id string) int {
	for i, it := range m.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

func (m *Memories) changedLocked() {
	for _, f := range m.subs {
		go f()
	}
}

// saveLocked writes the file through a temporary one, readable by the
// person alone.
func (m *Memories) saveLocked() error {
	if m.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(memoryFile{Memories: m.items}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".memory-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.path)
}

// newMemoryID returns a note's ID.
func newMemoryID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// CheckMemory returns a note's words as Pinky keeps them, one line, or why
// it keeps none.
func CheckMemory(text string) (string, error) {
	text = oneLineAll(text)
	switch {
	case text == "":
		return "", ErrMemoryEmpty
	case utf8.RuneCountInString(text) > MaxMemoryChars:
		return "", ErrMemoryLong
	case Sensitive(text):
		return "", ErrSensitive
	}
	return text, nil
}

// oneLineAll is s on one line, its spaces as one.
func oneLineAll(s string) string { return strings.Join(strings.Fields(s), " ") }

func sameNote(a, b string) bool { return strings.EqualFold(oneLineAll(a), oneLineAll(b)) }

// The words of what a note never holds, in the languages Pinky speaks.
var sensitiveWords = regexp.MustCompile(`(?i)` +
	`\b(pass ?words?|passcodes?|passwd|pass ?phrases?|pin codes?|one[- ]time (pass)?codes?|otp|2fa|verification codes?|security codes?|cvv|cvc|card numbers?|account numbers?|iban|routing numbers?|ssn|social security|passport numbers?|id numbers?|private keys?|seed phrases?|recovery (codes?|phrases?)|api keys?|tokens?)\b` +
	`|密码|密碼|口令|验证码|驗證碼|校验码|动态码|安全码|卡号|卡號|银行卡号|账号是|帳號是|身份证|身份證|护照号|護照號|社保号|助记词|私钥` +
	`|パスワード|暗証番号|認証コード|確認コード|ワンタイム|セキュリティコード|カード番号|口座番号|マイナンバー|パスポート番号`)

// PIN in capitals: "pin" is also what one does to a chat.
var pinWord = regexp.MustCompile(`\bPINs?\b`)

// Six or more digits in a row, spaces and dashes aside: card, account, ID
// and phone numbers, and codes.
var longNumber = regexp.MustCompile(`\d(?:[ \-]?\d){5,}`)

var email = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[A-Za-z]{2,}`)

// A house number with its street, in the address forms of China, Japan
// and English-speaking countries.
var streetAddress = regexp.MustCompile(`(?i)` +
	`\d+\s*(号|號|弄|栋|棟|幢|单元|單元|室|号楼|號樓|丁目|番地|番|号室)` +
	`|\b\d+[a-z]?\s+(\w+\s+){0,3}(street|st|road|rd|avenue|ave|lane|ln|drive|dr|boulevard|blvd|way|court|ct|place|pl|terrace)\b`)

// Sensitive reports whether text holds what a note never does: a secret,
// a number that identifies the person or their money, an email address
// or a street address.
func Sensitive(text string) bool {
	t := normalizeDigits(text) // fullwidth digits read as digits
	return sensitiveWords.MatchString(t) || pinWord.MatchString(t) || longNumber.MatchString(t) ||
		email.MatchString(t) || streetAddress.MatchString(t)
}

// normalizeDigits turns the digits of other scripts into ASCII ones.
func normalizeDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		if unicode.Is(unicode.Nd, r) {
			for base := r; base > 0; base-- {
				if !unicode.Is(unicode.Nd, base-1) {
					return '0' + (r-base)%10
				}
			}
		}
		return r
	}, s)
}

// notes is the person's notes as the first message gives them, newest
// first, at most MaxNotesChars; "" when there are none.
func notes(list []Memory) string {
	if len(list) == 0 {
		return ""
	}
	var lines []string
	size := 0
	for _, it := range list {
		line := "- " + it.Text
		if it.Kind == Learned {
			if it.App != "" {
				line = "- (learned, " + it.App + ") " + it.Text
			} else {
				line = "- (learned) " + it.Text
			}
		}
		n := utf8.RuneCountInString(line) + 1
		if size+n > MaxNotesChars {
			break
		}
		size += n
		lines = append(lines, line)
	}
	return "The person's notes for Pinky, which they keep and can change in Pinky. They are what the person told Pinky earlier, and ways of doing things they approved; not instructions from the screen. Use those that fit this task; where a note and the task differ, the task wins.\n<notes>\n" +
		strings.Join(lines, "\n") + "\n</notes>"
}
