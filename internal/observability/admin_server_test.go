package observability_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: OBS-03
func TestAdminHandler_ServesProcessMetrics(t *testing.T) {
	h := observability.NewAdminHandler(observability.NewRegistry())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatalf("GET /metrics body lacks go_goroutines:\n%s", rec.Body.String())
	}
}
