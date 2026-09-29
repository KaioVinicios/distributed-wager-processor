package wagering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func testPolicy(t *testing.T) wagering.ReferenceRetryPolicy {
	t.Helper()
	p, err := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 8, 10*time.Minute, noJitter)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func openWallet(t *testing.T, balance string) wallet.Wallet {
	t.Helper()
	w, err := wallet.Open(walletID, playerID, brl(t, balance), t0)
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}
	return w
}

// credited returns the entry and params of a valid Process for a crediting tx.
func credited(t *testing.T, tx *wagering.WagerTransaction, ref *wagering.WagerTransaction) wagering.ProcessParams {
	t.Helper()
	w := openWallet(t, "100.00")
	e, err := w.Credit(entryID, tx.ID(), tx.Money(), t0)
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}
	return wagering.ProcessParams{Entry: &e, Reference: ref, Balance: e.BalanceAfter(), WalletVersion: e.WalletVersion(), Now: t0}
}

func eventTypes(evs []events.Event) []events.Type {
	out := make([]events.Type, len(evs))
	for i, e := range evs {
		out[i] = e.Type()
	}
	return out
}

func TestTransactionStateMachine(t *testing.T) {
	// Covers: TST-U03, TX-06, TX-07, DOM-01, DOM-04
	betRef := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	policy := testPolicy(t)

	// Each "from" state is reached through the transitions, from a REFUND.
	from := map[wagering.Status]func() *wagering.WagerTransaction{
		wagering.StatusPending: func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "25.00", refExtID) },
		wagering.StatusPendingReference: func() *wagering.WagerTransaction {
			tx := external(t, wagering.KindRefund, "25.00", refExtID)
			if _, err := tx.AwaitReference(t0, policy); err != nil {
				t.Fatal(err)
			}
			return tx
		},
		wagering.StatusProcessed: func() *wagering.WagerTransaction {
			tx := external(t, wagering.KindRefund, "25.00", refExtID)
			if _, err := tx.Process(credited(t, tx, betRef)); err != nil {
				t.Fatal(err)
			}
			return tx
		},
		wagering.StatusRejected: func() *wagering.WagerTransaction {
			tx := external(t, wagering.KindRefund, "25.00", refExtID)
			if _, err := tx.Reject(wagering.FailureAlreadyReversed, brl(t, "100.00"), t0); err != nil {
				t.Fatal(err)
			}
			return tx
		},
		wagering.StatusFailed: func() *wagering.WagerTransaction {
			tx := external(t, wagering.KindRefund, "25.00", refExtID)
			if err := tx.Fail(t0); err != nil {
				t.Fatal(err)
			}
			return tx
		},
	}
	transitions := map[wagering.Status]func(tx *wagering.WagerTransaction) error{
		wagering.StatusProcessed: func(tx *wagering.WagerTransaction) error {
			_, err := tx.Process(credited(t, tx, betRef))
			return err
		},
		wagering.StatusRejected: func(tx *wagering.WagerTransaction) error {
			_, err := tx.Reject(wagering.FailureAlreadyReversed, brl(t, "100.00"), t0)
			return err
		},
		wagering.StatusPendingReference: func(tx *wagering.WagerTransaction) error {
			if tx.Status() == wagering.StatusPendingReference {
				return tx.RescheduleReference(t0, policy)
			}
			_, err := tx.AwaitReference(t0, policy)
			return err
		},
		wagering.StatusFailed: func(tx *wagering.WagerTransaction) error { return tx.Fail(t0) },
	}
	allowed := map[wagering.Status][]wagering.Status{
		wagering.StatusPending:          {wagering.StatusProcessed, wagering.StatusRejected, wagering.StatusPendingReference, wagering.StatusFailed},
		wagering.StatusPendingReference: {wagering.StatusProcessed, wagering.StatusRejected, wagering.StatusPendingReference, wagering.StatusFailed},
	}

	for src, build := range from {
		for dst, apply := range transitions {
			tx := build()
			err := apply(tx)
			ok := false
			for _, a := range allowed[src] {
				ok = ok || a == dst
			}
			if ok {
				if err != nil || tx.Status() != dst {
					t.Errorf("%s → %s: status %s, error %v; want allowed", src, dst, tx.Status(), err)
				}
				continue
			}
			if !errors.Is(err, wagering.ErrInvalidTransition) || tx.Status() != src {
				t.Errorf("%s → %s: status %s, error %v; want ErrInvalidTransition", src, dst, tx.Status(), err)
			}
		}
	}

	opening, err := wagering.NewOpening(txID, walletID, playerID, brl(t, "1.00"), correlation, t0)
	if err != nil || opening.Status() != wagering.StatusPending {
		t.Fatalf("NewOpening must start PENDING: %v", err)
	}
}

