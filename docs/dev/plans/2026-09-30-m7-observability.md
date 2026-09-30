# M7 — Observabilidade e papéis do processo: plano de implementação

> **Para quem executa:** o autor escolheu execução **inline** com `superpowers:executing-plans` ([`development-workflow.md`](../../development-workflow.md) §3; subagentes só com pedido explícito). Cada tarefa usa `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Não há passos de commit:** os commits são propostos no fim do marco, com o autor autorizando ([`development-workflow.md`](../../development-workflow.md) §6).

**Objetivo:** fechar OBS-01..04 e FX-01, FX-04 e FX-05: catálogo de métricas completo, logs com os IDs e sem segredos, papéis do processo por env e a prova da ordem de parada.

**Arquitetura:** o `app` declara a porta `Metrics` (sem Prometheus) e a `observability.Metrics` a implementa; o `httpapi` declara a sua própria porta menor. Os papéis entram em `bootstrap.OptionsFor(config.Roles)`, que monta o grafo sem os módulos desligados. Os logs ganham uma linha de conclusão por operação no `ProcessWager`.

**Stack:** Go 1.27.1, Uber Fx, `prometheus/client_golang` (`testutil`), `log/slog`, `caarlos0/env`, testes com as tags `integration` do repositório.

**Spec:** [`dev/specs/2026-09-30-m7-observability-design.md`](../specs/2026-09-30-m7-observability-design.md). Este plano ajusta a spec em 5 pontos, todos a registrar na Tarefa 8:
1. o método do `app.Metrics` para duplicatas chama-se `WagerDuplicate`, porque `observability.Metrics` já tem `Duplicate(layer)` (porta do consumidor, M5);
2. sem helper `LogAttrs` (decisão 11): as chaves ficam como literais `camelCase`, que o `sloglint` e o I14 conferem. Um pacote só de constantes não compraria nada;
3. o log do consumidor mantém `sqsMessageId` (id do broker); o `messageId` do envelope aparece na linha `wager concluded` (decisão 12, segunda metade);
4. os IDs de teste da spec colidem com os existentes (I24 já é `TestOpenWalletAPI`): `TestMetricsEndpoint` = **I25**, `TestConcurrencyConflictMetric` = **I26**, `TestFxRoles` = **I27**;
5. o 503 real com o PostgreSQL pausado (I13 na spec) fica com o R01 (M9): pausar o PostgreSQL do compose derrubaria os pacotes de teste em paralelo. O I13 do M7 é o teste unitário que já existe mais o `TestFxRoles`, que prova o `/health/ready` com PostgreSQL + SQS sob cada papel desligado.

## Restrições globais

- `make check` verde ao fim de cada tarefa que mexe em código; `make test-integration` verde ao fim das Tarefas 6 e 8.
- Testes novos usam o vocabulário do repositório: `// Covers: …` acima de cada teste, `// Sensitivity: …` nos testes escritos sobre comportamento que já existe (sabotar, ver falhar, desfazer).
- O red é uma **asserção falhando**, não só um erro de compilação: onde uma função nova é necessária para compilar, escreva antes o stub mínimo (assinatura correta, corpo neutro).
- Chaves de log em `camelCase` (`sloglint`); nenhum `float32/float64` em dinheiro (`forbidigo`); o `app` não importa Prometheus nem `observability`.
- Nenhum log registra: `Authorization`, o token, a `Idempotency-Key`, o corpo, `amount`, saldos, `DATABASE_URL`, chaves AWS.
- Sem commits, branches ou worktrees automáticos.

## Foco da revisão (o que os testes de cada tarefa não cobririam sozinhos)

1. **Papéis todos desligados** → o grafo valida e só o admin sobe (Tarefa 1, `TestOptionsFor`).
2. **Variável de papel inválida** (`HTTP_ENABLED=talvez`) → o erro nomeia a variável e **não** ecoa o valor (Tarefa 1).
3. **Cardinalidade da label `route`** → rota inexistente vira `unmatched`, nunca o path bruto (Tarefa 5).
4. **Contagem em dobro no SQS** → um replay pelo SQS não incrementa `wager_duplicates_total{channel="http"}` nem `wager_transactions_total` (Tarefa 4).
5. **Vazamento pelos logs de erro** → um fluxo que passa por 401, 403, 409 e 422 não escreve token, `amount`, chave nem corpo (Tarefa 7, I14).

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `internal/config/roles.go` (novo) | `Roles` e `RolesFromEnv` | 1 |
| `internal/config/config.go` | `envVarOf` também procura em `Roles` | 1 |
| `internal/bootstrap/bootstrap.go` | `OptionsFor(Roles)`, `Options()`, log dos papéis | 1 |
| `internal/observability/metrics.go` | coletores e métodos novos | 2 |
| `internal/app/ports.go`, `internal/app/system.go` | `app.Metrics` ampliada, `NopMetrics` | 3 |
| `internal/app/errors.go` | `ErrLockTimeout`, razões de conflito | 3 |
| `internal/adapters/postgres/errors.go` | `55P03`/`40P01` → `ErrLockTimeout` | 3 |
| `internal/app/process_wager.go` | `WithMetrics`, `observe`, linha `wager concluded` | 4 |
| `internal/app/resolve_references.go` | métricas do canal `worker`, `Kind` no resultado | 4 |
| `internal/app/reconcile.go` | `Reconciled`, log sem saldos | 4 |
| `internal/adapters/httpapi/{middleware,routes,handler,wagering_handler,module}.go` | `Metrics` do edge, `unmatched`, falhas de auth | 5 |
| `internal/bootstrap/app_module.go` | provider do `ProcessWager` com métricas | 6 |
| `test/integration/observability_test.go` (novo) | I14, I25, I26 | 7 |
| `internal/bootstrap/bootstrap_integration_test.go` | I07b (ordem), I07c, I27 | 6 |
| `docs/…`, `ARCHITECTURE.md` | decisões e fecho do marco | 8 |

---

### Tarefa 1: Papéis do processo (`config.Roles`, `OptionsFor`)

**Arquivos:**
- Criar: `internal/config/roles.go`, `internal/config/roles_test.go`
- Modificar: `internal/config/config.go` (`envVarOf`), `internal/bootstrap/bootstrap.go`, `internal/bootstrap/bootstrap_test.go`

**Interfaces:**
- Produz: `config.Roles{HTTP, Consumer, OutboxPublisher, ReferenceWorker bool}`; `config.RolesFromEnv() (Roles, error)`; `bootstrap.OptionsFor(config.Roles) []fx.Option`. `bootstrap.Options()` continua com a mesma assinatura.

- [ ] **Passo 1: stub dos papéis** (só para compilar o teste)

`internal/config/roles.go`:

```go
package config

// Roles are the parts of the process an instance runs (D-15).
type Roles struct {
	HTTP            bool `env:"HTTP_ENABLED" envDefault:"true"`
	Consumer        bool `env:"CONSUMER_ENABLED" envDefault:"true"`
	OutboxPublisher bool `env:"OUTBOX_PUBLISHER_ENABLED" envDefault:"true"`
	ReferenceWorker bool `env:"REFERENCE_WORKER_ENABLED" envDefault:"true"`
}

// RolesFromEnv reads only the four role variables. It runs before the Fx graph
// exists, because a disabled role removes its module from the graph.
func RolesFromEnv() (Roles, error) { return Roles{}, nil }
```

- [ ] **Passo 2: escrever o teste que falha** (`internal/config/roles_test.go`)

```go
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
```

- [ ] **Passo 3: ver falhar**

Run: `go test ./internal/config -run TestRolesFromEnv -v`
Expected: FAIL — `roles = {…all false…}, want all true` (o stub devolve o zero value).

- [ ] **Passo 4: implementar** (`roles.go`, substituindo o stub; e `envVarOf` em `config.go`)

```go
// RolesFromEnv reads only the four role variables. It runs before the Fx graph
// exists, because a disabled role removes its module from the graph. Errors
// name variables, never values.
func RolesFromEnv() (Roles, error) {
	roles, err := env.ParseAs[Roles]()
	if err != nil {
		return Roles{}, redactParseError(err)
	}
	return roles, nil
}
```

Adicione o import `github.com/caarlos0/env/v11` em `roles.go`. Em `config.go`, faça o `envVarOf` procurar nos dois tipos:

```go
// envVarOf maps a Config or Roles field name to its environment variable.
func envVarOf(field string) string {
	for _, t := range []reflect.Type{reflect.TypeFor[Config](), reflect.TypeFor[Roles]()} {
		if f, ok := t.FieldByName(field); ok {
			name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
			return name
		}
	}
	return field
}
```

- [ ] **Passo 5: ver passar**

Run: `go test ./internal/config -v`
Expected: PASS (todos os testes do pacote, incluindo os antigos).

- [ ] **Passo 6: teste do grafo por papel** (acrescente ao fim de `internal/bootstrap/bootstrap_test.go`)

```go
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
```

