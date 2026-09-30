package httpapi

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the API handler and starts the API server.
var Module = fx.Module("httpapi", fx.Provide(newHandler), fx.Invoke(RegisterServer))

type handlerParams struct {
	fx.In

	Config    config.Config
	Log       *slog.Logger
	Health    *observability.Health
	Metrics   *observability.Metrics
	Verifier  *auth.Verifier
	Wagers    *app.ProcessWager
	Wallets   *app.OpenWallet
	Queries   *app.Queries
	Reconcile *app.Reconcile
}

func newHandler(p handlerParams) http.Handler {
	return New(Options{DocsEnabled: p.Config.APIDocsEnabled, RequestTimeout: p.Config.HTTPRequestTimeout, Log: p.Log, Metrics: p.Metrics}, Services{
		Auth: p.Verifier, Wagers: p.Wagers, Wallets: p.Wallets, Queries: p.Queries,
		Reconcile: p.Reconcile, Health: p.Health,
	})
}