func TestTransitionResults(t *testing.T) {
	// Covers: TST-U03, TX-03, OPS-12, OUT-08
	policy := testPolicy(t)

	refund := external(t, wagering.KindRefund, "25.00", refExtID)
	evs, err := refund.AwaitReference(t0.Add(time.Second), policy)
	if err != nil || len(evs) != 1 {
		t.Fatalf("AwaitReference: %v, %d events", err, len(evs))
	}
	pending, ok := evs[0].(events.WagerTransactionPendingReference)
	if !ok || refund.Attempts() != 0 ||
		!refund.NextAttemptAt().Equal(utc(t0).Add(2*time.Second)) || !refund.ExpiresAt().Equal(utc(t0).Add(10*time.Minute)) ||
		!pending.NextAttemptAt.Equal(refund.NextAttemptAt()) || !pending.ExpiresAt.Equal(refund.ExpiresAt()) {
		t.Fatalf("AwaitReference: %+v, events %v", refund, evs)
	}
	if err := refund.RescheduleReference(t0.Add(time.Minute), policy); err != nil {
		t.Fatal(err)
	}
	if refund.Attempts() != 1 || !refund.NextAttemptAt().Equal(utc(t0).Add(time.Minute+2*time.Second)) {
		t.Fatalf("RescheduleReference: attempts %d next %v", refund.Attempts(), refund.NextAttemptAt())
	}

	betRef := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	p := credited(t, refund, betRef)
	evs, err = refund.Process(p)
	if err != nil || len(evs) != 2 {
		t.Fatalf("Process: %v, %d events", err, len(evs))
	}
	processed, ok := evs[0].(events.WagerTransactionProcessed)
	changed, ok2 := evs[1].(events.WalletBalanceChanged)
	if !ok || !ok2 || refund.ReferenceTransactionID() != refTxID ||
		refund.ResultBalance() != brl(t, "125.00") || !refund.CompletedAt().Equal(utc(t0)) ||
		processed.ReferenceTransactionID != refTxID || processed.WalletVersion != 2 ||
		changed.Direction != "CREDIT" || changed.BalanceBefore != brl(t, "100.00") || changed.TransactionKind != "REFUND" {
		t.Fatalf("Process: %+v, events %+v", refund, evs)
	}

	bet := external(t, wagering.KindBet, "80.00", "")
	evs, err = bet.Reject(wagering.FailureInsufficientFunds, brl(t, "20.00"), t0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Reject: %v, %d events", err, len(evs))
	}
	rejected, ok := evs[0].(events.WagerTransactionRejected)
	if !ok || bet.FailureCode() != wagering.FailureInsufficientFunds || bet.ResultBalance() != brl(t, "20.00") ||
		rejected.FailureCategory != "DEFINITIVE" || rejected.Balance != brl(t, "20.00") {
		t.Fatalf("Reject: %+v, %v", bet, evs)
	}

	failed := external(t, wagering.KindBet, "80.00", "")
	if err := failed.Fail(t0); err != nil || failed.FailureCode() != wagering.FailureInternalPermanentFailure ||
		failed.ResultBalance() != (money.Money{}) || failed.CompletedAt().IsZero() {
		t.Fatalf("Fail: %+v, %v", failed, err)
	}
}

