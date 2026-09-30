package testkit

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// composeTimeout bounds one docker compose command.
const composeTimeout = time.Minute

// Pause freezes a service of the compose with docker compose pause, to
// simulate its unavailability (test-plan §3.4, M9): its processes stop, while
// the kernel of the container keeps accepting connections. resume unpauses it
// once; it is also registered in tb.Cleanup. A test that pauses must not run
// in parallel with any other.
func Pause(tb testing.TB, service string) (resume func()) {
	tb.Helper()
	compose(tb, "pause", service)
	var once sync.Once
	resume = func() { once.Do(func() { compose(tb, "unpause", service) }) }
	tb.Cleanup(resume)
	return resume
}

// compose runs docker compose at the root of the repository, the project
// make infra-up started.
func compose(tb testing.TB, args ...string) {
	tb.Helper()
	root, err := findRepoRoot()
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), composeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...) //nolint:gosec // docker with the compose arguments of the test
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
