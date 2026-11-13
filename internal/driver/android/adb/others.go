package adb

import (
	"net"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Other adb servers. Several adb servers can run on one computer, each on
// its own port (`adb -P 5049 start-server`, or a tool that sets
// ANDROID_ADB_SERVER_PORT for its own), but a phone's USB interface goes
// to one of them: macOS gives it to one process at a time
// (kIOReturnExclusiveAccess, "Could not open interface: e00002c5") and
// Linux to the first that claims it. A phone on a cable that another
// server holds is missing from the server Pinky uses (GitHub issue #95).
//
// OtherServers finds those servers by their command lines, so that the
// driver can ask them which phones they hold (host:devices-l, and nothing
// else: never a request that changes what a server does).

// OtherServers returns the ports of the adb servers that run on this
// computer other than the client's, as ps shows them, lowest first. It
// returns none when the client's server is on another computer, and
// errors.ErrUnsupported on Windows, where a process's command line is not
// at hand and the ports an adb server listens on include those of `adb
// forward`, which belong to other tools' connections to their phones.
func (c *Client) OtherServers() ([]int, error) {
	host, port, err := net.SplitHostPort(c.addr())
	if err != nil || !isLoopback(host) {
		return nil, nil
	}
	own, _ := strconv.Atoi(port)
	out, err := processList()
	if err != nil {
		return nil, err
	}
	var ports []int
	for _, p := range serverPorts(out) {
		if p != own {
			ports = append(ports, p)
		}
	}
	return ports, nil
}

// serverPorts reads the ports of the adb servers from ps's lines, "<pid>
// <command line>": the servers adb's start-server and Pinky start (`adb
// -L tcp:<port> fork-server server --reply-fd <n>`), and those that run
// in the foreground (`adb -L tcp:<port> server nodaemon`, `adb -P <port>
// nodaemon server`). A server for a loopback address names it
// (`tcp:localhost:5037`); one on another address, or on a socket file
// (`localfilesystem:`), is not one Pinky can ask on 127.0.0.1. Without -L
// or -P a server uses 5037. Each port once, lowest first.
func serverPorts(out string) []int {
	var ports []int
	for _, line := range strings.Split(out, "\n") {
		_, command, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if p, ok := serverPort(strings.TrimSpace(command)); ok && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	slices.Sort(ports)
	return ports
}

// serverPort reads the port of an adb server from its command line, and
// reports false for any other command line. The program is an adb by its
// name or its path, which may hold spaces: what comes before its first
// option.
func serverPort(command string) (int, bool) {
	at := -1
	for _, opt := range []string{" -L ", " -P", " fork-server ", " nodaemon", " server"} {
		if i := strings.Index(command, opt); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	if at < 0 || path.Base(command[:at]) != "adb" {
		return 0, false
	}
	args := strings.Fields(command[at:])
	port, spec := "5037", ""
	server, daemon := false, false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-L" && i+1 < len(args):
			i++
			spec = args[i]
		case a == "-P" && i+1 < len(args):
			i++
			port = args[i]
		case strings.HasPrefix(a, "-P") && len(a) > 2:
			port = a[2:]
		case a == "server":
			server = true
		case a == "fork-server" || a == "nodaemon":
			daemon = true
		}
	}
	if !server || !daemon {
		return 0, false // a client command, such as `adb -P 5049 devices`
	}
	if spec != "" {
		rest, ok := strings.CutPrefix(spec, "tcp:")
		if !ok {
			return 0, false
		}
		port = rest
		if h, p, err := net.SplitHostPort(rest); err == nil {
			if !isLoopback(h) {
				return 0, false
			}
			port = p
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return 0, false
	}
	return n, true
}
