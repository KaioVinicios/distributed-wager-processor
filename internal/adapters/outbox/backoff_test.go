package outbox

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Covers: OUT-04 (spec M4, decision 6)
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempts      int
		base, ceiling time.Duration
		want          time.Duration
	}{
		{0, time.Second, 5 * time.Minute, time.Second}, // the first failure waits base
		{1, time.Second, 5 * time.Minute, 2 * time.Second},
		{3, time.Second, 5 * time.Minute, 8 * time.Second},
		{8, time.Second, 5 * time.Minute, 256 * time.Second},
		{9, time.Second, 5 * time.Minute, 5 * time.Minute},         // 512 s capped
		{1_000_000, time.Second, 5 * time.Minute, 5 * time.Minute}, // no overflow
		{62, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour},
		{2, 100 * time.Millisecond, time.Second, 400 * time.Millisecond},
		{-1, time.Second, time.Minute, time.Second}, // never below base
	}
	for _, tc := range cases {
		if got := retryDelay(tc.attempts, tc.base, tc.ceiling); got != tc.want {
			t.Errorf("retryDelay(%d, %v, %v) = %v, want %v", tc.attempts, tc.base, tc.ceiling, got, tc.want)
		}
	}
}

// Covers: OUT-04 (spec M4, decision 15; data-model §3.5)
func TestTruncateError(t *testing.T) {
	if got := truncateError("sns: throttled"); got != "sns: throttled" {
		t.Fatalf("short message = %q, want it untouched", got)
	}
	long := strings.Repeat("a", 1023) + "é" + strings.Repeat("b", 10) // é straddles byte 1024
	got := truncateError(long)
	if len(got) > 1024 || !utf8.ValidString(got) || got != strings.Repeat("a", 1023) {
		t.Fatalf("truncateError = %d bytes (valid UTF-8: %v), want the 1023 bytes before the cut rune", len(got), utf8.ValidString(got))
	}
	if got := truncateError(strings.Repeat("x", 5000)); len(got) != 1024 {
		t.Fatalf("truncateError = %d bytes, want 1024", len(got))
	}
}
