package postgres

import (
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the pool and contributes the "postgres" health checker.
var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		fx.Annotate(NewChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
)
