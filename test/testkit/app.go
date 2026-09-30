package testkit

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/observability"
)

// App is the application under test: the Fx graph of the binary, started in
// process over the package database and queues, with the real Keycloak (spec
// decision 21).
type App struct {
	BaseURL    string
	MetricsURL string
	// Audit reads the audit queue of the app's isolated events topic.
	Audit *Audit

	env      *Env
	http     *http.Client
	contract *Contract
	logs     *syncBuffer
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
// for assertions. stop stops it and deletes the queues and the topic.
func (e *Env) StartApp(ctx context.Context) (*App, func(), error) {
	awsCfg, err := rootAWS(ctx)
	if err != nil {
		return nil, nil, err
	}
	sqsClient, snsClient := sqs.NewFromConfig(awsCfg), sns.NewFromConfig(awsCfg)
	wager, dlq, removeQueues, err := createQueues(ctx, sqsClient)
	if err != nil {
		return nil, nil, err
	}
	topic, removeTopic, err := CreateEventsTopic(ctx, sqsClient, snsClient)
	if err != nil {
		removeQueues()
		return nil, nil, err
	}
	removeAWS := func() {
		removeTopic()
		removeQueues()
	}
	audit, err := NewAudit(ctx, sqsClient, topic.AuditQueueURL)
	if err != nil {
		removeAWS()
		return nil, nil, err
	}
	httpAddr, err := freeAddr(ctx)
	if err != nil {
		removeAWS()
		return nil, nil, err
	}
	metricsAddr, err := freeAddr(ctx)
	if err != nil {
		removeAWS()
		return nil, nil, err
	}
	cfg := e.Config()
	cfg.HTTPAddr, cfg.MetricsAddr = httpAddr, metricsAddr
	cfg.WagerQueueName, cfg.WagerDLQName = wager, dlq
	cfg.SNSEventsTopicName = topic.Name
	cfg.OIDCIssuer, cfg.OIDCJWKSURL = KeycloakIssuer, KeycloakIssuer+"/protocol/openid-connect/certs"
	cfg.OIDCAudience, cfg.OIDCClockSkew = "pda-api", time.Second
	cfg.APIDocsEnabled = true
	cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay = 100*time.Millisecond, time.Second
	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
	if err := cfg.Validate(); err != nil {
		removeAWS()
		return nil, nil, fmt.Errorf("testkit: app config: %w", err)
	}
	contract, err := LoadContract()
	if err != nil {
		removeAWS()
		return nil, nil, err
	}
	logs := &syncBuffer{}
	app := fx.New(append(bootstrap.Options(),
		fx.Replace(cfg),
		fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(logs, "info") }),
	)...)
	if err := app.Start(ctx); err != nil {
		removeAWS()
		return nil, nil, fmt.Errorf("testkit: start the app: %w (logs: %s)", err, logs.String())
	}
	a := &App{
		BaseURL: "http://" + httpAddr, MetricsURL: "http://" + metricsAddr, Audit: audit,
		env: e, http: &http.Client{Timeout: requestTimeout}, contract: contract, logs: logs,
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		a.http.CloseIdleConnections()
		_ = app.Stop(ctx)
		removeAWS()
	}
	return a, stop, nil
}

// Client returns a client with a real token of clientID in the realm pda;
// "" sends no token.
func (a *App) Client(tb testing.TB, clientID string) *Client {
	tb.Helper()
	if clientID == "" {
		return &Client{app: a}
	}
	return &Client{app: a, token: Token(tb, clientID)}
}

// ClientWithToken returns a client that sends raw as the bearer token.
func (a *App) ClientWithToken(raw string) *Client { return &Client{app: a, token: raw} }

// Logs returns what the application logged so far.
func (a *App) Logs() string { return a.logs.String() }

// Metric returns the value of an unlabeled sample of the admin /metrics.
func (a *App) Metric(tb testing.TB, name string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.MetricsURL+"/metrics", nil)
	if err != nil {
		tb.Fatal(err)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		tb.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatal(err)
	}
	for line := range strings.Lines(string(body)) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), name+" "); ok {
			return value
		}
	}
	return ""
}

// OpenWallet opens a wallet of a new player through the API, as the internal
// service, and checks it against test-plan §6 when the test ends.
func (a *App) OpenWallet(tb testing.TB, initial Money) Wallet {
	tb.Helper()
	resp := a.Client(tb, "wallet-service").Do(tb, Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": NewID(), "initialBalance": initial,
	}})
	if resp.Status != http.StatusCreated {
		tb.Fatalf("POST /wallets = %d %s", resp.Status, resp.Body)
	}
	var w Wallet
	resp.JSON(tb, &w)
	tb.Cleanup(func() { a.AssertWalletConsistent(tb, w.ID) })
	return w
}
