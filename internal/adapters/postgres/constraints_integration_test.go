//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// base are the valid rows every constraint case starts from.
type base struct {
	wallet, player, wallet2, opening, bet, entry string
}

func seedBase(ctx context.Context, tx pgx.Tx) (base, error) {
	b := base{wallet: newID(), player: newID(), wallet2: newID(), opening: newID(), bet: newID(), entry: newID()}
	steps := []struct {
		table string
		r     row
	}{
		{"wallets", walletRow(b.wallet, b.player, 10000)},
		{"wallets", walletRow(b.wallet2, newID(), 0)},
		{"wager_transactions", openingRow(b.opening, b.wallet, b.player, 10000)},
		{"wager_transactions", externalRow(b.bet, b.wallet, b.player)},
		{"wallet_ledger_entries", ledgerRow(b.entry, b.wallet, b.opening, "CREDIT", 10000, 0, 10000, 1)},
		{"inbox_messages", inboxRow("consumer", "msg-1")},
		{"outbox_events", outboxRow(b.entry, b.wallet)},
	}
	for _, s := range steps {
		if err := insert(ctx, tx, s.table, s.r); err != nil {
			return base{}, err
		}
	}
	return b, nil
}

type constraintCase struct {
	name, table string
	setup       func(b base) (string, row) // an extra valid row inserted first (optional)
	row         func(b base) row
	code        string
	constraint  string // "" when PostgreSQL does not name it
}

