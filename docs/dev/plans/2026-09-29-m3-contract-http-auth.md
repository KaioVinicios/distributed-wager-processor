# M3 — Contrato, casos de uso, HTTP e autenticação: plano de implementação

> **Execução:** `superpowers:executing-plans`, **inline** na própria sessão, sem subagentes ([`development-workflow.md`](../../development-workflow.md) §7). Cada tarefa segue `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Sem commits:** cada tarefa termina num *checkpoint* verificável, e os commits são propostos no fim do marco (§6 do workflow).

**Objetivo:** o HTTP síncrono de ponta a ponta, protegido pelo Keycloak real:
- os casos de uso `OpenWallet`, `ProcessWager`, `Queries` e `Reconcile` sobre as portas do M2;
- o `auth`, com go-oidc e a matriz D-07;
- o `httpapi`, com as 9 rotas, `problem+json` e os middlewares;
- o contrato `api/openapi.yaml` validado em todo teste de integração;
- a prova, com PostgreSQL, Keycloak e MiniStack reais, dos eliminatórios E1, E2, E5 (HTTP) e E6 (HTTP), com o E4 e o E5 antecipados pela concorrência em processo.

**Arquitetura:**
- **Casos de uso:** um tipo por caso de uso, com o passo compartilhado `settleAndPersist` (reaproveitado pelo worker no M6) e a tradução única `domainError` (spec §2, decisões 1, 2 e 6).
- **O `app` não conhece o `auth`:** a borda autoriza antes de chamar o caso de uso (decisão 3).
- **Borda testável sem infraestrutura:** o `httpapi` depende de interfaces pequenas (`Services`), então os unitários usam stubs. A integração sobe o Fx inteiro em processo, com o Keycloak real (decisão 21).
- **Testes A e I antes dos handlers** (development-workflow §2):
  - a tabela de rotas nasce completa na Tarefa 8, e as rotas de negócio respondem 501 até as Tarefas 11 e 12;
  - os testes de integração dessas tarefas ficam vermelhos pelo validador de contrato e ficam verdes com os handlers.
- **Ordem das tarefas:** config → base do `app` → abertura → processamento → corridas e falhas → consultas e reconciliação → `auth` → borda HTTP → Fx → harness → carteiras → apostas → provas de borda e concorrência → encerramento.

**Stack:**
- Go 1.27.1 e `pgx/v5` (M2);
- `github.com/coreos/go-oidc/v3` v3.21.0;
- `github.com/go-jose/go-jose/v4` v4.1.4, que também assina os tokens forjados do `testkit`;
- `github.com/getkin/kin-openapi` v0.149.0, só nos testes;
- Keycloak 26.7.4, PostgreSQL 18.6 e MiniStack 1.5.18 do compose.

**Spec:** [`docs/dev/specs/2026-09-29-m3-contract-http-auth-design.md`](../specs/2026-09-29-m3-contract-http-auth-design.md). Quem executa lê os dois documentos, além do [`api/openapi.yaml`](../../../api/openapi.yaml), que faz parte da spec.

**Validação prévia do plano:** o código abaixo foi escrito e testado numa cópia descartável do repositório (um `git clone` local, sem worktree nem branch), contra a infraestrutura do compose.
- **Cada tarefa:** o estado "tarefas anteriores + stubs + testes" compila e **falha por asserção**, sem `panic`; com a implementação, fica verde. A Tarefa 13 é de provas sobre o que já existe e usa sabotagens.
- **Estado final:**
  - `make check` verde: `0 issues.`, `gofmt`/`gofumpt`, `go mod tidy -diff`, `go vet` com e sem tags e `go test -race ./...`;
  - `go test -tags=integration -race ./...` verde;
  - três execuções seguidas de `internal/app`, `internal/adapters/postgres` e `test/integration`.
- **Sabotagens:** as seis das Tarefas 5 e 13 foram detectadas.
- **Imagem real:** a imagem Docker compilada da cópia rodou na rede do compose (JWKS por `keycloak:8080`, `iss` em `localhost`), e o fluxo do "pronto quando" por `curl` funcionou: 201, 200, replay 200, 422, reconciliação consistente e 401 sem token.
- **Reaplicação:** o plano foi reaplicado, passo a passo, numa segunda cópia limpa (stubs → red → implementação → green, com os comandos de cada passo), e o estado final ficou idêntico, arquivo por arquivo, ao código validado.

Na execução, o código é redigitado seguindo o ciclo red → green de cada tarefa.

**Achados da validação** (registrados na spec §2.2):
1. **Corrida entre as duas leituras de idempotência** (achado do C01a): 50 envios iguais em paralelo produziam, às vezes, um 409 `EXTERNAL_TRANSACTION_ID_CONFLICT`. Um envio confirmava **entre** a busca pela chave e a busca pelo `externalTransactionId`, e a segunda busca encontrava a transação da mesma chave. O `lookup` passa a tratar essa transação como a da chave (decisão 23), e a Tarefa 5 tem o teste determinístico.
2. **Ordem dos middlewares:** a correlação fica por fora do log de acesso e da recuperação de `panic`, para que o 500 do `panic` tenha `correlationId` e entre no log de acesso (decisão 24).
3. **Onde nascem as filas isoladas:** quem cria é o `StartApp`, não o `NewEnv`, então os pacotes que não sobem o app (`postgres`, `app`) não dependem do MiniStack. O `Eventually` fica para o M4/M6, quando houver trabalho assíncrono. O `SnapshotCounts` é global, e por isso o A03 roda sem `t.Parallel` (decisão 25).
4. **`depguard`:** a regra do `app` passa a excluir os testes (`!$test`, como já faz a do `apperrors`), porque os testes de integração do `app` usam o adapter real (decisão 26).
5. **Detalhes de biblioteca**, que a spec não precisa registrar:
   - o kin-openapi decodifica `application/yaml` como objeto e não tem decoder de `text/html`; o `testkit` registra decoders de texto puro;
   - o router `legacy` devolve um `*routers.RouteError` novo, e não as sentinelas;
   - o `recover` precisa de função adiada nomeada para o `contextcheck`;
   - o `auth` usa um cliente HTTP próprio, fechado no stop, por causa do `goleak` do I07b.

## Restrições globais

- Module path `github.com/KaioVinicios/pda`; `go 1.27.1`. As únicas dependências novas são `go-oidc/v3` v3.21.0, `go-jose/v4` v4.1.4 e `kin-openapi` v0.149.0 (só testes), com os indiretos que o `go mod tidy` trouxer.
- **Camadas:**
  - `internal/app` só importa domínio, `apperrors`, `google/uuid` e stdlib;
  - `internal/auth` não conhece o `app`;
  - `internal/adapters/httpapi` depende de interfaces próprias e das funções puras do `auth`;
  - o `depguard` segue valendo para o código de produção.
- **Erros:**
  - resultado de negócio é `app.ProcessResult`, nunca erro;
  - todo erro que sai do domínio passa por `domainError`;
  - a borda decide o status só pelo `apperrors.Kind` e pelo código (spec §6.3);
  - mensagens de erro e logs nunca carregam token, header, corpo nem valores recebidos.
- **Dinheiro nunca em `float`:** os DTOs de entrada usam `*string`, e os testes decodificam números com `json.Number`.
- **Contrato:** todo teste de integração HTTP passa pelo validador do `testkit`. O `api/openapi.yaml` só muda junto com a spec.
- **Logs:** chaves em `camelCase` e mensagens estáticas (`sloglint`).
- **Testes de integração:**
  - tag `integration` e `make infra-up` no ar (postgres, keycloak, ministack, aws-init, migrate);
  - um banco por pacote;
  - `t.Parallel()` com carteiras próprias e ids únicos;
  - `-race`;
  - `// Covers: <IDs>` em todo teste; só `testing` da stdlib.
- **`make lint` e `make fmt`** usam a imagem `golangci/golangci-lint:v2.14.0`, então o Docker precisa estar rodando.
- **Sem commits, branches ou worktrees.**

## Foco de revisão

Os cinco casos que a spec implica, mas não detalha, com mais chance de causar problema. Cada um tem teste na tarefa indicada:

1. **Duas entregas da mesma operação, com uma confirmando entre as duas leituras de idempotência.** Esperado: replay (200, `idempotentReplay: true`), nunca 409. → Tarefa 5 ("same key committed between the two lookups") e Tarefa 13 (C01a).
2. **`amount` como número JSON** (`"amount": 25.00`). Esperado: 400 `INVALID_AMOUNT` em `money.amount`, sem conversão para `float` e sem o caso de uso rodar. → Tarefas 11 e 12 ("amount as a number").
3. **Token vencido há poucos segundos.** Esperado: aceito dentro da tolerância e 401 fora dela; com tolerância zero, 401 logo após o `exp`. → Tarefa 7 (U16) e Tarefa 13 (A01b "expired").
4. **Consulta por id de uma transação de outro provedor, ou do `OPENING` interno.** Esperado: 404 `TRANSACTION_NOT_FOUND`, sem revelar que o id existe. → Tarefa 12 (U17 e A02a).
5. **Corpo acima de 64 KB, dados depois do JSON e `null` em campo obrigatório.** Esperado: 400 `MALFORMED_REQUEST`, `MALFORMED_REQUEST` e `MISSING_FIELD`, sem o caso de uso rodar. → Tarefa 11 (U17).

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `internal/config/{config,validate}.go` + `config_test.go`; `.env.example` | `OIDC_*`, `API_DOCS_ENABLED`, `REFERENCE_*` | 1 |
| `internal/app/{ports,system,errors,cursor}.go` + testes; `.golangci.yml` | `Clock`, `IDGenerator`, `Metrics`, `domainError` (U13), cursor (U14) | 2 |
| `internal/app/{open_wallet,seal}.go`; `internal/app/*_integration_test.go` | `OpenWallet` (I20) e o ambiente de integração do `app` | 3 |
| `internal/app/process_wager.go` + teste | Fluxo principal do `ProcessWager` (I21) | 4 |
| `internal/app/process_wager.go`; `faults_integration_test.go`, `process_wager_failures_integration_test.go` | Corridas (I22), `FAILED` (I03b) e o achado da validação | 5 |
| `internal/app/{queries,reconcile}.go`; `internal/observability/{metrics,module}.go` + testes | Consultas, reconciliação e `reconciliation_divergences_total` | 6 |
| `internal/auth/*` | `Verifier`, `Principal`, matriz D-07 e fail fast do JWKS (U15, U16) | 7 |
| `api/{embed.go,swagger.html}`; `internal/adapters/httpapi/{handler,problem,status,middleware,routes,docs_handler,placeholder_handlers,module,server}.go` + testes | Borda HTTP: middlewares, mapeamento, tabela de rotas (U17, I15) | 8 |
| `internal/adapters/httpapi/{module,server}.go`; `internal/bootstrap/*`; `docker-compose.yml`; `test/testkit/auth.go` | Composição Fx e timeouts | 9 |
| `test/testkit/{contract,api,app,assert,auth,aws,net}.go` + testes; `test/integration/{main,harness}_test.go`; `internal/adapters/postgres/outbox_problems_integration_test.go` | Harness da integração | 10 |
| `internal/adapters/httpapi/{dto,wallets_handler}.go` + testes; `test/integration/wallets_test.go` | Carteiras (HTTP-01..03, HTTP-07, I08) | 11 |
| `internal/adapters/httpapi/{dto,wagering_handler}.go` + testes; `test/integration/{helpers,wagering,errors,auth}_test.go` | Apostas (HTTP-04..06, I09–I12, A02, A03) | 12 |
| `test/integration/{auth,concurrency}_test.go` | A01, A02c, A04, C01a e C02 com sensibilidade | 13 |
| `docs/*`, `ARCHITECTURE.md`, `docs/dev/diary.md` | Encerramento do marco | 14 |

---

### Tarefa 1: `config`: OIDC, documentação da API e agenda das referências

Configuração nova da spec §7 (decisões 13 e 15), com a mesma regra do M0: um erro nomeia a variável e nunca ecoa o valor. `OIDC_ISSUER` e `OIDC_JWKS_URL` são obrigatórias, e as demais têm padrão.

**Arquivos:**
- Implementação: `internal/config/config.go`, `internal/config/validate.go` (alterar); `.env.example` (alterar)
- Testes: `internal/config/config_test.go` (alterar)

**Interfaces:**
- Consome: `config.Config`, `Validate`, `FieldError` (M0).
- Produz:
  - `OIDCIssuer`, `OIDCJWKSURL`: `string`, obrigatórias, URL absoluta `http(s)`;
  - `OIDCAudience`: `string`, padrão `pda-api`;
  - `OIDCClockSkew`: `time.Duration`, padrão `30s`, `>= 0`;
  - `APIDocsEnabled`: `bool`, padrão `true`;
  - `ReferenceRetryBaseDelay` (`1s`), `ReferenceRetryMaxDelay` (`60s`, entre a base e 24 h), `ReferenceMaxAttempts` (`int`, `8`, `>= 1`) e `ReferenceTTL` (`10m`).

- [ ] **Passo 1: criar os stubs (os campos, ainda sem padrão)**

Substituir `internal/config/config.go` (arquivo inteiro):

```go
// Package config loads and validates the process configuration from the environment.
package config

import (
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// DefaultHTTPAddr is the default API listen address, shared with the healthcheck probe.
const DefaultHTTPAddr = ":8080"

// MaxShutdownTimeout is the Fx stop timeout; SHUTDOWN_TIMEOUT must stay below it.
const MaxShutdownTimeout = 30 * time.Second

// Config is the validated process configuration.
type Config struct {
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	MetricsAddr     string        `env:"METRICS_ADDR" envDefault:":9090"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`
	DatabaseURL     string        `env:"DATABASE_URL"`
	DBMaxConns      int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	DBLockTimeout   time.Duration `env:"DB_LOCK_TIMEOUT" envDefault:"5s"`
	WagerQueueName  string        `env:"SQS_WAGER_QUEUE_NAME" envDefault:"wager-transactions.fifo"`
	WagerDLQName    string        `env:"SQS_WAGER_DLQ_NAME" envDefault:"wager-transactions-dlq.fifo"`

	// OIDC (D-07): the expected iss and where the keys are fetched are
	// separate, because the issuer seen by clients (localhost) differs from
	// the address reachable inside the compose network (keycloak).
	OIDCIssuer    string        `env:"OIDC_ISSUER"`
	OIDCJWKSURL   string        `env:"OIDC_JWKS_URL"`
	OIDCAudience  string        `env:"OIDC_AUDIENCE"`
	OIDCClockSkew time.Duration `env:"OIDC_CLOCK_SKEW"`

	APIDocsEnabled bool `env:"API_DOCS_ENABLED"`

	// Schedule of pending references (D-11).
	ReferenceRetryBaseDelay time.Duration `env:"REFERENCE_RETRY_BASE_DELAY"`
	ReferenceRetryMaxDelay  time.Duration `env:"REFERENCE_RETRY_MAX_DELAY"`
	ReferenceMaxAttempts    int           `env:"REFERENCE_MAX_ATTEMPTS"`
	ReferenceTTL            time.Duration `env:"REFERENCE_TTL"`
}

// Load reads the environment and validates it. Errors name variables, never values.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, redactParseError(err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// redactParseError rewrites caarlos0/env errors, whose messages include the raw value.
func redactParseError(err error) error {
	var out []error
	for _, e := range flatten(err) {
		var pe env.ParseError
		if errors.As(e, &pe) {
			out = append(out, &FieldError{Var: envVarOf(pe.Name), Reason: "invalid value"})
		}
	}
	if len(out) == 0 {
		return errors.New("config: failed to read the environment")
	}
	return errors.Join(out...)
}

func flatten(err error) []error {
	var agg env.AggregateError
	if errors.As(err, &agg) {
		return agg.Errors
	}
	return []error{err}
}

// envVarOf maps a Config field name to its environment variable.
func envVarOf(field string) string {
	f, ok := reflect.TypeFor[Config]().FieldByName(field)
	if !ok {
		return field
	}
	name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
	return name
}
```

- [ ] **Passo 2: escrever os testes que falham**

Substituir `internal/config/config_test.go` (arquivo inteiro):

```go
package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/config"
)

var allVars = []string{
	"LOG_LEVEL", "HTTP_ADDR", "METRICS_ADDR", "SHUTDOWN_TIMEOUT", "DATABASE_URL",
	"DB_MAX_CONNS", "DB_LOCK_TIMEOUT", "SQS_WAGER_QUEUE_NAME", "SQS_WAGER_DLQ_NAME",
	"OIDC_ISSUER", "OIDC_JWKS_URL", "OIDC_AUDIENCE", "OIDC_CLOCK_SKEW", "API_DOCS_ENABLED",
	"REFERENCE_RETRY_BASE_DELAY", "REFERENCE_RETRY_MAX_DELAY", "REFERENCE_MAX_ATTEMPTS", "REFERENCE_TTL",
}

const (
	validURL    = "postgres://pda_app:s3cr3t@localhost:5432/pda?sslmode=disable"
	validIssuer = "http://localhost:8080/realms/pda"
	validJWKS   = "http://keycloak:8080/realms/pda/protocol/openid-connect/certs"
)

// setRequired sets the variables without a default.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", validURL)
	t.Setenv("OIDC_ISSUER", validIssuer)
	t.Setenv("OIDC_JWKS_URL", validJWKS)
}

// cleanEnv unsets every config variable for the test, restoring them afterwards.
func cleanEnv(t *testing.T) {
	t.Helper()
	for _, k := range allVars {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unset %s: %v", k, err)
		}
	}
}

func validConfig() config.Config {
	return config.Config{
		LogLevel: "info", HTTPAddr: ":8080", MetricsAddr: ":9090",
		ShutdownTimeout: 20 * time.Second, DatabaseURL: validURL, DBMaxConns: 10, DBLockTimeout: 5 * time.Second,
		WagerQueueName: "wager-transactions.fifo", WagerDLQName: "wager-transactions-dlq.fifo",
		OIDCIssuer: validIssuer, OIDCJWKSURL: validJWKS, OIDCAudience: "pda-api", OIDCClockSkew: 30 * time.Second,
		APIDocsEnabled:          true,
		ReferenceRetryBaseDelay: time.Second, ReferenceRetryMaxDelay: time.Minute, ReferenceMaxAttempts: 8,
		ReferenceTTL: 10 * time.Minute,
	}
}

// fieldErrors flattens a joined error into its *FieldError parts.
func fieldErrors(err error) []*config.FieldError {
	var out []*config.FieldError
	var walk func(error)
	walk = func(e error) {
		var multi interface{ Unwrap() []error }
		if errors.As(e, &multi) {
			for _, inner := range multi.Unwrap() {
				walk(inner)
			}
			return
		}
		var fe *config.FieldError
		if errors.As(e, &fe) {
			out = append(out, fe)
		}
	}
	walk(err)
	return out
}

func vars(err error) []string {
	var names []string
	for _, fe := range fieldErrors(err) {
		names = append(names, fe.Var)
	}
	return names
}

// Covers: FX-02
func TestLoad_AppliesDefaults(t *testing.T) {
	cleanEnv(t)
	setRequired(t)

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := validConfig(); got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
	if got.HTTPAddr != config.DefaultHTTPAddr {
		t.Fatalf("HTTPAddr default %q != DefaultHTTPAddr %q", got.HTTPAddr, config.DefaultHTTPAddr)
	}
}

// Covers: FX-02
func TestLoad_ReadsEnvironment(t *testing.T) {
	cleanEnv(t)
	env := map[string]string{
		"LOG_LEVEL": "debug", "HTTP_ADDR": ":18080", "METRICS_ADDR": ":19090",
		"SHUTDOWN_TIMEOUT": "5s", "DATABASE_URL": "postgresql://u:p@db:5432/x",
		"DB_MAX_CONNS": "4", "DB_LOCK_TIMEOUT": "2s", "SQS_WAGER_QUEUE_NAME": "w.fifo", "SQS_WAGER_DLQ_NAME": "d.fifo",
		"OIDC_ISSUER": "https://idp.example/realms/x", "OIDC_JWKS_URL": "https://idp.internal/certs",
		"OIDC_AUDIENCE": "api", "OIDC_CLOCK_SKEW": "1s", "API_DOCS_ENABLED": "false",
		"REFERENCE_RETRY_BASE_DELAY": "100ms", "REFERENCE_RETRY_MAX_DELAY": "1s", "REFERENCE_MAX_ATTEMPTS": "3",
		"REFERENCE_TTL": "3s",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	got, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Config{
		LogLevel: "debug", HTTPAddr: ":18080", MetricsAddr: ":19090", ShutdownTimeout: 5 * time.Second,
		DatabaseURL: "postgresql://u:p@db:5432/x", DBMaxConns: 4, DBLockTimeout: 2 * time.Second,
		WagerQueueName: "w.fifo", WagerDLQName: "d.fifo",
		OIDCIssuer: "https://idp.example/realms/x", OIDCJWKSURL: "https://idp.internal/certs",
		OIDCAudience: "api", OIDCClockSkew: time.Second, APIDocsEnabled: false,
		ReferenceRetryBaseDelay: 100 * time.Millisecond, ReferenceRetryMaxDelay: time.Second,
		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

// Covers: FX-02
func TestLoad_RequiresURLs(t *testing.T) {
	cleanEnv(t)

	_, err := config.Load()
	if got := strings.Join(vars(err), ","); got != "DATABASE_URL,OIDC_ISSUER,OIDC_JWKS_URL" {
		t.Fatalf("Load() error vars = %s, want DATABASE_URL,OIDC_ISSUER,OIDC_JWKS_URL (err = %v)", got, err)
	}
}

// Covers: FX-02
func TestValidate_RejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*config.Config)
		wantVar string
	}{
		{"unknown log level", func(c *config.Config) { c.LogLevel = "verbose" }, "LOG_LEVEL"},
		{"empty http addr", func(c *config.Config) { c.HTTPAddr = "" }, "HTTP_ADDR"},
		{"empty metrics addr", func(c *config.Config) { c.MetricsAddr = "" }, "METRICS_ADDR"},
		{"http equals metrics", func(c *config.Config) { c.MetricsAddr = c.HTTPAddr }, "HTTP_ADDR"},
		{"zero shutdown", func(c *config.Config) { c.ShutdownTimeout = 0 }, "SHUTDOWN_TIMEOUT"},
		{"shutdown at stop timeout", func(c *config.Config) { c.ShutdownTimeout = config.MaxShutdownTimeout }, "SHUTDOWN_TIMEOUT"},
		{"wrong scheme", func(c *config.Config) { c.DatabaseURL = "mysql://u:p@h/db" }, "DATABASE_URL"},
		{"missing host", func(c *config.Config) { c.DatabaseURL = "postgres:///pda" }, "DATABASE_URL"},
		{"unparsable url", func(c *config.Config) { c.DatabaseURL = "postgres://u:p@h:bad port/db" }, "DATABASE_URL"},
		{"zero max conns", func(c *config.Config) { c.DBMaxConns = 0 }, "DB_MAX_CONNS"},
		{"zero lock timeout", func(c *config.Config) { c.DBLockTimeout = 0 }, "DB_LOCK_TIMEOUT"},
		{"negative lock timeout", func(c *config.Config) { c.DBLockTimeout = -time.Second }, "DB_LOCK_TIMEOUT"},
		{"non-fifo queue", func(c *config.Config) { c.WagerQueueName = "wager" }, "SQS_WAGER_QUEUE_NAME"},
		{"non-fifo dlq", func(c *config.Config) { c.WagerDLQName = "dlq" }, "SQS_WAGER_DLQ_NAME"},
		{"dlq equals queue", func(c *config.Config) { c.WagerDLQName = c.WagerQueueName }, "SQS_WAGER_DLQ_NAME"},
		{"missing issuer", func(c *config.Config) { c.OIDCIssuer = "" }, "OIDC_ISSUER"},
		{"relative issuer", func(c *config.Config) { c.OIDCIssuer = "/realms/pda" }, "OIDC_ISSUER"},
		{"issuer not http", func(c *config.Config) { c.OIDCIssuer = "ftp://idp/realms/pda" }, "OIDC_ISSUER"},
		{"missing jwks url", func(c *config.Config) { c.OIDCJWKSURL = "" }, "OIDC_JWKS_URL"},
		{"unparsable jwks url", func(c *config.Config) { c.OIDCJWKSURL = "http://idp:bad port/certs" }, "OIDC_JWKS_URL"},
		{"empty audience", func(c *config.Config) { c.OIDCAudience = "" }, "OIDC_AUDIENCE"},
		{"negative clock skew", func(c *config.Config) { c.OIDCClockSkew = -time.Second }, "OIDC_CLOCK_SKEW"},
		{"zero base delay", func(c *config.Config) { c.ReferenceRetryBaseDelay = 0 }, "REFERENCE_RETRY_BASE_DELAY"},
		{"max below base", func(c *config.Config) { c.ReferenceRetryMaxDelay = c.ReferenceRetryBaseDelay / 2 }, "REFERENCE_RETRY_MAX_DELAY"},
		{"max above a day", func(c *config.Config) { c.ReferenceRetryMaxDelay = 25 * time.Hour }, "REFERENCE_RETRY_MAX_DELAY"},
		{"zero attempts", func(c *config.Config) { c.ReferenceMaxAttempts = 0 }, "REFERENCE_MAX_ATTEMPTS"},
		{"zero ttl", func(c *config.Config) { c.ReferenceTTL = 0 }, "REFERENCE_TTL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			got := vars(cfg.Validate())
			if len(got) != 1 || got[0] != tc.wantVar {
				t.Fatalf("Validate() vars = %v, want [%s]", got, tc.wantVar)
			}
		})
	}
}

// Covers: FX-02
func TestValidate_AcceptsValidConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Covers: FX-02
func TestValidate_ReportsAllErrorsAtOnce(t *testing.T) {
	cfg := validConfig()
	cfg.LogLevel = "verbose"
	cfg.DBMaxConns = 0
	cfg.WagerQueueName = "wager"

	got := strings.Join(vars(cfg.Validate()), ",")
	if got != "LOG_LEVEL,DB_MAX_CONNS,SQS_WAGER_QUEUE_NAME" {
		t.Fatalf("Validate() vars = %s, want LOG_LEVEL,DB_MAX_CONNS,SQS_WAGER_QUEUE_NAME", got)
	}
}

// Covers: FX-02
func TestValidate_AcceptsZeroClockSkew(t *testing.T) {
	cfg := validConfig()
	cfg.OIDCClockSkew = 0
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// Covers: OBS-02
func TestLoad_ErrorsNeverContainValues(t *testing.T) {
	cases := []struct {
		name, key, value, secret, wantVar string
	}{
		{"password in invalid url", "DATABASE_URL", "mysql://pda_app:SuperSecret42@h/db", "SuperSecret42", "DATABASE_URL"},
		{"unparsable duration", "SHUTDOWN_TIMEOUT", "abc123xyz", "abc123xyz", "SHUTDOWN_TIMEOUT"},
		{"unparsable int", "DB_MAX_CONNS", "ten-conns-99", "ten-conns-99", "DB_MAX_CONNS"},
		{"unparsable lock timeout", "DB_LOCK_TIMEOUT", "forever-77", "forever-77", "DB_LOCK_TIMEOUT"},
		{"credentials in issuer", "OIDC_ISSUER", "ftp://admin:IssuerSecret7@idp/realms/pda", "IssuerSecret7", "OIDC_ISSUER"},
		{"unparsable flag", "API_DOCS_ENABLED", "maybe-42", "maybe-42", "API_DOCS_ENABLED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			setRequired(t)
			t.Setenv(tc.key, tc.value)

			_, err := config.Load()
			if err == nil {
				t.Fatal("Load() error = nil, want error")
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("error leaks the value: %q", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantVar) {
				t.Fatalf("error %q does not name %s", err.Error(), tc.wantVar)
			}
		})
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -race -count=1 ./internal/config/
```

Esperado: FAIL, sem `panic` nem erro de compilação:
- `TestLoad_AppliesDefaults` falha porque o stub não tem padrões (`OIDCAudience:` vazio);
- `TestLoad_RequiresURLs` mostra `vars = DATABASE_URL`;
- os 12 casos novos de `TestValidate_RejectsInvalidValues` mostram `Validate() vars = []`;
- "credentials in issuer" mostra `Load() error = nil`.

- [ ] **Passo 4: implementar**

Substituir `internal/config/config.go` (arquivo inteiro):

```go
// Package config loads and validates the process configuration from the environment.
package config

import (
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// DefaultHTTPAddr is the default API listen address, shared with the healthcheck probe.
const DefaultHTTPAddr = ":8080"

// MaxShutdownTimeout is the Fx stop timeout; SHUTDOWN_TIMEOUT must stay below it.
const MaxShutdownTimeout = 30 * time.Second

// Config is the validated process configuration.
type Config struct {
	LogLevel        string        `env:"LOG_LEVEL" envDefault:"info"`
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:":8080"`
	MetricsAddr     string        `env:"METRICS_ADDR" envDefault:":9090"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"20s"`
	DatabaseURL     string        `env:"DATABASE_URL"`
	DBMaxConns      int32         `env:"DB_MAX_CONNS" envDefault:"10"`
	DBLockTimeout   time.Duration `env:"DB_LOCK_TIMEOUT" envDefault:"5s"`
	WagerQueueName  string        `env:"SQS_WAGER_QUEUE_NAME" envDefault:"wager-transactions.fifo"`
	WagerDLQName    string        `env:"SQS_WAGER_DLQ_NAME" envDefault:"wager-transactions-dlq.fifo"`

	// OIDC (D-07): the expected iss and where the keys are fetched are
	// separate, because the issuer seen by clients (localhost) differs from
	// the address reachable inside the compose network (keycloak).
	OIDCIssuer    string        `env:"OIDC_ISSUER"`
	OIDCJWKSURL   string        `env:"OIDC_JWKS_URL"`
	OIDCAudience  string        `env:"OIDC_AUDIENCE" envDefault:"pda-api"`
	OIDCClockSkew time.Duration `env:"OIDC_CLOCK_SKEW" envDefault:"30s"`

	APIDocsEnabled bool `env:"API_DOCS_ENABLED" envDefault:"true"`

	// Schedule of pending references (D-11).
	ReferenceRetryBaseDelay time.Duration `env:"REFERENCE_RETRY_BASE_DELAY" envDefault:"1s"`
	ReferenceRetryMaxDelay  time.Duration `env:"REFERENCE_RETRY_MAX_DELAY" envDefault:"60s"`
	ReferenceMaxAttempts    int           `env:"REFERENCE_MAX_ATTEMPTS" envDefault:"8"`
	ReferenceTTL            time.Duration `env:"REFERENCE_TTL" envDefault:"10m"`
}

// Load reads the environment and validates it. Errors name variables, never values.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, redactParseError(err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// redactParseError rewrites caarlos0/env errors, whose messages include the raw value.
func redactParseError(err error) error {
	var out []error
	for _, e := range flatten(err) {
		var pe env.ParseError
		if errors.As(e, &pe) {
			out = append(out, &FieldError{Var: envVarOf(pe.Name), Reason: "invalid value"})
		}
	}
	if len(out) == 0 {
		return errors.New("config: failed to read the environment")
	}
	return errors.Join(out...)
}

func flatten(err error) []error {
	var agg env.AggregateError
	if errors.As(err, &agg) {
		return agg.Errors
	}
	return []error{err}
}

// envVarOf maps a Config field name to its environment variable.
func envVarOf(field string) string {
	f, ok := reflect.TypeFor[Config]().FieldByName(field)
	if !ok {
		return field
	}
	name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
	return name
}
```

Substituir `internal/config/validate.go` (arquivo inteiro):

```go
package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// FieldError reports an invalid variable by name. It never carries the value.
type FieldError struct {
	Var    string
	Reason string
}

func (e *FieldError) Error() string { return "config: " + e.Var + ": " + e.Reason }

// Validate checks every rule and reports all violations at once.
func (c Config) Validate() error {
	var errs []error
	fail := func(v, reason string) { errs = append(errs, &FieldError{Var: v, Reason: reason}) }

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		fail("LOG_LEVEL", "must be one of debug, info, warn, error")
	}
	if c.HTTPAddr == "" {
		fail("HTTP_ADDR", "must not be empty")
	}
	if c.MetricsAddr == "" {
		fail("METRICS_ADDR", "must not be empty")
	}
	if c.HTTPAddr != "" && c.HTTPAddr == c.MetricsAddr {
		fail("HTTP_ADDR", "must differ from METRICS_ADDR")
	}
	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout >= MaxShutdownTimeout {
		fail("SHUTDOWN_TIMEOUT", "must be greater than 0 and less than "+MaxShutdownTimeout.String())
	}
	if reason := databaseURLProblem(c.DatabaseURL); reason != "" {
		fail("DATABASE_URL", reason)
	}
	if c.DBMaxConns < 1 {
		fail("DB_MAX_CONNS", "must be at least 1")
	}
	if c.DBLockTimeout <= 0 {
		fail("DB_LOCK_TIMEOUT", "must be greater than 0")
	}
	if !strings.HasSuffix(c.WagerQueueName, ".fifo") {
		fail("SQS_WAGER_QUEUE_NAME", "must end with .fifo")
	}
	switch {
	case !strings.HasSuffix(c.WagerDLQName, ".fifo"):
		fail("SQS_WAGER_DLQ_NAME", "must end with .fifo")
	case c.WagerDLQName == c.WagerQueueName:
		fail("SQS_WAGER_DLQ_NAME", "must differ from SQS_WAGER_QUEUE_NAME")
	}
	if reason := httpURLProblem(c.OIDCIssuer); reason != "" {
		fail("OIDC_ISSUER", reason)
	}
	if reason := httpURLProblem(c.OIDCJWKSURL); reason != "" {
		fail("OIDC_JWKS_URL", reason)
	}
	if c.OIDCAudience == "" {
		fail("OIDC_AUDIENCE", "must not be empty")
	}
	if c.OIDCClockSkew < 0 {
		fail("OIDC_CLOCK_SKEW", "must not be negative")
	}
	if c.ReferenceRetryBaseDelay <= 0 {
		fail("REFERENCE_RETRY_BASE_DELAY", "must be greater than 0")
	}
	if c.ReferenceRetryMaxDelay < c.ReferenceRetryBaseDelay || c.ReferenceRetryMaxDelay > maxReferenceRetryDelay {
		fail("REFERENCE_RETRY_MAX_DELAY", "must be between REFERENCE_RETRY_BASE_DELAY and "+maxReferenceRetryDelay.String())
	}
	if c.ReferenceMaxAttempts < 1 {
		fail("REFERENCE_MAX_ATTEMPTS", "must be at least 1")
	}
	if c.ReferenceTTL <= 0 {
		fail("REFERENCE_TTL", "must be greater than 0")
	}
	return errors.Join(errs...)
}

// maxReferenceRetryDelay is the upper bound wagering.NewReferenceRetryPolicy accepts.
const maxReferenceRetryDelay = 24 * time.Hour

// httpURLProblem returns why raw is not an absolute http(s) URL, or "" if it is.
// Like databaseURLProblem, it never echoes the value.
func httpURLProblem(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "must be an absolute http or https URL"
	}
	return ""
}

// databaseURLProblem returns why raw is unusable, or "" if it is fine.
// The parse error is dropped on purpose: it would echo the URL (and its password).
func databaseURLProblem(raw string) string {
	if raw == "" {
		return "is required"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "must be a valid URL"
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "scheme must be postgres or postgresql"
	}
	if u.Host == "" {
		return "must include a host"
	}
	return ""
}
```

Em `.env.example`, trocar:

```text
OTHER_PROVIDER_SECRET=other-provider-local-secret

# --- AWS (MiniStack) ---
```

por:

```text
OTHER_PROVIDER_SECRET=other-provider-local-secret

# --- OIDC (validação dos tokens pela aplicação, D-07) ---
# O iss esperado é o que os clientes veem (localhost); as chaves vêm pela rede do compose.
OIDC_ISSUER=http://localhost:8080/realms/pda
OIDC_JWKS_URL=http://keycloak:8080/realms/pda/protocol/openid-connect/certs

# --- AWS (MiniStack) ---
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/config/
```

Esperado: `ok`.

**Checkpoint:** `go test -race -count=1 ./internal/config/` verde.

---

### Tarefa 2: `app`: portas novas, `domainError` (U13) e cursor do ledger (U14)

As partes puras do `app` (spec §4.1, §4.4 e §4.6):
- as portas `Clock`, `IDGenerator` e `Metrics`, com as implementações padrão;
- a tradução única dos erros do domínio (decisão 6);
- o cursor opaco e a faixa do `limit` (decisão 9).

O `depguard` do `app` passa a excluir os testes, porque os testes de integração das próximas tarefas usam o adapter `postgres` real (decisão 26).

**Arquivos:**
- Implementação: `internal/app/ports.go` (alterar), `internal/app/errors.go` (alterar), `internal/app/system.go`, `internal/app/cursor.go` (criar), `.golangci.yml` (alterar)
- Testes: `internal/app/errors_test.go`, `internal/app/cursor_test.go` (pacote `app`, internos), `internal/app/system_test.go`

**Interfaces:**
- Consome: `wagering.ValidationError`, `wagering.ConflictError`, `apperrors.New/Classify/CodeOf`.
- Produz:
  - as portas `app.Clock{Now() time.Time}`, `app.IDGenerator{New() string}` e `app.Metrics{ReconciliationDivergence()}`, com `app.SystemClock` e `app.UUIDv7`;
  - as constantes `app.CodeUnknownWallet`, `CodeWalletNotFound` e `CodeTransactionNotFound`;
  - `app.DefaultLedgerLimit = 50` e `app.MaxLedgerLimit = 200`;
  - as funções internas `domainError(error) error`, `invalidField(field string) error`, `encodeCursor(int64) string`, `decodeCursor(string) (int64, error)` e `checkLimit(int) error`.

- [ ] **Passo 1: criar os stubs**

Em `internal/app/ports.go`, trocar:

```go
// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
```

por:

```go
// Clock is the time source of the use cases; the domain receives now as a
// parameter and never reads the clock itself.
type Clock interface{ Now() time.Time }

// IDGenerator creates the identifiers of new rows and events: canonical
// lowercase UUIDv7 (D-08).
type IDGenerator interface{ New() string }

// Metrics is what the use cases report beyond logs. It grows with M7; the
// observability adapter implements it with Prometheus.
type Metrics interface {
	// ReconciliationDivergence counts a reconciliation whose stored balance
	// differs from the ledger (reconciliation_divergences_total, HTTP-07).
	ReconciliationDivergence()
}

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
```

Criar `internal/app/system.go`:

```go
package app

