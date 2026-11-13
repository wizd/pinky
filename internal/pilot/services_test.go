package pilot

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServicesFiles(t *testing.T) {
	want := ServicesJSON()
	for _, f := range ServicesFiles {
		got, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is out of date: run go generate ./internal/pilot", f)
		}
	}
}

func TestServices(t *testing.T) {
	seen := map[string]bool{}
	names := map[string]bool{}
	for _, s := range Services() {
		if s.ID == "" || seen[s.ID] {
			t.Errorf("ID %q missing or twice", s.ID)
		}
		seen[s.ID] = true
		if n := s.Name + "/" + s.Region; names[n] {
			t.Errorf("two services named %s", n)
		} else {
			names[n] = true
		}
		if s.API != Anthropic && s.API != OpenAI {
			t.Errorf("%s: API %q", s.ID, s.API)
		}
		switch {
		case s.Custom || s.ID == Anthropic:
			if s.BaseURL != "" {
				t.Errorf("%s: an address of its own", s.ID)
			}
		case s.Local:
			if !strings.HasPrefix(s.BaseURL, "http://localhost:") || !s.KeyOptional {
				t.Errorf("%s: %+v", s.ID, s)
			}
		default:
			if !strings.HasPrefix(s.BaseURL, "https://") || strings.HasSuffix(s.BaseURL, "/") || s.KeyOptional {
				t.Errorf("%s: %+v", s.ID, s)
			}
		}
		if s.Region != "" && s.Region != RegionChina && s.Region != RegionGlobal {
			t.Errorf("%s: region %q", s.ID, s.Region)
		}
		if KeyName(s.ID, s.API) == "" {
			t.Errorf("%s: no key name", s.ID)
		}
		// A service's own table prices its models, not another's.
		for _, m := range s.PricedModels {
			if _, ok := priceOf(Config{Service: s.ID, Provider: s.API, Model: m}); !ok {
				t.Errorf("%s: %s listed as priced", s.ID, m)
			}
		}
	}
	for id := range servicePrices {
		if !seen[id] {
			t.Errorf("prices for no service: %s", id)
		}
	}
	if s, _ := ServiceByID(Anthropic); len(s.PricedModels) == 0 {
		t.Error("Anthropic's models are not priced")
	}
	// The copies are the caller's.
	s, _ := ServiceByID(Anthropic)
	s.Models[0].ID = "changed"
	if s, _ := ServiceByID(Anthropic); s.Models[0].ID == "changed" {
		t.Error("ServiceByID shares its models")
	}
}

func TestServiceForAndKeyName(t *testing.T) {
	for _, c := range []struct{ provider, url, service, key string }{
		{"", "", Anthropic, Anthropic},
		{"anthropic", "https://proxy.example/", CustomAnthropic, Anthropic},
		{"openai", "", OpenAI, OpenAI},
		{"OpenAI", DefaultOpenAIURL + "/", OpenAI, OpenAI},
		// An address typed by hand stays the person's, even a service's.
		{"openai", "https://api.deepseek.com", CustomOpenAI, OpenAI},
	} {
		svc := ServiceFor(c.provider, c.url)
		if svc != c.service || KeyName(svc, c.provider) != c.key {
			t.Errorf("%q %q: %s, key %s", c.provider, c.url, svc, KeyName(svc, c.provider))
		}
	}
	if KeyName("", OpenAI) != OpenAI || KeyName("", "") != Anthropic || KeyName("deepseek", OpenAI) != "deepseek" {
		t.Error("KeyName")
	}
}

func TestNormalizeService(t *testing.T) {
	c := Config{Service: "deepseek"}.Normalize()
	if c.Provider != OpenAI || c.BaseURL != "https://api.deepseek.com" || c.Model != "deepseek-flash" || c.KeyName() != "deepseek" || c.KeyOptional() {
		t.Fatalf("%+v", c)
	}
	c = Config{Service: "deepseek", Provider: Anthropic, Model: "deepseek-v4-pro", BaseURL: "https://proxy.example/v1/"}.Normalize()
	if c.Provider != OpenAI || c.BaseURL != "https://proxy.example/v1" || c.Model != "deepseek-v4-pro" {
		t.Fatalf("%+v", c)
	}
	c = Config{Service: "ollama"}.Normalize()
	if c.Model != "" || !c.KeyOptional() || c.BaseURL != "http://localhost:11434/v1" {
		t.Fatalf("%+v", c)
	}
	c = Config{Service: CustomAnthropic, BaseURL: "https://x"}.Normalize()
	if c.Provider != Anthropic || c.Model != DefaultModel || c.KeyName() != Anthropic {
		t.Fatalf("%+v", c)
	}
	// Settings of before there were services.
	c = Config{Provider: OpenAI, Model: "m"}.Normalize()
	if c.Service != OpenAI || c.BaseURL != DefaultOpenAIURL || c.KeyName() != OpenAI {
		t.Fatalf("%+v", c)
	}
}

