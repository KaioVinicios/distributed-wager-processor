package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// ProcessRequest is one external operation, as HTTP (and, from M5, SQS)
// delivers it.
type ProcessRequest struct {
	Command       wagering.Command
	Via           wagering.ReceivedVia
	CorrelationID string
	// CausationID is the message that caused the operation ("" over HTTP).
	CausationID string
	// Inbox is the SQS message that carries the operation (nil over HTTP).
	Inbox *InboxReceipt
}

// InboxReceipt identifies the SQS message that carries the operation. When a
// ProcessRequest has one, every outcome records it in inbox_messages in the
// transaction that concludes the operation (SQS-04).
type InboxReceipt struct {
	Consumer    string
	MessageID   string
	MessageHash string
	MessageType string
	ReceivedAt  time.Time
}

// ProcessResult is the persisted outcome: PROCESSED, PENDING_REFERENCE,
// REJECTED or FAILED. Replay tells whether it was recorded by an earlier
// delivery of the same operation (D-08).
type ProcessResult struct {
	Tx     *wagering.WagerTransaction
	Replay bool
}

// ProcessWager is the single use case of HTTP, SQS and the reference worker
// (lifecycle §6).
type ProcessWager struct {
	uow    UnitOfWork
	reads  Repos
	clock  Clock
	ids    IDGenerator
	policy wagering.ReferenceRetryPolicy
	log    *slog.Logger

	metrics Metrics
}

// NewProcessWager builds the use case. reads are the repositories over the
// pool, used for the idempotency lookup before the transaction.
func NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager {
	return &ProcessWager{uow: uow, reads: reads, clock: clock, ids: ids, policy: policy, log: log, metrics: NopMetrics{}}
}

// WithMetrics reports the outcomes to m; the default discards them.
func (p *ProcessWager) WithMetrics(m Metrics) *ProcessWager {
	p.metrics = m
	return p
}

// maxAttempts bounds the reruns after a unique-index race (spec decision 4).
const maxAttempts = 3

// Execute runs the pipeline of lifecycle §6.1 for one operation. Business
// outcomes (PROCESSED, PENDING_REFERENCE, REJECTED, FAILED) are results, never
// errors; an error means nothing new was recorded: KindInput (UNKNOWN_WALLET),
// KindConflict (idempotency, 409), KindTransient (retry) or KindPermanent (an
// earlier delivery that cannot be read).
//
// A unique-index race (the same operation committed concurrently) rolls back
// and reruns from the lookup, which then finds the replay, the conflict or
// ALREADY_REVERSED; after maxAttempts the race is returned, still transient.
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	start := p.clock.Now()
	var err error
	for range maxAttempts {
		var res ProcessResult
		if res, err = p.attempt(ctx, req); !isRace(err) {
			p.observe(ctx, req, res, err, p.clock.Now().Sub(start))
			return res, err
		}
		p.metrics.Conflict(ConflictUniqueRace)
	}
	p.observe(ctx, req, ProcessResult{}, err, p.clock.Now().Sub(start))
	return ProcessResult{}, err
}

// observe reports one call: a lock conflict, or the conclusion. A replay is a
// duplicate, not a new conclusion; over SQS the consumer counts its own
// duplicates (inbox and idempotency), so only HTTP counts here (spec M7, decision 4).
func (p *ProcessWager) observe(ctx context.Context, req ProcessRequest, res ProcessResult, err error, d time.Duration) {
	if errors.Is(err, ErrLockTimeout) {
		p.metrics.Conflict(ConflictLockTimeout)
	}
	if err != nil || res.Tx == nil {
		return
	}
	tx, channel := res.Tx, strings.ToLower(string(req.Via))
	outcome := strings.ToLower(string(tx.Status()))
	if res.Replay {
		if req.Via == wagering.ReceivedViaHTTP {
			p.metrics.WagerDuplicate(channel, "idempotency")
		}
	} else {
		p.metrics.WagerConcluded(channel, string(tx.Kind()), outcome, string(tx.FailureCode()), d)
	}
	attrs := []any{
		"transactionId", tx.ID(), "walletId", tx.WalletID(), "providerId", tx.ProviderID(),
		"correlationId", req.CorrelationID, "channel", channel,
		"kind", string(tx.Kind()), "outcome", outcome, "failureCode", string(tx.FailureCode()), "replay", res.Replay,
	}
	if req.Inbox != nil {
		attrs = append(attrs, "messageId", req.Inbox.MessageID)
	}
	p.log.InfoContext(ctx, "wager concluded", attrs...)
}

