package observability_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/observability"
)

type fakeChecker struct {
	name      string
	err       error
	delay     time.Duration
	ignoreCtx bool // simulates a dependency stuck in I/O that ignores cancellation
}

func (f fakeChecker) Name() string { return f.name }

func (f fakeChecker) Check(ctx context.Context) error {
	if f.delay > 0 {
		if f.ignoreCtx {
			time.Sleep(f.delay)
			return f.err
		}
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func ready(t *testing.T, timeout time.Duration, cs ...observability.Checker) (observability.Report, time.Duration) {
	t.Helper()
	h := observability.NewHealth(discard(), cs, timeout)
	start := time.Now()
	r := h.Ready(t.Context())
	return r, time.Since(start)
}

// Covers: HTTP-08, OBS-04
func TestHealthReady_AllUp(t *testing.T) {
	r, _ := ready(t, time.Second, fakeChecker{name: "postgres"}, fakeChecker{name: "sqs"})
	if r.Status != observability.StatusUp || r.Checks["postgres"] != observability.StatusUp || r.Checks["sqs"] != observability.StatusUp {
		t.Fatalf("Ready() = %+v, want all UP", r)
	}
}

// Covers: HTTP-08, OBS-04
func TestHealthReady_OneDownMakesReportDown(t *testing.T) {
	r, _ := ready(t, time.Second, fakeChecker{name: "postgres", err: errors.New("refused")}, fakeChecker{name: "sqs"})
	if r.Status != observability.StatusDown || r.Checks["postgres"] != observability.StatusDown || r.Checks["sqs"] != observability.StatusUp {
		t.Fatalf("Ready() = %+v, want DOWN with postgres DOWN and sqs UP", r)
	}
}

// Covers: HTTP-08
func TestHealthReady_SlowCheckerTimesOut(t *testing.T) {
	r, elapsed := ready(t, 50*time.Millisecond, fakeChecker{name: "sqs", delay: 5 * time.Second})
	if r.Checks["sqs"] != observability.StatusDown {
		t.Fatalf("Ready() = %+v, want sqs DOWN", r)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Ready() took %v, want about the 50ms timeout", elapsed)
	}
}

// Covers: HTTP-08 (review focus: checker stuck in I/O ignoring ctx)
func TestHealthReady_CheckerIgnoringContextDoesNotHang(t *testing.T) {
	r, elapsed := ready(t, 50*time.Millisecond, fakeChecker{name: "postgres", delay: 400 * time.Millisecond, ignoreCtx: true})
	if r.Status != observability.StatusDown {
		t.Fatalf("Ready() = %+v, want DOWN", r)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Ready() took %v, want about the 50ms timeout", elapsed)
	}
}

// Covers: HTTP-08
func TestHealthReady_RunsCheckersConcurrently(t *testing.T) {
	cs := []observability.Checker{
		fakeChecker{name: "a", delay: 100 * time.Millisecond},
		fakeChecker{name: "b", delay: 100 * time.Millisecond},
		fakeChecker{name: "c", delay: 100 * time.Millisecond},
	}
	r, elapsed := ready(t, time.Second, cs...)
	if r.Status != observability.StatusUp {
		t.Fatalf("Ready() = %+v, want UP", r)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("Ready() took %v; checkers must run concurrently (sequential would be ~300ms)", elapsed)
	}
}

// Covers: HTTP-08
func TestHealthReady_NoCheckersIsUp(t *testing.T) {
	if r, _ := ready(t, time.Second); r.Status != observability.StatusUp {
		t.Fatalf("Ready() = %+v, want UP", r)
	}
}

// Covers: OBS-04
func TestHealthReady_LogsFailureReason(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	h := observability.NewHealth(log, []observability.Checker{fakeChecker{name: "postgres", err: errors.New("dial tcp: connection refused")}}, time.Second)
	h.Ready(t.Context())

	if out := buf.String(); !strings.Contains(out, "connection refused") || !strings.Contains(out, `"check":"postgres"`) {
		t.Fatalf("log = %q, want the checker name and the failure reason", out)
	}
}
