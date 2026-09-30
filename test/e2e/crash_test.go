//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C05, SQS-05, E5, E7 (C05a)
// Sensitivity: without the point → instance 0 did not exit; the inbox not recognizing the redelivery
// (ConsumeWager ignoring the row it found) → no inbox duplicate on instance 1.
//
// The consumer of instance 0 commits the BET and dies before it deletes the
// message. After the visibility timeout the message returns to the consumer
// of instance 1, which finds it in the inbox and only deletes it: one debit.
func TestCrashAfterCommitBeforeDelete(t *testing.T) {
	defer cluster.Restore(t)
	const point = "consumer.after_commit_before_delete"
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	crashOn(t, point, config.Roles{Consumer: true})
	msgID := unique("msg")
	cluster.SendWager(t, sqsWager(t, msgID, wager(w, "provider-a", "BET", "25.00", unique("bet"), "")), testkit.SendOpts{GroupID: w.ID})

	cluster.Instance(0).AssertFaultHit(t, point)
	if outcomes := inboxOutcomes(t, msgID); outcomes["PROCESSED"] != 1 {
		t.Fatalf("inbox before the redelivery = %v, want the PROCESSED of the crashed instance", outcomes)
	}
	cluster.Restart(t, 1) // the consumer again, on another instance
	cluster.AssertQueueDrained(t)
	if got := cluster.Instance(1).MetricValue(t, `wager_duplicates_total{channel="sqs",layer="inbox"}`); got != 1 {
		t.Fatalf("inbox duplicates on instance 1 = %d, want 1: the redelivery", got)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("75.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}

// Covers: TST-C05, SQS-05, E5 (C05b; CHALLENGE §3, abrupt stop before the commit)
// Sensitivity: without the point → instance 0 did not exit; the point after the commit → the operation and its inbox row persist.
//
// The consumer of instance 0 writes the BET and dies before the commit:
// nothing is recorded. The redelivery is processed once by instance 1.
func TestCrashBeforeCommit(t *testing.T) {
	defer cluster.Restore(t)
	const point = "consumer.before_commit"
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	crashOn(t, point, config.Roles{Consumer: true})
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	msgID := unique("msg")
	cluster.SendWager(t, sqsWager(t, msgID, bet), testkit.SendOpts{GroupID: w.ID})

	cluster.Instance(0).AssertFaultHit(t, point)
	var recorded int
	if err := cluster.Owner().QueryRow(t.Context(), `SELECT
		(SELECT count(*) FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2) +
		(SELECT count(*) FROM inbox_messages WHERE message_id = $3)`,
		bet.ProviderID, bet.ExternalTransactionID, msgID).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Fatalf("%d rows of the operation and its message after a crash before the commit, want none", recorded)
	}
	cluster.Restart(t, 1)
	if outcomes := inboxOutcomes(t, msgID); len(outcomes) != 1 || outcomes["PROCESSED"] != 1 {
		t.Fatalf("inbox after the redelivery = %v, want one PROCESSED", outcomes)
	}
	cluster.AssertQueueDrained(t)
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("75.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}

// Covers: IDEM-01, IDEM-08, OUT-06a, E5, E6 (C05c)
// Sensitivity: without the point → instance 0 answered 200; the point before Execute → nothing was recorded before the crash.
//
// Instance 0 commits the BET and dies before it answers. The client sends it
// again to another instance and gets the recorded result. The events of the
// operation, which instance 0 never published, reach the audit queue through
// another publisher: item 8 of the consistency check of the wallet.
func TestHTTPCrashAfterCommit(t *testing.T) {
	defer cluster.Restore(t)
	const point = "http.after_commit_before_response"
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	cluster.Restart(t, 0, testkit.Fault(point))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")

	if resp, err := cluster.Instance(0).Client(t, "provider-a").Try(t, wagerRequest(bet)); err == nil {
		t.Fatalf("instance 0 answered %d %s, want no answer: it dies after the commit", resp.Status, resp.Body)
	}
	cluster.Instance(0).AssertFaultHit(t, point)
	recorded := transactionOf(t, "provider-a", bet.ExternalTransactionID)
	again := result(t, cluster.Client(t, "provider-a"), bet, http.StatusOK)
	wantResult(t, again, "PROCESSED", "", "75.00", true)
	if again.TransactionID != recorded.TransactionID {
		t.Fatalf("resend answered %s, the recorded operation is %s", again.TransactionID, recorded.TransactionID)
	}
	if got := balanceOf(t, w.ID); got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}
