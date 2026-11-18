package gateway

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCanCreateIn(t *testing.T) {
	dir := t.TempDir()
	if err := canCreateIn(dir); err != nil {
		t.Fatalf("a writable folder: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*")); len(left) != 0 {
		t.Errorf("the probe left %v", left)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return // no read-only folders this way
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	if err := canCreateIn(ro); err == nil {
		t.Error("a read-only folder looks writable")
	}
}
