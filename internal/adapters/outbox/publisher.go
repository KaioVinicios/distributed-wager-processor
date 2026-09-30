package outbox

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/faultinject"
)

// Sink publishes one event to the broker.
type Sink interface {
	Publish(ctx context.Context, e app.PendingEvent) error
}

// Metrics is what the publisher reports (messaging.md §8).
type Metrics interface {
	Published(eventType string, lag time.Duration)
	PublishFailed(eventType string)
	LeaseReclaimed()
	Backlog(pending int, oldestAge time.Duration)
}

// Options are the publisher settings (messaging.md §5.1).
type Options struct {
	Owner          string // locked_by of this instance
	BatchSize      int
	Lease          time.Duration
	PollInterval   time.Duration
	Concurrency    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

const (
	// claimRetryMin and claimRetryMax bound the wait after a failed claim,
	// the database being unavailable (messaging.md §5.3).
	claimRetryMin = time.Second
	claimRetryMax = 30 * time.Second
	// backlogEvery bounds how often the lag gauges are refreshed.
	backlogEvery = time.Second
	// storeTimeout bounds the confirmation and the failure record, which run
	// detached from the cancellation of the loop.
	storeTimeout = 5 * time.Second
)

// Publisher is the outbox publisher of one instance (D-13). Any number of
// instances run one each over the same outbox.
type Publisher struct {
	store   app.OutboxStore
	sink    Sink
	metrics Metrics
	log     *slog.Logger
	opts    Options

	lastBacklog time.Time // only read and written by Run
}

// NewPublisher builds a publisher; Run starts it.
func NewPublisher(store app.OutboxStore, sink Sink, m Metrics, log *slog.Logger, opts Options) *Publisher {
	return &Publisher{store: store, sink: sink, metrics: m, log: log, opts: opts}
}

// Run publishes until ctx is canceled: claim a batch, publish it, and wait
// for the poll interval unless the batch was full. After the cancellation no
// claim and no publication begins; the ones in flight finish and are
// confirmed before Run returns (spec M4, decision 12).
func (p *Publisher) Run(ctx context.Context) {
	var claimDelay time.Duration
	for ctx.Err() == nil {
		p.refreshBacklog(ctx)
		batch, err := p.store.Claim(ctx, p.opts.Owner, p.opts.Lease, p.opts.BatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			claimDelay = min(max(2*claimDelay, claimRetryMin), claimRetryMax)
			p.log.Warn("outbox claim failed", "error", err.Error(), "retryIn", claimDelay.String())
			sleep(ctx, claimDelay)
			continue
		}
		claimDelay = 0
		p.publishBatch(ctx, batch)
		if len(batch) < p.opts.BatchSize {
			sleep(ctx, p.opts.PollInterval)
		}
	}
}

// publishBatch publishes up to Concurrency groups in parallel, each group in
// order of occurrence (spec M4, decision 9).
func (p *Publisher) publishBatch(ctx context.Context, batch []app.PendingEvent) {
	for i := range batch {
		if batch[i].Reclaimed {
			p.metrics.LeaseReclaimed()
			p.eventLog(batch[i]).Info("outbox lease reclaimed")
		}
	}
	sem := make(chan struct{}, p.opts.Concurrency)
	var wg sync.WaitGroup
	for _, group := range groups(batch) {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			for i := range group {
				if ctx.Err() != nil {
					return // the rest keeps its lease, which expires and is reclaimed
				}
				p.publish(ctx, group[i])
			}
		})
	}
	wg.Wait()
}

// publish sends one event and records the outcome. The publication and its
// record are detached from ctx's cancellation: once begun, they finish.
func (p *Publisher) publish(ctx context.Context, e app.PendingEvent) {
	log := p.eventLog(e)
	faultinject.Point("outbox.after_claim_before_publish") // crash holding the lease (test-plan §4)
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.opts.Lease/2)
	err := p.sink.Publish(pubCtx, e)
	cancel()
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
	defer cancel()
	if err != nil {
		p.fail(storeCtx, e, err, log)
		return
	}
	faultinject.Point("outbox.after_publish_before_ack") // published, never confirmed: republished (test-plan §4)
	at, ok, err := p.store.MarkPublished(storeCtx, e.EventID, p.opts.Owner)
	switch {
	case err != nil: // the lease expires and the event is republished with the same eventId (OUT-06b)
		log.Warn("outbox confirmation failed", "error", err.Error())
	case !ok:
		log.Info("outbox event confirmed by another instance")
	default:
		p.metrics.Published(e.EventType, at.Sub(e.OccurredAt))
	}
}

// fail counts the attempt, schedules the next one and releases the lease
// (D-13). Nothing is discarded, whatever the error (spec M4, decision 7).
func (p *Publisher) fail(ctx context.Context, e app.PendingEvent, cause error, log *slog.Logger) {
	p.metrics.PublishFailed(e.EventType)
	retryIn := retryDelay(e.Attempts, p.opts.RetryBaseDelay, p.opts.RetryMaxDelay)
	log.Warn("outbox publish failed", "eventType", e.EventType,
		"attempts", e.Attempts+1, "retryIn", retryIn.String(), "error", cause.Error())
	ok, err := p.store.MarkFailed(ctx, e.EventID, p.opts.Owner, retryIn, truncateError(cause.Error()))
	switch {
	case err != nil: // the lease expires and the event is retried anyway
		log.Warn("outbox failure not recorded", "error", err.Error())
	case !ok:
		log.Info("outbox event reclaimed before its failure was recorded")
	}
}

// eventLog is the logger of one event: its id, the wallet it belongs to
// (message_group_id, data-model §3.5) and its correlation (OBS-01).
func (p *Publisher) eventLog(e app.PendingEvent) *slog.Logger {
	return p.log.With("eventId", e.EventID, "walletId", e.MessageGroupID, "correlationId", e.CorrelationID)
}

// refreshBacklog updates the lag gauges at most once per backlogEvery (spec
// M4, decision 13). A failure keeps the previous values: the claim reports it.
func (p *Publisher) refreshBacklog(ctx context.Context) {
	if time.Since(p.lastBacklog) < backlogEvery {
		return
	}
	b, err := p.store.Backlog(ctx)
	if err != nil {
		return
	}
	p.lastBacklog = time.Now()
	p.metrics.Backlog(b.Pending, b.OldestAge)
}

// groups splits a batch by MessageGroupId, each group sorted by occurrence.
func groups(batch []app.PendingEvent) [][]app.PendingEvent {
	byGroup := map[string][]app.PendingEvent{}
	var order []string
	for i := range batch {
		g := batch[i].MessageGroupID
		if _, seen := byGroup[g]; !seen {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], batch[i])
	}
	out := make([][]app.PendingEvent, 0, len(order))
	for _, g := range order {
		events := byGroup[g]
		slices.SortFunc(events, func(a, b app.PendingEvent) int {
			return cmp.Or(a.OccurredAt.Compare(b.OccurredAt), cmp.Compare(a.EventID, b.EventID))
		})
		out = append(out, events)
	}
	return out
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
