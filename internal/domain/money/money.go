// Package money implements Money: an exact amount in minor units (cents) of a
// supported currency. Floating point is never used (CHALLENGE §5.1, D-03).
//
// Limits: from -92233720368547758.08 to 92233720368547758.07. External input
// (Parse, UnmarshalJSON) only accepts 0.00 up to the positive maximum.
package money

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
)

// scale is the number of minor units in one major unit (two decimals).
const scale = 100

// Money is an immutable amount in minor units of a currency. The zero value is
// uninitialized and rejected by every operation that returns an error.
//
// Money deliberately has no IsZero method: encoding/json's omitzero would use
// it and drop legitimate "0.00" values. Use Sign instead.
type Money struct {
	minor    int64
	currency Currency
}

// Parse builds Money from external input. amount must match
// ^(0|[1-9][0-9]*)\.[0-9]{2}$ (no sign, no exponent, exactly two decimals);
// nothing is normalized or rounded. The amount is checked before the currency.
func Parse(amount, currency string) (Money, error) {
	minor, err := parseAmount(amount)
	if err != nil {
		return Money{}, err
	}
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

func parseAmount(s string) (int64, error) {
	n := len(s)
	if n < 4 || s[n-3] != '.' {
		return 0, fmt.Errorf("%w: want digits, a dot and two decimals", ErrInvalidAmount)
	}
	whole, frac := s[:n-3], s[n-2:]
	if !allDigits(whole) || !allDigits(frac) {
		return 0, fmt.Errorf("%w: only digits are allowed", ErrInvalidAmount)
	}
	if len(whole) > 1 && whole[0] == '0' {
		return 0, fmt.Errorf("%w: leading zero", ErrInvalidAmount)
	}
	var v int64
	for _, c := range []byte(whole) {
		d := int64(c - '0')
		if v > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("%w: %w", ErrInvalidAmount, ErrOverflow)
		}
		v = v*10 + d
	}
	cents := int64(frac[0]-'0')*10 + int64(frac[1]-'0')
	if v > (math.MaxInt64-cents)/scale {
		return 0, fmt.Errorf("%w: %w", ErrInvalidAmount, ErrOverflow)
	}
	return v*scale + cents, nil
}

func allDigits(s string) bool {
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// FromMinor builds Money from minor units, for persistence and internal
// calculations. Negative values are allowed (e.g. a reconciliation difference);
// callers enforce their own sign rules.
func FromMinor(minor int64, c Currency) (Money, error) {
	if !c.Valid() {
		return Money{}, fmt.Errorf("%w: must be BRL, USD or EUR", ErrInvalidCurrency)
	}
	return Money{minor: minor, currency: c}, nil
}

// Zero returns zero in currency c.
func Zero(c Currency) (Money, error) { return FromMinor(0, c) }

// Minor returns the amount in minor units.
func (m Money) Minor() int64 { return m.minor }

// Currency returns the currency; "" for the zero value.
func (m Money) Currency() Currency { return m.currency }

// Sign returns -1, 0 or +1. The zero value reports 0.
func (m Money) Sign() int { return cmp.Compare(m.minor, 0) }

// String formats the amount with two decimals and no currency ("25.00",
// "-5.00"). The zero value formats as "".
func (m Money) String() string {
	if !m.currency.Valid() {
		return ""
	}
	v := m.minor
	if v == math.MinInt64 {
		return "-92233720368547758.08"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	cents := strconv.FormatInt(v%scale, 10)
	if len(cents) == 1 {
		cents = "0" + cents
	}
	return sign + strconv.FormatInt(v/scale, 10) + "." + cents
}

// Add returns m + o.
func (m Money) Add(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor > 0 && m.minor > math.MaxInt64-o.minor) || (o.minor < 0 && m.minor < math.MinInt64-o.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: m.minor + o.minor, currency: m.currency}, nil
}

// Sub returns m - o.
func (m Money) Sub(o Money) (Money, error) {
	if err := m.compatible(o); err != nil {
		return Money{}, err
	}
	if (o.minor < 0 && m.minor > math.MaxInt64+o.minor) || (o.minor > 0 && m.minor < math.MinInt64+o.minor) {
		return Money{}, ErrOverflow
	}
	return Money{minor: m.minor - o.minor, currency: m.currency}, nil
}

// Negate returns -m. Negating the minimum int64 overflows.
func (m Money) Negate() (Money, error) {
	if !m.currency.Valid() {
		return Money{}, ErrUninitialized
	}
	if m.minor == math.MinInt64 {
		return Money{}, ErrOverflow
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Cmp returns -1, 0 or +1 comparing m with o.
func (m Money) Cmp(o Money) (int, error) {
	if err := m.compatible(o); err != nil {
		return 0, err
	}
	return cmp.Compare(m.minor, o.minor), nil
}

func (m Money) compatible(o Money) error {
	if !m.currency.Valid() || !o.currency.Valid() {
		return ErrUninitialized
	}
	if m.currency != o.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	return nil
}
