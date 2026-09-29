package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const (
	txID    = "0192f298-345e-7e38-af88-e43f851a819d"
	entryID = "0192f298-3460-7c02-8a10-66778899aabb"
)

func ptr(s string) *string { return &s }

func command(t *testing.T, kind, amount, ext, ref string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr("provider-a:" + ext), ProviderID: ptr("provider-a"), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(playerID), WalletID: ptr(walletID), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(kind), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr("BRL")},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// settled is the operation after Settle on a wallet with balance.
func settled(t *testing.T, cmd wagering.Command, balance string) *wagering.WagerTransaction {
	t.Helper()
	w := openedWallet(t, balance)
	tx, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, "corr", stamp)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 3, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wagering.Settle(tx, &w, wagering.Reference{}, wagering.SettleParams{EntryID: entryID, Now: stamp, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	return tx
}

func failed(t *testing.T, cmd wagering.Command) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, "corr", stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Fail(stamp); err != nil {
		t.Fatal(err)
	}
	return tx
}

func opening(t *testing.T) *wagering.WagerTransaction {
	t.Helper()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: walletID, PlayerID: playerID, Initial: brl(t, "100.00"),
		TransactionID: txID, EntryID: entryID, CorrelationID: "corr", Now: stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.Tx
}

type stubWagers struct {
	req   app.ProcessRequest
	res   app.ProcessResult
	err   error
	calls int
}

func (s *stubWagers) Execute(_ context.Context, req app.ProcessRequest) (app.ProcessResult, error) {
	s.req, s.calls = req, s.calls+1
	return s.res, s.err
}

// stubReader serves one operation.
type stubReader struct {
	stubWallets
	tx                 *wagering.WagerTransaction
	provider, external string
	calls              int
}

func (s *stubReader) GetTransaction(_ context.Context, id string) (*wagering.WagerTransaction, error) {
	s.calls++
	if s.tx == nil || id != s.tx.ID() {
		return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
	}
	return s.tx, nil
}

func (s *stubReader) GetTransactionByExternalID(_ context.Context, provider, external string) (*wagering.WagerTransaction, error) {
	s.provider, s.external, s.calls = provider, external, s.calls+1
	return s.tx, nil
}

const betBody = `{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"` + playerID + `","walletId":"` + walletID +
	`","roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`

func postWager(t *testing.T, s *stubWagers, token, body string, keys ...string) *httptest.ResponseRecorder {
	t.Helper()
	h := http.Header{"X-Correlation-Id": {"corr-wager"}}
	for _, k := range keys {
		h.Add("Idempotency-Key", k)
	}
	e := newEdge(t, httpapi.Services{Wagers: s}, false)
	return e.do(t, call{method: http.MethodPost, path: "/wagering/transactions", token: token, contentType: "application/json", body: body, header: h})
}

// Covers: HTTP-06, HTTP-09, AUTH-04, D-04 (U17: POST /wagering/transactions)
func TestSubmitWagerHandler(t *testing.T) {
	t.Run("the command reaches the use case", func(t *testing.T) {
		s := &stubWagers{res: app.ProcessResult{Tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}}
		rec := postWager(t, s, tokenProviderA, betBody, "provider-a:bet-1")
		want := `{"transactionId":"` + txID + `","status":"PROCESSED","balance":{"amount":"75.00","currency":"BRL"},"idempotentReplay":false}`
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
		}
		if s.req.Via != wagering.ReceivedViaHTTP || s.req.CorrelationID != "corr-wager" || s.req.CausationID != "" ||
			s.req.Command.IdempotencyKey() != "provider-a:bet-1" || s.req.Command.PayloadHash() != command(t, "BET", "25.00", "bet-1", "").PayloadHash() {
			t.Fatalf("request = %+v", s.req)
		}
	})

	results := []struct {
		name   string
		res    func(t *testing.T) app.ProcessResult
		status int
		want   string
	}{
		{"replay", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00"), Replay: true}
		}, 200, `"status":"PROCESSED","balance":{"amount":"75.00","currency":"BRL"},"idempotentReplay":true}`},
		{"rejection", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "BET", "500.00", "bet-1", ""), "100.00")}
		}, 422, `"status":"REJECTED","balance":{"amount":"100.00","currency":"BRL"},"failureCode":"INSUFFICIENT_FUNDS","failureCategory":"DEFINITIVE","idempotentReplay":false}`},
		{"pending reference", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "REFUND", "25.00", "refund-1", "bet-9"), "100.00")}
		}, 202, `"status":"PENDING_REFERENCE","idempotentReplay":false}`},
		{"recorded permanent failure", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: failed(t, command(t, "BET", "25.00", "bet-1", ""))}
		}, 500, `"status":"FAILED","failureCode":"INTERNAL_PERMANENT_FAILURE","failureCategory":"DEFINITIVE","idempotentReplay":false}`},
	}
	for _, tc := range results {
		t.Run(tc.name, func(t *testing.T) {
			rec := postWager(t, &stubWagers{res: tc.res(t)}, tokenProviderA, betBody, "provider-a:bet-1")
			if rec.Code != tc.status || rec.Header().Get("Content-Type") != "application/json" || !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), tc.want) {
				t.Fatalf("POST = %d %s, want %d …%s", rec.Code, rec.Body.String(), tc.status, tc.want)
			}
		})
	}

	rejected := []struct {
		name, token, body string
		keys              []string
		status            int
		code, field       string
	}{
		{"missing key", tokenProviderA, betBody, nil, 400, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"empty key", tokenProviderA, betBody, []string{""}, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"repeated key", tokenProviderA, betBody, []string{"k-1", "k-2"}, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"opening", tokenProviderA, strings.Replace(betBody, `"BET"`, `"OPENING"`, 1), []string{"k"}, 400, "OPENING_NOT_ALLOWED", "kind"},
		{"amount as a number", tokenProviderA, strings.Replace(betBody, `"25.00"`, `25.00`, 1), []string{"k"}, 400, "INVALID_AMOUNT", "money.amount"},
		{"kind as a number", tokenProviderA, strings.Replace(betBody, `"BET"`, `1`, 1), []string{"k"}, 400, "INVALID_KIND", "kind"},
		{"unknown field", tokenProviderA, strings.Replace(betBody, `"kind"`, `"x":1,"kind"`, 1), []string{"k"}, 400, "MALFORMED_REQUEST", ""},
		{"another provider", tokenProviderB, betBody, []string{"k"}, 403, "PROVIDER_MISMATCH", ""},
		{"validation before the provider", tokenProviderB, strings.Replace(betBody, `"25.00"`, `"25"`, 1), []string{"k"}, 400, "INVALID_AMOUNT", "money.amount"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubWagers{}
			wantProblem(t, postWager(t, s, tc.token, tc.body, tc.keys...), tc.status, tc.code, tc.field)
			if s.calls != 0 {
				t.Fatal("the use case ran")
			}
		})
	}

	t.Run("errors of the use case", func(t *testing.T) {
		conflict := apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused})
		wantProblem(t, postWager(t, &stubWagers{err: conflict}, tokenProviderA, betBody, "k"), 409, "IDEMPOTENCY_KEY_REUSED", "")
		unknown := apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", app.ErrNotFound)
		wantProblem(t, postWager(t, &stubWagers{err: unknown}, tokenProviderA, betBody, "k"), 400, "UNKNOWN_WALLET", "")
	})
}

