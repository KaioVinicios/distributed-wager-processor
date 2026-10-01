//go:build integration

package sqsconsumer_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-09, SQS-07, D-23 (A06)
// Sensitivity: authenticate passing receivedAt instead of the SentTimestamp → "DLQ has 2 messages, want 1": the message sent while its token was valid goes to the DLQ too.
//
// The token is checked at the SentTimestamp, with the real IdP: a message sent
// while its token was valid is processed after the token expired; a message
// sent with the expired token goes to the DLQ.
func TestTokenCheckedAtSendTime(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w := openWallet(t, "100.00")
	token := testkit.FreshToken(t, "provider-short-lived") // 5 s of life, provider_id provider-a
	ext := "bet-" + testkit.NewID()
	testkit.SendMessage(t, f.sqs, f.queues.WagerURL, wager(t, "msg-"+testkit.NewID(), w, "provider-a", "BET", "1.00", ext, ""),
		testkit.SendOpts{GroupID: w.id, Token: token})
	// Past the exp and the 1 s skew of testkit.NewVerifier: the token is expired from here on.
	time.Sleep(time.Until(testkit.TokenExpiry(t, token).Add(1500 * time.Millisecond)))
	late := testkit.SendMessage(t, f.sqs, f.queues.WagerURL,
		wager(t, "msg-"+testkit.NewID(), w, "provider-a", "BET", "2.00", "bet-"+testkit.NewID(), ""),
		testkit.SendOpts{GroupID: w.id, Token: token})

	f.start(t, consumerOpts{auth: testkit.NewVerifier(t)})
	dlq := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)
	if dlq[0].Attributes["originalMessageId"] != late || dlq[0].Attributes["errorCode"] != "UNAUTHENTICATED" {
		t.Fatalf("DLQ = %+v, want the message sent with the expired token, UNAUTHENTICATED", dlq[0])
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM wager_transactions
		WHERE provider_id = 'provider-a' AND external_transaction_id = $1 AND status = 'PROCESSED'`, ext); n != 1 {
		t.Fatalf("%d PROCESSED for the message sent with a valid token, want 1", n)
	}
	if got := balance(t, w.id); got != "99.00" {
		t.Fatalf("balance = %s, want 99.00", got)
	}
}
