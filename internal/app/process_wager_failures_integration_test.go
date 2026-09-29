//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: IDEM-07, CONC-03 (I22)
//
// Two deliveries with the same key for different wallets do not serialize on
// a wallet lock: both pass the lookups and insert. The barrier holds both
// inserts until both transactions reach them, so the unique index always
// decides: the loser gets ErrIdempotencyRace, retries and rereads into 409.
func TestProcessWagerRaces(t *testing.T) {
	t.Parallel()

	t.Run("same key in two wallets", func(t *testing.T) {
		t.Parallel()
		p := newProvider()
		wallets := [2]string{}
		b := newBarrier(2)
		results := make([]app.ProcessResult, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			w := openWallet(t, "100.00")
			wallets[i] = w.ID()
			cmd := command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})
			var once sync.Once
			uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
				return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(ctx context.Context) error {
					once.Do(func() { b.arrive(ctx) })
					return nil
				}}}
			}}
			pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
			wg.Go(func() { results[i], errs[i] = pw.Execute(t.Context(), request(cmd)) })
		}
		wg.Wait()

		winner, loser := 0, 1
		if errs[0] != nil {
			winner, loser = 1, 0
		}
		if errs[winner] != nil || results[winner].Tx.Status() != wagering.StatusProcessed {
			t.Fatalf("winner: %s, %v", describe(results[winner]), errs[winner])
		}
		wantError(t, errs[loser], apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED")
		wantWallet(t, wallets[winner], "90.00", 2)
		wantWallet(t, wallets[loser], "100.00", 1)
	})

	// Found by the C01a of the plan validation: the two lookups are separate
	// reads, and a concurrent delivery may commit between them.
	t.Run("same key committed between the two lookups", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"}
		first := process(t, newProcessWager(), w, bet)
		late := faultyRepos{Repos: reads(), tx: txHooks{TransactionRepository: reads().Transactions(), hideKey: true}}
		pw := app.NewProcessWager(newUoW(), late, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))

		res, err := pw.Execute(t.Context(), request(command(t, w, bet)))
		if err != nil || !res.Replay || res.Tx.ID() != first.Tx.ID() {
			t.Fatalf("Execute = %s, %v; want the replay of %s", describe(res), err, first.Tx.ID())
		}
	})

	t.Run("a race is retried at most 3 times", func(t *testing.T) {
		t.Parallel()
		for _, race := range []error{app.ErrIdempotencyRace, app.ErrReversalRace} {
			w, p := openWallet(t, "100.00"), newProvider()
			var inserts int
			uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
				return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(context.Context) error {
					inserts++
					return apperrors.New(apperrors.KindTransient, "", race)
				}}}
			}}
			pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
			_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})))
			if apperrors.Classify(err) != apperrors.KindTransient || !errors.Is(err, race) || inserts != 3 {
				t.Fatalf("%v: error %v after %d inserts, want the transient race after 3", race, err, inserts)
			}
			wantWallet(t, w.ID(), "100.00", 1)
		}
	})
}

// Covers: TX-06, TX-10, TST-I03 (I03b, the HTTP part; the SQS part is M5)
func TestPermanentFailureRecorded(t *testing.T) {
	t.Parallel()
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))

	t.Run("recorded as FAILED in a separate transaction and replayed", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		var logs bytes.Buffer
		uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
			return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
		}}
		pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.NewJSONHandler(&logs, nil)))
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}

		res, err := pw.Execute(t.Context(), request(command(t, w, bet)))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		wantResult(t, res, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
		wantWallet(t, w.ID(), "100.00", 1)
		if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, res.Tx.ID()); n != 0 {
			t.Fatalf("%d entries for a FAILED operation", n)
		}
		if types, _ := outboxTypes(t, w.ID()); len(types) != 2 {
			t.Fatalf("outbox = %v, want only the 2 events of the opening", types)
		}
		line := logs.String()
		if !strings.Contains(line, `"level":"ERROR"`) || !strings.Contains(line, res.Tx.ID()) || strings.Contains(line, "30.00") {
			t.Fatalf("log = %s; want an ERROR with the transaction id and no amount", line)
		}

		replay := process(t, newProcessWager(), w, bet)
		wantResult(t, replay, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", true)
		if replay.Tx.ID() != res.Tx.ID() {
			t.Fatalf("replay id %s, want %s", replay.Tx.ID(), res.Tx.ID())
		}
	})

	t.Run("credit overflow fails permanently", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "92233720368547758.07"), newProvider()
		res := process(t, newProcessWager(), w, op{provider: p, kind: "WIN", amount: "0.01", ext: "win-1"})
		wantResult(t, res, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
		wantWallet(t, w.ID(), "92233720368547758.07", 1)
	})

	t.Run("a FAILED that cannot be recorded is transient", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, call int) app.Repos {
			if call == 1 {
				return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
			}
			return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(context.Context) error { return forced }}}
		}}
		pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
		_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindTransient, "")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations recorded", n)
		}
	})

	// Passes before recordFailure exists: it guards that a failed lookup
	// before the lock never records a FAILED.
	// Sensitivity: recordFailure also for a failed lookup before the lock →
	// the FAILED is written ("error = <nil>").
	t.Run("an unreadable earlier delivery is not recorded again", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		corrupted := faultyRepos{Repos: reads(), tx: txHooks{TransactionRepository: reads().Transactions(), findByKey: func() error { return forced }}}
		pw := app.NewProcessWager(newUoW(), corrupted, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
		_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindPermanent, "")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations recorded", n)
		}
	})
}
