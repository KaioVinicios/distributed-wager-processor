package events

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// WagerTransactionProcessed v1 is emitted on every successful completion,
// LOSS and OPENING included (messaging.md §6.3). External metadata is omitted
// for INTERNAL operations (OUT-13).
type WagerTransactionProcessed struct {
	TransactionID                  string      `json:"transactionId"`
	Origin                         string      `json:"origin"`
	Kind                           string      `json:"kind"`
	WalletID                       string      `json:"walletId"`
	PlayerID                       string      `json:"playerId"`
	ProviderID                     string      `json:"providerId,omitempty"`
	ExternalTransactionID          string      `json:"externalTransactionId,omitempty"`
	RoundID                        string      `json:"roundId,omitempty"`
	GameID                         string      `json:"gameId,omitempty"`
	Money                          money.Money `json:"money"`
	BalanceAfter                   money.Money `json:"balanceAfter"`
	WalletVersion                  int64       `json:"walletVersion"`
	ReferenceExternalTransactionID string      `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string      `json:"referenceTransactionId,omitempty"`
	ProcessedAt                    Time        `json:"processedAt"`
}

// NewWagerTransactionProcessed validates e.
func NewWagerTransactionProcessed(e WagerTransactionProcessed) (WagerTransactionProcessed, error) {
	if err := e.validate(); err != nil {
		return WagerTransactionProcessed{}, err
	}
	e.ProcessedAt = NewTime(e.ProcessedAt.Time)
	return e, nil
}

func (WagerTransactionProcessed) Type() Type   { return TypeWagerTransactionProcessed }
func (WagerTransactionProcessed) Version() int { return 1 }
func (e WagerTransactionProcessed) Aggregate() (AggregateType, string) {
	return AggregateWagerTransaction, e.TransactionID
}
func (e WagerTransactionProcessed) MessageGroupID() string { return e.WalletID }
func (e WagerTransactionProcessed) OccurredAt() time.Time  { return e.ProcessedAt.UTC() }

func (e WagerTransactionProcessed) validate() error {
	c := check{typ: TypeWagerTransactionProcessed}
	external := e.Origin == "EXTERNAL"
	c.require(ident.Valid(e.TransactionID), "transactionId")
	c.require(external || e.Origin == "INTERNAL", "origin")
	c.require(e.Kind != "", "kind")
	c.require(ident.Valid(e.WalletID), "walletId")
	c.require(ident.Valid(e.PlayerID), "playerId")
	c.require(external == (e.ProviderID != ""), "providerId")
	c.require(external == (e.ExternalTransactionID != ""), "externalTransactionId")
	c.require(external == (e.RoundID != ""), "roundId")
	c.require(external == (e.GameID != ""), "gameId")
	c.require(external || e.ReferenceExternalTransactionID == "", "referenceExternalTransactionId")
	c.require(e.ReferenceTransactionID == "" || ident.Valid(e.ReferenceTransactionID), "referenceTransactionId")
	c.require(nonNegative(e.Money), "money")
	c.require(nonNegative(e.BalanceAfter), "balanceAfter")
	c.require(e.WalletVersion >= 1, "walletVersion")
	c.require(!e.ProcessedAt.IsZero(), "processedAt")
	return c.err
}
