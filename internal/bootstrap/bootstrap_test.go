package bootstrap_test

import (
	"net/http"
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
