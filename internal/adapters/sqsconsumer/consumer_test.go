package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)

// fakeQueue answers the receives with batches, then with receiveErr or empty
// batches, and records the other calls.
type fakeQueue struct {
	QueueAPI
	mu         sync.Mutex
	batches    [][]types.Message
	receiveErr error
	deleteErr  error
	receives   []time.Time
	deletes    atomic.Int32
}

func (q *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.mu.Lock()
	q.receives = append(q.receives, time.Now())
	var batch []types.Message
	if len(q.batches) > 0 {
		batch, q.batches = q.batches[0], q.batches[1:]
	}
	err := q.receiveErr
	q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (q *fakeQueue) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	q.deletes.Add(1)
	return &sqs.DeleteMessageOutput{}, q.deleteErr
}

func (q *fakeQueue) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"ApproximateNumberOfMessages": "0"}}, nil
}

func (q *fakeQueue) receiveTimes() []time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]time.Time(nil), q.receives...)
}

// countingMetrics counts what the consumer reports.
type countingMetrics struct {
	receiveFailed, deleteFailed, duplicates atomic.Int32
}

func (*countingMetrics) Received()                       {}
func (*countingMetrics) Processed(string, time.Duration) {}
func (m *countingMetrics) Duplicate(string)              { m.duplicates.Add(1) }
func (*countingMetrics) Retried(string)                  {}
func (*countingMetrics) SentToDLQ(string)                {}
func (*countingMetrics) DLQDepth(string, int)            {}
func (m *countingMetrics) ReceiveFailed()                { m.receiveFailed.Add(1) }
func (m *countingMetrics) DeleteFailed()                 { m.deleteFailed.Add(1) }

type upPinger struct{}

func (upPinger) Ping(context.Context) error { return nil }

type duplicateProcessor struct{}

func (duplicateProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	return app.ConsumeResult{Duplicate: true}, nil
}

func unitConsumer(t *testing.T, q *fakeQueue, m *countingMetrics, waitTime time.Duration) *Consumer {
	t.Helper()
	c := NewConsumer(q, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, duplicateProcessor{}, upPinger{}, m,
		slog.New(slog.DiscardHandler), Options{
			Pollers: 1, ReceiveBatch: 10, WaitTime: waitTime, Visibility: 5 * time.Second,
			ProcessingTimeout: 3 * time.Second, MaxInFlight: 4, RetryMaxDelay: time.Second,
			ShutdownTimeout: time.Second, DLQName: "dlq",
		})
	c.Start(t.Context())
	t.Cleanup(func() { _ = c.Stop(context.WithoutCancel(t.Context())) })
	return c
}

// Covers: SQS-10 (spec M5 §2.2, decision 19)
// Sensitivity: without the pause → "280147 empty receives in 350 ms".
func TestShortPollingPauses(t *testing.T) {
	q := &fakeQueue{}
	c := unitConsumer(t, q, &countingMetrics{}, 0)
	time.Sleep(350 * time.Millisecond)
	if err := c.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(q.receiveTimes()); n < 2 || n > 5 {
		t.Fatalf("%d empty receives in 350 ms, want one every 100 ms", n)
	}
}

// Covers: SQS-05 (messaging.md §4.2)
// Sensitivity: without DeleteFailed → "delete failures 0".
func TestDeleteFailureIsCounted(t *testing.T) {
	msg := types.Message{
		MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(validBody),
		Attributes: map[string]string{"MessageGroupId": "g"},
	}
	q := &fakeQueue{batches: [][]types.Message{{msg}}, deleteErr: errors.New("throttled")}
	m := &countingMetrics{}
	unitConsumer(t, q, m, 0)
	deadline := time.Now().Add(2 * time.Second)
	for m.deleteFailed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.deleteFailed.Load() != 1 || m.duplicates.Load() != 1 || q.deletes.Load() != 1 {
		t.Fatalf("delete failures %d, duplicates %d, deletes %d; want 1 each", m.deleteFailed.Load(), m.duplicates.Load(), q.deletes.Load())
	}
}

// Covers: SQS-07 (messaging.md §4.3)
// Sensitivity: without the wait → thousands of receives in 1.5 s.
func TestReceiveFailureBackoff(t *testing.T) {
	q := &fakeQueue{receiveErr: errors.New("connection refused")}
	m := &countingMetrics{}
	c := unitConsumer(t, q, m, time.Second)
	time.Sleep(1500 * time.Millisecond)
	if err := c.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	times := q.receiveTimes()
	if len(times) != 2 || times[1].Sub(times[0]) < time.Second || m.receiveFailed.Load() != 2 {
		t.Fatalf("%d receives and %d failures in 1.5 s, want 2 receives 1 s apart", len(times), m.receiveFailed.Load())
	}
}
