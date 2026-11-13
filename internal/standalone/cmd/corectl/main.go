// Command corectl talks to Pinky's core on an Android phone over its
// control socket, as Pinky's own screens do, for checks from a shell. The
// socket takes only the app's own uid, so run it with run-as, which works
// for the development build (scripts/androidcore.sh) only:
//
//	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o corectl ./internal/standalone/cmd/corectl
//	adb push corectl /data/local/tmp/
//	adb shell run-as app.pinky.companion.dev sh -c 'cp /data/local/tmp/corectl . && ./corectl state'
//
// Commands:
//
//	state                      print the state
//	start -task T [-key K] [-base-url U] [-model M]   start a task
//	watch [-decide D] [-after 2s]   print steps and needs until the task ends,
//	                           answering each need with D (allow_once, deny,
//	                           or none to leave it), and time each step
//	stop [-computer]           stop the task, or with -computer Pinky on a
//	                           computer, as Stop on the pill does
//	dismiss
//	decide NEED DECISION
//	history [-before MS] [-limit N]   list the tasks that ended, newest first
//	history ID                 print one task with its steps
//	model [-service S | -provider P] [-base-url U] [-model M]   set the
//	                           model as Settings does and save the key read
//	                           from standard input (one line), so that it
//	                           never appears on a command line (debug
//	                           builds only)
//	check [-service S] [-provider P] [-base-url U] [-model M] [-key K]
//	                           check a model as Settings does
//	usage [-since 2006-01-02]  print what the model used, by day
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// socket is the development build's core socket (CORE_SOCKET in
// android/companion/app/build.gradle.kts).
const socket = "@pinky_core_dev"

// serviceSocket is the development build's accessibility service socket
// (SOCKET_NAME in android/companion/app/build.gradle.kts).
const serviceSocket = "@pinky_companion_dev"

// The parts of standalone.State it reads. It does not import the core, so
// it builds as a static binary for the phone's shell.
type state struct {
	Agent *struct {
		Running bool   `json:"running"`
		End     string `json:"end"`
		Summary string `json:"summary"`
		Steps   int    `json:"steps"`
		Tokens  int64  `json:"tokens"`
		Seconds int64  `json:"seconds"`
	} `json:"agent"`
	Task *struct {
		Steps []struct {
			Text   string `json:"text"`
			Status string `json:"status"`
			MS     int64  `json:"ms"`
		} `json:"steps"`
	} `json:"task"`
	Needs []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Rule   string `json:"rule"`
	} `json:"needs"`
}

type reply struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error"`
	State   json.RawMessage `json:"state"`
	History json.RawMessage `json:"history"`
	More    bool            `json:"more"`
	Check   json.RawMessage `json:"check"`
	Usage   json.RawMessage `json:"usage"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: corectl state|start|watch|stop|dismiss|decide|preset|history|model|check|usage")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "state":
		var r reply
		if r, err = call(map[string]any{"cmd": "state"}); err == nil {
			var v any
			_ = json.Unmarshal(r.State, &v)
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Println(string(b))
		}
	case "start":
		fs := flag.NewFlagSet("start", flag.ExitOnError)
		task := fs.String("task", "", "the task")
		key := fs.String("key", "fake", "the API key (a fake one for fakemodel)")
		service := fs.String("service", "", "the service's ID (internal/pilot/services.go); empty: from -provider and -base-url")
		provider := fs.String("provider", "", "anthropic (the default) or openai")
		base := fs.String("base-url", "", "the API's address")
		model := fs.String("model", "", "the model")
		steps := fs.Int("max-steps", 0, "step limit")
		mins := fs.Int("max-minutes", 0, "time limit")
		_ = fs.Parse(args)
		_, err = call(map[string]any{"cmd": "start", "task": *task, "key": *key, "service": *service, "provider": *provider,
			"base_url": *base, "model": *model, "max_steps": *steps, "max_minutes": *mins})
	case "check":
		fs := flag.NewFlagSet("check", flag.ExitOnError)
		key := fs.String("key", "fake", "the API key (a fake one for fakemodel)")
		service := fs.String("service", "", "the service's ID; empty: from -provider and -base-url")
		provider := fs.String("provider", "", "anthropic (the default) or openai")
		base := fs.String("base-url", "", "the API's address")
		model := fs.String("model", "", "the model")
		_ = fs.Parse(args)
		var r reply
		r, err = call(map[string]any{"cmd": "check", "key": *key, "service": *service, "provider": *provider,
			"base_url": *base, "model": *model})
		if err == nil {
			fmt.Println(string(r.Check))
		}
	case "usage":
		fs := flag.NewFlagSet("usage", flag.ExitOnError)
		since := fs.String("since", "", "the first day, 2006-01-02; empty for all")
		_ = fs.Parse(args)
		var r reply
		r, err = call(map[string]any{"cmd": "usage", "since": *since})
		if err == nil {
			fmt.Println(string(r.Usage))
		}
	case "stop":
		fs := flag.NewFlagSet("stop", flag.ExitOnError)
		computer := fs.Bool("computer", false, "end Pinky on a computer's claim instead of the task")
		_ = fs.Parse(args)
		_, err = call(map[string]any{"cmd": "stop", "computer": *computer})
	case "dismiss":
		_, err = call(map[string]any{"cmd": "dismiss"})
	case "decide":
		if len(args) != 2 {
			err = fmt.Errorf("decide NEED DECISION")
			break
		}
		_, err = call(map[string]any{"cmd": "decide", "need": args[0], "decision": args[1]})
	case "preset":
		var n int
		if len(args) != 1 {
			err = fmt.Errorf("preset 0|1|2|3")
			break
		}
		if n, err = strconv.Atoi(args[0]); err == nil {
			_, err = call(map[string]any{"cmd": "preset", "preset": n})
		}
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		decide := fs.String("decide", "allow_once", "answer to each need: allow_once, allow_task, deny, or none")
		after := fs.Duration("after", 2*time.Second, "wait this long before answering, as a person reading it")
		stopAfter := fs.Duration("stop-after", 0, "press Stop after this long (0: never)")
		_ = fs.Parse(args)
		err = watch(*decide, *after, *stopAfter)
	case "history":
		fs := flag.NewFlagSet("history", flag.ExitOnError)
		before := fs.Int64("before", 0, "tasks that started before this, in Unix milliseconds (0: now)")
		limit := fs.Int("limit", 0, "how many tasks (0: 20)")
		_ = fs.Parse(args)
		req := map[string]any{"cmd": "history", "before": *before, "limit": *limit}
		if fs.NArg() == 1 {
			req = map[string]any{"cmd": "history", "id": fs.Arg(0)}
		}
		var r reply
		if r, err = call(req); err == nil {
			var v any
			_ = json.Unmarshal(r.History, &v)
			b, _ := json.MarshalIndent(map[string]any{"history": v, "more": r.More}, "", "  ")
			fmt.Println(string(b))
		}
	case "model":
		fs := flag.NewFlagSet("model", flag.ExitOnError)
		service := fs.String("service", "", "the service's ID (internal/pilot/services.go); empty: from -provider and -base-url")
		provider := fs.String("provider", "anthropic", "anthropic or openai")
		base := fs.String("base-url", "", "the API's address (empty: the service's default)")
		model := fs.String("model", "", "the model")
		_ = fs.Parse(args)
		err = setModel(*service, *provider, *base, *model)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "corectl:", err)
		os.Exit(1)
	}
}

func call(req map[string]any) (reply, error) {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return reply{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply{}, err
	}
	var r reply
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return reply{}, err
	}
	if !r.OK {
		return r, fmt.Errorf("%s", r.Error)
	}
	return r, nil
}

// setModel sends debug_model to the service with the key read from
// standard input. It prints only whether a key is saved, never the key.
func setModel(service, provider, base, model string) error {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return fmt.Errorf("no key on standard input")
	}
	key := strings.TrimSpace(line)
	if key == "" {
		return fmt.Errorf("no key on standard input")
	}
	conn, err := net.Dial("unix", serviceSocket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"id": 1, "cmd": "debug_model", "service": service, "provider": provider,
		"base_url": base, "model": model, "key": key}); err != nil {
		return err
	}
	var r struct {
		OK     bool `json:"ok"`
		Result struct {
			Service  string `json:"service"`
			Provider string `json:"provider"`
			KeySaved bool   `json:"key_saved"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&r); err != nil {
		return err
	}
	if !r.OK {
		return fmt.Errorf("%v", r.Error)
	}
	fmt.Printf("service %s (%s), key saved: %v\n", r.Result.Service, r.Result.Provider, r.Result.KeySaved)
	return nil
}

