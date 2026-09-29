// Package postgres is the PostgreSQL driven adapter.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
)

const pingTimeout = 5 * time.Second

// NewPool builds the pool, pings it on start (fail fast) and closes it on stop.
func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		// The pgx parse error may echo the connection string; never wrap it.
		return nil, errors.New("postgres: DATABASE_URL could not be parsed")
	}
	pcfg.MaxConns = cfg.DBMaxConns

	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		return nil, errors.New("postgres: could not create the connection pool")
	}
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, pingTimeout)
			defer cancel()
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("postgres: ping: %w", err)
			}
			log.Info("postgres pool ready", "maxConns", pcfg.MaxConns)
			return nil
		},
		OnStop: func(context.Context) error {
			pool.Close()
			log.Info("postgres pool closed")
			return nil
		},
	})
	return pool, nil
}

// Checker reports whether PostgreSQL answers a ping.
type Checker struct{ pool *pgxpool.Pool }

// NewChecker builds the "postgres" health checker.
func NewChecker(pool *pgxpool.Pool) *Checker { return &Checker{pool: pool} }

// Name implements observability.Checker.
func (c *Checker) Name() string { return "postgres" }

// Check implements observability.Checker.
func (c *Checker) Check(ctx context.Context) error { return c.pool.Ping(ctx) }
