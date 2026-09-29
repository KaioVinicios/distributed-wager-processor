// Package apperrors is the error vocabulary shared by the use cases and the
// adapters. Errors cross layers by classification, not by concrete type: the
// adapters translate infrastructure errors into a Kind, and the edges map a
// Kind to an HTTP status or to a message action (structure.md §2). It is a leaf
// package: it imports only the standard library.
package apperrors

// Kind classifies an error for the edges (transaction-lifecycle.md §8).
type Kind string

const (
	KindInput     Kind = "INPUT"
	KindNotFound  Kind = "NOT_FOUND"
	KindForbidden Kind = "FORBIDDEN"
	KindConflict  Kind = "CONFLICT"
	KindBusiness  Kind = "BUSINESS"
	KindTransient Kind = "TRANSIENT"
	KindPermanent Kind = "PERMANENT"
)

// Error carries a Kind and an optional stable code (lifecycle §5) through %w chains.
type Error struct {
	Kind Kind
	Code string
	Err  error
}

// New returns an *Error.
func New(kind Kind, code string, err error) error {
	return &Error{Kind: kind, Code: code, Err: err}
}

func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }
