package references

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the reference worker of this instance (D-11, D-15): the loop
// starts with the application and, on stop, finishes the item in flight
// before the pool closes.
var Module = fx.Module("references",
	fx.Provide(newModuleWorker),
	fx.Invoke(func(*Worker) {}),
)

func newModuleWorker(lc fx.Lifecycle, cfg config.Config, r *app.ResolveReferences, m *observability.Metrics, log *slog.Logger) *Worker {
	w := NewWorker(r, m, log, Options{BatchSize: cfg.ReferenceBatchSize, PollInterval: cfg.ReferencePollInterval})
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				w.Run(run)
			}()
			log.Info("reference worker started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("reference worker stopping")
			cancel()
			select {
			case <-done:
				log.Info("reference worker stopped")
				return nil
			case <-ctx.Done():
				return fmt.Errorf("reference worker: stop: %w", ctx.Err())
			}
		},
	})
	return w
}
