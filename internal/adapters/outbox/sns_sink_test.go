package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/app"
)

// captureSNS records the inputs; err makes Publish fail.
type captureSNS struct {
	inputs []*sns.PublishInput
	err    error
}

func (c *captureSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	c.inputs = append(c.inputs, in)
	if c.err != nil {
		return nil, c.err
	}
	return &sns.PublishOutput{MessageId: aws.String("m-1")}, nil
}

var pending = app.PendingEvent{
	EventID: "0192f2a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b", MessageGroupID: "0192f291-27dd-7d3f-8071-5f8685deef37",
	EventType: "WalletBalanceChanged", EventVersion: 1, CorrelationID: "corr-1",
	Payload: []byte(`{"eventId": "0192f2a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b"}`),
}

// Covers: OUT-05, OUT-07 (messaging.md §5.2)
func TestSNSSinkPublishInput(t *testing.T) {
	api := &captureSNS{}
	topic := &awsclient.Topic{ARN: "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"}
	if err := outbox.NewSNSSink(api, topic).Publish(t.Context(), pending); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(api.inputs) != 1 {
		t.Fatalf("Publish calls = %d, want 1", len(api.inputs))
	}
	in := api.inputs[0]
	attr := func(name string) types.MessageAttributeValue { return in.MessageAttributes[name] }
	switch {
	case aws.ToString(in.TopicArn) != topic.ARN:
		t.Fatalf("TopicArn = %s", aws.ToString(in.TopicArn))
	case aws.ToString(in.Message) != string(pending.Payload): // the column text, never re-serialized
		t.Fatalf("Message = %s", aws.ToString(in.Message))
	case aws.ToString(in.MessageGroupId) != pending.MessageGroupID:
		t.Fatalf("MessageGroupId = %s, want the wallet", aws.ToString(in.MessageGroupId))
	case aws.ToString(in.MessageDeduplicationId) != pending.EventID:
		t.Fatalf("MessageDeduplicationId = %s, want the event id", aws.ToString(in.MessageDeduplicationId))
	case len(in.MessageAttributes) != 3,
		aws.ToString(attr("eventType").DataType) != "String" || aws.ToString(attr("eventType").StringValue) != "WalletBalanceChanged",
		aws.ToString(attr("eventVersion").DataType) != "Number" || aws.ToString(attr("eventVersion").StringValue) != "1",
		aws.ToString(attr("correlationId").DataType) != "String" || aws.ToString(attr("correlationId").StringValue) != "corr-1":
		t.Fatalf("MessageAttributes = %+v", in.MessageAttributes)
	}
}

// Covers: OUT-04
func TestSNSSinkReportsTheEvent(t *testing.T) {
	cause := errors.New("ThrottledException")
	err := outbox.NewSNSSink(&captureSNS{err: cause}, &awsclient.Topic{ARN: "arn"}).Publish(t.Context(), pending)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), pending.EventID) {
		t.Fatalf("Publish error = %v, want it to wrap the cause and name the event", err)
	}
}
