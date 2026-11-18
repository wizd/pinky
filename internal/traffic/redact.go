package traffic

import (
	"bytes"
	"encoding/json"
	"mime"
	"net/url"
	"regexp"
	"strings"
)

// Redaction, on by default (docs/notes/traffic-capture.md, "Safety"): the
// values of headers, query parameters and body fields whose names say
// they hold a credential are replaced with Redacted before an agent sees
// them, and so are bearer tokens and JWTs wherever they appear. Names and
// the shape of the request stay, which is what an agent working out an
// app's API needs.

// Redacted replaces a secret value.
const Redacted = "[redacted]"

// secretExact are names that are secrets only when they are the whole name
// (after lowercasing and dropping '-', '_' and '.'): "key" is, "keyword"
// is not.
var secretExact = map[string]bool{
	"key": true, "sig": true, "sid": true, "code": true, "otp": true, "pwd": true, "pass": true,
	"pin": true, "ticket": true, "auth": true, "csrf": true, "xsrf": true, "cvv": true, "cvc": true,
	"jwt": true, "nonce": true, "sign": true,
}

// secretParts are names that are secrets wherever they appear in the name.
var secretParts = []string{
	"token", "secret", "password", "passwd", "passcode", "session", "signature", "apikey",
	"authorization", "authentication", "cookie", "credential", "privatekey", "accesskey",
	"cardnumber", "securitycode", "verificationcode", "otpcode", "smscode", "authcode",
}

// SecretName reports whether a header, parameter or field name says its
// value is a credential: Authorization, Cookie, Set-Cookie, X-Api-Key,
// access_token, password, sessionId, sig and the like.
func SecretName(name string) bool {
	n := strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		}
		return r
	}, strings.ToLower(strings.TrimSpace(name)))
	if n == "" {
		return false
	}
	if secretExact[n] {
		return true
	}
	// A leading "x" as in X-Auth or X-Sig.
	if strings.HasPrefix(n, "x") && secretExact[n[1:]] {
		return true
	}
	for _, p := range secretParts {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

var (
	// reBearer finds a bearer or basic credential in free text.
	reBearer = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]{8,}`)
	// reJWT finds a JSON Web Token: three base64url parts, the first a
	// JSON header ("eyJ").
	reJWT = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
)

// RedactText replaces bearer credentials and JWTs in free text.
func RedactText(s string) string {
	s = reBearer.ReplaceAllStringFunc(s, func(m string) string {
		kind, _, _ := strings.Cut(m, " ")
		return kind + " " + Redacted
	})
	return reJWT.ReplaceAllString(s, Redacted)
}

// RedactHeaders returns the headers with secret values replaced; a value
// that is a URL (Location, Referer, …) is redacted as one.
func RedactHeaders(hs []Header) []Header {
	out := make([]Header, len(hs))
	for i, h := range hs {
		v := h.Value
		switch {
		case SecretName(h.Name):
			v = Redacted
		case absoluteURL(v):
			v = RedactURL(v)
		default:
			v = RedactText(v)
		}
		out[i] = Header{Name: h.Name, Value: v}
	}
	return out
}

// absoluteURL reports whether s is a URL with a scheme and a host.
func absoluteURL(s string) bool {
	if strings.ContainsAny(s, " \t") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && u.Host != ""
}

// RedactURL replaces the values of secret query parameters, a password in
// the URL's user info, and tokens anywhere in it. A URL that does not
// parse is redacted as text.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return RedactText(raw)
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), Redacted)
		}
	}
	if u.RawQuery != "" {
		u.RawQuery = redactQuery(u.RawQuery)
	}
	// url.String escapes the brackets of Redacted in user info.
	s := strings.ReplaceAll(u.String(), url.QueryEscape(Redacted), Redacted)
	return RedactText(s)
}

// redactQuery redacts a query or a form body, keeping the order of its
// parameters.
func redactQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, p := range parts {
		name, _, ok := strings.Cut(p, "=")
		if !ok {
			continue
		}
		if n, err := url.QueryUnescape(name); err == nil && SecretName(n) {
			parts[i] = name + "=" + Redacted
		}
	}
	return strings.Join(parts, "&")
}

// RedactBody redacts a body by its content type: JSON fields and form
// parameters with secret names, and tokens in any text. A body that
// starts like JSON is read as JSON whatever its type; one that does not
// parse (cut at BodyMax) has its secret fields found in the text. A body
// that is not text is left out entirely: the result says its type and
// size instead (binary is true).
func RedactBody(contentType string, body []byte) (text string, binary bool) {
	mt, _, _ := mime.ParseMediaType(contentType)
	trimmed := bytes.TrimSpace(body)
	jsonish := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	isJSON := mt == "application/json" || strings.HasSuffix(mt, "+json")
	if jsonish || isJSON {
		var v any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&v); err == nil && !dec.More() {
			if out, err := marshalJSON(redactJSON(v)); err == nil {
				return RedactText(string(out)), false
			}
		}
		return RedactText(redactJSONText(string(body))), false
	}
	switch {
	case mt == "application/x-www-form-urlencoded":
		return RedactText(redactQuery(string(body))), false
	case textual(mt, body):
		return RedactText(string(body)), false
	}
	return "", true
}

// textual reports whether a body reads as text.
func textual(mt string, body []byte) bool {
	switch {
	case strings.HasPrefix(mt, "text/"), strings.HasSuffix(mt, "+xml"), mt == "application/xml",
		mt == "application/javascript", mt == "application/graphql":
		return true
	case mt != "":
		return false
	}
	// No type: text when it is valid UTF-8 without control bytes.
	for _, r := range string(body) {
		if r == '�' || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			return false
		}
	}
	return true
}

// redactJSON replaces the values of secret fields, at any depth: a
// string, an object or a list whole, or a number (but a "code" number,
// which is mostly a status). Other fields are searched.
func redactJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			switch x.(type) {
			case nil, bool:
			case json.Number:
				if SecretName(k) && !strings.EqualFold(k, "code") {
					t[k] = Redacted
				}
			default:
				if SecretName(k) {
					t[k] = Redacted
				} else {
					t[k] = redactJSON(x)
				}
			}
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = redactJSON(x)
		}
		return t
	}
	return v
}

// reJSONKey finds a JSON object key and its colon.
var reJSONKey = regexp.MustCompile(`"((?:[^"\\]|\\.){1,200})"\s*:\s*`)

