package awsclient

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

const resolveTimeout = 5 * time.Second

// Module provides the SQS and SNS clients, resolves the queues on start
// (fail fast) and contributes the "sqs" health checker.
var Module = fx.Module("aws",
	fx.Provide(
		func(lc fx.Lifecycle) (aws.Config, error) { return NewAWSConfig(newHTTPClient(lc)) },
		func(c aws.Config) *sqs.Client { return sqs.NewFromConfig(c) },
		func(c aws.Config) *sns.Client { return sns.NewFromConfig(c) },
		newQueues,
		fx.Annotate(NewQueueChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
)

func newQueues(lc fx.Lifecycle, cfg config.Config, client *sqs.Client, log *slog.Logger) *Queues {
	q := &Queues{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		defer cancel()
		resolved, err := ResolveQueues(ctx, client, cfg.WagerQueueName, cfg.WagerDLQName)
		if err != nil {
			return err
		}
		*q = resolved
		log.Info("sqs queues resolved", "wagerQueue", cfg.WagerQueueName, "dlq", cfg.WagerDLQName)
		return nil
	}})
	return q
}
