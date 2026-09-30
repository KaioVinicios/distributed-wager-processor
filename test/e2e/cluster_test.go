//go:build e2e

package e2e_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// walletRead counts the GET /wallets/{walletId} an instance answered with 200.
const walletRead = `http_requests_total{method="GET",route="GET /wallets/{walletId}",status="200"}`

// readWallet reads the wallet n times through the cluster's round-robin.
func readWallet(t *testing.T, walletID string, n int) {
	t.Helper()
	c := cluster.Client(t, "wallet-service")
	for range n {
		if resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + walletID}); resp.Status != http.StatusOK {
			t.Fatalf("GET /wallets/%s = %d %s", walletID, resp.Status, resp.Body)
		}
	}
}

// walletReads is how many wallet reads each of the instances answered so far.
func walletReads(t *testing.T, instances ...int) map[int]int64 {
	t.Helper()
	out := map[int]int64{}
	for _, i := range instances {
		out[i] = cluster.Instance(i).MetricValue(t, walletRead)
	}
	return out
}

// Covers: CONC-04, TST-C04
// Sensitivity: pick always returning the first instance → instance 0 answered 6 of 6 reads.
//
// The cluster is 3 independent processes, and the round-robin spreads the
// requests evenly over them.
func TestClusterSpreadsRequests(t *testing.T) {
	cluster.AttachLogs(t)
	w := cluster.OpenWallet(t, testkit.BRL("10.00"))
	before := walletReads(t, 0, 1, 2)
	readWallet(t, w.ID, 6)
	after := walletReads(t, 0, 1, 2)
	for i := range 3 {
		if got := after[i] - before[i]; got != 2 {
			t.Fatalf("instance %d answered %d of 6 reads, want 2 (before %v, after %v)", i, got, before, after)
		}
	}
}

// Covers: CONC-04, TST-C04 (spec M8, decisions 3, 11 and 15)
// Sensitivity: pick ignoring live → connection refused in "killed"; pick ignoring armed → reads on
// instance 0 in "armed"; the child inheriting os.Environ → instance 1 fails to start in "environment".
func TestClusterInstanceLifecycle(t *testing.T) {
	w := cluster.OpenWallet(t, testkit.BRL("10.00"))

	t.Run("a killed instance leaves the round-robin and comes back", func(t *testing.T) {
		defer cluster.Restore(t)
		cluster.Kill(t, 2)
		if code := cluster.Instance(2).WaitExit(t); code != -1 {
			t.Fatalf("exit code after SIGKILL = %d, want -1 (ended by a signal)", code)
		}
		readWallet(t, w.ID, 6) // a pick of the dead instance fails with connection refused
		cluster.Restore(t)
		// Reaches the new process on the same port: a keep-alive connection to the
		// killed one would fail the read with EOF.
		readWallet(t, w.ID, 6)
	})

	t.Run("a stopped instance exits cleanly and stops answering", func(t *testing.T) {
		defer cluster.Restore(t)
		cluster.Stop(t, 1)
		if code := cluster.Instance(1).WaitExit(t); code != 0 {
			t.Fatalf("exit code after SIGTERM = %d, want 0", code)
		}
		if _, err := cluster.Instance(1).Client(t, "").Try(t, testkit.Request{Method: http.MethodGet, Path: "/health/live"}); err == nil {
			t.Fatal("the stopped instance still answers")
		}
	})

	t.Run("an armed instance leaves the round-robin", func(t *testing.T) {
		defer cluster.Restore(t)
		cluster.Restart(t, 0, testkit.Fault("test.never_reached"))
		before := walletReads(t, 0, 1, 2)
		readWallet(t, w.ID, 6)
		after := walletReads(t, 0, 1, 2)
		if after[0] != before[0] || after[1]-before[1] != 3 || after[2]-before[2] != 3 {
			t.Fatalf("reads per instance before %v, after %v; want none on the armed instance 0 and 3 on each other", before, after)
		}
	})

	t.Run("the developer's environment does not reach the instances", func(t *testing.T) {
		defer cluster.Restore(t)
		// An inherited AWS_PROFILE of a profile that does not exist fails the AWS
		// configuration of the instance at start.
		t.Setenv("AWS_PROFILE", "no-such-profile")
		cluster.Restart(t, 1)
		cluster.AssertAllReady(t)
	})
}
