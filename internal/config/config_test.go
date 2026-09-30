package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/config"
)

var allVars = []string{
	"LOG_LEVEL", "HTTP_ADDR", "METRICS_ADDR", "SHUTDOWN_TIMEOUT", "DATABASE_URL",
	"DB_MAX_CONNS", "DB_LOCK_TIMEOUT", "SQS_WAGER_QUEUE_NAME", "SQS_WAGER_DLQ_NAME",
	"OIDC_ISSUER", "OIDC_JWKS_URL", "OIDC_AUDIENCE", "OIDC_CLOCK_SKEW", "API_DOCS_ENABLED",
	"REFERENCE_RETRY_BASE_DELAY", "REFERENCE_RETRY_MAX_DELAY", "REFERENCE_MAX_ATTEMPTS", "REFERENCE_TTL",
	"REFERENCE_POLL_INTERVAL", "REFERENCE_BATCH_SIZE",
	"SNS_EVENTS_TOPIC_NAME", "OUTBOX_BATCH_SIZE", "OUTBOX_LEASE", "OUTBOX_POLL_INTERVAL", "OUTBOX_CONCURRENCY",
	"OUTBOX_RETRY_BASE_DELAY", "OUTBOX_RETRY_MAX_DELAY",
	"SQS_CONSUMER_POLLERS", "SQS_RECEIVE_BATCH", "SQS_WAIT_TIME", "SQS_VISIBILITY_TIMEOUT",
	"SQS_PROCESSING_TIMEOUT", "SQS_MAX_IN_FLIGHT", "SQS_RETRY_MAX_DELAY",
}

const (
	validURL    = "postgres://pda_app:s3cr3t@localhost:5432/pda?sslmode=disable"
	validIssuer = "http://localhost:8080/realms/pda"
	validJWKS   = "http://keycloak:8080/realms/pda/protocol/openid-connect/certs"
)

// setRequired sets the variables without a default.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", validURL)
	t.Setenv("OIDC_ISSUER", validIssuer)
	t.Setenv("OIDC_JWKS_URL", validJWKS)
}

// cleanEnv unsets every config variable for the test, restoring them afterwards.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range allVars {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

func validConfig() config.Config {
	return config.Config{
		LogLevel: "info", HTTPAddr: ":8080", MetricsAddr: ":9090",
		ShutdownTimeout: 20 * time.Second, DatabaseURL: validURL, DBMaxConns: 10, DBLockTimeout: 5 * time.Second,
		WagerQueueName: "wager-transactions.fifo", WagerDLQName: "wager-transactions-dlq.fifo",
		OIDCIssuer: validIssuer, OIDCJWKSURL: validJWKS, OIDCAudience: "pda-api", OIDCClockSkew: 30 * time.Second,
		APIDocsEnabled:          true,
		ReferenceRetryBaseDelay: time.Second, ReferenceRetryMaxDelay: time.Minute, ReferenceMaxAttempts: 8,
		ReferenceTTL:          10 * time.Minute,
		ReferencePollInterval: 500 * time.Millisecond, ReferenceBatchSize: 50,
		SNSEventsTopicName: "wallet-events.fifo",
		OutboxBatchSize:    50, OutboxLease: 30 * time.Second, OutboxPollInterval: 500 * time.Millisecond,
		OutboxConcurrency: 8, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute,
		SQSConsumerPollers: 2, SQSReceiveBatch: 10, SQSWaitTime: 20 * time.Second,
		SQSVisibilityTimeout: 30 * time.Second, SQSProcessingTimeout: 10 * time.Second,
		SQSMaxInFlight: 16, SQSRetryMaxDelay: 300 * time.Second,
	}
}

// fieldErrors flattens a joined error into its *FieldError parts.
func fieldErrors(err error) []*config.FieldError {
	var out []*config.FieldError
	var walk func(error)
	walk = func(e error) {
		var multi interface{ Unwrap() []error }
		if errors.As(e, &multi) {
			for _, inner := range multi.Unwrap() {
				walk(inner)
			}
			return
		}
		var fe *config.FieldError
		if errors.As(e, &fe) {
			out = append(out, fe)
		}
	}
	walk(err)
	return out
}

func vars(err error) []string {
	var names []string
	for _, fe := range fieldErrors(err) {
		names = append(names, fe.Var)
	}
	return names
}

// Covers: FX-02
func TestLoad_AppliesDefaults(t *testing.T) {
	cleanEnv(t)
	setRequired(t)

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := validConfig(); got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	if got.HTTPAddr != config.DefaultHTTPAddr {
		t.Fatalf("HTTPAddr default %q != DefaultHTTPAddr %q", got.HTTPAddr, config.DefaultHTTPAddr)
	}
}

