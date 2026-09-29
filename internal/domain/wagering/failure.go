package wagering

import "fmt"

// FailureCategory tells the provider what to do with a failure (lifecycle §5).
type FailureCategory string

const (
	CategoryCorrectable FailureCategory = "CORRECTABLE"
	CategoryDefinitive  FailureCategory = "DEFINITIVE"
	// CategoryTransient is only used by the edges (503).
	CategoryTransient FailureCategory = "TRANSIENT"
)

// FailureCode is a persisted failure: the rejections of lifecycle §5.1 and the
// permanent failure of §5.2. Codes are part of the contract and never renamed.
type FailureCode string

const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureAlreadyReversed           FailureCode = "ALREADY_REVERSED"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailurePlayerWalletMismatch      FailureCode = "PLAYER_WALLET_MISMATCH"
	FailureCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReversalAmountMismatch    FailureCode = "REVERSAL_AMOUNT_MISMATCH"
	FailureInvalidReferenceKind      FailureCode = "INVALID_REFERENCE_KIND"
	FailureInternalPermanentFailure  FailureCode = "INTERNAL_PERMANENT_FAILURE"
)

// ParseFailureCode accepts the exact name of a code (rehydration).
func ParseFailureCode(s string) (FailureCode, error) {
	c := FailureCode(s)
	if !c.Valid() {
		return "", fmt.Errorf("%w: failure code", ErrInvalidEnum)
	}
	return c, nil
}

// Category returns the category of lifecycle §5.1–§5.2, or "" for an unknown code.
func (c FailureCode) Category() FailureCategory {
	switch c {
	case FailureInsufficientFunds, FailureReversalInsufficientFunds, FailureAlreadyReversed,
		FailureReferenceNotFound, FailureReferenceNotProcessed, FailureInternalPermanentFailure:
		return CategoryDefinitive
	case FailurePlayerWalletMismatch, FailureCurrencyMismatch, FailureReferenceMismatch,
		FailureReversalAmountMismatch, FailureInvalidReferenceKind:
		return CategoryCorrectable
	}
	return ""
}

// Valid reports whether c is a known code. The zero value is not.
func (c FailureCode) Valid() bool { return c.Category() != "" }

// IsRejection reports whether c is a business rejection (§5.1), that is, any
// known code except INTERNAL_PERMANENT_FAILURE.
func (c FailureCode) IsRejection() bool {
	return c.Valid() && c != FailureInternalPermanentFailure
}
