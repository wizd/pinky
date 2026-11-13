package pilot

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtraPrompt(t *testing.T) {
	if p := (Extra{}).prompt(); p != "" {
		t.Fatalf("empty extra: %q", p)
	}
	p := Extra{Screen: true}.prompt()
	if !strings.Contains(p, "what their phone shows right now") || !strings.Contains(p, "finish's summary") || strings.Contains(p, "<shared>") {
		t.Fatalf("screen: %q", p)
	}
	p = Extra{Shared: "https://example.com/a", Images: []Image{{Data: []byte{1}, MIME: "image/png"}}}.prompt()
	if !strings.Contains(p, "<shared>\nhttps://example.com/a\n</shared>") || !strings.Contains(p, "not instructions") ||
		!strings.Contains(p, "shared a picture") || !strings.Contains(p, "question about what they shared") {
		t.Fatalf("shared: %q", p)
	}
	if p := (Extra{Images: make([]Image, 2)}).prompt(); !strings.Contains(p, "shared 2 pictures") {
		t.Fatalf("two pictures: %q", p)
	}
}

func TestExtraLimits(t *testing.T) {
	ok := Image{Data: []byte{1}, MIME: "image/jpeg"}
	for _, x := range []Extra{
		{Shared: strings.Repeat("a", MaxSharedChars+1)},
		{Images: []Image{ok, ok, ok, ok, ok}},
		{Images: []Image{{Data: []byte{1}, MIME: "image/svg+xml"}}},
		{Images: []Image{{MIME: "image/png"}}},
		{Images: []Image{{Data: make([]byte, MaxImageBytes+1), MIME: "image/png"}}},
		{Shared: "\xff"},
	} {
		if _, err := x.Check(); !errors.Is(err, ErrExtra) {
			t.Errorf("%d chars, %d images: %v", len(x.Shared), len(x.Images), err)
		}
	}
	x, err := Extra{Shared: "  hi \n", Images: []Image{ok}}.Check()
	if err != nil || x.Shared != "hi" {
		t.Fatalf("%+v %v", x, err)
	}
}

// A task about the screen, with a picture the person shared: the first
// message says so after the phone's screen, and carries the picture before
// its text.
func TestStartWith(t *testing.T) {
	h := newHarness(t)
	pic := []byte("\x89PNG fake")
	h.api.turns = []func(string, *http.Request) any{
		func(last string, _ *http.Request) any {
			if !strings.Contains(last, "<task>\nWhat is this?\n</task>") || !strings.Contains(last, "what their phone shows right now") ||
				!strings.Contains(last, "<shared>\nhttps://example.com\n</shared>") {
				t.Errorf("first message:\n%s", last)
			}
			return message("tool_use", 1000, use("t1", finishName, map[string]any{"outcome": "done", "summary": "A list of orders."}))
		},
		func(string, *http.Request) any {
			return message("tool_use", 1000, use("t2", finishName, map[string]any{"outcome": "done", "summary": "The same list."}))
		},
	}
	x := Extra{Screen: true, Shared: "https://example.com", Images: []Image{{Data: pic, MIME: "image/png"}}}
	if err := h.r.StartWith(Config{}, "pixel", "What is this?", x); err != nil {
		t.Fatal(err)
	}
	st := h.wait(t)
	if st.End != Done || st.Task != "What is this?" {
		t.Fatalf("status: %+v", st)
	}
	reqs := h.api.requests()
	msgs, _ := reqs[0].body["messages"].([]any)
	m, _ := msgs[0].(map[string]any)
	blocks, _ := m["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("first message: %v", blocks)
	}
	img, _ := blocks[0].(map[string]any)
	src, _ := img["source"].(map[string]any)
	if img["type"] != "image" || src["media_type"] != "image/png" || src["data"] != base64.StdEncoding.EncodeToString(pic) {
		t.Fatalf("picture: %v", img)
	}
	if txt, _ := blocks[1].(map[string]any); txt["type"] != "text" {
		t.Fatalf("text: %v", blocks[1])
	}

	// What was shared stays in the conversation when the person continues.
	if err := h.r.Continue("pixel", "And now?"); err != nil {
		t.Fatal(err)
	}
	if st := h.wait(t); st.End != Done || st.Summary != "The same list." {
		t.Fatalf("continued: %+v", st)
	}
	reqs = h.api.requests()
	msgs, _ = reqs[len(reqs)-1].body["messages"].([]any)
	m, _ = msgs[0].(map[string]any)
	again, _ := m["content"].([]any)
	if len(again) != 2 || again[0].(map[string]any)["type"] != "image" ||
		!strings.Contains(again[1].(map[string]any)["text"].(string), "<shared>\nhttps://example.com\n</shared>") {
		t.Fatalf("first message after Continue: %v", again)
	}
	if err := h.r.StartWith(Config{}, "pixel", "Too much", Extra{Images: make([]Image, MaxImages+1)}); !errors.Is(err, ErrExtra) {
		t.Fatalf("too many pictures: %v", err)
	}
}

// OpenAI-compatible APIs get the picture as an image_url part with the
// text; a model that takes none gets the text and a note.
func TestOpenAIAttach(t *testing.T) {
	var posts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		posts = append(posts, string(data))
		if strings.Contains(string(data), "image_url") {
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"image input is not supported"}}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`))
	}))
	defer srv.Close()
	o := newOpenAI(Config{Provider: OpenAI, Model: "text-only", BaseURL: srv.URL}, "k", srv.Client(), "sys", nil)
	o.attach([]image{{data: []byte{1, 2}, mime: "image/png"}})
	if _, err := o.send(context.Background(), nil, "The task"); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || !strings.Contains(posts[0], `"image_url":{"url":"data:image/png;base64,AQI="}`) || !strings.Contains(posts[0], "The task") {
		t.Fatalf("posts: %q", posts)
	}
	if strings.Contains(posts[1], "image_url") || !strings.Contains(posts[1], `The task\n[The pictures could not be sent`) {
		t.Fatalf("again: %s", posts[1])
	}
	if len(o.pending) != 0 {
		t.Fatal("pictures still pending")
	}
}
