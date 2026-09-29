package httpapi

import (
	"net/http"
	"strconv"

	"github.com/KaioVinicios/pda/internal/app"
)

// openWallet is POST /wallets (lifecycle §6.4): 201 with the wallet and its
// Location.
func (h handlers) openWallet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID       *string     `json:"playerId"`
		InitialBalance *moneyInput `json:"initialBalance"`
	}
	if code, field, ok := decodeBody(r, &body); !ok {
		writeProblem(w, r, code, field)
		return
	}
	ctx := r.Context()
	opened, err := h.s.Wallets.Execute(ctx, app.OpenWalletInput{
		PlayerID: body.PlayerID, InitialBalance: body.InitialBalance.domain(),
	}, correlationID(ctx))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	w.Header().Set("Location", "/wallets/"+opened.ID())
	writeJSON(w, http.StatusCreated, walletResponse(opened))
}

// getWallet is GET /wallets/{walletId}.
func (h handlers) getWallet(w http.ResponseWriter, r *http.Request) {
	found, err := h.s.Queries.GetWallet(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponse(found))
}

// listLedger is GET /wallets/{walletId}/ledger?cursor=&limit=. A limit that
// is not an integer is rejected here; its range and the cursor are checked by
// the query (D-16).
func (h handlers) listLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := app.DefaultLedgerLimit
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			writeProblem(w, r, codeInvalidField, "limit")
			return
		}
		limit = n
	}
	page, err := h.s.Queries.ListLedger(r.Context(), r.PathValue("walletId"), q.Get("cursor"), limit)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, ledgerResponse(page))
}

// reconcile is POST /wallets/{walletId}/reconciliation: 200 even when it
// finds a divergence (consistent false).
func (h handlers) reconcile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out, err := h.s.Reconcile.Execute(ctx, r.PathValue("walletId"), correlationID(ctx))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse(out))
}
