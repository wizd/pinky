package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStrayWord: a word on the command line of a command that takes flags
// alone is a usage error, 2, before the command does anything. The flag
// package stops reading flags at the first word, so before this the flags
// after it were dropped: `pinky update now --check` installed the update
// it was asked only to look for, and `pinky mcp stdio --fake` started a
// gateway with the real phones.
func TestStrayWord(t *testing.T) {
	// A Pinky home under a file, which no command can make: should a
	// command get past the check, it fails at once. Else `pinky daemon`
	// would start a gateway on the real phones, as --fake is dropped, and
	// `pinky mcp` would start this test binary in the background as the
	// gateway (os.Executable).
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PINKY_HOME", filepath.Join(file, "h"))
	for _, c := range []struct {
		name string
		run  func([]string) error
		args []string
	}{
		{"update", runUpdate, []string{"now", "--check"}},
		{"mcp", runMCP, []string{"stdio", "--fake"}},
		{"daemon", runDaemon, []string{"start", "--fake"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := exitCode(c.run(c.args)); got != 2 {
				t.Errorf("pinky %s %q exits %d, want 2", c.name, c.args, got)
			}
		})
	}
}
