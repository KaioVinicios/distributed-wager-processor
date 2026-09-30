package sqsconsumer

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the SQS consumer of this instance (D-12, D-15): it starts with
// the application and, on stop, finishes or releases the messages in flight
// before the pool and the AWS clients close (messaging.md §4.5).
var Module = fx.Module("sqsconsumer",
	fx.Provide(newModuleConsumer),
	fx.Invoke(func(*Consumer) {}),
)

func newModuleConsumer(lc fx.Lifecycle, cfg config.Config, api *sqs.Client, queues *awsclient.Queues,
	proc *app.ConsumeWager, pool *pgxpool.Pool, m *observability.Metrics, log *slog.Logger,
) *Consumer {
	c := NewConsumer(api, queues, proc, pool, m, log, Options{
		Pollers: cfg.SQSConsumerPollers, ReceiveBatch: cfg.SQSReceiveBatch, WaitTime: cfg.SQSWaitTime,
		Visibility: cfg.SQSVisibilityTimeout, ProcessingTimeout: cfg.SQSProcessingTimeout,
		MaxInFlight: cfg.SQSMaxInFlight, RetryMaxDelay: cfg.SQSRetryMaxDelay,
		ShutdownTimeout: cfg.ShutdownTimeout, DLQName: cfg.WagerDLQName,
	})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			c.Start(ctx)
			log.Info("sqs consumer started", "pollers", cfg.SQSConsumerPollers)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("sqs consumer stopping")
			err := c.Stop(ctx)
			log.Info("sqs consumer stopped")
			return err
		},
	})
	return c
}
