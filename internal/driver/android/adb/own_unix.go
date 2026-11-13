//go:build !windows

package adb

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// serverProcesses lists the processes of adb servers for port, as ps
// shows them: those whose command line has `-L tcp:<port> fork-server
// server`. Only one can listen on the port.
func serverProcesses(port string) ([]process, error) {
	out, err := processList()
	if err != nil {
		return nil, err
	}
	return parseProcesses(out, port), nil
}

// processList is ps's list of every process, "<pid> <command line>" a
// line, the command lines in full.
func processList() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-ww", "-A", "-o", "pid=,command=").Output()
	return string(out), err
}

// stopProcess asks the server to quit, as the system does when the
// computer shuts down.
func stopProcess(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }
