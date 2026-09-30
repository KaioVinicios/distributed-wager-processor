package config

import "github.com/caarlos0/env/v11"

// Roles are the parts of the process an instance runs (D-15).
type Roles struct {
	HTTP            bool `env:"HTTP_ENABLED" envDefault:"true"`
	Consumer        bool `env:"CONSUMER_ENABLED" envDefault:"true"`
	OutboxPublisher bool `env:"OUTBOX_PUBLISHER_ENABLED" envDefault:"true"`
	ReferenceWorker bool `env:"REFERENCE_WORKER_ENABLED" envDefault:"true"`
}

// RolesFromEnv reads only the four role variables. It runs before the Fx graph
// exists, because a disabled role removes its module from the graph. Errors
// name variables, never values.
func RolesFromEnv() (Roles, error) {
	roles, err := env.ParseAs[Roles]()
	if err != nil {
		return Roles{}, redactParseError(err)
	}
	return roles, nil
}
