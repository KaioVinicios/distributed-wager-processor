//go:build integration

package app_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/test/testkit"
)

// testClock is an app.Clock the test moves by hand. Tests that use it create
// the wallet first, so that the clock never precedes the wallet's creation.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixture is a ProcessWager and a ResolveReferences over one testClock.
type fixture struct {
	pw    *app.ProcessWager
	rr    *app.ResolveReferences
	clock *testClock
}

func newFixture() fixture {
	clock := newTestClock()
	pw := app.NewProcessWager(newUoW(), reads(), clock, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
	return fixture{pw: pw, rr: app.NewResolveReferences(pw), clock: clock}
}

func refOf(res app.ProcessResult) app.PendingReference {
	return app.PendingReference{ID: res.Tx.ID(), WalletID: res.Tx.WalletID()}
}

// resolve evaluates the operation and fails the test on error.
func resolve(t *testing.T, f fixture, res app.ProcessResult) app.ResolveResult {
	t.Helper()
	got, err := f.rr.Resolve(t.Context(), refOf(res))
	if err != nil {
		t.Fatalf("Resolve %s: %v", res.Tx.ID(), err)
	}
	return got
}

func wantResolved(t *testing.T, got app.ResolveResult, outcome app.ResolveOutcome, code wagering.FailureCode) {
	t.Helper()
	if got.Outcome != outcome || got.FailureCode != code {
		t.Fatalf("resolved %+v, want %s %q", got, outcome, code)
	}
}

// stored reads the operation back.
func stored(t *testing.T, id string) *wagering.WagerTransaction {
	t.Helper()
	tx, err := reads().Transactions().Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return tx
}

// wantUnchanged fails unless the stored operation still has the state,
// attempts, schedule and update time of before.
func wantUnchanged(t *testing.T, before *wagering.WagerTransaction) {
	t.Helper()
	after := stored(t, before.ID())
	if after.Status() != before.Status() || after.Attempts() != before.Attempts() ||
		!after.NextAttemptAt().Equal(before.NextAttemptAt()) || !after.UpdatedAt().Equal(before.UpdatedAt()) {
		t.Fatalf("operation changed: %s attempts %d next %s, was %s attempts %d next %s",
			after.Status(), after.Attempts(), after.NextAttemptAt(), before.Status(), before.Attempts(), before.NextAttemptAt())
	}
}

// eventCauses lists the causationId and correlationId of the events of one
// operation, except its WagerTransactionPendingReference (emitted when it
// arrived, not by the worker).
func eventCauses(t *testing.T, txID string) (causes, correlations []string) {
	t.Helper()
	rows, err := env.Owner.Query(t.Context(), `
		SELECT COALESCE(payload->>'causationId', ''), payload->>'correlationId' FROM outbox_events
		WHERE payload->'data'->>'transactionId' = $1 AND event_type <> 'WagerTransactionPendingReference'
		ORDER BY event_type`, txID)
	if err != nil {
		t.Fatalf("events of %s: %v", txID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cause, corr string
		if err := rows.Scan(&cause, &corr); err != nil {
			t.Fatal(err)
		}
		causes, correlations = append(causes, cause), append(correlations, corr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return causes, correlations
}

// entries counts the ledger entries of the wallet.
func entries(t *testing.T, walletID string) int {
	t.Helper()
	return count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
}

// Covers: OPS-12, OPS-13, OPS-14, TX-09 (I06c: the worker's use case)
// Sensitivity: settleAndPersist inserting instead of updating when insert is false → every subtest fails on the unique index.
func TestResolveReferences(t *testing.T) {
	t.Parallel()

	t.Run("a REFUND that waited is processed when its BET arrives (C2)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
		bet := process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResult(t, bet, wagering.StatusProcessed, "", "70.00", false)

		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		tx := stored(t, pending.Tx.ID())
		if tx.Status() != wagering.StatusProcessed || tx.ResultBalance().String() != "100.00" || tx.ReferenceTransactionID() != bet.Tx.ID() {
			t.Fatalf("REFUND = %s balance %s reference %s", tx.Status(), tx.ResultBalance(), tx.ReferenceTransactionID())
		}
		wantWallet(t, w.ID(), "100.00", 3)
		if n := entries(t, w.ID()); n != 3 {
			t.Fatalf("%d ledger entries, want the opening, the BET and the REFUND", n)
		}
		// The events cite the reference that unblocked the operation and keep the original correlation (messaging §7).
		causes, corrs := eventCauses(t, pending.Tx.ID())
		if !slices.Equal(causes, []string{bet.Tx.ID(), bet.Tx.ID()}) || !slices.Equal(corrs, []string{"corr-refund-1", "corr-refund-1"}) {
			t.Fatalf("causes %v correlations %v, want the BET %s and corr-refund-1 twice", causes, corrs, bet.Tx.ID())
		}
	})

	t.Run("a reference that is still absent reschedules", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		f.clock.Advance(time.Minute) // past the first delay (30 s ± 20%)

		got := resolve(t, f, pending)
		wantResolved(t, got, app.ResolveRescheduled, "")
		tx := stored(t, pending.Tx.ID())
		if got.Attempts != 1 || tx.Attempts() != 1 || tx.Status() != wagering.StatusPendingReference || !tx.NextAttemptAt().After(f.clock.Now()) {
			t.Fatalf("result attempts %d; stored %s attempts %d next %s (now %s)", got.Attempts, tx.Status(), tx.Attempts(), tx.NextAttemptAt(), f.clock.Now())
		}
		if causes, _ := eventCauses(t, pending.Tx.ID()); len(causes) != 0 {
			t.Fatalf("a retry emitted %d events, want none", len(causes))
		}
	})

	t.Run("the limit rejects with REFERENCE_NOT_FOUND (C3)", func(t *testing.T) {
		t.Parallel()
		t.Run("by attempts", func(t *testing.T) {
			t.Parallel()
			w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
			pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
			for i, step := range []time.Duration{time.Minute, 3 * time.Minute} {
				f.clock.Advance(step)
				got := resolve(t, f, pending)
				wantResolved(t, got, app.ResolveRescheduled, "")
				if got.Attempts != i+1 {
					t.Fatalf("attempts %d, want %d", got.Attempts, i+1)
				}
			}
			f.clock.Advance(3 * time.Minute) // 7 min: the 3rd attempt, before the 10 min TTL
			got := resolve(t, f, pending)
			wantResolved(t, got, app.ResolveRejected, wagering.FailureReferenceNotFound)
			if !got.Expired() {
				t.Fatalf("%+v is not an expiration", got)
			}
			tx := stored(t, pending.Tx.ID())
			if tx.Status() != wagering.StatusRejected || tx.ResultBalance().String() != "100.00" || entries(t, w.ID()) != 1 {
				t.Fatalf("stored %s balance %s, %d entries; want REJECTED, 100.00 and the opening only", tx.Status(), tx.ResultBalance(), entries(t, w.ID()))
			}
			causes, _ := eventCauses(t, pending.Tx.ID())
			if !slices.Equal(causes, []string{""}) { // one WagerTransactionRejected, with no reference to cite
				t.Fatalf("causes %v, want one event with no causationId", causes)
			}
		})
		t.Run("by TTL, whatever the attempts", func(t *testing.T) {
			t.Parallel()
			w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
			pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
			f.clock.Advance(11 * time.Minute)
			got := resolve(t, f, pending)
			wantResolved(t, got, app.ResolveRejected, wagering.FailureReferenceNotFound)
			if got.Attempts != 0 || !got.Expired() {
				t.Fatalf("%+v, want an expiration on the first attempt", got)
			}
		})
	})

	t.Run("a reference that is itself pending keeps waiting (R2)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		second := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, second, wagering.StatusPendingReference, "", "", false)
		f.clock.Advance(time.Minute)
		wantResolved(t, resolve(t, f, second), app.ResolveRescheduled, "")
	})

	t.Run("the rules of the reference apply when it arrives (R3–R6)", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name             string
			waiting, arrival op // the reversal that comes first, and what it waits for
			want             wagering.FailureCode
			balance          string // the wallet at the rejection
		}{
			{
				"R3 reference rejected",
				op{kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-1"},
				op{kind: "BET", amount: "500.00", ext: "bet-1"},
				wagering.FailureReferenceNotProcessed, "100.00",
			},
			{
				"R4 kind not accepted",
				op{kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "win-1"},
				op{kind: "WIN", amount: "30.00", ext: "win-1"},
				wagering.FailureInvalidReferenceKind, "130.00",
			},
			{
				"R6 amount differs",
				op{kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-1"},
				op{kind: "BET", amount: "30.00", ext: "bet-1"},
				wagering.FailureReversalAmountMismatch, "70.00",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
				tc.waiting.provider, tc.arrival.provider = p, p
				pending := process(t, f.pw, w, tc.waiting)
				wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
				process(t, f.pw, w, tc.arrival)

				wantResolved(t, resolve(t, f, pending), app.ResolveRejected, tc.want)
				if got := stored(t, pending.Tx.ID()).ResultBalance().String(); got != tc.balance {
					t.Fatalf("balance at the rejection %s, want %s", got, tc.balance)
				}
			})
		}
	})

	t.Run("a reference from another wallet is a mismatch (R5)", func(t *testing.T) {
		t.Parallel()
		a, b, f, p := openWallet(t, "100.00"), openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, a, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, b, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResolved(t, resolve(t, f, pending), app.ResolveRejected, wagering.FailureReferenceMismatch)
	})

	t.Run("a second reversal of the same BET is already reversed (R7, C4)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		refund := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}) // advances both

		wantResolved(t, resolve(t, f, refund), app.ResolveProcessed, "")
		wantResolved(t, resolve(t, f, rollback), app.ResolveRejected, wagering.FailureAlreadyReversed)
		wantWallet(t, w.ID(), "100.00", 3) // the debit was returned once
	})

	t.Run("a chain of pendings resolves in cascade", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		refund := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, rollback, wagering.StatusPendingReference, "", "", false)
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})

		wantResolved(t, resolve(t, f, refund), app.ResolveProcessed, "")   // its conclusion advances the ROLLBACK
		wantResolved(t, resolve(t, f, rollback), app.ResolveProcessed, "") // the ROLLBACK of a REFUND debits again
		wantWallet(t, w.ID(), "70.00", 4)
	})
}

