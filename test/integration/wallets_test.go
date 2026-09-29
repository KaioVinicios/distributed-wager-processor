//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-01, HTTP-02, WAL-03 (I20 through the API)
func TestOpenWalletAPI(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")
	player := testkit.NewID()

	created := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": player, "initialBalance": testkit.BRL("1000.00"),
	}})
	var w testkit.Wallet
	created.JSON(t, &w)
	if created.Status != http.StatusCreated || created.Header.Get("Location") != "/wallets/"+w.ID ||
		w.PlayerID != player || w.Balance != testkit.BRL("1000.00") || w.Version != 1 {
		t.Fatalf("POST /wallets = %d %+v (Location %q)", created.Status, w, created.Header.Get("Location"))
	}
	t.Cleanup(func() { server.AssertWalletConsistent(t, w.ID) })

	got := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	var read testkit.Wallet
	got.JSON(t, &read)
	if got.Status != http.StatusOK || read != w {
		t.Fatalf("GET /wallets/{id} = %d %+v, want %+v", got.Status, read, w)
	}

	again := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": player, "initialBalance": testkit.BRL("5.00"),
	}})
	if p := again.Problem(t); again.Status != http.StatusConflict || p.Code != "WALLET_ALREADY_EXISTS" || p.Category != "DEFINITIVE" {
		t.Fatalf("second POST /wallets = %d %+v", again.Status, p)
	}

	zero := server.OpenWallet(t, testkit.BRL("0.00"))
	ledger := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + zero.ID + "/ledger"})
	var page testkit.LedgerPage
	ledger.JSON(t, &page)
	if ledger.Status != http.StatusOK || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("ledger of a zero opening = %d %+v", ledger.Status, page)
	}
}

// Covers: HTTP-03, HTTP-09
func TestLedgerAPI(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("100.00"))

	resp := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger?limit=10"})
	var page testkit.LedgerPage
	resp.JSON(t, &page)
	if resp.Status != http.StatusOK || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("GET ledger = %d %+v", resp.Status, page)
	}
	if e := page.Items[0]; e.Direction != "CREDIT" || e.Amount != testkit.BRL("100.00") || e.BalanceBefore != testkit.BRL("0.00") ||
		e.BalanceAfter != testkit.BRL("100.00") || e.WalletVersion != 1 {
		t.Fatalf("opening entry = %+v", e)
	}

	for query, field := range map[string]string{"?limit=0": "limit", "?limit=201": "limit", "?limit=ten": "limit", "?cursor=bogus": "cursor"} {
		bad := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger" + query, Invalid: true})
		if p := bad.Problem(t); bad.Status != http.StatusBadRequest || p.Code != "INVALID_FIELD" || p.Field != field {
			t.Fatalf("GET ledger%s = %d %+v", query, bad.Status, p)
		}
	}
	for _, id := range []string{testkit.NewID(), "not-a-uuid"} {
		missing := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + id + "/ledger"})
		if p := missing.Problem(t); missing.Status != http.StatusNotFound || p.Code != "WALLET_NOT_FOUND" {
			t.Fatalf("GET ledger of %s = %d %+v", id, missing.Status, p)
		}
	}
}

// shiftBalance changes the stored balance behind the ledger's back. The wallet
// triggers are disabled only inside this transaction: ALTER TABLE holds an
// ACCESS EXCLUSIVE lock until the commit, so no parallel test ever sees them
// disabled. The context is detached: it also runs in t.Cleanup.
func shiftBalance(t *testing.T, walletID string, deltaMinor int64) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	tx, err := server.Owner().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE wallets DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = balance_minor + $1 WHERE id = $2`, deltaMinor, walletID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE wallets ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Covers: HTTP-07, LED-06, OBS-03 (I08)
func TestReconciliation(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")

	t.Run("consistent", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("975.00"))
		resp := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})
		var r testkit.Reconciliation
		resp.JSON(t, &r)
		want := testkit.Reconciliation{
			WalletID: w.ID, StoredBalance: testkit.BRL("975.00"), CalculatedBalance: testkit.BRL("975.00"),
			Difference: testkit.BRL("0.00"), Consistent: true, CheckedEntries: 1,
		}
		if resp.Status != http.StatusOK || r != want {
			t.Fatalf("reconciliation = %d %+v, want %+v", resp.Status, r, want)
		}
	})

	t.Run("divergence in the answer, the log and the metric, without changing the balance", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		shiftBalance(t, w.ID, -500)
		t.Cleanup(func() { shiftBalance(t, w.ID, 500) }) // runs before the consistency check
		before := server.Metric(t, "reconciliation_divergences_total")

		resp := internal.Do(t, testkit.Request{
			Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation",
			Header: http.Header{"X-Correlation-Id": {"corr-i08-" + w.ID[:8]}},
		})
		var r testkit.Reconciliation
		resp.JSON(t, &r)
		if resp.Status != http.StatusOK || r.Consistent || r.StoredBalance != testkit.BRL("95.00") ||
			r.CalculatedBalance != testkit.BRL("100.00") || r.Difference != testkit.BRL("-5.00") {
			t.Fatalf("reconciliation = %d %+v", resp.Status, r)
		}
		if after := server.Metric(t, "reconciliation_divergences_total"); after == before || after == "" {
			t.Fatalf("reconciliation_divergences_total %q → %q, want it counted", before, after)
		}
		logs := server.Logs()
		if !strings.Contains(logs, `"msg":"reconciliation divergence"`) || !strings.Contains(logs, "corr-i08-"+w.ID[:8]) {
			t.Fatalf("no WARN with the wallet and the correlation id in the logs")
		}
		var stored testkit.Wallet
		internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID}).JSON(t, &stored)
		if stored.Balance != testkit.BRL("95.00") || stored.Version != 1 {
			t.Fatalf("wallet after the reconciliation = %+v, want untouched", stored)
		}
	})

	t.Run("unknown wallet", func(t *testing.T) {
		t.Parallel()
		resp := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + testkit.NewID() + "/reconciliation"})
		if p := resp.Problem(t); resp.Status != http.StatusNotFound || p.Code != "WALLET_NOT_FOUND" {
			t.Fatalf("reconciliation of an unknown wallet = %d %+v", resp.Status, p)
		}
	})
}
