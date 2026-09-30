package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertWalletConsistent is the verification of test-plan §6 for one wallet:
// the reconciliation of the API (item 1), the SQL checks of the ledger (items
// 2–6), the event matrix of the outbox (item 7) and the outbox published and
// delivered (item 8). It runs in t.Cleanup, so every call detaches from the
// test's context.
func (h *Harness) AssertWalletConsistent(tb testing.TB, walletID string) {
	tb.Helper()
	resp := h.Client(tb, "wallet-service").Do(tb, Request{Method: http.MethodPost, Path: "/wallets/" + walletID + "/reconciliation"})
	var r Reconciliation
	resp.JSON(tb, &r)
	if resp.Status != http.StatusOK || !r.Consistent || r.Difference.Amount != "0.00" {
		tb.Errorf("wallet %s: reconciliation %d %+v", walletID, resp.Status, r)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){LedgerProblems, OutboxProblems} {
		problems, err := check(ctx, h.env.Owner, walletID)
		if err != nil {
			tb.Fatalf("wallet %s: %v", walletID, err)
		}
		for _, p := range problems {
			tb.Errorf("wallet %s: %s", walletID, p)
		}
	}
	h.assertOutboxDelivered(tb, walletID)
}

// assertOutboxDelivered is item 8 of test-plan §6 (spec M4, decision 18): no
// event of the wallet stays unpublished, and every one of them reached the
// audit queue with the content of its payload column, on the contract.
// Sensitivity: without outbox.Module in the graph, every wallet failed "outbox of wallet … published".
func (h *Harness) assertOutboxDelivered(tb testing.TB, walletID string) {
	tb.Helper()
	Eventually(tb, AuditTimeout, "outbox of wallet "+walletID+" published", func(ctx context.Context) (bool, error) {
		var pending int
		err := h.env.Owner.QueryRow(ctx, `SELECT count(*) FROM outbox_events
			WHERE message_group_id = $1 AND published_at IS NULL`, walletID).Scan(&pending)
		return pending == 0, err
	})
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	stored, err := OutboxPayloads(ctx, h.env.Owner, walletID)
	if err != nil {
		tb.Fatalf("wallet %s: %v", walletID, err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	for id, deliveries := range h.Audit.WaitFor(tb, ids...) {
		for _, m := range deliveries {
			if same, err := sameJSON(m.Body, stored[id]); err != nil || !same {
				tb.Errorf("wallet %s: event %s delivered %s, stored %s (%v)", walletID, id, m.Body, stored[id], err)
			}
		}
	}
}

// OutboxPayloads returns the payload column of every outbox event of a
// wallet, by event id.
func OutboxPayloads(ctx context.Context, pool *pgxpool.Pool, walletID string) (map[string][]byte, error) {
	rows, err := pool.Query(ctx, `SELECT event_id::text, payload FROM outbox_events WHERE message_group_id = $1`, walletID)
	if err != nil {
		return nil, fmt.Errorf("outbox payloads: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, fmt.Errorf("outbox payloads: %w", err)
		}
		out[id] = payload
	}
	return out, rows.Err()
}

// sameJSON compares two JSON documents by content: JSONB normalizes the text
// of the payload column (data-model §3.5).
func sameJSON(a, b []byte) (bool, error) {
	var va, vb any
	for raw, v := range map[*[]byte]*any{&a: &va, &b: &vb} {
		dec := json.NewDecoder(bytes.NewReader(*raw))
		dec.UseNumber()
		if err := dec.Decode(v); err != nil {
			return false, err
		}
	}
	return reflect.DeepEqual(va, vb), nil
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

// pollInterval is how often Eventually checks its condition.
const pollInterval = 50 * time.Millisecond

// Eventually polls cond until it holds, failing tb with what after timeout or
// on an error of cond (test-plan §1: waits have a deadline, never a sleep). It
// detaches from the test's context, so it also works in t.Cleanup.
func Eventually(tb testing.TB, timeout time.Duration, what string, cond func(ctx context.Context) (bool, error)) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), timeout)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		ok, err := cond(ctx)
		switch {
		case ctx.Err() != nil:
			tb.Fatalf("%s: not reached within %v", what, timeout)
			return
		case err != nil:
			tb.Fatalf("%s: %v", what, err)
			return
		case ok:
			return
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}