// Covers: OPS-12, E7 (spec M6, decision 3)
// Sensitivity: removing the tx.NextAttemptAt().After(now) check in lockPending → "rescheduled by another worker" resolves again (attempts 2).
func TestResolveReferencesSkips(t *testing.T) {
	t.Parallel()

	t.Run("not due yet", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		before := stored(t, pending.Tx.ID())
		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
	})

	t.Run("already concluded", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		before := stored(t, pending.Tx.ID())

		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "100.00", 3) // one movement only
	})

	t.Run("rescheduled by another worker after the claim", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		f.clock.Advance(time.Minute)
		wantResolved(t, resolve(t, f, pending), app.ResolveRescheduled, "") // the other worker
		before := stored(t, pending.Tx.ID())

		// This worker claimed the same operation before that, and only now gets the locks.
		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
		if got := before.Attempts(); got != 1 {
			t.Fatalf("attempts %d, want 1: the second evaluation must not count", got)
		}
	})
}

// Covers: OPS-12, E7 (spec M6, decision 3)
// Sensitivity: without the horizon check, both evaluations reschedule and attempts is 2.
//
// Two instances evaluate the same due operation at once: one attempt is
// counted, whoever gets the locks first.
func TestResolveReferencesConcurrent(t *testing.T) {
	t.Parallel()
	w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
	pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
	f.clock.Advance(time.Minute)
	ref := refOf(pending)

	results := make([]app.ResolveResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			<-start
			results[i], errs[i] = f.rr.Resolve(t.Context(), ref)
		})
	}
	close(start)
	wg.Wait()

	outcomes := map[app.ResolveOutcome]int{}
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("Resolve %d: %v", i, errs[i])
		}
		outcomes[results[i].Outcome]++
	}
	if outcomes[app.ResolveRescheduled] != 1 || outcomes[app.ResolveSkipped] != 1 {
		t.Fatalf("outcomes %v, want one rescheduled and one skipped", outcomes)
	}
	if got := stored(t, ref.ID).Attempts(); got != 1 {
		t.Fatalf("attempts %d, want 1", got)
	}
}

