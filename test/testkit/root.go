package testkit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// RepoRoot walks up from the working directory to the directory holding go.mod.
func RepoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := findRepoRoot()
	if err != nil {
		tb.Fatal(err)
	}
	return dir
}

// findRepoRoot is RepoRoot for callers without a testing.TB, such as TestMain.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("testkit: go.mod not found above the working directory")
		}
		dir = parent
	}
}
