# M6 — Worker de referências: plano de implementação

> **Execução:** `superpowers:executing-plans`, **inline** na própria sessão, sem subagentes ([`development-workflow.md`](../../development-workflow.md) §7). Cada tarefa segue `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Sem commits:** cada tarefa termina num *checkpoint* verificável, e os commits são propostos no fim do marco (§6 do workflow).

**Objetivo:** toda operação em `PENDING_REFERENCE` é retomada por qualquer instância a partir da agenda do banco: vira `PROCESSED`, `REJECTED` (R3–R7, ou `REFERENCE_NOT_FOUND` ao expirar) ou é reagendada, e duas instâncias nunca contam a mesma tentativa duas vezes (OPS-12, OPS-13, TX-09, SQS-08; prepara o **E7**).

**Arquitetura:**
- **`app.ResolveReferences`** (spec decisão 5): reusa o `ProcessWager` (UoW, relógio, IDs, política, `settleAndPersist` com `insert = false`). Em uma UoW: trava a carteira, depois a transação, refaz o recheck de **status e horário** (decisão 3) e avalia. `FAILED` vai para uma UoW separada (decisão 8).
- **Persistência:** `TransactionRepository` ganha `ClaimDue` (um statement no pool, `FOR UPDATE SKIP LOCKED`), `Lock` (`FOR UPDATE`) e `CountPendingReferences`. Nenhuma migration.
- **`adapters/references`:** `Worker.Run` (claim → itens **em sequência** → espera), `backoff.go` (claim com o banco fora) e o módulo Fx. As portas `Resolver` e `Metrics` são do adapter.
- **Ordem das tarefas:** config → métricas → persistência → caso de uso (núcleo, recheck, falhas) → loop do worker → módulo Fx e primeiros testes de ponta a ponta → `StartApp` com opções e reinício → expiração e SQS → concorrência → encerramento.

**Stack:** Go 1.27.1, `pgx/v5`, Prometheus `client_golang` v1.24.1 e `goleak` v1.3.0 (já no `go.mod`); PostgreSQL 18.6, Keycloak 26.7.4 e MiniStack 1.5.18 do compose. **Nenhuma dependência nova.**

**Spec:** [`docs/dev/specs/2026-09-30-m6-reference-worker-design.md`](../specs/2026-09-30-m6-reference-worker-design.md). Quem executa lê a spec inteira.

**Validação prévia do plano:** ao contrário dos planos do M3 ao M5, **este código não foi testado numa cópia descartável.** Ele foi escrito lendo o código atual (assinaturas, helpers de teste, `testkit`), então a execução é a primeira validação. Se uma tarefa divergir do que está aqui (nome de helper, tipo, lint), **pare, corrija o plano e registre o achado no diário** ([`development-workflow.md`](../../development-workflow.md) §7).

## Restrições globais

- Module path `github.com/KaioVinicios/pda`; `go 1.27.1`.
- **Camadas:** `internal/app` ganha `ResolveReferences` sem importar Prometheus, Fx nem pgx; `adapters/references` importa `app`, `config` e `observability`, nunca `adapters/postgres` (os testes podem).
- **Dinheiro nunca em `float`:** fora de `internal/observability`, nenhum `float64`.
- **Logs:** chaves em `camelCase`, mensagens estáticas (`sloglint`), sempre com `transactionId`, `walletId`, `providerId` e `correlationId` do desfecho, nunca o payload.
- **Contextos:** nunca em struct (`containedctx`). Cada item roda com `context.WithoutCancel(ctx)` e prazo próprio (`contextcheck`).
- **Testes de integração:** tag `integration` e `make infra-up` no ar; `-race`; `// Covers: <IDs>` em todo teste; só `testing` da stdlib; esperas sempre com prazo (`testkit.Eventually`). Exceções justificadas de `time.Sleep`: janelas negativas ("nada acontece durante X") no `TestWorkerWaitsBetweenBatches`.
- **Checagem de sensibilidade** (workflow §4.3) nos testes escritos sobre comportamento que já existe: registrar `// Sensitivity: …` com a sabotagem usada.
- **`make lint` e `make fmt`** usam a imagem `golangci/golangci-lint:v2.14.0`, então o Docker precisa estar rodando. Os blocos de código abaixo já seguem o `gofumpt`.
- **Sem commits, branches ou worktrees.**

## Foco de revisão

Os casos que a spec implica, mas nenhum teste óbvio cobre, com mais chance de causar problema. Cada um tem teste na tarefa indicada:

1. **Duas instâncias pegam o mesmo item ao mesmo tempo.** Esperado: uma avaliação só, a outra ignora sem contar tentativa. → Tarefa 5 (`TestResolveReferencesSkips`, `TestResolveReferencesConcurrent`) e Tarefa 11 (`TestConcurrentWorkers`).
2. **O banco cai (ou o `ctx` é cancelado) no meio de um item.** Esperado: rollback inteiro, sem efeito parcial, e o item continua devido. → Tarefa 6 (`TestResolveReferencesFailures`).
3. **A operação de que a pendência depende também é pendente, falhou ou foi rejeitada, e cadeias de pendências.** Esperado: R2 reagenda, R3 rejeita, a cadeia se resolve em cascata. → Tarefa 4.
4. **O banco fora durante o claim.** Esperado: o worker não gira em laço nem encerra o processo; espera 1 s, 2 s… e volta ao início depois de um sucesso. → Tarefa 7 (`TestWorkerClaimBackoff`).
5. **Todas as instâncias paradas além do TTL.** Esperado: ao subir, a pendência expira com `REFERENCE_NOT_FOUND`, mesmo sem nenhuma tentativa no intervalo. → Tarefa 9 (`TestPendingExpiresAfterDowntime`).
6. **Falha permanente de uma pendência que tem dependentes.** Esperado: `FAILED` sem lançamento nem evento, e os dependentes saem com `REFERENCE_NOT_PROCESSED`. → Tarefa 6.

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `internal/config/config.go`, `validate.go`, `config_test.go`; `test/testkit/env.go` | `REFERENCE_POLL_INTERVAL` e `REFERENCE_BATCH_SIZE`; tempos de teste | 1 |
| `internal/observability/metrics.go`, `metrics_test.go` | 3 métricas de referência | 2 |
| `internal/app/ports.go`; `internal/adapters/postgres/transaction_repo.go`; `transaction_claim_integration_test.go` (novo) | `PendingReference`, `ClaimDue`, `Lock`, `CountPendingReferences` | 3 |
| `internal/app/resolve_references.go` (novo), `process_wager.go` | `ResolveReferences`, `ResolveResult`, causa por função | 4 |
| `internal/app/resolve_references_integration_test.go` (novo) | `TestResolveReferences`, `Skips`, `Concurrent`, `Failures` | 4–6 |
| `internal/adapters/references/worker.go`, `backoff.go` (novos) + testes | Loop, portas `Resolver` e `Metrics` | 7 |
| `internal/adapters/references/module.go` (novo); `internal/bootstrap/app_module.go`, `bootstrap.go`, `bootstrap_test.go` | Módulo Fx e grafo | 8 |
| `test/integration/helpers_test.go`, `wagering_test.go`, `events_test.go`, `references_test.go` (novo) | `waitStatus`, I11 com o worker, C2 por HTTP, testes existentes com pendência | 8, 10 |
| `test/testkit/app.go`; `test/integration/restart_test.go` (novo) | `StartApp(ctx, opts...)`, I06 e I06b | 9 |
| `test/integration/sqs_test.go` | `TestSQSPendingReferenceResolved` | 10 |
| `internal/adapters/references/concurrent_integration_test.go` (novo); `test/integration/references_test.go` | `TestConcurrentWorkers`, `TestWorkerVersusHTTP` | 11 |
| `docs/*`, `ARCHITECTURE.md`, diário | Encerramento | 12 |

---

## Tarefa 1: Configuração do worker

**Arquivos:**
- Modificar: `internal/config/config.go`, `internal/config/validate.go`, `internal/config/config_test.go`
- Modificar: `test/testkit/env.go`

**Interfaces:**
- Produz: `config.Config.ReferencePollInterval` (`REFERENCE_POLL_INTERVAL`, padrão 500 ms, > 0) e `config.Config.ReferenceBatchSize` (`REFERENCE_BATCH_SIZE`, padrão 50, ≥ 1); `Env.Config()` com 50 ms e 50.

- [ ] **Passo 1: escrever os testes.** Em `internal/config/config_test.go`:

```diff
@@ var allVars = []string{
 	"REFERENCE_RETRY_BASE_DELAY", "REFERENCE_RETRY_MAX_DELAY", "REFERENCE_MAX_ATTEMPTS", "REFERENCE_TTL",
+	"REFERENCE_POLL_INTERVAL", "REFERENCE_BATCH_SIZE",
@@ func validConfig() config.Config {
 		ReferenceTTL:       10 * time.Minute,
+		ReferencePollInterval: 500 * time.Millisecond, ReferenceBatchSize: 50,
@@ func TestLoad_ReadsEnvironment(t *testing.T) {
 		"REFERENCE_TTL": "3s", "SNS_EVENTS_TOPIC_NAME": "events.fifo", "OUTBOX_BATCH_SIZE": "10",
+		"REFERENCE_POLL_INTERVAL": "50ms", "REFERENCE_BATCH_SIZE": "7",
@@ want := config.Config{
 		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
+		ReferencePollInterval: 50 * time.Millisecond, ReferenceBatchSize: 7,
@@ func TestValidate_RejectsInvalidValues(t *testing.T) {
 		{"zero ttl", func(c *config.Config) { c.ReferenceTTL = 0 }, "REFERENCE_TTL"},
+		{"zero reference poll interval", func(c *config.Config) { c.ReferencePollInterval = 0 }, "REFERENCE_POLL_INTERVAL"},
+		{"zero reference batch", func(c *config.Config) { c.ReferenceBatchSize = 0 }, "REFERENCE_BATCH_SIZE"},
```

Depois do `gofumpt`, o literal de `validConfig()` fica com o alinhamento que o formatador der (rode `make fmt` no fim).

- [ ] **Passo 2: stub dos campos, para o teste falhar na asserção.** Em `internal/config/config.go`, no bloco "Schedule of pending references (D-11)", **sem** as tags `env`:

```go
	ReferencePollInterval time.Duration
	ReferenceBatchSize    int
```

- [ ] **Passo 3: ver falhar.** Rode `go test -race ./internal/config/...`.
  Esperado: `TestLoad_Defaults` e `TestLoad_ReadsEnvironment` falham por `ReferencePollInterval`/`ReferenceBatchSize` zerados, e os dois casos novos de `TestValidate_RejectsInvalidValues` falham com `Validate() vars = [], want [REFERENCE_POLL_INTERVAL]` (o nome do primeiro teste de defaults pode variar: use o que `go test` mostrar).

- [ ] **Passo 4: implementar.** Em `config.go`, troque o stub:

```go
	ReferencePollInterval time.Duration `env:"REFERENCE_POLL_INTERVAL" envDefault:"500ms"`
	ReferenceBatchSize    int           `env:"REFERENCE_BATCH_SIZE" envDefault:"50"`
```

Em `validate.go`, logo depois da checagem de `ReferenceTTL`:

```go
	if c.ReferencePollInterval <= 0 {
		fail("REFERENCE_POLL_INTERVAL", "must be greater than 0")
	}
	if c.ReferenceBatchSize < 1 {
		fail("REFERENCE_BATCH_SIZE", "must be at least 1")
	}
```

Em `test/testkit/env.go`, no `Env.Config()`, depois de `DBLockTimeout`:

```go
		ReferencePollInterval: 50 * time.Millisecond,
		ReferenceBatchSize:    50,
```

- [ ] **Passo 5: ver passar.** `go test -race ./internal/config/... ./test/testkit/...` verde.

- [ ] **Checkpoint:** `make fmt` sem diferenças e `go test -race ./internal/config/...` verde.

---

## Tarefa 2: Métricas de referência

**Arquivos:**
- Modificar: `internal/observability/metrics.go`, `internal/observability/metrics_test.go`

**Interfaces:**
- Produz (`*observability.Metrics`, que passa a satisfazer a porta `references.Metrics` da Tarefa 7): `ReferenceRetried()`, `ReferenceExpired()`, `ReferencePending(n int)`. Séries `reference_retries_total`, `reference_expired_total` (counters) e `reference_pending_transactions` (gauge), sem labels.

- [ ] **Passo 1: escrever o teste.** Em `internal/observability/metrics_test.go`:

```go
// Covers: OBS-03, OPS-12, OPS-13 (spec M6, decision 13; ARCHITECTURE.md §13.2)
func TestMetrics_References(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.ReferenceRetried()
	m.ReferenceRetried()
	m.ReferenceExpired()
	m.ReferencePending(4)

	want := `
# HELP reference_expired_total Pending operations rejected with REFERENCE_NOT_FOUND after the retry limit or the TTL.
# TYPE reference_expired_total counter
reference_expired_total 1
# HELP reference_pending_transactions Operations waiting in PENDING_REFERENCE.
# TYPE reference_pending_transactions gauge
reference_pending_transactions 4
# HELP reference_retries_total Reference resolution attempts rescheduled for a later time.
# TYPE reference_retries_total counter
reference_retries_total 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		"reference_expired_total", "reference_pending_transactions", "reference_retries_total"); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Passo 2: stub.** Em `metrics.go`, no fim do arquivo, três métodos vazios:

```go
// ReferenceRetried counts a pending operation rescheduled for a later attempt.
func (m *Metrics) ReferenceRetried() {}

// ReferenceExpired counts a pending operation rejected because its limit was reached.
func (m *Metrics) ReferenceExpired() {}

// ReferencePending sets the number of operations in PENDING_REFERENCE.
func (m *Metrics) ReferencePending(n int) {}
```

- [ ] **Passo 3: ver falhar.** `go test -race -run '^TestMetrics_References$' ./internal/observability/...`.
  Esperado: falha da comparação (`expected metric name "reference_expired_total"` ausente, ou diff do texto): as séries não existem.