import "time"

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Time{} }

// UUIDv7 generates time-ordered UUIDs.
type UUIDv7 struct{}

// New returns a canonical lowercase UUIDv7.
func (UUIDv7) New() string { return "" }
```

Substituir `internal/app/errors.go` (arquivo inteiro):

```go
package app

import (
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Sentinels the persistence adapter wraps in an *apperrors.Error, so the use
// cases react with errors.Is and never see SQLSTATEs or constraint names
// (D-14).
var (
	// ErrNotFound: Get or Lock found no row (KindNotFound).
	ErrNotFound = errors.New("app: not found")
	// ErrWalletAlreadyExists: a wallet for (playerId, currency) exists
	// (KindConflict, code WALLET_ALREADY_EXISTS).
	ErrWalletAlreadyExists = errors.New("app: wallet already exists")
	// ErrIdempotencyRace: another request inserted the same (providerId,
	// idempotencyKey) or (providerId, externalTransactionId) first. Rollback,
	// reread and answer with the replay (D-08). KindTransient, so that an
	// unhandled race is retried into the reread.
	ErrIdempotencyRace = errors.New("app: concurrent insert of the same operation")
	// ErrReversalRace: another reversal of the same reference was PROCESSED
	// first (D-10). Rollback and reprocess into ALREADY_REVERSED. KindTransient.
	ErrReversalRace = errors.New("app: concurrent reversal of the same reference")
	// ErrInboxDuplicate: another consumer recorded the same message first. The
	// message is a duplicate (messaging.md). KindTransient.
	ErrInboxDuplicate = errors.New("app: message already recorded in the inbox")
)

// Codes of lifecycle §5.3 that the use cases assign; the others come from the
// domain (wagering.InputCode) or from the adapters.
const (
	CodeUnknownWallet       = "UNKNOWN_WALLET"
	CodeWalletNotFound      = "WALLET_NOT_FOUND"
	CodeTransactionNotFound = "TRANSACTION_NOT_FOUND"
)

// domainError classifies an error returned by the domain (spec decision 6).
func domainError(err error) error { return err }

// invalidField reports a malformed field or parameter as a validation error, so
// the edge answers 400 INVALID_FIELD with the field name.
func invalidField(field string) error {
	return domainError(&wagering.ValidationError{Code: wagering.InputInvalidField, Field: field})
}
```

Criar `internal/app/cursor.go`:

```go
package app

// Ledger page sizes (D-16, HTTP-03).
const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

// encodeCursor returns the opaque cursor that resumes the ledger after version.
func encodeCursor(version int64) string { return "" }

// decodeCursor returns the version a cursor resumes after.
func decodeCursor(s string) (int64, error) { return 0, nil }

// checkLimit accepts a page size from 1 to MaxLedgerLimit.
func checkLimit(limit int) error { return nil }
```

Em `.golangci.yml`, trocar:

```yaml
        app:
          files: ["**/internal/app/**"]
```

por:

```yaml
        app:
          files: ["**/internal/app/**", "!$test"] # os testes usam o adapter real (spec do M3, decisão 18)
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/app/errors_test.go`:

```go
package app

// Internal test: domainError is the private translation every use case
// applies to what the domain returns (spec decision 6).

import (
	"errors"
	"fmt"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Covers: TX-10, DOM-04 (U13)
func TestDomainErrorKind(t *testing.T) {
	transient := apperrors.New(apperrors.KindTransient, "", errors.New("lock timeout"))
	cases := []struct {
		name     string
		err      error
		wantKind apperrors.Kind
		wantCode string
	}{
		{"validation error", &wagering.ValidationError{Code: wagering.InputInvalidAmount, Field: "money.amount"}, apperrors.KindInput, "INVALID_AMOUNT"},
		{"conflict error", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused}, apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED"},
		{"already classified", fmt.Errorf("wrapped: %w", transient), apperrors.KindTransient, ""},
		{"money overflow", fmt.Errorf("%w: credit", money.ErrOverflow), apperrors.KindPermanent, ""},
		{"currency mismatch", money.ErrCurrencyMismatch, apperrors.KindPermanent, ""},
		{"uninitialized money", money.ErrUninitialized, apperrors.KindPermanent, ""},
		{"invalid transition", wagering.ErrInvalidTransition, apperrors.KindPermanent, ""},
		{"invalid argument", wagering.ErrInvalidArgument, apperrors.KindPermanent, ""},
		{"invalid snapshot", wagering.ErrInvalidSnapshot, apperrors.KindPermanent, ""},
		{"not persistable", wagering.ErrNotPersistable, apperrors.KindPermanent, ""},
		{"uninitialized transaction", wagering.ErrUninitialized, apperrors.KindPermanent, ""},
		{"invalid wallet", wallet.ErrInvalidWallet, apperrors.KindPermanent, ""},
		{"invalid ledger entry", wallet.ErrInvalidLedgerEntry, apperrors.KindPermanent, ""},
		{"invalid event", events.ErrInvalidEvent, apperrors.KindPermanent, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainError(tc.err)
			if kind := apperrors.Classify(got); kind != tc.wantKind {
				t.Fatalf("Classify(domainError(%v)) = %q, want %q", tc.err, kind, tc.wantKind)
			}
			if code := apperrors.CodeOf(got); code != tc.wantCode {
				t.Fatalf("CodeOf = %q, want %q", code, tc.wantCode)
			}
			if !errors.Is(got, tc.err) {
				t.Fatalf("domainError(%v) lost the original error", tc.err)
			}
		})
	}
	if domainError(nil) != nil {
		t.Fatal("domainError(nil) != nil")
	}
	var ve *wagering.ValidationError
	if !errors.As(domainError(&wagering.ValidationError{Code: wagering.InputMissingField, Field: "kind"}), &ve) || ve.Field != "kind" {
		t.Fatal("the validation error, with its field, must stay in the chain for the edge")
	}
}
```

Criar `internal/app/cursor_test.go`:

```go
package app

// Internal test: the cursor is opaque to clients, so its encoding is private.

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func wantInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *wagering.ValidationError
	if apperrors.Classify(err) != apperrors.KindInput || !errors.As(err, &ve) ||
		ve.Code != wagering.InputInvalidField || ve.Field != field {
		t.Fatalf("error = %v, want INVALID_FIELD on %s", err, field)
	}
}

// Covers: HTTP-03 (U14)
func TestLedgerCursor(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		for _, v := range []int64{1, 2, 50, 1 << 40} {
			c := encodeCursor(v)
			got, err := decodeCursor(c)
			if err != nil || got != v {
				t.Fatalf("decodeCursor(encodeCursor(%d) = %q) = %d, %v", v, c, got, err)
			}
		}
	})
	t.Run("opaque base64url of the version", func(t *testing.T) {
		if got := encodeCursor(2); got != "eyJ2IjoyfQ" {
			t.Fatalf("encodeCursor(2) = %q, want eyJ2IjoyfQ", got)
		}
	})
	t.Run("rejects malformed cursors", func(t *testing.T) {
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		for name, c := range map[string]string{
			"not base64":        "!!!",
			"padded base64":     base64.URLEncoding.EncodeToString([]byte(`{"v":2}`)),
			"not json":          enc(`v=2`),
			"unknown field":     enc(`{"v":2,"x":1}`),
			"missing version":   enc(`{}`),
			"zero version":      enc(`{"v":0}`),
			"negative version":  enc(`{"v":-1}`),
			"fractional":        enc(`{"v":2.5}`),
			"string version":    enc(`{"v":"2"}`),
			"trailing data":     enc(`{"v":2}{}`),
			"null":              enc(`null`),
			"overflowing value": enc(`{"v":99999999999999999999}`),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := decodeCursor(c)
				wantInvalidField(t, err, "cursor")
			})
		}
	})
	t.Run("limit from 1 to 200", func(t *testing.T) {
		for _, ok := range []int{1, DefaultLedgerLimit, MaxLedgerLimit} {
			if err := checkLimit(ok); err != nil {
				t.Fatalf("checkLimit(%d) = %v", ok, err)
			}
		}
		for _, bad := range []int{0, -1, MaxLedgerLimit + 1} {
			wantInvalidField(t, checkLimit(bad), "limit")
		}
	})
}
```

Criar `internal/app/system_test.go`:

```go
package app_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/ident"
)

func TestSystemClock(t *testing.T) {
	before := time.Now()
	got := app.SystemClock{}.Now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("SystemClock.Now() = %v, want the wall clock", got)
	}
}

// Covers: D-08
func TestUUIDv7(t *testing.T) {
	a, b := app.UUIDv7{}.New(), app.UUIDv7{}.New()
	for _, id := range []string{a, b} {
		if !ident.Valid(id) || id[14] != '7' {
			t.Fatalf("New() = %q, want a canonical UUIDv7", id)
		}
	}
	if a == b || a > b {
		t.Fatalf("New() = %q then %q, want distinct, time-ordered ids", a, b)
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet ./internal/app/ && go test -race -count=1 ./internal/app/
```

Esperado: FAIL em `TestDomainErrorKind` (os erros saem sem classificação), `TestLedgerCursor` (o stub aceita tudo e codifica `""`), `TestSystemClock` (`0001-01-01`) e `TestUUIDv7` (`New() = ""`).

- [ ] **Passo 4: implementar**

Substituir `internal/app/system.go` (arquivo inteiro):

```go
package app

import (
	"time"

	"github.com/google/uuid"
)

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// UUIDv7 generates time-ordered UUIDs.
type UUIDv7 struct{}

// New returns a canonical lowercase UUIDv7. uuid.NewV7 fails only when the
// system random source fails, which leaves nothing sensible to do.
func (UUIDv7) New() string { return uuid.Must(uuid.NewV7()).String() }
```

Substituir `internal/app/errors.go` (arquivo inteiro):

```go
package app

import (
	"errors"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Sentinels the persistence adapter wraps in an *apperrors.Error, so the use
// cases react with errors.Is and never see SQLSTATEs or constraint names
// (D-14).
var (
	// ErrNotFound: Get or Lock found no row (KindNotFound).
	ErrNotFound = errors.New("app: not found")
	// ErrWalletAlreadyExists: a wallet for (playerId, currency) exists
	// (KindConflict, code WALLET_ALREADY_EXISTS).
	ErrWalletAlreadyExists = errors.New("app: wallet already exists")
	// ErrIdempotencyRace: another request inserted the same (providerId,
	// idempotencyKey) or (providerId, externalTransactionId) first. Rollback,
	// reread and answer with the replay (D-08). KindTransient, so that an
	// unhandled race is retried into the reread.
	ErrIdempotencyRace = errors.New("app: concurrent insert of the same operation")
	// ErrReversalRace: another reversal of the same reference was PROCESSED
	// first (D-10). Rollback and reprocess into ALREADY_REVERSED. KindTransient.
	ErrReversalRace = errors.New("app: concurrent reversal of the same reference")
	// ErrInboxDuplicate: another consumer recorded the same message first. The
	// message is a duplicate (messaging.md). KindTransient.
	ErrInboxDuplicate = errors.New("app: message already recorded in the inbox")
)

// Codes of lifecycle §5.3 that the use cases assign; the others come from the
// domain (wagering.InputCode) or from the adapters.
const (
	CodeUnknownWallet       = "UNKNOWN_WALLET"
	CodeWalletNotFound      = "WALLET_NOT_FOUND"
	CodeTransactionNotFound = "TRANSACTION_NOT_FOUND"
)

// domainError classifies an error returned by the domain (spec decision 6).
// The domain does no I/O, so what it returns is either invalid input or a
// broken invariant: a *ValidationError is KindInput and a *ConflictError is
// KindConflict, both with their code; an error already classified by an
// adapter keeps its Kind; anything else (overflow, invalid transition,
// corrupted snapshot…) is KindPermanent. Without this, D-05 would treat an
// invariant violation as transient and retry it forever.
func domainError(err error) error {
	if err == nil {
		return nil
	}
	var classified *apperrors.Error
	if errors.As(err, &classified) {
		return err
	}
	var validation *wagering.ValidationError
	if errors.As(err, &validation) {
		return apperrors.New(apperrors.KindInput, string(validation.Code), err)
	}
	var conflict *wagering.ConflictError
	if errors.As(err, &conflict) {
		return apperrors.New(apperrors.KindConflict, string(conflict.Code), err)
	}
	return apperrors.New(apperrors.KindPermanent, "", err)
}

// invalidField reports a malformed field or parameter as a validation error, so
// the edge answers 400 INVALID_FIELD with the field name.
func invalidField(field string) error {
	return domainError(&wagering.ValidationError{Code: wagering.InputInvalidField, Field: field})
}
```

Substituir `internal/app/cursor.go` (arquivo inteiro):

```go
package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// Ledger page sizes (D-16, HTTP-03).
const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

// encodeCursor returns the opaque cursor that resumes the ledger after
// version: base64url, without padding, of {"v":version}.
func encodeCursor(version int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(`{"v":` + strconv.FormatInt(version, 10) + `}`))
}

// decodeCursor returns the version a cursor resumes after. Anything but an
// encodeCursor result is 400 INVALID_FIELD on "cursor": no silent fallback to
// the first page.
func decodeCursor(s string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, invalidField("cursor")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c struct {
		V *int64 `json:"v"`
	}
	if err := dec.Decode(&c); err != nil || c.V == nil || *c.V < 1 {
		return 0, invalidField("cursor")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, invalidField("cursor")
	}
	return *c.V, nil
}

// checkLimit accepts a page size from 1 to MaxLedgerLimit; there is no silent
// clamp (spec decision 9).
func checkLimit(limit int) error {
	if limit < 1 || limit > MaxLedgerLimit {
		return invalidField("limit")
	}
	return nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/app/
```

Esperado: `ok`.

**Checkpoint:** `go test -race -count=1 ./internal/app/` verde.

---

### Tarefa 3: `OpenWallet` (I20) e o ambiente de integração do `app`

O primeiro caso de uso (spec §4.3, decisão 7): validação na ordem do lifecycle §3.1 e, num único commit, a carteira e, se o saldo inicial for positivo, o `OPENING`, o lançamento e os dois eventos. Os testes rodam contra o PostgreSQL real, num banco próprio do pacote (decisão 18).

**Arquivos:**
- Implementação: `internal/app/open_wallet.go`, `internal/app/seal.go` (criar)
- Testes: `internal/app/main_integration_test.go`, `internal/app/helpers_integration_test.go`, `internal/app/open_wallet_integration_test.go` (tag `integration`)

**Interfaces:**
- Consome: `app.UnitOfWork`, `app.Repos` e `app.ErrWalletAlreadyExists` (M2); `wagering.OpenWallet`; `events.Seal`; `domainError` (Tarefa 2).
- Produz:
  - `app.OpenWalletInput{PlayerID *string; InitialBalance *wagering.MoneyInput}`;
  - `app.NewOpenWallet(uow UnitOfWork, clock Clock, ids IDGenerator) *OpenWallet`;
  - `(*OpenWallet).Execute(ctx, OpenWalletInput, correlationID string) (wallet.Wallet, error)`;
  - `sealEvents(ids IDGenerator, evs []events.Event, correlationID, causationID string) ([]events.Envelope, error)` (interno);
  - nos testes: `env`, `newID`, `ptr`, `newUoW`, `reads`, `openWallet`, `trackWallet`, `wantError`, `wantInvalid`, `outboxTypes` e `count`.

- [ ] **Passo 1: criar o stub**

Criar `internal/app/open_wallet.go`:

```go
package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

var errNotImplemented = errors.New("app: not implemented")

// OpenWalletInput is the raw body of POST /wallets; a nil field is absent.
type OpenWalletInput struct {
	PlayerID       *string
	InitialBalance *wagering.MoneyInput
}

// OpenWallet opens a wallet (lifecycle §6.4, HTTP-01).
type OpenWallet struct {
	uow   UnitOfWork
	clock Clock
	ids   IDGenerator
}

// NewOpenWallet builds the use case.
func NewOpenWallet(uow UnitOfWork, clock Clock, ids IDGenerator) *OpenWallet {
	return &OpenWallet{uow: uow, clock: clock, ids: ids}
}

// Execute validates the input and opens the wallet.
func (o *OpenWallet) Execute(ctx context.Context, in OpenWalletInput, correlationID string) (wallet.Wallet, error) {
	return wallet.Wallet{}, errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/app/main_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// env is this package's isolated database (test-plan §3.2): the use cases run
// against the real schema, triggers and unique indexes (spec decision 18).
var env *testkit.Env

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	e, cleanup, err := testkit.NewEnv(ctx, "app")
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.NewEnv:", err)
		os.Exit(1)
	}
	env = e
	code := m.Run()
	cleanup()
	os.Exit(code)
}
```

Criar `internal/app/helpers_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ptr(s string) *string { return &s }

// newUoW is the unit of work over the package database, as the app role.
func newUoW() app.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

func newOpenWallet() *app.OpenWallet {
	return app.NewOpenWallet(newUoW(), app.SystemClock{}, app.UUIDv7{})
}

func moneyInput(amount, currency string) *wagering.MoneyInput {
	return &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)}
}

// openWallet opens a BRL wallet through the use case and checks the ledger of
// test-plan §6 when the test ends.
func openWallet(t *testing.T, initial string) wallet.Wallet {
	t.Helper()
	w, err := newOpenWallet().Execute(t.Context(), app.OpenWalletInput{
		PlayerID: ptr(newID()), InitialBalance: moneyInput(initial, "BRL"),
	}, "corr-open")
	if err != nil {
		t.Fatalf("OpenWallet %s: %v", initial, err)
	}
	trackWallet(t, w.ID())
	return w
}

// trackWallet checks the wallet against test-plan §6 when the test ends.
func trackWallet(t *testing.T, walletID string) {
	t.Helper()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, walletID) })
}

// wantError fails unless err classifies as kind with code.
func wantError(t *testing.T, err error, kind apperrors.Kind, code string) {
	t.Helper()
	if got := apperrors.Classify(err); got != kind || apperrors.CodeOf(err) != code {
		t.Fatalf("error = %v (%s %q), want %s %q", err, got, apperrors.CodeOf(err), kind, code)
	}
}

// wantInvalid fails unless err is the validation error code on field.
func wantInvalid(t *testing.T, err error, code wagering.InputCode, field string) {
	t.Helper()
	var ve *wagering.ValidationError
	if apperrors.Classify(err) != apperrors.KindInput || !errors.As(err, &ve) || ve.Code != code || ve.Field != field {
		t.Fatalf("error = %v, want %s on %s", err, code, field)
	}
}

// outboxTypes lists the event types of a wallet, in insertion order, with
// the correlation id of each.
func outboxTypes(t *testing.T, walletID string) (types, correlations []string) {
	t.Helper()
	rows, err := env.Owner.Query(t.Context(), `
		SELECT event_type, correlation_id FROM outbox_events
		WHERE message_group_id = $1 ORDER BY occurred_at, event_type`, walletID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, corr string
		if err := rows.Scan(&typ, &corr); err != nil {
			t.Fatalf("outbox: %v", err)
		}
		types, correlations = append(types, typ), append(correlations, corr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return types, correlations
}

// count runs a count(*) query as the owner.
func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
```

Criar `internal/app/open_wallet_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-01, WAL-03, OUT-13, TST-U06 (I20)
func TestOpenWallet(t *testing.T) {
	t.Parallel()

	t.Run("positive balance opens with OPENING, entry and events in one commit", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		player := newID()
		w, err := newOpenWallet().Execute(ctx, app.OpenWalletInput{
			PlayerID: ptr(player), InitialBalance: moneyInput("100.00", "BRL"),
		}, "corr-open-1")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if w.PlayerID() != player || w.Balance().String() != "100.00" || w.Version() != 1 {
			t.Fatalf("wallet = %s %s v%d", w.PlayerID(), w.Balance(), w.Version())
		}
		stored, err := reads().Wallets().Get(ctx, w.ID())
		if err != nil || stored != w {
			t.Fatalf("stored wallet = %+v, %v; want %+v", stored, err, w)
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions
			WHERE wallet_id = $1 AND kind = 'OPENING' AND origin = 'INTERNAL' AND status = 'PROCESSED'`, w.ID()); n != 1 {
			t.Fatalf("%d OPENING operations, want 1", n)
		}
		sum, err := reads().Ledger().Sum(ctx, w.ID())
		if err != nil || sum.Entries != 1 || sum.NetMinor != 10000 {
			t.Fatalf("ledger = %+v, %v; want 1 credit of 100.00", sum, err)
		}
		types, corrs := outboxTypes(t, w.ID())
		if strings.Join(types, ",") != "WagerTransactionProcessed,WalletBalanceChanged" ||
			corrs[0] != "corr-open-1" || corrs[1] != "corr-open-1" {
			t.Fatalf("outbox = %v %v", types, corrs)
		}
		trackWallet(t, w.ID())
	})

	t.Run("zero balance opens only the wallet", func(t *testing.T) {
		t.Parallel()
		w := openWallet(t, "0.00")
		if w.Balance().String() != "0.00" || w.Version() != 1 {
			t.Fatalf("wallet = %s v%d", w.Balance(), w.Version())
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1`, w.ID()); n != 0 {
			t.Fatalf("%d operations, want none", n)
		}
		if types, _ := outboxTypes(t, w.ID()); len(types) != 0 {
			t.Fatalf("outbox = %v, want empty", types)
		}
	})

	t.Run("second wallet for the player and currency conflicts", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		player := newID()
		in := app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("10.00", "BRL")}
		first, err := newOpenWallet().Execute(ctx, in, "corr")
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		trackWallet(t, first.ID())

		_, err = newOpenWallet().Execute(ctx, in, "corr")
		wantError(t, err, apperrors.KindConflict, "WALLET_ALREADY_EXISTS")
		if !errors.Is(err, app.ErrWalletAlreadyExists) {
			t.Fatalf("errors.Is(%v, ErrWalletAlreadyExists) = false", err)
		}
		other, err := newOpenWallet().Execute(ctx, app.OpenWalletInput{
			PlayerID: ptr(strings.ToUpper(player)), InitialBalance: moneyInput("0.00", "USD"),
		}, "corr")
		if err != nil || other.PlayerID() != player {
			t.Fatalf("USD wallet for the same player = %s, %v; want the lowercase player id", other.PlayerID(), err)
		}
	})

	t.Run("invalid input is rejected before writing", func(t *testing.T) {
		t.Parallel()
		player := newID()
		cases := []struct {
			name  string
			in    app.OpenWalletInput
			code  wagering.InputCode
			field string
		}{
			{"missing player", app.OpenWalletInput{InitialBalance: moneyInput("1.00", "BRL")}, wagering.InputMissingField, "playerId"},
			{"missing balance", app.OpenWalletInput{PlayerID: ptr(player)}, wagering.InputMissingField, "initialBalance"},
			{"missing amount", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: &wagering.MoneyInput{Currency: ptr("BRL")}}, wagering.InputMissingField, "initialBalance.amount"},
			{"missing currency", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: &wagering.MoneyInput{Amount: ptr("1.00")}}, wagering.InputMissingField, "initialBalance.currency"},
			{"missing wins over invalid", app.OpenWalletInput{PlayerID: ptr("not-a-uuid")}, wagering.InputMissingField, "initialBalance"},
			{"invalid player", app.OpenWalletInput{PlayerID: ptr("not-a-uuid"), InitialBalance: moneyInput("1.00", "BRL")}, wagering.InputInvalidField, "playerId"},
			{"negative amount", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("-1.00", "BRL")}, wagering.InputInvalidAmount, "initialBalance.amount"},
			{"amount without decimals", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("10", "BRL")}, wagering.InputInvalidAmount, "initialBalance.amount"},
			{"lowercase currency", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("1.00", "brl")}, wagering.InputInvalidCurrency, "initialBalance.currency"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := newOpenWallet().Execute(t.Context(), tc.in, "corr")
				wantInvalid(t, err, tc.code, tc.field)
			})
		}
		if n := count(t, `SELECT count(*) FROM wallets WHERE player_id = $1`, player); n != 0 {
			t.Fatalf("%d wallets written for invalid input", n)
		}
	})
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet -tags=integration ./internal/app/ && go test -tags=integration -race -count=1 -run TestOpenWallet ./internal/app/
```

Esperado: FAIL nos quatro subtestes, com `Execute: app: not implemented` e `error = app: not implemented, want MISSING_FIELD on playerId` (e os demais códigos).

- [ ] **Passo 4: implementar**

Criar `internal/app/seal.go`:

```go
package app

import "github.com/KaioVinicios/pda/internal/domain/events"

// sealEvents wraps the events the domain returned in envelopes with fresh
// UUIDv7 event ids, ready for the outbox (D-13).
func sealEvents(ids IDGenerator, evs []events.Event, correlationID, causationID string) ([]events.Envelope, error) {
	out := make([]events.Envelope, 0, len(evs))
	for _, e := range evs {
		env, err := events.Seal(ids.New(), correlationID, causationID, e)
		if err != nil {
			return nil, domainError(err)
		}
		out = append(out, env)
	}
	return out, nil
}
```

Substituir `internal/app/open_wallet.go` (arquivo inteiro):

```go
package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// OpenWalletInput is the raw body of POST /wallets; a nil field is absent.
type OpenWalletInput struct {
	PlayerID       *string
	InitialBalance *wagering.MoneyInput
}

// OpenWallet opens a wallet (lifecycle §6.4, HTTP-01).
type OpenWallet struct {
	uow   UnitOfWork
	clock Clock
	ids   IDGenerator
}

// NewOpenWallet builds the use case.
func NewOpenWallet(uow UnitOfWork, clock Clock, ids IDGenerator) *OpenWallet {
	return &OpenWallet{uow: uow, clock: clock, ids: ids}
}

// Execute validates the input and writes, in one commit, the wallet and, for
// a positive balance, the OPENING, its CREDIT entry and its two events. A
// second wallet for (playerId, currency) is KindConflict WALLET_ALREADY_EXISTS.
func (o *OpenWallet) Execute(ctx context.Context, in OpenWalletInput, correlationID string) (wallet.Wallet, error) {
	playerID, initial, err := validateOpening(in)
	if err != nil {
		return wallet.Wallet{}, domainError(err)
	}
	opening, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: o.ids.New(), PlayerID: playerID, Initial: initial,
		TransactionID: o.ids.New(), EntryID: o.ids.New(), CorrelationID: correlationID, Now: o.clock.Now(),
	})
	if err != nil {
		return wallet.Wallet{}, domainError(err)
	}
	envs, err := sealEvents(o.ids, opening.Events, correlationID, "")
	if err != nil {
		return wallet.Wallet{}, err
	}
	err = o.uow.Do(ctx, func(r Repos) error {
		if err := r.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if opening.Tx == nil {
			return nil
		}
		if err := r.Transactions().Insert(ctx, opening.Tx); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *opening.Entry); err != nil {
			return err
		}
		return r.Outbox().Insert(ctx, envs...)
	})
	if err != nil {
		return wallet.Wallet{}, err
	}
	return opening.Wallet, nil
}

// validateOpening checks the body in the order of lifecycle §3.1: absent
// fields first, then formats. The amount follows the strict format and may be
// zero (OPS-11).
func validateOpening(in OpenWalletInput) (string, money.Money, error) {
	missing := func(field string) (string, money.Money, error) {
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputMissingField, Field: field}
	}
	switch {
	case in.PlayerID == nil:
		return missing("playerId")
	case in.InitialBalance == nil:
		return missing("initialBalance")
	case in.InitialBalance.Amount == nil:
		return missing("initialBalance.amount")
	case in.InitialBalance.Currency == nil:
		return missing("initialBalance.currency")
	}
	playerID, err := ident.Parse(*in.PlayerID)
	if err != nil {
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidField, Field: "playerId"}
	}
	initial, err := money.Parse(*in.InitialBalance.Amount, *in.InitialBalance.Currency)
	switch {
	case errors.Is(err, money.ErrInvalidAmount):
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidAmount, Field: "initialBalance.amount"}
	case err != nil:
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidCurrency, Field: "initialBalance.currency"}
	}
	return playerID, initial, nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/app/
```

Esperado: `ok`.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/app/` verde.

---

### Tarefa 4: `ProcessWager`: fluxo principal (I21)

O pipeline do lifecycle §6.1 (spec §4.2), ainda sem retentativas nem `FAILED`, que são da Tarefa 5:
- pré-checagem de idempotência sobre o pool;
- lock da carteira;
- nova checagem sob o lock;
- `settleAndPersist` na ordem dos triggers: operação → saldo e lançamento → eventos → antecipação das pendências.

**Arquivos:**
- Implementação: `internal/app/process_wager.go` (criar)
- Testes: `internal/app/helpers_integration_test.go` (substituir), `internal/app/process_wager_integration_test.go`

**Interfaces:**
- Consome: `wagering.NewExternal`, `CheckIdempotency` e `Settle`; os repositórios do M2; `sealEvents` (Tarefa 3).
- Produz:
  - `app.ProcessRequest{Command wagering.Command; Via wagering.ReceivedVia; CorrelationID, CausationID string}`;
  - `app.ProcessResult{Tx *wagering.WagerTransaction; Replay bool}`;
  - `app.NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager`;
  - `(*ProcessWager).Execute(ctx, ProcessRequest) (ProcessResult, error)`;
  - os internos `lookup` e `(*ProcessWager).settleAndPersist(ctx, r Repos, tx, w *wallet.Wallet, now time.Time, insert bool, causationID string) error`;
  - nos testes: `policy`, `newProcessWager`, `newProvider`, `op`, `command`, `request`, `process`, `wantResult`, `describe`, `wantWallet` e `walletLike`.

- [ ] **Passo 1: criar o stub**

Criar `internal/app/process_wager.go`:

```go
package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

var errNotImplemented = errors.New("app: not implemented")

// ProcessRequest is one external operation, as HTTP (and, from M5, SQS)
// delivers it.
type ProcessRequest struct {
	Command       wagering.Command
	Via           wagering.ReceivedVia
	CorrelationID string
	// CausationID is the message that caused the operation ("" over HTTP).
	CausationID string
}

// ProcessResult is the persisted outcome: PROCESSED, PENDING_REFERENCE,
// REJECTED or FAILED. Replay tells whether it was recorded by an earlier
// delivery of the same operation (D-08).
type ProcessResult struct {
	Tx     *wagering.WagerTransaction
	Replay bool
}

// ProcessWager is the single use case of HTTP, SQS and the reference worker
// (lifecycle §6).
type ProcessWager struct {
	uow    UnitOfWork
	reads  Repos
	clock  Clock
	ids    IDGenerator
	policy wagering.ReferenceRetryPolicy
	log    *slog.Logger
}

// NewProcessWager builds the use case. reads are the repositories over the
// pool, used for the idempotency lookup before the transaction.
func NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager {
	return &ProcessWager{uow: uow, reads: reads, clock: clock, ids: ids, policy: policy, log: log}
}

// Execute processes the operation.
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	return ProcessResult{}, errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

Substituir `internal/app/helpers_integration_test.go` (arquivo inteiro):

```go
//go:build integration

package app_test

import (
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ptr(s string) *string { return &s }

// newUoW is the unit of work over the package database, as the app role.
func newUoW() app.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

func newOpenWallet() *app.OpenWallet {
	return app.NewOpenWallet(newUoW(), app.SystemClock{}, app.UUIDv7{})
}

func moneyInput(amount, currency string) *wagering.MoneyInput {
	return &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)}
}

// openWallet opens a BRL wallet through the use case and checks the ledger of
// test-plan §6 when the test ends.
func openWallet(t *testing.T, initial string) wallet.Wallet {
	t.Helper()
	w, err := newOpenWallet().Execute(t.Context(), app.OpenWalletInput{
		PlayerID: ptr(newID()), InitialBalance: moneyInput(initial, "BRL"),
	}, "corr-open")
	if err != nil {
		t.Fatalf("OpenWallet %s: %v", initial, err)
	}
	trackWallet(t, w.ID())
	return w
}

// trackWallet checks the wallet against test-plan §6 when the test ends.
func trackWallet(t *testing.T, walletID string) {
	t.Helper()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, walletID) })
}

// wantError fails unless err classifies as kind with code.
func wantError(t *testing.T, err error, kind apperrors.Kind, code string) {
	t.Helper()
	if got := apperrors.Classify(err); got != kind || apperrors.CodeOf(err) != code {
		t.Fatalf("error = %v (%s %q), want %s %q", err, got, apperrors.CodeOf(err), kind, code)
	}
}

// wantInvalid fails unless err is the validation error code on field.
func wantInvalid(t *testing.T, err error, code wagering.InputCode, field string) {
	t.Helper()
	var ve *wagering.ValidationError
	if apperrors.Classify(err) != apperrors.KindInput || !errors.As(err, &ve) || ve.Code != code || ve.Field != field {
		t.Fatalf("error = %v, want %s on %s", err, code, field)
	}
}

