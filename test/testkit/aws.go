package testkit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// MiniStackURL is the emulator as seen from the host.
const MiniStackURL = "http://localhost:4566"

// rootKey is MiniStack's root access key: it bypasses IAM (spike-ministack §3).
const rootKey = "test"

// UseRootAWS points the SDK default chain (used by the app under test) at
// MiniStack with the root key, for the duration of the test.
func UseRootAWS(tb testing.TB) {
	tb.Helper()
	tb.Setenv("AWS_ENDPOINT_URL", MiniStackURL)
	tb.Setenv("AWS_REGION", DotEnv(tb)["AWS_REGION"])
	tb.Setenv("AWS_ACCESS_KEY_ID", rootKey)
	tb.Setenv("AWS_SECRET_ACCESS_KEY", rootKey)
	for _, k := range []string{"AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		tb.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			tb.Fatalf("unset %s: %v", k, err)
		}
	}
}

// RootAWSConfig returns an SDK config for MiniStack with the root key.
func RootAWSConfig(tb testing.TB) aws.Config {
	tb.Helper()
	return AWSConfigWithKeys(tb, AWSKeys{AccessKeyID: rootKey, SecretAccessKey: rootKey})
}

// AWSConfigWithKeys returns an SDK config for MiniStack with explicit keys.
func AWSConfigWithKeys(tb testing.TB, k AWSKeys) aws.Config {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(DotEnv(tb)["AWS_REGION"]),
		awsconfig.WithBaseEndpoint(MiniStackURL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(k.AccessKeyID, k.SecretAccessKey, "")),
	)
	if err != nil {
		tb.Fatalf("aws config: %v", err)
	}
	return cfg
}

// CreateQueues creates an isolated wager queue and DLQ (redrive after 3
// receives, test-plan §3.2) and deletes them at cleanup unless PDA_TEST_KEEP=1.
func CreateQueues(tb testing.TB, client *sqs.Client) (wager, dlq string) {
	tb.Helper()
	wager, dlq, remove, err := createQueues(context.Background(), client)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(remove)
	return wager, dlq
}

// createQueues is CreateQueues for callers without a testing.TB, such as
// TestMain; remove deletes the queues unless PDA_TEST_KEEP=1.
func createQueues(ctx context.Context, client *sqs.Client) (wager, dlq string, remove func(), err error) {
	suffix := uuid.NewString()[:8]
	dlq, wager = "wager-dlq-"+suffix+".fifo", "wager-"+suffix+".fifo"

	dlqOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(dlq), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		return "", "", nil, fmt.Errorf("testkit: create %s: %w", dlq, err)
	}
	urls := []*string{dlqOut.QueueUrl}
	remove = func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		for _, url := range urls {
			_, _ = client.DeleteQueue(context.WithoutCancel(ctx), &sqs.DeleteQueueInput{QueueUrl: url})
		}
	}
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: dlqOut.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		remove()
		return "", "", nil, fmt.Errorf("testkit: dlq arn: %w", err)
	}
	redrive := `{"deadLetterTargetArn":"` + attrs.Attributes["QueueArn"] + `","maxReceiveCount":"3"}`
	wagerOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(wager), Attributes: map[string]string{"FifoQueue": "true", "RedrivePolicy": redrive}})
	if err != nil {
		remove()
		return "", "", nil, fmt.Errorf("testkit: create %s: %w", wager, err)
	}
	urls = append(urls, wagerOut.QueueUrl)
	return wager, dlq, remove, nil
}

// rootAWS points the SDK default chain of this process, used by the
// application started in process, at MiniStack with the root key, and returns
// a config with the same key. For TestMain, where t.Setenv is not available.
func rootAWS(ctx context.Context) (aws.Config, error) {
	vals, err := LoadDotEnv()
	if err != nil {
		return aws.Config{}, err
	}
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL": MiniStackURL, "AWS_REGION": vals["AWS_REGION"],
		"AWS_ACCESS_KEY_ID": rootKey, "AWS_SECRET_ACCESS_KEY": rootKey,
	} {
		if err := os.Setenv(k, v); err != nil {
			return aws.Config{}, err
		}
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		if err := os.Unsetenv(k); err != nil {
			return aws.Config{}, err
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(vals["AWS_REGION"]), awsconfig.WithBaseEndpoint(MiniStackURL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(rootKey, rootKey, "")))
	if err != nil {
		return aws.Config{}, fmt.Errorf("testkit: aws config: %w", err)
	}
	return cfg, nil
}
