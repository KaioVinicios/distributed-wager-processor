package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: HTTP-09, D-18 (U17: correlation)
func TestEdgeCorrelationID(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	cases := []struct {
		name, sent string
		kept       bool
	}{
		{"valid id is kept", "abc-123_X.y", true},
		{"invalid characters are replaced", "bad id!", false},
		{"too long is replaced", strings.Repeat("a", 129), false},
		{"absent is generated", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.sent != "" {
				h.Set("X-Correlation-Id", tc.sent)
			}
			rec := e.do(t, call{method: http.MethodGet, path: "/nope", header: h})
			got := rec.Header().Get("X-Correlation-Id")
			switch {
			case tc.kept && got != tc.sent:
				t.Fatalf("correlation = %q, want %q", got, tc.sent)
			case !tc.kept && (got == "" || got == tc.sent || len(got) != 36):
				t.Fatalf("correlation = %q, want a generated UUID", got)
			}
			wantProblem(t, rec, http.StatusNotFound, "ROUTE_NOT_FOUND", "")
		})
	}
}

// Covers: HTTP-09 (U17: route fallback)
func TestEdgeRouteFallback(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/nope"}), http.StatusNotFound, "ROUTE_NOT_FOUND", "")
	rec := e.do(t, call{method: http.MethodDelete, path: "/wallets"})
	wantProblem(t, rec, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/docs"}), http.StatusNotFound, "ROUTE_NOT_FOUND", "")
}

// Covers: AUTH-02, AUTH-06, AUTH-07, AUTH-08 (U17: authentication and roles)
func TestEdgeAuthentication(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)

	t.Run("missing token", func(t *testing.T) {
		rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})
		wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda"` {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	})
	t.Run("other scheme counts as missing", func(t *testing.T) {
		rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", header: http.Header{"Authorization": {"Basic dTpw"}}})
		wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda"` {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	})
	for _, token := range []string{"forged", " "} {
		t.Run("invalid token "+token, func(t *testing.T) {
			rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", header: http.Header{"Authorization": {"Bearer " + token}}})
			wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda", error="invalid_token"` {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
	t.Run("role of another kind of caller", func(t *testing.T) {
		for _, c := range []call{
			{method: http.MethodGet, path: "/wallets/w-1", token: tokenProviderA},
			{method: http.MethodPost, path: "/wallets/w-1/reconciliation", token: tokenNoRole},
			{method: http.MethodPost, path: "/wagering/transactions", token: tokenInternal, contentType: "application/json", body: "{}"},
			{method: http.MethodGet, path: "/wagering/transactions/t-1", token: tokenNoRole},
			{method: http.MethodGet, path: "/providers/provider-a/wagering/transactions/e-1", token: tokenNoRole},
		} {
			wantProblem(t, e.do(t, c), http.StatusForbidden, "FORBIDDEN", "")
		}
	})
	t.Run("public routes need no token", func(t *testing.T) {
		for _, path := range []string{"/health/live", "/health/ready"} {
			if rec := e.do(t, call{method: http.MethodGet, path: path}); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", path, rec.Code)
			}
		}
	})
}

// Covers: HTTP-09 (U17: media type)
func TestEdgeRequiresJSON(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		rec := e.do(t, call{method: http.MethodPost, path: "/wallets", token: tokenInternal, contentType: ct, body: "{}"})
		wantProblem(t, rec, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "")
	}
	// The media type is checked after the credentials (lifecycle §6.1).
	wantProblem(t, e.do(t, call{method: http.MethodPost, path: "/wallets", contentType: "text/plain"}), http.StatusUnauthorized, "UNAUTHENTICATED", "")
}

// Covers: HTTP-09 (U17: panic)
func TestEdgeRecoversPanics(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenPanic}), http.StatusInternalServerError, "INTERNAL_ERROR", "")
	if logs := e.logs.String(); !strings.Contains(logs, "stub authenticator panic") || !strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("logs = %s, want the panic at ERROR", logs)
	}
}

