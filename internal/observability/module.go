package observability

import (
	"log/slog"

	"go.uber.org/fx"
)

// Module provides the logger, the metrics registry and the health aggregator,
// and starts the admin server.
var Module = fx.Module("observability",
	fx.Provide(
		NewLogger,
		NewRegistry,
		NewMetrics,
		fx.Annotate(
			func(log *slog.Logger, checkers []Checker) *Health {
				return NewHealth(log, checkers, DefaultCheckTimeout)
			},
			fx.ParamTags(``, `group:"health_checkers"`),
		),
	),
	fx.Invoke(RegisterAdminServer),
)
