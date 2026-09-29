//go:build integration

package app_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// faultyUoW decorates the repositories of every Do; call counts the Do calls
// from 1. The decorators provoke one failure at a time around the real
// adapter, never replacing it (test-plan §1).
type faultyUoW struct {
	app.UnitOfWork
	calls atomic.Int32
	wrap  func(r app.Repos, call int) app.Repos
}

func (u *faultyUoW) Do(ctx context.Context, fn func(app.Repos) error) error {
	call := int(u.calls.Add(1))
	return u.UnitOfWork.Do(ctx, func(r app.Repos) error { return fn(u.wrap(r, call)) })
}

// faultyRepos replaces the transaction or outbox repository when set.
type faultyRepos struct {
	app.Repos
	tx     app.TransactionRepository
	outbox app.OutboxRepository
}

func (f faultyRepos) Transactions() app.TransactionRepository {
	if f.tx != nil {
		return f.tx
	}
	return f.Repos.Transactions()
}

func (f faultyRepos) Outbox() app.OutboxRepository {
	if f.outbox != nil {
		return f.outbox
	}
	return f.Repos.Outbox()
}

type failingOutbox struct {
	app.OutboxRepository
	err error
}

func (f failingOutbox) Insert(context.Context, ...events.Envelope) error { return f.err }

// txHooks runs beforeInsert before Insert; findByKey, when set, runs before
// FindByIdempotencyKey, and hideKey makes it find nothing.
type txHooks struct {
	app.TransactionRepository
	beforeInsert func(ctx context.Context) error
	findByKey    func() error
	hideKey      bool
}

func (h txHooks) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	if h.beforeInsert != nil {
		if err := h.beforeInsert(ctx); err != nil {
			return err
		}
	}
	return h.TransactionRepository.Insert(ctx, t)
}

func (h txHooks) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error) {
	if h.findByKey != nil {
		if err := h.findByKey(); err != nil {
			return nil, err
		}
	}
	if h.hideKey {
		return nil, nil
	}
	return h.TransactionRepository.FindByIdempotencyKey(ctx, providerID, key)
}

// barrier holds each party until all of them arrive, or the context ends.
type barrier struct{ wg sync.WaitGroup }

func newBarrier(parties int) *barrier {
	b := &barrier{}
	b.wg.Add(parties)
	return b
}

func (b *barrier) arrive(ctx context.Context) {
	b.wg.Done()
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
	}
}