Acrescente ao bloco de imports do arquivo: `"strings"` e `"github.com/KaioVinicios/pda/internal/config"`.

- [ ] **Passo 7: stub do `OptionsFor` e ver falhar**

Em `bootstrap.go`, deixe `Options()` como está e acrescente:

```go
// OptionsFor is Options for an explicit set of roles.
func OptionsFor(config.Roles) []fx.Option { return Options() }
```

Run: `go test ./internal/bootstrap -run TestOptionsFor -v`
Expected: FAIL — `off` cases: `ValidateApp() = nil, want a missing type with the role off`.

- [ ] **Passo 8: implementar** (`internal/bootstrap/bootstrap.go`)

```go
// Options returns the application modules for the roles of the environment
// (D-15). A malformed role variable aborts the start with an error that names it.
func Options() []fx.Option {
	roles, err := config.RolesFromEnv()
	if err != nil {
		return []fx.Option{fx.NopLogger, fx.Error(err)}
	}
	return OptionsFor(roles)
}

// OptionsFor returns the application modules in registration order (D-15):
// dependencies first, then the enabled workers (references, outbox, consumer),
// HTTP last, so it starts last and stops first; the workers stop before the
// pool and the AWS clients close. A disabled role leaves its module out of the
// graph. The admin server and the observability module are always present.
func OptionsFor(roles config.Roles) []fx.Option {
	opts := []fx.Option{
		fx.StopTimeout(config.MaxShutdownTimeout),
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger { return &fxevent.SlogLogger{Logger: log} }),
		fx.Supply(roles),
		config.Module,
		observability.Module,
		postgres.Module,
		awsclient.Module,
	}
	if roles.HTTP {
		opts = append(opts, auth.Module)
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
```

- [ ] **Passo 9: ver passar**

Run: `go test ./internal/bootstrap ./internal/config -v` e depois `make check`
Expected: PASS; `make check` verde. Se o `ValidateApp` reclamar de um tipo que só o `httpapi` consumia (por exemplo o `auth.Policy`), o grafo está errado: leia a mensagem e ajuste o módulo que ficou sem o seu consumidor, sem afrouxar o teste.

- [ ] **Passo 10: sensibilidade.** Em `OptionsFor`, faça `references.Module` entrar sempre → o caso `reference worker off` deve falhar (`want a missing type`). Desfaça.

---

### Tarefa 2: Coletores Prometheus do catálogo (U20)

**Arquivos:**
- Modificar: `internal/observability/metrics.go`, `internal/observability/metrics_test.go`

**Interfaces:**
- Produz (em `*observability.Metrics`):
  - `WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration)`
  - `WagerDuplicate(channel, layer string)`
  - `Conflict(reason string)`
  - `Reconciled(consistent bool)`
  - `AuthFailure(reason string)`
  - `HTTPRequest(route, method string, status int, d time.Duration)`

- [ ] **Passo 1: stubs** (em `metrics.go`, no fim; corpo vazio)

```go
// WagerConcluded counts a newly concluded operation and its processing time.
func (m *Metrics) WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration) {}

// WagerDuplicate counts a repeated delivery caught by layer (inbox or idempotency).
func (m *Metrics) WagerDuplicate(channel, layer string) {}

// Conflict counts a concurrency conflict (lock_timeout or unique_race).
func (m *Metrics) Conflict(reason string) {}

// Reconciled counts a reconciliation run.
func (m *Metrics) Reconciled(consistent bool) {}

// AuthFailure counts a refused access (unauthenticated, forbidden, provider_mismatch).
func (m *Metrics) AuthFailure(reason string) {}

// HTTPRequest counts a request and its duration.
func (m *Metrics) HTTPRequest(route, method string, status int, d time.Duration) {}
```

- [ ] **Passo 2: escrever os testes que falham** (acrescente a `metrics_test.go`)

```go
// Covers: OBS-03 (U20; ARCHITECTURE.md §13.2)
func TestMetrics_Wagers(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.WagerConcluded("http", "BET", "processed", "", 40*time.Millisecond)
	m.WagerConcluded("http", "BET", "rejected", "INSUFFICIENT_FUNDS", 10*time.Millisecond)
	m.WagerConcluded("worker", "REFUND", "processed", "", 5*time.Millisecond)
	m.WagerDuplicate("http", "idempotency")
	m.WagerDuplicate("http", "idempotency")
	m.Conflict("lock_timeout")
	m.Conflict("unique_race")
	m.Conflict("unique_race")

	want := `
# HELP concurrency_conflicts_total Concurrency conflicts met while processing an operation, by reason.
# TYPE concurrency_conflicts_total counter
concurrency_conflicts_total{reason="lock_timeout"} 1
concurrency_conflicts_total{reason="unique_race"} 2
# HELP wager_duplicates_total Repeated deliveries of an operation, by channel and deduplication layer.
# TYPE wager_duplicates_total counter
wager_duplicates_total{channel="http",layer="idempotency"} 2
# HELP wager_transactions_total Newly concluded operations, by channel, kind, outcome and failure code.
# TYPE wager_transactions_total counter
wager_transactions_total{channel="http",failure_code="",kind="BET",outcome="processed"} 1
wager_transactions_total{channel="http",failure_code="INSUFFICIENT_FUNDS",kind="BET",outcome="rejected"} 1
wager_transactions_total{channel="worker",failure_code="",kind="REFUND",outcome="processed"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"concurrency_conflicts_total", "wager_duplicates_total", "wager_transactions_total"); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(reg, "wager_processing_duration_seconds"); n != 3 {
		t.Fatalf("wager_processing_duration_seconds series = %d, want 3 (http/processed, http/rejected, worker/processed)", n)
	}
}

// Covers: HTTP-07, OBS-03 (U20)
func TestMetrics_Reconciliation(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.Reconciled(true)
	m.Reconciled(true)
	m.Reconciled(false)

	want := `
# HELP reconciliation_runs_total Reconciliations run, by whether the stored balance matched the ledger.
# TYPE reconciliation_runs_total counter
reconciliation_runs_total{consistent="false"} 1
reconciliation_runs_total{consistent="true"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "reconciliation_runs_total"); err != nil {
		t.Fatal(err)
	}
}

// Covers: AUTH-02, OBS-03 (U20)
func TestMetrics_Auth(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.AuthFailure("unauthenticated")
	m.AuthFailure("unauthenticated")
	m.AuthFailure("provider_mismatch")

	want := `
# HELP auth_failures_total Refused accesses, by reason.
# TYPE auth_failures_total counter
auth_failures_total{reason="provider_mismatch"} 1
auth_failures_total{reason="unauthenticated"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "auth_failures_total"); err != nil {
		t.Fatal(err)
	}
}

