package observability

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
)

// NewAdminHandler serves the administrative endpoints (GET /metrics).
func NewAdminHandler(reg *prometheus.Registry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
	return mux
}

// RegisterAdminServer runs the admin server on METRICS_ADDR, separate from the API (D-18).
func RegisterAdminServer(lc fx.Lifecycle, sd fx.Shutdowner, cfg config.Config, reg *prometheus.Registry, log *slog.Logger) {
	srv := &http.Server{Addr: cfg.MetricsAddr, Handler: NewAdminHandler(reg), ReadHeaderTimeout: 5 * time.Second}
	ServeOnLifecycle(lc, sd, srv, cfg.ShutdownTimeout, log, "admin")
}
