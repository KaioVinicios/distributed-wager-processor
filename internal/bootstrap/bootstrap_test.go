package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
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

// Covers: OBS-01 (U32; M0 pending item 3: Fx lifecycle events at DEBUG, Fx errors at ERROR)
func TestFxEventsLogAtDebug(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/pda")
	t.Setenv("OIDC_ISSUER", "http://localhost:8080/realms/pda")
	t.Setenv("OIDC_JWKS_URL", "http://localhost:8080/realms/pda/protocol/openid-connect/certs")
	t.Setenv("AWS_REGION", "us-east-1")

	// build runs the constructors and invokes of the graph (no start, so no
	// network) with the logger at level, and returns what was logged.
	build := func(level string) string {
		var buf bytes.Buffer
		logTo := fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(&buf, level) })
		boom := fx.Invoke(func() error { return errors.New("boom") })
		if app := fx.New(append(bootstrap.OptionsFor(config.Roles{}), logTo, boom)...); app.Err() == nil {
			t.Fatal("fx.New() error = nil, want the failing invoke")
		}
		return buf.String()
	}
	fxEvents := map[string]bool{
		"provided": true, "supplied": true, "decorated": true, "invoking": true, "invoked": true,
		"run": true, "initialized custom fxevent.Logger": true,
	}

	info := build("info")
	failed := false
	for line := range strings.Lines(info) {
		var entry struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q is not JSON: %v", line, err)
		}
		if fxEvents[entry.Msg] {
			t.Errorf("Fx event %q logged at %s with LOG_LEVEL=info; want it at DEBUG", entry.Msg, entry.Level)
		}
		failed = failed || (entry.Msg == "invoke failed" && entry.Level == "ERROR")
	}
	if !failed {
		t.Fatalf("no ERROR \"invoke failed\" line with LOG_LEVEL=info:\n%s", info)
	}
	if debug := build("debug"); !strings.Contains(debug, `"level":"DEBUG","msg":"provided"`) {
		t.Fatalf("no DEBUG \"provided\" line with LOG_LEVEL=debug; the events must be demoted, not dropped:\n%s", debug)
	}
}
