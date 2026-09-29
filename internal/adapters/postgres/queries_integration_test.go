//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: IDEM-02, IDEM-05, IDEM-06, IDEM-07, OPS-06, OPS-08, OPS-12, E5, E6 (I18: transaction queries)
func TestTransactionQueries(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	bet := process(t, command(t, w, p, wagering.KindBet, "20.00", "bet-1", ""))

	t.Run("idempotency lookups", func(t *testing.T) {
		ctx := t.Context()
		byKey, err := reads().Transactions().FindByIdempotencyKey(ctx, p, bet.IdempotencyKey())
		if err != nil || byKey == nil || txSnapshot(t, byKey) != txSnapshot(t, bet) {
			t.Fatalf("FindByIdempotencyKey = %v, %v; want the BET", byKey, err)
		}
		byExt, err := reads().Transactions().FindByExternalID(ctx, p, "bet-1")
		if err != nil || byExt == nil || byExt.ID() != bet.ID() {
			t.Fatalf("FindByExternalID = %v, %v; want the BET", byExt, err)
		}
		for name, find := range map[string]func() (*wagering.WagerTransaction, error){
			"unknown key": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByIdempotencyKey(ctx, p, "unknown")
			},
			"key of another provider": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByIdempotencyKey(ctx, newProvider(), bet.IdempotencyKey())
			},
			"unknown external id": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByExternalID(ctx, p, "unknown")
			},
			"external id of another provider": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByExternalID(ctx, newProvider(), "bet-1")
			},
		} {
			if got, err := find(); got != nil || err != nil {
				t.Errorf("%s: %v, %v; want nil, nil", name, got, err)
			}
		}
	})

	t.Run("concurrent insert of the same operation", func(t *testing.T) {
		ctx := t.Context()
		sameKey := command(t, w, p, wagering.KindBet, "20.00", "bet-1", "")
		otherKey := command(t, w, p, wagering.KindBet, "20.00", "bet-1", "")
		for name, cmd := range map[string]wagering.Command{"same key": sameKey, "same external id": otherKey} {
			if name == "same external id" {
				cmd = withKey(t, cmd, "another-key-"+newID())
			}
			tx, err := wagering.NewExternal(newID(), cmd, wagering.ReceivedViaSQS, "corr", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Fail(time.Now()); err != nil { // any persistable state; the unique index fires first
				t.Fatal(err)
			}
			err = newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Insert(ctx, tx) })
			wantKind(t, err, apperrors.KindTransient, app.ErrIdempotencyRace)
		}
	})

	t.Run("reference resolution", func(t *testing.T) {
		ctx := t.Context()
		ref, err := reads().Transactions().FindReference(ctx, p, "bet-missing")
		if err != nil || ref.Tx != nil || ref.AlreadyReversed {
			t.Fatalf("FindReference(missing) = %+v, %v; want Reference{}", ref, err)
		}
		ref, err = reads().Transactions().FindReference(ctx, p, "bet-1")
		if err != nil || ref.Tx == nil || ref.Tx.ID() != bet.ID() || ref.AlreadyReversed {
			t.Fatalf("FindReference(bet-1) = %+v, %v; want the BET, not reversed", ref, err)
		}
		refund := process(t, command(t, w, p, wagering.KindRefund, "20.00", "refund-1", "bet-1"))
		if refund.Status() != wagering.StatusProcessed {
			t.Fatalf("refund %s %s, want PROCESSED", refund.Status(), refund.FailureCode())
		}
		ref, err = reads().Transactions().FindReference(ctx, p, "bet-1")
		if err != nil || !ref.AlreadyReversed {
			t.Fatalf("FindReference(bet-1) after the REFUND = %+v, %v; want AlreadyReversed", ref, err)
		}
	})

	t.Run("concurrent reversal of the same reference", func(t *testing.T) {
		ctx := t.Context()
		// A second REFUND settled on a stale read (AlreadyReversed = false), as
		// a racing transaction would without the wallet lock: the partial
		// unique index stops it (D-10).
		err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err := r.Wallets().Lock(ctx, w.ID())
			if err != nil {
				return err
			}
			tx, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindRefund, "20.00", "refund-2", "bet-1"), wagering.ReceivedViaHTTP, "corr", time.Now())
			if err != nil {
				return err
			}
			out, err := wagering.Settle(tx, &locked, wagering.Reference{Tx: bet}, wagering.SettleParams{EntryID: newID(), Now: time.Now(), Policy: policy})
			if err != nil {
				return err
			}
			if err := r.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
			return persistOutcome(ctx, r, locked, out, "corr")
		})
		wantKind(t, err, apperrors.KindTransient, app.ErrReversalRace)
	})

	t.Run("dependents are advanced with the creation floor", func(t *testing.T) {
		ctx := t.Context()
		// The pending REFUND was created by an instance whose clock is one hour
		// ahead; this instance advances it at its own now.
		ahead := time.Now().Add(time.Hour)
		pending, err := processWith(ctx, newUoW(), command(t, w, p, wagering.KindRefund, "5.00", "refund-3", "bet-late"), ahead, nil)
		if err != nil || pending.Status() != wagering.StatusPendingReference {
			t.Fatalf("pending refund: %v, %v", pending, err)
		}
		now := time.Now()
		var n int64
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			var err error
			n, err = r.Transactions().AdvanceDependents(ctx, p, "bet-late", now)
			return err
		}); err != nil || n != 1 {
			t.Fatalf("AdvanceDependents = %d, %v; want 1", n, err)
		}
		got, err := reads().Transactions().Get(ctx, pending.ID())
		if err != nil {
			t.Fatalf("Get after advancing: %v", err)
		}
		if want := now.UTC().Truncate(time.Microsecond); !got.NextAttemptAt().Equal(want) || !got.UpdatedAt().Equal(got.CreatedAt()) {
			t.Fatalf("next %v updated %v created %v; want next = %v and updated = created", got.NextAttemptAt(), got.UpdatedAt(), got.CreatedAt(), want)
		}
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			var err error
			n, err = r.Transactions().AdvanceDependents(ctx, newProvider(), "bet-late", now)
			return err
		}); err != nil || n != 0 {
			t.Fatalf("AdvanceDependents(another provider) = %d, %v; want 0", n, err)
		}
	})
}

