package sqsconsumer

import (
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const validBody = `{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
"data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123",
"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
"roundId":"round-987","gameId":"fortune-chimp","kind":"WIN","money":{"amount":"25.00","currency":"BRL"},
"referenceExternalTransactionId":"transaction-100"}}`

// Covers: SQS-07, SQS-10 (messaging.md §3.1, lifecycle §5.4; spec M5, decision 14)
func TestParseEnvelope(t *testing.T) {
	received := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a valid message becomes the input of the use case", func(t *testing.T) {
		m, err := parseEnvelope(validBody, "", received)
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if m.MessageID != "msg-123" || m.MessageType != "WagerTransactionRequested" || m.CorrelationID != "msg-123" ||
			!m.ReceivedAt.Equal(received) || len(m.MessageHash) != 64 {
			t.Fatalf("message = %+v", m)
		}
		cmd, err := wagering.NewCommand(m.Input)
		if err != nil {
			t.Fatalf("NewCommand: %v", err)
		}
		if cmd.IdempotencyKey() != "provider-a:transaction-123" || cmd.Kind() != wagering.KindWin ||
			cmd.Money().String() != "25.00" || cmd.ReferenceExternalTransactionID() != "transaction-100" {
			t.Fatalf("command = %+v", cmd)
		}
	})

	cases := []struct {
		name, body, code string
	}{
		{"not JSON", `{"messageId":`, codeMalformedMessage},
		{"not an object", `["msg"]`, codeMalformedMessage},
		{"trailing data", validBody + `{}`, codeMalformedMessage},
		{"unknown envelope field", strings.Replace(validBody, `"type"`, `"extra":1,"type"`, 1), codeMalformedMessage},
		{"unknown data field", strings.Replace(validBody, `"roundId"`, `"extra":1,"roundId"`, 1), codeMalformedMessage},
		{"data field of the wrong type", strings.Replace(validBody, `"amount":"25.00"`, `"amount":25`, 1), codeMalformedMessage},
		{"no messageId", strings.Replace(validBody, `"messageId":"msg-123",`, ``, 1), codeMalformedMessage},
		{"empty messageId", strings.Replace(validBody, `"msg-123"`, `""`, 1), codeMalformedMessage},
		{"messageId of 129 characters", strings.Replace(validBody, `"msg-123"`, `"`+strings.Repeat("é", 129)+`"`, 1), codeMalformedMessage},
		{"no occurredAt", strings.Replace(validBody, `"occurredAt":"2026-09-08T12:00:00.000Z",`, ``, 1), codeMalformedMessage},
		{"occurredAt not RFC 3339", strings.Replace(validBody, `2026-09-08T12:00:00.000Z`, `08/09/2026`, 1), codeMalformedMessage},
		{"no data", `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z"}`, codeMalformedMessage},
		{"null data", `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":null}`, codeMalformedMessage},
		{"no type", strings.Replace(validBody, `"type":"WagerTransactionRequested",`, ``, 1), codeMalformedMessage},
		{"another type", strings.Replace(validBody, `WagerTransactionRequested`, `WalletOpened`, 1), codeUnsupportedMessageType},
		{"another type with other data", `{"messageId":"m","type":"WalletOpened","occurredAt":"2026-09-08T12:00:00Z","data":{"x":1}}`, codeUnsupportedMessageType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEnvelope(tc.body, "", received)
			if apperrors.Classify(err) != apperrors.KindInput || apperrors.CodeOf(err) != tc.code {
				t.Fatalf("error = %v, want KindInput %s", err, tc.code)
			}
		})
	}

	t.Run("a messageId of 128 characters is accepted", func(t *testing.T) {
		if _, err := parseEnvelope(strings.Replace(validBody, `"msg-123"`, `"`+strings.Repeat("é", 128)+`"`, 1), "", received); err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
	})

	t.Run("invalid business fields are left to the use case", func(t *testing.T) {
		m, err := parseEnvelope(strings.Replace(validBody, `"kind":"WIN"`, `"kind":"OPENING"`, 1), "", received)
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if _, err := wagering.NewCommand(m.Input); err == nil {
			t.Fatal("NewCommand accepted an OPENING")
		}
	})
}

