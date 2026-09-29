package wallet_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

const (
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	txID     = "0192f298-345e-7e38-af88-e43f851a819d"
	entryID  = "0192f299-0000-7000-8000-000000000003"
)

// t0 has nanoseconds and a non-UTC zone on purpose: stored instants must come
// out in UTC, truncated to microseconds.
var t0 = time.Date(2026, 9, 29, 9, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

func minor(t *testing.T, v int64, c money.Currency) money.Money {
	t.Helper()
	m, err := money.FromMinor(v, c)
	if err != nil {
		t.Fatalf("FromMinor(%d): %v", v, err)
	}
	return m
}
