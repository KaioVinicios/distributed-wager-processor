package money

import "errors"

var (
	// ErrInvalidAmount reports an amount outside the strict external format.
	ErrInvalidAmount = errors.New("money: invalid amount")
	// ErrInvalidCurrency reports a currency that is not supported.
	ErrInvalidCurrency = errors.New("money: invalid currency")
	// ErrOverflow reports a result outside the int64 range of minor units.
	ErrOverflow = errors.New("money: int64 overflow")
	// ErrCurrencyMismatch reports arithmetic or comparison across currencies.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")
	// ErrUninitialized reports the use of the Money zero value.
	ErrUninitialized = errors.New("money: uninitialized value")
)
