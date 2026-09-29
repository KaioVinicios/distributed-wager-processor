package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type ledgerRepo struct{ q querier }

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

var errInvalidLimit = errors.New("postgres: ledger page limit must be positive")

// Insert appends an entry. The ledger_matches_wallet trigger requires the
// transaction to be written in its final state and the wallet updated first.
func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	if e.ID() == "" {
		return invalidValue(fmt.Errorf("%w: zero value", wallet.ErrInvalidLedgerEntry))
	}
	_, err := r.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Minor(),
		string(e.Amount().Currency()), e.BalanceBefore().Minor(), e.BalanceAfter().Minor(),
		e.WalletVersion(), e.CreatedAt())
	return translate(err)
}

// List pages by wallet_version, the stable order of the ledger (D-16).
func (r ledgerRepo) List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	if limit < 1 {
		return nil, invalidValue(errInvalidLimit)
	}
	rows, err := r.q.Query(ctx, `SELECT `+ledgerColumns+` FROM wallet_ledger_entries
		WHERE wallet_id = $1 AND wallet_version > $2 ORDER BY wallet_version LIMIT $3`,
		walletID, afterVersion, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []wallet.LedgerEntry
	for rows.Next() {
		var (
			p                     wallet.LedgerEntryParams
			direction, currency   string
			amount, before, after int64
		)
		if err := rows.Scan(&p.ID, &p.WalletID, &p.TransactionID, &direction, &amount, &currency,
			&before, &after, &p.WalletVersion, &p.CreatedAt); err != nil {
			return nil, translate(err)
		}
		e, err := ledgerEntry(p, direction, currency, amount, before, after)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func ledgerEntry(p wallet.LedgerEntryParams, direction, currency string, amount, before, after int64) (wallet.LedgerEntry, error) {
	var err error
	p.Direction = wallet.Direction(direction)
	if p.Amount, err = toMoney(amount, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	if p.BalanceBefore, err = toMoney(before, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	if p.BalanceAfter, err = toMoney(after, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	e, err := wallet.NewLedgerEntry(p)
	if err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	return e, nil
}

// Sum rebuilds the balance from the ledger (D-16). SUM(bigint) is numeric in
// PostgreSQL; the cast back to bigint raises 22003 (permanent) on overflow.
func (r ledgerRepo) Sum(ctx context.Context, walletID string) (app.LedgerSum, error) {
	var s app.LedgerSum
	err := r.q.QueryRow(ctx, `SELECT
		COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint,
		COUNT(*)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&s.NetMinor, &s.Entries)
	if err != nil {
		return app.LedgerSum{}, translate(err)
	}
	return s, nil
}
