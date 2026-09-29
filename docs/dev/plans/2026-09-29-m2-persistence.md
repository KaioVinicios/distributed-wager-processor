# M2 — Persistência: plano de implementação

> **Execução:** `superpowers:executing-plans`, **inline** na própria sessão, sem subagentes ([`development-workflow.md`](../../development-workflow.md) §7). Cada tarefa segue `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Sem commits:** cada tarefa termina num *checkpoint* verificável, e os commits são propostos no fim do marco (§6 do workflow).

**Objetivo:** o schema completo com todas as invariantes do banco (migrations 000001–000006), o adapter `postgres` que o M3 e o M5 consomem (UoW, repositórios, mapeamento de `Money`, tradução de erros) e a prova, com PostgreSQL real, de que o domínio do M1 e o schema concordam.

**Arquitetura:**
- As portas de persistência nascem em `internal/app/ports.go` e crescem tarefa a tarefa, junto com cada repositório; as sentinelas ficam em `internal/app/errors.go` (spec §2, decisões 1 e 3).
- Os repositórios recebem e devolvem tipos do domínio e servem dentro e fora da transação por uma interface interna `querier` (`pgx.Tx` ou `*pgxpool.Pool`).
- O `*pgconn.PgError` nunca sai do adapter: `translate` devolve só SQLSTATE, constraint, sentinela e `Kind` (decisão 10).
- Ordem: config → tradução de erros → schema (tabelas, triggers, grants) → UoW e carteiras → outbox e inbox → escrita de transações e lançamentos → consultas → fluxos completos → Fx e compose.

**Stack:** Go 1.27.1, `pgx/v5` v5.11.0 com SQL explícito, PostgreSQL 18.6 do compose, `golang-migrate` v4.20.1 (driver `pgx/v5` + fonte `iofs`, só no `test/testkit`) e a imagem `migrate/migrate:v4.20.1` no serviço `migrate`.

**Spec:** [`docs/dev/specs/2026-09-29-m2-persistence-design.md`](../specs/2026-09-29-m2-persistence-design.md). Quem executa lê os dois documentos.

**Validação prévia do plano:** o código abaixo foi escrito e testado numa cópia descartável do repositório, contra o PostgreSQL do compose:
- para cada tarefa, o estado "tarefas anteriores + stubs + testes" compila, passa no `gofmt`, no `go vet` (com e sem tags) e **falha por asserção**, sem `panic`; com a implementação, fica verde;
- o estado final passa no `golangci-lint` v2.14.0 (`0 issues.`), no `golangci-lint fmt --diff`, no `go mod tidy -diff` e em três execuções seguidas da suíte de integração do pacote `postgres`; os estados intermediários das Tarefas 2, 6, 7, 8 e 9 também passam no lint;
- o serviço `migrate` foi exercitado com a imagem real (`up`, `down 1`, `down -all`, `up`, `version`) num banco descartável;
- as três sabotagens da Tarefa 10 foram detectadas;
- o estado montado pelas 11 tarefas é idêntico, arquivo por arquivo, ao código validado.

Na execução, o código é redigitado seguindo o ciclo red → green de cada tarefa.

## Restrições globais

- Module path `github.com/KaioVinicios/pda`; `go 1.27.1`. Nenhuma dependência nova além do `golang-migrate` v4.20.1 (Tarefa 3).
- **`pgx/v5` com SQL explícito**; sem ORM nem geração de código. Locks, isolamento e constraints visíveis no código.
- **Camadas:** `internal/app` só importa domínio, `apperrors` e stdlib (sem `pgx`, `fx`, `net/http`); o adapter implementa as portas do `app` (`depguard`).
- **Erros:** o `*pgconn.PgError` nunca sai do adapter; mensagens nunca carregam valores do banco. Valor de domínio que não pode ser gravado (zero value, `PENDING`) e linha que o domínio recusa são `KindPermanent`; corridas de unicidade que o `app` trata são sentinelas; o resto segue a §4.4 da spec.
- **Tempo:** instantes em UTC com microssegundos; `updated_at` nunca antes de `created_at` (`GREATEST`).
- **Testes de integração:** tag `integration`, PostgreSQL do compose no ar (`docker compose up -d --wait postgres`; a partir da Tarefa 11, `make infra-up`), banco isolado por pacote via `testkit`, `t.Parallel()` com carteiras e provedor próprios, `-race` e `// Covers: <IDs>` em todo teste; só `testing` da stdlib.
- **`make lint` e `make fmt`** usam a imagem `golangci/golangci-lint:v2.14.0`: o Docker precisa estar rodando.
- **Sem commits, branches ou worktrees.**

## Foco de revisão

Os cinco casos que a spec implica, mas não detalha, com mais chance de causar problema. Cada um tem teste na tarefa indicada:

1. **Rejeição `CURRENCY_MISMATCH`** (operação em USD numa carteira em BRL). Esperado: o saldo observado volta do banco em BRL, a moeda da carteira, e o replay devolve o saldo certo. → Tarefa 8 (`TestTransactionRepository`, caso `bet-3`).
2. **Relógio de outra instância à frente** do relógio de quem antecipa as pendências. Esperado: `AdvanceDependents` grava `updated_at = created_at` e a operação continua reidratável. → Tarefa 9 (`TestTransactionQueries`, "dependents are advanced with the creation floor").
3. **ID malformado** (path param que não é UUID canônico) em `Get`/`Lock`. Esperado: `ErrNotFound`, nunca um `22P02` classificado como permanente (500). → Tarefas 6 e 8 (casos "not found").
4. **`ctx` cancelado ou prazo vencido no meio da UoW**, inclusive esperando o lock de uma carteira travada. Esperado: rollback, erro com o do `ctx` na cadeia, classificado como transitório, e a espera interrompida antes do `lock_timeout`. → Tarefa 6 (`TestContextCancellation`).
5. **`Detail` do PostgreSQL com a linha inteira** (`Failing row contains (…)`). Esperado: nunca sai do adapter, nem na mensagem nem na cadeia de erros. → Tarefa 2 (`TestPostgresErrorMapping`).

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `internal/config/{config,validate}.go` + `config_test.go` | `DB_LOCK_TIMEOUT` | 1 |
| `internal/app/errors.go`; `internal/adapters/postgres/errors.go` + `errors_test.go` | Sentinelas e tradução de erros (U09b) | 2, 6, 8 |
| `migrations/embed.go`, `migrations/00000{1..4}_*.sql` | Tabelas, constraints e índices | 3 |
| `test/testkit/{root,dotenv,postgres}.go` | Banco isolado, `LoadDotEnv`, `LedgerProblems` | 3, 8 |
| `internal/adapters/postgres/{rows,migrations,constraints}_integration_test.go` | Linhas cruas, I01, I02a | 3 |
| `migrations/000005_protection_triggers.*.sql`; `test/testkit/env.go`; `{main,protection}_integration_test.go` | Triggers, `NewEnv`, I02b–e | 4 |
| `migrations/000006_grant_app_role.*.sql`; `grants_integration_test.go` | Grants, I02b (app) | 5 |
| `internal/app/ports.go` | Portas de persistência | 6, 7, 8, 9 |
| `internal/adapters/postgres/{querier,repos,uow,money_mapping,wallet_repo}.go`; `{domain,wallet_repo,uow}_integration_test.go` | UoW e carteiras (I19, I16) | 6 |
| `internal/adapters/postgres/{outbox_repo,inbox_repo}.go`; `outbox_inbox_integration_test.go` | Outbox e inbox | 7 |
| `internal/adapters/postgres/{transaction_repo,ledger_repo}.go`; `transaction_repo_integration_test.go` | Escrita de transações e lançamentos | 8 |
| `queries_integration_test.go` | Consultas | 9 |
| `flows_integration_test.go` | I17, I03a, I03b | 10 |
| `internal/adapters/postgres/module.go`; `internal/bootstrap/bootstrap_test.go`; `docker-compose.yml`, `Makefile`, `.env.example` | Fx e serviço `migrate` | 11 |
| `docs/*`, `ARCHITECTURE.md`, `docs/dev/diary.md` | Encerramento do marco | 12 |

---

### Tarefa 1: `config`: `DB_LOCK_TIMEOUT`

O `lock_timeout` de toda transação do UoW vem da configuração (spec §2, decisão 4; D-09). Padrão 5 s; zero ou negativo é recusado sem ecoar o valor.

**Arquivos:**
- Implementação: `internal/config/config.go` (alterar), `internal/config/validate.go` (alterar)
- Testes e helpers de teste: `internal/config/config_test.go` (alterar)

**Interfaces:**
- Consome: `config.Config`, `Validate`, `FieldError` (M0).
- Produz: `config.Config.DBLockTimeout time.Duration` (`DB_LOCK_TIMEOUT`, padrão `5s`, `> 0`).

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/config/config.go` (substitui o arquivo inteiro):

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
	DBLockTimeout   time.Duration `env:"DB_LOCK_TIMEOUT"`
	WagerQueueName  string        `env:"SQS_WAGER_QUEUE_NAME" envDefault:"wager-transactions.fifo"`
	WagerDLQName    string        `env:"SQS_WAGER_DLQ_NAME" envDefault:"wager-transactions-dlq.fifo"`
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

`internal/config/config_test.go` (substitui o arquivo inteiro):

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
}

const validURL = "postgres://pda_app:s3cr3t@localhost:5432/pda?sslmode=disable"

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
	t.Setenv("DATABASE_URL", validURL)

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
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

// Covers: FX-02
func TestLoad_RequiresDatabaseURL(t *testing.T) {
	cleanEnv(t)

	_, err := config.Load()
	if got := vars(err); len(got) != 1 || got[0] != "DATABASE_URL" {
		t.Fatalf("Load() error vars = %v, want [DATABASE_URL] (err = %v)", got, err)
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

// Covers: OBS-02
func TestLoad_ErrorsNeverContainValues(t *testing.T) {
	cases := []struct {
		name, key, value, secret, wantVar string
	}{
		{"password in invalid url", "DATABASE_URL", "mysql://pda_app:SuperSecret42@h/db", "SuperSecret42", "DATABASE_URL"},
		{"unparsable duration", "SHUTDOWN_TIMEOUT", "abc123xyz", "abc123xyz", "SHUTDOWN_TIMEOUT"},
		{"unparsable int", "DB_MAX_CONNS", "ten-conns-99", "ten-conns-99", "DB_MAX_CONNS"},
		{"unparsable lock timeout", "DB_LOCK_TIMEOUT", "forever-77", "forever-77", "DB_LOCK_TIMEOUT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanEnv(t)
			t.Setenv("DATABASE_URL", validURL)
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

Esperado: FAIL. Falham `TestLoad_AppliesDefaults` (o stub não tem padrão: `DBLockTimeout:0s`) e os dois casos novos de `TestValidate_RejectsInvalidValues` (`Validate() vars = [], want [DB_LOCK_TIMEOUT]`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/config/config.go` (substitui o stub do Passo 1):

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

`internal/config/validate.go` (substitui o arquivo inteiro):

```go
package config

import (
	"errors"
	"net/url"
	"strings"
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
	return errors.Join(errs...)
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

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/config/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -race -count=1 ./internal/config/` verde.

---

### Tarefa 2: `postgres`: tradução de erros (U09b) e sentinelas do `app`

O adapter é o único lugar que conhece SQLSTATEs (spec §4.4, decisões 3 e 10). O `*pgconn.PgError` nunca sai dele: o `Detail` traz a linha inteira.

**Arquivos:**
- Implementação: `internal/adapters/postgres/errors.go`, `internal/app/errors.go`
- Testes e helpers de teste: `internal/adapters/postgres/errors_test.go`

**Interfaces:**
- Consome: `apperrors.New`, `Classify`, `CodeOf` (M1).
- Produz: `app.ErrNotFound`, `app.ErrWalletAlreadyExists`, `app.ErrIdempotencyRace`, `app.ErrReversalRace`, `app.ErrInboxDuplicate`; `postgres.translate(err error) error` (não exportado) e `dbError`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`internal/app/errors.go` (novo):

```go
package app

import "errors"

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
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/adapters/postgres/errors.go` (novo):

```go
package postgres

import "errors"

var errNotImplemented = errors.New("postgres: not implemented")

func translate(err error) error { return errNotImplemented }
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/errors_test.go` (novo):

```go
package postgres

// Internal test: translate is unexported on purpose (the adapter is the only
// place that knows SQLSTATEs), so U09b tests it from inside the package.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// Covers: TX-10, DB-02, OBS-02 (U09b)
func TestPostgresErrorMapping(t *testing.T) {
	const row = "Failing row contains (0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1, BRL, 99999)"
	cases := []struct {
		name       string
		code       string
		constraint string
		wantKind   apperrors.Kind
		wantCode   string
		wantErr    error
	}{
		{"wallet already exists", "23505", "wallets_player_currency_uq", apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
		{"idempotency key race", "23505", "wager_tx_idempotency_uq", apperrors.KindTransient, "", app.ErrIdempotencyRace},
		{"external id race", "23505", "wager_tx_external_id_uq", apperrors.KindTransient, "", app.ErrIdempotencyRace},
		{"reversal race", "23505", "wager_tx_single_reversal_uq", apperrors.KindTransient, "", app.ErrReversalRace},
		{"inbox duplicate", "23505", "inbox_pk", apperrors.KindTransient, "", app.ErrInboxDuplicate},
		{"other unique", "23505", "wager_tx_single_opening_uq", apperrors.KindPermanent, "", nil},
		{"not null", "23502", "", apperrors.KindPermanent, "", nil},
		{"foreign key", "23503", "ledger_tx_wallet_fk", apperrors.KindPermanent, "", nil},
		{"check", "23514", "wallets_balance_minor_check", apperrors.KindPermanent, "", nil},
		{"numeric overflow", "22003", "", apperrors.KindPermanent, "", nil},
		{"read only transaction", "25006", "", apperrors.KindPermanent, "", nil},
		{"insufficient privilege", "42501", "", apperrors.KindPermanent, "", nil},
		{"ledger append-only", "PDA01", "", apperrors.KindPermanent, "", nil},
		{"terminal transaction", "PDA02", "", apperrors.KindPermanent, "", nil},
		{"wallet guard", "PDA03", "", apperrors.KindPermanent, "", nil},
		{"ledger coupling", "PDA04", "", apperrors.KindPermanent, "", nil},
		{"outbox snapshot", "PDA05", "", apperrors.KindPermanent, "", nil},
		{"connection failure", "08006", "", apperrors.KindTransient, "", nil},
		{"connection does not exist", "08003", "", apperrors.KindTransient, "", nil},
		{"serialization failure", "40001", "", apperrors.KindTransient, "", nil},
		{"deadlock", "40P01", "", apperrors.KindTransient, "", nil},
		{"lock timeout", "55P03", "", apperrors.KindTransient, "", nil},
		{"admin shutdown", "57P01", "", apperrors.KindTransient, "", nil},
		{"query canceled", "57014", "", apperrors.KindTransient, "", nil},
		{"too many connections", "53300", "", apperrors.KindTransient, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := fmt.Errorf("exec: %w", &pgconn.PgError{
				Code: tc.code, ConstraintName: tc.constraint,
				Message: "message with a value 99999", Detail: row, Hint: "hint", Where: "where",
			})
			got := translate(in)
			if k := apperrors.Classify(got); k != tc.wantKind {
				t.Fatalf("Classify(translate(%s)) = %q, want %q (err = %v)", tc.code, k, tc.wantKind, got)
			}
			if c := apperrors.CodeOf(got); c != tc.wantCode {
				t.Fatalf("CodeOf = %q, want %q", c, tc.wantCode)
			}
			if tc.wantErr != nil && !errors.Is(got, tc.wantErr) {
				t.Fatalf("errors.Is(%v, %v) = false", got, tc.wantErr)
			}
			var pgErr *pgconn.PgError
			if errors.As(got, &pgErr) {
				t.Fatalf("translated error still carries the *pgconn.PgError: %v", got)
			}
			msg := got.Error()
			if strings.Contains(msg, "99999") || strings.Contains(msg, "Failing row") || !strings.Contains(msg, tc.code) {
				t.Fatalf("message %q must name the SQLSTATE and carry no value from the database", msg)
			}
			if tc.constraint != "" && !strings.Contains(msg, tc.constraint) {
				t.Fatalf("message %q must name the constraint %s", msg, tc.constraint)
			}
		})
	}
}

// Covers: TX-10, DOM-06 (U09b)
func TestPostgresErrorMapping_PassesOtherErrorsUntouched(t *testing.T) {
	if translate(nil) != nil {
		t.Fatal("translate(nil) != nil")
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.New("dial tcp: connection refused")} {
		if got := translate(err); got != err { //nolint:errorlint // identity is the point: nothing is wrapped
			t.Fatalf("translate(%v) = %v, want the same error", err, got)
		}
		if k := apperrors.Classify(translate(err)); k != apperrors.KindTransient {
			t.Fatalf("Classify(%v) = %q, want TRANSIENT (D-05)", err, k)
		}
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestPostgresErrorMapping` (todos os 25 casos: `Classify(translate(23505)) = "TRANSIENT", want "CONFLICT"`…) e `TestPostgresErrorMapping_PassesOtherErrorsUntouched` (o stub devolve `postgres: not implemented`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/errors.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// dbError is what a PostgreSQL error becomes when it leaves the adapter: the
// SQLSTATE and the constraint name only. Message, Detail, Where and Hint are
// dropped on purpose: Detail carries the whole row ("Failing row contains
// (…)"), a full financial payload that would end up in the logs (CHALLENGE §12).
type dbError struct {
	code       string
	constraint string
}

func (e *dbError) Error() string {
	if e.constraint == "" {
		return "postgres: " + e.code
	}
	return "postgres: " + e.code + " " + e.constraint
}

// sentinel is how a unique violation the use cases handle is reported.
type sentinel struct {
	kind apperrors.Kind
	code string
	err  error
}

// uniqueSentinels maps the constraints whose violation the app handles (D-14).
var uniqueSentinels = map[string]sentinel{
	"wallets_player_currency_uq":  {apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
	"wager_tx_idempotency_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_external_id_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_single_reversal_uq": {apperrors.KindTransient, "", app.ErrReversalRace},
	"inbox_pk":                    {apperrors.KindTransient, "", app.ErrInboxDuplicate},
}

// transientCodes are the SQLSTATEs that a retry can overcome, besides class 08
// (connection exceptions).
var transientCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout, D-09)
	"57P01": true, // admin_shutdown
	"57014": true, // query_canceled
	"53300": true, // too_many_connections
}

// translate classifies a database error (U09b). A *pgconn.PgError never leaves
// the adapter: unique violations the app handles become its sentinels, known
// transient SQLSTATEs become KindTransient, and every other rejection by the
// database (constraints, triggers PDA01–PDA05, 22003, 25006…) is
// KindPermanent, since a retry cannot fix it. Errors that are not PgError
// (context, network) pass untouched: apperrors.Classify treats them as
// transient (D-05).
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	cause := &dbError{code: pgErr.Code, constraint: pgErr.ConstraintName}
	if s, ok := uniqueSentinels[pgErr.ConstraintName]; ok && pgErr.Code == "23505" {
		return apperrors.New(s.kind, s.code, fmt.Errorf("%w: %w", s.err, cause))
	}
	if transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08") {
		return apperrors.New(apperrors.KindTransient, "", cause)
	}
	return apperrors.New(apperrors.KindPermanent, "", cause)
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 3: Schema: tabelas (000001–000004), banco isolado, I01 e I02a

Transcreve o data-model §3 nas migrations, cria o banco isolado por teste (`testkit.NewDatabase`) e prova cada constraint (spec §5 e §7). O I02a roda num banco próprio com os triggers desligados, porque os do 000005 disparam antes dos `CHECK` e os esconderiam.

**Arquivos:**
- Implementação: `migrations/000001_create_wallets.down.sql`, `migrations/000001_create_wallets.up.sql`, `migrations/000002_create_wager_transactions.down.sql`, `migrations/000002_create_wager_transactions.up.sql`, `migrations/000003_create_wallet_ledger_entries.down.sql`, `migrations/000003_create_wallet_ledger_entries.up.sql`, `migrations/000004_create_inbox_outbox.down.sql`, `migrations/000004_create_inbox_outbox.up.sql`, `migrations/embed.go`
- Testes e helpers de teste: `internal/adapters/postgres/constraints_integration_test.go`, `internal/adapters/postgres/migrations_integration_test.go`, `internal/adapters/postgres/rows_integration_test.go`, `test/testkit/dotenv.go` (alterar), `test/testkit/postgres.go` (alterar), `test/testkit/root.go` (alterar)

**Interfaces:**
- Consome: `testkit.DotEnv`, `testkit.RepoRoot` (M0).
- Produz: `migrations.FS`; `testkit.LoadDotEnv() (map[string]string, error)`; `testkit.Database{Name, OwnerURL, AppURL}`, `testkit.NewDatabase(ctx, name) (*Database, func() error, error)`, `(*Database).Migrator() (*migrate.Migrate, error)`; nos testes do pacote: `row`, `insert`, `walletRow`, `openingRow`, `externalRow`, `pendingRow`, `ledgerRow`, `inboxRow`, `outboxRow`, `sqlState`, `wantSQLState`, `newID`, `ts`, `hash`, `isolatedDB`.

- [ ] **Passo 0: dependência do golang-migrate**

```bash
go get github.com/golang-migrate/migrate/v4@v4.20.1
```

Esperado: `go: added github.com/golang-migrate/migrate/v4 v4.20.1`. O `go mod tidy` do Passo 5 completa o `go.sum` com o driver `pgx/v5` e o `iofs`. **O `pgx` continua em `v5.11.0`**; qualquer outra mudança de versão é um ruling.

O PostgreSQL do compose precisa estar no ar para todos os testes de integração do marco: `docker compose up -d --wait postgres`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`migrations/embed.go` (novo):

```go
// Package migrations embeds the versioned SQL migrations (data-model.md §7) so
// that the tests apply exactly the files the migrate service applies.
package migrations

import "embed"

// FS holds the NNNNNN_name.{up,down}.sql files, read by golang-migrate's iofs source.
//
//go:embed *.sql
var FS embed.FS
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`migrations/000001_create_wallets.down.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000001_create_wallets.up.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000002_create_wager_transactions.down.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000002_create_wager_transactions.up.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000003_create_wallet_ledger_entries.down.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000003_create_wallet_ledger_entries.up.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000004_create_inbox_outbox.down.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000004_create_inbox_outbox.up.sql` (novo):

```sql
-- Stub of task 3: replaced by the real migration in step 4.
SELECT 1;
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/constraints_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// base are the valid rows every constraint case starts from.
type base struct {
	wallet, player, wallet2, opening, bet, entry string
}

func seedBase(ctx context.Context, tx pgx.Tx) (base, error) {
	b := base{wallet: newID(), player: newID(), wallet2: newID(), opening: newID(), bet: newID(), entry: newID()}
	steps := []struct {
		table string
		r     row
	}{
		{"wallets", walletRow(b.wallet, b.player, 10000)},
		{"wallets", walletRow(b.wallet2, newID(), 0)},
		{"wager_transactions", openingRow(b.opening, b.wallet, b.player, 10000)},
		{"wager_transactions", externalRow(b.bet, b.wallet, b.player)},
		{"wallet_ledger_entries", ledgerRow(b.entry, b.wallet, b.opening, "CREDIT", 10000, 0, 10000, 1)},
		{"inbox_messages", inboxRow("consumer", "msg-1")},
		{"outbox_events", outboxRow(b.entry, b.wallet)},
	}
	for _, s := range steps {
		if err := insert(ctx, tx, s.table, s.r); err != nil {
			return base{}, err
		}
	}
	return b, nil
}

type constraintCase struct {
	name, table string
	setup       func(b base) (string, row) // an extra valid row inserted first (optional)
	row         func(b base) row
	code        string
	constraint  string // "" when PostgreSQL does not name it
}

