package postgres

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Money is stored as (amount BIGINT in minor units, currency CHAR(3)) (D-03).
// Absent values travel as NULL and come back as the zero values the domain
// snapshots use.

func toMoney(minor int64, currency string) (money.Money, error) {
	return money.FromMinor(minor, money.Currency(currency))
}

// nullableMinor is NULL for an absent Money (the zero value).
func nullableMinor(m money.Money) *int64 {
	if !m.Currency().Valid() {
		return nil
	}
	v := m.Minor()
	return &v
}

// optionalMoney rebuilds a nullable amount in the row's currency.
func optionalMoney(minor *int64, currency string) (money.Money, error) {
	if minor == nil {
		return money.Money{}, nil
	}
	return toMoney(*minor, currency)
}

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// instant returns a stored instant in UTC; NULL is the zero time.
func instant(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
