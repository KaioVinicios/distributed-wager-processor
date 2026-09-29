package app

import "errors"

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
