package wagering_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// validationError returns the code and field of err, failing if it is not a
// *ValidationError.
func validationError(t *testing.T, err error) (wagering.InputCode, string) {
	t.Helper()
	var ve *wagering.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	return ve.Code, ve.Field
}

func TestNewCommand(t *testing.T) {
	// Covers: TX-01, TX-02, OPS-06, OPS-15, IDEM-02
	in := betInput()
	in.PlayerID = ptr(strings.ToUpper(playerID))
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand: %v", err)
	}
	if cmd.IdempotencyKey() != "provider-a:transaction-123" || cmd.ProviderID() != provider ||
		cmd.ExternalTransactionID() != "transaction-123" || cmd.PlayerID() != playerID ||
		cmd.WalletID() != walletID || cmd.RoundID() != "round-987" || cmd.GameID() != "fortune-chimp" ||
		cmd.Kind() != wagering.KindBet || cmd.Money() != brl(t, "25.00") || cmd.ReferenceExternalTransactionID() != "" {
		t.Fatalf("command getters: %+v", cmd)
	}

	long := func(n int, s string) *string { return ptr(strings.Repeat(s, n)) }
	tests := []struct {
		name   string
		mutate func(in *wagering.Input)
		code   wagering.InputCode
		field  string
	}{
		{"missing key", func(in *wagering.Input) { in.IdempotencyKey = nil }, wagering.InputMissingIdempotencyKey, "idempotencyKey"},
		{"empty key", func(in *wagering.Input) { in.IdempotencyKey = ptr("") }, wagering.InputInvalidIdempotencyKey, "idempotencyKey"},
		{"key with space", func(in *wagering.Input) { in.IdempotencyKey = ptr("a b") }, wagering.InputInvalidIdempotencyKey, "idempotencyKey"},
		{"non ASCII key", func(in *wagering.Input) { in.IdempotencyKey = ptr("chave-é") }, wagering.InputInvalidIdempotencyKey, "idempotencyKey"},
		{"key with 256 chars", func(in *wagering.Input) { in.IdempotencyKey = long(256, "k") }, wagering.InputInvalidIdempotencyKey, "idempotencyKey"},
		{"missing provider", func(in *wagering.Input) { in.ProviderID = nil }, wagering.InputMissingField, "providerId"},
		{"missing external id", func(in *wagering.Input) { in.ExternalTransactionID = nil }, wagering.InputMissingField, "externalTransactionId"},
		{"missing player", func(in *wagering.Input) { in.PlayerID = nil }, wagering.InputMissingField, "playerId"},
		{"missing wallet", func(in *wagering.Input) { in.WalletID = nil }, wagering.InputMissingField, "walletId"},
		{"missing round", func(in *wagering.Input) { in.RoundID = nil }, wagering.InputMissingField, "roundId"},
		{"missing game", func(in *wagering.Input) { in.GameID = nil }, wagering.InputMissingField, "gameId"},
		{"missing kind", func(in *wagering.Input) { in.Kind = nil }, wagering.InputMissingField, "kind"},
		{"missing money", func(in *wagering.Input) { in.Money = nil }, wagering.InputMissingField, "money"},
		{"missing amount", func(in *wagering.Input) { in.Money.Amount = nil }, wagering.InputMissingField, "money.amount"},
		{"missing currency", func(in *wagering.Input) { in.Money.Currency = nil }, wagering.InputMissingField, "money.currency"},
		{"empty provider", func(in *wagering.Input) { in.ProviderID = ptr("") }, wagering.InputInvalidField, "providerId"},
		{"provider with 129 chars", func(in *wagering.Input) { in.ProviderID = long(129, "é") }, wagering.InputInvalidField, "providerId"},
		{"control char in external id", func(in *wagering.Input) { in.ExternalTransactionID = ptr("tx\n1") }, wagering.InputInvalidField, "externalTransactionId"},
		{"invalid UTF-8 round", func(in *wagering.Input) { in.RoundID = ptr("\xff") }, wagering.InputInvalidField, "roundId"},
		{"player not a UUID", func(in *wagering.Input) { in.PlayerID = ptr("player-1") }, wagering.InputInvalidField, "playerId"},
		{"wallet in braces", func(in *wagering.Input) { in.WalletID = ptr("{" + walletID + "}") }, wagering.InputInvalidField, "walletId"},
		{"empty game", func(in *wagering.Input) { in.GameID = ptr("") }, wagering.InputInvalidField, "gameId"},
		{"empty reference", func(in *wagering.Input) { in.ReferenceExternalTransactionID = ptr("") }, wagering.InputInvalidField, "referenceExternalTransactionId"},
		{"unknown kind", func(in *wagering.Input) { in.Kind = ptr("JACKPOT") }, wagering.InputInvalidKind, "kind"},
		{"lowercase kind", func(in *wagering.Input) { in.Kind = ptr("bet") }, wagering.InputInvalidKind, "kind"},
		{"opening", func(in *wagering.Input) { in.Kind = ptr("OPENING") }, wagering.InputOpeningNotAllowed, "kind"},
		{"invalid amount", func(in *wagering.Input) { in.Money.Amount = ptr("25") }, wagering.InputInvalidAmount, "money.amount"},
		{"negative amount", func(in *wagering.Input) { in.Money.Amount = ptr("-25.00") }, wagering.InputInvalidAmount, "money.amount"},
		{"unsupported currency", func(in *wagering.Input) { in.Money.Currency = ptr("JPY") }, wagering.InputInvalidCurrency, "money.currency"},
		{"refund without reference", func(in *wagering.Input) { in.Kind = ptr("REFUND") }, wagering.InputReferenceRequired, "referenceExternalTransactionId"},
		{"rollback without reference", func(in *wagering.Input) { in.Kind = ptr("ROLLBACK") }, wagering.InputReferenceRequired, "referenceExternalTransactionId"},
		{"bet with reference", func(in *wagering.Input) { in.ReferenceExternalTransactionID = ptr("tx-0") }, wagering.InputReferenceNotAllowed, "referenceExternalTransactionId"},
		{"loss with reference", func(in *wagering.Input) {
			in.Kind, in.Money.Amount, in.ReferenceExternalTransactionID = ptr("LOSS"), ptr("0.00"), ptr("tx-0")
		}, wagering.InputReferenceNotAllowed, "referenceExternalTransactionId"},
		{"self reference", func(in *wagering.Input) {
			in.Kind, in.ReferenceExternalTransactionID = ptr("REFUND"), ptr("transaction-123")
		}, wagering.InputSelfReference, "referenceExternalTransactionId"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := betInput()
			tc.mutate(&in)
			_, err := wagering.NewCommand(in)
			code, field := validationError(t, err)
			if code != tc.code || field != tc.field {
				t.Fatalf("got %s (%s), want %s (%s)", code, field, tc.code, tc.field)
			}
		})
	}

	accepted := map[string]func(in *wagering.Input){
		"key with 255 visible chars": func(in *wagering.Input) { in.IdempotencyKey = long(255, "~") },
		"provider with 128 runes":    func(in *wagering.Input) { in.ProviderID = long(128, "é") },
		"win without reference":      func(in *wagering.Input) { in.Kind = ptr("WIN") },
		"win with reference":         func(in *wagering.Input) { in.Kind, in.ReferenceExternalTransactionID = ptr("WIN"), ptr("tx-0") },
		"rollback with reference": func(in *wagering.Input) {
			in.Kind, in.ReferenceExternalTransactionID = ptr("ROLLBACK"), ptr("tx-0")
		},
	}
	for name, mutate := range accepted {
		in := betInput()
		mutate(&in)
		if _, err := wagering.NewCommand(in); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestZeroAmountPolicy(t *testing.T) {
	// Covers: TST-U04, OPS-03, OPS-11
	for _, kind := range []string{"BET", "WIN", "REFUND", "ROLLBACK"} {
		in := betInput()
		in.Kind, in.Money.Amount, in.ReferenceExternalTransactionID = ptr(kind), ptr("0.00"), ptr("tx-0")
		if kind == "BET" {
			in.ReferenceExternalTransactionID = nil
		}
		_, err := wagering.NewCommand(in)
		if code, _ := validationError(t, err); code != wagering.InputZeroAmountNotAllowed {
			t.Errorf("%s 0.00: got %s, want ZERO_AMOUNT_NOT_ALLOWED", kind, code)
		}
	}

	loss := betInput()
	loss.Kind, loss.Money.Amount = ptr("LOSS"), ptr("0.00")
	if cmd, err := wagering.NewCommand(loss); err != nil || cmd.Money().Sign() != 0 {
		t.Fatalf("LOSS 0.00: %v", err)
	}
	loss.Money.Amount = ptr("0.01")
	_, err := wagering.NewCommand(loss)
	if code, _ := validationError(t, err); code != wagering.InputLossAmountMustBeZero {
		t.Errorf("LOSS 0.01: got %s, want LOSS_AMOUNT_MUST_BE_ZERO", code)
	}
}

func TestEvaluationOrder(t *testing.T) {
	// Covers: TST-U04, OPS-15
	tests := []struct {
		name   string
		mutate func(in *wagering.Input)
		code   wagering.InputCode
		field  string
	}{
		{
			"key before fields", func(in *wagering.Input) { in.IdempotencyKey, in.ProviderID = nil, nil },
			wagering.InputMissingIdempotencyKey, "idempotencyKey",
		},
		{
			"invalid key before missing field", func(in *wagering.Input) { in.IdempotencyKey, in.Money = ptr(" "), nil },
			wagering.InputInvalidIdempotencyKey, "idempotencyKey",
		},
		{
			"presence before format", func(in *wagering.Input) { in.PlayerID, in.Kind = ptr("x"), nil },
			wagering.InputMissingField, "kind",
		},
		{
			"fields follow the contract order", func(in *wagering.Input) { in.GameID, in.ProviderID = nil, nil },
			wagering.InputMissingField, "providerId",
		},
		{
			"format before kind", func(in *wagering.Input) { in.PlayerID, in.Kind = ptr("x"), ptr("JACKPOT") },
			wagering.InputInvalidField, "playerId",
		},
		{"reference format before kind", func(in *wagering.Input) {
			in.ReferenceExternalTransactionID, in.Kind = ptr(""), ptr("OPENING")
		}, wagering.InputInvalidField, "referenceExternalTransactionId"},
		{
			"kind before money", func(in *wagering.Input) { in.Kind, in.Money.Amount = ptr("OPENING"), ptr("x") },
			wagering.InputOpeningNotAllowed, "kind",
		},
		{
			"amount before currency", func(in *wagering.Input) { in.Money.Amount, in.Money.Currency = ptr("x"), ptr("x") },
			wagering.InputInvalidAmount, "money.amount",
		},
		{
			"currency before zero policy", func(in *wagering.Input) { in.Money.Amount, in.Money.Currency = ptr("0.00"), ptr("x") },
			wagering.InputInvalidCurrency, "money.currency",
		},
		{"zero policy before reference", func(in *wagering.Input) {
			in.Money.Amount, in.ReferenceExternalTransactionID = ptr("0.00"), ptr("tx-0")
		}, wagering.InputZeroAmountNotAllowed, "money.amount"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := betInput()
			tc.mutate(&in)
			_, err := wagering.NewCommand(in)
			code, field := validationError(t, err)
			if code != tc.code || field != tc.field {
				t.Fatalf("got %s (%s), want %s (%s)", code, field, tc.code, tc.field)
			}
		})
	}
}
