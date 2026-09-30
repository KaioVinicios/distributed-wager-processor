package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// flakyPinger fails while down is set and counts the pings.
type flakyPinger struct {
	down  atomic.Bool
	pings atomic.Int32
}

func (p *flakyPinger) Ping(context.Context) error {
	p.pings.Add(1)
	if p.down.Load() {
		return errors.New("connection refused")
	}
	return nil
}

// Covers: SQS-07 (messaging.md §4.3; spec M5, decision 6)
func TestHealthGate(t *testing.T) {
	t.Run("a transient error with the database up keeps the gate open", func(t *testing.T) {
		p := &flakyPinger{}
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		if g.Paused() || p.pings.Load() != 1 {
			t.Fatalf("paused = %v after %d pings, want open after 1", g.Paused(), p.pings.Load())
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := g.Wait(ctx); err != nil {
			t.Fatalf("Wait on an open gate: %v", err)
		}
	})

	t.Run("a transient error with the database down pauses until the ping answers", func(t *testing.T) {
		p := &flakyPinger{}
		p.down.Store(true)
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		if !g.Paused() {
			t.Fatal("gate open with the database down")
		}
		waited := make(chan error, 1)
		go func() { waited <- g.Wait(t.Context()) }()
		select {
		case err := <-waited:
			t.Fatalf("Wait returned %v with the database down", err)
		case <-time.After(100 * time.Millisecond):
		}
		if p.pings.Load() < 3 {
			t.Fatalf("%d pings while paused, want the gate to keep probing", p.pings.Load())
		}
		p.down.Store(false)
		select {
		case err := <-waited:
			if err != nil || g.Paused() {
				t.Fatalf("Wait = %v, paused = %v; want resumed", err, g.Paused())
			}
		case <-time.After(time.Second):
			t.Fatal("Wait did not resume after the database came back")
		}
	})

	t.Run("Wait ends with its context", func(t *testing.T) {
		p := &flakyPinger{}
		p.down.Store(true)
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		if err := g.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Wait = %v, want the context error", err)
		}
	})
}
