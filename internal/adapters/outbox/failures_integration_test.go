//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// flakySink fails the first failures calls and records when each call began.
type flakySink struct {
	outbox.Sink
	failures int

	mu    sync.Mutex
	calls []time.Time
}

func (s *flakySink) Publish(ctx context.Context, e app.PendingEvent) error {
	s.mu.Lock()
	s.calls = append(s.calls, time.Now())
	n := len(s.calls)
	s.mu.Unlock()
	if n <= s.failures {
		return fmt.Errorf("sns: injected failure %d", n)
	}
	return s.Sink.Publish(ctx, e)
}

func (s *flakySink) callTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

// metricIs reports whether the registry exposes exactly want for the metric.
func metricIs(reg *prometheus.Registry, want, name string) bool {
	return testutil.GatherAndCompare(reg, strings.NewReader(want), name) == nil
}

// Covers: OUT-04 (I05c)
//
// A publication that fails is retried with backoff until it succeeds: every
// failure counts an attempt and reschedules the event base × 2^attempts later.
func TestOutboxRetryBackoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05c")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	sink := &flakySink{Sink: f.sink, failures: 3}
	m, reg := metrics()
	opts := f.options("publisher")
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, opts))

	f.audit.WaitFor(t, id)
	calls := sink.callTimes()
	if len(calls) != 4 {
		t.Fatalf("Publish calls = %d, want 3 failures and 1 success", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		want := opts.RetryBaseDelay << (i - 1) // 100 ms, 200 ms, 400 ms
		if gap := calls[i].Sub(calls[i-1]); gap < want {
			t.Errorf("attempt %d came %v after the previous one, want at least %v", i+1, gap, want)
		}
	}
	testkit.Eventually(t, 5*time.Second, "event confirmed", func(ctx context.Context) (bool, error) {
		var attempts int
		var published bool
		var lastError string
		err := f.env.Owner.QueryRow(ctx, `SELECT attempts, published_at IS NOT NULL, COALESCE(last_error, '')
			FROM outbox_events WHERE event_id = $1`, id).Scan(&attempts, &published, &lastError)
		if published && (attempts != 3 || lastError != "sns: injected failure 3") {
			return false, fmt.Errorf("attempts %d, last_error %q; want 3 and the last failure", attempts, lastError)
		}
		return published, err
	})
	if want := "# HELP outbox_publish_failures_total Failed publication attempts of outbox events.\n" +
		"# TYPE outbox_publish_failures_total counter\n" +
		"outbox_publish_failures_total{event_type=\"WalletBalanceChanged\"} 3\n"; !metricIs(reg, want, "outbox_publish_failures_total") {
		t.Fatal("outbox_publish_failures_total is not 3")
	}
}

// Covers: OUT-03, OUT-06 (I05d)
//
// Abandoned work is taken over: an event whose lease expired is reclaimed and
// published; an event with a live lease is left alone until the lease expires.
func TestOutboxLeaseRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05d")
	ids := f.insert(t, balanceChanged(t, newID(), 2), balanceChanged(t, newID(), 2))
	abandoned, busy := ids[0], ids[1]
	for id, lease := range map[string]string{abandoned: "-1 second", busy: "2 seconds"} {
		if _, err := f.env.Owner.Exec(t.Context(), `UPDATE outbox_events
			SET locked_by = 'dead-instance', locked_until = now() + $2::interval WHERE event_id = $1`, id, lease); err != nil {
			t.Fatal(err)
		}
	}
	m, reg := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), f.sink, m, discard, f.options("publisher")))

	f.audit.WaitFor(t, abandoned)
	f.audit.Absent(t, time.Second, busy)
	f.audit.WaitFor(t, busy)
	testkit.Eventually(t, 5*time.Second, "both reclaims counted", func(context.Context) (bool, error) {
		return metricIs(reg, "# HELP outbox_lease_reclaims_total Outbox events whose expired lease was taken over by a publisher.\n"+
			"# TYPE outbox_lease_reclaims_total counter\noutbox_lease_reclaims_total 2\n", "outbox_lease_reclaims_total"), nil
	})
}

// flakyStore fails the first claims, as an unavailable database would, and
// records when each claim began.
type flakyStore struct {
	app.OutboxStore
	failures int

	mu     sync.Mutex
	claims []time.Time
}

func (s *flakyStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.PendingEvent, error) {
	s.mu.Lock()
	s.claims = append(s.claims, time.Now())
	n := len(s.claims)
	s.mu.Unlock()
	if n <= s.failures {
		return nil, errors.New("postgres: 08006")
	}
	return s.OutboxStore.Claim(ctx, owner, lease, limit)
}

func (s *flakyStore) claimTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.claims...)
}

// Covers: OUT-04 (messaging.md §5.3, spec M4 decision 11)
//
// A failed claim never stops the publisher: it waits 1 s, then 2 s, and so on
// up to 30 s, and claims again.
func TestPublisherSurvivesClaimFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_claimfail")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	store := &flakyStore{OutboxStore: postgres.NewOutboxStore(f.env.App), failures: 2}
	m, _ := metrics()
	start(t, outbox.NewPublisher(store, f.sink, m, discard, f.options("publisher")))

	f.audit.WaitFor(t, id)
	claims := store.claimTimes()
	if len(claims) < 3 {
		t.Fatalf("claims = %d, want the 2 failures and a success", len(claims))
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second} {
		if gap := claims[i+1].Sub(claims[i]); gap < want {
			t.Errorf("claim %d came %v after the failed one, want at least %v", i+2, gap, want)
		}
	}
}

// switchSink fails while down is set.
type switchSink struct {
	outbox.Sink
	down atomic.Bool
}

func (s *switchSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if s.down.Load() {
		return errors.New("sns: unavailable")
	}
	return s.Sink.Publish(ctx, e)
}

// Covers: OUT-04, OBS-03 (spec M4, decision 13)
//
// While the broker is down, the backlog gauges show the pending event; once
// it is back and the event is published, they return to zero.
func TestOutboxBacklogGauges(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_backlog")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	sink := &switchSink{Sink: f.sink}
	sink.down.Store(true)
	m, reg := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, f.options("publisher")))

	pending := func(n int) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) {
			return metricIs(reg, fmt.Sprintf("# HELP outbox_pending_events Outbox events not yet published.\n"+
				"# TYPE outbox_pending_events gauge\noutbox_pending_events %d\n", n), "outbox_pending_events"), nil
		}
	}
	testkit.Eventually(t, 5*time.Second, "backlog shows the pending event", pending(1))
	sink.down.Store(false)
	f.audit.WaitFor(t, id)
	testkit.Eventually(t, 5*time.Second, "backlog back to zero", pending(0))
}
