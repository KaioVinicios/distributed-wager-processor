package observability_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
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

// recordingShutdowner counts Shutdown calls; the tests that use it expect none.
type recordingShutdowner struct{ calls atomic.Int32 }

func (s *recordingShutdowner) Shutdown(...fx.ShutdownOption) error {
	s.calls.Add(1)
	return nil
}

// Covers: FX-03, FX-04 (U31b: a normal stop never shuts the process down)
// Sensitivity: calling sd.Shutdown whenever Serve returns → "a normal stop called Shutdown".
func TestServeOnLifecycle_ServesUntilStopped(t *testing.T) {
	addr := freeAddr(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: time.Second}

	sd := &recordingShutdowner{}
	lc := fxtest.NewLifecycle(t)
	observability.ServeOnLifecycle(lc, sd, srv, time.Second, discard(), "test")
	lc.RequireStart()

	if code, err := get(t, "http://"+addr+"/ping"); err != nil || code != http.StatusNoContent {
		t.Fatalf("GET /ping = %d, %v; want 204", code, err)
	}
	lc.RequireStop()
	if _, err := get(t, "http://"+addr+"/ping"); err == nil {
		t.Fatal("server still answering after stop")
	}
	// The Serve goroutine finishes right after Shutdown returns; give it time to misbehave.
	time.Sleep(200 * time.Millisecond)
	if n := sd.calls.Load(); n != 0 {
		t.Fatalf("a normal stop called Shutdown %d times, want 0", n)
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
	observability.ServeOnLifecycle(lc, &recordingShutdowner{}, srv, time.Second, discard(), "test")

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := lc.Start(ctx); err == nil {
		_ = lc.Stop(ctx)
		t.Fatal("Start() error = nil, want address-in-use error")
	}
}

// Covers: FX-03 (U31; M0 pending item 1: a server that stops on its own ends the process)
func TestServeOnLifecycle_ShutsDownWhenServeFails(t *testing.T) {
	listeners := make(chan net.Listener, 1)
	srv := &http.Server{
		Addr: freeAddr(t), Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second,
		// Serve hands its listener to BaseContext, so the test can break it under the server.
		BaseContext: func(ln net.Listener) context.Context {
			listeners <- ln
			return t.Context()
		},
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	app := fxtest.New(t, fx.Invoke(func(lc fx.Lifecycle, sd fx.Shutdowner) {
		observability.ServeOnLifecycle(lc, sd, srv, time.Second, log, "test")
	}))
	app.RequireStart()
	defer app.RequireStop()
	wait := app.Wait()

	var ln net.Listener
	select {
	case ln = <-listeners:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve never started")
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close the listener: %v", err)
	}

	select {
	case sig := <-wait:
		if sig.ExitCode != 1 {
			t.Fatalf("shutdown exit code = %d, want 1", sig.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no shutdown after the server stopped serving; want the process to exit with code 1")
	}
	// The log line is written before Shutdown, which the receive above waited for.
	if !strings.Contains(logs.String(), `"msg":"http server stopped unexpectedly"`) {
		t.Fatalf("logs = %s, want the unexpected stop", logs.String())
	}
}