// outboxTypes lists the event types of a wallet, in insertion order, with
// the correlation id of each.
func outboxTypes(t *testing.T, walletID string) (types, correlations []string) {
	t.Helper()
	rows, err := env.Owner.Query(t.Context(), `
		SELECT event_type, correlation_id FROM outbox_events
		WHERE message_group_id = $1 ORDER BY occurred_at, event_type`, walletID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, corr string
		if err := rows.Scan(&typ, &corr); err != nil {
			t.Fatalf("outbox: %v", err)
		}
		types, correlations = append(types, typ), append(correlations, corr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return types, correlations
}

// count runs a count(*) query as the owner.
func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// policy is the reference schedule of these tests: a long first delay, so
// that an advanced pending operation is unmistakably earlier.
var policy = func() wagering.ReferenceRetryPolicy {
	p, err := wagering.NewReferenceRetryPolicy(30*time.Second, time.Minute, 3, 10*time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return p
}()

func newProcessWager() *app.ProcessWager {
	return app.NewProcessWager(newUoW(), reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
}

// newProvider isolates the external ids of a test from the parallel ones.
func newProvider() string { return "provider-" + newID() }

// op describes an operation on a wallet; command validates it.
type op struct {
	provider, kind, amount, ext, ref string
	key                              string // "" = provider:ext
	currency                         string // "" = BRL
}

func command(t *testing.T, w wallet.Wallet, o op) wagering.Command {
	t.Helper()
	key, currency := o.key, o.currency
	if key == "" {
		key = o.provider + ":" + o.ext
	}
	if currency == "" {
		currency = "BRL"
	}
	in := wagering.Input{
		IdempotencyKey: ptr(key), ProviderID: ptr(o.provider), ExternalTransactionID: ptr(o.ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(o.kind), Money: moneyInput(o.amount, currency),
	}
	if o.ref != "" {
		in.ReferenceExternalTransactionID = ptr(o.ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand %+v: %v", o, err)
	}
	return cmd
}

func request(cmd wagering.Command) app.ProcessRequest {
	return app.ProcessRequest{Command: cmd, Via: wagering.ReceivedViaHTTP, CorrelationID: "corr-" + cmd.ExternalTransactionID()}
}

// process runs the operation and fails the test on error.
func process(t *testing.T, pw *app.ProcessWager, w wallet.Wallet, o op) app.ProcessResult {
	t.Helper()
	res, err := pw.Execute(t.Context(), request(command(t, w, o)))
	if err != nil {
		t.Fatalf("Execute %s %s: %v", o.kind, o.ext, err)
	}
	return res
}

// wantResult fails unless res has the status, failure code and observed balance.
func wantResult(t *testing.T, res app.ProcessResult, status wagering.Status, code wagering.FailureCode, balance string, replay bool) {
	t.Helper()
	tx := res.Tx
	if tx == nil || tx.Status() != status || tx.FailureCode() != code || tx.ResultBalance().String() != balance || res.Replay != replay {
		t.Fatalf("result = %+v, want %s %s balance %q replay %v", describe(res), status, code, balance, replay)
	}
}

func describe(res app.ProcessResult) string {
	if res.Tx == nil {
		return "<nil>"
	}
	return string(res.Tx.Status()) + " " + string(res.Tx.FailureCode()) + " balance " + res.Tx.ResultBalance().String() +
		" replay " + strconv.FormatBool(res.Replay)
}

// wantWallet fails unless the stored wallet has the balance and version.
func wantWallet(t *testing.T, walletID, balance string, version int64) {
	t.Helper()
	w, err := reads().Wallets().Get(t.Context(), walletID)
	if err != nil || w.Balance().String() != balance || w.Version() != version {
		t.Fatalf("wallet = %s v%d, %v; want %s v%d", w.Balance(), w.Version(), err, balance, version)
	}
}

// walletLike is w with another id: a command for a wallet that does not exist.
func walletLike(t *testing.T, w wallet.Wallet, id string) wallet.Wallet {
	t.Helper()
	other, err := wallet.Open(id, w.PlayerID(), w.Balance(), time.Now())
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}
	return other
}
```

Criar `internal/app/process_wager_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-06, IDEM-05, IDEM-06, IDEM-07, IDEM-08, TX-06, OPS-01..03 (I21)
func TestProcessWager(t *testing.T) {
	t.Parallel()

	t.Run("processes and replays with the original balance", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}

		first := process(t, pw, w, bet)
		wantResult(t, first, wagering.StatusProcessed, "", "70.00", false)
		if first.Tx.CorrelationID() != "corr-bet-1" || first.Tx.ReceivedVia() != wagering.ReceivedViaHTTP {
			t.Fatalf("correlation %q via %q", first.Tx.CorrelationID(), first.Tx.ReceivedVia())
		}
		wantWallet(t, w.ID(), "70.00", 2)
		process(t, pw, w, op{provider: p, kind: "WIN", amount: "50.00", ext: "win-1"})
		wantWallet(t, w.ID(), "120.00", 3)

		replay := process(t, pw, w, bet)
		wantResult(t, replay, wagering.StatusProcessed, "", "70.00", true)
		if replay.Tx.ID() != first.Tx.ID() {
			t.Fatalf("replay id %s, want %s", replay.Tx.ID(), first.Tx.ID())
		}
		wantWallet(t, w.ID(), "120.00", 3)
		types, corrs := outboxTypes(t, w.ID())
		if len(types) != 6 || corrs[2] != "corr-bet-1" {
			t.Fatalf("outbox = %v %v; want 2 events each for the opening, the BET and the WIN", types, corrs)
		}
	})

	t.Run("LOSS processes without movement", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		res := process(t, pw, w, op{provider: p, kind: "LOSS", amount: "0.00", ext: "loss-1"})
		wantResult(t, res, wagering.StatusProcessed, "", "100.00", false)
		wantWallet(t, w.ID(), "100.00", 1)
	})

	t.Run("rejection is persisted and replayed", func(t *testing.T) {
		t.Parallel()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "500.00", ext: "bet-1"}
		wantResult(t, process(t, pw, w, bet), wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", false)
		wantResult(t, process(t, pw, w, bet), wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", true)
		wantWallet(t, w.ID(), "100.00", 1)
	})

	t.Run("idempotency conflicts write nothing", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		process(t, pw, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

		_, err := pw.Execute(ctx, request(command(t, w, op{provider: p, kind: "BET", amount: "20.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED")
		_, err = pw.Execute(ctx, request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1", key: "other-key"})))
		wantError(t, err, apperrors.KindConflict, "EXTERNAL_TRANSACTION_ID_CONFLICT")
		wantWallet(t, w.ID(), "90.00", 2)
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
			t.Fatalf("%d operations for the provider, want 1", n)
		}
	})

	t.Run("unknown wallet writes nothing", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		cmd := command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})
		missing := command(t, walletLike(t, w, newID()), op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

		_, err := pw.Execute(ctx, request(missing))
		wantError(t, err, apperrors.KindInput, "UNKNOWN_WALLET")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations written for an unknown wallet", n)
		}
		if _, err := pw.Execute(ctx, request(cmd)); err != nil {
			t.Fatalf("the same key on the real wallet: %v", err)
		}
	})

	t.Run("references resolve, wait and advance", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		pw, w, p := newProcessWager(), openWallet(t, "100.00"), newProvider()
		bet := process(t, pw, w, op{provider: p, kind: "BET", amount: "20.00", ext: "bet-1"})
		win := process(t, pw, w, op{provider: p, kind: "WIN", amount: "5.00", ext: "win-1", ref: "bet-1"})
		wantResult(t, win, wagering.StatusProcessed, "", "85.00", false)
		if win.Tx.ReferenceTransactionID() != bet.Tx.ID() {
			t.Fatalf("WIN resolved %q, want %s", win.Tx.ReferenceTransactionID(), bet.Tx.ID())
		}

		refund := op{provider: p, kind: "REFUND", amount: "10.00", ext: "refund-2", ref: "bet-2"}
		pending := process(t, pw, w, refund)
		wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
		wantResult(t, process(t, pw, w, refund), wagering.StatusPendingReference, "", "", true)

		process(t, pw, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-2"})
		advanced, err := reads().Transactions().Get(ctx, pending.Tx.ID())
		if err != nil {
			t.Fatal(err)
		}
		if !advanced.NextAttemptAt().Before(pending.Tx.NextAttemptAt()) {
			t.Fatalf("the pending REFUND was not advanced: next attempt %v, was %v", advanced.NextAttemptAt(), pending.Tx.NextAttemptAt())
		}
		types, _ := outboxTypes(t, w.ID())
		if !strings.Contains(strings.Join(types, ","), "WagerTransactionPendingReference") {
			t.Fatalf("outbox = %v, want the pending event", types)
		}
	})
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet -tags=integration ./internal/app/ && go test -tags=integration -race -count=1 -run TestProcessWager ./internal/app/
```

Esperado: FAIL nos seis subtestes, com `Execute BET bet-1: app: not implemented`; em "unknown wallet", com `error = app: not implemented (TRANSIENT ""), want INPUT "UNKNOWN_WALLET"`.

- [ ] **Passo 4: implementar (sem retentativas nem `FAILED`)**

Substituir `internal/app/process_wager.go` (arquivo inteiro):

```go
package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// ProcessRequest is one external operation, as HTTP (and, from M5, SQS)
// delivers it.
type ProcessRequest struct {
	Command       wagering.Command
	Via           wagering.ReceivedVia
	CorrelationID string
	// CausationID is the message that caused the operation ("" over HTTP).
	CausationID string
}

// ProcessResult is the persisted outcome: PROCESSED, PENDING_REFERENCE,
// REJECTED or FAILED. Replay tells whether it was recorded by an earlier
// delivery of the same operation (D-08).
type ProcessResult struct {
	Tx     *wagering.WagerTransaction
	Replay bool
}

// ProcessWager is the single use case of HTTP, SQS and the reference worker
// (lifecycle §6).
type ProcessWager struct {
	uow    UnitOfWork
	reads  Repos
	clock  Clock
	ids    IDGenerator
	policy wagering.ReferenceRetryPolicy
	log    *slog.Logger
}

// NewProcessWager builds the use case. reads are the repositories over the
// pool, used for the idempotency lookup before the transaction.
func NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager {
	return &ProcessWager{uow: uow, reads: reads, clock: clock, ids: ids, policy: policy, log: log}
}

// Execute runs the pipeline of lifecycle §6.1 for one operation. Business
// outcomes (PROCESSED, PENDING_REFERENCE, REJECTED, FAILED) are results, never
// errors; an error means nothing was recorded: KindInput (UNKNOWN_WALLET),
// KindConflict (idempotency, 409) or KindTransient (retry).
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	return p.attempt(ctx, req)
}

// attempt looks the operation up by its idempotency keys and, if it is new,
// settles it under the wallet lock.
func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	cmd := req.Command
	if replay, err := lookup(ctx, p.reads, cmd); err != nil || replay != nil {
		return ProcessResult{Tx: replay, Replay: replay != nil}, err
	}
	now := p.clock.Now()
	var res ProcessResult
	err := p.uow.Do(ctx, func(r Repos) error {
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if errors.Is(err, ErrNotFound) {
			return apperrors.New(apperrors.KindInput, CodeUnknownWallet, err)
		}
		if err != nil {
			return err
		}
		// Again under the lock: a concurrent delivery may have committed it.
		replay, err := lookup(ctx, r, cmd)
		if err != nil || replay != nil {
			res = ProcessResult{Tx: replay, Replay: replay != nil}
			return err
		}
		tx, err := wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now)
		if err != nil {
			return domainError(err)
		}
		if err := p.settleAndPersist(ctx, r, tx, &w, now, true, req.CausationID); err != nil {
			return err
		}
		res = ProcessResult{Tx: tx}
		return nil
	})
	if err != nil {
		return ProcessResult{}, err
	}
	return res, nil
}

// lookup applies D-08: the transaction found by (providerId, idempotencyKey)
// with the same hash is a replay; the same key with another hash, or the same
// externalTransactionId under another key, is a conflict (KindConflict).
func lookup(ctx context.Context, r Repos, cmd wagering.Command) (*wagering.WagerTransaction, error) {
	byKey, err := r.Transactions().FindByIdempotencyKey(ctx, cmd.ProviderID(), cmd.IdempotencyKey())
	if err != nil {
		return nil, err
	}
	var byExternalID *wagering.WagerTransaction
	if byKey == nil {
		if byExternalID, err = r.Transactions().FindByExternalID(ctx, cmd.ProviderID(), cmd.ExternalTransactionID()); err != nil {
			return nil, err
		}
	}
	replay, err := wagering.CheckIdempotency(cmd.PayloadHash(), byKey, byExternalID)
	return replay, domainError(err)
}

// settleAndPersist evaluates the operation against its locked wallet and
// writes the outcome in the order the triggers require (data-model §4.2):
// the operation, then the balance and its entry, then the events. A terminal
// outcome advances the operations waiting for it (D-11). The reference worker
// (M6) calls it with insert = false.
func (p *ProcessWager) settleAndPersist(ctx context.Context, r Repos, tx *wagering.WagerTransaction, w *wallet.Wallet, now time.Time, insert bool, causationID string) error {
	var ref wagering.Reference
	if refID := tx.ReferenceExternalTransactionID(); refID != "" {
		var err error
		if ref, err = r.Transactions().FindReference(ctx, tx.ProviderID(), refID); err != nil {
			return err
		}
	}
	out, err := wagering.Settle(tx, w, ref, wagering.SettleParams{EntryID: p.ids.New(), Now: now, Policy: p.policy})
	if err != nil {
		return domainError(err)
	}
	envs, err := sealEvents(p.ids, out.Events, tx.CorrelationID(), causationID)
	if err != nil {
		return err
	}
	if insert {
		err = r.Transactions().Insert(ctx, tx)
	} else {
		err = r.Transactions().Update(ctx, tx)
	}
	if err != nil {
		return err
	}
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, *w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	if len(envs) > 0 {
		if err := r.Outbox().Insert(ctx, envs...); err != nil {
			return err
		}
	}
	if tx.Status().IsTerminal() {
		_, err := r.Transactions().AdvanceDependents(ctx, tx.ProviderID(), tx.ExternalTransactionID(), now)
		return err
	}
	return nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/app/
```

Esperado: `ok`.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/app/` verde.

---

### Tarefa 5: `ProcessWager`: corridas (I22), `FAILED` (I03b) e a corrida entre as leituras

Os caminhos de exceção (spec §2, decisões 4, 5 e 23):
- **corridas de unicidade:** reexecutam desde a pré-checagem, em até 3 tentativas;
- **falha permanente:** gravada como `FAILED` numa segunda UoW. Se nem isso der certo, a falha é transitória;
- **falha na pré-checagem:** não grava nada (`KindPermanent`);
- **mesma chave confirmada entre as duas leituras:** é replay.

O red usa o código da Tarefa 4. As corridas são **determinísticas**:
- um decorador segura as duas transações no `Insert` até ambas chegarem lá;
- outro esconde a linha da primeira leitura.

**Arquivos:**
- Implementação: `internal/app/process_wager.go` (substituir)
- Testes: `internal/app/faults_integration_test.go`, `internal/app/process_wager_failures_integration_test.go`

**Interfaces:**
- Consome: o `ProcessWager` da Tarefa 4 e `app.ErrIdempotencyRace`/`ErrReversalRace` (M2).
- Produz:
  - o laço de `Execute` (`maxAttempts = 3`) e o `isRace` (internos);
  - `(*ProcessWager).recordFailure`;
  - o `lookup` com a regra da mesma chave;
  - nos testes, os decoradores `faultyUoW`, `faultyRepos`, `failingOutbox` e `txHooks` e o `barrier`.

- [ ] **Passo 1: escrever os testes que falham**

Criar `internal/app/faults_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// faultyUoW decorates the repositories of every Do; call counts the Do calls
// from 1. The decorators provoke one failure at a time around the real
// adapter, never replacing it (test-plan §1).
type faultyUoW struct {
	app.UnitOfWork
	calls atomic.Int32
	wrap  func(r app.Repos, call int) app.Repos
}

func (u *faultyUoW) Do(ctx context.Context, fn func(app.Repos) error) error {
	call := int(u.calls.Add(1))
	return u.UnitOfWork.Do(ctx, func(r app.Repos) error { return fn(u.wrap(r, call)) })
}

// faultyRepos replaces the transaction or outbox repository when set.
type faultyRepos struct {
	app.Repos
	tx     app.TransactionRepository
	outbox app.OutboxRepository
}

func (f faultyRepos) Transactions() app.TransactionRepository {
	if f.tx != nil {
		return f.tx
	}
	return f.Repos.Transactions()
}

func (f faultyRepos) Outbox() app.OutboxRepository {
	if f.outbox != nil {
		return f.outbox
	}
	return f.Repos.Outbox()
}

type failingOutbox struct {
	app.OutboxRepository
	err error
}

func (f failingOutbox) Insert(context.Context, ...events.Envelope) error { return f.err }

// txHooks runs beforeInsert before Insert; findByKey, when set, runs before
// FindByIdempotencyKey, and hideKey makes it find nothing.
type txHooks struct {
	app.TransactionRepository
	beforeInsert func(ctx context.Context) error
	findByKey    func() error
	hideKey      bool
}

func (h txHooks) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	if h.beforeInsert != nil {
		if err := h.beforeInsert(ctx); err != nil {
			return err
		}
	}
	return h.TransactionRepository.Insert(ctx, t)
}

func (h txHooks) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error) {
	if h.findByKey != nil {
		if err := h.findByKey(); err != nil {
			return nil, err
		}
	}
	if h.hideKey {
		return nil, nil
	}
	return h.TransactionRepository.FindByIdempotencyKey(ctx, providerID, key)
}

// barrier holds each party until all of them arrive, or the context ends.
type barrier struct{ wg sync.WaitGroup }

func newBarrier(parties int) *barrier {
	b := &barrier{}
	b.wg.Add(parties)
	return b
}

func (b *barrier) arrive(ctx context.Context) {
	b.wg.Done()
	done := make(chan struct{})
	go func() { b.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
	}
}
```

Criar `internal/app/process_wager_failures_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: IDEM-07, CONC-03 (I22)
//
// Two deliveries with the same key for different wallets do not serialize on
// a wallet lock: both pass the lookups and insert. The barrier holds both
// inserts until both transactions reach them, so the unique index always
// decides: the loser gets ErrIdempotencyRace, retries and rereads into 409.
func TestProcessWagerRaces(t *testing.T) {
	t.Parallel()

	t.Run("same key in two wallets", func(t *testing.T) {
		t.Parallel()
		p := newProvider()
		wallets := [2]string{}
		b := newBarrier(2)
		results := make([]app.ProcessResult, 2)
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range 2 {
			w := openWallet(t, "100.00")
			wallets[i] = w.ID()
			cmd := command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})
			var once sync.Once
			uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
				return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(ctx context.Context) error {
					once.Do(func() { b.arrive(ctx) })
					return nil
				}}}
			}}
			pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
			wg.Go(func() { results[i], errs[i] = pw.Execute(t.Context(), request(cmd)) })
		}
		wg.Wait()

		winner, loser := 0, 1
		if errs[0] != nil {
			winner, loser = 1, 0
		}
		if errs[winner] != nil || results[winner].Tx.Status() != wagering.StatusProcessed {
			t.Fatalf("winner: %s, %v", describe(results[winner]), errs[winner])
		}
		wantError(t, errs[loser], apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED")
		wantWallet(t, wallets[winner], "90.00", 2)
		wantWallet(t, wallets[loser], "100.00", 1)
	})

	// Found by the C01a of the plan validation: the two lookups are separate
	// reads, and a concurrent delivery may commit between them.
	t.Run("same key committed between the two lookups", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"}
		first := process(t, newProcessWager(), w, bet)
		late := faultyRepos{Repos: reads(), tx: txHooks{TransactionRepository: reads().Transactions(), hideKey: true}}
		pw := app.NewProcessWager(newUoW(), late, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))

		res, err := pw.Execute(t.Context(), request(command(t, w, bet)))
		if err != nil || !res.Replay || res.Tx.ID() != first.Tx.ID() {
			t.Fatalf("Execute = %s, %v; want the replay of %s", describe(res), err, first.Tx.ID())
		}
	})

	t.Run("a race is retried at most 3 times", func(t *testing.T) {
		t.Parallel()
		for _, race := range []error{app.ErrIdempotencyRace, app.ErrReversalRace} {
			w, p := openWallet(t, "100.00"), newProvider()
			var inserts int
			uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
				return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(context.Context) error {
					inserts++
					return apperrors.New(apperrors.KindTransient, "", race)
				}}}
			}}
			pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
			_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})))
			if apperrors.Classify(err) != apperrors.KindTransient || !errors.Is(err, race) || inserts != 3 {
				t.Fatalf("%v: error %v after %d inserts, want the transient race after 3", race, err, inserts)
			}
			wantWallet(t, w.ID(), "100.00", 1)
		}
	})
}

// Covers: TX-06, TX-10, TST-I03 (I03b, the HTTP part; the SQS part is M5)
func TestPermanentFailureRecorded(t *testing.T) {
	t.Parallel()
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))

	t.Run("recorded as FAILED in a separate transaction and replayed", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		var logs bytes.Buffer
		uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
			return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
		}}
		pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.NewJSONHandler(&logs, nil)))
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}

		res, err := pw.Execute(t.Context(), request(command(t, w, bet)))
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		wantResult(t, res, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
		wantWallet(t, w.ID(), "100.00", 1)
		if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, res.Tx.ID()); n != 0 {
			t.Fatalf("%d entries for a FAILED operation", n)
		}
		if types, _ := outboxTypes(t, w.ID()); len(types) != 2 {
			t.Fatalf("outbox = %v, want only the 2 events of the opening", types)
		}
		line := logs.String()
		if !strings.Contains(line, `"level":"ERROR"`) || !strings.Contains(line, res.Tx.ID()) || strings.Contains(line, "30.00") {
			t.Fatalf("log = %s; want an ERROR with the transaction id and no amount", line)
		}

		replay := process(t, newProcessWager(), w, bet)
		wantResult(t, replay, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", true)
		if replay.Tx.ID() != res.Tx.ID() {
			t.Fatalf("replay id %s, want %s", replay.Tx.ID(), res.Tx.ID())
		}
	})

	t.Run("credit overflow fails permanently", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "92233720368547758.07"), newProvider()
		res := process(t, newProcessWager(), w, op{provider: p, kind: "WIN", amount: "0.01", ext: "win-1"})
		wantResult(t, res, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
		wantWallet(t, w.ID(), "92233720368547758.07", 1)
	})

	t.Run("a FAILED that cannot be recorded is transient", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, call int) app.Repos {
			if call == 1 {
				return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
			}
			return faultyRepos{Repos: r, tx: txHooks{TransactionRepository: r.Transactions(), beforeInsert: func(context.Context) error { return forced }}}
		}}
		pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
		_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindTransient, "")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations recorded", n)
		}
	})

	// Passes before recordFailure exists: it guards that a failed lookup
	// before the lock never records a FAILED.
	// Sensitivity: recordFailure also for a failed lookup before the lock →
	// the FAILED is written ("error = <nil>").
	t.Run("an unreadable earlier delivery is not recorded again", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		corrupted := faultyRepos{Repos: reads(), tx: txHooks{TransactionRepository: reads().Transactions(), findByKey: func() error { return forced }}}
		pw := app.NewProcessWager(newUoW(), corrupted, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
		_, err := pw.Execute(t.Context(), request(command(t, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})))
		wantError(t, err, apperrors.KindPermanent, "")
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
			t.Fatalf("%d operations recorded", n)
		}
	})
}
```

- [ ] **Passo 2: rodar e ver falhar**

```bash
go vet -tags=integration ./internal/app/ && go test -tags=integration -race -count=1 -run 'TestProcessWagerRaces|TestPermanentFailureRecorded' ./internal/app/
```

Esperado: FAIL por asserção.
- "same key in two wallets": `want CONFLICT "IDEMPOTENCY_KEY_REUSED"`, porque o erro é `TRANSIENT … 23505 wager_tx_idempotency_uq` sem retentativa.
- "same key committed between the two lookups": `CONFLICT EXTERNAL_TRANSACTION_ID_CONFLICT; want the replay`.
- "a race is retried at most 3 times": `after 1 inserts`.
- "recorded as FAILED…" e "credit overflow…": `Execute: PERMANENT: …`.
- "a FAILED that cannot be recorded is transient": `(PERMANENT ""), want TRANSIENT ""`.
- "an unreadable earlier delivery…" **já passa**. É a guarda da regra, e a sabotagem descrita no teste o faz falhar.

- [ ] **Passo 3: implementar**

Substituir `internal/app/process_wager.go` (arquivo inteiro):

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// ProcessRequest is one external operation, as HTTP (and, from M5, SQS)
// delivers it.
type ProcessRequest struct {
	Command       wagering.Command
	Via           wagering.ReceivedVia
	CorrelationID string
	// CausationID is the message that caused the operation ("" over HTTP).
	CausationID string
}

// ProcessResult is the persisted outcome: PROCESSED, PENDING_REFERENCE,
// REJECTED or FAILED. Replay tells whether it was recorded by an earlier
// delivery of the same operation (D-08).
type ProcessResult struct {
	Tx     *wagering.WagerTransaction
	Replay bool
}

// ProcessWager is the single use case of HTTP, SQS and the reference worker
// (lifecycle §6).
type ProcessWager struct {
	uow    UnitOfWork
	reads  Repos
	clock  Clock
	ids    IDGenerator
	policy wagering.ReferenceRetryPolicy
	log    *slog.Logger
}

// NewProcessWager builds the use case. reads are the repositories over the
// pool, used for the idempotency lookup before the transaction.
func NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager {
	return &ProcessWager{uow: uow, reads: reads, clock: clock, ids: ids, policy: policy, log: log}
}

// maxAttempts bounds the reruns after a unique-index race (spec decision 4).
const maxAttempts = 3

// Execute runs the pipeline of lifecycle §6.1 for one operation. Business
// outcomes (PROCESSED, PENDING_REFERENCE, REJECTED, FAILED) are results, never
// errors; an error means nothing new was recorded: KindInput (UNKNOWN_WALLET),
// KindConflict (idempotency, 409), KindTransient (retry) or KindPermanent (an
// earlier delivery that cannot be read).
//
// A unique-index race (the same operation committed concurrently) rolls back
// and reruns from the lookup, which then finds the replay, the conflict or
// ALREADY_REVERSED; after maxAttempts the race is returned, still transient.
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	var err error
	for range maxAttempts {
		var res ProcessResult
		if res, err = p.attempt(ctx, req); !isRace(err) {
			return res, err
		}
	}
	return ProcessResult{}, err
}

func isRace(err error) bool {
	return errors.Is(err, ErrIdempotencyRace) || errors.Is(err, ErrReversalRace)
}

// attempt looks the operation up by its idempotency keys and, if it is new,
// settles it under the wallet lock.
func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
	cmd := req.Command
	if replay, err := lookup(ctx, p.reads, cmd); err != nil || replay != nil {
		return ProcessResult{Tx: replay, Replay: replay != nil}, err
	}
	now := p.clock.Now()
	var res ProcessResult
	err := p.uow.Do(ctx, func(r Repos) error {
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if errors.Is(err, ErrNotFound) {
			return apperrors.New(apperrors.KindInput, CodeUnknownWallet, err)
		}
		if err != nil {
			return err
		}
		// Again under the lock: a concurrent delivery may have committed it.
		replay, err := lookup(ctx, r, cmd)
		if err != nil || replay != nil {
			res = ProcessResult{Tx: replay, Replay: replay != nil}
			return err
		}
		tx, err := wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now)
		if err != nil {
			return domainError(err)
		}
		if err := p.settleAndPersist(ctx, r, tx, &w, now, true, req.CausationID); err != nil {
			return err
		}
		res = ProcessResult{Tx: tx}
		return nil
	})
	if apperrors.Classify(err) == apperrors.KindPermanent {
		return p.recordFailure(ctx, req, now, err)
	}
	if err != nil {
		return ProcessResult{}, err
	}
	return res, nil
}

// recordFailure writes the operation as FAILED, INTERNAL_PERMANENT_FAILURE,
// in a transaction of its own, without entry or events (D-05, lifecycle
// §5.2), and advances the operations waiting for it. If even that cannot be
// written, the failure is transient: nothing was recorded, retrying is safe.
func (p *ProcessWager) recordFailure(ctx context.Context, req ProcessRequest, now time.Time, cause error) (ProcessResult, error) {
	cmd := req.Command
	var tx *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(r Repos) error {
		var err error
		if tx, err = wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now); err != nil {
			return domainError(err)
		}
		if err := tx.Fail(now); err != nil {
			return domainError(err)
		}
		if err := r.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		_, err = r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now)
		return err
	})
	switch {
	case isRace(err):
		return ProcessResult{}, err
	case err != nil:
		return ProcessResult{}, apperrors.New(apperrors.KindTransient, "", fmt.Errorf("app: recording FAILED: %w (after %w)", err, cause))
	}
	p.log.ErrorContext(ctx, "permanent failure recorded",
		"transactionId", tx.ID(), "walletId", cmd.WalletID(), "providerId", cmd.ProviderID(),
		"correlationId", req.CorrelationID, "error", cause.Error())
	return ProcessResult{Tx: tx}, nil
}

// lookup applies D-08: the transaction found by (providerId, idempotencyKey)
// with the same hash is a replay; the same key with another hash, or the same
// externalTransactionId under another key, is a conflict (KindConflict).
//
// The two reads are separate statements: a concurrent delivery of the same
// operation may commit between them, and then only the second one finds it.
// Found under the same key, it is the transaction of the key, not a conflict.
func lookup(ctx context.Context, r Repos, cmd wagering.Command) (*wagering.WagerTransaction, error) {
	byKey, err := r.Transactions().FindByIdempotencyKey(ctx, cmd.ProviderID(), cmd.IdempotencyKey())
	if err != nil {
		return nil, err
	}
	var byExternalID *wagering.WagerTransaction
	if byKey == nil {
		if byExternalID, err = r.Transactions().FindByExternalID(ctx, cmd.ProviderID(), cmd.ExternalTransactionID()); err != nil {
			return nil, err
		}
		if byExternalID != nil && byExternalID.IdempotencyKey() == cmd.IdempotencyKey() {
			byKey, byExternalID = byExternalID, nil
		}
	}
	replay, err := wagering.CheckIdempotency(cmd.PayloadHash(), byKey, byExternalID)
	return replay, domainError(err)
}

// settleAndPersist evaluates the operation against its locked wallet and
// writes the outcome in the order the triggers require (data-model §4.2):
// the operation, then the balance and its entry, then the events. A terminal
// outcome advances the operations waiting for it (D-11). The reference worker
// (M6) calls it with insert = false.
func (p *ProcessWager) settleAndPersist(ctx context.Context, r Repos, tx *wagering.WagerTransaction, w *wallet.Wallet, now time.Time, insert bool, causationID string) error {
	var ref wagering.Reference
	if refID := tx.ReferenceExternalTransactionID(); refID != "" {
		var err error
		if ref, err = r.Transactions().FindReference(ctx, tx.ProviderID(), refID); err != nil {
			return err
		}
	}
	out, err := wagering.Settle(tx, w, ref, wagering.SettleParams{EntryID: p.ids.New(), Now: now, Policy: p.policy})
	if err != nil {
		return domainError(err)
	}
	envs, err := sealEvents(p.ids, out.Events, tx.CorrelationID(), causationID)
	if err != nil {
		return err
	}
	if insert {
		err = r.Transactions().Insert(ctx, tx)
	} else {
		err = r.Transactions().Update(ctx, tx)
	}
	if err != nil {
		return err
	}
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, *w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	if len(envs) > 0 {
		if err := r.Outbox().Insert(ctx, envs...); err != nil {
			return err
		}
	}
	if tx.Status().IsTerminal() {
		_, err := r.Transactions().AdvanceDependents(ctx, tx.ProviderID(), tx.ExternalTransactionID(), now)
		return err
	}
	return nil
}
```

- [ ] **Passo 4: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/app/ && go test -tags=integration -race -count=5 -run TestProcessWagerRaces ./internal/app/
```

Esperado: `ok` duas vezes.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/app/` verde, e `TestProcessWagerRaces` estável em 5 execuções.

---

### Tarefa 6: consultas, reconciliação e `reconciliation_divergences_total`

As leituras da API (spec §4.4 e §4.5, decisões 8, 9 e 16):
- `Queries` sobre o pool, lendo a carteira antes do ledger, o que fecha as pendências de ID malformado da revisão do M2;
- `Reconcile` num snapshot `REPEATABLE READ READ ONLY`: uma divergência gera log `WARN` e incrementa a métrica, e nada é alterado;
- a métrica é implementada na `observability`.

Para forçar a divergência sem banco próprio, os triggers de `wallets` são desligados **dentro de uma transação** do owner. O `ALTER TABLE` pega um `ACCESS EXCLUSIVE` até o commit, então nenhum teste paralelo enxerga os triggers desligados.

**Arquivos:**
- Implementação: `internal/app/queries.go`, `internal/app/reconcile.go` (criar); `internal/observability/metrics.go`, `internal/observability/module.go` (alterar)
- Testes: `internal/app/queries_integration_test.go`, `internal/app/reconcile_integration_test.go`, `internal/observability/metrics_test.go`

**Interfaces:**
- Consome: `checkLimit`, `decodeCursor`, `encodeCursor` e `domainError` (Tarefa 2); `app.Metrics` (Tarefa 2); `app.UnitOfWork.Snapshot` (M2).
- Produz:
  - `app.NewQueries(reads Repos) *Queries`, com `GetWallet`, `ListLedger(ctx, walletID, cursor string, limit int) (LedgerPage, error)`, `GetTransaction` e `GetTransactionByExternalID`;
  - `app.LedgerPage{Entries []wallet.LedgerEntry; NextCursor string}`;
  - `app.NewReconcile(uow UnitOfWork, metrics Metrics, log *slog.Logger) *Reconcile` e `(*Reconcile).Execute(ctx, walletID, correlationID string) (Reconciliation, error)`;
  - `app.Reconciliation{WalletID string; Stored, Calculated, Difference money.Money; Consistent bool; CheckedEntries int64}`;
  - `observability.NewMetrics(reg *prometheus.Registry) *Metrics`, com `(*Metrics).ReconciliationDivergence()`, fornecido pelo módulo `observability`.

- [ ] **Passo 1: criar os stubs**

Criar `internal/app/queries.go`:

```go
package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

var errNotImplemented = errors.New("app: not implemented")

// Queries are the reads of the API (HTTP-02..05), over the pool.
type Queries struct{ reads Repos }

// NewQueries builds the queries over the repositories of the pool.
func NewQueries(reads Repos) *Queries { return &Queries{reads: reads} }

// LedgerPage is one page of the ledger, in version order (D-16).
type LedgerPage struct {
	Entries []wallet.LedgerEntry
	// NextCursor resumes after the last entry; "" on the last page.
	NextCursor string
}

// GetWallet returns the wallet.
func (q *Queries) GetWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	return wallet.Wallet{}, errNotImplemented
}

// ListLedger returns one page of the wallet's ledger.
func (q *Queries) ListLedger(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error) {
	return LedgerPage{}, errNotImplemented
}

// GetTransaction returns the operation by its internal id.
func (q *Queries) GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	return nil, errNotImplemented
}

// GetTransactionByExternalID returns the operation by (providerId, externalTransactionId).
func (q *Queries) GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	return nil, errNotImplemented
}
```

Criar `internal/app/reconcile.go`:

```go
package app

import (
	"context"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Reconciliation compares the stored balance with the ledger (HTTP-07).
type Reconciliation struct {
	WalletID                       string
	Stored, Calculated, Difference money.Money
	Consistent                     bool
	CheckedEntries                 int64
}

// Reconcile rebuilds the balance from the ledger without changing anything.
type Reconcile struct {
	uow     UnitOfWork
	metrics Metrics
	log     *slog.Logger
}

// NewReconcile builds the use case.
func NewReconcile(uow UnitOfWork, metrics Metrics, log *slog.Logger) *Reconcile {
	return &Reconcile{uow: uow, metrics: metrics, log: log}
}

// Execute reconciles one wallet.
func (r *Reconcile) Execute(ctx context.Context, walletID, correlationID string) (Reconciliation, error) {
	return Reconciliation{}, errNotImplemented
}
```

Substituir `internal/observability/metrics.go` (arquivo inteiro):

```go
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewRegistry builds the process registry. The metrics catalog arrives in M7.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Metrics implements app.Metrics with Prometheus counters. M7 adds the rest
// of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct{}

// NewMetrics registers the counters on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics { return &Metrics{} }

// ReconciliationDivergence counts a reconciliation that found a divergence.
func (m *Metrics) ReconciliationDivergence() {}
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/observability/metrics_test.go`:

```go
package observability_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: HTTP-07, OBS-03
func TestMetrics_ReconciliationDivergences(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.ReconciliationDivergence()
	m.ReconciliationDivergence()

	want := `
# HELP reconciliation_divergences_total Reconciliations whose stored balance differs from the balance rebuilt from the ledger.
# TYPE reconciliation_divergences_total counter
reconciliation_divergences_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "reconciliation_divergences_total"); err != nil {
		t.Fatal(err)
	}
}
```

Criar `internal/app/queries_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"fmt"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-02, HTTP-04, HTTP-05
func TestQueries(t *testing.T) {
	t.Parallel()
	q := app.NewQueries(reads())
	w, p := openWallet(t, "100.00"), newProvider()
	bet := process(t, newProcessWager(), w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

	t.Run("wallet", func(t *testing.T) {
		got, err := q.GetWallet(t.Context(), w.ID())
		if err != nil || got.ID() != w.ID() || got.Balance().String() != "90.00" || got.Version() != 2 {
			t.Fatalf("GetWallet = %s %s v%d, %v", got.ID(), got.Balance(), got.Version(), err)
		}
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.GetWallet(t.Context(), id)
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})

	t.Run("transaction by id", func(t *testing.T) {
		got, err := q.GetTransaction(t.Context(), bet.Tx.ID())
		if err != nil || got.ID() != bet.Tx.ID() || got.Status() != wagering.StatusProcessed {
			t.Fatalf("GetTransaction = %v, %v", got, err)
		}
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.GetTransaction(t.Context(), id)
			wantError(t, err, apperrors.KindNotFound, "TRANSACTION_NOT_FOUND")
		}
	})

	t.Run("transaction by external id", func(t *testing.T) {
		got, err := q.GetTransactionByExternalID(t.Context(), p, "bet-1")
		if err != nil || got.ID() != bet.Tx.ID() {
			t.Fatalf("GetTransactionByExternalID = %v, %v", got, err)
		}
		for _, key := range [][2]string{{p, "bet-2"}, {newProvider(), "bet-1"}} {
			_, err := q.GetTransactionByExternalID(t.Context(), key[0], key[1])
			wantError(t, err, apperrors.KindNotFound, "TRANSACTION_NOT_FOUND")
		}
	})
}

// Covers: HTTP-03, LED-06
func TestListLedger(t *testing.T) {
	t.Parallel()
	q := app.NewQueries(reads())
	w, p := openWallet(t, "100.00"), newProvider()
	pw := newProcessWager()
	for i := range 4 {
		process(t, pw, w, op{provider: p, kind: "BET", amount: "1.00", ext: fmt.Sprintf("bet-%d", i)})
	}

	t.Run("pages follow the version with an opaque cursor", func(t *testing.T) {
		var versions []int64
		cursor, pages := "", 0
		for {
			page, err := q.ListLedger(t.Context(), w.ID(), cursor, 2)
			if err != nil {
				t.Fatalf("ListLedger(%q): %v", cursor, err)
			}
			pages++
			for _, e := range page.Entries {
				versions = append(versions, e.WalletVersion())
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if pages != 3 || fmt.Sprint(versions) != "[1 2 3 4 5]" {
			t.Fatalf("%d pages with versions %v, want 3 pages with [1 2 3 4 5]", pages, versions)
		}
	})

	t.Run("a full last page has no next cursor", func(t *testing.T) {
		page, err := q.ListLedger(t.Context(), w.ID(), "", 5)
		if err != nil || len(page.Entries) != 5 || page.NextCursor != "" {
			t.Fatalf("ListLedger limit 5 = %d entries, next %q, %v", len(page.Entries), page.NextCursor, err)
		}
	})

	t.Run("invalid parameters are rejected before the wallet is read", func(t *testing.T) {
		_, err := q.ListLedger(t.Context(), newID(), "", 0)
		wantInvalid(t, err, wagering.InputInvalidField, "limit")
		_, err = q.ListLedger(t.Context(), newID(), "not-a-cursor", 10)
		wantInvalid(t, err, wagering.InputInvalidField, "cursor")
	})

	t.Run("unknown wallet", func(t *testing.T) {
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.ListLedger(t.Context(), id, "", 10)
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})
}
```

Criar `internal/app/reconcile_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

type countingMetrics struct{ divergences atomic.Int32 }

func (m *countingMetrics) ReconciliationDivergence() { m.divergences.Add(1) }

// shiftBalance changes the stored balance behind the ledger's back, with the
// wallet triggers disabled only inside this transaction: ALTER TABLE holds an
// ACCESS EXCLUSIVE lock until the commit, so no parallel test ever sees them
// disabled. The context is detached: it also runs in t.Cleanup.
func shiftBalance(t *testing.T, walletID string, deltaMinor int64) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	tx, err := env.Owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, sql := range []string{
		`ALTER TABLE wallets DISABLE TRIGGER USER`,
		`UPDATE wallets SET balance_minor = balance_minor + $1 WHERE id = $2`,
		`ALTER TABLE wallets ENABLE TRIGGER USER`,
	} {
		var args []any
		if strings.HasPrefix(sql, "UPDATE") {
			args = []any{deltaMinor, walletID}
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Covers: HTTP-07, LED-06
func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("consistent wallet", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		process(t, newProcessWager(), w, op{provider: p, kind: "BET", amount: "25.00", ext: "bet-1"})
		metrics := &countingMetrics{}

		got, err := app.NewReconcile(newUoW(), metrics, slog.New(slog.DiscardHandler)).Execute(t.Context(), w.ID(), "corr")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.WalletID != w.ID() || got.Stored.String() != "75.00" || got.Calculated.String() != "75.00" ||
			got.Difference.String() != "0.00" || !got.Consistent || got.CheckedEntries != 2 || metrics.divergences.Load() != 0 {
			t.Fatalf("reconciliation = %+v, divergences %d", got, metrics.divergences.Load())
		}
	})

	t.Run("divergence is reported without changing the balance", func(t *testing.T) {
		t.Parallel()
		w := openWallet(t, "100.00")
		shiftBalance(t, w.ID(), -500)
		t.Cleanup(func() { shiftBalance(t, w.ID(), 500) }) // runs before the ledger check
		metrics := &countingMetrics{}
		var logs bytes.Buffer

		got, err := app.NewReconcile(newUoW(), metrics, slog.New(slog.NewJSONHandler(&logs, nil))).Execute(t.Context(), w.ID(), "corr-reconcile")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if got.Stored.String() != "95.00" || got.Calculated.String() != "100.00" || got.Difference.String() != "-5.00" ||
			got.Consistent || got.CheckedEntries != 1 {
			t.Fatalf("reconciliation = %+v", got)
		}
		if metrics.divergences.Load() != 1 {
			t.Fatalf("divergences = %d, want 1", metrics.divergences.Load())
		}
		line := logs.String()
		for _, want := range []string{`"level":"WARN"`, w.ID(), "corr-reconcile", `"difference":"-5.00"`} {
			if !strings.Contains(line, want) {
				t.Fatalf("log %s lacks %s", line, want)
			}
		}
		wantWallet(t, w.ID(), "95.00", 1)
	})

	t.Run("unknown wallet", func(t *testing.T) {
		t.Parallel()
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := app.NewReconcile(newUoW(), &countingMetrics{}, slog.New(slog.DiscardHandler)).Execute(t.Context(), id, "corr")
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})
}
```

```bash
go mod tidy
```

O `testutil` do Prometheus acrescenta `github.com/kylelemons/godebug v1.1.0 // indirect` ao `go.mod`.

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet -tags=integration ./internal/app/ ./internal/observability/ && go test -race -count=1 ./internal/observability/; go test -tags=integration -race -count=1 -run 'TestQueries|TestListLedger|TestReconcile' ./internal/app/
```

Esperado: FAIL.
- `TestMetrics_ReconciliationDivergences`: a métrica não está registrada, e o diff mostra as três linhas esperadas.
- `TestQueries`, `TestListLedger` e `TestReconcile`: `app: not implemented`.

O subteste da divergência também reporta, no `Cleanup`, a carteira com o saldo deslocado. É o sinal de que o `shiftBalance` agiu.

- [ ] **Passo 4: implementar**

Substituir `internal/app/queries.go` (arquivo inteiro):

```go
package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Queries are the reads of the API (HTTP-02..05), over the pool.
type Queries struct{ reads Repos }

// NewQueries builds the queries over the repositories of the pool.
func NewQueries(reads Repos) *Queries { return &Queries{reads: reads} }

// LedgerPage is one page of the ledger, in version order (D-16).
type LedgerPage struct {
	Entries []wallet.LedgerEntry
	// NextCursor resumes after the last entry; "" on the last page.
	NextCursor string
}

// GetWallet returns the wallet, or KindNotFound WALLET_NOT_FOUND (also for an
// id that is not a UUID).
func (q *Queries) GetWallet(ctx context.Context, id string) (wallet.Wallet, error) {
	w, err := q.reads.Wallets().Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return wallet.Wallet{}, apperrors.New(apperrors.KindNotFound, CodeWalletNotFound, err)
	}
	return w, err
}

// ListLedger returns up to limit entries after the cursor ("" = from the
// start). The parameters are checked before the wallet is read, and the wallet
// before the ledger, so a malformed id never reaches the ledger query.
func (q *Queries) ListLedger(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error) {
	if err := checkLimit(limit); err != nil {
		return LedgerPage{}, err
	}
	var after int64
	if cursor != "" {
		var err error
		if after, err = decodeCursor(cursor); err != nil {
			return LedgerPage{}, err
		}
	}
	w, err := q.GetWallet(ctx, walletID)
	if err != nil {
		return LedgerPage{}, err
	}
	entries, err := q.reads.Ledger().List(ctx, w.ID(), after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	if len(entries) <= limit {
		return LedgerPage{Entries: entries}, nil
	}
	entries = entries[:limit]
	return LedgerPage{Entries: entries, NextCursor: encodeCursor(entries[limit-1].WalletVersion())}, nil
}

// GetTransaction returns the operation by its internal id, or KindNotFound
// TRANSACTION_NOT_FOUND. Whether the caller may see it is decided by the edge.
func (q *Queries) GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	tx, err := q.reads.Transactions().Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, apperrors.New(apperrors.KindNotFound, CodeTransactionNotFound, err)
	}
	return tx, err
}