// Covers: OBS-01, OBS-02 (U17: access log)
func TestEdgeAccessLog(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	h := http.Header{"X-Correlation-Id": {"corr-log-1"}}
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenProviderA, header: h})
	logs := e.logs.String()
	for _, want := range []string{`"msg":"http request"`, `"method":"GET"`, `"route":"GET /wallets/{walletId}"`, `"status":403`, `"correlationId":"corr-log-1"`, `"providerId":"provider-a"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("access log %s lacks %s", logs, want)
		}
	}
	if strings.Contains(logs, tokenProviderA) {
		t.Fatalf("access log leaks the token: %s", logs)
	}
}

// Covers: DOC-06, D-20
func TestEdgeDocs(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, true)
	rec := e.do(t, call{method: http.MethodGet, path: "/openapi.yaml"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml" || rec.Body.String() != string(api.OpenAPI) {
		t.Fatalf("GET /openapi.yaml = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = e.do(t, call{method: http.MethodGet, path: "/docs"})
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), "swagger-ui-dist@5.33.0") || !strings.Contains(rec.Body.String(), `url: "/openapi.yaml"`) {
		t.Fatalf("GET /docs = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// Covers: OBS-03 (U20)
// Sensitivity: labeling with r.URL.Path instead of the route pattern → the "/wallets/w-1" label appears and the pattern assertion fails.
func TestEdgeRequestMetrics(t *testing.T) {
	m := &recordedMetrics{}
	e := newEdgeWith(t, httpapi.Services{}, false, m)

	e.do(t, call{method: http.MethodGet, path: "/health/live"})
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})         // no token: 401
	e.do(t, call{method: http.MethodGet, path: "/nope/12345/whatever"}) // unmatched
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-2", token: tokenNoRole})

	m.mu.Lock()
	defer m.mu.Unlock()
	want := []string{
		"GET /health/live|GET|200",
		"GET /wallets/{walletId}|GET|401",
		"unmatched|GET|404",
		"GET /wallets/{walletId}|GET|403",
	}
	if strings.Join(m.requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", m.requests, want)
	}
}

// Covers: AUTH-02, AUTH-05, OBS-03 (U20)
// Sensitivity: removing the "forbidden" count in authenticate → the third entry is missing.
func TestEdgeAuthFailureMetrics(t *testing.T) {
	m := &recordedMetrics{}
	e := newEdgeWith(t, httpapi.Services{}, false, m)

	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})                     // no token
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: "forged"})    // invalid token
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenNoRole}) // no role
	e.do(t, call{method: http.MethodGet, path: "/providers/provider-b/wagering/transactions/x", token: tokenProviderA})

	m.mu.Lock()
	defer m.mu.Unlock()
	want := []string{"unauthenticated", "unauthenticated", "forbidden", "provider_mismatch"}
	if strings.Join(m.failures, ",") != strings.Join(want, ",") {
		t.Fatalf("failures = %v, want %v", m.failures, want)
	}
}

// Covers: OBS-01 (closes the M3 minor: an empty route in the access log)
func TestEdgeAccessLogUnmatchedRoute(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	e.do(t, call{method: http.MethodGet, path: "/nope"})
	if line := e.logs.String(); !strings.Contains(line, `"route":"unmatched"`) {
		t.Fatalf("access log = %s, want route unmatched", line)
	}
}

// blockingReader answers GetWallet only when the request's context ends, as
// a query on a database that does not answer.
type blockingReader struct{ *stubReader }

func (blockingReader) GetWallet(ctx context.Context, _ string) (wallet.Wallet, error) {
	<-ctx.Done()
	return wallet.Wallet{}, ctx.Err()
}

// slowChecker takes 100 ms, within the 2 s of a health check.
type slowChecker struct{}

func (slowChecker) Name() string { return "postgres" }

func (slowChecker) Check(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// Covers: HTTP-09, D-04 (U30: request deadline)
func TestEdgeRequestDeadline(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	e := edge{logs: &syncBuffer{}, handler: httpapi.New(
		httpapi.Options{Log: log, RequestTimeout: 50 * time.Millisecond},
		httpapi.Services{
			Auth: stubAuth{}, Queries: blockingReader{&stubReader{}},
			Health: observability.NewHealth(log, []observability.Checker{slowChecker{}}, 2*time.Second),
		},
	)}

	t.Run("an authenticated route ends with 503", func(t *testing.T) {
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenInternal}) }()
		select {
		case rec := <-answered:
			wantProblem(t, rec, http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", "")
			if got := rec.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no answer within 2s: the request has no deadline")
		}
	})
	t.Run("the health has no deadline", func(t *testing.T) {
		if rec := e.do(t, call{method: http.MethodGet, path: "/health/ready"}); rec.Code != http.StatusOK {
			t.Fatalf("GET /health/ready = %d, want 200: a 100 ms check fits its 2 s", rec.Code)
		}
	})
}
