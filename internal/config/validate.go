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
	if c.ReferenceRetryMaxDelay < c.ReferenceRetryBaseDelay || c.ReferenceRetryMaxDelay > maxRetryDelay {
		fail("REFERENCE_RETRY_MAX_DELAY", "must be between REFERENCE_RETRY_BASE_DELAY and "+maxRetryDelay.String())
	}
	if c.ReferenceMaxAttempts < 1 {
		fail("REFERENCE_MAX_ATTEMPTS", "must be at least 1")
	}
	if c.ReferenceTTL <= 0 {
		fail("REFERENCE_TTL", "must be greater than 0")
	}
	if c.ReferencePollInterval <= 0 {
		fail("REFERENCE_POLL_INTERVAL", "must be greater than 0")
	}
	if c.ReferenceBatchSize < 1 {
		fail("REFERENCE_BATCH_SIZE", "must be at least 1")
	}
	c.validateOutbox(fail)
	c.validateConsumer(fail)
	return errors.Join(errs...)
}

// validateOutbox checks the publisher settings (messaging.md §5.1).
func (c Config) validateOutbox(fail func(v, reason string)) {
	if !strings.HasSuffix(c.SNSEventsTopicName, ".fifo") {
		fail("SNS_EVENTS_TOPIC_NAME", "must end with .fifo")
	}
	if c.OutboxBatchSize < 1 || c.OutboxBatchSize > maxOutboxBatchSize {
		fail("OUTBOX_BATCH_SIZE", "must be between 1 and 1000")
	}
	if c.OutboxLease <= 0 {
		fail("OUTBOX_LEASE", "must be greater than 0")
	}
	if c.OutboxPollInterval <= 0 {
		fail("OUTBOX_POLL_INTERVAL", "must be greater than 0")
	}
	if c.OutboxConcurrency < 1 {
		fail("OUTBOX_CONCURRENCY", "must be at least 1")
	}
	if c.OutboxRetryBaseDelay <= 0 {
		fail("OUTBOX_RETRY_BASE_DELAY", "must be greater than 0")
	}
	if c.OutboxRetryMaxDelay < c.OutboxRetryBaseDelay || c.OutboxRetryMaxDelay > maxRetryDelay {
		fail("OUTBOX_RETRY_MAX_DELAY", "must be between OUTBOX_RETRY_BASE_DELAY and "+maxRetryDelay.String())
	}
}

// validateConsumer checks the SQS consumer settings (messaging.md §4.1). SQS
// takes the wait and the visibility in whole seconds.
func (c Config) validateConsumer(fail func(v, reason string)) {
	if c.SQSConsumerPollers < 1 {
		fail("SQS_CONSUMER_POLLERS", "must be at least 1")
	}
	if c.SQSReceiveBatch < 1 || c.SQSReceiveBatch > maxReceiveBatch {
		fail("SQS_RECEIVE_BATCH", "must be between 1 and 10")
	}
	if c.SQSWaitTime < 0 || c.SQSWaitTime > maxWaitTime || c.SQSWaitTime%time.Second != 0 {
		fail("SQS_WAIT_TIME", "must be whole seconds between 0s and 20s")
	}
	visibilityOK := c.SQSVisibilityTimeout >= time.Second && c.SQSVisibilityTimeout <= maxVisibility &&
		c.SQSVisibilityTimeout%time.Second == 0
	if !visibilityOK {
		fail("SQS_VISIBILITY_TIMEOUT", "must be whole seconds between 1s and 12h")
	}
	// Compared only with a valid visibility, so one mistake is reported once.
	if c.SQSProcessingTimeout <= 0 || (visibilityOK && c.SQSProcessingTimeout >= c.SQSVisibilityTimeout) {
		fail("SQS_PROCESSING_TIMEOUT", "must be greater than 0 and less than SQS_VISIBILITY_TIMEOUT")
	}
	if c.SQSMaxInFlight < 1 {
		fail("SQS_MAX_IN_FLIGHT", "must be at least 1")
	}
	if c.SQSRetryMaxDelay < time.Second || c.SQSRetryMaxDelay > maxVisibility {
		fail("SQS_RETRY_MAX_DELAY", "must be between 1s and 12h")
	}
}

const (
	// maxReceiveBatch, maxWaitTime and maxVisibility are the limits of
	// ReceiveMessage and ChangeMessageVisibility.
	maxReceiveBatch = 10
	maxWaitTime     = 20 * time.Second
	maxVisibility   = 12 * time.Hour
	// maxRetryDelay bounds the retry delays; it is the upper bound
	// wagering.NewReferenceRetryPolicy accepts.
	maxRetryDelay = 24 * time.Hour
	// maxOutboxBatchSize bounds the rows one claim leases.
	maxOutboxBatchSize = 1000
)

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
