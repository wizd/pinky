package companion

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePhone stands in for adb and the companion app: a secure settings
// table, and a TCP server that speaks the protocol while Pinky's service is
// listed in enabled_accessibility_services. Like adb, it accepts
// connections when the service is off and closes them at once.
type fakePhone struct {
	t  *testing.T
	ln net.Listener

	mu        sync.Mutex
	settings  map[string]string // secure settings; absent means unset
	shell     []string          // every shell command, in order
	forwards  int
	installed []string
	installs  int
	requests  []map[string]any
	skew      time.Duration // phone clock minus host clock
	// rewrite simulates Android persisting the list again in its own form
	// while the service is on.
	rewrite func(string) string
	// ignoreWrites takes `settings put` and `settings delete` without an
	// error and changes nothing, as vivo's OriginOS 6 does with
	// enabled_accessibility_services (issue #94).
	ignoreWrites bool
	// deaf closes every connection at once, even while Pinky's service is
	// listed: a service that never starts.
	deaf bool
	// dropAfter closes a connection after that many requests on it.
	dropAfter int
	setText   func(req map[string]any) (any, *Error)
	input     func(req map[string]any) *Error // gesture and global; nil answers ok
	apkSum    string                          // sha256 of the installed APK
	// probe is what the phone shows when the client asks why the service
	// did not answer (probeCmd); "" fails the command.
	probe  string
	conns  []net.Conn
	events chan string
	// claim answers claim; nil gives the claim. An error with code
	// fakeHangUp closes the connection instead of answering.
	claim func(req map[string]any) (any, *Error)
}

// fakeHangUp, as the code of an answer's error, makes the fake phone close
// the connection without replying.
const fakeHangUp = "fake_hang_up"

func newFakePhone(t *testing.T) *fakePhone {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePhone{t: t, ln: ln, settings: map[string]string{}, events: make(chan string, 16)}
	go p.serve()
	t.Cleanup(func() {
		ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *fakePhone) on() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(parseServices(p.settings[settingKey]), New(nil).isOurs)
}

func (p *fakePhone) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		deaf := p.deaf
		p.mu.Unlock()
		if !p.on() || deaf {
			c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c)
		p.mu.Unlock()
		go p.handle(c)
	}
}

func (p *fakePhone) handle(c net.Conn) {
	defer c.Close()
	var wmu sync.Mutex
	write := func(b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := c.Write(append(b, '\n'))
		return err
	}
	rd := bufio.NewReader(c)
	n := 0
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req map[string]any
		if err := json.Unmarshal(line, &req); err != nil {
			p.t.Errorf("bad request %q: %v", line, err)
			return
		}
		p.mu.Lock()
		p.requests = append(p.requests, req)
		drop := p.dropAfter
		p.mu.Unlock()
		result, perr := p.answer(req)
		if perr != nil && perr.Code == fakeHangUp {
			return
		}
		resp := map[string]any{"id": req["id"], "ok": perr == nil}
		if perr != nil {
			resp["error"] = perr
		} else {
			resp["result"] = result
		}
		b, _ := json.Marshal(resp)
		if write(b) != nil {
			return
		}
		if req["cmd"] == "events" {
			// Events go out while the connection takes more requests,
			// as the app's do; closing p.events ends the connection.
			go func() {
				for ev := range p.events {
					if write([]byte(ev)) != nil {
						return
					}
				}
				c.Close()
			}()
			continue
		}
		n++
		if drop > 0 && n >= drop {
			return
		}
	}
}

