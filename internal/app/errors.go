package app

import (
	"errors"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Sentinels the persistence adapter wraps in an *apperrors.Error, so the use
// cases react with errors.Is and never see SQLSTATEs or constraint names
// (D-14).
var (
	// ErrNotFound: Get or Lock found no row (KindNotFound).
	ErrNotFound = errors.New("app: not found")
	// ErrWalletAlreadyExists: a wallet for (playerId, currency) exists
	// (KindConflict, code WALLET_ALREADY_EXISTS).
	ErrWalletAlreadyExists = errors.New("app: wallet already exists")
	// ErrIdempotencyRace: another request inserted the same (providerId,
	// idempotencyKey) or (providerId, externalTransactionId) first. Rollback,
	// reread and answer with the replay (D-08). KindTransient, so that an
	// unhandled race is retried into the reread.
	ErrIdempotencyRace = errors.New("app: concurrent insert of the same operation")
	// ErrReversalRace: another reversal of the same reference was PROCESSED
	// first (D-10). Rollback and reprocess into ALREADY_REVERSED. KindTransient.
	ErrReversalRace = errors.New("app: concurrent reversal of the same reference")
	// ErrInboxDuplicate: another consumer recorded the same message first. The
	// message is a duplicate (messaging.md). KindTransient.
	ErrInboxDuplicate = errors.New("app: message already recorded in the inbox")
)

// Codes of lifecycle §5.3 that the use cases assign; the others come from the
// domain (wagering.InputCode) or from the adapters.
const (
	CodeUnknownWallet       = "UNKNOWN_WALLET"
	CodeWalletNotFound      = "WALLET_NOT_FOUND"
	CodeTransactionNotFound = "TRANSACTION_NOT_FOUND"
)

// domainError classifies an error returned by the domain (spec decision 6).
// The domain does no I/O, so what it returns is either invalid input or a
// broken invariant: a *ValidationError is KindInput and a *ConflictError is
// KindConflict, both with their code; an error already classified by an
// adapter keeps its Kind; anything else (overflow, invalid transition,
// corrupted snapshot…) is KindPermanent. Without this, D-05 would treat an
// invariant violation as transient and retry it forever.
func domainError(err error) error {
	if err == nil {
		return nil
	}
	var classified *apperrors.Error
	if errors.As(err, &classified) {
		return err
	}
	var validation *wagering.ValidationError
	if errors.As(err, &validation) {
		return apperrors.New(apperrors.KindInput, string(validation.Code), err)
	}
	var conflict *wagering.ConflictError
	if errors.As(err, &conflict) {
		return apperrors.New(apperrors.KindConflict, string(conflict.Code), err)
	}
	return apperrors.New(apperrors.KindPermanent, "", err)
}

// invalidField reports a malformed field or parameter as a validation error, so
// the edge answers 400 INVALID_FIELD with the field name.
func invalidField(field string) error {
	return domainError(&wagering.ValidationError{Code: wagering.InputInvalidField, Field: field})
}
