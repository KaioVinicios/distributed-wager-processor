package observability_test

import (
	"strings"
	"testing"

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
