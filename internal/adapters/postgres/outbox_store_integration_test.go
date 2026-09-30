//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// storeFixture is a database of its own: the claim sees the whole table.
type storeFixture struct {
	t     *testing.T
	env   *testkit.Env
	store *postgres.OutboxStore
}

// due inserts an unpublished event, due now, and returns its id.
func (f storeFixture) due(ctx context.Context, extra ...any) string {
	f.t.Helper()
	id := newID()
	if err := insert(ctx, f.env.Owner, "outbox_events", outboxRow(id, newID()).with(extra...)); err != nil {
		f.t.Fatalf("insert event: %v", err)
	}
	return id
}

// settle publishes every pending event, so the next case starts empty.
func (f storeFixture) settle(ctx context.Context) {
	f.t.Helper()
	if _, err := f.env.Owner.Exec(ctx, `UPDATE outbox_events
		SET published_at = now(), locked_by = NULL, locked_until = NULL WHERE published_at IS NULL`); err != nil {
		f.t.Fatalf("settle: %v", err)
	}
}

// outboxState is the publication columns of one event; leaseIn and nextIn are
// milliseconds from the database now (locked_until − now, next_attempt_at − now).
type outboxState struct {
	lockedBy        *string
	leaseIn, nextIn int64
	attempts        int
	published       bool
	lastError       *string
}

func (f storeFixture) state(ctx context.Context, id string) outboxState {
	f.t.Helper()
	var s outboxState
	if err := f.env.Owner.QueryRow(ctx, `SELECT locked_by,
		COALESCE(EXTRACT(EPOCH FROM locked_until - now()) * 1000, 0)::bigint,
		(EXTRACT(EPOCH FROM next_attempt_at - now()) * 1000)::bigint,
		attempts, published_at IS NOT NULL, last_error FROM outbox_events WHERE event_id = $1`, id).
		Scan(&s.lockedBy, &s.leaseIn, &s.nextIn, &s.attempts, &s.published, &s.lastError); err != nil {
		f.t.Fatalf("read event %s: %v", id, err)
	}
	return s
}

