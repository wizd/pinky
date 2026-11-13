// Package gwtext says the gateway's English words in the language the app
// shows (i18n.Locale, with the catalogs of cmd/pinky/locales): steps,
// approvals and status lines. Desktop Pinky shows them through it, and so
// do the cores on the phones, which add them to the state their screens
// read. What goes to agents and logs stays the gateway's English.
package gwtext

import "github.com/DIYgod/pinky/internal/gateway"

// Step says a step's first line.
func Step(s string) string { return stepText(s) }

// StepSub says a step's second line: the person's note, or the rule that
// let it run without asking.
func StepSub(s string) string { return stepSub(s) }

// ActionAsked says the action of a step that waited for the person, as
// the gateway words it after "Waiting for you: ".
func ActionAsked(past string) string { return actionAsked(past) }

// NeedTitle is the title of an approval, a handoff or another card that
// waits for the person.
func NeedTitle(n gateway.NeedView) string { return needTitle(n) }

// NeedDetail is such a card's second line.
func NeedDetail(n gateway.NeedView) string { return needDetail(n) }

// StatusLine is a phone's status line.
func StatusLine(d gateway.DeviceView) string { return statusLine(d) }

// WhereProblem says what is wrong with where Pinky runs from.
func WhereProblem(p string) string { return whereProblem(p) }

// Imperative turns a step's past tense into what was asked, in English:
// "Tapped “Pay”" becomes "tap “Pay”".
func Imperative(s string) string { return imperative(s) }

// LowerFirst lowers a sentence's first letter for use mid-sentence,
// keeping acronyms such as "USB" as they are.
func LowerFirst(s string) string { return lowerFirst(s) }

// RuleName names a rule as the person reads it.
func RuleName(rule string) string { return ruleName(rule) }

// JoinAnd joins a list in the language the app shows: "a, b and c".
func JoinAnd(s []string) string { return joinAnd(s) }

// ScopeLine describes what an agent's scope lets it do.
func ScopeLine(s gateway.Scope) string { return scopeLine(s) }

// NewAgentNeed reports an agent's first-connection approval.
func NewAgentNeed(n gateway.NeedView) bool { return newAgentNeed(n) }

// Translated reports whether the app shows another language than the
// gateway's English.
func Translated() bool { return translated() }

// KeyName names a key the agent presses: "Home", "Volume up".
func KeyName(k string) string { return keyName(k) }
