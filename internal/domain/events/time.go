package events

import (
	"fmt"
	"time"
)

// layout is RFC 3339 in UTC with exactly three decimals (messaging.md §6.1).
const layout = "2006-01-02T15:04:05.000Z"

// Time is an instant serialized as UTC RFC 3339 with milliseconds (OUT-12).
type Time struct{ time.Time }

// NewTime wraps t in UTC.
func NewTime(t time.Time) Time { return Time{t.UTC()} }

// MarshalJSON emits "2006-01-02T15:04:05.000Z".
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("%w: zero instant", ErrInvalidEvent)
	}
	return []byte(`"` + t.UTC().Format(layout) + `"`), nil
}