func ids(events []app.PendingEvent) []string {
	out := make([]string, 0, len(events))
	for i := range events {
		out = append(out, events[i].EventID)
	}
	slices.Sort(out)
	return out
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

// Covers: OUT-03, OUT-04, OUT-06 (I18: outbox store, D-13)
func TestOutboxStore(t *testing.T) {
	t.Parallel()
	e := testkit.NewTestEnv(t, "outboxstore")
	f := storeFixture{t: t, env: e, store: postgres.NewOutboxStore(e.App)}
	ctx := t.Context()
	const lease = 30 * time.Second

	t.Run("claim leases only due, unleased, unpublished events", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		wallet := newID()
		due := f.due(ctx, "message_group_id", wallet, "correlation_id", "corr-claim")
		f.due(ctx, "next_attempt_at", time.Now().Add(time.Hour))                             // scheduled later
		f.due(ctx, "published_at", ts)                                                       // already published
		f.due(ctx, "locked_by", "other-instance", "locked_until", time.Now().Add(time.Hour)) // live lease

		got, err := f.store.Claim(ctx, "me", lease, 10)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Claim = %v, want only %s", ids(got), due)
		}
		ev := got[0]
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload["eventId"] != due {
			t.Fatalf("payload %s (%v), want the stored JSON", ev.Payload, err)
		}
		if ev.EventID != due || ev.MessageGroupID != wallet || ev.EventType != "WalletBalanceChanged" ||
			ev.EventVersion != 1 || ev.CorrelationID != "corr-claim" || !ev.OccurredAt.Equal(ts) ||
			ev.Attempts != 0 || ev.Reclaimed {
			t.Fatalf("Claim = %+v, want the row of %s (group %s, corr-claim, occurred %v, 0 attempts, not reclaimed)", ev, due, wallet, ts)
		}
		if s := f.state(ctx, due); s.lockedBy == nil || *s.lockedBy != "me" || s.leaseIn < 29_000 || s.leaseIn > 31_000 {
			t.Fatalf("after the claim: locked_by %v, lease in %dms, want me and ~30s", s.lockedBy, s.leaseIn)
		}
		if again, err := f.store.Claim(ctx, "someone-else", lease, 10); err != nil || len(again) != 0 {
			t.Fatalf("second Claim = %v, %v; want nothing while leased", ids(again), err)
		}
	})

	t.Run("claim takes the earliest due first, up to the limit", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		first := f.due(ctx, "next_attempt_at", ts)
		second := f.due(ctx, "next_attempt_at", ts.Add(time.Second))
		f.due(ctx, "next_attempt_at", ts.Add(2*time.Second))
		got, err := f.store.Claim(ctx, "me", lease, 2)
		if err != nil || !slices.Equal(ids(got), sorted(first, second)) {
			t.Fatalf("Claim(limit 2) = %v, %v; want %v", ids(got), err, sorted(first, second))
		}
	})

	t.Run("concurrent claims take disjoint events", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		var all []string
		for range 40 {
			all = append(all, f.due(ctx))
		}
		var wg sync.WaitGroup
		claimed := make([][]app.PendingEvent, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, owner := range []string{"a", "b"} {
			wg.Go(func() {
				<-start
				claimed[i], errs[i] = f.store.Claim(ctx, owner, lease, 25)
			})
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("Claim errors: %v, %v", errs[0], errs[1])
		}
		union := append(ids(claimed[0]), ids(claimed[1])...)
		slices.Sort(union)
		if !slices.Equal(union, sorted(all...)) {
			t.Fatalf("claims took %d and %d events (%d distinct), want the 40 exactly once",
				len(claimed[0]), len(claimed[1]), len(slices.Compact(union)))
		}
	})

	t.Run("an expired lease is reclaimed", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		abandoned := f.due(ctx, "locked_by", "dead-instance", "locked_until", time.Now().Add(-time.Second), "attempts", 2)
		got, err := f.store.Claim(ctx, "me", lease, 10)
		if err != nil || len(got) != 1 || got[0].EventID != abandoned || !got[0].Reclaimed || got[0].Attempts != 2 {
			t.Fatalf("Claim = %+v, %v; want %s reclaimed with 2 attempts", got, err, abandoned)
		}
		if s := f.state(ctx, abandoned); s.lockedBy == nil || *s.lockedBy != "me" {
			t.Fatalf("locked_by = %v, want me", s.lockedBy)
		}
	})

	t.Run("only the lease owner confirms", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		id := f.due(ctx)
		if _, err := f.store.Claim(ctx, "me", lease, 10); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.store.MarkPublished(ctx, id, "other-instance"); err != nil || ok {
			t.Fatalf("MarkPublished by another owner = %v, %v; want false, nil", ok, err)
		}
		before := time.Now()
		at, ok, err := f.store.MarkPublished(ctx, id, "me")
		if err != nil || !ok || at.Before(before.Add(-time.Minute)) || at.After(time.Now().Add(time.Minute)) {
			t.Fatalf("MarkPublished = %v, %v, %v; want now, true", at, ok, err)
		}
		if s := f.state(ctx, id); !s.published || s.lockedBy != nil || s.leaseIn != 0 {
			t.Fatalf("after the confirmation: %+v, want published and unleased", s)
		}
		if _, ok, err := f.store.MarkPublished(ctx, id, "me"); err != nil || ok {
			t.Fatalf("second MarkPublished = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("a failure counts the attempt, reschedules and releases the lease", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		id := f.due(ctx)
		if _, err := f.store.Claim(ctx, "me", lease, 10); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.store.MarkFailed(ctx, id, "other-instance", time.Minute, "boom"); err != nil || ok {
			t.Fatalf("MarkFailed by another owner = %v, %v; want false, nil", ok, err)
		}
		if ok, err := f.store.MarkFailed(ctx, id, "me", time.Minute, "sns: throttled"); err != nil || !ok {
			t.Fatalf("MarkFailed = %v, %v; want true, nil", ok, err)
		}
		s := f.state(ctx, id)
		if s.attempts != 1 || s.lockedBy != nil || s.published || s.nextIn < 59_000 || s.nextIn > 61_000 ||
			s.lastError == nil || *s.lastError != "sns: throttled" {
			t.Fatalf("after the failure: %+v (last_error %v), want 1 attempt, unleased, next in ~60s", s, s.lastError)
		}
		if got, err := f.store.Claim(ctx, "me", lease, 10); err != nil || len(got) != 0 {
			t.Fatalf("Claim before the next attempt = %v, %v; want nothing", ids(got), err)
		}
	})

	t.Run("a malformed id is a lost lease, not a database error", func(t *testing.T) {
		if _, ok, err := f.store.MarkPublished(ctx, "not-a-uuid", "me"); err != nil || ok {
			t.Fatalf("MarkPublished = %v, %v; want false, nil", ok, err)
		}
		if ok, err := f.store.MarkFailed(ctx, "not-a-uuid", "me", time.Second, "x"); err != nil || ok {
			t.Fatalf("MarkFailed = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("backlog", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		if b, err := f.store.Backlog(ctx); err != nil || b != (app.OutboxBacklog{}) {
			t.Fatalf("empty Backlog = %+v, %v; want zero", b, err)
		}
		f.due(ctx, "occurred_at", time.Now().Add(-10*time.Second))
		f.due(ctx, "occurred_at", time.Now().Add(-5*time.Second))
		f.due(ctx, "occurred_at", time.Now().Add(-time.Hour), "published_at", time.Now())
		b, err := f.store.Backlog(ctx)
		if err != nil || b.Pending != 2 || b.OldestAge < 9*time.Second || b.OldestAge > 15*time.Second {
			t.Fatalf("Backlog = %+v, %v; want 2 pending, oldest ~10s", b, err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := f.store.Claim(canceled, "me", lease, 10); err == nil {
			t.Fatal("Claim with a canceled context = nil error")
		}
	})
}
