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
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

const (
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
)

var stamp = time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func openedWallet(t *testing.T, balance string) wallet.Wallet {
	t.Helper()
	w, err := wallet.Open(walletID, playerID, brl(t, balance), stamp)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// stubWallets records what the handlers pass to the use cases.
type stubWallets struct {
	in          app.OpenWalletInput
	correlation string
	opened      wallet.Wallet
	err         error

	cursor string
	limit  int
	page   app.LedgerPage
	calls  int
}

func (s *stubWallets) Execute(_ context.Context, in app.OpenWalletInput, correlationID string) (wallet.Wallet, error) {
	s.in, s.correlation, s.calls = in, correlationID, s.calls+1
	return s.opened, s.err
}

func (s *stubWallets) GetWallet(_ context.Context, id string) (wallet.Wallet, error) {
	s.calls++
	if id != walletID {
		return wallet.Wallet{}, apperrors.New(apperrors.KindNotFound, "WALLET_NOT_FOUND", app.ErrNotFound)
	}
	return s.opened, nil
}

func (s *stubWallets) ListLedger(_ context.Context, id, cursor string, limit int) (app.LedgerPage, error) {
	s.cursor, s.limit, s.calls = cursor, limit, s.calls+1
	return s.page, s.err
}

func (s *stubWallets) GetTransaction(context.Context, string) (*wagering.WagerTransaction, error) {
	return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
}

func (s *stubWallets) GetTransactionByExternalID(context.Context, string, string) (*wagering.WagerTransaction, error) {
	return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
}

type stubReconcile struct {
	walletID, correlation string
	out                   app.Reconciliation
}

func (s *stubReconcile) Execute(_ context.Context, walletID, correlationID string) (app.Reconciliation, error) {
	s.walletID, s.correlation = walletID, correlationID
	return s.out, nil
}

func walletsEdge(t *testing.T, s *stubWallets, r *stubReconcile) edge {
	t.Helper()
	if r == nil {
		r = &stubReconcile{}
	}
	return newEdge(t, httpapi.Services{Wallets: s, Queries: s, Reconcile: r}, false)
}

func postWallet(t *testing.T, e edge, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, call{
		method: http.MethodPost, path: "/wallets", token: tokenInternal, contentType: "application/json", body: body,
		header: http.Header{"X-Correlation-Id": {"corr-w"}},
	})
}

// Covers: HTTP-01, HTTP-09 (U17: POST /wallets)
func TestOpenWalletHandler(t *testing.T) {
	t.Run("201 with the wallet and its location", func(t *testing.T) {
		s := &stubWallets{opened: openedWallet(t, "1000.00")}
		rec := postWallet(t, walletsEdge(t, s, nil), `{"playerId":"`+playerID+`","initialBalance":{"amount":"1000.00","currency":"BRL"}}`)
		var got map[string]any
		decodeJSON(t, rec, &got)
		if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/wallets/"+walletID {
			t.Fatalf("POST /wallets = %d, Location %q", rec.Code, rec.Header().Get("Location"))
		}
		balance, _ := got["balance"].(map[string]any)
		if got["id"] != walletID || got["playerId"] != playerID || balance["amount"] != "1000.00" || balance["currency"] != "BRL" ||
			got["version"] != json.Number("1") || got["createdAt"] != "2026-09-29T12:00:00.123456Z" || got["updatedAt"] != got["createdAt"] {
			t.Fatalf("body = %v", got)
		}
		if *s.in.PlayerID != playerID || *s.in.InitialBalance.Amount != "1000.00" || *s.in.InitialBalance.Currency != "BRL" || s.correlation != "corr-w" {
			t.Fatalf("use case got %+v, correlation %q", s.in, s.correlation)
		}
	})

	t.Run("absent and null fields reach the use case as absent", func(t *testing.T) {
		s := &stubWallets{err: apperrors.New(apperrors.KindInput, "MISSING_FIELD", &wagering.ValidationError{Code: wagering.InputMissingField, Field: "playerId"})}
		rec := postWallet(t, walletsEdge(t, s, nil), `{"playerId":null,"initialBalance":{"amount":"1.00","currency":null}}`)
		wantProblem(t, rec, http.StatusBadRequest, "MISSING_FIELD", "playerId")
		if s.in.PlayerID != nil || s.in.InitialBalance == nil || s.in.InitialBalance.Currency != nil {
			t.Fatalf("use case got %+v", s.in)
		}
	})

	cases := []struct {
		name, body, code, field string
	}{
		{"empty body", ``, "MALFORMED_REQUEST", ""},
		{"not an object", `[1]`, "MALFORMED_REQUEST", ""},
		{"broken JSON", `{"playerId":`, "MALFORMED_REQUEST", ""},
		{"unknown field", `{"playerId":"p","extra":1}`, "MALFORMED_REQUEST", ""},
		{"unknown money field", `{"initialBalance":{"amount":"1.00","cents":100}}`, "MALFORMED_REQUEST", ""},
		{"data after the object", `{} {}`, "MALFORMED_REQUEST", ""},
		{"amount as a number", `{"playerId":"p","initialBalance":{"amount":1000.00,"currency":"BRL"}}`, "INVALID_AMOUNT", "initialBalance.amount"},
		{"currency as a number", `{"initialBalance":{"amount":"1.00","currency":986}}`, "INVALID_CURRENCY", "initialBalance.currency"},
		{"money as a string", `{"initialBalance":"1000.00 BRL"}`, "INVALID_FIELD", "initialBalance"},
		{"player as a number", `{"playerId":1}`, "INVALID_FIELD", "playerId"},
		{"body above 64 KB", `{"playerId":"` + strings.Repeat("x", 64<<10) + `"}`, "MALFORMED_REQUEST", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubWallets{}
			wantProblem(t, postWallet(t, walletsEdge(t, s, nil), tc.body), http.StatusBadRequest, tc.code, tc.field)
			if s.calls != 0 {
				t.Fatal("the use case ran for a body that does not decode")
			}
		})
	}

	t.Run("conflict", func(t *testing.T) {
		s := &stubWallets{err: apperrors.New(apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists)}
		wantProblem(t, postWallet(t, walletsEdge(t, s, nil), `{}`), http.StatusConflict, "WALLET_ALREADY_EXISTS", "")
	})
}

