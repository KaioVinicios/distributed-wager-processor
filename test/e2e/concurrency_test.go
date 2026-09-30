//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C02, CONC-05, CONC-04, E4 (C02)
// Sensitivity: the FOR UPDATE removed from the wallet lock → the second BET ends FAILED (stale version) instead of REJECTED.
//
// 100.00 and two simultaneous BETs of 80.00, one on instance 1 and one on
// instance 2, repeated 20 times with new wallets: one PROCESSED, one
// INSUFFICIENT_FUNDS, 20.00 left, one debit; resending changes nothing.
func TestTwoBetsCompete(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	first, second := cluster.Instance(1).Client(t, "provider-a"), cluster.Instance(2).Client(t, "provider-a")
	resend := cluster.Client(t, "provider-a")
	for range 20 {
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		bets := [2]testkit.Wager{
			wager(w, "provider-a", "BET", "80.00", unique("bet-a"), ""),
			wager(w, "provider-a", "BET", "80.00", unique("bet-b"), ""),
		}
		var answers [2]*testkit.Response
		together(
			func() { answers[0] = submit(t, first, bets[0]) },
			func() { answers[1] = submit(t, second, bets[1]) },
		)
		outcome := map[string]testkit.TransactionResult{}
		var firsts [2]testkit.TransactionResult
		for i, resp := range answers {
			resp.JSON(t, &firsts[i])
			outcome[firsts[i].Status] = firsts[i]
		}
		processed, rejected := outcome["PROCESSED"], outcome["REJECTED"]
		if len(outcome) != 2 || processed.Balance == nil || processed.Balance.Amount != "20.00" ||
			rejected.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Balance == nil || rejected.Balance.Amount != "20.00" {
			t.Fatalf("outcomes = %+v", outcome)
		}
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("20.00") || got.Version != 2 {
			t.Fatalf("wallet = %+v, want 20.00 after one debit", got)
		}
		for i, body := range bets {
			var again testkit.TransactionResult
			submit(t, resend, body).JSON(t, &again)
			if again.TransactionID != firsts[i].TransactionID || again.Status != firsts[i].Status || !again.IdempotentReplay {
				t.Fatalf("resend of %s = %+v, first %+v", body.ExternalTransactionID, again, firsts[i])
			}
		}
	}
}

// Covers: TST-C03, CONC-06, CONC-04 (C03a)
// Sensitivity: the FOR UPDATE removed from the wallet lock → BETs of the same wallet end FAILED and answer 500.
//
// 20 wallets, 10 simultaneous BETs each, spread over the 3 processes: all
// processed, every wallet with its 10 debits.
func TestWalletsInParallel(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	a := cluster.Client(t, "provider-a")
	wallets := make([]testkit.Wallet, 20)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("100.00"))
	}
	statuses := make([][10]int, len(wallets))
	var fns []func()
	for i, w := range wallets {
		for j := range 10 {
			body := wager(w, "provider-a", "BET", "1.00", unique("bet"), "")
			fns = append(fns, func() { statuses[i][j] = submit(t, a, body).Status })
		}
	}
	together(fns...)
	for i, w := range wallets {
		for j, status := range statuses[i] {
			if status != http.StatusOK {
				t.Fatalf("wallet %d, BET %d answered %d, want 200", i, j, status)
			}
		}
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("90.00") || got.Version != 11 {
			t.Fatalf("wallet %d = %+v, want 90.00 after 10 debits", i, got)
		}
	}
}

// Covers: CONC-01, CONC-06, E7 (C03b)
// Sensitivity: LOCK TABLE wallets IN EXCLUSIVE MODE in Wallets().Lock → the BET on Y waits for X's lock and answers 503.
// (SHARE ROW EXCLUSIVE does not conflict with the ROW SHARE of a FOR UPDATE, so it is no global lock here.)
//
// With wallet X locked by an open transaction, a BET on wallet Y concludes at
// once, and the BET on X waits for the lock and concludes after its release.
func TestNoGlobalLock(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	a := cluster.Client(t, "provider-a")
	x, y := cluster.OpenWallet(t, testkit.BRL("100.00")), cluster.OpenWallet(t, testkit.BRL("100.00"))
	tx, err := cluster.Owner().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, x.ID); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	wantResult(t, result(t, a, wager(y, "provider-a", "BET", "10.00", unique("bet"), ""), http.StatusOK), "PROCESSED", "", "90.00", false)
	if took := time.Since(started); took > time.Second {
		t.Fatalf("a BET on another wallet took %v while X was locked, want under 1s: a global lock", took)
	}

	blocked := make(chan *testkit.Response, 1)
	onX := wager(x, "provider-a", "BET", "10.00", unique("bet"), "")
	go func() { blocked <- submit(t, a, onX) }()
	select {
	case resp := <-blocked:
		t.Fatalf("the BET on the locked wallet answered %d %s while the lock was held", resp.Status, resp.Body)
	case <-time.After(500 * time.Millisecond): // DB_LOCK_TIMEOUT is 2 s: still waiting, not timed out
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var r testkit.TransactionResult
	resp := <-blocked
	resp.JSON(t, &r)
	if resp.Status != http.StatusOK {
		t.Fatalf("the BET on X after the release = %d %+v, want 200", resp.Status, r)
	}
	wantResult(t, r, "PROCESSED", "", "90.00", false)
}
