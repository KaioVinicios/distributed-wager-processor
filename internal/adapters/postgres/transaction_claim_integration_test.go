//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

// claimFixture is a database of its own: ClaimDue sees the whole table, so
// tests that shared it would claim each other's operations.
type claimFixture struct {
	uow   *postgres.UnitOfWork
	repos app.Repos
	owner *pgxpool.Pool
	w     wallet.Wallet
	p     string
	base  time.Time
}

func newClaimFixture(t *testing.T) claimFixture {
	t.Helper()
	e := testkit.NewTestEnv(t, "claim_due")
	f := claimFixture{
		uow: postgres.NewUnitOfWork(e.App, e.Config()), repos: postgres.NewRepos(e.App), owner: e.Owner,
		w: zeroWallet(t), p: newProvider(), base: time.Now(),
	}
	if err := f.uow.Do(t.Context(), func(r app.Repos) error { return r.Wallets().Insert(t.Context(), f.w) }); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	return f
}

// pending writes a REFUND that waits for a BET that never arrives, created i
// seconds after base; its first retry falls about 100 ms later (the policy of
// this package).
func (f claimFixture) pending(t *testing.T, i int) *wagering.WagerTransaction {
	t.Helper()
	now := f.base.Add(time.Duration(i) * time.Second)
	cmd := command(t, f.w, f.p, wagering.KindRefund, "1.00", "refund-"+strconv.Itoa(i), "bet-missing")
	tx, err := wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AwaitReference(now, policy); err != nil {
		t.Fatal(err)
	}
	if err := f.uow.Do(t.Context(), func(r app.Repos) error { return r.Transactions().Insert(t.Context(), tx) }); err != nil {
		t.Fatalf("insert pending %d: %v", i, err)
	}
	return tx
}

func refIDs(refs []app.PendingReference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

// Covers: OPS-12, TX-09 (I18: ClaimDue and CountPendingReferences)
// Sensitivity: without SKIP LOCKED the claim waits for the held row (the "skips the rows another transaction holds" subtest hangs).
func TestClaimDue(t *testing.T) {
	t.Parallel()
	f := newClaimFixture(t)
	ctx := t.Context()
	r0, r1, r2 := f.pending(t, 0), f.pending(t, 1), f.pending(t, 2)
	rejected := f.pending(t, 3)
	if _, err := rejected.Reject(wagering.FailureReferenceNotFound, f.w.Balance(), f.base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.uow.Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, rejected) }); err != nil {
		t.Fatalf("reject: %v", err)
	}
	far := f.base.Add(time.Hour)

	claim := func(now time.Time, limit int) []app.PendingReference {
		t.Helper()
		refs, err := f.repos.Transactions().ClaimDue(ctx, now, limit)
		if err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		return refs
	}

	t.Run("only what is due, oldest first", func(t *testing.T) {
		got := claim(f.base.Add(1500*time.Millisecond), 10) // r0 and r1 are due, r2 is not
		if want := []string{r0.ID(), r1.ID()}; !slices.Equal(refIDs(got), want) {
			t.Fatalf("ClaimDue = %v, want %v", refIDs(got), want)
		}
		if got[0].WalletID != f.w.ID() {
			t.Fatalf("wallet %s, want %s", got[0].WalletID, f.w.ID())
		}
	})
	t.Run("respects the limit and skips concluded operations", func(t *testing.T) {
		if want := []string{r0.ID(), r1.ID()}; !slices.Equal(refIDs(claim(far, 2)), want) {
			t.Fatalf("ClaimDue(limit 2) = %v, want %v", refIDs(claim(far, 2)), want)
		}
		if want := []string{r0.ID(), r1.ID(), r2.ID()}; !slices.Equal(refIDs(claim(far, 10)), want) {
			t.Fatalf("ClaimDue = %v, want %v (the REJECTED one is not pending)", refIDs(claim(far, 10)), want)
		}
	})
	t.Run("nothing is due before the first retry", func(t *testing.T) {
		if got := claim(f.base.Add(-time.Hour), 10); len(got) != 0 {
			t.Fatalf("ClaimDue in the past = %v, want none", refIDs(got))
		}
	})
	t.Run("skips the rows another transaction holds", func(t *testing.T) {
		tx, err := f.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM wager_transactions WHERE id = $1 FOR UPDATE`, r0.ID()); err != nil {
			t.Fatal(err)
		}
		if want := []string{r1.ID(), r2.ID()}; !slices.Equal(refIDs(claim(far, 10)), want) {
			t.Fatalf("ClaimDue with %s locked = %v, want %v", r0.ID(), refIDs(claim(far, 10)), want)
		}
	})
	t.Run("counts the pending operations", func(t *testing.T) {
		n, err := f.repos.Transactions().CountPendingReferences(ctx)
		if err != nil || n != 3 {
			t.Fatalf("CountPendingReferences = %d, %v; want 3", n, err)
		}
	})
}

// Covers: OPS-12, D-09 (I18: Lock of the transaction)
func TestTransactionLock(t *testing.T) {
	t.Parallel()
	f := newClaimFixture(t)
	ctx := t.Context()
	pending := f.pending(t, 0)

	t.Run("reads the row and holds the lock until the transaction ends", func(t *testing.T) {
		err := f.uow.Do(ctx, func(r app.Repos) error {
			got, err := r.Transactions().Lock(ctx, pending.ID())
			if err != nil {
				return err
			}
			if got.ID() != pending.ID() || got.Status() != wagering.StatusPendingReference || got.Attempts() != 0 {
				t.Errorf("Lock = %s %s attempts %d", got.ID(), got.Status(), got.Attempts())
			}
			_, lockErr := f.owner.Exec(ctx, `SELECT 1 FROM wager_transactions WHERE id = $1 FOR UPDATE NOWAIT`, pending.ID())
			var pgErr *pgconn.PgError
			if !errors.As(lockErr, &pgErr) || pgErr.Code != "55P03" {
				t.Errorf("second lock = %v, want lock_not_available (55P03)", lockErr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		for _, id := range []string{newID(), "not-a-uuid"} {
			err := f.uow.Do(ctx, func(r app.Repos) error {
				_, err := r.Transactions().Lock(ctx, id)
				return err
			})
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})
}
