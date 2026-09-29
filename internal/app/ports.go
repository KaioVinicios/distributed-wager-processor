// Package app holds the use cases and the ports they depend on. It knows the
// domain and the error vocabulary, never Fx, HTTP, SQS or the database driver.
package app

import (
	"context"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Clock is the time source of the use cases; the domain receives now as a
// parameter and never reads the clock itself.
type Clock interface{ Now() time.Time }

// IDGenerator creates the identifiers of new rows and events: canonical
// lowercase UUIDv7 (D-08).
type IDGenerator interface{ New() string }

// Metrics is what the use cases report beyond logs. It grows with M7; the
// observability adapter implements it with Prometheus.
type Metrics interface {
	// ReconciliationDivergence counts a reconciliation whose stored balance
	// differs from the ledger (reconciliation_divergences_total, HTTP-07).
	ReconciliationDivergence()
}

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
// inside the transaction; it is never hidden in the context.
type UnitOfWork interface {
	// Do runs fn in one READ COMMITTED transaction with lock_timeout set
	// (D-09): commit when fn returns nil, rollback on an error or a panic.
	Do(ctx context.Context, fn func(Repos) error) error
	// Snapshot runs fn in a REPEATABLE READ READ ONLY transaction: one
	// consistent view, for the reconciliation (D-16).
	Snapshot(ctx context.Context, fn func(Repos) error) error
}

// Repos are the repositories bound to one transaction, or to the pool for
// reads outside a transaction.
type Repos interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	// Insert fails with ErrWalletAlreadyExists for a second (playerId, currency).
	Insert(ctx context.Context, w wallet.Wallet) error
	// Lock reads the wallet with SELECT … FOR UPDATE (D-09); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (wallet.Wallet, error)
	// Get reads the wallet without a lock; ErrNotFound when absent.
	Get(ctx context.Context, id string) (wallet.Wallet, error)
	// UpdateBalance writes balance, version and updatedAt of a wallet moved by
	// one version; it fails as permanent when the stored version is not the
	// previous one.
	UpdateBalance(ctx context.Context, w wallet.Wallet) error
}

// TransactionRepository persists wager transactions.
type TransactionRepository interface {
	Insert(ctx context.Context, t *wagering.WagerTransaction) error
	// Update writes the state columns of a transaction that left
	// PENDING_REFERENCE or was rescheduled.
	Update(ctx context.Context, t *wagering.WagerTransaction) error
	// Get returns ErrNotFound when absent.
	Get(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	// FindByIdempotencyKey and FindByExternalID return nil, nil when absent,
	// the shape wagering.CheckIdempotency expects.
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)
	// FindReference resolves (providerId, referenceExternalTransactionId) and
	// tells whether the reference already has a PROCESSED REFUND or ROLLBACK;
	// Reference{} when absent.
	FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error)
	// AdvanceDependents makes the PENDING_REFERENCE operations waiting for
	// (providerID, externalID) due at now and returns how many there were.
	AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error)
}

// LedgerRepository appends and reads ledger entries.
type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	// List returns up to limit entries with wallet_version > afterVersion,
	// ordered by version (HTTP-03).
	List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
	// Sum rebuilds the balance from the ledger (D-16).
	Sum(ctx context.Context, walletID string) (LedgerSum, error)
}

// LedgerSum is the balance rebuilt from the ledger.
type LedgerSum struct {
	NetMinor int64 // Σ CREDIT − Σ DEBIT, in minor units
	Entries  int64
}

// OutboxRepository records the events of the transaction (D-13).
type OutboxRepository interface {
	Insert(ctx context.Context, envs ...events.Envelope) error
}

// InboxRepository deduplicates SQS messages per consumer (SQS-03).
type InboxRepository interface {
	// Find returns nil, nil when the message was never recorded.
	Find(ctx context.Context, consumer, messageID string) (*InboxMessage, error)
	// Insert fails with ErrInboxDuplicate when the message is already recorded.
	Insert(ctx context.Context, m InboxMessage) error
}

// InboxOutcome is how a message was concluded (data-model §3.4).
type InboxOutcome string

const (
	InboxProcessed        InboxOutcome = "PROCESSED"
	InboxRejected         InboxOutcome = "REJECTED"
	InboxPendingReference InboxOutcome = "PENDING_REFERENCE"
	InboxIdempotentReplay InboxOutcome = "IDEMPOTENT_REPLAY"
	InboxFailed           InboxOutcome = "FAILED"
)

// InboxMessage is one row of inbox_messages.
type InboxMessage struct {
	ConsumerName  string
	MessageID     string
	MessageHash   string
	MessageType   string
	TransactionID string // "" when the message did not reach an operation
	Outcome       InboxOutcome
	ReceivedAt    time.Time
	ProcessedAt   time.Time
}
