//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// logLines returns the JSON log lines of the app that mention marker, decoded.
func logLines(t *testing.T, marker string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(server.Logs()) {
		if !strings.Contains(line, marker) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// Covers: OBS-01, OBS-02 (I14)
// Sensitivity: logging the Authorization header in logAccess → the token check fails; logging the amount in "wager concluded" → the amount check fails.
func TestLogsHaveIdsWithoutSecrets(t *testing.T) {
	t.Parallel()
	const amount = "4242.42"
	token := testkit.Token(t, "provider-a")
	a := server.ClientWithToken(token)
	w := server.OpenWallet(t, testkit.BRL("9000.00"))
	provider, ext := "provider-a", unique("bet")
	key := provider + ":" + ext
	corr := "corr-" + testkit.NewID()

	// HTTP: a BET, its replay, a conflict (same key, other amount), a rejection.
	bet := wager(w, provider, "BET", amount, ext, "")
	send := func(c *testkit.Client, body testkit.Wager, k string) *testkit.Response {
		return c.Do(t, testkit.Request{
			Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
			Header: http.Header{"Idempotency-Key": {k}, "X-Correlation-Id": {corr}},
		})
	}
	if r := send(a, bet, key); r.Status != http.StatusOK {
		t.Fatalf("BET = %d %s", r.Status, r.Body)
	}
	if r := send(a, bet, key); r.Status != http.StatusOK {
		t.Fatalf("replay = %d %s", r.Status, r.Body)
	}
	other := bet
	other.Money = testkit.BRL("1.11")
	if r := send(a, other, key); r.Status != http.StatusConflict {
		t.Fatalf("same key, other content = %d %s, want 409", r.Status, r.Body)
	}
	if r := send(a, wager(w, provider, "BET", "99999.99", unique("bet"), ""), unique("key")); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("BET without funds = %d %s, want 422", r.Status, r.Body)
	}
	// Refused accesses: no token, and a role that does not fit.
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	server.Client(t, "wallet-service").Do(t, testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: bet,
		Header: http.Header{"Idempotency-Key": {key}},
	})
	// SQS: another BET, with the envelope messageId.
	msgID, sqsExt := unique("msg"), unique("sqsbet")
	server.SendWager(t, sqsWager(t, msgID, wager(w, provider, "BET", "12.34", sqsExt, "")), testkit.SendOpts{GroupID: w.ID, CorrelationID: corr})
	testkit.Eventually(t, 15*time.Second, "the SQS BET is concluded", func(context.Context) (bool, error) {
		return len(logLines(t, msgID)) > 0, nil
	})
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})

	logs := server.Logs()
	// Markers unique to this test: no line of the whole app log may carry them.
	for _, secret := range []string{token, "Bearer ", amount, key} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the log leaks %q", secret)
		}
	}
	// Common values: only the lines about this wallet are checked.
	for _, secret := range []string{"12.34", "9000.00"} {
		for line := range strings.Lines(logs) {
			if strings.Contains(line, w.ID) && strings.Contains(line, secret) {
				t.Fatalf("a log line about the wallet leaks %q: %s", secret, line)
			}
		}
	}
	for line := range strings.Lines(logs) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
	}
	var viaHTTP, viaSQS map[string]any
	for _, m := range logLines(t, `"msg":"wager concluded"`) {
		if m["walletId"] == w.ID && m["channel"] == "http" && m["replay"] == false && m["outcome"] == "processed" {
			viaHTTP = m
		}
		if m["walletId"] == w.ID && m["channel"] == "sqs" {
			viaSQS = m
		}
	}
	for name, m := range map[string]map[string]any{"http": viaHTTP, "sqs": viaSQS} {
		if m == nil {
			t.Fatalf("no %q conclusion line for wallet %s", name, w.ID)
		}
		for _, k := range []string{"transactionId", "walletId", "providerId", "correlationId"} {
			if s, _ := m[k].(string); s == "" {
				t.Fatalf("%s conclusion line lacks %s: %v", name, k, m)
			}
		}
	}
	if viaHTTP["correlationId"] != corr || viaSQS["messageId"] != msgID {
		t.Fatalf("correlation/message ids = %v / %v, want %s / %s", viaHTTP["correlationId"], viaSQS["messageId"], corr, msgID)
	}
	if len(logLines(t, `"route":"POST /wagering/transactions"`)) == 0 {
		t.Fatal("no access log line names the route pattern")
	}
}

// Covers: OBS-03, HTTP-07, AUTH-02 (I25)
// Sensitivity: labeling the route with the raw path → the "{walletId}" series never grows; not counting refused tokens → auth_failures stays flat.
func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	series := map[string]string{
		"processed": `wager_transactions_total{channel="http",failure_code="",kind="BET",outcome="processed"}`,
		"rejected":  `wager_transactions_total{channel="http",failure_code="INSUFFICIENT_FUNDS",kind="BET",outcome="rejected"}`,
		"dup":       `wager_duplicates_total{channel="http",layer="idempotency"}`,
		"unauth":    `auth_failures_total{reason="unauthenticated"}`,
		"forbidden": `auth_failures_total{reason="forbidden"}`,
		"recon":     `reconciliation_runs_total{consistent="true"}`,
		"route":     `http_requests_total{method="GET",route="GET /wallets/{walletId}",status="200"}`,
		"unmatched": `http_requests_total{method="GET",route="unmatched",status="404"}`,
	}
	before := map[string]int64{}
	for name, sample := range series {
		before[name] = server.MetricValue(t, sample)
	}

	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "80.00", unique("bet"), "")
	result(t, a, bet, http.StatusOK)
	result(t, a, bet, http.StatusOK) // replay
	result(t, a, wager(w, "provider-a", "BET", "80.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	internal := server.Client(t, "wallet-service")
	internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID}) // provider on an internal route: 403
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/no/such/" + testkit.NewID()})

	for name, sample := range series {
		if got := server.MetricValue(t, sample) - before[name]; got < 1 {
			t.Fatalf("%s grew by %v, want at least 1", sample, got)
		}
	}
	// The admin port serves /metrics; the API port does not.
	resp := server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/metrics"})
	if resp.Status != http.StatusNotFound {
		t.Fatalf("GET /metrics on the API port = %d, want 404", resp.Status)
	}
}

// Covers: OBS-03, CONC-01 (I26)
// Sensitivity: dropping the ErrLockTimeout wrap in postgres.translate → the counter stays flat.
func TestConcurrencyConflictMetric(t *testing.T) {
	t.Parallel()
	const sample = `concurrency_conflicts_total{reason="lock_timeout"}`
	before := server.MetricValue(t, sample)
	w := server.OpenWallet(t, testkit.BRL("100.00"))

	// Hold the wallet row so the BET waits for the lock until lock_timeout.
	tx, err := server.Owner().Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, w.ID); err != nil {
		t.Fatalf("lock: %v", err)
	}

	a := server.Client(t, "provider-a")
	resp := submit(t, a, wager(w, "provider-a", "BET", "10.00", unique("bet"), ""))
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("BET on a locked wallet = %d %s, want 503", resp.Status, resp.Body)
	}
	if got := server.MetricValue(t, sample) - before; got < 1 {
		t.Fatalf("%s grew by %v, want at least 1", sample, got)
	}
}
