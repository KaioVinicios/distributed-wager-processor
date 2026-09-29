//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-09, D-04, D-06, IDEM-02, IDEM-06, IDEM-07 (I12)
//
// Every code of lifecycle §5.3 that a request can provoke answers its status,
// application/problem+json and the code; INTERNAL_ERROR and
// TEMPORARILY_UNAVAILABLE are covered by U17 and R01 (spec decision 20).
func TestHTTPErrorContract(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	placed := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	result(t, a, placed, http.StatusOK)

	valid := func(mutate func(*testkit.Wager)) testkit.Wager {
		b := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
		mutate(&b)
		return b
	}
	post := func(body any, key string, invalid bool) testkit.Request {
		r := testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: body, Invalid: invalid}
		if key != "" {
			r.Header = http.Header{"Idempotency-Key": {key}}
		}
		return r
	}
	withoutRound := map[string]any{
		"providerId": "provider-a", "externalTransactionId": unique("bet"), "playerId": w.PlayerID, "walletId": w.ID,
		"gameId": "game-1", "kind": "BET", "money": testkit.BRL("10.00"),
	}
	player := testkit.NewID()
	internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": player, "initialBalance": testkit.BRL("0.00")}})

	cases := []struct {
		name   string
		client *testkit.Client
		req    testkit.Request
		status int
		code   string
		field  string
	}{
		{"malformed JSON", a, post(`{"providerId":`, "k-1", true), 400, "MALFORMED_REQUEST", ""},
		{"unknown field", a, post(map[string]any{"extra": 1}, "k-1", true), 400, "MALFORMED_REQUEST", ""},
		{"missing key", a, post(valid(func(*testkit.Wager) {}), "", true), 400, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"invalid key", a, post(valid(func(*testkit.Wager) {}), "has space", true), 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"missing field", a, post(withoutRound, "k-2", true), 400, "MISSING_FIELD", "roundId"},
		{"empty text", a, post(valid(func(b *testkit.Wager) { b.RoundID = "" }), "k-2", true), 400, "INVALID_FIELD", "roundId"},
		{"invalid uuid", a, post(valid(func(b *testkit.Wager) { b.PlayerID = "not-a-uuid" }), "k-3", true), 400, "INVALID_FIELD", "playerId"},
		{"invalid amount", a, post(valid(func(b *testkit.Wager) { b.Money.Amount = "25" }), "k-4", true), 400, "INVALID_AMOUNT", "money.amount"},
		{"invalid currency", a, post(valid(func(b *testkit.Wager) { b.Money.Currency = "XYZ" }), "k-5", true), 400, "INVALID_CURRENCY", "money.currency"},
		{"invalid kind", a, post(valid(func(b *testkit.Wager) { b.Kind = "JACKPOT" }), "k-6", true), 400, "INVALID_KIND", "kind"},
		{"opening", a, post(valid(func(b *testkit.Wager) { b.Kind = "OPENING" }), "k-7", true), 400, "OPENING_NOT_ALLOWED", "kind"},
		{"zero amount", a, post(valid(func(b *testkit.Wager) { b.Money.Amount = "0.00" }), "k-8", false), 400, "ZERO_AMOUNT_NOT_ALLOWED", "money.amount"},
		{"loss with amount", a, post(valid(func(b *testkit.Wager) { b.Kind = "LOSS" }), "k-9", false), 400, "LOSS_AMOUNT_MUST_BE_ZERO", "money.amount"},
		{"refund without reference", a, post(valid(func(b *testkit.Wager) { b.Kind = "REFUND" }), "k-10", false), 400, "REFERENCE_REQUIRED", "referenceExternalTransactionId"},
		{"bet with reference", a, post(valid(func(b *testkit.Wager) { b.ReferenceExternalTransactionID = "x" }), "k-11", false), 400, "REFERENCE_NOT_ALLOWED", "referenceExternalTransactionId"},
		{"self reference", a, post(valid(func(b *testkit.Wager) { b.Kind, b.ReferenceExternalTransactionID = "WIN", b.ExternalTransactionID }), "k-12", false), 400, "SELF_REFERENCE", "referenceExternalTransactionId"},
		{"unknown wallet", a, post(valid(func(b *testkit.Wager) { b.WalletID = testkit.NewID() }), unique("k"), false), 400, "UNKNOWN_WALLET", ""},
		{"no token", server.Client(t, ""), post(valid(func(*testkit.Wager) {}), "k-13", false), 401, "UNAUTHENTICATED", ""},
		{"role of the internal service", internal, post(valid(func(*testkit.Wager) {}), "k-14", false), 403, "FORBIDDEN", ""},
		{"another provider in the body", a, post(valid(func(b *testkit.Wager) { b.ProviderID = "provider-b" }), "k-15", false), 403, "PROVIDER_MISMATCH", ""},
		{"wallet not found", internal, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + testkit.NewID()}, 404, "WALLET_NOT_FOUND", ""},
		{"transaction not found", a, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + testkit.NewID()}, 404, "TRANSACTION_NOT_FOUND", ""},
		{"route not found", a, testkit.Request{Method: http.MethodGet, Path: "/nope"}, 404, "ROUTE_NOT_FOUND", ""},
		{"method not allowed", a, testkit.Request{Method: http.MethodDelete, Path: "/wallets"}, 405, "METHOD_NOT_ALLOWED", ""},
		{"key reused", a, post(valid(func(b *testkit.Wager) {
			b.ExternalTransactionID = placed.ExternalTransactionID
			b.Money.Amount = "11.00"
		}), "provider-a:"+placed.ExternalTransactionID, false), 409, "IDEMPOTENCY_KEY_REUSED", ""},
		{"external id with another key", a, post(valid(func(b *testkit.Wager) { b.ExternalTransactionID = placed.ExternalTransactionID }), unique("other-key"), false), 409, "EXTERNAL_TRANSACTION_ID_CONFLICT", ""},
		{"wallet exists", internal, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": player, "initialBalance": testkit.BRL("0.00")}}, 409, "WALLET_ALREADY_EXISTS", ""},
		{"media type", a, testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: "{}", Header: http.Header{"Content-Type": {"text/plain"}, "Idempotency-Key": {"k-16"}}, Invalid: true}, 415, "UNSUPPORTED_MEDIA_TYPE", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.client.Do(t, tc.req)
			p := resp.Problem(t)
			if resp.Status != tc.status || p.Status != tc.status || p.Code != tc.code || (tc.field != "" && p.Field != tc.field) {
				t.Fatalf("%d %+v, want %d %s (field %q)", resp.Status, p, tc.status, tc.code, tc.field)
			}
		})
	}
}
