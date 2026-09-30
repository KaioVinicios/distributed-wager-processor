package observability

import (
	"time"

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

// Metrics implements app.Metrics and the outbox publisher's port with
// Prometheus collectors. M7 adds the rest of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct {
	reconciliationDivergences prometheus.Counter

	outboxPublished      *prometheus.CounterVec
	outboxPublishFailed  *prometheus.CounterVec
	outboxLeaseReclaims  prometheus.Counter
	outboxPending        prometheus.Gauge
	outboxOldestPending  prometheus.Gauge
	outboxPublishLatency *prometheus.HistogramVec
}

// NewMetrics registers the collectors on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
		outboxPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_published_total",
			Help: "Outbox events published to the events topic.",
		}, []string{"event_type"}),
		outboxPublishFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_publish_failures_total",
			Help: "Failed publication attempts of outbox events.",
		}, []string{"event_type"}),
		outboxLeaseReclaims: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "outbox_lease_reclaims_total",
			Help: "Outbox events whose expired lease was taken over by a publisher.",
		}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_pending_events",
			Help: "Outbox events not yet published.",
		}),
		outboxOldestPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_oldest_pending_age_seconds",
			Help: "Age of the oldest outbox event not yet published.",
		}),
		outboxPublishLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "outbox_publish_lag_seconds",
			Help:    "Time from the occurrence of an event to its confirmed publication.",
			Buckets: prometheus.ExponentialBucketsRange(0.005, 60, 12),
		}, []string{"event_type"}),
	}
	reg.MustRegister(m.reconciliationDivergences, m.outboxPublished, m.outboxPublishFailed,
		m.outboxLeaseReclaims, m.outboxPending, m.outboxOldestPending, m.outboxPublishLatency)
	return m
}

// ReconciliationDivergence counts a reconciliation that found a divergence.
func (m *Metrics) ReconciliationDivergence() { m.reconciliationDivergences.Inc() }

// Published counts a confirmed publication and its lag (published − occurred).
func (m *Metrics) Published(eventType string, lag time.Duration) {
	m.outboxPublished.WithLabelValues(eventType).Inc()
	m.outboxPublishLatency.WithLabelValues(eventType).Observe(lag.Seconds())
}

// PublishFailed counts a failed publication attempt.
func (m *Metrics) PublishFailed(eventType string) {
	m.outboxPublishFailed.WithLabelValues(eventType).Inc()
}

// LeaseReclaimed counts an event whose expired lease was taken over.
func (m *Metrics) LeaseReclaimed() { m.outboxLeaseReclaims.Inc() }

// Backlog sets the outbox lag gauges.
func (m *Metrics) Backlog(pending int, oldestAge time.Duration) {
	m.outboxPending.Set(float64(pending))
	m.outboxOldestPending.Set(oldestAge.Seconds())
}
