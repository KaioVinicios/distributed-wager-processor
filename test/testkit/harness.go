package testkit

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/config"
)

// Harness is what App and Cluster share (spec M8, decision 1): the isolated
// database, queues and events topic of a test package, the OpenAPI contract,
// and the instances of the application to call, one for App (in process) and
// one per process for Cluster.
type Harness struct {
	// Audit reads the audit queue of the isolated events topic.
	Audit *Audit
	// WagerQueueURL and DLQURL are the isolated queues.
	WagerQueueURL, DLQURL string

	env      *Env
	sqs      *sqs.Client
	http     *http.Client
	contract *Contract
	targets  []*target

	mu   sync.Mutex // guards next
	next int
}

// target is one instance to call. live turns false once its process exits;
// armed is true while it runs with a fault point. The round-robin only picks a
// live, disarmed instance (spec M8, decisions 3 and 15).
type target struct {
	baseURL, metricsURL string
	live, armed         atomic.Bool
}

func newTarget(httpAddr, metricsAddr string) *target {
	t := &target{baseURL: "http://" + httpAddr, metricsURL: "http://" + metricsAddr}
	t.live.Store(true)
	return t
}

// pick returns the next live, disarmed instance in round-robin order, or false
// when there is none. A plain counter under a mutex: the index needs no
// integer conversion.
func (h *Harness) pick() (*target, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for range h.targets {
		tg := h.targets[h.next%len(h.targets)]
		h.next++
		if tg.live.Load() && !tg.armed.Load() {
			return tg, true
		}
	}
	return nil, false
}

// Client returns a client with a real token of clientID in the realm pda; ""
// sends no token. Each request goes to the next live, disarmed instance.
func (h *Harness) Client(tb testing.TB, clientID string) *Client {
	tb.Helper()
	if clientID == "" {
		return &Client{h: h}
	}
	return &Client{h: h, token: Token(tb, clientID)}
}

// ClientWithToken returns a client that sends raw as the bearer token.
func (h *Harness) ClientWithToken(raw string) *Client { return &Client{h: h, token: raw} }

// CloseIdleConnections drops the idle keep-alive connections of the harness
// client, so the next request dials again (R04: a stopping instance refuses).
func (h *Harness) CloseIdleConnections() { h.http.CloseIdleConnections() }

// Owner is the pool of pda_owner, for setups and assertions the app role cannot do.
func (h *Harness) Owner() *pgxpool.Pool { return h.env.Owner }

// OpenWallet opens a wallet of a new player through the API, as the internal
// service, and checks it against test-plan §6 when the test ends.
func (h *Harness) OpenWallet(tb testing.TB, initial Money) Wallet {
	tb.Helper()
	resp := h.Client(tb, "wallet-service").Do(tb, Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": NewID(), "initialBalance": initial,
	}})
	if resp.Status != http.StatusCreated {
		tb.Fatalf("POST /wallets = %d %s", resp.Status, resp.Body)
	}
	var w Wallet
	resp.JSON(tb, &w)
	tb.Cleanup(func() { h.AssertWalletConsistent(tb, w.ID) })
	return w
}

// SendWager sends body to the wager queue and returns the SQS message id.
func (h *Harness) SendWager(tb testing.TB, body string, o SendOpts) string {
	tb.Helper()
	return SendMessage(tb, h.sqs, h.WagerQueueURL, body, o)
}

// AssertQueueDrained waits until the wager queue is empty.
func (h *Harness) AssertQueueDrained(tb testing.TB) {
	tb.Helper()
	AssertQueueDrained(tb, h.sqs, h.WagerQueueURL)
}

// DLQDepth is the visible plus in-flight messages of the DLQ.
func (h *Harness) DLQDepth(tb testing.TB) int {
	tb.Helper()
	n, err := QueueDepth(tb.Context(), h.sqs, h.DLQURL)
	if err != nil {
		tb.Fatal(err)
	}
	return n
}

// metric returns the value of the sample named exactly as the admin /metrics
// of the instance exposes it, labels included, or "" when it is absent.
func (h *Harness) metric(tb testing.TB, tg *target, name string) string {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tg.metricsURL+"/metrics", nil)
	if err != nil {
		tb.Fatal(err)
	}
	resp, err := h.http.Do(req)
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

// metricValue is metric for a counter sample (`http_requests_total{…}`): a
// whole number, 0 while the series does not exist. No floating point.
func (h *Harness) metricValue(tb testing.TB, tg *target, sample string) int64 {
	tb.Helper()
	raw := h.metric(tb, tg, sample)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		tb.Fatalf("metric %s = %q is not a whole number: %v", sample, raw, err)
	}
	return v
}

// fixture is what StartApp and StartCluster share (spec M8, decision 7): the
// isolated queues and events topic, the audit collector, the contract and the
// base configuration. Only the way the application runs differs.
type fixture struct {
	harness *Harness
	cfg     config.Config // everything but the listen addresses
	region  string
	remove  func() // deletes the queues and the topic
}

func (e *Env) newFixture(ctx context.Context) (*fixture, error) {
	awsCfg, err := rootAWS(ctx)
	if err != nil {
		return nil, err
	}
	sqsClient, snsClient := sqs.NewFromConfig(awsCfg), sns.NewFromConfig(awsCfg)
	wager, dlq, removeQueues, err := createQueues(ctx, sqsClient)
	if err != nil {
		return nil, err
	}
	queues, err := awsclient.ResolveQueues(ctx, sqsClient, wager, dlq)
	if err != nil {
		removeQueues()
		return nil, err
	}
	topic, removeTopic, err := CreateEventsTopic(ctx, sqsClient, snsClient)
	if err != nil {
		removeQueues()
		return nil, err
	}
	remove := func() {
		removeTopic()
		removeQueues()
	}
	audit, err := NewAudit(ctx, sqsClient, topic.AuditQueueURL)
	if err != nil {
		remove()
		return nil, err
	}
	contract, err := LoadContract()
	if err != nil {
		remove()
		return nil, err
	}
	cfg := e.Config()
	cfg.WagerQueueName, cfg.WagerDLQName = wager, dlq
	cfg.SNSEventsTopicName = topic.Name
	cfg.OIDCIssuer, cfg.OIDCJWKSURL = KeycloakIssuer, KeycloakIssuer+"/protocol/openid-connect/certs"
	cfg.OIDCAudience, cfg.OIDCClockSkew = "pda-api", time.Second
	cfg.APIDocsEnabled = true
	cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay = 100*time.Millisecond, time.Second
	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
	h := &Harness{
		Audit: audit, WagerQueueURL: queues.WagerURL, DLQURL: queues.DLQURL,
		env: e, sqs: sqsClient, http: &http.Client{Timeout: requestTimeout}, contract: contract,
	}
	return &fixture{harness: h, cfg: cfg, region: awsCfg.Region, remove: remove}, nil
}
