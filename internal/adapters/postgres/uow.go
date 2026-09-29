package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/config"
)

// rollbackTimeout bounds the rollback, which runs even when the caller's
// context is already done.
const rollbackTimeout = 5 * time.Second

// UnitOfWork is the SQL transaction boundary of the use cases (D-14).
type UnitOfWork struct {
	pool        *pgxpool.Pool
	lockTimeout string
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// NewUnitOfWork uses DB_LOCK_TIMEOUT as the lock_timeout of every Do.
func NewUnitOfWork(pool *pgxpool.Pool, cfg config.Config) *UnitOfWork {
	return &UnitOfWork{pool: pool, lockTimeout: strconv.FormatInt(cfg.DBLockTimeout.Milliseconds(), 10) + "ms"}
}

var (
	writeTx    = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	snapshotTx = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
)

// Do runs fn in one READ COMMITTED transaction. lock_timeout is set for the
// whole transaction with set_config, the parameterizable SET LOCAL (D-09).
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repos) error) error {
	return u.run(ctx, writeTx, true, fn)
}

// Snapshot runs fn in one REPEATABLE READ READ ONLY transaction (D-16).
func (u *UnitOfWork) Snapshot(ctx context.Context, fn func(app.Repos) error) error {
	return u.run(ctx, snapshotTx, false, fn)
}

func (u *UnitOfWork) run(ctx context.Context, opts pgx.TxOptions, setLockTimeout bool, fn func(app.Repos) error) error {
	tx, err := u.pool.BeginTx(ctx, opts)
	if err != nil {
		return interrupted(ctx, translate(err))
	}
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, tx)
			panic(p)
		}
	}()
	if setLockTimeout {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, u.lockTimeout); err != nil {
			rollback(ctx, tx)
			return interrupted(ctx, translate(err))
		}
	}
	if err := fn(repos{q: tx}); err != nil {
		rollback(ctx, tx)
		return interrupted(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return interrupted(ctx, translate(err))
	}
	return nil
}

// rollback runs on a context detached from the caller's cancellation, so a
// canceled request never leaves its transaction open.
func rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// interrupted keeps the context error in the chain when the context ended the
// transaction, and classifies the failure as transient: nothing was committed
// (or the commit outcome is unknown), and the idempotency path makes a retry
// safe (DOM-06, I16).
func interrupted(ctx context.Context, err error) error {
	cerr := ctx.Err()
	if cerr == nil {
		return err
	}
	if errors.Is(err, cerr) && apperrors.Classify(err) == apperrors.KindTransient {
		return err
	}
	return apperrors.New(apperrors.KindTransient, "", errors.Join(cerr, err))
}
