//go:build integration

package outbox_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-I05, OUT-03, OUT-05 (I05a)
//
// Two publishers, each with its own pool as two instances would have, share
// one outbox: every event reaches the topic with the content of the database,
// and none is left behind.
func TestOutboxConcurrentPublishers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05a")
	var ids []string
	for i := range 200 {
		ids = append(ids, f.insert(t, balanceChanged(t, fmt.Sprintf("0192f291-27dd-7d3f-8071-%012d", i%20), int64(i/20+1)))...)
	}
	sinks := []*countingSink{{Sink: f.sink}, {Sink: f.sink}}
	for i, s := range sinks {
		m, _ := metrics()
		start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.newPool(t)), s, m, discard, f.options(fmt.Sprintf("publisher-%d", i))))
	}

	got := f.audit.WaitFor(t, ids...)
	for _, id := range ids {
		if !jsonEqual(t, got[id][0].Body, f.storedPayload(t, id)) {
			t.Fatalf("event %s: delivered %s, stored %s", id, got[id][0].Body, f.storedPayload(t, id))
		}
	}
	testkit.Eventually(t, 5*time.Second, "outbox drained", func(ctx context.Context) (bool, error) {
		n, err := f.pendingCount(ctx)
		return n == 0, err
	})
	if sinks[0].n.Load() == 0 || sinks[1].n.Load() == 0 {
		t.Fatalf("publications = %d and %d, want both publishers to share the outbox", sinks[0].n.Load(), sinks[1].n.Load())
	}
}

// Covers: OUT-10, E8 (I05b)
// Sensitivity: an Outbox().Insert decorator that also published the envelope
// made Absent fail ("delivered 1 time(s), want none").
//
// The publisher only sees committed rows: while the transaction that wrote the
// event is open, nothing is published; after the commit, it is.
func TestNoPublishBeforeCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05b")
	m, _ := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), f.sink, m, discard, f.options("publisher")))

	env := balanceChanged(t, newID(), 2)
	inserted, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		ctx := context.WithoutCancel(t.Context())
		done <- f.uow.Do(ctx, func(r app.Repos) error {
			if err := r.Outbox().Insert(ctx, env); err != nil {
				return err
			}
			close(inserted)
			<-release // the commit waits for the test
			return nil
		})
	}()
	<-inserted
	f.audit.Absent(t, 2*time.Second, env.EventID())
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("commit: %v", err)
	}
	f.audit.WaitFor(t, env.EventID())
}
