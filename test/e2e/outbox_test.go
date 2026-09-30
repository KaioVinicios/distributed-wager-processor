//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-C06, OUT-05, OUT-06b, E7 (C06a)
// Sensitivity: without the point → instance 0 did not exit; a claim that never takes an expired lease
// over → the wallet's events stay unpublished.
func TestPublisherCrashAfterPublish(t *testing.T) {
	publisherCrash(t, "outbox.after_publish_before_ack")
}

// Covers: TST-C06, OUT-06a, E7 (C06b)
// Sensitivity: without the point → instance 0 did not exit; a claim that never takes an expired lease
// over → the wallet's events stay unpublished.
func TestPublisherCrashAfterClaim(t *testing.T) {
	publisherCrash(t, "outbox.after_claim_before_publish")
}

// publisherCrash: the publisher of instance 0, armed with point, claims the
// wallet's events and dies holding their lease. The publisher of instance 1,
// turned on after the crash so that who claims is no draw (two simultaneous
// publishers are I05a), takes them over when the lease expires, and every
// event reaches the audit queue with the content of the database (item 8 of
// the consistency check of the wallet).
func publisherCrash(t *testing.T, point string) {
	t.Helper()
	defer cluster.Restore(t)
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	waitPublished(t, w.ID) // the opening's events go out before the publishers change
	crashOn(t, point, config.Roles{OutboxPublisher: true})
	a := cluster.Client(t, "provider-a")
	for range 5 {
		result(t, a, wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), http.StatusOK)
	}

	cluster.Instance(0).AssertFaultHit(t, point)
	cluster.Restart(t, 1) // a second publisher
	waitPublished(t, w.ID)
	if got := cluster.Instance(1).MetricValue(t, "outbox_lease_reclaims_total"); got < 1 {
		t.Fatalf("outbox_lease_reclaims_total on instance 1 = %d, want at least 1: the dead publisher's lease was not taken over", got)
	}
}
