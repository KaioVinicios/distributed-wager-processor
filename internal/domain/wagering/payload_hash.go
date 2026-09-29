package wagering

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type canonicalMoney struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// canonicalFields are the business fields of D-08, declared in the
// lexicographic order of their JSON names, so that encoding/json writes the
// keys sorted. The idempotency key and every transport field stay out.
type canonicalFields struct {
	ExternalTransactionID          string         `json:"externalTransactionId"`
	GameID                         string         `json:"gameId"`
	Kind                           string         `json:"kind"`
	Money                          canonicalMoney `json:"money"`
	PlayerID                       string         `json:"playerId"`
	ProviderID                     string         `json:"providerId"`
	ReferenceExternalTransactionID string         `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        string         `json:"roundId"`
	WalletID                       string         `json:"walletId"`
}

// canonicalPayload is the canonical JSON of D-08: sorted keys, no spaces, no
// HTML escaping, no trailing newline, and the reference omitted when absent.
func canonicalPayload(c Command) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	err := enc.Encode(canonicalFields{
		ExternalTransactionID:          c.externalTransactionID,
		GameID:                         c.gameID,
		Kind:                           string(c.kind),
		Money:                          canonicalMoney{Amount: c.money.String(), Currency: string(c.money.Currency())},
		PlayerID:                       c.playerID,
		ProviderID:                     c.providerID,
		ReferenceExternalTransactionID: c.referenceExternalTransactionID,
		RoundID:                        c.roundID,
		WalletID:                       c.walletID,
	})
	if err != nil {
		return "", fmt.Errorf("wagering: canonical payload: %w", err)
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// hashHex is the lowercase hex SHA-256 of s.
func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// CanonicalPayload returns the canonical JSON that PayloadHash digests.
func (c Command) CanonicalPayload() string { return c.canonical }

// PayloadHash returns the SHA-256 (lowercase hex) of the canonical payload. It
// is the same for HTTP and SQS (IDEM-04).
func (c Command) PayloadHash() string { return c.payloadHash }