// GetTransactionByExternalID returns the operation by (providerId,
// externalTransactionId), or KindNotFound TRANSACTION_NOT_FOUND.
func (q *Queries) GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	tx, err := q.reads.Transactions().FindByExternalID(ctx, providerID, externalID)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, apperrors.New(apperrors.KindNotFound, CodeTransactionNotFound, ErrNotFound)
	}
	return tx, nil
}
```

Substituir `internal/app/reconcile.go` (arquivo inteiro):

```go
package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Reconciliation compares the stored balance with the ledger (HTTP-07).
type Reconciliation struct {
	WalletID                       string
	Stored, Calculated, Difference money.Money
	Consistent                     bool
	CheckedEntries                 int64
}

// Reconcile rebuilds the balance from the ledger without changing anything.
type Reconcile struct {
	uow     UnitOfWork
	metrics Metrics
	log     *slog.Logger
}

// NewReconcile builds the use case.
func NewReconcile(uow UnitOfWork, metrics Metrics, log *slog.Logger) *Reconcile {
	return &Reconcile{uow: uow, metrics: metrics, log: log}
}

// Execute reads the wallet and the ledger sum in one REPEATABLE READ READ ONLY
// snapshot (D-16) and returns difference = stored − calculated. A divergence
// is logged and counted, never corrected.
func (r *Reconcile) Execute(ctx context.Context, walletID, correlationID string) (Reconciliation, error) {
	var out Reconciliation
	err := r.uow.Snapshot(ctx, func(repos Repos) error {
		w, err := repos.Wallets().Get(ctx, walletID)
		if errors.Is(err, ErrNotFound) {
			return apperrors.New(apperrors.KindNotFound, CodeWalletNotFound, err)
		}
		if err != nil {
			return err
		}
		sum, err := repos.Ledger().Sum(ctx, w.ID())
		if err != nil {
			return err
		}
		calculated, err := money.FromMinor(sum.NetMinor, w.Currency())
		if err != nil {
			return domainError(err)
		}
		difference, err := w.Balance().Sub(calculated)
		if err != nil {
			return domainError(err)
		}
		out = Reconciliation{
			WalletID: w.ID(), Stored: w.Balance(), Calculated: calculated, Difference: difference,
			Consistent: difference.Sign() == 0, CheckedEntries: sum.Entries,
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, err
	}
	if !out.Consistent {
		r.log.WarnContext(ctx, "reconciliation divergence",
			"walletId", out.WalletID, "correlationId", correlationID,
			"storedBalance", out.Stored.String(), "calculatedBalance", out.Calculated.String(),
			"difference", out.Difference.String())
		r.metrics.ReconciliationDivergence()
	}
	return out, nil
}
```

Substituir `internal/observability/metrics.go` (arquivo inteiro):

```go
package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// NewRegistry builds the process registry. The metrics catalog arrives in M7.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// Metrics implements app.Metrics with Prometheus counters. M7 adds the rest
// of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct {
	reconciliationDivergences prometheus.Counter
}

// NewMetrics registers the counters on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
	}
	reg.MustRegister(m.reconciliationDivergences)
	return m
}

// ReconciliationDivergence counts a reconciliation that found a divergence.
func (m *Metrics) ReconciliationDivergence() { m.reconciliationDivergences.Inc() }
```

Em `internal/observability/module.go`, trocar:

```go
		NewLogger,
		NewRegistry,
```

por:

```go
		NewLogger,
		NewRegistry,
		NewMetrics,
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/observability/ ./internal/bootstrap/ && go test -tags=integration -race -count=1 ./internal/app/
```

Esperado: `ok` nos três pacotes.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/app/` e `go test -race -count=1 ./internal/observability/` verdes.

---

### Tarefa 7: `auth`: verificador OIDC, matriz D-07 e fail fast do JWKS (U15, U16)

A spec §5 (decisões 13, 14 e 15):
- **`Verifier`:**
  - `oidc.NewRemoteKeySet(OIDC_JWKS_URL)` + `oidc.NewVerifier(OIDC_ISSUER)`, sem discovery;
  - só RS256;
  - `aud` = `OIDC_AUDIENCE`;
  - `exp` com tolerância aplicada pelo relógio do verificador.
- **`Principal`:** montado a partir de `sub`, `azp`, `provider_id` e `resource_access.<audience>.roles`.
- **Matriz de permissões:** funções puras.
- **Módulo Fx:** busca o JWKS no start, com um cliente HTTP próprio, fechado no stop.

Os testes usam um JWKS local (`httptest`) e tokens assinados com `go-jose`. O Keycloak real entra na Tarefa 13.

**Arquivos:**
- Implementação: `internal/auth/principal.go`, `policy.go`, `verifier.go`, `module.go` (criar)
- Testes: `internal/auth/policy_test.go`, `verifier_test.go`, `module_test.go`

**Interfaces:**
- Consome: `config.Config` (Tarefa 1).
- Produz:
  - `auth.Role`, com `RoleProvider` e `RoleWalletInternal`;
  - `auth.Principal{Subject, ClientID, ProviderID string; Roles []Role}`, com `WithPrincipal` e `FromContext`;
  - as funções `HasRole`, `HasAnyRole`, `ActsAs` e `CanSeeTransaction`;
  - `auth.ErrUnauthenticated`;
  - `auth.NewVerifier(cfg config.Config, client *http.Client) *Verifier`, com `Authenticate(ctx, raw) (Principal, error)` e `CheckKeys(ctx) error`;
  - `auth.Module`, que fornece `*auth.Verifier`.

- [ ] **Passo 1: criar os tipos e os stubs**

Criar `internal/auth/principal.go`:

```go
// Package auth verifies the bearer tokens of the IdP and holds the permission
// matrix of D-07. It knows neither the use cases nor HTTP routes.
package auth

import "context"

// Role is a client role of the resource server (pda-api) in the token.
type Role string

const (
	RoleProvider       Role = "provider"
	RoleWalletInternal Role = "wallet-internal"
)

// Principal is the authenticated caller. ProviderID comes from the
// provider_id claim, never from the request (AUTH-04).
type Principal struct {
	Subject    string
	ClientID   string
	ProviderID string
	Roles      []Role
}

type principalKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// FromContext returns the principal of an authenticated request.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}
```

Criar `internal/auth/policy.go`:

```go
package auth

// HasRole reports whether p holds role.
func HasRole(p Principal, role Role) bool { return false }

// HasAnyRole reports whether p holds at least one of roles.
func HasAnyRole(p Principal, roles ...Role) bool { return false }

// ActsAs reports whether p is the provider providerID.
func ActsAs(p Principal, providerID string) bool { return false }

// CanSeeTransaction reports whether p may read an operation of txProviderID.
func CanSeeTransaction(p Principal, txProviderID string) bool { return false }
```

Criar `internal/auth/verifier.go`:

```go
package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/KaioVinicios/pda/internal/config"
)

// ErrUnauthenticated reports a missing, malformed, forged or expired token.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

var errNotImplemented = errors.New("auth: not implemented")

// Verifier checks bearer tokens against the IdP keys (D-07).
type Verifier struct{}

// NewVerifier builds the verifier; client fetches the keys.
func NewVerifier(cfg config.Config, client *http.Client) *Verifier { return &Verifier{} }

// Authenticate verifies raw and returns its principal.
func (v *Verifier) Authenticate(ctx context.Context, raw string) (Principal, error) {
	return Principal{}, errNotImplemented
}

// CheckKeys fetches the key set once.
func (v *Verifier) CheckKeys(ctx context.Context) error { return nil }
```

Criar `internal/auth/module.go`:

```go
package auth

import (
	"net/http"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
)

// Module provides the Verifier.
var Module = fx.Module("auth", fx.Provide(func(cfg config.Config) *Verifier { return NewVerifier(cfg, http.DefaultClient) }))
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/auth/policy_test.go`:

```go
package auth_test

import (
	"context"
	"testing"

	"github.com/KaioVinicios/pda/internal/auth"
)

var (
	providerA = auth.Principal{Subject: "a", ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}
	providerB = auth.Principal{Subject: "b", ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}
	internal  = auth.Principal{Subject: "w", Roles: []auth.Role{auth.RoleWalletInternal}}
	noRole    = auth.Principal{Subject: "n"}
	// A provider role without the provider_id claim is a misconfigured client.
	unnamed = auth.Principal{Subject: "u", Roles: []auth.Role{auth.RoleProvider}}
)

// Covers: AUTH-04, AUTH-05, AUTH-06 (U15)
func TestAuthPolicy(t *testing.T) {
	t.Run("roles", func(t *testing.T) {
		cases := []struct {
			p     auth.Principal
			roles []auth.Role
			want  bool
		}{
			{providerA, []auth.Role{auth.RoleProvider}, true},
			{providerA, []auth.Role{auth.RoleWalletInternal}, false},
			{internal, []auth.Role{auth.RoleWalletInternal}, true},
			{internal, []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}, true},
			{noRole, []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}, false},
			{unnamed, []auth.Role{auth.RoleProvider}, false},
			{providerA, nil, false},
		}
		for _, tc := range cases {
			if got := auth.HasAnyRole(tc.p, tc.roles...); got != tc.want {
				t.Errorf("HasAnyRole(%s, %v) = %v, want %v", tc.p.Subject, tc.roles, got, tc.want)
			}
		}
		if !auth.HasRole(internal, auth.RoleWalletInternal) || auth.HasRole(unnamed, auth.RoleProvider) {
			t.Error("HasRole disagrees with HasAnyRole")
		}
	})

	t.Run("acting as a provider", func(t *testing.T) {
		cases := []struct {
			p        auth.Principal
			provider string
			want     bool
		}{
			{providerA, "provider-a", true},
			{providerA, "provider-b", false},
			{providerB, "provider-a", false},
			{internal, "provider-a", false},
			{unnamed, "", false},
			{noRole, "", false},
		}
		for _, tc := range cases {
			if got := auth.ActsAs(tc.p, tc.provider); got != tc.want {
				t.Errorf("ActsAs(%s, %q) = %v, want %v", tc.p.Subject, tc.provider, got, tc.want)
			}
		}
	})

	t.Run("seeing an operation", func(t *testing.T) {
		cases := []struct {
			p        auth.Principal
			provider string // "" = the internal OPENING
			want     bool
		}{
			{providerA, "provider-a", true},
			{providerA, "provider-b", false},
			{providerA, "", false},
			{internal, "provider-a", true},
			{internal, "", true},
			{unnamed, "", false},
			{noRole, "provider-a", false},
		}
		for _, tc := range cases {
			if got := auth.CanSeeTransaction(tc.p, tc.provider); got != tc.want {
				t.Errorf("CanSeeTransaction(%s, %q) = %v, want %v", tc.p.Subject, tc.provider, got, tc.want)
			}
		}
	})

	t.Run("principal in the context", func(t *testing.T) {
		if _, ok := auth.FromContext(context.Background()); ok {
			t.Fatal("FromContext of a bare context reports a principal")
		}
		got, ok := auth.FromContext(auth.WithPrincipal(context.Background(), providerA))
		if !ok || got.ProviderID != "provider-a" {
			t.Fatalf("FromContext = %+v, %v", got, ok)
		}
	})
}
```

Criar `internal/auth/verifier_test.go`:

```go
package auth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
)

const (
	issuer   = "http://idp.test/realms/pda"
	audience = "pda-api"
)

// idp serves a JWKS with one RSA key and signs tokens with it.
type idp struct {
	key    *rsa.PrivateKey
	server *httptest.Server
	status int // answer of the JWKS endpoint; 0 = 200
	keys   []jose.JSONWebKey
}

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &idp{key: key}
	i.keys = []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}
	i.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if i.status != 0 {
			w.WriteHeader(i.status)
			return
		}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: i.keys})
	}))
	t.Cleanup(i.server.Close)
	return i
}

func (i *idp) verifier(skew time.Duration) *auth.Verifier {
	return auth.NewVerifier(config.Config{
		OIDCIssuer: issuer, OIDCJWKSURL: i.server.URL, OIDCAudience: audience, OIDCClockSkew: skew,
	}, i.server.Client())
}

// claims of a provider-a token that expires in 5 minutes.
func claims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": issuer, "aud": audience, "sub": "service-account-a", "azp": "provider-a",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "provider_id": "provider-a",
		"resource_access": map[string]any{audience: map[string]any{"roles": []string{"provider"}}},
	}
}

func sign(t *testing.T, alg jose.SignatureAlgorithm, key any, kid string, c map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: jose.JSONWebKey{Key: key, KeyID: kid}}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(c).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// unsigned is a token with alg "none".
func unsigned(t *testing.T, c map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "."
}

// with sets one claim.
func with(c map[string]any, key string, value any) map[string]any {
	c[key] = value
	return c
}

// Covers: AUTH-02, AUTH-04 (U16)
func TestVerifier(t *testing.T) {
	i := newIDP(t)
	v := i.verifier(30 * time.Second)
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("accepts a valid token and reads the principal", func(t *testing.T) {
		p, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", claims()))
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if p.Subject != "service-account-a" || p.ClientID != "provider-a" || p.ProviderID != "provider-a" ||
			!slices.Equal(p.Roles, []auth.Role{auth.RoleProvider}) {
			t.Fatalf("principal = %+v", p)
		}
	})

	t.Run("reads the roles of the audience only", func(t *testing.T) {
		c := with(with(claims(), "provider_id", nil), "resource_access", map[string]any{
			audience: map[string]any{"roles": []string{"wallet-internal"}},
			"other":  map[string]any{"roles": []string{"provider"}},
		})
		p, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c))
		if err != nil || p.ProviderID != "" || !slices.Equal(p.Roles, []auth.Role{auth.RoleWalletInternal}) {
			t.Fatalf("principal = %+v, %v", p, err)
		}
	})

	t.Run("accepts a token expired within the clock skew", func(t *testing.T) {
		c := with(claims(), "exp", time.Now().Add(-10*time.Second).Unix())
		if _, err := v.Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c)); err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
	})

	rejected := map[string]string{
		"malformed":               "not-a-jwt",
		"empty":                   "",
		"signed by another key":   sign(t, jose.RS256, other, "k1", claims()),
		"unknown key id":          sign(t, jose.RS256, other, "k2", claims()),
		"alg none":                unsigned(t, claims()),
		"HS256":                   sign(t, jose.HS256, []byte("a-shared-secret-of-at-least-32-bytes"), "k1", claims()),
		"another issuer":          sign(t, jose.RS256, i.key, "k1", with(claims(), "iss", "http://idp.test/realms/other")),
		"another audience":        sign(t, jose.RS256, i.key, "k1", with(claims(), "aud", "account")),
		"expired beyond the skew": sign(t, jose.RS256, i.key, "k1", with(claims(), "exp", time.Now().Add(-40*time.Second).Unix())),
	}
	for name, raw := range rejected {
		t.Run("rejects "+name, func(t *testing.T) {
			if _, err := v.Authenticate(t.Context(), raw); !errors.Is(err, auth.ErrUnauthenticated) {
				t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
			}
		})
	}

	t.Run("rejects an expired token when the skew is zero", func(t *testing.T) {
		c := with(claims(), "exp", time.Now().Add(-time.Second).Unix())
		if _, err := i.verifier(0).Authenticate(t.Context(), sign(t, jose.RS256, i.key, "k1", c)); !errors.Is(err, auth.ErrUnauthenticated) {
			t.Fatalf("Authenticate = %v, want ErrUnauthenticated", err)
		}
	})
}

// Covers: FX-02
func TestVerifierCheckKeys(t *testing.T) {
	t.Run("a key set with an RSA key", func(t *testing.T) {
		if err := newIDP(t).verifier(0).CheckKeys(t.Context()); err != nil {
			t.Fatalf("CheckKeys = %v", err)
		}
	})
	t.Run("an error answer", func(t *testing.T) {
		i := newIDP(t)
		i.status = http.StatusServiceUnavailable
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
	t.Run("an empty key set", func(t *testing.T) {
		i := newIDP(t)
		i.keys = nil
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
	t.Run("an unreachable endpoint", func(t *testing.T) {
		i := newIDP(t)
		i.server.Close()
		if err := i.verifier(0).CheckKeys(t.Context()); err == nil {
			t.Fatal("CheckKeys = nil, want an error")
		}
	})
}
```

Criar `internal/auth/module_test.go`:

```go
package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
)

func moduleApp(t *testing.T, jwksURL string) *fxtest.App {
	t.Helper()
	cfg := config.Config{OIDCIssuer: issuer, OIDCJWKSURL: jwksURL, OIDCAudience: audience}
	return fxtest.New(t, fx.NopLogger, fx.Supply(cfg), auth.Module, fx.Invoke(func(*auth.Verifier) {}))
}

// Covers: FX-02, AUTH-02
func TestModule(t *testing.T) {
	t.Run("starts with the key set", func(t *testing.T) {
		app := moduleApp(t, newIDP(t).server.URL)
		app.RequireStart()
		app.RequireStop()
	})

	t.Run("fails to start without the key set", func(t *testing.T) {
		i := newIDP(t)
		i.status = http.StatusServiceUnavailable
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := moduleApp(t, i.server.URL).Start(ctx)
		if err == nil || !strings.Contains(err.Error(), "JWKS") {
			t.Fatalf("Start() = %v, want a JWKS error", err)
		}
	})
}
```

```bash
go get github.com/go-jose/go-jose/v4@v4.1.4 && go mod tidy
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet ./internal/auth/ && go test -race -count=1 ./internal/auth/
```

Esperado: FAIL.
- `TestAuthPolicy`: todos os casos que esperam `true`.
- `TestVerifier`: o stub devolve `auth: not implemented`, que não é `ErrUnauthenticated`.
- `TestVerifierCheckKeys`: os três casos de erro recebem `nil`.
- `TestModule/fails_to_start_without_the_key_set`.

- [ ] **Passo 4: implementar**

Substituir `internal/auth/policy.go` (arquivo inteiro):

```go
package auth

import "slices"

// HasRole reports whether p holds role. The provider role only counts with
// the provider_id claim: a provider without a name is never authorized
// (spec decision 14).
func HasRole(p Principal, role Role) bool {
	if role == RoleProvider && p.ProviderID == "" {
		return false
	}
	return slices.Contains(p.Roles, role)
}

// HasAnyRole reports whether p holds at least one of roles.
func HasAnyRole(p Principal, roles ...Role) bool {
	return slices.ContainsFunc(roles, func(r Role) bool { return HasRole(p, r) })
}

// ActsAs reports whether p is the provider providerID: the body of POST
// /wagering/transactions and the path of /providers/{providerId}/… must name
// the provider of the token (D-07).
func ActsAs(p Principal, providerID string) bool {
	return HasRole(p, RoleProvider) && p.ProviderID == providerID
}

// CanSeeTransaction reports whether p may read an operation of txProviderID
// ("" for the internal OPENING): the internal service sees every operation, a
// provider only its own. The edge answers 404 otherwise, so the id is not
// revealed.
func CanSeeTransaction(p Principal, txProviderID string) bool {
	return HasRole(p, RoleWalletInternal) || txProviderID != "" && ActsAs(p, txProviderID)
}
```

Substituir `internal/auth/verifier.go` (arquivo inteiro):

```go
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/KaioVinicios/pda/internal/config"
)

// ErrUnauthenticated reports a missing, malformed, forged or expired token.
// The cause stays in the chain for debug logs; the answer never tells which.
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// maxJWKSBytes bounds the key set read by CheckKeys.
const maxJWKSBytes = 1 << 20

// Verifier checks bearer tokens against the IdP keys (D-07): RS256 only, the
// signature by the JWKS (cached, refetched for an unknown kid), iss, aud and
// exp with a tolerance of OIDC_CLOCK_SKEW. There is no discovery: inside the
// compose network the discovery document names another issuer (spike).
type Verifier struct {
	verifier *oidc.IDTokenVerifier
	jwksURL  string
	audience string
	client   *http.Client
}

// NewVerifier builds the verifier; client fetches the keys. go-oidc compares
// exp without any tolerance, so the skew is applied by moving its clock back
// (spec decision 13); nbf keeps the library's fixed 5 minutes.
func NewVerifier(cfg config.Config, client *http.Client) *Verifier {
	keys := oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), client), cfg.OIDCJWKSURL)
	skew := cfg.OIDCClockSkew
	return &Verifier{
		verifier: oidc.NewVerifier(cfg.OIDCIssuer, keys, &oidc.Config{
			ClientID:             cfg.OIDCAudience,
			SupportedSigningAlgs: []string{oidc.RS256},
			Now:                  func() time.Time { return time.Now().Add(-skew) },
		}),
		jwksURL:  cfg.OIDCJWKSURL,
		audience: cfg.OIDCAudience,
		client:   client,
	}
}

// Authenticate verifies raw and returns its principal: the subject, the
// client (azp), the provider_id claim and the client roles granted on the
// audience (resource_access.<audience>.roles).
func (v *Verifier) Authenticate(ctx context.Context, raw string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	var claims struct {
		AuthorizedParty string `json:"azp"`
		ProviderID      string `json:"provider_id"`
		ResourceAccess  map[string]struct {
			Roles []string `json:"roles"`
		} `json:"resource_access"`
	}
	if err := token.Claims(&claims); err != nil {
		return Principal{}, fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	p := Principal{Subject: token.Subject, ClientID: claims.AuthorizedParty, ProviderID: claims.ProviderID}
	for _, r := range claims.ResourceAccess[v.audience].Roles {
		p.Roles = append(p.Roles, Role(r))
	}
	return p, nil
}

// CheckKeys fetches the key set once and requires an RSA key: the fail-fast
// check of the start (FX-02, spec decision 15).
func (v *Verifier) CheckKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return fmt.Errorf("auth: JWKS request: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("auth: fetching the JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("auth: the JWKS endpoint answered %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return fmt.Errorf("auth: decoding the JWKS: %w", err)
	}
	for _, k := range set.Keys {
		if k.Kty == "RSA" {
			return nil
		}
	}
	return errors.New("auth: the JWKS has no RSA key")
}
```

Substituir `internal/auth/module.go` (arquivo inteiro):

```go
package auth

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
)

// jwksTimeout bounds each fetch of the key set.
const jwksTimeout = 5 * time.Second

// Module provides the Verifier. The key set is fetched on start, so an
// unreachable IdP fails the start (FX-02, spec decision 15); the HTTP client
// is the module's own, so its idle connections close on stop (goleak, I07b).
var Module = fx.Module("auth", fx.Provide(newModuleVerifier))

func newModuleVerifier(lc fx.Lifecycle, cfg config.Config) *Verifier {
	client := &http.Client{Timeout: jwksTimeout, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute}}
	v := NewVerifier(cfg, client)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, jwksTimeout)
			defer cancel()
			return v.CheckKeys(ctx)
		},
		OnStop: func(context.Context) error {
			client.CloseIdleConnections()
			return nil
		},
	})
	return v
}
```

```bash
go get github.com/coreos/go-oidc/v3@v3.21.0 && go mod tidy
```

O `go get` do go-oidc fica **depois** do import. Rodado antes, o `go mod tidy` o removeria por não ser usado.

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/auth/
```

Esperado: `ok`.

**Checkpoint:** `go test -race -count=1 ./internal/auth/` verde; o `go.mod` tem `github.com/coreos/go-oidc/v3 v3.21.0` e `github.com/go-jose/go-jose/v4 v4.1.4` diretos, e `golang.org/x/oauth2` indireto.

---

### Tarefa 8: borda HTTP: contrato embutido, `problem+json`, middlewares e tabela de rotas (U17, I15)

A spec §3 e §6.1–§6.3 (decisões 11, 12, 19, 22 e 24):
- **Contrato embutido:** `api/embed.go` e `swagger.html`.
- **`problem+json`:** o catálogo com o detalhe fixo de cada código.
- **`writeError`:** traduz `Kind` e código em status.
- **Middlewares:**
  - de fora para dentro: correlação → log de acesso → recuperação de `panic` → fallback de rota;
  - por rota: autenticação e role, e o corpo JSON.
- **Tabela de rotas:** uma única, que registra no `ServeMux` e alimenta o I15. Já nasce com **as 9 rotas mais os docs**; as de negócio respondem 501 até as Tarefas 11 e 12.
- **Módulo Fx:** já fornece o `http.Handler` do `New`, por ora só com o health.

**Arquivos:**
- Implementação:
  - `api/embed.go`, `api/swagger.html` (criar);
  - `internal/adapters/httpapi/handler.go`, `problem.go`, `status.go`, `middleware.go`, `docs_handler.go`, `placeholder_handlers.go` (criar);
  - `routes.go`, `module.go`, `server.go` (substituir);
  - `internal/bootstrap/bootstrap_test.go` (alterar).
- Testes: `internal/adapters/httpapi/stubs_test.go`, `edge_test.go`, `contract_test.go`, `status_test.go` (interno) e `health_handler_test.go` (alterar).

**Interfaces:**
- Consome:
  - `auth.Principal`, `auth.ErrUnauthenticated` e `auth.HasAnyRole` (Tarefa 7);
  - `app.ProcessRequest`, `ProcessResult`, `OpenWalletInput`, `LedgerPage` e `Reconciliation` (Tarefas 3–6);
  - `observability.Health` (M0).
- Produz:
  - as interfaces `httpapi.Authenticator`, `WagerProcessor`, `WalletOpener`, `Reader` e `Reconciler`;
  - `httpapi.Services{Auth, Wagers, Wallets, Queries, Reconcile, Health}` e `httpapi.Options{DocsEnabled bool; Log *slog.Logger}`;
  - `httpapi.New(Options, Services) http.Handler` e `httpapi.Routes(docsEnabled bool) []string`;
  - os internos `writeProblem`, `writeError`, `resultStatus`, `correlationID` e `handlers` (métodos `openWallet`, `getWallet`, `listLedger`, `reconcile`, `submitWager`, `getTransaction`, `getTransactionByExternalID`);
  - `api.OpenAPI` e `api.SwaggerHTML`.

- [ ] **Passo 1: criar o contrato embutido e os stubs**

Criar `api/embed.go`:

```go
// Package api embeds the HTTP contract (D-20): api/openapi.yaml is the single
// source of the contract, served at /openapi.yaml and used by the tests.
package api

import _ "embed" // go:embed

// OpenAPI is the OpenAPI 3.0.3 document of the API.
//
//go:embed openapi.yaml
var OpenAPI []byte

// SwaggerHTML is the page served at /docs.
//
//go:embed swagger.html
var SwaggerHTML []byte
```

Criar `api/swagger.html`:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>PDA API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.33.0/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.33.0/swagger-ui-bundle.js" crossorigin="anonymous"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: "/openapi.yaml",
        dom_id: "#swagger-ui",
        persistAuthorization: true,
      });
    };
  </script>
</body>
</html>
```

Criar `internal/adapters/httpapi/handler.go`:

```go
package httpapi

import (
	"context"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Authenticator verifies bearer tokens (auth.Verifier).
type Authenticator interface {
	Authenticate(ctx context.Context, raw string) (auth.Principal, error)
}

// WagerProcessor processes an operation (app.ProcessWager).
type WagerProcessor interface {
	Execute(ctx context.Context, req app.ProcessRequest) (app.ProcessResult, error)
}

// WalletOpener opens a wallet (app.OpenWallet).
type WalletOpener interface {
	Execute(ctx context.Context, in app.OpenWalletInput, correlationID string) (wallet.Wallet, error)
}

// Reader answers the queries (app.Queries).
type Reader interface {
	GetWallet(ctx context.Context, id string) (wallet.Wallet, error)
	ListLedger(ctx context.Context, walletID, cursor string, limit int) (app.LedgerPage, error)
	GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)
}

// Reconciler reconciles a wallet (app.Reconcile).
type Reconciler interface {
	Execute(ctx context.Context, walletID, correlationID string) (app.Reconciliation, error)
}

// Services are what the routes call. The handlers depend on these small
// interfaces, satisfied by the use cases and by stubs in the unit tests.
type Services struct {
	Auth      Authenticator
	Wagers    WagerProcessor
	Wallets   WalletOpener
	Queries   Reader
	Reconcile Reconciler
	Health    *observability.Health
}

// Options configure the handler.
type Options struct {
	DocsEnabled bool
	Log         *slog.Logger
}

// handlers are the business routes.
type handlers struct {
	s   Services
	log *slog.Logger
}
```

Substituir `internal/adapters/httpapi/routes.go` (arquivo inteiro):

```go
package httpapi

import "net/http"

// New returns the API handler.
func New(opts Options, s Services) http.Handler {
	mux := http.NewServeMux()
	hh := healthHandler{health: s.Health}
	mux.HandleFunc("GET /health/live", hh.live)
	mux.HandleFunc("GET /health/ready", hh.ready)
	return mux
}

// Routes lists the "METHOD /path" patterns New registers (I15).
func Routes(docsEnabled bool) []string { return nil }
```

Criar `internal/adapters/httpapi/status.go`:

```go
package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// writeError answers an error of a use case.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {}

// resultStatus is the status of a recorded operation.
func resultStatus(s wagering.Status) int { return 0 }
```

Substituir `internal/adapters/httpapi/module.go` (arquivo inteiro):

```go
package httpapi

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the API handler and starts the API server.
var Module = fx.Module("httpapi", fx.Provide(newHandler), fx.Invoke(RegisterServer))

func newHandler(cfg config.Config, log *slog.Logger, h *observability.Health) http.Handler {
	return New(Options{DocsEnabled: cfg.APIDocsEnabled, Log: log}, Services{Health: h})
}
```

Substituir `internal/adapters/httpapi/server.go` (arquivo inteiro):

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// RegisterServer runs the API server on HTTP_ADDR. It is registered last, so it
// starts after every dependency and stops first (D-15).
func RegisterServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, log *slog.Logger) {
	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	observability.ServeOnLifecycle(lc, srv, cfg.ShutdownTimeout, log, "api")
}
```

Substituir `internal/bootstrap/bootstrap_test.go` (arquivo inteiro):

```go
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
		handler http.Handler
		uow    app.UnitOfWork
		repos  app.Repos
	)
	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &handler, &uow, &repos))
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("fx.ValidateApp() = %v", err)
	}
}
```

Em `internal/adapters/httpapi/health_handler_test.go`, trocar:

```go
	httpapi.NewMux(h).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
```

por:

```go
	httpapi.New(httpapi.Options{Log: slog.New(slog.DiscardHandler)}, httpapi.Services{Health: h}).
		ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/adapters/httpapi/stubs_test.go`:

```go
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Tokens of stubAuth.
const (
	tokenProviderA = "token-provider-a"
	tokenProviderB = "token-provider-b"
	tokenInternal  = "token-internal"
	tokenNoRole    = "token-no-role"
	tokenPanic     = "token-panic"
)

// stubAuth accepts the tokens above; anything else is unauthenticated.
type stubAuth struct{}

func (stubAuth) Authenticate(_ context.Context, raw string) (auth.Principal, error) {
	switch raw {
	case tokenProviderA:
		return auth.Principal{Subject: "a", ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}, nil
	case tokenProviderB:
		return auth.Principal{Subject: "b", ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}, nil
	case tokenInternal:
		return auth.Principal{Subject: "w", Roles: []auth.Role{auth.RoleWalletInternal}}, nil
	case tokenNoRole:
		return auth.Principal{Subject: "n"}, nil
	case tokenPanic:
		panic("stub authenticator panic")
	}
	return auth.Principal{}, auth.ErrUnauthenticated
}

// syncBuffer collects log lines written by concurrent handlers.
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

// edge is the handler under test with its captured logs.
type edge struct {
	handler http.Handler
	logs    *syncBuffer
}

func newEdge(t *testing.T, s httpapi.Services, docs bool) edge {
	t.Helper()
	logs := &syncBuffer{}
	if s.Auth == nil {
		s.Auth = stubAuth{}
	}
	if s.Health == nil {
		s.Health = observability.NewHealth(slog.New(slog.DiscardHandler), nil, time.Second)
	}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return edge{handler: httpapi.New(httpapi.Options{DocsEnabled: docs, Log: log}, s), logs: logs}
}

// call sends one request; body is sent as is.
type call struct {
	method, path, token, contentType string
	body                             string
	header                           http.Header
}

func (e edge) do(t *testing.T, c call) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), c.method, c.path, bytes.NewBufferString(c.body))
	for k, vs := range c.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.contentType != "" {
		req.Header.Set("Content-Type", c.contentType)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// problem is the application/problem+json body.
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field"`
	CorrelationID string `json:"correlationId"`
}

// wantProblem fails unless rec is the problem code with status (and field
// when not empty), and returns it.
func wantProblem(t *testing.T, rec *httptest.ResponseRecorder, status int, code, field string) problem {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %s)", ct, rec.Body.String())
	}
	var p problem
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body.String(), err)
	}
	if p.Type != "about:blank" || p.Status != status || p.Title != http.StatusText(status) || p.Code != code ||
		p.Detail == "" || p.CorrelationID == "" || p.CorrelationID != rec.Header().Get("X-Correlation-Id") {
		t.Fatalf("problem = %+v, want %d %s", p, status, code)
	}
	if field != "" && p.Field != field {
		t.Fatalf("problem field = %q, want %q", p.Field, field)
	}
	return p
}
```

Criar `internal/adapters/httpapi/edge_test.go`:

```go
package httpapi_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
)

// Covers: HTTP-09, D-18 (U17: correlation)
func TestEdgeCorrelationID(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	cases := []struct {
		name, sent string
		kept       bool
	}{
		{"valid id is kept", "abc-123_X.y", true},
		{"invalid characters are replaced", "bad id!", false},
		{"too long is replaced", strings.Repeat("a", 129), false},
		{"absent is generated", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.sent != "" {
				h.Set("X-Correlation-Id", tc.sent)
			}
			rec := e.do(t, call{method: http.MethodGet, path: "/nope", header: h})
			got := rec.Header().Get("X-Correlation-Id")
			switch {
			case tc.kept && got != tc.sent:
				t.Fatalf("correlation = %q, want %q", got, tc.sent)
			case !tc.kept && (got == "" || got == tc.sent || len(got) != 36):
				t.Fatalf("correlation = %q, want a generated UUID", got)
			}
			wantProblem(t, rec, http.StatusNotFound, "ROUTE_NOT_FOUND", "")
		})
	}
}

// Covers: HTTP-09 (U17: route fallback)
func TestEdgeRouteFallback(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/nope"}), http.StatusNotFound, "ROUTE_NOT_FOUND", "")
	rec := e.do(t, call{method: http.MethodDelete, path: "/wallets"})
	wantProblem(t, rec, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "")
	if allow := rec.Header().Get("Allow"); allow != "POST" {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/docs"}), http.StatusNotFound, "ROUTE_NOT_FOUND", "")
}

// Covers: AUTH-02, AUTH-06, AUTH-07, AUTH-08 (U17: authentication and roles)
func TestEdgeAuthentication(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)

	t.Run("missing token", func(t *testing.T) {
		rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1"})
		wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda"` {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	})
	t.Run("other scheme counts as missing", func(t *testing.T) {
		rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", header: http.Header{"Authorization": {"Basic dTpw"}}})
		wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
		if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda"` {
			t.Fatalf("WWW-Authenticate = %q", got)
		}
	})
	for _, token := range []string{"forged", " "} {
		t.Run("invalid token "+token, func(t *testing.T) {
			rec := e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", header: http.Header{"Authorization": {"Bearer " + token}}})
			wantProblem(t, rec, http.StatusUnauthorized, "UNAUTHENTICATED", "")
			if got := rec.Header().Get("WWW-Authenticate"); got != `Bearer realm="pda", error="invalid_token"` {
				t.Fatalf("WWW-Authenticate = %q", got)
			}
		})
	}
	t.Run("role of another kind of caller", func(t *testing.T) {
		for _, c := range []call{
			{method: http.MethodGet, path: "/wallets/w-1", token: tokenProviderA},
			{method: http.MethodPost, path: "/wallets/w-1/reconciliation", token: tokenNoRole},
			{method: http.MethodPost, path: "/wagering/transactions", token: tokenInternal, contentType: "application/json", body: "{}"},
			{method: http.MethodGet, path: "/wagering/transactions/t-1", token: tokenNoRole},
			{method: http.MethodGet, path: "/providers/provider-a/wagering/transactions/e-1", token: tokenNoRole},
		} {
			wantProblem(t, e.do(t, c), http.StatusForbidden, "FORBIDDEN", "")
		}
	})
	t.Run("public routes need no token", func(t *testing.T) {
		for _, path := range []string{"/health/live", "/health/ready"} {
			if rec := e.do(t, call{method: http.MethodGet, path: path}); rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", path, rec.Code)
			}
		}
	})
}

