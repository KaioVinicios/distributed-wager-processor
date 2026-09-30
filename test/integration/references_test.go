//go:build integration

package integration_test

import (
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: OPS-12, OUT-07, OUT-08 (C2 over HTTP)
// Sensitivity: references.Module out of bootstrap.Options → waitStatus times out (the REFUND stays PENDING_REFERENCE).
//
// The REFUND that waited is resolved by the worker, and its events reach the
// audit queue citing the BET that unblocked it.
func TestPendingReferenceResolved(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet, refund := unique("bet"), unique("refund")
	pending := result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, bet), http.StatusAccepted)
	betResult := result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
	waitStatus(t, a, "provider-a", refund, "PROCESSED")

	causes := map[string][]string{}
	for _, env := range eventsOf(t, w.ID) {
		data, _ := env["data"].(map[string]any)
		if data["transactionId"] != pending.TransactionID {
			continue
		}
		cause, _ := env["causationId"].(string)
		typ, _ := env["eventType"].(string)
		causes[typ] = append(causes[typ], cause)
	}
	want := map[string][]string{
		"WagerTransactionPendingReference": {""}, // emitted when it arrived
		"WagerTransactionProcessed":        {betResult.TransactionID},
		"WalletBalanceChanged":             {betResult.TransactionID},
	}
	if !reflect.DeepEqual(causes, want) {
		t.Fatalf("events of the REFUND (type → causationId) = %v, want %v", causes, want)
	}
}

// Covers: OPS-13, OUT-07 (C3 over HTTP)
// Sensitivity: RescheduleReference never returning ErrReferenceExpired → waitStatus times out; a rejection with another code → the failureCode check fails.
//
// A REFUND whose BET never arrives is rejected when its attempts run out,
// with the rejection event and no ledger entry.
func TestPendingReferenceExpires(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	refund := unique("refund")
	pending := result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted)

	tx := waitStatus(t, a, "provider-a", refund, "REJECTED")
	if tx.FailureCode != "REFERENCE_NOT_FOUND" || tx.FailureCategory != "DEFINITIVE" || tx.Balance == nil || tx.Balance.Amount != "100.00" {
		t.Fatalf("operation = %+v, want REJECTED REFERENCE_NOT_FOUND (DEFINITIVE) with balance 100.00", tx)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("100.00") || got.Version != 1 {
		t.Fatalf("wallet = %+v, want 100.00 v1: an expiration moves nothing", got)
	}
	rejected := 0
	for _, env := range eventsOf(t, w.ID) {
		data, _ := env["data"].(map[string]any)
		if env["eventType"] != "WagerTransactionRejected" || data["transactionId"] != pending.TransactionID {
			continue
		}
		rejected++
		if data["failureCode"] != "REFERENCE_NOT_FOUND" || env["causationId"] != nil {
			t.Errorf("rejection event = %v, want REFERENCE_NOT_FOUND with no causationId", env)
		}
	}
	if rejected != 1 {
		t.Fatalf("%d WagerTransactionRejected events for the REFUND, want 1", rejected)
	}
}

// Covers: D-09 (lock order wallet → transaction), CONC-01 (spec M6, risks)
// Smoke test of the mix, not the proof of the lock order: inverting it in lockPending was NOT detected here
// (60 pairs, 3 runs); the deterministic proof is TestResolveReferencesLockOrder (internal/app).
//
// The BETs of ten waiting REFUNDs arrive over HTTP at the same moment as the
// REFUNDs themselves and as the worker: everything runs on one wallet, with no
// lock timeout or deadlock, and every REFUND ends PROCESSED.
func TestWorkerVersusHTTP(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	const pairs = 10
	type sent struct {
		refund, bet int
		refundExt   string
	}
	results := make([]sent, pairs)
	var wg sync.WaitGroup
	for i := range results {
		bet, refund := unique("bet"), unique("refund")
		results[i].refundExt = refund
		wg.Go(func() {
			results[i].refund = submit(t, a, wager(w, "provider-a", "REFUND", "10.00", refund, bet)).Status
		})
		wg.Go(func() { results[i].bet = submit(t, a, wager(w, "provider-a", "BET", "10.00", bet, "")).Status })
	}
	wg.Wait()

	for i, r := range results {
		// A REFUND that lands after its BET is processed at once (200); before it, it waits (202).
		if r.bet != http.StatusOK || (r.refund != http.StatusOK && r.refund != http.StatusAccepted) {
			t.Fatalf("pair %d: BET %d, REFUND %d; want 200 and 200 or 202 (a 503 is a lock timeout)", i, r.bet, r.refund)
		}
		waitStatus(t, a, "provider-a", r.refundExt, "PROCESSED")
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("1000.00") {
		t.Fatalf("wallet = %+v, want 1000.00", got)
	}
	// A deadlock or a lock timeout may pick the worker as the victim: nothing shows in the
	// responses, since the item is retried, but the worker logs the failure with the wallet.
	for line := range strings.Lines(server.Logs()) {
		if strings.Contains(line, "reference resolution failed") && strings.Contains(line, w.ID) {
			t.Errorf("the worker failed on this wallet: %s", strings.TrimSpace(line))
		}
	}
}
