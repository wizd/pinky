package pilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// CheckResult is how a model answered Check.
type CheckResult struct {
	// OK says the model answered and called the tool it was given.
	OK bool `json:"ok"`
	// Problem names what went wrong, one of the Check constants, for the
	// screens to say in the person's language; empty when OK.
	Problem string `json:"problem,omitempty"`
	// Detail is the service's own words, or the network's, for Problem;
	// never the key.
	Detail string `json:"detail,omitempty"`
	// Status is the HTTP status the service answered with, 0 for none.
	Status int `json:"status,omitempty"`
	// Millis is how long the check took.
	Millis int64 `json:"ms"`
}

// The problems of CheckResult.
const (
	CheckNoModel     = "no_model"    // no model is chosen
	CheckNoKey       = "no_key"      // no key is saved, and the service needs one
	CheckBadKey      = "bad_key"     // the service did not accept the key (401, 403)
	CheckNoCredit    = "no_credit"   // the account has no money left (402)
	CheckNotFound    = "not_found"   // no such address or model (404)
	CheckBusy        = "busy"        // a rate limit or the service's own error (429, 5xx): try later
	CheckRejected    = "rejected"    // the service refused the request; Detail says why
	CheckNoTools     = "no_tools"    // the model answered without calling the tool
	CheckUnreachable = "unreachable" // no answer from the address
	CheckBadAnswer   = "bad_answer"  // an answer Pinky could not read
)

// checkWait bounds a check, its retries after a rate limit included.
const checkWait = 45 * time.Second

// checkTool is the one tool a check offers.
var checkTool = tool{
	name:        "ready",
	description: "Say that you are ready.",
	schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"word": map[string]any{"type": "string", "description": "The word ready."},
		},
		"required": []string{"word"},
	},
}

const (
	checkSystem = "You are being checked before you operate a phone through tools. Call the ready tool once, with the word ready."
	checkText   = "Call the ready tool now."
)

// Check sends the model of cfg one tiny request with one tool and reports
// whether it answered and called the tool: what a task needs of it. It
// costs a few hundred tokens. key may be empty for a service that needs
// none (Config.KeyOptional).
func Check(ctx context.Context, cfg Config, key string, hc *http.Client) CheckResult {
	r, _ := check(ctx, cfg, key, hc)
	return r
}

// check is Check, with what the request used.
func check(ctx context.Context, cfg Config, key string, hc *http.Client) (CheckResult, usage) {
	start := time.Now()
	cfg = cfg.Normalize()
	key = strings.TrimSpace(key)
	done := func(r CheckResult) CheckResult {
		r.OK = r.Problem == ""
		r.Millis = time.Since(start).Milliseconds()
		return r
	}
	switch {
	case cfg.Model == "":
		return done(CheckResult{Problem: CheckNoModel}), usage{}
	case key == "" && !cfg.KeyOptional():
		return done(CheckResult{Problem: CheckNoKey}), usage{}
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	ctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	m, err := newModel(cfg, key, hc, checkSystem, []tool{checkTool})
	if err != nil {
		return done(CheckResult{Problem: CheckRejected, Detail: err.Error()}), usage{}
	}
	t, err := m.send(ctx, nil, checkText)
	if err != nil {
		return done(checkError(redact(err, key))), usage{}
	}
	if len(t.calls) == 0 {
		return done(CheckResult{Problem: CheckNoTools, Detail: short(strings.TrimSpace(t.text))}), t.usage
	}
	return done(CheckResult{}), t.usage
}

// checkError is the problem of a failed request.
func checkError(err error) CheckResult {
	var ae *apiError
	if !errors.As(err, &ae) {
		var se *json.SyntaxError
		if errors.As(err, &se) || strings.Contains(err.Error(), "not JSON") {
			return CheckResult{Problem: CheckBadAnswer, Detail: short(err.Error())}
		}
		return CheckResult{Problem: CheckUnreachable, Detail: short(err.Error())}
	}
	r := CheckResult{Status: ae.status, Detail: short(ae.message)}
	msg := strings.ToLower(ae.message)
	switch {
	case ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden:
		r.Problem = CheckBadKey
	case ae.status == http.StatusPaymentRequired:
		r.Problem = CheckNoCredit
	case ae.status == http.StatusNotFound:
		r.Problem = CheckNotFound
	case ae.status == http.StatusTooManyRequests || ae.status >= 500:
		r.Problem = CheckBusy
	case strings.Contains(msg, "tool") || strings.Contains(msg, "function"):
		// "does not support tools", "function calling is not enabled"
		r.Problem = CheckNoTools
	default:
		r.Problem = CheckRejected
	}
	return r
}

// short is s cut to a line's worth.
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}

// Check checks the model of cfg as Check does, with the key Options.Key
// gives for it and the Runner's HTTP client, and counts what it used in
// Options.Usage. It blocks: call it off the UI's thread.
func (r *Runner) Check(ctx context.Context, cfg Config) CheckResult {
	cfg = cfg.Normalize()
	key := ""
	if r.opt.Key != nil {
		k, err := r.opt.Key(ctx, cfg.KeyName())
		if err != nil && !cfg.KeyOptional() {
			return CheckResult{Problem: CheckNoKey}
		}
		key = k
	}
	return r.CheckWith(ctx, cfg, key)
}

// CheckWith is Check with a key of the caller's, which it does not keep.
func (r *Runner) CheckWith(ctx context.Context, cfg Config, key string) CheckResult {
	cfg = cfg.Normalize()
	res, u := check(ctx, cfg, key, r.opt.HTTPClient)
	r.opt.Usage.add(time.Now(), cfg, u)
	return res
}
