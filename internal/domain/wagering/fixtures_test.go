package wagering_test

import (
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const (
	txID        = "0192f298-345e-7e38-af88-e43f851a819d"
	refTxID     = "0192f297-0000-7000-8000-000000000002"
	entryID     = "0192f299-0000-7000-8000-000000000003"
	refExtID    = "tx-ref"
	correlation = "corr-1"
)

// t0 has nanoseconds and a non-UTC zone on purpose: stored instants must come
// out in UTC, truncated to microseconds.
var t0 = time.Date(2026, 9, 29, 9, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))

func utc(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// external builds a PENDING external operation. ref is the referenced
// externalTransactionId ("" = none).
func external(t *testing.T, kind wagering.Kind, amount, ref string) *wagering.WagerTransaction {
	t.Helper()
	in := betInput()
	in.Kind, in.Money.Amount = ptr(string(kind)), ptr(amount)
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	tx, err := wagering.NewExternal(txID, mustCommand(t, in), wagering.ReceivedViaHTTP, correlation, t0)
	if err != nil {
		t.Fatalf("NewExternal: %v", err)
	}
	return tx
}

// referenceSnapshot is a PROCESSED external operation "tx-ref" of the same
// provider, wallet, player and round as external(). Reversals get their own
// resolved reference, as the constraints require.
func referenceSnapshot(t *testing.T, kind wagering.Kind, amount string) wagering.Snapshot {
	t.Helper()
	s := wagering.Snapshot{
		ID: refTxID, Origin: wagering.OriginExternal, Kind: kind, Status: wagering.StatusProcessed,
		WalletID: walletID, PlayerID: playerID, Money: brl(t, amount),
		ProviderID: provider, ExternalTransactionID: refExtID, IdempotencyKey: "provider-a:tx-ref",
		PayloadHash: strings.Repeat("a", 64), RoundID: "round-987", GameID: "fortune-chimp",
		ReceivedVia: wagering.ReceivedViaHTTP, ResultBalance: brl(t, "100.00"), CorrelationID: "corr-ref",
		CreatedAt: utc(t0), UpdatedAt: utc(t0), CompletedAt: utc(t0),
	}
	if kind == wagering.KindRefund || kind == wagering.KindRollback {
		s.ReferenceExternalTransactionID, s.ReferenceTransactionID = "tx-older", otherID
	}
	return s
}

func rehydrate(t *testing.T, s wagering.Snapshot) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.Rehydrate(s)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	return tx
}
