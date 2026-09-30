//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"

	"github.com/KaioVinicios/pda/test/testkit"
)

const (
	wagerQueue = "wager-transactions.fifo"
	dlqQueue   = "wager-transactions-dlq.fifo"
	topicName  = "wallet-events.fifo"
	auditQueue = "wallet-events-audit.fifo"
)

func queueAttrs(t *testing.T, c *sqs.Client, name string) (string, map[string]string) {
	t.Helper()
	url, err := c.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("queue %s not provisioned: %v", name, err)
	}
	out, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: url.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll}})
	if err != nil {
		t.Fatalf("attributes of %s: %v", name, err)
	}
	return aws.ToString(url.QueueUrl), out.Attributes
}

// Covers: ART-06, AUTH-09 (validação do deploy/aws/init.sh; prévia do I04f)
// Sensitivity: dropped sqs:GetQueueAttributes from policies/pda-wallet-service.json → AccessDeniedException (403).
// Sensitivity (M4): before sns:GetTopicAttributes entered the policy, the service's check failed with AccessDenied.
// Sensitivity: init.sh with maxReceiveCount 5 → RedrivePolicy assertion fails.
func TestProvisioning(t *testing.T) {
	root := testkit.RootAWSConfig(t)
	rootSQS, rootSNS := sqs.NewFromConfig(root), sns.NewFromConfig(root)

	_, dlq := queueAttrs(t, rootSQS, dlqQueue)
	wagerURL, wager := queueAttrs(t, rootSQS, wagerQueue)
	if wager["FifoQueue"] != "true" || dlq["FifoQueue"] != "true" {
		t.Fatalf("queues must be FIFO: wager=%s dlq=%s", wager["FifoQueue"], dlq["FifoQueue"])
	}
	var redrive map[string]any
	if err := json.Unmarshal([]byte(wager["RedrivePolicy"]), &redrive); err != nil {
		t.Fatalf("RedrivePolicy %q: %v", wager["RedrivePolicy"], err)
	}
	if redrive["deadLetterTargetArn"] != dlq["QueueArn"] || fmt.Sprint(redrive["maxReceiveCount"]) != "10" {
		t.Fatalf("RedrivePolicy = %v, want DLQ %s and maxReceiveCount 10", redrive, dlq["QueueArn"])
	}

	_, audit := queueAttrs(t, rootSQS, auditQueue)
	topics, err := rootSNS.ListTopics(t.Context(), &sns.ListTopicsInput{})
	if err != nil {
		t.Fatalf("list topics: %v", err)
	}
	var topicARN string
	for _, tp := range topics.Topics {
		if strings.HasSuffix(aws.ToString(tp.TopicArn), ":"+topicName) {
			topicARN = aws.ToString(tp.TopicArn)
		}
	}
	if topicARN == "" {
		t.Fatalf("topic %s not provisioned", topicName)
	}
	tattrs, err := rootSNS.GetTopicAttributes(t.Context(), &sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)})
	if err != nil || tattrs.Attributes["FifoTopic"] != "true" {
		t.Fatalf("topic attributes = %v, %v; want FifoTopic=true", tattrs, err)
	}
	subs, err := rootSNS.ListSubscriptionsByTopic(t.Context(), &sns.ListSubscriptionsByTopicInput{TopicArn: aws.String(topicARN)})
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	var raw string
	for _, s := range subs.Subscriptions {
		if aws.ToString(s.Endpoint) == audit["QueueArn"] {
			a, err := rootSNS.GetSubscriptionAttributes(t.Context(), &sns.GetSubscriptionAttributesInput{SubscriptionArn: s.SubscriptionArn})
			if err != nil {
				t.Fatalf("subscription attributes: %v", err)
			}
			raw = a.Attributes["RawMessageDelivery"]
		}
	}
	if raw != "true" {
		t.Fatalf("audit subscription RawMessageDelivery = %q, want true", raw)
	}

	profiles := testkit.AWSProfiles(t)
	for _, p := range []string{"pda-wallet-service", "provider-a", "provider-b"} {
		if profiles[p].AccessKeyID == "" {
			t.Fatalf("credentials file lacks profile %s", p)
		}
	}
	svc := sqs.NewFromConfig(testkit.AWSConfigWithKeys(t, profiles["pda-wallet-service"]))
	if _, err := svc.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{QueueUrl: aws.String(wagerURL), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}}); err != nil {
		t.Fatalf("pda-wallet-service GetQueueAttributes on %s: %v (want allowed)", wagerQueue, err)
	}
	if _, err := sns.NewFromConfig(testkit.AWSConfigWithKeys(t, profiles["pda-wallet-service"])).GetTopicAttributes(t.Context(),
		&sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)}); err != nil {
		t.Fatalf("pda-wallet-service GetTopicAttributes on %s: %v (want allowed: the topic is verified on start)", topicName, err)
	}
	provider := sqs.NewFromConfig(testkit.AWSConfigWithKeys(t, profiles["provider-a"]))
	_, err = provider.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(wagerURL), WaitTimeSeconds: 0})
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.ErrorCode(), "AccessDenied") {
		t.Fatalf("provider-a ReceiveMessage error = %v, want AccessDenied", err)
	}
}
