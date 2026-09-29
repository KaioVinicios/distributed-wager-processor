//go:build integration

package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

type countingMetrics struct{ divergences atomic.Int32 }

func (m *countingMetrics) ReconciliationDivergence() { m.divergences.Add(1) }

// shiftBalance changes the stored balance behind the ledger's back, with the
// wallet triggers disabled only inside this transaction: ALTER TABLE holds an
// ACCESS EXCLUSIVE lock until the commit, so no parallel test ever sees them
// disabled. The context is detached: it also runs in t.Cleanup.
func shiftBalance(t *testing.T, walletID string, deltaMinor int64) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	tx, err := env.Owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, sql := range []string{
		`ALTER TABLE wallets DISABLE TRIGGER USER`,
		`UPDATE wallets SET balance_minor = balance_minor + $1 WHERE id = $2`,
		`ALTER TABLE wallets ENABLE TRIGGER USER`,
	} {
		var args []any
		if strings.HasPrefix(sql, "UPDATE") {
			args = []any{deltaMinor, walletID}
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Covers: HTTP-07, LED-06
func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("consistent wallet", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		process(t, newProcessWager(), w, op{provider: p, kind: "BET", amount: "25.00", ext: "bet-1"})
		metrics := &countingMetrics{}

		got, err := app.NewReconcile(newUoW(), metrics, slog.New(slog.DiscardHandler)).Execute(t.Context(), w.ID(), "corr")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.WalletID != w.ID() || got.Stored.String() != "75.00" || got.Calculated.String() != "75.00" ||
			got.Difference.String() != "0.00" || !got.Consistent || got.CheckedEntries != 2 || metrics.divergences.Load() != 0 {
			t.Fatalf("reconciliation = %+v, divergences %d", got, metrics.divergences.Load())
		}
	})

	t.Run("divergence is reported without changing the balance", func(t *testing.T) {
		t.Parallel()
		w := openWallet(t, "100.00")
		shiftBalance(t, w.ID(), -500)
		t.Cleanup(func() { shiftBalance(t, w.ID(), 500) }) // runs before the ledger check
		metrics := &countingMetrics{}
		var logs bytes.Buffer

		got, err := app.NewReconcile(newUoW(), metrics, slog.New(slog.NewJSONHandler(&logs, nil))).Execute(t.Context(), w.ID(), "corr-reconcile")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Stored.String() != "95.00" || got.Calculated.String() != "100.00" || got.Difference.String() != "-5.00" ||
			got.Consistent || got.CheckedEntries != 1 {
			t.Fatalf("reconciliation = %+v", got)
		}
		if metrics.divergences.Load() != 1 {
			t.Fatalf("divergences = %d, want 1", metrics.divergences.Load())
		}
		line := logs.String()
		for _, want := range []string{`"level":"WARN"`, w.ID(), "corr-reconcile", `"difference":"-5.00"`} {
			if !strings.Contains(line, want) {
				t.Fatalf("log %s lacks %s", line, want)
			}
		}
		wantWallet(t, w.ID(), "95.00", 1)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		t.Parallel()
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := app.NewReconcile(newUoW(), &countingMetrics{}, slog.New(slog.DiscardHandler)).Execute(t.Context(), id, "corr")
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})
}
