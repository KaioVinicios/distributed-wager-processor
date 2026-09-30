package bootstrap_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: TST-I07, FX-01 (I07a — M0 modules, M2 persistence, M3 auth, use cases and API, M4 outbox, M5 consumer, M6 reference worker)
// Sensitivity (M6): references.Module out of bootstrap.Options → "missing type: *references.Worker".
// Sensitivity (M5): sqsconsumer.Module out of bootstrap.Options → "missing type: *sqsconsumer.Consumer".
func TestFxGraph(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/pda")
	t.Setenv("OIDC_ISSUER", "http://localhost:8080/realms/pda")
	t.Setenv("OIDC_JWKS_URL", "http://localhost:8080/realms/pda/protocol/openid-connect/certs")
	t.Setenv("AWS_REGION", "us-east-1")

	var (
		health    *observability.Health
		pool      *pgxpool.Pool
		queues    *awsclient.Queues
		handler   http.Handler
		uow       app.UnitOfWork
		repos     app.Repos
		verifier  *auth.Verifier
		metrics   app.Metrics
		open      *app.OpenWallet
		process   *app.ProcessWager
		queries   *app.Queries
		reconcile *app.Reconcile
		store     app.OutboxStore
		topic     *awsclient.Topic
		publisher *outbox.Publisher
		consume   *app.ConsumeWager
		consumer  *sqsconsumer.Consumer
		resolve   *app.ResolveReferences
		worker    *references.Worker
	)
	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &handler, &uow, &repos,
		&verifier, &metrics, &open, &process, &queries, &reconcile, &store, &topic, &publisher,
		&consume, &consumer, &resolve, &worker))
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("fx.ValidateApp() = %v", err)
	}
}

// Covers: FX-01, D-15 (U21)
// Sensitivity: keeping references.Module in OptionsFor when ReferenceWorker is off → the "off" case finds the worker and fails.
func TestOptionsFor(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/pda")
	t.Setenv("OIDC_ISSUER", "http://localhost:8080/realms/pda")
	t.Setenv("OIDC_JWKS_URL", "http://localhost:8080/realms/pda/protocol/openid-connect/certs")
	t.Setenv("AWS_REGION", "us-east-1")

	all := config.Roles{HTTP: true, Consumer: true, OutboxPublisher: true, ReferenceWorker: true}
	cases := []struct {
		name string
		off  func(*config.Roles)
		need fx.Option // asks for the type the role provides
	}{
		{"http", func(r *config.Roles) { r.HTTP = false }, fx.Invoke(func(http.Handler) {})},
		{"consumer", func(r *config.Roles) { r.Consumer = false }, fx.Invoke(func(*sqsconsumer.Consumer) {})},
		{"outbox publisher", func(r *config.Roles) { r.OutboxPublisher = false }, fx.Invoke(func(*outbox.Publisher) {})},
		{"reference worker", func(r *config.Roles) { r.ReferenceWorker = false }, fx.Invoke(func(*references.Worker) {})},
	}
	for _, tc := range cases {
		t.Run(tc.name+" on", func(t *testing.T) {
			if err := fx.ValidateApp(append(bootstrap.OptionsFor(all), tc.need)...); err != nil {
				t.Fatalf("ValidateApp() = %v, want the type available with the role on", err)
			}
		})
		t.Run(tc.name+" off", func(t *testing.T) {
			roles := all
			tc.off(&roles)
			if err := fx.ValidateApp(bootstrap.OptionsFor(roles)...); err != nil {
				t.Fatalf("ValidateApp() = %v, want the graph valid without the role", err)
			}
			err := fx.ValidateApp(append(bootstrap.OptionsFor(roles), tc.need)...)
			if err == nil || !strings.Contains(err.Error(), "missing type") {
				t.Fatalf("ValidateApp() = %v, want a missing type with the role off", err)
			}
		})
	}

	t.Run("every role off is a valid graph", func(t *testing.T) {
		if err := fx.ValidateApp(bootstrap.OptionsFor(config.Roles{})...); err != nil {
			t.Fatalf("ValidateApp() = %v", err)
		}
	})
}
