package pilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// The OpenAI API family has two ways to talk to a model: Chat
// Completions, which every OpenAI-compatible service speaks, and the
// Responses API, which OpenAI's own newer models need to call tools.
// Pinky uses Responses for the models of OpenAI's own address that call
// tools only through it, and Chat Completions for everything else, until
// a service refuses a model's tools on Chat Completions and points at
// Responses (or at the model's reasoning, which Responses allows with
// tools): Pinky then asks Responses at the same address, and when that
// answers, keeps to it for that service and model (APIs).
//
// Responses has not been run against a live key yet, so a model that
// works on Chat Completions stays there.

// usesResponses reports whether a conversation with the model of cfg
// starts on the Responses API: a GPT-6 model at OpenAI's own address
// (GPT-6 Astra and Luna, 6.1 Sol and 6.1 Terra), which OpenAI documents
// as calling tools only through it, or on Chat Completions only without
// reasoning.
func usesResponses(cfg Config) bool {
	if cfg.Provider != OpenAI || (cfg.BaseURL != "" && cfg.BaseURL != DefaultOpenAIURL) {
		return false
	}
	return strings.HasPrefix(strings.ToLower(cfg.Model), "gpt-6")
}

// newOpenAIModel starts a conversation with an OpenAI-compatible service:
// on the Responses API for the models usesResponses names and for a
// service and model apis remembers, else on Chat Completions, which moves
// to Responses as the comment above says.
func newOpenAIModel(cfg Config, key string, hc *http.Client, system string, tools []tool, apis *APIs) model {
	if usesResponses(cfg) || apis.responses(cfg) {
		return newResponses(cfg, key, hc, system, tools)
	}
	return &chatFirst{m: newOpenAI(cfg, key, hc, system, tools), chat: true,
		cfg: cfg, key: key, hc: hc, system: system, tools: tools, apis: apis}
}

// chatFirst is a conversation on Chat Completions that moves to the
// Responses API when Chat Completions refuses the model's tools and
// points there, before any turn was answered.
type chatFirst struct {
	m    model
	chat bool // m is Chat Completions and has answered no turn yet
	cfg  Config
	key  string
	hc   *http.Client
	// system and tools start the Responses conversation.
	system string
	tools  []tool
	apis   *APIs
	// texts and images are the person's turn the model has not answered,
	// which Responses gets whole.
	texts  []string
	images []image
}

func (c *chatFirst) setKey(key string) {
	c.key = key
	c.m.setKey(key)
}

func (c *chatFirst) attach(images []image) {
	if a, ok := c.m.(attacher); ok {
		a.attach(images)
	}
	if c.chat {
		c.images = append(c.images, images...)
	}
}

func (c *chatFirst) send(ctx context.Context, results []result, text string) (*turn, error) {
	if !c.chat {
		return c.m.send(ctx, results, text)
	}
	if text != "" {
		c.texts = append(c.texts, text)
	}
	t, err := c.m.send(ctx, results, text)
	if err == nil {
		c.chat, c.texts, c.images = false, nil, nil
		return t, nil
	}
	if !needsResponses(err) || len(results) > 0 {
		return nil, err
	}
	r := newResponses(c.cfg, c.key, c.hc, c.system, c.tools)
	r.attach(c.images)
	t, rerr := r.send(ctx, nil, strings.Join(c.texts, "\n\n"))
	switch {
	case rerr == nil:
		c.m, c.chat, c.texts, c.images = r, false, nil, nil
		c.apis.setResponses(c.cfg)
		return t, nil
	case noEndpoint(rerr):
		// No Responses API at this address: what Chat Completions said
		// is what the person can act on.
		return nil, err
	}
	return nil, rerr
}

// needsResponses reports whether Chat Completions refused a request
// because the model calls tools only through the Responses API, or only
// without reasoning, as OpenAI says of its newer models ("Function tools
// with reasoning effort are not supported for … in /v1/chat/completions.
// To use function tools, use /v1/responses or set reasoning_effort to
// 'none'.").
func needsResponses(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || ae.status < 400 || ae.status >= 500 {
		return false
	}
	switch ae.status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return false
	}
	m := strings.ToLower(ae.message)
	if strings.Contains(m, "/responses") || strings.Contains(m, "responses api") {
		return true
	}
	return strings.Contains(m, "reasoning") && mentionsTools(m)
}

// mentionsTools reports whether a message of the service's speaks of
// tools.
func mentionsTools(m string) bool {
	return strings.Contains(m, "tool") || strings.Contains(m, "function")
}

// toolsRefused reports whether an error's message is about the request's
// tools or the model's reasoning, not its pictures: a request with
// screenshots that failed so is not one to send again without them.
func toolsRefused(msg string) bool {
	m := strings.ToLower(msg)
	return mentionsTools(m) || strings.Contains(m, "reasoning") || strings.Contains(m, "/responses")
}

// toolsUnsupported reports whether the service said the model takes no
// tools ("does not support tools", "function calling is not enabled").
func toolsUnsupported(msg string) bool {
	m := strings.ToLower(msg)
	if !mentionsTools(m) {
		return false
	}
	for _, s := range []string{"support", "not enabled", "not available", "not allowed", "disabled"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// noEndpoint reports whether the address has no Responses API.
func noEndpoint(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	m := strings.ToLower(ae.message)
	return ae.status == http.StatusBadRequest && (strings.Contains(m, "invalid url") || strings.Contains(m, "unknown url"))
}

// APIs remembers the OpenAI-compatible services and models that call
// tools only through the Responses API, as Chat Completions said, so that
// their next tasks and checks start there. It keeps them in a small JSON
// file on this device. A nil *APIs remembers nothing.
type APIs struct {
	path string

	mu     sync.Mutex
	list   []apiChoice
	loaded bool
}

// apiChoice is a service, its address and a model that use the
// Responses API.
type apiChoice struct {
	Service string `json:"service"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
}

type apisFile struct {
	Version   int         `json:"version"`
	Responses []apiChoice `json:"responses"`
}

// OpenAPIs remembers in the file at path; empty keeps it in memory. The
// file is read when first needed.
func OpenAPIs(path string) *APIs { return &APIs{path: path} }

func choiceOf(cfg Config) apiChoice {
	return apiChoice{Service: cfg.Service, BaseURL: cfg.BaseURL, Model: cfg.Model}
}

// responses reports whether the service and model of cfg use the
// Responses API.
func (a *APIs) responses(cfg Config) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	return slices.Contains(a.list, choiceOf(cfg))
}

// setResponses remembers that the service and model of cfg use the
// Responses API.
func (a *APIs) setResponses(cfg Config) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.load()
	c := choiceOf(cfg)
	if slices.Contains(a.list, c) {
		return
	}
	a.list = append(a.list, c)
	a.save()
}

func (a *APIs) load() {
	if a.loaded {
		return
	}
	a.loaded = true
	if a.path == "" {
		return
	}
	data, err := os.ReadFile(a.path)
	if err != nil {
		return
	}
	var f apisFile
	if json.Unmarshal(data, &f) == nil {
		a.list = f.Responses
	}
}

// save writes the file; a failure only costs asking Chat Completions
// first again.
func (a *APIs) save() {
	if a.path == "" {
		return
	}
	data, err := json.Marshal(apisFile{Version: 1, Responses: a.list})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(a.path), 0o700)
	tmp := a.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, a.path)
	}
}
