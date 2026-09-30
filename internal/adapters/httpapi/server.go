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
func RegisterServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, log *slog.Logger) {
	observability.ServeOnLifecycle(lc, NewServer(cfg.HTTPAddr, handler), cfg.ShutdownTimeout, log, "api")
}

// NewServer builds the API server. ReadTimeout bounds slow bodies; the
// WriteTimeout of 30 s covers the worst lock_timeout plus the race retries
// (spec decision 22) and bounds HTTP_REQUEST_TIMEOUT from above (D-04).
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: config.MaxHTTPRequestTimeout,
	}
}
