//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stmt is one step of a protection case: a SQL statement, or a row to insert.
type stmt struct {
	sql   string
	args  []any
	table string
	row   row
}

func exec(sql string, args ...any) stmt { return stmt{sql: sql, args: args} }

func ins(table string, r row) stmt { return stmt{table: table, row: r} }

func run(ctx context.Context, tx pgx.Tx, s stmt) error {
	if s.table != "" {
		return insert(ctx, tx, s.table, s.row)
	}
	_, err := tx.Exec(ctx, s.sql, s.args...)
	return err
}

// attempt runs the statements in one transaction that is always rolled back,
// and returns the first error. Nothing it does survives.
func attempt(t *testing.T, pool *pgxpool.Pool, stmts ...stmt) error {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, s := range stmts {
		if err := run(ctx, tx, s); err != nil {
			return err
		}
	}
	return nil
}

// attemptCommit runs the statements and commits: the deferred triggers only
// check at COMMIT. It returns the first error, from a statement or the commit.
func attemptCommit(t *testing.T, pool *pgxpool.Pool, stmts ...stmt) error {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, s := range stmts {
		if err := run(ctx, tx, s); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// seeded is a committed wallet with a 100.00 opening: the wallet, its
// PROCESSED OPENING and the CREDIT entry, written in the order the triggers
// require (data-model §4.2).
type seeded struct {
	wallet, player, opening, entry string
}

func seedWallet(t *testing.T) seeded {
	t.Helper()
	s := seeded{wallet: newID(), player: newID(), opening: newID(), entry: newID()}
	if err := attemptCommit(t, env.Owner,
		ins("wallets", walletRow(s.wallet, s.player, 10000)),
		ins("wager_transactions", openingRow(s.opening, s.wallet, s.player, 10000)),
		ins("wallet_ledger_entries", ledgerRow(s.entry, s.wallet, s.opening, "CREDIT", 10000, 0, 10000, 1)),
	); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return s
}

// seedRows commits extra rows for a seeded wallet.
func seedRows(t *testing.T, stmts ...stmt) {
	t.Helper()
	if err := attemptCommit(t, env.Owner, stmts...); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
}

func moveWallet(s seeded, balance, version int64) stmt {
	return exec(`UPDATE wallets SET balance_minor = $2, version = $3 WHERE id = $1`, s.wallet, balance, version)
}

// processed is a PROCESSED external operation of the seeded wallet.
func processed(s seeded, id, kind string, amount, result int64) row {
	return externalRow(id, s.wallet, s.player).with("kind", kind, "status", "PROCESSED",
		"failure_code", nil, "amount_minor", amount, "result_balance_minor", result)
}

// Covers: TST-I02, LED-04, E9 (I02b, as the owner)
func TestLedgerImmutable(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	mutations := map[string]stmt{
		"update":   exec(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, s.entry),
		"delete":   exec(`DELETE FROM wallet_ledger_entries WHERE id = $1`, s.entry),
		"truncate": exec(`TRUNCATE wallet_ledger_entries`),
	}
	for name, m := range mutations {
		t.Run("owner "+name, func(t *testing.T) {
			wantSQLState(t, attempt(t, env.Owner, m), "PDA01", "")
		})
	}
}

// Covers: TST-I02, WAL-06, LED-05, E9 (I02c)
func TestLedgerCoupling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		stmts    func(s seeded) []stmt
		atCommit bool
		code     string
	}{
		{name: "entry does not match the wallet state", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, s.wallet, s.player)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 1000, 10000, 9000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry for a LOSS", stmts: func(s seeded) []stmt {
			loss := newID()
			return []stmt{
				moveWallet(s, 15000, 2),
				ins("wager_transactions", processed(s, loss, "LOSS", 0, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, loss, "CREDIT", 5000, 10000, 15000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry for a REJECTED operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 15000, 2),
				ins("wager_transactions", externalRow(bet, s.wallet, s.player)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "CREDIT", 5000, 10000, 15000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry amount differs from the operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 3000, 7000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry currency differs from the operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 8000).with("currency", "USD")),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
			}
		}, code: "PDA04"},
		{name: "BET credited", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 12000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 12000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "CREDIT", 2000, 10000, 12000, 2)),
			}
		}, code: "PDA04"},
		{name: "ROLLBACK of a BET debited", stmts: func(s seeded) []stmt {
			bet, rollback := newID(), newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 8000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
				moveWallet(s, 6000, 3),
				ins("wager_transactions", processed(s, rollback, "ROLLBACK", 2000, 6000).with(
					"reference_external_transaction_id", "ext-"+bet, "reference_transaction_id", bet)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, rollback, "DEBIT", 2000, 8000, 6000, 3)),
			}
		}, code: "PDA04"},
		// The trigger's comparisons with a missing wallet and transaction are NULL
		// and do not raise; the foreign keys reject the entry (M2 spec §5, gap 5).
		{name: "entry for a missing wallet and operation stops at the foreign key", stmts: func(seeded) []stmt {
			return []stmt{ins("wallet_ledger_entries", ledgerRow(newID(), newID(), newID(), "CREDIT", 10, 0, 10, 2))}
		}, code: "23503"},
		{name: "balance changed without an entry", stmts: func(s seeded) []stmt {
			return []stmt{moveWallet(s, 5000, 2)}
		}, atCommit: true, code: "PDA04"},
		{name: "PROCESSED operation without its entry", stmts: func(s seeded) []stmt {
			return []stmt{ins("wager_transactions", processed(s, newID(), "BET", 2000, 8000))}
		}, atCommit: true, code: "PDA04"},
		{name: "wallet opened with a balance and no entry", stmts: func(seeded) []stmt {
			return []stmt{ins("wallets", walletRow(newID(), newID(), 500))}
		}, atCommit: true, code: "PDA04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stmts := tc.stmts(seedWallet(t))
			var err error
			if tc.atCommit {
				err = attemptCommit(t, env.Owner, stmts...)
			} else {
				err = attempt(t, env.Owner, stmts...)
			}
			wantSQLState(t, err, tc.code, "")
		})
	}
}

