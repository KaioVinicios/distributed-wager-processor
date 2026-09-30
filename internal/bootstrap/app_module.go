package bootstrap

import (
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/observability"
)

// appModule provides the use cases. They are plain constructors, registered
// here so that the app package never imports Fx (structure.md §3).
var appModule = fx.Module("app",
	fx.Provide(
		func() app.Clock { return app.SystemClock{} },
		func() app.IDGenerator { return app.UUIDv7{} },
		func(m *observability.Metrics) app.Metrics { return m },
		newReferencePolicy,
		app.NewOpenWallet,
		app.NewProcessWager,
		app.NewConsumeWager,
		app.NewQueries,
		app.NewReconcile,
	),
)

// newReferencePolicy is the schedule of pending references (D-11); the jitter
// uses math/rand/v2.
func newReferencePolicy(cfg config.Config) (wagering.ReferenceRetryPolicy, error) {
	return wagering.NewReferenceRetryPolicy(cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay,
		cfg.ReferenceMaxAttempts, cfg.ReferenceTTL, nil)
}
