// Command rppair prepares a phone for the standalone prototype from a Mac
// that the phone already trusts (Pinky's lockdown pairing):
//
//	rppair pair  -udid <udid> -out <file>   create a RemotePairing pairing
//	rppair check -udid <udid> -id <file> [-addr <ip>:49152]
//
// pair asks remotepairingd, over lockdown, to pair a fresh host identity with
// the fixed lockdown PIN; the phone shows nothing. It writes the identity to
// <file> (idevice's rp_pairing_file.plist format, mode 0600), which the
// prototype app then carries. Whoever holds that file can open a CoreDevice
// tunnel to the phone from its network: keep it outside the repo.
//
// check verifies the pairing over lockdown and, with -addr, the network path
// the app takes on the phone (pair-verify, a tunnel listener, TLS-PSK and the
// CDTunnel parameters). Run both under scripts/devlock.sh.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	goios "github.com/danielpaulus/go-ios/ios"

	"github.com/DIYgod/pinky/ios/rppair"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	udid := fs.String("udid", "", "the phone's UDID")
	out := fs.String("out", "", "pair: where to write the pairing file")
	idPath := fs.String("id", "", "check: the pairing file")
	addr := fs.String("addr", "", "check: the phone's address on the network, host:port")
	fs.Parse(os.Args[2:])
	if *udid == "" {
		usage()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	switch os.Args[1] {
	case "pair":
		err = pair(ctx, *udid, *out)
	case "check":
		err = check(ctx, *udid, *idPath, *addr)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rppair:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: rppair pair -udid <udid> -out <file>\n       rppair check -udid <udid> -id <file> [-addr <host:port>]")
	os.Exit(2)
}

func lockdown(udid string) (io.ReadWriteCloser, error) {
	entry, err := goios.GetDevice(udid)
	if err != nil {
		return nil, err
	}
	return goios.ConnectToService(entry, rppair.LockdownService)
}

func pair(ctx context.Context, udid, out string) error {
	if out == "" {
		return errors.New("-out is required")
	}
	id, err := rppair.LoadIdentity(out)
	if errors.Is(err, os.ErrNotExist) {
		id, err = rppair.NewIdentity()
	}
	if err != nil {
		return err
	}
	conn, err := lockdown(udid)
	if err != nil {
		return fmt.Errorf("lockdown service: %w", err)
	}
	defer conn.Close()
	id, err = rppair.Setup(ctx, conn, id)
	if err != nil {
		return err
	}
	b, err := id.Marshal()
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		return err
	}
	fmt.Printf("paired host %s; wrote %s\n", id.Identifier, out)
	return nil
}

func check(ctx context.Context, udid, idPath, addr string) error {
	id, err := rppair.LoadIdentity(idPath)
	if err != nil {
		return err
	}
	conn, err := lockdown(udid)
	if err != nil {
		return fmt.Errorf("lockdown service: %w", err)
	}
	s, err := rppair.Verify(ctx, conn, id)
	if err != nil {
		conn.Close()
		return fmt.Errorf("over lockdown: %w", err)
	}
	fmt.Printf("lockdown: pair-verify ok (%v)\n", s.Peer["name"])
	s.Close()
	if addr == "" {
		return nil
	}

	start := time.Now()
	s, err = rppair.Dial(ctx, addr, id)
	if err != nil {
		return fmt.Errorf("over the network: %w", err)
	}
	defer s.Close()
	fmt.Printf("network: pair-verify ok in %v\n", time.Since(start).Round(time.Millisecond))
	t, err := s.DialTunnel(ctx)
	if err != nil {
		return err
	}
	defer t.Close()
	params, err := cdtunnel(t)
	if err != nil {
		return fmt.Errorf("CDTunnel: %w", err)
	}
	fmt.Printf("network: tunnel up in %v: %s\n", time.Since(start).Round(time.Millisecond), params)
	return nil
}

// cdtunnel asks for the tunnel's parameters and returns them as the phone
// sent them.
func cdtunnel(rw io.ReadWriter) (string, error) {
	body, _ := json.Marshal(map[string]any{"type": "clientHandshakeRequest", "mtu": 1280})
	var req bytes.Buffer
	req.WriteString("CDTunnel")
	binary.Write(&req, binary.BigEndian, uint16(len(body)))
	req.Write(body)
	if _, err := rw.Write(req.Bytes()); err != nil {
		return "", err
	}
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(rw, hdr); err != nil {
		return "", err
	}
	if string(hdr[:8]) != "CDTunnel" {
		return "", fmt.Errorf("unexpected header %q", hdr[:8])
	}
	resp := make([]byte, binary.BigEndian.Uint16(hdr[8:]))
	if _, err := io.ReadFull(rw, resp); err != nil {
		return "", err
	}
	return string(resp), nil
}
