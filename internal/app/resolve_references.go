package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// ResolveOutcome is what one evaluation of a pending operation did.
type ResolveOutcome string

const (
	// ResolveSkipped: the operation was not pending or not due anymore under
	// the locks; nothing was written (spec M6, decision 3).
	ResolveSkipped     ResolveOutcome = "SKIPPED"
	ResolveProcessed   ResolveOutcome = "PROCESSED"
	ResolveRejected    ResolveOutcome = "REJECTED"
	ResolveRescheduled ResolveOutcome = "RESCHEDULED"
	ResolveFailed      ResolveOutcome = "FAILED"
)

// ResolveResult is the outcome of one Resolve.
type ResolveResult struct {
	Outcome       ResolveOutcome
	TransactionID string
	WalletID      string
	Kind          wagering.Kind
	FailureCode   wagering.FailureCode // Rejected only
	Attempts      int
}

// Expired tells whether the operation was rejected because its retry limit or
// its TTL was reached (REFERENCE_NOT_FOUND).
func (r ResolveResult) Expired() bool {
	return r.Outcome == ResolveRejected && r.FailureCode == wagering.FailureReferenceNotFound
}

// ResolveReferences is the use case of the reference worker (lifecycle §6.3).
type ResolveReferences struct{ p *ProcessWager }

// NewResolveReferences reuses the unit of work, clock, ids, policy and
// settlement of the ProcessWager (spec M6, decision 5).
func NewResolveReferences(p *ProcessWager) *ResolveReferences { return &ResolveReferences{p: p} }

// Claim lists the operations due now, oldest first (one statement on the
// pool; the locks come in Resolve).
func (r *ResolveReferences) Claim(ctx context.Context, limit int) ([]PendingReference, error) {
	return r.p.reads.Transactions().ClaimDue(ctx, r.p.clock.Now(), limit)
}

// CountPending counts the operations in PENDING_REFERENCE (the gauge).
func (r *ResolveReferences) CountPending(ctx context.Context) (int, error) {
	return r.p.reads.Transactions().CountPendingReferences(ctx)
}

// Resolve evaluates one pending operation (see resolve) and reports it: a
// terminal outcome is a conclusion on the "worker" channel, a rescheduled or
// skipped attempt is not (spec M7, decision 5).
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error) {
	start := r.p.clock.Now()
	res, err := r.resolve(ctx, ref)
	if errors.Is(err, ErrLockTimeout) {
		r.p.metrics.Conflict(ConflictLockTimeout)
	}
	if err != nil {
		return res, err
	}
	switch res.Outcome {
	case ResolveProcessed, ResolveRejected, ResolveFailed:
		r.p.metrics.WagerConcluded("worker", string(res.Kind), strings.ToLower(string(res.Outcome)),
			string(res.FailureCode), r.p.clock.Now().Sub(start))
	case ResolveSkipped, ResolveRescheduled:
	}
	return res, nil
}

// resolve evaluates one pending operation in its own unit of work: lock the
// wallet, then the operation (the order of the HTTP path, data-model §6),
// recheck, and settle it with insert = false. The outcomes are PROCESSED,
// REJECTED (including the expiration), RESCHEDULED and SKIPPED; an error means
// nothing was written and the operation stays due (KindTransient), except a
// permanent failure, which is recorded as FAILED (spec M6, decisions 8 and 9).
func (r *ResolveReferences) resolve(ctx context.Context, ref PendingReference) (ResolveResult, error) {
	tx, err := r.attempt(ctx, ref)
	if apperrors.Classify(err) == apperrors.KindPermanent {
		return r.recordFailure(ctx, ref, err)
	}
	if err != nil {
		return ResolveResult{}, err
	}
	if tx == nil {
		return ResolveResult{Outcome: ResolveSkipped, TransactionID: ref.ID, WalletID: ref.WalletID}, nil
	}
	res := resultOf(tx)
	if res.Outcome == ResolveRescheduled {
		r.p.log.DebugContext(ctx, "reference rescheduled", "transactionId", tx.ID(), "walletId", tx.WalletID(),
			"providerId", tx.ProviderID(), "correlationId", tx.CorrelationID(), "attempts", res.Attempts)
		return res, nil
	}
	r.p.log.InfoContext(ctx, "reference resolved", "transactionId", tx.ID(), "walletId", tx.WalletID(),
		"providerId", tx.ProviderID(), "correlationId", tx.CorrelationID(), "outcome", string(res.Outcome),
		"failureCode", string(res.FailureCode), "attempts", res.Attempts)
	return res, nil
}