func constraintCases() []constraintCase {
	ext := func(b base) row { return externalRow(newID(), b.wallet, b.player) }
	entry := func(b base, dir string, amount, before, after, version int64) row {
		return ledgerRow(newID(), b.wallet, b.bet, dir, amount, before, after, version)
	}
	return []constraintCase{
		// wallets (data-model §3.1)
		{name: "wallet currency not ISO", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("currency", "brl") }, code: "23514", constraint: "wallets_currency_check"},
		{name: "negative balance (WAL-04, E4)", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), -1) }, code: "23514", constraint: "wallets_balance_minor_check"},
		{name: "version below 1 (WAL-07)", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("version", int64(0)) }, code: "23514", constraint: "wallets_version_check"},
		{name: "updated before created", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("updated_at", ts.Add(-1)) }, code: "23514", constraint: "wallets_updated_after_created"},
		{name: "second wallet for player and currency (WAL-03)", table: "wallets", row: func(b base) row { return walletRow(newID(), b.player, 0) }, code: "23505", constraint: "wallets_player_currency_uq"},
		{name: "wallet without player", table: "wallets", row: func(b base) row { return walletRow(newID(), newID(), 0).with("player_id", nil) }, code: "23502"},

		// wager_transactions (data-model §3.2)
		{name: "unknown origin", table: "wager_transactions", row: func(b base) row { return ext(b).with("origin", "OTHER") }, code: "23514", constraint: "wager_transactions_origin_check"},
		{name: "unknown kind", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "JACKPOT") }, code: "23514", constraint: "wager_transactions_kind_check"},
		{name: "PENDING is never persisted (TX-09)", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("status", "PENDING", "failure_code", nil, "result_balance_minor", nil, "completed_at", nil)
		}, code: "23514", constraint: "wager_transactions_status_check"},
		{name: "negative amount", table: "wager_transactions", row: func(b base) row { return ext(b).with("amount_minor", int64(-1)) }, code: "23514", constraint: "wager_transactions_amount_minor_check"},
		{name: "operation currency not ISO", table: "wager_transactions", row: func(b base) row { return ext(b).with("currency", "br") }, code: "23514", constraint: "wager_transactions_currency_check"},
		{name: "payload hash not lowercase hex", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("payload_hash", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789")
		}, code: "23514", constraint: "wager_transactions_payload_hash_check"},
		{name: "unknown channel", table: "wager_transactions", row: func(b base) row { return ext(b).with("received_via", "GRPC") }, code: "23514", constraint: "wager_transactions_received_via_check"},
		{name: "negative result balance", table: "wager_transactions", row: func(b base) row { return ext(b).with("result_balance_minor", int64(-1)) }, code: "23514", constraint: "wager_transactions_result_balance_minor_check"},
		{name: "negative attempts", table: "wager_transactions", row: func(b base) row { return ext(b).with("attempts", -1) }, code: "23514", constraint: "wager_transactions_attempts_check"},
		{name: "unknown wallet", table: "wager_transactions", row: func(b base) row { return ext(b).with("wallet_id", newID()) }, code: "23503", constraint: "wager_transactions_wallet_id_fkey"},
		{name: "unknown resolved reference", table: "wager_transactions", row: func(b base) row { return ext(b).with("reference_transaction_id", newID()) }, code: "23503", constraint: "wager_transactions_reference_transaction_id_fkey"},
		{name: "external OPENING (TX-05)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "OPENING") }, code: "23514", constraint: "wager_tx_origin_kind"},
		{name: "OPENING with provider metadata (TX-05)", table: "wager_transactions", row: func(b base) row {
			return openingRow(newID(), b.wallet2, b.player, 500).with("provider_id", "provider-a")
		}, code: "23514", constraint: "wager_tx_internal_fields"},
		{name: "external operation without round", table: "wager_transactions", row: func(b base) row { return ext(b).with("round_id", nil) }, code: "23514", constraint: "wager_tx_external_fields"},
		{name: "LOSS with an amount (OPS-03)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "LOSS") }, code: "23514", constraint: "wager_tx_amount_policy"},
		{name: "BET of zero (OPS-11)", table: "wager_transactions", row: func(b base) row { return ext(b).with("amount_minor", int64(0)) }, code: "23514", constraint: "wager_tx_amount_policy"},
		{name: "BET with a reference (OPS-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("reference_external_transaction_id", "ext-x") }, code: "23514", constraint: "wager_tx_reference_policy"},
		{name: "REFUND without a reference (OPS-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("kind", "REFUND") }, code: "23514", constraint: "wager_tx_reference_policy"},
		{name: "processed REFUND without resolved reference", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("kind", "REFUND", "status", "PROCESSED", "failure_code", nil, "reference_external_transaction_id", "ext-x")
		}, code: "23514", constraint: "wager_tx_resolved_reference"},
		{name: "processed WIN without resolved reference", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("kind", "WIN", "status", "PROCESSED", "failure_code", nil, "reference_external_transaction_id", "ext-x")
		}, code: "23514", constraint: "wager_tx_resolved_win_reference"},
		{name: "REJECTED without failure code (TX-06)", table: "wager_transactions", row: func(b base) row { return ext(b).with("failure_code", nil) }, code: "23514", constraint: "wager_tx_failure_code"},
		{name: "PROCESSED with failure code", table: "wager_transactions", row: func(b base) row { return ext(b).with("status", "PROCESSED") }, code: "23514", constraint: "wager_tx_failure_code"},
		{name: "PROCESSED without result balance (IDEM-08)", table: "wager_transactions", row: func(b base) row {
			return ext(b).with("status", "PROCESSED", "failure_code", nil, "result_balance_minor", nil)
		}, code: "23514", constraint: "wager_tx_result_balance"},
		{name: "terminal without completion instant", table: "wager_transactions", row: func(b base) row { return ext(b).with("completed_at", nil) }, code: "23514", constraint: "wager_tx_completed_at"},
		{name: "PENDING_REFERENCE without schedule (TX-09)", table: "wager_transactions", row: func(b base) row {
			return pendingRow(newID(), b.wallet, b.player).with("next_attempt_at", nil)
		}, code: "23514", constraint: "wager_tx_pending_schedule"},
		{name: "idempotency key reused (IDEM-02, E6)", table: "wager_transactions", row: func(b base) row { return ext(b).with("idempotency_key", "key-"+b.bet) }, code: "23505", constraint: "wager_tx_idempotency_uq"},
		{name: "external id reused (IDEM-07)", table: "wager_transactions", row: func(b base) row { return ext(b).with("external_transaction_id", "ext-"+b.bet) }, code: "23505", constraint: "wager_tx_external_id_uq"},
		{name: "second OPENING (TX-05)", table: "wager_transactions", row: func(b base) row { return openingRow(newID(), b.wallet, b.player, 500) }, code: "23505", constraint: "wager_tx_single_opening_uq"},
		{
			name: "second successful reversal (OPS-08, D-10)", table: "wager_transactions",
			setup: func(b base) (string, row) { return "wager_transactions", reversalRow(b) },
			row:   reversalRow, code: "23505", constraint: "wager_tx_single_reversal_uq",
		},

		// wallet_ledger_entries (data-model §3.3)
		{name: "unknown direction", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "UP", 10, 0, 10, 2) }, code: "23514"},
		{name: "zero amount (LED-06)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 0, 0, 0, 2) }, code: "23514", constraint: "wallet_ledger_entries_amount_minor_check"},
		{name: "entry currency not ISO", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 2).with("currency", "brl") }, code: "23514", constraint: "wallet_ledger_entries_currency_check"},
		{name: "negative balance before", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, -1, 9, 2) }, code: "23514", constraint: "wallet_ledger_entries_balance_before_minor_check"},
		{name: "negative balance after (LED-06)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "DEBIT", 1, 0, -1, 2) }, code: "23514", constraint: "wallet_ledger_entries_balance_after_minor_check"},
		{name: "version below 1", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 0) }, code: "23514", constraint: "wallet_ledger_entries_wallet_version_check"},
		{name: "after != before ± amount (LED-02)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 11, 2) }, code: "23514", constraint: "ledger_balance_math"},
		{name: "second entry for a transaction (LED-03, E5)", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), b.wallet, b.opening, "CREDIT", 10, 10000, 10010, 2)
		}, code: "23505", constraint: "ledger_wallet_tx_uq"},
		{name: "second entry for a wallet version (D-16)", table: "wallet_ledger_entries", row: func(b base) row { return entry(b, "CREDIT", 10, 0, 10, 1) }, code: "23505", constraint: "ledger_wallet_version_uq"},
		{name: "transaction of another wallet", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), b.wallet2, b.opening, "CREDIT", 10, 0, 10, 2)
		}, code: "23503", constraint: "ledger_tx_wallet_fk"},
		{name: "unknown wallet", table: "wallet_ledger_entries", row: func(b base) row {
			return ledgerRow(newID(), newID(), b.bet, "CREDIT", 10, 0, 10, 2)
		}, code: "23503"},

		// inbox_messages (data-model §3.4)
		{name: "message hash not lowercase hex", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("message_hash", "x") }, code: "23514", constraint: "inbox_messages_message_hash_check"},
		{name: "unknown outcome", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("outcome", "DONE") }, code: "23514", constraint: "inbox_messages_outcome_check"},
		{name: "message recorded twice (SQS-03)", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", "msg-1") }, code: "23505", constraint: "inbox_pk"},
		{name: "unknown transaction", table: "inbox_messages", row: func(b base) row { return inboxRow("consumer", newID()).with("transaction_id", newID()) }, code: "23503", constraint: "inbox_messages_transaction_id_fkey"},

		// outbox_events (data-model §3.5)
		{name: "unknown aggregate type", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("aggregate_type", "Player") }, code: "23514", constraint: "outbox_events_aggregate_type_check"},
		{name: "unknown event type", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("event_type", "WalletClosed") }, code: "23514", constraint: "outbox_events_event_type_check"},
		{name: "event version below 1", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("event_version", 0) }, code: "23514", constraint: "outbox_events_event_version_check"},
		{name: "negative publish attempts", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("attempts", -1) }, code: "23514", constraint: "outbox_events_attempts_check"},
		{name: "lease owner without expiry", table: "outbox_events", row: func(b base) row { return outboxRow(newID(), b.wallet).with("locked_by", "instance-1") }, code: "23514", constraint: "outbox_lock_pair"},
		{name: "event recorded twice", table: "outbox_events", row: func(b base) row { return outboxRow(b.entry, b.wallet) }, code: "23505", constraint: "outbox_events_pkey"},
	}
}

// reversalRow is a PROCESSED REFUND of the base BET.
func reversalRow(b base) row {
	return externalRow(newID(), b.wallet, b.player).with(
		"kind", "REFUND", "status", "PROCESSED", "failure_code", nil,
		"reference_external_transaction_id", "ext-"+b.bet, "reference_transaction_id", b.bet)
}

// Covers: TST-I02, DB-03, WAL-03, WAL-04, WAL-07, TX-05, TX-09, LED-02, LED-03, LED-06, IDEM-02, IDEM-07, OPS-03, OPS-06, OPS-08, OPS-11, SQS-03, E4, E5 (I02a)
//
// The triggers of 000005 fire BEFORE the CHECKs and would mask them, so this
// test runs on its own database with the user triggers disabled; I02b–I02e
// test the triggers on the full schema.
func TestConstraints(t *testing.T) {
	_, pool := isolatedDB(t, "constraints")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers on %s: %v", table, err)
		}
	}
	for _, tc := range constraintCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			b, err := seedBase(ctx, tx)
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			if tc.setup != nil {
				table, r := tc.setup(b)
				if err := insert(ctx, tx, table, r); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			wantSQLState(t, insert(ctx, tx, tc.table, tc.row(b)), tc.code, tc.constraint)
		})
	}
}
```

`internal/adapters/postgres/migrations_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/migrations"
	"github.com/KaioVinicios/pda/test/testkit"
)

// schemaSnapshot lists every object the migrations own: columns, constraints,
// indexes, triggers, functions and grants (golang-migrate's own table apart).
const schemaSnapshot = `
SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
  FROM information_schema.columns WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
UNION ALL
SELECT 'constraint ' || conrelid::regclass || '.' || conname || ' ' || pg_get_constraintdef(oid)
  FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND conrelid::regclass::text <> 'schema_migrations'
UNION ALL
SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
UNION ALL
SELECT 'trigger ' || pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal
UNION ALL
SELECT 'function ' || proname || ' ' || md5(prosrc) FROM pg_proc WHERE pronamespace = 'public'::regnamespace
UNION ALL
SELECT 'grant ' || table_name || ' ' || grantee || ' ' || privilege_type
  FROM information_schema.role_table_grants WHERE table_schema = 'public' AND grantee = 'pda_app'
ORDER BY 1`

func snapshot(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), schemaSnapshot)
	if err != nil {
		t.Fatalf("schema snapshot: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("schema snapshot: %v", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema snapshot: %v", err)
	}
	return out
}

// isolatedDB creates a database only this test uses.
func isolatedDB(t *testing.T, name string) (*testkit.Database, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	db, drop, err := testkit.NewDatabase(ctx, name)
	if err != nil {
		t.Fatalf("testkit.NewDatabase: %v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	pool, err := pgxpool.New(t.Context(), db.OwnerURL)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db, pool
}

// Covers: TST-I01, DB-04, ART-05 (I01)
func TestMigrationsUpDownUp(t *testing.T) {
	db, pool := isolatedDB(t, "migrations")

	ups, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	m, err := db.Migrator()
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	version, dirty, err := m.Version()
	if err != nil || dirty || int(version) != len(ups) {
		t.Fatalf("after up: version %d dirty %v err %v; want version %d (one per up file), clean", version, dirty, err, len(ups))
	}

	first := snapshot(t, pool)
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		if !slices.ContainsFunc(first, func(l string) bool { return strings.HasPrefix(l, "column "+table+".") }) {
			t.Fatalf("table %s missing after up", table)
		}
	}

	if err := m.Down(); err != nil {
		t.Fatalf("down -all: %v", err)
	}
	if left := snapshot(t, pool); len(left) != 0 {
		t.Fatalf("down -all left %d objects behind, e.g. %q", len(left), left[0])
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up again: %v", err)
	}
	if second := snapshot(t, pool); !slices.Equal(first, second) {
		t.Fatalf("schema differs after up → down → up:\nfirst:  %d objects\nsecond: %d objects", len(first), len(second))
	}
}
```

`internal/adapters/postgres/rows_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// newID returns a UUIDv7, as the app generates them.
func newID() string { return uuid.Must(uuid.NewV7()).String() }

// row is one table row by column; nil is NULL. The schema tests write rows with
// raw SQL to check the database alone, without the repositories.
type row map[string]any

// with returns a copy of r with the given column/value pairs replaced.
func (r row) with(kv ...any) row {
	out := maps.Clone(r)
	for i := 0; i < len(kv); i += 2 {
		column, ok := kv[i].(string)
		if !ok {
			panic("row.with: columns are strings")
		}
		out[column] = kv[i+1]
	}
	return out
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func insert(ctx context.Context, q execer, table string, r row) error {
	cols := slices.Sorted(maps.Keys(r))
	args := make([]any, len(cols))
	marks := make([]string, len(cols))
	for i, c := range cols {
		args[i], marks[i] = r[c], "$"+strconv.Itoa(i+1)
	}
	_, err := q.Exec(ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...)
	return err
}

// ts is a fixed instant with microsecond precision.
var ts = time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)

const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func walletRow(id, playerID string, balance int64) row {
	return row{
		"id": id, "player_id": playerID, "currency": "BRL", "balance_minor": balance,
		"version": int64(1), "created_at": ts, "updated_at": ts,
	}
}

// openingRow is the INTERNAL OPENING of a positive initial balance.
func openingRow(id, walletID, playerID string, amount int64) row {
	return row{
		"id": id, "origin": "INTERNAL", "kind": "OPENING", "status": "PROCESSED",
		"wallet_id": walletID, "player_id": playerID, "amount_minor": amount, "currency": "BRL",
		"result_balance_minor": amount, "correlation_id": "corr", "created_at": ts, "updated_at": ts, "completed_at": ts,
	}
}

// externalRow is a REJECTED BET: valid under every constraint and trigger
// without a ledger entry.
func externalRow(id, walletID, playerID string) row {
	return row{
		"id": id, "origin": "EXTERNAL", "kind": "BET", "status": "REJECTED",
		"wallet_id": walletID, "player_id": playerID, "amount_minor": int64(1000), "currency": "BRL",
		"provider_id": "provider-a", "external_transaction_id": "ext-" + id, "idempotency_key": "key-" + id,
		"payload_hash": hash, "round_id": "round-1", "game_id": "game-1", "received_via": "HTTP",
		"failure_code": "INSUFFICIENT_FUNDS", "result_balance_minor": int64(0), "attempts": 0,
		"correlation_id": "corr", "created_at": ts, "updated_at": ts, "completed_at": ts,
	}
}

// pendingRow is a REFUND waiting for its reference.
func pendingRow(id, walletID, playerID string) row {
	return externalRow(id, walletID, playerID).with(
		"kind", "REFUND", "status", "PENDING_REFERENCE", "failure_code", nil, "result_balance_minor", nil,
		"completed_at", nil, "reference_external_transaction_id", "ext-missing",
		"next_attempt_at", ts, "expires_at", ts.Add(time.Minute))
}

func ledgerRow(id, walletID, txID, direction string, amount, before, after, version int64) row {
	return row{
		"id": id, "wallet_id": walletID, "transaction_id": txID, "direction": direction,
		"amount_minor": amount, "currency": "BRL", "balance_before_minor": before,
		"balance_after_minor": after, "wallet_version": version, "created_at": ts,
	}
}

func inboxRow(consumer, messageID string) row {
	return row{
		"consumer_name": consumer, "message_id": messageID, "message_hash": hash,
		"message_type": "WagerTransactionRequested", "outcome": "REJECTED", "received_at": ts, "processed_at": ts,
	}
}

func outboxRow(eventID, walletID string) row {
	return row{
		"event_id": eventID, "aggregate_type": "Wallet", "aggregate_id": walletID,
		"message_group_id": walletID, "event_type": "WalletBalanceChanged", "event_version": 1,
		"payload": `{"eventId":"` + eventID + `"}`, "correlation_id": "corr", "occurred_at": ts, "next_attempt_at": ts,
	}
}

// sqlState returns the SQLSTATE and constraint of a database error.
func sqlState(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

// wantSQLState fails unless err is the given SQLSTATE (and constraint, when
// not empty).
func wantSQLState(t *testing.T, err error, code, constraint string) {
	t.Helper()
	gotCode, gotConstraint := sqlState(err)
	if gotCode != code || (constraint != "" && gotConstraint != constraint) {
		t.Fatalf("error = %v (SQLSTATE %q, constraint %q), want SQLSTATE %q, constraint %q",
			err, gotCode, gotConstraint, code, constraint)
	}
}
```

`test/testkit/dotenv.go` (substitui o arquivo inteiro):

```go
// Package testkit holds shared helpers for the integration and e2e tests.
package testkit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ParseDotEnv parses KEY=VALUE lines, ignoring blanks and # comments and
// stripping one level of surrounding quotes.
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("dotenv: line %d has no '='", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}

// DotEnv loads .env.example, overridden by .env when present.
func DotEnv(tb testing.TB) map[string]string {
	tb.Helper()
	vals, err := LoadDotEnv()
	if err != nil {
		tb.Fatal(err)
	}
	return vals
}

// LoadDotEnv is DotEnv for callers without a testing.TB, such as TestMain.
func LoadDotEnv() (map[string]string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	for i, name := range []string{".env.example", ".env"} {
		f, err := os.Open(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) && i > 0 {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("testkit: open %s: %w", name, err)
		}
		vals, err := ParseDotEnv(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("testkit: parse %s: %w", name, err)
		}
		for k, v := range vals {
			merged[k] = v
		}
	}
	return merged, nil
}
```

`test/testkit/postgres.go` (substitui o arquivo inteiro):

```go
package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/migrations"
)

// pgHost is the compose PostgreSQL seen from the host.
const pgHost = "localhost:5432"

func databaseURL(user, password, db string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     pgHost,
		Path:     "/" + db,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// AppDatabaseURL is the shared pda database as pda_app, seen from the host.
func AppDatabaseURL(tb testing.TB) string {
	tb.Helper()
	return databaseURL("pda_app", DotEnv(tb)["PDA_APP_PASSWORD"], "pda")
}

// Database is an isolated database with the embedded migrations applied
// (test-plan §3.2).
type Database struct {
	Name     string
	OwnerURL string // pda_owner: migrations, setups and assertions the app role cannot do
	AppURL   string // pda_app: the role the application uses
}

var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// NewDatabase creates pda_t_<name>_<8 hex> as pda_owner, who has CREATEDB, and
// applies every embedded migration. drop removes the database, unless
// PDA_TEST_KEEP=1 keeps it for inspection.
func NewDatabase(ctx context.Context, name string) (db *Database, drop func() error, err error) {
	if !dbName.MatchString(name) {
		return nil, nil, fmt.Errorf("testkit: invalid database name %q", name)
	}
	vals, err := LoadDotEnv()
	if err != nil {
		return nil, nil, err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, nil, err
	}
	db = &Database{Name: "pda_t_" + name + "_" + hex.EncodeToString(suffix)}
	db.OwnerURL = databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], db.Name)
	db.AppURL = databaseURL("pda_app", vals["PDA_APP_PASSWORD"], db.Name)
	admin := databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], "pda")

	ident := pgx.Identifier{db.Name}.Sanitize()
	if err := adminExec(ctx, admin, "CREATE DATABASE "+ident); err != nil {
		return nil, nil, err
	}
	drop = func() error {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return nil
		}
		// Detached: the caller's context may be done by the time it cleans up.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return adminExec(ctx, admin, "DROP DATABASE "+ident+" WITH (FORCE)")
	}
	m, err := db.Migrator()
	if err != nil {
		_ = drop()
		return nil, nil, err
	}
	defer closeMigrator(m)
	if err := m.Up(); err != nil {
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: migrate up: %w", err)
	}
	return db, drop, nil
}

// Migrator returns golang-migrate over the embedded migrations, connected as
// pda_owner. The caller closes it with closeMigrator or m.Close.
func (d *Database) Migrator() (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("testkit: migrations source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5"+strings.TrimPrefix(d.OwnerURL, "postgres"))
	if err != nil {
		return nil, fmt.Errorf("testkit: migrate: %w", err)
	}
	return m, nil
}

func closeMigrator(m *migrate.Migrate) { _, _ = m.Close() }

func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("testkit: connect as pda_owner: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("testkit: %s: %w", strings.Fields(sql)[0], err)
	}
	return nil
}
```

`test/testkit/root.go` (substitui o arquivo inteiro):

```go
package testkit

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// RepoRoot walks up from the working directory to the directory holding go.mod.
func RepoRoot(tb testing.TB) string {
	tb.Helper()
	dir, err := findRepoRoot()
	if err != nil {
		tb.Fatal(err)
	}
	return dir
}

