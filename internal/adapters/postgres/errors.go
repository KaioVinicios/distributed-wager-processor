package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// dbError is what a PostgreSQL error becomes when it leaves the adapter: the
// SQLSTATE and the constraint name only. Message, Detail, Where and Hint are
// dropped on purpose: Detail carries the whole row ("Failing row contains
// (…)"), a full financial payload that would end up in the logs (CHALLENGE §12).
type dbError struct {
	code       string
	constraint string
}

func (e *dbError) Error() string {
	if e.constraint == "" {
		return "postgres: " + e.code
	}
	return "postgres: " + e.code + " " + e.constraint
}

// sentinel is how a unique violation the use cases handle is reported.
type sentinel struct {
	kind apperrors.Kind
	code string
	err  error
}

// uniqueSentinels maps the constraints whose violation the app handles (D-14).
var uniqueSentinels = map[string]sentinel{
	"wallets_player_currency_uq":  {apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
	"wager_tx_idempotency_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_external_id_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_single_reversal_uq": {apperrors.KindTransient, "", app.ErrReversalRace},
	"inbox_pk":                    {apperrors.KindTransient, "", app.ErrInboxDuplicate},
}

// transientCodes are the SQLSTATEs that a retry can overcome, besides class 08
// (connection exceptions).
var transientCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout, D-09)
	"57P01": true, // admin_shutdown
	"57014": true, // query_canceled
	"53300": true, // too_many_connections
}

// translate classifies a database error (U09b). A *pgconn.PgError never leaves
// the adapter: unique violations the app handles become its sentinels, known
// transient SQLSTATEs become KindTransient, and every other rejection by the
// database (constraints, triggers PDA01–PDA05, 22003, 25006…) is
// KindPermanent, since a retry cannot fix it. Errors that are not PgError
// (context, network) pass untouched: apperrors.Classify treats them as
// transient (D-05).
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	cause := &dbError{code: pgErr.Code, constraint: pgErr.ConstraintName}
	if s, ok := uniqueSentinels[pgErr.ConstraintName]; ok && pgErr.Code == "23505" {
		return apperrors.New(s.kind, s.code, fmt.Errorf("%w: %w", s.err, cause))
	}
	if pgErr.Code == "55P03" || pgErr.Code == "40P01" {
		return apperrors.New(apperrors.KindTransient, "", fmt.Errorf("%w: %w", app.ErrLockTimeout, cause))
	}
	if transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08") {
		return apperrors.New(apperrors.KindTransient, "", cause)
	}
	return apperrors.New(apperrors.KindPermanent, "", cause)
}

// notFound reports a missing row to Get and Lock.
func notFound() error {
	return apperrors.New(apperrors.KindNotFound, "", app.ErrNotFound)
}

// corrupted reports a stored row the domain refuses to rehydrate: a bug or
// tampering, never something a retry fixes.
func corrupted(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: stored row rejected by the domain: %w", err))
}

// invalidValue reports a domain value that cannot be written (zero value,
// PENDING…). Without it, D-05 would turn this programming error into a
// transient one, retried forever.
func invalidValue(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: value rejected before writing: %w", err))
}

// errNoRowUpdated reports an UPDATE that matched no row where one was expected.
var errNoRowUpdated = errors.New("postgres: the row to update was not found in the expected state")

func staleRow() error { return apperrors.New(apperrors.KindPermanent, "", errNoRowUpdated) }
