// Package httpapi is the HTTP driving adapter.
package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/internal/observability"
)

// NewMux registers the routes. M0 has only the public health routes (D-07).
func NewMux(h *observability.Health) *http.ServeMux {
	mux := http.NewServeMux()
	hh := healthHandler{health: h}
	mux.HandleFunc("GET /health/live", hh.live)
	mux.HandleFunc("GET /health/ready", hh.ready)
	return mux
}
