package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Queries are the reads of the API (HTTP-02..05), over the pool.
type Queries struct{ reads Repos }

// NewQueries builds the queries over the repositories of the pool.
func NewQueries(reads Repos) *Queries { return &Queries{reads: reads} }

// LedgerPage is one page of the ledger, in version order (D-16).
type LedgerPage struct {
	Entries []wallet.LedgerEntry
	// NextCursor resumes after the last entry; "" on the last page.
	NextCursor string
}

// GetWallet returns the wallet, or KindNotFound WALLET_NOT_FOUND (also for an
// id that is not a UUID).
func (q *Queries) GetWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	w, err := q.reads.Wallets().Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return wallet.Wallet{}, apperrors.New(apperrors.KindNotFound, CodeWalletNotFound, err)
	}
	return w, err
}

// ListLedger returns up to limit entries after the cursor ("" = from the
// start). The parameters are checked before the wallet is read, and the wallet
// before the ledger, so a malformed id never reaches the ledger query.
func (q *Queries) ListLedger(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error) {
	if err := checkLimit(limit); err != nil {
		return LedgerPage{}, err
	}
	var after int64
	if cursor != "" {
		var err error
		if after, err = decodeCursor(cursor); err != nil {
			return LedgerPage{}, err
		}
	}
	w, err := q.GetWallet(ctx, walletID)
	if err != nil {
		return LedgerPage{}, err
	}
	entries, err := q.reads.Ledger().List(ctx, w.ID(), after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	if len(entries) <= limit {
		return LedgerPage{Entries: entries}, nil
	}
	entries = entries[:limit]
	return LedgerPage{Entries: entries, NextCursor: encodeCursor(entries[limit-1].WalletVersion())}, nil
}

// GetTransaction returns the operation by its internal id, or KindNotFound
// TRANSACTION_NOT_FOUND. Whether the caller may see it is decided by the edge.
func (q *Queries) GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	tx, err := q.reads.Transactions().Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, apperrors.New(apperrors.KindNotFound, CodeTransactionNotFound, err)
	}
	return tx, err
}

// GetTransactionByExternalID returns the operation by (providerId,
// externalTransactionId), or KindNotFound TRANSACTION_NOT_FOUND.
func (q *Queries) GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	tx, err := q.reads.Transactions().FindByExternalID(ctx, providerID, externalID)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, apperrors.New(apperrors.KindNotFound, CodeTransactionNotFound, ErrNotFound)
	}
	return tx, nil
}
