package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/demo"
	"github.com/DIYgod/pinky/internal/driver"
	"github.com/DIYgod/pinky/internal/gateway"
	"github.com/DIYgod/pinky/internal/mcp"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/vcorplink"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// runVcorp pairs this Pinky with a VCorp Super Agent account and relays the
// account's cloud tasks to the phones (internal/vcorplink).
//
//	pinky vcorp pair <code> --origin https://maker.example.com
//	pinky vcorp run
//	pinky vcorp status
//	pinky vcorp unpair
func runVcorp(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pinky vcorp pair <code> --origin <url> | run | status | unpair")
	}
	dirs, err := api.DefaultDirs()
	if err != nil {
		return err
	}
	file := vcorplink.File(dirs.Config)
	if f := os.Getenv("PINKY_VCORP_CONFIG"); f != "" {
		file = f // tests pair a second time without touching the real pairing
	}
	switch args[0] {
	case "pair":
		return vcorpPair(file, args[1:])
	case "run":
		return vcorpRun(dirs, file, args[1:])
	case "status":
		c, err := vcorplink.Load(file)
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Println("not paired")
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("paired with %s on %s as %s (%s)\n", c.Account, c.Origin, c.Name, c.DeviceID)
		return nil
	case "unpair":
		if err := vcorplink.Remove(file); err != nil {
			return err
		}
		fmt.Println("unpaired here; remove it on the platform's device page too")
		return nil
	}
	return fmt.Errorf("unknown: pinky vcorp %s", args[0])
}

func vcorpPair(file string, args []string) error {
	fs := flag.NewFlagSet("pinky vcorp pair", flag.ContinueOnError)
	origin := fs.String("origin", "", "the platform's address, https://…")
	name := fs.String("name", "", "the name the platform shows (default: this computer's name)")
	yes := fs.Bool("yes", false, "pair without asking to confirm the account")
	code := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		code, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if code == "" && fs.NArg() > 0 {
		code = fs.Arg(0)
	}
	if code == "" || *origin == "" {
		return errors.New("usage: pinky vcorp pair <code> --origin https://…")
	}
	o, err := vcorplink.NormOrigin(*origin)
	if err != nil {
		return err
	}
	ctx := context.Background()
	account, err := vcorplink.Claim(ctx, o, code)
	if err != nil {
		return err
	}
	if !*yes {
		fmt.Printf("Pair Pinky with %s on %s? Its cloud tasks will be able to use your phones (approvals stay in Pinky). [y/N] ", account, o)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("not paired")
		}
	}
	n := *name
	if n == "" {
		host, _ := os.Hostname()
		n = "Pinky · " + host
	}
	c, err := vcorplink.Pair(ctx, o, code, n)
	if err != nil {
		return err
	}
	if err := vcorplink.Save(file, c); err != nil {
		return err
	}
	fmt.Printf("paired with %s as %s; Pinky links to the platform while it runs\n", c.Account, c.DeviceID)
	return nil
}

func vcorpRun(dirs api.Dirs, file string, args []string) error {
	fl := flag.NewFlagSet("pinky vcorp run", flag.ContinueOnError)
	verbose := fl.Bool("v", false, "log more")
	fake := fl.Bool("fake", false, "relay to a private gateway with fake phones instead of the running Pinky")
	if err := fl.Parse(args); err != nil {
		return err
	}
	c, err := vcorplink.Load(file)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("not paired: run pinky vcorp pair first")
	}
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	var host vcorplink.Host
	if *fake {
		providers, _ := demo.World()
		pol := policy.Default()
		for cat, e := range demo.PolicyApps() {
			pol.AddApps(cat, e)
		}
		g, err := gateway.New(gateway.Options{Providers: providers, Policy: pol, Logger: log, TrustNewAgents: true})
		if err != nil {
			return err
		}
		defer g.Close()
		host = &vcorplink.GatewayHost{G: g, MCP: mcp.New(g, version)}
	} else {
		// A Pinky that keeps the link itself already connects: a second
		// connection would push it off the platform.
		if ctl, err := api.DialControl(dirs); err == nil {
			own := slices.Contains(ctl.Reply.Features, vcorplink.Feature)
			ctl.Close()
			if own {
				return errors.New("the Pinky app already links to the platform while it runs; nothing to start (see pinky vcorp status)")
			}
		}
		tok, err := api.EnsureToken(dirs.TokenFile())
		if err != nil {
			return err
		}
		host = &socketHost{dirs: dirs, token: tok}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	l := &vcorplink.Link{Cfg: c, Host: host, Version: version, Platform: hostPlatform(), Log: log}
	err = l.Run(ctx)
	if errors.Is(err, vcorplink.ErrRevoked) {
		vcorplink.Remove(file)
		return errors.New("the platform removed this Pinky; pair again to reconnect")
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// StartVcorp keeps this gateway linked to the VCorp account it is paired
// with (`pinky vcorp pair`), in the background until ctx ends. Fake phones
// are never offered to the platform.
func (r *runningGateway) StartVcorp(ctx context.Context) {
	if r.fake {
		return
	}
	file := vcorplink.File(r.Dirs.Config)
	host := &vcorplink.GatewayHost{G: r.G, MCP: r.MCP}
	go vcorplink.Supervise(ctx, file, func(c *vcorplink.Config) *vcorplink.Link {
		return &vcorplink.Link{Cfg: c, Host: host, Version: version, Platform: hostPlatform(), Log: r.log}
	}, r.log)
}

func hostPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	default:
		return runtime.GOOS
	}
}

// socketHost reaches the running gateway (the Pinky app, or pinky daemon)
// through its per-user socket, as `pinky mcp` does.
type socketHost struct {
	dirs  api.Dirs
	token string

	mu  sync.Mutex
	ctl *api.Control
}

func (h *socketHost) Phones(ctx context.Context) ([]vcorplink.Phone, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctl == nil {
		ctl, err := api.DialControl(h.dirs)
		if err != nil {
			return nil, fmt.Errorf("Pinky is not running: %w", err)
		}
		h.ctl = ctl
	}
	var devs []gateway.DeviceView
	if err := h.ctl.Call("devices", nil, &devs); err != nil {
		h.ctl.Close()
		h.ctl = nil
		return nil, err
	}
	out := make([]vcorplink.Phone, 0, len(devs))
	for _, d := range devs {
		out = append(out, vcorplink.Phone{ID: d.Info.ID, Name: d.Info.Name, Platform: string(d.Info.Platform),
			Online: d.Info.State == driver.StateOnline && !d.Paused})
	}
	return out, nil
}

func (h *socketHost) Connect(ctx context.Context) (sdk.Transport, error) {
	conn, err := api.Dial(h.dirs)
	if err != nil {
		return nil, fmt.Errorf("Pinky is not running: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	br, _, err := api.Greet(conn, api.Hello{Role: api.RoleMCP, Token: h.token})
	conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &sdk.IOTransport{Reader: readCloser{br, conn}, Writer: conn}, nil
}

type readCloser struct {
	io.Reader
	c net.Conn
}

func (r readCloser) Close() error { return r.c.Close() }