func constraintCases() []constraintCase {
	ext := func(b base) row { return externalRow(newID(), b.wallet, b.player) }
	entry := func(b base, dir string, amount, before, after, version int64) row {
		return ledgerRow(newID(), b.wallet, b.bet, dir, amount, before, after, version)
	}
	return []constraintCase{
		// wallets (data-model §3.1)
		{name: "wallet currency not ISO", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("currency", "brl") }, code: "23514", constraint: "wallets_currency_check"},
		{name: "negative balance (WAL-04, E4)", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), -1) }, code: "23514", constraint: "wallets_balance_minor_check"},
		{name: "version below 1 (WAL-07)", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("version", int64(0)) }, code: "23514", constraint: "wallets_version_check"},
		{name: "updated before created", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("updated_at", ts.Add(-1)) }, code: "23514", constraint: "wallets_updated_after_created"},
		{name: "second wallet for player and currency (WAL-03)", table: "wallets", row: func(b base) row { return walletRow(newID(), b.player, 0) }, code: "23505", constraint: "wallets_player_currency_uq"},
		{name: "wallet without player", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("player_id", nil) }, code: "23502"},

		// wager_transactions (data-model §3.2)
		{name: "unknown origin", table: "wager_transactions", row: func(b base) row { return ext(b).with("origin", "OTHER") }, code: "23514", constraint: "wager_transactions_origin_check"},
		{name: "unknown kind", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "JACKPOT") }, code: "23514", constraint: "wager_transactions_kind_check"},
		{name: "PENDING is never persisted (TX-09)", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("status", "PENDING", "failure_code", nil, "result_balance_minor", nil, "completed_at", nil)
		}, code: "23514", constraint: "wager_transactions_status_check"},
		{name: "negative amount", table: "wager_transactions", row: func(b base) row { return ext(b).with("amount_minor", int64(-1)) }, code: "23514", constraint: "wager_transactions_amount_minor_check"},
		{name: "operation currency not ISO", table: "wager_transactions", row: func(b base) row { return ext(b).with("currency", "br") }, code: "23514", constraint: "wager_transactions_currency_check"},
		{name: "payload hash not lowercase hex", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("payload_hash", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789")
		}, code: "23514", constraint: "wager_transactions_payload_hash_check"},
		{name: "unknown channel", table: "wager_transactions", row: func(b base) row { return ext(b).with("received_via", "GRPC") }, code: "23514", constraint: "wager_transactions_received_via_check"},
		{name: "negative result balance", table: "wager_transactions", row: func(b base) row { return ext(b).with("result_balance_minor", int64(-1)) }, code: "23514", constraint: "wager_transactions_result_balance_minor_check"},
		{name: "negative attempts", table: "wager_transactions", row: func(b base) row { return ext(b).with("attempts", -1) }, code: "23514", constraint: "wager_transactions_attempts_check"},
		{name: "unknown wallet", table: "wager_transactions", row: func(b base) row { return ext(b).with("wallet_id", newID()) }, code: "23503", constraint: "wager_transactions_wallet_id_fkey"},
		{name: "unknown resolved reference", table: "wager_transactions", row: func(b base) row { return ext(b).with("reference_transaction_id", newID()) }, code: "23503", constraint: "wager_transactions_reference_transaction_id_fkey"},
		{name: "external OPENING (TX-05)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "OPENING") }, code: "23514", constraint: "wager_tx_origin_kind"},
		{name: "OPENING with provider metadata (TX-05)", table: "wager_transactions", row: func(b base) row {
			return openingRow(newID(), b.wallet2, b.player, 500).with("provider_id", "provider-a")
		}, code: "23514", constraint: "wager_tx_internal_fields"},
		{name: "external operation without round", table: "wager_transactions", row: func(b base) row { return ext(b).with("round_id", nil) }, code: "23514", constraint: "wager_tx_external_fields"},
		{name: "LOSS with an amount (OPS-03)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "LOSS") }, code: "23514", constraint: "wager_tx_amount_policy"},
		{name: "BET of zero (OPS-11)", table: "wager_transactions", row: func(b base) row { return ext(b).with("amount_minor", int64(0)) }, code: "23514", constraint: "wager_tx_amount_policy"},
		{name: "BET with a reference (OPS-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("reference_external_transaction_id", "ext-x") }, code: "23514", constraint: "wager_tx_reference_policy"},
		{name: "REFUND without a reference (OPS-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "REFUND") }, code: "23514", constraint: "wager_tx_reference_policy"},
		{name: "processed REFUND without resolved reference", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("kind", "REFUND", "status", "PROCESSED", "failure_code", nil, "reference_external_transaction_id", "ext-x")
		}, code: "23514", constraint: "wager_tx_resolved_reference"},
		{name: "processed WIN without resolved reference", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("kind", "WIN", "status", "PROCESSED", "failure_code", nil, "reference_external_transaction_id", "ext-x")
		}, code: "23514", constraint: "wager_tx_resolved_win_reference"},
		{name: "REJECTED without failure code (TX-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("failure_code", nil) }, code: "23514", constraint: "wager_tx_failure_code"},
		{name: "PROCESSED with failure code", table: "wager_transactions", row: func(b base) row { return ext(b).with("status", "PROCESSED") }, code: "23514", constraint: "wager_tx_failure_code"},
		{name: "PROCESSED without result balance (IDEM-08)", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("status", "PROCESSED", "failure_code", nil, "result_balance_minor", nil)
		}, code: "23514", constraint: "wager_tx_result_balance"},
		{name: "terminal without completion instant", table: "wager_transactions", row: func(b base) row { return ext(b).with("completed_at", nil) }, code: "23514", constraint: "wager_tx_completed_at"},
		{name: "PENDING_REFERENCE without schedule (TX-09)", table: "wager_transactions", row: func(b base) row {
			return pendingRow(newID(), b.wallet, b.player).with("next_attempt_at", nil)
		}, code: "23514", constraint: "wager_tx_pending_schedule"},
		{name: "idempotency key reused (IDEM-02, E6)", table: "wager_transactions", row: func(b base) row { return ext(b).with("idempotency_key", "key-"+b.bet) }, code: "23505", constraint: "wager_tx_idempotency_uq"},
		{name: "external id reused (IDEM-07)", table: "wager_transactions", row: func(b base) row { return ext(b).with("external_transaction_id", "ext-"+b.bet) }, code: "23505", constraint: "wager_tx_external_id_uq"},
		{name: "second OPENING (TX-05)", table: "wager_transactions", row: func(b base) row { return openingRow(newID(), b.wallet, b.player, 500) }, code: "23505", constraint: "wager_tx_single_opening_uq"},
		{
			name: "second successful reversal (OPS-08, D-10)", table: "wager_transactions",
			setup: func(b base) (string, row) { return "wager_transactions", reversalRow(b) },
			row:   reversalRow, code: "23505", constraint: "wager_tx_single_reversal_uq",
		},

		// wallet_ledger_entries (data-model §3.3)
		{name: "unknown direction", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "UP", 10, 0, 10, 2) }, code: "23514"},
		{name: "zero amount (LED-06)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 0, 0, 0, 2) }, code: "23514", constraint: "wallet_ledger_entries_amount_minor_check"},
		{name: "entry currency not ISO", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 2).with("currency", "brl") }, code: "23514", constraint: "wallet_ledger_entries_currency_check"},
		{name: "negative balance before", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, -1, 9, 2) }, code: "23514", constraint: "wallet_ledger_entries_balance_before_minor_check"},
		{name: "negative balance after (LED-06)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "DEBIT", 1, 0, -1, 2) }, code: "23514", constraint: "wallet_ledger_entries_balance_after_minor_check"},
		{name: "version below 1", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 0) }, code: "23514", constraint: "wallet_ledger_entries_wallet_version_check"},
		{name: "after != before ± amount (LED-02)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 11, 2) }, code: "23514", constraint: "ledger_balance_math"},
		{name: "second entry for a transaction (LED-03, E5)", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), b.wallet, b.opening, "CREDIT", 10, 10000, 10010, 2)
		}, code: "23505", constraint: "ledger_wallet_tx_uq"},
		{name: "second entry for a wallet version (D-16)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 1) }, code: "23505", constraint: "ledger_wallet_version_uq"},
		{name: "transaction of another wallet", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), b.wallet2, b.opening, "CREDIT", 10, 0, 10, 2)
		}, code: "23503", constraint: "ledger_tx_wallet_fk"},
		{name: "unknown wallet", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), newID(), b.bet, "CREDIT", 10, 0, 10, 2)
		}, code: "23503"},

		// inbox_messages (data-model §3.4)
		{name: "message hash not lowercase hex", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("message_hash", "x") }, code: "23514", constraint: "inbox_messages_message_hash_check"},
		{name: "unknown outcome", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("outcome", "DONE") }, code: "23514", constraint: "inbox_messages_outcome_check"},
		{name: "message recorded twice (SQS-03)", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", "msg-1") }, code: "23505", constraint: "inbox_pk"},
		{name: "unknown transaction", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("transaction_id", newID()) }, code: "23503", constraint: "inbox_messages_transaction_id_fkey"},

		// outbox_events (data-model §3.5)
		{name: "unknown aggregate type", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("aggregate_type", "Player") }, code: "23514", constraint: "outbox_events_aggregate_type_check"},
		{name: "unknown event type", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("event_type", "WalletClosed") }, code: "23514", constraint: "outbox_events_event_type_check"},
		{name: "event version below 1", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("event_version", 0) }, code: "23514", constraint: "outbox_events_event_version_check"},
		{name: "negative publish attempts", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("attempts", -1) }, code: "23514", constraint: "outbox_events_attempts_check"},
		{name: "lease owner without expiry", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("locked_by", "instance-1") }, code: "23514", constraint: "outbox_lock_pair"},
		{name: "event recorded twice", table: "outbox_events", row: func(b base) row { return outboxRow(b.entry, b.wallet) }, code: "23505", constraint: "outbox_events_pkey"},
	}
}

