package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// RegisterServer runs the API server on HTTP_ADDR. It is registered last, so it
// starts after every dependency and stops first (D-15).
func RegisterServer(lc fx.Lifecycle, cfg config.Config, mux *http.ServeMux, log *slog.Logger) {
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	observability.ServeOnLifecycle(lc, srv, cfg.ShutdownTimeout, log, "api")
}
