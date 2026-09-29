package wagering_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func TestReferenceResolution(t *testing.T) {
	// Covers: TST-U04, OPS-06, OPS-07, OPS-08, OPS-09, OPS-14
	usd := func(s *wagering.Snapshot) {
		m, err := money.Parse(s.Money.String(), "USD")
		if err != nil {
			t.Fatal(err)
		}
		s.Money = m
	}
	tests := []struct {
		name     string
		kind     wagering.Kind
		amount   string
		refKind  wagering.Kind // "" = reference not found
		mutate   func(s *wagering.Snapshot)
		reversed bool
		want     wagering.Status
		code     wagering.FailureCode
	}{
		{"R1 not found", wagering.KindRefund, "25.00", "", nil, false, wagering.StatusPendingReference, ""},
		{"R1 WIN also waits", wagering.KindWin, "40.00", "", nil, false, wagering.StatusPendingReference, ""},
		{"R2 reference still pending", wagering.KindRollback, "25.00", wagering.KindRefund, func(s *wagering.Snapshot) {
			s.Status, s.ReferenceTransactionID, s.ResultBalance, s.CompletedAt = wagering.StatusPendingReference, "", money.Money{}, time.Time{}
			s.NextAttemptAt, s.ExpiresAt = s.CreatedAt, s.CreatedAt.Add(time.Minute)
		}, false, wagering.StatusPendingReference, ""},
		{"R3 reference rejected", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) {
			s.Status, s.FailureCode = wagering.StatusRejected, wagering.FailureInsufficientFunds
		}, false, wagering.StatusRejected, wagering.FailureReferenceNotProcessed},
		{"R3 reference failed", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) {
			s.Status, s.FailureCode, s.ResultBalance = wagering.StatusFailed, wagering.FailureInternalPermanentFailure, money.Money{}
		}, false, wagering.StatusRejected, wagering.FailureReferenceNotProcessed},
		{"R3 before R4", wagering.KindRefund, "25.00", wagering.KindWin, func(s *wagering.Snapshot) {
			s.Status, s.FailureCode = wagering.StatusRejected, wagering.FailurePlayerWalletMismatch
		}, false, wagering.StatusRejected, wagering.FailureReferenceNotProcessed},
		{"R4 REFUND of a WIN", wagering.KindRefund, "25.00", wagering.KindWin, nil, false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind},
		{"R4 REFUND of a REFUND", wagering.KindRefund, "25.00", wagering.KindRefund, nil, false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind},
		{"R4 ROLLBACK of a ROLLBACK", wagering.KindRollback, "25.00", wagering.KindRollback, nil, false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind},
		{
			"R4 ROLLBACK of a LOSS", wagering.KindRollback, "25.00", wagering.KindLoss, func(s *wagering.Snapshot) { s.Money = brl(t, "0.00") },
			false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind,
		},
		{"R4 WIN of a WIN", wagering.KindWin, "40.00", wagering.KindWin, nil, false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind},
		{
			"R4 before R5", wagering.KindRefund, "25.00", wagering.KindWin, func(s *wagering.Snapshot) { s.RoundID = "round-000" },
			false, wagering.StatusRejected, wagering.FailureInvalidReferenceKind,
		},
		{
			"R5 other player", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) { s.PlayerID = otherID },
			false, wagering.StatusRejected, wagering.FailureReferenceMismatch,
		},
		{
			"R5 other wallet", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) { s.WalletID = otherID },
			false, wagering.StatusRejected, wagering.FailureReferenceMismatch,
		},
		{
			"R5 other round", wagering.KindWin, "40.00", wagering.KindBet, func(s *wagering.Snapshot) { s.RoundID = "round-000" },
			false, wagering.StatusRejected, wagering.FailureReferenceMismatch,
		},
		{"R5 other currency", wagering.KindRefund, "25.00", wagering.KindBet, usd, false, wagering.StatusRejected, wagering.FailureReferenceMismatch},
		{"R5 before R6", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) {
			s.RoundID, s.Money = "round-000", brl(t, "30.00")
		}, false, wagering.StatusRejected, wagering.FailureReferenceMismatch},
		{
			"R6 REFUND with another amount", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) { s.Money = brl(t, "30.00") },
			false, wagering.StatusRejected, wagering.FailureReversalAmountMismatch,
		},
		{
			"R6 ROLLBACK with another amount", wagering.KindRollback, "25.00", wagering.KindWin, func(s *wagering.Snapshot) { s.Money = brl(t, "30.00") },
			false, wagering.StatusRejected, wagering.FailureReversalAmountMismatch,
		},
		{
			"R6 before R7", wagering.KindRefund, "25.00", wagering.KindBet, func(s *wagering.Snapshot) { s.Money = brl(t, "30.00") },
			true, wagering.StatusRejected, wagering.FailureReversalAmountMismatch,
		},
		{"R7 BET already refunded", wagering.KindRefund, "25.00", wagering.KindBet, nil, true, wagering.StatusRejected, wagering.FailureAlreadyReversed},
		{"R7 ROLLBACK after a REFUND of the BET", wagering.KindRollback, "25.00", wagering.KindBet, nil, true, wagering.StatusRejected, wagering.FailureAlreadyReversed},
		{"R7 does not apply to WIN", wagering.KindWin, "40.00", wagering.KindBet, nil, true, wagering.StatusProcessed, ""},
		{"R8 REFUND", wagering.KindRefund, "25.00", wagering.KindBet, nil, false, wagering.StatusProcessed, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := wagering.Reference{AlreadyReversed: tc.reversed}
			if tc.refKind != "" {
				amount := tc.amount
				if tc.refKind == wagering.KindLoss {
					amount = "0.00"
				}
				s := referenceSnapshot(t, tc.refKind, amount)
				if tc.mutate != nil {
					tc.mutate(&s)
				}
				ref.Tx = rehydrate(t, s)
			}
			tx := external(t, tc.kind, tc.amount, refExtID)
			w := openWallet(t, "100.00")
			if _, err := settle(t, tx, &w, ref); err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if tx.Status() != tc.want || tx.FailureCode() != tc.code {
				t.Fatalf("got %s %s, want %s %s", tx.Status(), tx.FailureCode(), tc.want, tc.code)
			}
			if tx.Status() != wagering.StatusProcessed && (w.Version() != 1 || w.Balance() != brl(t, "100.00")) {
				t.Fatalf("the wallet moved: %v v%d", w.Balance(), w.Version())
			}
		})
	}
}
