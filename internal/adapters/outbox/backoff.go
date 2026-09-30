// Package outbox publishes the transactional outbox to the SNS FIFO topic
// (D-13): claim with a lease, publish outside any transaction, confirm only
// where the lease is still ours.
package outbox

import (
	"time"
	"unicode/utf8"
)

// maxErrorBytes bounds last_error (data-model §3.5).
const maxErrorBytes = 1024

// retryDelay is min(base × 2^attempts, ceiling) (spec M4, decision 6):
// attempts is the count stored before this failure, so the first failure
// waits base. The doubling stops at the ceiling, so it never overflows.
func retryDelay(attempts int, base, ceiling time.Duration) time.Duration {
	d := base
	for i := 0; i < attempts && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

// truncateError bounds last_error to maxErrorBytes without splitting a
// UTF-8 sequence (spec M4, decision 15).
func truncateError(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	cut := maxErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
