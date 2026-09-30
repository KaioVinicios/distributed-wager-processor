package observability_test

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: HTTP-07, OBS-03
func TestMetrics_ReconciliationDivergences(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.ReconciliationDivergence()
	m.ReconciliationDivergence()

	want := `
# HELP reconciliation_divergences_total Reconciliations whose stored balance differs from the balance rebuilt from the ledger.
# TYPE reconciliation_divergences_total counter
reconciliation_divergences_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "reconciliation_divergences_total"); err != nil {
		t.Fatal(err)
	}
}

// Covers: OBS-03, OUT-03, OUT-04 (spec M4, decision 19; messaging.md §8)
func TestMetrics_Outbox(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.Published("WalletBalanceChanged", 250*time.Millisecond)
	m.Published("WalletBalanceChanged", 2*time.Second)
	m.PublishFailed("WagerTransactionProcessed")
	m.LeaseReclaimed()
	m.LeaseReclaimed()
	m.Backlog(3, 90*time.Second)

	want := `
# HELP outbox_lease_reclaims_total Outbox events whose expired lease was taken over by a publisher.
# TYPE outbox_lease_reclaims_total counter
outbox_lease_reclaims_total 2
# HELP outbox_oldest_pending_age_seconds Age of the oldest outbox event not yet published.
# TYPE outbox_oldest_pending_age_seconds gauge
outbox_oldest_pending_age_seconds 90
# HELP outbox_pending_events Outbox events not yet published.
# TYPE outbox_pending_events gauge
outbox_pending_events 3
# HELP outbox_publish_failures_total Failed publication attempts of outbox events.
# TYPE outbox_publish_failures_total counter
outbox_publish_failures_total{event_type="WagerTransactionProcessed"} 1
# HELP outbox_published_total Outbox events published to the events topic.
# TYPE outbox_published_total counter
outbox_published_total{event_type="WalletBalanceChanged"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "outbox_published_total",
		"outbox_publish_failures_total", "outbox_lease_reclaims_total", "outbox_pending_events",
		"outbox_oldest_pending_age_seconds"); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "outbox_publish_lag_seconds" {
			continue
		}
		h := f.GetMetric()[0].GetHistogram()
		if label := f.GetMetric()[0].GetLabel()[0]; label.GetName() != "event_type" || label.GetValue() != "WalletBalanceChanged" ||
			h.GetSampleCount() != 2 || h.GetSampleSum() != 2.25 {
			t.Fatalf("outbox_publish_lag_seconds = %v %v", f.GetMetric()[0].GetLabel(), h)
		}
		return
	}
	t.Fatal("outbox_publish_lag_seconds not registered")
}

// Covers: OBS-03, SQS-03, SQS-07 (spec M5, decision 15; messaging.md §8)
func TestMetrics_SQS(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.Received()
	m.Received()
	m.Processed("processed", 40*time.Millisecond)
	m.Processed("replay", 10*time.Millisecond)
	m.Duplicate("inbox")
	m.Retried("transient")
	m.Retried("deadline_release")
	m.SentToDLQ("UNKNOWN_WALLET")
	m.DLQDepth("wager-transactions-dlq.fifo", 3)
	m.ReceiveFailed()
	m.DeleteFailed()

	want := `
# HELP sqs_delete_errors_total DeleteMessage calls that failed after the message was concluded.
# TYPE sqs_delete_errors_total counter
sqs_delete_errors_total 1
# HELP sqs_dlq_depth Approximate number of messages in the dead-letter queue.
# TYPE sqs_dlq_depth gauge
sqs_dlq_depth{queue="wager-transactions-dlq.fifo"} 3
# HELP sqs_dlq_sent_total Messages sent explicitly to the dead-letter queue.
# TYPE sqs_dlq_sent_total counter
sqs_dlq_sent_total{reason="UNKNOWN_WALLET"} 1
# HELP sqs_messages_processed_total Messages concluded by the wager use case.
# TYPE sqs_messages_processed_total counter
sqs_messages_processed_total{outcome="processed"} 1
sqs_messages_processed_total{outcome="replay"} 1
# HELP sqs_messages_received_total Messages received from the wager queue.
# TYPE sqs_messages_received_total counter
sqs_messages_received_total 2
# HELP sqs_receive_errors_total ReceiveMessage calls that failed.
# TYPE sqs_receive_errors_total counter
sqs_receive_errors_total 1
# HELP sqs_retries_total Messages returned to the queue to be received again.
# TYPE sqs_retries_total counter
sqs_retries_total{reason="deadline_release"} 1
sqs_retries_total{reason="transient"} 1
# HELP wager_duplicates_total Repeated deliveries of an operation, by channel and deduplication layer.
# TYPE wager_duplicates_total counter
wager_duplicates_total{channel="sqs",layer="inbox"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "sqs_delete_errors_total", "sqs_dlq_depth",
		"sqs_dlq_sent_total", "sqs_messages_processed_total", "sqs_messages_received_total", "sqs_receive_errors_total",
		"sqs_retries_total", "wager_duplicates_total"); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == "sqs_processing_duration_seconds" {
			if n := len(f.GetMetric()); n != 2 {
				t.Fatalf("sqs_processing_duration_seconds series = %d, want 2 (one per outcome)", n)
			}
			return
		}
	}
	t.Fatal("sqs_processing_duration_seconds not registered")
}

// Covers: OBS-03, OPS-12, OPS-13 (spec M6, decision 13; ARCHITECTURE.md §13.2)
func TestMetrics_References(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.ReferenceRetried()
	m.ReferenceRetried()
	m.ReferenceExpired()
	m.ReferencePending(4)

	want := `
# HELP reference_expired_total Pending operations rejected with REFERENCE_NOT_FOUND after the retry limit or the TTL.
# TYPE reference_expired_total counter
reference_expired_total 1
# HELP reference_pending_transactions Operations waiting in PENDING_REFERENCE.
# TYPE reference_pending_transactions gauge
reference_pending_transactions 4
# HELP reference_retries_total Reference resolution attempts rescheduled for a later time.
# TYPE reference_retries_total counter
reference_retries_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"reference_expired_total", "reference_pending_transactions", "reference_retries_total"); err != nil {
		t.Fatal(err)
	}
}
