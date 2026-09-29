package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Covers: ART-04 (review focus: wildcard and empty hosts)
func TestProbeURL(t *testing.T) {
	cases := []struct{ addr, want string }{
		{":8080", "http://127.0.0.1:8080/health/ready"},
		{"0.0.0.0:8081", "http://127.0.0.1:8081/health/ready"},
		{"[::]:8082", "http://127.0.0.1:8082/health/ready"},
		{"127.0.0.1:9000", "http://127.0.0.1:9000/health/ready"},
		{"localhost:8080", "http://localhost:8080/health/ready"},
	}
	for _, tc := range cases {
		got, err := probeURL(tc.addr)
		if err != nil || got != tc.want {
			t.Errorf("probeURL(%q) = %q, %v; want %q", tc.addr, got, err, tc.want)
		}
	}
	if _, err := probeURL("no-port"); err == nil {
		t.Error("probeURL(no-port) error = nil, want error")
	}
}

// Covers: ART-04
func TestProbe_ExitCodes(t *testing.T) {
	status := func(code int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	}
	up, down := status(http.StatusOK), status(http.StatusServiceUnavailable)
	defer up.Close()
	defer down.Close()
	closed := status(http.StatusOK)
	closedURL := closed.URL
	closed.Close()

	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	for _, tc := range []struct {
		name, url string
		want      int
	}{
		{"ready", up.URL, 0},
		{"not ready", down.URL, 1},
		{"connection refused", closedURL, 1},
	} {
		if got := probe(t.Context(), client, tc.url); got != tc.want {
			t.Errorf("%s: probe() = %d, want %d", tc.name, got, tc.want)
		}
	}
}