// Covers: HTTP-09 (U17: media type)
func TestEdgeRequiresJSON(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		rec := e.do(t, call{method: http.MethodPost, path: "/wallets", token: tokenInternal, contentType: ct, body: "{}"})
		wantProblem(t, rec, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "")
	}
	// The media type is checked after the credentials (lifecycle §6.1).
	wantProblem(t, e.do(t, call{method: http.MethodPost, path: "/wallets", contentType: "text/plain"}), http.StatusUnauthorized, "UNAUTHENTICATED", "")
}

// Covers: HTTP-09 (U17: panic)
func TestEdgeRecoversPanics(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	wantProblem(t, e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenPanic}), http.StatusInternalServerError, "INTERNAL_ERROR", "")
	if logs := e.logs.String(); !strings.Contains(logs, "stub authenticator panic") || !strings.Contains(logs, `"level":"ERROR"`) {
		t.Fatalf("logs = %s, want the panic at ERROR", logs)
	}
}

// Covers: OBS-01, OBS-02 (U17: access log)
func TestEdgeAccessLog(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, false)
	h := http.Header{"X-Correlation-Id": {"corr-log-1"}}
	e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenProviderA, header: h})
	logs := e.logs.String()
	for _, want := range []string{`"msg":"http request"`, `"method":"GET"`, `"route":"GET /wallets/{walletId}"`, `"status":403`, `"correlationId":"corr-log-1"`, `"providerId":"provider-a"`} {
		if !strings.Contains(logs, want) {
			t.Fatalf("access log %s lacks %s", logs, want)
		}
	}
	if strings.Contains(logs, tokenProviderA) {
		t.Fatalf("access log leaks the token: %s", logs)
	}
}

// Covers: DOC-06, D-20
func TestEdgeDocs(t *testing.T) {
	e := newEdge(t, httpapi.Services{}, true)
	rec := e.do(t, call{method: http.MethodGet, path: "/openapi.yaml"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/yaml" || rec.Body.String() != string(api.OpenAPI) {
		t.Fatalf("GET /openapi.yaml = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = e.do(t, call{method: http.MethodGet, path: "/docs"})
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") ||
		!strings.Contains(rec.Body.String(), "swagger-ui-dist@5.33.0") || !strings.Contains(rec.Body.String(), `url: "/openapi.yaml"`) {
		t.Fatalf("GET /docs = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
```

Criar `internal/adapters/httpapi/contract_test.go`:

```go
package httpapi_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
)

// Covers: HTTP-01..09, DOC-06, D-20 (I15)
//
// The document is valid, examples included, and it describes exactly the
// routes the handler registers: a route added on one side only fails here.
func TestOpenAPIContract(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPI)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := doc.Validate(t.Context(), openapi3.EnableExamplesValidation()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var documented []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	registered := httpapi.Routes(true)
	slices.Sort(documented)
	slices.Sort(registered)
	if !slices.Equal(documented, registered) {
		t.Fatalf("routes differ:\n documented %v\n registered %v", documented, registered)
	}
}
```

Criar `internal/adapters/httpapi/status_test.go`:

```go
package httpapi

// Internal test: writeError and resultStatus are the private mapping of
// spec §6.3, shared by every handler.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-09, D-04, TX-10 (U17: mapping)
func TestWriteError(t *testing.T) {
	input := func(code wagering.InputCode, field string) error {
		return apperrors.New(apperrors.KindInput, string(code), &wagering.ValidationError{Code: code, Field: field})
	}
	cases := []struct {
		name                  string
		err                   error
		status                int
		code, category, field string
		retryAfter            bool
	}{
		{"validation", input(wagering.InputInvalidAmount, "money.amount"), 400, "INVALID_AMOUNT", "CORRECTABLE", "money.amount", false},
		{"the key is a header", input(wagering.InputMissingIdempotencyKey, "idempotencyKey"), 400, "MISSING_IDEMPOTENCY_KEY", "CORRECTABLE", "Idempotency-Key", false},
		{"unknown wallet", apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", app.ErrNotFound), 400, "UNKNOWN_WALLET", "CORRECTABLE", "", false},
		{"not found", apperrors.New(apperrors.KindNotFound, "WALLET_NOT_FOUND", app.ErrNotFound), 404, "WALLET_NOT_FOUND", "CORRECTABLE", "", false},
		{"forbidden", apperrors.New(apperrors.KindForbidden, "PROVIDER_MISMATCH", nil), 403, "PROVIDER_MISMATCH", "CORRECTABLE", "", false},
		{"idempotency conflict", apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused}), 409, "IDEMPOTENCY_KEY_REUSED", "CORRECTABLE", "", false},
		{"wallet exists", apperrors.New(apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists), 409, "WALLET_ALREADY_EXISTS", "DEFINITIVE", "", false},
		{"transient", apperrors.New(apperrors.KindTransient, "", errors.New("lock timeout")), 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"unclassified", errors.New("boom"), 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"canceled", context.Canceled, 503, "TEMPORARILY_UNAVAILABLE", "TRANSIENT", "", true},
		{"permanent", apperrors.New(apperrors.KindPermanent, "", errors.New("corrupted")), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"business at the edge", apperrors.New(apperrors.KindBusiness, "", errors.New("unexpected")), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"code outside the catalog", apperrors.New(apperrors.KindInput, "WHATEVER", nil), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
		{"code of another status", apperrors.New(apperrors.KindNotFound, "UNKNOWN_WALLET", nil), 500, "INTERNAL_ERROR", "TRANSIENT", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			writeError(rec, req, slog.New(slog.DiscardHandler), tc.err)
			var p struct {
				Status   int    `json:"status"`
				Code     string `json:"code"`
				Category string `json:"category"`
				Field    string `json:"field"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
				t.Fatalf("body %q: %v", rec.Body.String(), err)
			}
			if rec.Code != tc.status || p.Status != tc.status || p.Code != tc.code || p.Category != tc.category || p.Field != tc.field {
				t.Fatalf("%d %+v, want %d %s %s %q", rec.Code, p, tc.status, tc.code, tc.category, tc.field)
			}
			if got := rec.Header().Get("Retry-After"); (got == "1") != tc.retryAfter || (got != "" && got != "1") {
				t.Fatalf("Retry-After = %q, want it only on 503", got)
			}
		})
	}
}

// Covers: D-04 (U17: result status)
func TestResultStatus(t *testing.T) {
	for status, want := range map[wagering.Status]int{
		wagering.StatusProcessed:        http.StatusOK,
		wagering.StatusPendingReference: http.StatusAccepted,
		wagering.StatusRejected:         http.StatusUnprocessableEntity,
		wagering.StatusFailed:           http.StatusInternalServerError,
		wagering.StatusPending:          http.StatusInternalServerError,
	} {
		if got := resultStatus(status); got != want {
			t.Errorf("resultStatus(%s) = %d, want %d", status, got, want)
		}
	}
}
```

```bash
go get github.com/getkin/kin-openapi@v0.149.0 && go mod tidy
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet ./... && go test -race -count=1 ./internal/adapters/httpapi/ ./internal/bootstrap/
```

Esperado: FAIL só no `httpapi`, e o `bootstrap` fica verde.
- `TestOpenAPIContract`: `routes differ`.
- `TestEdge*`: o 404 do `ServeMux` sai em `text/plain`, sem correlação nem `problem+json`, e o log fica vazio.
- `TestWriteError` e `TestResultStatus`: stubs vazios.

- [ ] **Passo 4: implementar**

Criar `internal/adapters/httpapi/problem.go`:

```go
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Codes of lifecycle §5.3 that only the edge produces.
const (
	codeMalformedRequest       = "MALFORMED_REQUEST"
	codeInvalidIdempotencyKey  = "INVALID_IDEMPOTENCY_KEY"
	codeInvalidField           = "INVALID_FIELD"
	codeInvalidAmount          = "INVALID_AMOUNT"
	codeInvalidCurrency        = "INVALID_CURRENCY"
	codeInvalidKind            = "INVALID_KIND"
	codeUnauthenticated        = "UNAUTHENTICATED"
	codeForbidden              = "FORBIDDEN"
	codeProviderMismatch       = "PROVIDER_MISMATCH"
	codeTransactionNotFound    = "TRANSACTION_NOT_FOUND"
	codeRouteNotFound          = "ROUTE_NOT_FOUND"
	codeMethodNotAllowed       = "METHOD_NOT_ALLOWED"
	codeUnsupportedMediaType   = "UNSUPPORTED_MEDIA_TYPE"
	codeInternalError          = "INTERNAL_ERROR"
	codeTemporarilyUnavailable = "TEMPORARILY_UNAVAILABLE"
)

// problemEntry is one line of the catalog: the status, the category and a
// fixed detail that never echoes a value received.
type problemEntry struct {
	status   int
	category wagering.FailureCategory
	detail   string
}

const (
	correctable = wagering.CategoryCorrectable
	definitive  = wagering.CategoryDefinitive
	transient   = wagering.CategoryTransient
)

// problems is the catalog of lifecycle §5.3; api/openapi.yaml lists the same
// codes in the enum of Problem.code.
var problems = map[string]problemEntry{
	"MALFORMED_REQUEST":                {400, correctable, "The request body is not a valid JSON object for this operation."},
	"MISSING_IDEMPOTENCY_KEY":          {400, correctable, "The Idempotency-Key header is required."},
	"INVALID_IDEMPOTENCY_KEY":          {400, correctable, "The Idempotency-Key header must appear once, with 1 to 255 visible ASCII characters."},
	"MISSING_FIELD":                    {400, correctable, "A required field is missing."},
	"INVALID_FIELD":                    {400, correctable, "A field or parameter has an invalid value."},
	"INVALID_AMOUNT":                   {400, correctable, "The amount must be a decimal string with exactly two decimal places."},
	"INVALID_CURRENCY":                 {400, correctable, "The currency is not supported."},
	"INVALID_KIND":                     {400, correctable, "The kind is not supported."},
	"OPENING_NOT_ALLOWED":              {400, correctable, "OPENING is reserved for the internal wallet opening."},
	"ZERO_AMOUNT_NOT_ALLOWED":          {400, correctable, "The amount must be positive for this kind."},
	"LOSS_AMOUNT_MUST_BE_ZERO":         {400, correctable, "A LOSS must have the amount 0.00."},
	"REFERENCE_REQUIRED":               {400, correctable, "REFUND and ROLLBACK require referenceExternalTransactionId."},
	"REFERENCE_NOT_ALLOWED":            {400, correctable, "BET and LOSS do not accept a reference."},
	"SELF_REFERENCE":                   {400, correctable, "An operation cannot reference itself."},
	"UNKNOWN_WALLET":                   {400, correctable, "The wallet does not exist."},
	"UNAUTHENTICATED":                  {401, correctable, "A valid bearer token is required."},
	"FORBIDDEN":                        {403, correctable, "The token does not grant access to this operation."},
	"PROVIDER_MISMATCH":                {403, correctable, "The provider does not match the authenticated identity."},
	"WALLET_NOT_FOUND":                 {404, correctable, "The wallet was not found."},
	"TRANSACTION_NOT_FOUND":            {404, correctable, "The transaction was not found."},
	"ROUTE_NOT_FOUND":                  {404, correctable, "No route matches the path."},
	"METHOD_NOT_ALLOWED":               {405, correctable, "The method is not allowed for this path."},
	"IDEMPOTENCY_KEY_REUSED":           {409, correctable, "The idempotency key was already used with a different payload."},
	"EXTERNAL_TRANSACTION_ID_CONFLICT": {409, correctable, "The external transaction id was already registered with another idempotency key."},
	"WALLET_ALREADY_EXISTS":            {409, definitive, "A wallet already exists for this player and currency."},
	"UNSUPPORTED_MEDIA_TYPE":           {415, correctable, "The request body must be application/json."},
	"INTERNAL_ERROR":                   {500, transient, "An unexpected error occurred; nothing was recorded."},
	"TEMPORARILY_UNAVAILABLE":          {503, transient, "A dependency is temporarily unavailable; retry the same request later."},
}

// problem is the application/problem+json body (RFC 9457).
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// writeProblem answers the code of the catalog; field names the culprit
// field or parameter, when there is one.
func writeProblem(w http.ResponseWriter, r *http.Request, code, field string) {
	e, ok := problems[code]
	if !ok {
		code, e = codeInternalError, problems[codeInternalError]
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(problem{ // the status line is already sent; nothing useful to do on error
		Type: "about:blank", Title: http.StatusText(e.status), Status: e.status,
		Code: code, Category: string(e.category), Detail: e.detail, Field: field,
		CorrelationID: correlationID(r.Context()),
	})
}
```

Substituir `internal/adapters/httpapi/status.go` (arquivo inteiro):

```go
package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// retryAfterSeconds is the Retry-After of every 503 (spec decision 12).
const retryAfterSeconds = "1"

// kindStatus is the status each classified error answers with (spec §6.3).
var kindStatus = map[apperrors.Kind]int{
	apperrors.KindInput:     http.StatusBadRequest,
	apperrors.KindNotFound:  http.StatusNotFound,
	apperrors.KindForbidden: http.StatusForbidden,
	apperrors.KindConflict:  http.StatusConflict,
}

// writeError answers an error of a use case (spec §6.3). A validation error
// keeps its field; KindInput, KindNotFound, KindForbidden and KindConflict
// answer their code; anything transient, including an unclassified error
// (D-05), is 503 with Retry-After; KindPermanent (and a code the catalog does
// not have for that status, a bug) is 500 INTERNAL_ERROR: nothing was recorded.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	ctx := r.Context()
	var validation *wagering.ValidationError
	if errors.As(err, &validation) {
		writeProblem(w, r, string(validation.Code), headerField(validation.Field))
		return
	}
	kind, code := apperrors.Classify(err), apperrors.CodeOf(err)
	switch kind {
	case apperrors.KindInput, apperrors.KindNotFound, apperrors.KindForbidden, apperrors.KindConflict:
		if e, ok := problems[code]; ok && e.status == kindStatus[kind] {
			writeProblem(w, r, code, "")
			return
		}
		log.ErrorContext(ctx, "error without a code of its status", "correlationId", correlationID(ctx), "error", err.Error())
		writeProblem(w, r, codeInternalError, "")
	case apperrors.KindTransient:
		log.WarnContext(ctx, "request failed transiently", "correlationId", correlationID(ctx), "error", err.Error())
		w.Header().Set("Retry-After", retryAfterSeconds)
		writeProblem(w, r, codeTemporarilyUnavailable, "")
	case apperrors.KindPermanent, apperrors.KindBusiness:
		log.ErrorContext(ctx, "request failed permanently", "correlationId", correlationID(ctx), "error", err.Error())
		writeProblem(w, r, codeInternalError, "")
	default:
		log.ErrorContext(ctx, "unclassifiable error", "correlationId", correlationID(ctx))
		writeProblem(w, r, codeInternalError, "")
	}
}

// headerField names the idempotency key as the client sent it: a header.
func headerField(field string) string {
	if field == "idempotencyKey" {
		return "Idempotency-Key"
	}
	return field
}

// resultStatus is the status of a recorded operation (D-04): the same for
// the first answer and for every replay.
func resultStatus(s wagering.Status) int {
	switch s {
	case wagering.StatusProcessed:
		return http.StatusOK
	case wagering.StatusPendingReference:
		return http.StatusAccepted
	case wagering.StatusRejected:
		return http.StatusUnprocessableEntity
	case wagering.StatusFailed, wagering.StatusPending: // PENDING is never persisted (D-05)
		return http.StatusInternalServerError
	}
	return http.StatusInternalServerError
}
```

Criar `internal/adapters/httpapi/middleware.go`:

```go
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/auth"
)

// maxBodyBytes bounds a request body (lifecycle §3.1).
const maxBodyBytes = 64 << 10

type ctxKey int

const (
	correlationKey ctxKey = iota
	requestInfoKey
)

// correlationPattern is what a client may send as X-Correlation-Id (D-18).
var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// withCorrelation keeps a valid X-Correlation-Id or generates a UUIDv7, and
// returns it in the response header.
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-Id")
		if !correlationPattern.MatchString(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Correlation-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

// correlationID returns the correlation id of the request.
func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

// requestInfo is filled while the request goes down the chain and read by
// the access log on the way back.
type requestInfo struct {
	route      string
	providerID string
}

func infoOf(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey).(*requestInfo)
	if info == nil {
		return &requestInfo{}
	}
	return info
}

// statusRecorder remembers the status written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// logAccess writes one line per request with a fixed set of fields: never a
// header or a body (OBS-02).
func logAccess(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		rec := &statusRecorder{ResponseWriter: w}
		ctx := context.WithValue(r.Context(), requestInfoKey, info)
		next.ServeHTTP(rec, r.WithContext(ctx))
		log.InfoContext(ctx, "http request",
			"method", r.Method, "route", info.route, "status", rec.status,
			"durationMs", time.Since(start).Milliseconds(),
			"correlationId", correlationID(ctx), "providerId", info.providerID)
	})
}

// recoverPanic turns a panic, always a bug, into 500 INTERNAL_ERROR: nothing
// was recorded, since the unit of work rolls back on panic.
func recoverPanic(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer answerPanic(log, w, r)
		next.ServeHTTP(w, r)
	})
}

// answerPanic is deferred by recoverPanic, so recover sees the panic.
// http.ErrAbortHandler keeps its meaning: abort without an answer.
func answerPanic(log *slog.Logger, w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	ctx := r.Context()
	log.ErrorContext(ctx, "panic serving the request",
		"correlationId", correlationID(ctx), "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	writeProblem(w, r, codeInternalError, "")
}

// discard remembers the status and headers a handler writes, without the body.
type discard struct {
	header http.Header
	status int
}

func (d *discard) Header() http.Header { return d.header }

func (d *discard) Write(b []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	return len(b), nil
}

func (d *discard) WriteHeader(code int) {
	if d.status == 0 {
		d.status = code
	}
}

// routeFallback answers 404 ROUTE_NOT_FOUND and 405 METHOD_NOT_ALLOWED as
// problem+json. The ServeMux decides (its own 404 and 405, with Allow); the
// fallback only rewrites the body. Its redirects pass through.
func routeFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		d := &discard{header: http.Header{}}
		h.ServeHTTP(d, r)
		switch d.status {
		case http.StatusNotFound:
			writeProblem(w, r, codeRouteNotFound, "")
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", d.header.Get("Allow"))
			writeProblem(w, r, codeMethodNotAllowed, "")
		default:
			mux.ServeHTTP(w, r)
		}
	})
}

// authenticate requires a bearer token of one of roles and puts the
// principal in the context (D-07). Authorization runs before any read or
// write (AUTH-07).
func authenticate(a Authenticator, log *slog.Logger, roles []auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		raw, ok := bearerToken(r)
		if !ok {
			unauthenticated(w, r, false)
			return
		}
		p, err := a.Authenticate(ctx, raw)
		if err != nil {
			log.DebugContext(ctx, "bearer token rejected", "correlationId", correlationID(ctx), "reason", err.Error())
			unauthenticated(w, r, true)
			return
		}
		infoOf(ctx).providerID = p.ProviderID
		if !auth.HasAnyRole(p, roles...) {
			writeProblem(w, r, codeForbidden, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
	})
}

// bearerToken returns the token of "Authorization: Bearer <token>"; ok is
// false when there is no bearer credential at all.
func bearerToken(r *http.Request) (token string, ok bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// unauthenticated answers 401 with the challenge of RFC 6750. The answer never
// says whether the token was malformed, forged or expired.
func unauthenticated(w http.ResponseWriter, r *http.Request, invalidToken bool) {
	challenge := `Bearer realm="pda"`
	if invalidToken {
		challenge += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeProblem(w, r, codeUnauthenticated, "")
}

// jsonBody requires Content-Type application/json and bounds the body.
func jsonBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeProblem(w, r, codeUnsupportedMediaType, "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
```

Criar `internal/adapters/httpapi/docs_handler.go`:

```go
package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/api"
)

// openAPI serves the embedded contract: always the version of the build.
func openAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(api.OpenAPI) // nothing useful to do if the client went away
}

// swaggerUI serves the Swagger UI page (D-20).
func swaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(api.SwaggerHTML) // nothing useful to do if the client went away
}
```

Substituir `internal/adapters/httpapi/routes.go` (arquivo inteiro):

```go
package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/internal/auth"
)

var (
	internalOnly = []auth.Role{auth.RoleWalletInternal}
	providerOnly = []auth.Role{auth.RoleProvider}
	anyCaller    = []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}
)

// route is one line of the route table: the single list that registers the
// ServeMux and that I15 compares with api/openapi.yaml.
type route struct {
	method, path string
	roles        []auth.Role // nil = public
	jsonBody     bool
	handle       http.HandlerFunc
}

func routes(opts Options, s Services) []route {
	hh := healthHandler{health: s.Health}
	h := handlers{s: s, log: opts.Log}
	rs := []route{
		{http.MethodGet, "/health/live", nil, false, hh.live},
		{http.MethodGet, "/health/ready", nil, false, hh.ready},
		{http.MethodPost, "/wallets", internalOnly, true, h.openWallet},
		{http.MethodGet, "/wallets/{walletId}", internalOnly, false, h.getWallet},
		{http.MethodGet, "/wallets/{walletId}/ledger", internalOnly, false, h.listLedger},
		{http.MethodPost, "/wallets/{walletId}/reconciliation", internalOnly, false, h.reconcile},
		{http.MethodPost, "/wagering/transactions", providerOnly, true, h.submitWager},
		{http.MethodGet, "/wagering/transactions/{transactionId}", anyCaller, false, h.getTransaction},
		{http.MethodGet, "/providers/{providerId}/wagering/transactions/{externalTransactionId}", anyCaller, false, h.getTransactionByExternalID},
	}
	if opts.DocsEnabled {
		rs = append(rs,
			route{http.MethodGet, "/openapi.yaml", nil, false, openAPI},
			route{http.MethodGet, "/docs", nil, false, swaggerUI},
		)
	}
	return rs
}

// New returns the API handler: the routes behind the middlewares, from the
// outside in: correlation, access log, panic recovery and the problem+json
// fallback for unmatched routes; then, per route, authentication and roles,
// and the JSON body checks.
func New(opts Options, s Services) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes(opts, s) {
		var h http.Handler = rt.handle
		if rt.jsonBody {
			h = jsonBody(h)
		}
		if rt.roles != nil {
			h = authenticate(s.Auth, opts.Log, rt.roles, h)
		}
		mux.Handle(rt.method+" "+rt.path, named(rt.method+" "+rt.path, h))
	}
	return withCorrelation(logAccess(opts.Log, recoverPanic(opts.Log, routeFallback(mux))))
}

// named records the matched route for the access log.
func named(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infoOf(r.Context()).route = pattern
		next.ServeHTTP(w, r)
	})
}

// Routes lists the "METHOD /path" patterns New registers (I15).
func Routes(docsEnabled bool) []string {
	var out []string
	for _, rt := range routes(Options{DocsEnabled: docsEnabled}, Services{}) {
		out = append(out, rt.method+" "+rt.path)
	}
	return out
}
```

Criar `internal/adapters/httpapi/placeholder_handlers.go`:

```go
package httpapi

import "net/http"

// notImplemented answers the business routes until their handlers exist.
func notImplemented(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }

func (h handlers) openWallet(w http.ResponseWriter, r *http.Request)     { notImplemented(w, r) }
func (h handlers) getWallet(w http.ResponseWriter, r *http.Request)      { notImplemented(w, r) }
func (h handlers) listLedger(w http.ResponseWriter, r *http.Request)     { notImplemented(w, r) }
func (h handlers) reconcile(w http.ResponseWriter, r *http.Request)      { notImplemented(w, r) }
func (h handlers) submitWager(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (h handlers) getTransaction(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

func (h handlers) getTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go vet ./... && go test -race -count=1 ./internal/adapters/httpapi/ ./internal/bootstrap/
```

Esperado: `ok` nos dois pacotes.

**Checkpoint:** `go test -race -count=1 ./internal/adapters/httpapi/ ./internal/bootstrap/` verde.

---

### Tarefa 9: composição Fx, timeouts do servidor e dependência do Keycloak

A spec §7 (decisões 15, 16 e 22):
- **`app_module.go`:** relógio, gerador de ids, `app.Metrics` (de `*observability.Metrics`, com o `As` no `bootstrap` para a `observability` não importar o `app`), a política de referências e os 4 casos de uso.
- **Ordem dos módulos:** `config → observability → postgres → aws → auth → app → httpapi`.
- **Módulo `httpapi`:** recebe todos os serviços.
- **Servidor:** `ReadTimeout` de 10 s e `WriteTimeout` de 30 s.
- **Compose:** as réplicas esperam o Keycloak saudável.
- **Testes de integração do `bootstrap`:** ganham a config OIDC e o caso do IdP inacessível.

**Arquivos:**
- Implementação: `internal/bootstrap/app_module.go` (criar); `internal/bootstrap/bootstrap.go`, `internal/adapters/httpapi/module.go`, `internal/adapters/httpapi/server.go`, `docker-compose.yml` (alterar); `test/testkit/auth.go` (criar)
- Testes: `internal/bootstrap/bootstrap_test.go` (substituir), `internal/bootstrap/bootstrap_integration_test.go` (alterar), `internal/adapters/httpapi/server_test.go`

**Interfaces:**
- Consome: os construtores das Tarefas 3–8; `auth.Module`; `observability.NewMetrics`.
- Produz:
  - `httpapi.NewServer(addr string, handler http.Handler) *http.Server`;
  - `bootstrap.Options()` com `auth.Module` e o `appModule`;
  - `testkit.KeycloakURL` e `testkit.KeycloakIssuer`.

- [ ] **Passo 1: criar o stub**

Substituir `internal/adapters/httpapi/server.go` (arquivo inteiro):

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// RegisterServer runs the API server on HTTP_ADDR. It is registered last, so it
// starts after every dependency and stops first (D-15).
func RegisterServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, log *slog.Logger) {
	observability.ServeOnLifecycle(lc, NewServer(cfg.HTTPAddr, handler), cfg.ShutdownTimeout, log, "api")
}

// NewServer builds the API server.
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
}
```

Criar `test/testkit/auth.go`:

```go
package testkit

// KeycloakURL is the compose Keycloak seen from the host.
const KeycloakURL = "http://localhost:8080"

// KeycloakIssuer is the iss of the realm pda: with KC_HOSTNAME it is the same
// seen from the host and from the compose network (D-07).
const KeycloakIssuer = KeycloakURL + "/realms/pda"
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `internal/adapters/httpapi/server_test.go`:

```go
package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
)

// Covers: FX-04 (spec decision 22)
func TestNewServerTimeouts(t *testing.T) {
	srv := httpapi.NewServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 10*time.Second || srv.WriteTimeout != 30*time.Second {
		t.Fatalf("timeouts = header %v, read %v, write %v; want 5s, 10s, 30s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout)
	}
}
```

Substituir `internal/bootstrap/bootstrap_test.go` (arquivo inteiro):

```go
package bootstrap_test

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/bootstrap"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: TST-I07, FX-01 (I07a — M0 modules, M2 persistence, M3 auth, use cases and API)
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
	)
	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &handler, &uow, &repos,
		&verifier, &metrics, &open, &process, &queries, &reconcile))
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("fx.ValidateApp() = %v", err)
	}
}
```

Em `internal/bootstrap/bootstrap_integration_test.go`, trocar:

```go
		ShutdownTimeout: 5 * time.Second, DatabaseURL: testkit.AppDatabaseURL(t), DBMaxConns: 2,
		WagerQueueName: wager, WagerDLQName: dlq,
	}
```

por:

```go
		ShutdownTimeout: 5 * time.Second, DatabaseURL: testkit.AppDatabaseURL(t), DBMaxConns: 2,
		DBLockTimeout: 2 * time.Second, WagerQueueName: wager, WagerDLQName: dlq,
		OIDCIssuer: testkit.KeycloakIssuer, OIDCJWKSURL: testkit.KeycloakIssuer + "/protocol/openid-connect/certs",
		OIDCAudience: "pda-api", OIDCClockSkew: time.Second, APIDocsEnabled: true,
		ReferenceRetryBaseDelay: 100 * time.Millisecond, ReferenceRetryMaxDelay: time.Second,
		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
	}
```

Em `internal/bootstrap/bootstrap_integration_test.go`, trocar:

```go
// Covers: FX-02 (I07c)
```

por:

```go
// Covers: FX-02, AUTH-02 (I07c)
```

Em `internal/bootstrap/bootstrap_integration_test.go`, trocar:

```go
		{"missing queue", func(c *config.Config) { c.WagerQueueName = "missing-" + c.WagerQueueName }, "missing-"},
	}
```

por:

```go
		{"missing queue", func(c *config.Config) { c.WagerQueueName = "missing-" + c.WagerQueueName }, "missing-"},
		{"unreachable identity provider", func(c *config.Config) { c.OIDCJWKSURL = "http://127.0.0.1:1/certs" }, "JWKS"},
	}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet ./... && go vet -tags=integration ./internal/bootstrap/ && go test -count=1 ./internal/bootstrap/ ./internal/adapters/httpapi/; go test -tags=integration -count=1 -run TestFxFailFast ./internal/bootstrap/
```

Esperado: FAIL.
- `TestFxGraph`: `missing types: *auth.Verifier; app.Metrics …; *app.OpenWallet; *app.ProcessWager …`.
- `TestNewServerTimeouts`: `read 0s, write 0s`.
- `TestFxFailFast/unreachable_identity_provider`: `Start() error = nil, want fail-fast error`, porque ainda não há módulo `auth`.

- [ ] **Passo 4: implementar**

Substituir `internal/adapters/httpapi/server.go` (arquivo inteiro):

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// RegisterServer runs the API server on HTTP_ADDR. It is registered last, so it
// starts after every dependency and stops first (D-15).
func RegisterServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, log *slog.Logger) {
	observability.ServeOnLifecycle(lc, NewServer(cfg.HTTPAddr, handler), cfg.ShutdownTimeout, log, "api")
}

// NewServer builds the API server. ReadTimeout bounds slow bodies; the
// WriteTimeout of 30 s covers the worst lock_timeout plus the race retries
// (spec decision 22).
func NewServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second,
	}
}
```

Substituir `internal/adapters/httpapi/module.go` (arquivo inteiro):

```go
package httpapi

import (
	"log/slog"
	"net/http"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the API handler and starts the API server.
var Module = fx.Module("httpapi", fx.Provide(newHandler), fx.Invoke(RegisterServer))

type handlerParams struct {
	fx.In

	Config    config.Config
	Log       *slog.Logger
	Health    *observability.Health
	Verifier  *auth.Verifier
	Wagers    *app.ProcessWager
	Wallets   *app.OpenWallet
	Queries   *app.Queries
	Reconcile *app.Reconcile
}

func newHandler(p handlerParams) http.Handler {
	return New(Options{DocsEnabled: p.Config.APIDocsEnabled, Log: p.Log}, Services{
		Auth: p.Verifier, Wagers: p.Wagers, Wallets: p.Wallets, Queries: p.Queries,
		Reconcile: p.Reconcile, Health: p.Health,
	})
}
```

Criar `internal/bootstrap/app_module.go`:

```go
package bootstrap

import (
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/observability"
)

// appModule provides the use cases. They are plain constructors, registered
// here so that the app package never imports Fx (structure.md §3).
var appModule = fx.Module("app",
	fx.Provide(
		func() app.Clock { return app.SystemClock{} },
		func() app.IDGenerator { return app.UUIDv7{} },
		func(m *observability.Metrics) app.Metrics { return m },
		newReferencePolicy,
		app.NewOpenWallet,
		app.NewProcessWager,
		app.NewQueries,
		app.NewReconcile,
	),
)

// newReferencePolicy is the schedule of pending references (D-11); the jitter
// uses math/rand/v2.
func newReferencePolicy(cfg config.Config) (wagering.ReferenceRetryPolicy, error) {
	return wagering.NewReferenceRetryPolicy(cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay,
		cfg.ReferenceMaxAttempts, cfg.ReferenceTTL, nil)
}
```

Em `internal/bootstrap/bootstrap.go`, trocar:

```go
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
```

por:

```go
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/auth"
```

Em `internal/bootstrap/bootstrap.go`, trocar:

```go
		awsclient.Module,
		httpapi.Module,
```

por:

```go
		awsclient.Module,
		auth.Module,
		appModule,
		httpapi.Module,
```

Em `docker-compose.yml`, trocar:

```yaml
    aws-init:
      condition: service_completed_successfully
  healthcheck:
```

por:

```yaml
    aws-init:
      condition: service_completed_successfully
    keycloak:
      condition: service_healthy # JWKS fetched on start (fail fast, D-07)
  healthcheck:
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/bootstrap/ ./internal/adapters/httpapi/ && go test -tags=integration -race -count=1 ./internal/bootstrap/ && docker compose config -q
```

Esperado: `ok` nos pacotes, inclusive o I07b (`TestFxLifecycle`, com `goleak`) e o novo caso do I07c. O `docker compose config -q` fica em silêncio.

**Checkpoint:** os comandos do Passo 5 verdes.

---

### Tarefa 10: harness da integração: app em processo, tokens, contrato e consistência

A spec §8 (decisões 20, 21 e 25).
- **`env.StartApp`:**
  - cria as filas isoladas com a chave raiz do MiniStack;
  - aponta a cadeia AWS do processo para elas, com `os.Setenv`, porque é chamado do `TestMain`;
  - sobe o Fx inteiro numa porta livre, com a config acelerada do test-plan §3.3 e os logs capturados.
- **`Client`:** passa **toda** troca pelo validador do `api/openapi.yaml`; uma requisição marcada `Invalid` valida só a resposta.
- **`AssertWalletConsistent`:** o test-plan §6 inteiro. O item 1 é a reconciliação pela API, os itens 2–6 o `LedgerProblems` e o item 7 o `OutboxProblems`, novo, com a sensibilidade provada como a do M2.
- **Tokens:** reais, com cache, e os forjados a partir das claims de um token real.

**Arquivos:**
- Implementação: `test/testkit/contract.go`, `api.go`, `app.go`, `assert.go` (criar); `test/testkit/auth.go`, `aws.go`, `net.go` (substituir)
- Testes: `test/testkit/contract_test.go`; `test/integration/main_test.go`, `harness_test.go`; `internal/adapters/postgres/outbox_problems_integration_test.go`

**Interfaces:**
- Consome: `bootstrap.Options()` (Tarefa 9), `observability.NewJSONLogger`, `testkit.NewEnv`/`LedgerProblems` (M2) e `api.OpenAPI` (Tarefa 8).
- Produz:
  - **contrato:** `testkit.LoadContract() (*Contract, error)` e `(*Contract).Check(req, reqBody, resp, respBody, skipRequest) error`;
  - **app:** `(*testkit.Env).StartApp(ctx) (*App, func(), error)`, com os métodos `Client(tb, clientID)`, `ClientWithToken(raw)`, `Logs()`, `Metric(tb, name)`, `OpenWallet(tb, Money) Wallet`, `AssertWalletConsistent(tb, walletID)` e `Owner()`;
  - **cliente:** `testkit.Client.Do(tb, Request) *Response`, com `Response.JSON(tb, v)` e `Response.Problem(tb)`;
  - **DTOs de teste:** `Request`, `Response`, `Money`, `BRL`, `Problem`, `Wallet`, `Wager`, `TransactionResult`, `Transaction`, `LedgerEntry`, `LedgerPage` e `Reconciliation`;
  - **tokens:** `Token`, `FreshToken`, `OtherRealmToken`, `ForgedToken`, `HS256Token` e `UnsignedToken`;
  - **asserções:** `OutboxProblems(ctx, pool, walletID)`, `SnapshotCounts(tb, pool)` e `NewID()`.

- [ ] **Passo 1: criar os stubs**

Criar `test/testkit/contract.go`:

```go
package testkit

import "net/http"

// Contract validates requests and responses against api/openapi.yaml (D-20).
type Contract struct{}

// LoadContract loads the embedded document.
func LoadContract() (*Contract, error) { return &Contract{}, nil }

// Check validates one exchange.
func (c *Contract) Check(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, skipRequest bool) error {
	return nil
}
```

Criar `test/testkit/api.go`:

```go
package testkit

import (
	"net/http"
	"testing"
)

// Client calls the API under test with one identity.
type Client struct{}

// Request is one call; Invalid marks a request deliberately off the contract.
type Request struct {
	Method, Path string
	Body         any
	Header       http.Header
	Invalid      bool
}

// Response is what the API answered.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Problem is the application/problem+json body.
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// Do sends the request.
func (c *Client) Do(tb testing.TB, r Request) *Response {
	tb.Helper()
	return &Response{Header: http.Header{}}
}

// JSON decodes an application/json body strictly into v.
func (r *Response) JSON(tb testing.TB, v any) { tb.Helper() }

// Problem decodes an application/problem+json body.
func (r *Response) Problem(tb testing.TB) Problem {
	tb.Helper()
	return Problem{}
}

// NewID returns a UUIDv7.
func NewID() string { return "" }
```

Criar `test/testkit/app.go`:

```go
package testkit

import (
	"context"
	"testing"
)

// App is the application under test, running in process (spec decision 21).
type App struct{}

// StartApp starts the application over the package database.
func (e *Env) StartApp(ctx context.Context) (*App, func(), error) { return &App{}, func() {}, nil }

// Client returns a client with a token of clientID ("" = no token).
func (a *App) Client(tb testing.TB, clientID string) *Client {
	tb.Helper()
	return &Client{}
}
```

Criar `test/testkit/assert.go`:

```go
package testkit

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OutboxProblems checks item 7 of test-plan §6.
func OutboxProblems(ctx context.Context, pool *pgxpool.Pool, walletID string) ([]string, error) {
	return nil, nil
}
```

- [ ] **Passo 2: escrever os testes que falham**

Criar `test/testkit/contract_test.go`:

```go
package testkit_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

func exchange(ctx context.Context, method, path, reqBody string, status int, contentType, respBody string) (*http.Request, *http.Response) {
	req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1:1"+path, strings.NewReader(reqBody))
	if reqBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp := &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader([]byte(respBody)))}
	resp.Header.Set("Content-Type", contentType)
	return req, resp
}

const (
	problemBody   = `{"type":"about:blank","title":"Not Found","status":404,"code":"WALLET_NOT_FOUND","category":"CORRECTABLE","detail":"d","correlationId":"c"}`
	malformedBody = `{"type":"about:blank","title":"Bad Request","status":400,"code":"MALFORMED_REQUEST","category":"CORRECTABLE","detail":"d","correlationId":"c"}`
)

// Covers: D-20, DOC-06
//
// The validator that every API test relies on must catch drift: a response
// or a request off the contract fails.
func TestContract(t *testing.T) {
	c, err := testkit.LoadContract()
	if err != nil {
		t.Fatal(err)
	}
	check := func(method, path, reqBody string, status int, ct, respBody string, skip bool) error {
		req, resp := exchange(t.Context(), method, path, reqBody, status, ct, respBody)
		defer resp.Body.Close()
		return c.Check(req, []byte(reqBody), resp, []byte(respBody), skip)
	}
	const wallet = "/wallets/0192f291-27dd-7d3f-8071-5f8685deef37"

	if err := check(http.MethodGet, wallet, "", 404, "application/problem+json", problemBody, false); err != nil {
		t.Fatalf("documented exchange: %v", err)
	}
	for name, err := range map[string]error{
		"field outside the schema": check(http.MethodGet, wallet, "", 404, "application/problem+json", strings.Replace(problemBody, `"c"}`, `"c","x":1}`, 1), false),
		"code outside the enum":    check(http.MethodGet, wallet, "", 404, "application/problem+json", strings.Replace(problemBody, "WALLET_NOT_FOUND", "NOPE", 1), false),
		"undocumented status":      check(http.MethodGet, wallet, "", 501, "text/plain", "", false),
		"request off the contract": check(http.MethodPost, "/wallets", `{"x":1}`, 400, "application/problem+json", malformedBody, false),
	} {
		if err == nil {
			t.Errorf("%s: Check = nil, want an error", name)
		}
	}
	if err := check(http.MethodPost, "/wallets", `{"x":1}`, 400, "application/problem+json", malformedBody, true); err != nil {
		t.Fatalf("a deliberately invalid request is not validated: %v", err)
	}
	if err := check(http.MethodGet, "/openapi.yaml", "", 200, "application/yaml", "openapi: 3.0.3\n", false); err != nil {
		t.Fatalf("yaml body: %v", err)
	}
	if err := check(http.MethodGet, "/docs", "", 200, "text/html; charset=utf-8", "<html></html>", false); err != nil {
		t.Fatalf("html body: %v", err)
	}
	if err := check(http.MethodGet, "/nope", "", 404, "application/problem+json", "{}", false); err != nil {
		t.Fatalf("a route outside the document is skipped: %v", err)
	}
}
```

Criar `test/integration/main_test.go`:

```go
//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// server is the application under test: in process, over this package's
// database and queues, with the real Keycloak (spec decision 21).
var server *testkit.App

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env, cleanup, err := testkit.NewEnv(ctx, "integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.NewEnv:", err)
		return 1
	}
	defer cleanup()
	app, stop, err := env.StartApp(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.StartApp:", err)
		return 1
	}
	defer stop()
	server = app
	return m.Run()
}
```

Criar `test/integration/harness_test.go`:

```go
//go:build integration

package integration_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-08, AUTH-08, DOC-06, D-20
//
// The in-process application answers through the contract validator.
func TestHarness(t *testing.T) {
	t.Parallel()
	anonymous := server.Client(t, "")

	ready := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/health/ready"})
	var health struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	ready.JSON(t, &health)
	if ready.Status != http.StatusOK || health.Checks["postgres"] != "UP" || health.Checks["sqs"] != "UP" {
		t.Fatalf("GET /health/ready = %d %+v", ready.Status, health)
	}

	doc := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/openapi.yaml"})
	if doc.Status != http.StatusOK || !bytes.Equal(doc.Body, api.OpenAPI) {
		t.Fatalf("GET /openapi.yaml = %d, %d bytes", doc.Status, len(doc.Body))
	}

	denied := anonymous.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + testkit.NewID()})
	if p := denied.Problem(t); denied.Status != http.StatusUnauthorized || p.Code != "UNAUTHENTICATED" {
		t.Fatalf("GET /wallets/{id} without a token = %d %s", denied.Status, p.Code)
	}
}
```

Criar `internal/adapters/postgres/outbox_problems_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// event is an outbox row of type typ for the operation txID of wallet w.
func event(typ, w, txID string) stmt {
	aggregate, id := "WagerTransaction", txID
	if typ == "WalletBalanceChanged" {
		aggregate, id = "Wallet", w
	}
	return ins("outbox_events", outboxRow(newID(), w).with(
		"event_type", typ, "aggregate_type", aggregate, "aggregate_id", id,
		"payload", `{"data":{"transactionId":"`+txID+`"}}`))
}

