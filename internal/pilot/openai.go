package pilot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// openAI is a conversation over an OpenAI-compatible Chat Completions
// API (responses.go has the Responses API, apis.go which one a
// conversation uses). Many services speak it; Pinky uses only what they share: tools as
// functions, tool_choice auto, and screenshots as images in a message of
// the person's after the tool results (a tool message holds only text).
// A service that takes no images gets the screens' text alone.
type openAI struct {
	url      string
	key      string
	model    string
	hc       *http.Client
	tools    []oaTool
	history  []oaMessage
	noImages bool    // the model took no images: send none
	pending  []image // pictures for the person's next message (attach)
	// session names the conversation to services that ask for it in a
	// header (OpenCode Go refuses requests without x-opencode-session);
	// empty for the others.
	session string
}

// shotLabel starts the message that carries a turn's screenshots.
const shotLabel = "The screenshot from the call above."

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description,omitempty"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content"` // a string, []oaPart or nil
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	// ReasoningContent goes back as it came, as the services whose
	// thinking models write it (DeepSeek, Kimi) ask.
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type oaPart struct {
	Type     string      `json:"type"`
	Text     string      `json:"text,omitempty"`
	ImageURL *oaImageURL `json:"image_url,omitempty"`
}

type oaImageURL struct {
	URL string `json:"url"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
	// ExtraContent is what a service adds to the call and wants back as it
	// was: Gemini's thought signature ({"google":{"thought_signature":…}}),
	// without which it refuses the next request.
	ExtraContent json.RawMessage `json:"extra_content,omitempty"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content          json.RawMessage `json:"content"`
			Refusal          string          `json:"refusal"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []oaToolCall    `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func newOpenAI(cfg Config, key string, hc *http.Client, system string, tools []tool) *openAI {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultOpenAIURL
	}
	o := &openAI{url: base + "/chat/completions", key: key, model: cfg.Model, hc: hc}
	if isOpenCode(base) {
		o.session = newSessionID()
	}
	for _, t := range tools {
		var ot oaTool
		ot.Type = "function"
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.name, t.description, parameters(t.schema)
		o.tools = append(o.tools, ot)
	}
	o.history = []oaMessage{{Role: "system", Content: system}}
	return o
}

// isOpenCode reports whether base is one of OpenCode's services, which
// want every request of a conversation to carry the same
// x-opencode-session.
func isOpenCode(base string) bool {
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// newSessionID is a random ID for one task's conversation.
func newSessionID() string {
	var b [16]byte
	rand.Read(b[:])
	return "pinky-" + hex.EncodeToString(b[:])
}

// parameters is a tool's JSON Schema without the keys some services
// refuse.
func parameters(s map[string]any) map[string]any {
	p := make(map[string]any, len(s))
	for k, v := range s {
		if k != "$schema" {
			p[k] = v
		}
	}
	if p["type"] == nil {
		p["type"] = "object"
	}
	if p["properties"] == nil {
		p["properties"] = map[string]any{}
	}
	return p
}

func (o *openAI) setKey(key string) { o.key = key }

// attach keeps pictures the person shared for their next message, before
// its text.
func (o *openAI) attach(images []image) { o.pending = append(o.pending, images...) }

func imageParts(images []image) []oaPart {
	var out []oaPart
	for _, im := range images {
		mime := im.mime
		if mime == "" {
			mime = "image/jpeg"
		}
		out = append(out, oaPart{Type: "image_url", ImageURL: &oaImageURL{
			URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(im.data)}})
	}
	return out
}

func (o *openAI) send(ctx context.Context, results []result, text string) (*turn, error) {
	var images []oaPart
	for _, r := range results {
		content := r.text
		if r.isError && !strings.HasPrefix(content, "error") {
			content = "error: " + content
		}
		o.history = append(o.history, oaMessage{Role: "tool", ToolCallID: r.id, Content: content})
		images = append(images, imageParts(r.images)...)
	}
	var shared []oaPart // pictures the person shared, with their text
	if text != "" {
		shared = imageParts(o.pending)
		o.pending = nil
	}
	if o.noImages {
		images, shared = nil, nil
	}
	switch {
	case len(shared) > 0:
		// The person's message, with the pictures they shared, after
		// any screenshots of the calls above.
		parts := append(shared, oaPart{Type: "text", Text: text})
		if len(images) > 0 {
			parts = append(append([]oaPart{{Type: "text", Text: shotLabel}}, images...), parts...)
		}
		images = append(images, shared...)
		o.history = append(o.history, oaMessage{Role: "user", Content: parts})
	case len(images) > 0:
		parts := append([]oaPart{{Type: "text", Text: shotLabel}}, images...)
		if text != "" {
			parts = append(parts, oaPart{Type: "text", Text: text})
		}
		o.history = append(o.history, oaMessage{Role: "user", Content: parts})
	case text != "":
		o.history = append(o.history, oaMessage{Role: "user", Content: text})
	}
	o.clearOld()

	resp, err := o.post(ctx)
	var ae *apiError
	if len(images) > 0 && errors.As(err, &ae) && ae.status == http.StatusBadRequest && !toolsRefused(ae.message) {
		// Likely a model that takes no images: the screens' text will do.
		o.noImages = true
		o.dropImages()
		resp, err = o.post(ctx)
	}
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("the API answered with no message")
	}
	ch := resp.Choices[0]
	cached := resp.Usage.PromptTokensDetails.CachedTokens
	t := &turn{usage: usage{
		input:     max(0, resp.Usage.PromptTokens-cached),
		output:    resp.Usage.CompletionTokens,
		cacheRead: cached,
	}}
	if ch.Message.Refusal != "" || ch.FinishReason == "content_filter" {
		t.refused = true
		return t, nil
	}
	var content string
	if len(ch.Message.Content) > 0 {
		json.Unmarshal(ch.Message.Content, &content) // a string; anything else is left out
	}
	t.text = content
	m := oaMessage{Role: "assistant", ToolCalls: ch.Message.ToolCalls, ReasoningContent: ch.Message.ReasoningContent}
	if content != "" {
		m.Content = content
	}
	for i := range m.ToolCalls {
		if m.ToolCalls[i].Type == "" {
			m.ToolCalls[i].Type = "function"
		}
		tc := m.ToolCalls[i]
		cl := call{id: tc.ID, name: tc.Function.Name}
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		if err := json.Unmarshal([]byte(args), &cl.args); err != nil {
			cl.bad = "The arguments are not a JSON object: " + err.Error()
		}
		t.calls = append(t.calls, cl)
	}
	o.history = append(o.history, m)
	return t, nil
}

// post sends the conversation.
func (o *openAI) post(ctx context.Context) (*oaResponse, error) {
	body, err := marshal(map[string]any{
		"model":       o.model,
		"messages":    o.history,
		"tools":       o.tools,
		"tool_choice": "auto",
	})
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	if o.key != "" { // a local server may need none
		h.Set("Authorization", "Bearer "+o.key)
	}
	if o.session != "" {
		h.Set("x-opencode-session", o.session)
	}
	data, err := post(ctx, o.hc, o.url, h, body)
	if err != nil {
		return nil, err
	}
	var r oaResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the API's answer is not JSON: %w", err)
	}
	return &r, nil
}

// dropImages takes the screenshots out of the history.
func (o *openAI) dropImages() {
	for i := range o.history {
		parts, ok := o.history[i].Content.([]oaPart)
		if !ok {
			continue
		}
		var texts []string
		pictures := false
		for _, p := range parts {
			if p.Type == "text" && p.Text != shotLabel {
				texts = append(texts, p.Text)
			}
			pictures = pictures || p.Type == "image_url"
		}
		if len(texts) == 0 {
			texts = []string{"[The screenshot could not be sent: this model takes no images.]"}
		} else if pictures {
			texts = append(texts, "[The pictures could not be sent: this model takes no images.]")
		}
		o.history[i].Content = strings.Join(texts, "\n")
	}
}

// clearOld clears old tool results and screenshots once the history grows
// past keepChars.
func (o *openAI) clearOld() {
	var tools []int
	size := 0
	for i, m := range o.history {
		switch c := m.Content.(type) {
		case string:
			if m.Role == "tool" {
				tools = append(tools, i)
				size += len(c)
			}
		case []oaPart:
			for _, p := range c {
				if p.ImageURL != nil {
					size += imageChars
				}
			}
		}
	}
	if size <= keepChars {
		return
	}
	if len(tools) <= keepResults {
		return
	}
	keep := tools[len(tools)-keepResults]
	for i := range o.history[:keep] {
		m := &o.history[i]
		switch c := m.Content.(type) {
		case string:
			if m.Role == "tool" {
				m.Content = cleared
			}
		case []oaPart:
			var texts []string
			for _, p := range c {
				if p.Type == "text" && p.Text != shotLabel {
					texts = append(texts, p.Text)
				}
			}
			texts = append(texts, cleared)
			m.Content = strings.Join(texts, "\n")
		}
	}
}
