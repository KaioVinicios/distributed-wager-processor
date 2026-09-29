package wagering

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidEnum reports an unknown kind, status, origin, channel or code.
	ErrInvalidEnum = errors.New("wagering: invalid enum value")
	// ErrInvalidTransition reports a transition the state machine does not allow
	// (lifecycle §1), including any transition from a terminal state.
	ErrInvalidTransition = errors.New("wagering: invalid state transition")
	// ErrInvalidArgument reports arguments that would break an invariant.
	ErrInvalidArgument = errors.New("wagering: invalid argument")
	// ErrInvalidSnapshot reports persisted state that cannot be rehydrated.
	ErrInvalidSnapshot = errors.New("wagering: invalid snapshot")
	// ErrNotPersistable reports an attempt to persist a PENDING transaction (D-05).
	ErrNotPersistable = errors.New("wagering: PENDING is never persisted")
	// ErrReferenceExpired reports that the reference retry limit is exhausted.
	ErrReferenceExpired = errors.New("wagering: reference retry limit exhausted")
	// ErrInvalidPolicy reports an invalid reference retry policy.
	ErrInvalidPolicy = errors.New("wagering: invalid reference retry policy")
	// ErrUninitialized reports the use of a zero value.
	ErrUninitialized = errors.New("wagering: uninitialized value")
)

// ValidationError is a stateless input error (lifecycle §3.1, HTTP 400). Field
// is the JSON path of the offending field.
type ValidationError struct {
	Code  InputCode
	Field string
}

func (e *ValidationError) Error() string {
	return "wagering: " + string(e.Code) + " (" + e.Field + ")"
}

// ConflictError is an idempotency conflict (D-08, HTTP 409).
type ConflictError struct {
	Code InputCode
}

func (e *ConflictError) Error() string { return "wagering: idempotency conflict: " + string(e.Code) }

// invalidArg wraps ErrInvalidArgument with a reason. Reasons never carry values.
func invalidArg(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, reason) }
