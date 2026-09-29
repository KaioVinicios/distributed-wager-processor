package apperrors

import "errors"

// Classify returns the Kind of err:
//  1. nil → "";
//  2. the first *Error of the %w chain → its Kind;
//  3. anything else → KindTransient. That covers context.Canceled and
//     context.DeadlineExceeded and, by decision D-05, any error no layer
//     classified: nothing is persisted, HTTP answers 503 and SQS retries until
//     the redrive, instead of recording a definitive FAILED for an unknown bug.
func Classify(err error) Kind {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return KindTransient
}

// CodeOf returns the Code of the first *Error of the chain, or "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
