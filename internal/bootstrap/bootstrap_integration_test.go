//go:build integration

package bootstrap_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

// integrationConfig points the whole graph at a database, queues and topic of
// the test's own: with the outbox publisher in the graph, the shared pda
// database would have its pending events published to the test's topic.
func integrationConfig(t *testing.T) config.Config {
	t.Helper()
	env := testkit.NewTestEnv(t, "bootstrap")
	testkit.UseRootAWS(t)
	root := testkit.RootAWSConfig(t)
	wager, dlq := testkit.CreateQueues(t, sqs.NewFromConfig(root))
	topic := testkit.NewEventsTopic(t, sqs.NewFromConfig(root), sns.NewFromConfig(root))
	cfg := env.Config() // the accelerated times of test-plan §3.3
	cfg.HTTPAddr, cfg.MetricsAddr, cfg.DBMaxConns = testkit.FreeAddr(t), testkit.FreeAddr(t), 2
	cfg.WagerQueueName, cfg.WagerDLQName, cfg.SNSEventsTopicName = wager, dlq, topic.Name
	cfg.OIDCIssuer, cfg.OIDCJWKSURL = testkit.KeycloakIssuer, testkit.KeycloakIssuer+"/protocol/openid-connect/certs"
	cfg.OIDCAudience, cfg.OIDCClockSkew, cfg.APIDocsEnabled = "pda-api", time.Second, true
	cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay = 100*time.Millisecond, time.Second
	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("integration config: %v", err)
	}
	t.Setenv("DATABASE_URL", cfg.DatabaseURL) // config.Load stays valid even if Fx calls it
	return cfg
}

// safeBuffer collects log lines written from many goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs replaces the application logger by a JSON logger on buf.
func captureLogs(buf *safeBuffer) fx.Option {
	return fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(buf, "info") })
}

// lineIndex is the position of the first log line that contains every part, or -1.
func lineIndex(logs string, parts ...string) int {
	i := 0
	for line := range strings.Lines(logs) {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return i
		}
		i++
	}
	return -1
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
// Sensitivity (review of M7): no log in the AWS OnStop → "aws http client closed at -1".
func TestFxLifecycle(t *testing.T) {
	cfg := integrationConfig(t)
	// Snapshot after setup: the testkit's own SQS client connection is not the app's.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var logs safeBuffer
	var pool *pgxpool.Pool
	app := fxtest.New(t, append(bootstrap.Options(), fx.Replace(cfg), captureLogs(&logs), fx.Populate(&pool))...)
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

	// FX-04, FX-05: the HTTP server stops first, then the consumer, the
	// publisher and the worker, and the pool closes last (D-15).
	order := []int{
		lineIndex(logs.String(), `"msg":"http server stopped"`, `"server":"api"`),
		lineIndex(logs.String(), `"msg":"sqs consumer stopped"`),
		lineIndex(logs.String(), `"msg":"outbox publisher stopped"`),
		lineIndex(logs.String(), `"msg":"reference worker stopped"`),
		lineIndex(logs.String(), `"msg":"postgres pool closed"`),
	}
	for i, at := range order {
		if at < 0 || (i > 0 && at <= order[i-1]) {
			t.Fatalf("stop order = %v (api, consumer, publisher, worker, pool); want every line present and increasing\n%s", order, logs.String())
		}
	}
	// FX-05: the AWS clients close after every component that uses them.
	if aws := lineIndex(logs.String(), `"msg":"aws http client closed"`); aws <= order[3] {
		t.Fatalf("aws http client closed at %d, want after the reference worker stopped (%d)\n%s", aws, order[3], logs.String())
	}
}

// Covers: FX-02, AUTH-02, OUT-07 (I07c)
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
		{"missing topic", func(c *config.Config) { c.SNSEventsTopicName = "missing-" + c.SNSEventsTopicName }, "missing-events-"},
		{"unreachable identity provider", func(c *config.Config) { c.OIDCJWKSURL = "http://127.0.0.1:1/certs" }, "JWKS"},
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

	t.Run("unreachable identity provider, consumer only (D-23)", func(t *testing.T) {
		cfg := integrationConfig(t)
		cfg.OIDCJWKSURL = "http://127.0.0.1:1/certs"
		app := fx.New(append(bootstrap.OptionsFor(config.Roles{Consumer: true}), fx.Replace(cfg), fx.NopLogger)...)
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		err := app.Start(ctx)
		if err == nil {
			_ = app.Stop(ctx)
			t.Fatal("Start() error = nil, want the JWKS fail-fast of the consumer")
		}
		if !strings.Contains(err.Error(), "JWKS") {
			t.Fatalf("Start() error = %q, want it to mention JWKS", err.Error())
		}
	})

	t.Run("invalid role variable", func(t *testing.T) {
		t.Setenv("HTTP_ENABLED", "talvez-42")
		app := fx.New(append(bootstrap.Options(), fx.NopLogger)...)
		err := app.Err()
		if err == nil || !strings.Contains(err.Error(), "HTTP_ENABLED") || strings.Contains(err.Error(), "talvez-42") {
			t.Fatalf("fx.New().Err() = %v, want an error naming HTTP_ENABLED without its value", err)
		}
	})

	t.Run("invalid configuration", func(t *testing.T) {
		t.Setenv("DATABASE_URL", "")
		app := fx.New(append(bootstrap.Options(), fx.NopLogger)...)
		if err := app.Err(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
			t.Fatalf("fx.New().Err() = %v, want an error naming DATABASE_URL", err)
		}
	})
}

// Covers: FX-01, HTTP-08, OBS-04 (I27; I13 with the roles)
// Sensitivity: making /health/ready check the SQS only when the consumer is on → the "consumer off" case reports no "sqs" check.
func TestFxRoles(t *testing.T) {
	cases := []struct {
		name string
		off  func(*config.Roles)
	}{
		{"consumer off", func(r *config.Roles) { r.Consumer = false }},
		{"outbox publisher off", func(r *config.Roles) { r.OutboxPublisher = false }},
		{"reference worker off", func(r *config.Roles) { r.ReferenceWorker = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := integrationConfig(t)
			roles := config.Roles{HTTP: true, Consumer: true, OutboxPublisher: true, ReferenceWorker: true}
			tc.off(&roles)
			app := fxtest.New(t, append(bootstrap.OptionsFor(roles), fx.Replace(cfg))...)
			app.RequireStart()
			defer app.RequireStop()

			client := &http.Client{Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			code, body := getJSON(t, client, "http://"+cfg.HTTPAddr+"/health/ready")
			checks, _ := body["checks"].(map[string]any)
			if code != http.StatusOK || checks["postgres"] != "UP" || checks["sqs"] != "UP" {
				t.Fatalf("GET /health/ready = %d %v, want 200 with postgres and sqs UP whatever the roles", code, body)
			}
		})
	}

	t.Run("http off leaves only the admin server", func(t *testing.T) {
		cfg := integrationConfig(t)
		roles := config.Roles{Consumer: true, OutboxPublisher: true, ReferenceWorker: true}
		app := fxtest.New(t, append(bootstrap.OptionsFor(roles), fx.Replace(cfg))...)
		app.RequireStart()
		defer app.RequireStop()

		client := &http.Client{Timeout: 5 * time.Second}
		defer client.CloseIdleConnections()
		if code, _ := getJSON(t, client, "http://"+cfg.MetricsAddr+"/metrics"); code != http.StatusOK {
			t.Fatalf("GET /metrics = %d, want 200", code)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+cfg.HTTPAddr+"/health/live", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			t.Fatal("the API answered with HTTP_ENABLED off; want the connection refused")
		}
	})
}