// reversalRow is a PROCESSED REFUND of the base BET.
func reversalRow(b base) row {
	return externalRow(newID(), b.wallet, b.player).with(
		"kind", "REFUND", "status", "PROCESSED", "failure_code", nil,
		"reference_external_transaction_id", "ext-"+b.bet, "reference_transaction_id", b.bet)
}

// Covers: TST-I02, DB-03, WAL-03, WAL-04, WAL-07, TX-05, TX-09, LED-02, LED-03, LED-06, IDEM-02, IDEM-07, OPS-03, OPS-06, OPS-08, OPS-11, SQS-03, E4, E5 (I02a)
//
// The triggers of 000005 fire BEFORE the CHECKs and would mask them, so this
// test runs on its own database with the user triggers disabled; I02b–I02e
// test the triggers on the full schema.
func TestConstraints(t *testing.T) {
	_, pool := isolatedDB(t, "constraints")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers on %s: %v", table, err)
		}
	}
	for _, tc := range constraintCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			b, err := seedBase(ctx, tx)
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			if tc.setup != nil {
				table, r := tc.setup(b)
				if err := insert(ctx, tx, table, r); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			wantSQLState(t, insert(ctx, tx, tc.table, tc.row(b)), tc.code, tc.constraint)
		})
	}
}
