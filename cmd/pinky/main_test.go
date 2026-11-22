package main

import (
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
