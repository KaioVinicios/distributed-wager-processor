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

// Metrics implements app.Metrics and the ports of the outbox publisher, of
// the SQS consumer and of the reference worker with
// Prometheus collectors. M7 adds the rest of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct {
	reconciliationDivergences prometheus.Counter

	outboxPublished      *prometheus.CounterVec
	outboxPublishFailed  *prometheus.CounterVec
	outboxLeaseReclaims  prometheus.Counter
	outboxPending        prometheus.Gauge
	outboxOldestPending  prometheus.Gauge
	outboxPublishLatency *prometheus.HistogramVec

	sqsReceived      prometheus.Counter
	sqsProcessed     *prometheus.CounterVec
	sqsDuration      *prometheus.HistogramVec
	wagerDuplicates  *prometheus.CounterVec
	sqsRetries       *prometheus.CounterVec
	sqsDLQSent       *prometheus.CounterVec
	sqsDLQDepth      *prometheus.GaugeVec
	sqsReceiveErrors prometheus.Counter
	sqsDeleteErrors  prometheus.Counter

	referenceRetries prometheus.Counter
	referenceExpired prometheus.Counter
	referencePending prometheus.Gauge
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
		sqsReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sqs_messages_received_total",
			Help: "Messages received from the wager queue.",
		}),
		sqsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sqs_messages_processed_total",
			Help: "Messages concluded by the wager use case.",
		}, []string{"outcome"}),
		sqsDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "sqs_processing_duration_seconds",
			Help:    "Time to conclude a message, from the start of its processing to its outcome.",
			Buckets: prometheus.ExponentialBucketsRange(0.005, 10, 12),
		}, []string{"outcome"}),
		wagerDuplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_duplicates_total",
			Help: "Repeated deliveries of an operation, by channel and deduplication layer.",
		}, []string{"channel", "layer"}),
		sqsRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sqs_retries_total",
			Help: "Messages returned to the queue to be received again.",
		}, []string{"reason"}),
		sqsDLQSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sqs_dlq_sent_total",
			Help: "Messages sent explicitly to the dead-letter queue.",
		}, []string{"reason"}),
		sqsDLQDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "sqs_dlq_depth",
			Help: "Approximate number of messages in the dead-letter queue.",
		}, []string{"queue"}),
		sqsReceiveErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sqs_receive_errors_total",
			Help: "ReceiveMessage calls that failed.",
		}),
		sqsDeleteErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "sqs_delete_errors_total",
			Help: "DeleteMessage calls that failed after the message was concluded.",
		}),
		referenceRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reference_retries_total",
			Help: "Reference resolution attempts rescheduled for a later time.",
		}),
		referenceExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reference_expired_total",
			Help: "Pending operations rejected with REFERENCE_NOT_FOUND after the retry limit or the TTL.",
		}),
		referencePending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "reference_pending_transactions",
			Help: "Operations waiting in PENDING_REFERENCE.",
		}),
	}
	reg.MustRegister(m.reconciliationDivergences, m.outboxPublished, m.outboxPublishFailed,
		m.outboxLeaseReclaims, m.outboxPending, m.outboxOldestPending, m.outboxPublishLatency,
		m.sqsReceived, m.sqsProcessed, m.sqsDuration, m.wagerDuplicates, m.sqsRetries, m.sqsDLQSent,
		m.sqsDLQDepth, m.sqsReceiveErrors, m.sqsDeleteErrors,
		m.referenceRetries, m.referenceExpired, m.referencePending)
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

// Received counts a message received from the wager queue.
func (m *Metrics) Received() { m.sqsReceived.Inc() }

// Processed counts a message concluded by the use case and its duration.
func (m *Metrics) Processed(outcome string, d time.Duration) {
	m.sqsProcessed.WithLabelValues(outcome).Inc()
	m.sqsDuration.WithLabelValues(outcome).Observe(d.Seconds())
}

// Duplicate counts a repeated SQS delivery caught by layer (inbox or idempotency).
func (m *Metrics) Duplicate(layer string) { m.wagerDuplicates.WithLabelValues("sqs", layer).Inc() }

// Retried counts a message returned to the queue.
func (m *Metrics) Retried(reason string) { m.sqsRetries.WithLabelValues(reason).Inc() }

// SentToDLQ counts an explicit send to the dead-letter queue.
func (m *Metrics) SentToDLQ(reason string) { m.sqsDLQSent.WithLabelValues(reason).Inc() }

// DLQDepth sets the approximate depth of the dead-letter queue.
func (m *Metrics) DLQDepth(queue string, n int) { m.sqsDLQDepth.WithLabelValues(queue).Set(float64(n)) }

// ReceiveFailed counts a failed ReceiveMessage.
func (m *Metrics) ReceiveFailed() { m.sqsReceiveErrors.Inc() }

// DeleteFailed counts a DeleteMessage that failed after the message was concluded.
func (m *Metrics) DeleteFailed() { m.sqsDeleteErrors.Inc() }

// ReferenceRetried counts a pending operation rescheduled for a later attempt.
func (m *Metrics) ReferenceRetried() { m.referenceRetries.Inc() }

// ReferenceExpired counts a pending operation rejected because its limit was reached.
func (m *Metrics) ReferenceExpired() { m.referenceExpired.Inc() }

// ReferencePending sets the number of operations in PENDING_REFERENCE.
func (m *Metrics) ReferencePending(n int) { m.referencePending.Set(float64(n)) }
