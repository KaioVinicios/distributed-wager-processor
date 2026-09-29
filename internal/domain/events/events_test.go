package events_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

const (
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	txID     = "0192f298-345e-7e38-af88-e43f851a819d"
	refTxID  = "0192f297-0000-7000-8000-000000000002"
	eventID  = "0192f2a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b"
)

// at has sub-millisecond digits and a non-UTC zone: JSON must show UTC with
// exactly three truncated decimals.
var at = time.Date(2026, 9, 8, 9, 0, 0, 123987654, time.FixedZone("BRT", -3*3600))

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}

func balanceChanged(t *testing.T) events.WalletBalanceChanged {
	t.Helper()
	return events.WalletBalanceChanged{
		WalletID: walletID, TransactionID: txID, TransactionKind: "BET", Direction: "DEBIT",
		Money: brl(t, "25.00"), BalanceBefore: brl(t, "1000.00"), BalanceAfter: brl(t, "975.00"),
		WalletVersion: 2, ChangedAt: events.NewTime(at),
	}
}

func processedExternal(t *testing.T) events.WagerTransactionProcessed {
	t.Helper()
	return events.WagerTransactionProcessed{
		TransactionID: txID, Origin: "EXTERNAL", Kind: "REFUND", WalletID: walletID, PlayerID: playerID,
		ProviderID: "provider-a", ExternalTransactionID: "transaction-124", RoundID: "round-987", GameID: "fortune-chimp",
		Money: brl(t, "25.00"), BalanceAfter: brl(t, "1000.00"), WalletVersion: 3,
		ReferenceExternalTransactionID: "transaction-123", ReferenceTransactionID: refTxID,
		ProcessedAt: events.NewTime(at),
	}
}

func processedOpening(t *testing.T) events.WagerTransactionProcessed {
	t.Helper()
	return events.WagerTransactionProcessed{
		TransactionID: txID, Origin: "INTERNAL", Kind: "OPENING", WalletID: walletID, PlayerID: playerID,
		Money: brl(t, "1000.00"), BalanceAfter: brl(t, "1000.00"), WalletVersion: 1,
		ProcessedAt: events.NewTime(at),
	}
}

func rejected(t *testing.T) events.WagerTransactionRejected {
	t.Helper()
	return events.WagerTransactionRejected{
		TransactionID: txID, Kind: "BET", WalletID: walletID, PlayerID: playerID,
		ProviderID: "provider-a", ExternalTransactionID: "transaction-123", RoundID: "round-987", GameID: "fortune-chimp",
		Money: brl(t, "80.00"), FailureCode: "INSUFFICIENT_FUNDS", FailureCategory: "DEFINITIVE",
		Balance: brl(t, "20.00"), RejectedAt: events.NewTime(at),
	}
}

func pendingReference(t *testing.T) events.WagerTransactionPendingReference {
	t.Helper()
	return events.WagerTransactionPendingReference{
		TransactionID: txID, Kind: "REFUND", WalletID: walletID, PlayerID: playerID,
		ProviderID: "provider-a", ExternalTransactionID: "transaction-124", RoundID: "round-987", GameID: "fortune-chimp",
		Money: brl(t, "25.00"), ReferenceExternalTransactionID: "transaction-123",
		NextAttemptAt: events.NewTime(at.Add(time.Second)), ExpiresAt: events.NewTime(at.Add(10 * time.Minute)),
		PendingAt: events.NewTime(at),
	}
}

