//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// The helpers of test/integration/helpers_test.go, repeated on purpose (spec
// M8, decision 13), over the cluster instead of the app in process.

// unique makes an external id unique across tests and runs.
func unique(prefix string) string { return prefix + "-" + testkit.NewID() }

// wager is an operation of provider on w; ref "" = no reference.
func wager(w testkit.Wallet, provider, kind, amount, ext, ref string) testkit.Wager {
	return testkit.Wager{
		ProviderID: provider, ExternalTransactionID: ext, PlayerID: w.PlayerID, WalletID: w.ID,
		RoundID: "round-1", GameID: "game-1", Kind: kind, Money: testkit.BRL(amount),
		ReferenceExternalTransactionID: ref,
	}
}

// wagerRequest posts the operation with the key {providerId}:{externalTransactionId}.
func wagerRequest(body testkit.Wager) testkit.Request {
	return testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
		Header: http.Header{"Idempotency-Key": {body.ProviderID + ":" + body.ExternalTransactionID}},
	}
}

// submit posts the operation.
func submit(t *testing.T, c *testkit.Client, body testkit.Wager) *testkit.Response {
	t.Helper()
	return c.Do(t, wagerRequest(body))
}

// balanceOf reads the wallet as the internal service.
func balanceOf(t *testing.T, walletID string) testkit.Wallet {
	t.Helper()
	var w testkit.Wallet
	cluster.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + walletID}).JSON(t, &w)
	return w
}

// sqsWager is the WagerTransactionRequested of the operation, with the key
// the HTTP helpers use.
func sqsWager(t *testing.T, messageID string, body testkit.Wager) string {
	t.Helper()
	money := body.Money
	return testkit.WagerMessage(t, messageID, testkit.WagerData{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		IdempotencyKey: body.ProviderID + ":" + body.ExternalTransactionID, PlayerID: body.PlayerID,
		WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID, Kind: body.Kind, Money: &money,
		ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	})
}

// together runs every fn at once, behind a start barrier (test-plan §1).
func together(fns ...func()) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, fn := range fns {
		wg.Go(func() {
			<-start
			fn()
		})
	}
	close(start)
	wg.Wait()
}

// inboxOutcomes waits until every message has its inbox row and counts the
// rows by outcome. The wait is on the test's own messages: the queue is shared
// by the tests running in parallel.
func inboxOutcomes(t *testing.T, messageIDs ...string) map[string]int {
	t.Helper()
	var outcomes []string
	testkit.Eventually(t, time.Minute, fmt.Sprintf("%d messages in the inbox", len(messageIDs)), func(ctx context.Context) (bool, error) {
		rows, err := cluster.Owner().Query(ctx, `SELECT outcome FROM inbox_messages WHERE message_id = ANY($1)`, messageIDs)
		if err != nil {
			return false, err
		}
		outcomes, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return len(outcomes) == len(messageIDs), err
	})
	counts := map[string]int{}
	for _, o := range outcomes {
		counts[o]++
	}
	return counts
}

// result submits and decodes the TransactionResult, failing unless the status
// is want.
func result(t *testing.T, c *testkit.Client, body testkit.Wager, want int) testkit.TransactionResult {
	t.Helper()
	resp := submit(t, c, body)
	var r testkit.TransactionResult
	resp.JSON(t, &r)
	if resp.Status != want {
		t.Fatalf("POST %s %s = %d %+v, want %d", body.Kind, body.ExternalTransactionID, resp.Status, r, want)
	}
	return r
}

// wantResult fails unless r has the status, failure code, balance ("" = none)
// and replay flag.
func wantResult(t *testing.T, r testkit.TransactionResult, status, code, balance string, replay bool) {
	t.Helper()
	gotBalance := ""
	if r.Balance != nil {
		gotBalance = r.Balance.Amount
	}
	if r.Status != status || r.FailureCode != code || gotBalance != balance || r.IdempotentReplay != replay {
		t.Fatalf("result = %+v (balance %q), want %s %s %q replay %v", r, gotBalance, status, code, balance, replay)
	}
}

// crashOn arms point on instance 0, which runs only component and HTTP (the
// round-robin skips it while it is armed); instances 1 and 2 run every role
// but component. The component under test is thus live on the armed instance
// alone (spec M8, decisions 5 and 15). The test defers cluster.Restore.
func crashOn(t *testing.T, point string, component config.Roles) {
	t.Helper()
	others := config.Roles{
		HTTP: true, Consumer: !component.Consumer,
		OutboxPublisher: !component.OutboxPublisher, ReferenceWorker: !component.ReferenceWorker,
	}
	cluster.Restart(t, 1, testkit.EnvOf(others))
	cluster.Restart(t, 2, testkit.EnvOf(others))
	component.HTTP = true
	cluster.Restart(t, 0, testkit.Fault(point), testkit.EnvOf(component))
}

// transactionOf waits until the operation exists and reads it as its provider.
func transactionOf(t *testing.T, provider, ext string) testkit.Transaction {
	t.Helper()
	c := cluster.Client(t, provider)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/" + provider + "/wagering/transactions/" + ext})
		if resp.Status == http.StatusOK {
			var tx testkit.Transaction
			resp.JSON(t, &tx)
			return tx
		}
	}
	t.Fatalf("operation %s not recorded within 30s", ext)
	return testkit.Transaction{}
}

// waitPublished waits until no outbox event of the wallet is left unpublished.
func waitPublished(t *testing.T, walletID string) {
	t.Helper()
	testkit.Eventually(t, 30*time.Second, "outbox of wallet "+walletID+" published", func(ctx context.Context) (bool, error) {
		var pending int
		err := cluster.Owner().QueryRow(ctx, `SELECT count(*) FROM outbox_events
			WHERE message_group_id = $1 AND published_at IS NULL`, walletID).Scan(&pending)
		return pending == 0, err
	})
}

// waitStatus polls the operation of provider until it has the status.
func waitStatus(t *testing.T, c *testkit.Client, provider, ext, status string) testkit.Transaction {
	t.Helper()
	var tx testkit.Transaction
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/" + provider + "/wagering/transactions/" + ext})
		if resp.Status != http.StatusOK {
			continue
		}
		resp.JSON(t, &tx)
		if tx.Status == status {
			return tx
		}
	}
	t.Fatalf("%s %s: not reached within 20s (last seen %q)", ext, status, tx.Status)
	return tx
}

// eventsOf waits for every outbox event of the wallet in the audit queue and
// returns their envelopes.
func eventsOf(t *testing.T, walletID string) []map[string]any {
	t.Helper()
	stored, err := testkit.OutboxPayloads(t.Context(), cluster.Owner(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	var out []map[string]any
	for _, deliveries := range cluster.Audit.WaitFor(t, ids...) {
		var env map[string]any
		if err := json.Unmarshal(deliveries[0].Body, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env)
	}
	return out
}
