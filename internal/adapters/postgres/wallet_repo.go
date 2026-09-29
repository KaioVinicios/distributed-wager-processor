package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type walletRepo struct{ q querier }

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r walletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, string(s.Balance.Currency()), s.Balance.Minor(), s.Version, s.CreatedAt, s.UpdatedAt)
	return translate(err)
}

// Lock takes the wallet's row lock until the end of the transaction (D-09).
// The lock_timeout set by the unit of work bounds the wait.
func (r walletRepo) Lock(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id)
}

func (r walletRepo) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

func (r walletRepo) get(ctx context.Context, sql, id string) (wallet.Wallet, error) {
	if !ident.Valid(id) { // not a canonical UUID: no such row, and no 22P02 from the database
		return wallet.Wallet{}, notFound()
	}
	var s wallet.Snapshot
	var currency string
	var minor int64
	err := r.q.QueryRow(ctx, sql, id).Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, notFound()
	}
	if err != nil {
		return wallet.Wallet{}, translate(err)
	}
	if s.Balance, err = toMoney(minor, currency); err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	return w, nil
}

// UpdateBalance relies on the wallet lock; the version condition is the
// second protection against a lost update (D-09), and the wallet_guard trigger
// the third.
func (r walletRepo) UpdateBalance(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx,
		`UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4 WHERE id = $1 AND version = $3 - 1`,
		s.ID, s.Balance.Minor(), s.Version, s.UpdatedAt)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}
