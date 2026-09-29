package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewRegistry builds the process registry. The metrics catalog arrives in M7.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Metrics implements app.Metrics with Prometheus counters. M7 adds the rest
// of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct {
	reconciliationDivergences prometheus.Counter
}

// NewMetrics registers the counters on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
	}
	reg.MustRegister(m.reconciliationDivergences)
	return m
}

// ReconciliationDivergence counts a reconciliation that found a divergence.
func (m *Metrics) ReconciliationDivergence() { m.reconciliationDivergences.Inc() }
