package wagering_test

import (
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const (
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	otherID  = "0192f2aa-0000-7000-8000-000000000001"
	provider = "provider-a"
)

func ptr(s string) *string { return &s }

// betInput is the BET of the challenge (CHALLENGE §9).
func betInput() wagering.Input {
	return wagering.Input{
		IdempotencyKey:        ptr("provider-a:transaction-123"),
		ProviderID:            ptr(provider),
		ExternalTransactionID: ptr("transaction-123"),
		PlayerID:              ptr(playerID),
		WalletID:              ptr(walletID),
		RoundID:               ptr("round-987"),
		GameID:                ptr("fortune-chimp"),
		Kind:                  ptr("BET"),
		Money:                 &wagering.MoneyInput{Amount: ptr("25.00"), Currency: ptr("BRL")},
	}
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("Parse(%q): %v", amount, err)
	}
	return m
}
