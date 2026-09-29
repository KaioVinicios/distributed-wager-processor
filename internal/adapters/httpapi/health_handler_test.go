package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/observability"
)

type stubChecker struct {
	name string
	err  error
}

func (s stubChecker) Name() string                { return s.name }
func (s stubChecker) Check(context.Context) error { return s.err }

func serve(t *testing.T, method, path string, cs ...observability.Checker) *httptest.ResponseRecorder {
	t.Helper()
	h := observability.NewHealth(slog.New(slog.DiscardHandler), cs, time.Second)
	rec := httptest.NewRecorder()
	httpapi.NewMux(h).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return body
}

// Covers: HTTP-08, AUTH-08
func TestLive_AlwaysUp(t *testing.T) {
	rec := serve(t, http.MethodGet, "/health/live", stubChecker{name: "postgres", err: errors.New("down")})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health/live = %d, want 200", rec.Code)
	}
	if body := decode(t, rec); body["status"] != "UP" {
		t.Fatalf("body = %v, want status UP", body)
	}
}

// Covers: HTTP-08
func TestReady_UpWhenAllChecksPass(t *testing.T) {
	rec := serve(t, http.MethodGet, "/health/ready", stubChecker{name: "postgres"}, stubChecker{name: "sqs"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health/ready = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	checks, _ := body["checks"].(map[string]any)
	if body["status"] != "UP" || checks["postgres"] != "UP" || checks["sqs"] != "UP" {
		t.Fatalf("body = %v, want all UP", body)
	}
}

// Covers: HTTP-08, OBS-02
func TestReady_ServiceUnavailableWithoutLeakingReason(t *testing.T) {
	rec := serve(t, http.MethodGet, "/health/ready",
		stubChecker{name: "postgres", err: errors.New("password authentication failed for user pda_app")},
		stubChecker{name: "sqs"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /health/ready = %d, want 503", rec.Code)
	}
	body := decode(t, rec)
	checks, _ := body["checks"].(map[string]any)
	if body["status"] != "DOWN" || checks["postgres"] != "DOWN" || checks["sqs"] != "UP" {
		t.Fatalf("body = %v, want DOWN with postgres DOWN", body)
	}
	if strings.Contains(rec.Body.String(), "password") {
		t.Fatalf("body leaks the failure reason: %s", rec.Body.String())
	}
}

// Covers: HTTP-08
func TestHealthRoutes_RejectOtherMethods(t *testing.T) {
	if rec := serve(t, http.MethodPost, "/health/ready"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health/ready = %d, want 405", rec.Code)
	}
}
