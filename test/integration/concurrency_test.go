//go:build integration

package integration_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// race sends every body at once, behind a start barrier, and returns the
// results in order.
func race(t *testing.T, c *testkit.Client, bodies []testkit.Wager) []*testkit.Response {
	t.Helper()
	out := make([]*testkit.Response, len(bodies))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Go(func() {
			<-start
			out[i] = submit(t, c, bodies[i])
		})
	}
	close(start)
	wg.Wait()
	return out
}

// Covers: TST-C01, IDEM-05, E5 (C01a, in process; with 3 processes in M8)
//
// Sensitivity: the idempotency lookup of ProcessWager returning nothing →
// the duplicates exhaust the race retries and answer 503.
func TestSameBet50xHTTP(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	bodies := make([]testkit.Wager, 50)
	for i := range bodies {
		bodies[i] = bet
	}

	ids, replays := map[string]int{}, 0
	for _, resp := range race(t, a, bodies) {
		var r testkit.TransactionResult
		resp.JSON(t, &r)
		if resp.Status != http.StatusOK || r.Status != "PROCESSED" || r.Balance == nil || r.Balance.Amount != "975.00" {
			t.Fatalf("answer %d %+v", resp.Status, r)
		}
		ids[r.TransactionID]++
		if r.IdempotentReplay {
			replays++
		}
	}
	if len(ids) != 1 || replays != 49 {
		t.Fatalf("%d transaction ids, %d replays; want 1 and 49", len(ids), replays)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("975.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}

// Covers: TST-C02, CONC-05, E4 (C02, in process; with 3 processes in M8)
//
// Sensitivity: the FOR UPDATE removed from the wallet lock → both BETs read
// 100.00 and the second one fails (stale version) instead of being rejected.
func TestTwoBetsCompete(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	for range 20 {
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bets := []testkit.Wager{
			wager(w, "provider-a", "BET", "80.00", unique("bet-a"), ""),
			wager(w, "provider-a", "BET", "80.00", unique("bet-b"), ""),
		}
		answers := race(t, a, bets)
		outcome := map[string]testkit.TransactionResult{}
		for _, resp := range answers {
			var r testkit.TransactionResult
			resp.JSON(t, &r)
			outcome[r.Status] = r
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
			submit(t, a, body).JSON(t, &again)
			var first testkit.TransactionResult
			answers[i].JSON(t, &first)
			if again.TransactionID != first.TransactionID || again.Status != first.Status || !again.IdempotentReplay {
				t.Fatalf("resend of %s = %+v, first %+v", body.ExternalTransactionID, again, first)
			}
		}
	}
}