// findRepoRoot is RepoRoot for callers without a testing.TB, such as TestMain.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("testkit: go.mod not found above the working directory")
		}
		dir = parent
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestMigrationsUpDownUp` (`table wallets missing after up`: as migrations stub não criam nada) e `TestConstraints` (`disable triggers on wallets: ERROR: relation "wallets" does not exist`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`migrations/000001_create_wallets.down.sql` (substitui o stub do Passo 1):

```sql
DROP TABLE wallets;
```

`migrations/000001_create_wallets.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §3.1
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor BIGINT      NOT NULL CHECK (balance_minor >= 0),          -- WAL-04
    version       BIGINT      NOT NULL CHECK (version >= 1),                -- WAL-07
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),     -- WAL-03
    CONSTRAINT wallets_updated_after_created CHECK (updated_at >= created_at)
);
```

`migrations/000002_create_wager_transactions.down.sql` (substitui o stub do Passo 1):

```sql
DROP TABLE wager_transactions;
```

`migrations/000002_create_wager_transactions.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §3.2
CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL CHECK (origin IN ('INTERNAL','EXTERNAL')),
    kind                              TEXT        NOT NULL CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
    status                            TEXT        NOT NULL CHECK (status IN ('PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
    wallet_id                         UUID        NOT NULL REFERENCES wallets(id),
    player_id                         UUID        NOT NULL,
    amount_minor                      BIGINT      NOT NULL CHECK (amount_minor >= 0),
    currency                          CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- External metadata (NULL for OPENING)
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      CHAR(64)    CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    received_via                      TEXT        CHECK (received_via IN ('HTTP','SQS')),

    -- Result
    reference_transaction_id          UUID        REFERENCES wager_transactions(id),
    failure_code                      TEXT,
    result_balance_minor              BIGINT      CHECK (result_balance_minor >= 0),

    -- Reference resolution schedule (D-11)
    attempts                          INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at                   TIMESTAMPTZ,
    expires_at                        TIMESTAMPTZ,

    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    -- Target of the ledger's composite FK
    CONSTRAINT wager_tx_id_wallet_uq UNIQUE (id, wallet_id),

    -- Origin × kind (TX-01, TX-04, TX-05)
    CONSTRAINT wager_tx_origin_kind CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wager_tx_internal_fields CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
            AND received_via IS NULL
            AND status = 'PROCESSED' AND amount_minor > 0
        )
    ),
    CONSTRAINT wager_tx_external_fields CHECK (
        origin <> 'EXTERNAL' OR (
            provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL AND received_via IS NOT NULL
        )
    ),

    -- Amount policy by kind (OPS-03, OPS-15)
    CONSTRAINT wager_tx_amount_policy CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),

    -- References by kind (OPS-06)
    CONSTRAINT wager_tx_reference_policy CHECK (
        CASE kind
            WHEN 'REFUND'   THEN reference_external_transaction_id IS NOT NULL
            WHEN 'ROLLBACK' THEN reference_external_transaction_id IS NOT NULL
            WHEN 'WIN'      THEN TRUE
            ELSE reference_external_transaction_id IS NULL
        END
    ),
    CONSTRAINT wager_tx_resolved_reference CHECK (
        NOT (status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK')) OR reference_transaction_id IS NOT NULL
    ),
    CONSTRAINT wager_tx_resolved_win_reference CHECK (
        NOT (status = 'PROCESSED' AND kind = 'WIN' AND reference_external_transaction_id IS NOT NULL)
        OR reference_transaction_id IS NOT NULL
    ),

    -- State × result (TX-03, TX-06)
    CONSTRAINT wager_tx_failure_code CHECK (
        (status IN ('REJECTED','FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_tx_result_balance CHECK (
        status NOT IN ('PROCESSED','REJECTED') OR result_balance_minor IS NOT NULL
    ),
    CONSTRAINT wager_tx_completed_at CHECK (
        (status IN ('PROCESSED','REJECTED','FAILED')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT wager_tx_pending_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND expires_at IS NOT NULL)
    )
);

-- Idempotency (IDEM-02, IDEM-07)
CREATE UNIQUE INDEX wager_tx_idempotency_uq
    ON wager_transactions (provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_external_id_uq
    ON wager_transactions (provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';

-- A single opening per wallet (TX-05)
CREATE UNIQUE INDEX wager_tx_single_opening_uq
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

-- One successful compensation per reference (OPS-08, D-10)
CREATE UNIQUE INDEX wager_tx_single_reversal_uq
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND','ROLLBACK') AND status = 'PROCESSED';

-- Reference worker (D-11)
CREATE INDEX wager_tx_pending_due_idx
    ON wager_transactions (next_attempt_at) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_tx_pending_by_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

-- Queries by wallet
CREATE INDEX wager_tx_wallet_created_idx ON wager_transactions (wallet_id, created_at);
```

`migrations/000003_create_wallet_ledger_entries.down.sql` (substitui o stub do Passo 1):

```sql
DROP TABLE wallet_ledger_entries;
```

`migrations/000003_create_wallet_ledger_entries.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §3.3
CREATE TABLE wallet_ledger_entries (
    id                   UUID        PRIMARY KEY,
    wallet_id            UUID        NOT NULL REFERENCES wallets(id),
    transaction_id       UUID        NOT NULL,
    direction            TEXT        NOT NULL CHECK (direction IN ('DEBIT','CREDIT')),
    amount_minor         BIGINT      NOT NULL CHECK (amount_minor > 0),
    currency             CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_before_minor BIGINT      NOT NULL CHECK (balance_before_minor >= 0),
    balance_after_minor  BIGINT      NOT NULL CHECK (balance_after_minor >= 0),
    wallet_version       BIGINT      NOT NULL CHECK (wallet_version >= 1),
    created_at           TIMESTAMPTZ NOT NULL,

    CONSTRAINT ledger_wallet_tx_uq      UNIQUE (wallet_id, transaction_id),  -- LED-03
    CONSTRAINT ledger_wallet_version_uq UNIQUE (wallet_id, wallet_version),  -- D-16
    CONSTRAINT ledger_tx_wallet_fk FOREIGN KEY (transaction_id, wallet_id)
        REFERENCES wager_transactions (id, wallet_id),
    CONSTRAINT ledger_balance_math CHECK (                                    -- LED-02
        (direction = 'CREDIT' AND balance_after_minor = balance_before_minor + amount_minor) OR
        (direction = 'DEBIT'  AND balance_after_minor = balance_before_minor - amount_minor)
    )
);
```

`migrations/000004_create_inbox_outbox.down.sql` (substitui o stub do Passo 1):

```sql
DROP TABLE outbox_events;
DROP TABLE inbox_messages;
```

`migrations/000004_create_inbox_outbox.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §3.4
CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    message_hash   CHAR(64)    NOT NULL CHECK (message_hash ~ '^[0-9a-f]{64}$'),
    message_type   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions(id),
    outcome        TEXT        NOT NULL CHECK (outcome IN ('PROCESSED','REJECTED','PENDING_REFERENCE','IDEMPOTENT_REPLAY','FAILED')),
    received_at    TIMESTAMPTZ NOT NULL,
    processed_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT inbox_pk PRIMARY KEY (consumer_name, message_id)   -- SQS-03
);

-- data-model.md §3.5
CREATE TABLE outbox_events (
    event_id         UUID        PRIMARY KEY,
    aggregate_type   TEXT        NOT NULL CHECK (aggregate_type IN ('Wallet','WagerTransaction')),
    aggregate_id     TEXT        NOT NULL,
    message_group_id TEXT        NOT NULL,   -- always the walletId (SNS MessageGroupId)
    event_type       TEXT        NOT NULL CHECK (event_type IN (
                         'WagerTransactionProcessed','WagerTransactionRejected',
                         'WalletBalanceChanged','WagerTransactionPendingReference')),
    event_version    INT         NOT NULL CHECK (event_version >= 1),
    payload          JSONB       NOT NULL,
    correlation_id   TEXT        NOT NULL,
    causation_id     TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL,

    -- Publication control (D-13)
    attempts         INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  TIMESTAMPTZ NOT NULL,
    locked_by        TEXT,
    locked_until     TIMESTAMPTZ,
    published_at     TIMESTAMPTZ,
    last_error       TEXT,

    CONSTRAINT outbox_lock_pair CHECK ((locked_by IS NULL) = (locked_until IS NULL))
);

CREATE INDEX outbox_due_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_id, occurred_at);
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

- [ ] **Passo 6: `go.mod` e lint**

```bash
go mod tidy && git diff go.mod
make fmt && make lint
```

Esperado no `go.mod`: `github.com/golang-migrate/migrate/v4 v4.20.1` entre as diretas e `github.com/jackc/pgerrcode` como indireta; nada mais muda. `make lint`: `0 issues.`

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 4: Triggers de proteção (000005): I02b (owner), I02c, I02d e I02e

Transcreve o data-model §4, já com as lacunas fechadas pela spec §5 (`wallet_guard_delete`, `wager_tx_guard_delete`, `outbox_guard_update`). Os testes rodam no banco do pacote (`TestMain` + `testkit.NewEnv`), com o schema completo, e toda violação é tentada numa transação desfeita no fim.

**Arquivos:**
- Implementação: `migrations/000005_protection_triggers.down.sql`, `migrations/000005_protection_triggers.up.sql`
- Testes e helpers de teste: `internal/adapters/postgres/main_integration_test.go`, `internal/adapters/postgres/protection_integration_test.go`, `test/testkit/env.go`

**Interfaces:**
- Consome: os helpers de linha da Tarefa 3; `testkit.NewDatabase`.
- Produz: `testkit.Env{DB, App, Owner}`, `testkit.NewEnv(ctx, pkg) (*Env, func(), error)`, `(*Env).Config() config.Config`; nos testes: `env`, `stmt`, `exec`, `ins`, `attempt`, `attemptCommit`, `seeded`, `seedWallet`, `seedRows`, `moveWallet`, `processed`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`migrations/000005_protection_triggers.down.sql` (novo):

```sql
-- Stub of task 4: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000005_protection_triggers.up.sql` (novo):

```sql
-- Stub of task 4: replaced by the real migration in step 4.
SELECT 1;
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/main_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// env is this package's isolated database (test-plan §3.2).
var env *testkit.Env

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	e, cleanup, err := testkit.NewEnv(ctx, "postgres")
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

`internal/adapters/postgres/protection_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stmt is one step of a protection case: a SQL statement, or a row to insert.
type stmt struct {
	sql   string
	args  []any
	table string
	row   row
}

func exec(sql string, args ...any) stmt { return stmt{sql: sql, args: args} }

func ins(table string, r row) stmt { return stmt{table: table, row: r} }

func run(ctx context.Context, tx pgx.Tx, s stmt) error {
	if s.table != "" {
		return insert(ctx, tx, s.table, s.row)
	}
	_, err := tx.Exec(ctx, s.sql, s.args...)
	return err
}

// attempt runs the statements in one transaction that is always rolled back,
// and returns the first error. Nothing it does survives.
func attempt(t *testing.T, pool *pgxpool.Pool, stmts ...stmt) error {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, s := range stmts {
		if err := run(ctx, tx, s); err != nil {
			return err
		}
	}
	return nil
}

// attemptCommit runs the statements and commits: the deferred triggers only
// check at COMMIT. It returns the first error, from a statement or the commit.
func attemptCommit(t *testing.T, pool *pgxpool.Pool, stmts ...stmt) error {
	t.Helper()
	ctx := t.Context()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	for _, s := range stmts {
		if err := run(ctx, tx, s); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// seeded is a committed wallet with a 100.00 opening: the wallet, its
// PROCESSED OPENING and the CREDIT entry, written in the order the triggers
// require (data-model §4.2).
type seeded struct {
	wallet, player, opening, entry string
}

func seedWallet(t *testing.T) seeded {
	t.Helper()
	s := seeded{wallet: newID(), player: newID(), opening: newID(), entry: newID()}
	if err := attemptCommit(t, env.Owner,
		ins("wallets", walletRow(s.wallet, s.player, 10000)),
		ins("wager_transactions", openingRow(s.opening, s.wallet, s.player, 10000)),
		ins("wallet_ledger_entries", ledgerRow(s.entry, s.wallet, s.opening, "CREDIT", 10000, 0, 10000, 1)),
	); err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	return s
}

// seedRows commits extra rows for a seeded wallet.
func seedRows(t *testing.T, stmts ...stmt) {
	t.Helper()
	if err := attemptCommit(t, env.Owner, stmts...); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
}

func moveWallet(s seeded, balance, version int64) stmt {
	return exec(`UPDATE wallets SET balance_minor = $2, version = $3 WHERE id = $1`, s.wallet, balance, version)
}

// processed is a PROCESSED external operation of the seeded wallet.
func processed(s seeded, id, kind string, amount, result int64) row {
	return externalRow(id, s.wallet, s.player).with("kind", kind, "status", "PROCESSED",
		"failure_code", nil, "amount_minor", amount, "result_balance_minor", result)
}

// Covers: TST-I02, LED-04, E9 (I02b, as the owner)
func TestLedgerImmutable(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	mutations := map[string]stmt{
		"update":   exec(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, s.entry),
		"delete":   exec(`DELETE FROM wallet_ledger_entries WHERE id = $1`, s.entry),
		"truncate": exec(`TRUNCATE wallet_ledger_entries`),
	}
	for name, m := range mutations {
		t.Run("owner "+name, func(t *testing.T) {
			wantSQLState(t, attempt(t, env.Owner, m), "PDA01", "")
		})
	}
}

// Covers: TST-I02, WAL-06, LED-05, E9 (I02c)
func TestLedgerCoupling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		stmts    func(s seeded) []stmt
		atCommit bool
		code     string
	}{
		{name: "entry does not match the wallet state", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, s.wallet, s.player)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 1000, 10000, 9000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry for a LOSS", stmts: func(s seeded) []stmt {
			loss := newID()
			return []stmt{
				moveWallet(s, 15000, 2),
				ins("wager_transactions", processed(s, loss, "LOSS", 0, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, loss, "CREDIT", 5000, 10000, 15000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry for a REJECTED operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 15000, 2),
				ins("wager_transactions", externalRow(bet, s.wallet, s.player)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "CREDIT", 5000, 10000, 15000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry amount differs from the operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 3000, 7000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
			}
		}, code: "PDA04"},
		{name: "entry currency differs from the operation", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 8000).with("currency", "USD")),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
			}
		}, code: "PDA04"},
		{name: "BET credited", stmts: func(s seeded) []stmt {
			bet := newID()
			return []stmt{
				moveWallet(s, 12000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 12000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "CREDIT", 2000, 10000, 12000, 2)),
			}
		}, code: "PDA04"},
		{name: "ROLLBACK of a BET debited", stmts: func(s seeded) []stmt {
			bet, rollback := newID(), newID()
			return []stmt{
				moveWallet(s, 8000, 2),
				ins("wager_transactions", processed(s, bet, "BET", 2000, 8000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, bet, "DEBIT", 2000, 10000, 8000, 2)),
				moveWallet(s, 6000, 3),
				ins("wager_transactions", processed(s, rollback, "ROLLBACK", 2000, 6000).with(
					"reference_external_transaction_id", "ext-"+bet, "reference_transaction_id", bet)),
				ins("wallet_ledger_entries", ledgerRow(newID(), s.wallet, rollback, "DEBIT", 2000, 8000, 6000, 3)),
			}
		}, code: "PDA04"},
		// The trigger's comparisons with a missing wallet and transaction are NULL
		// and do not raise; the foreign keys reject the entry (M2 spec §5, gap 5).
		{name: "entry for a missing wallet and operation stops at the foreign key", stmts: func(seeded) []stmt {
			return []stmt{ins("wallet_ledger_entries", ledgerRow(newID(), newID(), newID(), "CREDIT", 10, 0, 10, 2))}
		}, code: "23503"},
		{name: "balance changed without an entry", stmts: func(s seeded) []stmt {
			return []stmt{moveWallet(s, 5000, 2)}
		}, atCommit: true, code: "PDA04"},
		{name: "PROCESSED operation without its entry", stmts: func(s seeded) []stmt {
			return []stmt{ins("wager_transactions", processed(s, newID(), "BET", 2000, 8000))}
		}, atCommit: true, code: "PDA04"},
		{name: "wallet opened with a balance and no entry", stmts: func(seeded) []stmt {
			return []stmt{ins("wallets", walletRow(newID(), newID(), 500))}
		}, atCommit: true, code: "PDA04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stmts := tc.stmts(seedWallet(t))
			var err error
			if tc.atCommit {
				err = attemptCommit(t, env.Owner, stmts...)
			} else {
				err = attempt(t, env.Owner, stmts...)
			}
			wantSQLState(t, err, tc.code, "")
		})
	}
}

// Covers: TST-I02, TX-07 (I02d)
func TestTerminalTransactionImmutable(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	rejected, failed := newID(), newID()
	seedRows(t,
		ins("wager_transactions", externalRow(rejected, s.wallet, s.player)),
		ins("wager_transactions", externalRow(failed, s.wallet, s.player).with(
			"status", "FAILED", "failure_code", "INTERNAL_PERMANENT_FAILURE", "result_balance_minor", nil)),
	)
	for status, id := range map[string]string{"PROCESSED": s.opening, "REJECTED": rejected, "FAILED": failed} {
		t.Run(status, func(t *testing.T) {
			err := attempt(t, env.Owner, exec(`UPDATE wager_transactions SET updated_at = updated_at + interval '1 second' WHERE id = $1`, id))
			wantSQLState(t, err, "PDA02", "")
		})
	}
}

// Covers: TST-I02, WAL-07, TX-07, OUT-01 (I02e)
func TestGuardTriggers(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	pending, event := newID(), newID()
	seedRows(t,
		ins("wager_transactions", pendingRow(pending, s.wallet, s.player)),
		ins("outbox_events", outboxRow(event, s.wallet)),
	)
	wallet := func(set string, args ...any) stmt {
		return exec(`UPDATE wallets SET `+set+` WHERE id = $1`, append([]any{s.wallet}, args...)...)
	}
	tx := func(set string, args ...any) stmt {
		return exec(`UPDATE wager_transactions SET `+set+` WHERE id = $1`, append([]any{pending}, args...)...)
	}
	outbox := func(set string, args ...any) stmt {
		return exec(`UPDATE outbox_events SET `+set+` WHERE event_id = $1`, append([]any{event}, args...)...)
	}
	cases := []struct {
		name  string
		stmts []stmt
		code  string // "" = allowed
	}{
		{"wallet player changed", []stmt{wallet(`player_id = $2`, newID())}, "PDA03"},
		{"wallet currency changed", []stmt{wallet(`currency = 'USD'`)}, "PDA03"},
		{"wallet creation instant changed", []stmt{wallet(`created_at = created_at - interval '1 second'`)}, "PDA03"},
		{"balance changed without version + 1", []stmt{wallet(`balance_minor = 5000`)}, "PDA03"},
		{"balance changed with version + 2", []stmt{wallet(`balance_minor = 5000, version = 3`)}, "PDA03"},
		{"version changed without balance change", []stmt{wallet(`version = 2`)}, "PDA03"},
		{"wallet deleted", []stmt{exec(`DELETE FROM wallets WHERE id = $1`, s.wallet)}, "PDA03"},
		{"wallet touched without a balance change is allowed", []stmt{wallet(`updated_at = updated_at + interval '1 second'`)}, ""},
		{"operation amount changed", []stmt{tx(`amount_minor = 999`)}, "PDA02"},
		{"operation idempotency key changed", []stmt{tx(`idempotency_key = 'other'`)}, "PDA02"},
		{"operation deleted", []stmt{exec(`DELETE FROM wager_transactions WHERE id = $1`, pending)}, "PDA02"},
		{"pending operation rescheduled is allowed", []stmt{tx(`attempts = 1, next_attempt_at = next_attempt_at + interval '1 second'`)}, ""},
		{"event payload changed", []stmt{outbox(`payload = '{}'`)}, "PDA05"},
		{"event type changed", []stmt{outbox(`event_type = 'WagerTransactionProcessed'`)}, "PDA05"},
		{"event occurrence changed", []stmt{outbox(`occurred_at = occurred_at + interval '1 second'`)}, "PDA05"},
		{"event unpublished", []stmt{outbox(`published_at = now()`), outbox(`published_at = NULL`)}, "PDA05"},
		{"publication control is allowed", []stmt{
			outbox(`attempts = 1, locked_by = 'instance-1', locked_until = now()`),
			outbox(`published_at = now(), locked_by = NULL, locked_until = NULL`),
		}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := attempt(t, env.Owner, tc.stmts...)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("allowed change failed: %v", err)
				}
				return
			}
			wantSQLState(t, err, tc.code, "")
		})
	}
}
```

`test/testkit/env.go` (novo):

```go
package testkit

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/config"
)

// Env is the isolated infrastructure of one test package (test-plan §3.2). M2
// provides the database; M4 and M5 add the queues and the topic.
type Env struct {
	DB    *Database
	App   *pgxpool.Pool // pda_app, the application role
	Owner *pgxpool.Pool // pda_owner, for setups and assertions the app role cannot do
}

// NewEnv is called from TestMain, which has no testing.TB. cleanup closes the
// pools and drops the database.
func NewEnv(ctx context.Context, pkg string) (env *Env, cleanup func(), err error) {
	db, drop, err := NewDatabase(ctx, pkg)
	if err != nil {
		return nil, nil, err
	}
	app, err := pgxpool.New(ctx, db.AppURL)
	if err != nil {
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: app pool: %w", err)
	}
	owner, err := pgxpool.New(ctx, db.OwnerURL)
	if err != nil {
		app.Close()
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: owner pool: %w", err)
	}
	cleanup = func() {
		app.Close()
		owner.Close()
		if err := drop(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: drop database:", err)
		}
	}
	return &Env{DB: db, App: app, Owner: owner}, cleanup, nil
}

// Config points at the isolated database with the accelerated times of
// test-plan §3.3. Only the database part is filled; later milestones add the
// rest as their components need it.
func (e *Env) Config() config.Config {
	return config.Config{
		LogLevel:        "error",
		ShutdownTimeout: 5 * time.Second,
		DatabaseURL:     e.DB.AppURL,
		DBMaxConns:      4,
		DBLockTimeout:   2 * time.Second,
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestLedgerImmutable`, `TestLedgerCoupling`, `TestTerminalTransactionImmutable` e `TestGuardTriggers` (`error = <nil> …, want SQLSTATE "PDA0x"`). **Passam já no red, por desenho:** o caso de `TestLedgerCoupling` "entry for a missing wallet and operation stops at the foreign key" (a FK é da Tarefa 3; o caso fixa a lacuna 5 da spec) e os dois casos "is allowed" de `TestGuardTriggers` (mudanças legítimas que os triggers não podem barrar). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`migrations/000005_protection_triggers.down.sql` (substitui o stub do Passo 1):

```sql
DROP TRIGGER outbox_guard ON outbox_events;
DROP TRIGGER wager_tx_no_delete ON wager_transactions;
DROP TRIGGER wager_tx_guard ON wager_transactions;
DROP TRIGGER wallet_no_delete ON wallets;
DROP TRIGGER wallet_guard ON wallets;
DROP TRIGGER wager_tx_processed_has_ledger ON wager_transactions;
DROP TRIGGER wallet_balance_has_ledger ON wallets;
DROP TRIGGER ledger_matches_wallet ON wallet_ledger_entries;
DROP TRIGGER ledger_no_truncate ON wallet_ledger_entries;
DROP TRIGGER ledger_no_update_delete ON wallet_ledger_entries;

DROP FUNCTION outbox_guard_update();
DROP FUNCTION wager_tx_guard_delete();
DROP FUNCTION wager_tx_guard_update();
DROP FUNCTION wallet_guard_delete();
DROP FUNCTION wallet_guard_update();
DROP FUNCTION wager_tx_require_ledger();
DROP FUNCTION wallet_require_ledger();
DROP FUNCTION ledger_check_wallet();
DROP FUNCTION ledger_block_mutation();
```

`migrations/000005_protection_triggers.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §4. The triggers also apply to the table owner.

-- §4.1 Append-only ledger (LED-04): PDA01
CREATE FUNCTION ledger_block_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% blocked)', TG_OP
        USING ERRCODE = 'PDA01';
END $$;

CREATE TRIGGER ledger_no_update_delete BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_mutation();

-- §4.2 Ledger × wallet × transaction coherence (WAL-06, LED-05): PDA04
CREATE FUNCTION ledger_check_wallet() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    w        wallets%ROWTYPE;
    t        wager_transactions%ROWTYPE;
    expected TEXT;
BEGIN
    -- 1. The entry reflects the current wallet state
    SELECT * INTO w FROM wallets WHERE id = NEW.wallet_id;
    IF NEW.currency <> w.currency
       OR NEW.balance_after_minor <> w.balance_minor
       OR NEW.wallet_version <> w.version THEN
        RAISE EXCEPTION 'ledger entry does not match wallet % state', NEW.wallet_id
            USING ERRCODE = 'PDA04';
    END IF;

    -- 2. Only PROCESSED transactions that move the balance get an entry, with the same amount and currency
    SELECT * INTO t FROM wager_transactions WHERE id = NEW.transaction_id;
    IF t.status <> 'PROCESSED' OR t.kind = 'LOSS'
       OR NEW.amount_minor <> t.amount_minor OR NEW.currency <> t.currency THEN
        RAISE EXCEPTION 'ledger entry does not match transaction %', NEW.transaction_id
            USING ERRCODE = 'PDA04';
    END IF;

    -- 3. The direction matches the kind (a ROLLBACK depends on its reference's kind)
    expected := CASE t.kind
        WHEN 'BET' THEN 'DEBIT'
        WHEN 'ROLLBACK' THEN (
            SELECT CASE r.kind WHEN 'BET' THEN 'CREDIT' ELSE 'DEBIT' END
            FROM wager_transactions r WHERE r.id = t.reference_transaction_id)
        ELSE 'CREDIT'  -- OPENING, WIN, REFUND
    END;
    IF NEW.direction IS DISTINCT FROM expected THEN
        RAISE EXCEPTION 'ledger direction % does not match transaction kind %', NEW.direction, t.kind
            USING ERRCODE = 'PDA04';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER ledger_matches_wallet BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_check_wallet();

-- Checked at commit: every balance change has the entry of the new version
CREATE FUNCTION wallet_require_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.balance_minor > 0)
       OR (TG_OP = 'UPDATE' AND NEW.balance_minor <> OLD.balance_minor) THEN
        IF NOT EXISTS (
            SELECT 1 FROM wallet_ledger_entries
            WHERE wallet_id = NEW.id AND wallet_version = NEW.version
              AND balance_after_minor = NEW.balance_minor
        ) THEN
            RAISE EXCEPTION 'wallet % balance changed without ledger entry', NEW.id
                USING ERRCODE = 'PDA04';
        END IF;
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER wallet_balance_has_ledger
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_require_ledger();

-- Checked at commit: every PROCESSED transaction that moves the balance has its entry
CREATE FUNCTION wager_tx_require_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM wallet_ledger_entries
        WHERE wallet_id = NEW.wallet_id AND transaction_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'processed transaction % has no ledger entry', NEW.id
            USING ERRCODE = 'PDA04';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER wager_tx_processed_has_ledger
    AFTER INSERT OR UPDATE OF status ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    WHEN (NEW.status = 'PROCESSED' AND NEW.kind <> 'LOSS')
    EXECUTE FUNCTION wager_tx_require_ledger();

-- §4.3 Wallet: version and immutable columns (WAL-07): PDA03
CREATE FUNCTION wallet_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet % immutable column changed', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    IF NEW.balance_minor <> OLD.balance_minor AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'wallet % balance change requires version + 1', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    IF NEW.balance_minor = OLD.balance_minor AND NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'wallet % version changed without balance change', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION wallet_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet % cannot be deleted', OLD.id USING ERRCODE = 'PDA03';
END $$;

CREATE TRIGGER wallet_guard BEFORE UPDATE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallet_guard_update();
CREATE TRIGGER wallet_no_delete BEFORE DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallet_guard_delete();

-- §4.4 Transaction: terminal states and immutable columns (TX-07): PDA02
CREATE FUNCTION wager_tx_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN
        RAISE EXCEPTION 'wager transaction % is terminal (%)', OLD.id, OLD.status
            USING ERRCODE = 'PDA02';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.amount_minor,
        NEW.currency, NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key,
        NEW.payload_hash, NEW.round_id, NEW.game_id, NEW.reference_external_transaction_id,
        NEW.received_via, NEW.correlation_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.amount_minor,
        OLD.currency, OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key,
        OLD.payload_hash, OLD.round_id, OLD.game_id, OLD.reference_external_transaction_id,
        OLD.received_via, OLD.correlation_id, OLD.created_at) THEN
        RAISE EXCEPTION 'wager transaction % immutable column changed', OLD.id
            USING ERRCODE = 'PDA02';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION wager_tx_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wager transaction % cannot be deleted', OLD.id USING ERRCODE = 'PDA02';
END $$;

CREATE TRIGGER wager_tx_guard BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_tx_guard_update();
CREATE TRIGGER wager_tx_no_delete BEFORE DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_tx_guard_delete();

-- §4.5 Outbox: immutable snapshot (OUT-01): PDA05
CREATE FUNCTION outbox_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.event_id, NEW.aggregate_type, NEW.aggregate_id, NEW.message_group_id, NEW.event_type,
        NEW.event_version, NEW.payload, NEW.correlation_id, NEW.causation_id, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.event_id, OLD.aggregate_type, OLD.aggregate_id, OLD.message_group_id, OLD.event_type,
        OLD.event_version, OLD.payload, OLD.correlation_id, OLD.causation_id, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox event % snapshot is immutable', OLD.event_id USING ERRCODE = 'PDA05';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS NULL THEN
        RAISE EXCEPTION 'outbox event % cannot be unpublished', OLD.event_id USING ERRCODE = 'PDA05';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER outbox_guard BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_guard_update();
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 5: Grants do `pda_app` (000006): I02b (app) e matriz de privilégios

Aplica o data-model §5 (D-17). A matriz é conferida por `has_table_privilege`, sem depender de dados.

**Arquivos:**
- Implementação: `migrations/000006_grant_app_role.down.sql`, `migrations/000006_grant_app_role.up.sql`
- Testes e helpers de teste: `internal/adapters/postgres/grants_integration_test.go`

**Interfaces:**
- Consome: `env`, `seedWallet`, `attempt`, `wantSQLState` (Tarefa 4).
- Produz: os privilégios do `pda_app` que os repositórios das Tarefas 6–9 usam.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`migrations/000006_grant_app_role.down.sql` (novo):

```sql
-- Stub of task 5: replaced by the real migration in step 4.
SELECT 1;
```

`migrations/000006_grant_app_role.up.sql` (novo):

```sql
-- Stub of task 5: replaced by the real migration in step 4.
SELECT 1;
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/grants_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"testing"
)

// Covers: TST-I02, LED-04, DB-03, E9 (I02b, as the application role)
func TestLedgerImmutableForApp(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	mutations := map[string]stmt{
		"update":   exec(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, s.entry),
		"delete":   exec(`DELETE FROM wallet_ledger_entries WHERE id = $1`, s.entry),
		"truncate": exec(`TRUNCATE wallet_ledger_entries`),
	}
	for name, m := range mutations {
		t.Run("app "+name, func(t *testing.T) {
			wantSQLState(t, attempt(t, env.App, m), "42501", "")
		})
	}
}

// Covers: DB-03, LED-04 (D-17: the privileges of pda_app, data-model §5)
func TestAppRolePrivileges(t *testing.T) {
	t.Parallel()
	want := map[string]map[string]bool{
		"wallets":               {"SELECT": true, "INSERT": true, "UPDATE": true},
		"wager_transactions":    {"SELECT": true, "INSERT": true, "UPDATE": true},
		"wallet_ledger_entries": {"SELECT": true, "INSERT": true},
		"inbox_messages":        {"SELECT": true, "INSERT": true},
		"outbox_events":         {"SELECT": true, "INSERT": true, "UPDATE": true},
	}
	for table, allowed := range want {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			var has bool
			if err := env.Owner.QueryRow(t.Context(), `SELECT has_table_privilege('pda_app', $1, $2)`, table, privilege).Scan(&has); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", table, privilege, err)
			}
			if has != allowed[privilege] {
				t.Errorf("pda_app %s on %s = %v, want %v", privilege, table, has, allowed[privilege])
			}
		}
	}
	var canCreate bool
	if err := env.Owner.QueryRow(t.Context(), `SELECT has_schema_privilege('pda_app', 'public', 'CREATE')`).Scan(&canCreate); err != nil {
		t.Fatalf("has_schema_privilege: %v", err)
	}
	if canCreate {
		t.Error("pda_app can CREATE in schema public, want no DDL")
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falha `TestAppRolePrivileges` (`pda_app SELECT on wager_transactions = false, want true`…). `TestLedgerImmutableForApp` **passa já no red**: sem grant nenhum, o `pda_app` recebe `42501` em tudo. Ele fica como regressão: um grant de `UPDATE`/`DELETE` no ledger o quebraria. Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`migrations/000006_grant_app_role.down.sql` (substitui o stub do Passo 1):

```sql
REVOKE ALL ON wallets, wager_transactions, wallet_ledger_entries, inbox_messages, outbox_events FROM pda_app;
```

`migrations/000006_grant_app_role.up.sql` (substitui o stub do Passo 1):

```sql
-- data-model.md §5 (D-17). The roles are created by deploy/postgres/01-roles.sh.
GRANT SELECT, INSERT, UPDATE ON wallets TO pda_app;
GRANT SELECT, INSERT, UPDATE ON wager_transactions TO pda_app;
GRANT SELECT, INSERT ON wallet_ledger_entries TO pda_app;
GRANT SELECT, INSERT ON inbox_messages TO pda_app;
GRANT SELECT, INSERT, UPDATE ON outbox_events TO pda_app;
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 6: Portas, UoW e carteiras: I19, I16 e I18 (carteiras)

