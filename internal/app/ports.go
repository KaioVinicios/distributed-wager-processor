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

// Metrics is what the use cases report beyond logs; the observability adapter
// implements it with Prometheus (D-18). The use cases never import Prometheus.
type Metrics interface {
	// ReconciliationDivergence counts a reconciliation whose stored balance
	// differs from the ledger (reconciliation_divergences_total, HTTP-07).
	ReconciliationDivergence()
	// Reconciled counts every reconciliation run (reconciliation_runs_total).
	Reconciled(consistent bool)
	// WagerConcluded counts an operation concluded for the first time, by
	// channel (http, sqs, worker), kind, outcome and failure code, with the
	// time it took (wager_transactions_total, wager_processing_duration_seconds).
	WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration)
	// WagerDuplicate counts a repeated delivery caught by layer
	// (wager_duplicates_total).
	WagerDuplicate(channel, layer string)
	// Conflict counts a concurrency conflict (concurrency_conflicts_total).
	Conflict(reason string)
}

// NopMetrics discards every measurement: the default of a use case built
// without metrics.
type NopMetrics struct{}

func (NopMetrics) ReconciliationDivergence()                         {}
func (NopMetrics) Reconciled(bool)                                   {}
func (NopMetrics) WagerConcluded(_, _, _, _ string, _ time.Duration) {}
func (NopMetrics) WagerDuplicate(_, _ string)                        {}
func (NopMetrics) Conflict(string)                                   {}

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

// PendingReference is a due PENDING_REFERENCE operation: the wallet to lock
// first, then the operation (data-model §6).
type PendingReference struct{ ID, WalletID string }

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
	// ClaimDue lists up to limit PENDING_REFERENCE operations due at now
	// (next_attempt_at <= now), oldest first, in one statement: the rows another
	// transaction holds are skipped (FOR UPDATE SKIP LOCKED). It leases nothing;
	// the worker rechecks under the locks (D-11, spec M6 decision 2).
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]PendingReference, error)
	// Lock reads the operation with SELECT … FOR UPDATE, after the wallet lock
	// (data-model §6); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	// CountPendingReferences counts the operations in PENDING_REFERENCE.
	CountPendingReferences(ctx context.Context) (int, error)
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

// OutboxStore is the publisher's side of outbox_events (D-13). Every method is
// one statement on the pool, outside any unit of work; owner is the instance
// identity written to locked_by. Instants come from the database clock.
type OutboxStore interface {
	// Claim leases up to limit due events (unpublished, next_attempt_at <= now,
	// no live lease) with FOR UPDATE SKIP LOCKED.
	Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]PendingEvent, error)
	// MarkPublished confirms an event still leased by owner; ok is false when
	// the lease was lost (another instance confirmed or reclaimed it).
	MarkPublished(ctx context.Context, eventID, owner string) (publishedAt time.Time, ok bool, err error)
	// MarkFailed counts the attempt, schedules the next one retryIn from now
	// and releases the lease; ok is false when the lease was lost.
	MarkFailed(ctx context.Context, eventID, owner string, retryIn time.Duration, reason string) (ok bool, err error)
	// Backlog counts the unpublished events and the age of the oldest.
	Backlog(ctx context.Context) (OutboxBacklog, error)
}

// PendingEvent is a leased outbox row, ready to publish as is.
type PendingEvent struct {
	EventID        string
	MessageGroupID string
	EventType      string
	EventVersion   int
	CorrelationID  string
	Payload        []byte // the envelope JSON read from the column
	OccurredAt     time.Time
	Attempts       int
	Reclaimed      bool // the previous owner's lease had expired
}

// OutboxBacklog feeds the outbox lag gauges.
type OutboxBacklog struct {
	Pending   int
	OldestAge time.Duration // 0 when nothing is pending
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
