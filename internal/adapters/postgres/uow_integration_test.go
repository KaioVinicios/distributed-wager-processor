//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// Covers: DB-02, CONC-01, WAL-06 (I19; D-09, D-14, D-16)
func TestUnitOfWork(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// A pool of its own, so that the acquired-connection count is this test's.
	pool, err := pgxpool.New(ctx, env.DB.AppURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool, env.Config())
	persisted := func(id string) bool {
		t.Helper()
		_, err := reads().Wallets().Get(ctx, id)
		if err != nil && !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("Get: %v", err)
		}
		return err == nil
	}
	noLeak := func() {
		t.Helper()
		if n := pool.Stat().AcquiredConns(); n != 0 {
			t.Fatalf("%d connections still acquired after the unit of work", n)
		}
	}

	t.Run("commits when fn returns nil", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		if err := uow.Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, w) }); err != nil {
			t.Fatalf("Do: %v", err)
		}
		if !persisted(w.ID()) {
			t.Fatal("wallet not committed")
		}
		noLeak()
	})

	t.Run("rolls back when fn fails", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		boom := errors.New("boom")
		err := uow.Do(ctx, func(r app.Repos) error {
			if err := r.Wallets().Insert(ctx, w); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Do = %v, want the error of fn", err)
		}
		if persisted(w.ID()) {
			t.Fatal("wallet committed despite the error")
		}
		noLeak()
	})

	t.Run("rolls back and re-panics", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		recovered := func() (p any) {
			defer func() { p = recover() }()
			_ = uow.Do(ctx, func(r app.Repos) error {
				if err := r.Wallets().Insert(ctx, w); err != nil {
					return err
				}
				panic("boom")
			})
			return nil
		}()
		if recovered != "boom" {
			t.Fatalf("recovered %v, want the panic of fn", recovered)
		}
		if persisted(w.ID()) {
			t.Fatal("wallet committed despite the panic")
		}
		noLeak()
	})

	t.Run("lock timeout is transient", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		release := lockWallet(t, w.ID())
		defer release()
		start := time.Now()
		err := uow.Do(ctx, func(r app.Repos) error {
			_, err := r.Wallets().Lock(ctx, w.ID())
			return err
		})
		elapsed := time.Since(start)
		wantKind(t, err, apperrors.KindTransient, nil)
		if !strings.Contains(err.Error(), "55P03") {
			t.Fatalf("Do = %v, want lock_not_available (55P03)", err)
		}
		if lock := env.Config().DBLockTimeout; elapsed < lock*3/4 || elapsed > 3*lock {
			t.Fatalf("waited %v for the lock, want about DB_LOCK_TIMEOUT (%v)", elapsed, lock)
		}
		noLeak()
	})

	t.Run("snapshot is read only", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		err := uow.Snapshot(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, w) })
		wantKind(t, err, apperrors.KindPermanent, nil)
		if !strings.Contains(err.Error(), "25006") {
			t.Fatalf("Snapshot = %v, want read_only_sql_transaction (25006)", err)
		}
		noLeak()
	})
}

// Covers: DOM-06 (I16)
func TestContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("canceled inside the unit of work", func(t *testing.T) {
		first, second := zeroWallet(t), zeroWallet(t)
		ctx, cancel := context.WithCancel(t.Context())
		err := newUoW().Do(ctx, func(r app.Repos) error {
			if err := r.Wallets().Insert(ctx, first); err != nil {
				return err
			}
			cancel()
			return r.Wallets().Insert(ctx, second)
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do = %v, want context.Canceled", err)
		}
		wantKind(t, err, apperrors.KindTransient, nil)
		for _, w := range []string{first.ID(), second.ID()} {
			if _, err := reads().Wallets().Get(t.Context(), w); !errors.Is(err, app.ErrNotFound) {
				t.Fatalf("wallet %s after a canceled unit of work: %v, want not found", w, err)
			}
		}
	})

	t.Run("deadline while waiting for a lock", func(t *testing.T) {
		w := zeroWallet(t)
		insertWallet(t, w)
		release := lockWallet(t, w.ID())
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := newUoW().Do(ctx, func(r app.Repos) error {
			_, err := r.Wallets().Lock(ctx, w.ID())
			return err
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Do = %v, want context.DeadlineExceeded", err)
		}
		wantKind(t, err, apperrors.KindTransient, nil)
		if elapsed := time.Since(start); elapsed >= env.Config().DBLockTimeout {
			t.Fatalf("the deadline did not interrupt the wait: %v", elapsed)
		}
	})
}