Nascem as portas de persistência (spec §3, decisão 1), o UoW (§4.2, decisões 4, 5 e 14) e o primeiro repositório. As portas crescem nas Tarefas 7–9, junto com cada repositório.

**Arquivos:**
- Implementação: `internal/adapters/postgres/errors.go` (alterar), `internal/adapters/postgres/money_mapping.go`, `internal/adapters/postgres/querier.go`, `internal/adapters/postgres/repos.go`, `internal/adapters/postgres/uow.go`, `internal/adapters/postgres/wallet_repo.go`, `internal/app/ports.go`
- Testes e helpers de teste: `internal/adapters/postgres/domain_integration_test.go`, `internal/adapters/postgres/uow_integration_test.go`, `internal/adapters/postgres/wallet_repo_integration_test.go`

**Interfaces:**
- Consome: `translate`, sentinelas (Tarefa 2); `env`, `lockWallet` usa `env.Owner` (Tarefa 4); grants (Tarefa 5).
- Produz: `app.UnitOfWork{Do, Snapshot}`, `app.Repos{Wallets()}`, `app.WalletRepository{Insert, Lock, Get}`; `postgres.NewUnitOfWork(pool *pgxpool.Pool, cfg config.Config) *UnitOfWork`, `postgres.NewRepos(pool *pgxpool.Pool) app.Repos`; internos `querier`, `repos`, `walletRepo`, `toMoney`, `notFound`, `corrupted`, `invalidValue`, `interrupted`; nos testes: `brl`, `newUoW`, `reads`, `zeroWallet`, `insertWallet`, `walletSnapshot`, `wantKind`, `lockWallet`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`internal/adapters/postgres/querier.go` (novo):

```go
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is what the repositories need: a pgx.Tx inside the unit of work, the
// pool for reads outside it (D-14).
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

`internal/adapters/postgres/repos.go` (novo):

```go
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
)

// repos binds every repository to one querier.
type repos struct{ q querier }

var _ app.Repos = repos{}

// NewRepos returns the repositories over the pool, for reads outside a
// transaction (D-14). Writes belong in UnitOfWork.Do.
func NewRepos(pool *pgxpool.Pool) app.Repos { return repos{q: pool} }

func (r repos) Wallets() app.WalletRepository { return walletRepo(r) }
```

`internal/app/ports.go` (novo):

```go
// Package app holds the use cases and the ports they depend on. It knows the
// domain and the error vocabulary, never Fx, HTTP, SQS or the database driver.
package app

import (
	"context"

	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
// inside the transaction; it is never hidden in the context.
type UnitOfWork interface {
	// Do runs fn in one READ COMMITTED transaction with lock_timeout set
	// (D-09): commit when fn returns nil, rollback on an error or a panic.
	Do(ctx context.Context, fn func(Repos) error) error
	// Snapshot runs fn in a REPEATABLE READ READ ONLY transaction: one
	// consistent view, for the reconciliation (D-16).
	Snapshot(ctx context.Context, fn func(Repos) error) error
}

// Repos are the repositories bound to one transaction, or to the pool for
// reads outside a transaction.
type Repos interface {
	Wallets() WalletRepository
}

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	// Insert fails with ErrWalletAlreadyExists for a second (playerId, currency).
	Insert(ctx context.Context, w wallet.Wallet) error
	// Lock reads the wallet with SELECT … FOR UPDATE (D-09); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (wallet.Wallet, error)
	// Get reads the wallet without a lock; ErrNotFound when absent.
	Get(ctx context.Context, id string) (wallet.Wallet, error)
}
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/adapters/postgres/uow.go` (novo):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
)

var errNotImplemented = errors.New("postgres: not implemented")

// UnitOfWork is the SQL transaction boundary of the use cases (D-14).
type UnitOfWork struct{}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

func NewUnitOfWork(pool *pgxpool.Pool, cfg config.Config) *UnitOfWork { return &UnitOfWork{} }

func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repos) error) error {
	return errNotImplemented
}

func (u *UnitOfWork) Snapshot(ctx context.Context, fn func(app.Repos) error) error {
	return errNotImplemented
}
```

`internal/adapters/postgres/wallet_repo.go` (novo):

```go
package postgres

import (
	"context"

	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type walletRepo struct{ q querier }

func (r walletRepo) Insert(ctx context.Context, w wallet.Wallet) error { return errNotImplemented }

func (r walletRepo) Lock(ctx context.Context, id string) (wallet.Wallet, error) {
	return wallet.Wallet{}, errNotImplemented
}

func (r walletRepo) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return wallet.Wallet{}, errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/domain_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func brl(tb testing.TB, amount string) money.Money {
	tb.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		tb.Fatalf("money %s: %v", amount, err)
	}
	return m
}

// newUoW is the unit of work over the package database, as the app role.
func newUoW() *postgres.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

// zeroWallet is a new wallet with a zero opening balance: no OPENING, no entry.
func zeroWallet(tb testing.TB) wallet.Wallet {
	tb.Helper()
	w, err := wallet.Open(newID(), newID(), brl(tb, "0.00"), time.Now())
	if err != nil {
		tb.Fatalf("wallet.Open: %v", err)
	}
	return w
}

func insertWallet(tb testing.TB, w wallet.Wallet) {
	tb.Helper()
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return r.Wallets().Insert(tb.Context(), w) }); err != nil {
		tb.Fatalf("insert wallet: %v", err)
	}
}

func walletSnapshot(tb testing.TB, w wallet.Wallet) wallet.Snapshot {
	tb.Helper()
	s, err := w.Snapshot()
	if err != nil {
		tb.Fatalf("wallet snapshot: %v", err)
	}
	return s
}

// wantKind fails unless err classifies as kind and, when target is not nil,
// wraps target.
func wantKind(tb testing.TB, err error, kind apperrors.Kind, target error) {
	tb.Helper()
	if got := apperrors.Classify(err); got != kind {
		tb.Fatalf("Classify(%v) = %q, want %q", err, got, kind)
	}
	if target != nil && !errors.Is(err, target) {
		tb.Fatalf("errors.Is(%v, %v) = false", err, target)
	}
}

// lockWallet holds the wallet's row lock in another transaction until the test
// ends or release is called.
func lockWallet(t *testing.T, walletID string) (release func()) {
	t.Helper()
	tx, err := env.Owner.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, walletID); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}
	release = func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }
	t.Cleanup(release)
	return release
}
```

`internal/adapters/postgres/uow_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// Covers: DB-02, CONC-01, WAL-06 (I19; D-09, D-14, D-16)
func TestUnitOfWork(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// A pool of its own, so that the acquired-connection count is this test's.
	pool, err := pgxpool.New(ctx, env.DB.AppURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	uow := postgres.NewUnitOfWork(pool, env.Config())
	persisted := func(id string) bool {
		t.Helper()
		_, err := reads().Wallets().Get(ctx, id)
		if err != nil && !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("Get: %v", err)
		}
		return err == nil
	}
	noLeak := func() {
		t.Helper()
		if n := pool.Stat().AcquiredConns(); n != 0 {
			t.Fatalf("%d connections still acquired after the unit of work", n)
		}
	}

	t.Run("commits when fn returns nil", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		if err := uow.Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, w) }); err != nil {
			t.Fatalf("Do: %v", err)
		}
		if !persisted(w.ID()) {
			t.Fatal("wallet not committed")
		}
		noLeak()
	})

	t.Run("rolls back when fn fails", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		boom := errors.New("boom")
		err := uow.Do(ctx, func(r app.Repos) error {
			if err := r.Wallets().Insert(ctx, w); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("Do = %v, want the error of fn", err)
		}
		if persisted(w.ID()) {
			t.Fatal("wallet committed despite the error")
		}
		noLeak()
	})

	t.Run("rolls back and re-panics", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		recovered := func() (p any) {
			defer func() { p = recover() }()
			_ = uow.Do(ctx, func(r app.Repos) error {
				if err := r.Wallets().Insert(ctx, w); err != nil {
					return err
				}
				panic("boom")
			})
			return nil
		}()
		if recovered != "boom" {
			t.Fatalf("recovered %v, want the panic of fn", recovered)
		}
		if persisted(w.ID()) {
			t.Fatal("wallet committed despite the panic")
		}
		noLeak()
	})

	t.Run("lock timeout is transient", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		release := lockWallet(t, w.ID())
		defer release()
		start := time.Now()
		err := uow.Do(ctx, func(r app.Repos) error {
			_, err := r.Wallets().Lock(ctx, w.ID())
			return err
		})
		elapsed := time.Since(start)
		wantKind(t, err, apperrors.KindTransient, nil)
		if !strings.Contains(err.Error(), "55P03") {
			t.Fatalf("Do = %v, want lock_not_available (55P03)", err)
		}
		if lock := env.Config().DBLockTimeout; elapsed < lock*3/4 || elapsed > 3*lock {
			t.Fatalf("waited %v for the lock, want about DB_LOCK_TIMEOUT (%v)", elapsed, lock)
		}
		noLeak()
	})

	t.Run("snapshot is read only", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		err := uow.Snapshot(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, w) })
		wantKind(t, err, apperrors.KindPermanent, nil)
		if !strings.Contains(err.Error(), "25006") {
			t.Fatalf("Snapshot = %v, want read_only_sql_transaction (25006)", err)
		}
		noLeak()
	})
}

// Covers: DOM-06 (I16)
func TestContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("canceled inside the unit of work", func(t *testing.T) {
		first, second := zeroWallet(t), zeroWallet(t)
		ctx, cancel := context.WithCancel(t.Context())
		err := newUoW().Do(ctx, func(r app.Repos) error {
			if err := r.Wallets().Insert(ctx, first); err != nil {
				return err
			}
			cancel()
			return r.Wallets().Insert(ctx, second)
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do = %v, want context.Canceled", err)
		}
		wantKind(t, err, apperrors.KindTransient, nil)
		for _, w := range []string{first.ID(), second.ID()} {
			if _, err := reads().Wallets().Get(t.Context(), w); !errors.Is(err, app.ErrNotFound) {
				t.Fatalf("wallet %s after a canceled unit of work: %v, want not found", w, err)
			}
		}
	})

	t.Run("deadline while waiting for a lock", func(t *testing.T) {
		w := zeroWallet(t)
		insertWallet(t, w)
		release := lockWallet(t, w.ID())
		defer release()
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := newUoW().Do(ctx, func(r app.Repos) error {
			_, err := r.Wallets().Lock(ctx, w.ID())
			return err
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Do = %v, want context.DeadlineExceeded", err)
		}
		wantKind(t, err, apperrors.KindTransient, nil)
		if elapsed := time.Since(start); elapsed >= env.Config().DBLockTimeout {
			t.Fatalf("the deadline did not interrupt the wait: %v", elapsed)
		}
	})
}
```

`internal/adapters/postgres/wallet_repo_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Covers: DB-01, WAL-03, DOM-02, DOM-03 (I18: wallets)
func TestWalletRepository(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		got, err := reads().Wallets().Get(ctx, w.ID())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if walletSnapshot(t, got) != walletSnapshot(t, w) {
			t.Fatalf("Get = %+v, want %+v", walletSnapshot(t, got), walletSnapshot(t, w))
		}
		var locked wallet.Wallet
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err = r.Wallets().Lock(ctx, w.ID())
			return err
		}); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if walletSnapshot(t, locked) != walletSnapshot(t, w) {
			t.Fatalf("Lock = %+v, want %+v", walletSnapshot(t, locked), walletSnapshot(t, w))
		}
	})

	t.Run("not found", func(t *testing.T) {
		ctx := t.Context()
		for _, id := range []string{newID(), "not-a-uuid", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1"} {
			_, err := reads().Wallets().Get(ctx, id)
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})

	t.Run("second wallet for the player and currency", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		again, err := wallet.Open(newID(), w.PlayerID(), brl(t, "0.00"), w.CreatedAt())
		if err != nil {
			t.Fatal(err)
		}
		err = newUoW().Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, again) })
		wantKind(t, err, apperrors.KindConflict, app.ErrWalletAlreadyExists)
		if code := apperrors.CodeOf(err); code != "WALLET_ALREADY_EXISTS" {
			t.Fatalf("CodeOf = %q, want WALLET_ALREADY_EXISTS", code)
		}
	})

	t.Run("zero value is rejected before writing", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, wallet.Wallet{}) })
		wantKind(t, err, apperrors.KindPermanent, wallet.ErrUninitialized)
	})

	t.Run("stored row the domain refuses", func(t *testing.T) {
		ctx := t.Context()
		id := newID()
		seedRows(t, ins("wallets", walletRow(id, newID(), 0).with("currency", "XYZ")))
		_, err := reads().Wallets().Get(ctx, id)
		wantKind(t, err, apperrors.KindPermanent, money.ErrInvalidCurrency)
	})
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestWalletRepository`, `TestUnitOfWork` e `TestContextCancellation` (`postgres: not implemented`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/errors.go` (substitui a versão da Tarefa 2):

```go
package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// dbError is what a PostgreSQL error becomes when it leaves the adapter: the
// SQLSTATE and the constraint name only. Message, Detail, Where and Hint are
// dropped on purpose: Detail carries the whole row ("Failing row contains
// (…)"), a full financial payload that would end up in the logs (CHALLENGE §12).
type dbError struct {
	code       string
	constraint string
}

func (e *dbError) Error() string {
	if e.constraint == "" {
		return "postgres: " + e.code
	}
	return "postgres: " + e.code + " " + e.constraint
}

// sentinel is how a unique violation the use cases handle is reported.
type sentinel struct {
	kind apperrors.Kind
	code string
	err  error
}

// uniqueSentinels maps the constraints whose violation the app handles (D-14).
var uniqueSentinels = map[string]sentinel{
	"wallets_player_currency_uq":  {apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
	"wager_tx_idempotency_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_external_id_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_single_reversal_uq": {apperrors.KindTransient, "", app.ErrReversalRace},
	"inbox_pk":                    {apperrors.KindTransient, "", app.ErrInboxDuplicate},
}

// transientCodes are the SQLSTATEs that a retry can overcome, besides class 08
// (connection exceptions).
var transientCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout, D-09)
	"57P01": true, // admin_shutdown
	"57014": true, // query_canceled
	"53300": true, // too_many_connections
}

// translate classifies a database error (U09b). A *pgconn.PgError never leaves
// the adapter: unique violations the app handles become its sentinels, known
// transient SQLSTATEs become KindTransient, and every other rejection by the
// database (constraints, triggers PDA01–PDA05, 22003, 25006…) is
// KindPermanent, since a retry cannot fix it. Errors that are not PgError
// (context, network) pass untouched: apperrors.Classify treats them as
// transient (D-05).
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	cause := &dbError{code: pgErr.Code, constraint: pgErr.ConstraintName}
	if s, ok := uniqueSentinels[pgErr.ConstraintName]; ok && pgErr.Code == "23505" {
		return apperrors.New(s.kind, s.code, fmt.Errorf("%w: %w", s.err, cause))
	}
	if transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08") {
		return apperrors.New(apperrors.KindTransient, "", cause)
	}
	return apperrors.New(apperrors.KindPermanent, "", cause)
}

// notFound reports a missing row to Get and Lock.
func notFound() error {
	return apperrors.New(apperrors.KindNotFound, "", app.ErrNotFound)
}

// corrupted reports a stored row the domain refuses to rehydrate: a bug or
// tampering, never something a retry fixes.
func corrupted(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: stored row rejected by the domain: %w", err))
}

// invalidValue reports a domain value that cannot be written (zero value,
// PENDING…). Without it, D-05 would turn this programming error into a
// transient one, retried forever.
func invalidValue(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: value rejected before writing: %w", err))
}
```

`internal/adapters/postgres/money_mapping.go` (novo):

```go
package postgres

import (
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Money is stored as (amount BIGINT in minor units, currency CHAR(3)) (D-03).

func toMoney(minor int64, currency string) (money.Money, error) {
	return money.FromMinor(minor, money.Currency(currency))
}
```

`internal/adapters/postgres/uow.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/config"
)

// rollbackTimeout bounds the rollback, which runs even when the caller's
// context is already done.
const rollbackTimeout = 5 * time.Second

// UnitOfWork is the SQL transaction boundary of the use cases (D-14).
type UnitOfWork struct {
	pool        *pgxpool.Pool
	lockTimeout string
}

var _ app.UnitOfWork = (*UnitOfWork)(nil)

// NewUnitOfWork uses DB_LOCK_TIMEOUT as the lock_timeout of every Do.
func NewUnitOfWork(pool *pgxpool.Pool, cfg config.Config) *UnitOfWork {
	return &UnitOfWork{pool: pool, lockTimeout: strconv.FormatInt(cfg.DBLockTimeout.Milliseconds(), 10) + "ms"}
}

var (
	writeTx    = pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	snapshotTx = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
)

// Do runs fn in one READ COMMITTED transaction. lock_timeout is set for the
// whole transaction with set_config, the parameterizable SET LOCAL (D-09).
func (u *UnitOfWork) Do(ctx context.Context, fn func(app.Repos) error) error {
	return u.run(ctx, writeTx, true, fn)
}

// Snapshot runs fn in one REPEATABLE READ READ ONLY transaction (D-16).
func (u *UnitOfWork) Snapshot(ctx context.Context, fn func(app.Repos) error) error {
	return u.run(ctx, snapshotTx, false, fn)
}

func (u *UnitOfWork) run(ctx context.Context, opts pgx.TxOptions, setLockTimeout bool, fn func(app.Repos) error) error {
	tx, err := u.pool.BeginTx(ctx, opts)
	if err != nil {
		return interrupted(ctx, translate(err))
	}
	defer func() {
		if p := recover(); p != nil {
			rollback(ctx, tx)
			panic(p)
		}
	}()
	if setLockTimeout {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, u.lockTimeout); err != nil {
			rollback(ctx, tx)
			return interrupted(ctx, translate(err))
		}
	}
	if err := fn(repos{q: tx}); err != nil {
		rollback(ctx, tx)
		return interrupted(ctx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return interrupted(ctx, translate(err))
	}
	return nil
}

// rollback runs on a context detached from the caller's cancellation, so a
// canceled request never leaves its transaction open.
func rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// interrupted keeps the context error in the chain when the context ended the
// transaction, and classifies the failure as transient: nothing was committed
// (or the commit outcome is unknown), and the idempotency path makes a retry
// safe (DOM-06, I16).
func interrupted(ctx context.Context, err error) error {
	cerr := ctx.Err()
	if cerr == nil {
		return err
	}
	if errors.Is(err, cerr) && apperrors.Classify(err) == apperrors.KindTransient {
		return err
	}
	return apperrors.New(apperrors.KindTransient, "", errors.Join(cerr, err))
}
```

`internal/adapters/postgres/wallet_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type walletRepo struct{ q querier }

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r walletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, string(s.Balance.Currency()), s.Balance.Minor(), s.Version, s.CreatedAt, s.UpdatedAt)
	return translate(err)
}

// Lock takes the wallet's row lock until the end of the transaction (D-09).
// The lock_timeout set by the unit of work bounds the wait.
func (r walletRepo) Lock(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id)
}

func (r walletRepo) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

func (r walletRepo) get(ctx context.Context, sql, id string) (wallet.Wallet, error) {
	if !ident.Valid(id) { // not a canonical UUID: no such row, and no 22P02 from the database
		return wallet.Wallet{}, notFound()
	}
	var s wallet.Snapshot
	var currency string
	var minor int64
	err := r.q.QueryRow(ctx, sql, id).Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, notFound()
	}
	if err != nil {
		return wallet.Wallet{}, translate(err)
	}
	if s.Balance, err = toMoney(minor, currency); err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	return w, nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 7: Outbox e inbox: I18 (outbox e inbox)

A outbox grava o envelope selado; como `JSONB` normaliza o texto, o teste compara o payload como JSON (spec §4.3). A inbox recusa o zero value pelos `CHECK` da tabela.

**Arquivos:**
- Implementação: `internal/adapters/postgres/inbox_repo.go`, `internal/adapters/postgres/money_mapping.go` (alterar), `internal/adapters/postgres/outbox_repo.go`, `internal/adapters/postgres/repos.go` (alterar), `internal/app/ports.go` (alterar)
- Testes e helpers de teste: `internal/adapters/postgres/outbox_inbox_integration_test.go`

**Interfaces:**
- Consome: `newUoW`, `reads`, `wantKind`, `brl` (Tarefa 6); `hash` (Tarefa 3).
- Produz: `app.OutboxRepository{Insert}`, `app.InboxRepository{Find, Insert}`, `app.InboxMessage`, `app.InboxOutcome` e as 5 constantes, `Repos.Outbox()` e `Repos.Inbox()`; internos `outboxRepo`, `inboxRepo`, `nullableText`, `text`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`internal/adapters/postgres/repos.go` (substitui a versão da Tarefa 6):

```go
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
)

// repos binds every repository to one querier.
type repos struct{ q querier }

var _ app.Repos = repos{}

// NewRepos returns the repositories over the pool, for reads outside a
// transaction (D-14). Writes belong in UnitOfWork.Do.
func NewRepos(pool *pgxpool.Pool) app.Repos { return repos{q: pool} }

func (r repos) Wallets() app.WalletRepository { return walletRepo(r) }
func (r repos) Outbox() app.OutboxRepository  { return outboxRepo(r) }
func (r repos) Inbox() app.InboxRepository    { return inboxRepo(r) }
```

`internal/app/ports.go` (substitui a versão da Tarefa 6):

```go
// Package app holds the use cases and the ports they depend on. It knows the
// domain and the error vocabulary, never Fx, HTTP, SQS or the database driver.
package app

import (
	"context"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
// inside the transaction; it is never hidden in the context.
type UnitOfWork interface {
	// Do runs fn in one READ COMMITTED transaction with lock_timeout set
	// (D-09): commit when fn returns nil, rollback on an error or a panic.
	Do(ctx context.Context, fn func(Repos) error) error
	// Snapshot runs fn in a REPEATABLE READ READ ONLY transaction: one
	// consistent view, for the reconciliation (D-16).
	Snapshot(ctx context.Context, fn func(Repos) error) error
}

// Repos are the repositories bound to one transaction, or to the pool for
// reads outside a transaction.
type Repos interface {
	Wallets() WalletRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	// Insert fails with ErrWalletAlreadyExists for a second (playerId, currency).
	Insert(ctx context.Context, w wallet.Wallet) error
	// Lock reads the wallet with SELECT … FOR UPDATE (D-09); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (wallet.Wallet, error)
	// Get reads the wallet without a lock; ErrNotFound when absent.
	Get(ctx context.Context, id string) (wallet.Wallet, error)
}

// OutboxRepository records the events of the transaction (D-13).
type OutboxRepository interface {
	Insert(ctx context.Context, envs ...events.Envelope) error
}

// InboxRepository deduplicates SQS messages per consumer (SQS-03).
type InboxRepository interface {
	// Find returns nil, nil when the message was never recorded.
	Find(ctx context.Context, consumer, messageID string) (*InboxMessage, error)
	// Insert fails with ErrInboxDuplicate when the message is already recorded.
	Insert(ctx context.Context, m InboxMessage) error
}

// InboxOutcome is how a message was concluded (data-model §3.4).
type InboxOutcome string

const (
	InboxProcessed        InboxOutcome = "PROCESSED"
	InboxRejected         InboxOutcome = "REJECTED"
	InboxPendingReference InboxOutcome = "PENDING_REFERENCE"
	InboxIdempotentReplay InboxOutcome = "IDEMPOTENT_REPLAY"
	InboxFailed           InboxOutcome = "FAILED"
)

// InboxMessage is one row of inbox_messages.
type InboxMessage struct {
	ConsumerName  string
	MessageID     string
	MessageHash   string
	MessageType   string
	TransactionID string // "" when the message did not reach an operation
	Outcome       InboxOutcome
	ReceivedAt    time.Time
	ProcessedAt   time.Time
}
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/adapters/postgres/inbox_repo.go` (novo):

```go
package postgres

import (
	"context"

	"github.com/KaioVinicios/pda/internal/app"
)

type inboxRepo struct{ q querier }

func (r inboxRepo) Find(ctx context.Context, consumer, messageID string) (*app.InboxMessage, error) {
	return nil, errNotImplemented
}

func (r inboxRepo) Insert(ctx context.Context, m app.InboxMessage) error { return errNotImplemented }
```

`internal/adapters/postgres/outbox_repo.go` (novo):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/events"
)

var errNotImplemented = errors.New("postgres: not implemented")

type outboxRepo struct{ q querier }

func (r outboxRepo) Insert(ctx context.Context, envs ...events.Envelope) error {
	return errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/outbox_inbox_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: OUT-01, OUT-09, OUT-12 (I18: outbox)
func TestOutboxRepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: newID(), PlayerID: newID(), Initial: brl(t, "25.00"),
		TransactionID: newID(), EntryID: newID(), CorrelationID: "corr-outbox", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	processed, err := events.Seal(newID(), "corr-outbox", "", o.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	changed, err := events.Seal(newID(), "corr-outbox", "cause-1", o.Events[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, processed, changed) }); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for _, ev := range []events.Envelope{processed, changed} {
		want, err := ev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var (
			aggType, aggID, group, eventType, correlation string
			causation                                     *string
			version, attempts                             int
			occurred                                      time.Time
			published                                     *time.Time
			samePayload                                   bool
		)
		// JSONB normalizes the text (key order, spaces): the payload is compared as JSON.
		if err := env.Owner.QueryRow(ctx, `SELECT aggregate_type, aggregate_id, message_group_id, event_type,
			event_version, correlation_id, causation_id, occurred_at, attempts, published_at, payload = $2::jsonb
			FROM outbox_events WHERE event_id = $1`, ev.EventID(), string(want)).
			Scan(&aggType, &aggID, &group, &eventType, &version, &correlation, &causation, &occurred, &attempts, &published, &samePayload); err != nil {
			t.Fatalf("read event %s: %v", ev.Type(), err)
		}
		switch {
		case aggType != string(ev.AggregateType()) || aggID != ev.AggregateID() || group != ev.MessageGroupID():
			t.Errorf("%s: aggregate %s/%s group %s", ev.Type(), aggType, aggID, group)
		case eventType != string(ev.Type()) || version != ev.Version() || correlation != "corr-outbox":
			t.Errorf("%s: type %s v%d correlation %s", ev.Type(), eventType, version, correlation)
		case !occurred.Equal(ev.OccurredAt()) || attempts != 0 || published != nil:
			t.Errorf("%s: occurred %v attempts %d published %v", ev.Type(), occurred, attempts, published)
		case !samePayload:
			t.Errorf("%s: stored payload differs from the envelope JSON", ev.Type())
		}
		if (causation == nil) != (ev.CausationID() == "") {
			t.Errorf("%s: causation %v, want %q", ev.Type(), causation, ev.CausationID())
		}
	}

	t.Run("unsealed envelope is rejected before writing", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, events.Envelope{}) })
		wantKind(t, err, apperrors.KindPermanent, events.ErrInvalidEvent)
	})
	t.Run("event recorded twice", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, processed) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}