// Covers: FX-02
func TestLoad_ReadsEnvironment(t *testing.T) {
	cleanEnv(t)
	env := map[string]string{
		"LOG_LEVEL": "debug", "HTTP_ADDR": ":18080", "METRICS_ADDR": ":19090",
		"SHUTDOWN_TIMEOUT": "5s", "DATABASE_URL": "postgresql://u:p@db:5432/x",
		"DB_MAX_CONNS": "4", "DB_LOCK_TIMEOUT": "2s", "SQS_WAGER_QUEUE_NAME": "w.fifo", "SQS_WAGER_DLQ_NAME": "d.fifo",
		"OIDC_ISSUER": "https://idp.example/realms/x", "OIDC_JWKS_URL": "https://idp.internal/certs",
		"OIDC_AUDIENCE": "api", "OIDC_CLOCK_SKEW": "1s", "API_DOCS_ENABLED": "false",
		"REFERENCE_RETRY_BASE_DELAY": "100ms", "REFERENCE_RETRY_MAX_DELAY": "1s", "REFERENCE_MAX_ATTEMPTS": "3",
		"REFERENCE_TTL": "3s", "REFERENCE_POLL_INTERVAL": "50ms", "REFERENCE_BATCH_SIZE": "7", "SNS_EVENTS_TOPIC_NAME": "events.fifo", "OUTBOX_BATCH_SIZE": "10",
		"OUTBOX_LEASE": "2s", "OUTBOX_POLL_INTERVAL": "100ms", "OUTBOX_CONCURRENCY": "2",
		"OUTBOX_RETRY_BASE_DELAY": "100ms", "OUTBOX_RETRY_MAX_DELAY": "1s",
		"SQS_CONSUMER_POLLERS": "1", "SQS_RECEIVE_BATCH": "5", "SQS_WAIT_TIME": "1s",
		"SQS_VISIBILITY_TIMEOUT": "5s", "SQS_PROCESSING_TIMEOUT": "3s", "SQS_MAX_IN_FLIGHT": "4",
		"SQS_RETRY_MAX_DELAY": "1s",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Config{
		LogLevel: "debug", HTTPAddr: ":18080", MetricsAddr: ":19090", ShutdownTimeout: 5 * time.Second,
		DatabaseURL: "postgresql://u:p@db:5432/x", DBMaxConns: 4, DBLockTimeout: 2 * time.Second,
		WagerQueueName: "w.fifo", WagerDLQName: "d.fifo",
		OIDCIssuer: "https://idp.example/realms/x", OIDCJWKSURL: "https://idp.internal/certs",
		OIDCAudience: "api", OIDCClockSkew: time.Second, APIDocsEnabled: false,
		ReferenceRetryBaseDelay: 100 * time.Millisecond, ReferenceRetryMaxDelay: time.Second,
		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
		ReferencePollInterval: 50 * time.Millisecond, ReferenceBatchSize: 7,
		SNSEventsTopicName: "events.fifo", OutboxBatchSize: 10, OutboxLease: 2 * time.Second,
		OutboxPollInterval: 100 * time.Millisecond, OutboxConcurrency: 2,
		OutboxRetryBaseDelay: 100 * time.Millisecond, OutboxRetryMaxDelay: time.Second,
		SQSConsumerPollers: 1, SQSReceiveBatch: 5, SQSWaitTime: time.Second,
		SQSVisibilityTimeout: 5 * time.Second, SQSProcessingTimeout: 3 * time.Second,
		SQSMaxInFlight: 4, SQSRetryMaxDelay: time.Second,
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

// Covers: FX-02
func TestLoad_RequiresURLs(t *testing.T) {
	cleanEnv(t)

	_, err := config.Load()
	if got := strings.Join(vars(err), ","); got != "DATABASE_URL,OIDC_ISSUER,OIDC_JWKS_URL" {
		t.Fatalf("Load() error vars = %s, want DATABASE_URL,OIDC_ISSUER,OIDC_JWKS_URL (err = %v)", got, err)
	}
}

// Covers: FX-02
func TestValidate_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantVar string
	}{
		{"unknown log level", func(c *config.Config) { c.LogLevel = "verbose" }, "LOG_LEVEL"},
		{"empty http addr", func(c *config.Config) { c.HTTPAddr = "" }, "HTTP_ADDR"},
		{"empty metrics addr", func(c *config.Config) { c.MetricsAddr = "" }, "METRICS_ADDR"},
		{"http equals metrics", func(c *config.Config) { c.MetricsAddr = c.HTTPAddr }, "HTTP_ADDR"},
		{"zero shutdown", func(c *config.Config) { c.ShutdownTimeout = 0 }, "SHUTDOWN_TIMEOUT"},
		{"shutdown at stop timeout", func(c *config.Config) { c.ShutdownTimeout = config.MaxShutdownTimeout }, "SHUTDOWN_TIMEOUT"},
		{"wrong scheme", func(c *config.Config) { c.DatabaseURL = "mysql://u:p@h/db" }, "DATABASE_URL"},
		{"missing host", func(c *config.Config) { c.DatabaseURL = "postgres:///pda" }, "DATABASE_URL"},
		{"unparsable url", func(c *config.Config) { c.DatabaseURL = "postgres://u:p@h:bad port/db" }, "DATABASE_URL"},
		{"zero max conns", func(c *config.Config) { c.DBMaxConns = 0 }, "DB_MAX_CONNS"},
		{"zero lock timeout", func(c *config.Config) { c.DBLockTimeout = 0 }, "DB_LOCK_TIMEOUT"},
		{"negative lock timeout", func(c *config.Config) { c.DBLockTimeout = -time.Second }, "DB_LOCK_TIMEOUT"},
		{"non-fifo queue", func(c *config.Config) { c.WagerQueueName = "wager" }, "SQS_WAGER_QUEUE_NAME"},
		{"non-fifo dlq", func(c *config.Config) { c.WagerDLQName = "dlq" }, "SQS_WAGER_DLQ_NAME"},
		{"dlq equals queue", func(c *config.Config) { c.WagerDLQName = c.WagerQueueName }, "SQS_WAGER_DLQ_NAME"},
		{"missing issuer", func(c *config.Config) { c.OIDCIssuer = "" }, "OIDC_ISSUER"},
		{"relative issuer", func(c *config.Config) { c.OIDCIssuer = "/realms/pda" }, "OIDC_ISSUER"},
		{"issuer not http", func(c *config.Config) { c.OIDCIssuer = "ftp://idp/realms/pda" }, "OIDC_ISSUER"},
		{"missing jwks url", func(c *config.Config) { c.OIDCJWKSURL = "" }, "OIDC_JWKS_URL"},
		{"unparsable jwks url", func(c *config.Config) { c.OIDCJWKSURL = "http://idp:bad port/certs" }, "OIDC_JWKS_URL"},
		{"empty audience", func(c *config.Config) { c.OIDCAudience = "" }, "OIDC_AUDIENCE"},
		{"negative clock skew", func(c *config.Config) { c.OIDCClockSkew = -time.Second }, "OIDC_CLOCK_SKEW"},
		{"zero base delay", func(c *config.Config) { c.ReferenceRetryBaseDelay = 0 }, "REFERENCE_RETRY_BASE_DELAY"},
		{"max below base", func(c *config.Config) { c.ReferenceRetryMaxDelay = c.ReferenceRetryBaseDelay / 2 }, "REFERENCE_RETRY_MAX_DELAY"},
		{"max above a day", func(c *config.Config) { c.ReferenceRetryMaxDelay = 25 * time.Hour }, "REFERENCE_RETRY_MAX_DELAY"},
		{"zero attempts", func(c *config.Config) { c.ReferenceMaxAttempts = 0 }, "REFERENCE_MAX_ATTEMPTS"},
		{"zero ttl", func(c *config.Config) { c.ReferenceTTL = 0 }, "REFERENCE_TTL"},
		{"zero reference poll interval", func(c *config.Config) { c.ReferencePollInterval = 0 }, "REFERENCE_POLL_INTERVAL"},
		{"zero reference batch", func(c *config.Config) { c.ReferenceBatchSize = 0 }, "REFERENCE_BATCH_SIZE"},
		{"non-fifo topic", func(c *config.Config) { c.SNSEventsTopicName = "wallet-events" }, "SNS_EVENTS_TOPIC_NAME"},
		{"zero batch", func(c *config.Config) { c.OutboxBatchSize = 0 }, "OUTBOX_BATCH_SIZE"},
		{"batch above 1000", func(c *config.Config) { c.OutboxBatchSize = 1001 }, "OUTBOX_BATCH_SIZE"},
		{"zero lease", func(c *config.Config) { c.OutboxLease = 0 }, "OUTBOX_LEASE"},
		{"zero poll interval", func(c *config.Config) { c.OutboxPollInterval = 0 }, "OUTBOX_POLL_INTERVAL"},
		{"zero concurrency", func(c *config.Config) { c.OutboxConcurrency = 0 }, "OUTBOX_CONCURRENCY"},
		{"zero outbox base delay", func(c *config.Config) { c.OutboxRetryBaseDelay = 0 }, "OUTBOX_RETRY_BASE_DELAY"},
		{"outbox max below base", func(c *config.Config) { c.OutboxRetryMaxDelay = c.OutboxRetryBaseDelay / 2 }, "OUTBOX_RETRY_MAX_DELAY"},
		{"outbox max above a day", func(c *config.Config) { c.OutboxRetryMaxDelay = 25 * time.Hour }, "OUTBOX_RETRY_MAX_DELAY"},
		{"zero pollers", func(c *config.Config) { c.SQSConsumerPollers = 0 }, "SQS_CONSUMER_POLLERS"},
		{"zero receive batch", func(c *config.Config) { c.SQSReceiveBatch = 0 }, "SQS_RECEIVE_BATCH"},
		{"receive batch above 10", func(c *config.Config) { c.SQSReceiveBatch = 11 }, "SQS_RECEIVE_BATCH"},
		{"negative wait time", func(c *config.Config) { c.SQSWaitTime = -time.Second }, "SQS_WAIT_TIME"},
		{"wait time above 20s", func(c *config.Config) { c.SQSWaitTime = 21 * time.Second }, "SQS_WAIT_TIME"},
		{"wait time not in seconds", func(c *config.Config) { c.SQSWaitTime = 1500 * time.Millisecond }, "SQS_WAIT_TIME"},
		{"visibility below 1s", func(c *config.Config) { c.SQSVisibilityTimeout = 500 * time.Millisecond }, "SQS_VISIBILITY_TIMEOUT"},
		{"visibility above 12h", func(c *config.Config) { c.SQSVisibilityTimeout = 13 * time.Hour }, "SQS_VISIBILITY_TIMEOUT"},
		{"visibility not in seconds", func(c *config.Config) { c.SQSVisibilityTimeout = 30500 * time.Millisecond }, "SQS_VISIBILITY_TIMEOUT"},
		{"zero processing timeout", func(c *config.Config) { c.SQSProcessingTimeout = 0 }, "SQS_PROCESSING_TIMEOUT"},
		{"processing at visibility", func(c *config.Config) { c.SQSProcessingTimeout = c.SQSVisibilityTimeout }, "SQS_PROCESSING_TIMEOUT"},
		{"zero max in flight", func(c *config.Config) { c.SQSMaxInFlight = 0 }, "SQS_MAX_IN_FLIGHT"},
		{"retry max below 1s", func(c *config.Config) { c.SQSRetryMaxDelay = 500 * time.Millisecond }, "SQS_RETRY_MAX_DELAY"},
		{"retry max above 12h", func(c *config.Config) { c.SQSRetryMaxDelay = 13 * time.Hour }, "SQS_RETRY_MAX_DELAY"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			got := vars(cfg.Validate())
			if len(got) != 1 || got[0] != tc.wantVar {
				t.Fatalf("Validate() vars = %v, want [%s]", got, tc.wantVar)
			}
		})
	}
}

