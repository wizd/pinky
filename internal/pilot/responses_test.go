package pilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// issueMessage is what OpenAI's API answers on Chat Completions to a
// request with tools for gpt-6.1-terra (issue #121).
const issueMessage = "Function tools with reasoning effort are not supported for gpt-6.1-terra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'."

func TestCheckErrorKinds(t *testing.T) {
	for _, c := range []struct {
		status  int
		message string
		problem string
	}{
		{400, issueMessage, CheckToolsAPI},
		{404, "This model is only supported in v1/responses and not in v1/chat/completions.", CheckToolsAPI},
		{400, "Function tools are not supported with reasoning for this model.", CheckToolsAPI},
		{400, "this model does not support tools", CheckNoTools},
		{400, "Function calling is not enabled for this model", CheckNoTools},
		{400, "tools is unsupported", CheckNoTools},
		// A request the service refused for its tools' shape is not a
		// model without tools.
		{400, "Invalid schema for function 'ready': 'word' is required", CheckRejected},
		{400, "tool_choice must be one of none, auto", CheckRejected},
		{400, "bad request", CheckRejected},
		{401, "Use /v1/responses with a valid key", CheckBadKey},
		{429, "Function tools: slow down", CheckBusy},
		{404, "no such model", CheckNotFound},
	} {
		r := checkError(&apiError{status: c.status, message: c.message})
		if r.Problem != c.problem || r.Status != c.status || r.Detail != c.message {
			t.Errorf("%d %q: %+v, want %s", c.status, c.message, r, c.problem)
		}
	}
}

// fakeOpenAI is an OpenAI-compatible API with Chat Completions and the
// Responses API, each answering with the function given, which sees the
// request's body.
type fakeOpenAI struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []fakeReq
}

type fakeReq struct {
	path   string
	header http.Header
	body   map[string]any
	raw    string
}

func newFakeOpenAI(t *testing.T, chat, resp func(body map[string]any) (int, string)) *fakeOpenAI {
	t.Helper()
	f := &fakeOpenAI{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(data, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, fakeReq{path: r.URL.Path, header: r.Header.Clone(), body: body, raw: string(data)})
		f.mu.Unlock()
		answer := chat
		if strings.HasSuffix(r.URL.Path, "/responses") {
			answer = resp
		}
		if answer == nil {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":{"message":"Invalid URL (POST ` + r.URL.Path + `)"}}`))
			return
		}
		status, out := answer(body)
		w.WriteHeader(status)
		w.Write([]byte(out))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenAI) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.reqs {
		out = append(out, r.path)
	}
	return out
}

func (f *fakeOpenAI) last() fakeReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

func refuseTools(map[string]any) (int, string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": issueMessage, "type": "invalid_request_error"}})
	return 400, string(b)
}

const respToolCall = `{"id":"resp_1","status":"completed","output":[` +
	`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"enc-1"},` +
	`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"ready","arguments":"{\"word\":\"ready\"}","status":"completed"}],` +
	`"usage":{"input_tokens":1000,"input_tokens_details":{"cached_tokens":400},"output_tokens":50,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":1050}}`

func respondCall(map[string]any) (int, string) { return 200, respToolCall }