// Covers: HTTP-04, HTTP-05, AUTH-05, D-04 (U17: transaction reads)
func TestTransactionReadHandlers(t *testing.T) {
	get := func(t *testing.T, r *stubReader, token, path string) *httptest.ResponseRecorder {
		t.Helper()
		return newEdge(t, httpapi.Services{Queries: r}, false).do(t, call{method: http.MethodGet, path: path, token: token})
	}
	decode := func(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var got map[string]any
		decodeJSON(t, rec, &got)
		return got
	}

	t.Run("an operation is seen by its provider and the internal service", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}
		got := decode(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID))
		if got["transactionId"] != txID || got["origin"] != "EXTERNAL" || got["kind"] != "BET" || got["status"] != "PROCESSED" ||
			got["providerId"] != "provider-a" || got["externalTransactionId"] != "bet-1" || got["receivedVia"] != "HTTP" ||
			got["completedAt"] != "2026-09-29T12:00:00.123456Z" || got["attempts"] != nil || got["expiresAt"] != nil {
			t.Fatalf("representation = %v", got)
		}
		if balance, _ := got["balance"].(map[string]any); balance["amount"] != "75.00" {
			t.Fatalf("balance = %v", got["balance"])
		}
		if rec := get(t, r, tokenInternal, "/wagering/transactions/"+txID); rec.Code != http.StatusOK {
			t.Fatalf("internal service: %d", rec.Code)
		}
		wantProblem(t, get(t, r, tokenProviderB, "/wagering/transactions/"+txID), http.StatusNotFound, "TRANSACTION_NOT_FOUND", "")
	})

	t.Run("a pending operation shows its schedule", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "REFUND", "25.00", "refund-1", "bet-9"), "100.00")}
		got := decode(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID))
		if got["status"] != "PENDING_REFERENCE" || got["attempts"] != json.Number("0") || got["nextAttemptAt"] == nil ||
			got["expiresAt"] == nil || got["balance"] != nil || got["completedAt"] != nil || got["referenceExternalTransactionId"] != "bet-9" {
			t.Fatalf("representation = %v", got)
		}
	})

	t.Run("the opening is seen only by the internal service", func(t *testing.T) {
		r := &stubReader{tx: opening(t)}
		wantProblem(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID), http.StatusNotFound, "TRANSACTION_NOT_FOUND", "")
		got := decode(t, get(t, r, tokenInternal, "/wagering/transactions/"+txID))
		for _, key := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "receivedVia"} {
			if _, ok := got[key]; ok {
				t.Fatalf("the opening has %s: %v", key, got)
			}
		}
		if got["kind"] != "OPENING" || got["origin"] != "INTERNAL" {
			t.Fatalf("representation = %v", got)
		}
	})

	t.Run("by external id", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}
		wantProblem(t, get(t, r, tokenProviderA, "/providers/provider-b/wagering/transactions/bet-1"), http.StatusForbidden, "PROVIDER_MISMATCH", "")
		if r.calls != 0 {
			t.Fatal("the query ran for another provider")
		}
		if rec := get(t, r, tokenProviderA, "/providers/provider-a/wagering/transactions/bet-1"); rec.Code != http.StatusOK || r.provider != "provider-a" || r.external != "bet-1" {
			t.Fatalf("own provider: %d (%q %q)", rec.Code, r.provider, r.external)
		}
		if rec := get(t, r, tokenInternal, "/providers/provider-b/wagering/transactions/bet-1"); rec.Code != http.StatusOK || r.provider != "provider-b" {
			t.Fatalf("internal service: %d (%q)", rec.Code, r.provider)
		}
	})
}
