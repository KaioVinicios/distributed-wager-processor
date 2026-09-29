//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// unique makes an external id unique across tests and runs.
func unique(prefix string) string { return prefix + "-" + testkit.NewID() }

// wager is an operation of provider on w; ref "" = no reference.
func wager(w testkit.Wallet, provider, kind, amount, ext, ref string) testkit.Wager {
	return testkit.Wager{
		ProviderID: provider, ExternalTransactionID: ext, PlayerID: w.PlayerID, WalletID: w.ID,
		RoundID: "round-1", GameID: "game-1", Kind: kind, Money: testkit.BRL(amount),
		ReferenceExternalTransactionID: ref,
	}
}

// submit posts the operation with the key {providerId}:{externalTransactionId}.
func submit(t *testing.T, c *testkit.Client, body testkit.Wager) *testkit.Response {
	t.Helper()
	return c.Do(t, testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
		Header: http.Header{"Idempotency-Key": {body.ProviderID + ":" + body.ExternalTransactionID}},
	})
}

// result submits and decodes the TransactionResult, failing unless the status
// is want.
func result(t *testing.T, c *testkit.Client, body testkit.Wager, want int) testkit.TransactionResult {
	t.Helper()
	resp := submit(t, c, body)
	var r testkit.TransactionResult
	resp.JSON(t, &r)
	if resp.Status != want {
		t.Fatalf("POST %s %s = %d %+v, want %d", body.Kind, body.ExternalTransactionID, resp.Status, r, want)
	}
	return r
}

// wantResult fails unless r has the status, failure code, balance ("" = none)
// and replay flag.
func wantResult(t *testing.T, r testkit.TransactionResult, status, code, balance string, replay bool) {
	t.Helper()
	gotBalance := ""
	if r.Balance != nil {
		gotBalance = r.Balance.Amount
	}
	if r.Status != status || r.FailureCode != code || gotBalance != balance || r.IdempotentReplay != replay {
		t.Fatalf("result = %+v (balance %q), want %s %s %q replay %v", r, gotBalance, status, code, balance, replay)
	}
}

// balanceOf reads the wallet as the internal service.
func balanceOf(t *testing.T, walletID string) testkit.Wallet {
	t.Helper()
	var w testkit.Wallet
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + walletID}).JSON(t, &w)
	return w
}
