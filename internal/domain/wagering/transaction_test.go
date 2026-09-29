package wagering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func TestNewExternal(t *testing.T) {
	// Covers: TST-U03, TX-02, TX-06, DOM-01
	tx := external(t, wagering.KindBet, "25.00", "")
	if tx.ID() != txID || tx.Origin() != wagering.OriginExternal || tx.Kind() != wagering.KindBet ||
		tx.Status() != wagering.StatusPending || tx.WalletID() != walletID || tx.PlayerID() != playerID ||
		tx.Money() != brl(t, "25.00") || tx.ProviderID() != provider || tx.ExternalTransactionID() != "transaction-123" ||
		tx.IdempotencyKey() != "provider-a:transaction-123" || tx.RoundID() != "round-987" ||
		tx.GameID() != "fortune-chimp" || tx.ReceivedVia() != wagering.ReceivedViaHTTP ||
		tx.CorrelationID() != correlation || tx.PayloadHash() != mustCommand(t, betInput()).PayloadHash() {
		t.Fatalf("NewExternal = %+v", tx)
	}
	if !tx.CreatedAt().Equal(utc(t0)) || tx.CreatedAt().Location() != time.UTC || !tx.UpdatedAt().Equal(utc(t0)) ||
		!tx.CompletedAt().IsZero() || tx.ResultBalance() != (money.Money{}) || tx.ReferenceTransactionID() != "" {
		t.Fatalf("NewExternal times/result: %+v", tx)
	}
	sqs, err := wagering.NewExternal(txID, mustCommand(t, betInput()), wagering.ReceivedViaSQS, correlation, t0)
	if err != nil || sqs.PayloadHash() != tx.PayloadHash() {
		t.Fatalf("the channel must not change the hash: %v", err)
	}

	cmd := mustCommand(t, betInput())
	if _, err := wagering.NewExternal(txID, wagering.Command{}, wagering.ReceivedViaHTTP, correlation, t0); !errors.Is(err, wagering.ErrUninitialized) {
		t.Errorf("Command{}: error = %v, want ErrUninitialized", err)
	}
	invalid := map[string]func() error{
		"id": func() error {
			_, err := wagering.NewExternal("tx-1", cmd, wagering.ReceivedViaHTTP, correlation, t0)
			return err
		},
		"channel":     func() error { _, err := wagering.NewExternal(txID, cmd, "GRPC", correlation, t0); return err },
		"correlation": func() error { _, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, "", t0); return err },
		"now": func() error {
			_, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, correlation, time.Time{})
			return err
		},
	}
	for name, op := range invalid {
		if err := op(); !errors.Is(err, wagering.ErrInvalidArgument) {
			t.Errorf("invalid %s: error = %v, want ErrInvalidArgument", name, err)
		}
	}
}

func TestNewOpening(t *testing.T) {
	// Covers: TST-U06, TX-04, TX-06
	tx, err := wagering.NewOpening(txID, walletID, playerID, brl(t, "1000.00"), correlation, t0)
	if err != nil {
		t.Fatalf("NewOpening: %v", err)
	}
	if tx.Origin() != wagering.OriginInternal || tx.Kind() != wagering.KindOpening || tx.Status() != wagering.StatusPending ||
		tx.ProviderID() != "" || tx.ExternalTransactionID() != "" || tx.IdempotencyKey() != "" || tx.PayloadHash() != "" ||
		tx.RoundID() != "" || tx.GameID() != "" || tx.ReceivedVia() != "" {
		t.Fatalf("NewOpening = %+v", tx)
	}
	for name, amount := range map[string]money.Money{"zero": brl(t, "0.00"), "uninitialized": {}} {
		if _, err := wagering.NewOpening(txID, walletID, playerID, amount, correlation, t0); !errors.Is(err, wagering.ErrInvalidArgument) {
			t.Errorf("%s amount: error = %v, want ErrInvalidArgument", name, err)
		}
	}
	if _, err := wagering.NewOpening(txID, "w-1", playerID, brl(t, "1.00"), correlation, t0); !errors.Is(err, wagering.ErrInvalidArgument) {
		t.Errorf("invalid wallet id: error = %v", err)
	}
}

