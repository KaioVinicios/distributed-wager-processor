//go:build integration

package sqsconsumer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-I04, SQS-03, SQS-05, TST-C11 (I04a)
// Sensitivity: no DeleteMessage after the commit → the deliveries were
// redriven and "DLQ depth = 2" (before the DLQ check, the redrive drained the
// queue and the test passed).
func TestInboxDeduplication(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{})
	msgID := "msg-" + testkit.NewID()
	body := wager(t, msgID, w, p, "BET", "30.00", "bet-1", "")
	f.send(t, body, w.id) // two sends, two MessageDeduplicationIds
	f.send(t, body, w.id)

	testkit.Eventually(t, 20*time.Second, "the second delivery counted as an inbox duplicate", func(context.Context) (bool, error) {
		return f.metric(t, "wager_duplicates_total", "channel=sqs", "layer=inbox") == 1, nil
	})
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n, err := testkit.QueueDepth(t.Context(), f.sqs, f.queues.DLQURL); err != nil || n != 0 {
		t.Fatalf("DLQ depth = %d, %v; want both deliveries deleted, none redriven", n, err)
	}
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
		t.Fatalf("%d operations, want 1", n)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.id); n != 2 {
		t.Fatalf("%d entries, want the opening and one debit", n)
	}
	if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msgID); n != 1 {
		t.Fatalf("%d inbox rows, want 1", n)
	}
	if got := balance(t, w.id); got != "70.00" {
		t.Fatalf("balance = %s, want 70.00", got)
	}
}

// Covers: SQS-03 (I04b)
func TestInboxHashMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{})
	msgID := "msg-" + testkit.NewID()
	f.send(t, wager(t, msgID, w, p, "BET", "30.00", "bet-1", ""), w.id)
	testkit.Eventually(t, 20*time.Second, "the first message concluded", func(ctx context.Context) (bool, error) {
		var n int
		err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msgID).Scan(&n)
		return n == 1, err
	})

	f.send(t, wager(t, msgID, w, p, "BET", "31.00", "bet-1", ""), w.id)
	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != app.CodeMessageHashMismatch || !strings.Contains(got.Body, `"31.00"`) {
		t.Fatalf("DLQ = %+v, want the second message with MESSAGE_HASH_MISMATCH", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}

// Covers: SQS-07, SQS-10 (I04c)
func TestInvalidMessagesGoToDLQ(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	ghost := testWallet{id: testkit.NewID(), player: w.player}
	valid := wager(t, "msg-x", w, p, "BET", "1.00", "bet-x", "")
	cases := map[string]string{ // errorCode by body
		"MALFORMED_MESSAGE":        `{"messageId":`,
		"UNSUPPORTED_MESSAGE_TYPE": strings.Replace(valid, "WagerTransactionRequested", "WalletOpened", 1),
		"OPENING_NOT_ALLOWED":      wager(t, "msg-"+testkit.NewID(), w, p, "OPENING", "1.00", "open-1", ""),
		"UNKNOWN_WALLET":           wager(t, "msg-"+testkit.NewID(), ghost, p, "BET", "1.00", "bet-2", ""),
	}
	unknownField := strings.Replace(valid, `"type"`, `"extra":1,"type"`, 1)
	f.start(t, consumerOpts{})
	sent := map[string]string{} // SQS id → expected code
	bodies := map[string]string{}
	for code, body := range cases {
		id := f.send(t, body, "group-"+code)
		sent[id], bodies[id] = code, body
	}
	id := f.send(t, unknownField, "group-unknown-field")
	sent[id], bodies[id] = "MALFORMED_MESSAGE", unknownField

	for _, m := range testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, len(sent)) {
		orig := m.Attributes["originalMessageId"]
		if m.Attributes["errorCode"] != sent[orig] || m.Body != bodies[orig] || m.DedupID != orig ||
			m.Attributes["errorCategory"] != "CORRECTABLE" || m.Attributes["consumerName"] != app.ConsumerName ||
			!strings.HasSuffix(m.Attributes["failedAt"], "Z") || !strings.HasPrefix(m.GroupID, "group-") {
			t.Errorf("DLQ message = %+v, want errorCode %s for %s", m, sent[orig], orig)
		}
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
		t.Fatalf("%d operations recorded from invalid messages", n)
	}
}
