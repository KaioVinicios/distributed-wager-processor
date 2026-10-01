//go:build integration

package integration_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-05, TST-A02, E2 (A02a)
func TestProviderIsolationQueries(t *testing.T) {
	t.Parallel()
	a, b := server.Client(t, "provider-a"), server.Client(t, "provider-b")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	placed := result(t, a, bet, http.StatusOK)

	byID := b.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + placed.TransactionID})
	if p := byID.Problem(t); byID.Status != http.StatusNotFound || p.Code != "TRANSACTION_NOT_FOUND" {
		t.Fatalf("provider-b reads a transaction of provider-a by id: %d %+v", byID.Status, p)
	}
	byExt := b.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/provider-a/wagering/transactions/" + bet.ExternalTransactionID})
	if p := byExt.Problem(t); byExt.Status != http.StatusForbidden || p.Code != "PROVIDER_MISMATCH" {
		t.Fatalf("provider-b reads under /providers/provider-a: %d %+v", byExt.Status, p)
	}
	for _, body := range [][]byte{byID.Body, byExt.Body} {
		if strings.Contains(string(body), placed.TransactionID) || strings.Contains(string(body), w.ID) {
			t.Fatalf("the answer exposes data of provider-a: %s", body)
		}
	}
	opening := server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + placed.TransactionID})
	if opening.Status != http.StatusOK {
		t.Fatalf("the internal service reads the transaction: %d", opening.Status)
	}
}

// Covers: AUTH-04, AUTH-05, TST-A02, E2 (A02b)
func TestProviderIsolationReplay(t *testing.T) {
	t.Parallel()
	a, b := server.Client(t, "provider-a"), server.Client(t, "provider-b")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	placed := result(t, a, bet, http.StatusOK)

	replay := submit(t, b, bet) // the body and the key of provider-a
	if p := replay.Problem(t); replay.Status != http.StatusForbidden || p.Code != "PROVIDER_MISMATCH" ||
		strings.Contains(string(replay.Body), placed.TransactionID) {
		t.Fatalf("provider-b replays provider-a: %d %s", replay.Status, replay.Body)
	}
	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "90.00", true)
}

// Covers: AUTH-07, TST-A03, E2 (A03)
//
// Not parallel: the counts are global, so no other test may write meanwhile.
func TestUnauthorizedHasNoEffects(t *testing.T) {
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	before := testkit.SnapshotCounts(t, server.Owner())

	attempts := []struct {
		name   string
		client *testkit.Client
		req    testkit.Request
		status int
	}{
		{"no token", server.Client(t, ""), postWager(bet), http.StatusUnauthorized},
		{"forged token", server.ClientWithToken(testkit.ForgedToken(t)), postWager(bet), http.StatusUnauthorized},
		{"another provider", server.Client(t, "provider-b"), postWager(bet), http.StatusForbidden},
		{"internal service", server.Client(t, "wallet-service"), postWager(bet), http.StatusForbidden},
		{"no role", server.Client(t, "no-role-client"), postWager(bet), http.StatusForbidden},
		{"provider opens a wallet", a, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": testkit.NewID(), "initialBalance": testkit.BRL("5.00")}}, http.StatusForbidden},
		{"provider reconciles", a, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"}, http.StatusForbidden},
	}
	for _, at := range attempts {
		if resp := at.client.Do(t, at.req); resp.Status != at.status {
			t.Fatalf("%s: %d %s, want %d", at.name, resp.Status, resp.Body, at.status)
		}
	}
	if after := testkit.SnapshotCounts(t, server.Owner()); !maps.Equal(before, after) {
		t.Fatalf("rows changed: before %v, after %v", before, after)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("100.00") || got.Version != 1 {
		t.Fatalf("wallet = %+v, want untouched", got)
	}
}

func postWager(body testkit.Wager) testkit.Request {
	return testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
		Header: http.Header{"Idempotency-Key": {body.ProviderID + ":" + body.ExternalTransactionID}},
	}
}

// Covers: AUTH-01, AUTH-03, TST-A01, E1 (A01a)
func TestAuthRealIdP(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	wantResult(t, result(t, server.Client(t, "provider-a"), wager(w, "provider-a", "BET", "10.00", unique("bet"), ""), http.StatusOK),
		"PROCESSED", "", "90.00", false)
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("90.00") {
		t.Fatalf("wallet = %+v", got)
	}
}

// expiredToken is a real token of provider-short-lived (5 s of life), once it
// is past its exp plus the clock skew of the application under test (1 s).
func expiredToken(t *testing.T) string {
	t.Helper()
	raw := testkit.FreshToken(t, "provider-short-lived")
	time.Sleep(time.Until(testkit.TokenExpiry(t, raw).Add(time.Second + 500*time.Millisecond)))
	return raw
}

// Covers: AUTH-02, TST-A01, E1 (A01b)
//
// Sensitivity: SkipClientIDCheck in the verifier → "another audience" is
// accepted (403 FORBIDDEN, the client has no role) instead of 401.
func TestAuthRejects(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	path := "/wallets/" + w.ID
	cases := map[string]string{
		"malformed":        "not-a-jwt",
		"forged signature": testkit.ForgedToken(t),
		"alg none":         testkit.UnsignedToken(t),
		"HS256":            testkit.HS256Token(t),
		"another audience": testkit.Token(t, "no-audience-client"),
		"another realm":    testkit.OtherRealmToken(t),
	}
	check := func(t *testing.T, c *testkit.Client, challenge string) {
		t.Helper()
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: path})
		if p := resp.Problem(t); resp.Status != http.StatusUnauthorized || p.Code != "UNAUTHENTICATED" ||
			resp.Header.Get("WWW-Authenticate") != challenge {
			t.Fatalf("%d %+v, WWW-Authenticate %q", resp.Status, p, resp.Header.Get("WWW-Authenticate"))
		}
	}
	t.Run("no token", func(t *testing.T) {
		t.Parallel()
		check(t, server.Client(t, ""), `Bearer realm="pda"`)
	})
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			check(t, server.ClientWithToken(raw), `Bearer realm="pda", error="invalid_token"`)
		})
	}
	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		check(t, server.ClientWithToken(expiredToken(t)), `Bearer realm="pda", error="invalid_token"`)
	})
}

