package sqsconsumer

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Pinger probes the database: *pgxpool.Pool (spec M5, decision 16).
type Pinger interface {
	Ping(ctx context.Context) error
}

// pingTimeout bounds each probe of the gate.
const pingTimeout = time.Second

// healthGate is the pause of messaging.md §4.3 (spec M5, decision 6): after
// a transient error it pings the database; if the ping fails, the pollers
// stop receiving, so the messages still in the queue do not spend receives,
// and probe every interval until the database answers.
type healthGate struct {
	pinger   Pinger
	log      *slog.Logger
	interval time.Duration
	paused   atomic.Bool
}

func newHealthGate(p Pinger, log *slog.Logger, interval time.Duration) *healthGate {
	return &healthGate{pinger: p, log: log, interval: interval}
}

// Paused tells whether the pollers are held.
func (g *healthGate) Paused() bool { return g.paused.Load() }

// Report is called after a message failed transiently: it closes the gate
// when the database does not answer.
func (g *healthGate) Report(ctx context.Context) {
	if g.ping(ctx) == nil {
		return
	}
	if g.paused.CompareAndSwap(false, true) {
		g.log.WarnContext(ctx, "sqs consumer paused: database unavailable")
	}
}

// Wait returns at once when the gate is open; otherwise it probes every
// interval until the database answers, or ctx ends.
func (g *healthGate) Wait(ctx context.Context) error {
	if !g.paused.Load() {
		return nil
	}
	tick := time.NewTicker(g.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		if !g.paused.Load() {
			return nil
		}
		if g.ping(ctx) == nil {
			if g.paused.CompareAndSwap(true, false) {
				g.log.InfoContext(ctx, "sqs consumer resumed: database available")
			}
			return nil
		}
	}
}

func (g *healthGate) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return g.pinger.Ping(ctx)
}
