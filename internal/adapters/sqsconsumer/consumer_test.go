package sqsconsumer

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
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

func (q *fakeQueue) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (q *fakeQueue) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return &sqs.SendMessageOutput{}, nil
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
	return unitConsumerWith(t, q, m, waitTime, duplicateProcessor{}, slog.New(slog.DiscardHandler))
}

func unitConsumerWith(t *testing.T, q *fakeQueue, m *countingMetrics, waitTime time.Duration, proc Processor, log *slog.Logger) *Consumer {
	t.Helper()
	c := NewConsumer(q, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, proc, upPinger{}, m, log, Options{
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

// lockedLog collects the consumer's log lines, written from its goroutines.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// line waits for the log line whose message is msg and returns it.
func (l *lockedLog) line(t *testing.T, msg string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		logs := l.buf.String()
		l.mu.Unlock()
		for line := range strings.Lines(logs) {
			if strings.Contains(line, `"msg":"`+msg+`"`) {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line %q", msg)
	return ""
}

// failingProcessor answers every message with err.
type failingProcessor struct{ err error }

func (p failingProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	return app.ConsumeResult{}, p.err
}

// Covers: OBS-01 (U25; review of M7, finding 2)
// Sensitivity: apply logging with c.log instead of the message's logger → the lines lack messageId, walletId, providerId and correlationId.
func TestFailureLogsCarryTheMessageIDs(t *testing.T) {
	longWallet := strings.Repeat("w", 300)
	longBody := strings.Replace(validBody, "0192f291-27dd-7d3f-8071-5f8685deef37", longWallet, 1)
	cases := []struct {
		name, body, msg string
		proc            Processor
		want, absent    []string
	}{
		{
			"transient failure", validBody, "sqs message failed transiently",
			failingProcessor{errors.New("database unavailable")},
			[]string{
				`"sqsMessageId":"sqs-1"`, `"messageId":"msg-123"`, `"correlationId":"msg-123"`,
				`"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"`, `"providerId":"provider-a"`,
			},
			nil,
		},
		{
			"sent to the dlq", validBody, "sqs message sent to the dlq",
			failingProcessor{apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", errors.New("no wallet"))},
			[]string{
				`"sqsMessageId":"sqs-1"`, `"messageId":"msg-123"`, `"correlationId":"msg-123"`,
				`"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"`, `"providerId":"provider-a"`,
			},
			nil,
		},
		{
			"an unreadable message has only the broker id", "{", "sqs message sent to the dlq",
			duplicateProcessor{},
			[]string{`"sqsMessageId":"sqs-1"`},
			[]string{`"messageId"`, `"walletId"`, `"correlationId"`},
		},
		{
			"a huge wallet id is cut", longBody, "sqs message failed transiently",
			failingProcessor{errors.New("database unavailable")},
			[]string{`"walletId":"` + strings.Repeat("w", 128) + `"`},
			[]string{strings.Repeat("w", 129)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(tc.body),
				Attributes: map[string]string{"MessageGroupId": "g"},
			}
			logs := &lockedLog{}
			unitConsumerWith(t, &fakeQueue{batches: [][]types.Message{{msg}}}, &countingMetrics{}, 0, tc.proc,
				slog.New(slog.NewJSONHandler(logs, nil)))
			line := logs.line(t, tc.msg)
			for _, w := range tc.want {
				if !strings.Contains(line, w) {
					t.Errorf("log %s lacks %s", line, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(line, a) {
					t.Errorf("log %s has %s", line, a)
				}
			}
		})
	}
}
