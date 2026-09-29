package testkit

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/config"
)

// Env is the isolated infrastructure of one test package (test-plan §3.2). M2
// provides the database; M4 and M5 add the queues and the topic.
type Env struct {
	DB    *Database
	App   *pgxpool.Pool // pda_app, the application role
	Owner *pgxpool.Pool // pda_owner, for setups and assertions the app role cannot do
}

// NewEnv is called from TestMain, which has no testing.TB. cleanup closes the
// pools and drops the database.
func NewEnv(ctx context.Context, pkg string) (env *Env, cleanup func(), err error) {
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
	cleanup = func() {
		app.Close()
		owner.Close()
		if err := drop(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: drop database:", err)
		}
	}
	return &Env{DB: db, App: app, Owner: owner}, cleanup, nil
}

// Config points at the isolated database with the accelerated times of
// test-plan §3.3. Only the database part is filled; later milestones add the
// rest as their components need it.
func (e *Env) Config() config.Config {
	return config.Config{
		LogLevel:        "error",
		ShutdownTimeout: 5 * time.Second,
		DatabaseURL:     e.DB.AppURL,
		DBMaxConns:      4,
		DBLockTimeout:   2 * time.Second,
	}
}
