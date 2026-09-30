package references

import "time"

const (
	claimRetryMin = time.Second
	claimRetryMax = 30 * time.Second
)

// nextClaimDelay is the wait after one more failed claim, the database being
// unavailable: 1 s, 2 s, 4 s… up to 30 s (spec M6, decision 11). prev is 0
// after a successful claim.
func nextClaimDelay(prev time.Duration) time.Duration {
	return min(max(2*prev, claimRetryMin), claimRetryMax)
}