func isRace(err error) bool {
	return errors.Is(err, ErrIdempotencyRace) || errors.Is(err, ErrReversalRace)
}

// attempt looks the operation up by its idempotency keys and, if it is new,
// settles it under the wallet lock.
func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	cmd := req.Command
	if replay, err := lookup(ctx, p.reads, cmd); err != nil || replay != nil {
		if err == nil && req.Inbox != nil {
			err = p.uow.Do(ctx, func(r Repos) error { return recordInbox(ctx, r, req.Inbox, replay, true, p.clock.Now()) })
		}
		if err != nil {
			return ProcessResult{}, err
		}
		return ProcessResult{Tx: replay, Replay: true}, nil
	}
	now := p.clock.Now()
	var res ProcessResult
	err := p.uow.Do(ctx, func(r Repos) error {
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if errors.Is(err, ErrNotFound) {
			return apperrors.New(apperrors.KindInput, CodeUnknownWallet, err)
		}
		if err != nil {
			return err
		}
		// Again under the lock: a concurrent delivery may have committed it.
		replay, err := lookup(ctx, r, cmd)
		if err != nil || replay != nil {
			res = ProcessResult{Tx: replay, Replay: replay != nil}
			if err == nil {
				err = recordInbox(ctx, r, req.Inbox, replay, true, now)
			}
			return err
		}
		tx, err := wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now)
		if err != nil {
			return domainError(err)
		}
		if err := p.settleAndPersist(ctx, r, tx, &w, now, true, fixedCause(req.CausationID)); err != nil {
			return err
		}
		res = ProcessResult{Tx: tx}
		return recordInbox(ctx, r, req.Inbox, tx, false, now)
	})
	if apperrors.Classify(err) == apperrors.KindPermanent {
		return p.recordFailure(ctx, req, now, err)
	}
	if err != nil {
		return ProcessResult{}, err
	}
	return res, nil
}

// recordFailure writes the operation as FAILED, INTERNAL_PERMANENT_FAILURE,
// in a transaction of its own, without entry or events (D-05, lifecycle
// §5.2), and advances the operations waiting for it. If even that cannot be
// written, the failure is transient: nothing was recorded, retrying is safe.
func (p *ProcessWager) recordFailure(ctx context.Context, req ProcessRequest, now time.Time, cause error) (ProcessResult, error) {
	cmd := req.Command
	var tx *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(r Repos) error {
		var err error
		if tx, err = wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now); err != nil {
			return domainError(err)
		}
		if err := tx.Fail(now); err != nil {
			return domainError(err)
		}
		if err := r.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		if _, err := r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now); err != nil {
			return err
		}
		return recordInbox(ctx, r, req.Inbox, tx, false, now)
	})
	switch {
	case isRace(err):
		return ProcessResult{}, err
	case err != nil:
		return ProcessResult{}, apperrors.New(apperrors.KindTransient, "", fmt.Errorf("app: recording FAILED: %w (after %w)", err, cause))
	}
	p.log.ErrorContext(ctx, "permanent failure recorded",
		"transactionId", tx.ID(), "walletId", cmd.WalletID(), "providerId", cmd.ProviderID(),
		"correlationId", req.CorrelationID, "error", cause.Error())
	return ProcessResult{Tx: tx}, nil
}

// recordInbox writes the SQS delivery of the operation with the outcome of tx
// (SQS-04), in the transaction of r; nothing over HTTP (in == nil). A second
// delivery of the same message fails with ErrInboxDuplicate.
func recordInbox(ctx context.Context, r Repos, in *InboxReceipt, tx *wagering.WagerTransaction, replay bool, now time.Time) error {
	if in == nil {
		return nil
	}
	return r.Inbox().Insert(ctx, InboxMessage{
		ConsumerName: in.Consumer, MessageID: in.MessageID, MessageHash: in.MessageHash, MessageType: in.MessageType,
		TransactionID: tx.ID(), Outcome: inboxOutcome(tx.Status(), replay), ReceivedAt: in.ReceivedAt, ProcessedAt: now,
	})
}

