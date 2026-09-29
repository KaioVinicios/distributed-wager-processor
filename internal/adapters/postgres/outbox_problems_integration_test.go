//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// event is an outbox row of type typ for the operation txID of wallet w.
func event(typ, w, txID string) stmt {
	aggregate, id := "WagerTransaction", txID
	if typ == "WalletBalanceChanged" {
		aggregate, id = "Wallet", w
	}
	return ins("outbox_events", outboxRow(newID(), w).with(
		"event_type", typ, "aggregate_type", aggregate, "aggregate_id", id,
		"payload", `{"data":{"transactionId":"`+txID+`"}}`))
}

// Sensitivity of testkit.OutboxProblems, item 7 of test-plan §6: each
// divergence from the event matrix of lifecycle §7, written with the triggers
// disabled on a database of its own, is reported.
func TestOutboxProblemsDetectsDivergence(t *testing.T) {
	t.Parallel()
	_, pool := isolatedDB(t, "outboxcheck")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers: %v", err)
		}
	}
	cases := []struct {
		name string
		rows func(w, p, o string) []stmt
		want string // empty when the events match the matrix
	}{
		{"consistent", func(w, p, o string) []stmt {
			rejected, pending := newID(), newID()
			return []stmt{
				ins("wager_transactions", externalRow(rejected, w, p)),
				event("WagerTransactionRejected", w, rejected),
				ins("wager_transactions", pendingRow(pending, w, p)),
				event("WagerTransactionPendingReference", w, pending),
			}
		}, ""},
		{"processed without its balance change", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "PROCESSED", "failure_code", nil)),
				event("WagerTransactionProcessed", w, bet),
			}
		}, "changed=0"},
		{"rejection twice", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p)),
				event("WagerTransactionRejected", w, bet), event("WagerTransactionRejected", w, bet),
			}
		}, "rejected=2"},
		{"failed with an event", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "FAILED", "failure_code", "INTERNAL_PERMANENT_FAILURE", "result_balance_minor", nil)),
				event("WagerTransactionRejected", w, bet),
			}
		}, "rejected=1"},
		{"pending without its event", func(w, p, o string) []stmt {
			return []stmt{ins("wager_transactions", pendingRow(newID(), w, p))}
		}, "pending=0"},
		{"LOSS with a balance change", func(w, p, o string) []stmt {
			loss := newID()
			return []stmt{
				ins("wager_transactions", externalRow(loss, w, p).with("kind", "LOSS", "amount_minor", int64(0), "status", "PROCESSED", "failure_code", nil)),
				event("WagerTransactionProcessed", w, loss), event("WalletBalanceChanged", w, loss),
			}
		}, "changed=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, p, o := newID(), newID(), newID()
			stmts := append([]stmt{
				ins("wallets", walletRow(w, p, 10000)),
				ins("wager_transactions", openingRow(o, w, p, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, o, "CREDIT", 10000, 0, 10000, 1)),
				event("WagerTransactionProcessed", w, o), event("WalletBalanceChanged", w, o),
			}, tc.rows(w, p, o)...)
			if err := attemptCommit(t, pool, stmts...); err != nil {
				t.Fatalf("seed: %v", err)
			}
			problems, err := testkit.OutboxProblems(t.Context(), pool, w)
			if err != nil {
				t.Fatalf("OutboxProblems: %v", err)
			}
			joined := strings.Join(problems, "; ")
			if tc.want == "" && len(problems) != 0 || tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("problems = %q, want %q", joined, tc.want)
			}
		})
	}
}
