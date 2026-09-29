package wagering_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func mustCommand(t *testing.T, in wagering.Input) wagering.Command {
	t.Helper()
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand: %v", err)
	}
	return cmd
}

func TestPayloadHashGolden(t *testing.T) {
	// Covers: TST-U05, IDEM-03
	// The expected hash is computed outside Go:
	//   printf '%s' '<wantCanonical>' | shasum -a 256
	const wantCanonical = `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",` +
		`"providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}`
	const wantHash = "629836932b79106b99523d06a1e7fa80689b0ea1e1c47aa3f0a5a2c87d0c4344"

	cmd := mustCommand(t, betInput())
	if cmd.CanonicalPayload() != wantCanonical {
		t.Fatalf("canonical payload:\n got %s\nwant %s", cmd.CanonicalPayload(), wantCanonical)
	}
	if cmd.PayloadHash() != wantHash {
		t.Fatalf("PayloadHash = %s, want %s", cmd.PayloadHash(), wantHash)
	}

	changes := map[string]func(in *wagering.Input){
		"providerId":            func(in *wagering.Input) { in.ProviderID = ptr("provider-b") },
		"externalTransactionId": func(in *wagering.Input) { in.ExternalTransactionID = ptr("transaction-124") },
		"playerId":              func(in *wagering.Input) { in.PlayerID = ptr(otherID) },
		"walletId":              func(in *wagering.Input) { in.WalletID = ptr(otherID) },
		"roundId":               func(in *wagering.Input) { in.RoundID = ptr("round-988") },
		"gameId":                func(in *wagering.Input) { in.GameID = ptr("other-game") },
		"kind":                  func(in *wagering.Input) { in.Kind = ptr("WIN") },
		"money.amount":          func(in *wagering.Input) { in.Money.Amount = ptr("25.01") },
		"money.currency":        func(in *wagering.Input) { in.Money.Currency = ptr("USD") },
		"reference": func(in *wagering.Input) {
			in.Kind, in.ReferenceExternalTransactionID = ptr("WIN"), ptr("transaction-100")
		},
	}
	seen := map[string]string{cmd.PayloadHash(): "original"}
	for field, mutate := range changes {
		in := betInput()
		mutate(&in)
		h := mustCommand(t, in).PayloadHash()
		if prev, dup := seen[h]; dup {
			t.Errorf("changing %s kept the hash of %s", field, prev)
		}
		seen[h] = field
	}

	win := betInput()
	win.Kind, win.ReferenceExternalTransactionID = ptr("WIN"), ptr("transaction-100")
	if c := mustCommand(t, win).CanonicalPayload(); !strings.Contains(c, `"providerId":"provider-a","referenceExternalTransactionId":"transaction-100","roundId"`) {
		t.Errorf("reference must be in lexicographic position: %s", c)
	}

	html := betInput()
	html.GameID = ptr("a<b>&c")
	if c := mustCommand(t, html).CanonicalPayload(); !strings.Contains(c, `"gameId":"a<b>&c"`) {
		t.Errorf("canonical JSON must not escape HTML: %s", c)
	}

	otherKey := betInput()
	otherKey.IdempotencyKey = ptr("another-key")
	if mustCommand(t, otherKey).PayloadHash() != wantHash {
		t.Error("the idempotency key must stay out of the hash")
	}
}

func TestPayloadHashHTTPEqualsSQS(t *testing.T) {
	// Covers: IDEM-04
	// HTTP: header key, lowercase UUIDs. SQS: data.idempotencyKey with another
	// value and uppercase UUIDs. Same business, same hash.
	httpIn := betInput()
	sqsIn := betInput()
	sqsIn.IdempotencyKey = ptr("msg-123")
	sqsIn.PlayerID, sqsIn.WalletID = ptr(strings.ToUpper(playerID)), ptr(strings.ToUpper(walletID))

	h, s := mustCommand(t, httpIn), mustCommand(t, sqsIn)
	if len(h.PayloadHash()) != 64 || h.PayloadHash() != s.PayloadHash() || h.CanonicalPayload() != s.CanonicalPayload() {
		t.Fatalf("HTTP and SQS hashes differ:\n%s\n%s", h.CanonicalPayload(), s.CanonicalPayload())
	}
}