// Sensitivity of testkit.OutboxProblems, item 7 of test-plan §6: each
// divergence from the event matrix of lifecycle §7, written with the triggers
// disabled on a database of its own, is reported.
func TestOutboxProblemsDetectsDivergence(t *testing.T) {
	t.Parallel()
	_, pool := isolatedDB(t, "outboxcheck")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers: %v", err)
		}
	}
	cases := []struct {
		name string
		rows func(w, p, o string) []stmt
		want string // empty when the events match the matrix
	}{
		{"consistent", func(w, p, o string) []stmt {
			rejected, pending := newID(), newID()
			return []stmt{
				ins("wager_transactions", externalRow(rejected, w, p)),
				event("WagerTransactionRejected", w, rejected),
				ins("wager_transactions", pendingRow(pending, w, p)),
				event("WagerTransactionPendingReference", w, pending),
			}
		}, ""},
		{"processed without its balance change", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "PROCESSED", "failure_code", nil)),
				event("WagerTransactionProcessed", w, bet),
			}
		}, "changed=0"},
		{"rejection twice", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p)),
				event("WagerTransactionRejected", w, bet), event("WagerTransactionRejected", w, bet),
			}
		}, "rejected=2"},
		{"failed with an event", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "FAILED", "failure_code", "INTERNAL_PERMANENT_FAILURE", "result_balance_minor", nil)),
				event("WagerTransactionRejected", w, bet),
			}
		}, "rejected=1"},
		{"pending without its event", func(w, p, o string) []stmt {
			return []stmt{ins("wager_transactions", pendingRow(newID(), w, p))}
		}, "pending=0"},
		{"LOSS with a balance change", func(w, p, o string) []stmt {
			loss := newID()
			return []stmt{
				ins("wager_transactions", externalRow(loss, w, p).with("kind", "LOSS", "amount_minor", int64(0), "status", "PROCESSED", "failure_code", nil)),
				event("WagerTransactionProcessed", w, loss), event("WalletBalanceChanged", w, loss),
			}
		}, "changed=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, p, o := newID(), newID(), newID()
			stmts := append([]stmt{
				ins("wallets", walletRow(w, p, 10000)),
				ins("wager_transactions", openingRow(o, w, p, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, o, "CREDIT", 10000, 0, 10000, 1)),
				event("WagerTransactionProcessed", w, o), event("WalletBalanceChanged", w, o),
			}, tc.rows(w, p, o)...)
			if err := attemptCommit(t, pool, stmts...); err != nil {
				t.Fatalf("seed: %v", err)
			}
			problems, err := testkit.OutboxProblems(t.Context(), pool, w)
			if err != nil {
				t.Fatalf("OutboxProblems: %v", err)
			}
			joined := strings.Join(problems, "; ")
			if tc.want == "" && len(problems) != 0 || tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("problems = %q, want %q", joined, tc.want)
			}
		})
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go vet ./test/... && go vet -tags=integration ./test/... ./internal/adapters/postgres/ && go test -count=1 ./test/testkit/; go test -tags=integration -count=1 -run 'TestHarness|TestProvisioning' ./test/integration/; go test -tags=integration -count=1 -run TestOutboxProblemsDetectsDivergence ./internal/adapters/postgres/
```

Esperado: FAIL.
- `TestContract`: quatro vezes `Check = nil, want an error`.
- `TestHarness`: `GET /health/ready = 0`.
- `TestOutboxProblemsDetectsDivergence`: os cinco casos de divergência.
- `TestProvisioning` segue verde. Ele precisa do `.local/aws/credentials` do `aws-init`, que o `make infra-up` gera.

- [ ] **Passo 4: implementar**

Substituir `test/testkit/contract.go` (arquivo inteiro):

```go
package testkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/KaioVinicios/pda/api"
)

// Contract validates requests and responses against api/openapi.yaml (D-20):
// a response off the contract fails the test that received it.
type Contract struct {
	router routers.Router
}

var registerDecoders sync.Once

// LoadContract loads the embedded document. The servers are dropped, so the
// routes match the application under test on any address.
func LoadContract() (*Contract, error) {
	registerDecoders.Do(func() {
		// The yaml decoder of kin-openapi would parse the document served at
		// /openapi.yaml into an object, and text/html has none: both bodies are
		// plain strings in the contract.
		plain := func(body io.Reader, _ http.Header, _ *openapi3.SchemaRef, _ openapi3filter.EncodingFn) (any, error) {
			b, err := io.ReadAll(body)
			return string(b), err
		}
		openapi3filter.RegisterBodyDecoder("application/yaml", plain)
		openapi3filter.RegisterBodyDecoder("text/html", plain)
	})
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPI)
	if err != nil {
		return nil, fmt.Errorf("testkit: load the contract: %w", err)
	}
	doc.Servers = nil
	router, err := legacy.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("testkit: contract router: %w", err)
	}
	return &Contract{router: router}, nil
}