// Covers: SQS-03 (messaging.md §3.3; spec M5, decision 4)
func TestMessageHash(t *testing.T) {
	hash := func(t *testing.T, body string) string {
		t.Helper()
		m, err := parseEnvelope(body, "", time.Now())
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		return m.MessageHash
	}
	base := hash(t, validBody)

	same := map[string]string{
		"keys in another order": `{"data":{"money":{"currency":"BRL","amount":"25.00"},"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
"roundId":"round-987","referenceExternalTransactionId":"transaction-100","providerId":"provider-a",
"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","kind":"WIN","idempotencyKey":"provider-a:transaction-123",
"gameId":"fortune-chimp","externalTransactionId":"transaction-123"},"occurredAt":"2026-09-08T12:00:00.000Z",
"type":"WagerTransactionRequested","messageId":"msg-123"}`,
		"other spacing":    strings.ReplaceAll(validBody, `":"`, `" : "`),
		"other messageId":  strings.Replace(validBody, `"msg-123"`, `"msg-999"`, 1),
		"other occurredAt": strings.Replace(validBody, `2026-09-08T12:00:00.000Z`, `2026-09-09T08:30:00Z`, 1),
	}
	for name, body := range same {
		if got := hash(t, body); got != base {
			t.Errorf("%s: hash changed", name)
		}
	}
	differs := map[string]string{
		"other idempotencyKey": strings.Replace(validBody, `"provider-a:transaction-123"`, `"provider-a:other"`, 1),
		"other amount":         strings.Replace(validBody, `"25.00"`, `"25.01"`, 1),
		"other currency":       strings.Replace(validBody, `"BRL"`, `"USD"`, 1),
		"other reference":      strings.Replace(validBody, `"transaction-100"`, `"transaction-101"`, 1),
		"other game":           strings.Replace(validBody, `"fortune-chimp"`, `"fortune-ox"`, 1),
		"uppercase wallet id":  strings.Replace(validBody, `0192f291-27dd-7d3f-8071-5f8685deef37`, `0192F291-27DD-7D3F-8071-5F8685DEEF37`, 1),
	}
	for name, body := range differs {
		if got := hash(t, body); got == base {
			t.Errorf("%s: hash did not change", name)
		}
	}

	absent := hash(t, strings.Replace(validBody, `,
"referenceExternalTransactionId":"transaction-100"`, ``, 1))
	null := hash(t, strings.Replace(validBody, `"transaction-100"`, `null`, 1))
	if absent != null {
		t.Error("an absent field and a null one hash differently")
	}
}

// Covers: IDEM-04 (U05b, the SQS envelope)
//
// The data of the envelope and the same operation as the HTTP edge builds it
// (other case in the UUIDs, no key in the hash) produce the same payload hash.
func TestPayloadHashHTTPEqualsSQS(t *testing.T) {
	m, err := parseEnvelope(strings.NewReplacer(
		"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1",
		`"provider-a:transaction-123"`, `"another-key"`,
	).Replace(validBody), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fromSQS, err := wagering.NewCommand(m.Input)
	if err != nil {
		t.Fatal(err)
	}
	s := func(v string) *string { return &v }
	fromHTTP, err := wagering.NewCommand(wagering.Input{
		IdempotencyKey: s("provider-a:transaction-123"), ProviderID: s("provider-a"), ExternalTransactionID: s("transaction-123"),
		PlayerID: s("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"), WalletID: s("0192f291-27dd-7d3f-8071-5f8685deef37"),
		RoundID: s("round-987"), GameID: s("fortune-chimp"), Kind: s("WIN"),
		Money:                          &wagering.MoneyInput{Amount: s("25.00"), Currency: s("BRL")},
		ReferenceExternalTransactionID: s("transaction-100"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fromSQS.PayloadHash() != fromHTTP.PayloadHash() {
		t.Fatalf("payload hash SQS %s != HTTP %s", fromSQS.PayloadHash(), fromHTTP.PayloadHash())
	}
}

// Covers: OBS-02 (D-18; spec M5, decision 13)
func TestCorrelationID(t *testing.T) {
	cases := []struct{ attr, want string }{
		{"", "msg-1"},
		{"7f1c9a4e-abc_1.2", "7f1c9a4e-abc_1.2"},
		{"has space", "msg-1"},
		{strings.Repeat("a", 128), strings.Repeat("a", 128)},
		{strings.Repeat("a", 129), "msg-1"},
		{"ação", "msg-1"},
	}
	for _, tc := range cases {
		if got := correlationID(tc.attr, "msg-1"); got != tc.want {
			t.Errorf("correlationID(%q) = %q, want %q", tc.attr, got, tc.want)
		}
	}
}