// withKey rebuilds cmd with another idempotency key.
func withKey(t *testing.T, cmd wagering.Command, key string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr(key), ProviderID: ptr(cmd.ProviderID()), ExternalTransactionID: ptr(cmd.ExternalTransactionID()),
		PlayerID: ptr(cmd.PlayerID()), WalletID: ptr(cmd.WalletID()), RoundID: ptr(cmd.RoundID()), GameID: ptr(cmd.GameID()),
		Kind: ptr(string(cmd.Kind())), Money: &wagering.MoneyInput{Amount: ptr(cmd.Money().String()), Currency: ptr(string(cmd.Money().Currency()))},
	}
	out, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand: %v", err)
	}
	return out
}

// Covers: HTTP-03, HTTP-07, LED-01 (I18: ledger reads; D-16)
func TestLedgerQueries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	for i, amount := range []string{"1.00", "2.00", "3.00", "4.00", "5.00"} {
		process(t, command(t, w, p, wagering.KindBet, amount, "bet-"+string(rune('a'+i)), ""))
	}
	testkit.AssertLedgerConsistent(t, env.Owner, w.ID())

	var versions []int64
	after := int64(0)
	for {
		page, err := reads().Ledger().List(ctx, w.ID(), after, 2)
		if err != nil {
			t.Fatalf("List(after %d): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.WalletID() != w.ID() {
				t.Fatalf("entry of wallet %s in the page of %s", e.WalletID(), w.ID())
			}
			versions = append(versions, e.WalletVersion())
		}
		after = page[len(page)-1].WalletVersion()
	}
	if len(versions) != 6 || versions[0] != 1 || versions[5] != 6 {
		t.Fatalf("paged versions = %v, want 1..6 without gaps or repeats", versions)
	}

	sum, err := reads().Ledger().Sum(ctx, w.ID())
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.NetMinor != 8500 || sum.Entries != 6 {
		t.Fatalf("Sum = %+v, want 85.00 over 6 entries", sum)
	}
	if empty, err := reads().Ledger().Sum(ctx, newID()); err != nil || empty != (app.LedgerSum{}) {
		t.Fatalf("Sum(unknown wallet) = %+v, %v; want zero", empty, err)
	}
	_, err = reads().Ledger().List(ctx, w.ID(), 0, 0)
	wantKind(t, err, apperrors.KindPermanent, nil)
}
