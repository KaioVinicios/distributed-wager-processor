package config_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/config"
)

var roleVars = []string{"HTTP_ENABLED", "CONSUMER_ENABLED", "OUTBOX_PUBLISHER_ENABLED", "REFERENCE_WORKER_ENABLED"}

func setRoles(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range roleVars {
		t.Setenv(name, values[name])
	}
}

// Covers: FX-01, D-15 (U21)
func TestRolesFromEnv(t *testing.T) {
	t.Run("every role is on by default", func(t *testing.T) {
		setRoles(t, nil)
		got, err := config.RolesFromEnv()
		if err != nil || got != (config.Roles{HTTP: true, Consumer: true, OutboxPublisher: true, ReferenceWorker: true}) {
			t.Fatalf("RolesFromEnv() = %+v, %v; want all true", got, err)
		}
	})

	t.Run("each variable turns off only its role", func(t *testing.T) {
		cases := map[string]func(config.Roles) bool{
			"HTTP_ENABLED":             func(r config.Roles) bool { return r.HTTP },
			"CONSUMER_ENABLED":         func(r config.Roles) bool { return r.Consumer },
			"OUTBOX_PUBLISHER_ENABLED": func(r config.Roles) bool { return r.OutboxPublisher },
			"REFERENCE_WORKER_ENABLED": func(r config.Roles) bool { return r.ReferenceWorker },
		}
		for name, on := range cases {
			setRoles(t, map[string]string{name: "false"})
			got, err := config.RolesFromEnv()
			if err != nil || on(got) {
				t.Fatalf("%s=false: roles = %+v, %v; want that role off", name, got, err)
			}
			enabled := 0
			for _, other := range cases {
				if other(got) {
					enabled++
				}
			}
			if enabled != 3 {
				t.Fatalf("%s=false: %d roles on, want 3", name, enabled)
			}
		}
	})

	t.Run("an invalid value names the variable and never echoes the value", func(t *testing.T) {
		setRoles(t, map[string]string{"HTTP_ENABLED": "talvez-42"})
		_, err := config.RolesFromEnv()
		if err == nil || !strings.Contains(err.Error(), "HTTP_ENABLED") || strings.Contains(err.Error(), "talvez-42") {
			t.Fatalf("RolesFromEnv() error = %v; want it naming HTTP_ENABLED without the value", err)
		}
	})
}
