package testkit_test

import (
	"testing"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: spec M8, decision 6 (U28)
//
// The environment EnvOf writes, parsed by the library the binary uses, gives
// the struct back: every field, in the format the process reads.
func TestEnvOf(t *testing.T) {
	cfg, err := env.ParseAsWithOptions[config.Config](env.Options{Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	cfg.LogLevel, cfg.HTTPAddr = "warn", "127.0.0.1:1"
	cfg.DatabaseURL = "postgres://u:p@h:5432/db?sslmode=disable"
	cfg.DBMaxConns, cfg.ReferenceBatchSize = 7, 13
	cfg.ShutdownTimeout, cfg.OutboxLease, cfg.SQSWaitTime = 1500*time.Millisecond, 3*time.Second, 0
	cfg.APIDocsEnabled = false

	back, err := env.ParseAsWithOptions[config.Config](env.Options{Environment: testkit.EnvOf(cfg)})
	if err != nil || back != cfg {
		t.Fatalf("Config back from EnvOf = %+v, %v; want %+v", back, err, cfg)
	}
	roles := config.Roles{HTTP: true, OutboxPublisher: true}
	backRoles, err := env.ParseAsWithOptions[config.Roles](env.Options{Environment: testkit.EnvOf(roles)})
	if err != nil || backRoles != roles {
		t.Fatalf("Roles back from EnvOf = %+v, %v; want %+v", backRoles, err, roles)
	}
}
