package wagering

import (
	"math/rand/v2"
	"time"
)

// maxRetryDelay bounds the policy so that jitter arithmetic never overflows.
const maxRetryDelay = 24 * time.Hour

// ReferenceRetryPolicy is the schedule of pending references (D-11): backoff
// min(base·2ⁿ, maxDelay) ± 20% jitter, limited by attempts and by a TTL from
// the creation of the operation. Only integer arithmetic is used.
type ReferenceRetryPolicy struct {
	base        time.Duration
	maxDelay    time.Duration
	maxAttempts int
	ttl         time.Duration
	randN       func(int64) int64
}

// NewReferenceRetryPolicy validates the parameters: 0 < base <= maxDelay <=
// 24h, maxAttempts >= 1 and ttl > 0. randN(n) must return a value in [0, n);
// nil means math/rand/v2.Int64N, which is safe across goroutines. Tests inject
// a deterministic randN.
func NewReferenceRetryPolicy(base, maxDelay time.Duration, maxAttempts int, ttl time.Duration, randN func(int64) int64) (ReferenceRetryPolicy, error) {
	if base <= 0 || maxDelay < base || maxDelay > maxRetryDelay || maxAttempts < 1 || ttl <= 0 {
		return ReferenceRetryPolicy{}, ErrInvalidPolicy
	}
	if randN == nil {
		randN = rand.Int64N
	}
	return ReferenceRetryPolicy{base: base, maxDelay: maxDelay, maxAttempts: maxAttempts, ttl: ttl, randN: randN}, nil
}

// Delay is the wait before attempt n+1: min(base·2ⁿ, maxDelay) ± 20%.
func (p ReferenceRetryPolicy) Delay(n int) time.Duration {
	d := p.base
	for range n {
		if d >= p.maxDelay-d { // 2d >= maxDelay, checked without overflow
			d = p.maxDelay
			break
		}
		d *= 2
	}
	j := int64(d / 5)
	if j == 0 || p.randN == nil {
		return d
	}
	return d + time.Duration(p.randN(2*j+1)-j)
}

// Exhausted reports whether an operation that has failed attempts times must
// expire: attempts >= maxAttempts or now >= expiresAt, whichever comes first.
func (p ReferenceRetryPolicy) Exhausted(attempts int, expiresAt, now time.Time) bool {
	return attempts >= p.maxAttempts || !now.Before(expiresAt)
}

// TTL is the maximum wait, counted from the creation of the operation.
func (p ReferenceRetryPolicy) TTL() time.Duration { return p.ttl }

func (p ReferenceRetryPolicy) valid() bool { return p.randN != nil }
