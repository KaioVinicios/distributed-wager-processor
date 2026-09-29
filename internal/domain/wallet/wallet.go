// Package wallet implements the financial aggregate root: its balance only
// changes through Open, Debit and Credit, and every change yields the matching
// ledger entry (CHALLENGE §6.2).
package wallet

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Wallet is the aggregate root. The zero value is uninitialized.
type Wallet struct {
	id        string
	playerID  string
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// Snapshot is the persisted state of a wallet (mapped by the postgres adapter).
type Snapshot struct {
	ID        string
	PlayerID  string
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Open creates a wallet with version 1 and balance = initial (>= 0). A positive
// initial balance must be recorded with OpeningEntry.
func Open(id, playerID string, initial money.Money, now time.Time) (Wallet, error) {
	switch {
	case !ident.Valid(id) || !ident.Valid(playerID):
		return Wallet{}, invalid(ErrInvalidWallet, "ids must be canonical UUIDs")
	case !initial.Currency().Valid():
		return Wallet{}, invalid(ErrInvalidWallet, "initial balance is uninitialized")
	case initial.Sign() < 0:
		return Wallet{}, invalid(ErrInvalidWallet, "initial balance must not be negative")
	case now.IsZero():
		return Wallet{}, invalid(ErrInvalidWallet, "now is required")
	}
	now = normalize(now)
	return Wallet{id: id, playerID: playerID, balance: initial, version: 1, createdAt: now, updatedAt: now}, nil
}

// Rehydrate rebuilds a wallet from persisted state, without transitions. A
// corrupted snapshot is rejected with ErrInvalidWallet.
func Rehydrate(s Snapshot) (Wallet, error) {
	switch {
	case !ident.Valid(s.ID) || !ident.Valid(s.PlayerID):
		return Wallet{}, invalid(ErrInvalidWallet, "ids must be canonical UUIDs")
	case !s.Balance.Currency().Valid() || s.Balance.Sign() < 0:
		return Wallet{}, invalid(ErrInvalidWallet, "balance must be a non-negative supported amount")
	case s.Version < 1:
		return Wallet{}, invalid(ErrInvalidWallet, "version must be at least 1")
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return Wallet{}, invalid(ErrInvalidWallet, "updated at must not precede created at")
	}
	return Wallet{
		id:        s.ID,
		playerID:  s.PlayerID,
		balance:   s.Balance,
		version:   s.Version,
		createdAt: normalize(s.CreatedAt),
		updatedAt: normalize(s.UpdatedAt),
	}, nil
}

func (w Wallet) initialized() bool { return w.id != "" }

// Snapshot returns the state to persist.
func (w Wallet) Snapshot() (Snapshot, error) {
	if !w.initialized() {
		return Snapshot{}, ErrUninitialized
	}
	return Snapshot{
		ID: w.id, PlayerID: w.playerID, Balance: w.balance, Version: w.version,
		CreatedAt: w.createdAt, UpdatedAt: w.updatedAt,
	}, nil
}

func (w Wallet) ID() string               { return w.id }
func (w Wallet) PlayerID() string         { return w.playerID }
func (w Wallet) Currency() money.Currency { return w.balance.Currency() }
func (w Wallet) Balance() money.Money     { return w.balance }
func (w Wallet) Version() int64           { return w.version }
func (w Wallet) CreatedAt() time.Time     { return w.createdAt }
func (w Wallet) UpdatedAt() time.Time     { return w.updatedAt }

// OpeningEntry is the ledger entry of the initial balance: CREDIT from 0.00 to
// the balance, at version 1 and at the creation instant.
func (w Wallet) OpeningEntry(entryID, txID string) (LedgerEntry, error) {
	if !w.initialized() {
		return LedgerEntry{}, ErrUninitialized
	}
	if w.version != 1 || w.balance.Sign() <= 0 {
		return LedgerEntry{}, ErrInvalidOpening
	}
	zero, err := money.Zero(w.balance.Currency())
	if err != nil {
		return LedgerEntry{}, err
	}
	return NewLedgerEntry(LedgerEntryParams{
		ID: entryID, WalletID: w.id, TransactionID: txID, Direction: DirectionCredit,
		Amount: w.balance, BalanceBefore: zero, BalanceAfter: w.balance,
		WalletVersion: 1, CreatedAt: w.createdAt,
	})
}

// Debit removes amount from the balance, keeping it >= 0 (WAL-04).
func (w *Wallet) Debit(entryID, txID string, amount money.Money, now time.Time) (LedgerEntry, error) {
	return w.move(DirectionDebit, entryID, txID, amount, now)
}

// Credit adds amount to the balance.
func (w *Wallet) Credit(entryID, txID string, amount money.Money, now time.Time) (LedgerEntry, error) {
	return w.move(DirectionCredit, entryID, txID, amount, now)
}

// move applies a movement and returns its ledger entry. On error the wallet is
// left untouched.
func (w *Wallet) move(d Direction, entryID, txID string, amount money.Money, now time.Time) (LedgerEntry, error) {
	if w == nil || !w.initialized() {
		return LedgerEntry{}, ErrUninitialized
	}
	if amount.Sign() <= 0 {
		return LedgerEntry{}, ErrInvalidAmount
	}
	var after money.Money
	var err error
	if d == DirectionDebit {
		after, err = w.balance.Sub(amount)
		if err == nil && after.Sign() < 0 {
			err = ErrInsufficientFunds
		}
	} else {
		after, err = w.balance.Add(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	entry, err := NewLedgerEntry(LedgerEntryParams{
		ID: entryID, WalletID: w.id, TransactionID: txID, Direction: d,
		Amount: amount, BalanceBefore: w.balance, BalanceAfter: after,
		WalletVersion: w.version + 1, CreatedAt: now,
	})
	if err != nil {
		return LedgerEntry{}, err
	}
	w.balance, w.version, w.updatedAt = after, entry.WalletVersion(), notBefore(entry.CreatedAt(), w.createdAt)
	return entry, nil
}
