// Package references runs the reference worker (D-11): it claims the due
// PENDING_REFERENCE operations and resolves them one at a time through the
// ResolveReferences use case, on any instance.
package references

import (
	"context"
	"log/slog"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

const (
	// itemTimeout bounds one resolution, which runs detached from the
	// cancellation of the loop (spec M6, decision 10).
	itemTimeout = 10 * time.Second
	// gaugeEvery bounds how often the pending gauge is refreshed.
	gaugeEvery = time.Second
)

// Resolver is the use case the worker drives (*app.ResolveReferences).
type Resolver interface {
	Claim(ctx context.Context, limit int) ([]app.PendingReference, error)
	Resolve(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error)
	CountPending(ctx context.Context) (int, error)
}

// Metrics is what the worker reports (ARCHITECTURE.md §13.2).
type Metrics interface {
	ReferenceRetried()
	ReferenceExpired()
	ReferencePending(n int)
}

// Options are the worker settings (spec M6, decision 12).
type Options struct {
	BatchSize    int
	PollInterval time.Duration
}

// Worker is the reference worker of one instance.
type Worker struct {
	resolver Resolver
	metrics  Metrics
	log      *slog.Logger
	opts     Options

	lastGauge time.Time // only read and written by Run
}

// NewWorker builds a worker; Run starts it.
func NewWorker(r Resolver, m Metrics, log *slog.Logger, opts Options) *Worker {
	return &Worker{resolver: r, metrics: m, log: log, opts: opts}
}

// Run claims a batch, resolves its items in sequence and waits for the poll
// interval, unless the batch was full and had no errors. After the
// cancellation no claim and no item begins; the item in flight finishes
// (spec M6, decisions 1, 10 and 11).
func (w *Worker) Run(ctx context.Context) {
	var claimDelay time.Duration
	for ctx.Err() == nil {
		w.refreshPending(ctx)
		batch, err := w.resolver.Claim(ctx, w.opts.BatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			claimDelay = nextClaimDelay(claimDelay)
			w.log.Warn("reference claim failed", "error", err.Error(), "retryIn", claimDelay.String())
			sleep(ctx, claimDelay)
			continue
		}
		claimDelay = 0
		failed := w.resolveBatch(ctx, batch)
		if len(batch) < w.opts.BatchSize || failed > 0 {
			sleep(ctx, w.opts.PollInterval)
		}
	}
}

// resolveBatch resolves the items in order and returns how many failed.
func (w *Worker) resolveBatch(ctx context.Context, batch []app.PendingReference) (failed int) {
	for _, ref := range batch {
		if ctx.Err() != nil {
			return failed
		}
		if !w.resolve(ctx, ref) {
			failed++
		}
	}
	return failed
}

// resolve runs one item detached from ctx's cancellation and counts the
// outcome. It returns false when the resolution failed: the operation stays
// due and comes back in the next cycle (spec M6, decision 9).
func (w *Worker) resolve(ctx context.Context, ref app.PendingReference) bool {
	itemCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), itemTimeout)
	defer cancel()
	res, err := w.resolver.Resolve(itemCtx, ref)
	if err != nil {
		w.log.Warn("reference resolution failed", "transactionId", ref.ID, "walletId", ref.WalletID, "error", err.Error())
		return false
	}
	switch {
	case res.Outcome == app.ResolveRescheduled:
		w.metrics.ReferenceRetried()
	case res.Expired():
		w.metrics.ReferenceExpired()
	}
	return true
}

// refreshPending updates the gauge at most once per gaugeEvery. A failure
// keeps the previous value: the claim reports the database being down.
func (w *Worker) refreshPending(ctx context.Context) {
	if time.Since(w.lastGauge) < gaugeEvery {
		return
	}
	n, err := w.resolver.CountPending(ctx)
	if err != nil {
		return
	}
	w.lastGauge = time.Now()
	w.metrics.ReferencePending(n)
}

// sleep waits for d or until ctx is canceled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
