//go:build integration

package sqsconsumer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// receiveNow receives one message of the queue with another consumer's
// credentials, as another instance would, within wait.
func (f *fixture) receiveNow(t *testing.T, wait time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		out, err := f.sqs.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(f.queues.WagerURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0, VisibilityTimeout: 30,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 1 {
			return aws.ToString(out.Messages[0].Body), true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// shortPolling are the options of the shutdown tests. A long poll canceled
// by the client stays open in the broker until its wait ends and may take a
// message released meanwhile, hiding it for a visibility timeout (found in the
// plan validation, spec M5 §2.2): short polling leaves no such poll, so the
// tests see the release itself.
func shortPolling() *sqsconsumer.Options {
	opts := options()
	opts.WaitTime = 0
	return &opts
}

// Covers: SQS-09, FX-03 (messaging.md §4.5)
// Sensitivity: processing what was received and not started → "balance =
// 70.00, want the message in flight concluded (90.00)"; without the abort at
// the deadline → "Stop took 3s".
func TestConsumerShutdown(t *testing.T) {
	// Not parallel: goleak sees every goroutine of the process.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(),
		// idle keep-alive connections of the test's SQS clients, not the consumer's
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"))

	t.Run("waits for the message in flight and releases the rest of its group", func(t *testing.T) {
		f := newFixture(t)
		w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
		real := newConsumeWager(newUoW())
		started, release := make(chan struct{}), make(chan struct{})
		proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
			if *m.Input.ExternalTransactionID == "bet-1" {
				close(started)
				<-release
			}
			return real.Execute(ctx, m)
		})
		second := wager(t, "msg-"+testkit.NewID(), w, p, "BET", "20.00", "bet-2", "")
		f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "10.00", "bet-1", ""), w.id)
		f.send(t, second, w.id)
		_, stop := f.start(t, consumerOpts{proc: proc, opts: shortPolling()})
		<-started

		stopped := make(chan error, 1)
		go func() { stopped <- stop() }()
		select {
		case err := <-stopped:
			t.Fatalf("Stop returned %v with a message in flight", err)
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		if err := <-stopped; err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if b := balance(t, w.id); b != "90.00" {
			t.Fatalf("balance = %s, want the message in flight concluded (90.00)", b)
		}
		if body, ok := f.receiveNow(t, time.Second); !ok || body != second {
			t.Fatalf("another consumer received %q, %v; want the second message at once", body, ok)
		}
	})

	t.Run("at the deadline the message in flight is canceled and released", func(t *testing.T) {
		f := newFixture(t)
		proc := processorFunc(func(ctx context.Context, _ app.WagerMessage) (app.ConsumeResult, error) {
			<-ctx.Done() // a transaction that only ends with its context
			return app.ConsumeResult{}, ctx.Err()
		})
		opts := shortPolling()
		opts.ShutdownTimeout = 300 * time.Millisecond
		var started atomic.Bool
		_, stop := f.start(t, consumerOpts{proc: processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
			started.Store(true)
			return proc(ctx, m)
		}), opts: opts})
		body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
		f.send(t, body, "group-1")
		testkit.Eventually(t, 10*time.Second, "the message in flight", func(ctx context.Context) (bool, error) {
			n, err := testkit.QueueDepth(ctx, f.sqs, f.queues.WagerURL)
			return n == 1 && started.Load(), err
		})

		begin := time.Now()
		if err := stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if took := time.Since(begin); took > 2*time.Second {
			t.Fatalf("Stop took %v, want the deadline (300 ms) plus the release", took)
		}
		if got, ok := f.receiveNow(t, time.Second); !ok || got != body {
			t.Fatalf("another consumer received %q, %v; want the canceled message at once", got, ok)
		}
	})
}
