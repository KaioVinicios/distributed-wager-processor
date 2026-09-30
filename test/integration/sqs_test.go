//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// sqsWager is the WagerTransactionRequested of the operation, with the key
// {providerId}:{externalTransactionId}, as the HTTP helpers use.
func sqsWager(t *testing.T, messageID string, body testkit.Wager) string {
	t.Helper()
	money := body.Money
	return testkit.WagerMessage(t, messageID, testkit.WagerData{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		IdempotencyKey: body.ProviderID + ":" + body.ExternalTransactionID, PlayerID: body.PlayerID,
		WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID, Kind: body.Kind, Money: &money,
		ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	})
}

// transactionOf waits until the operation exists and reads it as its provider.
func transactionOf(t *testing.T, provider, ext string) testkit.Transaction {
	t.Helper()
	c := server.Client(t, provider)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/" + provider + "/wagering/transactions/" + ext})
		if resp.Status == http.StatusOK {
			var tx testkit.Transaction
			resp.JSON(t, &tx)
			return tx
		}
	}
	t.Fatalf("operation %s not recorded within 20s", ext)
	return testkit.Transaction{}
}

// eventsOf waits for every outbox event of the wallet in the audit queue and
// returns their envelopes.
func eventsOf(t *testing.T, walletID string) []map[string]any {
	t.Helper()
	stored, err := testkit.OutboxPayloads(t.Context(), server.Owner(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	var out []map[string]any
	for _, deliveries := range server.Audit.WaitFor(t, ids...) {
		var env map[string]any
		if err := json.Unmarshal(deliveries[0].Body, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env)
	}
	return out
}

// Covers: SQS-06 (I04e)
func TestBusinessRejectionDeletesMessage(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("10.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	server.SendWager(t, sqsWager(t, unique("msg"), bet), testkit.SendOpts{GroupID: w.ID})

	tx := transactionOf(t, "provider-a", bet.ExternalTransactionID)
	if tx.Status != "REJECTED" || tx.FailureCode != "INSUFFICIENT_FUNDS" || tx.ReceivedVia != "SQS" {
		t.Fatalf("operation = %+v, want REJECTED INSUFFICIENT_FUNDS over SQS", tx)
	}
	server.AssertQueueDrained(t)
	if n := server.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ has %d messages, want none", n)
	}
	rejected := false
	for _, env := range eventsOf(t, w.ID) {
		rejected = rejected || env["eventType"] == "WagerTransactionRejected"
	}
	if !rejected {
		t.Fatal("no WagerTransactionRejected in the audit queue")
	}
}

// Covers: SQS-02, SQS-04, IDEM-04
//
// A BET over SQS is processed with the message as the cause of its events and
// the correlation id of the attribute; the same operation over HTTP afterwards
// is the replay of the SQS one.
func TestSQSEndToEnd(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	msgID, corr := unique("msg"), unique("corr")
	server.SendWager(t, sqsWager(t, msgID, bet), testkit.SendOpts{GroupID: w.ID, CorrelationID: corr})

	tx := transactionOf(t, "provider-a", bet.ExternalTransactionID)
	if tx.Status != "PROCESSED" || tx.ReceivedVia != "SQS" || tx.Balance == nil || tx.Balance.Amount != "70.00" {
		t.Fatalf("operation = %+v, want PROCESSED over SQS with balance 70.00", tx)
	}
	server.AssertQueueDrained(t)
	caused := 0
	for _, env := range eventsOf(t, w.ID) {
		if data, _ := env["data"].(map[string]any); data["transactionId"] != tx.TransactionID {
			continue // the events of the opening
		}
		caused++
		if env["causationId"] != msgID || env["correlationId"] != corr {
			t.Errorf("%s: causation %v correlation %v, want %s %s", env["eventType"], env["causationId"], env["correlationId"], msgID, corr)
		}
	}
	if caused != 2 {
		t.Fatalf("%d events of the BET, want WagerTransactionProcessed and WalletBalanceChanged", caused)
	}

	replay := result(t, server.Client(t, "provider-a"), bet, http.StatusOK)
	wantResult(t, replay, "PROCESSED", "", "70.00", true)
	if replay.TransactionID != tx.TransactionID {
		t.Fatalf("HTTP replay of %s, want %s", replay.TransactionID, tx.TransactionID)
	}
}
