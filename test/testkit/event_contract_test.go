package testkit_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/test/testkit"
)

const (
	evWallet = "0192f291-27dd-7d3f-8071-5f8685deef37"
	evPlayer = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	evTx     = "0192f298-345e-7e38-af88-e43f851a819d"
	evRefTx  = "0192f297-0000-7000-8000-000000000002"
)

var evAt = time.Date(2026, 9, 29, 12, 0, 0, 123987654, time.UTC)

func evMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// sealed builds an event with its constructor and returns the envelope JSON,
// sealed as the app does: a UUIDv7 event id.
func sealed(t *testing.T, causation string, build func() (events.Event, error)) []byte {
	t.Helper()
	e, err := build()
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.Seal(uuid.Must(uuid.NewV7()).String(), "corr-1", causation, e)
	if err != nil {
		t.Fatal(err)
	}
	body, err := env.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// mutate decodes body, applies fn to the envelope and encodes it again.
func mutate(t *testing.T, body []byte, fn func(env, data map[string]any)) []byte {
	t.Helper()
	var env map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatal("envelope without data")
	}
	fn(env, data)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Covers: OUT-08, OUT-09, OUT-11, OUT-12, OUT-13
//
// Every envelope the domain seals passes, and the contract catches drift.
func TestEventContract(t *testing.T) {
	c, err := testkit.LoadEventContract(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changed := sealed(t, "", func() (events.Event, error) {
		return events.NewWalletBalanceChanged(events.WalletBalanceChanged{
			WalletID: evWallet, TransactionID: evTx, TransactionKind: "BET", Direction: "DEBIT",
			Money: evMoney(t, "25.00"), BalanceBefore: evMoney(t, "1000.00"), BalanceAfter: evMoney(t, "975.00"),
			WalletVersion: 2, ChangedAt: events.NewTime(evAt),
		})
	})
	opening := sealed(t, "", func() (events.Event, error) {
		return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
			TransactionID: evTx, Origin: "INTERNAL", Kind: "OPENING", WalletID: evWallet, PlayerID: evPlayer,
			Money: evMoney(t, "1000.00"), BalanceAfter: evMoney(t, "1000.00"), WalletVersion: 1,
			ProcessedAt: events.NewTime(evAt),
		})
	})
	valid := map[string][]byte{
		"balance changed": changed,
		"opening":         opening,
		"refund with references, from SQS": sealed(t, "msg-1", func() (events.Event, error) {
			return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
				TransactionID: evTx, Origin: "EXTERNAL", Kind: "REFUND", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-124", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "25.00"), BalanceAfter: evMoney(t, "1000.00"), WalletVersion: 3,
				ReferenceExternalTransactionID: "tx-123", ReferenceTransactionID: evRefTx, ProcessedAt: events.NewTime(evAt),
			})
		}),
		"loss": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
				TransactionID: evTx, Origin: "EXTERNAL", Kind: "LOSS", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-125", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "0.00"), BalanceAfter: evMoney(t, "975.00"), WalletVersion: 2, ProcessedAt: events.NewTime(evAt),
			})
		}),
		"rejected": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionRejected(events.WagerTransactionRejected{
				TransactionID: evTx, Kind: "BET", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-126", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "80.00"), FailureCode: "INSUFFICIENT_FUNDS", FailureCategory: "DEFINITIVE",
				Balance: evMoney(t, "20.00"), RejectedAt: events.NewTime(evAt),
			})
		}),
		"pending reference": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionPendingReference(events.WagerTransactionPendingReference{
				TransactionID: evTx, Kind: "REFUND", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-127", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "25.00"), ReferenceExternalTransactionID: "tx-123",
				NextAttemptAt: events.NewTime(evAt.Add(time.Second)), ExpiresAt: events.NewTime(evAt.Add(10 * time.Minute)),
				PendingAt: events.NewTime(evAt),
			})
		}),
	}
	for name, body := range valid {
		if err := c.Validate(body); err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
	}

	invalid := map[string][]byte{
		"missing field":          mutate(t, changed, func(_, d map[string]any) { delete(d, "walletVersion") }),
		"null optional field":    mutate(t, changed, func(e, _ map[string]any) { e["causationId"] = nil }),
		"field outside data":     mutate(t, changed, func(_, d map[string]any) { d["note"] = "x" }),
		"field outside envelope": mutate(t, changed, func(e, _ map[string]any) { e["messageGroupId"] = evWallet }),
		"amount as a number": mutate(t, changed, func(_, d map[string]any) {
			d["money"] = map[string]any{"amount": json.Number("25.00"), "currency": "BRL"}
		}),
		"timestamp without milliseconds": mutate(t, changed, func(e, _ map[string]any) { e["occurredAt"] = "2026-09-29T12:00:00Z" }),
		"aggregate of another type":      mutate(t, changed, func(e, _ map[string]any) { e["aggregateType"] = "WagerTransaction" }),
		"unknown version":                mutate(t, changed, func(e, _ map[string]any) { e["version"] = json.Number("2") }),
		"external metadata on opening":   mutate(t, opening, func(_, d map[string]any) { d["providerId"] = "provider-a" }),
		"not json":                       []byte(`{"eventId":`),
	}
	for name, body := range invalid {
		if err := c.Validate(body); err == nil {
			t.Errorf("%s: Validate = nil, want an error", name)
		}
	}
}