// inboxOutcome is how the operation concluded the message (data-model §3.4).
func inboxOutcome(s wagering.Status, replay bool) InboxOutcome {
	switch {
	case replay:
		return InboxIdempotentReplay
	case s == wagering.StatusRejected:
		return InboxRejected
	case s == wagering.StatusPendingReference:
		return InboxPendingReference
	case s == wagering.StatusFailed:
		return InboxFailed
	default:
		return InboxProcessed
	}
}

// lookup applies D-08: the transaction found by (providerId, idempotencyKey)
// with the same hash is a replay; the same key with another hash, or the same
// externalTransactionId under another key, is a conflict (KindConflict).
//
// The two reads are separate statements: a concurrent delivery of the same
// operation may commit between them, and then only the second one finds it.
// Found under the same key, it is the transaction of the key, not a conflict.
func lookup(ctx context.Context, r Repos, cmd wagering.Command) (*wagering.WagerTransaction, error) {
	byKey, err := r.Transactions().FindByIdempotencyKey(ctx, cmd.ProviderID(), cmd.IdempotencyKey())
	if err != nil {
		return nil, err
	}
	var byExternalID *wagering.WagerTransaction
	if byKey == nil {
		if byExternalID, err = r.Transactions().FindByExternalID(ctx, cmd.ProviderID(), cmd.ExternalTransactionID()); err != nil {
			return nil, err
		}
		if byExternalID != nil && byExternalID.IdempotencyKey() == cmd.IdempotencyKey() {
			byKey, byExternalID = byExternalID, nil
		}
	}
	replay, err := wagering.CheckIdempotency(cmd.PayloadHash(), byKey, byExternalID)
	return replay, domainError(err)
}

// causation gives the causationId of the events of an evaluation from the
// reference it found (Reference.Tx is nil when there is none).
type causation func(ref wagering.Reference) string

// fixedCause is the message that carries the operation ("" over HTTP).
func fixedCause(id string) causation { return func(wagering.Reference) string { return id } }

// referenceCause is the operation that unblocked a pending one, when it exists
// (messaging §7); empty when the reference was never found.
func referenceCause(ref wagering.Reference) string {
	if ref.Tx == nil {
		return ""
	}
	return ref.Tx.ID()
}

// settleAndPersist evaluates the operation against its locked wallet and
// writes the outcome in the order the triggers require (data-model §4.2):
// the operation, then the balance and its entry, then the events. A terminal
// outcome advances the operations waiting for it (D-11). The reference worker
// calls it with insert = false and the reference as the cause.
func (p *ProcessWager) settleAndPersist(ctx context.Context, r Repos, tx *wagering.WagerTransaction, w *wallet.Wallet, now time.Time, insert bool, cause causation) error {
	var ref wagering.Reference
	if refID := tx.ReferenceExternalTransactionID(); refID != "" {
		var err error
		if ref, err = r.Transactions().FindReference(ctx, tx.ProviderID(), refID); err != nil {
			return err
		}
	}
	out, err := wagering.Settle(tx, w, ref, wagering.SettleParams{EntryID: p.ids.New(), Now: now, Policy: p.policy})
	if err != nil {
		return domainError(err)
	}
	envs, err := sealEvents(p.ids, out.Events, tx.CorrelationID(), cause(ref))
	if err != nil {
		return err
	}
	if insert {
		err = r.Transactions().Insert(ctx, tx)
	} else {
		err = r.Transactions().Update(ctx, tx)
	}
	if err != nil {
		return err
	}
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, *w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	if len(envs) > 0 {
		if err := r.Outbox().Insert(ctx, envs...); err != nil {
			return err
		}
	}
	if tx.Status().IsTerminal() {
		_, err := r.Transactions().AdvanceDependents(ctx, tx.ProviderID(), tx.ExternalTransactionID(), now)
		return err
	}
	return nil
}