// Check validates one exchange: the response always, the request unless
// skipRequest (a request deliberately off the contract, spec decision 20). A
// route the document does not have (404/405 of routing) is not validated; I15
// guarantees the document and the routes are the same set.
func (c *Contract) Check(req *http.Request, reqBody []byte, resp *http.Response, respBody []byte, skipRequest bool) error {
	route, params, err := c.router.FindRoute(req)
	var notRouted *routers.RouteError // path or method outside the document
	if errors.As(err, &notRouted) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("contract: %w", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(reqBody))
	ctx := context.WithoutCancel(req.Context())
	in := &openapi3filter.RequestValidationInput{
		Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if !skipRequest {
		if err := openapi3filter.ValidateRequest(ctx, in); err != nil {
			return fmt.Errorf("contract: request %s %s: %s", req.Method, req.URL.Path, firstLine(err))
		}
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header,
		Body:    io.NopCloser(bytes.NewReader(respBody)),
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(ctx, out); err != nil {
		return fmt.Errorf("contract: response %d to %s %s: %s", resp.StatusCode, req.Method, req.URL.Path, firstLine(err))
	}
	return nil
}

// firstLine drops the schema dump kin-openapi appends to its errors.
func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}
```

Substituir `test/testkit/api.go` (arquivo inteiro):

```go
package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// requestTimeout bounds each call; calls also run in t.Cleanup, where the
// test's context is already done, so the context is detached.
const requestTimeout = 30 * time.Second

// Client calls the API under test with one identity. Every exchange goes
// through the contract (D-20).
type Client struct {
	app   *App
	token string
}

// Request is one call. A Body of type string or []byte is sent as is;
// anything else is encoded as JSON. Invalid marks a request deliberately off
// the contract: only its response is validated (spec decision 20).
type Request struct {
	Method, Path string
	Body         any
	Header       http.Header
	Invalid      bool
}

// Response is what the API answered.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends the request, validates the exchange against the contract and
// fails the test on a transport error or a violation.
func (c *Client) Do(tb testing.TB, r Request) *Response {
	tb.Helper()
	var body []byte
	switch b := r.Body.(type) {
	case nil:
	case string:
		body = []byte(b)
	case []byte:
		body = b
	default:
		var err error
		if body, err = json.Marshal(b); err != nil {
			tb.Fatalf("encode body: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, c.app.BaseURL+r.Path, bytes.NewReader(body))
	if err != nil {
		tb.Fatalf("request: %v", err)
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range r.Header {
		req.Header.Del(k)
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.app.http.Do(req)
	if err != nil {
		tb.Fatalf("%s %s: %v", r.Method, r.Path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		tb.Fatalf("%s %s: read body: %v", r.Method, r.Path, err)
	}
	if err := c.app.contract.Check(req, body, resp, respBody, r.Invalid); err != nil {
		tb.Fatal(err)
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: respBody}
}

// JSON decodes an application/json body strictly into v.
func (r *Response) JSON(tb testing.TB, v any) {
	tb.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		tb.Fatalf("Content-Type = %q, want application/json (status %d, body %s)", ct, r.Status, r.Body)
	}
	decodeStrict(tb, r.Body, v)
}

// Problem decodes an application/problem+json body.
func (r *Response) Problem(tb testing.TB) Problem {
	tb.Helper()
	if ct := r.Header.Get("Content-Type"); ct != "application/problem+json" {
		tb.Fatalf("Content-Type = %q, want application/problem+json (status %d, body %s)", ct, r.Status, r.Body)
	}
	var p Problem
	decodeStrict(tb, r.Body, &p)
	return p
}

func decodeStrict(tb testing.TB, body []byte, v any) {
	tb.Helper()
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		tb.Fatalf("decode %s: %v", body, err)
	}
}

// NewID returns a UUIDv7.
func NewID() string { return uuid.Must(uuid.NewV7()).String() }

// Money is the {"amount","currency"} of the contract.
type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// BRL is amount in reais.
func BRL(amount string) Money { return Money{Amount: amount, Currency: "BRL"} }

// Problem is the application/problem+json body.
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// Wallet is the Wallet schema.
type Wallet struct {
	ID        string    `json:"id"`
	PlayerID  string    `json:"playerId"`
	Balance   Money     `json:"balance"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Wager is the WagerTransactionRequest schema.
type Wager struct {
	ProviderID                     string `json:"providerId"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	PlayerID                       string `json:"playerId"`
	WalletID                       string `json:"walletId"`
	RoundID                        string `json:"roundId"`
	GameID                         string `json:"gameId"`
	Kind                           string `json:"kind"`
	Money                          Money  `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
}

// TransactionResult is the TransactionResult schema.
type TransactionResult struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	Balance          *Money `json:"balance,omitempty"`
	FailureCode      string `json:"failureCode,omitempty"`
	FailureCategory  string `json:"failureCategory,omitempty"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

// Transaction is the Transaction schema.
type Transaction struct {
	TransactionID                  string     `json:"transactionId"`
	Origin                         string     `json:"origin"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	WalletID                       string     `json:"walletId"`
	PlayerID                       string     `json:"playerId"`
	Money                          Money      `json:"money"`
	ProviderID                     string     `json:"providerId,omitempty"`
	ExternalTransactionID          string     `json:"externalTransactionId,omitempty"`
	RoundID                        string     `json:"roundId,omitempty"`
	GameID                         string     `json:"gameId,omitempty"`
	ReceivedVia                    string     `json:"receivedVia,omitempty"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string     `json:"referenceTransactionId,omitempty"`
	Balance                        *Money     `json:"balance,omitempty"`
	FailureCode                    string     `json:"failureCode,omitempty"`
	FailureCategory                string     `json:"failureCategory,omitempty"`
	Attempts                       *int       `json:"attempts,omitempty"`
	NextAttemptAt                  *time.Time `json:"nextAttemptAt,omitempty"`
	ExpiresAt                      *time.Time `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time  `json:"createdAt"`
	UpdatedAt                      time.Time  `json:"updatedAt"`
	CompletedAt                    *time.Time `json:"completedAt,omitempty"`
}

// LedgerEntry is the LedgerEntry schema.
type LedgerEntry struct {
	ID            string    `json:"id"`
	TransactionID string    `json:"transactionId"`
	Direction     string    `json:"direction"`
	Amount        Money     `json:"amount"`
	BalanceBefore Money     `json:"balanceBefore"`
	BalanceAfter  Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
	CreatedAt     time.Time `json:"createdAt"`
}

// LedgerPage is the LedgerPage schema.
type LedgerPage struct {
	Items      []LedgerEntry `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// Reconciliation is the Reconciliation schema.
type Reconciliation struct {
	WalletID          string `json:"walletId"`
	StoredBalance     Money  `json:"storedBalance"`
	CalculatedBalance Money  `json:"calculatedBalance"`
	Difference        Money  `json:"difference"`
	Consistent        bool   `json:"consistent"`
	CheckedEntries    int64  `json:"checkedEntries"`
}
```

Substituir `test/testkit/auth.go` (arquivo inteiro):

```go
package testkit

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// KeycloakURL is the compose Keycloak seen from the host.
const KeycloakURL = "http://localhost:8080"

// KeycloakIssuer is the iss of the realm pda: with KC_HOSTNAME it is the same
// seen from the host and from the compose network (D-07).
const KeycloakIssuer = KeycloakURL + "/realms/pda"

// tokenMargin renews a cached token this long before it expires.
const tokenMargin = 30 * time.Second

type cachedToken struct {
	raw     string
	expires time.Time
}

var (
	tokenMu     sync.Mutex
	tokenCache  = map[string]cachedToken{}
	tokenClient = &http.Client{Timeout: 10 * time.Second}
)

// Token returns a real access token of clientID in the realm pda, by
// client_credentials with the secret of .env.example, cached until shortly
// before it expires.
func Token(tb testing.TB, clientID string) string {
	tb.Helper()
	return cachedTokenOf(tb, "pda", clientID)
}

// OtherRealmToken is a valid token of the realm other: another issuer and
// other keys.
func OtherRealmToken(tb testing.TB) string {
	tb.Helper()
	return cachedTokenOf(tb, "other", "other-provider")
}

// FreshToken requests a new token of clientID in the realm pda, bypassing the
// cache (the expiry test needs one issued now).
func FreshToken(tb testing.TB, clientID string) string {
	tb.Helper()
	raw, _, err := requestToken(context.WithoutCancel(tb.Context()), "pda", clientID)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

func cachedTokenOf(tb testing.TB, realm, clientID string) string {
	tb.Helper()
	tokenMu.Lock()
	defer tokenMu.Unlock()
	key := realm + "/" + clientID
	if t, ok := tokenCache[key]; ok && time.Until(t.expires) > tokenMargin {
		return t.raw
	}
	raw, expires, err := requestToken(context.WithoutCancel(tb.Context()), realm, clientID)
	if err != nil {
		tb.Fatal(err)
	}
	tokenCache[key] = cachedToken{raw: raw, expires: expires}
	return raw
}

func requestToken(ctx context.Context, realm, clientID string) (string, time.Time, error) {
	vals, err := LoadDotEnv()
	if err != nil {
		return "", time.Time{}, err
	}
	secret := vals[strings.ToUpper(strings.ReplaceAll(clientID, "-", "_"))+"_SECRET"]
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {secret}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		KeycloakURL+"/realms/"+realm+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	issued := time.Now()
	resp, err := tokenClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: %w", realm, clientID, err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: status %d", realm, clientID, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("testkit: token of %s/%s: no access token: %w", realm, clientID, err)
	}
	return out.AccessToken, issued.Add(time.Duration(out.ExpiresIn) * time.Second), nil
}

// providerClaims are the claims of a real provider-a token: a forged token
// differs from a valid one only in how it is signed.
func providerClaims(tb testing.TB) map[string]any {
	tb.Helper()
	parts := strings.Split(Token(tb, "provider-a"), ".")
	if len(parts) != 3 {
		tb.Fatal("testkit: the provider-a token is not a JWS")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		tb.Fatalf("testkit: token payload: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		tb.Fatalf("testkit: token claims: %v", err)
	}
	return claims
}

func signClaims(tb testing.TB, alg jose.SignatureAlgorithm, key any, claims map[string]any) string {
	tb.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: jose.JSONWebKey{Key: key, KeyID: "forged"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// ForgedToken has the claims of a real provider-a token, signed by an RSA key
// the IdP never published.
func ForgedToken(tb testing.TB) string {
	tb.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatal(err)
	}
	return signClaims(tb, jose.RS256, key, providerClaims(tb))
}

// HS256Token has the claims of a real provider-a token, signed with HS256.
func HS256Token(tb testing.TB) string {
	tb.Helper()
	return signClaims(tb, jose.HS256, []byte("a-shared-secret-of-at-least-32-bytes"), providerClaims(tb))
}

// UnsignedToken has the claims of a real provider-a token and alg "none".
func UnsignedToken(tb testing.TB) string {
	tb.Helper()
	payload, err := json.Marshal(providerClaims(tb))
	if err != nil {
		tb.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + enc(payload) + "."
}
```

Substituir `test/testkit/app.go` (arquivo inteiro):

```go
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
// queues, the accelerated times of test-plan §3.3, OIDC against the compose
// Keycloak with a clock skew of 1 s, and the logs captured for assertions.
// stop stops it and deletes the queues.
func (e *Env) StartApp(ctx context.Context) (*App, func(), error) {
	client, err := rootAWS(ctx)
	if err != nil {
		return nil, nil, err
	}
	wager, dlq, removeQueues, err := createQueues(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	httpAddr, err := freeAddr(ctx)
	if err != nil {
		removeQueues()
		return nil, nil, err
	}
	metricsAddr, err := freeAddr(ctx)
	if err != nil {
		removeQueues()
		return nil, nil, err
	}
	cfg := e.Config()
	cfg.HTTPAddr, cfg.MetricsAddr = httpAddr, metricsAddr
	cfg.WagerQueueName, cfg.WagerDLQName = wager, dlq
	cfg.OIDCIssuer, cfg.OIDCJWKSURL = KeycloakIssuer, KeycloakIssuer+"/protocol/openid-connect/certs"
	cfg.OIDCAudience, cfg.OIDCClockSkew = "pda-api", time.Second
	cfg.APIDocsEnabled = true
	cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay = 100*time.Millisecond, time.Second
	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
	if err := cfg.Validate(); err != nil {
		removeQueues()
		return nil, nil, fmt.Errorf("testkit: app config: %w", err)
	}
	contract, err := LoadContract()
	if err != nil {
		removeQueues()
		return nil, nil, err
	}
	logs := &syncBuffer{}
	app := fx.New(append(bootstrap.Options(),
		fx.Replace(cfg),
		fx.Decorate(func() (*slog.Logger, error) { return observability.NewJSONLogger(logs, "info") }),
	)...)
	if err := app.Start(ctx); err != nil {
		removeQueues()
		return nil, nil, fmt.Errorf("testkit: start the app: %w (logs: %s)", err, logs.String())
	}
	a := &App{
		BaseURL: "http://" + httpAddr, MetricsURL: "http://" + metricsAddr,
		env: e, http: &http.Client{Timeout: requestTimeout}, contract: contract, logs: logs,
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		a.http.CloseIdleConnections()
		_ = app.Stop(ctx)
		removeQueues()
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
```

Substituir `test/testkit/assert.go` (arquivo inteiro):

```go
package testkit

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AssertWalletConsistent is the verification of test-plan §6 for one wallet:
// the reconciliation of the API (item 1), the SQL checks of the ledger (items
// 2–6) and the event matrix of the outbox (item 7). It runs in t.Cleanup, so
// every call detaches from the test's context.
func (a *App) AssertWalletConsistent(tb testing.TB, walletID string) {
	tb.Helper()
	resp := a.Client(tb, "wallet-service").Do(tb, Request{Method: http.MethodPost, Path: "/wallets/" + walletID + "/reconciliation"})
	var r Reconciliation
	resp.JSON(tb, &r)
	if resp.Status != http.StatusOK || !r.Consistent || r.Difference.Amount != "0.00" {
		tb.Errorf("wallet %s: reconciliation %d %+v", walletID, resp.Status, r)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){LedgerProblems, OutboxProblems} {
		problems, err := check(ctx, a.env.Owner, walletID)
		if err != nil {
			tb.Fatalf("wallet %s: %v", walletID, err)
		}
		for _, p := range problems {
			tb.Errorf("wallet %s: %s", walletID, p)
		}
	}
}

// OutboxProblems checks item 7 of test-plan §6 for one wallet: every operation
// has exactly the events of the matrix of lifecycle §7. PROCESSED has one
// WagerTransactionProcessed and, unless it is a LOSS, one WalletBalanceChanged;
// REJECTED has one WagerTransactionRejected; FAILED has none; an operation that
// waited for its reference (expires_at set) has one
// WagerTransactionPendingReference. None means consistent.
func OutboxProblems(ctx context.Context, pool *pgxpool.Pool, walletID string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT t.id::text, t.status, t.kind, t.expires_at IS NOT NULL,
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionProcessed'),
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionRejected'),
		       count(*) FILTER (WHERE o.event_type = 'WalletBalanceChanged'),
		       count(*) FILTER (WHERE o.event_type = 'WagerTransactionPendingReference')
		FROM wager_transactions t
		LEFT JOIN outbox_events o
		  ON o.message_group_id = t.wallet_id::text AND o.payload->'data'->>'transactionId' = t.id::text
		WHERE t.wallet_id = $1
		GROUP BY t.id, t.status, t.kind, t.expires_at
		ORDER BY t.id`, walletID)
	if err != nil {
		return nil, fmt.Errorf("outbox matrix: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var id, status, kind string
		var waited bool
		var processed, rejected, changed, pending int
		if err := rows.Scan(&id, &status, &kind, &waited, &processed, &rejected, &changed, &pending); err != nil {
			return nil, fmt.Errorf("outbox matrix: %w", err)
		}
		want := [4]int{}
		if status == "PROCESSED" {
			want[0] = 1
			if kind != "LOSS" {
				want[2] = 1
			}
		}
		if status == "REJECTED" {
			want[1] = 1
		}
		if waited {
			want[3] = 1
		}
		if got := [4]int{processed, rejected, changed, pending}; got != want {
			problems = append(problems, fmt.Sprintf(
				"operation %s (%s %s): events processed=%d rejected=%d changed=%d pending=%d, want %d %d %d %d",
				id, kind, status, got[0], got[1], got[2], got[3], want[0], want[1], want[2], want[3]))
		}
	}
	return problems, rows.Err()
}

// SnapshotCounts counts the rows of every table, for the tests that prove an
// access had no effect (A03). The counts are global: such a test must not run
// in parallel with tests that write.
func SnapshotCounts(tb testing.TB, pool *pgxpool.Pool) map[string]int64 {
	tb.Helper()
	counts := map[string]int64{}
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "outbox_events", "inbox_messages"} {
		var n int64
		if err := pool.QueryRow(tb.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			tb.Fatalf("count %s: %v", table, err)
		}
		counts[table] = n
	}
	return counts
}

// Owner is the pool of pda_owner, for setups and assertions the app role cannot do.
func (a *App) Owner() *pgxpool.Pool { return a.env.Owner }
```

Substituir `test/testkit/aws.go` (arquivo inteiro):

```go
package testkit

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// MiniStackURL is the emulator as seen from the host.
const MiniStackURL = "http://localhost:4566"

// rootKey is MiniStack's root access key: it bypasses IAM (spike-ministack §3).
const rootKey = "test"

// UseRootAWS points the SDK default chain (used by the app under test) at
// MiniStack with the root key, for the duration of the test.
func UseRootAWS(tb testing.TB) {
	tb.Helper()
	tb.Setenv("AWS_ENDPOINT_URL", MiniStackURL)
	tb.Setenv("AWS_REGION", DotEnv(tb)["AWS_REGION"])
	tb.Setenv("AWS_ACCESS_KEY_ID", rootKey)
	tb.Setenv("AWS_SECRET_ACCESS_KEY", rootKey)
	for _, k := range []string{"AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		tb.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			tb.Fatalf("unset %s: %v", k, err)
		}
	}
}

// RootAWSConfig returns an SDK config for MiniStack with the root key.
func RootAWSConfig(tb testing.TB) aws.Config {
	tb.Helper()
	return AWSConfigWithKeys(tb, AWSKeys{AccessKeyID: rootKey, SecretAccessKey: rootKey})
}

// AWSConfigWithKeys returns an SDK config for MiniStack with explicit keys.
func AWSConfigWithKeys(tb testing.TB, k AWSKeys) aws.Config {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(DotEnv(tb)["AWS_REGION"]),
		awsconfig.WithBaseEndpoint(MiniStackURL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(k.AccessKeyID, k.SecretAccessKey, "")),
	)
	if err != nil {
		tb.Fatalf("aws config: %v", err)
	}
	return cfg
}

// CreateQueues creates an isolated wager queue and DLQ (redrive after 3
// receives, test-plan §3.2) and deletes them at cleanup unless PDA_TEST_KEEP=1.
func CreateQueues(tb testing.TB, client *sqs.Client) (wager, dlq string) {
	tb.Helper()
	wager, dlq, remove, err := createQueues(context.Background(), client)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(remove)
	return wager, dlq
}

// createQueues is CreateQueues for callers without a testing.TB, such as
// TestMain; remove deletes the queues unless PDA_TEST_KEEP=1.
func createQueues(ctx context.Context, client *sqs.Client) (wager, dlq string, remove func(), err error) {
	suffix := uuid.NewString()[:8]
	dlq, wager = "wager-dlq-"+suffix+".fifo", "wager-"+suffix+".fifo"

	dlqOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(dlq), Attributes: map[string]string{"FifoQueue": "true"}})
	if err != nil {
		return "", "", nil, fmt.Errorf("testkit: create %s: %w", dlq, err)
	}
	urls := []*string{dlqOut.QueueUrl}
	remove = func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		for _, url := range urls {
			_, _ = client.DeleteQueue(context.WithoutCancel(ctx), &sqs.DeleteQueueInput{QueueUrl: url})
		}
	}
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: dlqOut.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn}})
	if err != nil {
		remove()
		return "", "", nil, fmt.Errorf("testkit: dlq arn: %w", err)
	}
	redrive := `{"deadLetterTargetArn":"` + attrs.Attributes["QueueArn"] + `","maxReceiveCount":"3"}`
	wagerOut, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(wager), Attributes: map[string]string{"FifoQueue": "true", "RedrivePolicy": redrive}})
	if err != nil {
		remove()
		return "", "", nil, fmt.Errorf("testkit: create %s: %w", wager, err)
	}
	urls = append(urls, wagerOut.QueueUrl)
	return wager, dlq, remove, nil
}

// rootAWS points the SDK default chain of this process, used by the
// application started in process, at MiniStack with the root key, and returns
// a client with the same key. For TestMain, where t.Setenv is not available.
func rootAWS(ctx context.Context) (*sqs.Client, error) {
	vals, err := LoadDotEnv()
	if err != nil {
		return nil, err
	}
	for k, v := range map[string]string{
		"AWS_ENDPOINT_URL": MiniStackURL, "AWS_REGION": vals["AWS_REGION"],
		"AWS_ACCESS_KEY_ID": rootKey, "AWS_SECRET_ACCESS_KEY": rootKey,
	} {
		if err := os.Setenv(k, v); err != nil {
			return nil, err
		}
	}
	for _, k := range []string{"AWS_PROFILE", "AWS_SHARED_CREDENTIALS_FILE"} {
		if err := os.Unsetenv(k); err != nil {
			return nil, err
		}
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(vals["AWS_REGION"]), awsconfig.WithBaseEndpoint(MiniStackURL),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(rootKey, rootKey, "")))
	if err != nil {
		return nil, fmt.Errorf("testkit: aws config: %w", err)
	}
	return sqs.NewFromConfig(cfg), nil
}
```

Substituir `test/testkit/net.go` (arquivo inteiro):

```go
package testkit

import (
	"context"
	"net"
	"testing"
)

// FreeAddr reserves a loopback port and releases it for the code under test.
func FreeAddr(tb testing.TB) string {
	tb.Helper()
	addr, err := freeAddr(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	return addr
}

// freeAddr is FreeAddr for callers without a testing.TB.
func freeAddr(ctx context.Context) (string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}
```

```bash
go mod tidy
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go vet ./test/... && go test -count=1 ./test/testkit/ && go test -tags=integration -race -count=1 -run 'TestHarness|TestProvisioning' ./test/integration/ && go test -tags=integration -race -count=1 ./internal/adapters/postgres/ ./internal/bootstrap/
```

Esperado: `ok` em todos.

**Checkpoint:** o app sobe em processo, as respostas passam pelo contrato, e o `OutboxProblems` detecta cada divergência.

---

### Tarefa 11: carteiras: `POST /wallets`, leituras, ledger e reconciliação (HTTP-01..03, HTTP-07, I08)

Os testes de integração vêm **antes** do handler e ficam vermelhos com o 501 da Tarefa 8, pelo validador de contrato. Os unitários da borda (U17) cobrem a decodificação (decisão 11) com stubs.
- **`decodeBody`:** um objeto JSON, sem campo desconhecido e nada depois dele. Tipo errado responde com o código do campo, e o resto é `MALFORMED_REQUEST`.
- **`limit`:** um valor não inteiro já é recusado na borda; a faixa e o cursor ficam com a consulta.

**Arquivos:**
- Implementação: `internal/adapters/httpapi/dto.go`, `wallets_handler.go` (criar); `placeholder_handlers.go` (substituir)
- Testes: `internal/adapters/httpapi/wallets_handler_test.go`; `stubs_test.go` (acrescentar); `test/integration/wallets_test.go`

**Interfaces:**
- Consome: `httpapi.Services` e `writeError`/`writeProblem`/`correlationID` (Tarefa 8); o harness (Tarefa 10).
- Produz:
  - os internos `decodeBody(r, v) (code, field string, ok bool)` e `moneyInput`;
  - as funções de resposta `walletResponse`, `ledgerResponse` e `reconciliationResponse`;
  - os métodos `openWallet`, `getWallet`, `listLedger` e `reconcile` de `handlers`.

- [ ] **Passo 1: escrever os testes que falham**

Criar `test/integration/wallets_test.go`:

```go
//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-01, HTTP-02, WAL-03 (I20 through the API)
func TestOpenWalletAPI(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")
	player := testkit.NewID()

	created := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": player, "initialBalance": testkit.BRL("1000.00"),
	}})
	var w testkit.Wallet
	created.JSON(t, &w)
	if created.Status != http.StatusCreated || created.Header.Get("Location") != "/wallets/"+w.ID ||
		w.PlayerID != player || w.Balance != testkit.BRL("1000.00") || w.Version != 1 {
		t.Fatalf("POST /wallets = %d %+v (Location %q)", created.Status, w, created.Header.Get("Location"))
	}
	t.Cleanup(func() { server.AssertWalletConsistent(t, w.ID) })

	got := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID})
	var read testkit.Wallet
	got.JSON(t, &read)
	if got.Status != http.StatusOK || read != w {
		t.Fatalf("GET /wallets/{id} = %d %+v, want %+v", got.Status, read, w)
	}

	again := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": player, "initialBalance": testkit.BRL("5.00"),
	}})
	if p := again.Problem(t); again.Status != http.StatusConflict || p.Code != "WALLET_ALREADY_EXISTS" || p.Category != "DEFINITIVE" {
		t.Fatalf("second POST /wallets = %d %+v", again.Status, p)
	}

	zero := server.OpenWallet(t, testkit.BRL("0.00"))
	ledger := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + zero.ID + "/ledger"})
	var page testkit.LedgerPage
	ledger.JSON(t, &page)
	if ledger.Status != http.StatusOK || len(page.Items) != 0 || page.NextCursor != "" {
		t.Fatalf("ledger of a zero opening = %d %+v", ledger.Status, page)
	}
}

// Covers: HTTP-03, HTTP-09
func TestLedgerAPI(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("100.00"))

	resp := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger?limit=10"})
	var page testkit.LedgerPage
	resp.JSON(t, &page)
	if resp.Status != http.StatusOK || len(page.Items) != 1 || page.NextCursor != "" {
		t.Fatalf("GET ledger = %d %+v", resp.Status, page)
	}
	if e := page.Items[0]; e.Direction != "CREDIT" || e.Amount != testkit.BRL("100.00") || e.BalanceBefore != testkit.BRL("0.00") ||
		e.BalanceAfter != testkit.BRL("100.00") || e.WalletVersion != 1 {
		t.Fatalf("opening entry = %+v", e)
	}

	for query, field := range map[string]string{"?limit=0": "limit", "?limit=201": "limit", "?limit=ten": "limit", "?cursor=bogus": "cursor"} {
		bad := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger" + query, Invalid: true})
		if p := bad.Problem(t); bad.Status != http.StatusBadRequest || p.Code != "INVALID_FIELD" || p.Field != field {
			t.Fatalf("GET ledger%s = %d %+v", query, bad.Status, p)
		}
	}
	for _, id := range []string{testkit.NewID(), "not-a-uuid"} {
		missing := internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + id + "/ledger"})
		if p := missing.Problem(t); missing.Status != http.StatusNotFound || p.Code != "WALLET_NOT_FOUND" {
			t.Fatalf("GET ledger of %s = %d %+v", id, missing.Status, p)
		}
	}
}

// shiftBalance changes the stored balance behind the ledger's back. The wallet
// triggers are disabled only inside this transaction: ALTER TABLE holds an
// ACCESS EXCLUSIVE lock until the commit, so no parallel test ever sees them
// disabled. The context is detached: it also runs in t.Cleanup.
func shiftBalance(t *testing.T, walletID string, deltaMinor int64) {
	t.Helper()
	ctx := context.WithoutCancel(t.Context())
	tx, err := server.Owner().Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `ALTER TABLE wallets DISABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE wallets SET balance_minor = balance_minor + $1 WHERE id = $2`, deltaMinor, walletID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE wallets ENABLE TRIGGER USER`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// Covers: HTTP-07, LED-06, OBS-03 (I08)
func TestReconciliation(t *testing.T) {
	t.Parallel()
	internal := server.Client(t, "wallet-service")

	t.Run("consistent", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("975.00"))
		resp := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"})
		var r testkit.Reconciliation
		resp.JSON(t, &r)
		want := testkit.Reconciliation{
			WalletID: w.ID, StoredBalance: testkit.BRL("975.00"), CalculatedBalance: testkit.BRL("975.00"),
			Difference: testkit.BRL("0.00"), Consistent: true, CheckedEntries: 1,
		}
		if resp.Status != http.StatusOK || r != want {
			t.Fatalf("reconciliation = %d %+v, want %+v", resp.Status, r, want)
		}
	})

	t.Run("divergence in the answer, the log and the metric, without changing the balance", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		shiftBalance(t, w.ID, -500)
		t.Cleanup(func() { shiftBalance(t, w.ID, 500) }) // runs before the consistency check
		before := server.Metric(t, "reconciliation_divergences_total")

		resp := internal.Do(t, testkit.Request{
			Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation",
			Header: http.Header{"X-Correlation-Id": {"corr-i08-" + w.ID[:8]}},
		})
		var r testkit.Reconciliation
		resp.JSON(t, &r)
		if resp.Status != http.StatusOK || r.Consistent || r.StoredBalance != testkit.BRL("95.00") ||
			r.CalculatedBalance != testkit.BRL("100.00") || r.Difference != testkit.BRL("-5.00") {
			t.Fatalf("reconciliation = %d %+v", resp.Status, r)
		}
		if after := server.Metric(t, "reconciliation_divergences_total"); after == before || after == "" {
			t.Fatalf("reconciliation_divergences_total %q → %q, want it counted", before, after)
		}
		logs := server.Logs()
		if !strings.Contains(logs, `"msg":"reconciliation divergence"`) || !strings.Contains(logs, "corr-i08-"+w.ID[:8]) {
			t.Fatalf("no WARN with the wallet and the correlation id in the logs")
		}
		var stored testkit.Wallet
		internal.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + w.ID}).JSON(t, &stored)
		if stored.Balance != testkit.BRL("95.00") || stored.Version != 1 {
			t.Fatalf("wallet after the reconciliation = %+v, want untouched", stored)
		}
	})

	t.Run("unknown wallet", func(t *testing.T) {
		t.Parallel()
		resp := internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + testkit.NewID() + "/reconciliation"})
		if p := resp.Problem(t); resp.Status != http.StatusNotFound || p.Code != "WALLET_NOT_FOUND" {
			t.Fatalf("reconciliation of an unknown wallet = %d %+v", resp.Status, p)
		}
	})
}
```

Acrescentar ao fim de `internal/adapters/httpapi/stubs_test.go`:

```go
// decodeJSON decodes an application/json body strictly into v.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (body %s)", ct, rec.Body.String())
	}
	dec := json.NewDecoder(bytes.NewReader(rec.Body.Bytes()))
	dec.DisallowUnknownFields()
	dec.UseNumber() // numbers stay json.Number: no float, as for money
	if err := dec.Decode(v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
}
```

Criar `internal/adapters/httpapi/wallets_handler_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

const (
	walletID = "0192f291-27dd-7d3f-8071-5f8685deef37"
	playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
)

var stamp = time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func openedWallet(t *testing.T, balance string) wallet.Wallet {
	t.Helper()
	w, err := wallet.Open(walletID, playerID, brl(t, balance), stamp)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// stubWallets records what the handlers pass to the use cases.
type stubWallets struct {
	in          app.OpenWalletInput
	correlation string
	opened      wallet.Wallet
	err         error

	cursor string
	limit  int
	page   app.LedgerPage
	calls  int
}

func (s *stubWallets) Execute(_ context.Context, in app.OpenWalletInput, correlationID string) (wallet.Wallet, error) {
	s.in, s.correlation, s.calls = in, correlationID, s.calls+1
	return s.opened, s.err
}

func (s *stubWallets) GetWallet(_ context.Context, id string) (wallet.Wallet, error) {
	s.calls++
	if id != walletID {
		return wallet.Wallet{}, apperrors.New(apperrors.KindNotFound, "WALLET_NOT_FOUND", app.ErrNotFound)
	}
	return s.opened, nil
}

func (s *stubWallets) ListLedger(_ context.Context, id, cursor string, limit int) (app.LedgerPage, error) {
	s.cursor, s.limit, s.calls = cursor, limit, s.calls+1
	return s.page, s.err
}

func (s *stubWallets) GetTransaction(context.Context, string) (*wagering.WagerTransaction, error) {
	return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
}

func (s *stubWallets) GetTransactionByExternalID(context.Context, string, string) (*wagering.WagerTransaction, error) {
	return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
}

type stubReconcile struct {
	walletID, correlation string
	out                   app.Reconciliation
}

func (s *stubReconcile) Execute(_ context.Context, walletID, correlationID string) (app.Reconciliation, error) {
	s.walletID, s.correlation = walletID, correlationID
	return s.out, nil
}

func walletsEdge(t *testing.T, s *stubWallets, r *stubReconcile) edge {
	t.Helper()
	if r == nil {
		r = &stubReconcile{}
	}
	return newEdge(t, httpapi.Services{Wallets: s, Queries: s, Reconcile: r}, false)
}

func postWallet(t *testing.T, e edge, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.do(t, call{
		method: http.MethodPost, path: "/wallets", token: tokenInternal, contentType: "application/json", body: body,
		header: http.Header{"X-Correlation-Id": {"corr-w"}},
	})
}

// Covers: HTTP-01, HTTP-09 (U17: POST /wallets)
func TestOpenWalletHandler(t *testing.T) {
	t.Run("201 with the wallet and its location", func(t *testing.T) {
		s := &stubWallets{opened: openedWallet(t, "1000.00")}
		rec := postWallet(t, walletsEdge(t, s, nil), `{"playerId":"`+playerID+`","initialBalance":{"amount":"1000.00","currency":"BRL"}}`)
		var got map[string]any
		decodeJSON(t, rec, &got)
		if rec.Code != http.StatusCreated || rec.Header().Get("Location") != "/wallets/"+walletID {
			t.Fatalf("POST /wallets = %d, Location %q", rec.Code, rec.Header().Get("Location"))
		}
		balance, _ := got["balance"].(map[string]any)
		if got["id"] != walletID || got["playerId"] != playerID || balance["amount"] != "1000.00" || balance["currency"] != "BRL" ||
			got["version"] != json.Number("1") || got["createdAt"] != "2026-09-29T12:00:00.123456Z" || got["updatedAt"] != got["createdAt"] {
			t.Fatalf("body = %v", got)
		}
		if *s.in.PlayerID != playerID || *s.in.InitialBalance.Amount != "1000.00" || *s.in.InitialBalance.Currency != "BRL" || s.correlation != "corr-w" {
			t.Fatalf("use case got %+v, correlation %q", s.in, s.correlation)
		}
	})

	t.Run("absent and null fields reach the use case as absent", func(t *testing.T) {
		s := &stubWallets{err: apperrors.New(apperrors.KindInput, "MISSING_FIELD", &wagering.ValidationError{Code: wagering.InputMissingField, Field: "playerId"})}
		rec := postWallet(t, walletsEdge(t, s, nil), `{"playerId":null,"initialBalance":{"amount":"1.00","currency":null}}`)
		wantProblem(t, rec, http.StatusBadRequest, "MISSING_FIELD", "playerId")
		if s.in.PlayerID != nil || s.in.InitialBalance == nil || s.in.InitialBalance.Currency != nil {
			t.Fatalf("use case got %+v", s.in)
		}
	})

	cases := []struct {
		name, body, code, field string
	}{
		{"empty body", ``, "MALFORMED_REQUEST", ""},
		{"not an object", `[1]`, "MALFORMED_REQUEST", ""},
		{"broken JSON", `{"playerId":`, "MALFORMED_REQUEST", ""},
		{"unknown field", `{"playerId":"p","extra":1}`, "MALFORMED_REQUEST", ""},
		{"unknown money field", `{"initialBalance":{"amount":"1.00","cents":100}}`, "MALFORMED_REQUEST", ""},
		{"data after the object", `{} {}`, "MALFORMED_REQUEST", ""},
		{"amount as a number", `{"playerId":"p","initialBalance":{"amount":1000.00,"currency":"BRL"}}`, "INVALID_AMOUNT", "initialBalance.amount"},
		{"currency as a number", `{"initialBalance":{"amount":"1.00","currency":986}}`, "INVALID_CURRENCY", "initialBalance.currency"},
		{"money as a string", `{"initialBalance":"1000.00 BRL"}`, "INVALID_FIELD", "initialBalance"},
		{"player as a number", `{"playerId":1}`, "INVALID_FIELD", "playerId"},
		{"body above 64 KB", `{"playerId":"` + strings.Repeat("x", 64<<10) + `"}`, "MALFORMED_REQUEST", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubWallets{}
			wantProblem(t, postWallet(t, walletsEdge(t, s, nil), tc.body), http.StatusBadRequest, tc.code, tc.field)
			if s.calls != 0 {
				t.Fatal("the use case ran for a body that does not decode")
			}
		})
	}

	t.Run("conflict", func(t *testing.T) {
		s := &stubWallets{err: apperrors.New(apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists)}
		wantProblem(t, postWallet(t, walletsEdge(t, s, nil), `{}`), http.StatusConflict, "WALLET_ALREADY_EXISTS", "")
	})
}

// Covers: HTTP-02, HTTP-03 (U17: reads)
func TestWalletReadHandlers(t *testing.T) {
	get := func(t *testing.T, e edge, path string) *httptest.ResponseRecorder {
		t.Helper()
		return e.do(t, call{method: http.MethodGet, path: path, token: tokenInternal})
	}

	t.Run("wallet", func(t *testing.T) {
		e := walletsEdge(t, &stubWallets{opened: openedWallet(t, "5.00")}, nil)
		var got map[string]any
		rec := get(t, e, "/wallets/"+walletID)
		decodeJSON(t, rec, &got)
		if rec.Code != http.StatusOK || got["id"] != walletID {
			t.Fatalf("GET /wallets/{id} = %d %v", rec.Code, got)
		}
		wantProblem(t, get(t, e, "/wallets/"+playerID), http.StatusNotFound, "WALLET_NOT_FOUND", "")
	})

	t.Run("ledger page", func(t *testing.T) {
		w := openedWallet(t, "0.00")
		entry, err := w.Credit("0192f298-3460-7c02-8a10-66778899aabb", "0192f298-345e-7e38-af88-e43f851a819d", brl(t, "25.00"), stamp)
		if err != nil {
			t.Fatal(err)
		}
		s := &stubWallets{page: app.LedgerPage{Entries: []wallet.LedgerEntry{entry}, NextCursor: "eyJ2IjoyfQ"}}
		e := walletsEdge(t, s, nil)
		rec := get(t, e, "/wallets/"+walletID+"/ledger?cursor=abc&limit=10")
		var page struct {
			Items []map[string]any `json:"items"`
			Next  string           `json:"nextCursor"`
		}
		decodeJSON(t, rec, &page)
		if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Next != "eyJ2IjoyfQ" || s.cursor != "abc" || s.limit != 10 {
			t.Fatalf("GET ledger = %d %+v (cursor %q, limit %d)", rec.Code, page, s.cursor, s.limit)
		}
		item := page.Items[0]
		if item["direction"] != "CREDIT" || item["walletVersion"] != json.Number("2") || item["transactionId"] != "0192f298-345e-7e38-af88-e43f851a819d" {
			t.Fatalf("item = %v", item)
		}
	})

	t.Run("ledger defaults and an empty page", func(t *testing.T) {
		s := &stubWallets{}
		rec := get(t, walletsEdge(t, s, nil), "/wallets/"+walletID+"/ledger")
		if rec.Code != http.StatusOK || s.limit != app.DefaultLedgerLimit || s.cursor != "" ||
			strings.TrimSpace(rec.Body.String()) != `{"items":[]}` {
			t.Fatalf("GET ledger = %d %s (limit %d)", rec.Code, rec.Body.String(), s.limit)
		}
	})

	t.Run("ledger limit that is not an integer", func(t *testing.T) {
		s := &stubWallets{}
		wantProblem(t, get(t, walletsEdge(t, s, nil), "/wallets/"+walletID+"/ledger?limit=ten"), http.StatusBadRequest, "INVALID_FIELD", "limit")
		if s.calls != 0 {
			t.Fatal("the query ran with a malformed limit")
		}
	})

	t.Run("reconciliation", func(t *testing.T) {
		difference, err := brl(t, "95.00").Sub(brl(t, "100.00"))
		if err != nil {
			t.Fatal(err)
		}
		r := &stubReconcile{out: app.Reconciliation{
			WalletID: walletID, Stored: brl(t, "95.00"), Calculated: brl(t, "100.00"),
			Difference: difference, Consistent: false, CheckedEntries: 3,
		}}
		e := walletsEdge(t, &stubWallets{}, r)
		rec := e.do(t, call{
			method: http.MethodPost, path: "/wallets/" + walletID + "/reconciliation", token: tokenInternal,
			header: http.Header{"X-Correlation-Id": {"corr-r"}},
		})
		want := `{"walletId":"` + walletID + `","storedBalance":{"amount":"95.00","currency":"BRL"},` +
			`"calculatedBalance":{"amount":"100.00","currency":"BRL"},"difference":{"amount":"-5.00","currency":"BRL"},` +
			`"consistent":false,"checkedEntries":3}`
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want || r.walletID != walletID || r.correlation != "corr-r" {
			t.Fatalf("POST reconciliation = %d %s (use case got %q %q)", rec.Code, rec.Body.String(), r.walletID, r.correlation)
		}
	})
}
```

- [ ] **Passo 2: rodar e ver falhar**

```bash
go vet ./internal/adapters/httpapi/ && go vet -tags=integration ./test/integration/ && go test -count=1 -run 'TestOpenWalletHandler|TestWalletReadHandlers' ./internal/adapters/httpapi/; go test -tags=integration -count=1 -run 'TestOpenWalletAPI|TestLedgerAPI|TestReconciliation' ./test/integration/
```

Esperado: FAIL.
- Unitários: todos os casos, com o status 501 do placeholder.
- Integração: `contract: response 501 to POST /wallets: status is not supported`.

- [ ] **Passo 3: implementar**

Criar `internal/adapters/httpapi/dto.go`:

```go
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// decodeBody decodes the body strictly into v: one JSON object, no unknown
// field, nothing after it (lifecycle §3.1 step 1). A value of the wrong JSON
// type is reported with the code of its field (spec decision 11); every other
// failure, including a body above the limit, is MALFORMED_REQUEST. The fields
// are pointers to strings, so a JSON number is never converted: no float.
func decodeBody(r *http.Request, v any) (code, field string, ok bool) {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return typeErrorCode(typeErr.Field), typeErr.Field, false
	case err != nil:
		return codeMalformedRequest, "", false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return codeMalformedRequest, "", false
	}
	return "", "", true
}

func typeErrorCode(field string) string {
	switch {
	case strings.HasSuffix(field, ".amount"):
		return codeInvalidAmount
	case strings.HasSuffix(field, ".currency"):
		return codeInvalidCurrency
	case field == "kind":
		return codeInvalidKind
	}
	return codeInvalidField
}

// moneyInput is a raw {"amount","currency"}; nil fields are absent.
type moneyInput struct {
	Amount   *string `json:"amount"`
	Currency *string `json:"currency"`
}

func (m *moneyInput) domain() *wagering.MoneyInput {
	if m == nil {
		return nil
	}
	return &wagering.MoneyInput{Amount: m.Amount, Currency: m.Currency}
}

type walletBody struct {
	ID        string      `json:"id"`
	PlayerID  string      `json:"playerId"`
	Balance   money.Money `json:"balance"`
	Version   int64       `json:"version"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}

func walletResponse(w wallet.Wallet) walletBody {
	return walletBody{
		ID: w.ID(), PlayerID: w.PlayerID(), Balance: w.Balance(), Version: w.Version(),
		CreatedAt: w.CreatedAt(), UpdatedAt: w.UpdatedAt(),
	}
}

type ledgerEntryBody struct {
	ID            string      `json:"id"`
	TransactionID string      `json:"transactionId"`
	Direction     string      `json:"direction"`
	Amount        money.Money `json:"amount"`
	BalanceBefore money.Money `json:"balanceBefore"`
	BalanceAfter  money.Money `json:"balanceAfter"`
	WalletVersion int64       `json:"walletVersion"`
	CreatedAt     time.Time   `json:"createdAt"`
}

type ledgerPageBody struct {
	Items      []ledgerEntryBody `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func ledgerResponse(p app.LedgerPage) ledgerPageBody {
	items := make([]ledgerEntryBody, 0, len(p.Entries))
	for i := range p.Entries {
		e := &p.Entries[i]
		items = append(items, ledgerEntryBody{
			ID: e.ID(), TransactionID: e.TransactionID(), Direction: string(e.Direction()), Amount: e.Amount(),
			BalanceBefore: e.BalanceBefore(), BalanceAfter: e.BalanceAfter(), WalletVersion: e.WalletVersion(),
			CreatedAt: e.CreatedAt(),
		})
	}
	return ledgerPageBody{Items: items, NextCursor: p.NextCursor}
}

type reconciliationBody struct {
	WalletID          string      `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int64       `json:"checkedEntries"`
}

func reconciliationResponse(r app.Reconciliation) reconciliationBody {
	return reconciliationBody{
		WalletID: r.WalletID, StoredBalance: r.Stored, CalculatedBalance: r.Calculated,
		Difference: r.Difference, Consistent: r.Consistent, CheckedEntries: r.CheckedEntries,
	}
}
```

Criar `internal/adapters/httpapi/wallets_handler.go`:

```go
package httpapi

import (
	"net/http"
	"strconv"

	"github.com/KaioVinicios/pda/internal/app"
)

// openWallet is POST /wallets (lifecycle §6.4): 201 with the wallet and its
// Location.
func (h handlers) openWallet(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PlayerID       *string     `json:"playerId"`
		InitialBalance *moneyInput `json:"initialBalance"`
	}
	if code, field, ok := decodeBody(r, &body); !ok {
		writeProblem(w, r, code, field)
		return
	}
	ctx := r.Context()
	opened, err := h.s.Wallets.Execute(ctx, app.OpenWalletInput{
		PlayerID: body.PlayerID, InitialBalance: body.InitialBalance.domain(),
	}, correlationID(ctx))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	w.Header().Set("Location", "/wallets/"+opened.ID())
	writeJSON(w, http.StatusCreated, walletResponse(opened))
}

// getWallet is GET /wallets/{walletId}.
func (h handlers) getWallet(w http.ResponseWriter, r *http.Request) {
	found, err := h.s.Queries.GetWallet(r.Context(), r.PathValue("walletId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponse(found))
}

// listLedger is GET /wallets/{walletId}/ledger?cursor=&limit=. A limit that
// is not an integer is rejected here; its range and the cursor are checked by
// the query (D-16).
func (h handlers) listLedger(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := app.DefaultLedgerLimit
	if q.Has("limit") {
		n, err := strconv.Atoi(q.Get("limit"))
		if err != nil {
			writeProblem(w, r, codeInvalidField, "limit")
			return
		}
		limit = n
	}
	page, err := h.s.Queries.ListLedger(r.Context(), r.PathValue("walletId"), q.Get("cursor"), limit)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, ledgerResponse(page))
}

// reconcile is POST /wallets/{walletId}/reconciliation: 200 even when it
// finds a divergence (consistent false).
func (h handlers) reconcile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out, err := h.s.Reconcile.Execute(ctx, r.PathValue("walletId"), correlationID(ctx))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, reconciliationResponse(out))
}
```

Substituir `internal/adapters/httpapi/placeholder_handlers.go` (arquivo inteiro):

```go
package httpapi

import "net/http"

// notImplemented answers the business routes until their handlers exist.
func notImplemented(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotImplemented) }

func (h handlers) submitWager(w http.ResponseWriter, r *http.Request)    { notImplemented(w, r) }
func (h handlers) getTransaction(w http.ResponseWriter, r *http.Request) { notImplemented(w, r) }

func (h handlers) getTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	notImplemented(w, r)
}
```

- [ ] **Passo 4: rodar e ver passar**

```bash
go test -race -count=1 ./internal/adapters/httpapi/ && go test -tags=integration -race -count=1 -run 'TestOpenWalletAPI|TestLedgerAPI|TestReconciliation|TestHarness' ./test/integration/
```

Esperado: `ok`. O I08 prova a divergência na resposta, no log capturado do app e em `reconciliation_divergences_total`.

**Checkpoint:** os comandos do Passo 4 verdes.

---

### Tarefa 12: apostas: `POST /wagering/transactions` e consultas (HTTP-04..06, I09–I12, A02, A03)

A ordem do lifecycle §6.1:
1. decodificação (400);
2. `wagering.NewCommand`, com a chave vinda do header; mais de um header dá `INVALID_IDEMPOTENCY_KEY`;
3. provedor do corpo igual ao do token (403);
4. caso de uso;
5. status do resultado (D-04).

Nas consultas por id, uma transação que o chamador não pode ver responde 404, igual a uma inexistente. Os testes de integração vêm antes e ficam vermelhos com o 501.

**Arquivos:**
- Implementação: `internal/adapters/httpapi/wagering_handler.go` (criar); `dto.go` (acrescentar); `placeholder_handlers.go` (apagar)
- Testes: `internal/adapters/httpapi/wagering_handler_test.go`; `test/integration/helpers_test.go`, `wagering_test.go`, `errors_test.go`, `auth_test.go`

**Interfaces:**
- Consome: `auth.FromContext`, `ActsAs`, `CanSeeTransaction` e `HasRole` (Tarefa 7); `resultStatus` (Tarefa 8); `decodeBody` e `moneyInput` (Tarefa 11).
- Produz:
  - as funções de resposta `resultResponse`, `transactionResponse` e `observedBalance`;
  - os métodos `submitWager`, `getTransaction` e `getTransactionByExternalID` de `handlers`;
  - os helpers de teste `unique`, `wager`, `submit`, `result`, `wantResult`, `balanceOf` e `postWager`.

- [ ] **Passo 1: escrever os testes que falham**

Criar `test/integration/helpers_test.go`:

```go
//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// unique makes an external id unique across tests and runs.
func unique(prefix string) string { return prefix + "-" + testkit.NewID() }

// wager is an operation of provider on w; ref "" = no reference.
func wager(w testkit.Wallet, provider, kind, amount, ext, ref string) testkit.Wager {
	return testkit.Wager{
		ProviderID: provider, ExternalTransactionID: ext, PlayerID: w.PlayerID, WalletID: w.ID,
		RoundID: "round-1", GameID: "game-1", Kind: kind, Money: testkit.BRL(amount),
		ReferenceExternalTransactionID: ref,
	}
}

// submit posts the operation with the key {providerId}:{externalTransactionId}.
func submit(t *testing.T, c *testkit.Client, body testkit.Wager) *testkit.Response {
	t.Helper()
	return c.Do(t, testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
		Header: http.Header{"Idempotency-Key": {body.ProviderID + ":" + body.ExternalTransactionID}},
	})
}

// result submits and decodes the TransactionResult, failing unless the status
// is want.
func result(t *testing.T, c *testkit.Client, body testkit.Wager, want int) testkit.TransactionResult {
	t.Helper()
	resp := submit(t, c, body)
	var r testkit.TransactionResult
	resp.JSON(t, &r)
	if resp.Status != want {
		t.Fatalf("POST %s %s = %d %+v, want %d", body.Kind, body.ExternalTransactionID, resp.Status, r, want)
	}
	return r
}

// wantResult fails unless r has the status, failure code, balance ("" = none)
// and replay flag.
func wantResult(t *testing.T, r testkit.TransactionResult, status, code, balance string, replay bool) {
	t.Helper()
	gotBalance := ""
	if r.Balance != nil {
		gotBalance = r.Balance.Amount
	}
	if r.Status != status || r.FailureCode != code || gotBalance != balance || r.IdempotentReplay != replay {
		t.Fatalf("result = %+v (balance %q), want %s %s %q replay %v", r, gotBalance, status, code, balance, replay)
	}
}

// balanceOf reads the wallet as the internal service.
func balanceOf(t *testing.T, walletID string) testkit.Wallet {
	t.Helper()
	var w testkit.Wallet
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + walletID}).JSON(t, &w)
	return w
}
```

Criar `test/integration/wagering_test.go`:

```go
//go:build integration

package integration_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-04, HTTP-05, HTTP-06, IDEM-05, IDEM-06, TST-A01 (the flow of the M3 definition of done)
func TestHappyPathFlow(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "80.00", unique("bet"), "")

	first := result(t, a, bet, http.StatusOK)
	wantResult(t, first, "PROCESSED", "", "20.00", false)
	replay := result(t, a, bet, http.StatusOK)
	wantResult(t, replay, "PROCESSED", "", "20.00", true)
	if replay.TransactionID != first.TransactionID {
		t.Fatalf("replay id %s, want %s", replay.TransactionID, first.TransactionID)
	}
	rejected := result(t, a, wager(w, "provider-a", "BET", "80.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	wantResult(t, rejected, "REJECTED", "INSUFFICIENT_FUNDS", "20.00", false)
	if rejected.FailureCategory != "DEFINITIVE" {
		t.Fatalf("failure category %q, want DEFINITIVE", rejected.FailureCategory)
	}

	var tx testkit.Transaction
	byID := a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + first.TransactionID})
	byID.JSON(t, &tx)
	if byID.Status != http.StatusOK || tx.TransactionID != first.TransactionID || tx.Origin != "EXTERNAL" || tx.Kind != "BET" ||
		tx.Status != "PROCESSED" || tx.ProviderID != "provider-a" || tx.ReceivedVia != "HTTP" || tx.Balance == nil ||
		tx.Balance.Amount != "20.00" || tx.CompletedAt == nil || tx.Attempts != nil {
		t.Fatalf("GET /wagering/transactions/{id} = %d %+v", byID.Status, tx)
	}
	var sameTx testkit.Transaction
	byExt := a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/provider-a/wagering/transactions/" + bet.ExternalTransactionID})
	byExt.JSON(t, &sameTx)
	if byExt.Status != http.StatusOK || sameTx.TransactionID != first.TransactionID {
		t.Fatalf("GET by external id = %d %+v", byExt.Status, sameTx)
	}

	var r testkit.Reconciliation
	server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"}).JSON(t, &r)
	if !r.Consistent || r.StoredBalance != testkit.BRL("20.00") || r.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v", r)
	}
}

// Covers: IDEM-08 (I10)
func TestReplayReturnsOriginalBalance(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "70.00", false)
	wantResult(t, result(t, a, wager(w, "provider-a", "WIN", "50.00", unique("win"), ""), http.StatusOK), "PROCESSED", "", "120.00", false)

	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "70.00", true)
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("120.00") || got.Version != 3 {
		t.Fatalf("wallet = %+v, want 120.00 v3", got)
	}
}

// Covers: OPS-04..10, OPS-12, TX-06, HTTP-04 (I11)
func TestReversalRules(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")

	t.Run("a REFUND occupies the compensation of its BET (C4)", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bet := unique("bet")
		wantResult(t, result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK), "PROCESSED", "", "70.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "REFUND", "30.00", unique("refund"), bet), http.StatusOK), "PROCESSED", "", "100.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "30.00", unique("rollback"), bet), http.StatusUnprocessableEntity),
			"REJECTED", "ALREADY_REVERSED", "100.00", false)
	})

	t.Run("after a ROLLBACK of the REFUND the BET is not refunded again (C5)", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bet, refund := unique("bet"), unique("refund")
		result(t, a, wager(w, "provider-a", "BET", "20.00", bet, ""), http.StatusOK)
		result(t, a, wager(w, "provider-a", "REFUND", "20.00", refund, bet), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "20.00", unique("rollback"), refund), http.StatusOK), "PROCESSED", "", "80.00", false)
		wantResult(t, result(t, a, wager(w, "provider-a", "REFUND", "20.00", unique("refund"), bet), http.StatusUnprocessableEntity),
			"REJECTED", "ALREADY_REVERSED", "80.00", false)
	})

	t.Run("ROLLBACK of a WIN debits it back", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		win := unique("win")
		result(t, a, wager(w, "provider-a", "WIN", "50.00", win, ""), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "50.00", unique("rollback"), win), http.StatusOK), "PROCESSED", "", "100.00", false)
	})

	t.Run("a reversal without funds has its own code", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("10.00"))
		win := unique("win")
		result(t, a, wager(w, "provider-a", "WIN", "100.00", win, ""), http.StatusOK)
		result(t, a, wager(w, "provider-a", "BET", "110.00", unique("bet"), ""), http.StatusOK)
		reversal := result(t, a, wager(w, "provider-a", "ROLLBACK", "100.00", unique("rollback"), win), http.StatusUnprocessableEntity)
		bet := result(t, a, wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), http.StatusUnprocessableEntity)
		wantResult(t, reversal, "REJECTED", "REVERSAL_INSUFFICIENT_FUNDS", "0.00", false)
		wantResult(t, bet, "REJECTED", "INSUFFICIENT_FUNDS", "0.00", false)
	})

	t.Run("a reversal before its reference waits", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		pending := result(t, a, wager(w, "provider-a", "REFUND", "10.00", unique("refund"), unique("bet")), http.StatusAccepted)
		wantResult(t, pending, "PENDING_REFERENCE", "", "", false)
		var tx testkit.Transaction
		a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + pending.TransactionID}).JSON(t, &tx)
		if tx.Status != "PENDING_REFERENCE" || tx.Attempts == nil || tx.NextAttemptAt == nil || tx.ExpiresAt == nil ||
			tx.Balance != nil || tx.CompletedAt != nil {
			t.Fatalf("pending operation = %+v", tx)
		}
	})
}

// Covers: HTTP-03, LED-06 (I09)
func TestLedgerPagination(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	for range 119 {
		result(t, a, wager(w, "provider-a", "BET", "1.00", unique("bet"), ""), http.StatusOK)
	}

	var versions []int64
	var sizes []int
	cursor := ""
	for {
		path := "/wallets/" + w.ID + "/ledger?limit=50"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		var page testkit.LedgerPage
		internal.Do(t, testkit.Request{Method: http.MethodGet, Path: path}).JSON(t, &page)
		sizes = append(sizes, len(page.Items))
		for _, e := range page.Items {
			versions = append(versions, e.WalletVersion)
		}
		if page.NextCursor == "" {
			break
		}
		if strings.Contains(page.NextCursor, fmt.Sprint(versions[len(versions)-1])) {
			t.Fatalf("cursor %q shows the version: it must be opaque", page.NextCursor)
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(sizes) != "[50 50 20]" {
		t.Fatalf("page sizes %v, want [50 50 20]", sizes)
	}
	for i, v := range versions {
		if v != int64(i+1) {
			t.Fatalf("versions %v: repetition or gap at %d", versions, i)
		}
	}
}
```

Criar `test/integration/errors_test.go`:

```go
//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: HTTP-09, D-04, D-06, IDEM-02, IDEM-06, IDEM-07 (I12)
//
// Every code of lifecycle §5.3 that a request can provoke answers its status,
// application/problem+json and the code; INTERNAL_ERROR and
// TEMPORARILY_UNAVAILABLE are covered by U17 and R01 (spec decision 20).
func TestHTTPErrorContract(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	internal := server.Client(t, "wallet-service")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	placed := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	result(t, a, placed, http.StatusOK)

	valid := func(mutate func(*testkit.Wager)) testkit.Wager {
		b := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
		mutate(&b)
		return b
	}
	post := func(body any, key string, invalid bool) testkit.Request {
		r := testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: body, Invalid: invalid}
		if key != "" {
			r.Header = http.Header{"Idempotency-Key": {key}}
		}
		return r
	}
	withoutRound := map[string]any{
		"providerId": "provider-a", "externalTransactionId": unique("bet"), "playerId": w.PlayerID, "walletId": w.ID,
		"gameId": "game-1", "kind": "BET", "money": testkit.BRL("10.00"),
	}
	player := testkit.NewID()
	internal.Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": player, "initialBalance": testkit.BRL("0.00")}})

	cases := []struct {
		name   string
		client *testkit.Client
		req    testkit.Request
		status int
		code   string
		field  string
	}{
		{"malformed JSON", a, post(`{"providerId":`, "k-1", true), 400, "MALFORMED_REQUEST", ""},
		{"unknown field", a, post(map[string]any{"extra": 1}, "k-1", true), 400, "MALFORMED_REQUEST", ""},
		{"missing key", a, post(valid(func(*testkit.Wager) {}), "", true), 400, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"invalid key", a, post(valid(func(*testkit.Wager) {}), "has space", true), 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"missing field", a, post(withoutRound, "k-2", true), 400, "MISSING_FIELD", "roundId"},
		{"empty text", a, post(valid(func(b *testkit.Wager) { b.RoundID = "" }), "k-2", true), 400, "INVALID_FIELD", "roundId"},
		{"invalid uuid", a, post(valid(func(b *testkit.Wager) { b.PlayerID = "not-a-uuid" }), "k-3", true), 400, "INVALID_FIELD", "playerId"},
		{"invalid amount", a, post(valid(func(b *testkit.Wager) { b.Money.Amount = "25" }), "k-4", true), 400, "INVALID_AMOUNT", "money.amount"},
		{"invalid currency", a, post(valid(func(b *testkit.Wager) { b.Money.Currency = "XYZ" }), "k-5", true), 400, "INVALID_CURRENCY", "money.currency"},
		{"invalid kind", a, post(valid(func(b *testkit.Wager) { b.Kind = "JACKPOT" }), "k-6", true), 400, "INVALID_KIND", "kind"},
		{"opening", a, post(valid(func(b *testkit.Wager) { b.Kind = "OPENING" }), "k-7", true), 400, "OPENING_NOT_ALLOWED", "kind"},
		{"zero amount", a, post(valid(func(b *testkit.Wager) { b.Money.Amount = "0.00" }), "k-8", false), 400, "ZERO_AMOUNT_NOT_ALLOWED", "money.amount"},
		{"loss with amount", a, post(valid(func(b *testkit.Wager) { b.Kind = "LOSS" }), "k-9", false), 400, "LOSS_AMOUNT_MUST_BE_ZERO", "money.amount"},
		{"refund without reference", a, post(valid(func(b *testkit.Wager) { b.Kind = "REFUND" }), "k-10", false), 400, "REFERENCE_REQUIRED", "referenceExternalTransactionId"},
		{"bet with reference", a, post(valid(func(b *testkit.Wager) { b.ReferenceExternalTransactionID = "x" }), "k-11", false), 400, "REFERENCE_NOT_ALLOWED", "referenceExternalTransactionId"},
		{"self reference", a, post(valid(func(b *testkit.Wager) { b.Kind, b.ReferenceExternalTransactionID = "WIN", b.ExternalTransactionID }), "k-12", false), 400, "SELF_REFERENCE", "referenceExternalTransactionId"},
		{"unknown wallet", a, post(valid(func(b *testkit.Wager) { b.WalletID = testkit.NewID() }), unique("k"), false), 400, "UNKNOWN_WALLET", ""},
		{"no token", server.Client(t, ""), post(valid(func(*testkit.Wager) {}), "k-13", false), 401, "UNAUTHENTICATED", ""},
		{"role of the internal service", internal, post(valid(func(*testkit.Wager) {}), "k-14", false), 403, "FORBIDDEN", ""},
		{"another provider in the body", a, post(valid(func(b *testkit.Wager) { b.ProviderID = "provider-b" }), "k-15", false), 403, "PROVIDER_MISMATCH", ""},
		{"wallet not found", internal, testkit.Request{Method: http.MethodGet, Path: "/wallets/" + testkit.NewID()}, 404, "WALLET_NOT_FOUND", ""},
		{"transaction not found", a, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + testkit.NewID()}, 404, "TRANSACTION_NOT_FOUND", ""},
		{"route not found", a, testkit.Request{Method: http.MethodGet, Path: "/nope"}, 404, "ROUTE_NOT_FOUND", ""},
		{"method not allowed", a, testkit.Request{Method: http.MethodDelete, Path: "/wallets"}, 405, "METHOD_NOT_ALLOWED", ""},
		{"key reused", a, post(valid(func(b *testkit.Wager) {
			b.ExternalTransactionID = placed.ExternalTransactionID
			b.Money.Amount = "11.00"
		}), "provider-a:"+placed.ExternalTransactionID, false), 409, "IDEMPOTENCY_KEY_REUSED", ""},
		{"external id with another key", a, post(valid(func(b *testkit.Wager) { b.ExternalTransactionID = placed.ExternalTransactionID }), unique("other-key"), false), 409, "EXTERNAL_TRANSACTION_ID_CONFLICT", ""},
		{"wallet exists", internal, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": player, "initialBalance": testkit.BRL("0.00")}}, 409, "WALLET_ALREADY_EXISTS", ""},
		{"media type", a, testkit.Request{Method: http.MethodPost, Path: "/wagering/transactions", Body: "{}", Header: http.Header{"Content-Type": {"text/plain"}, "Idempotency-Key": {"k-16"}}, Invalid: true}, 415, "UNSUPPORTED_MEDIA_TYPE", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.client.Do(t, tc.req)
			p := resp.Problem(t)
			if resp.Status != tc.status || p.Status != tc.status || p.Code != tc.code || (tc.field != "" && p.Field != tc.field) {
				t.Fatalf("%d %+v, want %d %s (field %q)", resp.Status, p, tc.status, tc.code, tc.field)
			}
		})
	}
}
```

Criar `test/integration/auth_test.go`:

```go
//go:build integration

package integration_test

import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-05, TST-A02, E2 (A02a)
func TestProviderIsolationQueries(t *testing.T) {
	t.Parallel()
	a, b := server.Client(t, "provider-a"), server.Client(t, "provider-b")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	placed := result(t, a, bet, http.StatusOK)

	byID := b.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + placed.TransactionID})
	if p := byID.Problem(t); byID.Status != http.StatusNotFound || p.Code != "TRANSACTION_NOT_FOUND" {
		t.Fatalf("provider-b reads a transaction of provider-a by id: %d %+v", byID.Status, p)
	}
	byExt := b.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/provider-a/wagering/transactions/" + bet.ExternalTransactionID})
	if p := byExt.Problem(t); byExt.Status != http.StatusForbidden || p.Code != "PROVIDER_MISMATCH" {
		t.Fatalf("provider-b reads under /providers/provider-a: %d %+v", byExt.Status, p)
	}
	for _, body := range [][]byte{byID.Body, byExt.Body} {
		if strings.Contains(string(body), placed.TransactionID) || strings.Contains(string(body), w.ID) {
			t.Fatalf("the answer exposes data of provider-a: %s", body)
		}
	}
	opening := server.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + placed.TransactionID})
	if opening.Status != http.StatusOK {
		t.Fatalf("the internal service reads the transaction: %d", opening.Status)
	}
}

// Covers: AUTH-04, AUTH-05, TST-A02, E2 (A02b)
func TestProviderIsolationReplay(t *testing.T) {
	t.Parallel()
	a, b := server.Client(t, "provider-a"), server.Client(t, "provider-b")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	placed := result(t, a, bet, http.StatusOK)

	replay := submit(t, b, bet) // the body and the key of provider-a
	if p := replay.Problem(t); replay.Status != http.StatusForbidden || p.Code != "PROVIDER_MISMATCH" ||
		strings.Contains(string(replay.Body), placed.TransactionID) {
		t.Fatalf("provider-b replays provider-a: %d %s", replay.Status, replay.Body)
	}
	wantResult(t, result(t, a, bet, http.StatusOK), "PROCESSED", "", "90.00", true)
}

// Covers: AUTH-07, TST-A03, E2 (A03)
//
// Not parallel: the counts are global, so no other test may write meanwhile.
func TestUnauthorizedHasNoEffects(t *testing.T) {
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "10.00", unique("bet"), "")
	before := testkit.SnapshotCounts(t, server.Owner())

	attempts := []struct {
		name   string
		client *testkit.Client
		req    testkit.Request
		status int
	}{
		{"no token", server.Client(t, ""), postWager(bet), http.StatusUnauthorized},
		{"forged token", server.ClientWithToken(testkit.ForgedToken(t)), postWager(bet), http.StatusUnauthorized},
		{"another provider", server.Client(t, "provider-b"), postWager(bet), http.StatusForbidden},
		{"internal service", server.Client(t, "wallet-service"), postWager(bet), http.StatusForbidden},
		{"no role", server.Client(t, "no-role-client"), postWager(bet), http.StatusForbidden},
		{"provider opens a wallet", a, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": testkit.NewID(), "initialBalance": testkit.BRL("5.00")}}, http.StatusForbidden},
		{"provider reconciles", a, testkit.Request{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"}, http.StatusForbidden},
	}
	for _, at := range attempts {
		if resp := at.client.Do(t, at.req); resp.Status != at.status {
			t.Fatalf("%s: %d %s, want %d", at.name, resp.Status, resp.Body, at.status)
		}
	}
	if after := testkit.SnapshotCounts(t, server.Owner()); !maps.Equal(before, after) {
		t.Fatalf("rows changed: before %v, after %v", before, after)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("100.00") || got.Version != 1 {
		t.Fatalf("wallet = %+v, want untouched", got)
	}
}

func postWager(body testkit.Wager) testkit.Request {
	return testkit.Request{
		Method: http.MethodPost, Path: "/wagering/transactions", Body: body,
		Header: http.Header{"Idempotency-Key": {body.ProviderID + ":" + body.ExternalTransactionID}},
	}
}
```

Criar `internal/adapters/httpapi/wagering_handler_test.go`:

```go
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const (
	txID    = "0192f298-345e-7e38-af88-e43f851a819d"
	entryID = "0192f298-3460-7c02-8a10-66778899aabb"
)

func ptr(s string) *string { return &s }

func command(t *testing.T, kind, amount, ext, ref string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr("provider-a:" + ext), ProviderID: ptr("provider-a"), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(playerID), WalletID: ptr(walletID), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(kind), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr("BRL")},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// settled is the operation after Settle on a wallet with balance.
func settled(t *testing.T, cmd wagering.Command, balance string) *wagering.WagerTransaction {
	t.Helper()
	w := openedWallet(t, balance)
	tx, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, "corr", stamp)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 3, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wagering.Settle(tx, &w, wagering.Reference{}, wagering.SettleParams{EntryID: entryID, Now: stamp, Policy: policy}); err != nil {
		t.Fatal(err)
	}
	return tx
}

func failed(t *testing.T, cmd wagering.Command) *wagering.WagerTransaction {
	t.Helper()
	tx, err := wagering.NewExternal(txID, cmd, wagering.ReceivedViaHTTP, "corr", stamp)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Fail(stamp); err != nil {
		t.Fatal(err)
	}
	return tx
}

func opening(t *testing.T) *wagering.WagerTransaction {
	t.Helper()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: walletID, PlayerID: playerID, Initial: brl(t, "100.00"),
		TransactionID: txID, EntryID: entryID, CorrelationID: "corr", Now: stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.Tx
}

type stubWagers struct {
	req   app.ProcessRequest
	res   app.ProcessResult
	err   error
	calls int
}

func (s *stubWagers) Execute(_ context.Context, req app.ProcessRequest) (app.ProcessResult, error) {
	s.req, s.calls = req, s.calls+1
	return s.res, s.err
}

// stubReader serves one operation.
type stubReader struct {
	stubWallets
	tx                 *wagering.WagerTransaction
	provider, external string
	calls              int
}

func (s *stubReader) GetTransaction(_ context.Context, id string) (*wagering.WagerTransaction, error) {
	s.calls++
	if s.tx == nil || id != s.tx.ID() {
		return nil, apperrors.New(apperrors.KindNotFound, "TRANSACTION_NOT_FOUND", app.ErrNotFound)
	}
	return s.tx, nil
}

func (s *stubReader) GetTransactionByExternalID(_ context.Context, provider, external string) (*wagering.WagerTransaction, error) {
	s.provider, s.external, s.calls = provider, external, s.calls+1
	return s.tx, nil
}

const betBody = `{"providerId":"provider-a","externalTransactionId":"bet-1","playerId":"` + playerID + `","walletId":"` + walletID +
	`","roundId":"round-1","gameId":"game-1","kind":"BET","money":{"amount":"25.00","currency":"BRL"}}`

func postWager(t *testing.T, s *stubWagers, token, body string, keys ...string) *httptest.ResponseRecorder {
	t.Helper()
	h := http.Header{"X-Correlation-Id": {"corr-wager"}}
	for _, k := range keys {
		h.Add("Idempotency-Key", k)
	}
	e := newEdge(t, httpapi.Services{Wagers: s}, false)
	return e.do(t, call{method: http.MethodPost, path: "/wagering/transactions", token: token, contentType: "application/json", body: body, header: h})
}

// Covers: HTTP-06, HTTP-09, AUTH-04, D-04 (U17: POST /wagering/transactions)
func TestSubmitWagerHandler(t *testing.T) {
	t.Run("the command reaches the use case", func(t *testing.T) {
		s := &stubWagers{res: app.ProcessResult{Tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}}
		rec := postWager(t, s, tokenProviderA, betBody, "provider-a:bet-1")
		want := `{"transactionId":"` + txID + `","status":"PROCESSED","balance":{"amount":"75.00","currency":"BRL"},"idempotentReplay":false}`
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
		}
		if s.req.Via != wagering.ReceivedViaHTTP || s.req.CorrelationID != "corr-wager" || s.req.CausationID != "" ||
			s.req.Command.IdempotencyKey() != "provider-a:bet-1" || s.req.Command.PayloadHash() != command(t, "BET", "25.00", "bet-1", "").PayloadHash() {
			t.Fatalf("request = %+v", s.req)
		}
	})

	results := []struct {
		name   string
		res    func(t *testing.T) app.ProcessResult
		status int
		want   string
	}{
		{"replay", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00"), Replay: true}
		}, 200, `"status":"PROCESSED","balance":{"amount":"75.00","currency":"BRL"},"idempotentReplay":true}`},
		{"rejection", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "BET", "500.00", "bet-1", ""), "100.00")}
		}, 422, `"status":"REJECTED","balance":{"amount":"100.00","currency":"BRL"},"failureCode":"INSUFFICIENT_FUNDS","failureCategory":"DEFINITIVE","idempotentReplay":false}`},
		{"pending reference", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: settled(t, command(t, "REFUND", "25.00", "refund-1", "bet-9"), "100.00")}
		}, 202, `"status":"PENDING_REFERENCE","idempotentReplay":false}`},
		{"recorded permanent failure", func(t *testing.T) app.ProcessResult {
			t.Helper()
			return app.ProcessResult{Tx: failed(t, command(t, "BET", "25.00", "bet-1", ""))}
		}, 500, `"status":"FAILED","failureCode":"INTERNAL_PERMANENT_FAILURE","failureCategory":"DEFINITIVE","idempotentReplay":false}`},
	}
	for _, tc := range results {
		t.Run(tc.name, func(t *testing.T) {
			rec := postWager(t, &stubWagers{res: tc.res(t)}, tokenProviderA, betBody, "provider-a:bet-1")
			if rec.Code != tc.status || rec.Header().Get("Content-Type") != "application/json" || !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), tc.want) {
				t.Fatalf("POST = %d %s, want %d …%s", rec.Code, rec.Body.String(), tc.status, tc.want)
			}
		})
	}

	rejected := []struct {
		name, token, body string
		keys              []string
		status            int
		code, field       string
	}{
		{"missing key", tokenProviderA, betBody, nil, 400, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"empty key", tokenProviderA, betBody, []string{""}, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"repeated key", tokenProviderA, betBody, []string{"k-1", "k-2"}, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key"},
		{"opening", tokenProviderA, strings.Replace(betBody, `"BET"`, `"OPENING"`, 1), []string{"k"}, 400, "OPENING_NOT_ALLOWED", "kind"},
		{"amount as a number", tokenProviderA, strings.Replace(betBody, `"25.00"`, `25.00`, 1), []string{"k"}, 400, "INVALID_AMOUNT", "money.amount"},
		{"kind as a number", tokenProviderA, strings.Replace(betBody, `"BET"`, `1`, 1), []string{"k"}, 400, "INVALID_KIND", "kind"},
		{"unknown field", tokenProviderA, strings.Replace(betBody, `"kind"`, `"x":1,"kind"`, 1), []string{"k"}, 400, "MALFORMED_REQUEST", ""},
		{"another provider", tokenProviderB, betBody, []string{"k"}, 403, "PROVIDER_MISMATCH", ""},
		{"validation before the provider", tokenProviderB, strings.Replace(betBody, `"25.00"`, `"25"`, 1), []string{"k"}, 400, "INVALID_AMOUNT", "money.amount"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubWagers{}
			wantProblem(t, postWager(t, s, tc.token, tc.body, tc.keys...), tc.status, tc.code, tc.field)
			if s.calls != 0 {
				t.Fatal("the use case ran")
			}
		})
	}

	t.Run("errors of the use case", func(t *testing.T) {
		conflict := apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", &wagering.ConflictError{Code: wagering.InputIdempotencyKeyReused})
		wantProblem(t, postWager(t, &stubWagers{err: conflict}, tokenProviderA, betBody, "k"), 409, "IDEMPOTENCY_KEY_REUSED", "")
		unknown := apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", app.ErrNotFound)
		wantProblem(t, postWager(t, &stubWagers{err: unknown}, tokenProviderA, betBody, "k"), 400, "UNKNOWN_WALLET", "")
	})
}

