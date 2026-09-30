package sqsconsumer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: SQS-05, SQS-06, SQS-07 (lifecycle §6.2; spec M5 §4.3)
func TestDecide(t *testing.T) {
	transient := errors.New("connection refused")
	cases := []struct {
		name     string
		c        conclusion
		stopping bool
		want     action
	}{
		{
			"duplicate in the inbox",
			conclusion{duplicate: true},
			false,
			action{kind: actDelete, duplicate: "inbox"},
		},
		{
			"processed",
			conclusion{status: wagering.StatusProcessed},
			false,
			action{kind: actDelete, outcome: "processed"},
		},
		{
			"rejected",
			conclusion{status: wagering.StatusRejected},
			false,
			action{kind: actDelete, outcome: "rejected"},
		},
		{
			"pending reference",
			conclusion{status: wagering.StatusPendingReference},
			false,
			action{kind: actDelete, outcome: "pending_reference"},
		},
		{
			"replay",
			conclusion{status: wagering.StatusProcessed, replay: true},
			false,
			action{kind: actDelete, outcome: "replay", duplicate: "idempotency"},
		},
		{
			"replay of a FAILED",
			conclusion{status: wagering.StatusFailed, replay: true},
			false,
			action{kind: actDelete, outcome: "replay", duplicate: "idempotency"},
		},
		{
			"failed",
			conclusion{status: wagering.StatusFailed},
			false,
			action{kind: actDLQ, code: "INTERNAL_PERMANENT_FAILURE", category: "DEFINITIVE", outcome: "failed"},
		},
		{
			"invalid input",
			conclusion{err: apperrors.New(apperrors.KindInput, "INVALID_AMOUNT", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "INVALID_AMOUNT", category: "CORRECTABLE"},
		},
		{
			"unknown wallet",
			conclusion{err: apperrors.New(apperrors.KindInput, app.CodeUnknownWallet, errors.New("x"))},
			false,
			action{kind: actDLQ, code: "UNKNOWN_WALLET", category: "CORRECTABLE"},
		},
		{
			"conflict",
			conclusion{err: apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "IDEMPOTENCY_KEY_REUSED", category: "CORRECTABLE"},
		},
		{
			"permanent without a record",
			conclusion{err: apperrors.New(apperrors.KindPermanent, "", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "INTERNAL_ERROR", category: "TRANSIENT"},
		},
		{
			"transient",
			conclusion{err: apperrors.New(apperrors.KindTransient, "", transient)},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"unclassified is transient (D-05)",
			conclusion{err: transient},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"deadline of the message is transient",
			conclusion{err: context.DeadlineExceeded},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"canceled while stopping",
			conclusion{err: fmt.Errorf("uow: %w", context.Canceled)},
			true,
			action{kind: actRelease},
		},
		{
			"canceled while running is transient",
			conclusion{err: context.Canceled},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.c, tc.stopping, 2, 5*time.Minute); got != tc.want {
				t.Fatalf("decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Covers: SQS-07 (messaging.md §4.1)
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		receiveCount int
		ceiling      time.Duration
		want         time.Duration
	}{
		{1, 300 * time.Second, 2 * time.Second},
		{2, 300 * time.Second, 4 * time.Second},
		{8, 300 * time.Second, 256 * time.Second},
		{9, 300 * time.Second, 300 * time.Second}, // 512 s capped
		{1_000_000, 300 * time.Second, 300 * time.Second},
		{0, 300 * time.Second, 2 * time.Second}, // an unknown count counts as the first
		{-3, 300 * time.Second, 2 * time.Second},
		{3, time.Second, time.Second},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.receiveCount, tc.ceiling); got != tc.want {
			t.Errorf("retryDelay(%d, %v) = %v, want %v", tc.receiveCount, tc.ceiling, got, tc.want)
		}
	}
}

// Covers: SQS-07, SQS-10 (messaging.md §4.4)
func TestDLQInput(t *testing.T) {
	failedAt := time.Date(2026, 9, 29, 12, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))
	msg := types.Message{
		MessageId: aws.String("sqs-id-1"), Body: aws.String(validBody),
		Attributes: map[string]string{"MessageGroupId": "wallet-1"},
	}
	in := dlqInput("https://dlq", msg, action{kind: actDLQ, code: "UNKNOWN_WALLET", category: "CORRECTABLE"}, failedAt)
	if aws.ToString(in.QueueUrl) != "https://dlq" || aws.ToString(in.MessageBody) != validBody ||
		aws.ToString(in.MessageGroupId) != "wallet-1" || aws.ToString(in.MessageDeduplicationId) != "sqs-id-1" {
		t.Fatalf("input = %+v", in)
	}
	want := map[string]string{
		"errorCode": "UNKNOWN_WALLET", "errorCategory": "CORRECTABLE", "originalMessageId": "sqs-id-1",
		"consumerName": app.ConsumerName, "failedAt": "2026-09-29T15:00:00.123Z",
	}
	if len(in.MessageAttributes) != len(want) {
		t.Fatalf("attributes = %v", in.MessageAttributes)
	}
	for k, v := range want {
		if a := in.MessageAttributes[k]; aws.ToString(a.DataType) != "String" || aws.ToString(a.StringValue) != v {
			t.Errorf("attribute %s = %s %s, want String %s", k, aws.ToString(a.DataType), aws.ToString(a.StringValue), v)
		}
	}

	msg.Attributes = nil
	if got := aws.ToString(dlqInput("https://dlq", msg, action{kind: actDLQ, code: "X", category: "Y"}, failedAt).MessageGroupId); got != "invalid-messages" {
		t.Fatalf("group without MessageGroupId = %q, want invalid-messages", got)
	}
}

// Covers: SQS-07 (messaging.md §4.2)
func TestGroupBatch(t *testing.T) {
	m := func(id, group string) types.Message {
		return types.Message{MessageId: aws.String(id), Attributes: map[string]string{"MessageGroupId": group}}
	}
	groups := groupBatch([]types.Message{m("1", "a"), m("2", "b"), m("3", "a"), m("4", "c"), m("5", "b"), m("6", "a")})
	var got [][]string
	for _, g := range groups {
		var ids []string
		for _, msg := range g {
			ids = append(ids, aws.ToString(msg.MessageId))
		}
		got = append(got, ids)
	}
	if fmt.Sprint(got) != "[[1 3 6] [2 5] [4]]" {
		t.Fatalf("groups = %v, want [[1 3 6] [2 5] [4]]", got)
	}
}