- [ ] **Passo 4: implementar.** Em `metrics.go`: atualize o comentário do tipo `Metrics` para "…the ports of the outbox publisher, of the SQS consumer and of the reference worker…"; acrescente ao struct:

```go
	referenceRetries prometheus.Counter
	referenceExpired prometheus.Counter
	referencePending prometheus.Gauge
```

Em `NewMetrics`, dentro do literal (depois de `sqsDeleteErrors`):

```go
		referenceRetries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reference_retries_total",
			Help: "Reference resolution attempts rescheduled for a later time.",
		}),
		referenceExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reference_expired_total",
			Help: "Pending operations rejected with REFERENCE_NOT_FOUND after the retry limit or the TTL.",
		}),
		referencePending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "reference_pending_transactions",
			Help: "Operations waiting in PENDING_REFERENCE.",
		}),
```

No `reg.MustRegister(...)`, acrescente `m.referenceRetries, m.referenceExpired, m.referencePending`. Troque os três métodos:

```go
func (m *Metrics) ReferenceRetried() { m.referenceRetries.Inc() }

func (m *Metrics) ReferenceExpired() { m.referenceExpired.Inc() }

func (m *Metrics) ReferencePending(n int) { m.referencePending.Set(float64(n)) }
```

- [ ] **Passo 5: ver passar.** `go test -race ./internal/observability/...` verde.

- [ ] **Checkpoint:** `make fmt` limpo e o pacote `observability` verde.

---

## Tarefa 3: Persistência da fila de pendências

**Arquivos:**
- Modificar: `internal/app/ports.go`, `internal/adapters/postgres/transaction_repo.go`
- Criar: `internal/adapters/postgres/transaction_claim_integration_test.go`

**Interfaces:**
- Produz (`internal/app/ports.go`):

```go
// PendingReference is a due PENDING_REFERENCE operation: the wallet to lock
// first, then the operation (data-model §6).
type PendingReference struct{ ID, WalletID string }

// no TransactionRepository:
ClaimDue(ctx context.Context, now time.Time, limit int) ([]PendingReference, error)
Lock(ctx context.Context, id string) (*wagering.WagerTransaction, error)
CountPendingReferences(ctx context.Context) (int, error)
```

- [ ] **Passo 1: escrever os testes.** Crie `internal/adapters/postgres/transaction_claim_integration_test.go`. O `ClaimDue` enxerga a tabela inteira, então este arquivo usa um **banco próprio** (`NewTestEnv`), como o `TestOutboxStore` do M4:

```go
//go:build integration

package postgres_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

// claimFixture is a database of its own: ClaimDue sees the whole table, so
// tests that shared it would claim each other's operations.
type claimFixture struct {
	uow   *postgres.UnitOfWork
	repos app.Repos
	owner *pgxpool.Pool
	w     wallet.Wallet
	p     string
	base  time.Time
}

func newClaimFixture(t *testing.T) claimFixture {
	t.Helper()
	e := testkit.NewTestEnv(t, "claim_due")
	f := claimFixture{
		uow: postgres.NewUnitOfWork(e.App, e.Config()), repos: postgres.NewRepos(e.App), owner: e.Owner,
		w: zeroWallet(t), p: newProvider(), base: time.Now(),
	}
	if err := f.uow.Do(t.Context(), func(r app.Repos) error { return r.Wallets().Insert(t.Context(), f.w) }); err != nil {
		t.Fatalf("insert wallet: %v", err)
	}
	return f
}

// pending writes a REFUND that waits for a BET that never arrives, created i
// seconds after base; its first retry falls about 100 ms later (the policy of
// this package).
func (f claimFixture) pending(t *testing.T, i int) *wagering.WagerTransaction {
	t.Helper()
	now := f.base.Add(time.Duration(i) * time.Second)
	cmd := command(t, f.w, f.p, wagering.KindRefund, "1.00", "refund-"+strconv.Itoa(i), "bet-missing")
	tx, err := wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AwaitReference(now, policy); err != nil {
		t.Fatal(err)
	}
	if err := f.uow.Do(t.Context(), func(r app.Repos) error { return r.Transactions().Insert(t.Context(), tx) }); err != nil {
		t.Fatalf("insert pending %d: %v", i, err)
	}
	return tx
}

func refIDs(refs []app.PendingReference) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.ID
	}
	return out
}

// Covers: OPS-12, TX-09 (I18: ClaimDue and CountPendingReferences)
func TestClaimDue(t *testing.T) {
	t.Parallel()
	f := newClaimFixture(t)
	ctx := t.Context()
	r0, r1, r2 := f.pending(t, 0), f.pending(t, 1), f.pending(t, 2)
	rejected := f.pending(t, 3)
	if _, err := rejected.Reject(wagering.FailureReferenceNotFound, f.w.Balance(), f.base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := f.uow.Do(ctx, func(r app.Repos) error { return r.Transactions().Update(ctx, rejected) }); err != nil {
		t.Fatalf("reject: %v", err)
	}
	far := f.base.Add(time.Hour)

	claim := func(now time.Time, limit int) []app.PendingReference {
		t.Helper()
		refs, err := f.repos.Transactions().ClaimDue(ctx, now, limit)
		if err != nil {
			t.Fatalf("ClaimDue: %v", err)
		}
		return refs
	}

	t.Run("only what is due, oldest first", func(t *testing.T) {
		got := claim(f.base.Add(1500*time.Millisecond), 10) // r0 and r1 are due, r2 is not
		if want := []string{r0.ID(), r1.ID()}; !slices.Equal(refIDs(got), want) {
			t.Fatalf("ClaimDue = %v, want %v", refIDs(got), want)
		}
		if got[0].WalletID != f.w.ID() {
			t.Fatalf("wallet %s, want %s", got[0].WalletID, f.w.ID())
		}
	})
	t.Run("respects the limit and skips concluded operations", func(t *testing.T) {
		if want := []string{r0.ID(), r1.ID()}; !slices.Equal(refIDs(claim(far, 2)), want) {
			t.Fatalf("ClaimDue(limit 2) = %v, want %v", refIDs(claim(far, 2)), want)
		}
		if want := []string{r0.ID(), r1.ID(), r2.ID()}; !slices.Equal(refIDs(claim(far, 10)), want) {
			t.Fatalf("ClaimDue = %v, want %v (the REJECTED one is not pending)", refIDs(claim(far, 10)), want)
		}
	})
	t.Run("nothing is due before the first retry", func(t *testing.T) {
		if got := claim(f.base.Add(-time.Hour), 10); len(got) != 0 {
			t.Fatalf("ClaimDue in the past = %v, want none", refIDs(got))
		}
	})
	t.Run("skips the rows another transaction holds", func(t *testing.T) {
		tx, err := f.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT 1 FROM wager_transactions WHERE id = $1 FOR UPDATE`, r0.ID()); err != nil {
			t.Fatal(err)
		}
		if want := []string{r1.ID(), r2.ID()}; !slices.Equal(refIDs(claim(far, 10)), want) {
			t.Fatalf("ClaimDue with %s locked = %v, want %v", r0.ID(), refIDs(claim(far, 10)), want)
		}
	})
	t.Run("counts the pending operations", func(t *testing.T) {
		n, err := f.repos.Transactions().CountPendingReferences(ctx)
		if err != nil || n != 3 {
			t.Fatalf("CountPendingReferences = %d, %v; want 3", n, err)
		}
	})
}

