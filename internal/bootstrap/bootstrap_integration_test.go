//go:build integration

package bootstrap_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

func integrationConfig(t *testing.T) config.Config {
	t.Helper()
	testkit.UseRootAWS(t)
	wager, dlq := testkit.CreateQueues(t, sqs.NewFromConfig(testkit.RootAWSConfig(t)))
	cfg := config.Config{
		LogLevel: "error", HTTPAddr: testkit.FreeAddr(t), MetricsAddr: testkit.FreeAddr(t),
		ShutdownTimeout: 5 * time.Second, DatabaseURL: testkit.AppDatabaseURL(t), DBMaxConns: 2,
		WagerQueueName: wager, WagerDLQName: dlq,
	}
	t.Setenv("DATABASE_URL", cfg.DatabaseURL) // config.Load stays valid even if Fx calls it
	return cfg
}

func getJSON(t *testing.T, client *http.Client, url string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, body
}

// Covers: TST-I07, FX-03, FX-04, FX-05, HTTP-08 (I07b — M0 modules)
// Sensitivity: removed pool.Close() from the postgres OnStop → "pool still usable after Stop" + goleak failure.
// Sensitivity: before awsclient closed idle SDK connections on stop, goleak reported the app's persistConn goroutines.
func TestFxLifecycle(t *testing.T) {
	cfg := integrationConfig(t)
	// Snapshot after setup: the testkit's own SQS client connection is not the app's.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var pool *pgxpool.Pool
	app := fxtest.New(t, append(bootstrap.Options(), fx.Replace(cfg), fx.Populate(&pool))...)
	app.RequireStart()

	client := &http.Client{Timeout: 5 * time.Second}
	code, body := getJSON(t, client, "http://"+cfg.HTTPAddr+"/health/ready")
	checks, _ := body["checks"].(map[string]any)
	if code != http.StatusOK || checks["postgres"] != "UP" || checks["sqs"] != "UP" {
		t.Fatalf("GET /health/ready = %d %v, want 200 with postgres and sqs UP", code, body)
	}
	if code, _ := getJSON(t, client, "http://"+cfg.MetricsAddr+"/metrics"); code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", code)
	}
	client.CloseIdleConnections()

	app.RequireStop()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := pool.Ping(ctx); err == nil {
		t.Fatal("pool still usable after Stop; want it closed")
	}
}

// Covers: FX-02 (I07c)
func TestFxFailFast(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantErr string
	}{
		{"unreachable database", func(c *config.Config) {
			c.DatabaseURL = "postgres://pda_app:x@127.0.0.1:1/pda?sslmode=disable&connect_timeout=2"
		}, "postgres"},
		{"missing queue", func(c *config.Config) { c.WagerQueueName = "missing-" + c.WagerQueueName }, "missing-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := integrationConfig(t)
			tc.mutate(&cfg)
			app := fx.New(append(bootstrap.Options(), fx.Replace(cfg), fx.NopLogger)...)

			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			err := app.Start(ctx)
			if err == nil {
				_ = app.Stop(ctx)
				t.Fatal("Start() error = nil, want fail-fast error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Start() error = %q, want it to mention %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("invalid configuration", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		app := fx.New(append(bootstrap.Options(), fx.NopLogger)...)
		if err := app.Err(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Fatalf("fx.New().Err() = %v, want an error naming DATABASE_URL", err)
		}
	})
}
