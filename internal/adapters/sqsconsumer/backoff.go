package sqsconsumer

import "time"

// maxBackoffExponent keeps 2^n seconds far from overflowing a Duration.
const maxBackoffExponent = 30

// retryDelay is the visibility of a message that failed transiently:
// min(2^receiveCount s, ceiling) (messaging.md §4.1). A missing receive count
// counts as the first receive.
func retryDelay(receiveCount int, ceiling time.Duration) time.Duration {
	n := min(max(receiveCount, 1), maxBackoffExponent)
	return min(time.Duration(1<<n)*time.Second, ceiling)
}
