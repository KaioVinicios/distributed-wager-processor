//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/test/testkit"
)

// resume re-evaluates a PENDING_REFERENCE operation like the reference worker
// (lifecycle §6.3): lock the wallet first, then read the operation again.
func resume(tb testing.TB, txID string) *wagering.WagerTransaction {
	tb.Helper()
	ctx := tb.Context()
	var tx *wagering.WagerTransaction
	err := newUoW().Do(ctx, func(r app.Repos) error {
		pending, err := r.Transactions().Get(ctx, txID)
		if err != nil {
			return err
		}
		w, err := r.Wallets().Lock(ctx, pending.WalletID())
		if err != nil {
			return err
		}
		if tx, err = r.Transactions().Get(ctx, txID); err != nil {
			return err
		}
		ref, err := r.Transactions().FindReference(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
		if err != nil {
			return err
		}
		out, err := wagering.Settle(tx, &w, ref, wagering.SettleParams{EntryID: newID(), Now: time.Now(), Policy: policy})
		if err != nil {
			return err
		}
		if err := r.Transactions().Update(ctx, tx); err != nil {
			return err
		}
		return persistOutcome(ctx, r, w, out, tx.CorrelationID())
	})
	if err != nil {
		tb.Fatalf("resume %s: %v", txID, err)
	}
	return tx
}

func wantBalance(t *testing.T, walletID, amount string, version int64) {
	t.Helper()
	w, err := reads().Wallets().Get(t.Context(), walletID)
	if err != nil {
		t.Fatalf("Get wallet: %v", err)
	}
	if w.Balance() != brl(t, amount) || w.Version() != version {
		t.Fatalf("wallet = %s v%d, want %s BRL v%d", w.Balance(), w.Version(), amount, version)
	}
}

func outboxCount(t *testing.T, walletID string) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE message_group_id = $1`, walletID).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

// Covers: WAL-06, LED-05, TX-09, OPS-01..10, OPS-12, OUT-02, E9 (I17)
// Sensitivity: an AdvanceDependents that writes nothing → refund-2 keeps its original schedule, not bet-3's conclusion.
//
// Every kind, written through the repositories the way the use cases will,
// passes the triggers: the domain of M1 and the schema agree.
//
// Sensitivity: FindReference with "false" in place of the EXISTS (never
// AlreadyReversed) → refund-3 fails with ErrReversalRace (23505
// wager_tx_single_reversal_uq) instead of REJECTED ALREADY_REVERSED.
func TestDomainFlowsPersist(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	steps := []struct {
		kind             wagering.Kind
		amount, ext, ref string
		status           wagering.Status
		failure          wagering.FailureCode
		balance          string
		version          int64
	}{
		{wagering.KindBet, "30.00", "bet-1", "", wagering.StatusProcessed, "", "70.00", 2},
		{wagering.KindWin, "50.00", "win-1", "bet-1", wagering.StatusProcessed, "", "120.00", 3},
		{wagering.KindLoss, "0.00", "loss-1", "", wagering.StatusProcessed, "", "120.00", 3},
		{wagering.KindRefund, "30.00", "refund-1", "bet-1", wagering.StatusProcessed, "", "150.00", 4},
		{wagering.KindRollback, "50.00", "rollback-1", "win-1", wagering.StatusProcessed, "", "100.00", 5},
		{wagering.KindBet, "500.00", "bet-2", "", wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", 5},
		{wagering.KindRefund, "10.00", "refund-2", "bet-3", wagering.StatusPendingReference, "", "100.00", 5},
		{wagering.KindBet, "10.00", "bet-3", "", wagering.StatusProcessed, "", "90.00", 6},
	}
	byExt := map[string]*wagering.WagerTransaction{}
	for _, s := range steps {
		tx := process(t, command(t, w, p, s.kind, s.amount, s.ext, s.ref))
		if tx.Status() != s.status || tx.FailureCode() != s.failure {
			t.Fatalf("%s: %s %s, want %s %s", s.ext, tx.Status(), tx.FailureCode(), s.status, s.failure)
		}
		if s.ref != "" && s.status == wagering.StatusProcessed && tx.ReferenceTransactionID() != byExt[s.ref].ID() {
			t.Fatalf("%s: resolved reference %s, want %s", s.ext, tx.ReferenceTransactionID(), byExt[s.ref].ID())
		}
		wantStored(t, tx)
		wantBalance(t, w.ID(), s.balance, s.version)
		byExt[s.ext] = tx
	}

	// bet-3 advanced refund-2, which waited for it; the worker resolves it.
	pending, err := reads().Transactions().Get(t.Context(), byExt["refund-2"].ID())
	if err != nil {
		t.Fatal(err)
	}
	// The conclusion of bet-3 advanced refund-2 to its own instant: AdvanceDependents
	// gets the now of the conclusion (D-11). Comparing with the original schedule
	// would race the clock: bet-3 may conclude after refund-2's first retry.
	if !pending.NextAttemptAt().Equal(byExt["bet-3"].CompletedAt()) {
		t.Fatalf("refund-2 next attempt %v, want the conclusion of bet-3 %v", pending.NextAttemptAt(), byExt["bet-3"].CompletedAt())
	}
	if got := resume(t, pending.ID()); got.Status() != wagering.StatusProcessed || got.ReferenceTransactionID() != byExt["bet-3"].ID() {
		t.Fatalf("refund-2 after the worker: %s, reference %s", got.Status(), got.ReferenceTransactionID())
	}
	wantBalance(t, w.ID(), "100.00", 7)

	if again := process(t, command(t, w, p, wagering.KindRefund, "30.00", "refund-3", "bet-1")); again.FailureCode() != wagering.FailureAlreadyReversed {
		t.Fatalf("second reversal of bet-1: %s %s, want REJECTED ALREADY_REVERSED", again.Status(), again.FailureCode())
	}
	if rb := process(t, command(t, w, p, wagering.KindRollback, "10.00", "rollback-2", "refund-2")); rb.Status() != wagering.StatusProcessed {
		t.Fatalf("rollback of refund-2: %s %s", rb.Status(), rb.FailureCode())
	}
	wantBalance(t, w.ID(), "90.00", 8)

	// Lifecycle §7: 2 events per movement, 1 per LOSS, rejection or pending.
	if n := outboxCount(t, w.ID()); n != 20 {
		t.Fatalf("outbox has %d events for the wallet, want 20", n)
	}
}

// forcedOutbox makes Outbox().Insert fail, standing in for a failure on the
// last write of the unit of work.
type forcedOutbox struct {
	app.OutboxRepository
	err error
}

func (f forcedOutbox) Insert(context.Context, ...events.Envelope) error { return f.err }

type forcedRepos struct {
	app.Repos
	err error
}

func (f forcedRepos) Outbox() app.OutboxRepository { return forcedOutbox{f.Repos.Outbox(), f.err} }

func failOutbox(err error) func(app.Repos) app.Repos {
	return func(r app.Repos) app.Repos { return forcedRepos{r, err} }
}

// wantUntouched checks that the failed BET left nothing behind.
func wantUntouched(t *testing.T, walletID, provider, ext string) {
	t.Helper()
	ctx := t.Context()
	if tx, err := reads().Transactions().FindByExternalID(ctx, provider, ext); err != nil || tx != nil {
		t.Fatalf("operation %s persisted (found: %v, err: %v)", ext, tx != nil, err)
	}
	if sum, err := reads().Ledger().Sum(ctx, walletID); err != nil || sum.Entries != 1 {
		t.Fatalf("ledger = %+v, %v; want only the opening entry", sum, err)
	}
	if n := outboxCount(t, walletID); n != 2 {
		t.Fatalf("outbox has %d events, want only the 2 of the opening", n)
	}
	wantBalance(t, walletID, "100.00", 1)
}

// Covers: TST-I03, WAL-06, OUT-02, E5 (I03a)
// Sensitivity: UnitOfWork committing when fn fails → "operation bet-1 persisted".
func TestFinancialAtomicity(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	forced := apperrors.New(apperrors.KindTransient, "", errors.New("forced outbox failure"))

	_, err := processWith(t.Context(), newUoW(), command(t, w, p, wagering.KindBet, "30.00", "bet-1", ""), time.Now(), failOutbox(forced))
	if !errors.Is(err, forced) {
		t.Fatalf("process = %v, want the forced failure", err)
	}
	wantKind(t, err, apperrors.KindTransient, nil)
	wantUntouched(t, w.ID(), p, "bet-1")
}

// Covers: TX-06, TX-10 (I03b, partial: the replay with 500 is M3, the DLQ is M5)
// Sensitivity: UnitOfWork committing when fn fails → "operation bet-1 persisted".
func TestPermanentFailureRecorded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	cmd := command(t, w, p, wagering.KindBet, "30.00", "bet-1", "")
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))

	_, err := processWith(ctx, newUoW(), cmd, time.Now(), failOutbox(forced))
	wantKind(t, err, apperrors.KindPermanent, forced)
	wantUntouched(t, w.ID(), p, "bet-1")

	// D-05: the permanent failure is recorded in a separate transaction.
	var failed *wagering.WagerTransaction
	if err := newUoW().Do(ctx, func(r app.Repos) error {
		if _, err := r.Wallets().Lock(ctx, cmd.WalletID()); err != nil {
			return err
		}
		var err error
		if failed, err = wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr", time.Now()); err != nil {
			return err
		}
		if err := failed.Fail(time.Now()); err != nil {
			return err
		}
		return r.Transactions().Insert(ctx, failed)
	}); err != nil {
		t.Fatalf("record FAILED: %v", err)
	}
	got, err := reads().Transactions().FindByExternalID(ctx, p, "bet-1")
	if err != nil || got == nil {
		t.Fatalf("FindByExternalID = %v, %v", got, err)
	}
	if got.Status() != wagering.StatusFailed || got.FailureCode() != wagering.FailureInternalPermanentFailure || got.CompletedAt().IsZero() {
		t.Fatalf("stored %s %s completed %v, want FAILED INTERNAL_PERMANENT_FAILURE", got.Status(), got.FailureCode(), got.CompletedAt())
	}
	wantStored(t, failed)
	wantBalance(t, w.ID(), "100.00", 1)
	if sum, err := reads().Ledger().Sum(ctx, w.ID()); err != nil || sum.Entries != 1 {
		t.Fatalf("ledger = %+v, %v; want only the opening entry", sum, err)
	}
}

// Sensitivity of testkit.LedgerProblems, the SQL part of test-plan §6 that
// I03 and I17 rely on: each divergence, written with the triggers disabled on a
// database of its own, is reported.
//
// Sensitivity: the version-chain case disabled in LedgerProblems → "gap in the
// version chain" fails with problems = "".
func TestLedgerProblemsDetectsDivergence(t *testing.T) {
	t.Parallel()
	_, pool := isolatedDB(t, "ledgercheck")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers: %v", err)
		}
	}
	cases := []struct {
		name string
		rows func(w, p, o string) []stmt
		want string // empty when the wallet is consistent
	}{
		{"consistent", func(w, p, o string) []stmt { return nil }, ""},
		{"balance differs from the ledger", func(w, p, o string) []stmt {
			return []stmt{exec(`UPDATE wallets SET balance_minor = 5000 WHERE id = $1`, w)}
		}, "Σ credits"},
		{"gap in the version chain", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "PROCESSED", "failure_code", nil, "result_balance_minor", int64(9000))),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, bet, "DEBIT", 1000, 10000, 9000, 3)),
				exec(`UPDATE wallets SET balance_minor = 9000, version = 3 WHERE id = $1`, w),
			}
		}, "does not follow"},
		{"version ahead of the ledger", func(w, p, o string) []stmt {
			return []stmt{exec(`UPDATE wallets SET version = 3 WHERE id = $1`, w)}
		}, "last ledger version"},
		{"entry of a rejected operation", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, bet, "DEBIT", 1000, 10000, 9000, 2)),
				exec(`UPDATE wallets SET balance_minor = 9000, version = 2 WHERE id = $1`, w),
			}
		}, "do not move the balance"},
		{"processed operation without entry", func(w, p, o string) []stmt {
			return []stmt{ins("wager_transactions", externalRow(newID(), w, p).with("status", "PROCESSED", "failure_code", nil))}
		}, "without exactly one entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, p, o := newID(), newID(), newID()
			stmts := append([]stmt{
				ins("wallets", walletRow(w, p, 10000)),
				ins("wager_transactions", openingRow(o, w, p, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, o, "CREDIT", 10000, 0, 10000, 1)),
			}, tc.rows(w, p, o)...)
			if err := attemptCommit(t, pool, stmts...); err != nil {
				t.Fatalf("seed: %v", err)
			}
			problems, err := testkit.LedgerProblems(t.Context(), pool, w)
			if err != nil {
				t.Fatalf("LedgerProblems: %v", err)
			}
			joined := strings.Join(problems, "; ")
			if tc.want == "" && len(problems) != 0 || tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("problems = %q, want %q", joined, tc.want)
			}
		})
	}
}