// Covers: HTTP-02, HTTP-03 (U17: reads)
func TestWalletReadHandlers(t *testing.T) {
	get := func(t *testing.T, e edge, path string) *httptest.ResponseRecorder {
		t.Helper()
		return e.do(t, call{method: http.MethodGet, path: path, token: tokenInternal})
	}

	t.Run("wallet", func(t *testing.T) {
		e := walletsEdge(t, &stubWallets{opened: openedWallet(t, "5.00")}, nil)
		var got map[string]any
		rec := get(t, e, "/wallets/"+walletID)
		decodeJSON(t, rec, &got)
		if rec.Code != http.StatusOK || got["id"] != walletID {
			t.Fatalf("GET /wallets/{id} = %d %v", rec.Code, got)
		}
		wantProblem(t, get(t, e, "/wallets/"+playerID), http.StatusNotFound, "WALLET_NOT_FOUND", "")
	})

	t.Run("ledger page", func(t *testing.T) {
		w := openedWallet(t, "0.00")
		entry, err := w.Credit("0192f298-3460-7c02-8a10-66778899aabb", "0192f298-345e-7e38-af88-e43f851a819d", brl(t, "25.00"), stamp)
		if err != nil {
			t.Fatal(err)
		}
		s := &stubWallets{page: app.LedgerPage{Entries: []wallet.LedgerEntry{entry}, NextCursor: "eyJ2IjoyfQ"}}
		e := walletsEdge(t, s, nil)
		rec := get(t, e, "/wallets/"+walletID+"/ledger?cursor=abc&limit=10")
		var page struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"nextCursor"`
		}
		decodeJSON(t, rec, &page)
		if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Next != "eyJ2IjoyfQ" || s.cursor != "abc" || s.limit != 10 {
			t.Fatalf("GET ledger = %d %+v (cursor %q, limit %d)", rec.Code, page, s.cursor, s.limit)
		}
		item := page.Items[0]
		if item["direction"] != "CREDIT" || item["walletVersion"] != json.Number("2") || item["transactionId"] != "0192f298-345e-7e38-af88-e43f851a819d" {
			t.Fatalf("item = %v", item)
		}
	})

	t.Run("ledger defaults and an empty page", func(t *testing.T) {
		s := &stubWallets{}
		rec := get(t, walletsEdge(t, s, nil), "/wallets/"+walletID+"/ledger")
		if rec.Code != http.StatusOK || s.limit != app.DefaultLedgerLimit || s.cursor != "" ||
			strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
			t.Fatalf("GET ledger = %d %s (limit %d)", rec.Code, rec.Body.String(), s.limit)
		}
	})

	t.Run("ledger limit that is not an integer", func(t *testing.T) {
		s := &stubWallets{}
		wantProblem(t, get(t, walletsEdge(t, s, nil), "/wallets/"+walletID+"/ledger?limit=ten"), http.StatusBadRequest, "INVALID_FIELD", "limit")
		if s.calls != 0 {
			t.Fatal("the query ran with a malformed limit")
		}
	})

	t.Run("reconciliation", func(t *testing.T) {
		difference, err := brl(t, "95.00").Sub(brl(t, "100.00"))
		if err != nil {
			t.Fatal(err)
		}
		r := &stubReconcile{out: app.Reconciliation{
			WalletID: walletID, Stored: brl(t, "95.00"), Calculated: brl(t, "100.00"),
			Difference: difference, Consistent: false, CheckedEntries: 3,
		}}
		e := walletsEdge(t, &stubWallets{}, r)
		rec := e.do(t, call{
			method: http.MethodPost, path: "/wallets/" + walletID + "/reconciliation", token: tokenInternal,
			header: http.Header{"X-Correlation-Id": {"corr-r"}},
		})
		want := `{"walletId":"` + walletID + `","storedBalance":{"amount":"95.00","currency":"BRL"},` +
			`"calculatedBalance":{"amount":"100.00","currency":"BRL"},"difference":{"amount":"-5.00","currency":"BRL"},` +
			`"consistent":false,"checkedEntries":3}`
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || r.walletID != walletID || r.correlation != "corr-r" {
			t.Fatalf("POST reconciliation = %d %s (use case got %q %q)", rec.Code, rec.Body.String(), r.walletID, r.correlation)
		}
	})
}