// Covers: OPS-12, D-09 (I18: Lock of the transaction)
func TestTransactionLock(t *testing.T) {
	t.Parallel()
	f := newClaimFixture(t)
	ctx := t.Context()
	pending := f.pending(t, 0)

	t.Run("reads the row and holds the lock until the transaction ends", func(t *testing.T) {
		err := f.uow.Do(ctx, func(r app.Repos) error {
			got, err := r.Transactions().Lock(ctx, pending.ID())
			if err != nil {
				return err
			}
			if got.ID() != pending.ID() || got.Status() != wagering.StatusPendingReference || got.Attempts() != 0 {
				t.Errorf("Lock = %s %s attempts %d", got.ID(), got.Status(), got.Attempts())
			}
			_, lockErr := f.owner.Exec(ctx, `SELECT 1 FROM wager_transactions WHERE id = $1 FOR UPDATE NOWAIT`, pending.ID())
			var pgErr *pgconn.PgError
			if !errors.As(lockErr, &pgErr) || pgErr.Code != "55P03" {
				t.Errorf("second lock = %v, want lock_not_available (55P03)", lockErr)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		for _, id := range []string{newID(), "not-a-uuid"} {
			err := f.uow.Do(ctx, func(r app.Repos) error {
				_, err := r.Transactions().Lock(ctx, id)
				return err
			})
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})
}
```

- [ ] **Passo 2: portas e stubs.** Em `internal/app/ports.go`, acrescente o tipo (antes de `TransactionRepository`) e os três métodos no fim da interface, com os comentários da seção Interfaces:

```go
// PendingReference is a due PENDING_REFERENCE operation: the wallet to lock
// first, then the operation (data-model §6).
type PendingReference struct{ ID, WalletID string }
```

```go
	// ClaimDue lists up to limit PENDING_REFERENCE operations due at now
	// (next_attempt_at <= now), oldest first, in one statement: the rows another
	// transaction holds are skipped (FOR UPDATE SKIP LOCKED). It leases nothing;
	// the worker rechecks under the locks (D-11, spec M6 decision 2).
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]PendingReference, error)
	// Lock reads the operation with SELECT … FOR UPDATE, after the wallet lock
	// (data-model §6); ErrNotFound when absent.
	Lock(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	// CountPendingReferences counts the operations in PENDING_REFERENCE.
	CountPendingReferences(ctx context.Context) (int, error)
```

Em `internal/adapters/postgres/transaction_repo.go`, stubs:

```go
func (r transactionRepo) ClaimDue(context.Context, time.Time, int) ([]app.PendingReference, error) {
	return nil, nil
}

func (r transactionRepo) Lock(context.Context, string) (*wagering.WagerTransaction, error) {
	return nil, nil
}

func (r transactionRepo) CountPendingReferences(context.Context) (int, error) { return 0, nil }
```

e o import `"github.com/KaioVinicios/pda/internal/app"` (o pacote já o importa em `repos.go`, então não há ciclo).

- [ ] **Passo 3: ver falhar.** `make infra-up` no ar; `go test -tags=integration -race -run 'TestClaimDue|TestTransactionLock' ./internal/adapters/postgres/...`.
  Esperado: `TestClaimDue` falha (`ClaimDue = [], want [...]`, `CountPendingReferences = 0`) e `TestTransactionLock` falha por nil pointer ou por `Lock` sem erro nos "not found" (`wantKind`); as falhas são asserções, não erro de compilação.

- [ ] **Passo 4: implementar.** Troque os stubs em `transaction_repo.go`:

```go
// ClaimDue lists the due PENDING_REFERENCE operations in one statement,
// skipping the rows another transaction holds (D-11, spec M6 decision 2). The
// instant is truncated to the microsecond, as AdvanceDependents writes it.
func (r transactionRepo) ClaimDue(ctx context.Context, now time.Time, limit int) ([]app.PendingReference, error) {
	rows, err := r.q.Query(ctx, `SELECT id, wallet_id FROM wager_transactions
		WHERE status = 'PENDING_REFERENCE' AND next_attempt_at <= $1
		ORDER BY next_attempt_at
		FOR UPDATE SKIP LOCKED
		LIMIT $2`, now.UTC().Truncate(time.Microsecond), limit)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []app.PendingReference
	for rows.Next() {
		var ref app.PendingReference
		if err := rows.Scan(&ref.ID, &ref.WalletID); err != nil {
			return nil, translate(err)
		}
		out = append(out, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// Lock reads the operation FOR UPDATE, after the caller locked its wallet. OF t
// keeps the join with the wallet from locking a second row.
func (r transactionRepo) Lock(ctx context.Context, id string) (*wagering.WagerTransaction, error) {
	if !ident.Valid(id) {
		return nil, notFound()
	}
	t, err := scanTransaction(r.q.QueryRow(ctx, txSelect+txFrom+` WHERE t.id = $1 FOR UPDATE OF t`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notFound()
	}
	return t, err
}

// CountPendingReferences feeds the reference_pending_transactions gauge.
func (r transactionRepo) CountPendingReferences(ctx context.Context) (int, error) {
	var n int
	if err := r.q.QueryRow(ctx, `SELECT count(*) FROM wager_transactions WHERE status = 'PENDING_REFERENCE'`).Scan(&n); err != nil {
		return 0, translate(err)
	}
	return n, nil
}
```

- [ ] **Passo 5: ver passar.** O mesmo comando do Passo 3, verde. Depois `go build ./...` (os decoradores dos testes embutem a interface, então compilam).

- [ ] **Passo 6: checagem de sensibilidade** do "pula linhas travadas". Troque temporariamente `FOR UPDATE SKIP LOCKED` por `FOR UPDATE`: o subteste "skips the rows another transaction holds" deve **travar** até o prazo do teste ou falhar. Desfaça e confirme o verde. Registre acima do `TestClaimDue`: `// Sensitivity: without SKIP LOCKED the claim waits for the held row (the "skips the rows another transaction holds" subtest hangs).`

- [ ] **Checkpoint:** `go test -tags=integration -race ./internal/adapters/postgres/...` verde, e `go vet ./...` limpo.

---

## Tarefa 4: `ResolveReferences` — o núcleo

**Arquivos:**
- Criar: `internal/app/resolve_references.go`, `internal/app/resolve_references_integration_test.go`
- Modificar: `internal/app/process_wager.go`

**Interfaces:**
- Consome: `TransactionRepository.ClaimDue/Lock/CountPendingReferences` (Tarefa 3), `ProcessWager` (`uow`, `reads`, `clock`, `ids`, `policy`, `log`), `settleAndPersist`.
- Produz:

```go
type ResolveOutcome string // ResolveSkipped, ResolveProcessed, ResolveRejected, ResolveRescheduled, ResolveFailed

type ResolveResult struct {
	Outcome       ResolveOutcome
	TransactionID string
	WalletID      string
	FailureCode   wagering.FailureCode // Rejected only
	Attempts      int
}
func (r ResolveResult) Expired() bool // Rejected with REFERENCE_NOT_FOUND

func NewResolveReferences(p *ProcessWager) *ResolveReferences
func (r *ResolveReferences) Claim(ctx context.Context, limit int) ([]PendingReference, error)
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error)
func (r *ResolveReferences) CountPending(ctx context.Context) (int, error)
```

- [ ] **Passo 1: escrever o teste.** Crie `internal/app/resolve_references_integration_test.go`. Os testes controlam o tempo com um relógio de teste e chamam `Resolve` direto com o ID conhecido (o `Claim` enxerga o banco compartilhado do pacote, que outros testes em paralelo usam):

```go
//go:build integration

package app_test

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// testClock is an app.Clock the test moves by hand. Tests that use it create
// the wallet first, so that the clock never precedes the wallet's creation.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock { return &testClock{now: time.Now()} }

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixture is a ProcessWager and a ResolveReferences over one testClock.
type fixture struct {
	pw    *app.ProcessWager
	rr    *app.ResolveReferences
	clock *testClock
}

func newFixture() fixture {
	clock := newTestClock()
	pw := app.NewProcessWager(newUoW(), reads(), clock, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
	return fixture{pw: pw, rr: app.NewResolveReferences(pw), clock: clock}
}

func refOf(res app.ProcessResult) app.PendingReference {
	return app.PendingReference{ID: res.Tx.ID(), WalletID: res.Tx.WalletID()}
}

// resolve evaluates the operation and fails the test on error.
func resolve(t *testing.T, f fixture, res app.ProcessResult) app.ResolveResult {
	t.Helper()
	got, err := f.rr.Resolve(t.Context(), refOf(res))
	if err != nil {
		t.Fatalf("Resolve %s: %v", res.Tx.ID(), err)
	}
	return got
}

func wantResolved(t *testing.T, got app.ResolveResult, outcome app.ResolveOutcome, code wagering.FailureCode) {
	t.Helper()
	if got.Outcome != outcome || got.FailureCode != code {
		t.Fatalf("resolved %+v, want %s %q", got, outcome, code)
	}
}

// stored reads the operation back.
func stored(t *testing.T, id string) *wagering.WagerTransaction {
	t.Helper()
	tx, err := reads().Transactions().Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return tx
}

// wantUnchanged fails unless the stored operation still has the state,
// attempts, schedule and update time of before.
func wantUnchanged(t *testing.T, before *wagering.WagerTransaction) {
	t.Helper()
	after := stored(t, before.ID())
	if after.Status() != before.Status() || after.Attempts() != before.Attempts() ||
		!after.NextAttemptAt().Equal(before.NextAttemptAt()) || !after.UpdatedAt().Equal(before.UpdatedAt()) {
		t.Fatalf("operation changed: %s attempts %d next %s, was %s attempts %d next %s",
			after.Status(), after.Attempts(), after.NextAttemptAt(), before.Status(), before.Attempts(), before.NextAttemptAt())
	}
}

// eventCauses lists the causationId and correlationId of the events of one
// operation, except its WagerTransactionPendingReference (emitted when it
// arrived, not by the worker).
func eventCauses(t *testing.T, txID string) (causes, correlations []string) {
	t.Helper()
	rows, err := env.Owner.Query(t.Context(), `
		SELECT COALESCE(payload->>'causationId', ''), payload->>'correlationId' FROM outbox_events
		WHERE payload->'data'->>'transactionId' = $1 AND event_type <> 'WagerTransactionPendingReference'
		ORDER BY event_type`, txID)
	if err != nil {
		t.Fatalf("events of %s: %v", txID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cause, corr string
		if err := rows.Scan(&cause, &corr); err != nil {
			t.Fatal(err)
		}
		causes, correlations = append(causes, cause), append(correlations, corr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return causes, correlations
}

// entries counts the ledger entries of the wallet.
func entries(t *testing.T, walletID string) int {
	t.Helper()
	return count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID)
}

// Covers: OPS-12, OPS-13, OPS-14, TX-09 (I06c: the worker's use case)
// Sensitivity: settleAndPersist inserting instead of updating when insert is false → every subtest fails on the unique index.
func TestResolveReferences(t *testing.T) {
	t.Parallel()

	t.Run("a REFUND that waited is processed when its BET arrives (C2)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
		bet := process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResult(t, bet, wagering.StatusProcessed, "", "70.00", false)

		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		tx := stored(t, pending.Tx.ID())
		if tx.Status() != wagering.StatusProcessed || tx.ResultBalance().String() != "100.00" || tx.ReferenceTransactionID() != bet.Tx.ID() {
			t.Fatalf("REFUND = %s balance %s reference %s", tx.Status(), tx.ResultBalance(), tx.ReferenceTransactionID())
		}
		wantWallet(t, w.ID(), "100.00", 3)
		if n := entries(t, w.ID()); n != 3 {
			t.Fatalf("%d ledger entries, want the opening, the BET and the REFUND", n)
		}
		// The events cite the reference that unblocked the operation and keep the original correlation (messaging §7).
		causes, corrs := eventCauses(t, pending.Tx.ID())
		if !slices.Equal(causes, []string{bet.Tx.ID(), bet.Tx.ID()}) || !slices.Equal(corrs, []string{"corr-refund-1", "corr-refund-1"}) {
			t.Fatalf("causes %v correlations %v, want the BET %s and corr-refund-1 twice", causes, corrs, bet.Tx.ID())
		}
	})

	t.Run("a reference that is still absent reschedules", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		f.clock.Advance(time.Minute) // past the first delay (30 s ± 20%)

		got := resolve(t, f, pending)
		wantResolved(t, got, app.ResolveRescheduled, "")
		tx := stored(t, pending.Tx.ID())
		if got.Attempts != 1 || tx.Attempts() != 1 || tx.Status() != wagering.StatusPendingReference || !tx.NextAttemptAt().After(f.clock.Now()) {
			t.Fatalf("result attempts %d; stored %s attempts %d next %s (now %s)", got.Attempts, tx.Status(), tx.Attempts(), tx.NextAttemptAt(), f.clock.Now())
		}
		if causes, _ := eventCauses(t, pending.Tx.ID()); len(causes) != 0 {
			t.Fatalf("a retry emitted %d events, want none", len(causes))
		}
	})

	t.Run("the limit rejects with REFERENCE_NOT_FOUND (C3)", func(t *testing.T) {
		t.Parallel()
		t.Run("by attempts", func(t *testing.T) {
			t.Parallel()
			w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
			pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
			for i, step := range []time.Duration{time.Minute, 3 * time.Minute} {
				f.clock.Advance(step)
				got := resolve(t, f, pending)
				wantResolved(t, got, app.ResolveRescheduled, "")
				if got.Attempts != i+1 {
					t.Fatalf("attempts %d, want %d", got.Attempts, i+1)
				}
			}
			f.clock.Advance(3 * time.Minute) // 7 min: the 3rd attempt, before the 10 min TTL
			got := resolve(t, f, pending)
			wantResolved(t, got, app.ResolveRejected, wagering.FailureReferenceNotFound)
			if !got.Expired() {
				t.Fatalf("%+v is not an expiration", got)
			}
			tx := stored(t, pending.Tx.ID())
			if tx.Status() != wagering.StatusRejected || tx.ResultBalance().String() != "100.00" || entries(t, w.ID()) != 1 {
				t.Fatalf("stored %s balance %s, %d entries; want REJECTED, 100.00 and the opening only", tx.Status(), tx.ResultBalance(), entries(t, w.ID()))
			}
			causes, _ := eventCauses(t, pending.Tx.ID())
			if !slices.Equal(causes, []string{""}) { // one WagerTransactionRejected, with no reference to cite
				t.Fatalf("causes %v, want one event with no causationId", causes)
			}
		})
		t.Run("by TTL, whatever the attempts", func(t *testing.T) {
			t.Parallel()
			w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
			pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
			f.clock.Advance(11 * time.Minute)
			got := resolve(t, f, pending)
			wantResolved(t, got, app.ResolveRejected, wagering.FailureReferenceNotFound)
			if got.Attempts != 0 || !got.Expired() {
				t.Fatalf("%+v, want an expiration on the first attempt", got)
			}
		})
	})

	t.Run("a reference that is itself pending keeps waiting (R2)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		second := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, second, wagering.StatusPendingReference, "", "", false)
		f.clock.Advance(time.Minute)
		wantResolved(t, resolve(t, f, second), app.ResolveRescheduled, "")
	})

	t.Run("the rules of the reference apply when it arrives (R3–R6)", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name             string
			waiting, arrival op // the reversal that comes first, and what it waits for
			want             wagering.FailureCode
			balance          string // the wallet at the rejection
		}{
			{"R3 reference rejected",
				op{kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-1"}, op{kind: "BET", amount: "500.00", ext: "bet-1"},
				wagering.FailureReferenceNotProcessed, "100.00"},
			{"R4 kind not accepted",
				op{kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "win-1"}, op{kind: "WIN", amount: "30.00", ext: "win-1"},
				wagering.FailureInvalidReferenceKind, "130.00"},
			{"R6 amount differs",
				op{kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-1"}, op{kind: "BET", amount: "30.00", ext: "bet-1"},
				wagering.FailureReversalAmountMismatch, "70.00"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
				tc.waiting.provider, tc.arrival.provider = p, p
				pending := process(t, f.pw, w, tc.waiting)
				wantResult(t, pending, wagering.StatusPendingReference, "", "", false)
				process(t, f.pw, w, tc.arrival)

				wantResolved(t, resolve(t, f, pending), app.ResolveRejected, tc.want)
				if got := stored(t, pending.Tx.ID()).ResultBalance().String(); got != tc.balance {
					t.Fatalf("balance at the rejection %s, want %s", got, tc.balance)
				}
			})
		}
	})

	t.Run("a reference from another wallet is a mismatch (R5)", func(t *testing.T) {
		t.Parallel()
		a, b, f, p := openWallet(t, "100.00"), openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, a, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, b, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResolved(t, resolve(t, f, pending), app.ResolveRejected, wagering.FailureReferenceMismatch)
	})

	t.Run("a second reversal of the same BET is already reversed (R7, C4)", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		refund := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}) // advances both

		wantResolved(t, resolve(t, f, refund), app.ResolveProcessed, "")
		wantResolved(t, resolve(t, f, rollback), app.ResolveRejected, wagering.FailureAlreadyReversed)
		wantWallet(t, w.ID(), "100.00", 3) // the debit was returned once
	})

	t.Run("a chain of pendings resolves in cascade", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		refund := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, rollback, wagering.StatusPendingReference, "", "", false)
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})

		wantResolved(t, resolve(t, f, refund), app.ResolveProcessed, "")   // its conclusion advances the ROLLBACK
		wantResolved(t, resolve(t, f, rollback), app.ResolveProcessed, "") // the ROLLBACK of a REFUND debits again
		wantWallet(t, w.ID(), "70.00", 4)
	})
}
```

Acrescente `"sync"` aos imports (o `testClock` usa `sync.Mutex`).

- [ ] **Passo 2: stub.** Crie `internal/app/resolve_references.go` só com os tipos e métodos que devolvem o valor zero:

```go
package app

import (
	"context"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// ResolveOutcome is what one evaluation of a pending operation did.
type ResolveOutcome string

const (
	// ResolveSkipped: the operation was not pending or not due anymore under
	// the locks; nothing was written (spec M6, decision 3).
	ResolveSkipped     ResolveOutcome = "SKIPPED"
	ResolveProcessed   ResolveOutcome = "PROCESSED"
	ResolveRejected    ResolveOutcome = "REJECTED"
	ResolveRescheduled ResolveOutcome = "RESCHEDULED"
	ResolveFailed      ResolveOutcome = "FAILED"
)

// ResolveResult is the outcome of one Resolve.
type ResolveResult struct {
	Outcome       ResolveOutcome
	TransactionID string
	WalletID      string
	FailureCode   wagering.FailureCode // Rejected only
	Attempts      int
}

// Expired tells whether the operation was rejected because its retry limit or
// its TTL was reached (REFERENCE_NOT_FOUND).
func (r ResolveResult) Expired() bool {
	return r.Outcome == ResolveRejected && r.FailureCode == wagering.FailureReferenceNotFound
}

// ResolveReferences is the use case of the reference worker (lifecycle §6.3).
type ResolveReferences struct{ p *ProcessWager }

// NewResolveReferences reuses the unit of work, clock, ids, policy and
// settlement of the ProcessWager (spec M6, decision 5).
func NewResolveReferences(p *ProcessWager) *ResolveReferences { return &ResolveReferences{p: p} }

// Claim lists the operations due now.
func (r *ResolveReferences) Claim(ctx context.Context, limit int) ([]PendingReference, error) {
	return nil, nil
}

// CountPending counts the operations in PENDING_REFERENCE.
func (r *ResolveReferences) CountPending(ctx context.Context) (int, error) { return 0, nil }

// Resolve evaluates one pending operation.
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error) {
	return ResolveResult{}, nil
}
```

- [ ] **Passo 3: ver falhar.** `go test -tags=integration -race -run '^TestResolveReferences$' ./internal/app/...`.
  Esperado: todos os subtestes falham na asserção `resolved {Outcome: ...}, want PROCESSED ""` (o stub devolve o resultado zero).

- [ ] **Passo 4: a causa por função.** Em `internal/app/process_wager.go`, acrescente logo antes de `settleAndPersist`:

```go
// causation gives the causationId of the events of an evaluation from the
// reference it found (Reference.Tx is nil when there is none).
type causation func(ref wagering.Reference) string

// fixedCause is the message that carries the operation ("" over HTTP).
func fixedCause(id string) causation { return func(wagering.Reference) string { return id } }

// referenceCause is the operation that unblocked a pending one, when it exists
// (messaging §7); empty when the reference was never found.
func referenceCause(ref wagering.Reference) string {
	if ref.Tx == nil {
		return ""
	}
	return ref.Tx.ID()
}
```

Troque a assinatura e o uso em `settleAndPersist`:

```diff
-func (p *ProcessWager) settleAndPersist(ctx context.Context, r Repos, tx *wagering.WagerTransaction, w *wallet.Wallet, now time.Time, insert bool, causationID string) error {
+func (p *ProcessWager) settleAndPersist(ctx context.Context, r Repos, tx *wagering.WagerTransaction, w *wallet.Wallet, now time.Time, insert bool, cause causation) error {
@@
-	envs, err := sealEvents(p.ids, out.Events, tx.CorrelationID(), causationID)
+	envs, err := sealEvents(p.ids, out.Events, tx.CorrelationID(), cause(ref))
```

e a chamada em `attempt`: `p.settleAndPersist(ctx, r, tx, &w, now, true, fixedCause(req.CausationID))`. Atualize o comentário da função: "The reference worker calls it with insert = false and the reference as the cause".

- [ ] **Passo 5: implementar o caso de uso.** Em `resolve_references.go`, troque os três stubs e acrescente os auxiliares (imports: `errors`, `fmt`, `time`, `apperrors`, `wallet`):

```go
// Claim lists the operations due now, oldest first (one statement on the
// pool; the locks come in Resolve).
func (r *ResolveReferences) Claim(ctx context.Context, limit int) ([]PendingReference, error) {
	return r.p.reads.Transactions().ClaimDue(ctx, r.p.clock.Now(), limit)
}

// CountPending counts the operations in PENDING_REFERENCE (the gauge).
func (r *ResolveReferences) CountPending(ctx context.Context) (int, error) {
	return r.p.reads.Transactions().CountPendingReferences(ctx)
}

// Resolve evaluates one pending operation in its own unit of work: lock the
// wallet, then the operation (the order of the HTTP path, data-model §6),
// recheck, and settle it with insert = false. The outcomes are PROCESSED,
// REJECTED (including the expiration), RESCHEDULED and SKIPPED; an error means
// nothing was written and the operation stays due (KindTransient), except a
// permanent failure, which is recorded as FAILED (spec M6, decisions 8 and 9).
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error) {
	tx, err := r.attempt(ctx, ref)
	if apperrors.Classify(err) == apperrors.KindPermanent {
		return r.recordFailure(ctx, ref, err)
	}
	if err != nil {
		return ResolveResult{}, err
	}
	if tx == nil {
		return ResolveResult{Outcome: ResolveSkipped, TransactionID: ref.ID, WalletID: ref.WalletID}, nil
	}
	res := resultOf(tx)
	if res.Outcome == ResolveRescheduled {
		r.p.log.DebugContext(ctx, "reference rescheduled", "transactionId", tx.ID(), "walletId", tx.WalletID(),
			"providerId", tx.ProviderID(), "correlationId", tx.CorrelationID(), "attempts", res.Attempts)
		return res, nil
	}
	r.p.log.InfoContext(ctx, "reference resolved", "transactionId", tx.ID(), "walletId", tx.WalletID(),
		"providerId", tx.ProviderID(), "correlationId", tx.CorrelationID(), "outcome", string(res.Outcome),
		"failureCode", string(res.FailureCode), "attempts", res.Attempts)
	return res, nil
}

// attempt settles the operation, or returns nil when it was skipped.
func (r *ResolveReferences) attempt(ctx context.Context, ref PendingReference) (*wagering.WagerTransaction, error) {
	p := r.p
	now := p.clock.Now()
	var done *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(repos Repos) error {
		w, tx, err := lockPending(ctx, repos, ref, now)
		if err != nil || tx == nil {
			return err
		}
		if err := p.settleAndPersist(ctx, repos, tx, &w, now, false, referenceCause); err != nil {
			return err
		}
		done = tx
		return nil
	})
	if err != nil {
		return nil, err
	}
	return done, nil
}

// lockPending locks the wallet, then the operation, and rechecks under both
// locks: it must still be PENDING_REFERENCE and due. Another instance may have
// concluded it, or rescheduled it after our claim, and then counting a second
// attempt would shorten its wait (spec M6, decision 3). A nil operation means skip.
func lockPending(ctx context.Context, r Repos, ref PendingReference, now time.Time) (wallet.Wallet, *wagering.WagerTransaction, error) {
	w, err := r.Wallets().Lock(ctx, ref.WalletID)
	if err != nil {
		return wallet.Wallet{}, nil, err
	}
	tx, err := r.Transactions().Lock(ctx, ref.ID)
	if errors.Is(err, ErrNotFound) {
		return wallet.Wallet{}, nil, nil
	}
	if err != nil {
		return wallet.Wallet{}, nil, err
	}
	if tx.Status() != wagering.StatusPendingReference || tx.NextAttemptAt().After(now) {
		return wallet.Wallet{}, nil, nil
	}
	return w, tx, nil
}

// resultOf reads the outcome from the state settleAndPersist left.
func resultOf(tx *wagering.WagerTransaction) ResolveResult {
	res := ResolveResult{TransactionID: tx.ID(), WalletID: tx.WalletID(), Attempts: tx.Attempts()}
	switch tx.Status() {
	case wagering.StatusProcessed:
		res.Outcome = ResolveProcessed
	case wagering.StatusRejected:
		res.Outcome, res.FailureCode = ResolveRejected, tx.FailureCode()
	default:
		res.Outcome = ResolveRescheduled
	}
	return res
}

// recordFailure writes the operation as FAILED, in a transaction of its own
// (lock order and recheck as in Resolve), and advances the operations waiting
// for it: no entry, no event (D-05, lifecycle §5.2). If even that cannot be
// written, the failure is transient and the operation stays due.
func (r *ResolveReferences) recordFailure(ctx context.Context, ref PendingReference, cause error) (ResolveResult, error) {
	p := r.p
	now := p.clock.Now()
	var failed *wagering.WagerTransaction
	err := p.uow.Do(ctx, func(repos Repos) error {
		_, tx, err := lockPending(ctx, repos, ref, now)
		if err != nil || tx == nil {
			return err
		}
		if err := tx.Fail(now); err != nil {
			return domainError(err)
		}
		if err := repos.Transactions().Update(ctx, tx); err != nil {
			return err
		}
		if _, err := repos.Transactions().AdvanceDependents(ctx, tx.ProviderID(), tx.ExternalTransactionID(), now); err != nil {
			return err
		}
		failed = tx
		return nil
	})
	if err != nil {
		return ResolveResult{}, apperrors.New(apperrors.KindTransient, "", fmt.Errorf("app: recording FAILED: %w (after %w)", err, cause))
	}
	if failed == nil { // concluded or rescheduled by another instance meanwhile
		return ResolveResult{Outcome: ResolveSkipped, TransactionID: ref.ID, WalletID: ref.WalletID}, nil
	}
	p.log.ErrorContext(ctx, "permanent failure recorded",
		"transactionId", failed.ID(), "walletId", failed.WalletID(), "providerId", failed.ProviderID(),
		"correlationId", failed.CorrelationID(), "error", cause.Error())
	return ResolveResult{Outcome: ResolveFailed, TransactionID: failed.ID(), WalletID: failed.WalletID(), Attempts: failed.Attempts()}, nil
}
```

- [ ] **Passo 6: ver passar.** O comando do Passo 3, verde; depois `go test -tags=integration -race ./internal/app/...` inteiro (a refatoração da causa não pode quebrar o `TestProcessWager` nem o `TestConsumeWager`).

- [ ] **Passo 7: checagem de sensibilidade.** No `settleAndPersist`, troque temporariamente `r.Transactions().Update(ctx, tx)` por `Insert` no ramo `insert == false`. Os subtestes devem falhar por violação de unicidade. Desfaça e confirme o verde (o comentário `// Sensitivity:` já está no teste).

- [ ] **Checkpoint:** `go test -tags=integration -race ./internal/app/...` verde e `go vet ./...` limpo.

---

## Tarefa 5: O recheck sob os locks

**Arquivos:**
- Modificar: `internal/app/resolve_references_integration_test.go`

Esta tarefa cobre o **foco de revisão 1**. O código de produção já existe (Tarefa 4); os testes são escritos sobre comportamento que já existe, então valem a checagem de sensibilidade.

- [ ] **Passo 1: escrever os testes.** No fim do arquivo (acrescente `"sync"` já importado na Tarefa 4):

```go
// Covers: OPS-12, E7 (spec M6, decision 3)
// Sensitivity: removing the tx.NextAttemptAt().After(now) check in lockPending → "rescheduled by another worker" resolves again (attempts 2).
func TestResolveReferencesSkips(t *testing.T) {
	t.Parallel()

	t.Run("not due yet", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		before := stored(t, pending.Tx.ID())
		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
	})

	t.Run("already concluded", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		before := stored(t, pending.Tx.ID())

		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "100.00", 3) // one movement only
	})

	t.Run("rescheduled by another worker after the claim", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
		f.clock.Advance(time.Minute)
		wantResolved(t, resolve(t, f, pending), app.ResolveRescheduled, "") // the other worker
		before := stored(t, pending.Tx.ID())

		// This worker claimed the same operation before that, and only now gets the locks.
		wantResolved(t, resolve(t, f, pending), app.ResolveSkipped, "")
		wantUnchanged(t, before)
		if got := before.Attempts(); got != 1 {
			t.Fatalf("attempts %d, want 1: the second evaluation must not count", got)
		}
	})
}

// Covers: OPS-12, E7 (spec M6, decision 3)
// Sensitivity: without the horizon check, both evaluations reschedule and attempts is 2.
//
// Two instances evaluate the same due operation at once: one attempt is
// counted, whoever gets the locks first.
func TestResolveReferencesConcurrent(t *testing.T) {
	t.Parallel()
	w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
	pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-missing"})
	f.clock.Advance(time.Minute)
	ref := refOf(pending)

	results := make([]app.ResolveResult, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() {
			<-start
			results[i], errs[i] = f.rr.Resolve(t.Context(), ref)
		})
	}
	close(start)
	wg.Wait()

	outcomes := map[app.ResolveOutcome]int{}
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("Resolve %d: %v", i, errs[i])
		}
		outcomes[results[i].Outcome]++
	}
	if outcomes[app.ResolveRescheduled] != 1 || outcomes[app.ResolveSkipped] != 1 {
		t.Fatalf("outcomes %v, want one rescheduled and one skipped", outcomes)
	}
	if got := stored(t, ref.ID).Attempts(); got != 1 {
		t.Fatalf("attempts %d, want 1", got)
	}
}
```

- [ ] **Passo 2: ver passar** (o comportamento já existe): `go test -tags=integration -race -count=3 -run 'TestResolveReferencesSkips|TestResolveReferencesConcurrent' ./internal/app/...` verde nas 3 execuções.

- [ ] **Passo 3: checagem de sensibilidade.** Em `lockPending`, remova temporariamente `|| tx.NextAttemptAt().After(now)`. Confirme que falham **os dois**: `Skips/"not due yet"` e `"rescheduled by another worker…"` (attempts 2), e `Concurrent` (`outcomes map[RESCHEDULED:2]`). Desfaça e confirme o verde.

- [ ] **Checkpoint:** `go test -tags=integration -race ./internal/app/...` verde.

---

## Tarefa 6: Falhas do worker

**Arquivos:**
- Modificar: `internal/app/resolve_references_integration_test.go`

Cobre os **focos 2 e 6**. Os decoradores de `faults_integration_test.go` (`faultyUoW`, `faultyRepos`, `failingOutbox`) já existem.

- [ ] **Passo 1: escrever os testes.** Acrescente os imports `"context"`, `"errors"` e `"github.com/KaioVinicios/pda/internal/apperrors"`, e no fim do arquivo:

```go
// faultyResolver resolves with a ProcessWager whose outbox fails with err in
// the first unit of work only, so that the FAILED write of the second one goes
// through. The clock is the fixture's.
func faultyResolver(f fixture, err error) *app.ResolveReferences {
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, call int) app.Repos {
		if call != 1 {
			return r
		}
		return faultyRepos{Repos: r, outbox: failingOutbox{OutboxRepository: r.Outbox(), err: err}}
	}}
	return app.NewResolveReferences(app.NewProcessWager(uow, reads(), f.clock, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler)))
}

// Covers: TX-06, D-05, OPS-12 (spec M6, decisions 8 and 9)
func TestResolveReferencesFailures(t *testing.T) {
	t.Parallel()

	t.Run("a permanent failure records FAILED and releases the dependents", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		rollback := process(t, f.pw, w, op{provider: p, kind: "ROLLBACK", amount: "30.00", ext: "rollback-1", ref: "refund-1"})
		wantResult(t, rollback, wagering.StatusPendingReference, "", "", false) // waits for the REFUND (R2)
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})

		permanent := apperrors.New(apperrors.KindPermanent, "", errors.New("outbox broken"))
		got, err := faultyResolver(f, permanent).Resolve(t.Context(), refOf(pending))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		wantResolved(t, got, app.ResolveFailed, "")
		tx := stored(t, pending.Tx.ID())
		if tx.Status() != wagering.StatusFailed || tx.FailureCode() != wagering.FailureInternalPermanentFailure {
			t.Fatalf("REFUND = %s %s, want FAILED INTERNAL_PERMANENT_FAILURE", tx.Status(), tx.FailureCode())
		}
		wantWallet(t, w.ID(), "70.00", 2) // nothing moved
		if n := entries(t, w.ID()); n != 2 {
			t.Fatalf("%d ledger entries, want the opening and the BET only", n)
		}
		if causes, _ := eventCauses(t, pending.Tx.ID()); len(causes) != 0 {
			t.Fatalf("FAILED emitted %d events, want none", len(causes))
		}
		// The ROLLBACK that waited for it is released at once and rejected.
		wantResolved(t, resolve(t, f, rollback), app.ResolveRejected, wagering.FailureReferenceNotProcessed)
	})

	t.Run("a transient failure leaves the operation due", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		before := stored(t, pending.Tx.ID())

		_, err := faultyResolver(f, errors.New("outbox down")).Resolve(t.Context(), refOf(pending))
		if apperrors.Classify(err) != apperrors.KindTransient {
			t.Fatalf("error = %v (%s), want transient", err, apperrors.Classify(err))
		}
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "70.00", 2)
		// Nothing was lost: the next cycle resolves it.
		wantResolved(t, resolve(t, f, pending), app.ResolveProcessed, "")
		wantWallet(t, w.ID(), "100.00", 3)
	})

	t.Run("a canceled context writes nothing", func(t *testing.T) {
		t.Parallel()
		w, f, p := openWallet(t, "100.00"), newFixture(), newProvider()
		pending := process(t, f.pw, w, op{provider: p, kind: "REFUND", amount: "30.00", ext: "refund-1", ref: "bet-1"})
		process(t, f.pw, w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"})
		before := stored(t, pending.Tx.ID())

		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, err := f.rr.Resolve(ctx, refOf(pending))
		if apperrors.Classify(err) != apperrors.KindTransient {
			t.Fatalf("error = %v (%s), want transient", err, apperrors.Classify(err))
		}
		wantUnchanged(t, before)
		wantWallet(t, w.ID(), "70.00", 2)
	})
}
```

- [ ] **Passo 2: ver passar** (o comportamento já existe): `go test -tags=integration -race -count=3 -run 'TestResolveReferencesFailures' ./internal/app/...` verde.

- [ ] **Passo 3: checagem de sensibilidade.** (a) Em `recordFailure`, remova temporariamente a chamada a `AdvanceDependents`: o primeiro subteste deve falhar no `resolve(... rollback)` (`SKIPPED`, porque o horário do ROLLBACK ainda está no futuro). (b) Em `Resolve`, troque `KindPermanent` por `KindNotFound` na checagem: o primeiro subteste deve falhar (o erro sobe em vez de gravar `FAILED`). Desfaça as duas e confirme o verde.

- [ ] **Checkpoint:** `go test -tags=integration -race ./internal/app/...` verde.

---

## Tarefa 7: O loop do worker

**Arquivos:**
- Criar: `internal/adapters/references/worker.go`, `internal/adapters/references/backoff.go`
- Criar: `internal/adapters/references/backoff_test.go`, `internal/adapters/references/worker_test.go`

**Interfaces:**
- Consome: `app.PendingReference`, `app.ResolveResult` (Tarefa 4).
- Produz:

```go
type Resolver interface {
	Claim(ctx context.Context, limit int) ([]app.PendingReference, error)
	Resolve(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error)
	CountPending(ctx context.Context) (int, error)
}
type Metrics interface { ReferenceRetried(); ReferenceExpired(); ReferencePending(n int) }
type Options struct { BatchSize int; PollInterval time.Duration }
func NewWorker(r Resolver, m Metrics, log *slog.Logger, opts Options) *Worker
func (w *Worker) Run(ctx context.Context)
```

- [ ] **Passo 1: escrever o teste do backoff.** `internal/adapters/references/backoff_test.go` (pacote interno):

```go
package references

import (
	"testing"
	"time"
)

// Covers: OPS-12 (spec M6, decision 11; messaging.md §5.3)
func TestNextClaimDelay(t *testing.T) {
	for _, tc := range []struct{ prev, want time.Duration }{
		{0, time.Second},
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second},
	} {
		if got := nextClaimDelay(tc.prev); got != tc.want {
			t.Errorf("nextClaimDelay(%v) = %v, want %v", tc.prev, got, tc.want)
		}
	}
}
```

- [ ] **Passo 2: escrever os testes do loop.** `internal/adapters/references/worker_test.go` (pacote externo, sem infraestrutura: um `Resolver` dublê, **não** o banco; o comportamento com o banco é da Tarefa 11):

```go
package references_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// fakeResolver scripts a Resolver: claim decides what each Claim returns.
type fakeResolver struct {
	mu      sync.Mutex
	claims  []time.Time
	claim   func(call int) ([]app.PendingReference, error)
	resolve func(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error)
	pending int
	counted atomic.Int32
}

func (f *fakeResolver) Claim(context.Context, int) ([]app.PendingReference, error) {
	f.mu.Lock()
	f.claims = append(f.claims, time.Now())
	call := len(f.claims)
	f.mu.Unlock()
	return f.claim(call)
}

func (f *fakeResolver) Resolve(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error) {
	if f.resolve == nil {
		return app.ResolveResult{Outcome: app.ResolveProcessed, TransactionID: ref.ID}, nil
	}
	return f.resolve(ctx, ref)
}

func (f *fakeResolver) CountPending(context.Context) (int, error) {
	f.counted.Add(1)
	return f.pending, nil
}

func (f *fakeResolver) claimTimes() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.claims...)
}

type fakeMetrics struct{ retried, expired, pending atomic.Int32 }

func (m *fakeMetrics) ReferenceRetried()      { m.retried.Add(1) }
func (m *fakeMetrics) ReferenceExpired()      { m.expired.Add(1) }
func (m *fakeMetrics) ReferencePending(n int) { m.pending.Store(int32(n)) }

func refs(ids ...string) []app.PendingReference {
	out := make([]app.PendingReference, len(ids))
	for i, id := range ids {
		out[i] = app.PendingReference{ID: id, WalletID: "wallet-" + id}
	}
	return out
}

// run starts the worker; stop cancels it and waits for Run to return.
func run(t *testing.T, r references.Resolver, m references.Metrics, opts references.Options) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	w := references.NewWorker(r, m, slog.New(slog.DiscardHandler), opts)
	go func() {
		defer close(done)
		w.Run(ctx)
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after the cancellation")
		}
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s: not reached within %v", what, timeout)
}

// Covers: OPS-12, TX-09 (spec M6, decision 1)
//
// A full batch without errors is claimed again at once; with a poll interval
// of an hour, only that can produce three claims in two seconds.
func TestWorkerRepeatsFullCleanBatch(t *testing.T) {
	t.Parallel()
	f := &fakeResolver{claim: func(int) ([]app.PendingReference, error) { return refs("a", "b"), nil }}
	stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 2, PollInterval: time.Hour})
	defer stop()
	eventually(t, 2*time.Second, "three claims", func() bool { return len(f.claimTimes()) >= 3 })
}

// Covers: OPS-12 (spec M6, decision 1)
func TestWorkerWaitsBetweenBatches(t *testing.T) {
	t.Parallel()
	cases := map[string]*fakeResolver{
		"an empty batch": {claim: func(int) ([]app.PendingReference, error) { return nil, nil }},
		"a short batch":  {claim: func(int) ([]app.PendingReference, error) { return refs("a"), nil }},
		"a full batch with an error": {
			claim: func(int) ([]app.PendingReference, error) { return refs("a", "b"), nil },
			resolve: func(_ context.Context, ref app.PendingReference) (app.ResolveResult, error) {
				if ref.ID == "a" {
					return app.ResolveResult{}, context.DeadlineExceeded
				}
				return app.ResolveResult{Outcome: app.ResolveProcessed}, nil
			},
		},
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 2, PollInterval: 300 * time.Millisecond})
			defer stop()
			eventually(t, time.Second, "the first claim", func() bool { return len(f.claimTimes()) == 1 })
			// A negative window: nothing may happen before the poll interval (the only sleep of this file).
			time.Sleep(150 * time.Millisecond)
			if n := len(f.claimTimes()); n != 1 {
				t.Fatalf("%d claims before the poll interval, want 1", n)
			}
			eventually(t, 2*time.Second, "the second claim", func() bool { return len(f.claimTimes()) >= 2 })
		})
	}
}

// Covers: OPS-12 (spec M6, decision 11; ARCHITECTURE.md §12)
//
// The database is down for the first claim: the worker waits 1 s and tries
// again; after a success, the next failure waits 1 s again, not 2 s.
func TestWorkerClaimBackoff(t *testing.T) {
	t.Parallel()
	f := &fakeResolver{claim: func(call int) ([]app.PendingReference, error) {
		if call == 1 || call == 3 {
			return nil, context.DeadlineExceeded
		}
		return nil, nil
	}}
	stop := run(t, f, &fakeMetrics{}, references.Options{BatchSize: 1, PollInterval: 10 * time.Millisecond})
	defer stop()
	eventually(t, 6*time.Second, "four claims", func() bool { return len(f.claimTimes()) >= 4 })

	at := f.claimTimes()
	for _, gap := range []struct {
		name     string
		from, to int
	}{{"after the first failure", 0, 1}, {"after the failure that follows a success", 2, 3}} {
		got := at[gap.to].Sub(at[gap.from])
		if got < 950*time.Millisecond || got > 1800*time.Millisecond {
			t.Errorf("%s: waited %v, want about 1s", gap.name, got)
		}
	}
}

// Covers: OBS-03 (spec M6, decision 13)
func TestWorkerMetrics(t *testing.T) {
	t.Parallel()
	byID := map[string]app.ResolveResult{
		"resched":   {Outcome: app.ResolveRescheduled},
		"expired":   {Outcome: app.ResolveRejected, FailureCode: wagering.FailureReferenceNotFound},
		"rejected":  {Outcome: app.ResolveRejected, FailureCode: wagering.FailureAlreadyReversed},
		"processed": {Outcome: app.ResolveProcessed},
		"skipped":   {Outcome: app.ResolveSkipped},
	}
	f := &fakeResolver{
		pending: 7,
		claim: func(call int) ([]app.PendingReference, error) {
			if call == 1 {
				return refs("resched", "expired", "rejected", "processed", "skipped"), nil
			}
			return nil, nil
		},
		resolve: func(_ context.Context, ref app.PendingReference) (app.ResolveResult, error) { return byID[ref.ID], nil },
	}
	m := &fakeMetrics{}
	stop := run(t, f, m, references.Options{BatchSize: 10, PollInterval: time.Millisecond})
	defer stop()
	eventually(t, 2*time.Second, "the batch resolved", func() bool { return len(f.claimTimes()) >= 20 })

	if m.retried.Load() != 1 || m.expired.Load() != 1 || m.pending.Load() != 7 {
		t.Fatalf("retried %d expired %d pending %d, want 1 1 7", m.retried.Load(), m.expired.Load(), m.pending.Load())
	}
	// Twenty claims took a few milliseconds: the gauge is refreshed once a second at most.
	if n := f.counted.Load(); n != 1 {
		t.Fatalf("CountPending called %d times, want 1", n)
	}
}

// Covers: FX-03, OPS-12 (spec M6, decision 10)
//
// Not parallel: goleak sees every goroutine of the process.
//
// On the cancellation, the item in flight finishes with a context that is
// still alive, and no other begins.
func TestWorkerStop(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	entered, release := make(chan struct{}), make(chan struct{})
	var resolved atomic.Int32
	var itemCtxErr atomic.Value
	f := &fakeResolver{
		claim: func(call int) ([]app.PendingReference, error) {
			if call == 1 {
				return refs("a", "b", "c"), nil
			}
			return nil, nil
		},
		resolve: func(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error) {
			if resolved.Add(1) == 1 {
				close(entered)
				<-release
			}
			itemCtxErr.Store(ctx.Err() == nil)
			return app.ResolveResult{Outcome: app.ResolveProcessed}, nil
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	w := references.NewWorker(f, &fakeMetrics{}, slog.New(slog.DiscardHandler), references.Options{BatchSize: 10, PollInterval: time.Hour})
	go func() {
		defer close(done)
		w.Run(ctx)
	}()

	<-entered
	cancel()
	select {
	case <-done:
		t.Fatal("Run returned with an item in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the item in flight finished")
	}
	if n := resolved.Load(); n != 1 {
		t.Fatalf("%d items resolved, want only the one in flight", n)
	}
	if alive, _ := itemCtxErr.Load().(bool); !alive {
		t.Fatal("the item in flight saw its context canceled")
	}
}
```

- [ ] **Passo 3: stubs, para o red ser uma asserção.** Crie `backoff.go`:

```go
package references

import "time"

const (
	claimRetryMin = time.Second
	claimRetryMax = 30 * time.Second
)

// nextClaimDelay is the wait after one more failed claim.
func nextClaimDelay(prev time.Duration) time.Duration { return 0 }
```

e `worker.go` com os tipos e um `Run` que só espera o cancelamento:

```go
// Package references runs the reference worker (D-11): it claims the due
// PENDING_REFERENCE operations and resolves them one at a time through the
// ResolveReferences use case, on any instance.
package references

import (
	"context"
	"log/slog"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// Resolver is the use case the worker drives (*app.ResolveReferences).
type Resolver interface {
	Claim(ctx context.Context, limit int) ([]app.PendingReference, error)
	Resolve(ctx context.Context, ref app.PendingReference) (app.ResolveResult, error)
	CountPending(ctx context.Context) (int, error)
}

// Metrics is what the worker reports (ARCHITECTURE.md §13.2).
type Metrics interface {
	ReferenceRetried()
	ReferenceExpired()
	ReferencePending(n int)
}

// Options are the worker settings (spec M6, decision 12).
type Options struct {
	BatchSize    int
	PollInterval time.Duration
}

// Worker is the reference worker of one instance.
type Worker struct {
	resolver Resolver
	metrics  Metrics
	log      *slog.Logger
	opts     Options
}

// NewWorker builds a worker; Run starts it.
func NewWorker(r Resolver, m Metrics, log *slog.Logger, opts Options) *Worker {
	return &Worker{resolver: r, metrics: m, log: log, opts: opts}
}

// Run resolves until ctx is canceled.
func (w *Worker) Run(ctx context.Context) { <-ctx.Done() }
```

- [ ] **Passo 4: ver falhar.** `go test -race ./internal/adapters/references/...`.
  Esperado: `TestNextClaimDelay` falha (`nextClaimDelay(0) = 0s, want 1s`), `TestWorkerRepeatsFullCleanBatch` falha (`three claims: not reached within 2s`), os demais falham por claims que nunca acontecem, e `TestWorkerStop` trava até o `<-entered` (o timeout do `go test` o derruba; se preferir um red rápido, rode `-run` sem ele).

- [ ] **Passo 5: implementar.** Em `backoff.go`:

```go
// nextClaimDelay is the wait after one more failed claim, the database being
// unavailable: 1 s, 2 s, 4 s… up to 30 s (spec M6, decision 11). prev is 0
// after a successful claim.
func nextClaimDelay(prev time.Duration) time.Duration {
	return min(max(2*prev, claimRetryMin), claimRetryMax)
}
```

Em `worker.go`, troque o `Run` e acrescente o resto (imports: `context`, `log/slog`, `time`, `app`, `wagering`):

```go
const (
	// itemTimeout bounds one resolution, which runs detached from the
	// cancellation of the loop (spec M6, decision 10).
	itemTimeout = 10 * time.Second
	// gaugeEvery bounds how often the pending gauge is refreshed.
	gaugeEvery = time.Second
)

// Worker is the reference worker of one instance.
type Worker struct {
	resolver Resolver
	metrics  Metrics
	log      *slog.Logger
	opts     Options

	lastGauge time.Time // only read and written by Run
}

// Run claims a batch, resolves its items in sequence and waits for the poll
// interval, unless the batch was full and had no errors. After the
// cancellation no claim and no item begins; the item in flight finishes
// (spec M6, decisions 1, 10 and 11).
func (w *Worker) Run(ctx context.Context) {
	var claimDelay time.Duration
	for ctx.Err() == nil {
		w.refreshPending(ctx)
		batch, err := w.resolver.Claim(ctx, w.opts.BatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			claimDelay = nextClaimDelay(claimDelay)
			w.log.Warn("reference claim failed", "error", err.Error(), "retryIn", claimDelay.String())
			sleep(ctx, claimDelay)
			continue
		}
		claimDelay = 0
		failed := w.resolveBatch(ctx, batch)
		if len(batch) < w.opts.BatchSize || failed > 0 {
			sleep(ctx, w.opts.PollInterval)
		}
	}
}

// resolveBatch resolves the items in order and returns how many failed.
func (w *Worker) resolveBatch(ctx context.Context, batch []app.PendingReference) (failed int) {
	for _, ref := range batch {
		if ctx.Err() != nil {
			return failed
		}
		if !w.resolve(ctx, ref) {
			failed++
		}
	}
	return failed
}

// resolve runs one item detached from ctx's cancellation and counts the
// outcome. It returns false when the resolution failed: the operation stays
// due and comes back in the next cycle (spec M6, decision 9).
func (w *Worker) resolve(ctx context.Context, ref app.PendingReference) bool {
	itemCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), itemTimeout)
	defer cancel()
	res, err := w.resolver.Resolve(itemCtx, ref)
	if err != nil {
		w.log.Warn("reference resolution failed", "transactionId", ref.ID, "walletId", ref.WalletID, "error", err.Error())
		return false
	}
	switch {
	case res.Outcome == app.ResolveRescheduled:
		w.metrics.ReferenceRetried()
	case res.Expired():
		w.metrics.ReferenceExpired()
	}
	return true
}

// refreshPending updates the gauge at most once per gaugeEvery. A failure
// keeps the previous value: the claim reports the database being down.
func (w *Worker) refreshPending(ctx context.Context) {
	if time.Since(w.lastGauge) < gaugeEvery {
		return
	}
	n, err := w.resolver.CountPending(ctx)
	if err != nil {
		return
	}
	w.lastGauge = time.Now()
	w.metrics.ReferencePending(n)
}

// sleep waits for d or until ctx is canceled.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
```

(Apague a declaração antiga do struct `Worker` do stub: fica só a que tem `lastGauge`.)

- [ ] **Passo 6: ver passar.** `go test -race -count=1 ./internal/adapters/references/...` verde (~2,5 s, por causa do `TestWorkerClaimBackoff`).

- [ ] **Passo 7: checagem de sensibilidade.** (a) Troque `failed > 0` por `false` na condição do `Run`: `TestWorkerWaitsBetweenBatches/"a full batch with an error"` deve falhar. (b) Chame `Resolve` com `ctx` em vez de `itemCtx`: `TestWorkerStop` deve falhar (`the item in flight saw its context canceled`). (c) Remova o `ctx.Err() != nil` do `resolveBatch`: `TestWorkerStop` deve falhar (`3 items resolved`). Desfaça as três e confirme o verde.

- [ ] **Checkpoint:** `go test -race ./internal/adapters/references/...` verde e `go vet ./...` limpo.

---

## Tarefa 8: Módulo Fx, grafo e os primeiros testes de ponta a ponta

**Arquivos:**
- Criar: `internal/adapters/references/module.go`
- Modificar: `internal/bootstrap/app_module.go`, `internal/bootstrap/bootstrap.go`, `internal/bootstrap/bootstrap_test.go`
- Modificar: `test/integration/helpers_test.go`, `test/integration/wagering_test.go`, `test/integration/events_test.go`
- Criar: `test/integration/references_test.go`

**Interfaces:**
- Consome: `app.NewResolveReferences` (Tarefa 4), `references.NewWorker` (Tarefa 7), `*observability.Metrics` (Tarefa 2), `config.Config.ReferenceBatchSize/PollInterval` (Tarefa 1).
- Produz: `references.Module`, e o helper de teste `waitStatus(t, c, provider, ext, status) testkit.Transaction`.

- [ ] **Passo 1: o grafo (red 1).** Em `internal/bootstrap/bootstrap_test.go`, importe `"github.com/KaioVinicios/pda/internal/adapters/references"`, acrescente as variáveis e ao `fx.Populate`, e atualize o comentário:

```go
		resolve   *app.ResolveReferences
		worker    *references.Worker
```

```go
		&consume, &consumer, &resolve, &worker))
```

Comentário: `// Covers: TST-I07, FX-01 (I07a — …, M5 consumer, M6 reference worker)` e `// Sensitivity (M6): references.Module out of bootstrap.Options → "missing type: *references.Worker".`

Rode `go test -race -run '^TestFxGraph$' ./internal/bootstrap/...`.
Esperado: falha `fx.ValidateApp() = … missing type: *app.ResolveReferences` (e, depois de o registrar, `*references.Worker`).

- [ ] **Passo 2: os testes de ponta a ponta (red 2).** Em `test/integration/helpers_test.go`, acrescente `"context"` e `"time"` aos imports e o helper:

```go
// waitStatus polls the operation of provider until it has the status, and
// returns it. The wait has a deadline: the reference worker resolves within
// seconds with the test times of test-plan §3.3.
func waitStatus(t *testing.T, c *testkit.Client, provider, ext, status string) testkit.Transaction {
	t.Helper()
	var tx testkit.Transaction
	testkit.Eventually(t, 10*time.Second, ext+" "+status, func(context.Context) (bool, error) {
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/" + provider + "/wagering/transactions/" + ext})
		if resp.Status != http.StatusOK {
			return false, nil
		}
		resp.JSON(t, &tx)
		return tx.Status == status, nil
	})
	return tx
}
```

Em `test/integration/wagering_test.go`, troque o último subteste de `TestReversalRules` ("a reversal before its reference waits") por:

```go
	t.Run("a reversal before its reference waits and the worker resolves it (C2)", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		bet, refund := unique("bet"), unique("refund")
		pending := result(t, a, wager(w, "provider-a", "REFUND", "10.00", refund, bet), http.StatusAccepted)
		wantResult(t, pending, "PENDING_REFERENCE", "", "", false)
		var tx testkit.Transaction
		a.Do(t, testkit.Request{Method: http.MethodGet, Path: "/wagering/transactions/" + pending.TransactionID}).JSON(t, &tx)
		if tx.Status != "PENDING_REFERENCE" || tx.Attempts == nil || tx.NextAttemptAt == nil || tx.ExpiresAt == nil ||
			tx.Balance != nil || tx.CompletedAt != nil {
			t.Fatalf("pending operation = %+v", tx)
		}

		wantResult(t, result(t, a, wager(w, "provider-a", "BET", "10.00", bet, ""), http.StatusOK), "PROCESSED", "", "90.00", false)
		done := waitStatus(t, a, "provider-a", refund, "PROCESSED")
		if done.Balance == nil || done.Balance.Amount != "100.00" || done.ReferenceTransactionID == "" {
			t.Fatalf("resolved operation = %+v, want PROCESSED with balance 100.00 and its reference", done)
		}
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("100.00") || got.Version != 3 {
			t.Fatalf("wallet = %+v, want 100.00 v3", got)
		}
	})

	t.Run("a REFUND and a ROLLBACK of a WIN or of a REFUND follow the reference kinds", func(t *testing.T) {
		t.Parallel()
		w := server.OpenWallet(t, testkit.BRL("100.00"))
		win, bet, refund := unique("win"), unique("bet"), unique("refund")
		result(t, a, wager(w, "provider-a", "WIN", "20.00", win, ""), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "REFUND", "20.00", unique("refund"), win), http.StatusUnprocessableEntity),
			"REJECTED", "INVALID_REFERENCE_KIND", "120.00", false) // a REFUND reverses a BET only
		result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
		result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, bet), http.StatusOK)
		wantResult(t, result(t, a, wager(w, "provider-a", "ROLLBACK", "30.00", unique("rollback"), refund), http.StatusOK),
			"PROCESSED", "", "90.00", false)
	})
```

Crie `test/integration/references_test.go`:

```go
//go:build integration

package integration_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: OPS-12, OUT-07, OUT-08 (C2 over HTTP)
//
// The REFUND that waited is resolved by the worker, and its events reach the
// audit queue citing the BET that unblocked it.
func TestPendingReferenceResolved(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet, refund := unique("bet"), unique("refund")
	pending := result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, bet), http.StatusAccepted)
	betResult := result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
	waitStatus(t, a, "provider-a", refund, "PROCESSED")

	causes := map[string][]string{}
	for _, env := range eventsOf(t, w.ID) {
		data, _ := env["data"].(map[string]any)
		if data["transactionId"] != pending.TransactionID {
			continue
		}
		cause, _ := env["causationId"].(string)
		typ, _ := env["eventType"].(string)
		causes[typ] = append(causes[typ], cause)
	}
	want := map[string][]string{
		"WagerTransactionPendingReference": {""}, // emitted when it arrived
		"WagerTransactionProcessed":        {betResult.TransactionID},
		"WalletBalanceChanged":             {betResult.TransactionID},
	}
	if !reflect.DeepEqual(causes, want) {
		t.Fatalf("events of the REFUND (type → causationId) = %v, want %v", causes, want)
	}
}
```

Em `test/integration/events_test.go`, o REFUND pendente sem BET agora é rejeitado pelo worker em cerca de 0,7 s, o que pode acrescentar um décimo evento antes da leitura:

```diff
-	if len(ids) != 9 {
-		t.Fatalf("outbox of the wallet has %d events, want 9", len(ids))
+	// The REFUND without a BET waits for its reference; if the worker already
+	// rejected it, its WagerTransactionRejected is a tenth event.
+	if len(ids) != 9 && len(ids) != 10 {
+		t.Fatalf("outbox of the wallet has %d events, want 9 (or 10 once the worker expired the REFUND)", len(ids))
```

Rode `go test -tags=integration -race -run 'TestReversalRules|TestPendingReferenceResolved' ./test/integration/...`.
Esperado: os dois falham em `waitStatus`: `<ext> PROCESSED: not reached within 10s` (o REFUND nunca é resolvido, porque o worker ainda não está no grafo). O subteste da tabela de tipos de referência passa (comportamento existente do M3).

- [ ] **Passo 3: implementar o módulo.** Crie `internal/adapters/references/module.go`:

```go
package references

import (
	"context"
	"fmt"
	"log/slog"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the reference worker of this instance (D-11, D-15): the loop
// starts with the application and, on stop, finishes the item in flight
// before the pool closes.
var Module = fx.Module("references",
	fx.Provide(newModuleWorker),
	fx.Invoke(func(*Worker) {}),
)

func newModuleWorker(lc fx.Lifecycle, cfg config.Config, r *app.ResolveReferences, m *observability.Metrics, log *slog.Logger) *Worker {
	w := NewWorker(r, m, log, Options{BatchSize: cfg.ReferenceBatchSize, PollInterval: cfg.ReferencePollInterval})
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				w.Run(run)
			}()
			log.Info("reference worker started")
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("reference worker stopping")
			cancel()
			select {
			case <-done:
				log.Info("reference worker stopped")
				return nil
			case <-ctx.Done():
				return fmt.Errorf("reference worker: stop: %w", ctx.Err())
			}
		},
	})
	return w
}
```

Em `internal/bootstrap/app_module.go`, dentro do `fx.Provide(...)`, depois de `app.NewProcessWager`: `app.NewResolveReferences,`.

Em `internal/bootstrap/bootstrap.go`: importe `"github.com/KaioVinicios/pda/internal/adapters/references"` e, em `Options()`, entre `appModule` e `outbox.Module`:

```go
		appModule,
		references.Module,
		outbox.Module,
```

Atualize o comentário do `Options()`: "…dependencies first, then the workers (references, outbox, consumer)…".

- [ ] **Passo 4: ver passar.**
  - `go test -race ./internal/bootstrap/...` verde (o `TestFxGraph` valida o grafo).
  - `go test -tags=integration -race -run 'TestReversalRules|TestPendingReferenceResolved|TestEventContracts' ./test/integration/...` verde.
  - `go test -tags=integration -race ./internal/bootstrap/...` verde (o `TestFxLifecycle` prova que o worker sobe e para com o resto, com `goleak` limpo).

- [ ] **Passo 5: rodar tudo e olhar os efeitos do worker no grafo.** `make test-integration` (~1 a 2 min). O worker agora expira as pendências dos testes existentes: confirme que **nenhum** teste anterior quebrou. Se algum quebrar (por exemplo, um que assume a pendência intacta por mais de 0,7 s), **pare**: ajuste o teste como o `events_test.go` acima, registre o achado no diário e siga.

- [ ] **Passo 6: checagem de sensibilidade.** Remova `references.Module` de `bootstrap.Options()`: o `TestFxGraph` deve falhar (`missing type: *references.Worker`) e o `TestPendingReferenceResolved`, por `waitStatus` (timeout). Desfaça e confirme o verde. Registre acima do `TestPendingReferenceResolved`: `// Sensitivity: references.Module out of bootstrap.Options → waitStatus times out (the REFUND stays PENDING_REFERENCE).`

- [ ] **Checkpoint:** `make check` verde e `make test-integration` verde.

---

## Tarefa 9: `StartApp` com opções, I06 e I06b

**Arquivos:**
- Modificar: `test/testkit/app.go`
- Criar: `test/integration/restart_test.go`

**Interfaces:**
- Produz: `func (e *Env) StartApp(ctx context.Context, opts ...func(*config.Config)) (*App, func(), error)`. As opções ajustam a configuração depois dos tempos de teste e antes do `Validate`. Os chamadores existentes não mudam.

- [ ] **Passo 1: escrever os testes.** Crie `test/integration/restart_test.go`. Os dois testes sobem **dois apps sobre o mesmo banco**, um depois do outro, cada um com fila e tópico próprios (o `StartApp` cria os dois). Por isso **não** usam o `AssertWalletConsistent` de `App.OpenWallet` (a entrega dos eventos é conferida na fila de auditoria do app que os publicou, que já parou): a carteira é aberta pela API e conferida por SQL.

```go
//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/test/testkit"
)

// openWalletVia opens a wallet through the API of one app, as the internal
// service, without the delivery check of App.OpenWallet.
func openWalletVia(t *testing.T, a *testkit.App, initial testkit.Money) testkit.Wallet {
	t.Helper()
	resp := a.Client(t, "wallet-service").Do(t, testkit.Request{Method: http.MethodPost, Path: "/wallets", Body: map[string]any{
		"playerId": testkit.NewID(), "initialBalance": initial,
	}})
	if resp.Status != http.StatusCreated {
		t.Fatalf("POST /wallets = %d %s", resp.Status, resp.Body)
	}
	var w testkit.Wallet
	resp.JSON(t, &w)
	return w
}

// assertStoredConsistent runs the SQL checks of test-plan §6 (items 2–7) on
// the wallet: the ledger and the event matrix of its operations.
func assertStoredConsistent(t *testing.T, e *testkit.Env, walletID string) {
	t.Helper()
	for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){testkit.LedgerProblems, testkit.OutboxProblems} {
		problems, err := check(t.Context(), e.Owner, walletID)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems {
			t.Errorf("wallet %s: %s", walletID, p)
		}
	}
}

// Covers: TST-I06, IDEM-01, OPS-12, E6 (I06)
// Sensitivity: removing references.Module from bootstrap.Options → the second app never resolves the REFUND (waitStatus times out).
//
// The first app records a REFUND before its BET and stops. The second app,
// started over the same database, receives the BET and its worker resolves the
// pending operation from the schedule the database kept; the replays return
// what was recorded.
func TestRecoveryAfterRestart(t *testing.T) {
	e := testkit.NewTestEnv(t, "restart")
	longWait := func(c *config.Config) { c.ReferenceMaxAttempts, c.ReferenceTTL = 50, 2*time.Minute }

	first, stopFirst, err := e.StartApp(t.Context(), longWait)
	if err != nil {
		t.Fatal(err)
	}
	stoppedFirst := false
	t.Cleanup(func() {
		if !stoppedFirst {
			stopFirst()
		}
	})
	w := openWalletVia(t, first, testkit.BRL("100.00"))
	bet, refund := unique("bet"), unique("refund")
	betRequest := wager(w, "provider-a", "BET", "30.00", bet, "")
	refundRequest := wager(w, "provider-a", "REFUND", "30.00", refund, bet)
	wantResult(t, result(t, first.Client(t, "provider-a"), refundRequest, http.StatusAccepted), "PENDING_REFERENCE", "", "", false)
	stopFirst()
	stoppedFirst = true

	second, stopSecond, err := e.StartApp(t.Context(), longWait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSecond)
	a := second.Client(t, "provider-a")
	wantResult(t, result(t, a, betRequest, http.StatusOK), "PROCESSED", "", "70.00", false)
	done := waitStatus(t, a, "provider-a", refund, "PROCESSED")
	if done.Balance == nil || done.Balance.Amount != "100.00" {
		t.Fatalf("REFUND after the restart = %+v, want PROCESSED with balance 100.00", done)
	}

	// The replays return what was recorded, by another instance than the first.
	wantResult(t, result(t, a, betRequest, http.StatusOK), "PROCESSED", "", "70.00", true)
	wantResult(t, result(t, a, refundRequest, http.StatusOK), "PROCESSED", "", "100.00", true)
	assertStoredConsistent(t, e, w.ID)
}

// Covers: OPS-13 (I06b)
// Sensitivity: rejecting only from the worker that saw the attempts run out (ignoring expires_at) → the second app leaves the REFUND pending and waitStatus times out.
//
// Every instance is down past the TTL. When one starts, the operation is
// rejected with REFERENCE_NOT_FOUND, though no attempt ran in the meantime.
func TestPendingExpiresAfterDowntime(t *testing.T) {
	e := testkit.NewTestEnv(t, "downtime")
	slow := func(c *config.Config) {
		c.ReferenceMaxAttempts, c.ReferenceTTL = 50, 3*time.Second
		c.ReferenceRetryBaseDelay, c.ReferenceRetryMaxDelay = time.Second, 2*time.Second
	}

	first, stopFirst, err := e.StartApp(t.Context(), slow)
	if err != nil {
		t.Fatal(err)
	}
	stoppedFirst := false
	t.Cleanup(func() {
		if !stoppedFirst {
			stopFirst()
		}
	})
	w := openWalletVia(t, first, testkit.BRL("100.00"))
	refund := unique("refund")
	c := first.Client(t, "provider-a")
	wantResult(t, result(t, c, wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted), "PENDING_REFERENCE", "", "", false)
	pending := waitStatus(t, c, "provider-a", refund, "PENDING_REFERENCE")
	if pending.ExpiresAt == nil {
		t.Fatalf("pending operation without expiresAt: %+v", pending)
	}
	expiresAt := *pending.ExpiresAt
	stopFirst()
	stoppedFirst = true
	var status string
	if err := e.Owner.QueryRow(t.Context(), `SELECT status FROM wager_transactions WHERE external_transaction_id = $1`, refund).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING_REFERENCE" {
		t.Fatalf("the first app rejected it before it stopped (%s): the TTL is too short for this machine", status)
	}
	testkit.Eventually(t, 10*time.Second, "the TTL to pass with every instance down", func(context.Context) (bool, error) {
		return time.Now().After(expiresAt), nil
	})

	second, stopSecond, err := e.StartApp(t.Context(), slow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSecond)
	rejected := waitStatus(t, second.Client(t, "provider-a"), "provider-a", refund, "REJECTED")
	if rejected.FailureCode != "REFERENCE_NOT_FOUND" || rejected.Balance == nil || rejected.Balance.Amount != "100.00" ||
		rejected.CompletedAt == nil || rejected.CompletedAt.Before(expiresAt) {
		t.Fatalf("operation = %+v, want REJECTED REFERENCE_NOT_FOUND after %s with balance 100.00", rejected, expiresAt)
	}
	assertStoredConsistent(t, e, w.ID)
}
```

Nota: o `waitStatus` do Passo 2 da Tarefa 8 usa `testkit.Eventually` com `context.Context`; o `pgxpool` importado é o do `assertStoredConsistent`.

- [ ] **Passo 2: ver falhar.** Rode `go test -tags=integration -race -run 'TestRecoveryAfterRestart|TestPendingExpiresAfterDowntime' ./test/integration/...`.
  Esperado: **erro de compilação** (`too many arguments in call to e.StartApp`). O red do plano do M2 aceita um erro de compilação **só** quando o símbolo é uma assinatura de infraestrutura de teste; para ter a asserção, faça o stub do Passo 3 e rode de novo: o teste falha então porque a opção é ignorada e o REFUND expira antes do restart (`waitStatus`: `PROCESSED: not reached`, ou `REJECTED REFERENCE_NOT_FOUND`), com o TTL padrão de 3 s do `StartApp`.

- [ ] **Passo 3: stub e implementação.** Em `test/testkit/app.go`, importe `"github.com/KaioVinicios/pda/internal/config"` e mude a assinatura (stub: a opção é aceita e ignorada):

```go
func (e *Env) StartApp(ctx context.Context, opts ...func(*config.Config)) (*App, func(), error) {
```

Rode o Passo 2 de novo e confirme o red por asserção. Depois aplique as opções, logo antes do `Validate`:

```go
	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
	for _, opt := range opts {
		opt(&cfg)
	}
	if err := cfg.Validate(); err != nil {
```

e atualize o comentário: "…the logs captured for assertions. opts adjust the configuration before it is validated (a test that needs a longer reference TTL)."

- [ ] **Passo 4: ver passar.** O comando do Passo 2, verde. Rode 3 vezes (`-count=3`) para o I06b, que depende de relógio.

- [ ] **Passo 5: checagem de sensibilidade.** (a) Em `newModuleWorker`, não inicie o loop (comente o `go func()` do `OnStart`): o I06 deve falhar por `waitStatus` (REFUND continua `PENDING_REFERENCE`) e o I06b, idem (`REJECTED`). (b) Em `wagering.Settle`/`RescheduleReference` não se mexe; para o I06b, a sabotagem é a de (a). Desfaça e confirme o verde.

- [ ] **Checkpoint:** `go test -tags=integration -race -count=3 ./test/integration/... -run 'TestRecoveryAfterRestart|TestPendingExpiresAfterDowntime'` verde, e `make check` verde.

---

## Tarefa 10: Expiração por HTTP e continuidade pelo SQS

**Arquivos:**
- Modificar: `test/integration/references_test.go`, `test/integration/sqs_test.go`

Testes sobre comportamento que já existe (Tarefas 4–8): a checagem de sensibilidade é obrigatória.

- [ ] **Passo 1: escrever os testes.** Em `test/integration/references_test.go`:

```go
// Covers: OPS-13, OUT-07 (C3 over HTTP)
// Sensitivity: RescheduleReference never returning ErrReferenceExpired → waitStatus times out; a rejection with another code → the failureCode check fails.
//
// A REFUND whose BET never arrives is rejected when its attempts run out,
// with the rejection event and no ledger entry.
func TestPendingReferenceExpires(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	refund := unique("refund")
	pending := result(t, a, wager(w, "provider-a", "REFUND", "30.00", refund, unique("bet")), http.StatusAccepted)

	tx := waitStatus(t, a, "provider-a", refund, "REJECTED")
	if tx.FailureCode != "REFERENCE_NOT_FOUND" || tx.FailureCategory != "DEFINITIVE" || tx.Balance == nil || tx.Balance.Amount != "100.00" {
		t.Fatalf("operation = %+v, want REJECTED REFERENCE_NOT_FOUND (DEFINITIVE) with balance 100.00", tx)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("100.00") || got.Version != 1 {
		t.Fatalf("wallet = %+v, want 100.00 v1: an expiration moves nothing", got)
	}
	rejected := 0
	for _, env := range eventsOf(t, w.ID) {
		data, _ := env["data"].(map[string]any)
		if env["eventType"] != "WagerTransactionRejected" || data["transactionId"] != pending.TransactionID {
			continue
		}
		rejected++
		if data["failureCode"] != "REFERENCE_NOT_FOUND" || env["causationId"] != nil {
			t.Errorf("rejection event = %v, want REFERENCE_NOT_FOUND with no causationId", env)
		}
	}
	if rejected != 1 {
		t.Fatalf("%d WagerTransactionRejected events for the REFUND, want 1", rejected)
	}
}
```

Em `test/integration/sqs_test.go` (acrescente `"testing"` já importado):

```go
// Covers: SQS-08, OPS-12
// Sensitivity: deleting the message only after the pending is resolved (not after the commit) → AssertQueueDrained fails while the REFUND waits.
//
// A REFUND that arrives over SQS before its BET is recorded as pending and the
// message is deleted (the inbox says PENDING_REFERENCE); once the BET arrives,
// the worker resolves it, with no help from the queue.
func TestSQSPendingReferenceResolved(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet, refund := unique("bet"), unique("refund")
	msgID := unique("msg")
	server.SendWager(t, sqsWager(t, msgID, wager(w, "provider-a", "REFUND", "30.00", refund, bet)), testkit.SendOpts{GroupID: w.ID})

	tx := transactionOf(t, "provider-a", refund)
	if tx.Status != "PENDING_REFERENCE" || tx.ReceivedVia != "SQS" {
		t.Fatalf("operation = %+v, want PENDING_REFERENCE over SQS", tx)
	}
	server.AssertQueueDrained(t) // the message is gone: the wait is the worker's
	var outcome string
	if err := server.Owner().QueryRow(t.Context(),
		`SELECT outcome FROM inbox_messages WHERE message_id = $1`, msgID).Scan(&outcome); err != nil || outcome != "PENDING_REFERENCE" {
		t.Fatalf("inbox outcome = %q, %v; want PENDING_REFERENCE", outcome, err)
	}

	result(t, server.Client(t, "provider-a"), wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
	done := waitStatus(t, server.Client(t, "provider-a"), "provider-a", refund, "PROCESSED")
	if done.Balance == nil || done.Balance.Amount != "100.00" || done.ReceivedVia != "SQS" {
		t.Fatalf("REFUND = %+v, want PROCESSED over SQS with balance 100.00", done)
	}
}
```

- [ ] **Passo 2: ver passar.** `go test -tags=integration -race -count=3 -run 'TestPendingReferenceExpires|TestSQSPendingReferenceResolved' ./test/integration/...` verde nas 3 execuções.

- [ ] **Passo 3: checagem de sensibilidade.** (a) Em `wagering.Settle`, no ramo `settleUnresolved`, faça temporariamente o `errors.Is(err, ErrReferenceExpired)` nunca casar (`if false && …`): `TestPendingReferenceExpires` deve falhar (`waitStatus … REJECTED: not reached`). (b) Em `sqsconsumer`, faça o `handler` não apagar a mensagem de `PENDING_REFERENCE` (ou troque a decisão de `decide`): `TestSQSPendingReferenceResolved` deve falhar em `AssertQueueDrained`. Se a sabotagem (b) for invasiva demais, registre no diário que o SQS-08 é provado por `TestDecide` (M5) e por este teste, e faça só a (a). Desfaça e confirme o verde.

- [ ] **Checkpoint:** `go test -tags=integration -race ./test/integration/...` verde.

---

## Tarefa 11: Concorrência entre workers e contra o HTTP

**Arquivos:**
- Criar: `internal/adapters/references/concurrent_integration_test.go`
- Modificar: `test/integration/references_test.go`

Cobre o **foco 1** com dois workers de verdade, e a **ordem de lock** contra o HTTP.

- [ ] **Passo 1: escrever o teste com dois workers.** Crie `internal/adapters/references/concurrent_integration_test.go`:

```go
//go:build integration

package references_test

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ptr(s string) *string { return &s }

// operation validates an operation of provider on w; ref "" = no reference.
func operation(t *testing.T, w wallet.Wallet, provider, kind, amount, ext, ref string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr(provider + ":" + ext), ProviderID: ptr(provider), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(kind), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr("BRL")},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand %s %s: %v", kind, ext, err)
	}
	return cmd
}

// Covers: E7, TX-09, OPS-12 (spec M6, decision 3)
// Sensitivity: removing the horizon check in lockPending → attempts are counted twice and REFUNDs expire before their BET arrives (more than 0 REJECTED).
//
// Two workers, each with its own pool, run over the same database while the
// BETs of 40 waiting REFUNDs arrive from both. Every REFUND is processed once,
// and every wallet is consistent.
func TestConcurrentWorkers(t *testing.T) {
	e := testkit.NewTestEnv(t, "refs_concurrent")
	second, err := pgxpool.New(t.Context(), e.DB.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	// Long limits: nothing expires while the test runs.
	policy, err := wagering.NewReferenceRetryPolicy(100*time.Millisecond, time.Second, 50, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	build := func(pool *pgxpool.Pool) (*app.ProcessWager, *references.Worker) {
		pw := app.NewProcessWager(postgres.NewUnitOfWork(pool, e.Config()), postgres.NewRepos(pool), app.SystemClock{}, app.UUIDv7{}, policy, log)
		worker := references.NewWorker(app.NewResolveReferences(pw), observability.NewMetrics(observability.NewRegistry()), log,
			references.Options{BatchSize: 5, PollInterval: 20 * time.Millisecond})
		return pw, worker
	}
	pw1, worker1 := build(e.App)
	pw2, worker2 := build(second)

	const wallets, perWallet = 10, 4
	provider := "provider-" + newID()
	open := app.NewOpenWallet(postgres.NewUnitOfWork(e.App, e.Config()), app.SystemClock{}, app.UUIDv7{})
	ws := make([]wallet.Wallet, wallets)
	for i := range ws {
		amount := "100.00"
		if ws[i], err = open.Execute(t.Context(), app.OpenWalletInput{
			PlayerID: ptr(newID()), InitialBalance: &wagering.MoneyInput{Amount: &amount, Currency: ptr("BRL")},
		}, "corr-open"); err != nil {
			t.Fatalf("OpenWallet: %v", err)
		}
	}
	name := func(prefix string, i, k int) string { return prefix + "-" + strconv.Itoa(i) + "-" + strconv.Itoa(k) }
	for i, w := range ws { // the REFUNDs come first and wait
		for k := range perWallet {
			res, err := pw1.Execute(t.Context(), app.ProcessRequest{
				Command: operation(t, w, provider, "REFUND", "10.00", name("refund", i, k), name("bet", i, k)),
				Via:     wagering.ReceivedViaHTTP, CorrelationID: "corr-" + name("refund", i, k),
			})
			if err != nil || res.Tx.Status() != wagering.StatusPendingReference {
				t.Fatalf("REFUND %d-%d: %v, %v; want PENDING_REFERENCE", i, k, res.Tx, err)
			}
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	for _, worker := range []*references.Worker{worker1, worker2} {
		running.Go(func() { worker.Run(ctx) })
	}
	t.Cleanup(func() { cancel(); running.Wait() })

	var wg sync.WaitGroup // the BETs arrive from both instances and wake their REFUNDs
	for i, w := range ws {
		for k := range perWallet {
			wg.Go(func() {
				pw := pw1
				if (i+k)%2 == 1 {
					pw = pw2
				}
				res, err := pw.Execute(ctx, app.ProcessRequest{
					Command: operation(t, w, provider, "BET", "10.00", name("bet", i, k), ""),
					Via:     wagering.ReceivedViaHTTP, CorrelationID: "corr-" + name("bet", i, k),
				})
				if err != nil || res.Tx.Status() != wagering.StatusProcessed {
					t.Errorf("BET %d-%d: %v, %v", i, k, res.Tx, err)
				}
			})
		}
	}
	wg.Wait()

	testkit.Eventually(t, 20*time.Second, "every REFUND processed", func(ctx context.Context) (bool, error) {
		var n int
		err := e.Owner.QueryRow(ctx, `SELECT count(*) FROM wager_transactions
			WHERE provider_id = $1 AND kind = 'REFUND' AND status = 'PROCESSED'`, provider).Scan(&n)
		return n == wallets*perWallet, err
	})
	var rejected int
	if err := e.Owner.QueryRow(t.Context(), `SELECT count(*) FROM wager_transactions
		WHERE provider_id = $1 AND status IN ('REJECTED','FAILED')`, provider).Scan(&rejected); err != nil || rejected != 0 {
		t.Fatalf("%d operations rejected or failed, %v; want none", rejected, err)
	}
	for _, w := range ws {
		var balance string
		var entries int
		if err := e.Owner.QueryRow(t.Context(), `SELECT (balance_minor / 100.0)::numeric(12,2)::text,
			(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1) FROM wallets WHERE id = $1`, w.ID()).Scan(&balance, &entries); err != nil {
			t.Fatal(err)
		}
		if balance != "100.00" || entries != 1+2*perWallet { // the opening, the BETs and the REFUNDs
			t.Fatalf("wallet %s: balance %s, %d entries; want 100.00 and %d", w.ID(), balance, entries, 1+2*perWallet)
		}
		for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){testkit.LedgerProblems, testkit.OutboxProblems} {
			problems, err := check(t.Context(), e.Owner, w.ID())
			if err != nil || len(problems) != 0 {
				t.Errorf("wallet %s: %v, %v", w.ID(), problems, err)
			}
		}
	}
}
```

- [ ] **Passo 2: o teste contra o HTTP.** Em `test/integration/references_test.go`, acrescente `"sync"` e:

```go
// Covers: D-09 (lock order wallet → transaction), CONC-01 (spec M6, risks)
// Sensitivity: locking the transaction before the wallet in lockPending → 503s or 40P01 in the responses below.
//
// The BETs of ten waiting REFUNDs arrive over HTTP at the same moment as the
// REFUNDs themselves and as the worker: everything runs on one wallet, with no
// lock timeout or deadlock, and every REFUND ends PROCESSED.
func TestWorkerVersusHTTP(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("1000.00"))
	const pairs = 10
	type sent struct {
		refund, bet int
		refundExt   string
	}
	results := make([]sent, pairs)
	var wg sync.WaitGroup
	for i := range results {
		bet, refund := unique("bet"), unique("refund")
		results[i].refundExt = refund
		wg.Go(func() { results[i].refund = submit(t, a, wager(w, "provider-a", "REFUND", "10.00", refund, bet)).Status })
		wg.Go(func() { results[i].bet = submit(t, a, wager(w, "provider-a", "BET", "10.00", bet, "")).Status })
	}
	wg.Wait()

	for i, r := range results {
		// A REFUND that lands after its BET is processed at once (200); before it, it waits (202).
		if r.bet != http.StatusOK || (r.refund != http.StatusOK && r.refund != http.StatusAccepted) {
			t.Fatalf("pair %d: BET %d, REFUND %d; want 200 and 200 or 202 (a 503 is a lock timeout)", i, r.bet, r.refund)
		}
		waitStatus(t, a, "provider-a", r.refundExt, "PROCESSED")
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("1000.00") {
		t.Fatalf("wallet = %+v, want 1000.00", got)
	}
}
```

- [ ] **Passo 3: ver passar** (o comportamento já existe): `go test -tags=integration -race -count=3 -run 'TestConcurrentWorkers' ./internal/adapters/references/...` e `-count=3 -run 'TestWorkerVersusHTTP' ./test/integration/...`. Ambos verdes nas 3 execuções.

- [ ] **Passo 4: checagem de sensibilidade.** (a) Em `lockPending`, remova o `|| tx.NextAttemptAt().After(now)`: o `TestConcurrentWorkers` deve falhar (REFUNDs expirados: `N operations rejected or failed`) ou, com o mesmo sintoma, o `Eventually` estourar. Se passar mesmo assim (a corrida é estreita), registre isso no diário: a prova da decisão 3 é o `TestResolveReferencesConcurrent` (Tarefa 5), que é determinístico. (b) Inverta a ordem dos locks em `lockPending` (transação antes da carteira): o `TestWorkerVersusHTTP` deve falhar com 503 (lock timeout de 2 s) ou o Postgres derrubar um deles por `40P01`. Se nenhuma das duas sabotagens for detectada em 3 execuções, **pare e registre**: o teste não está provando o que promete e precisa de mais pares. Desfaça e confirme o verde.

- [ ] **Checkpoint:** `make test-integration` verde.

---

## Tarefa 12: Verificação do marco e encerramento

**Arquivos:**
- Modificar: `docs/delivery-requirements.md`, `docs/implementation-plan.md`, `docs/dev/diary.md`, `ARCHITECTURE.md`

- [ ] **Passo 1: `make check`.** Saída completa na mensagem: formatação, lint (`0 issues.`), `go vet`, `go mod tidy -diff`, versão do Go e `go test -race ./...`.

- [ ] **Passo 2: `make test-integration`,** três execuções seguidas (`for i in 1 2 3; do make test-integration || break; done`), todas verdes. Se houver flake, **investigue** com `superpowers:systematic-debugging`, não repita até passar.

- [ ] **Passo 3: sabotagens do marco,** cada uma vista falhar e desfeita (a lista do que ficou registrado nos `// Sensitivity:` das Tarefas 3 a 11). Anote no diário o número total.

- [ ] **Passo 4: compose de verdade.** `docker compose up --build --wait`: as 3 réplicas saudáveis. Com um token real (`client_credentials`, README do M0):
  1. `POST /wallets` com saldo 100.00;
  2. `POST /wagering/transactions` de um REFUND (202) referenciando um BET que ainda não existe;
  3. o `POST` do BET (200);
  4. `GET /providers/{p}/wagering/transactions/{ext}` do REFUND: `PROCESSED` em segundos, `balance` 100.00;
  5. um REFUND sem BET: `REJECTED/REFERENCE_NOT_FOUND` em cerca de 3 minutos (limite padrão de 8 tentativas);
  6. `docker compose logs app-1 app-2 app-3 | grep "reference"`: as três réplicas iniciam o worker, e as resoluções aparecem distribuídas;
  7. `docker compose stop` (ou `kill -TERM`): o log mostra `reference worker stopping`/`stopped` na ordem HTTP → consumidor → publisher → worker.
  Se o passo 5 for lento demais para a sessão, execute-o com `REFERENCE_MAX_ATTEMPTS=2 REFERENCE_TTL=20s` numa réplica avulsa e diga isso na saída.

- [ ] **Passo 5: requisitos e docs** ([`development-workflow.md`](../../development-workflow.md) §5):
  - `delivery-requirements.md`: OPS-12, OPS-13, TX-09 e SQS-08 → `[x]`, cada um citando os testes (`TestResolveReferences`, `TestResolveReferencesSkips`, `TestResolveReferencesConcurrent`, `TestRecoveryAfterRestart`, `TestPendingExpiresAfterDowntime`, `TestPendingReferenceExpires`, `TestSQSPendingReferenceResolved`); OPS-04..10 confirmados pelo `TestReversalRules` (I11); OBS-03, FX-01, FX-03 e E7 parciais, com a nota do que falta (C08b e o resto do catálogo);
  - `implementation-plan.md`: M6 → "✅ concluído em 30/09", com o "Entregue também" e o achado da validação; §5 (riscos): a linha do deadlock ou do recheck; §6: E7 "M3–M6" com o M6 ✅;
  - `ARCHITECTURE.md`: §5 (o worker: execução em sequência, o recheck de horário, `FAILED`, a causa), §12/§13 (stop, as 3 métricas com o gauge), e as limitações: o ciclo de lock cruzado entre carteiras (spec §11) e os eventos do último item do worker esperando a próxima publicação;
  - `docs/dev/diary.md`: entrada do M6 (escolhas do autor, achados da execução, prova final) e "Onde paramos" → M7.

- [ ] **Passo 6: `superpowers:verification-before-completion`.** Nenhuma afirmação de "pronto" sem a saída de `make check` e de `make test-integration` **na mesma mensagem**.

- [ ] **Passo 7: proposta de commits** (skill `git-commit` do projeto; **sem** trailer de coautoria; o autor decide). Sugestão, em ordem:
  1. `feat(config): add the reference worker poll interval and batch size`
  2. `feat(observability): add the reference worker metrics`
  3. `feat(postgres): claim, lock and count pending references`
  4. `feat(app): resolve pending references with the wagering settlement`
  5. `feat(references): add the reference worker and its fx module`
  6. `test(integration): cover the worker with restarts, expiration, sqs and concurrency`
  7. `docs: record the m6 reference worker decisions` e `docs(dev): add m6 spec, plan and diary entry`

- [ ] **Checkpoint final:** o autor recebe o resumo com a evidência e a proposta de commits.
