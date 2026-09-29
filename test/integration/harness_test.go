//go:build integration

package integration_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-08, AUTH-08, DOC-06, D-20
//
// The in-process application answers through the contract validator.
func TestHarness(t *testing.T) {
	t.Parallel()
	anonymous := server.Client(t, "")

	ready := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/health/ready"})
	var health struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	ready.JSON(t, &health)
	if ready.Status != http.StatusOK || health.Checks["postgres"] != "UP" || health.Checks["sqs"] != "UP" {
		t.Fatalf("GET /health/ready = %d %+v", ready.Status, health)
	}

	doc := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/openapi.yaml"})
	if doc.Status != http.StatusOK || !bytes.Equal(doc.Body, api.OpenAPI) {
		t.Fatalf("GET /openapi.yaml = %d, %d bytes", doc.Status, len(doc.Body))
	}

	denied := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + testkit.NewID()})
	if p := denied.Problem(t); denied.Status != http.StatusUnauthorized || p.Code != "UNAUTHENTICATED" {
		t.Fatalf("GET /wallets/{id} without a token = %d %s", denied.Status, p.Code)
	}
}
