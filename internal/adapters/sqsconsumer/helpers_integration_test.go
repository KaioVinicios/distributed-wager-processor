//go:build integration

package sqsconsumer_test

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

// fixture is a pair of isolated queues (redrive after 3 receives) and a
// consumer over them, built without Fx.
type fixture struct {
	sqs    *sqs.Client
	queues awsclient.Queues
	reg    *prometheus.Registry
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	client := sqs.NewFromConfig(testkit.RootAWSConfig(t))
	wager, dlq := testkit.CreateQueues(t, client)
	queues, err := awsclient.ResolveQueues(t.Context(), client, wager, dlq)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{sqs: client, queues: queues, reg: observability.NewRegistry()}
}

// options are the accelerated settings of test-plan §3.3.
func options() sqsconsumer.Options {
	c := env.Config()
	return sqsconsumer.Options{
		Pollers: c.SQSConsumerPollers, ReceiveBatch: c.SQSReceiveBatch, WaitTime: c.SQSWaitTime,
		Visibility: c.SQSVisibilityTimeout, ProcessingTimeout: c.SQSProcessingTimeout, MaxInFlight: c.SQSMaxInFlight,
		RetryMaxDelay: c.SQSRetryMaxDelay, ShutdownTimeout: c.ShutdownTimeout, DLQName: "dlq",
	}
}

// consumerOpts customizes one consumer of a fixture.
type consumerOpts struct {
	proc   sqsconsumer.Processor     // nil = the real use case
	api    sqsconsumer.QueueAPI      // nil = the SQS client
	pinger sqsconsumer.Pinger        // nil = the package database
	auth   sqsconsumer.Authenticator // nil = trustingAuth
	opts   *sqsconsumer.Options      // nil = options()
}

// trustingAuth reads the token as the provider id. The tests of this package
// use a provider per test ("provider-<id>"), which the IdP does not know; the
// real IdP is exercised here by A06 and by every SQS test of test/integration.
type trustingAuth struct{}

func (trustingAuth) AuthenticateAt(_ context.Context, raw string, _ time.Time) (auth.Principal, error) {
	return auth.Principal{ProviderID: raw, Roles: []auth.Role{auth.RoleProvider}}, nil
}

// start runs a consumer until the returned stop, or the end of the test.
func (f *fixture) start(t *testing.T, o consumerOpts) (c *sqsconsumer.Consumer, stop func() error) {
	t.Helper()
	if o.proc == nil {
		o.proc = newConsumeWager(newUoW())
	}
	if o.api == nil {
		o.api = f.sqs
	}
	if o.pinger == nil {
		o.pinger = env.App
	}
	if o.auth == nil {
		o.auth = trustingAuth{}
	}
	opts := options()
	if o.opts != nil {
		opts = *o.opts
	}
	c = sqsconsumer.NewConsumer(o.api, &f.queues, o.proc, o.auth, o.pinger, observability.NewMetrics(f.reg), slog.New(slog.DiscardHandler), opts)
	c.Start(t.Context())
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
			defer cancel()
			err = c.Stop(ctx)
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return c, stop
}

func (f *fixture) send(t *testing.T, body, group string) string {
	t.Helper()
	token := cmp.Or(testkit.BodyProviderID(body), "provider-a") // read as the provider id by trustingAuth
	return testkit.SendMessage(t, f.sqs, f.queues.WagerURL, body, testkit.SendOpts{GroupID: group, Token: token})
}

// metric is the value of the counter or gauge sample of name whose labels
// contain all of labels ("k=v"), or 0; the values the tests read are counts.
func (f *fixture) metric(t *testing.T, name string, labels ...string) int {
	t.Helper()
	families, err := f.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
	metrics:
		for _, m := range fam.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}
			for _, kv := range labels {
				k, v, _ := strings.Cut(kv, "=")
				if have[k] != v {
					continue metrics
				}
			}
			if c := m.GetCounter(); c != nil {
				return int(c.GetValue())
			}
			return int(m.GetGauge().GetValue())
		}
	}
	return 0
}

func newUoW() app.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

func newConsumeWager(uow app.UnitOfWork) *app.ConsumeWager {
	reads := postgres.NewRepos(env.App)
	policy, err := wagering.NewReferenceRetryPolicy(time.Minute, time.Minute, 3, 10*time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return app.NewConsumeWager(reads, app.NewProcessWager(uow, reads, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler)))
}

// testWallet is a wallet opened through the use case.
type testWallet struct{ id, player string }

func openWallet(t *testing.T, initial string) testWallet {
	t.Helper()
	player, amount, currency := testkit.NewID(), initial, "BRL"
	w, err := app.NewOpenWallet(newUoW(), app.SystemClock{}, app.UUIDv7{}).Execute(t.Context(), app.OpenWalletInput{
		PlayerID: &player, InitialBalance: &wagering.MoneyInput{Amount: &amount, Currency: &currency},
	}, "corr-open")
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	return testWallet{id: w.ID(), player: player}
}

// wager is a WagerTransactionRequested for w; ext is also the idempotency key suffix.
func wager(t *testing.T, messageID string, w testWallet, provider, kind, amount, ext, ref string) string {
	t.Helper()
	return testkit.WagerMessage(t, messageID, testkit.WagerData{
		ProviderID: provider, ExternalTransactionID: ext, IdempotencyKey: provider + ":" + ext,
		PlayerID: w.player, WalletID: w.id, RoundID: "round-1", GameID: "game-1", Kind: kind,
		Money: &testkit.Money{Amount: amount, Currency: "BRL"}, ReferenceExternalTransactionID: ref,
	})
}

func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// balance is the stored balance of the wallet.
func balance(t *testing.T, walletID string) string {
	t.Helper()
	w, err := postgres.NewRepos(env.App).Wallets().Get(t.Context(), walletID)
	if err != nil {
		t.Fatalf("wallet %s: %v", walletID, err)
	}
	return w.Balance().String()
}

// processorFunc adapts a function to sqsconsumer.Processor.
type processorFunc func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error)

func (f processorFunc) Execute(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
	return f(ctx, m)
}

// failingOutboxUoW makes every Outbox().Insert fail permanently, the forced
// permanent failure of I03b; the FAILED of the separate transaction is written.
type failingOutboxUoW struct{ app.UnitOfWork }

func (u failingOutboxUoW) Do(ctx context.Context, fn func(app.Repos) error) error {
	return u.UnitOfWork.Do(ctx, func(r app.Repos) error { return fn(outboxFails{r}) })
}

type outboxFails struct{ app.Repos }

func (outboxFails) Outbox() app.OutboxRepository { return failingOutbox{} }

type failingOutbox struct{}

func (failingOutbox) Insert(context.Context, ...events.Envelope) error {
	return apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))
}

// queueSpy counts the receives and fails the first failSends DLQ sends.
type queueSpy struct {
	sqsconsumer.QueueAPI
	receives  atomic.Int32
	failSends atomic.Int32
}

func (q *queueSpy) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.receives.Add(1)
	return q.QueueAPI.ReceiveMessage(ctx, in, optFns...)
}

func (q *queueSpy) SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	if q.failSends.Add(-1) >= 0 {
		return nil, errors.New("dlq unavailable")
	}
	return q.QueueAPI.SendMessage(ctx, in, optFns...)
}

// switchPinger fails while down is set.
type switchPinger struct{ down atomic.Bool }

func (p *switchPinger) Ping(ctx context.Context) error {
	if p.down.Load() {
		return errors.New("connection refused")
	}
	return env.App.Ping(ctx)
}
