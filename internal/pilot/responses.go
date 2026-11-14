package pilot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// responses is a conversation over OpenAI's Responses API
// (<base>/responses), which OpenAI's newer models need to call tools
// (GPT-6 Astra, 6.1 Sol and 6.1 Terra). Pinky keeps the history itself
// and sends it whole each time, with store false, as it does on Chat
// Completions: so old screens can leave it (clearOld) and nothing stays
// on the service. Screenshots go in a message of the person's after the
// tool results, as on Chat Completions.
//
// The model's own output items go back as they came, without their ids:
// a reasoning item carries its encrypted_content, which the API returns
// by default, so the model keeps its reasoning between calls; one without
// it is left out, as the service could not find it.
type responses struct {
	url      string
	key      string
	model    string
	hc       *http.Client
	system   string
	tools    []rTool
	history  []rItem
	noImages bool    // the model took no images: send none
	pending  []image // pictures for the person's next message (attach)
	session  string  // x-opencode-session, as openAI's
}

type rTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
	// Strict is false: Pinky's tools have optional arguments, which
	// strict validation refuses, and the API tries it when it is left
	// out.
	Strict bool `json:"strict"`
}

// rItem is an item of the Responses API's input: a message, a tool's
// result, or an item of the model's output as it came (raw).
type rItem struct {
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"` // a string or []rPart
	CallID  string `json:"call_id,omitempty"`
	Output  string `json:"output,omitempty"`
	raw     json.RawMessage
}

func (it rItem) MarshalJSON() ([]byte, error) {
	if it.raw != nil {
		return it.raw, nil
	}
	type plain rItem
	return json.Marshal(plain(it))
}

type rPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// rOutput is what Pinky reads of an item of the model's output.
type rOutput struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	CallID           string `json:"call_id"`
	Name             string `json:"name"`
	Arguments        string `json:"arguments"`
	EncryptedContent string `json:"encrypted_content"`
}

type rResponse struct {
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens        int64 `json:"input_tokens"`
		OutputTokens       int64 `json:"output_tokens"`
		InputTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	} `json:"usage"`
}

func newResponses(cfg Config, key string, hc *http.Client, system string, tools []tool) *responses {
	base := cfg.BaseURL
	if base == "" {
		base = DefaultOpenAIURL
	}
	r := &responses{url: base + "/responses", key: key, model: cfg.Model, hc: hc, system: system}
	if isOpenCode(base) {
		r.session = newSessionID()
	}
	for _, t := range tools {
		r.tools = append(r.tools, rTool{Type: "function", Name: t.name, Description: t.description, Parameters: parameters(t.schema)})
	}
	return r
}

func (r *responses) setKey(key string) { r.key = key }

func (r *responses) attach(images []image) { r.pending = append(r.pending, images...) }

func rImageParts(images []image) []rPart {
	var out []rPart
	for _, im := range images {
		mime := im.mime
		if mime == "" {
			mime = "image/jpeg"
		}
		out = append(out, rPart{Type: "input_image",
			ImageURL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(im.data)})
	}
	return out
}

