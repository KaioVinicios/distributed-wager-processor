package postgres

// Internal test: translate is unexported on purpose (the adapter is the only
// place that knows SQLSTATEs), so U09b tests it from inside the package.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// Covers: TX-10, DB-02, OBS-02 (U09b)
func TestPostgresErrorMapping(t *testing.T) {
	const row = "Failing row contains (0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1, BRL, 99999)"
	cases := []struct {
		name       string
		code       string
		constraint string
		wantKind   apperrors.Kind
		wantCode   string
		wantErr    error
	}{
		{"wallet already exists", "23505", "wallets_player_currency_uq", apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
		{"idempotency key race", "23505", "wager_tx_idempotency_uq", apperrors.KindTransient, "", app.ErrIdempotencyRace},
		{"external id race", "23505", "wager_tx_external_id_uq", apperrors.KindTransient, "", app.ErrIdempotencyRace},
		{"reversal race", "23505", "wager_tx_single_reversal_uq", apperrors.KindTransient, "", app.ErrReversalRace},
		{"inbox duplicate", "23505", "inbox_pk", apperrors.KindTransient, "", app.ErrInboxDuplicate},
		{"other unique", "23505", "wager_tx_single_opening_uq", apperrors.KindPermanent, "", nil},
		{"not null", "23502", "", apperrors.KindPermanent, "", nil},
		{"foreign key", "23503", "ledger_tx_wallet_fk", apperrors.KindPermanent, "", nil},
		{"check", "23514", "wallets_balance_minor_check", apperrors.KindPermanent, "", nil},
		{"numeric overflow", "22003", "", apperrors.KindPermanent, "", nil},
		{"read only transaction", "25006", "", apperrors.KindPermanent, "", nil},
		{"insufficient privilege", "42501", "", apperrors.KindPermanent, "", nil},
		{"ledger append-only", "PDA01", "", apperrors.KindPermanent, "", nil},
		{"terminal transaction", "PDA02", "", apperrors.KindPermanent, "", nil},
		{"wallet guard", "PDA03", "", apperrors.KindPermanent, "", nil},
		{"ledger coupling", "PDA04", "", apperrors.KindPermanent, "", nil},
		{"outbox snapshot", "PDA05", "", apperrors.KindPermanent, "", nil},
		{"connection failure", "08006", "", apperrors.KindTransient, "", nil},
		{"connection does not exist", "08003", "", apperrors.KindTransient, "", nil},
		{"serialization failure", "40001", "", apperrors.KindTransient, "", nil},
		{"deadlock", "40P01", "", apperrors.KindTransient, "", app.ErrLockTimeout},
		{"lock timeout", "55P03", "", apperrors.KindTransient, "", app.ErrLockTimeout},
		{"admin shutdown", "57P01", "", apperrors.KindTransient, "", nil},
		{"query canceled", "57014", "", apperrors.KindTransient, "", nil},
		{"too many connections", "53300", "", apperrors.KindTransient, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := fmt.Errorf("exec: %w", &pgconn.PgError{
				Code: tc.code, ConstraintName: tc.constraint,
				Message: "message with a value 99999", Detail: row, Hint: "hint", Where: "where",
			})
			got := translate(in)
			if k := apperrors.Classify(got); k != tc.wantKind {
				t.Fatalf("Classify(translate(%s)) = %q, want %q (err = %v)", tc.code, k, tc.wantKind, got)
			}
			if c := apperrors.CodeOf(got); c != tc.wantCode {
				t.Fatalf("CodeOf = %q, want %q", c, tc.wantCode)
			}
			if tc.wantErr != nil && !errors.Is(got, tc.wantErr) {
				t.Fatalf("errors.Is(%v, %v) = false", got, tc.wantErr)
			}
			var pgErr *pgconn.PgError
			if errors.As(got, &pgErr) {
				t.Fatalf("translated error still carries the *pgconn.PgError: %v", got)
			}
			msg := got.Error()
			if strings.Contains(msg, "99999") || strings.Contains(msg, "Failing row") || !strings.Contains(msg, tc.code) {
				t.Fatalf("message %q must name the SQLSTATE and carry no value from the database", msg)
			}
			if tc.constraint != "" && !strings.Contains(msg, tc.constraint) {
				t.Fatalf("message %q must name the constraint %s", msg, tc.constraint)
			}
		})
	}
}

// Covers: TX-10, DOM-06 (U09b)
func TestPostgresErrorMapping_PassesOtherErrorsUntouched(t *testing.T) {
	if translate(nil) != nil {
		t.Fatal("translate(nil) != nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("dial tcp: connection refused")} {
		if got := translate(err); got != err { //nolint:errorlint // identity is the point: nothing is wrapped
			t.Fatalf("translate(%v) = %v, want the same error", err, got)
		}
		if k := apperrors.Classify(translate(err)); k != apperrors.KindTransient {
			t.Fatalf("Classify(%v) = %q, want TRANSIENT (D-05)", err, k)
		}
	}
}
