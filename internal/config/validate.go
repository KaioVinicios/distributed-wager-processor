package config

import (
	"errors"
	"net/url"
	"strings"
)

// FieldError reports an invalid variable by name. It never carries the value.
type FieldError struct {
	Var    string
	Reason string
}

func (e *FieldError) Error() string { return "config: " + e.Var + ": " + e.Reason }

// Validate checks every rule and reports all violations at once.
func (c Config) Validate() error {
	var errs []error
	fail := func(v, reason string) { errs = append(errs, &FieldError{Var: v, Reason: reason}) }

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		fail("LOG_LEVEL", "must be one of debug, info, warn, error")
	}
	if c.HTTPAddr == "" {
		fail("HTTP_ADDR", "must not be empty")
	}
	if c.MetricsAddr == "" {
		fail("METRICS_ADDR", "must not be empty")
	}
	if c.HTTPAddr != "" && c.HTTPAddr == c.MetricsAddr {
		fail("HTTP_ADDR", "must differ from METRICS_ADDR")
	}
	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout >= MaxShutdownTimeout {
		fail("SHUTDOWN_TIMEOUT", "must be greater than 0 and less than "+MaxShutdownTimeout.String())
	}
	if reason := databaseURLProblem(c.DatabaseURL); reason != "" {
		fail("DATABASE_URL", reason)
	}
	if c.DBMaxConns < 1 {
		fail("DB_MAX_CONNS", "must be at least 1")
	}
	if !strings.HasSuffix(c.WagerQueueName, ".fifo") {
		fail("SQS_WAGER_QUEUE_NAME", "must end with .fifo")
	}
	switch {
	case !strings.HasSuffix(c.WagerDLQName, ".fifo"):
		fail("SQS_WAGER_DLQ_NAME", "must end with .fifo")
	case c.WagerDLQName == c.WagerQueueName:
		fail("SQS_WAGER_DLQ_NAME", "must differ from SQS_WAGER_QUEUE_NAME")
	}
	return errors.Join(errs...)
}

// databaseURLProblem returns why raw is unusable, or "" if it is fine.
// The parse error is dropped on purpose: it would echo the URL (and its password).
func databaseURLProblem(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "must be a valid URL"
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "scheme must be postgres or postgresql"
	}
	if u.Host == "" {
		return "must include a host"
	}
	return ""
}
