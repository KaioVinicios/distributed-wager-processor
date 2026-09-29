package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// decodeBody decodes the body strictly into v: one JSON object, no unknown
// field, nothing after it (lifecycle §3.1 step 1). A value of the wrong JSON
// type is reported with the code of its field (spec decision 11); every other
// failure, including a body above the limit, is MALFORMED_REQUEST. The fields
// are pointers to strings, so a JSON number is never converted: no float.
func decodeBody(r *http.Request, v any) (code, field string, ok bool) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return typeErrorCode(typeErr.Field), typeErr.Field, false
	case err != nil:
		return codeMalformedRequest, "", false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return codeMalformedRequest, "", false
	}
	return "", "", true
}

func typeErrorCode(field string) string {
	switch {
	case strings.HasSuffix(field, ".amount"):
		return codeInvalidAmount
	case strings.HasSuffix(field, ".currency"):
		return codeInvalidCurrency
	case field == "kind":
		return codeInvalidKind
	}
	return codeInvalidField
}

// moneyInput is a raw {"amount","currency"}; nil fields are absent.
type moneyInput struct {
	Amount   *string `json:"amount"`
	Currency *string `json:"currency"`
}

func (m *moneyInput) domain() *wagering.MoneyInput {
	if m == nil {
		return nil
	}
	return &wagering.MoneyInput{Amount: m.Amount, Currency: m.Currency}
}

type walletBody struct {
	ID        string      `json:"id"`
	PlayerID  string      `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

func walletResponse(w wallet.Wallet) walletBody {
	return walletBody{
		ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version(),
		CreatedAt: w.CreatedAt(), UpdatedAt: w.UpdatedAt(),
	}
}

type ledgerEntryBody struct {
	ID            string      `json:"id"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
}

type ledgerPageBody struct {
	Items      []ledgerEntryBody `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func ledgerResponse(p app.LedgerPage) ledgerPageBody {
	items := make([]ledgerEntryBody, 0, len(p.Entries))
	for i := range p.Entries {
		e := &p.Entries[i]
		items = append(items, ledgerEntryBody{
			ID: e.ID(), TransactionID: e.TransactionID(), Direction: string(e.Direction()), Amount: e.Amount(),
			BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(), WalletVersion: e.WalletVersion(),
			CreatedAt: e.CreatedAt(),
		})
	}
	return ledgerPageBody{Items: items, NextCursor: p.NextCursor}
}

type reconciliationBody struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

func reconciliationResponse(r app.Reconciliation) reconciliationBody {
	return reconciliationBody{
		WalletID: r.WalletID, StoredBalance: r.Stored, CalculatedBalance: r.Calculated,
		Difference: r.Difference, Consistent: r.Consistent, CheckedEntries: r.CheckedEntries,
	}
}

// resultBody is the TransactionResult of a recorded operation (D-04).
type resultBody struct {
	TransactionID    string       `json:"transactionId"`
	Status           string       `json:"status"`
	Balance          *money.Money `json:"balance,omitempty"`
	FailureCode      string       `json:"failureCode,omitempty"`
	FailureCategory  string       `json:"failureCategory,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

func resultResponse(tx *wagering.WagerTransaction, replay bool) resultBody {
	b := resultBody{TransactionID: tx.ID(), Status: string(tx.Status()), IdempotentReplay: replay}
	b.Balance = observedBalance(tx)
	if code := tx.FailureCode(); code != "" {
		b.FailureCode, b.FailureCategory = string(code), string(code.Category())
	}
	return b
}

// observedBalance is the balance recorded by PROCESSED and REJECTED (the one
// replays return, IDEM-08); nil for the other states.
func observedBalance(tx *wagering.WagerTransaction) *money.Money {
	if b := tx.ResultBalance(); b.Currency().Valid() {
		return &b
	}
	return nil
}

// transactionBody is the full representation of an operation (D-04).
type transactionBody struct {
	TransactionID                  string       `json:"transactionId"`
	Origin                         string       `json:"origin"`
	Kind                           string       `json:"kind"`
	Status                         string       `json:"status"`
	WalletID                       string       `json:"walletId"`
	PlayerID                       string       `json:"playerId"`
	Money                          money.Money  `json:"money"`
	ProviderID                     string       `json:"providerId,omitempty"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	ReceivedVia                    string       `json:"receivedVia,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string       `json:"referenceTransactionId,omitempty"`
	Balance                        *money.Money `json:"balance,omitempty"`
	FailureCode                    string       `json:"failureCode,omitempty"`
	FailureCategory                string       `json:"failureCategory,omitempty"`
	Attempts                       *int         `json:"attempts,omitempty"`
	NextAttemptAt                  *time.Time   `json:"nextAttemptAt,omitempty"`
	ExpiresAt                      *time.Time   `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time    `json:"createdAt"`
	UpdatedAt                      time.Time    `json:"updatedAt"`
	CompletedAt                    *time.Time   `json:"completedAt,omitempty"`
}

func transactionResponse(tx *wagering.WagerTransaction) transactionBody {
	b := transactionBody{
		TransactionID: tx.ID(), Origin: string(tx.Origin()), Kind: string(tx.Kind()), Status: string(tx.Status()),
		WalletID: tx.WalletID(), PlayerID: tx.PlayerID(), Money: tx.Money(),
		ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(),
		RoundID: tx.RoundID(), GameID: tx.GameID(), ReceivedVia: string(tx.ReceivedVia()),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceTransactionID:         tx.ReferenceTransactionID(),
		Balance:                        observedBalance(tx),
		CreatedAt:                      tx.CreatedAt(), UpdatedAt: tx.UpdatedAt(),
	}
	if code := tx.FailureCode(); code != "" {
		b.FailureCode, b.FailureCategory = string(code), string(code.Category())
	}
	if tx.Status() == wagering.StatusPendingReference {
		attempts, next, expires := tx.Attempts(), tx.NextAttemptAt(), tx.ExpiresAt()
		b.Attempts, b.NextAttemptAt, b.ExpiresAt = &attempts, &next, &expires
	}
	if completed := tx.CompletedAt(); !completed.IsZero() {
		b.CompletedAt = &completed
	}
	return b
}
