//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: DB-01, DB-02, TX-05, TX-07, WAL-06, IDEM-08, DOM-02, DOM-03 (I18: transactions and ledger writes)
func TestTransactionRepository(t *testing.T) {
	t.Parallel()
	w, opening := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })

	t.Run("round trip of every persisted state", func(t *testing.T) {
		ctx := t.Context()
		wantStored(t, opening.Tx) // INTERNAL OPENING, PROCESSED
		for _, tc := range []struct {
			kind        wagering.Kind
			amount, ext string
			ref         string
			status      wagering.Status
			failure     wagering.FailureCode
		}{
			{wagering.KindBet, "30.00", "bet-1", "", wagering.StatusProcessed, ""},
			{wagering.KindLoss, "0.00", "loss-1", "", wagering.StatusProcessed, ""},
			{wagering.KindBet, "500.00", "bet-2", "", wagering.StatusRejected, wagering.FailureInsufficientFunds},
			// The observed balance is the wallet's (BRL), not the operation's currency.
			{wagering.KindBet, "10.00 USD", "bet-3", "", wagering.StatusRejected, wagering.FailureCurrencyMismatch},
			{wagering.KindRefund, "30.00", "refund-1", "bet-missing", wagering.StatusPendingReference, ""},
		} {
			tx := process(t, command(t, w, p, tc.kind, tc.amount, tc.ext, tc.ref))
			if tx.Status() != tc.status || tx.FailureCode() != tc.failure {
				t.Fatalf("%s: %s %s, want %s %s", tc.ext, tx.Status(), tx.FailureCode(), tc.status, tc.failure)
			}
			wantStored(t, tx)
		}

		failed, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindBet, "5.00", "bet-4", ""), wagering.ReceivedViaSQS, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := failed.Fail(time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Insert(ctx, failed) }); err != nil {
			t.Fatalf("insert FAILED: %v", err)
		}
		wantStored(t, failed)
	})

	t.Run("pending operation is updated until terminal", func(t *testing.T) {
		ctx := t.Context()
		tx := process(t, command(t, w, p, wagering.KindRefund, "30.00", "refund-2", "bet-never"))
		update := func() error {
			return newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, tx) })
		}
		if err := tx.RescheduleReference(time.Now(), policy); err != nil {
			t.Fatal(err)
		}
		if err := update(); err != nil {
			t.Fatalf("Update rescheduled: %v", err)
		}
		wantStored(t, tx)
		if _, err := tx.Reject(wagering.FailureReferenceNotFound, w.Balance(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := update(); err != nil {
			t.Fatalf("Update rejected: %v", err)
		}
		wantStored(t, tx)
		// The row is terminal now: the wager_tx_guard trigger refuses any change (TX-07).
		wantKind(t, update(), apperrors.KindPermanent, nil)
	})

	t.Run("not found", func(t *testing.T) {
		ctx := t.Context()
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := reads().Transactions().Get(ctx, id)
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})

	t.Run("values that cannot be written", func(t *testing.T) {
		ctx := t.Context()
		pending, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindBet, "1.00", "bet-5", ""), wagering.ReceivedViaHTTP, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string]struct {
			write  func(r app.Repos) error
			target error
		}{
			"PENDING is never persisted": {func(r app.Repos) error { return r.Transactions().Insert(ctx, pending) }, wagering.ErrNotPersistable},
			"nil operation":              {func(r app.Repos) error { return r.Transactions().Insert(ctx, nil) }, wagering.ErrUninitialized},
			"nil operation update":       {func(r app.Repos) error { return r.Transactions().Update(ctx, nil) }, wagering.ErrUninitialized},
			"zero ledger entry":          {func(r app.Repos) error { return r.Ledger().Insert(ctx, wallet.LedgerEntry{}) }, wallet.ErrInvalidLedgerEntry},
			"zero wallet update":         {func(r app.Repos) error { return r.Wallets().UpdateBalance(ctx, wallet.Wallet{}) }, wallet.ErrUninitialized},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				wantKind(t, newUoW().Do(ctx, tc.write), apperrors.KindPermanent, tc.target)
			})
		}
	})

	t.Run("update of an operation that was never written", func(t *testing.T) {
		ctx := t.Context()
		tx, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindRefund, "1.00", "refund-3", "bet-x"), wagering.ReceivedViaHTTP, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.AwaitReference(time.Now(), policy); err != nil {
			t.Fatal(err)
		}
		err = newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, tx) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})

	t.Run("balance update on a stale version", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err := r.Wallets().Lock(ctx, w.ID())
			if err != nil {
				return err
			}
			for range 2 { // two versions ahead of the stored one
				if _, err := locked.Credit(newID(), newID(), brl(t, "1.00"), time.Now()); err != nil {
					return err
				}
			}
			return r.Wallets().UpdateBalance(ctx, locked)
		})
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}
