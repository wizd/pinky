package main

import "testing"

func TestWebMirrorText(t *testing.T) {
	for _, tc := range []struct{ accept, lang, title string }{
		{"zh-CN,zh;q=0.9,en;q=0.8", "zh-Hans", "手机画面 · Pinky"},
		{"ja-JP", "ja", "スマホの画面 · Pinky"},
		{"fr-FR,fr;q=0.9", "en", "Phone screen · Pinky"},
		{"", "en", "Phone screen · Pinky"},
	} {
		lang, text := webMirrorText(tc.accept)
		if lang != tc.lang || text["title"] != tc.title {
			t.Errorf("webMirrorText(%q) = %q, %q; want %q, %q", tc.accept, lang, text["title"], tc.lang, tc.title)
		}
		if text["unsupported"] == "" || text["streamFailed"] == "" {
			t.Errorf("webMirrorText(%q) lacks words: %v", tc.accept, text)
		}
	}
}
