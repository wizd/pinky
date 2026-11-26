package gateway

import (
	"image"
	"sort"
	"strings"

	"github.com/DIYgod/pinky/internal/history"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/screen"
)

// What an agent types into a password or one-time-code field, and the keys
// it taps on an app's hidden passcode keypad, are never kept or shown in
// plain text, wherever the person allowed the typing (Settings › Safety,
// on virtual phones and on phones) and when Pinky refused it. prep.secret
// marks such an action, and then:
//
//   - its step in Activity, live and in history, its approval card and the
//     step an approval holds say "Typed “••••••” into “Password”", and a
//     keypad tap "Tapped “•”" without where it went;
//   - its arguments in history (and so in exports) carry the mask for the
//     text, and its intent has the text taken out;
//   - the screen text history keeps after a step shows a password or code
//     field's content as the mask (hideTyped), and history screenshots
//     cover such fields where they show text (credentialPlaces).
//
// The agent's own result keeps the screen as it reads, field included: the
// agent gave the text, and checks the field with it. Screen recordings show
// the screen as the phone drew it, as for one-time codes (codes.go): they
// stay on this computer and reach an agent only when the person allows it.

// secretMask stands for the text typed into a password or code field: of
// one length, whatever the text's.
const secretMask = history.SecretMask

// secretKey stands for the key tapped on an app's hidden passcode keypad.
const secretKey = "•"

// hideSecret returns a with the text it types masked.
func hideSecret(a Action) Action {
	if a.Tool == "type" && a.Text != "" {
		a.Text = secretMask
	}
	if a.Tool == "tap" {
		// Which key of the passcode keypad.
		a.Ref, a.Point = 0, nil
	}
	return a
}

// shown returns the action and call as Pinky keeps and shows them: for a
// secret action (prep.secret), the text masked and taken out of the intent.
// An action on a password or code field that holds text has that text taken
// out of its intent too: the agent may quote the field as it read it ("Tap
// Verification code, 804217").
func shown(a Action, c Call, p *prep) (Action, Call) {
	if p != nil && p.target != nil && typedSecret(p.target) {
		c.Intent = scrubSecret(c.Intent, p.target.Content)
	}
	if p == nil || !p.secret {
		return a, c
	}
	if a.Tool == "type" {
		c.Intent = scrubSecret(c.Intent, a.Text)
	}
	return hideSecret(a), c
}

// scrubSecret takes secret out of s. Text this short also stands in
// ordinary words, so one or two characters stay.
func scrubSecret(s, secret string) string {
	secret = strings.TrimSpace(secret)
	if s == "" || len([]rune(secret)) < 3 {
		return s
	}
	return strings.ReplaceAll(s, secret, secretMask)
}

// typedSecret reports whether e is a text field that holds a password or
// code: a credential field with text in it.
func typedSecret(e *screen.Element) bool {
	return e.Content != "" && (e.Role == screen.RoleInput || e.Has(screen.StateEditable)) && policy.IsCredentialField(e)
}

// shownLabel returns e's label as steps and approval cards show it: a
// password or code field that holds text names it with the mask for the
// text, as the screen text history keeps does (hideTyped).
func shownLabel(e *screen.Element) string {
	if typedSecret(e) {
		return maskContent(e.Label, e.Content)
	}
	return e.Label
}

// hideTyped returns s with what its password and code fields hold masked,
// for the screen text history keeps; s itself when there is none.
func hideTyped(s *screen.Screen) *screen.Screen {
	if s == nil {
		return nil
	}
	var out *screen.Screen
	for i := range s.Elements {
		e := &s.Elements[i]
		if !typedSecret(e) {
			continue
		}
		if out == nil {
			c := *s
			c.Elements = append([]screen.Element(nil), s.Elements...)
			out = &c
		}
		m := &out.Elements[i]
		m.Label = maskContent(m.Label, m.Content)
		m.Content = secretMask
		// OCR reads the field's pixels too, and a line it misread is an
		// element of its own.
		for j := range out.Elements {
			o := &out.Elements[j]
			if o.Source != screen.SourceOCR {
				continue
			}
			if o.Label = scrubSecret(o.Label, e.Content); e.Bounds.Contains(o.Bounds.Center()) {
				o.Label = secretMask
			}
		}
	}
	if out == nil {
		return s
	}
	return out
}

// maskContent replaces the content a label shows (all of it, or its end
// after the field's name) with the mask.
func maskContent(label, content string) string {
	if label == content {
		return secretMask
	}
	if rest, ok := strings.CutSuffix(label, content); ok {
		return rest + secretMask
	}
	return strings.ReplaceAll(label, content, secretMask)
}

// credentialPlaces returns where reads show password and code fields with
// text in them, in the pixels of a w x h picture of the screen, and
// whether one could not be placed.
func credentialPlaces(w, h int, reads ...*screen.Screen) (boxes []image.Rectangle, unplaced bool) {
	for _, s := range reads {
		if s == nil {
			continue
		}
		for i := range s.Elements {
			e := &s.Elements[i]
			if !typedSecret(e) {
				continue
			}
			if e.Bounds.Empty() || s.Width <= 0 || s.Height <= 0 {
				unplaced = true
				continue
			}
			if r := scaleBox(e.Bounds, s.Width, s.Height, w, h); !r.Empty() {
				boxes = append(boxes, r)
			}
		}
	}
	return boxes, unplaced
}

// batchSecrets returns the texts a batch's type steps may type into a
// password or code field, longest first. A batch's intent goes with every
// step, and shown takes a typed password or code out of it only for the
// step that types it; these are taken out of every step's intent, a step's
// own included (scrubSecrets).
//
// Before the steps run, Pinky can tell only a type step by ref into a field
// of the screen the agent last read: the text for an ordinary field there
// is taken as ordinary and stays. A step without ref, or with one that
// screen lacks, may reach a password or code field on a screen the batch
// comes to, so its text counts.
func batchSecrets(g *Gateway, l *lease, steps []Action) []string {
	g.mu.Lock()
	seen := l.baseline
	g.mu.Unlock()
	var secrets []string
	for _, a := range steps {
		if a.Tool != "type" || a.Text == "" {
			continue
		}
		if a.Ref > 0 && seen != nil {
			if e, ok := seen.Find(a.Ref); ok && !policy.IsCredentialField(&e) {
				continue
			}
		}
		secrets = append(secrets, a.Text)
	}
	// A longer text first, so a shorter one inside it leaves none of it.
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

// scrubSecrets takes each of secrets out of s (scrubSecret).
func scrubSecrets(s string, secrets []string) string {
	for _, secret := range secrets {
		s = scrubSecret(s, secret)
	}
	return s
}
