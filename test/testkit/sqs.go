package testkit

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// WagerData is the data of a WagerTransactionRequested (messaging.md §3.1);
// empty fields are omitted.
type WagerData struct {
	ProviderID                     string `json:"providerId,omitempty"`
	ExternalTransactionID          string `json:"externalTransactionId,omitempty"`
	IdempotencyKey                 string `json:"idempotencyKey,omitempty"`
	PlayerID                       string `json:"playerId,omitempty"`
	WalletID                       string `json:"walletId,omitempty"`
	RoundID                        string `json:"roundId,omitempty"`
	GameID                         string `json:"gameId,omitempty"`
	Kind                           string `json:"kind,omitempty"`
	Money                          *Money `json:"money,omitempty"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
}

// WagerMessage is the body of a WagerTransactionRequested with messageID.
func WagerMessage(tb testing.TB, messageID string, d WagerData) string {
	tb.Helper()
	body, err := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "data": d,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return string(body)
}

// SendOpts are the send attributes of messaging.md §3.2.
type SendOpts struct {
	GroupID       string
	DedupID       string // "" = a new one: the app's deduplication is exercised, not the FIFO's (TST-C11)
	CorrelationID string // "" = no correlationId attribute
}

// SendMessage sends body to the FIFO queue and returns the SQS message id.
func SendMessage(tb testing.TB, client *sqs.Client, queueURL, body string, o SendOpts) string {
	tb.Helper()
	if o.DedupID == "" {
		o.DedupID = NewID()
	}
	in := &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(o.GroupID), MessageDeduplicationId: aws.String(o.DedupID),
	}
	if o.CorrelationID != "" {
		in.MessageAttributes = map[string]types.MessageAttributeValue{
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(o.CorrelationID)},
		}
	}
	out, err := client.SendMessage(tb.Context(), in)
	if err != nil {
		tb.Fatalf("send to %s: %v", queueURL, err)
	}
	return aws.ToString(out.MessageId)
}

// DLQMessage is a message read from a dead-letter queue.
type DLQMessage struct {
	Body       string
	GroupID    string
	DedupID    string
	Attributes map[string]string // message attributes, by name
}

// ReceiveDLQ reads and deletes n messages of the DLQ, failing tb if they do
// not arrive within 20 s.
func ReceiveDLQ(tb testing.TB, client *sqs.Client, dlqURL string, n int) []DLQMessage {
	tb.Helper()
	var got []DLQMessage
	Eventually(tb, 20*time.Second, strconv.Itoa(n)+" messages in the DLQ", func(ctx context.Context) (bool, error) {
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(dlqURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
			MessageAttributeNames:       []string{"All"},
		})
		if err != nil {
			return false, err
		}
		for _, m := range out.Messages {
			attrs := map[string]string{}
			for k, v := range m.MessageAttributes {
				attrs[k] = aws.ToString(v.StringValue)
			}
			got = append(got, DLQMessage{
				Body: aws.ToString(m.Body), Attributes: attrs,
				GroupID: m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
				DedupID: m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
			})
			if _, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(dlqURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
				return false, err
			}
		}
		return len(got) >= n, nil
	})
	if len(got) != n {
		tb.Fatalf("DLQ has %d messages, want %d", len(got), n)
	}
	return got
}

// QueueDepth is the visible plus in-flight messages of the queue.
func QueueDepth(ctx context.Context, client *sqs.Client, queueURL string) (int, error) {
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, k := range []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	} {
		n, err := strconv.Atoi(out.Attributes[string(k)])
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// AssertQueueDrained waits until the queue has no visible nor in-flight message.
func AssertQueueDrained(tb testing.TB, client *sqs.Client, queueURL string) {
	tb.Helper()
	Eventually(tb, 20*time.Second, "queue drained", func(ctx context.Context) (bool, error) {
		n, err := QueueDepth(ctx, client, queueURL)
		return n == 0, err
	})
}
