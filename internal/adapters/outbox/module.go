package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the outbox publisher of this instance (D-13, D-15): the loop
// starts with the application and, on stop, finishes the publications in
// flight before the pool and the AWS clients close.
var Module = fx.Module("outbox",
	fx.Provide(newModulePublisher),
	fx.Invoke(func(*Publisher) {}),
)

func newModulePublisher(lc fx.Lifecycle, cfg config.Config, store app.OutboxStore, api *sns.Client,
	topic *awsclient.Topic, m *observability.Metrics, log *slog.Logger,
) *Publisher {
	owner := instanceID()
	p := NewPublisher(store, NewSNSSink(api, topic), m, log, Options{
		Owner: owner, BatchSize: cfg.OutboxBatchSize, Lease: cfg.OutboxLease, PollInterval: cfg.OutboxPollInterval,
		Concurrency: cfg.OutboxConcurrency, RetryBaseDelay: cfg.OutboxRetryBaseDelay, RetryMaxDelay: cfg.OutboxRetryMaxDelay,
	})
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				p.Run(run)
			}()
			log.Info("outbox publisher started", "owner", owner)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("outbox publisher stopping", "owner", owner)
			cancel()
			select {
			case <-done:
				log.Info("outbox publisher stopped", "owner", owner)
				return nil
			case <-ctx.Done():
				return fmt.Errorf("outbox publisher: stop: %w", ctx.Err())
			}
		},
	})
	return p
}

// instanceID is the locked_by of this process: <hostname>-<pid>-<8 hex>, so
// processes that share a host (tests, containers reusing a PID) never share a
// lease (spec M4, decision 14).
func instanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + uuid.NewString()[:8]
}