func (r *responses) send(ctx context.Context, results []result, text string) (*turn, error) {
	var images []rPart
	for _, res := range results {
		content := res.text
		if res.isError && !strings.HasPrefix(content, "error") {
			content = "error: " + content
		}
		if content == "" {
			content = "ok" // output is required
		}
		r.history = append(r.history, rItem{Type: "function_call_output", CallID: res.id, Output: content})
		images = append(images, rImageParts(res.images)...)
	}
	var shared []rPart
	if text != "" {
		shared = rImageParts(r.pending)
		r.pending = nil
	}
	if r.noImages {
		images, shared = nil, nil
	}
	switch {
	case len(shared) > 0:
		parts := append(shared, rPart{Type: "input_text", Text: text})
		if len(images) > 0 {
			parts = append(append([]rPart{{Type: "input_text", Text: shotLabel}}, images...), parts...)
		}
		images = append(images, shared...)
		r.history = append(r.history, rItem{Role: "user", Content: parts})
	case len(images) > 0:
		parts := append([]rPart{{Type: "input_text", Text: shotLabel}}, images...)
		if text != "" {
			parts = append(parts, rPart{Type: "input_text", Text: text})
		}
		r.history = append(r.history, rItem{Role: "user", Content: parts})
	case text != "":
		r.history = append(r.history, rItem{Role: "user", Content: text})
	}
	r.clearOld()

	resp, err := r.post(ctx)
	var ae *apiError
	if len(images) > 0 && errors.As(err, &ae) && ae.status == http.StatusBadRequest && !toolsRefused(ae.message) {
		// Likely a model that takes no images: the screens' text will do.
		r.noImages = true
		r.dropImages()
		resp, err = r.post(ctx)
	}
	if err != nil {
		return nil, err
	}
	if resp.Status == "failed" {
		msg := "the API could not answer"
		if resp.Error != nil && resp.Error.Message != "" {
			msg += ": " + resp.Error.Message
		}
		return nil, errors.New(msg)
	}
	cached := resp.Usage.InputTokensDetails.CachedTokens
	t := &turn{usage: usage{
		input:     max(0, resp.Usage.InputTokens-cached),
		output:    resp.Usage.OutputTokens,
		cacheRead: cached,
	}}
	if resp.IncompleteDetails != nil && resp.IncompleteDetails.Reason == "content_filter" {
		t.refused = true
		return t, nil
	}
	var texts []string
	var items []rItem
	for _, raw := range resp.Output {
		var o rOutput
		if json.Unmarshal(raw, &o) != nil {
			continue
		}
		switch o.Type {
		case "message":
			for _, p := range o.Content {
				switch p.Type {
				case "refusal":
					t.refused = true
				case "output_text":
					texts = append(texts, p.Text)
				}
			}
		case "function_call":
			cl := call{id: o.CallID, name: o.Name}
			args := strings.TrimSpace(o.Arguments)
			if args == "" {
				args = "{}"
			}
			if err := json.Unmarshal([]byte(args), &cl.args); err != nil {
				cl.bad = "The arguments are not a JSON object: " + err.Error()
			}
			t.calls = append(t.calls, cl)
		case "reasoning":
			if o.EncryptedContent == "" {
				continue // without it, the service could not find the item
			}
		default:
			continue // an item of a tool Pinky does not offer
		}
		items = append(items, rItem{raw: withoutID(raw, o.Type)})
	}
	if t.refused {
		return t, nil
	}
	t.text = strings.Join(texts, "")
	if len(resp.Output) == 0 {
		return nil, errors.New("the API answered with no message")
	}
	r.history = append(r.history, items...)
	return t, nil
}

// withoutID is an output item as it came, without the id of a message or
// a call: with store false the service keeps no items to find by it.
// A reasoning item keeps its id with its encrypted_content.
func withoutID(raw json.RawMessage, typ string) json.RawMessage {
	if typ == "reasoning" {
		return raw
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	delete(m, "id")
	b, err := marshal(m)
	if err != nil {
		return raw
	}
	return b
}

// post sends the conversation.
func (r *responses) post(ctx context.Context) (*rResponse, error) {
	body, err := marshal(map[string]any{
		"model":        r.model,
		"instructions": r.system,
		"input":        r.history,
		"tools":        r.tools,
		"tool_choice":  "auto",
		"store":        false,
	})
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	if r.key != "" {
		h.Set("Authorization", "Bearer "+r.key)
	}
	if r.session != "" {
		h.Set("x-opencode-session", r.session)
	}
	data, err := post(ctx, r.hc, r.url, h, body)
	if err != nil {
		return nil, err
	}
	var resp rResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("the API's answer is not JSON: %w", err)
	}
	return &resp, nil
}

// dropImages takes the screenshots out of the history.
func (r *responses) dropImages() {
	for i := range r.history {
		parts, ok := r.history[i].Content.([]rPart)
		if !ok {
			continue
		}
		var texts []string
		pictures := false
		for _, p := range parts {
			if p.Type == "input_text" && p.Text != shotLabel {
				texts = append(texts, p.Text)
			}
			pictures = pictures || p.Type == "input_image"
		}
		if len(texts) == 0 {
			texts = []string{"[The screenshot could not be sent: this model takes no images.]"}
		} else if pictures {
			texts = append(texts, "[The pictures could not be sent: this model takes no images.]")
		}
		r.history[i].Content = strings.Join(texts, "\n")
	}
}

// clearOld clears old tool results and screenshots once the history grows
// past keepChars, as openAI's does.
func (r *responses) clearOld() {
	var outs []int
	size := 0
	for i, it := range r.history {
		switch {
		case it.Type == "function_call_output":
			outs = append(outs, i)
			size += len(it.Output)
		default:
			if parts, ok := it.Content.([]rPart); ok {
				for _, p := range parts {
					if p.Type == "input_image" {
						size += imageChars
					}
				}
			}
		}
	}
	if size <= keepChars || len(outs) <= keepResults {
		return
	}
	keep := outs[len(outs)-keepResults]
	for i := range r.history[:keep] {
		it := &r.history[i]
		if it.Type == "function_call_output" {
			it.Output = cleared
			continue
		}
		parts, ok := it.Content.([]rPart)
		if !ok {
			continue
		}
		var texts []string
		for _, p := range parts {
			if p.Type == "input_text" && p.Text != shotLabel {
				texts = append(texts, p.Text)
			}
		}
		texts = append(texts, cleared)
		it.Content = strings.Join(texts, "\n")
	}
}
