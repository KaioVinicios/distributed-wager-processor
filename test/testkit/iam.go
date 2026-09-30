package testkit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// PolicyARNs are the resources the policy templates of deploy/aws/policies
// name, as aws-init renders them.
type PolicyARNs struct {
	WagerQueue, DLQ, Topic, AuditQueue string
}

// RenderPolicy reads deploy/aws/policies/<name>.json with the placeholders
// replaced by arns, the same substitution as deploy/aws/init.sh.
func RenderPolicy(tb testing.TB, name string, arns PolicyARNs) string {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(RepoRoot(tb), "deploy", "aws", "policies", name+".json"))
	if err != nil {
		tb.Fatalf("policy %s: %v", name, err)
	}
	return strings.NewReplacer("${WAGER_QUEUE_ARN}", arns.WagerQueue, "${DLQ_ARN}", arns.DLQ,
		"${TOPIC_ARN}", arns.Topic, "${AUDIT_QUEUE_ARN}", arns.AuditQueue).Replace(string(raw))
}

// NewIAMUser creates an IAM user of the test with the identity policy
// (none when policy is "") and an access key; everything is deleted at
// cleanup unless PDA_TEST_KEEP=1.
func NewIAMUser(tb testing.TB, client *iam.Client, policy string) AWSKeys {
	tb.Helper()
	ctx := tb.Context()
	name := "test-" + NewID()[:23]
	if _, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(name)}); err != nil {
		tb.Fatalf("create user: %v", err)
	}
	if policy != "" {
		if _, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
			UserName: aws.String(name), PolicyName: aws.String(name + "-policy"), PolicyDocument: aws.String(policy),
		}); err != nil {
			tb.Fatalf("put user policy: %v", err)
		}
	}
	key, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(name)})
	if err != nil {
		tb.Fatalf("create access key: %v", err)
	}
	tb.Cleanup(func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		ctx := context.WithoutCancel(ctx)
		_, _ = client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(name), AccessKeyId: key.AccessKey.AccessKeyId})
		if policy != "" {
			_, _ = client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(name), PolicyName: aws.String(name + "-policy")})
		}
		_, _ = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(name)})
	})
	return AWSKeys{AccessKeyID: aws.ToString(key.AccessKey.AccessKeyId), SecretAccessKey: aws.ToString(key.AccessKey.SecretAccessKey)}
}
