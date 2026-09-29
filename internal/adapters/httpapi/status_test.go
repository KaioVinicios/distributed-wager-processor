package httpapi

// Internal test: writeError and resultStatus are the private mapping of
// spec §6.3, shared by every handler.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-09, D-04, TX-10 (U17: mapping)
func TestWriteError(t *testing.T) {
	input := func(code wagering.InputCode, field string) error {
		return apperrors.New(apperrors.KindInput, string(code), &wagering.ValidationError{Code: code, Field: field})
	}
	cases := []struct {
		name                  string
		err                   error
		status                int
		code, category, field string
		retryAfter            bool
	}{
		{"validation", input(wagering.InputInvalidAmount, "money.amount"), 400, "INVALID_AMOUNT", "CORRECTABLE", "money.amount", false},
		{"the key is a header", input(wagering.InputMissingIdempotencyKey, "idempotencyKey"), 400, "MISSING_IDEMPOTENCY_KEY", "CORRECTABLE", "Idempotency-Key", false},
		{"unknown wallet", apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", app.ErrNotFound), 400, "UNKNOWN_WALLET", "CORRECTABLE", "", false},
		{"not found", apperrors.New(apperrors.KindNotFound, "WALLET_NOT_FOUND", app.ErrNotFound), 404, "WALLET_NOT_FOUND", "CORRECTABLE", "", false},
		{"forbidden", apperrors.New(apperrors.KindForbidden, "PROVIDER_MISMATCH", nil), 403, "PROVIDER_MISMATCH", "CORRECTABLE", "", false},
		{"idempotency conflict", apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused}), 409, "IDEMPOTENCY_KEY_REUSED", "CORRECTABLE", "", false},
		{"wallet exists", apperrors.New(apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists), 409, "WALLET_ALREADY_EXISTS", "DEFINITIVE", "", false},
		{"transient", apperrors.New(apperrors.KindTransient, "", errors.New("lock timeout")), 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"unclassified", errors.New("boom"), 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"canceled", context.Canceled, 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"permanent", apperrors.New(apperrors.KindPermanent, "", errors.New("corrupted")), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"business at the edge", apperrors.New(apperrors.KindBusiness, "", errors.New("unexpected")), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"code outside the catalog", apperrors.New(apperrors.KindInput, "WHATEVER", nil), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"code of another status", apperrors.New(apperrors.KindNotFound, "UNKNOWN_WALLET", nil), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			writeError(rec, req, slog.New(slog.DiscardHandler), tc.err)
			var p struct {
				Status   int    `json:"status"`
				Code     string `json:"code"`
				Category string `json:"category"`
				Field    string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if rec.Code != tc.status || p.Status != tc.status || p.Code != tc.code || p.Category != tc.category || p.Field != tc.field {
				t.Fatalf("%d %+v, want %d %s %s %q", rec.Code, p, tc.status, tc.code, tc.category, tc.field)
			}
			if got := rec.Header().Get("Retry-After"); (got == "1") != tc.retryAfter || (got != "" && got != "1") {
				t.Fatalf("Retry-After = %q, want it only on 503", got)
			}
		})
	}
}

// Covers: D-04 (U17: result status)
func TestResultStatus(t *testing.T) {
	for status, want := range map[wagering.Status]int{
		wagering.StatusProcessed:        http.StatusOK,
		wagering.StatusPendingReference: http.StatusAccepted,
		wagering.StatusRejected:         http.StatusUnprocessableEntity,
		wagering.StatusFailed:           http.StatusInternalServerError,
		wagering.StatusPending:          http.StatusInternalServerError,
	} {
		if got := resultStatus(status); got != want {
			t.Errorf("resultStatus(%s) = %d, want %d", status, got, want)
		}
	}
}
