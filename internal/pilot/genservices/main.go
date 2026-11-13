// Command genservices writes pilot.ServicesJSON into the phone apps, which
// read the services Settings offers from it: `go generate ./internal/pilot`.
// The pilot package's tests fail when a file is out of date.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/DIYgod/pinky/internal/pilot"
)

func main() {
	// go generate runs in internal/pilot.
	root := filepath.Join("..", "..")
	for _, f := range pilot.ServicesFiles {
		path := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, pilot.ServicesJSON(), 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "genservices:", err)
	os.Exit(1)
}