func TestEventConstructors(t *testing.T) {
	// Covers: TST-U06, OUT-08, OUT-09
	type result struct {
		ev  events.Event
		err error
	}
	build := map[events.Type]func() result{
		events.TypeWalletBalanceChanged: func() result {
			e, err := events.NewWalletBalanceChanged(balanceChanged(t))
			return result{e, err}
		},
		events.TypeWagerTransactionProcessed: func() result {
			e, err := events.NewWagerTransactionProcessed(processedExternal(t))
			return result{e, err}
		},
		events.TypeWagerTransactionRejected: func() result {
			e, err := events.NewWagerTransactionRejected(rejected(t))
			return result{e, err}
		},
		events.TypeWagerTransactionPendingReference: func() result {
			e, err := events.NewWagerTransactionPendingReference(pendingReference(t))
			return result{e, err}
		},
	}
	for typ, b := range build {
		r := b()
		if r.err != nil {
			t.Fatalf("%s: %v", typ, r.err)
		}
		aggType, aggID := r.ev.Aggregate()
		wantAgg, wantID := events.AggregateWagerTransaction, txID
		if typ == events.TypeWalletBalanceChanged {
			wantAgg, wantID = events.AggregateWallet, walletID
		}
		if r.ev.Type() != typ || r.ev.Version() != 1 || aggType != wantAgg || aggID != wantID ||
			r.ev.MessageGroupID() != walletID || !r.ev.OccurredAt().Equal(at) || r.ev.OccurredAt().Location() != time.UTC {
			t.Errorf("%s: type %s version %d aggregate %s/%s group %s occurredAt %v",
				typ, r.ev.Type(), r.ev.Version(), aggType, aggID, r.ev.MessageGroupID(), r.ev.OccurredAt())
		}
	}

	invalid := map[string]func() error{
		"balance changed without wallet": func() error {
			e := balanceChanged(t)
			e.WalletID = ""
			_, err := events.NewWalletBalanceChanged(e)
			return err
		},
		"balance changed with zero money": func() error {
			e := balanceChanged(t)
			e.Money = brl(t, "0.00")
			_, err := events.NewWalletBalanceChanged(e)
			return err
		},
		"balance changed with unknown direction": func() error {
			e := balanceChanged(t)
			e.Direction = "debit"
			_, err := events.NewWalletBalanceChanged(e)
			return err
		},
		"external processed without provider": func() error {
			e := processedExternal(t)
			e.ProviderID = ""
			_, err := events.NewWagerTransactionProcessed(e)
			return err
		},
		"internal processed with external metadata": func() error {
			e := processedOpening(t)
			e.RoundID = "round-1"
			_, err := events.NewWagerTransactionProcessed(e)
			return err
		},
		"processed without instant": func() error {
			e := processedExternal(t)
			e.ProcessedAt = events.Time{}
			_, err := events.NewWagerTransactionProcessed(e)
			return err
		},
		"rejected without failure code": func() error {
			e := rejected(t)
			e.FailureCode = ""
			_, err := events.NewWagerTransactionRejected(e)
			return err
		},
		"pending without reference": func() error {
			e := pendingReference(t)
			e.ReferenceExternalTransactionID = ""
			_, err := events.NewWagerTransactionPendingReference(e)
			return err
		},
	}
	for name, op := range invalid {
		if err := op(); !errors.Is(err, events.ErrInvalidEvent) {
			t.Errorf("%s: error = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestSeal(t *testing.T) {
	// Covers: OUT-09
	ev, err := events.NewWalletBalanceChanged(balanceChanged(t))
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.Seal(eventID, "corr-1", "msg-123", ev)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if env.EventID() != eventID || env.CorrelationID() != "corr-1" || env.CausationID() != "msg-123" ||
		env.Type() != events.TypeWalletBalanceChanged || env.Version() != 1 ||
		env.AggregateType() != events.AggregateWallet || env.AggregateID() != walletID ||
		env.MessageGroupID() != walletID || !env.OccurredAt().Equal(at) || env.Event() != events.Event(ev) {
		t.Fatalf("envelope getters: %+v", env)
	}

	long := strings.Repeat("x", 129)
	invalid := map[string]func() error{
		"event id not a UUID":    func() error { _, err := events.Seal("evt-1", "corr", "", ev); return err },
		"uppercase event id":     func() error { _, err := events.Seal(strings.ToUpper(eventID), "corr", "", ev); return err },
		"empty correlation":      func() error { _, err := events.Seal(eventID, "", "", ev); return err },
		"long correlation":       func() error { _, err := events.Seal(eventID, long, "", ev); return err },
		"long causation":         func() error { _, err := events.Seal(eventID, "corr", long, ev); return err },
		"nil event":              func() error { _, err := events.Seal(eventID, "corr", "", nil); return err },
		"literal without ctor":   func() error { _, err := events.Seal(eventID, "corr", "", events.WalletBalanceChanged{}); return err },
		"unsealed envelope JSON": func() error { _, err := json.Marshal(events.Envelope{}); return err },
	}
	for name, op := range invalid {
		if err := op(); !errors.Is(err, events.ErrInvalidEvent) {
			t.Errorf("%s: error = %v, want ErrInvalidEvent", name, err)
		}
	}
}

func TestEnvelopeJSON(t *testing.T) {
	// Covers: OUT-08, OUT-09, OUT-11, OUT-12, OUT-13
	tests := map[string]struct {
		ev        events.Event
		causation string
		want      string
	}{
		"WalletBalanceChanged": {
			ev: balanceChanged(t),
			want: `{"eventId":"` + eventID + `","eventType":"WalletBalanceChanged","version":1,` +
				`"aggregateType":"Wallet","aggregateId":"` + walletID + `","correlationId":"corr-1",` +
				`"occurredAt":"2026-09-08T12:00:00.123Z","data":{"walletId":"` + walletID + `",` +
				`"transactionId":"` + txID + `","transactionKind":"BET","direction":"DEBIT",` +
				`"money":{"amount":"25.00","currency":"BRL"},"balanceBefore":{"amount":"1000.00","currency":"BRL"},` +
				`"balanceAfter":{"amount":"975.00","currency":"BRL"},"walletVersion":2}}`,
		},
		"WagerTransactionProcessed external": {
			ev: processedExternal(t), causation: "msg-123",
			want: `{"eventId":"` + eventID + `","eventType":"WagerTransactionProcessed","version":1,` +
				`"aggregateType":"WagerTransaction","aggregateId":"` + txID + `","correlationId":"corr-1",` +
				`"causationId":"msg-123","occurredAt":"2026-09-08T12:00:00.123Z","data":{"transactionId":"` + txID + `",` +
				`"origin":"EXTERNAL","kind":"REFUND","walletId":"` + walletID + `","playerId":"` + playerID + `",` +
				`"providerId":"provider-a","externalTransactionId":"transaction-124","roundId":"round-987",` +
				`"gameId":"fortune-chimp","money":{"amount":"25.00","currency":"BRL"},` +
				`"balanceAfter":{"amount":"1000.00","currency":"BRL"},"walletVersion":3,` +
				`"referenceExternalTransactionId":"transaction-123","referenceTransactionId":"` + refTxID + `",` +
				`"processedAt":"2026-09-08T12:00:00.123Z"}}`,
		},
		"WagerTransactionProcessed opening omits external metadata": {
			ev: processedOpening(t),
			want: `{"eventId":"` + eventID + `","eventType":"WagerTransactionProcessed","version":1,` +
				`"aggregateType":"WagerTransaction","aggregateId":"` + txID + `","correlationId":"corr-1",` +
				`"occurredAt":"2026-09-08T12:00:00.123Z","data":{"transactionId":"` + txID + `",` +
				`"origin":"INTERNAL","kind":"OPENING","walletId":"` + walletID + `","playerId":"` + playerID + `",` +
				`"money":{"amount":"1000.00","currency":"BRL"},"balanceAfter":{"amount":"1000.00","currency":"BRL"},` +
				`"walletVersion":1,"processedAt":"2026-09-08T12:00:00.123Z"}}`,
		},
		"WagerTransactionRejected": {
			ev: rejected(t),
			want: `{"eventId":"` + eventID + `","eventType":"WagerTransactionRejected","version":1,` +
				`"aggregateType":"WagerTransaction","aggregateId":"` + txID + `","correlationId":"corr-1",` +
				`"occurredAt":"2026-09-08T12:00:00.123Z","data":{"transactionId":"` + txID + `","kind":"BET",` +
				`"walletId":"` + walletID + `","playerId":"` + playerID + `","providerId":"provider-a",` +
				`"externalTransactionId":"transaction-123","roundId":"round-987","gameId":"fortune-chimp",` +
				`"money":{"amount":"80.00","currency":"BRL"},"failureCode":"INSUFFICIENT_FUNDS",` +
				`"failureCategory":"DEFINITIVE","balance":{"amount":"20.00","currency":"BRL"},` +
				`"rejectedAt":"2026-09-08T12:00:00.123Z"}}`,
		},
		"WagerTransactionPendingReference": {
			ev: pendingReference(t),
			want: `{"eventId":"` + eventID + `","eventType":"WagerTransactionPendingReference","version":1,` +
				`"aggregateType":"WagerTransaction","aggregateId":"` + txID + `","correlationId":"corr-1",` +
				`"occurredAt":"2026-09-08T12:00:00.123Z","data":{"transactionId":"` + txID + `","kind":"REFUND",` +
				`"walletId":"` + walletID + `","playerId":"` + playerID + `","providerId":"provider-a",` +
				`"externalTransactionId":"transaction-124","roundId":"round-987","gameId":"fortune-chimp",` +
				`"money":{"amount":"25.00","currency":"BRL"},"referenceExternalTransactionId":"transaction-123",` +
				`"nextAttemptAt":"2026-09-08T12:00:01.123Z","expiresAt":"2026-09-08T12:10:00.123Z"}}`,
		},
	}
	for name, tc := range tests {
		env, err := events.Seal(eventID, "corr-1", tc.causation, tc.ev)
		if err != nil {
			t.Fatalf("%s: Seal: %v", name, err)
		}
		got, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("%s: Marshal: %v", name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}
}
