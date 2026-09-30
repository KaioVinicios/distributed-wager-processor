//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C01, IDEM-05, CONC-04, E5 (C01a)
// Sensitivity: the idempotency lookup returning nothing → the duplicates answer 503/409.
//
// The same BET, 50 times at once, spread over the 3 processes: one debit.
func TestSameBet50xHTTP(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	a := cluster.Client(t, "provider-a")
	w := cluster.OpenWallet(t, testkit.BRL("1000.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	answers := make([]*testkit.Response, 50)
	fns := make([]func(), len(answers))
	for i := range fns {
		fns[i] = func() { answers[i] = submit(t, a, bet) }
	}
	together(fns...)

	ids, replays := map[string]int{}, 0
	for _, resp := range answers {
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

// Covers: TST-C01, TST-C11, SQS-03, CONC-04, E5 (C01b)
// Sensitivity: no inbox row on the replay path of ProcessWager → the inbox never has the 50 rows.
//
// 50 messages of the same operation, each with its own messageId and its own
// MessageDeduplicationId, so the deduplication exercised is the application's
// and not the FIFO's (TST-C11): one debit, and one inbox row per message.
func TestSameBet50xSQS(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	w := cluster.OpenWallet(t, testkit.BRL("1000.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = unique("msg")
		cluster.SendWager(t, sqsWager(t, ids[i], bet), testkit.SendOpts{GroupID: w.ID}) // DedupID "": a new one each
	}
	outcomes := inboxOutcomes(t, ids...)
	if len(outcomes) != 2 || outcomes["PROCESSED"] != 1 || outcomes["IDEMPOTENT_REPLAY"] != 49 {
		t.Fatalf("inbox outcomes = %v, want 1 PROCESSED and 49 IDEMPOTENT_REPLAY", outcomes)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("975.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}