func TestServicePrices(t *testing.T) {
	cost := func(cfg Config, at time.Time) float64 {
		t.Helper()
		p, ok := priceOf(cfg)
		if !ok {
			t.Fatalf("%+v not priced", cfg)
		}
		return p.at(at).cost(usage{input: 1_000_000})
	}
	ds := Config{Service: "deepseek", Provider: OpenAI, Model: "deepseek-flash"}
	wed := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	if got := cost(ds, wed.Add(2*time.Hour)); math.Abs(got-0.30) > 1e-9 {
		t.Errorf("DeepSeek at peak: %v", got)
	}
	for _, at := range []time.Time{wed.Add(5 * time.Hour), wed.Add(12 * time.Hour), wed.AddDate(0, 0, 3).Add(2 * time.Hour)} {
		if got := cost(ds, at); math.Abs(got-0.15) > 1e-9 {
			t.Errorf("DeepSeek off-peak at %v: %v", at, got)
		}
	}
	gc := Config{Service: "gemini", Provider: OpenAI, Model: "gemini-4-carbon"}
	if a, b := cost(gc, geminiRise.Add(-time.Second)), cost(gc, geminiRise); a != 2 || b != 4 {
		t.Errorf("Gemini 4 Carbon: %v, then %v", a, b)
	}
	// The longest start wins.
	if got := cost(Config{Service: "glm-intl", Provider: OpenAI, Model: "glm-5.3-flash"}, wed); math.Abs(got-0.15) > 1e-9 {
		t.Errorf("GLM-5.3-Flash: %v", got)
	}
	for _, cfg := range []Config{
		{Service: CustomOpenAI, Provider: OpenAI, Model: "gpt-5.6-terra"},
		{Service: "kimi-intl", Provider: OpenAI, Model: "kimi-k3"},
		{Service: "glm-cn", Provider: OpenAI, Model: "glm-5.3-flash"},
		{Service: "openrouter", Provider: OpenAI, Model: "anthropic/claude-sonnet-5.5"},
	} {
		if _, ok := priceOf(cfg); ok {
			t.Errorf("%s priced on %s", cfg.Model, cfg.Service)
		}
	}
	if !(Config{Service: OpenAI, Model: "gpt-5.6-terra"}).Normalize().Priced() {
		t.Error("OpenAI's model not priced")
	}
	if !(Config{Service: CustomAnthropic, BaseURL: "https://x"}).Normalize().Priced() {
		t.Error("a Claude model on a typed address not priced at Anthropic's prices")
	}
}

// checkServer is an OpenAI-compatible API that answers each request with
// status and body.
func checkServer(t *testing.T, status int, body string) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r)
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

const (
	checkToolCall = `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"ready","arguments":"{\"word\":\"ready\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":300,"completion_tokens":10}}`
	checkSaid     = `{"choices":[{"message":{"role":"assistant","content":"Ready!"},"finish_reason":"stop"}],"usage":{"prompt_tokens":300,"completion_tokens":3}}`
)

func TestCheck(t *testing.T) {
	ctx := context.Background()
	const key = "sk-check-secret"
	for _, c := range []struct {
		status  int
		body    string
		problem string
	}{
		{200, checkToolCall, ""},
		{200, checkSaid, CheckNoTools},
		{401, `{"error":{"message":"bad key ` + key + `"}}`, CheckBadKey},
		{403, `{"error":{"message":"forbidden"}}`, CheckBadKey},
		{402, `{"error":{"message":"Insufficient Balance"}}`, CheckNoCredit},
		{404, `{"error":{"message":"no such model"}}`, CheckNotFound},
		{400, `{"error":{"message":"this model does not support tools"}}`, CheckNoTools},
		{400, `{"error":{"message":"bad request"}}`, CheckRejected},
		{200, `<html>hello</html>`, CheckBadAnswer},
	} {
		srv, reqs := checkServer(t, c.status, c.body)
		r := Check(ctx, Config{Service: CustomOpenAI, Model: "m", BaseURL: srv.URL}, key, srv.Client())
		if r.Problem != c.problem || r.OK != (c.problem == "") {
			t.Errorf("%d %s: %+v", c.status, c.body, r)
		}
		if strings.Contains(r.Detail, key) {
			t.Errorf("the key in the detail: %q", r.Detail)
		}
		if c.status >= 400 && r.Status != c.status {
			t.Errorf("status %d: %+v", c.status, r)
		}
		if len(*reqs) != 1 {
			t.Fatalf("%d requests", len(*reqs))
		}
		req := (*reqs)[0]
		if req.Header.Get("Authorization") != "Bearer "+key || req.URL.Path != "/chat/completions" {
			t.Errorf("request: %s %v", req.URL.Path, req.Header)
		}
	}

	// No answer at all.
	srv, _ := checkServer(t, 200, checkToolCall)
	url := srv.URL
	srv.Close()
	if r := Check(ctx, Config{Service: CustomOpenAI, Model: "m", BaseURL: url}, key, nil); r.Problem != CheckUnreachable {
		t.Errorf("closed server: %+v", r)
	}
	// Nothing to send without a model or a key, and a local server
	// needs no key: no Authorization at all.
	if r := Check(ctx, Config{Service: CustomOpenAI, BaseURL: url}, key, nil); r.Problem != CheckNoModel {
		t.Errorf("no model: %+v", r)
	}
	if r := Check(ctx, Config{Service: "deepseek"}, "", nil); r.Problem != CheckNoKey {
		t.Errorf("no key: %+v", r)
	}
	srv, reqs := checkServer(t, 200, checkToolCall)
	if r := Check(ctx, Config{Service: "ollama", Model: "qwen", BaseURL: srv.URL}, "", srv.Client()); !r.OK {
		t.Errorf("local: %+v", r)
	}
	if h := (*reqs)[0].Header; h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" {
		t.Errorf("local server got %v", h)
	}
	for _, s := range []int{429, 500, 503} {
		if r := checkError(&apiError{status: s, message: "later"}); r.Problem != CheckBusy {
			t.Errorf("%d: %+v", s, r)
		}
	}
}

