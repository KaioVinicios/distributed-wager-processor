//go:build integration

package sqsconsumer_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: SQS-07, TST-I05 (I04d)
// Sensitivity: a retry that also deleted the message → "1 messages in the
// DLQ: not reached within 20s".
func TestTransientFailureRedrive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var calls atomic.Int32
	proc := processorFunc(func(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
		calls.Add(1)
		return app.ConsumeResult{}, apperrors.New(apperrors.KindTransient, "", errors.New("forced transient failure"))
	})
	f.start(t, consumerOpts{proc: proc})
	body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
	f.send(t, body, "group-1")

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Body != body || got.Attributes["errorCode"] != "" {
		t.Fatalf("DLQ = %+v, want the original message moved by the redrive", got)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("processed %d times, want the 3 receives of the redrive policy", n)
	}
	if n := f.metric(t, "sqs_retries_total", "reason=transient"); n != 3 {
		t.Fatalf("sqs_retries_total{transient} = %v, want 3", n)
	}
	if f.metric(t, "sqs_dlq_sent_total") != 0 {
		t.Fatal("a transient failure was sent to the DLQ explicitly")
	}
}

// Covers: TX-06, SQS-07, TST-I03 (I03b, the SQS part)
func TestPermanentFailureToDLQ(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{proc: newConsumeWager(failingOutboxUoW{newUoW()})})
	msgID := "msg-" + testkit.NewID()
	f.send(t, wager(t, msgID, w, p, "BET", "30.00", "bet-1", ""), w.id)

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != "INTERNAL_PERMANENT_FAILURE" || got.Attributes["errorCategory"] != "DEFINITIVE" {
		t.Fatalf("DLQ = %+v, want INTERNAL_PERMANENT_FAILURE", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM inbox_messages i JOIN wager_transactions t ON t.id = i.transaction_id
		WHERE i.message_id = $1 AND i.outcome = 'FAILED' AND t.status = 'FAILED'`, msgID); n != 1 {
		t.Fatalf("inbox FAILED rows with their FAILED operation = %d, want 1", n)
	}
	if b := balance(t, w.id); b != "100.00" {
		t.Fatalf("balance = %s, want 100.00", b)
	}
}

// Covers: SQS-05, SQS-07 (messaging.md §4.4)
// Sensitivity: deleting after a failed DLQ send → "1 messages in the DLQ: not
// reached within 20s".
func TestDLQSendFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	spy := &queueSpy{QueueAPI: f.sqs}
	spy.failSends.Store(1)
	f.start(t, consumerOpts{api: spy})
	f.send(t, `{"messageId":`, "group-1")

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != "MALFORMED_MESSAGE" {
		t.Fatalf("DLQ = %+v", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := f.metric(t, "sqs_retries_total", "reason=transient"); n != 1 {
		t.Fatalf("sqs_retries_total{transient} = %v, want the failed send retried once", n)
	}
	if n := f.metric(t, "sqs_dlq_sent_total", "reason=MALFORMED_MESSAGE"); n != 1 {
		t.Fatalf("sqs_dlq_sent_total = %v, want 1", n)
	}
}
