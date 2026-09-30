//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

// fixture is a database and an events topic of the test's own: the claim sees
// the whole table, so publishers of different tests never share events.
type fixture struct {
	env   *testkit.Env
	audit *testkit.Audit
	sink  *outbox.SNSSink
	uow   *postgres.UnitOfWork
}

func newFixture(t *testing.T, name string) fixture {
	t.Helper()
	env := testkit.NewTestEnv(t, name)
	root := testkit.RootAWSConfig(t)
	q, n := sqs.NewFromConfig(root), sns.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, q, n)
	audit, err := testkit.NewAudit(t.Context(), q, topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		env: env, audit: audit, sink: outbox.NewSNSSink(n, &awsclient.Topic{ARN: topic.ARN}),
		uow: postgres.NewUnitOfWork(env.App, env.Config()),
	}
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

// balanceChanged seals a WalletBalanceChanged of wallet, as the app does.
func balanceChanged(t *testing.T, wallet string, version int64) events.Envelope {
	t.Helper()
	m := func(amount string) money.Money {
		v, err := money.Parse(amount, "BRL")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ev, err := events.NewWalletBalanceChanged(events.WalletBalanceChanged{
		WalletID: wallet, TransactionID: newID(), TransactionKind: "BET", Direction: "DEBIT",
		Money: m("1.00"), BalanceBefore: m("100.00"), BalanceAfter: m("99.00"), WalletVersion: version,
		ChangedAt: events.NewTime(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.Seal(newID(), "corr-"+wallet[:8], "", ev)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// insert records the envelopes in one unit of work and returns their ids.
func (f fixture) insert(t *testing.T, envs ...events.Envelope) []string {
	t.Helper()
	ctx := t.Context()
	if err := f.uow.Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, envs...) }); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ids := make([]string, len(envs))
	for i, e := range envs {
		ids[i] = e.EventID()
	}
	return ids
}

// options are the accelerated settings of test-plan §3.3.
func (f fixture) options(owner string) outbox.Options {
	c := f.env.Config()
	return outbox.Options{
		Owner: owner, BatchSize: c.OutboxBatchSize, Lease: c.OutboxLease,
		PollInterval: c.OutboxPollInterval, Concurrency: c.OutboxConcurrency,
		RetryBaseDelay: c.OutboxRetryBaseDelay, RetryMaxDelay: c.OutboxRetryMaxDelay,
	}
}

// metrics is a registry of the publisher's own.
func metrics() (*observability.Metrics, *prometheus.Registry) {
	reg := observability.NewRegistry()
	return observability.NewMetrics(reg), reg
}

// start runs p until the returned stop is called, or the test ends.
func start(t *testing.T, p *outbox.Publisher) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("publisher did not stop within 10s")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// newPool is another pool as the app role: another publisher instance.
func (f fixture) newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), f.env.DB.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// pendingCount counts the unpublished events.
func (f fixture) pendingCount(ctx context.Context) (int, error) {
	var n int
	err := f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n)
	return n, err
}

// storedPayload is the payload column of an event.
func (f fixture) storedPayload(t *testing.T, id string) []byte {
	t.Helper()
	var payload []byte
	if err := f.env.Owner.QueryRow(t.Context(), `SELECT payload FROM outbox_events WHERE event_id = $1`, id).Scan(&payload); err != nil {
		t.Fatalf("payload of %s: %v", id, err)
	}
	return payload
}

// jsonEqual compares two JSON documents by content.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	decode := func(raw []byte) any {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// countingSink counts the successful publications of its publisher.
type countingSink struct {
	outbox.Sink
	n atomic.Int64
}

func (c *countingSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if err := c.Sink.Publish(ctx, e); err != nil {
		return err
	}
	c.n.Add(1)
	return nil
}

var discard = slog.New(slog.DiscardHandler)
