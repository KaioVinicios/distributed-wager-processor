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
	WagerQueueName  string        `env:"SQS_WAGER_QUEUE_NAME" envDefault:"wager-transactions.fifo"`
	WagerDLQName    string        `env:"SQS_WAGER_DLQ_NAME" envDefault:"wager-transactions-dlq.fifo"`
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
