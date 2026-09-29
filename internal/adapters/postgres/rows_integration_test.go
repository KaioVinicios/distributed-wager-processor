//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// newID returns a UUIDv7, as the app generates them.
func newID() string { return uuid.Must(uuid.NewV7()).String() }

// row is one table row by column; nil is NULL. The schema tests write rows with
// raw SQL to check the database alone, without the repositories.
type row map[string]any

// with returns a copy of r with the given column/value pairs replaced.
func (r row) with(kv ...any) row {
	out := maps.Clone(r)
	for i := 0; i < len(kv); i += 2 {
		column, ok := kv[i].(string)
		if !ok {
			panic("row.with: columns are strings")
		}
		out[column] = kv[i+1]
	}
	return out
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insert(ctx context.Context, q execer, table string, r row) error {
	cols := slices.Sorted(maps.Keys(r))
	args := make([]any, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		args[i], marks[i] = r[c], "$"+strconv.Itoa(i+1)
	}
	_, err := q.Exec(ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
	return err
}

// ts is a fixed instant with microsecond precision.
var ts = time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)

const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func walletRow(id, playerID string, balance int64) row {
	return row{
		"id": id, "player_id": playerID, "currency": "BRL", "balance_minor": balance,
		"version": int64(1), "created_at": ts, "updated_at": ts,
	}
}

// openingRow is the INTERNAL OPENING of a positive initial balance.
func openingRow(id, walletID, playerID string, amount int64) row {
	return row{
		"id": id, "origin": "INTERNAL", "kind": "OPENING", "status": "PROCESSED",
		"wallet_id": walletID, "player_id": playerID, "amount_minor": amount, "currency": "BRL",
		"result_balance_minor": amount, "correlation_id": "corr", "created_at": ts, "updated_at": ts, "completed_at": ts,
	}
}

// externalRow is a REJECTED BET: valid under every constraint and trigger
// without a ledger entry.
func externalRow(id, walletID, playerID string) row {
	return row{
		"id": id, "origin": "EXTERNAL", "kind": "BET", "status": "REJECTED",
		"wallet_id": walletID, "player_id": playerID, "amount_minor": int64(1000), "currency": "BRL",
		"provider_id": "provider-a", "external_transaction_id": "ext-" + id, "idempotency_key": "key-" + id,
		"payload_hash": hash, "round_id": "round-1", "game_id": "game-1", "received_via": "HTTP",
		"failure_code": "INSUFFICIENT_FUNDS", "result_balance_minor": int64(0), "attempts": 0,
		"correlation_id": "corr", "created_at": ts, "updated_at": ts, "completed_at": ts,
	}
}

// pendingRow is a REFUND waiting for its reference.
func pendingRow(id, walletID, playerID string) row {
	return externalRow(id, walletID, playerID).with(
		"kind", "REFUND", "status", "PENDING_REFERENCE", "failure_code", nil, "result_balance_minor", nil,
		"completed_at", nil, "reference_external_transaction_id", "ext-missing",
		"next_attempt_at", ts, "expires_at", ts.Add(time.Minute))
}

func ledgerRow(id, walletID, txID, direction string, amount, before, after, version int64) row {
	return row{
		"id": id, "wallet_id": walletID, "transaction_id": txID, "direction": direction,
		"amount_minor": amount, "currency": "BRL", "balance_before_minor": before,
		"balance_after_minor": after, "wallet_version": version, "created_at": ts,
	}
}

func inboxRow(consumer, messageID string) row {
	return row{
		"consumer_name": consumer, "message_id": messageID, "message_hash": hash,
		"message_type": "WagerTransactionRequested", "outcome": "REJECTED", "received_at": ts, "processed_at": ts,
	}
}

func outboxRow(eventID, walletID string) row {
	return row{
		"event_id": eventID, "aggregate_type": "Wallet", "aggregate_id": walletID,
		"message_group_id": walletID, "event_type": "WalletBalanceChanged", "event_version": 1,
		"payload": `{"eventId":"` + eventID + `"}`, "correlation_id": "corr", "occurred_at": ts, "next_attempt_at": ts,
	}
}

// sqlState returns the SQLSTATE and constraint of a database error.
func sqlState(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

// wantSQLState fails unless err is the given SQLSTATE (and constraint, when
// not empty).
func wantSQLState(t *testing.T, err error, code, constraint string) {
	t.Helper()
	gotCode, gotConstraint := sqlState(err)
	if gotCode != code || (constraint != "" && gotConstraint != constraint) {
		t.Fatalf("error = %v (SQLSTATE %q, constraint %q), want SQLSTATE %q, constraint %q",
			err, gotCode, gotConstraint, code, constraint)
	}
}
