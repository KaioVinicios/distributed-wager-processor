package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultCheckTimeout bounds each dependency check.
const DefaultCheckTimeout = 2 * time.Second

// Status is the state of a dependency or of the whole process.
type Status string

const (
	StatusUp   Status = "UP"
	StatusDown Status = "DOWN"
)

// Checker probes one dependency. Adapters provide checkers through the
// "health_checkers" Fx value group.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Report is the readiness result. Failure reasons are logged, never exposed.
type Report struct {
	Status Status            `json:"status"`
	Checks map[string]Status `json:"checks,omitempty"`
}

// Health aggregates checkers.
type Health struct {
	log      *slog.Logger
	checkers []Checker
	timeout  time.Duration
}

// NewHealth builds an aggregator that runs checkers concurrently, each bounded by timeout.
func NewHealth(log *slog.Logger, checkers []Checker, timeout time.Duration) *Health {
	return &Health{log: log, checkers: checkers, timeout: timeout}
}

// Ready runs every checker and reports UP only if all of them succeed.
func (h *Health) Ready(ctx context.Context) Report {
	errs := make([]error, len(h.checkers))
	var wg sync.WaitGroup
	for i, c := range h.checkers {
		wg.Go(func() { errs[i] = h.check(ctx, c) })
	}
	wg.Wait()

	report := Report{Status: StatusUp, Checks: make(map[string]Status, len(h.checkers))}
	for i, c := range h.checkers {
		if errs[i] != nil {
			report.Status = StatusDown
			report.Checks[c.Name()] = StatusDown
			h.log.WarnContext(ctx, "health check failed", "check", c.Name(), "error", errs[i].Error())
			continue
		}
		report.Checks[c.Name()] = StatusUp
	}
	return report
}

// check bounds one checker by the timeout even if it ignores ctx cancellation.
func (h *Health) check(ctx context.Context, c Checker) error {
	ctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Check(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
