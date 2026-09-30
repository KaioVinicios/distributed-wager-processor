package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type transactionRepo struct{ q querier }

var txColumnList = []string{
	"id", "origin", "kind", "status", "wallet_id", "player_id", "amount_minor", "currency",
	"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id",
	"reference_external_transaction_id", "received_via", "reference_transaction_id", "failure_code",
	"result_balance_minor", "attempts", "next_attempt_at", "expires_at", "correlation_id",
	"created_at", "updated_at", "completed_at",
}

var (
	txColumns = strings.Join(txColumnList, ", ")
	// txSelect also reads the wallet's currency: result_balance_minor is a
	// wallet balance, and in a CURRENCY_MISMATCH rejection its currency differs
	// from the operation's.
	txSelect = `SELECT t.` + strings.Join(txColumnList, ", t.") + `, w.currency`
	txFrom   = ` FROM wager_transactions t JOIN wallets w ON w.id = t.wallet_id`
)

func (r transactionRepo) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx, `INSERT INTO wager_transactions (`+txColumns+`) VALUES
		($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID,
		s.Money.Minor(), string(s.Money.Currency()),
		nullableText(s.ProviderID), nullableText(s.ExternalTransactionID), nullableText(s.IdempotencyKey),
		nullableText(s.PayloadHash), nullableText(s.RoundID), nullableText(s.GameID),
		nullableText(s.ReferenceExternalTransactionID), nullableText(string(s.ReceivedVia)),
		nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.CorrelationID, s.CreatedAt, s.UpdatedAt, nullableTime(s.CompletedAt))
	return translate(err)
}

// Update writes only the state columns; the wager_tx_guard trigger refuses
// terminal rows and immutable columns (PDA02).
func (r transactionRepo) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions SET
		status = $2, reference_transaction_id = $3, failure_code = $4, result_balance_minor = $5,
		attempts = $6, next_attempt_at = $7, expires_at = $8, updated_at = $9, completed_at = $10
		WHERE id = $1`,
		s.ID, string(s.Status), nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.UpdatedAt, nullableTime(s.CompletedAt))
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}

func (r transactionRepo) Get(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

func (r transactionRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error) {
	return r.find(ctx, txSelect+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.idempotency_key = $2`, providerID, key)
}

func (r transactionRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	return r.find(ctx, txSelect+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.external_transaction_id = $2`, providerID, externalID)
}

// find returns nil, nil when no row matches.
func (r transactionRepo) find(ctx context.Context, sql string, args ...any) (*wagering.WagerTransaction, error) {
	t, err := scanTransaction(r.q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// FindReference reads the reference and whether it already has a PROCESSED
// REFUND or ROLLBACK (R7, D-10) in one statement, under the caller's wallet lock.
func (r transactionRepo) FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error) {
	var reversed bool
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+`,
		EXISTS (SELECT 1 FROM wager_transactions c
		        WHERE c.reference_transaction_id = t.id
		          AND c.kind IN ('REFUND','ROLLBACK') AND c.status = 'PROCESSED')`+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.external_transaction_id = $2`,
		providerID, referenceExternalID), &reversed)
	if errors.Is(err, pgx.ErrNoRows) {
		return wagering.Reference{}, nil
	}
	if err != nil {
		return wagering.Reference{}, err
	}
	return wagering.Reference{Tx: t, AlreadyReversed: reversed}, nil
}

// AdvanceDependents keeps updated_at >= created_at even when this instance's
// clock lags behind the one that created the pending operation (M1 spec §2,
// decision 10).
func (r transactionRepo) AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions
		SET next_attempt_at = $3, updated_at = GREATEST($3, created_at)
		WHERE status = 'PENDING_REFERENCE' AND provider_id = $1 AND reference_external_transaction_id = $2`,
		providerID, externalID, now.UTC().Truncate(time.Microsecond))
	if err != nil {
		return 0, translate(err)
	}
	return tag.RowsAffected(), nil
}

// ClaimDue lists the due PENDING_REFERENCE operations in one statement,
// skipping the rows another transaction holds (D-11, spec M6 decision 2). The
// instant is truncated to the microsecond, as AdvanceDependents writes it.
func (r transactionRepo) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.PendingReference, error) {
	rows, err := r.q.Query(ctx, `SELECT id, wallet_id FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1
		ORDER BY next_attempt_at
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, now.UTC().Truncate(time.Microsecond), limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []app.PendingReference
	for rows.Next() {
		var ref app.PendingReference
		if err := rows.Scan(&ref.ID, &ref.WalletID); err != nil {
			return nil, translate(err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// Lock reads the operation FOR UPDATE, after the caller locked its wallet. OF t
// keeps the join with the wallet from locking a second row.
func (r transactionRepo) Lock(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1 FOR UPDATE OF t`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

// CountPendingReferences feeds the reference_pending_transactions gauge.
func (r transactionRepo) CountPendingReferences(ctx context.Context) (int, error) {
	var n int
	if err := r.q.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`).Scan(&n); err != nil {
		return 0, translate(err)
	}
	return n, nil
}

// scanTransaction reads the txSelect columns of one row, plus extra
// destinations, and rehydrates it. pgx.ErrNoRows passes untouched for the
// callers to map.
func scanTransaction(row pgx.Row, extra ...any) (*wagering.WagerTransaction, error) {
	var (
		s                                                           wagering.Snapshot
		origin, kind, status, currency, walletCurrency              string
		amount                                                      int64
		provider, external, key, hash, round, game, ref, via, refTx *string
		failure                                                     *string
		result                                                      *int64
		next, expires, completed                                    *time.Time
	)
	dest := append([]any{
		&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &amount, &currency,
		&provider, &external, &key, &hash, &round, &game, &ref, &via, &refTx, &failure,
		&result, &s.Attempts, &next, &expires, &s.CorrelationID, &s.CreatedAt, &s.UpdatedAt, &completed,
		&walletCurrency,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, translate(err)
	}
	s.Origin, s.Kind, s.Status = wagering.Origin(origin), wagering.Kind(kind), wagering.Status(status)
	s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey = text(provider), text(external), text(key)
	s.PayloadHash, s.RoundID, s.GameID = text(hash), text(round), text(game)
	s.ReferenceExternalTransactionID, s.ReceivedVia = text(ref), wagering.ReceivedVia(text(via))
	s.ReferenceTransactionID, s.FailureCode = text(refTx), wagering.FailureCode(text(failure))
	s.NextAttemptAt, s.ExpiresAt, s.CompletedAt = instant(next), instant(expires), instant(completed)
	var err error
	if s.Money, err = toMoney(amount, currency); err != nil {
		return nil, corrupted(err)
	}
	if s.ResultBalance, err = optionalMoney(result, walletCurrency); err != nil {
		return nil, corrupted(err)
	}
	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, corrupted(err)
	}
	return t, nil
}