// Covers: SQS-03, SQS-04 (I18: inbox)
func TestInboxRepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	received := time.Now().UTC().Truncate(time.Microsecond)
	m := app.InboxMessage{
		ConsumerName: "wager-transactions", MessageID: "msg-" + newID(), MessageHash: hash,
		MessageType: "WagerTransactionRequested", Outcome: app.InboxRejected,
		ReceivedAt: received, ProcessedAt: received.Add(time.Millisecond),
	}
	if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, m) }); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := reads().Inbox().Find(ctx, m.ConsumerName, m.MessageID)
	if err != nil || got == nil || *got != m {
		t.Fatalf("Find = %+v, %v; want %+v", got, err, m)
	}

	t.Run("unknown message", func(t *testing.T) {
		ctx := t.Context()
		got, err := reads().Inbox().Find(ctx, m.ConsumerName, "msg-unknown")
		if got != nil || err != nil {
			t.Fatalf("Find = %+v, %v; want nil, nil", got, err)
		}
	})
	t.Run("message recorded twice", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, m) })
		wantKind(t, err, apperrors.KindTransient, app.ErrInboxDuplicate)
	})
	t.Run("zero value is rejected", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, app.InboxMessage{}) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestOutboxRepository` e `TestInboxRepository` (`Insert: postgres: not implemented`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/inbox_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/app"
)

type inboxRepo struct{ q querier }

const inboxColumns = `consumer_name, message_id, message_hash, message_type, transaction_id, outcome, received_at, processed_at`

func (r inboxRepo) Find(ctx context.Context, consumer, messageID string) (*app.InboxMessage, error) {
	var m app.InboxMessage
	var txID *string
	err := r.q.QueryRow(ctx, `SELECT `+inboxColumns+` FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).
		Scan(&m.ConsumerName, &m.MessageID, &m.MessageHash, &m.MessageType, &txID, &m.Outcome, &m.ReceivedAt, &m.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, translate(err)
	}
	m.TransactionID = text(txID)
	m.ReceivedAt, m.ProcessedAt = m.ReceivedAt.UTC(), m.ProcessedAt.UTC()
	return &m, nil
}

// Insert records the message in the transaction of its domain changes
// (SQS-04). The table CHECKs refuse a zero value (empty hash, unknown outcome).
func (r inboxRepo) Insert(ctx context.Context, m app.InboxMessage) error {
	_, err := r.q.Exec(ctx, `INSERT INTO inbox_messages (`+inboxColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ConsumerName, m.MessageID, m.MessageHash, m.MessageType, nullableText(m.TransactionID),
		string(m.Outcome), m.ReceivedAt, m.ProcessedAt)
	return translate(err)
}
```

`internal/adapters/postgres/money_mapping.go` (substitui a versão da Tarefa 6):

```go
package postgres

import (
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Money is stored as (amount BIGINT in minor units, currency CHAR(3)) (D-03).
// Absent values travel as NULL and come back as the zero values the domain
// snapshots use.

func toMoney(minor int64, currency string) (money.Money, error) {
	return money.FromMinor(minor, money.Currency(currency))
}

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
```

`internal/adapters/postgres/outbox_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"encoding/json"

	"github.com/KaioVinicios/pda/internal/domain/events"
)

type outboxRepo struct{ q querier }

// Insert records sealed envelopes as immutable snapshots (OUT-01, OUT-09). The
// payload is the envelope JSON; next_attempt_at uses the database clock, the
// same one the publisher's claim compares with.
func (r outboxRepo) Insert(ctx context.Context, envs ...events.Envelope) error {
	for _, env := range envs {
		payload, err := env.MarshalJSON()
		if err != nil {
			return invalidValue(err)
		}
		_, err = r.q.Exec(ctx, `INSERT INTO outbox_events
			(event_id, aggregate_type, aggregate_id, message_group_id, event_type, event_version,
			 payload, correlation_id, causation_id, occurred_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
			env.EventID(), string(env.AggregateType()), env.AggregateID(), env.MessageGroupID(),
			string(env.Type()), env.Version(), json.RawMessage(payload), env.CorrelationID(),
			nullableText(env.CausationID()), env.OccurredAt())
		if err != nil {
			return translate(err)
		}
	}
	return nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 8: Caminho de escrita: transações, lançamentos e saldo (I18)

Grava cada estado persistível e reidrata idêntico, inclusive a rejeição `CURRENCY_MISMATCH`, cujo saldo observado está na moeda da carteira (spec §2, decisão 12). Os helpers de teste passam a rodar o pipeline do lifecycle §6.1 como o caso de uso do M3 fará.

**Arquivos:**
- Implementação: `internal/adapters/postgres/errors.go` (alterar), `internal/adapters/postgres/ledger_repo.go`, `internal/adapters/postgres/money_mapping.go` (alterar), `internal/adapters/postgres/repos.go` (alterar), `internal/adapters/postgres/transaction_repo.go`, `internal/adapters/postgres/wallet_repo.go` (alterar), `internal/app/ports.go` (alterar)
- Testes e helpers de teste: `internal/adapters/postgres/domain_integration_test.go` (alterar), `internal/adapters/postgres/transaction_repo_integration_test.go`, `test/testkit/postgres.go` (alterar)

**Interfaces:**
- Consome: tudo das Tarefas 6 e 7; `wagering.OpenWallet`, `NewCommand`, `NewExternal`, `Settle`, `events.Seal` (M1).
- Produz: `app.TransactionRepository{Insert, Update, Get}`, `app.LedgerRepository{Insert}`, `WalletRepository.UpdateBalance`, `Repos.Transactions()` e `Repos.Ledger()`; internos `transactionRepo`, `scanTransaction`, `ledgerRepo`, `staleRow`, `nullableMinor`, `optionalMoney`, `nullableTime`, `instant`; `testkit.LedgerProblems(ctx, pool, walletID) ([]string, error)` e `testkit.AssertLedgerConsistent(tb, pool, walletID)`; nos testes: `policy`, `newProvider`, `ptr`, `command`, `seal`, `persistOpening`, `openWallet`, `persistOutcome`, `processWith`, `process`, `txSnapshot`, `wantStored`.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`internal/adapters/postgres/repos.go` (substitui a versão da Tarefa 7):

```go
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
)

// repos binds every repository to one querier.
type repos struct{ q querier }

var _ app.Repos = repos{}

// NewRepos returns the repositories over the pool, for reads outside a
// transaction (D-14). Writes belong in UnitOfWork.Do.
func NewRepos(pool *pgxpool.Pool) app.Repos { return repos{q: pool} }

func (r repos) Wallets() app.WalletRepository           { return walletRepo(r) }
func (r repos) Transactions() app.TransactionRepository { return transactionRepo(r) }
func (r repos) Ledger() app.LedgerRepository            { return ledgerRepo(r) }
func (r repos) Outbox() app.OutboxRepository            { return outboxRepo(r) }
func (r repos) Inbox() app.InboxRepository              { return inboxRepo(r) }
```

`internal/app/ports.go` (substitui a versão da Tarefa 7):

```go
// Package app holds the use cases and the ports they depend on. It knows the
// domain and the error vocabulary, never Fx, HTTP, SQS or the database driver.
package app

import (
	"context"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
// inside the transaction; it is never hidden in the context.
type UnitOfWork interface {
	// Do runs fn in one READ COMMITTED transaction with lock_timeout set
	// (D-09): commit when fn returns nil, rollback on an error or a panic.
	Do(ctx context.Context, fn func(Repos) error) error
	// Snapshot runs fn in a REPEATABLE READ READ ONLY transaction: one
	// consistent view, for the reconciliation (D-16).
	Snapshot(ctx context.Context, fn func(Repos) error) error
}

// Repos are the repositories bound to one transaction, or to the pool for
// reads outside a transaction.
type Repos interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	// Insert fails with ErrWalletAlreadyExists for a second (playerId, currency).
	Insert(ctx context.Context, w wallet.Wallet) error
	// Lock reads the wallet with SELECT … FOR UPDATE (D-09); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (wallet.Wallet, error)
	// Get reads the wallet without a lock; ErrNotFound when absent.
	Get(ctx context.Context, id string) (wallet.Wallet, error)
	// UpdateBalance writes balance, version and updatedAt of a wallet moved by
	// one version; it fails as permanent when the stored version is not the
	// previous one.
	UpdateBalance(ctx context.Context, w wallet.Wallet) error
}

// TransactionRepository persists wager transactions.
type TransactionRepository interface {
	Insert(ctx context.Context, t *wagering.WagerTransaction) error
	// Update writes the state columns of a transaction that left
	// PENDING_REFERENCE or was rescheduled.
	Update(ctx context.Context, t *wagering.WagerTransaction) error
	// Get returns ErrNotFound when absent.
	Get(ctx context.Context, id string) (*wagering.WagerTransaction, error)
}

// LedgerRepository appends and reads ledger entries.
type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
}

// OutboxRepository records the events of the transaction (D-13).
type OutboxRepository interface {
	Insert(ctx context.Context, envs ...events.Envelope) error
}

// InboxRepository deduplicates SQS messages per consumer (SQS-03).
type InboxRepository interface {
	// Find returns nil, nil when the message was never recorded.
	Find(ctx context.Context, consumer, messageID string) (*InboxMessage, error)
	// Insert fails with ErrInboxDuplicate when the message is already recorded.
	Insert(ctx context.Context, m InboxMessage) error
}

// InboxOutcome is how a message was concluded (data-model §3.4).
type InboxOutcome string

const (
	InboxProcessed        InboxOutcome = "PROCESSED"
	InboxRejected         InboxOutcome = "REJECTED"
	InboxPendingReference InboxOutcome = "PENDING_REFERENCE"
	InboxIdempotentReplay InboxOutcome = "IDEMPOTENT_REPLAY"
	InboxFailed           InboxOutcome = "FAILED"
)

// InboxMessage is one row of inbox_messages.
type InboxMessage struct {
	ConsumerName  string
	MessageID     string
	MessageHash   string
	MessageType   string
	TransactionID string // "" when the message did not reach an operation
	Outcome       InboxOutcome
	ReceivedAt    time.Time
	ProcessedAt   time.Time
}
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/adapters/postgres/ledger_repo.go` (novo):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

var errNotImplemented = errors.New("postgres: not implemented")

type ledgerRepo struct{ q querier }

func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error { return errNotImplemented }
```

`internal/adapters/postgres/transaction_repo.go` (novo):

```go
package postgres

import (
	"context"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type transactionRepo struct{ q querier }

func (r transactionRepo) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	return errNotImplemented
}

func (r transactionRepo) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	return errNotImplemented
}

func (r transactionRepo) Get(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	return nil, errNotImplemented
}
```

`internal/adapters/postgres/wallet_repo.go` (substitui a versão da Tarefa 6):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type walletRepo struct{ q querier }

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r walletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, string(s.Balance.Currency()), s.Balance.Minor(), s.Version, s.CreatedAt, s.UpdatedAt)
	return translate(err)
}

// Lock takes the wallet's row lock until the end of the transaction (D-09).
// The lock_timeout set by the unit of work bounds the wait.
func (r walletRepo) Lock(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id)
}

func (r walletRepo) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

func (r walletRepo) get(ctx context.Context, sql, id string) (wallet.Wallet, error) {
	if !ident.Valid(id) { // not a canonical UUID: no such row, and no 22P02 from the database
		return wallet.Wallet{}, notFound()
	}
	var s wallet.Snapshot
	var currency string
	var minor int64
	err := r.q.QueryRow(ctx, sql, id).Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, notFound()
	}
	if err != nil {
		return wallet.Wallet{}, translate(err)
	}
	if s.Balance, err = toMoney(minor, currency); err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	return w, nil
}

func (r walletRepo) UpdateBalance(ctx context.Context, w wallet.Wallet) error {
	return errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/domain_integration_test.go` (substitui a versão da Tarefa 6):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func brl(tb testing.TB, amount string) money.Money {
	tb.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		tb.Fatalf("money %s: %v", amount, err)
	}
	return m
}

// newUoW is the unit of work over the package database, as the app role.
func newUoW() *postgres.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

// zeroWallet is a new wallet with a zero opening balance: no OPENING, no entry.
func zeroWallet(tb testing.TB) wallet.Wallet {
	tb.Helper()
	w, err := wallet.Open(newID(), newID(), brl(tb, "0.00"), time.Now())
	if err != nil {
		tb.Fatalf("wallet.Open: %v", err)
	}
	return w
}

func insertWallet(tb testing.TB, w wallet.Wallet) {
	tb.Helper()
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return r.Wallets().Insert(tb.Context(), w) }); err != nil {
		tb.Fatalf("insert wallet: %v", err)
	}
}

func walletSnapshot(tb testing.TB, w wallet.Wallet) wallet.Snapshot {
	tb.Helper()
	s, err := w.Snapshot()
	if err != nil {
		tb.Fatalf("wallet snapshot: %v", err)
	}
	return s
}

// wantKind fails unless err classifies as kind and, when target is not nil,
// wraps target.
func wantKind(tb testing.TB, err error, kind apperrors.Kind, target error) {
	tb.Helper()
	if got := apperrors.Classify(err); got != kind {
		tb.Fatalf("Classify(%v) = %q, want %q", err, got, kind)
	}
	if target != nil && !errors.Is(err, target) {
		tb.Fatalf("errors.Is(%v, %v) = false", err, target)
	}
}

// lockWallet holds the wallet's row lock in another transaction until the test
// ends or release is called.
func lockWallet(t *testing.T, walletID string) (release func()) {
	t.Helper()
	tx, err := env.Owner.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, walletID); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}
	release = func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }
	t.Cleanup(release)
	return release
}

// policy is the reference schedule of the integration tests (test-plan §3.3).
var policy = func() wagering.ReferenceRetryPolicy {
	p, err := wagering.NewReferenceRetryPolicy(100*time.Millisecond, time.Second, 3, time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return p
}()

// newProvider isolates the external ids of a test from the parallel ones.
func newProvider() string { return "provider-" + newID() }

func ptr(s string) *string { return &s }

// command validates an operation on w. ref "" = no reference; the currency is
// BRL unless the amount says otherwise ("10.00 USD").
func command(tb testing.TB, w wallet.Wallet, provider string, kind wagering.Kind, amount, ext, ref string) wagering.Command {
	tb.Helper()
	currency := "BRL"
	if a, c, ok := strings.Cut(amount, " "); ok {
		amount, currency = a, c
	}
	in := wagering.Input{
		IdempotencyKey: ptr(provider + ":" + ext), ProviderID: ptr(provider), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(string(kind)), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		tb.Fatalf("NewCommand: %v", err)
	}
	return cmd
}

// seal wraps the domain events in envelopes, as the app does before the INSERT.
func seal(evs []events.Event, correlationID string) ([]events.Envelope, error) {
	out := make([]events.Envelope, 0, len(evs))
	for _, e := range evs {
		env, err := events.Seal(newID(), correlationID, "", e)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// persistOpening writes a wallet opening like POST /wallets (lifecycle §6.4).
func persistOpening(ctx context.Context, r app.Repos, o wagering.Opening) error {
	if err := r.Wallets().Insert(ctx, o.Wallet); err != nil {
		return err
	}
	if o.Tx == nil {
		return nil
	}
	if err := r.Transactions().Insert(ctx, o.Tx); err != nil {
		return err
	}
	if err := r.Ledger().Insert(ctx, *o.Entry); err != nil {
		return err
	}
	envs, err := seal(o.Events, o.Tx.CorrelationID())
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// openWallet opens and persists a wallet with the initial balance.
func openWallet(tb testing.TB, initial string) (wallet.Wallet, wagering.Opening) {
	tb.Helper()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: newID(), PlayerID: newID(), Initial: brl(tb, initial),
		TransactionID: newID(), EntryID: newID(), CorrelationID: "corr-open", Now: time.Now(),
	})
	if err != nil {
		tb.Fatalf("OpenWallet: %v", err)
	}
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return persistOpening(tb.Context(), r, o) }); err != nil {
		tb.Fatalf("persist opening: %v", err)
	}
	return o.Wallet, o
}

// persistOutcome writes what Settle decided, in the order the triggers
// require: the operation is already written; then the wallet, the entry and
// the events (data-model §4.2).
func persistOutcome(ctx context.Context, r app.Repos, w wallet.Wallet, out wagering.Outcome, correlationID string) error {
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	envs, err := seal(out.Events, correlationID)
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// processWith runs the lifecycle §6.1 pipeline of a new operation the way the
// M3 use case will: lock the wallet, Settle, write. Task 9 adds the reference
// resolution and the advance of the dependents.
// wrap decorates the repositories (nil = none).
func processWith(ctx context.Context, uow app.UnitOfWork, cmd wagering.Command, now time.Time, wrap func(app.Repos) app.Repos) (*wagering.WagerTransaction, error) {
	var tx *wagering.WagerTransaction
	err := uow.Do(ctx, func(r app.Repos) error {
		if wrap != nil {
			r = wrap(r)
		}
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if err != nil {
			return err
		}
		var ref wagering.Reference // resolved from task 9 on
		if tx, err = wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr-"+cmd.ExternalTransactionID(), now); err != nil {
			return err
		}
		out, err := wagering.Settle(tx, &w, ref, wagering.SettleParams{EntryID: newID(), Now: now, Policy: policy})
		if err != nil {
			return err
		}
		if err := r.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		return persistOutcome(ctx, r, w, out, tx.CorrelationID())
	})
	return tx, err
}

// process runs processWith now and fails the test on error.
func process(tb testing.TB, cmd wagering.Command) *wagering.WagerTransaction {
	tb.Helper()
	tx, err := processWith(tb.Context(), newUoW(), cmd, time.Now(), nil)
	if err != nil {
		tb.Fatalf("process %s %s: %v", cmd.Kind(), cmd.ExternalTransactionID(), err)
	}
	return tx
}

func txSnapshot(tb testing.TB, tx *wagering.WagerTransaction) wagering.Snapshot {
	tb.Helper()
	s, err := tx.Snapshot()
	if err != nil {
		tb.Fatalf("transaction snapshot: %v", err)
	}
	return s
}

// wantStored fails unless the stored operation equals tx.
func wantStored(tb testing.TB, tx *wagering.WagerTransaction) {
	tb.Helper()
	got, err := reads().Transactions().Get(tb.Context(), tx.ID())
	if err != nil {
		tb.Fatalf("Get %s: %v", tx.ID(), err)
	}
	if g, w := txSnapshot(tb, got), txSnapshot(tb, tx); g != w {
		tb.Fatalf("stored operation differs:\n got  %+v\n want %+v", g, w)
	}
}
```

`internal/adapters/postgres/transaction_repo_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: DB-01, DB-02, TX-05, TX-07, WAL-06, IDEM-08, DOM-02, DOM-03 (I18: transactions and ledger writes)
func TestTransactionRepository(t *testing.T) {
	t.Parallel()
	w, opening := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })

	t.Run("round trip of every persisted state", func(t *testing.T) {
		ctx := t.Context()
		wantStored(t, opening.Tx) // INTERNAL OPENING, PROCESSED
		for _, tc := range []struct {
			kind        wagering.Kind
			amount, ext string
			ref         string
			status      wagering.Status
			failure     wagering.FailureCode
		}{
			{wagering.KindBet, "30.00", "bet-1", "", wagering.StatusProcessed, ""},
			{wagering.KindLoss, "0.00", "loss-1", "", wagering.StatusProcessed, ""},
			{wagering.KindBet, "500.00", "bet-2", "", wagering.StatusRejected, wagering.FailureInsufficientFunds},
			// The observed balance is the wallet's (BRL), not the operation's currency.
			{wagering.KindBet, "10.00 USD", "bet-3", "", wagering.StatusRejected, wagering.FailureCurrencyMismatch},
			{wagering.KindRefund, "30.00", "refund-1", "bet-missing", wagering.StatusPendingReference, ""},
		} {
			tx := process(t, command(t, w, p, tc.kind, tc.amount, tc.ext, tc.ref))
			if tx.Status() != tc.status || tx.FailureCode() != tc.failure {
				t.Fatalf("%s: %s %s, want %s %s", tc.ext, tx.Status(), tx.FailureCode(), tc.status, tc.failure)
			}
			wantStored(t, tx)
		}

		failed, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindBet, "5.00", "bet-4", ""), wagering.ReceivedViaSQS, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := failed.Fail(time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Insert(ctx, failed) }); err != nil {
			t.Fatalf("insert FAILED: %v", err)
		}
		wantStored(t, failed)
	})

	t.Run("pending operation is updated until terminal", func(t *testing.T) {
		ctx := t.Context()
		tx := process(t, command(t, w, p, wagering.KindRefund, "30.00", "refund-2", "bet-never"))
		update := func() error {
			return newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, tx) })
		}
		if err := tx.RescheduleReference(time.Now(), policy); err != nil {
			t.Fatal(err)
		}
		if err := update(); err != nil {
			t.Fatalf("Update rescheduled: %v", err)
		}
		wantStored(t, tx)
		if _, err := tx.Reject(wagering.FailureReferenceNotFound, w.Balance(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := update(); err != nil {
			t.Fatalf("Update rejected: %v", err)
		}
		wantStored(t, tx)
		// The row is terminal now: the wager_tx_guard trigger refuses any change (TX-07).
		wantKind(t, update(), apperrors.KindPermanent, nil)
	})

	t.Run("not found", func(t *testing.T) {
		ctx := t.Context()
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := reads().Transactions().Get(ctx, id)
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})

	t.Run("values that cannot be written", func(t *testing.T) {
		ctx := t.Context()
		pending, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindBet, "1.00", "bet-5", ""), wagering.ReceivedViaHTTP, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string]struct {
			write  func(r app.Repos) error
			target error
		}{
			"PENDING is never persisted": {func(r app.Repos) error { return r.Transactions().Insert(ctx, pending) }, wagering.ErrNotPersistable},
			"nil operation":              {func(r app.Repos) error { return r.Transactions().Insert(ctx, nil) }, wagering.ErrUninitialized},
			"nil operation update":       {func(r app.Repos) error { return r.Transactions().Update(ctx, nil) }, wagering.ErrUninitialized},
			"zero ledger entry":          {func(r app.Repos) error { return r.Ledger().Insert(ctx, wallet.LedgerEntry{}) }, wallet.ErrInvalidLedgerEntry},
			"zero wallet update":         {func(r app.Repos) error { return r.Wallets().UpdateBalance(ctx, wallet.Wallet{}) }, wallet.ErrUninitialized},
		}
		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				wantKind(t, newUoW().Do(ctx, tc.write), apperrors.KindPermanent, tc.target)
			})
		}
	})

	t.Run("update of an operation that was never written", func(t *testing.T) {
		ctx := t.Context()
		tx, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindRefund, "1.00", "refund-3", "bet-x"), wagering.ReceivedViaHTTP, "corr", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.AwaitReference(time.Now(), policy); err != nil {
			t.Fatal(err)
		}
		err = newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, tx) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})

	t.Run("balance update on a stale version", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err := r.Wallets().Lock(ctx, w.ID())
			if err != nil {
				return err
			}
			for range 2 { // two versions ahead of the stored one
				if _, err := locked.Credit(newID(), newID(), brl(t, "1.00"), time.Now()); err != nil {
					return err
				}
			}
			return r.Wallets().UpdateBalance(ctx, locked)
		})
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}
```

`test/testkit/postgres.go` (substitui a versão da Tarefa 3):

```go
package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/migrations"
)

// pgHost is the compose PostgreSQL seen from the host.
const pgHost = "localhost:5432"

func databaseURL(user, password, db string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     pgHost,
		Path:     "/" + db,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// AppDatabaseURL is the shared pda database as pda_app, seen from the host.
func AppDatabaseURL(tb testing.TB) string {
	tb.Helper()
	return databaseURL("pda_app", DotEnv(tb)["PDA_APP_PASSWORD"], "pda")
}

// Database is an isolated database with the embedded migrations applied
// (test-plan §3.2).
type Database struct {
	Name     string
	OwnerURL string // pda_owner: migrations, setups and assertions the app role cannot do
	AppURL   string // pda_app: the role the application uses
}

var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// NewDatabase creates pda_t_<name>_<8 hex> as pda_owner, who has CREATEDB, and
// applies every embedded migration. drop removes the database, unless
// PDA_TEST_KEEP=1 keeps it for inspection.
func NewDatabase(ctx context.Context, name string) (db *Database, drop func() error, err error) {
	if !dbName.MatchString(name) {
		return nil, nil, fmt.Errorf("testkit: invalid database name %q", name)
	}
	vals, err := LoadDotEnv()
	if err != nil {
		return nil, nil, err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, nil, err
	}
	db = &Database{Name: "pda_t_" + name + "_" + hex.EncodeToString(suffix)}
	db.OwnerURL = databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], db.Name)
	db.AppURL = databaseURL("pda_app", vals["PDA_APP_PASSWORD"], db.Name)
	admin := databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], "pda")

	ident := pgx.Identifier{db.Name}.Sanitize()
	if err := adminExec(ctx, admin, "CREATE DATABASE "+ident); err != nil {
		return nil, nil, err
	}
	drop = func() error {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return nil
		}
		// Detached: the caller's context may be done by the time it cleans up.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return adminExec(ctx, admin, "DROP DATABASE "+ident+" WITH (FORCE)")
	}
	m, err := db.Migrator()
	if err != nil {
		_ = drop()
		return nil, nil, err
	}
	defer closeMigrator(m)
	if err := m.Up(); err != nil {
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: migrate up: %w", err)
	}
	return db, drop, nil
}

// Migrator returns golang-migrate over the embedded migrations, connected as
// pda_owner. The caller closes it with closeMigrator or m.Close.
func (d *Database) Migrator() (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("testkit: migrations source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5"+strings.TrimPrefix(d.OwnerURL, "postgres"))
	if err != nil {
		return nil, fmt.Errorf("testkit: migrate: %w", err)
	}
	return m, nil
}

func closeMigrator(m *migrate.Migrate) { _, _ = m.Close() }

func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("testkit: connect as pda_owner: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("testkit: %s: %w", strings.Fields(sql)[0], err)
	}
	return nil
}

// AssertLedgerConsistent fails the test when LedgerProblems finds any. It is
// meant for t.Cleanup, where the test's context is already canceled, so it
// uses a detached one.
func AssertLedgerConsistent(tb testing.TB, pool *pgxpool.Pool, walletID string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	problems, err := LedgerProblems(ctx, pool, walletID)
	if err != nil {
		tb.Fatalf("wallet %s: %v", walletID, err)
	}
	for _, p := range problems {
		tb.Errorf("wallet %s: %s", walletID, p)
	}
}

// LedgerProblems runs the SQL part of the consistency verification of
// test-plan §6 (items 2–6) for one wallet and returns what is wrong; none
// means consistent. M3 adds the reconciliation endpoint (item 1) and the event
// matrix (item 7).
func LedgerProblems(ctx context.Context, pool *pgxpool.Pool, walletID string) ([]string, error) {
	var problems []string
	var balance, version int64
	if err := pool.QueryRow(ctx, `SELECT balance_minor, version FROM wallets WHERE id = $1`, walletID).
		Scan(&balance, &version); err != nil {
		return nil, fmt.Errorf("read wallet: %w", err)
	}

	// 2. stored == Σ CREDIT − Σ DEBIT
	var net int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&net); err != nil {
		return nil, fmt.Errorf("ledger sum: %w", err)
	}
	if net != balance {
		problems = append(problems, fmt.Sprintf("balance %d != Σ credits − Σ debits %d", balance, net))
	}

	// 3. chain: consecutive versions, before = previous after, the first starts at 0.
	// 4. wallets.version == max(wallet_version), or 1 without entries.
	rows, err := pool.Query(ctx, `
		SELECT wallet_version, balance_before_minor, balance_after_minor
		FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY wallet_version`, walletID)
	if err != nil {
		return nil, fmt.Errorf("ledger chain: %w", err)
	}
	defer rows.Close()
	last, prevAfter := int64(1), int64(0)
	first := true
	for rows.Next() {
		var v, before, after int64
		if err := rows.Scan(&v, &before, &after); err != nil {
			return nil, fmt.Errorf("ledger chain: %w", err)
		}
		switch {
		case first && (before != 0 || (v != 1 && v != 2)):
			problems = append(problems, fmt.Sprintf("first entry at version %d starts at %d, want version 1 or 2 from 0", v, before))
		case !first && (v != last+1 || before != prevAfter):
			problems = append(problems, fmt.Sprintf("entry v%d (before %d) does not follow v%d (after %d)", v, before, last, prevAfter))
		}
		first, last, prevAfter = false, v, after
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger chain: %w", err)
	}
	if version != last {
		problems = append(problems, fmt.Sprintf("version %d != last ledger version %d", version, last))
	}

	// 5. no entries for REJECTED, FAILED, PENDING_REFERENCE or LOSS.
	// 6. every PROCESSED operation that moves the balance has exactly one entry.
	var orphans, missing int64
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM wallet_ledger_entries l JOIN wager_transactions t ON t.id = l.transaction_id
		   WHERE l.wallet_id = $1 AND (t.status <> 'PROCESSED' OR t.kind = 'LOSS')),
		  (SELECT count(*) FROM wager_transactions t
		   WHERE t.wallet_id = $1 AND t.status = 'PROCESSED' AND t.kind <> 'LOSS'
		     AND (SELECT count(*) FROM wallet_ledger_entries l WHERE l.transaction_id = t.id) <> 1)`,
		walletID).Scan(&orphans, &missing); err != nil {
		return nil, fmt.Errorf("ledger coupling: %w", err)
	}
	if orphans != 0 {
		problems = append(problems, fmt.Sprintf("%d entries of operations that do not move the balance", orphans))
	}
	if missing != 0 {
		problems = append(problems, fmt.Sprintf("%d moving operations without exactly one entry", missing))
	}
	return problems, nil
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falha `TestTransactionRepository` (`persist opening: postgres: not implemented`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/errors.go` (substitui a versão da Tarefa 6):

```go
package postgres

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
)

