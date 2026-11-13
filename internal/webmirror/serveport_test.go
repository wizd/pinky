package webmirror

import (
	"testing"

	"github.com/DIYgod/pinky/internal/tailnet"
)

func TestServePort(t *testing.T) {
	web := func(urls ...string) tailnet.Serve {
		var s tailnet.Serve
		for _, u := range urls {
			s.Web = append(s.Web, tailnet.ServeWeb{URL: u})
		}
		return s
	}
	for _, tc := range []struct {
		s    tailnet.Serve
		want string
	}{
		{web(), "8443"},
		{web("https://mac.tail1.ts.net", "http://mac.tail1.ts.net:15244"), "8443"},
		// Another program's forward on 8443 stays: the next port.
		{web("https://mac.tail1.ts.net", "https://mac.tail1.ts.net:8443"), "10000"},
		{web("https://m.ts.net:8443", "https://m.ts.net:10000", "https://m.ts.net:9443", "https://m.ts.net:7443", "https://m.ts.net:4443"), ""},
	} {
		if got := ServePort(tc.s); got != tc.want {
			t.Errorf("ServePort(%v) = %q, want %q", tc.s.Web, got, tc.want)
		}
	}
}
