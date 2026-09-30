package testkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// EventsTopic is an isolated events topic with its audit queue (test-plan §3.2).
type EventsTopic struct {
	Name          string
	ARN           string
	AuditQueueURL string
}

// NewEventsTopic is CreateEventsTopic for a test; the resources are removed at
// cleanup.
func NewEventsTopic(tb testing.TB, q *sqs.Client, n *sns.Client) EventsTopic {
	tb.Helper()
	topic, remove, err := CreateEventsTopic(tb.Context(), q, n)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(remove)
	return topic
}

// CreateEventsTopic creates the FIFO topic events-<rand>.fifo and the FIFO
// queue events-audit-<rand>.fifo, subscribed to it with raw delivery and the
// queue policy of deploy/aws/policies/wallet-events-audit.json, the same
// resources aws-init provisions (messaging.md §2). remove deletes them unless
// PDA_TEST_KEEP=1.
func CreateEventsTopic(ctx context.Context, q *sqs.Client, n *sns.Client) (EventsTopic, func(), error) {
	suffix := uuid.NewString()[:8]
	topic := EventsTopic{Name: "events-" + suffix + ".fifo"}
	auditName := "events-audit-" + suffix + ".fifo"

	out, err := n.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(topic.Name),
		Attributes: map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		return EventsTopic{}, nil, fmt.Errorf("testkit: create topic %s: %w", topic.Name, err)
	}
	topic.ARN = aws.ToString(out.TopicArn)
	var queueURL *string
	remove := func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		ctx := context.WithoutCancel(ctx)
		_, _ = n.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: out.TopicArn})
		if queueURL != nil {
			_, _ = q.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queueURL})
		}
	}
	fail := func(err error) (EventsTopic, func(), error) {
		remove()
		return EventsTopic{}, nil, err
	}

	queue, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(auditName),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		return fail(fmt.Errorf("testkit: create queue %s: %w", auditName, err))
	}
	queueURL, topic.AuditQueueURL = queue.QueueUrl, aws.ToString(queue.QueueUrl)
	attrs, err := q.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queueURL,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return fail(fmt.Errorf("testkit: arn of %s: %w", auditName, err))
	}
	queueARN := attrs.Attributes["QueueArn"]
	policy, err := auditPolicy(topic.ARN, queueARN)
	if err != nil {
		return fail(err)
	}
	if _, err := q.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   queueURL,
		Attributes: map[string]string{"Policy": policy},
	}); err != nil {
		return fail(fmt.Errorf("testkit: policy of %s: %w", auditName, err))
	}
	if _, err := n.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: out.TopicArn, Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN), Attributes: map[string]string{"RawMessageDelivery": "true"},
	}); err != nil {
		return fail(fmt.Errorf("testkit: subscribe %s: %w", auditName, err))
	}
	return topic, remove, nil
}

// auditPolicy renders the queue policy that lets the topic deliver to the queue.
func auditPolicy(topicARN, queueARN string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "aws", "policies", "wallet-events-audit.json"))
	if err != nil {
		return "", fmt.Errorf("testkit: read the audit queue policy: %w", err)
	}
	return strings.NewReplacer("${AUDIT_QUEUE_ARN}", queueARN, "${TOPIC_ARN}", topicARN).Replace(string(raw)), nil
}