// Covers: AUTH-06, TST-A02, E2 (A02c)
//
// Sensitivity: the roles of POST /wallets removed from the route table →
// provider-a opens a wallet (201) instead of 403.
func TestInternalOperationsRestricted(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	walletRoutes := []testkit.Request{
		{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": testkit.NewID(), "initialBalance": testkit.BRL("1.00")}},
		{Method: http.MethodGet, Path: "/wallets/" + w.ID},
		{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger"},
		{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"},
	}
	for _, client := range []string{"provider-a", "provider-b", "no-role-client"} {
		for _, req := range walletRoutes {
			resp := server.Client(t, client).Do(t, req)
			if p := resp.Problem(t); resp.Status != http.StatusForbidden || p.Code != "FORBIDDEN" {
				t.Fatalf("%s %s %s = %d %+v", client, req.Method, req.Path, resp.Status, p)
			}
		}
	}
	resp := submit(t, server.Client(t, "wallet-service"), wager(w, "provider-a", "BET", "1.00", unique("bet"), ""))
	if p := resp.Problem(t); resp.Status != http.StatusForbidden || p.Code != "FORBIDDEN" {
		t.Fatalf("wallet-service submits an operation: %d %+v", resp.Status, p)
	}
}

// Covers: AUTH-08, D-20 (A04)
//
// Sensitivity: GET /wallets/{walletId} registered without roles → it answers
// 404 without a token instead of 401.
func TestPublicEndpoints(t *testing.T) {
	t.Parallel()
	anonymous := server.Client(t, "")
	public := map[string]bool{"GET /health/live": true, "GET /health/ready": true, "GET /docs": true, "GET /openapi.yaml": true}
	id := strings.NewReplacer("{walletId}", testkit.NewID(), "{transactionId}", testkit.NewID(),
		"{providerId}", "provider-a", "{externalTransactionId}", "ext-1")
	for _, route := range httpapi.Routes(true) {
		method, path, _ := strings.Cut(route, " ")
		resp := anonymous.Do(t, testkit.Request{Method: method, Path: id.Replace(path), Invalid: true})
		switch {
		case public[route] && resp.Status != http.StatusOK:
			t.Errorf("%s without a token = %d, want 200", route, resp.Status)
		case !public[route] && resp.Status != http.StatusUnauthorized:
			t.Errorf("%s without a token = %d, want 401", route, resp.Status)
		}
	}
}

// Covers: AUTH-04, AUTH-05, AUTH-07, AUTH-09, TST-A02, TST-A03, E2 (A05)
// Sensitivity: matchProvider returning nil → the REFUND of provider-b in the name of provider-a is PROCESSED and "4 messages in the DLQ" is never reached.
//
// The scenario of the final audit (D-23): provider-b, with its own token,
// sends a REFUND naming provider-a against a BET of provider-a. It, and the
// messages without a valid provider token, go to the DLQ and record nothing.
// The queue is the package's isolated one; the IAM permission to send is I04f.
//
// Not parallel: the counts are global, so no other test may write meanwhile.
func TestSQSProviderIdentity(t *testing.T) {
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	result(t, server.Client(t, "provider-a"), bet, http.StatusOK)
	before := testkit.SnapshotCounts(t, server.Owner())

	sends := []struct {
		code string
		body testkit.Wager
		opts testkit.SendOpts
	}{
		{
			"PROVIDER_MISMATCH", wager(w, "provider-a", "REFUND", "30.00", unique("refund"), bet.ExternalTransactionID),
			testkit.SendOpts{Token: testkit.Token(t, "provider-b")},
		},
		{"UNAUTHENTICATED", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{NoToken: true}},
		{"FORBIDDEN", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{Token: testkit.Token(t, "wallet-service")}},
		{"UNAUTHENTICATED", wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), testkit.SendOpts{Token: testkit.ForgedToken(t)}},
	}
	want := map[string]string{} // SQS id → errorCode
	for _, s := range sends {
		o := s.opts
		o.GroupID = w.ID
		want[server.SendWager(t, sqsWager(t, unique("msg"), s.body), o)] = s.code
	}
	for _, m := range server.ReceiveDLQ(t, len(sends)) {
		id := m.Attributes["originalMessageId"]
		if code, ok := want[id]; !ok || m.Attributes["errorCode"] != code || m.Attributes["errorCategory"] != "CORRECTABLE" {
			t.Errorf("DLQ message of %s = %v, want errorCode %s", id, m.Attributes, code)
		}
		if _, leaked := m.Attributes["accessToken"]; leaked {
			t.Errorf("the DLQ copy of %s carries the access token", id)
		}
	}
	server.AssertQueueDrained(t)
	if after := testkit.SnapshotCounts(t, server.Owner()); !maps.Equal(before, after) {
		t.Fatalf("rows changed: before %v, after %v", before, after)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("70.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want 70.00 after the BET only", got)
	}
}
