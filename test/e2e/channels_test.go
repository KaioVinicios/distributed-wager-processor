//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C10, SQS-02, IDEM-04, E5 (C10a)
// Sensitivity: the consumer altering the idempotencyKey → the channels conflict on the externalTransactionId
// (409 EXTERNAL_TRANSACTION_ID_CONFLICT over HTTP; the SQS delivery goes to the DLQ and never reaches the inbox).
//
// The operation over HTTP and then over SQS, and the reverse: one debit, and
// the second channel answers with the replay.
func TestHTTPThenSQSSameOperation(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	t.Run("HTTP first", func(t *testing.T) {
		t.Parallel()
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
		wantResult(t, result(t, cluster.Client(t, "provider-a"), bet, http.StatusOK), "PROCESSED", "", "75.00", false)
		msgID := unique("msg")
		cluster.SendWager(t, sqsWager(t, msgID, bet), testkit.SendOpts{GroupID: w.ID})
		if outcomes := inboxOutcomes(t, msgID); outcomes["IDEMPOTENT_REPLAY"] != 1 {
			t.Fatalf("inbox = %v, want IDEMPOTENT_REPLAY", outcomes)
		}
		if got := balanceOf(t, w.ID); got.Version != 2 {
			t.Fatalf("wallet = %+v, want one debit", got)
		}
	})
	t.Run("SQS first", func(t *testing.T) {
		t.Parallel()
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
		msgID := unique("msg")
		cluster.SendWager(t, sqsWager(t, msgID, bet), testkit.SendOpts{GroupID: w.ID})
		if outcomes := inboxOutcomes(t, msgID); outcomes["PROCESSED"] != 1 {
			t.Fatalf("inbox = %v, want PROCESSED", outcomes)
		}
		recorded := transactionOf(t, "provider-a", bet.ExternalTransactionID)
		again := result(t, cluster.Client(t, "provider-a"), bet, http.StatusOK)
		wantResult(t, again, "PROCESSED", "", "75.00", true)
		if again.TransactionID != recorded.TransactionID {
			t.Fatalf("HTTP replay of %s, the SQS operation is %s", again.TransactionID, recorded.TransactionID)
		}
		if got := balanceOf(t, w.ID); got.Version != 2 {
			t.Fatalf("wallet = %+v, want one debit", got)
		}
	})
}

// raceBehindLock makes an SQS delivery and an HTTP request on the same wallet
// meet on its row lock, whatever the latency of each channel (SQS delivers
// long after an HTTP request is answered): the test holds the lock, sends the
// message, waits for the consumer to block on the lock, sends the request,
// waits for it to block too, and releases the lock. It returns the HTTP answer.
func raceBehindLock(t *testing.T, walletID, message string, send func() *testkit.Response) *testkit.Response {
	t.Helper()
	tx, err := cluster.Owner().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, walletID); err != nil {
		t.Fatal(err)
	}
	cluster.SendWager(t, message, testkit.SendOpts{GroupID: walletID})
	waitLockWaiters(t, 1)
	answer := make(chan *testkit.Response, 1)
	go func() { answer <- send() }()
	waitLockWaiters(t, 2)
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	return <-answer
}

// waitLockWaiters waits until n sessions of this database wait for a lock.
// The test that uses it runs alone (no t.Parallel), so they are its own.
func waitLockWaiters(t *testing.T, n int) {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, fmt.Sprintf("%d sessions waiting for the wallet lock", n), func(ctx context.Context) (bool, error) {
		var waiting int
		err := cluster.Owner().QueryRow(ctx, `SELECT count(DISTINCT l.pid) FROM pg_locks l
			JOIN pg_stat_activity a ON a.pid = l.pid
			WHERE NOT l.granted AND a.datname = current_database()`).Scan(&waiting)
		return waiting >= n, err
	})
}

// Covers: TST-C10, SQS-11, E4, E5 (C10b)
// Sensitivity: the FOR UPDATE removed from the wallet lock → both BETs of 80.00 update the same version
// and one ends FAILED.
//
// The same operation over HTTP and SQS at once: exactly one channel records
// it. And 100.00 against 80.00 over HTTP and 80.00 over SQS at once: C02
// across the channels. "At once" is made by raceBehindLock; it counts the
// lock waits of the database, so this test does not run in parallel.
func TestHTTPAndSQSConcurrent(t *testing.T) {
	cluster.AttachLogs(t)
	a := cluster.Client(t, "provider-a")
	t.Run("the same operation", func(t *testing.T) {
		for range 5 {
			w := cluster.OpenWallet(t, testkit.BRL("100.00"))
			bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
			msgID := unique("msg")
			resp := raceBehindLock(t, w.ID, sqsWager(t, msgID, bet), func() *testkit.Response { return submit(t, a, bet) })
			var r testkit.TransactionResult
			resp.JSON(t, &r)
			if resp.Status != http.StatusOK || r.Status != "PROCESSED" {
				t.Fatalf("HTTP = %d %+v, want 200 PROCESSED", resp.Status, r)
			}
			outcomes := inboxOutcomes(t, msgID)
			if sqsRecorded := outcomes["PROCESSED"] == 1; sqsRecorded == !r.IdempotentReplay {
				t.Fatalf("HTTP replay %v and inbox %v: exactly one channel must record the operation", r.IdempotentReplay, outcomes)
			}
			if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("75.00") || got.Version != 2 {
				t.Fatalf("wallet = %+v, want one debit", got)
			}
		}
	})
	t.Run("100.00 against 80.00 by HTTP and 80.00 by SQS", func(t *testing.T) {
		for range 5 {
			w := cluster.OpenWallet(t, testkit.BRL("100.00"))
			byHTTP := wager(w, "provider-a", "BET", "80.00", unique("bet-http"), "")
			bySQS := wager(w, "provider-a", "BET", "80.00", unique("bet-sqs"), "")
			resp := raceBehindLock(t, w.ID, sqsWager(t, unique("msg"), bySQS), func() *testkit.Response { return submit(t, a, byHTTP) })
			var h testkit.TransactionResult
			resp.JSON(t, &h)
			if resp.Status != http.StatusOK && resp.Status != http.StatusUnprocessableEntity {
				t.Fatalf("HTTP = %d %+v, want 200 or 422", resp.Status, h)
			}
			s := transactionOf(t, "provider-a", bySQS.ExternalTransactionID)
			processed, short := 0, 0
			for _, got := range [][2]string{{h.Status, h.FailureCode}, {s.Status, s.FailureCode}} {
				switch got {
				case [2]string{"PROCESSED", ""}:
					processed++
				case [2]string{"REJECTED", "INSUFFICIENT_FUNDS"}:
					short++
				}
			}
			if processed != 1 || short != 1 {
				t.Fatalf("HTTP %s %s, SQS %s %s; want one PROCESSED and one INSUFFICIENT_FUNDS", h.Status, h.FailureCode, s.Status, s.FailureCode)
			}
			if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("20.00") || got.Version != 2 {
				t.Fatalf("wallet = %+v, want 20.00 after one debit", got)
			}
		}
	})
}
