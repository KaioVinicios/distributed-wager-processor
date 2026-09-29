// Package events defines the typed integration events (messaging.md §6) and
// their envelope. The domain returns events; the app seals them into envelopes
// with transport metadata before writing them to the outbox.
package events

import (
	"errors"
	"fmt"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

// ErrInvalidEvent reports an event or envelope that breaks its contract.
var ErrInvalidEvent = errors.New("events: invalid event")

// Type is the eventType of the envelope.
type Type string

const (
	TypeWalletBalanceChanged             Type = "WalletBalanceChanged"
	TypeWagerTransactionProcessed        Type = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         Type = "WagerTransactionRejected"
	TypeWagerTransactionPendingReference Type = "WagerTransactionPendingReference"
)

// AggregateType is the aggregateType of the envelope.
type AggregateType string

const (
	AggregateWallet           AggregateType = "Wallet"
	AggregateWagerTransaction AggregateType = "WagerTransaction"
)

// Event is one of the four concrete event types. Type and version are methods
// of the Go type, so no caller can pick an arbitrary type or version (OUT-09).
// The interface is sealed by the unexported validate method.
type Event interface {
	Type() Type
	Version() int
	Aggregate() (AggregateType, string)
	MessageGroupID() string
	OccurredAt() time.Time
	validate() error
}

// check collects the first contract violation of an event.
type check struct {
	typ Type
	err error
}

func (c *check) require(ok bool, field string) {
	if !ok && c.err == nil {
		c.err = fmt.Errorf("%w: %s: invalid %s", ErrInvalidEvent, c.typ, field)
	}
}

func positive(m money.Money) bool    { return m.Currency().Valid() && m.Sign() > 0 }
func nonNegative(m money.Money) bool { return m.Currency().Valid() && m.Sign() >= 0 }
