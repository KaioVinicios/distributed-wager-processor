package bootstrap_test

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: TST-I07, FX-01 (I07a — M0 modules + M2 persistence ports)
func TestFxGraph(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/pda")
	t.Setenv("AWS_REGION", "us-east-1")

	var (
		health *observability.Health
		pool   *pgxpool.Pool
		queues *awsclient.Queues
		mux    *http.ServeMux
		uow    app.UnitOfWork
		repos  app.Repos
	)
	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &mux, &uow, &repos))
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("fx.ValidateApp() = %v", err)
	}
}