// attempt settles the operation, or returns nil when it was skipped.
func (r *ResolveReferences) attempt(ctx context.Context, ref PendingReference) (*wagering.WagerTransaction, error) {
	p := r.p
	now := p.clock.Now()
	var done *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(repos Repos) error {
		w, tx, err := lockPending(ctx, repos, ref, now)
		if err != nil || tx == nil {
			return err
		}
		if err := p.settleAndPersist(ctx, repos, tx, &w, now, false, referenceCause); err != nil {
			return err
		}
		done = tx
		return nil
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}

// lockPending locks the wallet, then the operation, and rechecks under both
// locks: it must still be PENDING_REFERENCE and due. Another instance may have
// concluded it, or rescheduled it after our claim, and then counting a second
// attempt would shorten its wait (spec M6, decision 3). A nil operation means skip.
func lockPending(ctx context.Context, r Repos, ref PendingReference, now time.Time) (wallet.Wallet, *wagering.WagerTransaction, error) {
	w, err := r.Wallets().Lock(ctx, ref.WalletID)
	if err != nil {
		return wallet.Wallet{}, nil, err
	}
	tx, err := r.Transactions().Lock(ctx, ref.ID)
	if errors.Is(err, ErrNotFound) {
		return wallet.Wallet{}, nil, nil
	}
	if err != nil {
		return wallet.Wallet{}, nil, err
	}
	if tx.Status() != wagering.StatusPendingReference || tx.NextAttemptAt().After(now) {
		return wallet.Wallet{}, nil, nil
	}
	return w, tx, nil
}

// resultOf reads the outcome from the state settleAndPersist left.
func resultOf(tx *wagering.WagerTransaction) ResolveResult {
	res := ResolveResult{TransactionID: tx.ID(), WalletID: tx.WalletID(), Kind: tx.Kind(), Attempts: tx.Attempts()}
	switch tx.Status() {
	case wagering.StatusProcessed:
		res.Outcome = ResolveProcessed
	case wagering.StatusRejected:
		res.Outcome, res.FailureCode = ResolveRejected, tx.FailureCode()
	case wagering.StatusFailed: // recordFailure builds its own result; kept for exhaustiveness
		res.Outcome = ResolveFailed
	case wagering.StatusPending, wagering.StatusPendingReference: // PENDING never persists (D-05)
		res.Outcome = ResolveRescheduled
	}
	return res
}

// recordFailure writes the operation as FAILED, in a transaction of its own
// (lock order and recheck as in Resolve), and advances the operations waiting
// for it: no entry, no event (D-05, lifecycle §5.2). If even that cannot be
// written, the failure is transient and the operation stays due.
func (r *ResolveReferences) recordFailure(ctx context.Context, ref PendingReference, cause error) (ResolveResult, error) {
	p := r.p
	now := p.clock.Now()
	var failed *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(repos Repos) error {
		_, tx, err := lockPending(ctx, repos, ref, now)
		if err != nil || tx == nil {
			return err
		}
		if err := tx.Fail(now); err != nil {
			return domainError(err)
		}
		if err := repos.Transactions().Update(ctx, tx); err != nil {
			return err
		}
		if _, err := repos.Transactions().AdvanceDependents(ctx, tx.ProviderID(), tx.ExternalTransactionID(), now); err != nil {
			return err
		}
		failed = tx
		return nil
	})
	if err != nil {
		return ResolveResult{}, apperrors.New(apperrors.KindTransient, "", fmt.Errorf("app: recording FAILED: %w (after %w)", err, cause))
	}
	if failed == nil { // concluded or rescheduled by another instance meanwhile
		return ResolveResult{Outcome: ResolveSkipped, TransactionID: ref.ID, WalletID: ref.WalletID}, nil
	}
	p.log.ErrorContext(ctx, "permanent failure recorded",
		"transactionId", failed.ID(), "walletId", failed.WalletID(), "providerId", failed.ProviderID(),
		"correlationId", failed.CorrelationID(), "error", cause.Error())
	return ResolveResult{Outcome: ResolveFailed, TransactionID: failed.ID(), WalletID: failed.WalletID(), Kind: failed.Kind(), Attempts: failed.Attempts()}, nil
}
