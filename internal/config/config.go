// Package config loads and validates the process configuration from the environment.
package config

import (
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// DefaultHTTPAddr is the default API listen address, shared with the healthcheck probe.
const DefaultHTTPAddr = ":8080"

// MaxShutdownTimeout is the Fx stop timeout; SHUTDOWN_TIMEOUT must stay below it.
const MaxShutdownTimeout = 30 * time.Second

// Config is the validated process configuration.
type Config struct {
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	MetricsAddr     string        `env:"METRICS_ADDR" envDefault:":9090"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`
	DatabaseURL     string        `env:"DATABASE_URL"`
	DBMaxConns      int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	DBLockTimeout   time.Duration `env:"DB_LOCK_TIMEOUT" envDefault:"5s"`
	WagerQueueName  string        `env:"SQS_WAGER_QUEUE_NAME" envDefault:"wager-transactions.fifo"`
	WagerDLQName    string        `env:"SQS_WAGER_DLQ_NAME" envDefault:"wager-transactions-dlq.fifo"`

	// OIDC (D-07): the expected iss and where the keys are fetched are
	// separate, because the issuer seen by clients (localhost) differs from
	// the address reachable inside the compose network (keycloak).
	OIDCIssuer    string        `env:"OIDC_ISSUER"`
	OIDCJWKSURL   string        `env:"OIDC_JWKS_URL"`
	OIDCAudience  string        `env:"OIDC_AUDIENCE" envDefault:"pda-api"`
	OIDCClockSkew time.Duration `env:"OIDC_CLOCK_SKEW" envDefault:"30s"`

	APIDocsEnabled bool `env:"API_DOCS_ENABLED" envDefault:"true"`

	// Schedule of pending references (D-11).
	ReferenceRetryBaseDelay time.Duration `env:"REFERENCE_RETRY_BASE_DELAY" envDefault:"1s"`
	ReferenceRetryMaxDelay  time.Duration `env:"REFERENCE_RETRY_MAX_DELAY" envDefault:"60s"`
	ReferenceMaxAttempts    int           `env:"REFERENCE_MAX_ATTEMPTS" envDefault:"8"`
	ReferenceTTL            time.Duration `env:"REFERENCE_TTL" envDefault:"10m"`

	// Outbox publisher (D-13, messaging.md §5.1).
	SNSEventsTopicName   string        `env:"SNS_EVENTS_TOPIC_NAME" envDefault:"wallet-events.fifo"`
	OutboxBatchSize      int           `env:"OUTBOX_BATCH_SIZE" envDefault:"50"`
	OutboxLease          time.Duration `env:"OUTBOX_LEASE" envDefault:"30s"`
	OutboxPollInterval   time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"500ms"`
	OutboxConcurrency    int           `env:"OUTBOX_CONCURRENCY" envDefault:"8"`
	OutboxRetryBaseDelay time.Duration `env:"OUTBOX_RETRY_BASE_DELAY" envDefault:"1s"`
	OutboxRetryMaxDelay  time.Duration `env:"OUTBOX_RETRY_MAX_DELAY" envDefault:"5m"`

	// SQS consumer (D-12, messaging.md §4.1).
	SQSConsumerPollers   int           `env:"SQS_CONSUMER_POLLERS" envDefault:"2"`
	SQSReceiveBatch      int           `env:"SQS_RECEIVE_BATCH" envDefault:"10"`
	SQSWaitTime          time.Duration `env:"SQS_WAIT_TIME" envDefault:"20s"`
	SQSVisibilityTimeout time.Duration `env:"SQS_VISIBILITY_TIMEOUT" envDefault:"30s"`
	SQSProcessingTimeout time.Duration `env:"SQS_PROCESSING_TIMEOUT" envDefault:"10s"`
	SQSMaxInFlight       int           `env:"SQS_MAX_IN_FLIGHT" envDefault:"16"`
	SQSRetryMaxDelay     time.Duration `env:"SQS_RETRY_MAX_DELAY" envDefault:"300s"`
}

// Load reads the environment and validates it. Errors name variables, never values.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, redactParseError(err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// redactParseError rewrites caarlos0/env errors, whose messages include the raw value.
func redactParseError(err error) error {
	var out []error
	for _, e := range flatten(err) {
		var pe env.ParseError
		if errors.As(e, &pe) {
			out = append(out, &FieldError{Var: envVarOf(pe.Name), Reason: "invalid value"})
		}
	}
	if len(out) == 0 {
		return errors.New("config: failed to read the environment")
	}
	return errors.Join(out...)
}

func flatten(err error) []error {
	var agg env.AggregateError
	if errors.As(err, &agg) {
		return agg.Errors
	}
	return []error{err}
}

// envVarOf maps a Config field name to its environment variable.
func envVarOf(field string) string {
	f, ok := reflect.TypeFor[Config]().FieldByName(field)
	if !ok {
		return field
	}
	name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
	return name
}
