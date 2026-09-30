package references_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// fakeResolver scripts a Resolver: claim decides what each Claim returns.
type fakeResolver struct {
	mu      sync.Mutex
	claims  []time.Time
	claim   func(call int) ([]app.PendingReference, error)
	resolve func(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error)
	pending int
	counted atomic.Int32
}

func (f *fakeResolver) Claim(context.Context, int) ([]app.PendingReference, error) {
	f.mu.Lock()
	f.claims = append(f.claims, time.Now())
	call := len(f.claims)
	f.mu.Unlock()
	return f.claim(call)
}

func (f *fakeResolver) Resolve(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error) {
	if f.resolve == nil {
		return app.ResolveResult{Outcome: app.ResolveProcessed, TransactionID: ref.ID}, nil
	}
	return f.resolve(ctx, ref)
}

func (f *fakeResolver) CountPending(context.Context) (int, error) {
	f.counted.Add(1)
	return f.pending, nil
}

func (f *fakeResolver) claimTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.claims...)
}

type fakeMetrics struct{ retried, expired, pending atomic.Int32 }

func (m *fakeMetrics) ReferenceRetried()      { m.retried.Add(1) }
func (m *fakeMetrics) ReferenceExpired()      { m.expired.Add(1) }
func (m *fakeMetrics) ReferencePending(n int) { m.pending.Store(int32(n)) }

func refs(ids ...string) []app.PendingReference {
	out := make([]app.PendingReference, len(ids))
	for i, id := range ids {
		out[i] = app.PendingReference{ID: id, WalletID: "wallet-" + id}
	}
	return out
}

// run starts the worker; stop cancels it and waits for Run to return.
func run(t *testing.T, r references.Resolver, m references.Metrics, opts references.Options) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	w := references.NewWorker(r, m, slog.New(slog.DiscardHandler), opts)
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after the cancellation")
		}
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s: not reached within %v", what, timeout)
}

// Covers: OPS-12, TX-09 (spec M6, decision 1)
//
// A full batch without errors is claimed again at once; with a poll interval
// of an hour, only that can produce three claims in two seconds.
func TestWorkerRepeatsFullCleanBatch(t *testing.T) {
	t.Parallel()
	f := &fakeResolver{claim: func(int) ([]app.PendingReference, error) { return refs("a", "b"), nil }}
	stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 2, PollInterval: time.Hour})
	defer stop()
	eventually(t, 2*time.Second, "three claims", func() bool { return len(f.claimTimes()) >= 3 })
}

// Covers: OPS-12 (spec M6, decision 1)
func TestWorkerWaitsBetweenBatches(t *testing.T) {
	t.Parallel()
	cases := map[string]*fakeResolver{
		"an empty batch": {claim: func(int) ([]app.PendingReference, error) { return nil, nil }},
		"a short batch":  {claim: func(int) ([]app.PendingReference, error) { return refs("a"), nil }},
		"a full batch with an error": {
			claim: func(int) ([]app.PendingReference, error) { return refs("a", "b"), nil },
			resolve: func(_ context.Context, ref app.PendingReference) (app.ResolveResult, error) {
				if ref.ID == "a" {
					return app.ResolveResult{}, context.DeadlineExceeded
				}
				return app.ResolveResult{Outcome: app.ResolveProcessed}, nil
			},
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 2, PollInterval: 300 * time.Millisecond})
			defer stop()
			eventually(t, time.Second, "the first claim", func() bool { return len(f.claimTimes()) == 1 })
			// A negative window: nothing may happen before the poll interval (the only sleep of this file).
			time.Sleep(150 * time.Millisecond)
			if n := len(f.claimTimes()); n != 1 {
				t.Fatalf("%d claims before the poll interval, want 1", n)
			}
			eventually(t, 2*time.Second, "the second claim", func() bool { return len(f.claimTimes()) >= 2 })
		})
	}
}

