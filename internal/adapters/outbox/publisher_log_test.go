package outbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// logStore fails the confirmation and accepts the failure record.
type logStore struct{ app.OutboxStore }

func (logStore) MarkPublished(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, errors.New("connection reset")
}

func (logStore) MarkFailed(context.Context, string, string, time.Duration, string) (bool, error) {
	return true, nil
}

type logSink struct{ err error }

func (s logSink) Publish(context.Context, app.PendingEvent) error { return s.err }

type nopPublisherMetrics struct{}

func (nopPublisherMetrics) Published(string, time.Duration) {}
func (nopPublisherMetrics) PublishFailed(string)            {}
func (nopPublisherMetrics) LeaseReclaimed()                 {}
func (nopPublisherMetrics) Backlog(int, time.Duration)      {}

// Covers: OBS-01 (U26; review of M7, finding 2)
// Sensitivity: fail logging with p.log and only the eventId → the line lacks walletId and correlationId.
func TestPublisherLogsCarryTheEventIDs(t *testing.T) {
	e := app.PendingEvent{
		EventID: "evt-1", MessageGroupID: "wallet-1", EventType: "WalletBalanceChanged",
		CorrelationID: "corr-1", OccurredAt: time.Now(),
	}
	cases := []struct {
		name, msg string
		sinkErr   error
	}{
		{"publication failed", "outbox publish failed", errors.New("throttled")},
		{"confirmation failed", "outbox confirmation failed", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			p := NewPublisher(logStore{}, logSink{err: tc.sinkErr}, nopPublisherMetrics{}, slog.New(slog.NewJSONHandler(&logs, nil)),
				Options{
					Owner: "o", BatchSize: 1, Lease: time.Second, PollInterval: time.Second, Concurrency: 1,
					RetryBaseDelay: time.Second, RetryMaxDelay: time.Minute,
				})
			p.publish(t.Context(), e)
			var line string
			for l := range strings.Lines(logs.String()) {
				if strings.Contains(l, `"msg":"`+tc.msg+`"`) {
					line = l
				}
			}
			for _, want := range []string{`"eventId":"evt-1"`, `"walletId":"wallet-1"`, `"correlationId":"corr-1"`} {
				if !strings.Contains(line, want) {
					t.Errorf("log %q lacks %s", line, want)
				}
			}
		})
	}
}
