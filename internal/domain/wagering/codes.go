package wagering

// InputCode is a non-persisted error born in the domain: the stateless
// validation codes and the idempotency conflicts of lifecycle §5.3. All of
// them are CORRECTABLE. The codes that do not come from the domain (wallet,
// auth, transport) live in the app and the edges.
type InputCode string

const (
	InputMissingIdempotencyKey         InputCode = "MISSING_IDEMPOTENCY_KEY"
	InputInvalidIdempotencyKey         InputCode = "INVALID_IDEMPOTENCY_KEY"
	InputMissingField                  InputCode = "MISSING_FIELD"
	InputInvalidField                  InputCode = "INVALID_FIELD"
	InputInvalidAmount                 InputCode = "INVALID_AMOUNT"
	InputInvalidCurrency               InputCode = "INVALID_CURRENCY"
	InputInvalidKind                   InputCode = "INVALID_KIND"
	InputOpeningNotAllowed             InputCode = "OPENING_NOT_ALLOWED"
	InputZeroAmountNotAllowed          InputCode = "ZERO_AMOUNT_NOT_ALLOWED"
	InputLossAmountMustBeZero          InputCode = "LOSS_AMOUNT_MUST_BE_ZERO"
	InputReferenceRequired             InputCode = "REFERENCE_REQUIRED"
	InputReferenceNotAllowed           InputCode = "REFERENCE_NOT_ALLOWED"
	InputSelfReference                 InputCode = "SELF_REFERENCE"
	InputIdempotencyKeyReused          InputCode = "IDEMPOTENCY_KEY_REUSED"
	InputExternalTransactionIDConflict InputCode = "EXTERNAL_TRANSACTION_ID_CONFLICT"
)

// Category is always CORRECTABLE: fix the input and resend.
func (InputCode) Category() FailureCategory { return CategoryCorrectable }
