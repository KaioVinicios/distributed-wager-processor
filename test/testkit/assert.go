package testkit

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertWalletConsistent is the verification of test-plan §6 for one wallet:
// the reconciliation of the API (item 1), the SQL checks of the ledger (items
// 2–6) and the event matrix of the outbox (item 7). It runs in t.Cleanup, so
// every call detaches from the test's context.
func (a *App) AssertWalletConsistent(tb testing.TB, walletID string) {
	tb.Helper()
	resp := a.Client(tb, "wallet-service").Do(tb, Request{Method: http.MethodPost, Path: "/wallets/" + walletID + "/reconciliation"})
	var r Reconciliation
	resp.JSON(tb, &r)
	if resp.Status != http.StatusOK || !r.Consistent || r.Difference.Amount != "0.00" {
		tb.Errorf("wallet %s: reconciliation %d %+v", walletID, resp.Status, r)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){LedgerProblems, OutboxProblems} {
		problems, err := check(ctx, a.env.Owner, walletID)
		if err != nil {
			tb.Fatalf("wallet %s: %v", walletID, err)
		}
		for _, p := range problems {
			tb.Errorf("wallet %s: %s", walletID, p)
		}
	}
}

// OutboxProblems checks item 7 of test-plan §6 for one wallet: every operation
// has exactly the events of the matrix of lifecycle §7. PROCESSED has one
// WagerTransactionProcessed and, unless it is a LOSS, one WalletBalanceChanged;
// REJECTED has one WagerTransactionRejected; FAILED has none; an operation that
// waited for its reference (expires_at set) has one
// WagerTransactionPendingReference. None means consistent.
func OutboxProblems(ctx context.Context, pool *pgxpool.Pool, walletID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT t.id::text, t.status, t.kind, t.expires_at IS NOT NULL,
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionProcessed'),
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionRejected'),
		       count(*) FILTER (WHERE o.event_type = 'WalletBalanceChanged'),
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionPendingReference')
		FROM wager_transactions t
		LEFT JOIN outbox_events o
		  ON o.message_group_id = t.wallet_id::text AND o.payload->'data'->>'transactionId' = t.id::text
		WHERE t.wallet_id = $1
		GROUP BY t.id, t.status, t.kind, t.expires_at
		ORDER BY t.id`, walletID)
	if err != nil {
		return nil, fmt.Errorf("outbox matrix: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var id, status, kind string
		var waited bool
		var processed, rejected, changed, pending int
		if err := rows.Scan(&id, &status, &kind, &waited, &processed, &rejected, &changed, &pending); err != nil {
			return nil, fmt.Errorf("outbox matrix: %w", err)
		}
		want := [4]int{}
		if status == "PROCESSED" {
			want[0] = 1
			if kind != "LOSS" {
				want[2] = 1
			}
		}
		if status == "REJECTED" {
			want[1] = 1
		}
		if waited {
			want[3] = 1
		}
		if got := [4]int{processed, rejected, changed, pending}; got != want {
			problems = append(problems, fmt.Sprintf(
				"operation %s (%s %s): events processed=%d rejected=%d changed=%d pending=%d, want %d %d %d %d",
				id, kind, status, got[0], got[1], got[2], got[3], want[0], want[1], want[2], want[3]))
		}
	}
	return problems, rows.Err()
}

// SnapshotCounts counts the rows of every table, for the tests that prove an
// access had no effect (A03). The counts are global: such a test must not run
// in parallel with tests that write.
func SnapshotCounts(tb testing.TB, pool *pgxpool.Pool) map[string]int64 {
	tb.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events", "inbox_messages"} {
		var n int64
		if err := pool.QueryRow(tb.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			tb.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// Owner is the pool of pda_owner, for setups and assertions the app role cannot do.
func (a *App) Owner() *pgxpool.Pool { return a.env.Owner }
