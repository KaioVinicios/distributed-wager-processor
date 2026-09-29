package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// requestTimeout bounds each call; calls also run in t.Cleanup, where the
// test's context is already done, so the context is detached.
const requestTimeout = 30 * time.Second

// Client calls the API under test with one identity. Every exchange goes
// through the contract (D-20).
type Client struct {
	app   *App
	token string
}

// Request is one call. A Body of type string or []byte is sent as is;
// anything else is encoded as JSON. Invalid marks a request deliberately off
// the contract: only its response is validated (spec decision 20).
type Request struct {
	Method, Path string
	Body         any
	Header       http.Header
	Invalid      bool
}

// Response is what the API answered.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends the request, validates the exchange against the contract and
// fails the test on a transport error or a violation.
func (c *Client) Do(tb testing.TB, r Request) *Response {
	tb.Helper()
	var body []byte
	switch b := r.Body.(type) {
	case nil:
	case string:
		body = []byte(b)
	case []byte:
		body = b
	default:
		var err error
		if body, err = json.Marshal(b); err != nil {
			tb.Fatalf("encode body: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, c.app.BaseURL+r.Path, bytes.NewReader(body))
	if err != nil {
		tb.Fatalf("request: %v", err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range r.Header {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.app.http.Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", r.Method, r.Path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatalf("%s %s: read body: %v", r.Method, r.Path, err)
	}
	if err := c.app.contract.Check(req, body, resp, respBody, r.Invalid); err != nil {
		tb.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: respBody}
}

// JSON decodes an application/json body strictly into v.
func (r *Response) JSON(tb testing.TB, v any) {
	tb.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		tb.Fatalf("Content-Type = %q, want application/json (status %d, body %s)", ct, r.Status, r.Body)
	}
	decodeStrict(tb, r.Body, v)
}

// Problem decodes an application/problem+json body.
func (r *Response) Problem(tb testing.TB) Problem {
	tb.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		tb.Fatalf("Content-Type = %q, want application/problem+json (status %d, body %s)", ct, r.Status, r.Body)
	}
	var p Problem
	decodeStrict(tb, r.Body, &p)
	return p
}

func decodeStrict(tb testing.TB, body []byte, v any) {
	tb.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		tb.Fatalf("decode %s: %v", body, err)
	}
}

// NewID returns a UUIDv7.
func NewID() string { return uuid.Must(uuid.NewV7()).String() }

// Money is the {"amount","currency"} of the contract.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// BRL is amount in reais.
func BRL(amount string) Money { return Money{Amount: amount, Currency: "BRL"} }

// Problem is the application/problem+json body.
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// Wallet is the Wallet schema.
type Wallet struct {
	ID        string    `json:"id"`
	PlayerID  string    `json:"playerId"`
	Balance   Money     `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Wager is the WagerTransactionRequest schema.
type Wager struct {
	ProviderID                     string `json:"providerId"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	PlayerID                       string `json:"playerId"`
	WalletID                       string `json:"walletId"`
	RoundID                        string `json:"roundId"`
	GameID                         string `json:"gameId"`
	Kind                           string `json:"kind"`
	Money                          Money  `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
}

// TransactionResult is the TransactionResult schema.
type TransactionResult struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	Balance          *Money `json:"balance,omitempty"`
	FailureCode      string `json:"failureCode,omitempty"`
	FailureCategory  string `json:"failureCategory,omitempty"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

// Transaction is the Transaction schema.
type Transaction struct {
	TransactionID                  string     `json:"transactionId"`
	Origin                         string     `json:"origin"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	Money                          Money      `json:"money"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	ReceivedVia                    string     `json:"receivedVia,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string     `json:"referenceTransactionId,omitempty"`
	Balance                        *Money     `json:"balance,omitempty"`
	FailureCode                    string     `json:"failureCode,omitempty"`
	FailureCategory                string     `json:"failureCategory,omitempty"`
	Attempts                       *int       `json:"attempts,omitempty"`
	NextAttemptAt                  *time.Time `json:"nextAttemptAt,omitempty"`
	ExpiresAt                      *time.Time `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time  `json:"createdAt"`
	UpdatedAt                      time.Time  `json:"updatedAt"`
	CompletedAt                    *time.Time `json:"completedAt,omitempty"`
}

// LedgerEntry is the LedgerEntry schema.
type LedgerEntry struct {
	ID            string    `json:"id"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Amount        Money     `json:"amount"`
	BalanceBefore Money     `json:"balanceBefore"`
	BalanceAfter  Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

// LedgerPage is the LedgerPage schema.
type LedgerPage struct {
	Items      []LedgerEntry `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// Reconciliation is the Reconciliation schema.
type Reconciliation struct {
	WalletID          string `json:"walletId"`
	StoredBalance     Money  `json:"storedBalance"`
	CalculatedBalance Money  `json:"calculatedBalance"`
	Difference        Money  `json:"difference"`
	Consistent        bool   `json:"consistent"`
	CheckedEntries    int64  `json:"checkedEntries"`
}
