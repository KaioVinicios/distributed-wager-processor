package wagering

import (
	"errors"
	"fmt"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Reference is what the caller found for the operation's reference, looked up
// by (providerId, referenceExternalTransactionId) while holding the wallet lock.
type Reference struct {
	// Tx is the referenced transaction; nil when not found.
	Tx *WagerTransaction
	// AlreadyReversed tells whether Tx already has a PROCESSED REFUND or
	// ROLLBACK (R7, D-10).
	AlreadyReversed bool
}

// SettleParams are the inputs of Settle that do not come from the database.
type SettleParams struct {
	// EntryID identifies the ledger entry, used only when there is a movement.
	EntryID string
	Now     time.Time
	Policy  ReferenceRetryPolicy
}

// Outcome is what the caller persists: the ledger entry (nil when the balance
// did not change) and the events for the outbox. The transaction and the wallet
// passed to Settle carry the new state.
type Outcome struct {
	Entry  *wallet.LedgerEntry
	Events []events.Event
}

// Settle evaluates an external operation against its locked wallet (lifecycle
// §3.4 steps 12–15 and §4 R1–R8) and applies the result: PROCESSED (with the
// movement done by the wallet aggregate), REJECTED or PENDING_REFERENCE. HTTP,
// SQS and the reference worker call it the same way; re-evaluating steps 12 and
// 13 in the worker is harmless because their data never changes.
//
// Business outcomes are not errors. An error means a broken precondition or
// invariant (a caller bug or corrupted data); then neither the transaction nor
// the wallet changes.
func Settle(tx *WagerTransaction, w *wallet.Wallet, ref Reference, p SettleParams) (Outcome, error) {
	if err := settlePreconditions(tx, w, ref); err != nil {
		return Outcome{}, err
	}
	reject := func(code FailureCode) (Outcome, error) {
		evs, err := tx.Reject(code, w.Balance(), p.Now)
		return Outcome{Events: evs}, err
	}

	// Steps 12 and 13.
	if tx.playerID != w.PlayerID() {
		return reject(FailurePlayerWalletMismatch)
	}
	if tx.money.Currency() != w.Currency() {
		return reject(FailureCurrencyMismatch)
	}

	// Step 14: reference resolution.
	var resolved *WagerTransaction
	if tx.hasReference() {
		r := ref.Tx
		if r == nil || r.status == StatusPendingReference || r.status == StatusPending { // R1, R2
			return settleUnresolved(tx, w, p)
		}
		if code := tx.referenceRejection(r); code != "" { // R3–R6
			return reject(code)
		}
		if tx.kind.isReversal() && ref.AlreadyReversed { // R7
			return reject(FailureAlreadyReversed)
		}
		resolved = r // R8
	}

	// Step 15: movement.
	direction, moves := tx.movement(resolved)
	if !moves { // LOSS: no entry, no new version
		evs, err := tx.Process(ProcessParams{Reference: resolved, Balance: w.Balance(), WalletVersion: w.Version(), Now: p.Now})
		return Outcome{Events: evs}, err
	}
	next := *w // work on a copy: the wallet only changes if Process succeeds
	var entry wallet.LedgerEntry
	var err error
	if direction == wallet.DirectionDebit {
		entry, err = next.Debit(p.EntryID, tx.id, tx.money, p.Now)
	} else {
		entry, err = next.Credit(p.EntryID, tx.id, tx.money, p.Now)
	}
	if errors.Is(err, wallet.ErrInsufficientFunds) {
		if tx.kind == KindRollback {
			return reject(FailureReversalInsufficientFunds)
		}
		return reject(FailureInsufficientFunds)
	}
	if err != nil {
		return Outcome{}, err
	}
	evs, err := tx.Process(ProcessParams{
		Entry: &entry, Reference: resolved, Balance: entry.BalanceAfter(), WalletVersion: entry.WalletVersion(), Now: p.Now,
	})
	if err != nil {
		return Outcome{}, err
	}
	*w = next
	return Outcome{Entry: &entry, Events: evs}, nil
}

// settleUnresolved handles R1/R2: start waiting, retry later or expire.
func settleUnresolved(tx *WagerTransaction, w *wallet.Wallet, p SettleParams) (Outcome, error) {
	if tx.status == StatusPending {
		evs, err := tx.AwaitReference(p.Now, p.Policy)
		return Outcome{Events: evs}, err
	}
	err := tx.RescheduleReference(p.Now, p.Policy)
	if errors.Is(err, ErrReferenceExpired) {
		evs, err := tx.Reject(FailureReferenceNotFound, w.Balance(), p.Now)
		return Outcome{Events: evs}, err
	}
	return Outcome{}, err
}

func settlePreconditions(tx *WagerTransaction, w *wallet.Wallet, ref Reference) error {
	switch {
	case !tx.initialized():
		return fmt.Errorf("%w: wager transaction", ErrUninitialized)
	case tx.status != StatusPending && tx.status != StatusPendingReference:
		return fmt.Errorf("%w: settle from %s", ErrInvalidTransition, tx.status)
	case tx.origin != OriginExternal:
		return fmt.Errorf("%w: the opening is settled by OpenWallet", ErrInvalidTransition)
	case w == nil || w.ID() == "":
		return fmt.Errorf("%w: wallet", ErrUninitialized)
	case tx.walletID != w.ID():
		return invalidArg("the wallet is not the operation's wallet")
	case ref.Tx != nil && (!ref.Tx.initialized() || ref.Tx.providerID != tx.providerID ||
		ref.Tx.externalTransactionID != tx.referenceExternalTransactionID):
		return invalidArg("the reference does not match (providerId, referenceExternalTransactionId)")
	}
	return nil
}
