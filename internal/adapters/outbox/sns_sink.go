package outbox

import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)

// SNSPublishAPI is the subset of *sns.Client the sink uses.
type SNSPublishAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SNSSink publishes outbox events to the SNS FIFO events topic with the
// mapping of messaging.md §5.2.
type SNSSink struct {
	api   SNSPublishAPI
	topic *awsclient.Topic
}

// NewSNSSink publishes to topic, whose ARN is resolved on start.
func NewSNSSink(api SNSPublishAPI, topic *awsclient.Topic) *SNSSink {
	return &SNSSink{api: api, topic: topic}
}

// Publish sends the payload read from the column, never re-serialized, so a
// republication carries the same eventId and content (OUT-05). The group is
// the wallet and the deduplication id is the eventId (D-13).
func (s *SNSSink) Publish(ctx context.Context, e app.PendingEvent) error {
	_, err := s.api.Publish(ctx, &sns.PublishInput{
		TopicArn:               aws.String(s.topic.ARN),
		Message:                aws.String(string(e.Payload)),
		MessageGroupId:         aws.String(e.MessageGroupID),
		MessageDeduplicationId: aws.String(e.EventID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType":     {DataType: aws.String("String"), StringValue: aws.String(e.EventType)},
			"eventVersion":  {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(e.EventVersion))},
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(e.CorrelationID)},
		},
	})
	if err != nil {
		return fmt.Errorf("outbox: publish event %s: %w", e.EventID, err)
	}
	return nil
}