// dbError is what a PostgreSQL error becomes when it leaves the adapter: the
// SQLSTATE and the constraint name only. Message, Detail, Where and Hint are
// dropped on purpose: Detail carries the whole row ("Failing row contains
// (…)"), a full financial payload that would end up in the logs (CHALLENGE §12).
type dbError struct {
	code       string
	constraint string
}

func (e *dbError) Error() string {
	if e.constraint == "" {
		return "postgres: " + e.code
	}
	return "postgres: " + e.code + " " + e.constraint
}

// sentinel is how a unique violation the use cases handle is reported.
type sentinel struct {
	kind apperrors.Kind
	code string
	err  error
}

// uniqueSentinels maps the constraints whose violation the app handles (D-14).
var uniqueSentinels = map[string]sentinel{
	"wallets_player_currency_uq":  {apperrors.KindConflict, "WALLET_ALREADY_EXISTS", app.ErrWalletAlreadyExists},
	"wager_tx_idempotency_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_external_id_uq":     {apperrors.KindTransient, "", app.ErrIdempotencyRace},
	"wager_tx_single_reversal_uq": {apperrors.KindTransient, "", app.ErrReversalRace},
	"inbox_pk":                    {apperrors.KindTransient, "", app.ErrInboxDuplicate},
}

// transientCodes are the SQLSTATEs that a retry can overcome, besides class 08
// (connection exceptions).
var transientCodes = map[string]bool{
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
	"55P03": true, // lock_not_available (lock_timeout, D-09)
	"57P01": true, // admin_shutdown
	"57014": true, // query_canceled
	"53300": true, // too_many_connections
}

// translate classifies a database error (U09b). A *pgconn.PgError never leaves
// the adapter: unique violations the app handles become its sentinels, known
// transient SQLSTATEs become KindTransient, and every other rejection by the
// database (constraints, triggers PDA01–PDA05, 22003, 25006…) is
// KindPermanent, since a retry cannot fix it. Errors that are not PgError
// (context, network) pass untouched: apperrors.Classify treats them as
// transient (D-05).
func translate(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	cause := &dbError{code: pgErr.Code, constraint: pgErr.ConstraintName}
	if s, ok := uniqueSentinels[pgErr.ConstraintName]; ok && pgErr.Code == "23505" {
		return apperrors.New(s.kind, s.code, fmt.Errorf("%w: %w", s.err, cause))
	}
	if transientCodes[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08") {
		return apperrors.New(apperrors.KindTransient, "", cause)
	}
	return apperrors.New(apperrors.KindPermanent, "", cause)
}

// notFound reports a missing row to Get and Lock.
func notFound() error {
	return apperrors.New(apperrors.KindNotFound, "", app.ErrNotFound)
}

// corrupted reports a stored row the domain refuses to rehydrate: a bug or
// tampering, never something a retry fixes.
func corrupted(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: stored row rejected by the domain: %w", err))
}

// invalidValue reports a domain value that cannot be written (zero value,
// PENDING…). Without it, D-05 would turn this programming error into a
// transient one, retried forever.
func invalidValue(err error) error {
	return apperrors.New(apperrors.KindPermanent, "", fmt.Errorf("postgres: value rejected before writing: %w", err))
}

// errNoRowUpdated reports an UPDATE that matched no row where one was expected.
var errNoRowUpdated = errors.New("postgres: the row to update was not found in the expected state")

func staleRow() error { return apperrors.New(apperrors.KindPermanent, "", errNoRowUpdated) }
```

`internal/adapters/postgres/ledger_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"fmt"

	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type ledgerRepo struct{ q querier }

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

// Insert appends an entry. The ledger_matches_wallet trigger requires the
// transaction to be written in its final state and the wallet updated first.
func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	if e.ID() == "" {
		return invalidValue(fmt.Errorf("%w: zero value", wallet.ErrInvalidLedgerEntry))
	}
	_, err := r.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Minor(),
		string(e.Amount().Currency()), e.BalanceBefore().Minor(), e.BalanceAfter().Minor(),
		e.WalletVersion(), e.CreatedAt())
	return translate(err)
}
```

`internal/adapters/postgres/money_mapping.go` (substitui a versão da Tarefa 7):

```go
package postgres

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Money is stored as (amount BIGINT in minor units, currency CHAR(3)) (D-03).
// Absent values travel as NULL and come back as the zero values the domain
// snapshots use.

func toMoney(minor int64, currency string) (money.Money, error) {
	return money.FromMinor(minor, money.Currency(currency))
}

// nullableMinor is NULL for an absent Money (the zero value).
func nullableMinor(m money.Money) *int64 {
	if !m.Currency().Valid() {
		return nil
	}
	v := m.Minor()
	return &v
}

// optionalMoney rebuilds a nullable amount in the row's currency.
func optionalMoney(minor *int64, currency string) (money.Money, error) {
	if minor == nil {
		return money.Money{}, nil
	}
	return toMoney(*minor, currency)
}

func nullableText(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// instant returns a stored instant in UTC; NULL is the zero time.
func instant(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
```

`internal/adapters/postgres/transaction_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type transactionRepo struct{ q querier }

var txColumnList = []string{
	"id", "origin", "kind", "status", "wallet_id", "player_id", "amount_minor", "currency",
	"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id",
	"reference_external_transaction_id", "received_via", "reference_transaction_id", "failure_code",
	"result_balance_minor", "attempts", "next_attempt_at", "expires_at", "correlation_id",
	"created_at", "updated_at", "completed_at",
}

var (
	txColumns = strings.Join(txColumnList, ", ")
	// txSelect also reads the wallet's currency: result_balance_minor is a
	// wallet balance, and in a CURRENCY_MISMATCH rejection its currency differs
	// from the operation's.
	txSelect = `SELECT t.` + strings.Join(txColumnList, ", t.") + `, w.currency`
	txFrom   = ` FROM wager_transactions t JOIN wallets w ON w.id = t.wallet_id`
)

func (r transactionRepo) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx, `INSERT INTO wager_transactions (`+txColumns+`) VALUES
		($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID,
		s.Money.Minor(), string(s.Money.Currency()),
		nullableText(s.ProviderID), nullableText(s.ExternalTransactionID), nullableText(s.IdempotencyKey),
		nullableText(s.PayloadHash), nullableText(s.RoundID), nullableText(s.GameID),
		nullableText(s.ReferenceExternalTransactionID), nullableText(string(s.ReceivedVia)),
		nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.CorrelationID, s.CreatedAt, s.UpdatedAt, nullableTime(s.CompletedAt))
	return translate(err)
}

// Update writes only the state columns; the wager_tx_guard trigger refuses
// terminal rows and immutable columns (PDA02).
func (r transactionRepo) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions SET
		status = $2, reference_transaction_id = $3, failure_code = $4, result_balance_minor = $5,
		attempts = $6, next_attempt_at = $7, expires_at = $8, updated_at = $9, completed_at = $10
		WHERE id = $1`,
		s.ID, string(s.Status), nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.UpdatedAt, nullableTime(s.CompletedAt))
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}

func (r transactionRepo) Get(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

// scanTransaction reads the txSelect columns of one row, plus extra
// destinations, and rehydrates it. pgx.ErrNoRows passes untouched for the
// callers to map.
func scanTransaction(row pgx.Row, extra ...any) (*wagering.WagerTransaction, error) {
	var (
		s                                                           wagering.Snapshot
		origin, kind, status, currency, walletCurrency              string
		amount                                                      int64
		provider, external, key, hash, round, game, ref, via, refTx *string
		failure                                                     *string
		result                                                      *int64
		next, expires, completed                                    *time.Time
	)
	dest := append([]any{
		&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &amount, &currency,
		&provider, &external, &key, &hash, &round, &game, &ref, &via, &refTx, &failure,
		&result, &s.Attempts, &next, &expires, &s.CorrelationID, &s.CreatedAt, &s.UpdatedAt, &completed,
		&walletCurrency,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, translate(err)
	}
	s.Origin, s.Kind, s.Status = wagering.Origin(origin), wagering.Kind(kind), wagering.Status(status)
	s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey = text(provider), text(external), text(key)
	s.PayloadHash, s.RoundID, s.GameID = text(hash), text(round), text(game)
	s.ReferenceExternalTransactionID, s.ReceivedVia = text(ref), wagering.ReceivedVia(text(via))
	s.ReferenceTransactionID, s.FailureCode = text(refTx), wagering.FailureCode(text(failure))
	s.NextAttemptAt, s.ExpiresAt, s.CompletedAt = instant(next), instant(expires), instant(completed)
	var err error
	if s.Money, err = toMoney(amount, currency); err != nil {
		return nil, corrupted(err)
	}
	if s.ResultBalance, err = optionalMoney(result, walletCurrency); err != nil {
		return nil, corrupted(err)
	}
	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, corrupted(err)
	}
	return t, nil
}
```

`internal/adapters/postgres/wallet_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type walletRepo struct{ q querier }

const walletColumns = `id, player_id, currency, balance_minor, version, created_at, updated_at`

func (r walletRepo) Insert(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, string(s.Balance.Currency()), s.Balance.Minor(), s.Version, s.CreatedAt, s.UpdatedAt)
	return translate(err)
}

// Lock takes the wallet's row lock until the end of the transaction (D-09).
// The lock_timeout set by the unit of work bounds the wait.
func (r walletRepo) Lock(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1 FOR UPDATE`, id)
}

func (r walletRepo) Get(ctx context.Context, id string) (wallet.Wallet, error) {
	return r.get(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`, id)
}

func (r walletRepo) get(ctx context.Context, sql, id string) (wallet.Wallet, error) {
	if !ident.Valid(id) { // not a canonical UUID: no such row, and no 22P02 from the database
		return wallet.Wallet{}, notFound()
	}
	var s wallet.Snapshot
	var currency string
	var minor int64
	err := r.q.QueryRow(ctx, sql, id).Scan(&s.ID, &s.PlayerID, &currency, &minor, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return wallet.Wallet{}, notFound()
	}
	if err != nil {
		return wallet.Wallet{}, translate(err)
	}
	if s.Balance, err = toMoney(minor, currency); err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		return wallet.Wallet{}, corrupted(err)
	}
	return w, nil
}

// UpdateBalance relies on the wallet lock; the version condition is the
// second protection against a lost update (D-09), and the wallet_guard trigger
// the third.
func (r walletRepo) UpdateBalance(ctx context.Context, w wallet.Wallet) error {
	s, err := w.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx,
		`UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4 WHERE id = $1 AND version = $3 - 1`,
		s.ID, s.Balance.Minor(), s.Version, s.UpdatedAt)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 9: Consultas: idempotência, referência, antecipação e ledger (I18)

Fecha as portas da spec §3. `FindReference` traz `AlreadyReversed` na mesma consulta; `AdvanceDependents` usa `GREATEST($now, created_at)` (decisão 7). O `processWith` dos testes passa a resolver a referência e a antecipar as pendências, como o caso de uso.

**Arquivos:**
- Implementação: `internal/adapters/postgres/ledger_repo.go` (alterar), `internal/adapters/postgres/transaction_repo.go` (alterar), `internal/app/ports.go` (alterar)
- Testes e helpers de teste: `internal/adapters/postgres/domain_integration_test.go` (alterar), `internal/adapters/postgres/queries_integration_test.go`

**Interfaces:**
- Consome: tudo da Tarefa 8.
- Produz: `TransactionRepository.FindByIdempotencyKey`, `FindByExternalID`, `FindReference`, `AdvanceDependents`; `LedgerRepository.List`, `Sum`; `app.LedgerSum{NetMinor, Entries}`; nos testes: `withKey` e o `processWith` final.

- [ ] **Passo 1: criar os stubs** (para o red falhar por asserção, não por compilação)

Arquivos só com declarações entram já com o conteúdo final:

`internal/app/ports.go` (substitui a versão da Tarefa 8):

```go
// Package app holds the use cases and the ports they depend on. It knows the
// domain and the error vocabulary, never Fx, HTTP, SQS or the database driver.
package app

import (
	"context"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// UnitOfWork delimits one SQL transaction (D-14). Whoever holds the Repos is
// inside the transaction; it is never hidden in the context.
type UnitOfWork interface {
	// Do runs fn in one READ COMMITTED transaction with lock_timeout set
	// (D-09): commit when fn returns nil, rollback on an error or a panic.
	Do(ctx context.Context, fn func(Repos) error) error
	// Snapshot runs fn in a REPEATABLE READ READ ONLY transaction: one
	// consistent view, for the reconciliation (D-16).
	Snapshot(ctx context.Context, fn func(Repos) error) error
}

// Repos are the repositories bound to one transaction, or to the pool for
// reads outside a transaction.
type Repos interface {
	Wallets() WalletRepository
	Transactions() TransactionRepository
	Ledger() LedgerRepository
	Outbox() OutboxRepository
	Inbox() InboxRepository
}

// WalletRepository persists the wallet aggregate.
type WalletRepository interface {
	// Insert fails with ErrWalletAlreadyExists for a second (playerId, currency).
	Insert(ctx context.Context, w wallet.Wallet) error
	// Lock reads the wallet with SELECT … FOR UPDATE (D-09); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (wallet.Wallet, error)
	// Get reads the wallet without a lock; ErrNotFound when absent.
	Get(ctx context.Context, id string) (wallet.Wallet, error)
	// UpdateBalance writes balance, version and updatedAt of a wallet moved by
	// one version; it fails as permanent when the stored version is not the
	// previous one.
	UpdateBalance(ctx context.Context, w wallet.Wallet) error
}

// TransactionRepository persists wager transactions.
type TransactionRepository interface {
	Insert(ctx context.Context, t *wagering.WagerTransaction) error
	// Update writes the state columns of a transaction that left
	// PENDING_REFERENCE or was rescheduled.
	Update(ctx context.Context, t *wagering.WagerTransaction) error
	// Get returns ErrNotFound when absent.
	Get(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	// FindByIdempotencyKey and FindByExternalID return nil, nil when absent,
	// the shape wagering.CheckIdempotency expects.
	FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error)
	FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)
	// FindReference resolves (providerId, referenceExternalTransactionId) and
	// tells whether the reference already has a PROCESSED REFUND or ROLLBACK;
	// Reference{} when absent.
	FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error)
	// AdvanceDependents makes the PENDING_REFERENCE operations waiting for
	// (providerID, externalID) due at now and returns how many there were.
	AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error)
}

// LedgerRepository appends and reads ledger entries.
type LedgerRepository interface {
	Insert(ctx context.Context, e wallet.LedgerEntry) error
	// List returns up to limit entries with wallet_version > afterVersion,
	// ordered by version (HTTP-03).
	List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
	// Sum rebuilds the balance from the ledger (D-16).
	Sum(ctx context.Context, walletID string) (LedgerSum, error)
}

// LedgerSum is the balance rebuilt from the ledger.
type LedgerSum struct {
	NetMinor int64 // Σ CREDIT − Σ DEBIT, in minor units
	Entries  int64
}

// OutboxRepository records the events of the transaction (D-13).
type OutboxRepository interface {
	Insert(ctx context.Context, envs ...events.Envelope) error
}

// InboxRepository deduplicates SQS messages per consumer (SQS-03).
type InboxRepository interface {
	// Find returns nil, nil when the message was never recorded.
	Find(ctx context.Context, consumer, messageID string) (*InboxMessage, error)
	// Insert fails with ErrInboxDuplicate when the message is already recorded.
	Insert(ctx context.Context, m InboxMessage) error
}

// InboxOutcome is how a message was concluded (data-model §3.4).
type InboxOutcome string

const (
	InboxProcessed        InboxOutcome = "PROCESSED"
	InboxRejected         InboxOutcome = "REJECTED"
	InboxPendingReference InboxOutcome = "PENDING_REFERENCE"
	InboxIdempotentReplay InboxOutcome = "IDEMPOTENT_REPLAY"
	InboxFailed           InboxOutcome = "FAILED"
)

// InboxMessage is one row of inbox_messages.
type InboxMessage struct {
	ConsumerName  string
	MessageID     string
	MessageHash   string
	MessageType   string
	TransactionID string // "" when the message did not reach an operation
	Outcome       InboxOutcome
	ReceivedAt    time.Time
	ProcessedAt   time.Time
}
```

Stubs com as assinaturas finais; as funções novas devolvem `errNotImplemented` e as migrations não fazem nada:

`internal/adapters/postgres/ledger_repo.go` (substitui a versão da Tarefa 8):

```go
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type ledgerRepo struct{ q querier }

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

// Insert appends an entry. The ledger_matches_wallet trigger requires the
// transaction to be written in its final state and the wallet updated first.
func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	if e.ID() == "" {
		return invalidValue(fmt.Errorf("%w: zero value", wallet.ErrInvalidLedgerEntry))
	}
	_, err := r.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Minor(),
		string(e.Amount().Currency()), e.BalanceBefore().Minor(), e.BalanceAfter().Minor(),
		e.WalletVersion(), e.CreatedAt())
	return translate(err)
}

var errNotImplemented = errors.New("postgres: not implemented")

func (r ledgerRepo) List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	return nil, errNotImplemented
}

func (r ledgerRepo) Sum(ctx context.Context, walletID string) (app.LedgerSum, error) {
	return app.LedgerSum{}, errNotImplemented
}
```

`internal/adapters/postgres/transaction_repo.go` (substitui a versão da Tarefa 8):

```go
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type transactionRepo struct{ q querier }

var txColumnList = []string{
	"id", "origin", "kind", "status", "wallet_id", "player_id", "amount_minor", "currency",
	"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id",
	"reference_external_transaction_id", "received_via", "reference_transaction_id", "failure_code",
	"result_balance_minor", "attempts", "next_attempt_at", "expires_at", "correlation_id",
	"created_at", "updated_at", "completed_at",
}

var (
	txColumns = strings.Join(txColumnList, ", ")
	// txSelect also reads the wallet's currency: result_balance_minor is a
	// wallet balance, and in a CURRENCY_MISMATCH rejection its currency differs
	// from the operation's.
	txSelect = `SELECT t.` + strings.Join(txColumnList, ", t.") + `, w.currency`
	txFrom   = ` FROM wager_transactions t JOIN wallets w ON w.id = t.wallet_id`
)

func (r transactionRepo) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx, `INSERT INTO wager_transactions (`+txColumns+`) VALUES
		($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID,
		s.Money.Minor(), string(s.Money.Currency()),
		nullableText(s.ProviderID), nullableText(s.ExternalTransactionID), nullableText(s.IdempotencyKey),
		nullableText(s.PayloadHash), nullableText(s.RoundID), nullableText(s.GameID),
		nullableText(s.ReferenceExternalTransactionID), nullableText(string(s.ReceivedVia)),
		nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.CorrelationID, s.CreatedAt, s.UpdatedAt, nullableTime(s.CompletedAt))
	return translate(err)
}

// Update writes only the state columns; the wager_tx_guard trigger refuses
// terminal rows and immutable columns (PDA02).
func (r transactionRepo) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions SET
		status = $2, reference_transaction_id = $3, failure_code = $4, result_balance_minor = $5,
		attempts = $6, next_attempt_at = $7, expires_at = $8, updated_at = $9, completed_at = $10
		WHERE id = $1`,
		s.ID, string(s.Status), nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.UpdatedAt, nullableTime(s.CompletedAt))
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}

func (r transactionRepo) Get(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

// scanTransaction reads the txSelect columns of one row, plus extra
// destinations, and rehydrates it. pgx.ErrNoRows passes untouched for the
// callers to map.
func scanTransaction(row pgx.Row, extra ...any) (*wagering.WagerTransaction, error) {
	var (
		s                                                           wagering.Snapshot
		origin, kind, status, currency, walletCurrency              string
		amount                                                      int64
		provider, external, key, hash, round, game, ref, via, refTx *string
		failure                                                     *string
		result                                                      *int64
		next, expires, completed                                    *time.Time
	)
	dest := append([]any{
		&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &amount, &currency,
		&provider, &external, &key, &hash, &round, &game, &ref, &via, &refTx, &failure,
		&result, &s.Attempts, &next, &expires, &s.CorrelationID, &s.CreatedAt, &s.UpdatedAt, &completed,
		&walletCurrency,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, translate(err)
	}
	s.Origin, s.Kind, s.Status = wagering.Origin(origin), wagering.Kind(kind), wagering.Status(status)
	s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey = text(provider), text(external), text(key)
	s.PayloadHash, s.RoundID, s.GameID = text(hash), text(round), text(game)
	s.ReferenceExternalTransactionID, s.ReceivedVia = text(ref), wagering.ReceivedVia(text(via))
	s.ReferenceTransactionID, s.FailureCode = text(refTx), wagering.FailureCode(text(failure))
	s.NextAttemptAt, s.ExpiresAt, s.CompletedAt = instant(next), instant(expires), instant(completed)
	var err error
	if s.Money, err = toMoney(amount, currency); err != nil {
		return nil, corrupted(err)
	}
	if s.ResultBalance, err = optionalMoney(result, walletCurrency); err != nil {
		return nil, corrupted(err)
	}
	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, corrupted(err)
	}
	return t, nil
}

func (r transactionRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error) {
	return nil, errNotImplemented
}

func (r transactionRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	return nil, errNotImplemented
}

func (r transactionRepo) FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error) {
	return wagering.Reference{}, errNotImplemented
}

func (r transactionRepo) AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error) {
	return 0, errNotImplemented
}
```

- [ ] **Passo 2: escrever os testes que falham**

`internal/adapters/postgres/domain_integration_test.go` (substitui a versão da Tarefa 8):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func brl(tb testing.TB, amount string) money.Money {
	tb.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		tb.Fatalf("money %s: %v", amount, err)
	}
	return m
}

// newUoW is the unit of work over the package database, as the app role.
func newUoW() *postgres.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

// zeroWallet is a new wallet with a zero opening balance: no OPENING, no entry.
func zeroWallet(tb testing.TB) wallet.Wallet {
	tb.Helper()
	w, err := wallet.Open(newID(), newID(), brl(tb, "0.00"), time.Now())
	if err != nil {
		tb.Fatalf("wallet.Open: %v", err)
	}
	return w
}

func insertWallet(tb testing.TB, w wallet.Wallet) {
	tb.Helper()
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return r.Wallets().Insert(tb.Context(), w) }); err != nil {
		tb.Fatalf("insert wallet: %v", err)
	}
}

func walletSnapshot(tb testing.TB, w wallet.Wallet) wallet.Snapshot {
	tb.Helper()
	s, err := w.Snapshot()
	if err != nil {
		tb.Fatalf("wallet snapshot: %v", err)
	}
	return s
}

// wantKind fails unless err classifies as kind and, when target is not nil,
// wraps target.
func wantKind(tb testing.TB, err error, kind apperrors.Kind, target error) {
	tb.Helper()
	if got := apperrors.Classify(err); got != kind {
		tb.Fatalf("Classify(%v) = %q, want %q", err, got, kind)
	}
	if target != nil && !errors.Is(err, target) {
		tb.Fatalf("errors.Is(%v, %v) = false", err, target)
	}
}

// lockWallet holds the wallet's row lock in another transaction until the test
// ends or release is called.
func lockWallet(t *testing.T, walletID string) (release func()) {
	t.Helper()
	tx, err := env.Owner.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, walletID); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}
	release = func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }
	t.Cleanup(release)
	return release
}