// faultyResolver resolves with a ProcessWager whose outbox fails with err in
// the first unit of work only, so that the FAILED write of the second one goes
// through. The clock is the fixture's.
func faultyResolver(f fixture, err error) *app.ResolveReferences {
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, call int) app.Repos {
		if call != 1 {
			return r
		}
		return faultyRepos{Repos: r, outbox: failingOutbox{OutboxRepository: r.Outbox(), err: err}}
	}}
	return app.NewResolveReferences(app.NewProcessWager(uow, reads(), f.clock, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler)))
}

// Covers: TX-06, D-05, OPS-12 (spec M6, decisions 8 and 9)
// Sensitivity: dropping AdvanceDependents from recordFailure → the ROLLBACK stays not due (SKIPPED); treating the permanent error as any other → it surfaces instead of recording FAILED.
func TestResolveReferencesFailures(t *testing.T) {
	t.Parallel()

	t.Run("a permanent failure records FAILED and releases the dependents", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, rollback, wagering.StatusPendingReference, "", "", false) // waits for the REFUND (R2)
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})

		permanent := apperrors.New(apperrors.KindPermanent, "", errors.New("outbox broken"))
		got, err := faultyResolver(f, permanent).Resolve(t.Context(), refOf(pending))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		wantResolved(t, got, app.ResolveFailed, "")
		tx := stored(t, pending.Tx.ID())
		if tx.Status() != wagering.StatusFailed || tx.FailureCode() != wagering.FailureInternalPermanentFailure {
			t.Fatalf("REFUND = %s %s, want FAILED INTERNAL_PERMANENT_FAILURE", tx.Status(), tx.FailureCode())
		}
		wantWallet(t, w.ID(), "70.00", 2) // nothing moved
		if n := entries(t, w.ID()); n != 2 {
			t.Fatalf("%d ledger entries, want the opening and the BET only", n)
		}
		if causes, _ := eventCauses(t, pending.Tx.ID()); len(causes) != 0 {
			t.Fatalf("FAILED emitted %d events, want none", len(causes))
		}
		// The ROLLBACK that waited for it is released at once and rejected.
		wantResolved(t, resolve(t, f, rollback), app.ResolveRejected, wagering.FailureReferenceNotProcessed)
	})

	t.Run("a transient failure leaves the operation due", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		before := stored(t, pending.Tx.ID())

		_, err := faultyResolver(f, errors.New("outbox down")).Resolve(t.Context(), refOf(pending))
		if apperrors.Classify(err) != apperrors.KindTransient {
			t.Fatalf("error = %v (%s), want transient", err, apperrors.Classify(err))
		}
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "70.00", 2)
		// Nothing was lost: the next cycle resolves it.
		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		wantWallet(t, w.ID(), "100.00", 3)
	})

	t.Run("a canceled context writes nothing", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		before := stored(t, pending.Tx.ID())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := f.rr.Resolve(ctx, refOf(pending))
		if apperrors.Classify(err) != apperrors.KindTransient {
			t.Fatalf("error = %v (%s), want transient", err, apperrors.Classify(err))
		}
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "70.00", 2)
	})
}

