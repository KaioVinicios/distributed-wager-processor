package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
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
	if c.DBLockTimeout <= 0 {
		fail("DB_LOCK_TIMEOUT", "must be greater than 0")
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
	if reason := httpURLProblem(c.OIDCIssuer); reason != "" {
		fail("OIDC_ISSUER", reason)
	}
	if reason := httpURLProblem(c.OIDCJWKSURL); reason != "" {
		fail("OIDC_JWKS_URL", reason)
	}
	if c.OIDCAudience == "" {
		fail("OIDC_AUDIENCE", "must not be empty")
	}
	if c.OIDCClockSkew < 0 {
		fail("OIDC_CLOCK_SKEW", "must not be negative")
	}
	if c.ReferenceRetryBaseDelay <= 0 {
		fail("REFERENCE_RETRY_BASE_DELAY", "must be greater than 0")
	}
	if c.ReferenceRetryMaxDelay < c.ReferenceRetryBaseDelay || c.ReferenceRetryMaxDelay > maxReferenceRetryDelay {
		fail("REFERENCE_RETRY_MAX_DELAY", "must be between REFERENCE_RETRY_BASE_DELAY and "+maxReferenceRetryDelay.String())
	}
	if c.ReferenceMaxAttempts < 1 {
		fail("REFERENCE_MAX_ATTEMPTS", "must be at least 1")
	}
	if c.ReferenceTTL <= 0 {
		fail("REFERENCE_TTL", "must be greater than 0")
	}
	return errors.Join(errs...)
}

// maxReferenceRetryDelay is the upper bound wagering.NewReferenceRetryPolicy accepts.
const maxReferenceRetryDelay = 24 * time.Hour

// httpURLProblem returns why raw is not an absolute http(s) URL, or "" if it is.
// Like databaseURLProblem, it never echoes the value.
func httpURLProblem(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "must be an absolute http or https URL"
	}
	return ""
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
