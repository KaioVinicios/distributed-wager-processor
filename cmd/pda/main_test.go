package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runMainEnv makes the test binary run main() instead of the tests, so a test
// observes what the real process prints and how it exits.
const runMainEnv = "PDA_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Covers: FX-02, D-15 (U24; review of M7, finding 1)
// Sensitivity: fx.NopLogger back in the error branch of bootstrap.Options → the output is empty.
func TestMainReportsInvalidRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "HTTP_ENABLED=talvez-42")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want code 1 (output %q)", err, out)
	}
	if !strings.Contains(string(out), "HTTP_ENABLED") || strings.Contains(string(out), "talvez-42") {
		t.Fatalf("output = %q, want HTTP_ENABLED named and its value absent", out)
	}
}
