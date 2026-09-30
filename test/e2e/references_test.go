//go:build e2e

package e2e_test

import (
	"maps"
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// eventTypesOf counts the events of one operation of the wallet by type.
func eventTypesOf(t *testing.T, walletID, transactionID string) map[string]int {
	t.Helper()
	types := map[string]int{}
	for _, env := range eventsOf(t, walletID) {
		if data, _ := env["data"].(map[string]any); data["transactionId"] == transactionID {
			typ, _ := env["eventType"].(string)
			types[typ]++
		}
	}
	return types
}

// assertRefundResolved: the REFUND ends PROCESSED, the balance is back to
// 100.00 after the debit and the credit, and its events are the pending one
// and then the processed ones.
func assertRefundResolved(t *testing.T, walletID, refundID, refundExt string) {
	t.Helper()
	done := waitStatus(t, cluster.Client(t, "provider-a"), "provider-a", refundExt, "PROCESSED")
	if done.Balance == nil || done.Balance.Amount != "100.00" {
		t.Fatalf("REFUND = %+v, want PROCESSED with balance 100.00", done)
	}
	if got := balanceOf(t, walletID); got.Balance != testkit.BRL("100.00") || got.Version != 3 {
		t.Fatalf("wallet = %+v, want 100.00 after the debit and the credit", got)
	}
	want := map[string]int{"WagerTransactionPendingReference": 1, "WagerTransactionProcessed": 1, "WalletBalanceChanged": 1}
	if got := eventTypesOf(t, walletID, refundID); !maps.Equal(got, want) {
		t.Fatalf("events of the REFUND = %v, want %v", got, want)
	}
}

// assertRefundExpired: the REFUND ends REJECTED with REFERENCE_NOT_FOUND, with
// its rejection event and no ledger entry.
func assertRefundExpired(t *testing.T, walletID, refundID, refundExt string) {
	t.Helper()
	tx := waitStatus(t, cluster.Client(t, "provider-a"), "provider-a", refundExt, "REJECTED")
	if tx.FailureCode != "REFERENCE_NOT_FOUND" || tx.Balance == nil || tx.Balance.Amount != "100.00" {
		t.Fatalf("REFUND = %+v, want REJECTED REFERENCE_NOT_FOUND with balance 100.00", tx)
	}
	if got := balanceOf(t, walletID); got.Version != 1 {
		t.Fatalf("wallet = %+v, want version 1: an expiration moves nothing", got)
	}
	if got := eventTypesOf(t, walletID, refundID); got["WagerTransactionRejected"] != 1 {
		t.Fatalf("events of the REFUND = %v, want one WagerTransactionRejected", got)
	}
}

// Covers: TST-C07, OPS-12, E7 (C07a)
// Sensitivity: references.Module out of the graph → the REFUND stays PENDING_REFERENCE.
//
// A REFUND before its BET waits, and is processed once the BET arrives on
// another instance, over HTTP and over SQS.
func TestRefundBeforeBet(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	t.Run("HTTP", func(t *testing.T) {
		t.Parallel()
		a := cluster.Client(t, "provider-a") // round-robin: the REFUND and the BET land on different instances
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		bet, refund := unique("bet"), unique("refund")
		pending := result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, bet), http.StatusAccepted)
		result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
		assertRefundResolved(t, w.ID, pending.TransactionID, refund)
	})
	t.Run("SQS", func(t *testing.T) {
		t.Parallel()
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		bet, refund := unique("bet"), unique("refund")
		cluster.SendWager(t, sqsWager(t, unique("msg"), wager(w, "provider-a", "REFUND", "30.00", refund, bet)), testkit.SendOpts{GroupID: w.ID})
		cluster.SendWager(t, sqsWager(t, unique("msg"), wager(w, "provider-a", "BET", "30.00", bet, "")), testkit.SendOpts{GroupID: w.ID})
		pending := transactionOf(t, "provider-a", refund)
		assertRefundResolved(t, w.ID, pending.TransactionID, refund)
	})
}

// Covers: TST-C07, OPS-13 (C07b)
// Sensitivity: RescheduleReference never expiring → the REFUND is never rejected.
//
// A REFUND whose BET never arrives is rejected when its attempts or its TTL
// run out, over HTTP and over SQS.
func TestRefundReferenceExpires(t *testing.T) {
	t.Parallel()
	cluster.AttachLogs(t)
	t.Run("HTTP", func(t *testing.T) {
		t.Parallel()
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		refund := unique("refund")
		pending := result(t, cluster.Client(t, "provider-a"), wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted)
		assertRefundExpired(t, w.ID, pending.TransactionID, refund)
	})
	t.Run("SQS", func(t *testing.T) {
		t.Parallel()
		w := cluster.OpenWallet(t, testkit.BRL("100.00"))
		refund := unique("refund")
		cluster.SendWager(t, sqsWager(t, unique("msg"), wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet"))), testkit.SendOpts{GroupID: w.ID})
		pending := transactionOf(t, "provider-a", refund)
		assertRefundExpired(t, w.ID, pending.TransactionID, refund)
	})
}
