package wagering

import "fmt"

// Status is the state of a transaction (lifecycle §1). PENDING only exists in
// memory (D-05).
type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

// ParseStatus accepts the exact name of a status.
func ParseStatus(s string) (Status, error) {
	st := Status(s)
	if !st.Valid() {
		return "", fmt.Errorf("%w: status", ErrInvalidEnum)
	}
	return st, nil
}

// Valid reports whether s is a known status. The zero value is not.
func (s Status) Valid() bool {
	switch s {
	case StatusPending, StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed:
		return true
	}
	return false
}

// IsTerminal reports whether s accepts no further transition (TX-07).
func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}
