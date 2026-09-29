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
	"DB_MAX_CONNS", "SQS_WAGER_QUEUE_NAME", "SQS_WAGER_DLQ_NAME",
}

const validURL = "postgres://pda_app:s3cr3t@localhost:5432/pda?sslmode=disable"

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
		ShutdownTimeout: 20 * time.Second, DatabaseURL: validURL, DBMaxConns: 10,
		WagerQueueName: "wager-transactions.fifo", WagerDLQName: "wager-transactions-dlq.fifo",
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
	t.Setenv("DATABASE_URL", validURL)

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
		"DB_MAX_CONNS": "4", "SQS_WAGER_QUEUE_NAME": "w.fifo", "SQS_WAGER_DLQ_NAME": "d.fifo",
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
		DatabaseURL: "postgresql://u:p@db:5432/x", DBMaxConns: 4, WagerQueueName: "w.fifo", WagerDLQName: "d.fifo",
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

// Covers: FX-02
func TestLoad_RequiresDatabaseURL(t *testing.T) {
	cleanEnv(t)

	_, err := config.Load()
	if got := vars(err); len(got) != 1 || got[0] != "DATABASE_URL" {
		t.Fatalf("Load() error vars = %v, want [DATABASE_URL] (err = %v)", got, err)
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
		{"non-fifo queue", func(c *config.Config) { c.WagerQueueName = "wager" }, "SQS_WAGER_QUEUE_NAME"},
		{"non-fifo dlq", func(c *config.Config) { c.WagerDLQName = "dlq" }, "SQS_WAGER_DLQ_NAME"},
		{"dlq equals queue", func(c *config.Config) { c.WagerDLQName = c.WagerQueueName }, "SQS_WAGER_DLQ_NAME"},
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

// Covers: OBS-02
func TestLoad_ErrorsNeverContainValues(t *testing.T) {
	cases := []struct {
		name, key, value, secret, wantVar string
	}{
		{"password in invalid url", "DATABASE_URL", "mysql://pda_app:SuperSecret42@h/db", "SuperSecret42", "DATABASE_URL"},
		{"unparsable duration", "SHUTDOWN_TIMEOUT", "abc123xyz", "abc123xyz", "SHUTDOWN_TIMEOUT"},
		{"unparsable int", "DB_MAX_CONNS", "ten-conns-99", "ten-conns-99", "DB_MAX_CONNS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("DATABASE_URL", validURL)
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