func TestRehydrate(t *testing.T) {
	// Covers: TST-U03, DOM-02, TX-02, TX-03, TX-05, TX-09
	pendingSnapshot, err := external(t, wagering.KindBet, "25.00", "").Snapshot()
	if !errors.Is(err, wagering.ErrNotPersistable) || pendingSnapshot != (wagering.Snapshot{}) {
		t.Fatalf("Snapshot of PENDING: error = %v, want ErrNotPersistable", err)
	}

	valid := map[string]func() wagering.Snapshot{
		"processed BET": func() wagering.Snapshot { return referenceSnapshot(t, wagering.KindBet, "25.00") },
		"processed REFUND": func() wagering.Snapshot {
			return referenceSnapshot(t, wagering.KindRefund, "25.00")
		},
		"processed LOSS": func() wagering.Snapshot { return referenceSnapshot(t, wagering.KindLoss, "0.00") },
		"rejected": func() wagering.Snapshot {
			s := referenceSnapshot(t, wagering.KindBet, "80.00")
			s.Status, s.FailureCode = wagering.StatusRejected, wagering.FailureInsufficientFunds
			return s
		},
		"failed": func() wagering.Snapshot {
			s := referenceSnapshot(t, wagering.KindBet, "80.00")
			s.Status, s.FailureCode, s.ResultBalance = wagering.StatusFailed, wagering.FailureInternalPermanentFailure, money.Money{}
			return s
		},
		"pending reference": func() wagering.Snapshot {
			s := referenceSnapshot(t, wagering.KindRefund, "25.00")
			s.Status, s.ReferenceTransactionID, s.ResultBalance, s.CompletedAt = wagering.StatusPendingReference, "", money.Money{}, time.Time{}
			s.Attempts, s.NextAttemptAt, s.ExpiresAt = 3, utc(t0).Add(time.Second), utc(t0).Add(time.Minute)
			return s
		},
		"opening": func() wagering.Snapshot {
			return wagering.Snapshot{
				ID: txID, Origin: wagering.OriginInternal, Kind: wagering.KindOpening, Status: wagering.StatusProcessed,
				WalletID: walletID, PlayerID: playerID, Money: brl(t, "1000.00"), ResultBalance: brl(t, "1000.00"),
				CorrelationID: correlation, CreatedAt: utc(t0), UpdatedAt: utc(t0), CompletedAt: utc(t0),
			}
		},
	}
	for name, build := range valid {
		s := build()
		tx, err := wagering.Rehydrate(s)
		if err != nil {
			t.Errorf("%s: Rehydrate: %v", name, err)
			continue
		}
		got, err := tx.Snapshot()
		if err != nil || got != s {
			t.Errorf("%s: round trip changed the state:\n got %+v\nwant %+v", name, got, s)
		}
	}

	corrupt := map[string]func(s *wagering.Snapshot){
		"PENDING":                    func(s *wagering.Snapshot) { s.Status = wagering.StatusPending },
		"unknown kind":               func(s *wagering.Snapshot) { s.Kind = "JACKPOT" },
		"unknown status":             func(s *wagering.Snapshot) { s.Status = "" },
		"invalid wallet id":          func(s *wagering.Snapshot) { s.WalletID = "w" },
		"uninitialized money":        func(s *wagering.Snapshot) { s.Money = money.Money{} },
		"OPENING as EXTERNAL":        func(s *wagering.Snapshot) { s.Kind = wagering.KindOpening },
		"INTERNAL BET":               func(s *wagering.Snapshot) { s.Origin = wagering.OriginInternal },
		"EXTERNAL without round":     func(s *wagering.Snapshot) { s.RoundID = "" },
		"EXTERNAL with a bad hash":   func(s *wagering.Snapshot) { s.PayloadHash = "ABC" },
		"EXTERNAL without channel":   func(s *wagering.Snapshot) { s.ReceivedVia = "" },
		"BET with zero":              func(s *wagering.Snapshot) { s.Money = brl(t, "0.00") },
		"LOSS with amount":           func(s *wagering.Snapshot) { s.Kind = wagering.KindLoss },
		"BET with reference":         func(s *wagering.Snapshot) { s.ReferenceExternalTransactionID = "tx-0" },
		"REFUND without reference":   func(s *wagering.Snapshot) { s.Kind = wagering.KindRefund },
		"PROCESSED WIN not resolved": func(s *wagering.Snapshot) { s.Kind, s.ReferenceExternalTransactionID = wagering.KindWin, "tx-0" },
		"REJECTED without code":      func(s *wagering.Snapshot) { s.Status = wagering.StatusRejected },
		"PROCESSED with code":        func(s *wagering.Snapshot) { s.FailureCode = wagering.FailureInsufficientFunds },
		"REJECTED as permanent failure": func(s *wagering.Snapshot) {
			s.Status, s.FailureCode = wagering.StatusRejected, wagering.FailureInternalPermanentFailure
		},
		"FAILED with a rejection code": func(s *wagering.Snapshot) {
			s.Status, s.FailureCode = wagering.StatusFailed, wagering.FailureInsufficientFunds
		},
		"PROCESSED without balance":     func(s *wagering.Snapshot) { s.ResultBalance = money.Money{} },
		"terminal without completed at": func(s *wagering.Snapshot) { s.CompletedAt = time.Time{} },
		"PENDING_REFERENCE without schedule": func(s *wagering.Snapshot) {
			s.Kind, s.ReferenceExternalTransactionID, s.Status, s.CompletedAt = wagering.KindWin, "tx-0", wagering.StatusPendingReference, time.Time{}
		},
		"BET waiting for a reference": func(s *wagering.Snapshot) {
			s.Status, s.CompletedAt = wagering.StatusPendingReference, time.Time{}
			s.NextAttemptAt, s.ExpiresAt = s.CreatedAt, s.CreatedAt
		},
		"negative attempts":      func(s *wagering.Snapshot) { s.Attempts = -1 },
		"empty correlation":      func(s *wagering.Snapshot) { s.CorrelationID = "" },
		"updated before created": func(s *wagering.Snapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) },
	}
	for name, mutate := range corrupt {
		s := referenceSnapshot(t, wagering.KindBet, "25.00")
		mutate(&s)
		if _, err := wagering.Rehydrate(s); !errors.Is(err, wagering.ErrInvalidSnapshot) {
			t.Errorf("%s: error = %v, want ErrInvalidSnapshot", name, err)
		}
	}
}
