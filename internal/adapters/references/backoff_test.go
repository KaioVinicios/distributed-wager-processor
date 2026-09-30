package references

import (
	"testing"
	"time"
)

// Covers: OPS-12 (spec M6, decision 11; messaging.md §5.3)
func TestNextClaimDelay(t *testing.T) {
	for _, tc := range []struct{ prev, want time.Duration }{
		{0, time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second},
	} {
		if got := nextClaimDelay(tc.prev); got != tc.want {
			t.Errorf("nextClaimDelay(%v) = %v, want %v", tc.prev, got, tc.want)
		}
	}
}