func watch(decide string, after, stopAfter time.Duration) error {
	conn, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(map[string]any{"cmd": "subscribe"}); err != nil {
		return err
	}
	t0 := time.Now()
	in := bufio.NewScanner(conn)
	in.Buffer(make([]byte, 64<<10), 4<<20)
	printed := 0
	seenNeed := map[string]time.Time{}
	stopped := false
	wasRunning := false
	for in.Scan() {
		var r reply
		var st state
		if err := json.Unmarshal(in.Bytes(), &r); err != nil || json.Unmarshal(r.State, &st) != nil {
			continue
		}
		el := time.Since(t0).Seconds()
		if st.Task != nil {
			for ; printed < len(st.Task.Steps); printed++ {
				s := st.Task.Steps[printed]
				fmt.Printf("%7.1fs step %d: %s [%s, %d ms]\n", el, printed+1, s.Text, s.Status, s.MS)
			}
		}
		for _, n := range st.Needs {
			if _, ok := seenNeed[n.ID]; ok {
				continue
			}
			seenNeed[n.ID] = time.Now()
			fmt.Printf("%7.1fs NEED %s: %s · %s (rule %q)\n", el, n.Kind, n.Title, n.Detail, n.Rule)
			if decide != "none" {
				go func(id string) {
					time.Sleep(after)
					d := decide
					t := time.Now()
					if _, err := call(map[string]any{"cmd": "decide", "need": id, "decision": d}); err != nil {
						fmt.Printf("         decide %s: %v\n", id, err)
						return
					}
					fmt.Printf("%7.1fs decided %s (%s, %d ms)\n", time.Since(t0).Seconds(), d, id, time.Since(t).Milliseconds())
				}(n.ID)
			}
		}
		if stopAfter > 0 && !stopped && time.Since(t0) > stopAfter {
			stopped = true
			t := time.Now()
			if _, err := call(map[string]any{"cmd": "stop"}); err != nil {
				return err
			}
			fmt.Printf("%7.1fs STOP pressed (%d ms)\n", time.Since(t0).Seconds(), time.Since(t).Milliseconds())
		}
		if a := st.Agent; a != nil {
			if a.Running {
				wasRunning = true
			} else if wasRunning || time.Since(t0) > 3*time.Second {
				fmt.Printf("%7.1fs ENDED %s: %s (%d steps, %d tokens, %d s)\n", el, a.End, oneLine(a.Summary), a.Steps, a.Tokens, a.Seconds)
				return nil
			}
		}
	}
	return in.Err()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
