package history

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// SecretMask is what history keeps in place of text typed into a password
// or one-time-code field (the gateway's secret.go), of one length whatever
// the text's.
const SecretMask = "••••••"

// secretKey is what history keeps in place of a key of an app's hidden
// passcode keypad.
const secretKey = "•"

// scrubbedVersion is the schema version from which the gateway keeps typed
// passwords and codes masked. History written before it may hold them in
// plain text: the steps Pinky refused under the credentials rule, and the
// typing it let through on virtual phones. Open scrubs them once.
const scrubbedVersion = 8

// clearedVersion is the schema version from which the database's files hold
// no text from before scrubTypedSecrets. SQLite leaves the old text of a
// changed row in the file, in space it freed, so Open rebuilds the file
// once, also when version 8 already masked the steps.
const clearedVersion = 9

// clearFreed rebuilds the database's file without the space SQLite freed,
// and empties its write-ahead log into it.
func clearFreed(db *sql.DB) error {
	if _, err := db.Exec("VACUUM"); err != nil {
		return err
	}
	_, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// scrubTypedSecrets masks, in steps from before scrubbedVersion, the text
// typed into password and code fields and the keys tapped on a hidden
// passcode keypad: in the step's words, arguments, intent and screen text,
// and in the title of the approval the step held.
func scrubTypedSecrets(db *sql.DB) error {
	type row struct {
		task                       string
		idx                        int
		args, intent, text, screen string
	}
	rs, err := db.Query(`SELECT task_id, idx, args, intent, text, screen_text FROM steps WHERE tool IN ('phone_type', 'phone_tap')`)
	if err != nil {
		return err
	}
	var rows []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.task, &r.idx, &r.args, &r.intent, &r.text, &r.screen); err != nil {
			rs.Close()
			return err
		}
		rows = append(rows, r)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		var args map[string]any
		if json.Unmarshal([]byte(r.args), &args) != nil {
			args = map[string]any{}
		}
		refused := strings.HasPrefix(r.text, "Blocked by “"+policy.RuleCredentials+"”: ")
		typed, _ := args["text"].(string)
		var secret string
		switch {
		case typed != "" && typed != SecretMask && strings.Contains(r.text, "Typed “"):
			if !refused && !typedIntoCredential(r.text, typed) {
				continue
			}
			secret = typed
			args["text"] = SecretMask
			r.text = strings.Replace(r.text, "Typed “"+clip(typed, 60)+"”", "Typed “"+SecretMask+"”", 1)
		case refused && strings.Contains(r.text, ": Tapped") && !strings.Contains(r.text, "Tapped “"+secretKey+"”"):
			// The app's hidden passcode keypad: which key, and where.
			head, _, _ := strings.Cut(r.text, ": Tapped")
			r.text = head + ": Tapped “" + secretKey + "”"
			delete(args, "ref")
			delete(args, "x")
			delete(args, "y")
		default:
			continue
		}
		if i, ok := args["intent"].(string); ok {
			args["intent"] = scrubText(i, secret)
		}
		b, err := json.Marshal(args)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE steps SET args = ?, intent = ?, text = ?, screen_text = ?, target_x = 0, target_y = 0, target_w = 0, target_h = 0, point_x = 0, point_y = 0
			WHERE task_id = ? AND idx = ?`, string(b), scrubText(r.intent, secret), r.text, scrubText(r.screen, secret), r.task, r.idx); err != nil {
			return err
		}
		if secret == "" {
			continue
		}
		// "Send “…”?" quotes the text; the quotes keep a short one from
		// matching other words.
		if _, err := tx.Exec(`UPDATE approvals SET title = replace(title, ?, ?) WHERE task_id = ? AND step_idx = ?`,
			"“"+clip(secret, 80)+"”", "“"+SecretMask+"”", r.task, r.idx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// typedIntoCredential reports whether a step's words ("Typed “…” into
// “Password”") name a field that takes a password or code.
func typedIntoCredential(text, typed string) bool {
	_, rest, ok := strings.Cut(text, "Typed “"+clip(typed, 60)+"” into “")
	if !ok {
		return false
	}
	label, _, ok := strings.Cut(rest, "”")
	return ok && policy.IsCredentialField(&screen.Element{Role: screen.RoleInput, Label: label})
}

// scrubText takes secret out of s; one or two characters stay, as they
// also stand in ordinary words.
func scrubText(s, secret string) string {
	if s == "" || len([]rune(strings.TrimSpace(secret))) < 3 {
		return s
	}
	return strings.ReplaceAll(s, strings.TrimSpace(secret), SecretMask)
}

// clip is the gateway's: steps show at most n characters of the text.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
