package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Tokens of stubAuth.
const (
	tokenProviderA = "token-provider-a"
	tokenProviderB = "token-provider-b"
	tokenInternal  = "token-internal"
	tokenNoRole    = "token-no-role"
	tokenPanic     = "token-panic"
)

// stubAuth accepts the tokens above; anything else is unauthenticated.
type stubAuth struct{}

func (stubAuth) Authenticate(_ context.Context, raw string) (auth.Principal, error) {
	switch raw {
	case tokenProviderA:
		return auth.Principal{Subject: "a", ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}, nil
	case tokenProviderB:
		return auth.Principal{Subject: "b", ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}, nil
	case tokenInternal:
		return auth.Principal{Subject: "w", Roles: []auth.Role{auth.RoleWalletInternal}}, nil
	case tokenNoRole:
		return auth.Principal{Subject: "n"}, nil
	case tokenPanic:
		panic("stub authenticator panic")
	}
	return auth.Principal{}, auth.ErrUnauthenticated
}

// syncBuffer collects log lines written by concurrent handlers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// edge is the handler under test with its captured logs.
type edge struct {
	handler http.Handler
	logs    *syncBuffer
}

func newEdge(t *testing.T, s httpapi.Services, docs bool) edge {
	t.Helper()
	return newEdgeWith(t, s, docs, nil)
}

// newEdgeWith is newEdge with the metrics the edge reports to.
func newEdgeWith(t *testing.T, s httpapi.Services, docs bool, m httpapi.Metrics) edge {
	t.Helper()
	logs := &syncBuffer{}
	if s.Auth == nil {
		s.Auth = stubAuth{}
	}
	if s.Health == nil {
		s.Health = observability.NewHealth(slog.New(slog.DiscardHandler), nil, time.Second)
	}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return edge{handler: httpapi.New(httpapi.Options{DocsEnabled: docs, Log: log, Metrics: m}, s), logs: logs}
}

// recordedMetrics keeps what the edge reports.
type recordedMetrics struct {
	mu       sync.Mutex
	requests []string // route|method|status
	failures []string
}

func (m *recordedMetrics) HTTPRequest(route, method string, status int, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, fmt.Sprintf("%s|%s|%d", route, method, status))
}

func (m *recordedMetrics) AuthFailure(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, reason)
}

// call sends one request; body is sent as is.
type call struct {
	method, path, token, contentType string
	body                             string
	header                           http.Header
}

func (e edge) do(t *testing.T, c call) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), c.method, c.path, bytes.NewBufferString(c.body))
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// problem is the application/problem+json body.
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field"`
	CorrelationID string `json:"correlationId"`
}

// wantProblem fails unless rec is the problem code with status (and field
// when not empty), and returns it.
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code, field string) problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %s)", ct, rec.Body.String())
	}
	var p problem
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body.String(), err)
	}
	if p.Type != "about:blank" || p.Status != status || p.Title != http.StatusText(status) || p.Code != code ||
		p.Detail == "" || p.CorrelationID == "" || p.CorrelationID != rec.Header().Get("X-Correlation-Id") {
		t.Fatalf("problem = %+v, want %d %s", p, status, code)
	}
	if field != "" && p.Field != field {
		t.Fatalf("problem field = %q, want %q", p.Field, field)
	}
	return p
}

// decodeJSON decodes an application/json body strictly into v.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (body %s)", ct, rec.Body.String())
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	dec.UseNumber() // numbers stay json.Number: no float, as for money
	if err := dec.Decode(v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
}