// Covers: OBS-03 (U20)
func TestMetrics_HTTP(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.HTTPRequest("POST /wallets", "POST", 201, 30*time.Millisecond)
	m.HTTPRequest("POST /wallets", "POST", 201, 10*time.Millisecond)
	m.HTTPRequest("unmatched", "GET", 404, time.Millisecond)

	want := `
# HELP http_requests_total HTTP requests served, by route pattern, method and status.
# TYPE http_requests_total counter
http_requests_total{method="GET",route="unmatched",status="404"} 1
http_requests_total{method="POST",route="POST /wallets",status="201"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "http_requests_total"); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(reg, "http_request_duration_seconds"); n != 2 {
		t.Fatalf("http_request_duration_seconds series = %d, want 2", n)
	}
}
```

- [ ] **Passo 3: ver falhar**

Run: `go test ./internal/observability -run 'TestMetrics_(Wagers|Reconciliation|Auth|HTTP)' -v`
Expected: FAIL — `GatherAndCompare`: `expected metric … not found` / diferença de saída vazia.

- [ ] **Passo 4: implementar** (`metrics.go`)

Acrescente os campos à struct `Metrics` (depois de `referencePending`):

```go
	wagerTransactions  *prometheus.CounterVec
	wagerDuration      *prometheus.HistogramVec
	conflicts          *prometheus.CounterVec
	reconciliationRuns *prometheus.CounterVec
	authFailures       *prometheus.CounterVec
	httpRequests       *prometheus.CounterVec
	httpDuration       *prometheus.HistogramVec
```

No literal de `NewMetrics`, antes do `}` que fecha `m := &Metrics{…}`:

```go
		wagerTransactions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "wager_transactions_total",
			Help: "Newly concluded operations, by channel, kind, outcome and failure code.",
		}, []string{"channel", "kind", "outcome", "failure_code"}),
		wagerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "wager_processing_duration_seconds",
			Help:    "Time to conclude an operation, by channel and outcome.",
			Buckets: prometheus.ExponentialBucketsRange(0.005, 10, 12),
		}, []string{"channel", "outcome"}),
		conflicts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "concurrency_conflicts_total",
			Help: "Concurrency conflicts met while processing an operation, by reason.",
		}, []string{"reason"}),
		reconciliationRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "reconciliation_runs_total",
			Help: "Reconciliations run, by whether the stored balance matched the ledger.",
		}, []string{"consistent"}),
		authFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "auth_failures_total",
			Help: "Refused accesses, by reason.",
		}, []string{"reason"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests served, by route pattern, method and status.",
		}, []string{"route", "method", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request duration, by route pattern, method and status.",
			Buckets: prometheus.ExponentialBucketsRange(0.001, 30, 12),
		}, []string{"route", "method", "status"}),
```

E no `reg.MustRegister(...)` acrescente `m.wagerTransactions, m.wagerDuration, m.conflicts, m.reconciliationRuns, m.authFailures, m.httpRequests, m.httpDuration`. Troque os stubs pelos corpos (mantendo os comentários):

```go
func (m *Metrics) WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration) {
	m.wagerTransactions.WithLabelValues(channel, kind, outcome, failureCode).Inc()
	m.wagerDuration.WithLabelValues(channel, outcome).Observe(d.Seconds())
}

func (m *Metrics) WagerDuplicate(channel, layer string) {
	m.wagerDuplicates.WithLabelValues(channel, layer).Inc()
}

func (m *Metrics) Conflict(reason string) { m.conflicts.WithLabelValues(reason).Inc() }

func (m *Metrics) Reconciled(consistent bool) {
	m.reconciliationRuns.WithLabelValues(strconv.FormatBool(consistent)).Inc()
}

func (m *Metrics) AuthFailure(reason string) { m.authFailures.WithLabelValues(reason).Inc() }

func (m *Metrics) HTTPRequest(route, method string, status int, d time.Duration) {
	s := strconv.Itoa(status)
	m.httpRequests.WithLabelValues(route, method, s).Inc()
	m.httpDuration.WithLabelValues(route, method, s).Observe(d.Seconds())
}
```

Acrescente `"strconv"` aos imports e atualize o comentário de `NewRegistry` ("The metrics catalog arrives in M7" → "The metrics are registered by NewMetrics.") e o da struct (`M7 adds the rest of the catalog` → remova a frase).

- [ ] **Passo 5: ver passar**

Run: `go test ./internal/observability -v` e `make lint`
Expected: PASS; `promlinter` sem avisos.

- [ ] **Passo 6: sensibilidade.** Troque o rótulo `"layer"` por `"channel"` na ordem de `wagerDuplicates` só localmente? Não: `wagerDuplicates` já existe. Sabote `Reconciled` para sempre passar `"true"` → o teste de reconciliação deve falhar. Desfaça.

---

### Tarefa 3: Porta `app.Metrics`, `ErrLockTimeout` e a tradução no PostgreSQL (U22)

**Arquivos:**
- Modificar: `internal/app/ports.go`, `internal/app/system.go`, `internal/app/errors.go`, `internal/adapters/postgres/errors.go`, `internal/adapters/postgres/errors_test.go`, `internal/app/reconcile_integration_test.go` (o `countingMetrics`)

**Interfaces:**
- Produz: `app.Metrics` com `ReconciliationDivergence()`, `Reconciled(bool)`, `WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration)`, `WagerDuplicate(channel, layer string)`, `Conflict(reason string)`; `app.NopMetrics`; `app.ErrLockTimeout`; `app.ConflictLockTimeout = "lock_timeout"`, `app.ConflictUniqueRace = "unique_race"`.
- `*observability.Metrics` (Tarefa 2) satisfaz a porta sem mudança.

- [ ] **Passo 1: declarar a sentinela e a porta** (sem uso ainda)

Em `errors.go`, dentro do `var (…)` das sentinelas:

```go
	// ErrLockTimeout: a row lock was not obtained in time (lock_timeout) or the
	// database chose this transaction as a deadlock victim. KindTransient.
	ErrLockTimeout = errors.New("app: lock not obtained")
```

E, depois do bloco de códigos:

```go
// Reasons of concurrency_conflicts_total.
const (
	ConflictLockTimeout = "lock_timeout"
	ConflictUniqueRace  = "unique_race"
)
```

Em `ports.go`, substitua a interface `Metrics`:

```go
// Metrics is what the use cases report beyond logs; the observability adapter
// implements it with Prometheus (D-18). The use cases never import Prometheus.
type Metrics interface {
	// ReconciliationDivergence counts a reconciliation whose stored balance
	// differs from the ledger (reconciliation_divergences_total, HTTP-07).
	ReconciliationDivergence()
	// Reconciled counts every reconciliation run (reconciliation_runs_total).
	Reconciled(consistent bool)
	// WagerConcluded counts an operation concluded for the first time, by
	// channel (http, sqs, worker), kind, outcome and failure code, with the
	// time it took (wager_transactions_total, wager_processing_duration_seconds).
	WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration)
	// WagerDuplicate counts a repeated delivery caught by layer
	// (wager_duplicates_total).
	WagerDuplicate(channel, layer string)
	// Conflict counts a concurrency conflict (concurrency_conflicts_total).
	Conflict(reason string)
}

// NopMetrics discards every measurement: the default of a use case built
// without metrics.
type NopMetrics struct{}

func (NopMetrics) ReconciliationDivergence()                                    {}
func (NopMetrics) Reconciled(bool)                                              {}
func (NopMetrics) WagerConcluded(_, _, _, _ string, _ time.Duration)            {}
func (NopMetrics) WagerDuplicate(_, _ string)                                   {}
func (NopMetrics) Conflict(string)                                              {}
```

(O `gofumpt` realinha os comentários e as colunas: rode `make fmt`.)

Em `reconcile_integration_test.go`, faça o `countingMetrics` embutir a nop e manter só o que conta:

```go
type countingMetrics struct {
	app.NopMetrics
	divergences atomic.Int32
	runs        atomic.Int32
}

func (m *countingMetrics) ReconciliationDivergence() { m.divergences.Add(1) }
func (m *countingMetrics) Reconciled(bool)           { m.runs.Add(1) }
```

- [ ] **Passo 2: teste que falha** (`internal/adapters/postgres/errors_test.go`)

Nas linhas da tabela de `TestPostgresErrorMapping`, troque o último campo (hoje `nil`) para `app.ErrLockTimeout` nas linhas dos SQLSTATEs `40P01` ("deadlock") e `55P03` (procure a linha do lock timeout com `grep -n '55P03' internal/adapters/postgres/errors_test.go`):

```go
		{"deadlock", "40P01", "", apperrors.KindTransient, "", app.ErrLockTimeout},
```

Deixe a linha do `55P03` no mesmo formato. Confirme que o loop de asserções já verifica `errors.Is(err, tc.wantErr)` quando `wantErr != nil` (é como as outras sentinelas são testadas).

- [ ] **Passo 3: ver falhar**

Run: `go test ./internal/adapters/postgres -run TestPostgresErrorMapping -v`
Expected: FAIL — `deadlock` e `lock timeout`: `errors.Is(err, app.ErrLockTimeout) = false`.

- [ ] **Passo 4: implementar** (`postgres/errors.go`, em `translate`, logo depois do bloco `uniqueSentinels`)

```go
	if pgErr.Code == "55P03" || pgErr.Code == "40P01" {
		return apperrors.New(apperrors.KindTransient, "", fmt.Errorf("%w: %w", app.ErrLockTimeout, cause))
	}
```

- [ ] **Passo 5: ver passar**

Run: `go test ./internal/adapters/postgres -run 'TestPostgresErrorMapping' -v` e `go build ./... && go vet -tags=integration ./internal/app/...`
Expected: PASS; compila (o `countingMetrics` usa a nop; as demais implementações de `app.Metrics` são só a `observability.Metrics`).

- [ ] **Passo 6: sensibilidade.** Remova o `if` do passo 4 → o teste volta a falhar. Desfaça.

---

### Tarefa 4: Os casos de uso contam e registram (U23)

**Arquivos:**
- Modificar: `internal/app/process_wager.go`, `internal/app/resolve_references.go`, `internal/app/reconcile.go`, `internal/app/reconcile_integration_test.go`
- Criar: `internal/app/metrics_integration_test.go`

**Interfaces:**
- Consome: `app.Metrics`, `app.NopMetrics`, `app.ErrLockTimeout` (Tarefa 3).
- Produz: `(*ProcessWager).WithMetrics(Metrics) *ProcessWager`; `ResolveResult.Kind wagering.Kind`.

- [ ] **Passo 1: stubs**

Em `process_wager.go`: acrescente o campo `metrics Metrics` à struct `ProcessWager`, inicialize-o com `NopMetrics{}` em `NewProcessWager`, e acrescente

```go
// WithMetrics reports the outcomes to m; the default discards them.
func (p *ProcessWager) WithMetrics(m Metrics) *ProcessWager {
	p.metrics = m
	return p
}
```

Em `resolve_references.go`, acrescente `Kind wagering.Kind` a `ResolveResult` (o campo ainda não é preenchido).

- [ ] **Passo 2: escrever os testes que falham** (`internal/app/metrics_integration_test.go`)

```go
//go:build integration

package app_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// recordingMetrics keeps every report, in order.
type recordingMetrics struct {
	app.NopMetrics
	mu         sync.Mutex
	concluded  []string // channel/kind/outcome/failureCode
	duplicates []string // channel/layer
	conflicts  []string
	runs       []bool
}

func (m *recordingMetrics) WagerConcluded(channel, kind, outcome, code string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.concluded = append(m.concluded, channel+"/"+kind+"/"+outcome+"/"+code)
}

func (m *recordingMetrics) WagerDuplicate(channel, layer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.duplicates = append(m.duplicates, channel+"/"+layer)
}

func (m *recordingMetrics) Conflict(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflicts = append(m.conflicts, reason)
}

func (m *recordingMetrics) Reconciled(consistent bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs = append(m.runs, consistent)
}

func (m *recordingMetrics) snapshot() (concluded, duplicates, conflicts []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.concluded...), append([]string(nil), m.duplicates...), append([]string(nil), m.conflicts...)
}

func wantList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// Covers: OBS-03 (U23)
// Sensitivity: counting a replay as a new conclusion → "concluded" gets a second BET entry; counting HTTP duplicates in the SQS path → "duplicates" gets an sqs entry.
func TestProcessWagerMetrics(t *testing.T) {
	t.Parallel()

	t.Run("a new operation is counted once and its replay is a duplicate", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()

		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
		process(t, pw, w, bet)
		process(t, pw, w, bet) // same key and content: replay
		process(t, pw, w, op{provider: p, kind: "BET", amount: "500.00", ext: "bet-2"})

		concluded, duplicates, conflicts := m.snapshot()
		wantList(t, "concluded", concluded, "http/BET/processed/", "http/BET/rejected/INSUFFICIENT_FUNDS")
		wantList(t, "duplicates", duplicates, "http/idempotency")
		wantList(t, "conflicts", conflicts)
	})

	t.Run("a replay over SQS is left to the consumer", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()
		req := request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"}))
		req.Via = wagering.ReceivedViaSQS
		for range 2 {
			if _, err := pw.Execute(t.Context(), req); err != nil {
				t.Fatalf("Execute: %v", err)
			}
		}
		concluded, duplicates, _ := m.snapshot()
		wantList(t, "concluded", concluded, "sqs/BET/processed/")
		wantList(t, "duplicates", duplicates)
	})

	t.Run("a pending reference is counted as pending_reference", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()
		process(t, pw, w, op{provider: p, kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-missing"})
		concluded, _, _ := m.snapshot()
		wantList(t, "concluded", concluded, "http/REFUND/pending_reference/")
	})
}

// Covers: OBS-01, OBS-02 (U23; spec M7, decision 12)
// Sensitivity: logging the amount → the "no amount" check fails; dropping messageId for SQS → the SQS line lacks it.
func TestWagerConcludedLog(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	pw := app.NewProcessWager(newUoW(), reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.NewJSONHandler(&logs, nil)))
	w, p := openWallet(t, "1000.00"), newProvider()

	req := request(command(t, w, op{provider: p, kind: "BET", amount: "137.29", ext: "bet-1", key: "secret-key-7"}))
	req.Via = wagering.ReceivedViaSQS
	req.Inbox = &app.InboxReceipt{Consumer: app.ConsumerName, MessageID: "msg-777", MessageHash: "h", MessageType: "wager.requested", ReceivedAt: time.Now()}
	res, err := pw.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	line := logs.String()
	for _, want := range []string{
		`"msg":"wager concluded"`, `"transactionId":"` + res.Tx.ID() + `"`, `"walletId":"` + w.ID() + `"`,
		`"providerId":"` + p + `"`, `"correlationId":"corr-bet-1"`, `"messageId":"msg-777"`,
		`"channel":"sqs"`, `"kind":"BET"`, `"outcome":"processed"`, `"replay":false`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %s lacks %s", line, want)
		}
	}
	for _, secret := range []string{"137.29", "secret-key-7", "863.71"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log %s leaks %q", line, secret)
		}
	}
}
```

Em `reconcile_integration_test.go`, no subteste "divergence is reported without changing the balance", **troque** a verificação do log (a política mudou, spec decisão 13) e acrescente a das execuções:

```go
		line := logs.String()
		for _, want := range []string{`"level":"WARN"`, w.ID(), "corr-reconcile", `"entries":1`} {
			if !strings.Contains(line, want) {
				t.Fatalf("log %s lacks %s", line, want)
			}
		}
		for _, balance := range []string{"95.00", "100.00", "-5.00"} {
			if strings.Contains(line, balance) {
				t.Fatalf("log %s leaks the balance %s", line, balance)
			}
		}
		if metrics.runs.Load() != 1 {
			t.Fatalf("runs = %d, want 1", metrics.runs.Load())
		}
```

E no subteste "consistent wallet" acrescente, depois da checagem existente: `if metrics.runs.Load() != 1 { t.Fatalf("runs = %d, want 1", metrics.runs.Load()) }`.

Em `resolve_references_integration_test.go`, no fim do arquivo, acrescente o teste do canal `worker`:

```go
// Covers: OBS-03 (U23)
// Sensitivity: not counting the worker's terminal outcomes → "concluded" stays empty; counting a rescheduled attempt → it gets an extra entry.
func TestResolveReferencesMetrics(t *testing.T) {
	t.Parallel()
	m := &recordingMetrics{}
	f := newFixture()
	f.pw.WithMetrics(m)
	w, f2, p := openWallet(t, "100.00"), f, newProvider()

	pending := process(t, f2.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
	f2.clock.Advance(time.Minute)
	wantResolved(t, resolve(t, f2, pending), app.ResolveRescheduled, "") // still no BET

	process(t, f2.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
	f2.clock.Advance(time.Hour)
	wantResolved(t, resolve(t, f2, pending), app.ResolveProcessed, "")

	concluded, _, _ := m.snapshot()
	wantList(t, "concluded", concluded, "http/REFUND/pending_reference/", "http/BET/processed/", "worker/REFUND/processed/")
}
```

(`wantResolved`'s signature está no arquivo: `(t, got, outcome, code)`. `f.pw.WithMetrics` muta o mesmo `*ProcessWager` que o `f.rr` usa, então o `ResolveReferences` enxerga as métricas.)

- [ ] **Passo 3: ver falhar**

Run: `make infra-up && go test -tags=integration ./internal/app -run 'TestProcessWagerMetrics|TestWagerConcludedLog|TestResolveReferencesMetrics|TestReconcile' -v`
Expected: FAIL por asserção — `concluded = [], want [http/BET/processed/ …]`; `log … lacks "msg":"wager concluded"`; `TestReconcile`: `log … leaks the balance` e `runs = 0, want 1`.

- [ ] **Passo 4: implementar o `ProcessWager`** (`process_wager.go`)

Troque o `Execute` e acrescente `observe`:

```go
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	start := p.clock.Now()
	var err error
	for range maxAttempts {
		var res ProcessResult
		if res, err = p.attempt(ctx, req); !isRace(err) {
			p.observe(ctx, req, res, err, p.clock.Now().Sub(start))
			return res, err
		}
		p.metrics.Conflict(ConflictUniqueRace)
	}
	p.observe(ctx, req, ProcessResult{}, err, p.clock.Now().Sub(start))
	return ProcessResult{}, err
}

// observe reports one call: a lock conflict, or the conclusion. A replay is a
// duplicate, not a new conclusion; over SQS the consumer counts its own
// duplicates (inbox and idempotency), so only HTTP counts here (spec M7, decision 4).
func (p *ProcessWager) observe(ctx context.Context, req ProcessRequest, res ProcessResult, err error, d time.Duration) {
	if errors.Is(err, ErrLockTimeout) {
		p.metrics.Conflict(ConflictLockTimeout)
	}
	if err != nil || res.Tx == nil {
		return
	}
	tx, channel := res.Tx, strings.ToLower(string(req.Via))
	outcome := strings.ToLower(string(tx.Status()))
	if res.Replay {
		if req.Via == wagering.ReceivedViaHTTP {
			p.metrics.WagerDuplicate(channel, "idempotency")
		}
	} else {
		p.metrics.WagerConcluded(channel, string(tx.Kind()), outcome, string(tx.FailureCode()), d)
	}
	messageID := ""
	if req.Inbox != nil {
		messageID = req.Inbox.MessageID
	}
	p.log.InfoContext(ctx, "wager concluded",
		"transactionId", tx.ID(), "walletId", tx.WalletID(), "providerId", tx.ProviderID(),
		"correlationId", req.CorrelationID, "messageId", messageID, "channel", channel,
		"kind", string(tx.Kind()), "outcome", outcome, "failureCode", string(tx.FailureCode()), "replay", res.Replay)
}
```

Acrescente `"strings"` aos imports. O `messageId` sai como `""` no HTTP; o I14 aceita isso (a chave existe "quando disponível" — se preferir omitir, troque por um `attrs` montado com `append` só quando `messageID != ""`; faça isso, é 4 linhas: monte `attrs := []any{…}` e `if messageID != "" { attrs = append(attrs, "messageId", messageID) }`, e chame `p.log.InfoContext(ctx, "wager concluded", attrs...)`).

- [ ] **Passo 5: implementar o worker** (`resolve_references.go`)

Renomeie o `Resolve` atual para `resolve` (minúsculo, mesmo corpo) e crie o novo:

```go
// Resolve evaluates one pending operation (see resolve) and reports it: a
// terminal outcome is a conclusion on the "worker" channel, a rescheduled or
// skipped attempt is not (spec M7, decision 5).
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error) {
	start := r.p.clock.Now()
	res, err := r.resolve(ctx, ref)
	if errors.Is(err, ErrLockTimeout) {
		r.p.metrics.Conflict(ConflictLockTimeout)
	}
	if err != nil {
		return res, err
	}
	switch res.Outcome {
	case ResolveProcessed, ResolveRejected, ResolveFailed:
		r.p.metrics.WagerConcluded("worker", string(res.Kind), strings.ToLower(string(res.Outcome)),
			string(res.FailureCode), r.p.clock.Now().Sub(start))
	case ResolveSkipped, ResolveRescheduled:
	}
	return res, nil
}
```

Em `resultOf`, preencha `Kind: tx.Kind()` no literal inicial; em `recordFailure`, no `ResolveResult{Outcome: ResolveFailed, …}` acrescente `Kind: failed.Kind()`. Acrescente `"strings"` aos imports. A linha `case ResolveSkipped, ResolveRescheduled:` existe por causa do linter `exhaustive`.

- [ ] **Passo 6: implementar a reconciliação** (`reconcile.go`, no fim de `Execute`)

```go
	r.metrics.Reconciled(out.Consistent)
	if !out.Consistent {
		r.log.WarnContext(ctx, "reconciliation divergence",
			"walletId", out.WalletID, "correlationId", correlationID, "entries", out.CheckedEntries)
		r.metrics.ReconciliationDivergence()
	}
	return out, nil
```

- [ ] **Passo 7: ver passar**

Run: `go test -tags=integration ./internal/app -v -race` e `make check`
Expected: PASS em todo o pacote `app` (os testes antigos de `ProcessWager` seguem verdes: a nop é o padrão).

- [ ] **Passo 8: sensibilidade.** (a) Em `observe`, conte o replay com `WagerConcluded` → `TestProcessWagerMetrics` falha; (b) tire o `Reconciled` do `Reconcile` → `TestReconcile` falha em `runs`; (c) faça o worker contar `ResolveRescheduled` → `TestResolveReferencesMetrics` falha. Desfaça cada uma.

---

### Tarefa 5: Métricas e logs no `httpapi`

**Arquivos:**
- Modificar: `internal/adapters/httpapi/handler.go` (porta e `Options`), `middleware.go`, `routes.go`, `wagering_handler.go`, `module.go`, `stubs_test.go`, `edge_test.go`

**Interfaces:**
- Produz: `httpapi.Metrics` (`HTTPRequest(route, method string, status int, d time.Duration)`, `AuthFailure(reason string)`); `httpapi.Options.Metrics Metrics` (nil = descarta).

- [ ] **Passo 1: porta e stubs** (`handler.go`, junto às outras portas; e o campo em `Options`)

```go
// Metrics is what the edge reports (*observability.Metrics).
type Metrics interface {
	HTTPRequest(route, method string, status int, d time.Duration)
	AuthFailure(reason string)
}

type nopMetrics struct{}

func (nopMetrics) HTTPRequest(string, string, int, time.Duration) {}
func (nopMetrics) AuthFailure(string)                             {}
```

Localize o `type Options struct` (`grep -n "type Options" internal/adapters/httpapi/*.go`) e acrescente `Metrics Metrics`. Acrescente `"time"` aos imports de `handler.go`.

- [ ] **Passo 2: testes que falham** (`stubs_test.go`: helper; `edge_test.go`: testes)

Em `stubs_test.go`, troque `newEdge` para delegar:

```go
func newEdge(t *testing.T, s httpapi.Services, docs bool) edge { return newEdgeWith(t, s, docs, nil) }

// newEdgeWith is newEdge with the metrics the edge reports to.
func newEdgeWith(t *testing.T, s httpapi.Services, docs bool, m httpapi.Metrics) edge {
	t.Helper()
	logs := &syncBuffer{}
	if s.Auth == nil {
		s.Auth = stubAuth{}
	}
	if s.Health == nil {
		s.Health = observability.NewHealth(slog.New(slog.DiscardHandler), nil, time.Second)
	}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return edge{handler: httpapi.New(httpapi.Options{DocsEnabled: docs, Log: log, Metrics: m}, s), logs: logs}
}

// recordedMetrics keeps what the edge reports.
type recordedMetrics struct {
	mu       sync.Mutex
	requests []string // route|method|status
	failures []string
}

func (m *recordedMetrics) HTTPRequest(route, method string, status int, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, fmt.Sprintf("%s|%s|%d", route, method, status))
}

func (m *recordedMetrics) AuthFailure(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures = append(m.failures, reason)
}
```

(Reaproveite o corpo original do `newEdge` — o trecho acima já o contém; acrescente `"fmt"` aos imports.) Em `edge_test.go`:

```go
// Covers: OBS-03 (U20)
// Sensitivity: labeling with r.URL.Path instead of the route pattern → the "/wallets/w-1" label appears and the pattern assertion fails.
func TestEdgeRequestMetrics(t *testing.T) {
	m := &recordedMetrics{}
	e := newEdgeWith(t, httpapi.Services{}, false, m)

	e.do(t, call{method: http.MethodGet, path: "/health/live"})
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})            // no token: 401
	e.do(t, call{method: http.MethodGet, path: "/nope/12345/whatever"})    // unmatched
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-2", token: tokenNoRole})

	m.mu.Lock()
	defer m.mu.Unlock()
	want := []string{
		"GET /health/live|GET|200",
		"GET /wallets/{walletId}|GET|401",
		"unmatched|GET|404",
		"GET /wallets/{walletId}|GET|403",
	}
	if strings.Join(m.requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", m.requests, want)
	}
}

// Covers: AUTH-02, AUTH-05, OBS-03 (U20)
func TestEdgeAuthFailureMetrics(t *testing.T) {
	m := &recordedMetrics{}
	e := newEdgeWith(t, httpapi.Services{}, false, m)

	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})                       // no token
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: "forged"})       // invalid token
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenNoRole})    // no role
	e.do(t, call{method: http.MethodGet, path: "/providers/provider-b/wagering/transactions/x", token: tokenProviderA})

	m.mu.Lock()
	defer m.mu.Unlock()
	want := []string{"unauthenticated", "unauthenticated", "forbidden", "provider_mismatch"}
	if strings.Join(m.failures, ",") != strings.Join(want, ",") {
		t.Fatalf("failures = %v, want %v", m.failures, want)
	}
}
```

E em `TestEdgeAccessLog`, na lista de `want` do log (linha ~128), não mude nada; acrescente um teste para o `route` de rota inexistente:

```go
// Covers: OBS-01 (closes the M3 minor: an empty route in the access log)
func TestEdgeAccessLogUnmatchedRoute(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	e.do(t, call{method: http.MethodGet, path: "/nope"})
	if line := e.logs.String(); !strings.Contains(line, `"route":"unmatched"`) {
		t.Fatalf("access log = %s, want route unmatched", line)
	}
}
```

- [ ] **Passo 3: ver falhar**

Run: `go test ./internal/adapters/httpapi -run 'TestEdge(RequestMetrics|AuthFailureMetrics|AccessLogUnmatchedRoute)' -v`
Expected: FAIL por asserção — `requests = [], want […]`; `failures = []`; `access log … want route unmatched`.

- [ ] **Passo 4: implementar**

`middleware.go` — `logAccess` recebe as métricas, usa `unmatched` e reporta:

```go
// unmatchedRoute labels what no route pattern matched, keeping the metric's
// cardinality fixed (spec M7, decision 8).
const unmatchedRoute = "unmatched"

// logAccess writes one line per request with a fixed set of fields: never a
// header or a body (OBS-02), and reports the request to the metrics.
func logAccess(log *slog.Logger, m Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		rec := &statusRecorder{ResponseWriter: w}
		ctx := context.WithValue(r.Context(), requestInfoKey, info)
		next.ServeHTTP(rec, r.WithContext(ctx))
		route := info.route
		if route == "" {
			route = unmatchedRoute
		}
		elapsed := time.Since(start)
		m.HTTPRequest(route, r.Method, rec.status, elapsed)
		log.InfoContext(ctx, "http request",
			"method", r.Method, "route", route, "status", rec.status,
			"durationMs", elapsed.Milliseconds(),
			"correlationId", correlationID(ctx), "providerId", info.providerID)
	})
}
```

`authenticate` recebe `m Metrics` e conta:

```go
func authenticate(a Authenticator, log *slog.Logger, m Metrics, roles []auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		raw, ok := bearerToken(r)
		if !ok {
			m.AuthFailure("unauthenticated")
			unauthenticated(w, r, false)
			return
		}
		p, err := a.Authenticate(ctx, raw)
		if err != nil {
			m.AuthFailure("unauthenticated")
			log.DebugContext(ctx, "bearer token rejected", "correlationId", correlationID(ctx), "reason", err.Error())
			unauthenticated(w, r, true)
			return
		}
		infoOf(ctx).providerID = p.ProviderID
		if !auth.HasAnyRole(p, roles...) {
			m.AuthFailure("forbidden")
			writeProblem(w, r, codeForbidden, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
	})
}
```

`routes.go` — em `New`, no início: `if opts.Metrics == nil { opts.Metrics = nopMetrics{} }`; troque a chamada `authenticate(s.Auth, opts.Log, rt.roles, h)` por `authenticate(s.Auth, opts.Log, opts.Metrics, rt.roles, h)` e `logAccess(opts.Log, recoverPanic(…))` por `logAccess(opts.Log, opts.Metrics, recoverPanic(opts.Log, routeFallback(mux)))`; em `routes(opts, s)`, `h := handlers{s: s, log: opts.Log, metrics: opts.Metrics}` (o `Routes()` passa `Options{DocsEnabled}` sem métricas: o campo fica nil, sem uso). No `handlers` (arquivo `handler.go`) acrescente o campo `metrics Metrics`.

`wagering_handler.go` — nos dois pontos do `codeProviderMismatch` (linhas ~51 e ~89), antes do `writeProblem`, acrescente `h.metrics.AuthFailure("provider_mismatch")`. Como `handlers` pode ser construído sem métricas em testes de handler diretos, proteja: adicione ao `handlers` um método

```go
func (h handlers) providerMismatch(w http.ResponseWriter, r *http.Request) {
	if h.metrics != nil {
		h.metrics.AuthFailure("provider_mismatch")
	}
	writeProblem(w, r, codeProviderMismatch, "")
}
```

e troque os dois `writeProblem(w, r, codeProviderMismatch, "")` por `h.providerMismatch(w, r)`.

`module.go` — `handlerParams` ganha `Metrics *observability.Metrics`, e `newHandler` passa `Options{…, Metrics: p.Metrics}`.

- [ ] **Passo 5: ver passar**

Run: `go test -race ./internal/adapters/httpapi -v` e `make check`
Expected: PASS em todo o pacote (os testes de handler existentes seguem com métricas nil/nop).

- [ ] **Passo 6: sensibilidade.** (a) Use `r.URL.Path` como label em `logAccess` → `TestEdgeRequestMetrics` falha; (b) remova o `m.AuthFailure("forbidden")` → `TestEdgeAuthFailureMetrics` falha. Desfaça.

---

### Tarefa 6: Ligação no `bootstrap` e provas de Fx (I07b ordem, I07c, I27)

**Arquivos:**
- Modificar: `internal/bootstrap/app_module.go`, `internal/bootstrap/bootstrap_integration_test.go`

**Interfaces:**
- Consome: `(*ProcessWager).WithMetrics`, `app.Metrics` (Tarefas 3–4), `httpapi.Options.Metrics` (Tarefa 5, já ligado em `module.go`).

- [ ] **Passo 1: testes que falham** (`bootstrap_integration_test.go`)

Adicione o helper e os testes (imports novos: `"bytes"`, `"log/slog"`, `"strings"` se faltar, e `"github.com/KaioVinicios/pda/internal/observability"`; a `syncBuffer` do pacote de teste pode não existir aqui — use este tipo local):

```go
// safeBuffer collects log lines written from many goroutines.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs replaces the application logger by a JSON logger on buf.
func captureLogs(buf *safeBuffer) fx.Option {
	return fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(buf, "info") })
}

// lineIndex is the position of the first log line that contains every part, or -1.
func lineIndex(logs string, parts ...string) int {
	i := 0
	for line := range strings.Lines(logs) {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return i
		}
		i++
	}
	return -1
}
```

No `TestFxLifecycle`, troque a criação do app e acrescente a checagem da ordem depois do `RequireStop` (o resto do teste fica):

```go
	var logs safeBuffer
	var pool *pgxpool.Pool
	app := fxtest.New(t, append(bootstrap.Options(), fx.Replace(cfg), captureLogs(&logs), fx.Populate(&pool))...)
```

```go
	// FX-04, FX-05: the HTTP server stops first, then the consumer, the
	// publisher and the worker, and the pool closes last (D-15).
	order := []int{
		lineIndex(logs.String(), `"msg":"http server stopped"`, `"server":"api"`),
		lineIndex(logs.String(), `"msg":"sqs consumer stopped"`),
		lineIndex(logs.String(), `"msg":"outbox publisher stopped"`),
		lineIndex(logs.String(), `"msg":"reference worker stopped"`),
		lineIndex(logs.String(), `"msg":"postgres pool closed"`),
	}
	for i, at := range order {
		if at < 0 || (i > 0 && at <= order[i-1]) {
			t.Fatalf("stop order = %v (api, consumer, publisher, worker, pool); want every line present and increasing\n%s", order, logs.String())
		}
	}
```

Acrescente a `TestFxFailFast` o caso da variável de papel inválida (dentro da função, depois do subteste "invalid configuration"):

```go
	t.Run("invalid role variable", func(t *testing.T) {
		t.Setenv("HTTP_ENABLED", "talvez-42")
		app := fx.New(append(bootstrap.Options(), fx.NopLogger)...)
		err := app.Err()
		if err == nil || !strings.Contains(err.Error(), "HTTP_ENABLED") || strings.Contains(err.Error(), "talvez-42") {
			t.Fatalf("fx.New().Err() = %v, want an error naming HTTP_ENABLED without its value", err)
		}
	})
```

E o teste dos papéis (I27):

```go
// Covers: FX-01, HTTP-08, OBS-04 (I27; I13 with the roles)
// Sensitivity: making /health/ready check the SQS only when the consumer is on → the "consumer off" case reports no "sqs" check.
func TestFxRoles(t *testing.T) {
	cases := []struct {
		name string
		off  func(*config.Roles)
	}{
		{"consumer off", func(r *config.Roles) { r.Consumer = false }},
		{"outbox publisher off", func(r *config.Roles) { r.OutboxPublisher = false }},
		{"reference worker off", func(r *config.Roles) { r.ReferenceWorker = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := integrationConfig(t)
			roles := config.Roles{HTTP: true, Consumer: true, OutboxPublisher: true, ReferenceWorker: true}
			tc.off(&roles)
			app := fxtest.New(t, append(bootstrap.OptionsFor(roles), fx.Replace(cfg))...)
			app.RequireStart()
			defer app.RequireStop()

			client := &http.Client{Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			code, body := getJSON(t, client, "http://"+cfg.HTTPAddr+"/health/ready")
			checks, _ := body["checks"].(map[string]any)
			if code != http.StatusOK || checks["postgres"] != "UP" || checks["sqs"] != "UP" {
				t.Fatalf("GET /health/ready = %d %v, want 200 with postgres and sqs UP whatever the roles", code, body)
			}
		})
	}

	t.Run("http off leaves only the admin server", func(t *testing.T) {
		cfg := integrationConfig(t)
		app := fxtest.New(t, append(bootstrap.OptionsFor(config.Roles{Consumer: true, OutboxPublisher: true, ReferenceWorker: true}), fx.Replace(cfg))...)
		app.RequireStart()
		defer app.RequireStop()

		client := &http.Client{Timeout: 5 * time.Second}
		defer client.CloseIdleConnections()
		if code, _ := getJSON(t, client, "http://"+cfg.MetricsAddr+"/metrics"); code != http.StatusOK {
			t.Fatalf("GET /metrics = %d, want 200", code)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+cfg.HTTPAddr+"/health/live", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			t.Fatal("the API answered with HTTP_ENABLED off; want the connection refused")
		}
	})
}
```

Acrescente `"sync"` aos imports se necessário.

- [ ] **Passo 2: ver falhar**

Run: `make infra-up && go test -tags=integration ./internal/bootstrap -run 'TestFxLifecycle|TestFxFailFast|TestFxRoles' -v`
Expected: `TestFxRoles` já passa (a Tarefa 1 entregou os papéis) — isso é esperado, o teste é sobre comportamento que já existe e leva sensibilidade; `TestFxLifecycle` **passa ou falha** conforme o texto dos logs: se algum `msg` de parada estiver diferente do citado (`"sqs consumer stopped"`, `"outbox publisher stopped"`, `"reference worker stopped"`, `"postgres pool closed"`, `"http server stopped"` com `"server":"api"`), a falha mostra os logs — ajuste a **string do teste** ao texto real desses logs, sem mexer no código. `TestFxFailFast/invalid role variable` deve passar.

- [ ] **Passo 3: sensibilidade** (o red dos testes sobre comportamento existente): (a) em `OptionsFor`, inverta a ordem de `outbox.Module` e `sqsconsumer.Module` → `TestFxLifecycle` deve falhar em `stop order`; (b) faça o `/health/ready` deixar de checar o SQS → `TestFxRoles` falha; (c) remova a chamada de `RolesFromEnv` em `Options` (use `OptionsFor(config.Roles{HTTP: true, …})`) → o caso `invalid role variable` falha. Desfaça cada uma e registre `// Sensitivity: …` nos comentários dos testes.

- [ ] **Passo 4: ligar as métricas ao `ProcessWager`** (`app_module.go`)

Troque `app.NewProcessWager,` na lista do `fx.Provide` por:

```go
		newProcessWager,
```

e acrescente ao arquivo:

```go
// newProcessWager builds the use case with the process metrics; app.NewProcessWager
// stays free of them so that its callers in tests need no metrics.
func newProcessWager(uow app.UnitOfWork, reads app.Repos, clock app.Clock, ids app.IDGenerator,
	policy wagering.ReferenceRetryPolicy, log *slog.Logger, m app.Metrics,
) *app.ProcessWager {
	return app.NewProcessWager(uow, reads, clock, ids, policy, log).WithMetrics(m)
}
```

Acrescente `"log/slog"` aos imports.

- [ ] **Passo 5: ver passar**

Run: `go test -tags=integration -race ./internal/bootstrap -v` e `go test ./internal/bootstrap -run TestFxGraph -v` e `make check`
Expected: PASS; `TestFxGraph` continua validando o grafo completo.

---

### Tarefa 7: Provas de ponta a ponta (I14, I25, I26)

**Arquivos:**
- Criar: `test/integration/observability_test.go`
- Modificar: `test/testkit/app.go` (helper `MetricValue`)

**Interfaces:**
- Produz: `(*testkit.App).MetricValue(tb, sample string) float64` — o valor de uma amostra da `/metrics` pelo nome exato com labels (`0` se ainda não existe).
- Consome: `server` (o `*testkit.App` compartilhado do pacote), `wager`, `submit`, `result`, `unique`, `sqsWager`, `server.SendWager`, `server.OpenWallet`, `server.Client`, `server.Logs()`.

- [ ] **Passo 1: helper** (`test/testkit/app.go`, depois do `Metric`)

```go
// MetricValue returns the value of the sample named exactly as exposed,
// labels included (`http_requests_total{method="GET",route="…",status="200"}`),
// or 0 while the series does not exist yet.
func (a *App) MetricValue(tb testing.TB, sample string) float64 {
	tb.Helper()
	raw := a.Metric(tb, sample)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		tb.Fatalf("metric %s = %q: %v", sample, raw, err)
	}
	return v
}
```

Acrescente `"strconv"` aos imports. (`forbidigo` proíbe `ParseFloat`; o `.golangci.yml` isenta `test/testkit`? Se o lint reclamar, acrescente a exceção de path `test/testkit/` na mesma seção do `test/load/` com o mesmo motivo — valores de métrica não são dinheiro — e diga isso ao autor.)

- [ ] **Passo 2: escrever os testes** (`test/integration/observability_test.go`)

```go
//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// logLines returns the JSON log lines of the app that mention marker, decoded.
func logLines(t *testing.T, marker string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.Lines(server.Logs()) {
		if !strings.Contains(line, marker) {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// Covers: OBS-01, OBS-02 (I14)
// Sensitivity: logging the Authorization header in logAccess → the token check fails; logging money.Amount in "wager concluded" → the amount check fails.
func TestLogsHaveIdsWithoutSecrets(t *testing.T) {
	t.Parallel()
	const amount = "4242.42"
	token := testkit.Token(t, "provider-a")
	a := server.ClientWithToken(token)
	w := server.OpenWallet(t, testkit.BRL("9000.00"))
	provider, ext := "provider-a", unique("bet")
	key := provider + ":" + ext
	corr := "corr-" + testkit.NewID()

	// HTTP: a BET, its replay, a conflict (same key, other amount), a rejection.
	bet := wager(w, provider, "BET", amount, ext, "")
	send := func(c *testkit.Client, body testkit.Wager, k string) *testkit.Response {
		return c.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
			Header: http.Header{"Idempotency-Key": {k}, "X-Correlation-Id": {corr}}})
	}
	if r := send(a, bet, key); r.Status != http.StatusOK {
		t.Fatalf("BET = %d %s", r.Status, r.Body)
	}
	if r := send(a, bet, key); r.Status != http.StatusOK {
		t.Fatalf("replay = %d %s", r.Status, r.Body)
	}
	other := bet
	other.Money = testkit.BRL("1.11")
	if r := send(a, other, key); r.Status != http.StatusConflict {
		t.Fatalf("same key, other content = %d %s, want 409", r.Status, r.Body)
	}
	if r := send(a, wager(w, provider, "BET", "99999.99", unique("bet"), ""), unique("key")); r.Status != http.StatusUnprocessableEntity {
		t.Fatalf("BET without funds = %d %s, want 422", r.Status, r.Body)
	}
	// Refused accesses: no token, and a role that does not fit.
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: bet,
		Header: http.Header{"Idempotency-Key": {key}}})
	// SQS: another BET, with the envelope messageId.
	msgID, sqsExt := unique("msg"), unique("sqsbet")
	server.SendWager(t, sqsWager(t, msgID, wager(w, provider, "BET", "12.34", sqsExt, "")), testkit.SendOpts{GroupID: w.ID, CorrelationID: corr})
	testkit.Eventually(t, 15*time.Second, "the SQS BET is concluded", func(context.Context) (bool, error) {
		return len(logLines(t, msgID)) > 0, nil
	})
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})

	logs := server.Logs()
	for _, secret := range []string{token, "Bearer ", amount, "12.34", "9000.00", key, "secret", "password"} {
		for line := range strings.Lines(logs) {
			if strings.Contains(line, w.ID) && strings.Contains(line, secret) {
				t.Fatalf("a log line about the wallet leaks %q: %s", secret, line)
			}
		}
	}
	// Every line is JSON; the conclusion lines carry the ids.
	for line := range strings.Lines(logs) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
	}
	concluded := logLines(t, `"msg":"wager concluded"`)
	var http200, viaSQS map[string]any
	for _, m := range concluded {
		if m["walletId"] == w.ID && m["channel"] == "http" && m["replay"] == false && m["outcome"] == "processed" {
			http200 = m
		}
		if m["walletId"] == w.ID && m["channel"] == "sqs" {
			viaSQS = m
		}
	}
	for name, m := range map[string]map[string]any{"http": http200, "sqs": viaSQS} {
		if m == nil {
			t.Fatalf("no %q conclusion line for wallet %s", name, w.ID)
		}
		for _, k := range []string{"transactionId", "walletId", "providerId", "correlationId"} {
			if s, _ := m[k].(string); s == "" {
				t.Fatalf("%s conclusion line lacks %s: %v", name, k, m)
			}
		}
	}
	if http200["correlationId"] != corr || viaSQS["messageId"] != msgID {
		t.Fatalf("correlation/message ids = %v / %v, want %s / %s", http200["correlationId"], viaSQS["messageId"], corr, msgID)
	}
	if lines := logLines(t, `"route":"POST /wagering/transactions"`); len(lines) == 0 {
		t.Fatal("no access log line names the route pattern")
	}
}

// Covers: OBS-03, HTTP-07, AUTH-02 (I25)
// Sensitivity: labeling the route with the raw path → the "{walletId}" series never grows and the id series does; not counting refused tokens → auth_failures stays flat.
func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	series := map[string]string{
		"processed": `wager_transactions_total{channel="http",failure_code="",kind="BET",outcome="processed"}`,
		"rejected":  `wager_transactions_total{channel="http",failure_code="INSUFFICIENT_FUNDS",kind="BET",outcome="rejected"}`,
		"dup":       `wager_duplicates_total{channel="http",layer="idempotency"}`,
		"unauth":    `auth_failures_total{reason="unauthenticated"}`,
		"forbidden": `auth_failures_total{reason="forbidden"}`,
		"recon":     `reconciliation_runs_total{consistent="true"}`,
		"route":     `http_requests_total{method="GET",route="GET /wallets/{walletId}",status="200"}`,
		"unmatched": `http_requests_total{method="GET",route="unmatched",status="404"}`,
	}
	before := map[string]float64{}
	for name, sample := range series {
		before[name] = server.MetricValue(t, sample)
	}

	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "80.00", unique("bet"), "")
	result(t, a, bet, http.StatusOK)
	result(t, a, bet, http.StatusOK) // replay
	result(t, a, wager(w, "provider-a", "BET", "80.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	internal := server.Client(t, "wallet-service")
	internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID}) // provider on an internal route: 403
	server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/no/such/" + testkit.NewID()})

	for name, minGrowth := range map[string]float64{
		"processed": 1, "rejected": 1, "dup": 1, "unauth": 1, "forbidden": 1, "recon": 1, "route": 1, "unmatched": 1,
	} {
		if got := server.MetricValue(t, series[name]) - before[name]; got < minGrowth {
			t.Fatalf("%s grew by %v, want at least %v", series[name], got, minGrowth)
		}
	}
	// The admin port serves /metrics; the API port does not.
	resp := server.Client(t, "").Do(t, testkit.Request{Method: http.MethodGet, Path: "/metrics"})
	if resp.Status != http.StatusNotFound {
		t.Fatalf("GET /metrics on the API port = %d, want 404", resp.Status)
	}
}

// Covers: OBS-03, CONC-01 (I26)
// Sensitivity: dropping the ErrLockTimeout wrap in postgres.translate → the counter stays flat.
func TestConcurrencyConflictMetric(t *testing.T) {
	t.Parallel()
	const sample = `concurrency_conflicts_total{reason="lock_timeout"}`
	before := server.MetricValue(t, sample)
	w := server.OpenWallet(t, testkit.BRL("100.00"))

	// Hold the wallet row so the BET waits for the lock until lock_timeout.
	tx, err := server.Owner().Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, w.ID); err != nil {
		t.Fatalf("lock: %v", err)
	}

	a := server.Client(t, "provider-a")
	resp := submit(t, a, wager(w, "provider-a", "BET", "10.00", unique("bet"), ""))
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("BET on a locked wallet = %d %s, want 503", resp.Status, resp.Body)
	}
	if got := server.MetricValue(t, sample) - before; got < 1 {
		t.Fatalf("%s grew by %v, want at least 1", sample, got)
	}
}
```

Ajustes de import: acrescente `"context"`. `testkit.Eventually` tem a assinatura `(tb, timeout, what, cond func(ctx context.Context) (bool, error))`.

- [ ] **Passo 3: ver falhar**

Run: `make infra-up && go test -tags=integration ./test/integration -run 'TestLogsHaveIdsWithoutSecrets|TestMetricsEndpoint|TestConcurrencyConflictMetric' -v`
Expected: as Tarefas 2–6 já entregaram o comportamento, então estes testes **podem passar de primeira** — são testes sobre comportamento existente. Se passarem, a checagem de sensibilidade é obrigatória (Passo 4). Se algum falhar, leia a asserção: `no "sqs" conclusion line` indica que o `messageId` não chegou; `grew by 0` indica métrica não ligada (volte à Tarefa 6 Passo 4).

- [ ] **Passo 4: sensibilidade** (uma por vez, ver falhar, desfazer): (a) em `logAccess`, acrescente `"authorization", r.Header.Get("Authorization")` ao log → I14 falha em `leaks`; (b) em `observe`, acrescente `"amount", …` ao log → I14 falha; (c) em `postgres.translate`, remova o wrap `ErrLockTimeout` → I26 falha; (d) em `logAccess`, use `r.URL.Path` como `route` da métrica → I25 falha em `route`. Registre-as nos comentários `// Sensitivity:` (já esboçados).

- [ ] **Passo 5: ver passar**

Run: `go test -tags=integration -race ./test/integration -v` e `make check`
Expected: PASS em todo o pacote de integração; `make check` verde.

---

### Tarefa 8: Verificação completa, docs e fecho do marco

**Arquivos:**
- Modificar: `docs/decisions.md` (D-15, D-18), `ARCHITECTURE.md` (§13.1–§13.3, §15 item 12, §17 se citar as flags), `docs/messaging.md` §8, `docs/structure.md` §3, `docs/test-plan.md`, `docs/delivery-requirements.md`, `docs/implementation-plan.md` (M7 ✅), `docs/dev/diary.md`, a spec do M7 (status e os 5 ajustes deste plano)

- [ ] **Passo 1: prova de código**

Run: `make check`, depois `make test-integration`, três vezes seguidas (o repositório exige a estabilidade do marco; sem `-count` mágico: repita o comando).
Expected: `0 issues.` e todos os pacotes `ok`. Cole as saídas na mensagem final.

- [ ] **Passo 2: prova no compose**

Run: `docker compose up --build --wait`, depois um fluxo com `scripts/get-token.sh` (abrir carteira → BET → replay → reconciliação → um `curl` sem token) e `curl -s localhost:9091/metrics | grep -E '^(wager_transactions_total|http_requests_total|auth_failures_total|reconciliation_runs_total|concurrency_conflicts_total)'` e `docker compose logs app-1 | grep '"wager concluded"'`.
Expected: as séries com valores coerentes com o fluxo; nenhuma linha de log com o token, o `amount` ou a chave.

- [ ] **Passo 3: `decisions.md`**

- D-15: troque "Entrega das flags de papel: … Até lá, todos os papéis rodam em todas as instâncias" por: as flags foram entregues no M7 em `bootstrap.OptionsFor(config.Roles)`; `Options()` lê as quatro variáveis antes do Fx; papéis desligados saem do grafo; o admin e a `observability` ficam sempre; todos desligados é válido (só o admin sobe); com `HTTP_ENABLED=false` não há rotas de health.
- D-18: acrescente as decisões da spec §2 (1, 4–9, 12–14) e as correções deste plano: `version_mismatch` removida; `/health/ready` sempre com PostgreSQL + SQS; uma linha `wager concluded` por conclusão; a reconciliação não registra saldos; `route` é o padrão do mux ou `unmatched`; método `WagerDuplicate`.

- [ ] **Passo 4: `ARCHITECTURE.md`**

§13.1: chaves de log e a linha de conclusão. §13.2: tabela final (sem `version_mismatch`; `wager_transactions_total` com `channel` = `http`/`sqs`/`worker`; `reconciliation_runs_total`, `auth_failures_total`, `http_*` com o `route` do padrão) e troque a frase "As demais chegam no M7" por "O catálogo está completo desde o M7". §13.3: uma linha sobre `HTTP_ENABLED=false`. §15 item 12: passa a dizer que as flags existem. Confira a §17 (trabalho não concluído) por menções ao M7.

- [ ] **Passo 5: demais documentos**

- `messaging.md` §8: as métricas novas que o consumidor toca (`wager_duplicates_total` do HTTP vem do `app`).
- `structure.md` §3: `OptionsFor`/`Options`, papéis; tire "papéis habilitáveis" do futuro.
- `test-plan.md`: linhas U20–U23, I25–I27; ajuste I07b (ordem de parada), I07c (papel inválido), I13 (o 503 real é o R01) e I14 (o fluxo e os marcadores).
- `delivery-requirements.md`: OBS-01 `[x]`, OBS-02 `[x]`, OBS-03 `[x]`, FX-01 `[x]`, FX-03 `[x]`, FX-04 `[~]` (completa no R03/R04, M9), FX-05 `[x]`, TST-I07 `[x]`, cada um citando os testes (`TestMetrics_*`, `TestProcessWagerMetrics`, `TestWagerConcludedLog`, `TestEdge*Metrics`, `TestOptionsFor`, `TestRolesFromEnv`, `TestFxLifecycle`, `TestFxRoles`, `TestLogsHaveIdsWithoutSecrets`, `TestMetricsEndpoint`, `TestConcurrencyConflictMetric`).
- `implementation-plan.md`: M7 "✅ concluído em 30/09" com o resumo do entregue (formato do M6) e os riscos novos, se houver; `Cobre:` OBS-01..04, FX-01, FX-03..05.
- `diary.md`: entrada "30/09/2026 (qua): M7" no formato do M6 e atualize "Onde paramos" (próximo: M8).
- Spec do M7: troque o status para "aprovada pelo autor" e registre os 5 ajustes do cabeçalho deste plano.

- [ ] **Passo 6: verificação final**

Rode `make check` e `make test-integration` uma última vez depois dos documentos (o `make check` inclui o lint e os `tidy-check`) e reporte as saídas. Proponha os commits atômicos (Conventional Commits, skill `git-commit`) e **espere a autorização do autor**; sem trailer de coautoria de IA.

---

## Autoavaliação do plano

**Cobertura da spec:** decisão 1 → Tarefa 6 (`TestFxRoles`); 2–3 → Tarefa 1; 4–5 → Tarefas 2–4; 6 → Tarefas 3–4 e I26; 7 → Tarefas 2 e 4; 8–9 → Tarefa 5; 10 → nenhum código (o admin só serve `/metrics`; I25 afirma o 404 da API); 11–12 → ajustes 2–3 do cabeçalho e Tarefa 4; 13 → Tarefa 4 (reconcile); 14 → I14 (Tarefa 7); 15 → Tarefa 6. Requisitos → Tarefa 8.

**Tipos consistentes:** `WagerDuplicate` e `Reconciled` (Tarefas 2, 3, 4); `WithMetrics` (Tarefas 4, 6); `httpapi.Metrics` implementada por `*observability.Metrics` (Tarefas 2, 5); `ResolveResult.Kind` (Tarefa 4).

**Riscos de execução:** o passo 2 da Tarefa 6 e o da Tarefa 7 escrevem testes sobre comportamento que já existe; a sensibilidade deles não é opcional. O plano não foi validado numa cópia antes: leia cada red e cada green.
