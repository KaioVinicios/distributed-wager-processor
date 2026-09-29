package wagering

import (
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

const (
	maxTextLen           = 128
	maxIdempotencyKeyLen = 255
)

// MoneyInput is the raw money of an Input.
type MoneyInput struct {
	Amount   *string
	Currency *string
}

// Input is the raw operation built by an edge: the HTTP body plus the
// Idempotency-Key header, or the data of a WagerTransactionRequested message. A
// nil field is absent. Money is not parsed while decoding JSON, so that the
// evaluation order of lifecycle §3.1 holds (U04d).
type Input struct {
	IdempotencyKey                 *string
	ProviderID                     *string
	ExternalTransactionID          *string
	PlayerID                       *string
	WalletID                       *string
	RoundID                        *string
	GameID                         *string
	Kind                           *string
	Money                          *MoneyInput
	ReferenceExternalTransactionID *string
}

// Command is a validated external operation, identical for HTTP and SQS. UUIDs
// are lowercase; the payload hash is computed once (D-08).
type Command struct {
	idempotencyKey                 string
	providerID                     string
	externalTransactionID          string
	playerID                       string
	walletID                       string
	roundID                        string
	gameID                         string
	kind                           Kind
	money                          money.Money
	referenceExternalTransactionID string
	canonical                      string
	payloadHash                    string
}

// NewCommand runs the stateless validation of lifecycle §3.1 (steps 2–7) in a
// fixed order and returns the first violation as a *ValidationError.
func NewCommand(in Input) (Command, error) {
	// Step 2: idempotency key.
	if in.IdempotencyKey == nil {
		return Command{}, invalidInput(InputMissingIdempotencyKey, "idempotencyKey")
	}
	if !validIdempotencyKey(*in.IdempotencyKey) {
		return Command{}, invalidInput(InputInvalidIdempotencyKey, "idempotencyKey")
	}

	// Step 3a: required fields are present.
	required := []struct {
		name  string
		value *string
	}{
		{"providerId", in.ProviderID},
		{"externalTransactionId", in.ExternalTransactionID},
		{"playerId", in.PlayerID},
		{"walletId", in.WalletID},
		{"roundId", in.RoundID},
		{"gameId", in.GameID},
		{"kind", in.Kind},
	}
	for _, f := range required {
		if f.value == nil {
			return Command{}, invalidInput(InputMissingField, f.name)
		}
	}
	switch {
	case in.Money == nil:
		return Command{}, invalidInput(InputMissingField, "money")
	case in.Money.Amount == nil:
		return Command{}, invalidInput(InputMissingField, "money.amount")
	case in.Money.Currency == nil:
		return Command{}, invalidInput(InputMissingField, "money.currency")
	}

	// Step 3b: formats, in the same order; the reference comes last.
	playerID, playerErr := ident.Parse(*in.PlayerID)
	walletID, walletErr := ident.Parse(*in.WalletID)
	ref := in.ReferenceExternalTransactionID
	formats := []struct {
		name string
		ok   bool
	}{
		{"providerId", validText(*in.ProviderID)},
		{"externalTransactionId", validText(*in.ExternalTransactionID)},
		{"playerId", playerErr == nil},
		{"walletId", walletErr == nil},
		{"roundId", validText(*in.RoundID)},
		{"gameId", validText(*in.GameID)},
		{"referenceExternalTransactionId", ref == nil || validText(*ref)},
	}
	for _, f := range formats {
		if !f.ok {
			return Command{}, invalidInput(InputInvalidField, f.name)
		}
	}

	// Step 4: kind.
	kind, err := ParseKind(*in.Kind)
	switch {
	case err != nil:
		return Command{}, invalidInput(InputInvalidKind, "kind")
	case kind == KindOpening:
		return Command{}, invalidInput(InputOpeningNotAllowed, "kind")
	}

	// Step 5: money, amount before currency.
	amount, err := money.Parse(*in.Money.Amount, *in.Money.Currency)
	switch {
	case errors.Is(err, money.ErrInvalidAmount):
		return Command{}, invalidInput(InputInvalidAmount, "money.amount")
	case err != nil:
		return Command{}, invalidInput(InputInvalidCurrency, "money.currency")
	}

	// Step 6: zero only in LOSS, and LOSS only with zero (OPS-11).
	switch {
	case kind == KindLoss && amount.Sign() != 0:
		return Command{}, invalidInput(InputLossAmountMustBeZero, "money.amount")
	case kind != KindLoss && amount.Sign() == 0:
		return Command{}, invalidInput(InputZeroAmountNotAllowed, "money.amount")
	}

	// Step 7: reference policy by kind.
	rule := kind.referenceRule()
	switch {
	case rule == referenceRequired && ref == nil:
		return Command{}, invalidInput(InputReferenceRequired, "referenceExternalTransactionId")
	case rule == referenceForbidden && ref != nil:
		return Command{}, invalidInput(InputReferenceNotAllowed, "referenceExternalTransactionId")
	case ref != nil && *ref == *in.ExternalTransactionID:
		return Command{}, invalidInput(InputSelfReference, "referenceExternalTransactionId")
	}

	c := Command{
		idempotencyKey:        *in.IdempotencyKey,
		providerID:            *in.ProviderID,
		externalTransactionID: *in.ExternalTransactionID,
		playerID:              playerID,
		walletID:              walletID,
		roundID:               *in.RoundID,
		gameID:                *in.GameID,
		kind:                  kind,
		money:                 amount,
	}
	if ref != nil {
		c.referenceExternalTransactionID = *ref
	}
	if c.canonical, err = canonicalPayload(c); err != nil {
		return Command{}, err
	}
	c.payloadHash = hashHex(c.canonical)
	return c, nil
}

func (c Command) IdempotencyKey() string        { return c.idempotencyKey }
func (c Command) ProviderID() string            { return c.providerID }
func (c Command) ExternalTransactionID() string { return c.externalTransactionID }
func (c Command) PlayerID() string              { return c.playerID }
func (c Command) WalletID() string              { return c.walletID }
func (c Command) RoundID() string               { return c.roundID }
func (c Command) GameID() string                { return c.gameID }
func (c Command) Kind() Kind                    { return c.kind }
func (c Command) Money() money.Money            { return c.money }

// ReferenceExternalTransactionID returns "" when the operation has no reference.
func (c Command) ReferenceExternalTransactionID() string { return c.referenceExternalTransactionID }

func invalidInput(code InputCode, field string) error {
	return &ValidationError{Code: code, Field: field}
}

// validText accepts 1 to 128 characters of valid UTF-8 without control characters.
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
		n++
	}
	return n >= 1 && n <= maxTextLen
}

// validIdempotencyKey accepts 1 to 255 visible ASCII characters (0x21–0x7E).
func validIdempotencyKey(s string) bool {
	if s == "" || len(s) > maxIdempotencyKeyLen {
		return false
	}
	for _, c := range []byte(s) {
		if c < 0x21 || c > 0x7E {
			return false
		}
	}
	return true
}
