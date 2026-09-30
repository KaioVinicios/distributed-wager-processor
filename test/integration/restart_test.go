//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// openWalletVia opens a wallet through the API of one app, as the internal
// service, without the delivery check of App.OpenWallet.
func openWalletVia(t *testing.T, a *testkit.App, initial testkit.Money) testkit.Wallet {
	t.Helper()
	resp := a.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": testkit.NewID(), "initialBalance": initial,
	}})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /wallets = %d %s", resp.Status, resp.Body)
	}
	var w testkit.Wallet
	resp.JSON(t, &w)
	return w
}

// assertStoredConsistent runs the SQL checks of test-plan §6 (items 2–7) on
// the wallet: the ledger and the event matrix of its operations.
func assertStoredConsistent(t *testing.T, e *testkit.Env, walletID string) {
	t.Helper()
	for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){testkit.LedgerProblems, testkit.OutboxProblems} {
		problems, err := check(t.Context(), e.Owner, walletID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			t.Errorf("wallet %s: %s", walletID, p)
		}
	}
}

// Covers: TST-I06, IDEM-01, OPS-12, E6 (I06)
// Sensitivity: removing references.Module from bootstrap.Options → the second app never resolves the REFUND (waitStatus times out).
//
// The first app records a REFUND before its BET and stops. The second app,
// started over the same database, receives the BET and its worker resolves the
// pending operation from the schedule the database kept; the replays return
// what was recorded.
func TestRecoveryAfterRestart(t *testing.T) {
	e := testkit.NewTestEnv(t, "restart")
	longWait := func(c *config.Config) { c.ReferenceMaxAttempts, c.ReferenceTTL = 50, 2*time.Minute }

	first, stopFirst, err := e.StartApp(t.Context(), longWait)
	if err != nil {
		t.Fatal(err)
	}
	stoppedFirst := false
	t.Cleanup(func() {
		if !stoppedFirst {
			stopFirst()
		}
	})
	w := openWalletVia(t, first, testkit.BRL("100.00"))
	bet, refund := unique("bet"), unique("refund")
	betRequest := wager(w, "provider-a", "BET", "30.00", bet, "")
	refundRequest := wager(w, "provider-a", "REFUND", "30.00", refund, bet)
	c := first.Client(t, "provider-a")
	wantResult(t, result(t, c, refundRequest, http.StatusAccepted), "PENDING_REFERENCE", "", "", false)
	// The precondition of the test, not luck of timing: the app took the long TTL, so the
	// REFUND cannot expire however long the restart takes.
	if pending := waitStatus(t, c, "provider-a", refund, "PENDING_REFERENCE"); pending.ExpiresAt == nil || pending.ExpiresAt.Sub(pending.CreatedAt) < time.Minute {
		t.Fatalf("pending operation = %+v, want an expiresAt at least a minute after createdAt (StartApp option ignored?)", pending)
	}
	stopFirst()
	stoppedFirst = true

	second, stopSecond, err := e.StartApp(t.Context(), longWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSecond)
	a := second.Client(t, "provider-a")
	wantResult(t, result(t, a, betRequest, http.StatusOK), "PROCESSED", "", "70.00", false)
	done := waitStatus(t, a, "provider-a", refund, "PROCESSED")
	if done.Balance == nil || done.Balance.Amount != "100.00" {
		t.Fatalf("REFUND after the restart = %+v, want PROCESSED with balance 100.00", done)
	}

	// The replays return what was recorded, by another instance than the first.
	wantResult(t, result(t, a, betRequest, http.StatusOK), "PROCESSED", "", "70.00", true)
	wantResult(t, result(t, a, refundRequest, http.StatusOK), "PROCESSED", "", "100.00", true)
	assertStoredConsistent(t, e, w.ID)
}

// Covers: OPS-13 (I06b)
// Sensitivity: ignoring expires_at in the expiration → the second app leaves the REFUND pending and waitStatus times out.
//
// Every instance is down past the TTL. When one starts, the operation is
// rejected with REFERENCE_NOT_FOUND, though no attempt ran in the meantime.
func TestPendingExpiresAfterDowntime(t *testing.T) {
	e := testkit.NewTestEnv(t, "downtime")
	slow := func(c *config.Config) {
		c.ReferenceMaxAttempts, c.ReferenceTTL = 50, 3*time.Second
		c.ReferenceRetryBaseDelay, c.ReferenceRetryMaxDelay = time.Second, 2*time.Second
	}

	first, stopFirst, err := e.StartApp(t.Context(), slow)
	if err != nil {
		t.Fatal(err)
	}
	stoppedFirst := false
	t.Cleanup(func() {
		if !stoppedFirst {
			stopFirst()
		}
	})
	w := openWalletVia(t, first, testkit.BRL("100.00"))
	refund := unique("refund")
	c := first.Client(t, "provider-a")
	wantResult(t, result(t, c, wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted), "PENDING_REFERENCE", "", "", false)
	pending := waitStatus(t, c, "provider-a", refund, "PENDING_REFERENCE")
	// The precondition of the test: the app took the slow schedule, so the first app
	// cannot reach the limit before it stops.
	if pending.ExpiresAt == nil || pending.NextAttemptAt == nil || pending.NextAttemptAt.Sub(pending.CreatedAt) < 700*time.Millisecond {
		t.Fatalf("pending operation = %+v, want the first retry at least 700 ms after createdAt (StartApp option ignored?)", pending)
	}
	expiresAt := *pending.ExpiresAt
	stopFirst()
	stoppedFirst = true
	var status string
	if err := e.Owner.QueryRow(t.Context(), `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`, refund).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING_REFERENCE" {
		t.Fatalf("the first app rejected it before it stopped (%s): the TTL is too short for this machine", status)
	}
	testkit.Eventually(t, 10*time.Second, "the TTL to pass with every instance down", func(context.Context) (bool, error) {
		return time.Now().After(expiresAt), nil
	})

	second, stopSecond, err := e.StartApp(t.Context(), slow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSecond)
	rejected := waitStatus(t, second.Client(t, "provider-a"), "provider-a", refund, "REJECTED")
	if rejected.FailureCode != "REFERENCE_NOT_FOUND" || rejected.Balance == nil || rejected.Balance.Amount != "100.00" ||
		rejected.CompletedAt == nil || rejected.CompletedAt.Before(expiresAt) {
		t.Fatalf("operation = %+v, want REJECTED REFERENCE_NOT_FOUND after %s with balance 100.00", rejected, expiresAt)
	}
	assertStoredConsistent(t, e, w.ID)
}