// redactJSONText redacts the secret fields of JSON that does not parse,
// as a body cut short: each value of a field with a secret name, a
// string, a number or a whole object or list, up to where it ends or the
// text does.
func redactJSONText(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range reJSONKey.FindAllStringSubmatchIndex(s, -1) {
		if m[0] < last {
			continue // inside a value already redacted
		}
		name := s[m[2]:m[3]]
		if !SecretName(name) {
			continue
		}
		end := jsonValueEnd(s, m[1])
		if end == m[1] {
			continue
		}
		if strings.EqualFold(name, "code") && end > m[1] && s[m[1]] != '"' && s[m[1]] != '{' && s[m[1]] != '[' {
			continue // a status number
		}
		b.WriteString(s[last:m[1]])
		b.WriteString(`"` + Redacted + `"`)
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// jsonValueEnd returns where the JSON value starting at i ends: after a
// string, after the object or list that opens there (or at the end of s
// when it is cut), or before the next ',', '}' or ']'.
func jsonValueEnd(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '"':
		for j := i + 1; j < len(s); j++ {
			switch s[j] {
			case '\\':
				j++
			case '"':
				return j + 1
			}
		}
		return len(s)
	case '{', '[':
		depth, inStr := 0, false
		for j := i; j < len(s); j++ {
			c := s[j]
			switch {
			case inStr && c == '\\':
				j++
			case c == '"':
				inStr = !inStr
			case inStr:
			case c == '{' || c == '[':
				depth++
			case c == '}' || c == ']':
				depth--
				if depth == 0 {
					return j + 1
				}
			}
		}
		return len(s)
	}
	j := i
	for j < len(s) && !strings.ContainsRune(",}]\n", rune(s[j])) {
		j++
	}
	return j
}

// marshalJSON writes JSON without escaping HTML; encoding/json sorts map
// keys, so a redacted body reads the same each time.
func marshalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
