package wallet

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Direction is the side of a ledger entry.
type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// Valid reports whether d is DEBIT or CREDIT.
func (d Direction) Valid() bool { return d == DirectionDebit || d == DirectionCredit }

// LedgerEntry is an immutable wallet movement (CHALLENGE §6.4).
type LedgerEntry struct {
	id            string
	walletID      string
	transactionID string
	direction     Direction
	amount        money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	walletVersion int64
	createdAt     time.Time
}

// LedgerEntryParams are the fields of a ledger entry.
type LedgerEntryParams struct {
	ID            string
	WalletID      string
	TransactionID string
	Direction     Direction
	Amount        money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	WalletVersion int64
	CreatedAt     time.Time
}

// NewLedgerEntry validates p: canonical IDs, a known direction, a positive
// amount, one currency, non-negative balances, BalanceAfter = BalanceBefore ±
// Amount (LED-02) and WalletVersion >= 1. It serves both creation and
// rehydration: an entry has no transitions or events, so rehydrating it is
// validating the same invariants.
func NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error) {
	switch {
	case !ident.Valid(p.ID) || !ident.Valid(p.WalletID) || !ident.Valid(p.TransactionID):
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "ids must be canonical UUIDs")
	case !p.Direction.Valid():
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "unknown direction")
	case !p.Amount.Currency().Valid() || p.Amount.Sign() <= 0:
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "amount must be positive")
	case p.BalanceBefore.Currency() != p.Amount.Currency() || p.BalanceAfter.Currency() != p.Amount.Currency():
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "amount and balances must share one currency")
	case p.BalanceBefore.Sign() < 0 || p.BalanceAfter.Sign() < 0:
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "balances must not be negative")
	case p.WalletVersion < 1:
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "wallet version must be at least 1")
	case p.CreatedAt.IsZero():
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "created at is required")
	}
	want, err := p.BalanceBefore.Add(p.Amount)
	if p.Direction == DirectionDebit {
		want, err = p.BalanceBefore.Sub(p.Amount)
	}
	if err != nil || want != p.BalanceAfter {
		return LedgerEntry{}, invalid(ErrInvalidLedgerEntry, "balance after must equal balance before plus or minus amount")
	}
	return LedgerEntry{
		id:            p.ID,
		walletID:      p.WalletID,
		transactionID: p.TransactionID,
		direction:     p.Direction,
		amount:        p.Amount,
		balanceBefore: p.BalanceBefore,
		balanceAfter:  p.BalanceAfter,
		walletVersion: p.WalletVersion,
		createdAt:     normalize(p.CreatedAt),
	}, nil
}

func (e LedgerEntry) ID() string                 { return e.id }
func (e LedgerEntry) WalletID() string           { return e.walletID }
func (e LedgerEntry) TransactionID() string      { return e.transactionID }
func (e LedgerEntry) Direction() Direction       { return e.direction }
func (e LedgerEntry) Amount() money.Money        { return e.amount }
func (e LedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e LedgerEntry) WalletVersion() int64       { return e.walletVersion }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }

// normalize keeps instants in UTC with microsecond precision, the precision of
// PostgreSQL, so rehydration returns exactly what was stored (data-model §2).
func normalize(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// notBefore returns t, or floor when t precedes it. Another instance's clock
// may lag behind the creation instant; updatedAt must still not precede
// createdAt (wallets_updated_after_created).
func notBefore(t, floor time.Time) time.Time {
	if t.Before(floor) {
		return floor
	}
	return t
}