func TestCheckCountsUsage(t *testing.T) {
	srv, _ := checkServer(t, 200, checkToolCall)
	u := OpenUsage("")
	r := New(Options{HTTPClient: srv.Client(), Usage: u,
		Key: func(_ context.Context, name string) (string, error) {
			if name != OpenAI {
				t.Errorf("key asked for %q", name)
			}
			return "k", nil
		}})
	defer r.Close()
	if res := r.Check(context.Background(), Config{Service: CustomOpenAI, Model: "m", BaseURL: srv.URL}); !res.OK {
		t.Fatalf("%+v", res)
	}
	rows := r.Usage("")
	if len(rows) != 1 || rows[0].Requests != 1 || rows[0].Input != 300 || rows[0].Output != 10 || rows[0].Service != CustomOpenAI ||
		rows[0].Host != strings.TrimPrefix(srv.URL, "http://") || rows[0].Unpriced != 310 {
		t.Fatalf("%+v", rows)
	}
}

func TestUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.json")
	u := OpenUsage(path)
	day := time.Date(2026, 11, 13, 12, 0, 0, 0, time.Local)
	opus := Config{Service: Anthropic, Model: "claude-opus-5-5"}
	u.add(day, opus, usage{input: 1_000_000, output: 100_000})
	u.add(day, opus, usage{input: 1_000_000})
	u.add(day, Config{Service: "kimi-cn", Model: "kimi-k3"}, usage{input: 5_000_000})
	u.add(day.AddDate(0, 0, -1), opus, usage{input: 10})
	u.add(day, opus, usage{}) // nothing used: no request counted
	rows := u.Rows("")
	if len(rows) != 3 {
		t.Fatalf("%+v", rows)
	}
	if r := rows[0]; r.Day != "2026-11-13" || r.Service != "kimi-cn" || r.Unpriced != 5_000_000 || r.Cost != 0 {
		t.Errorf("most used first: %+v", r)
	}
	if r := rows[1]; r.Requests != 2 || r.Input != 2_000_000 || math.Abs(r.Cost-(8+2)) > 1e-9 || !r.Priced() {
		t.Errorf("Opus: %+v", r)
	}
	if rows[2].Day != "2026-11-12" {
		t.Errorf("oldest last: %+v", rows[2])
	}
	if got := u.Rows("2026-11-13"); len(got) != 2 {
		t.Errorf("since: %+v", got)
	}
	tot := Total(rows)
	if tot.Requests != 4 || tot.Tokens != 7_100_010 || tot.Unpriced != 5_000_000 {
		t.Errorf("total: %+v", tot)
	}

	// The file has it, for the next start; and only this device's user
	// can read it.
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("file: %v %v", fi, err)
	}
	var f usageFile
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || len(f.Rows) != 3 {
		t.Fatalf("file %s: %v", data, err)
	}
	if got := OpenUsage(path).Rows(""); len(got) != 3 {
		t.Fatalf("read back: %+v", got)
	}

	// Old days go.
	u.add(day.AddDate(0, 0, UsageKeepDays), opus, usage{input: 1})
	if got := u.Rows(""); len(got) != 1 {
		t.Errorf("after %d days: %+v", UsageKeepDays, got)
	}
	if err := u.Clear(); err != nil || len(u.Rows("")) != 0 {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file after Clear: %v", err)
	}
	var none *Usage
	none.add(day, opus, usage{input: 1})
	if none.Rows("") != nil {
		t.Error("a nil Usage counted")
	}
}
