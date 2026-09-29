package awsclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
)

// fakeSQS knows a fixed set of queues; attrErr makes GetQueueAttributes fail.
type fakeSQS struct {
	urls    map[string]string
	attrErr error
	probed  []string
}

func (f *fakeSQS) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	url, ok := f.urls[aws.ToString(in.QueueName)]
	if !ok {
		return nil, errors.New("AWS.SimpleQueueService.NonExistentQueue")
	}
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String(url)}, nil
}

func (f *fakeSQS) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.probed = append(f.probed, aws.ToString(in.QueueUrl))
	if f.attrErr != nil {
		return nil, f.attrErr
	}
	return &sqs.GetQueueAttributesOutput{}, nil
}

func known() *fakeSQS {
	return &fakeSQS{urls: map[string]string{"w.fifo": "http://q/w.fifo", "d.fifo": "http://q/d.fifo"}}
}

// Covers: FX-02
func TestResolveQueues_ReturnsBothURLs(t *testing.T) {
	api := known()
	got, err := awsclient.ResolveQueues(t.Context(), api, "w.fifo", "d.fifo")
	if err != nil {
		t.Fatalf("ResolveQueues() error = %v", err)
	}
	if got != (awsclient.Queues{WagerURL: "http://q/w.fifo", DLQURL: "http://q/d.fifo"}) {
		t.Fatalf("ResolveQueues() = %+v", got)
	}
	if len(api.probed) != 2 {
		t.Fatalf("GetQueueAttributes called %d times, want 2 (both queues verified)", len(api.probed))
	}
}

// Covers: FX-02
func TestResolveQueues_NamesTheMissingQueue(t *testing.T) {
	for _, tc := range []struct{ wager, dlq, missing string }{
		{"absent.fifo", "d.fifo", "absent.fifo"},
		{"w.fifo", "absent-dlq.fifo", "absent-dlq.fifo"},
	} {
		_, err := awsclient.ResolveQueues(t.Context(), known(), tc.wager, tc.dlq)
		if err == nil || !strings.Contains(err.Error(), tc.missing) {
			t.Fatalf("ResolveQueues(%s, %s) error = %v, want it to name %s", tc.wager, tc.dlq, err, tc.missing)
		}
	}
}

// Covers: FX-02, AUTH-09
func TestResolveQueues_FailsWhenQueueIsNotReadable(t *testing.T) {
	api := known()
	api.attrErr = errors.New("AccessDenied")
	if _, err := awsclient.ResolveQueues(t.Context(), api, "w.fifo", "d.fifo"); err == nil || !strings.Contains(err.Error(), "w.fifo") {
		t.Fatalf("ResolveQueues() error = %v, want an error naming w.fifo", err)
	}
}

// Covers: HTTP-08
func TestQueueChecker_ProbesTheWagerQueue(t *testing.T) {
	c := awsclient.NewQueueChecker(nil, &awsclient.Queues{})
	if c.Name() != "sqs" {
		t.Fatalf("Name() = %q, want sqs", c.Name())
	}
	if err := c.Check(t.Context()); err == nil {
		t.Fatal("Check() before start = nil, want 'queues not resolved' error")
	}
}
