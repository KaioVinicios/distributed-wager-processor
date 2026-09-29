package events

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// WagerTransactionRejected v1 is emitted on every business rejection, including
// reference expiry (messaging.md §6.4). Rejections only happen to EXTERNAL
// operations.
type WagerTransactionRejected struct {
	TransactionID                  string      `json:"transactionId"`
	Kind                           string      `json:"kind"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Money                          money.Money `json:"money"`
	FailureCode                    string      `json:"failureCode"`
	FailureCategory                string      `json:"failureCategory"`
	Balance                        money.Money `json:"balance"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	RejectedAt                     Time        `json:"rejectedAt"`
}

// NewWagerTransactionRejected validates e.
func NewWagerTransactionRejected(e WagerTransactionRejected) (WagerTransactionRejected, error) {
	if err := e.validate(); err != nil {
		return WagerTransactionRejected{}, err
	}
	e.RejectedAt = NewTime(e.RejectedAt.Time)
	return e, nil
}

func (WagerTransactionRejected) Type() Type   { return TypeWagerTransactionRejected }
func (WagerTransactionRejected) Version() int { return 1 }
func (e WagerTransactionRejected) Aggregate() (AggregateType, string) {
	return AggregateWagerTransaction, e.TransactionID
}
func (e WagerTransactionRejected) MessageGroupID() string { return e.WalletID }
func (e WagerTransactionRejected) OccurredAt() time.Time  { return e.RejectedAt.UTC() }

func (e WagerTransactionRejected) validate() error {
	c := check{typ: TypeWagerTransactionRejected}
	c.require(ident.Valid(e.TransactionID), "transactionId")
	c.require(e.Kind != "", "kind")
	c.require(ident.Valid(e.WalletID), "walletId")
	c.require(ident.Valid(e.PlayerID), "playerId")
	c.require(e.ProviderID != "", "providerId")
	c.require(e.ExternalTransactionID != "", "externalTransactionId")
	c.require(e.RoundID != "", "roundId")
	c.require(e.GameID != "", "gameId")
	c.require(nonNegative(e.Money), "money")
	c.require(e.FailureCode != "", "failureCode")
	c.require(e.FailureCategory != "", "failureCategory")
	c.require(nonNegative(e.Balance), "balance")
	c.require(!e.RejectedAt.IsZero(), "rejectedAt")
	return c.err
}
