package events

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// WalletBalanceChanged v1 is emitted whenever a balance changes
// (messaging.md §6.2, OUT-11).
type WalletBalanceChanged struct {
	WalletID        string      `json:"walletId"`
	TransactionID   string      `json:"transactionId"`
	TransactionKind string      `json:"transactionKind"`
	Direction       string      `json:"direction"`
	Money           money.Money `json:"money"`
	BalanceBefore   money.Money `json:"balanceBefore"`
	BalanceAfter    money.Money `json:"balanceAfter"`
	WalletVersion   int64       `json:"walletVersion"`
	// ChangedAt is the ledger entry instant; it becomes the envelope occurredAt.
	ChangedAt Time `json:"-"`
}

// NewWalletBalanceChanged validates e.
func NewWalletBalanceChanged(e WalletBalanceChanged) (WalletBalanceChanged, error) {
	if err := e.validate(); err != nil {
		return WalletBalanceChanged{}, err
	}
	e.ChangedAt = NewTime(e.ChangedAt.Time)
	return e, nil
}

func (WalletBalanceChanged) Type() Type                           { return TypeWalletBalanceChanged }
func (WalletBalanceChanged) Version() int                         { return 1 }
func (e WalletBalanceChanged) Aggregate() (AggregateType, string) { return AggregateWallet, e.WalletID }
func (e WalletBalanceChanged) MessageGroupID() string             { return e.WalletID }
func (e WalletBalanceChanged) OccurredAt() time.Time              { return e.ChangedAt.UTC() }

func (e WalletBalanceChanged) validate() error {
	c := check{typ: TypeWalletBalanceChanged}
	c.require(ident.Valid(e.WalletID), "walletId")
	c.require(ident.Valid(e.TransactionID), "transactionId")
	c.require(e.TransactionKind != "", "transactionKind")
	c.require(e.Direction == "DEBIT" || e.Direction == "CREDIT", "direction")
	c.require(positive(e.Money), "money")
	c.require(nonNegative(e.BalanceBefore) && e.BalanceBefore.Currency() == e.Money.Currency(), "balanceBefore")
	c.require(nonNegative(e.BalanceAfter) && e.BalanceAfter.Currency() == e.Money.Currency(), "balanceAfter")
	c.require(e.WalletVersion >= 1, "walletVersion")
	c.require(!e.ChangedAt.IsZero(), "changedAt")
	return c.err
}