// policy is the reference schedule of the integration tests (test-plan §3.3).
var policy = func() wagering.ReferenceRetryPolicy {
	p, err := wagering.NewReferenceRetryPolicy(100*time.Millisecond, time.Second, 3, time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return p
}()

// newProvider isolates the external ids of a test from the parallel ones.
func newProvider() string { return "provider-" + newID() }

func ptr(s string) *string { return &s }

// command validates an operation on w. ref "" = no reference; the currency is
// BRL unless the amount says otherwise ("10.00 USD").
func command(tb testing.TB, w wallet.Wallet, provider string, kind wagering.Kind, amount, ext, ref string) wagering.Command {
	tb.Helper()
	currency := "BRL"
	if a, c, ok := strings.Cut(amount, " "); ok {
		amount, currency = a, c
	}
	in := wagering.Input{
		IdempotencyKey: ptr(provider + ":" + ext), ProviderID: ptr(provider), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(string(kind)), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		tb.Fatalf("NewCommand: %v", err)
	}
	return cmd
}

// seal wraps the domain events in envelopes, as the app does before the INSERT.
func seal(evs []events.Event, correlationID string) ([]events.Envelope, error) {
	out := make([]events.Envelope, 0, len(evs))
	for _, e := range evs {
		env, err := events.Seal(newID(), correlationID, "", e)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// persistOpening writes a wallet opening like POST /wallets (lifecycle §6.4).
func persistOpening(ctx context.Context, r app.Repos, o wagering.Opening) error {
	if err := r.Wallets().Insert(ctx, o.Wallet); err != nil {
		return err
	}
	if o.Tx == nil {
		return nil
	}
	if err := r.Transactions().Insert(ctx, o.Tx); err != nil {
		return err
	}
	if err := r.Ledger().Insert(ctx, *o.Entry); err != nil {
		return err
	}
	envs, err := seal(o.Events, o.Tx.CorrelationID())
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// openWallet opens and persists a wallet with the initial balance.
func openWallet(tb testing.TB, initial string) (wallet.Wallet, wagering.Opening) {
	tb.Helper()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: newID(), PlayerID: newID(), Initial: brl(tb, initial),
		TransactionID: newID(), EntryID: newID(), CorrelationID: "corr-open", Now: time.Now(),
	})
	if err != nil {
		tb.Fatalf("OpenWallet: %v", err)
	}
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return persistOpening(tb.Context(), r, o) }); err != nil {
		tb.Fatalf("persist opening: %v", err)
	}
	return o.Wallet, o
}

// persistOutcome writes what Settle decided, in the order the triggers
// require: the operation is already written; then the wallet, the entry and
// the events (data-model §4.2).
func persistOutcome(ctx context.Context, r app.Repos, w wallet.Wallet, out wagering.Outcome, correlationID string) error {
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	envs, err := seal(out.Events, correlationID)
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// processWith runs the lifecycle §6.1 pipeline of a new operation the way the
// M3 use case will: lock the wallet, resolve the reference, Settle, write.
// wrap decorates the repositories (nil = none).
func processWith(ctx context.Context, uow app.UnitOfWork, cmd wagering.Command, now time.Time, wrap func(app.Repos) app.Repos) (*wagering.WagerTransaction, error) {
	var tx *wagering.WagerTransaction
	err := uow.Do(ctx, func(r app.Repos) error {
		if wrap != nil {
			r = wrap(r)
		}
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if err != nil {
			return err
		}
		var ref wagering.Reference
		if cmd.ReferenceExternalTransactionID() != "" {
			if ref, err = r.Transactions().FindReference(ctx, cmd.ProviderID(), cmd.ReferenceExternalTransactionID()); err != nil {
				return err
			}
		}
		if tx, err = wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr-"+cmd.ExternalTransactionID(), now); err != nil {
			return err
		}
		out, err := wagering.Settle(tx, &w, ref, wagering.SettleParams{EntryID: newID(), Now: now, Policy: policy})
		if err != nil {
			return err
		}
		if err := r.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		if err := persistOutcome(ctx, r, w, out, tx.CorrelationID()); err != nil {
			return err
		}
		if tx.Status().IsTerminal() {
			_, err = r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now)
		}
		return err
	})
	return tx, err
}

// process runs processWith now and fails the test on error.
func process(tb testing.TB, cmd wagering.Command) *wagering.WagerTransaction {
	tb.Helper()
	tx, err := processWith(tb.Context(), newUoW(), cmd, time.Now(), nil)
	if err != nil {
		tb.Fatalf("process %s %s: %v", cmd.Kind(), cmd.ExternalTransactionID(), err)
	}
	return tx
}

func txSnapshot(tb testing.TB, tx *wagering.WagerTransaction) wagering.Snapshot {
	tb.Helper()
	s, err := tx.Snapshot()
	if err != nil {
		tb.Fatalf("transaction snapshot: %v", err)
	}
	return s
}

// wantStored fails unless the stored operation equals tx.
func wantStored(tb testing.TB, tx *wagering.WagerTransaction) {
	tb.Helper()
	got, err := reads().Transactions().Get(tb.Context(), tx.ID())
	if err != nil {
		tb.Fatalf("Get %s: %v", tx.ID(), err)
	}
	if g, w := txSnapshot(tb, got), txSnapshot(tb, tx); g != w {
		tb.Fatalf("stored operation differs:\n got  %+v\n want %+v", g, w)
	}
}
```

`internal/adapters/postgres/queries_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: IDEM-02, IDEM-05, IDEM-06, IDEM-07, OPS-06, OPS-08, OPS-12, E5, E6 (I18: transaction queries)
func TestTransactionQueries(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	bet := process(t, command(t, w, p, wagering.KindBet, "20.00", "bet-1", ""))

	t.Run("idempotency lookups", func(t *testing.T) {
		ctx := t.Context()
		byKey, err := reads().Transactions().FindByIdempotencyKey(ctx, p, bet.IdempotencyKey())
		if err != nil || byKey == nil || txSnapshot(t, byKey) != txSnapshot(t, bet) {
			t.Fatalf("FindByIdempotencyKey = %v, %v; want the BET", byKey, err)
		}
		byExt, err := reads().Transactions().FindByExternalID(ctx, p, "bet-1")
		if err != nil || byExt == nil || byExt.ID() != bet.ID() {
			t.Fatalf("FindByExternalID = %v, %v; want the BET", byExt, err)
		}
		for name, find := range map[string]func() (*wagering.WagerTransaction, error){
			"unknown key": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByIdempotencyKey(ctx, p, "unknown")
			},
			"key of another provider": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByIdempotencyKey(ctx, newProvider(), bet.IdempotencyKey())
			},
			"unknown external id": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByExternalID(ctx, p, "unknown")
			},
			"external id of another provider": func() (*wagering.WagerTransaction, error) {
				return reads().Transactions().FindByExternalID(ctx, newProvider(), "bet-1")
			},
		} {
			if got, err := find(); got != nil || err != nil {
				t.Errorf("%s: %v, %v; want nil, nil", name, got, err)
			}
		}
	})

	t.Run("concurrent insert of the same operation", func(t *testing.T) {
		ctx := t.Context()
		sameKey := command(t, w, p, wagering.KindBet, "20.00", "bet-1", "")
		otherKey := command(t, w, p, wagering.KindBet, "20.00", "bet-1", "")
		for name, cmd := range map[string]wagering.Command{"same key": sameKey, "same external id": otherKey} {
			if name == "same external id" {
				cmd = withKey(t, cmd, "another-key-"+newID())
			}
			tx, err := wagering.NewExternal(newID(), cmd, wagering.ReceivedViaSQS, "corr", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Fail(time.Now()); err != nil { // any persistable state; the unique index fires first
				t.Fatal(err)
			}
			err = newUoW().Do(ctx, func(r app.Repos) error { return r.Transactions().Insert(ctx, tx) })
			wantKind(t, err, apperrors.KindTransient, app.ErrIdempotencyRace)
		}
	})

	t.Run("reference resolution", func(t *testing.T) {
		ctx := t.Context()
		ref, err := reads().Transactions().FindReference(ctx, p, "bet-missing")
		if err != nil || ref.Tx != nil || ref.AlreadyReversed {
			t.Fatalf("FindReference(missing) = %+v, %v; want Reference{}", ref, err)
		}
		ref, err = reads().Transactions().FindReference(ctx, p, "bet-1")
		if err != nil || ref.Tx == nil || ref.Tx.ID() != bet.ID() || ref.AlreadyReversed {
			t.Fatalf("FindReference(bet-1) = %+v, %v; want the BET, not reversed", ref, err)
		}
		refund := process(t, command(t, w, p, wagering.KindRefund, "20.00", "refund-1", "bet-1"))
		if refund.Status() != wagering.StatusProcessed {
			t.Fatalf("refund %s %s, want PROCESSED", refund.Status(), refund.FailureCode())
		}
		ref, err = reads().Transactions().FindReference(ctx, p, "bet-1")
		if err != nil || !ref.AlreadyReversed {
			t.Fatalf("FindReference(bet-1) after the REFUND = %+v, %v; want AlreadyReversed", ref, err)
		}
	})

	t.Run("concurrent reversal of the same reference", func(t *testing.T) {
		ctx := t.Context()
		// A second REFUND settled on a stale read (AlreadyReversed = false), as
		// a racing transaction would without the wallet lock: the partial
		// unique index stops it (D-10).
		err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err := r.Wallets().Lock(ctx, w.ID())
			if err != nil {
				return err
			}
			tx, err := wagering.NewExternal(newID(), command(t, w, p, wagering.KindRefund, "20.00", "refund-2", "bet-1"), wagering.ReceivedViaHTTP, "corr", time.Now())
			if err != nil {
				return err
			}
			out, err := wagering.Settle(tx, &locked, wagering.Reference{Tx: bet}, wagering.SettleParams{EntryID: newID(), Now: time.Now(), Policy: policy})
			if err != nil {
				return err
			}
			if err := r.Transactions().Insert(ctx, tx); err != nil {
				return err
			}
			return persistOutcome(ctx, r, locked, out, "corr")
		})
		wantKind(t, err, apperrors.KindTransient, app.ErrReversalRace)
	})

	t.Run("dependents are advanced with the creation floor", func(t *testing.T) {
		ctx := t.Context()
		// The pending REFUND was created by an instance whose clock is one hour
		// ahead; this instance advances it at its own now.
		ahead := time.Now().Add(time.Hour)
		pending, err := processWith(ctx, newUoW(), command(t, w, p, wagering.KindRefund, "5.00", "refund-3", "bet-late"), ahead, nil)
		if err != nil || pending.Status() != wagering.StatusPendingReference {
			t.Fatalf("pending refund: %v, %v", pending, err)
		}
		now := time.Now()
		var n int64
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			var err error
			n, err = r.Transactions().AdvanceDependents(ctx, p, "bet-late", now)
			return err
		}); err != nil || n != 1 {
			t.Fatalf("AdvanceDependents = %d, %v; want 1", n, err)
		}
		got, err := reads().Transactions().Get(ctx, pending.ID())
		if err != nil {
			t.Fatalf("Get after advancing: %v", err)
		}
		if want := now.UTC().Truncate(time.Microsecond); !got.NextAttemptAt().Equal(want) || !got.UpdatedAt().Equal(got.CreatedAt()) {
			t.Fatalf("next %v updated %v created %v; want next = %v and updated = created", got.NextAttemptAt(), got.UpdatedAt(), got.CreatedAt(), want)
		}
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			var err error
			n, err = r.Transactions().AdvanceDependents(ctx, newProvider(), "bet-late", now)
			return err
		}); err != nil || n != 0 {
			t.Fatalf("AdvanceDependents(another provider) = %d, %v; want 0", n, err)
		}
	})
}

// withKey rebuilds cmd with another idempotency key.
func withKey(t *testing.T, cmd wagering.Command, key string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr(key), ProviderID: ptr(cmd.ProviderID()), ExternalTransactionID: ptr(cmd.ExternalTransactionID()),
		PlayerID: ptr(cmd.PlayerID()), WalletID: ptr(cmd.WalletID()), RoundID: ptr(cmd.RoundID()), GameID: ptr(cmd.GameID()),
		Kind: ptr(string(cmd.Kind())), Money: &wagering.MoneyInput{Amount: ptr(cmd.Money().String()), Currency: ptr(string(cmd.Money().Currency()))},
	}
	out, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand: %v", err)
	}
	return out
}

// Covers: HTTP-03, HTTP-07, LED-01 (I18: ledger reads; D-16)
func TestLedgerQueries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	for i, amount := range []string{"1.00", "2.00", "3.00", "4.00", "5.00"} {
		process(t, command(t, w, p, wagering.KindBet, amount, "bet-"+string(rune('a'+i)), ""))
	}
	testkit.AssertLedgerConsistent(t, env.Owner, w.ID())

	var versions []int64
	after := int64(0)
	for {
		page, err := reads().Ledger().List(ctx, w.ID(), after, 2)
		if err != nil {
			t.Fatalf("List(after %d): %v", after, err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if e.WalletID() != w.ID() {
				t.Fatalf("entry of wallet %s in the page of %s", e.WalletID(), w.ID())
			}
			versions = append(versions, e.WalletVersion())
		}
		after = page[len(page)-1].WalletVersion()
	}
	if len(versions) != 6 || versions[0] != 1 || versions[5] != 6 {
		t.Fatalf("paged versions = %v, want 1..6 without gaps or repeats", versions)
	}

	sum, err := reads().Ledger().Sum(ctx, w.ID())
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum.NetMinor != 8500 || sum.Entries != 6 {
		t.Fatalf("Sum = %+v, want 85.00 over 6 entries", sum)
	}
	if empty, err := reads().Ledger().Sum(ctx, newID()); err != nil || empty != (app.LedgerSum{}) {
		t.Fatalf("Sum(unknown wallet) = %+v, %v; want zero", empty, err)
	}
	_, err = reads().Ledger().List(ctx, w.ID(), 0, 0)
	wantKind(t, err, apperrors.KindPermanent, nil)
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: FAIL. Falham `TestTransactionQueries` e `TestLedgerQueries` (`process BET bet-a: postgres: not implemented`) e também `TestTransactionRepository` da Tarefa 8: o `processWith` novo chama `AdvanceDependents` e `FindReference`, que ainda são stub. Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/ledger_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

type ledgerRepo struct{ q querier }

const ledgerColumns = `id, wallet_id, transaction_id, direction, amount_minor, currency,
	balance_before_minor, balance_after_minor, wallet_version, created_at`

var errInvalidLimit = errors.New("postgres: ledger page limit must be positive")

// Insert appends an entry. The ledger_matches_wallet trigger requires the
// transaction to be written in its final state and the wallet updated first.
func (r ledgerRepo) Insert(ctx context.Context, e wallet.LedgerEntry) error {
	if e.ID() == "" {
		return invalidValue(fmt.Errorf("%w: zero value", wallet.ErrInvalidLedgerEntry))
	}
	_, err := r.q.Exec(ctx, `INSERT INTO wallet_ledger_entries (`+ledgerColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID(), e.WalletID(), e.TransactionID(), string(e.Direction()), e.Amount().Minor(),
		string(e.Amount().Currency()), e.BalanceBefore().Minor(), e.BalanceAfter().Minor(),
		e.WalletVersion(), e.CreatedAt())
	return translate(err)
}

// List pages by wallet_version, the stable order of the ledger (D-16).
func (r ledgerRepo) List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error) {
	if limit < 1 {
		return nil, invalidValue(errInvalidLimit)
	}
	rows, err := r.q.Query(ctx, `SELECT `+ledgerColumns+` FROM wallet_ledger_entries
		WHERE wallet_id = $1 AND wallet_version > $2 ORDER BY wallet_version LIMIT $3`,
		walletID, afterVersion, limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []wallet.LedgerEntry
	for rows.Next() {
		var (
			p                     wallet.LedgerEntryParams
			direction, currency   string
			amount, before, after int64
		)
		if err := rows.Scan(&p.ID, &p.WalletID, &p.TransactionID, &direction, &amount, &currency,
			&before, &after, &p.WalletVersion, &p.CreatedAt); err != nil {
			return nil, translate(err)
		}
		e, err := ledgerEntry(p, direction, currency, amount, before, after)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

func ledgerEntry(p wallet.LedgerEntryParams, direction, currency string, amount, before, after int64) (wallet.LedgerEntry, error) {
	var err error
	p.Direction = wallet.Direction(direction)
	if p.Amount, err = toMoney(amount, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	if p.BalanceBefore, err = toMoney(before, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	if p.BalanceAfter, err = toMoney(after, currency); err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	p.CreatedAt = p.CreatedAt.UTC()
	e, err := wallet.NewLedgerEntry(p)
	if err != nil {
		return wallet.LedgerEntry{}, corrupted(err)
	}
	return e, nil
}

// Sum rebuilds the balance from the ledger (D-16). SUM(bigint) is numeric in
// PostgreSQL; the cast back to bigint raises 22003 (permanent) on overflow.
func (r ledgerRepo) Sum(ctx context.Context, walletID string) (app.LedgerSum, error) {
	var s app.LedgerSum
	err := r.q.QueryRow(ctx, `SELECT
		COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint,
		COUNT(*)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&s.NetMinor, &s.Entries)
	if err != nil {
		return app.LedgerSum{}, translate(err)
	}
	return s, nil
}
```

`internal/adapters/postgres/transaction_repo.go` (substitui o stub do Passo 1):

```go
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type transactionRepo struct{ q querier }

var txColumnList = []string{
	"id", "origin", "kind", "status", "wallet_id", "player_id", "amount_minor", "currency",
	"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id",
	"reference_external_transaction_id", "received_via", "reference_transaction_id", "failure_code",
	"result_balance_minor", "attempts", "next_attempt_at", "expires_at", "correlation_id",
	"created_at", "updated_at", "completed_at",
}

var (
	txColumns = strings.Join(txColumnList, ", ")
	// txSelect also reads the wallet's currency: result_balance_minor is a
	// wallet balance, and in a CURRENCY_MISMATCH rejection its currency differs
	// from the operation's.
	txSelect = `SELECT t.` + strings.Join(txColumnList, ", t.") + `, w.currency`
	txFrom   = ` FROM wager_transactions t JOIN wallets w ON w.id = t.wallet_id`
)

func (r transactionRepo) Insert(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	_, err = r.q.Exec(ctx, `INSERT INTO wager_transactions (`+txColumns+`) VALUES
		($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26)`,
		s.ID, string(s.Origin), string(s.Kind), string(s.Status), s.WalletID, s.PlayerID,
		s.Money.Minor(), string(s.Money.Currency()),
		nullableText(s.ProviderID), nullableText(s.ExternalTransactionID), nullableText(s.IdempotencyKey),
		nullableText(s.PayloadHash), nullableText(s.RoundID), nullableText(s.GameID),
		nullableText(s.ReferenceExternalTransactionID), nullableText(string(s.ReceivedVia)),
		nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.CorrelationID, s.CreatedAt, s.UpdatedAt, nullableTime(s.CompletedAt))
	return translate(err)
}

// Update writes only the state columns; the wager_tx_guard trigger refuses
// terminal rows and immutable columns (PDA02).
func (r transactionRepo) Update(ctx context.Context, t *wagering.WagerTransaction) error {
	s, err := t.Snapshot()
	if err != nil {
		return invalidValue(err)
	}
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions SET
		status = $2, reference_transaction_id = $3, failure_code = $4, result_balance_minor = $5,
		attempts = $6, next_attempt_at = $7, expires_at = $8, updated_at = $9, completed_at = $10
		WHERE id = $1`,
		s.ID, string(s.Status), nullableText(s.ReferenceTransactionID), nullableText(string(s.FailureCode)),
		nullableMinor(s.ResultBalance), s.Attempts, nullableTime(s.NextAttemptAt), nullableTime(s.ExpiresAt),
		s.UpdatedAt, nullableTime(s.CompletedAt))
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() != 1 {
		return staleRow()
	}
	return nil
}

func (r transactionRepo) Get(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

func (r transactionRepo) FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error) {
	return r.find(ctx, txSelect+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.idempotency_key = $2`, providerID, key)
}

func (r transactionRepo) FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error) {
	return r.find(ctx, txSelect+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.external_transaction_id = $2`, providerID, externalID)
}

// find returns nil, nil when no row matches.
func (r transactionRepo) find(ctx context.Context, sql string, args ...any) (*wagering.WagerTransaction, error) {
	t, err := scanTransaction(r.q.QueryRow(ctx, sql, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// FindReference reads the reference and whether it already has a PROCESSED
// REFUND or ROLLBACK (R7, D-10) in one statement, under the caller's wallet lock.
func (r transactionRepo) FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error) {
	var reversed bool
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+`,
		EXISTS (SELECT 1 FROM wager_transactions c
		        WHERE c.reference_transaction_id = t.id
		          AND c.kind IN ('REFUND','ROLLBACK') AND c.status = 'PROCESSED')`+txFrom+`
		WHERE t.origin = 'EXTERNAL' AND t.provider_id = $1 AND t.external_transaction_id = $2`,
		providerID, referenceExternalID), &reversed)
	if errors.Is(err, pgx.ErrNoRows) {
		return wagering.Reference{}, nil
	}
	if err != nil {
		return wagering.Reference{}, err
	}
	return wagering.Reference{Tx: t, AlreadyReversed: reversed}, nil
}

// AdvanceDependents keeps updated_at >= created_at even when this instance's
// clock lags behind the one that created the pending operation (M1 spec §2,
// decision 10).
func (r transactionRepo) AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error) {
	tag, err := r.q.Exec(ctx, `UPDATE wager_transactions
		SET next_attempt_at = $3, updated_at = GREATEST($3, created_at)
		WHERE status = 'PENDING_REFERENCE' AND provider_id = $1 AND reference_external_transaction_id = $2`,
		providerID, externalID, now.UTC().Truncate(time.Microsecond))
	if err != nil {
		return 0, translate(err)
	}
	return tag.RowsAffected(), nil
}

// scanTransaction reads the txSelect columns of one row, plus extra
// destinations, and rehydrates it. pgx.ErrNoRows passes untouched for the
// callers to map.
func scanTransaction(row pgx.Row, extra ...any) (*wagering.WagerTransaction, error) {
	var (
		s                                                           wagering.Snapshot
		origin, kind, status, currency, walletCurrency              string
		amount                                                      int64
		provider, external, key, hash, round, game, ref, via, refTx *string
		failure                                                     *string
		result                                                      *int64
		next, expires, completed                                    *time.Time
	)
	dest := append([]any{
		&s.ID, &origin, &kind, &status, &s.WalletID, &s.PlayerID, &amount, &currency,
		&provider, &external, &key, &hash, &round, &game, &ref, &via, &refTx, &failure,
		&result, &s.Attempts, &next, &expires, &s.CorrelationID, &s.CreatedAt, &s.UpdatedAt, &completed,
		&walletCurrency,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, translate(err)
	}
	s.Origin, s.Kind, s.Status = wagering.Origin(origin), wagering.Kind(kind), wagering.Status(status)
	s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey = text(provider), text(external), text(key)
	s.PayloadHash, s.RoundID, s.GameID = text(hash), text(round), text(game)
	s.ReferenceExternalTransactionID, s.ReceivedVia = text(ref), wagering.ReceivedVia(text(via))
	s.ReferenceTransactionID, s.FailureCode = text(refTx), wagering.FailureCode(text(failure))
	s.NextAttemptAt, s.ExpiresAt, s.CompletedAt = instant(next), instant(expires), instant(completed)
	var err error
	if s.Money, err = toMoney(amount, currency); err != nil {
		return nil, corrupted(err)
	}
	if s.ResultBalance, err = optionalMoney(result, walletCurrency); err != nil {
		return nil, corrupted(err)
	}
	t, err := wagering.Rehydrate(s)
	if err != nil {
		return nil, corrupted(err)
	}
	return t, nil
}
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 10: fluxos do domínio no banco (I17), atomicidade (I03a) e falha permanente (I03b)

Só testes: o comportamento já existe desde a Tarefa 9, então eles passam de primeira. O red é a **checagem de sensibilidade** ([`development-workflow.md`](../../development-workflow.md) §4.3), com três sabotagens. O I17 é a prova de que o domínio do M1 e o schema concordam em todos os tipos (spec §8).

**Arquivos:**
- Testes: `internal/adapters/postgres/flows_integration_test.go` (novo)

**Interfaces:**
- Consome: `process`, `processWith`, `openWallet`, `persistOutcome`, `command`, `wantStored` (Tarefas 8 e 9); `testkit.LedgerProblems`, `AssertLedgerConsistent` (Tarefa 8); `isolatedDB`, `attemptCommit` (Tarefas 3 e 4).
- Produz: `resume` (o passo do worker de referências, lifecycle §6.3), `forcedRepos`/`failOutbox` (dublê pontual de falha, test-plan §1).

- [ ] **Passo 1: escrever os testes**

`internal/adapters/postgres/flows_integration_test.go` (novo):

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/test/testkit"
)

// resume re-evaluates a PENDING_REFERENCE operation like the reference worker
// (lifecycle §6.3): lock the wallet first, then read the operation again.
func resume(tb testing.TB, txID string) *wagering.WagerTransaction {
	tb.Helper()
	ctx := tb.Context()
	var tx *wagering.WagerTransaction
	err := newUoW().Do(ctx, func(r app.Repos) error {
		pending, err := r.Transactions().Get(ctx, txID)
		if err != nil {
			return err
		}
		w, err := r.Wallets().Lock(ctx, pending.WalletID())
		if err != nil {
			return err
		}
		if tx, err = r.Transactions().Get(ctx, txID); err != nil {
			return err
		}
		ref, err := r.Transactions().FindReference(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
		if err != nil {
			return err
		}
		out, err := wagering.Settle(tx, &w, ref, wagering.SettleParams{EntryID: newID(), Now: time.Now(), Policy: policy})
		if err != nil {
			return err
		}
		if err := r.Transactions().Update(ctx, tx); err != nil {
			return err
		}
		return persistOutcome(ctx, r, w, out, tx.CorrelationID())
	})
	if err != nil {
		tb.Fatalf("resume %s: %v", txID, err)
	}
	return tx
}

func wantBalance(t *testing.T, walletID, amount string, version int64) {
	t.Helper()
	w, err := reads().Wallets().Get(t.Context(), walletID)
	if err != nil {
		t.Fatalf("Get wallet: %v", err)
	}
	if w.Balance() != brl(t, amount) || w.Version() != version {
		t.Fatalf("wallet = %s v%d, want %s BRL v%d", w.Balance(), w.Version(), amount, version)
	}
}

func outboxCount(t *testing.T, walletID string) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), `SELECT count(*) FROM outbox_events WHERE message_group_id = $1`, walletID).Scan(&n); err != nil {
		t.Fatalf("count outbox: %v", err)
	}
	return n
}

// Covers: WAL-06, LED-05, TX-09, OPS-01..10, OPS-12, OUT-02, E9 (I17)
//
// Every kind, written through the repositories the way the use cases will,
// passes the triggers: the domain of M1 and the schema agree.
//
// Sensitivity: FindReference with "false" in place of the EXISTS (never
// AlreadyReversed) → refund-3 fails with ErrReversalRace (23505
// wager_tx_single_reversal_uq) instead of REJECTED ALREADY_REVERSED.
func TestDomainFlowsPersist(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	steps := []struct {
		kind             wagering.Kind
		amount, ext, ref string
		status           wagering.Status
		failure          wagering.FailureCode
		balance          string
		version          int64
	}{
		{wagering.KindBet, "30.00", "bet-1", "", wagering.StatusProcessed, "", "70.00", 2},
		{wagering.KindWin, "50.00", "win-1", "bet-1", wagering.StatusProcessed, "", "120.00", 3},
		{wagering.KindLoss, "0.00", "loss-1", "", wagering.StatusProcessed, "", "120.00", 3},
		{wagering.KindRefund, "30.00", "refund-1", "bet-1", wagering.StatusProcessed, "", "150.00", 4},
		{wagering.KindRollback, "50.00", "rollback-1", "win-1", wagering.StatusProcessed, "", "100.00", 5},
		{wagering.KindBet, "500.00", "bet-2", "", wagering.StatusRejected, wagering.FailureInsufficientFunds, "100.00", 5},
		{wagering.KindRefund, "10.00", "refund-2", "bet-3", wagering.StatusPendingReference, "", "100.00", 5},
		{wagering.KindBet, "10.00", "bet-3", "", wagering.StatusProcessed, "", "90.00", 6},
	}
	byExt := map[string]*wagering.WagerTransaction{}
	for _, s := range steps {
		tx := process(t, command(t, w, p, s.kind, s.amount, s.ext, s.ref))
		if tx.Status() != s.status || tx.FailureCode() != s.failure {
			t.Fatalf("%s: %s %s, want %s %s", s.ext, tx.Status(), tx.FailureCode(), s.status, s.failure)
		}
		if s.ref != "" && s.status == wagering.StatusProcessed && tx.ReferenceTransactionID() != byExt[s.ref].ID() {
			t.Fatalf("%s: resolved reference %s, want %s", s.ext, tx.ReferenceTransactionID(), byExt[s.ref].ID())
		}
		wantStored(t, tx)
		wantBalance(t, w.ID(), s.balance, s.version)
		byExt[s.ext] = tx
	}

	// bet-3 advanced refund-2, which waited for it; the worker resolves it.
	pending, err := reads().Transactions().Get(t.Context(), byExt["refund-2"].ID())
	if err != nil {
		t.Fatal(err)
	}
	if !pending.NextAttemptAt().Before(byExt["refund-2"].NextAttemptAt()) {
		t.Fatalf("refund-2 was not advanced: next attempt %v", pending.NextAttemptAt())
	}
	if got := resume(t, pending.ID()); got.Status() != wagering.StatusProcessed || got.ReferenceTransactionID() != byExt["bet-3"].ID() {
		t.Fatalf("refund-2 after the worker: %s, reference %s", got.Status(), got.ReferenceTransactionID())
	}
	wantBalance(t, w.ID(), "100.00", 7)

	if again := process(t, command(t, w, p, wagering.KindRefund, "30.00", "refund-3", "bet-1")); again.FailureCode() != wagering.FailureAlreadyReversed {
		t.Fatalf("second reversal of bet-1: %s %s, want REJECTED ALREADY_REVERSED", again.Status(), again.FailureCode())
	}
	if rb := process(t, command(t, w, p, wagering.KindRollback, "10.00", "rollback-2", "refund-2")); rb.Status() != wagering.StatusProcessed {
		t.Fatalf("rollback of refund-2: %s %s", rb.Status(), rb.FailureCode())
	}
	wantBalance(t, w.ID(), "90.00", 8)

	// Lifecycle §7: 2 events per movement, 1 per LOSS, rejection or pending.
	if n := outboxCount(t, w.ID()); n != 20 {
		t.Fatalf("outbox has %d events for the wallet, want 20", n)
	}
}

// forcedOutbox makes Outbox().Insert fail, standing in for a failure on the
// last write of the unit of work.
type forcedOutbox struct {
	app.OutboxRepository
	err error
}

func (f forcedOutbox) Insert(context.Context, ...events.Envelope) error { return f.err }

type forcedRepos struct {
	app.Repos
	err error
}

func (f forcedRepos) Outbox() app.OutboxRepository { return forcedOutbox{f.Repos.Outbox(), f.err} }

func failOutbox(err error) func(app.Repos) app.Repos {
	return func(r app.Repos) app.Repos { return forcedRepos{r, err} }
}

// wantUntouched checks that the failed BET left nothing behind.
func wantUntouched(t *testing.T, walletID, provider, ext string) {
	t.Helper()
	ctx := t.Context()
	if tx, err := reads().Transactions().FindByExternalID(ctx, provider, ext); err != nil || tx != nil {
		t.Fatalf("operation %s persisted (found: %v, err: %v)", ext, tx != nil, err)
	}
	if sum, err := reads().Ledger().Sum(ctx, walletID); err != nil || sum.Entries != 1 {
		t.Fatalf("ledger = %+v, %v; want only the opening entry", sum, err)
	}
	if n := outboxCount(t, walletID); n != 2 {
		t.Fatalf("outbox has %d events, want only the 2 of the opening", n)
	}
	wantBalance(t, walletID, "100.00", 1)
}

// Covers: TST-I03, WAL-06, OUT-02, E5 (I03a)
// Sensitivity: UnitOfWork committing when fn fails → "operation bet-1 persisted".
func TestFinancialAtomicity(t *testing.T) {
	t.Parallel()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	forced := apperrors.New(apperrors.KindTransient, "", errors.New("forced outbox failure"))

	_, err := processWith(t.Context(), newUoW(), command(t, w, p, wagering.KindBet, "30.00", "bet-1", ""), time.Now(), failOutbox(forced))
	if !errors.Is(err, forced) {
		t.Fatalf("process = %v, want the forced failure", err)
	}
	wantKind(t, err, apperrors.KindTransient, nil)
	wantUntouched(t, w.ID(), p, "bet-1")
}

// Covers: TX-06, TX-10 (I03b, partial: the replay with 500 is M3, the DLQ is M5)
// Sensitivity: UnitOfWork committing when fn fails → "operation bet-1 persisted".
func TestPermanentFailureRecorded(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	w, _ := openWallet(t, "100.00")
	p := newProvider()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	cmd := command(t, w, p, wagering.KindBet, "30.00", "bet-1", "")
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))

	_, err := processWith(ctx, newUoW(), cmd, time.Now(), failOutbox(forced))
	wantKind(t, err, apperrors.KindPermanent, forced)
	wantUntouched(t, w.ID(), p, "bet-1")

	// D-05: the permanent failure is recorded in a separate transaction.
	var failed *wagering.WagerTransaction
	if err := newUoW().Do(ctx, func(r app.Repos) error {
		if _, err := r.Wallets().Lock(ctx, cmd.WalletID()); err != nil {
			return err
		}
		var err error
		if failed, err = wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr", time.Now()); err != nil {
			return err
		}
		if err := failed.Fail(time.Now()); err != nil {
			return err
		}
		return r.Transactions().Insert(ctx, failed)
	}); err != nil {
		t.Fatalf("record FAILED: %v", err)
	}
	got, err := reads().Transactions().FindByExternalID(ctx, p, "bet-1")
	if err != nil || got == nil {
		t.Fatalf("FindByExternalID = %v, %v", got, err)
	}
	if got.Status() != wagering.StatusFailed || got.FailureCode() != wagering.FailureInternalPermanentFailure || got.CompletedAt().IsZero() {
		t.Fatalf("stored %s %s completed %v, want FAILED INTERNAL_PERMANENT_FAILURE", got.Status(), got.FailureCode(), got.CompletedAt())
	}
	wantStored(t, failed)
	wantBalance(t, w.ID(), "100.00", 1)
	if sum, err := reads().Ledger().Sum(ctx, w.ID()); err != nil || sum.Entries != 1 {
		t.Fatalf("ledger = %+v, %v; want only the opening entry", sum, err)
	}
}

