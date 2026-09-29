package wagering

import (
	"fmt"
	"slices"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// ProcessParams are the results of a successful operation.
type ProcessParams struct {
	// Entry is the ledger entry of the movement: required when the kind moves
	// the balance, nil for LOSS.
	Entry *wallet.LedgerEntry
	// Reference is the resolved PROCESSED reference: required for REFUND,
	// ROLLBACK and WIN with a reference, nil otherwise.
	Reference *WagerTransaction
	// Balance and WalletVersion are the wallet state after the operation (the
	// entry's when there is one, the current one for LOSS).
	Balance       money.Money
	WalletVersion int64
	Now           time.Time
}

// Process completes the operation: PENDING/PENDING_REFERENCE → PROCESSED. It
// returns WagerTransactionProcessed, plus WalletBalanceChanged when there was
// a movement (lifecycle §7). Arguments that would break an invariant are
// rejected with ErrInvalidArgument, and nothing changes on error.
func (t *WagerTransaction) Process(p ProcessParams) ([]events.Event, error) {
	if err := t.guard("process", StatusPending, StatusPendingReference); err != nil {
		return nil, err
	}
	if err := t.checkProcessReference(p.Reference); err != nil {
		return nil, err
	}
	direction, moves := t.movement(p.Reference)
	if err := t.checkProcessEntry(p, direction, moves); err != nil {
		return nil, err
	}
	if !nonNegative(p.Balance) || p.WalletVersion < 1 || p.Now.IsZero() {
		return nil, invalidArg("balance, wallet version and now are required")
	}
	now := normalize(p.Now)
	refID := ""
	if p.Reference != nil {
		refID = p.Reference.id
	}
	processed, err := t.processedEvent(p.Balance, p.WalletVersion, refID, now)
	if err != nil {
		return nil, err
	}
	out := []events.Event{processed}
	if moves {
		changed, err := t.balanceChangedEvent(*p.Entry)
		if err != nil {
			return nil, err
		}
		out = append(out, changed)
	}
	t.status, t.referenceTransactionID, t.resultBalance = StatusProcessed, refID, p.Balance
	t.completedAt, t.updatedAt = now, notBefore(now, t.createdAt)
	return out, nil
}

// Reject records a business rejection (lifecycle §5.1): PENDING/
// PENDING_REFERENCE → REJECTED, with the balance observed at that moment. It
// returns WagerTransactionRejected.
func (t *WagerTransaction) Reject(code FailureCode, observed money.Money, now time.Time) ([]events.Event, error) {
	if err := t.guard("reject", StatusPending, StatusPendingReference); err != nil {
		return nil, err
	}
	if t.origin != OriginExternal {
		return nil, fmt.Errorf("%w: only external operations are rejected", ErrInvalidTransition)
	}
	if !code.IsRejection() {
		return nil, invalidArg("code must be a business rejection (lifecycle §5.1)")
	}
	if !nonNegative(observed) || now.IsZero() {
		return nil, invalidArg("observed balance and now are required")
	}
	now = normalize(now)
	rejected, err := events.NewWagerTransactionRejected(events.WagerTransactionRejected{
		TransactionID: t.id, Kind: string(t.kind), WalletID: t.walletID, PlayerID: t.playerID,
		ProviderID: t.providerID, ExternalTransactionID: t.externalTransactionID,
		RoundID: t.roundID, GameID: t.gameID, Money: t.money,
		FailureCode: string(code), FailureCategory: string(code.Category()), Balance: observed,
		ReferenceExternalTransactionID: t.referenceExternalTransactionID, RejectedAt: events.NewTime(now),
	})
	if err != nil {
		return nil, err
	}
	t.status, t.failureCode, t.resultBalance = StatusRejected, code, observed
	t.completedAt, t.updatedAt = now, notBefore(now, t.createdAt)
	return []events.Event{rejected}, nil
}

// AwaitReference starts waiting for a reference that is missing or still
// pending: PENDING → PENDING_REFERENCE, attempts = 0, first retry after
// policy.Delay(0), expiry at createdAt + TTL. It returns
// WagerTransactionPendingReference, emitted only once (D-11).
func (t *WagerTransaction) AwaitReference(now time.Time, policy ReferenceRetryPolicy) ([]events.Event, error) {
	if err := t.guard("await reference", StatusPending); err != nil {
		return nil, err
	}
	if !t.hasReference() {
		return nil, fmt.Errorf("%w: %s has no reference to await", ErrInvalidTransition, t.kind)
	}
	if !policy.valid() || now.IsZero() {
		return nil, invalidArg("policy and now are required")
	}
	now = normalize(now)
	next := normalize(now.Add(policy.Delay(0)))
	expires := normalize(t.createdAt.Add(policy.TTL()))
	pending, err := events.NewWagerTransactionPendingReference(events.WagerTransactionPendingReference{
		TransactionID: t.id, Kind: string(t.kind), WalletID: t.walletID, PlayerID: t.playerID,
		ProviderID: t.providerID, ExternalTransactionID: t.externalTransactionID,
		RoundID: t.roundID, GameID: t.gameID, Money: t.money,
		ReferenceExternalTransactionID: t.referenceExternalTransactionID,
		NextAttemptAt:                  events.NewTime(next), ExpiresAt: events.NewTime(expires), PendingAt: events.NewTime(now),
	})
	if err != nil {
		return nil, err
	}
	t.status, t.attempts, t.nextAttemptAt, t.expiresAt = StatusPendingReference, 0, next, expires
	t.updatedAt = notBefore(now, t.createdAt)
	return []events.Event{pending}, nil
}

// RescheduleReference records one more unsuccessful attempt:
// PENDING_REFERENCE → PENDING_REFERENCE, attempts++, next retry after
// policy.Delay(attempts). When the limit is exhausted it returns
// ErrReferenceExpired and changes nothing: the caller rejects with
// REFERENCE_NOT_FOUND. No event.
func (t *WagerTransaction) RescheduleReference(now time.Time, policy ReferenceRetryPolicy) error {
	if err := t.guard("reschedule reference", StatusPendingReference); err != nil {
		return err
	}
	if !policy.valid() || now.IsZero() {
		return invalidArg("policy and now are required")
	}
	if policy.Exhausted(t.attempts+1, t.expiresAt, now) {
		return ErrReferenceExpired
	}
	now = normalize(now)
	t.attempts++
	t.nextAttemptAt, t.updatedAt = normalize(now.Add(policy.Delay(t.attempts))), notBefore(now, t.createdAt)
	return nil
}

// Fail records a permanent infrastructure failure for audit (D-05):
// PENDING/PENDING_REFERENCE → FAILED with INTERNAL_PERMANENT_FAILURE. Only
// external operations fail this way: the database requires an INTERNAL
// operation to be PROCESSED, so a failed opening is a rollback. No event.
func (t *WagerTransaction) Fail(now time.Time) error {
	if err := t.guard("fail", StatusPending, StatusPendingReference); err != nil {
		return err
	}
	if t.origin != OriginExternal {
		return fmt.Errorf("%w: only external operations are recorded as FAILED", ErrInvalidTransition)
	}
	if now.IsZero() {
		return invalidArg("now is required")
	}
	now = normalize(now)
	t.status, t.failureCode, t.completedAt = StatusFailed, FailureInternalPermanentFailure, now
	t.updatedAt = notBefore(now, t.createdAt)
	return nil
}

func (t *WagerTransaction) guard(transition string, from ...Status) error {
	if !t.initialized() {
		return fmt.Errorf("%w: wager transaction", ErrUninitialized)
	}
	if !slices.Contains(from, t.status) {
		return fmt.Errorf("%w: %s from %s", ErrInvalidTransition, transition, t.status)
	}
	return nil
}

func (t *WagerTransaction) checkProcessReference(ref *WagerTransaction) error {
	if !t.hasReference() {
		if ref != nil {
			return invalidArg("this operation has no reference")
		}
		return nil
	}
	switch {
	case !ref.initialized():
		return invalidArg("the resolved reference is required")
	case ref.providerID != t.providerID || ref.externalTransactionID != t.referenceExternalTransactionID:
		return invalidArg("reference does not match referenceExternalTransactionId")
	case ref.status != StatusProcessed:
		return invalidArg("reference must be PROCESSED")
	}
	if code := t.referenceRejection(ref); code != "" {
		return invalidArg("reference is not acceptable: " + string(code))
	}
	return nil
}

func (t *WagerTransaction) checkProcessEntry(p ProcessParams, direction wallet.Direction, moves bool) error {
	if !moves {
		if p.Entry != nil {
			return invalidArg("LOSS has no ledger entry")
		}
		return nil
	}
	e := p.Entry
	switch {
	case e == nil || e.ID() == "":
		return invalidArg("the ledger entry is required")
	case e.TransactionID() != t.id || e.WalletID() != t.walletID:
		return invalidArg("the ledger entry belongs to another transaction or wallet")
	case e.Amount() != t.money:
		return invalidArg("the ledger entry amount differs from the operation")
	case e.Direction() != direction:
		return invalidArg("the ledger entry direction does not match the kind")
	case p.Balance != e.BalanceAfter() || p.WalletVersion != e.WalletVersion():
		return invalidArg("balance and wallet version must match the ledger entry")
	}
	return nil
}

func (t *WagerTransaction) processedEvent(balance money.Money, version int64, refID string, at time.Time) (events.Event, error) {
	e := events.WagerTransactionProcessed{
		TransactionID: t.id, Origin: string(t.origin), Kind: string(t.kind),
		WalletID: t.walletID, PlayerID: t.playerID, Money: t.money,
		BalanceAfter: balance, WalletVersion: version, ReferenceTransactionID: refID,
		ProcessedAt: events.NewTime(at),
	}
	if t.origin == OriginExternal {
		e.ProviderID, e.ExternalTransactionID = t.providerID, t.externalTransactionID
		e.RoundID, e.GameID = t.roundID, t.gameID
		e.ReferenceExternalTransactionID = t.referenceExternalTransactionID
	}
	return events.NewWagerTransactionProcessed(e)
}

func (t *WagerTransaction) balanceChangedEvent(e wallet.LedgerEntry) (events.Event, error) {
	return events.NewWalletBalanceChanged(events.WalletBalanceChanged{
		WalletID: e.WalletID(), TransactionID: e.TransactionID(), TransactionKind: string(t.kind),
		Direction: string(e.Direction()), Money: e.Amount(),
		BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(),
		WalletVersion: e.WalletVersion(), ChangedAt: events.NewTime(e.CreatedAt()),
	})
}