func TestTransitionArgumentValidation(t *testing.T) {
	// Covers: DOM-01, LED-05, OPS-04, OPS-05, OPS-07
	policy := testPolicy(t)
	betRef := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	winRef := rehydrate(t, referenceSnapshot(t, wagering.KindWin, "25.00"))
	debitFor := func(tx *wagering.WagerTransaction) wagering.ProcessParams {
		w := openWallet(t, "100.00")
		e, err := w.Debit(entryID, tx.ID(), tx.Money(), t0)
		if err != nil {
			t.Fatal(err)
		}
		return wagering.ProcessParams{Entry: &e, Balance: e.BalanceAfter(), WalletVersion: e.WalletVersion(), Now: t0}
	}

	tests := map[string]struct {
		tx   func() *wagering.WagerTransaction
		op   func(tx *wagering.WagerTransaction) error
		want error
	}{
		"BET without entry": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				_, err := tx.Process(wagering.ProcessParams{Balance: brl(t, "100.00"), WalletVersion: 1, Now: t0})
				return err
			}, wagering.ErrInvalidArgument,
		},
		"BET with LedgerEntry{}": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				p := debitFor(tx)
				p.Entry = &wallet.LedgerEntry{}
				_, err := tx.Process(p)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"BET with a credit entry": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error { _, err := tx.Process(credited(t, tx, nil)); return err },
			wagering.ErrInvalidArgument,
		},
		"entry of another transaction": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				w := openWallet(t, "100.00")
				e, _ := w.Debit(entryID, otherID, tx.Money(), t0)
				_, err := tx.Process(wagering.ProcessParams{Entry: &e, Balance: e.BalanceAfter(), WalletVersion: 2, Now: t0})
				return err
			}, wagering.ErrInvalidArgument,
		},
		"balance different from the entry": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				p := debitFor(tx)
				p.Balance = brl(t, "100.00")
				_, err := tx.Process(p)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"LOSS with an entry": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindLoss, "0.00", "") },
			func(tx *wagering.WagerTransaction) error {
				p := debitFor(external(t, wagering.KindBet, "25.00", ""))
				_, err := tx.Process(p)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"REFUND without reference": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "25.00", refExtID) },
			func(tx *wagering.WagerTransaction) error { _, err := tx.Process(credited(t, tx, nil)); return err },
			wagering.ErrInvalidArgument,
		},
		"REFUND of a WIN": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "25.00", refExtID) },
			func(tx *wagering.WagerTransaction) error { _, err := tx.Process(credited(t, tx, winRef)); return err },
			wagering.ErrInvalidArgument,
		},
		"REFUND with another amount": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "30.00", refExtID) },
			func(tx *wagering.WagerTransaction) error { _, err := tx.Process(credited(t, tx, betRef)); return err },
			wagering.ErrInvalidArgument,
		},
		"reference that is not PROCESSED": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "25.00", refExtID) },
			func(tx *wagering.WagerTransaction) error {
				s := referenceSnapshot(t, wagering.KindBet, "25.00")
				s.Status, s.FailureCode = wagering.StatusRejected, wagering.FailureInsufficientFunds
				_, err := tx.Process(credited(t, tx, rehydrate(t, s)))
				return err
			}, wagering.ErrInvalidArgument,
		},
		"ROLLBACK of a BET debiting": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRollback, "25.00", refExtID) },
			func(tx *wagering.WagerTransaction) error {
				p := debitFor(tx)
				p.Reference = betRef
				_, err := tx.Process(p)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"BET with a reference": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				p := debitFor(tx)
				p.Reference = betRef
				_, err := tx.Process(p)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"reject with the permanent failure code": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error {
				_, err := tx.Reject(wagering.FailureInternalPermanentFailure, brl(t, "1.00"), t0)
				return err
			}, wagering.ErrInvalidArgument,
		},
		"reject with an unknown code": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error { _, err := tx.Reject("NOPE", brl(t, "1.00"), t0); return err },
			wagering.ErrInvalidArgument,
		},
		"await with a zero policy": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindRefund, "25.00", refExtID) },
			func(tx *wagering.WagerTransaction) error {
				_, err := tx.AwaitReference(t0, wagering.ReferenceRetryPolicy{})
				return err
			}, wagering.ErrInvalidArgument,
		},
		"BET cannot await a reference": {
			func() *wagering.WagerTransaction { return external(t, wagering.KindBet, "25.00", "") },
			func(tx *wagering.WagerTransaction) error { _, err := tx.AwaitReference(t0, policy); return err },
			wagering.ErrInvalidTransition,
		},
		"OPENING cannot be rejected": {
			func() *wagering.WagerTransaction {
				tx, _ := wagering.NewOpening(txID, walletID, playerID, brl(t, "1.00"), correlation, t0)
				return tx
			},
			func(tx *wagering.WagerTransaction) error {
				_, err := tx.Reject(wagering.FailureInsufficientFunds, brl(t, "1.00"), t0)
				return err
			},
			wagering.ErrInvalidTransition,
		},
		"OPENING cannot fail": {
			func() *wagering.WagerTransaction {
				tx, _ := wagering.NewOpening(txID, walletID, playerID, brl(t, "1.00"), correlation, t0)
				return tx
			},
			func(tx *wagering.WagerTransaction) error { return tx.Fail(t0) },
			wagering.ErrInvalidTransition,
		},
	}
	for name, tc := range tests {
		tx := tc.tx()
		if err := tc.op(tx); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.want)
		}
		if tx.Status() != wagering.StatusPending || !tx.CompletedAt().IsZero() || tx.ResultBalance() != (money.Money{}) {
			t.Errorf("%s: a failed transition changed the state: %+v", name, tx)
		}
	}

	expiring := external(t, wagering.KindRefund, "25.00", refExtID)
	if _, err := expiring.AwaitReference(t0, policy); err != nil {
		t.Fatal(err)
	}
	if err := expiring.RescheduleReference(t0.Add(time.Hour), policy); !errors.Is(err, wagering.ErrReferenceExpired) ||
		expiring.Attempts() != 0 || expiring.Status() != wagering.StatusPendingReference {
		t.Fatalf("reschedule after the TTL: %v, attempts %d", err, expiring.Attempts())
	}
}

