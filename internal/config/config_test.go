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
		ReferenceTTL: 10 * time.Minute,
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
		"REFERENCE_TTL": "3s",
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
