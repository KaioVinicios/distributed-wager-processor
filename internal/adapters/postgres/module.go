package postgres

import (
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the pool, the unit of work, the repositories over the pool
// for reads (D-14) and the "postgres" health checker.
var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		fx.Annotate(NewUnitOfWork, fx.As(new(app.UnitOfWork))),
		NewRepos,
		fx.Annotate(NewChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
)
