package awsclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
)

// fakeIdentity answers GetCallerIdentity with arn, or fails with err.
type fakeIdentity struct {
	account, arn string
	err          error
}

func (f fakeIdentity) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.account), Arn: aws.String(f.arn)}, nil
}

// fakeTopics knows a fixed set of topic ARNs and records what was probed.
type fakeTopics struct {
	arns   map[string]bool
	probed []string
}

func (f *fakeTopics) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	f.probed = append(f.probed, aws.ToString(in.TopicArn))
	if !f.arns[aws.ToString(in.TopicArn)] {
		return nil, errors.New("NotFound: Topic does not exist")
	}
	return &sns.GetTopicAttributesOutput{}, nil
}

var service = fakeIdentity{account: "000000000000", arn: "arn:aws:iam::000000000000:user/pda-wallet-service"}

// Covers: OUT-07, FX-02 (spec M4, decision 1)
func TestResolveTopic_BuildsTheARNFromTheCallerAndVerifiesIt(t *testing.T) {
	want := "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"
	api := &fakeTopics{arns: map[string]bool{want: true}}
	got, err := awsclient.ResolveTopic(t.Context(), service, api, "us-east-1", "wallet-events.fifo")
	if err != nil || got != (awsclient.Topic{ARN: want}) {
		t.Fatalf("ResolveTopic() = %+v, %v; want %s", got, err, want)
	}
	if len(api.probed) != 1 || api.probed[0] != want {
		t.Fatalf("GetTopicAttributes probed %v, want [%s]", api.probed, want)
	}
}

// Covers: OUT-07 (spec M4, decision 1)
func TestResolveTopic_KeepsThePartitionOfTheCaller(t *testing.T) {
	gov := fakeIdentity{account: "123456789012", arn: "arn:aws-us-gov:sts::123456789012:assumed-role/pda/x"}
	want := "arn:aws-us-gov:sns:us-gov-west-1:123456789012:wallet-events.fifo"
	got, err := awsclient.ResolveTopic(t.Context(), gov, &fakeTopics{arns: map[string]bool{want: true}}, "us-gov-west-1", "wallet-events.fifo")
	if err != nil || got.ARN != want {
		t.Fatalf("ResolveTopic() = %+v, %v; want %s", got, err, want)
	}
}

// Covers: FX-02 (spec M4, decision 1)
func TestResolveTopic_FailsNamingTheTopic(t *testing.T) {
	cases := map[string]awsclient.CallerIdentityAPI{
		"topic absent or not readable": service,
		"identity unavailable":         fakeIdentity{err: errors.New("UnrecognizedClientException")},
		"identity without an ARN":      fakeIdentity{account: "000000000000", arn: "not-an-arn"},
	}
	for name, id := range cases {
		_, err := awsclient.ResolveTopic(t.Context(), id, &fakeTopics{}, "us-east-1", "missing.fifo")
		if err == nil || !strings.Contains(err.Error(), "missing.fifo") {
			t.Errorf("%s: ResolveTopic() error = %v, want an error naming missing.fifo", name, err)
		}
	}
}
