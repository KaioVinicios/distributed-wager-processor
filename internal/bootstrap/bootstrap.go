// Package bootstrap is the only place that knows every Fx module.
package bootstrap

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Options returns the application modules for the roles of the environment
// (D-15). A malformed role variable aborts the start with an error that names it.
func Options() []fx.Option {
	roles, err := config.RolesFromEnv()
	if err != nil {
		// No fx.WithLogger yet: Fx's console logger reports the error on stderr.
		return []fx.Option{fx.Error(err)}
	}
	return OptionsFor(roles)
}

// OptionsFor returns the application modules in registration order (D-15):
// dependencies first, then the enabled workers (references, outbox, consumer),
// HTTP last, so it starts last and stops first; the workers stop before the
// pool and the AWS clients close. A disabled role leaves its module out of the
// graph; the auth module comes with the HTTP or with the consumer. The admin
// server and the observability module are always present.
func OptionsFor(roles config.Roles) []fx.Option {
	opts := []fx.Option{
		fx.StopTimeout(config.MaxShutdownTimeout),
		fx.WithLogger(fxLogger),
		fx.Supply(roles),
		config.Module,
		observability.Module,
		postgres.Module,
		awsclient.Module,
	}
	if roles.HTTP || roles.Consumer {
		opts = append(opts, auth.Module) // bearer tokens of the API and accessToken of the messages (D-23)
	}
	opts = append(opts, appModule)
	if roles.ReferenceWorker {
		opts = append(opts, references.Module)
	}
	if roles.OutboxPublisher {
		opts = append(opts, outbox.Module)
	}
	if roles.Consumer {
		opts = append(opts, sqsconsumer.Module)
	}
	if roles.HTTP {
		opts = append(opts, httpapi.Module)
	}
	return append(opts, fx.Invoke(logRoles))
}

// logRoles records which roles this instance runs; with none, only the admin
// server is up, which is valid but rarely intended.
func logRoles(roles config.Roles, log *slog.Logger) {
	log.Info("roles resolved", "http", roles.HTTP, "consumer", roles.Consumer,
		"outboxPublisher", roles.OutboxPublisher, "referenceWorker", roles.ReferenceWorker)
	if roles == (config.Roles{}) {
		log.Warn("every role is disabled: only the admin server runs")
	}
}

// fxLogger writes the Fx lifecycle events at DEBUG, so they do not bury the
// application logs at INFO; Fx errors stay at ERROR (spec of the M0 pending
// items, decision 3). LOG_LEVEL=debug shows the events again.
func fxLogger(log *slog.Logger) fxevent.Logger {
	l := &fxevent.SlogLogger{Logger: log}
	l.UseLogLevel(slog.LevelDebug)
	return l
}
