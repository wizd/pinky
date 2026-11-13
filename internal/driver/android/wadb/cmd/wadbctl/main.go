// Command wadbctl pairs with and talks to an adbd over Wireless debugging
// with the wadb package, for checks during development. On a computer,
// forward the emulator's ports first (its Wireless debugging listens on
// the emulator's own network):
//
//	adb -s emulator-5660 forward tcp:15001 tcp:<pairing port>
//	go run ./internal/driver/android/wadb/cmd/wadbctl -key /tmp/k pair -addr 127.0.0.1:15001 -code 123456
//	adb -s emulator-5660 forward tcp:15002 tcp:<connect port>
//	go run ./internal/driver/android/wadb/cmd/wadbctl -key /tmp/k shell -addr 127.0.0.1:15002 id
//
// Commands:
//
//	pair -addr A -code C        pair the key in -key with the pairing port A
//	shell -addr A CMD...        run CMD as the shell user and print its output
//	push -addr A LOCAL REMOTE   copy a file to the phone
//	display -addr A [PACKAGE]   start the scrcpy server on a new virtual
//	                            display, start PACKAGE there and count
//	                            video for a few seconds
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DIYgod/pinky/internal/driver/android/adb"
	"github.com/DIYgod/pinky/internal/driver/android/scrcpy"
	"github.com/DIYgod/pinky/internal/driver/android/wadb"
)

func main() {
	keyDir := flag.String("key", "", "directory of the adb key (created when missing)")
	flag.Parse()
	if *keyDir == "" || flag.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: wadbctl -key DIR pair|shell|push ...")
		os.Exit(2)
	}
	key, err := wadb.LoadKey(*keyDir)
	if err != nil {
		fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fs := flag.NewFlagSet(flag.Arg(0), flag.ExitOnError)
	addr := fs.String("addr", "", "host:port")
	code := fs.String("code", "", "pairing code")
	_ = fs.Parse(flag.Args()[1:])
	switch flag.Arg(0) {
	case "pair":
		guid, err := wadb.Pair(ctx, *addr, *code, key, "pinky@wadbctl")
		if err != nil {
			fail(err)
		}
		fmt.Println("paired:", guid)
	case "shell", "push", "display":
		c, err := wadb.Dial(ctx, *addr, key)
		if err != nil {
			fail(err)
		}
		defer c.Close()
		fmt.Fprintln(os.Stderr, "connected:", c.Banner)
		cl := &adb.Client{Direct: c.Dialer()}
		if flag.Arg(0) == "display" {
			display(ctx, cl, fs.Arg(0))
			return
		}
		if flag.Arg(0) == "push" {
			f, err := os.Open(fs.Arg(0))
			if err != nil {
				fail(err)
			}
			defer f.Close()
			if err := cl.Push(ctx, "self", f, fs.Arg(1), 0o644, time.Now()); err != nil {
				fail(err)
			}
			return
		}
		r, err := cl.Run(ctx, "self", strings.Join(fs.Args(), " "))
		if err != nil {
			fail(err)
		}
		os.Stdout.Write(r.Stdout)
		os.Stderr.Write(r.Stderr)
		os.Exit(r.ExitCode)
	default:
		fail(fmt.Errorf("unknown command %q", flag.Arg(0)))
	}
}

// display checks the background display path over wadb alone: the
// scrcpy server pushed, run and reached through the phone's adbd, with no
// adb server.
func display(ctx context.Context, cl *adb.Client, pkg string) {
	if pkg == "" {
		pkg = "com.android.settings"
	}
	t0 := time.Now()
	s, err := scrcpy.Start(ctx, cl, "self", scrcpy.Options{
		MaxSize:          1080,
		Virtual:          &scrcpy.VirtualDisplay{Width: 720, Height: 1280, DPI: 320, NoSystemDecorations: true},
		DisplayIMEPolicy: "local",
	})
	if err != nil {
		fail(err)
	}
	defer s.Close()
	id, err := s.DisplayID(ctx)
	if err != nil {
		fail(err)
	}
	fmt.Printf("display %d after %v\n", id, time.Since(t0).Round(time.Millisecond))
	app, err := s.StartApp(ctx, pkg)
	if err != nil {
		fail(err)
	}
	fmt.Printf("started %s (%s) on display %d\n", app.Name, app.Package, app.Display)
	// A still screen sends no new frames: count what comes in 4 seconds.
	type result struct {
		p   scrcpy.Packet
		err error
	}
	ch := make(chan result)
	go func() {
		for {
			p, err := s.ReadPacket()
			ch <- result{p, err}
			if err != nil {
				return
			}
		}
	}()
	var n, size, keys int
	timeout := time.After(4 * time.Second)
read:
	for {
		select {
		case r := <-ch:
			if r.err != nil {
				fail(r.err)
			}
			if r.p.Session {
				fmt.Printf("video %dx%d\n", r.p.Width, r.p.Height)
				continue
			}
			n++
			size += len(r.p.Data)
			if r.p.KeyFrame {
				keys++
			}
		case <-timeout:
			break read
		}
	}
	fmt.Printf("%d packets, %d key frames, %d bytes in 4s\n", n, keys, size)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "wadbctl:", err)
	os.Exit(1)
}
