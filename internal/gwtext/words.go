package gwtext

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DIYgod/pinky/cmd/pinky/locales"
	"github.com/DIYgod/pinky/internal/gateway"
)

// Helpers the gateway's words share with desktop Pinky's own: the rules'
// names, scopes, lists and the cases of a sentence's first letter.

// ruleNames are the rules of the policy presets and the built-in ones, as
// approvals and steps name them.
var ruleNames = map[string]func() string{
	"Payments":           locales.RulePayments,
	"Messages":           locales.RuleMessages,
	"Commits":            locales.RuleCommits,
	"Installs and links": locales.RuleInstallsLinks,
	"Calls":              locales.RuleCalls,
	"Settings changes":   locales.RuleSettingsChanges,
	"Phone settings":     locales.RuleSettingsPhone,
	"Every action":       locales.RuleEveryAction,
	"Blocked by you":     locales.RuleBlockedByYou,
	"credentials":        locales.RuleCredentials,
	"Read-only":          locales.RuleReadOnly,
	"Task apps":          locales.RuleTaskApps,
	"Agent scope":        locales.RuleAgentScope,
	"Finance apps":       locales.RuleFinanceApps,
	"Password managers":  locales.RulePasswordManagers,
	"Unsafe links":       locales.RuleUnsafeLinks,
	"Files":              locales.RuleFiles,
	"Recordings":         locales.RuleRecordings,
	"App logs":           locales.RuleAppLogs,
	"Agent request":      locales.RuleAgentRequest,

	"Installing from the computer": locales.RuleInstallFromComputer,
	"Uninstalling apps":            locales.RuleUninstallApps,
	"Pinky's own apps":             locales.RulePinkyApps,
	"System apps":                  locales.RuleSystemApps,
}

// ruleName names a rule as the person reads it: "Payments". A rule of the
// person's own rules file keeps its name.
func ruleName(rule string) string {
	if f := ruleNames[rule]; f != nil {
		return f()
	}
	return rule
}

// joinAnd joins names for a sentence: "Dinely, Chatter and Calendar".
func joinAnd(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	case 2:
		return locales.ListPair(s[0], s[1])
	}
	return locales.ListMany(strings.Join(s[:len(s)-1], locales.ListSeparator()), s[len(s)-1])
}

// imperative turns a step's past tense into what was asked: "Tapped “Pay”"
// becomes "tap “Pay”".
func imperative(s string) string {
	for _, v := range [][2]string{
		{"Long-pressed ", "long-press "}, {"Tapped ", "tap "}, {"Typed ", "type "}, {"Swiped ", "swipe "},
		{"Pressed ", "press "}, {"Opened ", "open "}, {"Zoomed ", "zoom "}, {"Turned ", "turn "},
		{"Dragged ", "drag "},
	} {
		if rest, ok := strings.CutPrefix(s, v[0]); ok {
			return v[1] + rest
		}
	}
	return lowerFirst(s)
}

// scopeLine describes a scope in Settings › Agents.
func scopeLine(s gateway.Scope) string {
	switch s.Mode {
	case gateway.ScopeApps:
		if len(s.Apps) == 0 {
			return locales.ScopeLineNoApp()
		}
		return locales.ScopeLineApps(joinAnd(s.Apps))
	case gateway.ScopeReadOnly:
		return locales.ScopeLineReadOnly()
	case gateway.ScopeWatchOnly:
		return locales.ScopeLineWatchOnly()
	}
	return locales.ScopeLineEverything()
}

// newAgentNeed reports an agent's first-connection approval.
func newAgentNeed(n gateway.NeedView) bool {
	return n.Kind == gateway.NeedApproval && n.Rule == "New agent"
}

// lowerFirst lowers a sentence's first letter for use mid-sentence,
// keeping acronyms such as "USB" as they are. It works on runes: text in
// Chinese, Japanese or Russian keeps its first character whole.
func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || !unicode.IsUpper(r) {
		return s
	}
	if next, _ := utf8.DecodeRuneInString(s[n:]); unicode.IsUpper(next) {
		return s // an acronym
	}
	return string(unicode.ToLower(r)) + s[n:]
}
