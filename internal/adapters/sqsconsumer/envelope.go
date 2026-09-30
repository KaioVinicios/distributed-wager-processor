package sqsconsumer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// messageType is the only type the wager queue carries (messaging.md §3.1).
const messageType = "WagerTransactionRequested"

// Codes of the SQS-only rejections (lifecycle §5.4).
const (
	codeMalformedMessage       = "MALFORMED_MESSAGE"
	codeUnsupportedMessageType = "UNSUPPORTED_MESSAGE_TYPE"
)

// maxMessageIDLen bounds the messageId, in characters.
const maxMessageIDLen = 128

// correlationPattern is what a producer may send as the correlationId
// attribute: the rule of the HTTP X-Correlation-Id (D-18).
var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// envelope is the outer object; data is decoded once the type is known
// (spec M5, decision 14).
type envelope struct {
	MessageID  *string         `json:"messageId"`
	Type       *string         `json:"type"`
	OccurredAt *string         `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

// wagerData is the data of a WagerTransactionRequested. The fields are
// declared in the lexicographic order of their JSON names and omitted when
// nil, so that encoding it is the canonical JSON of the message hash, with an
// absent field and a null one alike (spec M5, decision 4).
type wagerData struct {
	ExternalTransactionID          *string    `json:"externalTransactionId,omitempty"`
	GameID                         *string    `json:"gameId,omitempty"`
	IdempotencyKey                 *string    `json:"idempotencyKey,omitempty"`
	Kind                           *string    `json:"kind,omitempty"`
	Money                          *moneyData `json:"money,omitempty"`
	PlayerID                       *string    `json:"playerId,omitempty"`
	ProviderID                     *string    `json:"providerId,omitempty"`
	ReferenceExternalTransactionID *string    `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        *string    `json:"roundId,omitempty"`
	WalletID                       *string    `json:"walletId,omitempty"`
}

type moneyData struct {
	Amount   *string `json:"amount,omitempty"`
	Currency *string `json:"currency,omitempty"`
}

// parseEnvelope decodes a message body into the input of app.ConsumeWager.
// A body that is not a well-formed WagerTransactionRequested is KindInput with
// MALFORMED_MESSAGE or UNSUPPORTED_MESSAGE_TYPE; the business fields are left
// to the stateless validation of the use case.
func parseEnvelope(body, correlationAttr string, receivedAt time.Time) (app.WagerMessage, error) {
	var env envelope
	if err := decodeStrict([]byte(body), &env); err != nil {
		return app.WagerMessage{}, malformed(err)
	}
	switch {
	case env.MessageID == nil || *env.MessageID == "" || utf8.RuneCountInString(*env.MessageID) > maxMessageIDLen ||
		!utf8.ValidString(*env.MessageID):
		return app.WagerMessage{}, malformed(errors.New("messageId must have 1 to 128 characters"))
	case env.Type == nil:
		return app.WagerMessage{}, malformed(errors.New("type is required"))
	case env.OccurredAt == nil:
		return app.WagerMessage{}, malformed(errors.New("occurredAt is required"))
	}
	if _, err := time.Parse(time.RFC3339Nano, *env.OccurredAt); err != nil {
		return app.WagerMessage{}, malformed(errors.New("occurredAt must be RFC 3339"))
	}
	if *env.Type != messageType {
		return app.WagerMessage{}, apperrors.New(apperrors.KindInput, codeUnsupportedMessageType,
			fmt.Errorf("sqsconsumer: message type %q", *env.Type))
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return app.WagerMessage{}, malformed(errors.New("data is required"))
	}
	var data wagerData
	if err := decodeStrict(env.Data, &data); err != nil {
		return app.WagerMessage{}, malformed(err)
	}
	hash, err := messageHash(data)
	if err != nil {
		return app.WagerMessage{}, err
	}
	return app.WagerMessage{
		MessageID: *env.MessageID, MessageHash: hash, MessageType: messageType,
		CorrelationID: correlationID(correlationAttr, *env.MessageID), Input: data.input(), ReceivedAt: receivedAt,
	}, nil
}

// decodeStrict decodes exactly one JSON value into v, refusing unknown fields
// and trailing data, as the HTTP edge does.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the message")
	}
	return nil
}

func malformed(err error) error {
	return apperrors.New(apperrors.KindInput, codeMalformedMessage, fmt.Errorf("sqsconsumer: %w", err))
}

// messageHash is the SHA-256 (lowercase hex) of the canonical {"data","type"}
// (messaging.md §3.3): sorted keys, no spaces, no HTML escaping.
func messageHash(data wagerData) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Data wagerData `json:"data"`
		Type string    `json:"type"`
	}{data, messageType}); err != nil {
		return "", fmt.Errorf("sqsconsumer: message hash: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(buf.String(), "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// input is the raw operation for wagering.NewCommand.
func (d wagerData) input() wagering.Input {
	in := wagering.Input{
		IdempotencyKey: d.IdempotencyKey, ProviderID: d.ProviderID, ExternalTransactionID: d.ExternalTransactionID,
		PlayerID: d.PlayerID, WalletID: d.WalletID, RoundID: d.RoundID, GameID: d.GameID, Kind: d.Kind,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
	}
	if d.Money != nil {
		in.Money = &wagering.MoneyInput{Amount: d.Money.Amount, Currency: d.Money.Currency}
	}
	return in
}

// correlationID is the correlationId attribute when it follows the HTTP rule,
// else the messageId (messaging.md §3.2; spec M5, decision 13).
func correlationID(attr, messageID string) string {
	if correlationPattern.MatchString(attr) {
		return attr
	}
	return messageID
}
