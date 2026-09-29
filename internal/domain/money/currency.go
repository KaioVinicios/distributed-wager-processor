package money

import "fmt"

// Currency is a supported ISO 4217 code. Every supported currency has two
// decimal places.
type Currency string

const (
	BRL Currency = "BRL"
	USD Currency = "USD"
	EUR Currency = "EUR"
)

// ParseCurrency accepts only the exact uppercase code of a supported currency.
func ParseCurrency(s string) (Currency, error) {
	c := Currency(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: must be BRL, USD or EUR", ErrInvalidCurrency)
	}
	return c, nil
}

// Valid reports whether c is supported. The zero value is not.
func (c Currency) Valid() bool {
	switch c {
	case BRL, USD, EUR:
		return true
	}
	return false
}
