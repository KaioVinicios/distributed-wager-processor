package sqsconsumer

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
)

// invalidMessagesGroup groups the DLQ copies of messages without a group.
const invalidMessagesGroup = "invalid-messages"

// dlqInput is the explicit send of msg to the DLQ (messaging.md §4.4): the
// original body and group, deduplicated by the SQS id of the original, so a
// crash between the send and the delete leaves at most one extra copy.
func dlqInput(dlqURL string, msg types.Message, a action, failedAt time.Time) *sqs.SendMessageInput {
	group := msg.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = invalidMessagesGroup
	}
	str := func(v string) types.MessageAttributeValue {
		return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
	}
	return &sqs.SendMessageInput{
		QueueUrl:               aws.String(dlqURL),
		MessageBody:            msg.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: msg.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"errorCode":         str(a.code),
			"errorCategory":     str(a.category),
			"originalMessageId": str(aws.ToString(msg.MessageId)),
			"consumerName":      str(app.ConsumerName),
			"failedAt":          str(failedAt.UTC().Format("2006-01-02T15:04:05.000Z")),
		},
	}
}
