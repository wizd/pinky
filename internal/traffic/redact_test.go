package traffic

import "testing"

func TestSecretName(t *testing.T) {
	for _, n := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "api_key",
		"access_token", "refreshToken", "X-CSRF-Token", "password", "newPassword", "sessionId", "JSESSIONID", "sig", "X-Sign",
		"key", "client_secret", "otp", "verification_code", "x-auth"} {
		if !SecretName(n) {
			t.Errorf("%s is not secret", n)
		}
	}
	for _, n := range []string{"Content-Type", "keyword", "monkey", "page", "Host", "author", "X-Trace", "user_agent", "zipcode", "codec"} {
		if SecretName(n) {
			t.Errorf("%s is secret", n)
		}
	}
}

func TestRedactHeaders(t *testing.T) {
	in := []Header{
		{"Authorization", "Bearer abcdefghijklmnop"},
		{"Cookie", "sid=1; theme=dark"},
		{"Set-Cookie", "session=abc; HttpOnly"},
		{"X-Api-Key", "k-123"},
		{"Content-Type", "application/json"},
		{"X-Forwarded", "token abcdefghijk1234"},
		{"X-Debug", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl"},
	}
	out := RedactHeaders(in)
	want := []string{Redacted, Redacted, Redacted, Redacted, "application/json", "token " + Redacted, Redacted}
	for i, h := range out {
		if h.Value != want[i] || h.Name != in[i].Name {
			t.Errorf("%s: %q, want %q", h.Name, h.Value, want[i])
		}
	}
	if in[0].Value != "Bearer abcdefghijklmnop" {
		t.Error("RedactHeaders changed its input")
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/items?page=2&access_token=abc&q=shoes": "https://api.example.com/v1/items?page=2&access_token=" + Redacted + "&q=shoes",
		"https://u:hunter2@example.com/x":                                  "https://u:" + Redacted + "@example.com/x",
		"https://example.com/cb?code=4242&state=s":                         "https://example.com/cb?code=" + Redacted + "&state=s",
		"https://example.com/plain":                                        "https://example.com/plain",
		"https://example.com/t/eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln":  "https://example.com/t/" + Redacted,
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Errorf("RedactURL(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestRedactBody(t *testing.T) {
	text, bin := RedactBody("application/json; charset=utf-8",
		[]byte(`{"user":"amy","password":"hunter2","code":0,"data":{"token":"t-1","items":[{"id":1,"secret":"s"}]},"session":{"id":"x","sid":"y"}}`))
	want := `{"code":0,"data":{"items":[{"id":1,"secret":"[redacted]"}],"token":"[redacted]"},"password":"[redacted]","session":"[redacted]","user":"amy"}`
	if bin || text != want {
		t.Errorf("json:\n%s\nwant\n%s", text, want)
	}
	text, _ = RedactBody("application/x-www-form-urlencoded", []byte("username=amy&password=hunter2&remember=1"))
	if text != "username=amy&password="+Redacted+"&remember=1" {
		t.Errorf("form: %s", text)
	}
	text, _ = RedactBody("", []byte(`{"apiKey":"k"}`))
	if text != `{"apiKey":"[redacted]"}` {
		t.Errorf("untyped json: %s", text)
	}
	text, _ = RedactBody("text/plain", []byte("Authorization: Bearer abcdefghijklmnop"))
	if text != "Authorization: Bearer "+Redacted {
		t.Errorf("text: %s", text)
	}
	if _, bin := RedactBody("image/png", []byte{0x89, 'P', 'N', 'G'}); !bin {
		t.Error("png is not binary")
	}
	if _, bin := RedactBody("", []byte{0, 1, 2, 0xff}); !bin {
		t.Error("bytes are not binary")
	}
}
