// Package awsclient builds the AWS SDK clients and resolves the SQS resources.
package awsclient

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// QueueAPI is the subset of *sqs.Client used to resolve and probe queues.
type QueueAPI interface {
	GetQueueUrl(ctx context.Context, in *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// Queues holds the resolved queue URLs. It is filled on start.
type Queues struct {
	WagerURL string
	DLQURL   string
}

// ResolveQueues looks both queues up and verifies they answer GetQueueAttributes.
func ResolveQueues(ctx context.Context, api QueueAPI, wagerName, dlqName string) (Queues, error) {
	wager, err := resolveQueue(ctx, api, wagerName)
	if err != nil {
		return Queues{}, err
	}
	dlq, err := resolveQueue(ctx, api, dlqName)
	if err != nil {
		return Queues{}, err
	}
	return Queues{WagerURL: wager, DLQURL: dlq}, nil
}

func resolveQueue(ctx context.Context, api QueueAPI, name string) (string, error) {
	out, err := api.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		return "", fmt.Errorf("awsclient: resolve queue %s: %w", name, err)
	}
	url := aws.ToString(out.QueueUrl)
	if _, err := api.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(url),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	}); err != nil {
		return "", fmt.Errorf("awsclient: read attributes of queue %s: %w", name, err)
	}
	return url, nil
}

// QueueChecker probes the wager queue.
type QueueChecker struct {
	api    QueueAPI
	queues *Queues
}

// NewQueueChecker builds the "sqs" health checker.
func NewQueueChecker(api *sqs.Client, q *Queues) *QueueChecker {
	return &QueueChecker{api: api, queues: q}
}

// Name implements observability.Checker.
func (c *QueueChecker) Name() string { return "sqs" }

// Check implements observability.Checker.
func (c *QueueChecker) Check(ctx context.Context) error {
	if c.queues.WagerURL == "" {
		return errors.New("awsclient: queues not resolved")
	}
	_, err := c.api.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(c.queues.WagerURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	return err
}
