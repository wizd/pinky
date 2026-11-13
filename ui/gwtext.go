package ui

import "github.com/DIYgod/pinky/internal/gwtext"

// The gateway words steps, approvals and status lines in English;
// internal/gwtext says them in the language the app shows, for the client
// and for the cores on the phones alike.
var (
	stepText     = gwtext.Step
	stepSub      = gwtext.StepSub
	actionAsked  = gwtext.ActionAsked
	needTitle    = gwtext.NeedTitle
	needDetail   = gwtext.NeedDetail
	statusLine   = gwtext.StatusLine
	whereProblem = gwtext.WhereProblem
	imperative   = gwtext.Imperative
	lowerFirst   = gwtext.LowerFirst
	ruleName     = gwtext.RuleName
	joinAnd      = gwtext.JoinAnd
	scopeLine    = gwtext.ScopeLine
	newAgentNeed = gwtext.NewAgentNeed
	translated   = gwtext.Translated
	keyName      = gwtext.KeyName
)
