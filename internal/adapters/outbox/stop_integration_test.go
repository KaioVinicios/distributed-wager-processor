//go:build integration

package outbox_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
)

// blockingSink holds the first publication until release is closed.
type blockingSink struct {
	outbox.Sink
	entered, release chan struct{}
	calls            atomic.Int32
}

func (s *blockingSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.Sink.Publish(ctx, e)
}

// Covers: FX-03, OUT-06 (spec M4, decision 12)
//
// Not parallel: goleak sees every goroutine of the process.
//
// On stop, the publication in flight finishes and is confirmed; no other
// begins, and the rest of the batch keeps its lease for another instance.
func TestPublisherStop(t *testing.T) {
	f := newFixture(t, "outbox_stop")
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(),
		// idle keep-alive connections of the test's SNS client, not the publisher's
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"))
	wallet := newID()
	ids := f.insert(t, balanceChanged(t, wallet, 2), balanceChanged(t, wallet, 3)) // one group: in sequence
	sink := &blockingSink{Sink: f.sink, entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := metrics()
	stop := start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, f.options("publisher")))

	<-sink.entered
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stop()
	}()
	select {
	case <-stopped:
		t.Fatal("Run returned with a publication in flight")
	case <-time.After(300 * time.Millisecond):
	}
	close(sink.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the publication in flight finished")
	}

	f.audit.WaitFor(t, ids[0])
	var published bool
	var lockedBy *string
	for i, want := range []bool{true, false} {
		if err := f.env.Owner.QueryRow(t.Context(), `SELECT published_at IS NOT NULL, locked_by
			FROM outbox_events WHERE event_id = $1`, ids[i]).Scan(&published, &lockedBy); err != nil {
			t.Fatal(err)
		}
		if published != want {
			t.Fatalf("event %d published = %v, want %v", i+1, published, want)
		}
	}
	if lockedBy == nil || *lockedBy != "publisher" || sink.calls.Load() != 1 {
		t.Fatalf("second event locked by %v after %d publications; want it still leased and never sent", lockedBy, sink.calls.Load())
	}
}
