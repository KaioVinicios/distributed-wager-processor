package wagering

// CheckIdempotency applies D-08 to the transactions found by (providerId,
// idempotencyKey) and by (providerId, externalTransactionId):
//   - same key and same hash: replay of byKey;
//   - same key and another hash: IDEMPOTENCY_KEY_REUSED;
//   - another key but the same externalTransactionId: EXTERNAL_TRANSACTION_ID_CONFLICT;
//   - nothing found: (nil, nil), the operation proceeds.
func CheckIdempotency(hash string, byKey, byExternalID *WagerTransaction) (*WagerTransaction, error) {
	if byKey != nil {
		if byKey.payloadHash == hash {
			return byKey, nil
		}
		return nil, &ConflictError{Code: InputIdempotencyKeyReused}
	}
	if byExternalID != nil {
		return nil, &ConflictError{Code: InputExternalTransactionIDConflict}
	}
	return nil, nil
}
