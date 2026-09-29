package wallet

import (
	"errors"
	"fmt"
)

var (
	// ErrInsufficientFunds reports a debit larger than the balance.
	ErrInsufficientFunds = errors.New("wallet: insufficient funds")
	// ErrInvalidAmount reports a movement that is not positive.
	ErrInvalidAmount = errors.New("wallet: movement amount must be positive")
	// ErrInvalidOpening reports an opening entry outside version 1 or for a zero balance.
	ErrInvalidOpening = errors.New("wallet: opening entry requires version 1 and a positive balance")
	// ErrInvalidWallet reports invalid creation arguments or a corrupted snapshot.
	ErrInvalidWallet = errors.New("wallet: invalid wallet")
	// ErrInvalidLedgerEntry reports a ledger entry that breaks its invariants.
	ErrInvalidLedgerEntry = errors.New("wallet: invalid ledger entry")
	// ErrUninitialized reports the use of the Wallet zero value or a nil wallet.
	ErrUninitialized = errors.New("wallet: uninitialized wallet")
)

// invalid wraps a sentinel with a reason. Reasons never carry values.
func invalid(sentinel error, reason string) error {
	return fmt.Errorf("%w: %s", sentinel, reason)
}