func TestCheckFallsBackToResponses(t *testing.T) {
	const key = "sk-fallback-test"
	f := newFakeOpenAI(t, refuseTools, respondCall)
	path := filepath.Join(t.TempDir(), "model-apis.json")
	r := New(Options{HTTPClient: f.srv.Client(), APIs: OpenAPIs(path), Usage: OpenUsage("")})
	defer r.Close()
	cfg := Config{Service: CustomOpenAI, Model: "gpt-6.1-terra", BaseURL: f.srv.URL + "/v1"}
	res := r.CheckWith(context.Background(), cfg, key)
	if !res.OK {
		t.Fatalf("check: %+v", res)
	}
	if got := f.paths(); len(got) != 2 || got[0] != "/v1/chat/completions" || got[1] != "/v1/responses" {
		t.Fatalf("requests: %v", got)
	}
	req := f.last()
	if req.header.Get("Authorization") != "Bearer "+key || strings.Contains(req.raw, key) {
		t.Errorf("the key: %v", req.header)
	}
	b := req.body
	if b["model"] != "gpt-6.1-terra" || b["instructions"] != checkSystem || b["tool_choice"] != "auto" || b["store"] != false {
		t.Errorf("body: %s", req.raw)
	}
	tools, _ := b["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %s", req.raw)
	}
	tl := tools[0].(map[string]any)
	if tl["type"] != "function" || tl["name"] != "ready" || tl["strict"] != false || tl["parameters"] == nil || tl["function"] != nil {
		t.Errorf("tool: %v", tl)
	}
	input, _ := b["input"].([]any)
	if len(input) != 1 || input[0].(map[string]any)["role"] != "user" || input[0].(map[string]any)["content"] != checkText {
		t.Errorf("input: %v", input)
	}
	// What it used: 1000 in, 400 of them from the cache, 50 out.
	rows := r.Usage("")
	if len(rows) != 1 || rows[0].Input != 600 || rows[0].CacheRead != 400 || rows[0].Output != 50 {
		t.Errorf("usage: %+v", rows)
	}

	// Remembered, in the file too: the next check goes to Responses at
	// once, and so does a Runner that reads the file.
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"model":"gpt-6.1-terra"`) {
		t.Fatalf("file: %s, %v", data, err)
	}
	r2 := New(Options{HTTPClient: f.srv.Client(), APIs: OpenAPIs(path)})
	defer r2.Close()
	if res := r2.CheckWith(context.Background(), cfg, key); !res.OK {
		t.Fatalf("second check: %+v", res)
	}
	if got := f.paths(); len(got) != 3 || got[2] != "/v1/responses" {
		t.Fatalf("requests: %v", got)
	}
	// Another model of the same service starts on Chat Completions.
	other := cfg
	other.Model = "other"
	if res := r2.CheckWith(context.Background(), other, key); !res.OK {
		t.Fatalf("other model: %+v", res)
	}
	if got := f.paths(); len(got) != 5 || got[3] != "/v1/chat/completions" {
		t.Fatalf("requests: %v", got)
	}
}

func TestCheckNoResponsesAPI(t *testing.T) {
	// Chat Completions points at Responses, which the address lacks: the
	// person reads what Chat Completions said, never that the model
	// answered without a tool.
	f := newFakeOpenAI(t, refuseTools, nil)
	apis := OpenAPIs("")
	r := New(Options{HTTPClient: f.srv.Client(), APIs: apis})
	defer r.Close()
	cfg := Config{Service: CustomOpenAI, Model: "gpt-6.1-terra", BaseURL: f.srv.URL}
	res := r.CheckWith(context.Background(), cfg, "k")
	if res.OK || res.Problem != CheckToolsAPI || res.Status != 400 || res.Detail != issueMessage {
		t.Fatalf("%+v", res)
	}
	if got := f.paths(); len(got) != 2 || got[1] != "/responses" {
		t.Fatalf("requests: %v", got)
	}
	if apis.responses(cfg.Normalize()) {
		t.Error("remembered a Responses API that is not there")
	}

	// Responses refuses the request for a reason of its own: that is
	// what the person reads.
	f = newFakeOpenAI(t, refuseTools, func(map[string]any) (int, string) {
		return 400, `{"error":{"message":"Unsupported parameter: 'store'"}}`
	})
	res = Check(context.Background(), Config{Service: CustomOpenAI, Model: "m", BaseURL: f.srv.URL}, "k", f.srv.Client())
	if res.Problem != CheckRejected || res.Detail != "Unsupported parameter: 'store'" {
		t.Fatalf("%+v", res)
	}
}

func TestOpenAIOwnAPIUsesResponses(t *testing.T) {
	// Settings that never chose a model keep GPT-5.6 Terra, on Chat
	// Completions as before: settings of before there were services too.
	for _, cfg := range []Config{
		{Service: OpenAI},
		{Provider: OpenAI},
		{Provider: OpenAI, BaseURL: DefaultOpenAIURL + "/"},
	} {
		cfg = cfg.Normalize()
		if cfg.Model != "gpt-5.6-terra" {
			t.Errorf("%+v: default model %q", cfg, cfg.Model)
		}
		m, _ := newModel(cfg, "k", http.DefaultClient, "s", nil, nil)
		c, ok := m.(*chatFirst)
		if !ok || c.m.(*openAI).url != DefaultOpenAIURL+"/chat/completions" {
			t.Fatalf("%+v: %T", cfg, m)
		}
	}
	// The GPT-6 models start on Responses at OpenAI's address, with the
	// preset or the custom entry.
	for _, cfg := range []Config{
		{Service: OpenAI, Model: "gpt-6.1-terra"},
		{Service: OpenAI, Model: "gpt-6.1-sol"},
		{Service: OpenAI, Model: "gpt-6-astra"},
		{Service: OpenAI, Model: "gpt-6-luna"},
		{Service: CustomOpenAI, Model: "gpt-6.1-sol", BaseURL: DefaultOpenAIURL},
	} {
		m, _ := newModel(cfg.Normalize(), "k", http.DefaultClient, "s", nil, nil)
		if r, ok := m.(*responses); !ok || r.url != DefaultOpenAIURL+"/responses" {
			t.Fatalf("%+v: %T", cfg, m)
		}
	}
	// Other services start on Chat Completions, with the fallback, even
	// with a GPT-6 model's name.
	for _, cfg := range []Config{
		{Service: "deepseek"},
		{Service: "openrouter", Model: "gpt-6.1-terra"},
		{Service: CustomOpenAI, Model: "gpt-6.1-terra", BaseURL: "https://relay.example/v1"},
	} {
		m, _ := newModel(cfg.Normalize(), "k", http.DefaultClient, "s", nil, nil)
		if _, ok := m.(*chatFirst); !ok {
			t.Fatalf("%+v: %T", cfg, m)
		}
	}
	// OpenCode's services get their session header on Responses too.
	r := newResponses(Config{Provider: OpenAI, Model: "m", BaseURL: "https://opencode.ai/zen/v1"}, "k", http.DefaultClient, "s", nil)
	if !strings.HasPrefix(r.session, "pinky-") {
		t.Errorf("session %q", r.session)
	}
}

func TestResponsesConversation(t *testing.T) {
	ctx := context.Background()
	turn := 0
	f := newFakeOpenAI(t, nil, func(map[string]any) (int, string) {
		turn++
		switch turn {
		case 1:
			return 200, respToolCall
		case 2:
			return 200, `{"status":"completed","output":[{"type":"reasoning","id":"rs_2","summary":[]},` +
				`{"type":"message","id":"msg_2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"All ","annotations":[]},{"type":"output_text","text":"done.","annotations":[]}]}],` +
				`"usage":{"input_tokens":2000,"input_tokens_details":{"cached_tokens":1000},"output_tokens":5}}`
		case 3:
			return 200, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that."}]}],"usage":{"input_tokens":10,"output_tokens":5}}`
		}
		return 200, `{"status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}`
	})
	tools := []tool{{name: "phone_tap", description: "Tap.", schema: map[string]any{"$schema": "x", "type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}}}}}
	r := newResponses(Config{Provider: OpenAI, Model: "gpt-6.1-terra", BaseURL: f.srv.URL + "/v1"}, "k", f.srv.Client(), "sys", tools)
	r.attach([]image{{data: []byte{1, 2, 3}, mime: "image/png"}})
	tr, err := r.send(ctx, nil, "Open Settings.")
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.calls) != 1 || tr.calls[0].id != "call_1" || tr.calls[0].name != "ready" || tr.calls[0].args["word"] != "ready" {
		t.Fatalf("calls %+v", tr.calls)
	}
	if tr.usage != (usage{input: 600, output: 50, cacheRead: 400}) {
		t.Errorf("usage %+v", tr.usage)
	}
	req := f.last()
	if req.path != "/v1/responses" {
		t.Errorf("path %s", req.path)
	}
	if strings.Contains(req.raw, `"$schema"`) {
		t.Errorf("$schema sent: %s", req.raw)
	}
	in := req.body["input"].([]any)
	parts := in[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "input_image" ||
		parts[0].(map[string]any)["image_url"] != "data:image/png;base64,AQID" ||
		parts[1].(map[string]any)["type"] != "input_text" || parts[1].(map[string]any)["text"] != "Open Settings." {
		t.Errorf("first message: %v", parts)
	}

	// The call's result, with a screenshot: the model's items go back as
	// they came, without the call's id; then the output; then the
	// screenshot in a message of the person's.
	tr, err = r.send(ctx, []result{{id: "call_1", text: "tapped", images: []image{{data: []byte{9}}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if tr.text != "All done." || len(tr.calls) != 0 || tr.usage != (usage{input: 1000, output: 5, cacheRead: 1000}) {
		t.Errorf("turn %+v", tr)
	}
	in = f.last().body["input"].([]any)
	if len(in) != 5 {
		t.Fatalf("input %d items: %s", len(in), f.last().raw)
	}
	rs := in[1].(map[string]any)
	if rs["type"] != "reasoning" || rs["id"] != "rs_1" || rs["encrypted_content"] != "enc-1" {
		t.Errorf("reasoning: %v", rs)
	}
	fc := in[2].(map[string]any)
	if fc["type"] != "function_call" || fc["id"] != nil || fc["call_id"] != "call_1" || fc["name"] != "ready" || fc["arguments"] != `{"word":"ready"}` {
		t.Errorf("call: %v", fc)
	}
	out := in[3].(map[string]any)
	if out["type"] != "function_call_output" || out["call_id"] != "call_1" || out["output"] != "tapped" {
		t.Errorf("output: %v", out)
	}
	shot := in[4].(map[string]any)["content"].([]any)
	if in[4].(map[string]any)["role"] != "user" || shot[0].(map[string]any)["text"] != shotLabel ||
		shot[1].(map[string]any)["image_url"] != "data:image/jpeg;base64,CQ==" {
		t.Errorf("screenshot: %v", in[4])
	}

	// The answer's message goes back without its id; the reasoning item
	// without encrypted content does not go back at all.
	if tr, err = r.send(ctx, nil, "Thanks."); err != nil || !tr.refused {
		t.Fatalf("refusal: %+v %v", tr, err)
	}
	in = f.last().body["input"].([]any)
	if len(in) != 7 {
		t.Fatalf("input %d items: %s", len(in), f.last().raw)
	}
	msg := in[5].(map[string]any)
	if msg["type"] != "message" || msg["id"] != nil || msg["role"] != "assistant" {
		t.Errorf("message: %v", msg)
	}
	if strings.Contains(f.last().raw, "rs_2") {
		t.Error("a reasoning item without its content went back")
	}

	if _, err := r.send(ctx, nil, "Again."); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failed response: %v", err)
	}
}

func TestResponsesErrorOutput(t *testing.T) {
	f := newFakeOpenAI(t, nil, func(map[string]any) (int, string) {
		return 200, `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"phone_tap","arguments":"{\"x\":"}]}`
	})
	r := newResponses(Config{Provider: OpenAI, Model: "m", BaseURL: f.srv.URL}, "k", f.srv.Client(), "sys", nil)
	r.history = append(r.history, rItem{Role: "user", Content: "go"})
	tr, err := r.send(context.Background(), []result{}, "")
	if err != nil || len(tr.calls) != 1 || tr.calls[0].bad == "" {
		t.Fatalf("%+v %v", tr, err)
	}
	if _, err := r.send(context.Background(), []result{{id: "c1", text: "bad arguments", isError: true}}, ""); err != nil {
		t.Fatal(err)
	}
	in := f.last().body["input"].([]any)
	if o := in[len(in)-1].(map[string]any); o["output"] != "error: bad arguments" {
		t.Errorf("error result: %v", o)
	}
}

func TestChatFirstFallback(t *testing.T) {
	// A task's first turn: Chat Completions refuses the tools, Responses
	// gets the person's whole turn with the pictures they shared, and the
	// conversation stays there.
	ctx := context.Background()
	f := newFakeOpenAI(t, refuseTools, respondCall)
	apis := OpenAPIs("")
	cfg := Config{Service: "openrouter", Model: "openai/gpt-6.1-terra", BaseURL: f.srv.URL}.Normalize()
	m := newOpenAIModel(cfg, "k", f.srv.Client(), "sys", []tool{checkTool}, apis)
	m.(attacher).attach([]image{{data: []byte{1}, mime: "image/png"}})
	tr, err := m.send(ctx, nil, "First words.")
	if err != nil || len(tr.calls) != 1 {
		t.Fatalf("turn %+v, err %v", tr, err)
	}
	// The refusal was about tools, not pictures: Chat Completions was not
	// asked again without them.
	if got := f.paths(); len(got) != 2 || got[0] != "/chat/completions" || got[1] != "/responses" {
		t.Fatalf("requests: %v", got)
	}
	parts := f.last().body["input"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 2 || parts[0].(map[string]any)["type"] != "input_image" || parts[1].(map[string]any)["text"] != "First words." {
		t.Errorf("first turn: %v", parts)
	}
	if !apis.responses(cfg) {
		t.Error("not remembered")
	}
	if _, err := m.send(ctx, []result{{id: "call_1", text: "ready"}}, ""); err != nil {
		t.Fatal(err)
	}
	if got := f.paths(); len(got) != 3 || got[2] != "/responses" {
		t.Fatalf("requests: %v", got)
	}
	if _, ok := newOpenAIModel(cfg, "k", f.srv.Client(), "sys", nil, apis).(*responses); !ok {
		t.Error("the next conversation starts on Chat Completions")
	}

	// Responses fails once for a reason of its own; the person continues
	// the task, and Responses gets both their messages.
	n := 0
	f = newFakeOpenAI(t, refuseTools, func(map[string]any) (int, string) {
		n++
		if n == 1 {
			return 400, `{"error":{"message":"Unsupported parameter: 'store'"}}`
		}
		return 200, respToolCall
	})
	cfg.BaseURL = f.srv.URL
	m = newOpenAIModel(cfg, "k", f.srv.Client(), "sys", nil, OpenAPIs(""))
	if _, err := m.send(ctx, nil, "First words."); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("err %v", err)
	}
	if _, err := m.send(ctx, nil, "More words."); err != nil {
		t.Fatal(err)
	}
	in := f.last().body["input"].([]any)
	if len(in) != 1 || in[0].(map[string]any)["content"] != "First words.\n\nMore words." {
		t.Errorf("input: %v", in)
	}

	// A refusal for another reason stays on Chat Completions.
	f = newFakeOpenAI(t, func(map[string]any) (int, string) { return 400, `{"error":{"message":"bad request"}}` }, respondCall)
	cfg.BaseURL = f.srv.URL
	m = newOpenAIModel(cfg, "k", f.srv.Client(), "sys", nil, OpenAPIs(""))
	if _, err := m.send(ctx, nil, "Hi."); err == nil {
		t.Fatal("no error")
	}
	if got := f.paths(); len(got) != 1 {
		t.Fatalf("requests: %v", got)
	}
}
