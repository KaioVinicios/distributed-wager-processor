package observability_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"

	"github.com/KaioVinicios/pda/internal/observability"
)

// freeAddr reserves a loopback port and releases it for the server under test.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func get(t *testing.T, url string) (int, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// Covers: FX-03, FX-04
func TestServeOnLifecycle_ServesUntilStopped(t *testing.T) {
	addr := freeAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: time.Second}

	lc := fxtest.NewLifecycle(t)
	observability.ServeOnLifecycle(lc, srv, time.Second, discard(), "test")
	lc.RequireStart()

	if code, err := get(t, "http://"+addr+"/ping"); err != nil || code != http.StatusNoContent {
		t.Fatalf("GET /ping = %d, %v; want 204", code, err)
	}
	lc.RequireStop()
	if _, err := get(t, "http://"+addr+"/ping"); err == nil {
		t.Fatal("server still answering after stop")
	}
}

// Covers: FX-02 (review focus: busy port must fail Start)
func TestServeOnLifecycle_StartFailsWhenPortIsBusy(t *testing.T) {
	busy, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer busy.Close()

	srv := &http.Server{Addr: busy.Addr().String(), Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}
	lc := fxtest.NewLifecycle(t)
	observability.ServeOnLifecycle(lc, srv, time.Second, discard(), "test")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := lc.Start(ctx); err == nil {
		_ = lc.Stop(ctx)
		t.Fatal("Start() error = nil, want address-in-use error")
	}
}
