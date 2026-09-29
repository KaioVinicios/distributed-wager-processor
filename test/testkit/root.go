package testkit

import (
	"os"
	"path/filepath"
	"testing"
)

// RepoRoot walks up from the working directory to the directory holding go.mod.
func RepoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			tb.Fatal("go.mod not found above the working directory")
		}
		dir = parent
	}
}