// Covers: OPS-12 (spec M6, decision 11; ARCHITECTURE.md §12)
//
// The database is down for the first claim: the worker waits 1 s and tries
// again; after a success, the next failure waits 1 s again, not 2 s.
func TestWorkerClaimBackoff(t *testing.T) {
	t.Parallel()
	f := &fakeResolver{claim: func(call int) ([]app.PendingReference, error) {
		if call == 1 || call == 3 {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}}
	stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 1, PollInterval: 10 * time.Millisecond})
	defer stop()
	eventually(t, 6*time.Second, "four claims", func() bool { return len(f.claimTimes()) >= 4 })

	at := f.claimTimes()
	for _, gap := range []struct {
		name     string
		from, to int
	}{{"after the first failure", 0, 1}, {"after the failure that follows a success", 2, 3}} {
		got := at[gap.to].Sub(at[gap.from])
		if got < 950*time.Millisecond || got > 1800*time.Millisecond {
			t.Errorf("%s: waited %v, want about 1s", gap.name, got)
		}
	}
}

// Covers: OBS-03 (spec M6, decision 13)
func TestWorkerMetrics(t *testing.T) {
	t.Parallel()
	byID := map[string]app.ResolveResult{
		"resched":   {Outcome: app.ResolveRescheduled},
		"expired":   {Outcome: app.ResolveRejected, FailureCode: wagering.FailureReferenceNotFound},
		"rejected":  {Outcome: app.ResolveRejected, FailureCode: wagering.FailureAlreadyReversed},
		"processed": {Outcome: app.ResolveProcessed},
		"skipped":   {Outcome: app.ResolveSkipped},
	}
	f := &fakeResolver{
		pending: 7,
		claim: func(call int) ([]app.PendingReference, error) {
			if call == 1 {
				return refs("resched", "expired", "rejected", "processed", "skipped"), nil
			}
			return nil, nil
		},
		resolve: func(_ context.Context, ref app.PendingReference) (app.ResolveResult, error) { return byID[ref.ID], nil },
	}
	m := &fakeMetrics{}
	stop := run(t, f, m, references.Options{BatchSize: 10, PollInterval: time.Millisecond})
	defer stop()
	eventually(t, 2*time.Second, "the batch resolved", func() bool { return len(f.claimTimes()) >= 20 })

	if m.retried.Load() != 1 || m.expired.Load() != 1 || m.pending.Load() != 7 {
		t.Fatalf("retried %d expired %d pending %d, want 1 1 7", m.retried.Load(), m.expired.Load(), m.pending.Load())
	}
	// Twenty claims took a few milliseconds: the gauge is refreshed once a second at most.
	if n := f.counted.Load(); n != 1 {
		t.Fatalf("CountPending called %d times, want 1", n)
	}
}

// Covers: FX-03, OPS-12 (spec M6, decision 10)
//
// Not parallel: goleak sees every goroutine of the process.
//
// On the cancellation, the item in flight finishes with a context that is
// still alive, and no other begins.
func TestWorkerStop(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	entered, release := make(chan struct{}), make(chan struct{})
	var resolved atomic.Int32
	var itemCtxAlive atomic.Bool
	f := &fakeResolver{
		claim: func(call int) ([]app.PendingReference, error) {
			if call == 1 {
				return refs("a", "b", "c"), nil
			}
			return nil, nil
		},
		resolve: func(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error) {
			if resolved.Add(1) == 1 {
				close(entered)
				<-release
			}
			itemCtxAlive.Store(ctx.Err() == nil)
			return app.ResolveResult{Outcome: app.ResolveProcessed}, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	w := references.NewWorker(f, &fakeMetrics{}, slog.New(slog.DiscardHandler), references.Options{BatchSize: 10, PollInterval: time.Hour})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned with an item in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the item in flight finished")
	}
	if n := resolved.Load(); n != 1 {
		t.Fatalf("%d items resolved, want only the one in flight", n)
	}
	if !itemCtxAlive.Load() {
		t.Fatal("the item in flight saw its context canceled")
	}
}