// Covers: HTTP-04, HTTP-05, AUTH-05, D-04 (U17: transaction reads)
func TestTransactionReadHandlers(t *testing.T) {
	get := func(t *testing.T, r *stubReader, token, path string) *httptest.ResponseRecorder {
		t.Helper()
		return newEdge(t, httpapi.Services{Queries: r}, false).do(t, call{method: http.MethodGet, path: path, token: token})
	}
	decode := func(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
		t.Helper()
		var got map[string]any
		decodeJSON(t, rec, &got)
		return got
	}

	t.Run("an operation is seen by its provider and the internal service", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}
		got := decode(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID))
		if got["transactionId"] != txID || got["origin"] != "EXTERNAL" || got["kind"] != "BET" || got["status"] != "PROCESSED" ||
			got["providerId"] != "provider-a" || got["externalTransactionId"] != "bet-1" || got["receivedVia"] != "HTTP" ||
			got["completedAt"] != "2026-09-29T12:00:00.123456Z" || got["attempts"] != nil || got["expiresAt"] != nil {
			t.Fatalf("representation = %v", got)
		}
		if balance, _ := got["balance"].(map[string]any); balance["amount"] != "75.00" {
			t.Fatalf("balance = %v", got["balance"])
		}
		if rec := get(t, r, tokenInternal, "/wagering/transactions/"+txID); rec.Code != http.StatusOK {
			t.Fatalf("internal service: %d", rec.Code)
		}
		wantProblem(t, get(t, r, tokenProviderB, "/wagering/transactions/"+txID), http.StatusNotFound, "TRANSACTION_NOT_FOUND", "")
	})

	t.Run("a pending operation shows its schedule", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "REFUND", "25.00", "refund-1", "bet-9"), "100.00")}
		got := decode(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID))
		if got["status"] != "PENDING_REFERENCE" || got["attempts"] != json.Number("0") || got["nextAttemptAt"] == nil ||
			got["expiresAt"] == nil || got["balance"] != nil || got["completedAt"] != nil || got["referenceExternalTransactionId"] != "bet-9" {
			t.Fatalf("representation = %v", got)
		}
	})

	t.Run("the opening is seen only by the internal service", func(t *testing.T) {
		r := &stubReader{tx: opening(t)}
		wantProblem(t, get(t, r, tokenProviderA, "/wagering/transactions/"+txID), http.StatusNotFound, "TRANSACTION_NOT_FOUND", "")
		got := decode(t, get(t, r, tokenInternal, "/wagering/transactions/"+txID))
		for _, key := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "receivedVia"} {
			if _, ok := got[key]; ok {
				t.Fatalf("the opening has %s: %v", key, got)
			}
		}
		if got["kind"] != "OPENING" || got["origin"] != "INTERNAL" {
			t.Fatalf("representation = %v", got)
		}
	})

	t.Run("by external id", func(t *testing.T) {
		r := &stubReader{tx: settled(t, command(t, "BET", "25.00", "bet-1", ""), "100.00")}
		wantProblem(t, get(t, r, tokenProviderA, "/providers/provider-b/wagering/transactions/bet-1"), http.StatusForbidden, "PROVIDER_MISMATCH", "")
		if r.calls != 0 {
			t.Fatal("the query ran for another provider")
		}
		if rec := get(t, r, tokenProviderA, "/providers/provider-a/wagering/transactions/bet-1"); rec.Code != http.StatusOK || r.provider != "provider-a" || r.external != "bet-1" {
			t.Fatalf("own provider: %d (%q %q)", rec.Code, r.provider, r.external)
		}
		if rec := get(t, r, tokenInternal, "/providers/provider-b/wagering/transactions/bet-1"); rec.Code != http.StatusOK || r.provider != "provider-b" {
			t.Fatalf("internal service: %d (%q)", rec.Code, r.provider)
		}
	})
}
```

- [ ] **Passo 2: rodar e ver falhar**

```bash
go vet ./internal/adapters/httpapi/ && go vet -tags=integration ./test/integration/ && go test -count=1 -run 'TestSubmitWagerHandler|TestTransactionReadHandlers' ./internal/adapters/httpapi/; go test -tags=integration -count=1 ./test/integration/
```

Esperado: FAIL.
- Unitários: todos os casos, com o 501.
- Integração: `contract: response 501 to POST /wagering/transactions: status is not supported` em `TestHappyPathFlow`, I09–I12, A02a/b e A03.
- Os testes da Tarefa 11 seguem verdes.

- [ ] **Passo 3: implementar**

Acrescentar ao fim de `internal/adapters/httpapi/dto.go`:

```go
// resultBody is the TransactionResult of a recorded operation (D-04).
type resultBody struct {
	TransactionID    string       `json:"transactionId"`
	Status           string       `json:"status"`
	Balance          *money.Money `json:"balance,omitempty"`
	FailureCode      string       `json:"failureCode,omitempty"`
	FailureCategory  string       `json:"failureCategory,omitempty"`
	IdempotentReplay bool         `json:"idempotentReplay"`
}

func resultResponse(tx *wagering.WagerTransaction, replay bool) resultBody {
	b := resultBody{TransactionID: tx.ID(), Status: string(tx.Status()), IdempotentReplay: replay}
	b.Balance = observedBalance(tx)
	if code := tx.FailureCode(); code != "" {
		b.FailureCode, b.FailureCategory = string(code), string(code.Category())
	}
	return b
}

// observedBalance is the balance recorded by PROCESSED and REJECTED (the one
// replays return, IDEM-08); nil for the other states.
func observedBalance(tx *wagering.WagerTransaction) *money.Money {
	if b := tx.ResultBalance(); b.Currency().Valid() {
		return &b
	}
	return nil
}

// transactionBody is the full representation of an operation (D-04).
type transactionBody struct {
	TransactionID                  string       `json:"transactionId"`
	Origin                         string       `json:"origin"`
	Kind                           string       `json:"kind"`
	Status                         string       `json:"status"`
	WalletID                       string       `json:"walletId"`
	PlayerID                       string       `json:"playerId"`
	Money                          money.Money  `json:"money"`
	ProviderID                     string       `json:"providerId,omitempty"`
	ExternalTransactionID          string       `json:"externalTransactionId,omitempty"`
	RoundID                        string       `json:"roundId,omitempty"`
	GameID                         string       `json:"gameId,omitempty"`
	ReceivedVia                    string       `json:"receivedVia,omitempty"`
	ReferenceExternalTransactionID string       `json:"referenceExternalTransactionId,omitempty"`
	ReferenceTransactionID         string       `json:"referenceTransactionId,omitempty"`
	Balance                        *money.Money `json:"balance,omitempty"`
	FailureCode                    string       `json:"failureCode,omitempty"`
	FailureCategory                string       `json:"failureCategory,omitempty"`
	Attempts                       *int         `json:"attempts,omitempty"`
	NextAttemptAt                  *time.Time   `json:"nextAttemptAt,omitempty"`
	ExpiresAt                      *time.Time   `json:"expiresAt,omitempty"`
	CreatedAt                      time.Time    `json:"createdAt"`
	UpdatedAt                      time.Time    `json:"updatedAt"`
	CompletedAt                    *time.Time   `json:"completedAt,omitempty"`
}

func transactionResponse(tx *wagering.WagerTransaction) transactionBody {
	b := transactionBody{
		TransactionID: tx.ID(), Origin: string(tx.Origin()), Kind: string(tx.Kind()), Status: string(tx.Status()),
		WalletID: tx.WalletID(), PlayerID: tx.PlayerID(), Money: tx.Money(),
		ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(),
		RoundID: tx.RoundID(), GameID: tx.GameID(), ReceivedVia: string(tx.ReceivedVia()),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		ReferenceTransactionID:         tx.ReferenceTransactionID(),
		Balance:                        observedBalance(tx),
		CreatedAt:                      tx.CreatedAt(), UpdatedAt: tx.UpdatedAt(),
	}
	if code := tx.FailureCode(); code != "" {
		b.FailureCode, b.FailureCategory = string(code), string(code.Category())
	}
	if tx.Status() == wagering.StatusPendingReference {
		attempts, next, expires := tx.Attempts(), tx.NextAttemptAt(), tx.ExpiresAt()
		b.Attempts, b.NextAttemptAt, b.ExpiresAt = &attempts, &next, &expires
	}
	if completed := tx.CompletedAt(); !completed.IsZero() {
		b.CompletedAt = &completed
	}
	return b
}
```

Criar `internal/adapters/httpapi/wagering_handler.go`:

```go
package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// submitWager is POST /wagering/transactions (lifecycle §6.1): decode (400) →
// stateless validation by the domain (400) → the provider of the body must be
// the token's (403, before any read) → the use case → the status of the
// recorded outcome (D-04).
func (h handlers) submitWager(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderID                     *string     `json:"providerId"`
		ExternalTransactionID          *string     `json:"externalTransactionId"`
		PlayerID                       *string     `json:"playerId"`
		WalletID                       *string     `json:"walletId"`
		RoundID                        *string     `json:"roundId"`
		GameID                         *string     `json:"gameId"`
		Kind                           *string     `json:"kind"`
		Money                          *moneyInput `json:"money"`
		ReferenceExternalTransactionID *string     `json:"referenceExternalTransactionId"`
	}
	if code, field, ok := decodeBody(r, &body); !ok {
		writeProblem(w, r, code, field)
		return
	}
	in := wagering.Input{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		PlayerID: body.PlayerID, WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID,
		Kind: body.Kind, Money: body.Money.domain(), ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	}
	switch keys := r.Header.Values("Idempotency-Key"); len(keys) {
	case 0:
	case 1:
		in.IdempotencyKey = &keys[0]
	default:
		writeProblem(w, r, codeInvalidIdempotencyKey, "Idempotency-Key")
		return
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	ctx := r.Context()
	if p, _ := auth.FromContext(ctx); !auth.ActsAs(p, cmd.ProviderID()) {
		writeProblem(w, r, codeProviderMismatch, "")
		return
	}
	res, err := h.s.Wagers.Execute(ctx, app.ProcessRequest{
		Command: cmd, Via: wagering.ReceivedViaHTTP, CorrelationID: correlationID(ctx),
	})
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, resultStatus(res.Tx.Status()), resultResponse(res.Tx, res.Replay))
}

// getTransaction is GET /wagering/transactions/{transactionId}: a provider
// only sees its own operations; any other answers 404, like a missing one,
// so the id is not revealed (D-07).
func (h handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := h.s.Queries.GetTransaction(ctx, r.PathValue("transactionId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	if p, _ := auth.FromContext(ctx); !auth.CanSeeTransaction(p, tx.ProviderID()) {
		writeProblem(w, r, codeTransactionNotFound, "")
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx))
}

// getTransactionByExternalID is GET
// /providers/{providerId}/wagering/transactions/{externalTransactionId}: a
// provider may only name itself in the path (403 before any read); the
// internal service may name any provider.
func (h handlers) getTransactionByExternalID(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerID := r.PathValue("providerId")
	if p, _ := auth.FromContext(ctx); !auth.HasRole(p, auth.RoleWalletInternal) && !auth.ActsAs(p, providerID) {
		writeProblem(w, r, codeProviderMismatch, "")
		return
	}
	tx, err := h.s.Queries.GetTransactionByExternalID(ctx, providerID, r.PathValue("externalTransactionId"))
	if err != nil {
		writeError(w, r, h.log, err)
		return
	}
	writeJSON(w, http.StatusOK, transactionResponse(tx))
}
```

Apagar `internal/adapters/httpapi/placeholder_handlers.go`:

```bash
rm internal/adapters/httpapi/placeholder_handlers.go
```

- [ ] **Passo 4: rodar e ver passar**

```bash
go vet ./... && go test -race -count=1 ./internal/adapters/httpapi/ && go test -tags=integration -race -count=1 ./test/integration/
```

Esperado: `ok`.

**Checkpoint:** os comandos do Passo 4 verdes, com as 9 rotas implementadas.

---

### Tarefa 13: provas de borda e de concorrência: A01, A02c, A04, C01a e C02

Estes testes cobrem comportamento que já existe:
- a autenticação e as roles, feitas por TDD nas Tarefas 7 e 8;
- o lock da carteira (M2);
- a idempotência (Tarefas 4 e 5).

Por isso cada um vale só depois da **checagem de sensibilidade** (development-workflow §4.3), registrada num comentário `// Sensitivity:`. O A01b espera o vencimento real de um token de 5 s: `exp` + tolerância de 1 s + 0,5 s. O C02 repete a disputa 20 vezes com carteiras novas.

**Arquivos:**
- Testes: `test/integration/auth_test.go` (alterar), `test/integration/concurrency_test.go` (criar)

**Interfaces:**
- Consome: o harness (Tarefa 10) e `httpapi.Routes` (Tarefa 8).
- Produz: `expiredToken`, `tokenExpiry` e `race` (helpers de teste).

- [ ] **Passo 1: escrever os testes**

Em `test/integration/auth_test.go`, trocar:

```go
import (
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)
```

por:

```go
import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
	"github.com/KaioVinicios/pda/test/testkit"
)
```

Acrescentar ao fim de `test/integration/auth_test.go`:

```go
// Covers: AUTH-01, AUTH-03, TST-A01, E1 (A01a)
func TestAuthRealIdP(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	wantResult(t, result(t, server.Client(t, "provider-a"), wager(w, "provider-a", "BET", "10.00", unique("bet"), ""), http.StatusOK),
		"PROCESSED", "", "90.00", false)
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("90.00") {
		t.Fatalf("wallet = %+v", got)
	}
}

// expiredToken is a real token of provider-short-lived (5 s of life), once it
// is past its exp plus the clock skew of the application under test (1 s).
func expiredToken(t *testing.T) string {
	t.Helper()
	raw := testkit.FreshToken(t, "provider-short-lived")
	time.Sleep(time.Until(tokenExpiry(t, raw).Add(time.Second + 500*time.Millisecond)))
	return raw
}

func tokenExpiry(t *testing.T, raw string) time.Time {
	t.Helper()
	parts := strings.Split(raw, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	return time.Unix(claims.Exp, 0)
}

// Covers: AUTH-02, TST-A01, E1 (A01b)
//
// Sensitivity: SkipClientIDCheck in the verifier → "another audience" is
// accepted (403 FORBIDDEN, the client has no role) instead of 401.
func TestAuthRejects(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	path := "/wallets/" + w.ID
	cases := map[string]string{
		"malformed":        "not-a-jwt",
		"forged signature": testkit.ForgedToken(t),
		"alg none":         testkit.UnsignedToken(t),
		"HS256":            testkit.HS256Token(t),
		"another audience": testkit.Token(t, "no-audience-client"),
		"another realm":    testkit.OtherRealmToken(t),
	}
	check := func(t *testing.T, c *testkit.Client, challenge string) {
		t.Helper()
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: path})
		if p := resp.Problem(t); resp.Status != http.StatusUnauthorized || p.Code != "UNAUTHENTICATED" ||
			resp.Header.Get("WWW-Authenticate") != challenge {
			t.Fatalf("%d %+v, WWW-Authenticate %q", resp.Status, p, resp.Header.Get("WWW-Authenticate"))
		}
	}
	t.Run("no token", func(t *testing.T) {
		t.Parallel()
		check(t, server.Client(t, ""), `Bearer realm="pda"`)
	})
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			check(t, server.ClientWithToken(raw), `Bearer realm="pda", error="invalid_token"`)
		})
	}
	t.Run("expired", func(t *testing.T) {
		t.Parallel()
		check(t, server.ClientWithToken(expiredToken(t)), `Bearer realm="pda", error="invalid_token"`)
	})
}

// Covers: AUTH-06, TST-A02, E2 (A02c)
//
// Sensitivity: the roles of POST /wallets removed from the route table →
// provider-a opens a wallet (201) instead of 403.
func TestInternalOperationsRestricted(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	walletRoutes := []testkit.Request{
		{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{"playerId": testkit.NewID(), "initialBalance": testkit.BRL("1.00")}},
		{Method: http.MethodGet, Path: "/wallets/" + w.ID},
		{Method: http.MethodGet, Path: "/wallets/" + w.ID + "/ledger"},
		{Method: http.MethodPost, Path: "/wallets/" + w.ID + "/reconciliation"},
	}
	for _, client := range []string{"provider-a", "provider-b", "no-role-client"} {
		for _, req := range walletRoutes {
			resp := server.Client(t, client).Do(t, req)
			if p := resp.Problem(t); resp.Status != http.StatusForbidden || p.Code != "FORBIDDEN" {
				t.Fatalf("%s %s %s = %d %+v", client, req.Method, req.Path, resp.Status, p)
			}
		}
	}
	resp := submit(t, server.Client(t, "wallet-service"), wager(w, "provider-a", "BET", "1.00", unique("bet"), ""))
	if p := resp.Problem(t); resp.Status != http.StatusForbidden || p.Code != "FORBIDDEN" {
		t.Fatalf("wallet-service submits an operation: %d %+v", resp.Status, p)
	}
}

// Covers: AUTH-08, D-20 (A04)
//
// Sensitivity: GET /wallets/{walletId} registered without roles → it answers
// 404 without a token instead of 401.
func TestPublicEndpoints(t *testing.T) {
	t.Parallel()
	anonymous := server.Client(t, "")
	public := map[string]bool{"GET /health/live": true, "GET /health/ready": true, "GET /docs": true, "GET /openapi.yaml": true}
	id := strings.NewReplacer("{walletId}", testkit.NewID(), "{transactionId}", testkit.NewID(),
		"{providerId}", "provider-a", "{externalTransactionId}", "ext-1")
	for _, route := range httpapi.Routes(true) {
		method, path, _ := strings.Cut(route, " ")
		resp := anonymous.Do(t, testkit.Request{Method: method, Path: id.Replace(path), Invalid: true})
		switch {
		case public[route] && resp.Status != http.StatusOK:
			t.Errorf("%s without a token = %d, want 200", route, resp.Status)
		case !public[route] && resp.Status != http.StatusUnauthorized:
			t.Errorf("%s without a token = %d, want 401", route, resp.Status)
		}
	}
}
```

Criar `test/integration/concurrency_test.go`:

```go
//go:build integration

package integration_test

import (
	"net/http"
	"sync"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// race sends every body at once, behind a start barrier, and returns the
// results in order.
func race(t *testing.T, c *testkit.Client, bodies []testkit.Wager) []*testkit.Response {
	t.Helper()
	out := make([]*testkit.Response, len(bodies))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Go(func() {
			<-start
			out[i] = submit(t, c, bodies[i])
		})
	}
	close(start)
	wg.Wait()
	return out
}

// Covers: TST-C01, IDEM-05, E5 (C01a, in process; with 3 processes in M8)
//
// Sensitivity: the idempotency lookup of ProcessWager returning nothing →
// the duplicates exhaust the race retries and answer 503.
func TestSameBet50xHTTP(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	bodies := make([]testkit.Wager, 50)
	for i := range bodies {
		bodies[i] = bet
	}

	ids, replays := map[string]int{}, 0
	for _, resp := range race(t, a, bodies) {
		var r testkit.TransactionResult
		resp.JSON(t, &r)
		if resp.Status != http.StatusOK || r.Status != "PROCESSED" || r.Balance == nil || r.Balance.Amount != "975.00" {
			t.Fatalf("answer %d %+v", resp.Status, r)
		}
		ids[r.TransactionID]++
		if r.IdempotentReplay {
			replays++
		}
	}
	if len(ids) != 1 || replays != 49 {
		t.Fatalf("%d transaction ids, %d replays; want 1 and 49", len(ids), replays)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("975.00") || got.Version != 2 {
		t.Fatalf("wallet = %+v, want one debit", got)
	}
}

// Covers: TST-C02, CONC-05, E4 (C02, in process; with 3 processes in M8)
//
// Sensitivity: the FOR UPDATE removed from the wallet lock → both BETs read
// 100.00 and the second one fails (stale version) instead of being rejected.
func TestTwoBetsCompete(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	for range 20 {
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bets := []testkit.Wager{
			wager(w, "provider-a", "BET", "80.00", unique("bet-a"), ""),
			wager(w, "provider-a", "BET", "80.00", unique("bet-b"), ""),
		}
		answers := race(t, a, bets)
		outcome := map[string]testkit.TransactionResult{}
		for _, resp := range answers {
			var r testkit.TransactionResult
			resp.JSON(t, &r)
			outcome[r.Status] = r
		}
		processed, rejected := outcome["PROCESSED"], outcome["REJECTED"]
		if len(outcome) != 2 || processed.Balance == nil || processed.Balance.Amount != "20.00" ||
			rejected.FailureCode != "INSUFFICIENT_FUNDS" || rejected.Balance == nil || rejected.Balance.Amount != "20.00" {
			t.Fatalf("outcomes = %+v", outcome)
		}
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("20.00") || got.Version != 2 {
			t.Fatalf("wallet = %+v, want 20.00 after one debit", got)
		}
		for i, body := range bets {
			var again testkit.TransactionResult
			submit(t, a, body).JSON(t, &again)
			var first testkit.TransactionResult
			answers[i].JSON(t, &first)
			if again.TransactionID != first.TransactionID || again.Status != first.Status || !again.IdempotentReplay {
				t.Fatalf("resend of %s = %+v, first %+v", body.ExternalTransactionID, again, first)
			}
		}
	}
}
```

- [ ] **Passo 2: rodar e ver passar**

```bash
go vet -tags=integration ./test/integration/ && go test -tags=integration -race -count=1 -run 'TestAuthRealIdP|TestAuthRejects|TestInternalOperationsRestricted|TestPublicEndpoints|TestSameBet50xHTTP|TestTwoBetsCompete' ./test/integration/ && go test -tags=integration -race -count=5 -run 'TestSameBet50xHTTP|TestTwoBetsCompete' ./test/integration/
```

Esperado: `ok` duas vezes. O A01b leva cerca de 7 s, pela espera do vencimento.

- [ ] **Passo 3: checagem de sensibilidade (uma de cada vez, desfazendo depois)**

Cada linha abaixo é uma sabotagem aplicada, o teste que ela deve derrubar e a mensagem esperada. Rodar com `go test -tags=integration -count=1 -run '<teste>' <pacote>`:

| Sabotagem | Teste | Falha esperada |
| --- | --- | --- |
| `internal/adapters/postgres/wallet_repo.go`: remover `FOR UPDATE` do `Lock` | `TestTwoBetsCompete` (`./test/integration/`) | `outcomes = map[FAILED:… PROCESSED:…]`: a segunda BET falha por versão desatualizada |
| `internal/app/process_wager.go`, no `lookup`: `if err != nil \|\| true { return nil, err }` logo após o `FindByIdempotencyKey` | `TestSameBet50xHTTP` | `status 503 … TEMPORARILY_UNAVAILABLE`: as duplicatas esgotam as retentativas |
| `internal/auth/verifier.go`: `SkipClientIDCheck: true` no `oidc.Config` | `TestAuthRejects` | `another audience`: `403 … FORBIDDEN` em vez de 401 |
| `internal/adapters/httpapi/routes.go`: `POST /wallets` com `anyCaller` | `TestInternalOperationsRestricted` | `status 201` para `provider-a` |
| `internal/adapters/httpapi/routes.go`: `GET /wallets/{walletId}` com roles `nil` | `TestPublicEndpoints` | `GET /wallets/{walletId} without a token = 404, want 401` |

Depois de desfazer cada sabotagem, o teste volta ao verde (`git diff` sem sobras).

**Checkpoint:** as cinco sabotagens detectadas, e o Passo 2 verde de novo.

---

### Tarefa 14: verificação e encerramento do marco

Segue `superpowers:verification-before-completion` e a definição de pronto de [`development-workflow.md`](../../development-workflow.md) §5. **Nenhuma afirmação de "pronto" sem a saída dos comandos na mesma mensagem.**

**Arquivos:** `ARCHITECTURE.md`, `docs/delivery-requirements.md`, `docs/implementation-plan.md`, `docs/test-plan.md`, `docs/structure.md`, `docs/decisions.md`, `docs/dev/specs/2026-09-29-m3-contract-http-auth-design.md` (status), `docs/dev/diary.md`.

- [ ] **Passo 1: portão de qualidade**

```bash
make check
```

Esperado: `0 issues.` no lint; `gofmt`/`gofumpt` e `go mod tidy -diff` limpos; `go vet` com e sem tags sem avisos; `go test -race ./...` verde.

- [ ] **Passo 2: integração completa**

```bash
make test-integration
```

Esperado: `ok` em todos os pacotes com a tag, inclusive `internal/app`, `internal/bootstrap` e `test/integration`.

- [ ] **Passo 3: estabilidade**

```bash
go test -tags=integration -race -count=3 ./internal/app/ ./internal/adapters/postgres/ ./test/integration/
```

Esperado: `ok`.

- [ ] **Passo 4: ambiente completo e o fluxo do "pronto quando"**

```bash
docker compose up --build --wait
docker compose ps
```

Depois, o fluxo do [`api/requests.http`](../../../api/requests.http) por `curl` contra `app-1` (`localhost:8081`), com `scripts/get-token.sh wallet-service` e `scripts/get-token.sh provider-a`.

Esperado:
- `app-1..3` `healthy`, e elas só sobem depois do Keycloak;
- abertura 201;
- BET de 80.00: 200 `PROCESSED` com saldo 20.00;
- replay: 200 com `idempotentReplay: true` e saldo 20.00;
- segunda BET: 422 `INSUFFICIENT_FUNDS`;
- reconciliação: `consistent: true`, `checkedEntries: 2`;
- 401 sem token;
- `/docs` e `/openapi.yaml`: 200.

- [ ] **Passo 5: documentação**

- **`ARCHITECTURE.md`:** autenticação e autorização (AUTH-10: por que Keycloak, validação, tolerância de relógio, matriz); contrato HTTP e códigos (D-04 e D-20); casos de uso e o `FAILED` em UoW separada; §17 atualizado.
- **`delivery-requirements.md`:** marcar com `*(M3, 29/09: …)*`, citando os testes, os requisitos da spec §10:
  - **completos:** AUTH-01..08, AUTH-10, HTTP-01..07, HTTP-09, IDEM-02, IDEM-08, CONC-01..03, CONC-05, DOC-06, TST-A01..A03, TST-I03;
  - **parciais:** DOM-06, OBS-02, IDEM-01, IDEM-04, FX-01, TST-C01, TST-C02.
- **`implementation-plan.md`:** M3 ✅ com os links da spec e do plano; notas para o M4 (a outbox já recebe os envelopes selados pelo `app`), o M5 (o `ProcessRequest` ganha o registro da inbox, e o `ProcessWager` já é o caso de uso do consumidor) e o M6 (o worker chama `settleAndPersist` com `insert = false`); E1, E2, E5 e E6 na tabela §6.
- **`test-plan.md`:** os testes além da lista da spec (`TestQueries`, `TestListLedger`, `TestReconcile`, `TestHarness`, `TestContract`, `TestModule`, `TestVerifierCheckKeys`, `TestNewServerTimeouts`, `TestMetrics_ReconciliationDivergences`, `TestOutboxProblemsDetectsDivergence`, `TestOpenWalletAPI`, `TestLedgerAPI`, `TestHappyPathFlow`) e a corrida entre as leituras no I22.
- **`structure.md`:** `app/system.go`, `app/seal.go`, `httpapi/handler.go`, `httpapi/dto.go` e os arquivos do `testkit`.
- **`decisions.md`:** a regra do `lookup` (decisão 23) em D-08 e a ordem dos middlewares (decisão 24) em D-18.
- **`transaction-lifecycle.md`:** §3.3 com a regra da mesma chave entre as duas buscas (decisão 23).
- **Spec:** status "aprovada e implementada".
- **`docs/dev/diary.md`:** entrada do M3 e "onde paramos".

- [ ] **Passo 6: proposta de commits (o autor decide; §6 do workflow)**

1. `feat(config): add oidc, api docs and reference retry settings`
2. `feat(app): add clock, id and metrics ports and domain error translation`
3. `feat(app): add wallet opening and wager processing use cases`
4. `feat(app): add queries and reconciliation`
5. `feat(auth): verify oidc tokens and apply the permission matrix`
6. `feat(api): add the openapi contract, swagger ui and request collection`
7. `feat(httpapi): add routes, handlers, problem responses and middlewares`
8. `feat(bootstrap): compose auth, use cases and the api`
9. `test(testkit): run the app in process with contract and consistency checks`
10. `test(integration): cover auth, wallets, wagers, errors and concurrency over http`
11. `build: wait for keycloak and allow app tests to use the postgres adapter`
12. `docs: record m3 contract, http and auth decisions`
13. `docs(dev): add m3 spec, plan and diary entry`

**Checkpoint:** evidência dos Passos 1–4 no chat e a proposta de commits aprovada pelo autor.
