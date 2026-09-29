package postgres_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/config"
)

const secret = "SuperSecret42"

func cfgWithURL(url string) config.Config {
	return config.Config{DatabaseURL: url, DBMaxConns: 2, ShutdownTimeout: time.Second}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Covers: FX-02, OBS-02
func TestNewPool_RejectsInvalidURLWithoutLeakingIt(t *testing.T) {
	_, err := postgres.NewPool(fxtest.NewLifecycle(t), cfgWithURL("postgres://pda_app:"+secret+"@localhost:5432/pda?pool_max_conns=abc"), discard())
	if err == nil {
		t.Fatal("NewPool() error = nil, want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks the password: %q", err.Error())
	}
}

// Covers: FX-02, OBS-02 (review focus: pgx errors must not leak the password)
func TestNewPool_StartFailsWhenDatabaseIsUnreachable(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	pool, err := postgres.NewPool(lc, cfgWithURL("postgres://pda_app:"+secret+"@127.0.0.1:1/pda?sslmode=disable&connect_timeout=2"), discard())
	if err != nil {
		t.Fatalf("NewPool() error = %v, want lazy pool", err)
	}
	if pool == nil {
		t.Fatal("NewPool() pool = nil")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err = lc.Start(ctx)
	if err == nil {
		_ = lc.Stop(ctx)
		t.Fatal("Start() error = nil, want ping failure")
	}
	if !strings.Contains(err.Error(), "postgres") || strings.Contains(err.Error(), secret) {
		t.Fatalf("Start() error = %q; want it to name postgres and not leak the password", err.Error())
	}
}

// Covers: HTTP-08
func TestChecker_IsNamedPostgres(t *testing.T) {
	if got := postgres.NewChecker(nil).Name(); got != "postgres" {
		t.Fatalf("Name() = %q, want postgres", got)
	}
}
