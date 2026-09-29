package events

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// WagerTransactionPendingReference v1 is emitted once, when an operation starts
// waiting for its reference (messaging.md §6.5).
type WagerTransactionPendingReference struct {
	TransactionID                  string      `json:"transactionId"`
	Kind                           string      `json:"kind"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId"`
	ExternalTransactionID          string      `json:"externalTransactionId"`
	RoundID                        string      `json:"roundId"`
	GameID                         string      `json:"gameId"`
	Money                          money.Money `json:"money"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId"`
	NextAttemptAt                  Time        `json:"nextAttemptAt"`
	ExpiresAt                      Time        `json:"expiresAt"`
	// PendingAt is the transition instant; it becomes the envelope occurredAt.
	PendingAt Time `json:"-"`
}

// NewWagerTransactionPendingReference validates e.
func NewWagerTransactionPendingReference(e WagerTransactionPendingReference) (WagerTransactionPendingReference, error) {
	if err := e.validate(); err != nil {
		return WagerTransactionPendingReference{}, err
	}
	e.NextAttemptAt, e.ExpiresAt, e.PendingAt = NewTime(e.NextAttemptAt.Time), NewTime(e.ExpiresAt.Time), NewTime(e.PendingAt.Time)
	return e, nil
}

func (WagerTransactionPendingReference) Type() Type   { return TypeWagerTransactionPendingReference }
func (WagerTransactionPendingReference) Version() int { return 1 }
func (e WagerTransactionPendingReference) Aggregate() (AggregateType, string) {
	return AggregateWagerTransaction, e.TransactionID
}
func (e WagerTransactionPendingReference) MessageGroupID() string { return e.WalletID }
func (e WagerTransactionPendingReference) OccurredAt() time.Time  { return e.PendingAt.UTC() }

func (e WagerTransactionPendingReference) validate() error {
	c := check{typ: TypeWagerTransactionPendingReference}
	c.require(ident.Valid(e.TransactionID), "transactionId")
	c.require(e.Kind != "", "kind")
	c.require(ident.Valid(e.WalletID), "walletId")
	c.require(ident.Valid(e.PlayerID), "playerId")
	c.require(e.ProviderID != "", "providerId")
	c.require(e.ExternalTransactionID != "", "externalTransactionId")
	c.require(e.RoundID != "", "roundId")
	c.require(e.GameID != "", "gameId")
	c.require(positive(e.Money), "money")
	c.require(e.ReferenceExternalTransactionID != "", "referenceExternalTransactionId")
	c.require(!e.NextAttemptAt.IsZero(), "nextAttemptAt")
	c.require(!e.ExpiresAt.IsZero(), "expiresAt")
	c.require(!e.PendingAt.IsZero(), "pendingAt")
	return c.err
}
