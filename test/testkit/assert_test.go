package testkit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// recordingTB keeps the failure instead of stopping the test.
type recordingTB struct {
	testing.TB
	failure string
}

func (r *recordingTB) Fatalf(format string, args ...any) { r.failure = fmt.Sprintf(format, args...) }

// Covers: test-plan §1 (Eventually with a deadline)
func TestEventually(t *testing.T) {
	calls := 0
	testkit.Eventually(t, time.Second, "third call", func(context.Context) (bool, error) {
		calls++
		return calls == 3, nil
	})
	if calls != 3 {
		t.Fatalf("cond called %d times, want 3 (polls until it holds)", calls)
	}

	rec := &recordingTB{TB: t}
	start := time.Now()
	testkit.Eventually(rec, 200*time.Millisecond, "never", func(context.Context) (bool, error) { return false, nil })
	if !strings.Contains(rec.failure, "never") || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("after %v: failure %q, want a failure naming the condition after the timeout", time.Since(start), rec.failure)
	}

	rec = &recordingTB{TB: t}
	testkit.Eventually(rec, time.Second, "broken", func(context.Context) (bool, error) { return false, errors.New("boom") })
	if !strings.Contains(rec.failure, "boom") {
		t.Fatalf("failure %q, want the error of the condition", rec.failure)
	}
}
