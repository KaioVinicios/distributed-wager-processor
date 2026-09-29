//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-04, HTTP-05, HTTP-06, IDEM-05, IDEM-06, TST-A01 (the flow of the M3 definition of done)
func TestHappyPathFlow(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "80.00", unique("bet"), "")

	first := result(t, a, bet, http.StatusOK)
	wantResult(t, first, "PROCESSED", "", "20.00", false)
	replay := result(t, a, bet, http.StatusOK)
	wantResult(t, replay, "PROCESSED", "", "20.00", true)
	if replay.TransactionID != first.TransactionID {
		t.Fatalf("replay id %s, want %s", replay.TransactionID, first.TransactionID)
	}
	rejected := result(t, a, wager(w, "provider-a", "BET", "80.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	wantResult(t, rejected, "REJECTED", "INSUFFICIENT_FUNDS", "20.00", false)
	if rejected.FailureCategory != "DEFINITIVE" {
		t.Fatalf("failure category %q, want DEFINITIVE", rejected.FailureCategory)
	}

	var tx testkit.Transaction
	byID := a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + first.TransactionID})
	byID.JSON(t, &tx)
	if byID.Status != http.StatusOK || tx.TransactionID != first.TransactionID || tx.Origin != "EXTERNAL" || tx.Kind != "BET" ||
		tx.Status != "PROCESSED" || tx.ProviderID != "provider-a" || tx.ReceivedVia != "HTTP" || tx.Balance == nil ||
		tx.Balance.Amount != "20.00" || tx.CompletedAt == nil || tx.Attempts != nil {
		t.Fatalf("GET /wagering/transactions/{id} = %d %+v", byID.Status, tx)
	}
	var sameTx testkit.Transaction
	byExt := a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/provider-a/wagering/transactions/" + bet.ExternalTransactionID})
	byExt.JSON(t, &sameTx)
	if byExt.Status != http.StatusOK || sameTx.TransactionID != first.TransactionID {
		t.Fatalf("GET by external id = %d %+v", byExt.Status, sameTx)
	}

	var r testkit.Reconciliation
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"}).JSON(t, &r)
	if !r.Consistent || r.StoredBalance != testkit.BRL("20.00") || r.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v", r)
	}
}

// Covers: IDEM-08 (I10)
func TestReplayReturnsOriginalBalance(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "70.00", false)
	wantResult(t, result(t, a, wager(w, "provider-a", "WIN", "50.00", unique("win"), ""), http.StatusOK), "PROCESSED", "", "120.00", false)

	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "70.00", true)
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("120.00") || got.Version != 3 {
		t.Fatalf("wallet = %+v, want 120.00 v3", got)
	}
}

// Covers: OPS-04..10, OPS-12, TX-06, HTTP-04 (I11)
func TestReversalRules(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")

	t.Run("a REFUND occupies the compensation of its BET (C4)", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bet := unique("bet")
		wantResult(t, result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK), "PROCESSED", "", "70.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "REFUND", "30.00", unique("refund"), bet), http.StatusOK), "PROCESSED", "", "100.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "30.00", unique("rollback"), bet), http.StatusUnprocessableEntity),
			"REJECTED", "ALREADY_REVERSED", "100.00", false)
	})

	t.Run("after a ROLLBACK of the REFUND the BET is not refunded again (C5)", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bet, refund := unique("bet"), unique("refund")
		result(t, a, wager(w, "provider-a", "BET", "20.00", bet, ""), http.StatusOK)
		result(t, a, wager(w, "provider-a", "REFUND", "20.00", refund, bet), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "20.00", unique("rollback"), refund), http.StatusOK), "PROCESSED", "", "80.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "REFUND", "20.00", unique("refund"), bet), http.StatusUnprocessableEntity),
			"REJECTED", "ALREADY_REVERSED", "80.00", false)
	})

	t.Run("ROLLBACK of a WIN debits it back", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		win := unique("win")
		result(t, a, wager(w, "provider-a", "WIN", "50.00", win, ""), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "50.00", unique("rollback"), win), http.StatusOK), "PROCESSED", "", "100.00", false)
	})

	t.Run("a reversal without funds has its own code", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("10.00"))
		win := unique("win")
		result(t, a, wager(w, "provider-a", "WIN", "100.00", win, ""), http.StatusOK)
		result(t, a, wager(w, "provider-a", "BET", "110.00", unique("bet"), ""), http.StatusOK)
		reversal := result(t, a, wager(w, "provider-a", "ROLLBACK", "100.00", unique("rollback"), win), http.StatusUnprocessableEntity)
		bet := result(t, a, wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), http.StatusUnprocessableEntity)
		wantResult(t, reversal, "REJECTED", "REVERSAL_INSUFFICIENT_FUNDS", "0.00", false)
		wantResult(t, bet, "REJECTED", "INSUFFICIENT_FUNDS", "0.00", false)
	})

	t.Run("a reversal before its reference waits", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		pending := result(t, a, wager(w, "provider-a", "REFUND", "10.00", unique("refund"), unique("bet")), http.StatusAccepted)
		wantResult(t, pending, "PENDING_REFERENCE", "", "", false)
		var tx testkit.Transaction
		a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + pending.TransactionID}).JSON(t, &tx)
		if tx.Status != "PENDING_REFERENCE" || tx.Attempts == nil || tx.NextAttemptAt == nil || tx.ExpiresAt == nil ||
			tx.Balance != nil || tx.CompletedAt != nil {
			t.Fatalf("pending operation = %+v", tx)
		}
	})
}

// Covers: HTTP-03, LED-06 (I09)
func TestLedgerPagination(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	for range 119 {
		result(t, a, wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), http.StatusOK)
	}

	var versions []int64
	var sizes []int
	cursor := ""
	for {
		path := "/wallets/" + w.ID + "/ledger?limit=50"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page testkit.LedgerPage
		internal.Do(t, testkit.Request{Method: http.MethodGet, Path: path}).JSON(t, &page)
		sizes = append(sizes, len(page.Items))
		for _, e := range page.Items {
			versions = append(versions, e.WalletVersion)
		}
		if page.NextCursor == "" {
			break
		}
		if strings.Contains(page.NextCursor, fmt.Sprint(versions[len(versions)-1])) {
			t.Fatalf("cursor %q shows the version: it must be opaque", page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(sizes) != "[50 50 20]" {
		t.Fatalf("page sizes %v, want [50 50 20]", sizes)
	}
	for i, v := range versions {
		if v != int64(i+1) {
			t.Fatalf("versions %v: repetition or gap at %d", versions, i)
		}
	}
}