// Covers: TST-I02, TX-07 (I02d)
func TestTerminalTransactionImmutable(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	rejected, failed := newID(), newID()
	seedRows(t,
		ins("wager_transactions", externalRow(rejected, s.wallet, s.player)),
		ins("wager_transactions", externalRow(failed, s.wallet, s.player).with(
			"status", "FAILED", "failure_code", "INTERNAL_PERMANENT_FAILURE", "result_balance_minor", nil)),
	)
	for status, id := range map[string]string{"PROCESSED": s.opening, "REJECTED": rejected, "FAILED": failed} {
		t.Run(status, func(t *testing.T) {
			err := attempt(t, env.Owner, exec(`UPDATE wager_transactions SET updated_at = updated_at + interval '1 second' WHERE id = $1`, id))
			wantSQLState(t, err, "PDA02", "")
		})
	}
}

// Covers: TST-I02, WAL-07, TX-07, OUT-01 (I02e)
func TestGuardTriggers(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	pending, event := newID(), newID()
	seedRows(t,
		ins("wager_transactions", pendingRow(pending, s.wallet, s.player)),
		ins("outbox_events", outboxRow(event, s.wallet)),
	)
	wallet := func(set string, args ...any) stmt {
		return exec(`UPDATE wallets SET `+set+` WHERE id = $1`, append([]any{s.wallet}, args...)...)
	}
	tx := func(set string, args ...any) stmt {
		return exec(`UPDATE wager_transactions SET `+set+` WHERE id = $1`, append([]any{pending}, args...)...)
	}
	outbox := func(set string, args ...any) stmt {
		return exec(`UPDATE outbox_events SET `+set+` WHERE event_id = $1`, append([]any{event}, args...)...)
	}
	cases := []struct {
		name  string
		stmts []stmt
		code  string // "" = allowed
	}{
		{"wallet player changed", []stmt{wallet(`player_id = $2`, newID())}, "PDA03"},
		{"wallet currency changed", []stmt{wallet(`currency = 'USD'`)}, "PDA03"},
		{"wallet creation instant changed", []stmt{wallet(`created_at = created_at - interval '1 second'`)}, "PDA03"},
		{"balance changed without version + 1", []stmt{wallet(`balance_minor = 5000`)}, "PDA03"},
		{"balance changed with version + 2", []stmt{wallet(`balance_minor = 5000, version = 3`)}, "PDA03"},
		{"version changed without balance change", []stmt{wallet(`version = 2`)}, "PDA03"},
		{"wallet deleted", []stmt{exec(`DELETE FROM wallets WHERE id = $1`, s.wallet)}, "PDA03"},
		{"wallet touched without a balance change is allowed", []stmt{wallet(`updated_at = updated_at + interval '1 second'`)}, ""},
		{"operation amount changed", []stmt{tx(`amount_minor = 999`)}, "PDA02"},
		{"operation idempotency key changed", []stmt{tx(`idempotency_key = 'other'`)}, "PDA02"},
		{"operation deleted", []stmt{exec(`DELETE FROM wager_transactions WHERE id = $1`, pending)}, "PDA02"},
		{"pending operation rescheduled is allowed", []stmt{tx(`attempts = 1, next_attempt_at = next_attempt_at + interval '1 second'`)}, ""},
		{"event payload changed", []stmt{outbox(`payload = '{}'`)}, "PDA05"},
		{"event type changed", []stmt{outbox(`event_type = 'WagerTransactionProcessed'`)}, "PDA05"},
		{"event occurrence changed", []stmt{outbox(`occurred_at = occurred_at + interval '1 second'`)}, "PDA05"},
		{"event unpublished", []stmt{outbox(`published_at = now()`), outbox(`published_at = NULL`)}, "PDA05"},
		{"publication control is allowed", []stmt{
			outbox(`attempts = 1, locked_by = 'instance-1', locked_until = now()`),
			outbox(`published_at = now(), locked_by = NULL, locked_until = NULL`),
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := attempt(t, env.Owner, tc.stmts...)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("allowed change failed: %v", err)
				}
				return
			}
			wantSQLState(t, err, tc.code, "")
		})
	}
}