// Sensitivity of testkit.LedgerProblems, the SQL part of test-plan §6 that
// I03 and I17 rely on: each divergence, written with the triggers disabled on a
// database of its own, is reported.
//
// Sensitivity: the version-chain case disabled in LedgerProblems → "gap in the
// version chain" fails with problems = "".
func TestLedgerProblemsDetectsDivergence(t *testing.T) {
	t.Parallel()
	_, pool := isolatedDB(t, "ledgercheck")
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries"} {
		if _, err := pool.Exec(t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			t.Fatalf("disable triggers: %v", err)
		}
	}
	cases := []struct {
		name string
		rows func(w, p, o string) []stmt
		want string // empty when the wallet is consistent
	}{
		{"consistent", func(w, p, o string) []stmt { return nil }, ""},
		{"balance differs from the ledger", func(w, p, o string) []stmt {
			return []stmt{exec(`UPDATE wallets SET balance_minor = 5000 WHERE id = $1`, w)}
		}, "Σ credits"},
		{"gap in the version chain", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p).with("status", "PROCESSED", "failure_code", nil, "result_balance_minor", int64(9000))),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, bet, "DEBIT", 1000, 10000, 9000, 3)),
				exec(`UPDATE wallets SET balance_minor = 9000, version = 3 WHERE id = $1`, w),
			}
		}, "does not follow"},
		{"version ahead of the ledger", func(w, p, o string) []stmt {
			return []stmt{exec(`UPDATE wallets SET version = 3 WHERE id = $1`, w)}
		}, "last ledger version"},
		{"entry of a rejected operation", func(w, p, o string) []stmt {
			bet := newID()
			return []stmt{
				ins("wager_transactions", externalRow(bet, w, p)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, bet, "DEBIT", 1000, 10000, 9000, 2)),
				exec(`UPDATE wallets SET balance_minor = 9000, version = 2 WHERE id = $1`, w),
			}
		}, "do not move the balance"},
		{"processed operation without entry", func(w, p, o string) []stmt {
			return []stmt{ins("wager_transactions", externalRow(newID(), w, p).with("status", "PROCESSED", "failure_code", nil))}
		}, "without exactly one entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, p, o := newID(), newID(), newID()
			stmts := append([]stmt{
				ins("wallets", walletRow(w, p, 10000)),
				ins("wager_transactions", openingRow(o, w, p, 10000)),
				ins("wallet_ledger_entries", ledgerRow(newID(), w, o, "CREDIT", 10000, 0, 10000, 1)),
			}, tc.rows(w, p, o)...)
			if err := attemptCommit(t, pool, stmts...); err != nil {
				t.Fatalf("seed: %v", err)
			}
			problems, err := testkit.LedgerProblems(t.Context(), pool, w)
			if err != nil {
				t.Fatalf("LedgerProblems: %v", err)
			}
			joined := strings.Join(problems, "; ")
			if tc.want == "" && len(problems) != 0 || tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("problems = %q, want %q", joined, tc.want)
			}
		})
	}
}
```

- [ ] **Passo 2: rodar**

```bash
go test -tags=integration -race -count=1 ./internal/adapters/postgres/
```

Esperado: `ok`.

- [ ] **Passo 3: sensibilidade A (I17)**

Em `internal/adapters/postgres/transaction_repo.go`, no `FindReference`, troque temporariamente o `EXISTS (SELECT 1 … c.status = 'PROCESSED')` por `false`. Rode `go test -tags=integration -race -count=1 -run '^TestDomainFlowsPersist$' ./internal/adapters/postgres/`. Esperado: FAIL em `process REFUND refund-3: TRANSIENT: app: concurrent reversal of the same reference: postgres: 23505 wager_tx_single_reversal_uq`. Desfaça e rode de novo: `ok`.

- [ ] **Passo 4: sensibilidade B (I03a e I03b)**

Em `internal/adapters/postgres/uow.go`, no `run`, troque temporariamente o `rollback(ctx, tx)` do ramo `if err := fn(repos{q: tx}); err != nil` por `_ = tx.Commit(ctx)`. Rode `go test -tags=integration -race -count=1 -run '^(TestFinancialAtomicity|TestPermanentFailureRecorded)$' ./internal/adapters/postgres/`. Esperado: os dois FAIL com `operation bet-1 persisted`. Desfaça e rode de novo: `ok`.

- [ ] **Passo 5: sensibilidade C (`LedgerProblems`)**

Em `test/testkit/postgres.go`, troque temporariamente `case !first && (v != last+1 || before != prevAfter):` por `case !first && false:`. Rode `go test -tags=integration -race -count=1 -run '^TestLedgerProblemsDetectsDivergence$' ./internal/adapters/postgres/`. Esperado: FAIL em `gap in the version chain` com `problems = "", want "does not follow"`. Desfaça e rode de novo: `ok`. As três sabotagens já estão registradas nos comentários `// Sensitivity:` dos testes.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/` verde.

---

### Tarefa 11: Fx, serviço `migrate`, Makefile e `.env.example`

O módulo `postgres` passa a fornecer `app.UnitOfWork` e o `app.Repos` sobre o pool (spec §2, decisão 6). O compose ganha o serviço `migrate`, e as réplicas só sobem depois dele (spec §6, decisão 9).

**Arquivos:**
- Implementação: `internal/adapters/postgres/module.go` (alterar), `.env.example` (alterar), `Makefile` (alterar), `docker-compose.yml` (alterar)
- Testes e helpers de teste: `internal/bootstrap/bootstrap_test.go` (alterar)

**Interfaces:**
- Consome: `NewUnitOfWork`, `NewRepos` (Tarefa 6).
- Produz: `postgres.Module` com `app.UnitOfWork` e `app.Repos`; serviço `migrate`; `make migrate-up`, `make migrate-down N=1`; `DATABASE_OWNER_URL`.

- [ ] **Passo 2: escrever os testes que falham**

`internal/bootstrap/bootstrap_test.go` (substitui o arquivo inteiro):

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
		mux    *http.ServeMux
		uow    app.UnitOfWork
		repos  app.Repos
	)
	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &mux, &uow, &repos))
	if err := fx.ValidateApp(opts...); err != nil {
		t.Fatalf("fx.ValidateApp() = %v", err)
	}
}
```

- [ ] **Passo 3: rodar e ver falhar**

```bash
go test -race -count=1 ./internal/bootstrap/
```

Esperado: FAIL. Falha `TestFxGraph` (`missing types: app.UnitOfWork`). Nenhum `panic` nem erro de compilação.

- [ ] **Passo 4: implementar**

`internal/adapters/postgres/module.go` (substitui o arquivo inteiro):

```go
package postgres

import (
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module provides the pool, the unit of work, the repositories over the pool
// for reads (D-14) and the "postgres" health checker.
var Module = fx.Module("postgres",
	fx.Provide(
		NewPool,
		fx.Annotate(NewUnitOfWork, fx.As(new(app.UnitOfWork))),
		NewRepos,
		fx.Annotate(NewChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
)
```

`.env.example` (substitui o arquivo inteiro):

```sh
# Valores LOCAIS de desenvolvimento e teste. Nenhum segredo real (ART-10).
# Para sobrescrever sem editar este arquivo, crie um .env (ignorado pelo git).
# Carregado por todos os serviços do compose (env_file) e pelo test/testkit.

# --- PostgreSQL ---
POSTGRES_USER=postgres
POSTGRES_PASSWORD=postgres-local
POSTGRES_DB=pda
PDA_OWNER_PASSWORD=pda-owner-local
PDA_APP_PASSWORD=pda-app-local

# --- Aplicação (endereços da rede do compose) ---
DATABASE_URL=postgres://pda_app:pda-app-local@postgres:5432/pda?sslmode=disable

# --- Migrations (serviço migrate, como pda_owner) ---
DATABASE_OWNER_URL=postgres://pda_owner:pda-owner-local@postgres:5432/pda?sslmode=disable

# --- Keycloak ---
KC_BOOTSTRAP_ADMIN_USERNAME=admin
KC_BOOTSTRAP_ADMIN_PASSWORD=admin-local
PROVIDER_A_SECRET=provider-a-local-secret
PROVIDER_B_SECRET=provider-b-local-secret
WALLET_SERVICE_SECRET=wallet-service-local-secret
NO_ROLE_CLIENT_SECRET=no-role-client-local-secret
NO_AUDIENCE_CLIENT_SECRET=no-audience-client-local-secret
PROVIDER_SHORT_LIVED_SECRET=provider-short-lived-local-secret
OTHER_PROVIDER_SECRET=other-provider-local-secret

# --- AWS (MiniStack) ---
# As chaves NÃO ficam aqui: o aws-init gera usuários IAM e grava .local/aws/credentials.
AWS_REGION=us-east-1
AWS_ENDPOINT_URL=http://ministack:4566
```

`Makefile` (substitui o arquivo inteiro):

```make
SHELL := /bin/bash

GOLANGCI_LINT_VERSION ?= v2.14.0
GOLANGCI_LINT ?= docker run --rm -t \
	-v $(CURDIR):/app -w /app \
	-v $(shell go env GOMODCACHE):/go/pkg/mod \
	-v $(HOME)/.cache/golangci-lint:/root/.cache \
	golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint
GOVULNCHECK_VERSION ?= v1.8.0
COMPOSE ?= docker compose
INFRA_SERVICES := postgres keycloak ministack

.PHONY: up down infra-up migrate-up migrate-down fmt fmt-check lint vet vuln tidy-check go-version-check test test-integration check

up:
	$(COMPOSE) up --build

down:
	$(COMPOSE) down -v

# aws-init and migrate are one-shot: they run once their dependencies are healthy.
infra-up:
	$(COMPOSE) up -d --wait $(INFRA_SERVICES)
	$(COMPOSE) up aws-init --exit-code-from aws-init
	$(COMPOSE) up migrate --exit-code-from migrate

# Applies every pending migration (data-model §7).
migrate-up:
	$(COMPOSE) run --rm migrate up

# Reverts the last N migrations (default 1).
N ?= 1
migrate-down:
	$(COMPOSE) run --rm migrate down $(N)

fmt:
	$(GOLANGCI_LINT) fmt

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	@$(GOLANGCI_LINT) fmt --diff

lint:
	$(GOLANGCI_LINT) run

vet:
	go vet ./...
	go vet -tags=integration,e2e,faultinject ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

tidy-check:
	go mod tidy -diff

go-version-check:
	@mod=$$(awk '/^go /{print $$2}' go.mod); \
	img=$$(sed -nE 's/^FROM golang:([0-9.]+)-alpine.*/\1/p' Dockerfile); \
	test "$$mod" = "$$img" || { echo "go.mod ($$mod) != Dockerfile ($$img)"; exit 1; }

test:
	go test -race ./...

test-integration: infra-up
	go test -tags=integration -race -count=1 ./...

check: fmt-check lint vet tidy-check go-version-check test
```

`docker-compose.yml` (substitui o arquivo inteiro):

```yaml
name: pda

x-env-files: &env-files
  env_file:
    - path: .env.example
    - path: .env
      required: false

x-app: &app
  build: .
  image: pda:local
  env_file:
    - path: .env.example
    - path: .env
      required: false
  environment:
    LOG_LEVEL: info
    AWS_SHARED_CREDENTIALS_FILE: /aws/credentials
    AWS_PROFILE: pda-wallet-service
  volumes:
    - ./.local/aws:/aws:ro
  depends_on:
    postgres:
      condition: service_healthy
    migrate:
      condition: service_completed_successfully
    aws-init:
      condition: service_completed_successfully
  healthcheck:
    test: ["CMD", "/pda", "healthcheck"]
    interval: 5s
    timeout: 4s
    retries: 12
    start_period: 5s

services:
  postgres:
    image: postgres:18.6-alpine
    <<: *env-files
    ports: ["5432:5432"]
    volumes:
      - ./deploy/postgres/01-roles.sh:/docker-entrypoint-initdb.d/01-roles.sh:ro
      - pgdata:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres -d pda"]
      interval: 2s
      timeout: 3s
      retries: 30

  # Applies migrations/ as pda_owner (data-model §7). The URL comes from the
  # env_file, which compose does not interpolate, so a shell expands it.
  migrate:
    image: migrate/migrate:v4.20.1
    <<: *env-files
    entrypoint: ["sh", "-c", "exec migrate -path /migrations -database \"$$DATABASE_OWNER_URL\" \"$$@\"", "migrate"]
    command: ["up"]
    volumes:
      - ./migrations:/migrations:ro
    depends_on:
      postgres:
        condition: service_healthy

  keycloak:
    image: quay.io/keycloak/keycloak:26.7.4
    <<: *env-files
    command: ["start-dev", "--import-realm"]
    environment:
      KC_HOSTNAME: http://localhost:8080
      KC_HOSTNAME_BACKCHANNEL_DYNAMIC: "true"
      KC_HEALTH_ENABLED: "true"
    ports: ["8080:8080"]
    volumes:
      - ./deploy/keycloak:/opt/keycloak/data/import:ro
    healthcheck:
      test: ["CMD", "bash", "-c", "exec 3<>/dev/tcp/localhost/9000 && printf 'GET /health/ready HTTP/1.0\\r\\nHost: localhost\\r\\n\\r\\n' >&3 && read -r line <&3 && [[ $$line == *' 200 '* ]]"]
      interval: 5s
      timeout: 5s
      retries: 30
      start_period: 60s

  ministack:
    image: ministackorg/ministack:1.5.18
    environment:
      AUTH: "true"
    ports: ["4566:4566"]

  aws-init:
    image: amazon/aws-cli:2.36.31
    <<: *env-files
    entrypoint: ["bash", "/deploy/aws/init.sh"]
    environment:
      AWS_ACCESS_KEY_ID: test
      AWS_SECRET_ACCESS_KEY: test
    volumes:
      - ./deploy/aws:/deploy/aws:ro
      - ./.local/aws:/aws-shared
    depends_on:
      ministack:
        condition: service_healthy

  app-1:
    <<: *app
    ports: ["8081:8080", "9091:9090"]

  app-2:
    <<: *app
    ports: ["8082:8080", "9092:9090"]

  app-3:
    <<: *app
    ports: ["8083:8080", "9093:9090"]

volumes:
  pgdata:
```

- [ ] **Passo 5: rodar e ver passar**

```bash
go test -race -count=1 ./internal/bootstrap/
```

Esperado: `ok`, com todos os testes do pacote verdes (inclusive os das tarefas anteriores).

- [ ] **Passo 6: compose e Makefile**

```bash
docker compose config -q
make infra-up
docker compose run --rm migrate version
make test-integration
```

Esperado: `config` sem saída; `infra-up` termina com o `migrate` aplicando `1/u create_wallets` … `6/u grant_app_role` no banco `pda` (ou `no change` se já aplicadas); `version` imprime `6`; `make test-integration` verde em todos os pacotes (inclusive `internal/bootstrap`, que agora constrói o UoW e os repositórios).

**Checkpoint:** `go test -race -count=1 ./internal/bootstrap/` verde.

---

### Tarefa 12: verificação e encerramento do marco

Segue `superpowers:verification-before-completion` e a definição de pronto de [`development-workflow.md`](../../development-workflow.md) §5. **Nenhuma afirmação de "pronto" sem a saída dos comandos na mesma mensagem.**

**Arquivos:** `ARCHITECTURE.md`, `docs/delivery-requirements.md`, `docs/implementation-plan.md`, `docs/dev/specs/2026-09-29-m2-persistence-design.md` (status), `docs/dev/diary.md`.

- [ ] **Passo 1: portão de qualidade**

```bash
make check
```

Esperado: `0 issues.` no lint, `gofmt` e `go mod tidy -diff` limpos, `go vet` com e sem tags sem avisos e `go test -race ./...` verde.

- [ ] **Passo 2: integração completa**

```bash
make test-integration
```

Esperado: `ok` em todos os pacotes com a tag, inclusive `internal/bootstrap` (I07a–c) e `test/integration` (`TestProvisioning`), agora com o `migrate` no `infra-up`.

- [ ] **Passo 3: estabilidade do pacote `postgres`**

```bash
go test -tags=integration -race -count=3 ./internal/adapters/postgres/
```

Esperado: `ok`.

- [ ] **Passo 4: ambiente completo e migrations pelo compose**

```bash
docker compose up --build --wait
docker compose ps
make migrate-down N=1 && make migrate-up
docker compose exec -T postgres psql -U pda_owner -d pda -c '\dt'
```

Esperado: `migrate` sai com código 0 antes das réplicas; `app-1..3` `healthy`; `6/d grant_app_role` e `6/u grant_app_role`; as 5 tabelas e `schema_migrations` listadas.

- [ ] **Passo 5: documentação**

- `ARCHITECTURE.md`:
  - §3.1: migrations pelo serviço `migrate` e `golang-migrate` nos testes;
  - §3.2: `Do` e `Snapshot`, portas com tipos do domínio, sentinelas das corridas, UoW interrompida pelo `ctx` transitória;
  - §3: o mapeamento de `Money` (`BIGINT` + `CHAR(3)`, `result_balance_minor` na moeda da carteira) — DB-05;
  - §4: `lock_timeout` por `set_config` com `DB_LOCK_TIMEOUT`;
  - §17: nada novo (o I03b completo é do M3/M5, registrado lá).
- `delivery-requirements.md`: marcar com `*(M2, 29/09: …)*` citando os testes: ART-05, DB-01, DB-02, DB-03, DB-05, WAL-03, WAL-04 (`CHECK`), WAL-06, WAL-07, LED-03, LED-04, LED-05, LED-06, TX-05, TX-07, TX-09, OUT-01; parciais: DB-04, IDEM-02, IDEM-07, SQS-03, OPS-08.
- `implementation-plan.md`: M2 ✅ com o link do plano; M3 consome as portas (`app.UnitOfWork`, `app.Repos`, sentinelas); M4 acrescenta claim/ack/lease à outbox; M6 acrescenta o `Lock` da transação e o claim com `SKIP LOCKED`; E4 (constraint) e E9 comprovados.
- Spec: status "aprovada e implementada".
- `docs/dev/diary.md`: entrada do M2 e "onde paramos".

- [ ] **Passo 6: proposta de commits** (o autor decide; §6 do workflow)

1. `feat(config): add DB_LOCK_TIMEOUT`
2. `feat(postgres): translate database errors into app sentinels and kinds`
3. `feat(db): add schema migrations with constraints, triggers and grants`
4. `test(testkit): add isolated database, env and ledger consistency check`
5. `feat(app): add persistence ports`
6. `feat(postgres): add unit of work and repositories`
7. `test(postgres): verify domain flows, atomicity and failures against the schema`
8. `build(compose): run migrations before the replicas`
9. `docs: record m2 persistence decisions`
10. `docs(dev): add m2 spec, plan and diary entry`

**Checkpoint:** evidência dos Passos 1–4 no chat e a proposta de commits aprovada pelo autor.
