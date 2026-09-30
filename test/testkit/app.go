package testkit

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// App is the application under test in process: the Fx graph of the binary,
// started over the package's database and queues, with the real Keycloak
// (spec M3, decision 21). Its Harness has the one instance.
type App struct {
	*Harness
	logs *syncBuffer
}

// syncBuffer collects the log lines of the application.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// StartApp starts the application once per package, from TestMain: isolated
// queues and events topic, the accelerated times of test-plan §3.3, OIDC
// against the compose Keycloak with a clock skew of 1 s, and the logs captured
// for assertions. opts adjust the configuration before it is validated (a test
// that needs another reference schedule). stop stops it and deletes the queues
// and the topic.
func (e *Env) StartApp(ctx context.Context, opts ...func(*config.Config)) (*App, func(), error) {
	f, err := e.newFixture(ctx)
	if err != nil {
		return nil, nil, err
	}
	httpAddr, err := freeAddr(ctx)
	if err != nil {
		f.remove()
		return nil, nil, err
	}
	metricsAddr, err := freeAddr(ctx)
	if err != nil {
		f.remove()
		return nil, nil, err
	}
	cfg := f.cfg
	cfg.HTTPAddr, cfg.MetricsAddr = httpAddr, metricsAddr
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		f.remove()
		return nil, nil, fmt.Errorf("testkit: app config: %w", err)
	}
	logs := &syncBuffer{}
	app := fx.New(append(bootstrap.Options(),
		fx.Replace(cfg),
		fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(logs, "info") }),
	)...)
	if err := app.Start(ctx); err != nil {
		f.remove()
		return nil, nil, fmt.Errorf("testkit: start the app: %w (logs: %s)", err, logs.String())
	}
	h := f.harness
	h.targets = []*target{newTarget(httpAddr, metricsAddr)}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		h.http.CloseIdleConnections()
		_ = app.Stop(ctx)
		f.remove()
	}
	return &App{Harness: h, logs: logs}, stop, nil
}

// Logs returns what the application logged so far.
func (a *App) Logs() string { return a.logs.String() }

// Metric returns the value of an unlabeled sample of the admin /metrics.
func (a *App) Metric(tb testing.TB, name string) string {
	tb.Helper()
	return a.metric(tb, a.targets[0], name)
}

// MetricValue returns the value of the counter sample named exactly as
// exposed, labels included (`http_requests_total{method="GET",route="…",status="200"}`),
// or 0 while the series does not exist yet.
func (a *App) MetricValue(tb testing.TB, sample string) int64 {
	tb.Helper()
	return a.metricValue(tb, a.targets[0], sample)
}
