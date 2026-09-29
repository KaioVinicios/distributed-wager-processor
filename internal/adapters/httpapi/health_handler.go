package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/KaioVinicios/pda/internal/observability"
)

type healthHandler struct{ health *observability.Health }

func (h healthHandler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, observability.Report{Status: observability.StatusUp})
}

func (h healthHandler) ready(w http.ResponseWriter, r *http.Request) {
	report := h.health.Ready(r.Context())
	code := http.StatusOK
	if report.Status != observability.StatusUp {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, report)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v) // the status line is already sent; nothing useful to do on error
}
