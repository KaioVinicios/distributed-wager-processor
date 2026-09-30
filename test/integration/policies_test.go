//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-09 (I04f; messaging.md §2.1)
// Sensitivity: sqs:ChangeMessageVisibility removed from
// policies/pda-wallet-service.json → "service ChangeMessageVisibility on the
// wager queue: … AccessDeniedException".
//
// The policy documents of deploy/aws/policies, applied to IAM users of the
// test over isolated resources, allow exactly the matrix of messaging §2.1:
// MiniStack evaluates them with AUTH=true (D-02).
func TestBrokerPoliciesEnforced(t *testing.T) {
	t.Parallel()
	root := testkit.RootAWSConfig(t)
	rootSQS, rootSNS := sqs.NewFromConfig(root), sns.NewFromConfig(root)
	wager, dlq := testkit.CreateQueues(t, rootSQS)
	wagerURL, wagerARN := queueURLAndARN(t, rootSQS, wager)
	dlqURL, dlqARN := queueURLAndARN(t, rootSQS, dlq)
	topic := testkit.NewEventsTopic(t, rootSQS, rootSNS)
	_, auditARN := queueURLAndARN(t, rootSQS, topic.AuditQueueURL[strings.LastIndex(topic.AuditQueueURL, "/")+1:])
	arns := testkit.PolicyARNs{WagerQueue: wagerARN, DLQ: dlqARN, Topic: topic.ARN, AuditQueue: auditARN}

	users := iam.NewFromConfig(root)
	as := func(keys testkit.AWSKeys) (*sqs.Client, *sns.Client) {
		cfg := testkit.AWSConfigWithKeys(t, keys)
		return sqs.NewFromConfig(cfg), sns.NewFromConfig(cfg)
	}
	providerSQS, providerSNS := as(testkit.NewIAMUser(t, users, testkit.RenderPolicy(t, "provider", arns)))
	serviceSQS, serviceSNS := as(testkit.NewIAMUser(t, users, testkit.RenderPolicy(t, "pda-wallet-service", arns)))
	nobodySQS, _ := as(testkit.NewIAMUser(t, users, ""))

	body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
	send := func(c *sqs.Client, url string) error {
		_, err := c.SendMessage(t.Context(), &sqs.SendMessageInput{
			QueueUrl: aws.String(url), MessageBody: aws.String(body),
			MessageGroupId: aws.String("g"), MessageDeduplicationId: aws.String(testkit.NewID()),
		})
		return err
	}
	receive := func(c *sqs.Client) (*sqs.ReceiveMessageOutput, error) {
		return c.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(wagerURL), WaitTimeSeconds: 1})
	}
	publish := func(c *sns.Client) error {
		_, err := c.Publish(t.Context(), &sns.PublishInput{
			TopicArn: aws.String(topic.ARN), Message: aws.String("{}"),
			MessageGroupId: aws.String("g"), MessageDeduplicationId: aws.String(testkit.NewID()),
		})
		return err
	}
	attrs := func(c *sqs.Client, url string) error {
		_, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		return err
	}

	// Allowed.
	allow(t, "provider SendMessage to the wager queue", send(providerSQS, wagerURL))
	got, err := receive(serviceSQS)
	allow(t, "service ReceiveMessage from the wager queue", err)
	if err == nil && len(got.Messages) != 1 {
		t.Fatalf("service received %d messages, want the provider's", len(got.Messages))
	}
	if err == nil {
		handle := got.Messages[0].ReceiptHandle
		_, err = serviceSQS.ChangeMessageVisibility(t.Context(), &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(wagerURL), ReceiptHandle: handle, VisibilityTimeout: 0,
		})
		allow(t, "service ChangeMessageVisibility on the wager queue", err)
		got, err = receive(serviceSQS)
		allow(t, "service ReceiveMessage again", err)
		if err == nil && len(got.Messages) == 1 {
			_, err = serviceSQS.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(wagerURL), ReceiptHandle: got.Messages[0].ReceiptHandle})
			allow(t, "service DeleteMessage on the wager queue", err)
		}
	}
	allow(t, "service GetQueueAttributes of the wager queue", attrs(serviceSQS, wagerURL))
	allow(t, "service SendMessage to the DLQ", send(serviceSQS, dlqURL))
	allow(t, "service GetQueueAttributes of the DLQ", attrs(serviceSQS, dlqURL))
	allow(t, "service Publish to the topic", publish(serviceSNS))
	_, err = serviceSNS.GetTopicAttributes(t.Context(), &sns.GetTopicAttributesInput{TopicArn: aws.String(topic.ARN)})
	allow(t, "service GetTopicAttributes", err)

	// Denied.
	_, err = receive(providerSQS)
	deny(t, "provider ReceiveMessage from the wager queue", err)
	deny(t, "provider Publish to the topic", publish(providerSNS))
	deny(t, "provider SendMessage to the DLQ", send(providerSQS, dlqURL))
	deny(t, "service SendMessage to the wager queue", send(serviceSQS, wagerURL))
	deny(t, "user without policy SendMessage to the wager queue", send(nobodySQS, wagerURL))
	_, err = receive(nobodySQS)
	deny(t, "user without policy ReceiveMessage", err)
}

func queueURLAndARN(t *testing.T, c *sqs.Client, name string) (url, arn string) {
	t.Helper()
	out, err := c.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("queue %s: %v", name, err)
	}
	a, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: out.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("arn of %s: %v", name, err)
	}
	return aws.ToString(out.QueueUrl), a.Attributes["QueueArn"]
}

func allow(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v, want allowed", what, err)
	}
}

func deny(t *testing.T, what string, err error) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.ErrorCode(), "AccessDenied") {
		t.Errorf("%s: %v, want AccessDenied", what, err)
	}
}