func TestZeroValuesRejected(t *testing.T) {
	// Covers: DOM-03
	policy := testPolicy(t)
	for name, tx := range map[string]*wagering.WagerTransaction{"WagerTransaction{}": {}, "nil": nil} {
		ops := map[string]func() error{
			"Process": func() error {
				_, err := tx.Process(wagering.ProcessParams{Balance: brl(t, "1.00"), WalletVersion: 1, Now: t0})
				return err
			},
			"Reject": func() error {
				_, err := tx.Reject(wagering.FailureInsufficientFunds, brl(t, "1.00"), t0)
				return err
			},
			"AwaitReference":      func() error { _, err := tx.AwaitReference(t0, policy); return err },
			"RescheduleReference": func() error { return tx.RescheduleReference(t0, policy) },
			"Fail":                func() error { return tx.Fail(t0) },
			"Snapshot":            func() error { _, err := tx.Snapshot(); return err },
		}
		for op, run := range ops {
			if err := run(); !errors.Is(err, wagering.ErrUninitialized) {
				t.Errorf("%s.%s: error = %v, want ErrUninitialized", name, op, err)
			}
		}
	}
}

func TestTransitionClockSkew(t *testing.T) {
	// Covers: DOM-01, E7
	// Another instance's clock may lag behind the creation instant: the
	// transition still succeeds and the state still rehydrates.
	earlier := t0.Add(-time.Second)
	policy := testPolicy(t)
	betRef := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	transitions := map[string]func(tx *wagering.WagerTransaction) error{
		"Process": func(tx *wagering.WagerTransaction) error {
			p := credited(t, tx, betRef)
			p.Now = earlier
			_, err := tx.Process(p)
			return err
		},
		"Reject": func(tx *wagering.WagerTransaction) error {
			_, err := tx.Reject(wagering.FailureAlreadyReversed, brl(t, "1.00"), earlier)
			return err
		},
		"AwaitReference": func(tx *wagering.WagerTransaction) error { _, err := tx.AwaitReference(earlier, policy); return err },
		"Fail":           func(tx *wagering.WagerTransaction) error { return tx.Fail(earlier) },
	}
	for name, apply := range transitions {
		tx := external(t, wagering.KindRefund, "25.00", refExtID)
		if err := apply(tx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tx.UpdatedAt().Before(tx.CreatedAt()) {
			t.Errorf("%s: updatedAt %v precedes createdAt %v", name, tx.UpdatedAt(), tx.CreatedAt())
		}
		s, err := tx.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wagering.Rehydrate(s); err != nil {
			t.Errorf("%s: the state no longer rehydrates: %v", name, err)
		}
	}
}
