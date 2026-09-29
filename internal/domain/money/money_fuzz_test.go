package money_test

import (
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

func FuzzParseMoney(f *testing.F) {
	// Covers: TST-U01, MON-05, DOM-05
	for _, s := range []string{
		"0.00", "25.00", "92233720368547758.07", "92233720368547758.08",
		"", "25", "-1.00", "1e3", "NaN", "01.00", "1.001",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := money.Parse(s, "BRL")
		if err != nil {
			if !errors.Is(err, money.ErrInvalidAmount) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidAmount", s, err)
			}
			return
		}
		if m.Sign() < 0 {
			t.Fatalf("Parse(%q) accepted a negative amount", s)
		}
		if got := m.String(); got != s {
			t.Fatalf("Parse(%q).String() = %q: accepted input must round-trip unchanged", s, got)
		}
	})
}
