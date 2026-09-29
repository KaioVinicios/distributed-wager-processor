//go:build integration

package app_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-06, IDEM-05, IDEM-06, IDEM-07, IDEM-08, TX-06, OPS-01..03 (I21)
func TestProcessWager(t *testing.T) {
	t.Parallel()

	t.Run("processes and replays with the original balance", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}

		first := process(t, pw, w, bet)
		wantResult(t, first, wagering.StatusProcessed, "", "70.00", false)
		if first.Tx.CorrelationID() != "corr-bet-1" || first.Tx.ReceivedVia() != wagering.ReceivedViaHTTP {
			t.Fatalf("correlation %q via %q", first.Tx.CorrelationID(), first.Tx.ReceivedVia())
		}
		wantWallet(t, w.ID(), "70.00", 2)
		process(t, pw, w, op{provider: p, kind: "WIN", amount: "50.00", ext: "win-1"})
		wantWallet(t, w.ID(), "120.00", 3)

		replay := process(t, pw, w, bet)
		wantResult(t, replay, wagering.StatusProcessed, "", "70.00", true)
		if replay.Tx.ID() != first.Tx.ID() {
			t.Fatalf("replay id %s, want %s", replay.Tx.ID(), first.Tx.ID())
		}
		wantWallet(t, w.ID(), "120.00", 3)
		types, corrs := outboxTypes(t, w.ID())
		if len(types) != 6 || corrs[2] != "corr-bet-1" {
			t.Fatalf("outbox = %v %v; want 2 events each for the opening, the BET and the WIN", types, corrs)
		}
	})

	t.Run("LOSS processes without movement", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		res := process(t, pw, w, op{provider: p, kind: "LOSS", amount: "0.00", ext: "loss-1"})
		wantResult(t, res, wagering.StatusProcessed, "", "100.00", false)
		wantWallet(t, w.ID(), "100.00", 1)
	})

	t.Run("rejection is persisted and replayed", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "500.00", ext: "bet-1"}
		wantResult(t, process(t, pw, w, bet), wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", false)
		wantResult(t, process(t, pw, w, bet), wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", true)
		wantWallet(t, w.ID(), "100.00", 1)
	})

	t.Run("idempotency conflicts write nothing", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		process(t, pw, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

		_, err := pw.Execute(ctx, request(command(t, w, op{provider: p, kind: "BET", amount: "20.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED")
		_, err = pw.Execute(ctx, request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1", key: "other-key"})))
		wantError(t, err, apperrors.KindConflict, "EXTERNAL_TRANSACTION_ID_CONFLICT")
		wantWallet(t, w.ID(), "90.00", 2)
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
			t.Fatalf("%d operations for the provider, want 1", n)
		}
	})

	t.Run("unknown wallet writes nothing", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		cmd := command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})
		missing := command(t, walletLike(t, w, newID()), op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

		_, err := pw.Execute(ctx, request(missing))
		wantError(t, err, apperrors.KindInput, "UNKNOWN_WALLET")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations written for an unknown wallet", n)
		}
		if _, err := pw.Execute(ctx, request(cmd)); err != nil {
			t.Fatalf("the same key on the real wallet: %v", err)
		}
	})

	t.Run("references resolve, wait and advance", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := process(t, pw, w, op{provider: p, kind: "BET", amount: "20.00", ext: "bet-1"})
		win := process(t, pw, w, op{provider: p, kind: "WIN", amount: "5.00", ext: "win-1", ref: "bet-1"})
		wantResult(t, win, wagering.StatusProcessed, "", "85.00", false)
		if win.Tx.ReferenceTransactionID() != bet.Tx.ID() {
			t.Fatalf("WIN resolved %q, want %s", win.Tx.ReferenceTransactionID(), bet.Tx.ID())
		}

		refund := op{provider: p, kind: "REFUND", amount: "10.00", ext: "refund-2", ref: "bet-2"}
		pending := process(t, pw, w, refund)
		wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
		wantResult(t, process(t, pw, w, refund), wagering.StatusPendingReference, "", "", true)

		process(t, pw, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-2"})
		advanced, err := reads().Transactions().Get(ctx, pending.Tx.ID())
		if err != nil {
			t.Fatal(err)
		}
		if !advanced.NextAttemptAt().Before(pending.Tx.NextAttemptAt()) {
			t.Fatalf("the pending REFUND was not advanced: next attempt %v, was %v", advanced.NextAttemptAt(), pending.Tx.NextAttemptAt())
		}
		types, _ := outboxTypes(t, w.ID())
		if !strings.Contains(strings.Join(types, ","), "WagerTransactionPendingReference") {
			t.Fatalf("outbox = %v, want the pending event", types)
		}
	})
}
