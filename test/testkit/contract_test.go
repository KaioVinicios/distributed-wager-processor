package testkit_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

func exchange(ctx context.Context, method, path, reqBody string, status int, contentType, respBody string) (*http.Request, *http.Response) {
	req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1:1"+path, strings.NewReader(reqBody))
	if reqBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte(respBody)))}
	resp.Header.Set("Content-Type", contentType)
	return req, resp
}

const (
	problemBody   = `{"type":"about:blank","title":"Not Found","status":404,"code":"WALLET_NOT_FOUND","category":"CORRECTABLE","detail":"d","correlationId":"c"}`
	malformedBody = `{"type":"about:blank","title":"Bad Request","status":400,"code":"MALFORMED_REQUEST","category":"CORRECTABLE","detail":"d","correlationId":"c"}`
)

// Covers: D-20, DOC-06
//
// The validator that every API test relies on must catch drift: a response
// or a request off the contract fails.
func TestContract(t *testing.T) {
	c, err := testkit.LoadContract()
	if err != nil {
		t.Fatal(err)
	}
	check := func(method, path, reqBody string, status int, ct, respBody string, skip bool) error {
		req, resp := exchange(t.Context(), method, path, reqBody, status, ct, respBody)
		defer resp.Body.Close()
		return c.Check(req, []byte(reqBody), resp, []byte(respBody), skip)
	}
	const wallet = "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37"

	if err := check(http.MethodGet, wallet, "", 404, "application/problem+json", problemBody, false); err != nil {
		t.Fatalf("documented exchange: %v", err)
	}
	for name, err := range map[string]error{
		"field outside the schema": check(http.MethodGet, wallet, "", 404, "application/problem+json", strings.Replace(problemBody, `"c"}`, `"c","x":1}`, 1), false),
		"code outside the enum":    check(http.MethodGet, wallet, "", 404, "application/problem+json", strings.Replace(problemBody, "WALLET_NOT_FOUND", "NOPE", 1), false),
		"undocumented status":      check(http.MethodGet, wallet, "", 501, "text/plain", "", false),
		"request off the contract": check(http.MethodPost, "/wallets", `{"x":1}`, 400, "application/problem+json", malformedBody, false),
	} {
		if err == nil {
			t.Errorf("%s: Check = nil, want an error", name)
		}
	}
	if err := check(http.MethodPost, "/wallets", `{"x":1}`, 400, "application/problem+json", malformedBody, true); err != nil {
		t.Fatalf("a deliberately invalid request is not validated: %v", err)
	}
	if err := check(http.MethodGet, "/openapi.yaml", "", 200, "application/yaml", "openapi: 3.0.3\n", false); err != nil {
		t.Fatalf("yaml body: %v", err)
	}
	if err := check(http.MethodGet, "/docs", "", 200, "text/html; charset=utf-8", "<html></html>", false); err != nil {
		t.Fatalf("html body: %v", err)
	}
	if err := check(http.MethodGet, "/nope", "", 404, "application/problem+json", "{}", false); err != nil {
		t.Fatalf("a route outside the document is skipped: %v", err)
	}
}
