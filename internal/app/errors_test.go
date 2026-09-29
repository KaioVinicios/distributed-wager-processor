package app

// Internal test: domainError is the private translation every use case
// applies to what the domain returns (spec decision 6).

import (
	"errors"
	"fmt"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Covers: TX-10, DOM-04 (U13)
func TestDomainErrorKind(t *testing.T) {
	transient := apperrors.New(apperrors.KindTransient, "", errors.New("lock timeout"))
	cases := []struct {
		name     string
		err      error
		wantKind apperrors.Kind
		wantCode string
	}{
		{"validation error", &wagering.ValidationError{Code: wagering.InputInvalidAmount, Field: "money.amount"}, apperrors.KindInput, "INVALID_AMOUNT"},
		{"conflict error", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused}, apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED"},
		{"already classified", fmt.Errorf("wrapped: %w", transient), apperrors.KindTransient, ""},
		{"money overflow", fmt.Errorf("%w: credit", money.ErrOverflow), apperrors.KindPermanent, ""},
		{"currency mismatch", money.ErrCurrencyMismatch, apperrors.KindPermanent, ""},
		{"uninitialized money", money.ErrUninitialized, apperrors.KindPermanent, ""},
		{"invalid transition", wagering.ErrInvalidTransition, apperrors.KindPermanent, ""},
		{"invalid argument", wagering.ErrInvalidArgument, apperrors.KindPermanent, ""},
		{"invalid snapshot", wagering.ErrInvalidSnapshot, apperrors.KindPermanent, ""},
		{"not persistable", wagering.ErrNotPersistable, apperrors.KindPermanent, ""},
		{"uninitialized transaction", wagering.ErrUninitialized, apperrors.KindPermanent, ""},
		{"invalid wallet", wallet.ErrInvalidWallet, apperrors.KindPermanent, ""},
		{"invalid ledger entry", wallet.ErrInvalidLedgerEntry, apperrors.KindPermanent, ""},
		{"invalid event", events.ErrInvalidEvent, apperrors.KindPermanent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainError(tc.err)
			if kind := apperrors.Classify(got); kind != tc.wantKind {
				t.Fatalf("Classify(domainError(%v)) = %q, want %q", tc.err, kind, tc.wantKind)
			}
			if code := apperrors.CodeOf(got); code != tc.wantCode {
				t.Fatalf("CodeOf = %q, want %q", code, tc.wantCode)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("domainError(%v) lost the original error", tc.err)
			}
		})
	}
	if domainError(nil) != nil {
		t.Fatal("domainError(nil) != nil")
	}
	var ve *wagering.ValidationError
	if !errors.As(domainError(&wagering.ValidationError{Code: wagering.InputMissingField, Field: "kind"}), &ve) || ve.Field != "kind" {
		t.Fatal("the validation error, with its field, must stay in the chain for the edge")
	}
}
