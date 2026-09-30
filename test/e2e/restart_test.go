//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C08, IDEM-01, E6 (C08a)
// Sensitivity: the idempotency lookup returning nothing → the replays after the restart answer 503.
//
// Processed, rejected and pending operations, over HTTP and SQS; then every
// instance is killed and started again. The replays return what was recorded,
// the redelivered message is a duplicate, and the pending operation ends.
// Asynchronous acceptance does not apply: PENDING is never persisted (D-05).
func TestFullRestart(t *testing.T) {
	defer cluster.Restore(t)
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	a := cluster.Client(t, "provider-a")
	processed := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	rejected := wager(w, "provider-a", "BET", "500.00", unique("bet"), "")
	refund := wager(w, "provider-a", "REFUND", "10.00", unique("refund"), unique("bet"))
	bySQS := wager(w, "provider-a", "BET", "20.00", unique("bet"), "")
	msgID := unique("msg")
	first := result(t, a, processed, http.StatusOK)
	firstRejected := result(t, a, rejected, http.StatusUnprocessableEntity)
	result(t, a, refund, http.StatusAccepted)
	cluster.SendWager(t, sqsWager(t, msgID, bySQS), testkit.SendOpts{GroupID: w.ID})
	fromSQS := transactionOf(t, "provider-a", bySQS.ExternalTransactionID)

	for i := range 3 {
		cluster.Kill(t, i)
	}
	cluster.Restore(t)

	again := result(t, a, processed, http.StatusOK)
	wantResult(t, again, "PROCESSED", "", "70.00", true)
	againRejected := result(t, a, rejected, http.StatusUnprocessableEntity)
	wantResult(t, againRejected, "REJECTED", "INSUFFICIENT_FUNDS", "70.00", true)
	againSQS := result(t, a, bySQS, http.StatusOK)
	wantResult(t, againSQS, "PROCESSED", "", "50.00", true)
	if again.TransactionID != first.TransactionID || againRejected.TransactionID != firstRejected.TransactionID ||
		againSQS.TransactionID != fromSQS.TransactionID {
		t.Fatalf("replays %s %s %s, recorded %s %s %s", again.TransactionID, againRejected.TransactionID, againSQS.TransactionID,
			first.TransactionID, firstRejected.TransactionID, fromSQS.TransactionID)
	}
	cluster.SendWager(t, sqsWager(t, msgID, bySQS), testkit.SendOpts{GroupID: w.ID}) // the same message again
	cluster.AssertQueueDrained(t)
	if outcomes := inboxOutcomes(t, msgID); len(outcomes) != 1 || outcomes["PROCESSED"] != 1 {
		t.Fatalf("inbox of the redelivered message = %v, want the one PROCESSED row", outcomes)
	}
	expired := waitStatus(t, a, "provider-a", refund.ExternalTransactionID, "REJECTED")
	if expired.FailureCode != "REFERENCE_NOT_FOUND" {
		t.Fatalf("pending REFUND after the restart = %+v, want REJECTED REFERENCE_NOT_FOUND", expired)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("50.00") || got.Version != 3 {
		t.Fatalf("wallet = %+v, want 50.00 after two debits", got)
	}
}

// Covers: TST-C08, TX-09, OPS-13, E7 (C08b)
// Sensitivity: without the point → instance 0 did not exit; a claim that hides the operation for an
// hour → instance 1 never concludes it.
//
// The worker of instance 0 claims the pending REFUND and dies before it
// resolves it. The claim is no lease: the operation stays due in the
// database, and the worker of instance 1 concludes it (with no BET ever, by
// expiration).
func TestReferenceWorkerCrash(t *testing.T) {
	defer cluster.Restore(t)
	const point = "references.after_claim"
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	crashOn(t, point, config.Roles{ReferenceWorker: true})
	refund := unique("refund")
	result(t, cluster.Client(t, "provider-a"), wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted)

	cluster.Instance(0).AssertFaultHit(t, point)
	cluster.Restart(t, 1) // a worker again, on another instance
	tx := waitStatus(t, cluster.Client(t, "provider-a"), "provider-a", refund, "REJECTED")
	if tx.FailureCode != "REFERENCE_NOT_FOUND" {
		t.Fatalf("REFUND = %+v, want REJECTED REFERENCE_NOT_FOUND", tx)
	}
	if got := cluster.Instance(1).MetricValue(t, "reference_expired_total"); got != 1 {
		t.Fatalf("reference_expired_total on instance 1 = %d, want 1: instance 1 concluded it", got)
	}
}
