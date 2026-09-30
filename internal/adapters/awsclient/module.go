package awsclient

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

const resolveTimeout = 5 * time.Second

// Module provides the SQS, SNS and STS clients, resolves the queues and the
// events topic on start (fail fast) and contributes the "sqs" health checker.
// SNS stays out of the readiness (spec M4, decision 3).
var Module = fx.Module("aws",
	fx.Provide(
		func(lc fx.Lifecycle, log *slog.Logger) (aws.Config, error) {
			return NewAWSConfig(newHTTPClient(lc, log))
		},
		func(c aws.Config) *sqs.Client { return sqs.NewFromConfig(c) },
		func(c aws.Config) *sns.Client { return sns.NewFromConfig(c) },
		func(c aws.Config) *sts.Client { return sts.NewFromConfig(c) },
		newQueues,
		newTopic,
		fx.Annotate(NewQueueChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
	// The topic is verified on start even before anything publishes to it.
	fx.Invoke(func(*Topic) {}),
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

func newTopic(lc fx.Lifecycle, cfg config.Config, awsCfg aws.Config, id *sts.Client, api *sns.Client, log *slog.Logger) *Topic {
	t := &Topic{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		defer cancel()
		resolved, err := ResolveTopic(ctx, id, api, awsCfg.Region, cfg.SNSEventsTopicName)
		if err != nil {
			return err
		}
		*t = resolved
		log.Info("sns topic resolved", "topic", cfg.SNSEventsTopicName)
		return nil
	}})
	return t
}
