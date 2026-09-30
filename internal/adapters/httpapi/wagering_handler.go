package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// submitWager is POST /wagering/transactions (lifecycle §6.1): decode (400) →
// stateless validation by the domain (400) → the provider of the body must be
// the token's (403, before any read) → the use case → the status of the
// recorded outcome (D-04).
func (h handlers) submitWager(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID                     *string     `json:"providerId"`
		ExternalTransactionID          *string     `json:"externalTransactionId"`
		PlayerID                       *string     `json:"playerId"`
		WalletID                       *string     `json:"walletId"`
		RoundID                        *string     `json:"roundId"`
		GameID                         *string     `json:"gameId"`
		Kind                           *string     `json:"kind"`
		Money                          *moneyInput `json:"money"`
		ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId"`
	}
	if code, field, ok := decodeBody(r, &body); !ok {
		writeProblem(w, r, code, field)
		return
	}
	in := wagering.Input{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		PlayerID: body.PlayerID, WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID,
		Kind: body.Kind, Money: body.Money.domain(), ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	}
	switch keys := r.Header.Values("Idempotency-Key"); len(keys) {
	case 0:
	case 1:
		in.IdempotencyKey = &keys[0]
	default:
		writeProblem(w, r, codeInvalidIdempotencyKey, "Idempotency-Key")
		return
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	ctx := r.Context()
	if p, _ := auth.FromContext(ctx); !auth.ActsAs(p, cmd.ProviderID()) {
		h.providerMismatch(w, r)
		return
	}
	res, err := h.s.Wagers.Execute(ctx, app.ProcessRequest{
		Command: cmd, Via: wagering.ReceivedViaHTTP, CorrelationID: correlationID(ctx),
	})
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, resultStatus(res.Tx.Status()), resultResponse(res.Tx, res.Replay))
}

// getTransaction is GET /wagering/transactions/{transactionId}: a provider
// only sees its own operations; any other answers 404, like a missing one,
// so the id is not revealed (D-07).
func (h handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := h.s.Queries.GetTransaction(ctx, r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	if p, _ := auth.FromContext(ctx); !auth.CanSeeTransaction(p, tx.ProviderID()) {
		writeProblem(w, r, codeTransactionNotFound, "")
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx))
}

// getTransactionByExternalID is GET
// /providers/{providerId}/wagering/transactions/{externalTransactionId}: a
// provider may only name itself in the path (403 before any read); the
// internal service may name any provider.
func (h handlers) getTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerID := r.PathValue("providerId")
	if p, _ := auth.FromContext(ctx); !auth.HasRole(p, auth.RoleWalletInternal) && !auth.ActsAs(p, providerID) {
		h.providerMismatch(w, r)
		return
	}
	tx, err := h.s.Queries.GetTransactionByExternalID(ctx, providerID, r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx))
}

// providerMismatch answers 403 PROVIDER_MISMATCH and counts it.
func (h handlers) providerMismatch(w http.ResponseWriter, r *http.Request) {
	if h.metrics != nil {
		h.metrics.AuthFailure("provider_mismatch")
	}
	writeProblem(w, r, codeProviderMismatch, "")
}
