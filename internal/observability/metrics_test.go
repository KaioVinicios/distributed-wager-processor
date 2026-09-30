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
