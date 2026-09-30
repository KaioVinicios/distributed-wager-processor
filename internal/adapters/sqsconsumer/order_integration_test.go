//go:build integration

package sqsconsumer_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: SQS-07 (messaging.md §4.2)
// Sensitivity: the group went on after a retry → "order = [bet-2 bet-3 bet-1]".
//
// A transient failure of the first message of a group releases the rest of
// the group: nothing of the group runs before the first one concludes.
func TestGroupOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	real := newConsumeWager(newUoW())
	var mu sync.Mutex
	var failed bool
	var order []string
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		mu.Lock()
		if *m.Input.ExternalTransactionID == "bet-1" && !failed {
			failed = true
			mu.Unlock()
			return app.ConsumeResult{}, errors.New("forced transient failure")
		}
		order = append(order, *m.Input.ExternalTransactionID)
		mu.Unlock()
		return real.Execute(ctx, m)
	})
	for i, amount := range []string{"10.00", "20.00", "30.00"} {
		ext := "bet-" + string(rune('1'+i))
		f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", amount, ext, ""), w.id)
	}
	f.start(t, consumerOpts{proc: proc})

	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "bet-1,bet-2,bet-3" {
		t.Fatalf("order = %v, want bet-1,bet-2,bet-3", order)
	}
	if b := balance(t, w.id); b != "40.00" {
		t.Fatalf("balance = %s, want 40.00", b)
	}
}

// Covers: SQS-07 (spec M5, decision 8)
// Sensitivity: without the deadline check → "sqs_retries_total{deadline_release} = 0".
//
// A slow first message leaves the second one of its group with less
// visibility than its processing deadline: it is released unprocessed and
// concluded on a later receive.
func TestDeadlineRelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	real := newConsumeWager(newUoW())
	var slowOnce sync.Once
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		if *m.Input.ExternalTransactionID == "bet-1" {
			slowOnce.Do(func() { time.Sleep(2500 * time.Millisecond) }) // visibility 5 s − 2.5 s < processing 3 s
		}
		return real.Execute(ctx, m)
	})
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "10.00", "bet-1", ""), w.id)
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "20.00", "bet-2", ""), w.id)
	f.start(t, consumerOpts{proc: proc})

	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := f.metric(t, "sqs_retries_total", "reason=deadline_release"); n < 1 {
		t.Fatalf("sqs_retries_total{deadline_release} = %v, want the second message released", n)
	}
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}

// Covers: SQS-07 (messaging.md §4.3; spec M5, decision 6)
// Sensitivity: a gate that never pauses → "while paused: 1 processings and 2
// new receives".
func TestHealthGatePauses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	pinger := &switchPinger{}
	pinger.down.Store(true)
	real := newConsumeWager(newUoW())
	var calls atomic.Int32
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		if calls.Add(1) == 1 {
			return app.ConsumeResult{}, errors.New("connection refused")
		}
		return real.Execute(ctx, m)
	})
	spy := &queueSpy{QueueAPI: f.sqs}
	opts := options()
	opts.RetryMaxDelay = 3 * time.Second // the failed message is visible again 2 s later
	f.start(t, consumerOpts{proc: proc, api: spy, pinger: pinger, opts: &opts})
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "30.00", "bet-1", ""), w.id)

	testkit.Eventually(t, 10*time.Second, "the first transient failure", func(context.Context) (bool, error) {
		return calls.Load() == 1, nil
	})
	// The gate holds new receives; a long poll already in flight (wait 1 s)
	// still ends normally, before the message is visible again.
	time.Sleep(1200 * time.Millisecond)
	receives := spy.receives.Load()
	time.Sleep(1500 * time.Millisecond) // past the 2 s retry delay: the message is visible again
	if calls.Load() != 1 || spy.receives.Load() != receives {
		t.Fatalf("while paused: %d processings and %d new receives, want none", calls.Load()-1, spy.receives.Load()-receives)
	}

	pinger.down.Store(false)
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}
