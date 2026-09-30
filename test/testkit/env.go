package testkit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/config"
)

// Env is the isolated infrastructure of one test package (test-plan §3.2): the
// database. The queues and the events topic are created by StartApp, or by
// the TestMain of a package that needs them without the app.
type Env struct {
	DB    *Database
	App   *pgxpool.Pool // pda_app, the application role
	Owner *pgxpool.Pool // pda_owner, for setups and assertions the app role cannot do
}

// NewEnv is called from TestMain, which has no testing.TB. cleanup closes the
// pools and drops the database; its error is the caller's to report.
func NewEnv(ctx context.Context, pkg string) (env *Env, cleanup func() error, err error) {
	db, drop, err := NewDatabase(ctx, pkg)
	if err != nil {
		return nil, nil, err
	}
	app, err := pgxpool.New(ctx, db.AppURL)
	if err != nil {
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: app pool: %w", err)
	}
	owner, err := pgxpool.New(ctx, db.OwnerURL)
	if err != nil {
		app.Close()
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: owner pool: %w", err)
	}
	cleanup = func() error {
		app.Close()
		owner.Close()
		return drop()
	}
	return &Env{DB: db, App: app, Owner: owner}, cleanup, nil
}

// NewTestEnv is NewEnv for one test: a database of its own, for tests whose
// assertions cover a whole table, such as the outbox claim. The database is
// dropped at cleanup.
func NewTestEnv(tb testing.TB, name string) *Env {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), time.Minute)
	defer cancel()
	env, cleanup, err := NewEnv(ctx, name)
	if err != nil {
		tb.Fatalf("testkit.NewEnv: %v", err)
	}
	tb.Cleanup(func() {
		if err := cleanup(); err != nil {
			tb.Errorf("testkit: drop database %s: %v", env.DB.Name, err)
		}
	})
	return env
}

// Config points at the isolated database with the accelerated times of
// test-plan §3.3. The resources outside the database (queues, topic, IdP) are
// filled by whoever creates them.
func (e *Env) Config() config.Config {
	return config.Config{
		LogLevel:              "error",
		ShutdownTimeout:       5 * time.Second,
		DatabaseURL:           e.DB.AppURL,
		DBMaxConns:            4,
		DBLockTimeout:         2 * time.Second,
		ReferencePollInterval: 50 * time.Millisecond,
		ReferenceBatchSize:    50,
		OutboxBatchSize:       50,
		OutboxLease:           2 * time.Second,
		OutboxPollInterval:    100 * time.Millisecond,
		OutboxConcurrency:     8,
		OutboxRetryBaseDelay:  100 * time.Millisecond,
		OutboxRetryMaxDelay:   time.Second,
		SQSConsumerPollers:    2,
		SQSReceiveBatch:       10,
		SQSWaitTime:           time.Second,
		SQSVisibilityTimeout:  5 * time.Second,
		SQSProcessingTimeout:  3 * time.Second,
		SQSMaxInFlight:        16,
		SQSRetryMaxDelay:      time.Second,
	}
}
