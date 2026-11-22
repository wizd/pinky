package main

import (
	"errors"
	"flag"
	"slices"
	"testing"
)

func TestCommandOf(t *testing.T) {
	for _, tc := range []struct {
		args []string
		cmd  string
		rest []string
	}{
		{nil, "app", nil},
		{[]string{"-fake"}, "app", []string{"-fake"}},
		{[]string{"--http", "127.0.0.1:7465"}, "app", []string{"--http", "127.0.0.1:7465"}},
		{[]string{"mcp"}, "mcp", []string{}},
		{[]string{"mcp", "--fake"}, "mcp", []string{"--fake"}},
		{[]string{"version"}, "version", []string{}},
		{[]string{"--version"}, "--version", []string{}},
		{[]string{"-v"}, "-v", []string{}},
		{[]string{"help"}, "help", []string{}},
		{[]string{"--help"}, "--help", []string{}},
		{[]string{"-h"}, "-h", []string{}},
		{[]string{"-help"}, "-help", []string{}},
	} {
		cmd, rest := commandOf(tc.args)
		if cmd != tc.cmd || !slices.Equal(rest, tc.rest) {
			t.Errorf("commandOf(%q) = %q, %q; want %q, %q", tc.args, cmd, rest, tc.cmd, tc.rest)
		}
	}
}

// Every flag isCommandFlag accepts must be a command dispatch runs: one it
// lacks prints the usage and quits 2, as an unknown command does, which is
// what -help did before. The command prints its usage or version to the
// test's own output.
func TestCommandFlagIsDispatched(t *testing.T) {
	for _, flag := range []string{"version", "--version", "-v", "help", "--help", "-h", "-help"} {
		if err := dispatch(flag, nil); err != nil {
			t.Errorf("dispatch(%q) = %v; want the usage or the version, not an unknown command", flag, err)
		}
	}
}

// TestExitCode checks how a command ends: 0 when it did what it was asked,
// which includes a command line that asked for the usage, 2 for an unknown
// command and a usage error, 1 for another failure.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"done", nil, 0},
		{"help", flag.ErrHelp, 0},
		{"unknown command", errUnknownCommand, 2},
		{"usage error", &exitError{code: exitTaskError}, exitTaskError},
		{"failure", errors.New("boom"), 1},
	} {
		if got := exitCode(tc.err); got != tc.want {
			t.Errorf("%s: exitCode(%v) = %d; want %d", tc.name, tc.err, got, tc.want)
		}
	}
}

// TestHelpIsNotAUsageError checks that every command's -h, --help and help
// print its usage and end it successfully, as `pinky -help` does, while a
// command line Pinky cannot use still ends it with exitTaskError, which is
// what -h did for every command before. The flags end a command before it
// starts its work, so this never starts a gateway or an update.
func TestHelpIsNotAUsageError(t *testing.T) {
	for _, cmd := range []string{"quit", "devices", "update", "token", "daemon", "mcp"} {
		for _, arg := range []string{"-h", "--help"} {
			if code := exitCode(dispatch(cmd, []string{arg})); code != 0 {
				t.Errorf("%s %s: exit %d; want 0", cmd, arg, code)
			}
		}
	}
	for _, cmd := range []string{"task", "helper"} {
		for _, arg := range []string{"help", "-h", "--help"} {
			if code := exitCode(dispatch(cmd, []string{arg})); code != 0 {
				t.Errorf("%s %s: exit %d; want 0", cmd, arg, code)
			}
		}
	}
	for _, tc := range []struct {
		cmd  string
		args []string
	}{
		{"task", nil},
		{"task", []string{"bogus"}},
		{"task", []string{"status"}},
		{"task", []string{"run"}},
	} {
		if code := exitCode(dispatch(tc.cmd, tc.args)); code != exitTaskError {
			t.Errorf("%s %q: exit %d; want %d", tc.cmd, tc.args, code, exitTaskError)
		}
	}
}
