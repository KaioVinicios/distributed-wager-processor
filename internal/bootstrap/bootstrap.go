// Package bootstrap is the only place that knows every Fx module.
package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Options returns the application modules in registration order (D-15):
// dependencies first, HTTP last, so it starts last and stops first.
func Options() []fx.Option {
	return []fx.Option{
		fx.StopTimeout(config.MaxShutdownTimeout),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: log} }),
		config.Module,
		observability.Module,
		postgres.Module,
		awsclient.Module,
		httpapi.Module,
	}
}