// waitingLocks counts the lock requests waiting anywhere in the database.
func waitingLocks(ctx context.Context) (int, error) {
	var n int
	err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&n)
	return n, err
}

// Covers: D-09, CONC-01 (spec M6, risks: lock order wallet → transaction, as the HTTP path)
// Sensitivity: locking the transaction before the wallet in lockPending → the NOWAIT lock on the operation fails (55P03) while the wallet is held.
//
// With the wallet held by another connection, the evaluation queues behind it
// without holding the operation: the order is fixed, so it cannot deadlock with
// a request that holds the wallet and advances this operation. A waiting lock
// from a parallel test can only make this check start early, never fail it.
func TestResolveReferencesLockOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
	pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
	process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}) // the REFUND is due now

	holder, err := env.Owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, w.ID()); err != nil {
		t.Fatalf("hold the wallet: %v", err)
	}
	baseline, err := waitingLocks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.rr.Resolve(ctx, refOf(pending))
		done <- err
	}()
	testkit.Eventually(t, 5*time.Second, "the evaluation to queue for the wallet", func(ctx context.Context) (bool, error) {
		n, err := waitingLocks(ctx)
		return n > baseline, err
	})

	var one int
	if err := env.Owner.QueryRow(ctx, `SELECT 1 FROM wager_transactions WHERE id = $1 FOR UPDATE NOWAIT`, pending.Tx.ID()).Scan(&one); err != nil {
		t.Fatalf("the operation is locked while the evaluation waits for the wallet: %v", err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Resolve after the wallet was released: %v", err)
	}
	if got := stored(t, pending.Tx.ID()).Status(); got != wagering.StatusProcessed {
		t.Fatalf("operation %s, want PROCESSED", got)
	}
}
