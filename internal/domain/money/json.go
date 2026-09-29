package money

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MarshalJSON emits {"amount":"25.00","currency":"BRL"}: the amount is always a
// decimal string, never a JSON number (MON-04).
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.currency.Valid() {
		return nil, ErrUninitialized
	}
	return json.Marshal(struct {
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}{Amount: m.String(), Currency: string(m.currency)})
}

// UnmarshalJSON is the strict external input: both fields are required and
// must be strings, unknown fields are rejected, and the amount follows Parse
// (so negatives are rejected). An outer DisallowUnknownFields does not reach a
// custom unmarshaler, hence the decoder of its own.
func (m *Money) UnmarshalJSON(data []byte) error {
	var wire struct {
		Amount   *string `json:"amount"`
		Currency *string `json:"currency"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	if wire.Amount == nil {
		return fmt.Errorf("%w: amount is required", ErrInvalidAmount)
	}
	if wire.Currency == nil {
		return fmt.Errorf("%w: currency is required", ErrInvalidCurrency)
	}
	v, err := Parse(*wire.Amount, *wire.Currency)
	if err != nil {
		return err
	}
	*m = v
	return nil
}
