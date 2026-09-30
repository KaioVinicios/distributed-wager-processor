package awsclient

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// CallerIdentityAPI is the subset of *sts.Client used to learn the account.
type CallerIdentityAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// TopicAPI is the subset of *sns.Client used to verify the topic.
type TopicAPI interface {
	GetTopicAttributes(ctx context.Context, in *sns.GetTopicAttributesInput, optFns ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
}

// Topic holds the resolved ARN of the events topic. It is filled on start.
type Topic struct {
	ARN string
}

// ResolveTopic builds the ARN of the topic name and verifies that it exists
// (spec M4, decision 1). SNS has no lookup by name: the partition and the
// account come from the caller identity, which needs no permission, and
// GetTopicAttributes, allowed on the topic only, fails fast when the topic is
// missing or unreadable.
func ResolveTopic(ctx context.Context, id CallerIdentityAPI, api TopicAPI, region, name string) (Topic, error) {
	who, err := id.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Topic{}, fmt.Errorf("awsclient: resolve topic %s: caller identity: %w", name, err)
	}
	caller, err := arn.Parse(aws.ToString(who.Arn))
	if err != nil {
		return Topic{}, fmt.Errorf("awsclient: resolve topic %s: caller identity: %w", name, err)
	}
	topicARN := arn.ARN{
		Partition: caller.Partition, Service: "sns", Region: region,
		AccountID: aws.ToString(who.Account), Resource: name,
	}.String()
	if _, err := api.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)}); err != nil {
		return Topic{}, fmt.Errorf("awsclient: read attributes of topic %s: %w", name, err)
	}
	return Topic{ARN: topicARN}, nil
}