func (p *fakePhone) answer(req map[string]any) (any, *Error) {
	switch req["cmd"] {
	case "ping":
		p.mu.Lock()
		skew := p.skew
		p.mu.Unlock()
		return map[string]any{"protocol": 1, "version": "0.1.0", "sdk": 29, "time": time.Now().Add(skew).UnixMilli(), "displays": []int{0}}, nil
	case "tree":
		return json.RawMessage(sampleTree), nil
	case "set_text":
		if p.setText != nil {
			return p.setText(req)
		}
		return map[string]any{"window": 41, "node": 4242, "text": req["text"], "verified": true}, nil
	case "wait_idle":
		return map[string]any{"settled": true, "waited_ms": 300}, nil
	case "config":
		return map[string]any{}, nil
	case "gesture", "global":
		if p.input != nil {
			if err := p.input(req); err != nil {
				return nil, err
			}
		}
		return map[string]any{"took_ms": 12}, nil
	case "events":
		return map[string]any{"kinds": req["kinds"]}, nil
	case "claim":
		p.mu.Lock()
		claim := p.claim
		p.mu.Unlock()
		if claim != nil {
			return claim(req)
		}
		return map[string]any{"by": req["by"], "task": req["task"]}, nil
	}
	return nil, &Error{Code: "unknown_command", Message: fmt.Sprint(req["cmd"])}
}

func (p *fakePhone) lastRequest(cmd string) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.requests) - 1; i >= 0; i-- {
		if p.requests[i]["cmd"] == cmd {
			return p.requests[i]
		}
	}
	return nil
}

// Runner implementation.

func (p *fakePhone) Shell(_ context.Context, cmd string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shell = append(p.shell, cmd)
	f := strings.Fields(cmd)
	switch {
	case len(f) == 4 && f[0] == "settings" && f[1] == "get" && f[2] == "secure":
		v, ok := p.settings[f[3]]
		if !ok {
			return "null\n", nil
		}
		return v + "\n", nil
	case len(f) >= 4 && f[0] == "settings" && f[1] == "put" && f[2] == "secure":
		rest := strings.TrimPrefix(cmd, "settings put secure "+f[3]+" ")
		v, err := unquote(rest)
		if err != nil {
			return "", err
		}
		if p.ignoreWrites {
			return "", nil
		}
		if p.rewrite != nil {
			v = p.rewrite(v)
		}
		p.settings[f[3]] = v
		return "", nil
	case len(f) == 4 && f[0] == "settings" && f[1] == "delete" && f[2] == "secure":
		if p.ignoreWrites {
			return "", nil
		}
		delete(p.settings, f[3])
		return "Deleted 1 rows\n", nil
	case len(f) == 3 && f[0] == "pm" && f[1] == "path":
		if slices.Contains(p.installed, f[2]) {
			return "package:/data/app/~~abc/" + f[2] + "-xyz/base.apk\n", nil
		}
		return "", fmt.Errorf("exit status 1")
	case len(f) == 2 && f[0] == "sha256sum":
		return p.apkSum + "  " + strings.Trim(f[1], "'") + "\n", nil
	case strings.HasPrefix(cmd, "echo "+probeMark):
		if p.probe == "" {
			return "", fmt.Errorf("exit status 1")
		}
		return p.probe, nil
	case len(f) == 3 && f[0] == "dumpsys" && f[1] == "package":
		if slices.Contains(p.installed, f[2]) {
			return "Packages:\n  Package [" + f[2] + "] (1a2b3c):\n    versionCode=1 minSdk=29 targetSdk=36\n    versionName=0.1.0\n", nil
		}
		return "", nil
	}
	return "", fmt.Errorf("fake shell: unexpected command %q", cmd)
}

func (p *fakePhone) Forward(context.Context, string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forwards++
	return p.ln.Addr().String(), nil
}

func (p *fakePhone) RemoveForward(context.Context, string) error { return nil }

func (p *fakePhone) Install(_ context.Context, apk string) error {
	sum, err := fileSHA256(apk)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Contains(p.installed, Release.Package) {
		p.installed = append(p.installed, Release.Package)
	}
	p.apkSum = sum
	p.installs++
	return nil
}

// unquote reverses shellQuote.
func unquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", fmt.Errorf("not single-quoted: %q", s)
	}
	return strings.ReplaceAll(s[1:len(s)-1], `'\''`, `'`), nil
}