// Covers: FX-02
func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Covers: FX-02
func TestValidate_ReportsAllErrorsAtOnce(t *testing.T) {
	cfg := validConfig()
	cfg.LogLevel = "verbose"
	cfg.DBMaxConns = 0
	cfg.WagerQueueName = "wager"

	got := strings.Join(vars(cfg.Validate()), ",")
	if got != "LOG_LEVEL,DB_MAX_CONNS,SQS_WAGER_QUEUE_NAME" {
		t.Fatalf("Validate() vars = %s, want LOG_LEVEL,DB_MAX_CONNS,SQS_WAGER_QUEUE_NAME", got)
	}
}

// Covers: FX-02
func TestValidate_AcceptsZeroClockSkew(t *testing.T) {
	cfg := validConfig()
	cfg.OIDCClockSkew = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Covers: OBS-02
func TestLoad_ErrorsNeverContainValues(t *testing.T) {
	cases := []struct {
		name, key, value, secret, wantVar string
	}{
		{"password in invalid url", "DATABASE_URL", "mysql://pda_app:SuperSecret42@h/db", "SuperSecret42", "DATABASE_URL"},
		{"unparsable duration", "SHUTDOWN_TIMEOUT", "abc123xyz", "abc123xyz", "SHUTDOWN_TIMEOUT"},
		{"unparsable int", "DB_MAX_CONNS", "ten-conns-99", "ten-conns-99", "DB_MAX_CONNS"},
		{"unparsable lock timeout", "DB_LOCK_TIMEOUT", "forever-77", "forever-77", "DB_LOCK_TIMEOUT"},
		{"credentials in issuer", "OIDC_ISSUER", "ftp://admin:IssuerSecret7@idp/realms/pda", "IssuerSecret7", "OIDC_ISSUER"},
		{"unparsable flag", "API_DOCS_ENABLED", "maybe-42", "maybe-42", "API_DOCS_ENABLED"},
		{"unparsable lease", "OUTBOX_LEASE", "long-lease-5", "long-lease-5", "OUTBOX_LEASE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			setRequired(t)
			t.Setenv(tc.key, tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("error leaks the value: %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("error %q does not name %s", err.Error(), tc.wantVar)
			}
		})
	}
}
