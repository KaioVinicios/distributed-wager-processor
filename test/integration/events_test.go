//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: OUT-07, OUT-08, OUT-09, OUT-11, OUT-12, OUT-13, TST-I05 (I05e)
// Sensitivity: without the correlationId attribute in the SNS sink, every event failed the attribute check.
//
// Every kind of event, produced through the API, reaches the audit queue on
// the contract of api/events.yaml (the Audit validates each delivery) and with
// the routing of messaging.md §5.2.
func TestEventContracts(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00")) // OPENING: INTERNAL
	bet := unique("bet")
	result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
	result(t, a, wager(w, "provider-a", "WIN", "10.00", unique("win"), bet), http.StatusOK) // with references
	result(t, a, wager(w, "provider-a", "LOSS", "0.00", unique("loss"), ""), http.StatusOK)
	result(t, a, wager(w, "provider-a", "BET", "1000.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	result(t, a, wager(w, "provider-a", "REFUND", "5.00", unique("refund"), unique("bet")), http.StatusAccepted)

	stored, err := testkit.OutboxPayloads(t.Context(), server.Owner(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	if len(ids) != 9 {
		t.Fatalf("outbox of the wallet has %d events, want 9", len(ids))
	}
	got := server.Audit.WaitFor(t, ids...)
	seen := map[string]bool{}
	for id, payload := range stored {
		var head struct {
			EventType     string `json:"eventType"`
			CorrelationID string `json:"correlationId"`
			Data          struct {
				Origin                 string `json:"origin"`
				ReferenceTransactionID string `json:"referenceTransactionId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			t.Fatal(err)
		}
		m := got[id][0]
		if m.GroupID != w.ID || m.DedupID != id ||
			m.Attributes["eventType"] != (testkit.Attribute{Type: "String", Value: head.EventType}) ||
			m.Attributes["eventVersion"] != (testkit.Attribute{Type: "Number", Value: "1"}) ||
			m.Attributes["correlationId"] != (testkit.Attribute{Type: "String", Value: head.CorrelationID}) {
			t.Errorf("%s %s: group %q dedup %q attributes %v", head.EventType, id, m.GroupID, m.DedupID, m.Attributes)
		}
		seen[head.EventType+" "+head.Data.Origin] = true
		if head.Data.ReferenceTransactionID != "" {
			seen["with references"] = true
		}
	}
	for _, want := range []string{
		"WagerTransactionProcessed INTERNAL", "WagerTransactionProcessed EXTERNAL", "with references",
		"WalletBalanceChanged ", "WagerTransactionRejected ", "WagerTransactionPendingReference ",
	} {
		if !seen[want] {
			t.Errorf("no %q among the delivered events", want)
		}
	}
}
