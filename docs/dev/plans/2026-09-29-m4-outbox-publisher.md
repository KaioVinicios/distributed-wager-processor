# M4 — Outbox publisher: plano de implementação

> **Execução:** `superpowers:executing-plans`, **inline** na própria sessão, sem subagentes ([`development-workflow.md`](../../development-workflow.md) §7). Cada tarefa segue `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Sem commits:** cada tarefa termina num *checkpoint* verificável, e os commits são propostos no fim do marco (§6 do workflow).

**Objetivo:** todo evento confirmado na outbox chega ao `wallet-events.fifo` pelo menos uma vez, com o mesmo `eventId` e o mesmo conteúdo, publicado por qualquer instância, e nada é publicado antes do commit (**E8**).

**Arquitetura:**
- **Porta `app.OutboxStore`** (spec decisão 4), implementada pelo `postgres.OutboxStore`: claim, confirmação, falha e backlog, cada um com um statement no pool.
- **`adapters/outbox`:**
  - o `Publisher` (loop claim → publish → ack/fail, com lease), que depende das portas pequenas `Sink` e `Metrics`;
  - o `SNSSink` (mapeamento do messaging §5.2);
  - o `module.go` (identidade da instância e lifecycle).
- **`awsclient`** resolve o ARN do tópico no start: STS + `GetTopicAttributes`, com fail fast.
- **O contrato dos eventos** ([`api/events.yaml`](../../../api/events.yaml)) é validado pelo `testkit` em **toda** mensagem lida da fila de auditoria, e o `AssertWalletConsistent` passa a exigir a outbox publicada e entregue (item 8).
- **Ordem das tarefas:** contrato e harness do `testkit` → config → porta e adapter PostgreSQL → tópico no `awsclient` → métricas → backoff e sink → publisher (caminho feliz, falhas, stop e módulo) → item 8 e I05e → encerramento.

**Stack:**
- Go 1.27.1, `pgx/v5` e `aws-sdk-go-v2`;
- `service/sns` v1.47.2 e `service/sts` v1.51.1: esta última já estava no `go.mod` como indireta e vira direta;
- Prometheus `client_golang` v1.24.1 e `goleak` v1.3.0 (já no `go.mod`);
- `kin-openapi` v0.149.0, só nos testes;
- PostgreSQL 18.6, Keycloak 26.7.4 e MiniStack 1.5.18 do compose.

**Spec:** [`docs/dev/specs/2026-09-29-m4-outbox-publisher-design.md`](../specs/2026-09-29-m4-outbox-publisher-design.md). Quem executa lê a spec, com os achados da §2.2, e o [`api/events.yaml`](../../../api/events.yaml), que faz parte dela.

**Validação prévia do plano:** o código abaixo foi escrito e testado numa cópia descartável do repositório (um `git clone` local, sem worktree nem branch), contra a infraestrutura do compose.
- **Cada tarefa:** o teste novo foi visto falhando pelo motivo previsto (asserção, ou o erro do stub) e depois passando.
  - As versões intermediárias do `publisher.go` (Tarefas 8 e 9) foram testadas separadamente: a da Tarefa 8 passa no I05a/I05b e falha nos 4 testes da Tarefa 9; a da Tarefa 9 passa nos seus e falha no `TestPublisherStop`.
- **Estado final:**
  - `make check` verde (`0 issues.`, `gofmt`/`gofumpt`, `go mod tidy -diff`, `go vet` com e sem tags e `go test -race ./...`);
  - `go test -tags=integration -race -count=1 ./...` verde três vezes seguidas;
  - `test/integration` sem aumento de tempo (≈ 10 s) com o item 8 em toda carteira.
- **Sabotagens detectadas (6):**
  1. publicar dentro da transação (I05b);
  2. remover o guarda de id malformado do store;
  3. remover o `cancel()` do `OnStop` (`TestFxLifecycle`: `OnStop` falha e o goleak aponta o loop);
  4. remover o `outbox.Module` (item 8 em todo teste);
  5. remover o atributo `correlationId` (I05e);
  6. publicar com o contexto do loop (`TestPublisherStop`).
- **Imagem real:** a imagem compilada da cópia rodou na rede do compose com o usuário IAM `pda-wallet-service`.
  - Resolveu o tópico e publicou os 2 eventos de um `POST /wallets` na primeira tentativa, e eles chegaram a `wallet-events-audit.fifo` com grupo e atributos corretos.
  - O stop gracioso aparece no log, e um reinício com o mesmo PID recebeu outro sufixo de identidade.

Na execução, o código é redigitado seguindo o ciclo red → green de cada tarefa.

**Achados da validação** (registrados na spec §2.2, decisões 20–26): o principal é que os testes do `bootstrap` usavam o banco compartilhado `pda`. Com o publisher no grafo, eles publicaram, num tópico de teste depois apagado, os eventos pendentes do ambiente de desenvolvimento. A Tarefa 5 isola esses testes **antes** de o publisher entrar no grafo (Tarefa 10).

## Restrições globais

- Module path `github.com/KaioVinicios/pda`; `go 1.27.1`. A única mudança de dependência é `aws-sdk-go-v2/service/sts` v1.51.1, de indireta para direta (via `go mod tidy`).
- **Camadas:**
  - `internal/app` só ganha tipos e a interface `OutboxStore`;
  - `adapters/outbox` importa `app`, `awsclient`, `config` e `observability`, nunca `adapters/postgres` (o teste pode);
  - o `depguard` segue valendo.
- **Dinheiro nunca em `float`:** fora de `internal/observability` (exceção do `.golangci.yml`), nenhum `float64`. Os testes leem tempos do banco em milissegundos `bigint`, e o validador de eventos decodifica com `json.Number`.
- **Logs:** chaves em `camelCase` e mensagens estáticas (`sloglint`). Nunca o payload, só `eventId`, `eventType`, `attempts` e o erro.
- **Relógio:** `lease`, `next_attempt_at`, `published_at` e o backlog usam o `now()` do banco (spec decisão 4).
- **Testes de integração:**
  - tag `integration` e `make infra-up` no ar;
  - todo teste do publisher e o `TestOutboxStore` com **banco e tópico próprios** (spec decisão 21);
  - `-race`; `// Covers: <IDs>` em todo teste; só `testing` da stdlib;
  - esperas sempre com prazo (`testkit.Eventually`, `Audit.WaitFor`), nunca `time.Sleep` como asserção.
- **`make lint` e `make fmt`** usam a imagem `golangci/golangci-lint:v2.14.0`, então o Docker precisa estar rodando. Os blocos de código abaixo já estão formatados pelo `gofumpt`.
- **Sem commits, branches ou worktrees.**

## Foco de revisão

Os cinco casos que a spec implica, mas não detalha, com mais chance de causar problema. Cada um tem teste na tarefa indicada:

1. **Stop com uma publicação em andamento.** Esperado: o `Run` espera ela terminar, o evento é confirmado, nenhum outro envio começa e o resto do lote fica com o lease. → Tarefa 10 (`TestPublisherStop`).
2. **Banco indisponível durante o claim.** Esperado: o publisher não morre; espera 1 s, depois 2 s (até 30 s) e volta a publicar. → Tarefa 9 (`TestPublisherSurvivesClaimFailures`).
3. **Broker indisponível.** Esperado: o evento nunca é descartado, `outbox_pending_events` mostra o pendente e volta a zero depois da publicação. → Tarefa 9 (`TestOutboxBacklogGauges` e I05c).
4. **Testes rodando ao lado do ambiente de desenvolvimento.** Esperado: nenhum teste publica os eventos do banco compartilhado `pda`. → Tarefa 5 (banco próprio no `integrationConfig`, com a verificação manual do passo 12).
5. **Confirmação com lease perdido ou id malformado.** Esperado: `ok = false` sem erro de banco, e nenhuma métrica de publicação contada. → Tarefa 4 (`TestOutboxStore`, subtestes "only the lease owner confirms" e "a malformed id…").

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `api/embed.go`; `test/testkit/event_contract.go` + teste | `api.Events` e o validador do `events.yaml` | 1 |
| `test/testkit/{events,audit}.go`; `test/integration/harness_test.go` | Tópico isolado e coletor da fila de auditoria | 2 |
| `internal/config/{config,validate}.go` + teste; `test/testkit/{env,aws,app}.go` | 7 variáveis da outbox; o app em teste com tópico isolado e `App.Audit` | 3 |
| `internal/app/ports.go`; `internal/adapters/postgres/{outbox_repo,module}.go` + teste; `test/testkit/env.go`; `internal/bootstrap/bootstrap_test.go` | `OutboxStore` e `NewTestEnv` | 4 |
| `internal/adapters/awsclient/{topic,module}.go` + teste; `go.mod`; `deploy/aws/policies/pda-wallet-service.json`; `internal/bootstrap/*_test.go`; `test/integration/provisioning_test.go`; `test/testkit/postgres.go` | ARN do tópico, política e isolamento do `bootstrap` | 5 |
| `internal/observability/metrics.go` + teste | As 6 métricas de outbox | 6 |
| `internal/adapters/outbox/{backoff,sns_sink}.go` + testes | `retryDelay`, `truncateError` e `SNSSink` | 7 |
| `test/testkit/assert.go` + `assert_test.go`; `internal/adapters/outbox/publisher.go`; `internal/adapters/outbox/{helpers,publisher}_integration_test.go` | `Eventually` e o publisher, caminho feliz (I05a, I05b) | 8 |
| `internal/adapters/outbox/publisher.go`; `failures_integration_test.go` | Falhas, reclaim, backlog e claim com o banco fora (I05c, I05d) | 9 |
| `internal/adapters/outbox/{publisher,module}.go` + `module_test.go`, `stop_integration_test.go`; `internal/bootstrap/bootstrap.go` | Stop e composição Fx | 10 |
| `test/testkit/assert.go`; `test/integration/events_test.go` | Item 8 da consistência e I05e | 11 |
| `docs/*`, `ARCHITECTURE.md`, `docs/dev/diary.md` | Verificação e encerramento | 12 |

---

### Tarefa 1: contrato dos eventos no `testkit`

O `api/events.yaml` (spec §5) vira um validador reutilizável. Todo teste que lê a fila de auditoria passa por ele (spec decisões 16 e 17).

**Arquivos:**
- Implementação: `api/embed.go` (alterar), `test/testkit/event_contract.go` (criar)
- Testes: `test/testkit/event_contract_test.go` (criar)

**Interfaces:**
- Consome: `api/events.yaml` (spec); `events.Seal`, os 4 construtores de `internal/domain/events` e `money.Parse` (M1); `firstLine` (`test/testkit/contract.go`, M3).
- Produz:
  - `api.Events []byte`;
  - `testkit.LoadEventContract(ctx context.Context) (*testkit.EventContract, error)`;
  - `(*EventContract).Validate(body []byte) error`.

- [ ] **Passo 1: embutir o documento.** Acrescentar ao fim de `api/embed.go`:

```go

// Events is the OpenAPI 3.0.3 document with the schemas of the integration
// events published to SNS (messaging.md §6); it has no paths.
//
//go:embed events.yaml
var Events []byte
```

- [ ] **Passo 2: stub do validador** em `test/testkit/event_contract.go`, que aceita tudo:

```go
package testkit

import "context"

// EventContract validates the integration events against api/events.yaml
// (spec M4, decision 16).
type EventContract struct{}

// LoadEventContract loads and validates the embedded document.
func LoadEventContract(ctx context.Context) (*EventContract, error) { return &EventContract{}, nil }

// Validate checks one message body: the envelope against Envelope and data
// against <eventType>V<version>.
func (c *EventContract) Validate(body []byte) error { return nil }
```

- [ ] **Passo 3: escrever o teste** em `test/testkit/event_contract_test.go`:

```go
package testkit_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/test/testkit"
)

const (
	evWallet = "0192f291-27dd-7d3f-8071-5f8685deef37"
	evPlayer = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	evTx     = "0192f298-345e-7e38-af88-e43f851a819d"
	evRefTx  = "0192f297-0000-7000-8000-000000000002"
)

var evAt = time.Date(2026, 9, 29, 12, 0, 0, 123987654, time.UTC)

func evMoney(t *testing.T, amount string) money.Money {
	t.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// sealed builds an event with its constructor and returns the envelope JSON,
// sealed as the app does: a UUIDv7 event id.
func sealed(t *testing.T, causation string, build func() (events.Event, error)) []byte {
	t.Helper()
	e, err := build()
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.Seal(uuid.Must(uuid.NewV7()).String(), "corr-1", causation, e)
	if err != nil {
		t.Fatal(err)
	}
	body, err := env.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// mutate decodes body, applies fn to the envelope and encodes it again.
func mutate(t *testing.T, body []byte, fn func(env, data map[string]any)) []byte {
	t.Helper()
	var env map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		t.Fatal(err)
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatal("envelope without data")
	}
	fn(env, data)
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Covers: OUT-08, OUT-09, OUT-11, OUT-12, OUT-13
//
// Every envelope the domain seals passes, and the contract catches drift.
func TestEventContract(t *testing.T) {
	c, err := testkit.LoadEventContract(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	changed := sealed(t, "", func() (events.Event, error) {
		return events.NewWalletBalanceChanged(events.WalletBalanceChanged{
			WalletID: evWallet, TransactionID: evTx, TransactionKind: "BET", Direction: "DEBIT",
			Money: evMoney(t, "25.00"), BalanceBefore: evMoney(t, "1000.00"), BalanceAfter: evMoney(t, "975.00"),
			WalletVersion: 2, ChangedAt: events.NewTime(evAt),
		})
	})
	opening := sealed(t, "", func() (events.Event, error) {
		return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
			TransactionID: evTx, Origin: "INTERNAL", Kind: "OPENING", WalletID: evWallet, PlayerID: evPlayer,
			Money: evMoney(t, "1000.00"), BalanceAfter: evMoney(t, "1000.00"), WalletVersion: 1,
			ProcessedAt: events.NewTime(evAt),
		})
	})
	valid := map[string][]byte{
		"balance changed": changed,
		"opening":         opening,
		"refund with references, from SQS": sealed(t, "msg-1", func() (events.Event, error) {
			return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
				TransactionID: evTx, Origin: "EXTERNAL", Kind: "REFUND", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-124", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "25.00"), BalanceAfter: evMoney(t, "1000.00"), WalletVersion: 3,
				ReferenceExternalTransactionID: "tx-123", ReferenceTransactionID: evRefTx, ProcessedAt: events.NewTime(evAt),
			})
		}),
		"loss": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionProcessed(events.WagerTransactionProcessed{
				TransactionID: evTx, Origin: "EXTERNAL", Kind: "LOSS", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-125", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "0.00"), BalanceAfter: evMoney(t, "975.00"), WalletVersion: 2, ProcessedAt: events.NewTime(evAt),
			})
		}),
		"rejected": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionRejected(events.WagerTransactionRejected{
				TransactionID: evTx, Kind: "BET", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-126", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "80.00"), FailureCode: "INSUFFICIENT_FUNDS", FailureCategory: "DEFINITIVE",
				Balance: evMoney(t, "20.00"), RejectedAt: events.NewTime(evAt),
			})
		}),
		"pending reference": sealed(t, "", func() (events.Event, error) {
			return events.NewWagerTransactionPendingReference(events.WagerTransactionPendingReference{
				TransactionID: evTx, Kind: "REFUND", WalletID: evWallet, PlayerID: evPlayer,
				ProviderID: "provider-a", ExternalTransactionID: "tx-127", RoundID: "round-1", GameID: "game-1",
				Money: evMoney(t, "25.00"), ReferenceExternalTransactionID: "tx-123",
				NextAttemptAt: events.NewTime(evAt.Add(time.Second)), ExpiresAt: events.NewTime(evAt.Add(10 * time.Minute)),
				PendingAt: events.NewTime(evAt),
			})
		}),
	}
	for name, body := range valid {
		if err := c.Validate(body); err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
	}

	invalid := map[string][]byte{
		"missing field":          mutate(t, changed, func(_, d map[string]any) { delete(d, "walletVersion") }),
		"null optional field":    mutate(t, changed, func(e, _ map[string]any) { e["causationId"] = nil }),
		"field outside data":     mutate(t, changed, func(_, d map[string]any) { d["note"] = "x" }),
		"field outside envelope": mutate(t, changed, func(e, _ map[string]any) { e["messageGroupId"] = evWallet }),
		"amount as a number": mutate(t, changed, func(_, d map[string]any) {
			d["money"] = map[string]any{"amount": json.Number("25.00"), "currency": "BRL"}
		}),
		"timestamp without milliseconds": mutate(t, changed, func(e, _ map[string]any) { e["occurredAt"] = "2026-09-29T12:00:00Z" }),
		"aggregate of another type":      mutate(t, changed, func(e, _ map[string]any) { e["aggregateType"] = "WagerTransaction" }),
		"unknown version":                mutate(t, changed, func(e, _ map[string]any) { e["version"] = json.Number("2") }),
		"external metadata on opening":   mutate(t, opening, func(_, d map[string]any) { d["providerId"] = "provider-a" }),
		"not json":                       []byte(`{"eventId":`),
	}
	for name, body := range invalid {
		if err := c.Validate(body); err == nil {
			t.Errorf("%s: Validate = nil, want an error", name)
		}
	}
}
```

- [ ] **Passo 4: ver falhar.** `go test -race -count=1 -run '^TestEventContract$' ./test/testkit/`
  - Esperado: FAIL, com as 10 linhas `…: Validate = nil, want an error`. Os 6 casos válidos passam.

- [ ] **Passo 5: implementar** `test/testkit/event_contract.go`:

```go
package testkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/KaioVinicios/pda/api"
)

// EventContract validates the integration events against api/events.yaml
// (spec M4, decision 16): a message off the contract fails the test that
// received it.
type EventContract struct {
	schemas openapi3.Schemas
}

// LoadEventContract loads and validates the embedded document.
func LoadEventContract(ctx context.Context) (*EventContract, error) {
	doc, err := openapi3.NewLoader().LoadFromData(api.Events)
	if err != nil {
		return nil, fmt.Errorf("testkit: load the event contract: %w", err)
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, fmt.Errorf("testkit: invalid event contract: %w", err)
	}
	return &EventContract{schemas: doc.Components.Schemas}, nil
}

// Validate checks one message body: the envelope against Envelope and data
// against <eventType>V<version>. Numbers are decoded as json.Number, never as
// floating point.
func (c *EventContract) Validate(body []byte) error {
	var msg map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&msg); err != nil {
		return fmt.Errorf("event contract: body is not a JSON object: %w", err)
	}
	if dec.More() {
		return errors.New("event contract: data after the JSON object")
	}
	if err := c.schemas["Envelope"].Value.VisitJSON(msg); err != nil {
		return fmt.Errorf("event contract: envelope: %s", firstLine(err))
	}
	name := fmt.Sprintf("%sV%s", msg["eventType"], msg["version"])
	schema, ok := c.schemas[name]
	if !ok {
		return fmt.Errorf("event contract: no schema %s", name)
	}
	if err := schema.Value.VisitJSON(msg["data"]); err != nil {
		return fmt.Errorf("event contract: %s: %s", name, firstLine(err))
	}
	return nil
}
```

- [ ] **Passo 6: ver passar.** `go test -race -count=1 ./test/testkit/`: PASS.
- [ ] **Passo 7: conferir os motivos.** Trocar temporariamente o `t.Errorf` do laço `invalid` por um `t.Logf("%s: %v", name, err)` e rodar com `-v`. Cada caso precisa falhar pelo motivo do nome:
  - `property "walletVersion" is missing`;
  - `Value is not nullable`;
  - `property "note" is unsupported`;
  - `property "messageGroupId" is unsupported`;
  - `value must be a string`;
  - `doesn't match the regular expression`;
  - `value is not one of the allowed values ["Wallet"]`;
  - `…allowed values [1]`;
  - `property "providerId" is unsupported`;
  - `unexpected EOF`.

  Desfazer a troca.

**Checkpoint:** `go test -race ./test/testkit/ ./api/...` verde.

---

### Tarefa 2: tópico isolado e coletor da fila de auditoria

Os mesmos recursos que o `aws-init` provisiona (tópico FIFO, fila FIFO, policy da fila e assinatura raw), com sufixo aleatório. O `Audit` guarda **todas** as entregas por `eventId` e valida cada uma contra o contrato (spec §6).

**Arquivos:**
- Implementação: `test/testkit/events.go`, `test/testkit/audit.go` (criar)
- Testes: `test/integration/harness_test.go` (alterar)

**Interfaces:**
- Consome: `LoadEventContract` (Tarefa 1); `findRepoRoot` (`root.go`), `RootAWSConfig` (`aws.go`); `deploy/aws/policies/wallet-events-audit.json`.
- Produz:
  - `testkit.EventsTopic{Name, ARN, AuditQueueURL string}`;
  - `testkit.CreateEventsTopic(ctx, *sqs.Client, *sns.Client) (EventsTopic, func(), error)` e `testkit.NewEventsTopic(tb, *sqs.Client, *sns.Client) EventsTopic`;
  - `testkit.AuditTimeout = 10 * time.Second`;
  - `testkit.Attribute{Type, Value string}` e `testkit.AuditMessage{Body []byte; GroupID, DedupID string; Attributes map[string]Attribute; ContractErr error}`;
  - `testkit.NewAudit(ctx, *sqs.Client, queueURL string) (*testkit.Audit, error)`;
  - `(*Audit).Wait(ctx, ids ...string) (map[string][]AuditMessage, error)`, `WaitFor(tb, ids ...string) map[string][]AuditMessage` e `Absent(tb, window time.Duration, ids ...string)`.

- [ ] **Passo 1: stubs.** `test/testkit/events.go`:

```go
package testkit

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// EventsTopic is an isolated events topic with its audit queue (test-plan §3.2).
type EventsTopic struct {
	Name          string
	ARN           string
	AuditQueueURL string
}

// NewEventsTopic is CreateEventsTopic for a test; the resources are removed at
// cleanup.
func NewEventsTopic(tb testing.TB, q *sqs.Client, n *sns.Client) EventsTopic {
	tb.Helper()
	topic, remove, err := CreateEventsTopic(tb.Context(), q, n)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(remove)
	return topic
}

// CreateEventsTopic creates the FIFO topic events-<rand>.fifo and the FIFO
// queue events-audit-<rand>.fifo subscribed to it with raw delivery.
func CreateEventsTopic(ctx context.Context, q *sqs.Client, n *sns.Client) (EventsTopic, func(), error) {
	return EventsTopic{}, func() {}, errors.New("testkit: not implemented")
}
```

`test/testkit/audit.go`:

```go
package testkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// AuditTimeout bounds how long WaitFor waits for the events.
const AuditTimeout = 10 * time.Second

// Attribute is one message attribute of a delivery.
type Attribute struct {
	Type  string // String or Number
	Value string
}

// AuditMessage is one delivery read from the audit queue.
type AuditMessage struct {
	Body        []byte
	GroupID     string
	DedupID     string
	Attributes  map[string]Attribute
	ContractErr error // the body violates api/events.yaml
}

// Audit collects the audit queue of one events topic.
type Audit struct{}

// NewAudit reads queueURL and validates every delivery against the event contract.
func NewAudit(ctx context.Context, client *sqs.Client, queueURL string) (*Audit, error) {
	return &Audit{}, nil
}

// Wait receives until every id has at least one delivery, or ctx ends.
func (a *Audit) Wait(ctx context.Context, ids ...string) (map[string][]AuditMessage, error) {
	return nil, errors.New("testkit: not implemented")
}

// WaitFor is Wait with AuditTimeout, failing tb on a missing id or on a
// delivery off the contract.
func (a *Audit) WaitFor(tb testing.TB, ids ...string) map[string][]AuditMessage {
	tb.Helper()
	return nil
}

// Absent receives during window and fails tb if any of ids arrives.
func (a *Audit) Absent(tb testing.TB, window time.Duration, ids ...string) { tb.Helper() }
```

- [ ] **Passo 2: escrever o teste.** Em `test/integration/harness_test.go`, trocar o bloco de imports por:

```go
import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/KaioVinicios/pda/api"
	"github.com/KaioVinicios/pda/test/testkit"
)
```

e acrescentar ao fim do arquivo:

```go

// auditEnvelope is a valid WalletBalanceChanged envelope; extra adds fields to data.
func auditEnvelope(eventID, walletID, extra string) string {
	return `{"eventId":"` + eventID + `","eventType":"WalletBalanceChanged","version":1,"aggregateType":"Wallet",` +
		`"aggregateId":"` + walletID + `","correlationId":"corr-audit","occurredAt":"2026-09-29T12:00:00.123Z","data":{` +
		`"walletId":"` + walletID + `","transactionId":"` + testkit.NewID() + `","transactionKind":"BET","direction":"DEBIT",` +
		`"money":{"amount":"25.00","currency":"BRL"},"balanceBefore":{"amount":"100.00","currency":"BRL"},` +
		`"balanceAfter":{"amount":"75.00","currency":"BRL"},"walletVersion":2` + extra + `}}`
}

// Covers: OUT-07 (harness do M4)
//
// The isolated topic delivers raw to its audit queue; the collector keeps
// every delivery per event id, in the order of the group, and checks each one
// against api/events.yaml.
func TestAuditCollector(t *testing.T) {
	t.Parallel()
	root := testkit.RootAWSConfig(t)
	snsClient, sqsClient := sns.NewFromConfig(root), sqs.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, sqsClient, snsClient)
	audit, err := testkit.NewAudit(t.Context(), sqsClient, topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	wallet := testkit.NewID()
	publish := func(body, dedup string) {
		t.Helper()
		if _, err := snsClient.Publish(t.Context(), &sns.PublishInput{
			TopicArn: aws.String(topic.ARN), Message: aws.String(body),
			MessageGroupId: aws.String(wallet), MessageDeduplicationId: aws.String(dedup),
			MessageAttributes: map[string]snstypes.MessageAttributeValue{
				"eventType":    {DataType: aws.String("String"), StringValue: aws.String("WalletBalanceChanged")},
				"eventVersion": {DataType: aws.String("Number"), StringValue: aws.String("1")},
			},
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	good, bad, last := testkit.NewID(), testkit.NewID(), testkit.NewID()
	publish(auditEnvelope(good, wallet, ""), good)
	publish(auditEnvelope(good, wallet, ""), good+"-again") // republished past the dedup window
	publish(auditEnvelope(bad, wallet, `,"note":"x"`), bad)
	publish(auditEnvelope(last, wallet, ""), last)

	ctx, cancel := context.WithTimeout(t.Context(), testkit.AuditTimeout)
	defer cancel()
	if _, err := audit.Wait(ctx, last); err != nil { // same group: the earlier ones arrived before it
		t.Fatalf("Wait: %v", err)
	}
	got, err := audit.Wait(ctx, good, bad)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if n := len(got[good]); n != 2 {
		t.Fatalf("deliveries of %s = %d, want 2", good, n)
	}
	first := got[good][0]
	switch {
	case first.ContractErr != nil:
		t.Fatalf("valid envelope flagged: %v", first.ContractErr)
	case first.GroupID != wallet || first.DedupID != good:
		t.Fatalf("group %q dedup %q, want %s %s", first.GroupID, first.DedupID, wallet, good)
	case first.Attributes["eventType"] != testkit.Attribute{Type: "String", Value: "WalletBalanceChanged"},
		first.Attributes["eventVersion"] != testkit.Attribute{Type: "Number", Value: "1"}:
		t.Fatalf("attributes = %v", first.Attributes)
	}
	if got[bad][0].ContractErr == nil {
		t.Fatal("envelope with a field outside the schema was not flagged")
	}
	audit.Absent(t, time.Second, testkit.NewID())
}
```

- [ ] **Passo 3: ver falhar.** `make infra-up`, depois `go test -tags=integration -race -count=1 -run '^TestAuditCollector$' ./test/integration/`
  - Esperado: FAIL com `harness_test.go:…: testkit: not implemented` (o `NewEventsTopic` do stub).

- [ ] **Passo 4: implementar** `test/testkit/events.go`:

```go
package testkit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"
)

// EventsTopic is an isolated events topic with its audit queue (test-plan §3.2).
type EventsTopic struct {
	Name          string
	ARN           string
	AuditQueueURL string
}

// NewEventsTopic is CreateEventsTopic for a test; the resources are removed at
// cleanup.
func NewEventsTopic(tb testing.TB, q *sqs.Client, n *sns.Client) EventsTopic {
	tb.Helper()
	topic, remove, err := CreateEventsTopic(tb.Context(), q, n)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(remove)
	return topic
}

// CreateEventsTopic creates the FIFO topic events-<rand>.fifo and the FIFO
// queue events-audit-<rand>.fifo, subscribed to it with raw delivery and the
// queue policy of deploy/aws/policies/wallet-events-audit.json, the same
// resources aws-init provisions (messaging.md §2). remove deletes them unless
// PDA_TEST_KEEP=1.
func CreateEventsTopic(ctx context.Context, q *sqs.Client, n *sns.Client) (EventsTopic, func(), error) {
	suffix := uuid.NewString()[:8]
	topic := EventsTopic{Name: "events-" + suffix + ".fifo"}
	auditName := "events-audit-" + suffix + ".fifo"

	out, err := n.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(topic.Name),
		Attributes: map[string]string{"FifoTopic": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		return EventsTopic{}, nil, fmt.Errorf("testkit: create topic %s: %w", topic.Name, err)
	}
	topic.ARN = aws.ToString(out.TopicArn)
	var queueURL *string
	remove := func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		ctx := context.WithoutCancel(ctx)
		_, _ = n.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: out.TopicArn})
		if queueURL != nil {
			_, _ = q.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queueURL})
		}
	}
	fail := func(err error) (EventsTopic, func(), error) {
		remove()
		return EventsTopic{}, nil, err
	}

	queue, err := q.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(auditName),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		return fail(fmt.Errorf("testkit: create queue %s: %w", auditName, err))
	}
	queueURL, topic.AuditQueueURL = queue.QueueUrl, aws.ToString(queue.QueueUrl)
	attrs, err := q.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queueURL,
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		return fail(fmt.Errorf("testkit: arn of %s: %w", auditName, err))
	}
	queueARN := attrs.Attributes["QueueArn"]
	policy, err := auditPolicy(topic.ARN, queueARN)
	if err != nil {
		return fail(err)
	}
	if _, err := q.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   queueURL,
		Attributes: map[string]string{"Policy": policy},
	}); err != nil {
		return fail(fmt.Errorf("testkit: policy of %s: %w", auditName, err))
	}
	if _, err := n.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: out.TopicArn, Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN), Attributes: map[string]string{"RawMessageDelivery": "true"},
	}); err != nil {
		return fail(fmt.Errorf("testkit: subscribe %s: %w", auditName, err))
	}
	return topic, remove, nil
}

// auditPolicy renders the queue policy that lets the topic deliver to the queue.
func auditPolicy(topicARN, queueARN string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "aws", "policies", "wallet-events-audit.json"))
	if err != nil {
		return "", fmt.Errorf("testkit: read the audit queue policy: %w", err)
	}
	return strings.NewReplacer("${AUDIT_QUEUE_ARN}", queueARN, "${TOPIC_ARN}", topicARN).Replace(string(raw)), nil
}
```

e `test/testkit/audit.go`:

```go
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// AuditTimeout bounds how long WaitFor waits for the events.
const AuditTimeout = 10 * time.Second

// Attribute is one message attribute of a delivery.
type Attribute struct {
	Type  string // String or Number
	Value string
}

// AuditMessage is one delivery read from the audit queue.
type AuditMessage struct {
	Body        []byte
	GroupID     string
	DedupID     string
	Attributes  map[string]Attribute
	ContractErr error // the body violates api/events.yaml
}

// Audit collects the audit queue of one events topic. Every delivery is kept
// by eventId, so tests running in parallel over the same queue each find their
// own events, and each delivery is checked against the event contract (spec M4,
// decision 17). Deliveries are deleted from the queue as soon as they are read.
type Audit struct {
	client   *sqs.Client
	url      string
	contract *EventContract

	mu   sync.Mutex
	byID map[string][]AuditMessage // "" keeps bodies without a readable eventId
}

// NewAudit reads queueURL and validates every delivery against the event contract.
func NewAudit(ctx context.Context, client *sqs.Client, queueURL string) (*Audit, error) {
	contract, err := LoadEventContract(ctx)
	if err != nil {
		return nil, err
	}
	return &Audit{client: client, url: queueURL, contract: contract, byID: map[string][]AuditMessage{}}, nil
}

// Wait receives until every id has at least one delivery, or ctx ends. It
// returns every delivery seen so far of the ids.
func (a *Audit) Wait(ctx context.Context, ids ...string) (map[string][]AuditMessage, error) {
	for {
		got, missing := a.lookup(ids)
		if len(missing) == 0 {
			return got, nil
		}
		if err := a.receive(ctx); err != nil {
			return got, fmt.Errorf("audit: %d of %d events not delivered (first: %v): %w",
				len(missing), len(ids), missing[:min(3, len(missing))], err)
		}
	}
}

// WaitFor is Wait with AuditTimeout, failing tb on a missing id or on a
// delivery off the contract. It detaches from the test's context, so it also
// works in t.Cleanup.
func (a *Audit) WaitFor(tb testing.TB, ids ...string) map[string][]AuditMessage {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), AuditTimeout)
	defer cancel()
	got, err := a.Wait(ctx, ids...)
	if err != nil {
		tb.Fatal(err)
	}
	for id, deliveries := range got {
		for _, m := range deliveries {
			if m.ContractErr != nil {
				tb.Errorf("event %s: %v", id, m.ContractErr)
			}
		}
	}
	return got
}

// Absent receives during window and fails tb if any of ids arrives.
func (a *Audit) Absent(tb testing.TB, window time.Duration, ids ...string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), window)
	defer cancel()
	for a.receive(ctx) == nil {
	}
	for id, deliveries := range a.lookupAll(ids) {
		if len(deliveries) > 0 {
			tb.Errorf("event %s delivered %d time(s), want none", id, len(deliveries))
		}
	}
}

func (a *Audit) lookup(ids []string) (map[string][]AuditMessage, []string) {
	got := a.lookupAll(ids)
	var missing []string
	for _, id := range ids {
		if len(got[id]) == 0 {
			missing = append(missing, id)
		}
	}
	return got, missing
}

func (a *Audit) lookupAll(ids []string) map[string][]AuditMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	got := make(map[string][]AuditMessage, len(ids))
	for _, id := range ids {
		got[id] = slices.Clone(a.byID[id])
	}
	return got
}

// receive reads one batch (long poll of 1 s), records it and deletes it.
func (a *Audit) receive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := a.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(a.url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameMessageDeduplicationId,
		},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return err
	}
	if len(out.Messages) == 0 {
		return nil
	}
	entries := make([]types.DeleteMessageBatchRequestEntry, 0, len(out.Messages))
	a.mu.Lock()
	for i, m := range out.Messages {
		a.record(m)
		entries = append(entries, types.DeleteMessageBatchRequestEntry{Id: aws.String(fmt.Sprint(i)), ReceiptHandle: m.ReceiptHandle})
	}
	a.mu.Unlock()
	_, err = a.client.DeleteMessageBatch(context.WithoutCancel(ctx), &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(a.url), Entries: entries})
	return err
}

// record keeps one delivery; a.mu is held.
func (a *Audit) record(m types.Message) {
	body := []byte(aws.ToString(m.Body))
	msg := AuditMessage{
		Body:        body,
		GroupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
		DedupID:     m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
		Attributes:  map[string]Attribute{},
		ContractErr: a.contract.Validate(body),
	}
	for name, v := range m.MessageAttributes {
		msg.Attributes[name] = Attribute{Type: aws.ToString(v.DataType), Value: aws.ToString(v.StringValue)}
	}
	var head struct {
		EventID string `json:"eventId"`
	}
	_ = json.Unmarshal(body, &head) // an unreadable body stays under ""
	a.byID[head.EventID] = append(a.byID[head.EventID], msg)
}
```

- [ ] **Passo 5: ver passar.** `go test -tags=integration -race -count=1 -run '^TestAuditCollector$' ./test/integration/`: PASS em ~1 s.

**Checkpoint:** `go test -tags=integration -race -count=1 ./test/integration/` verde; o app em teste continua igual.

---

### Tarefa 3: configuração do publisher e tópico isolado do app em teste

As 7 variáveis da spec §3.5. O `StartApp` valida a config, então a mesma tarefa dá ao app em teste o tópico isolado, com `App.Audit`, e os tempos acelerados do test-plan §3.3.

**Arquivos:**
- Implementação: `internal/config/config.go`, `internal/config/validate.go`; `test/testkit/env.go`, `test/testkit/aws.go`, `test/testkit/app.go` (alterar)
- Testes: `internal/config/config_test.go` (alterar)

**Interfaces:**
- Consome: `CreateEventsTopic`, `NewAudit` (Tarefa 2).
- Produz:
  - os campos de `config.Config`: `SNSEventsTopicName string`, `OutboxBatchSize int`, `OutboxLease`, `OutboxPollInterval time.Duration`, `OutboxConcurrency int` e `OutboxRetryBaseDelay`, `OutboxRetryMaxDelay time.Duration`;
  - `testkit.App.Audit *testkit.Audit`;
  - o `Env.Config()` com os tempos da outbox.

- [ ] **Passo 1: stub dos campos**, sem tags nem validação. Em `internal/config/config.go`, depois de `ReferenceTTL`:

```go

	// Outbox publisher (D-13, messaging.md §5.1).
	SNSEventsTopicName   string
	OutboxBatchSize      int
	OutboxLease          time.Duration
	OutboxPollInterval   time.Duration
	OutboxConcurrency    int
	OutboxRetryBaseDelay time.Duration
	OutboxRetryMaxDelay  time.Duration
```

- [ ] **Passo 2: escrever os testes** em `internal/config/config_test.go`:
  - em `allVars`, acrescentar ao fim:

```go
	"SNS_EVENTS_TOPIC_NAME", "OUTBOX_BATCH_SIZE", "OUTBOX_LEASE", "OUTBOX_POLL_INTERVAL", "OUTBOX_CONCURRENCY",
	"OUTBOX_RETRY_BASE_DELAY", "OUTBOX_RETRY_MAX_DELAY",
```

  - em `validConfig()`, trocar `ReferenceTTL: 10 * time.Minute,` por:

```go
		ReferenceTTL:       10 * time.Minute,
		SNSEventsTopicName: "wallet-events.fifo",
		OutboxBatchSize:    50, OutboxLease: 30 * time.Second, OutboxPollInterval: 500 * time.Millisecond,
		OutboxConcurrency: 8, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute,
```

  - em `TestLoad_ReadsEnvironment`, trocar `"REFERENCE_TTL": "3s",` por:

```go
		"REFERENCE_TTL": "3s", "SNS_EVENTS_TOPIC_NAME": "events.fifo", "OUTBOX_BATCH_SIZE": "10",
		"OUTBOX_LEASE": "2s", "OUTBOX_POLL_INTERVAL": "100ms", "OUTBOX_CONCURRENCY": "2",
		"OUTBOX_RETRY_BASE_DELAY": "100ms", "OUTBOX_RETRY_MAX_DELAY": "1s",
```

    e, no `want`, depois de `ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,`:

```go
		SNSEventsTopicName: "events.fifo", OutboxBatchSize: 10, OutboxLease: 2 * time.Second,
		OutboxPollInterval: 100 * time.Millisecond, OutboxConcurrency: 2,
		OutboxRetryBaseDelay: 100 * time.Millisecond, OutboxRetryMaxDelay: time.Second,
```

  - em `TestValidate_RejectsInvalidValues`, depois do caso `"zero ttl"`:

```go
		{"non-fifo topic", func(c *config.Config) { c.SNSEventsTopicName = "wallet-events" }, "SNS_EVENTS_TOPIC_NAME"},
		{"zero batch", func(c *config.Config) { c.OutboxBatchSize = 0 }, "OUTBOX_BATCH_SIZE"},
		{"batch above 1000", func(c *config.Config) { c.OutboxBatchSize = 1001 }, "OUTBOX_BATCH_SIZE"},
		{"zero lease", func(c *config.Config) { c.OutboxLease = 0 }, "OUTBOX_LEASE"},
		{"zero poll interval", func(c *config.Config) { c.OutboxPollInterval = 0 }, "OUTBOX_POLL_INTERVAL"},
		{"zero concurrency", func(c *config.Config) { c.OutboxConcurrency = 0 }, "OUTBOX_CONCURRENCY"},
		{"zero outbox base delay", func(c *config.Config) { c.OutboxRetryBaseDelay = 0 }, "OUTBOX_RETRY_BASE_DELAY"},
		{"outbox max below base", func(c *config.Config) { c.OutboxRetryMaxDelay = c.OutboxRetryBaseDelay / 2 }, "OUTBOX_RETRY_MAX_DELAY"},
		{"outbox max above a day", func(c *config.Config) { c.OutboxRetryMaxDelay = 25 * time.Hour }, "OUTBOX_RETRY_MAX_DELAY"},
```

  - em `TestLoad_ErrorsNeverContainValues`, depois do caso `"unparsable flag"`:

```go
		{"unparsable lease", "OUTBOX_LEASE", "long-lease-5", "long-lease-5", "OUTBOX_LEASE"},
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 ./internal/config/`
  - Esperado: FAIL.
    - `TestLoad_AppliesDefaults` e `TestLoad_ReadsEnvironment` mostram os 7 campos zerados.
    - Os 9 casos novos de `TestValidate_RejectsInvalidValues` dão `Validate() vars = [], want [...]`.
    - `unparsable lease` dá `Load() error = nil, want error`.

- [ ] **Passo 4: implementar.** Em `config.go`, trocar o bloco do passo 1 por:

```go

	// Outbox publisher (D-13, messaging.md §5.1).
	SNSEventsTopicName   string        `env:"SNS_EVENTS_TOPIC_NAME" envDefault:"wallet-events.fifo"`
	OutboxBatchSize      int           `env:"OUTBOX_BATCH_SIZE" envDefault:"50"`
	OutboxLease          time.Duration `env:"OUTBOX_LEASE" envDefault:"30s"`
	OutboxPollInterval   time.Duration `env:"OUTBOX_POLL_INTERVAL" envDefault:"500ms"`
	OutboxConcurrency    int           `env:"OUTBOX_CONCURRENCY" envDefault:"8"`
	OutboxRetryBaseDelay time.Duration `env:"OUTBOX_RETRY_BASE_DELAY" envDefault:"1s"`
	OutboxRetryMaxDelay  time.Duration `env:"OUTBOX_RETRY_MAX_DELAY" envDefault:"5m"`
```

  Em `validate.go`:
  - no `REFERENCE_RETRY_MAX_DELAY`, trocar `maxReferenceRetryDelay` por `maxRetryDelay` (as duas ocorrências);
  - antes de `return errors.Join(errs...)`, acrescentar `c.validateOutbox(fail)`;
  - trocar a constante `maxReferenceRetryDelay` (e o seu comentário) por:

```go
// validateOutbox checks the publisher settings (messaging.md §5.1).
func (c Config) validateOutbox(fail func(v, reason string)) {
	if !strings.HasSuffix(c.SNSEventsTopicName, ".fifo") {
		fail("SNS_EVENTS_TOPIC_NAME", "must end with .fifo")
	}
	if c.OutboxBatchSize < 1 || c.OutboxBatchSize > maxOutboxBatchSize {
		fail("OUTBOX_BATCH_SIZE", "must be between 1 and 1000")
	}
	if c.OutboxLease <= 0 {
		fail("OUTBOX_LEASE", "must be greater than 0")
	}
	if c.OutboxPollInterval <= 0 {
		fail("OUTBOX_POLL_INTERVAL", "must be greater than 0")
	}
	if c.OutboxConcurrency < 1 {
		fail("OUTBOX_CONCURRENCY", "must be at least 1")
	}
	if c.OutboxRetryBaseDelay <= 0 {
		fail("OUTBOX_RETRY_BASE_DELAY", "must be greater than 0")
	}
	if c.OutboxRetryMaxDelay < c.OutboxRetryBaseDelay || c.OutboxRetryMaxDelay > maxRetryDelay {
		fail("OUTBOX_RETRY_MAX_DELAY", "must be between OUTBOX_RETRY_BASE_DELAY and "+maxRetryDelay.String())
	}
}

const (
	// maxRetryDelay bounds the retry delays; it is the upper bound
	// wagering.NewReferenceRetryPolicy accepts.
	maxRetryDelay = 24 * time.Hour
	// maxOutboxBatchSize bounds the rows one claim leases.
	maxOutboxBatchSize = 1000
)
```

- [ ] **Passo 5: ver passar.** `go test -race -count=1 ./internal/config/`: PASS.
- [ ] **Passo 6: ver a integração falhar.** `go test -tags=integration -race -count=1 ./test/integration/`
  - Esperado: FAIL no `TestMain`, com `testkit.StartApp: testkit: app config: config: SNS_EVENTS_TOPIC_NAME: must end with .fifo` e os 5 erros dos campos da outbox.

- [ ] **Passo 7: implementar o app em teste.**
  - Em `test/testkit/env.go`:
    - trocar o comentário do `Env` por:

```go
// Env is the isolated infrastructure of one test package (test-plan §3.2): the
// database. The queues and the events topic are created by StartApp, or by
// the TestMain of a package that needs them without the app.
```

    - trocar o `Config()` inteiro por:

```go
// Config points at the isolated database with the accelerated times of
// test-plan §3.3. The resources outside the database (queues, topic, IdP) are
// filled by whoever creates them.
func (e *Env) Config() config.Config {
	return config.Config{
		LogLevel:             "error",
		ShutdownTimeout:      5 * time.Second,
		DatabaseURL:          e.DB.AppURL,
		DBMaxConns:           4,
		DBLockTimeout:        2 * time.Second,
		OutboxBatchSize:      50,
		OutboxLease:          2 * time.Second,
		OutboxPollInterval:   100 * time.Millisecond,
		OutboxConcurrency:    8,
		OutboxRetryBaseDelay: 100 * time.Millisecond,
		OutboxRetryMaxDelay:  time.Second,
	}
}
```

  - Em `test/testkit/aws.go`, o `rootAWS` passa a devolver o `aws.Config`:
    - o comentário termina em `…and returns a config with the same key. For TestMain, where t.Setenv is not available.`;
    - a assinatura vira `func rootAWS(ctx context.Context) (aws.Config, error)`;
    - todo `return nil, err` vira `return aws.Config{}, err`, e o `return nil, fmt.Errorf(…)` vira `return aws.Config{}, fmt.Errorf(…)`;
    - o último `return sqs.NewFromConfig(cfg), nil` vira `return cfg, nil`.
  - Em `test/testkit/app.go`:
    - imports: acrescentar `"github.com/aws/aws-sdk-go-v2/service/sns"` e `"github.com/aws/aws-sdk-go-v2/service/sqs"` antes de `"go.uber.org/fx"`;
    - no `App`, depois de `MetricsURL string`:

```go
	// Audit reads the audit queue of the app's isolated events topic.
	Audit *Audit
```

    - trocar o comentário e o início do `StartApp`, até `cfg.WagerQueueName, cfg.WagerDLQName = wager, dlq`, por:

```go
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
```

    - no restante do `StartApp`, trocar as 4 chamadas a `removeQueues()` (validação, contrato, start e `stop`) por `removeAWS()`;
    - no literal do `App`, acrescentar `Audit: audit,` depois de `MetricsURL: "http://" + metricsAddr,`.

- [ ] **Passo 8: ver passar.** `go test -tags=integration -race -count=1 ./test/integration/ ./internal/bootstrap/`: PASS.
  - O `bootstrap` monta a config como literal e não valida; ele ganha os campos da outbox na Tarefa 5.

**Checkpoint:** `go test -race ./internal/config/` e `go test -tags=integration -race -count=1 ./test/...` verdes.

---

### Tarefa 4: porta `OutboxStore` e adapter PostgreSQL

Claim, confirmação, falha e backlog do data-model §6 (spec §3.1–§3.2). O teste tem um banco só dele, porque o claim vê a tabela inteira (spec decisão 21).

**Arquivos:**
- Implementação: `internal/app/ports.go`, `internal/adapters/postgres/outbox_repo.go`, `internal/adapters/postgres/module.go`, `test/testkit/env.go` (alterar)
- Testes: `internal/adapters/postgres/outbox_store_integration_test.go` (criar); `internal/bootstrap/bootstrap_test.go` (alterar)

**Interfaces:**
- Consome: `translate` (`errors.go`), `ident.Valid`, `insert`/`outboxRow`/`row.with`/`ts`/`newID` (testes do pacote `postgres`, M2); `NewEnv` (`testkit`).
- Produz:
  - `app.OutboxStore` (spec §3.1), `app.PendingEvent` e `app.OutboxBacklog`;
  - `postgres.NewOutboxStore(*pgxpool.Pool) *postgres.OutboxStore`;
  - `testkit.NewTestEnv(tb testing.TB, name string) *testkit.Env`.

- [ ] **Passo 1: a porta.** Em `internal/app/ports.go`, depois de `OutboxRepository`, colar o bloco da spec §3.1 (`OutboxStore`, `PendingEvent`, `OutboxBacklog`), com os comentários como estão lá.

- [ ] **Passo 2: stub do store.** Ao fim de `internal/adapters/postgres/outbox_repo.go`:

```go

// OutboxStore implements app.OutboxStore over the pool (D-13).
type OutboxStore struct{ pool *pgxpool.Pool }

var _ app.OutboxStore = (*OutboxStore)(nil)

// NewOutboxStore is the store the outbox publisher uses.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }

func (s *OutboxStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.PendingEvent, error) {
	return nil, nil
}

func (s *OutboxStore) MarkPublished(ctx context.Context, eventID, owner string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func (s *OutboxStore) MarkFailed(ctx context.Context, eventID, owner string, retryIn time.Duration, reason string) (bool, error) {
	return false, nil
}

func (s *OutboxStore) Backlog(ctx context.Context) (app.OutboxBacklog, error) {
	return app.OutboxBacklog{}, nil
}
```

  Os imports passam a incluir `"time"`, `"github.com/jackc/pgx/v5/pgxpool"` e `"github.com/KaioVinicios/pda/internal/app"`.

- [ ] **Passo 3: `NewTestEnv`.** Em `test/testkit/env.go`, antes do `Config()`, e com `"testing"` nos imports:

```go
// NewTestEnv is NewEnv for one test: a database of its own, for tests whose
// assertions cover a whole table, such as the outbox claim. The database is
// dropped at cleanup.
func NewTestEnv(tb testing.TB, name string) *Env {
	tb.Helper()
	ctx, cancel := context.WithTimeout(tb.Context(), time.Minute)
	defer cancel()
	env, cleanup, err := NewEnv(ctx, name)
	if err != nil {
		tb.Fatalf("testkit.NewEnv: %v", err)
	}
	tb.Cleanup(cleanup)
	return env
}
```

- [ ] **Passo 4: escrever o teste** `internal/adapters/postgres/outbox_store_integration_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// storeFixture is a database of its own: the claim sees the whole table.
type storeFixture struct {
	t     *testing.T
	env   *testkit.Env
	store *postgres.OutboxStore
}

// due inserts an unpublished event, due now, and returns its id.
func (f storeFixture) due(ctx context.Context, extra ...any) string {
	f.t.Helper()
	id := newID()
	if err := insert(ctx, f.env.Owner, "outbox_events", outboxRow(id, newID()).with(extra...)); err != nil {
		f.t.Fatalf("insert event: %v", err)
	}
	return id
}

// settle publishes every pending event, so the next case starts empty.
func (f storeFixture) settle(ctx context.Context) {
	f.t.Helper()
	if _, err := f.env.Owner.Exec(ctx, `UPDATE outbox_events
		SET published_at = now(), locked_by = NULL, locked_until = NULL WHERE published_at IS NULL`); err != nil {
		f.t.Fatalf("settle: %v", err)
	}
}

// outboxState is the publication columns of one event; leaseIn and nextIn are
// milliseconds from the database now (locked_until − now, next_attempt_at − now).
type outboxState struct {
	lockedBy        *string
	leaseIn, nextIn int64
	attempts        int
	published       bool
	lastError       *string
}

func (f storeFixture) state(ctx context.Context, id string) outboxState {
	f.t.Helper()
	var s outboxState
	if err := f.env.Owner.QueryRow(ctx, `SELECT locked_by,
		COALESCE(EXTRACT(EPOCH FROM locked_until - now()) * 1000, 0)::bigint,
		(EXTRACT(EPOCH FROM next_attempt_at - now()) * 1000)::bigint,
		attempts, published_at IS NOT NULL, last_error FROM outbox_events WHERE event_id = $1`, id).
		Scan(&s.lockedBy, &s.leaseIn, &s.nextIn, &s.attempts, &s.published, &s.lastError); err != nil {
		f.t.Fatalf("read event %s: %v", id, err)
	}
	return s
}

func ids(events []app.PendingEvent) []string {
	out := make([]string, 0, len(events))
	for i := range events {
		out = append(out, events[i].EventID)
	}
	slices.Sort(out)
	return out
}

func sorted(ids ...string) []string { slices.Sort(ids); return ids }

// Covers: OUT-03, OUT-04, OUT-06 (I18: outbox store, D-13)
func TestOutboxStore(t *testing.T) {
	t.Parallel()
	e := testkit.NewTestEnv(t, "outboxstore")
	f := storeFixture{t: t, env: e, store: postgres.NewOutboxStore(e.App)}
	ctx := t.Context()
	const lease = 30 * time.Second

	t.Run("claim leases only due, unleased, unpublished events", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		wallet := newID()
		due := f.due(ctx, "message_group_id", wallet, "correlation_id", "corr-claim")
		f.due(ctx, "next_attempt_at", time.Now().Add(time.Hour))                             // scheduled later
		f.due(ctx, "published_at", ts)                                                       // already published
		f.due(ctx, "locked_by", "other-instance", "locked_until", time.Now().Add(time.Hour)) // live lease

		got, err := f.store.Claim(ctx, "me", lease, 10)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Claim = %v, want only %s", ids(got), due)
		}
		ev := got[0]
		var payload map[string]any
		if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload["eventId"] != due {
			t.Fatalf("payload %s (%v), want the stored JSON", ev.Payload, err)
		}
		if ev.EventID != due || ev.MessageGroupID != wallet || ev.EventType != "WalletBalanceChanged" ||
			ev.EventVersion != 1 || ev.CorrelationID != "corr-claim" || !ev.OccurredAt.Equal(ts) ||
			ev.Attempts != 0 || ev.Reclaimed {
			t.Fatalf("Claim = %+v, want the row of %s (group %s, corr-claim, occurred %v, 0 attempts, not reclaimed)", ev, due, wallet, ts)
		}
		if s := f.state(ctx, due); s.lockedBy == nil || *s.lockedBy != "me" || s.leaseIn < 29_000 || s.leaseIn > 31_000 {
			t.Fatalf("after the claim: locked_by %v, lease in %dms, want me and ~30s", s.lockedBy, s.leaseIn)
		}
		if again, err := f.store.Claim(ctx, "someone-else", lease, 10); err != nil || len(again) != 0 {
			t.Fatalf("second Claim = %v, %v; want nothing while leased", ids(again), err)
		}
	})

	t.Run("claim takes the earliest due first, up to the limit", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		first := f.due(ctx, "next_attempt_at", ts)
		second := f.due(ctx, "next_attempt_at", ts.Add(time.Second))
		f.due(ctx, "next_attempt_at", ts.Add(2*time.Second))
		got, err := f.store.Claim(ctx, "me", lease, 2)
		if err != nil || !slices.Equal(ids(got), sorted(first, second)) {
			t.Fatalf("Claim(limit 2) = %v, %v; want %v", ids(got), err, sorted(first, second))
		}
	})

	t.Run("concurrent claims take disjoint events", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		var all []string
		for range 40 {
			all = append(all, f.due(ctx))
		}
		var wg sync.WaitGroup
		claimed := make([][]app.PendingEvent, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, owner := range []string{"a", "b"} {
			wg.Go(func() {
				<-start
				claimed[i], errs[i] = f.store.Claim(ctx, owner, lease, 25)
			})
		}
		close(start)
		wg.Wait()
		if errs[0] != nil || errs[1] != nil {
			t.Fatalf("Claim errors: %v, %v", errs[0], errs[1])
		}
		union := append(ids(claimed[0]), ids(claimed[1])...)
		slices.Sort(union)
		if !slices.Equal(union, sorted(all...)) {
			t.Fatalf("claims took %d and %d events (%d distinct), want the 40 exactly once",
				len(claimed[0]), len(claimed[1]), len(slices.Compact(union)))
		}
	})

	t.Run("an expired lease is reclaimed", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		abandoned := f.due(ctx, "locked_by", "dead-instance", "locked_until", time.Now().Add(-time.Second), "attempts", 2)
		got, err := f.store.Claim(ctx, "me", lease, 10)
		if err != nil || len(got) != 1 || got[0].EventID != abandoned || !got[0].Reclaimed || got[0].Attempts != 2 {
			t.Fatalf("Claim = %+v, %v; want %s reclaimed with 2 attempts", got, err, abandoned)
		}
		if s := f.state(ctx, abandoned); s.lockedBy == nil || *s.lockedBy != "me" {
			t.Fatalf("locked_by = %v, want me", s.lockedBy)
		}
	})

	t.Run("only the lease owner confirms", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		id := f.due(ctx)
		if _, err := f.store.Claim(ctx, "me", lease, 10); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := f.store.MarkPublished(ctx, id, "other-instance"); err != nil || ok {
			t.Fatalf("MarkPublished by another owner = %v, %v; want false, nil", ok, err)
		}
		before := time.Now()
		at, ok, err := f.store.MarkPublished(ctx, id, "me")
		if err != nil || !ok || at.Before(before.Add(-time.Minute)) || at.After(time.Now().Add(time.Minute)) {
			t.Fatalf("MarkPublished = %v, %v, %v; want now, true", at, ok, err)
		}
		if s := f.state(ctx, id); !s.published || s.lockedBy != nil || s.leaseIn != 0 {
			t.Fatalf("after the confirmation: %+v, want published and unleased", s)
		}
		if _, ok, err := f.store.MarkPublished(ctx, id, "me"); err != nil || ok {
			t.Fatalf("second MarkPublished = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("a failure counts the attempt, reschedules and releases the lease", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		id := f.due(ctx)
		if _, err := f.store.Claim(ctx, "me", lease, 10); err != nil {
			t.Fatal(err)
		}
		if ok, err := f.store.MarkFailed(ctx, id, "other-instance", time.Minute, "boom"); err != nil || ok {
			t.Fatalf("MarkFailed by another owner = %v, %v; want false, nil", ok, err)
		}
		if ok, err := f.store.MarkFailed(ctx, id, "me", time.Minute, "sns: throttled"); err != nil || !ok {
			t.Fatalf("MarkFailed = %v, %v; want true, nil", ok, err)
		}
		s := f.state(ctx, id)
		if s.attempts != 1 || s.lockedBy != nil || s.published || s.nextIn < 59_000 || s.nextIn > 61_000 ||
			s.lastError == nil || *s.lastError != "sns: throttled" {
			t.Fatalf("after the failure: %+v (last_error %v), want 1 attempt, unleased, next in ~60s", s, s.lastError)
		}
		if got, err := f.store.Claim(ctx, "me", lease, 10); err != nil || len(got) != 0 {
			t.Fatalf("Claim before the next attempt = %v, %v; want nothing", ids(got), err)
		}
	})

	t.Run("a malformed id is a lost lease, not a database error", func(t *testing.T) {
		if _, ok, err := f.store.MarkPublished(ctx, "not-a-uuid", "me"); err != nil || ok {
			t.Fatalf("MarkPublished = %v, %v; want false, nil", ok, err)
		}
		if ok, err := f.store.MarkFailed(ctx, "not-a-uuid", "me", time.Second, "x"); err != nil || ok {
			t.Fatalf("MarkFailed = %v, %v; want false, nil", ok, err)
		}
	})

	t.Run("backlog", func(t *testing.T) {
		f := storeFixture{t: t, env: e, store: f.store}
		defer f.settle(ctx)
		if b, err := f.store.Backlog(ctx); err != nil || b != (app.OutboxBacklog{}) {
			t.Fatalf("empty Backlog = %+v, %v; want zero", b, err)
		}
		f.due(ctx, "occurred_at", time.Now().Add(-10*time.Second))
		f.due(ctx, "occurred_at", time.Now().Add(-5*time.Second))
		f.due(ctx, "occurred_at", time.Now().Add(-time.Hour), "published_at", time.Now())
		b, err := f.store.Backlog(ctx)
		if err != nil || b.Pending != 2 || b.OldestAge < 9*time.Second || b.OldestAge > 15*time.Second {
			t.Fatalf("Backlog = %+v, %v; want 2 pending, oldest ~10s", b, err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := f.store.Claim(canceled, "me", lease, 10); err == nil {
			t.Fatal("Claim with a canceled context = nil error")
		}
	})
}
```

- [ ] **Passo 5: ver falhar.** `go test -tags=integration -race -count=1 -run '^TestOutboxStore$' ./internal/adapters/postgres/`
  - Esperado: FAIL em 8 subtestes. Exemplos: `Claim = [], want only …`, `MarkPublished = 0001-01-01 …, false, <nil>; want now, true`, `Backlog = {Pending:0 OldestAge:0s}…` e `Claim with a canceled context = nil error`.
  - O subteste do id malformado passa no stub: é uma guarda, e o passo 8 prova a sensibilidade dele.

- [ ] **Passo 6: implementar.** Em `outbox_repo.go`, os imports ficam:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/ident"
)
```

  e o bloco do stub vira:

```go
// OutboxStore implements app.OutboxStore over the pool (D-13): each method
// is one statement, so the claim is the short transaction of the publisher.
type OutboxStore struct{ pool *pgxpool.Pool }

var _ app.OutboxStore = (*OutboxStore)(nil)

// NewOutboxStore is the store the outbox publisher uses.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }

// Claim is the claim of data-model §6. A previous owner means the lease had
// expired: abandoned work taken over.
func (s *OutboxStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.PendingEvent, error) {
	rows, err := s.pool.Query(ctx, `WITH due AS (
			SELECT event_id, locked_by AS previous_owner FROM outbox_events
			WHERE published_at IS NULL
			  AND next_attempt_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE outbox_events o
		SET locked_by = $2, locked_until = now() + $3::interval
		FROM due WHERE o.event_id = due.event_id
		RETURNING o.event_id::text, o.message_group_id, o.event_type, o.event_version,
		          o.correlation_id, o.payload, o.occurred_at, o.attempts, due.previous_owner`,
		limit, owner, lease)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []app.PendingEvent
	for rows.Next() {
		var e app.PendingEvent
		var previous *string
		if err := rows.Scan(&e.EventID, &e.MessageGroupID, &e.EventType, &e.EventVersion,
			&e.CorrelationID, &e.Payload, &e.OccurredAt, &e.Attempts, &previous); err != nil {
			return nil, translate(err)
		}
		e.Reclaimed = previous != nil
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// MarkPublished is the conditional confirmation of data-model §6.
func (s *OutboxStore) MarkPublished(ctx context.Context, eventID, owner string) (time.Time, bool, error) {
	if !ident.Valid(eventID) { // not a canonical UUID: no such lease, and no 22P02 from the database
		return time.Time{}, false, nil
	}
	var at time.Time
	err := s.pool.QueryRow(ctx, `UPDATE outbox_events
		SET published_at = now(), locked_by = NULL, locked_until = NULL
		WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL
		RETURNING published_at`, eventID, owner).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, translate(err)
	}
	return at, true, nil
}

// MarkFailed is the failure of data-model §6: the attempt is counted, the
// next one scheduled and the lease released.
func (s *OutboxStore) MarkFailed(ctx context.Context, eventID, owner string, retryIn time.Duration, reason string) (bool, error) {
	if !ident.Valid(eventID) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events
		SET attempts = attempts + 1, next_attempt_at = now() + $3::interval,
		    locked_by = NULL, locked_until = NULL, last_error = $4
		WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, owner, retryIn, reason)
	if err != nil {
		return false, translate(err)
	}
	return tag.RowsAffected() == 1, nil
}

// Backlog is the backlog query of data-model §6.
func (s *OutboxStore) Backlog(ctx context.Context) (app.OutboxBacklog, error) {
	var b app.OutboxBacklog
	var ageMicros int64
	err := s.pool.QueryRow(ctx, `SELECT count(*),
		COALESCE(GREATEST(EXTRACT(EPOCH FROM now() - min(occurred_at)), 0) * 1000000, 0)::bigint
		FROM outbox_events WHERE published_at IS NULL`).Scan(&b.Pending, &ageMicros)
	if err != nil {
		return app.OutboxBacklog{}, translate(err)
	}
	b.OldestAge = time.Duration(ageMicros) * time.Microsecond
	return b, nil
}
```

- [ ] **Passo 7: ver passar.** `go test -tags=integration -race -count=1 -run '^TestOutboxStore$' -v ./internal/adapters/postgres/`: os 9 subtestes passam.
- [ ] **Passo 8: sensibilidade da guarda.** Trocar temporariamente o `if !ident.Valid(eventID) {…}` do `MarkPublished` por `_ = ident.Valid` e rodar `-run 'TestOutboxStore/malformed'`.
  - Esperado: `MarkPublished = false, PERMANENT: postgres: 22P02; want false, nil`. Desfazer.
- [ ] **Passo 9: registrar no Fx.** No `TestFxGraph` (`internal/bootstrap/bootstrap_test.go`):
  - declarar `store app.OutboxStore` depois de `reconcile *app.Reconcile`;
  - acrescentar `&store` ao `fx.Populate`;
  - o comentário `// Covers:` termina em `…use cases and API, M4 outbox)`.

  Rodar `go test -race -count=1 -run '^TestFxGraph$' ./internal/bootstrap/`.
  - Esperado: FAIL com `missing type: app.OutboxStore`.

  Em `internal/adapters/postgres/module.go`:
  - o comentário do `Module` passa a dizer `…the repositories over the pool for reads (D-14), the outbox store of the publisher (D-13) and the "postgres" health checker.`;
  - depois de `NewRepos,`, acrescentar `fx.Annotate(NewOutboxStore, fx.As(new(app.OutboxStore))),`.

  Rodar de novo: PASS.

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/postgres/ ./internal/bootstrap/` e `go test -race ./internal/...` verdes.

---

### Tarefa 5: tópico no `awsclient`, política IAM e isolamento do `bootstrap`

O ARN do tópico é resolvido no start (spec decisões 1–3 e 22). Esta tarefa também faz duas coisas:
- dá aos testes do `bootstrap` um banco próprio **antes** de o publisher entrar no grafo (decisão 20);
- verifica a permissão nova do usuário do serviço.

**Arquivos:**
- Implementação: `internal/adapters/awsclient/topic.go` (criar), `internal/adapters/awsclient/module.go` (alterar), `go.mod`/`go.sum`, `deploy/aws/policies/pda-wallet-service.json`, `test/testkit/postgres.go` (alterar)
- Testes: `internal/adapters/awsclient/topic_test.go` (criar); `internal/bootstrap/bootstrap_test.go`, `internal/bootstrap/bootstrap_integration_test.go`, `test/integration/provisioning_test.go` (alterar)

**Interfaces:**
- Consome: `config.SNSEventsTopicName` (Tarefa 3); `NewEventsTopic`, `NewTestEnv` (Tarefas 2 e 4).
- Produz:
  - `awsclient.Topic{ARN string}`;
  - `awsclient.CallerIdentityAPI` e `awsclient.TopicAPI`;
  - `awsclient.ResolveTopic(ctx, CallerIdentityAPI, TopicAPI, region, name string) (Topic, error)`;
  - no Fx: `*sts.Client` e `*awsclient.Topic` (resolvido no `OnStart`).

- [ ] **Passo 1: stub** `internal/adapters/awsclient/topic.go`:

```go
package awsclient

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// CallerIdentityAPI is the subset of *sts.Client used to learn the account.
type CallerIdentityAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

// TopicAPI is the subset of *sns.Client used to verify the topic.
type TopicAPI interface {
	GetTopicAttributes(ctx context.Context, in *sns.GetTopicAttributesInput, optFns ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
}

// Topic holds the resolved ARN of the events topic. It is filled on start.
type Topic struct {
	ARN string
}

// ResolveTopic builds the ARN of the topic name and verifies that it exists.
func ResolveTopic(ctx context.Context, id CallerIdentityAPI, api TopicAPI, region, name string) (Topic, error) {
	return Topic{}, errors.New("awsclient: not implemented")
}
```

- [ ] **Passo 2: escrever os testes** `internal/adapters/awsclient/topic_test.go`:

```go
package awsclient_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
)

// fakeIdentity answers GetCallerIdentity with arn, or fails with err.
type fakeIdentity struct {
	account, arn string
	err          error
}

func (f fakeIdentity) GetCallerIdentity(context.Context, *sts.GetCallerIdentityInput, ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &sts.GetCallerIdentityOutput{Account: aws.String(f.account), Arn: aws.String(f.arn)}, nil
}

// fakeTopics knows a fixed set of topic ARNs and records what was probed.
type fakeTopics struct {
	arns   map[string]bool
	probed []string
}

func (f *fakeTopics) GetTopicAttributes(_ context.Context, in *sns.GetTopicAttributesInput, _ ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	f.probed = append(f.probed, aws.ToString(in.TopicArn))
	if !f.arns[aws.ToString(in.TopicArn)] {
		return nil, errors.New("NotFound: Topic does not exist")
	}
	return &sns.GetTopicAttributesOutput{}, nil
}

var service = fakeIdentity{account: "000000000000", arn: "arn:aws:iam::000000000000:user/pda-wallet-service"}

// Covers: OUT-07, FX-02 (spec M4, decision 1)
func TestResolveTopic_BuildsTheARNFromTheCallerAndVerifiesIt(t *testing.T) {
	want := "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"
	api := &fakeTopics{arns: map[string]bool{want: true}}
	got, err := awsclient.ResolveTopic(t.Context(), service, api, "us-east-1", "wallet-events.fifo")
	if err != nil || got != (awsclient.Topic{ARN: want}) {
		t.Fatalf("ResolveTopic() = %+v, %v; want %s", got, err, want)
	}
	if len(api.probed) != 1 || api.probed[0] != want {
		t.Fatalf("GetTopicAttributes probed %v, want [%s]", api.probed, want)
	}
}

// Covers: OUT-07 (spec M4, decision 1)
func TestResolveTopic_KeepsThePartitionOfTheCaller(t *testing.T) {
	gov := fakeIdentity{account: "123456789012", arn: "arn:aws-us-gov:sts::123456789012:assumed-role/pda/x"}
	want := "arn:aws-us-gov:sns:us-gov-west-1:123456789012:wallet-events.fifo"
	got, err := awsclient.ResolveTopic(t.Context(), gov, &fakeTopics{arns: map[string]bool{want: true}}, "us-gov-west-1", "wallet-events.fifo")
	if err != nil || got.ARN != want {
		t.Fatalf("ResolveTopic() = %+v, %v; want %s", got, err, want)
	}
}

// Covers: FX-02 (spec M4, decision 1)
func TestResolveTopic_FailsNamingTheTopic(t *testing.T) {
	cases := map[string]awsclient.CallerIdentityAPI{
		"topic absent or not readable": service,
		"identity unavailable":         fakeIdentity{err: errors.New("UnrecognizedClientException")},
		"identity without an ARN":      fakeIdentity{account: "000000000000", arn: "not-an-arn"},
	}
	for name, id := range cases {
		_, err := awsclient.ResolveTopic(t.Context(), id, &fakeTopics{}, "us-east-1", "missing.fifo")
		if err == nil || !strings.Contains(err.Error(), "missing.fifo") {
			t.Errorf("%s: ResolveTopic() error = %v, want an error naming missing.fifo", name, err)
		}
	}
}
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 -run '^TestResolveTopic' ./internal/adapters/awsclient/`
  - Esperado: FAIL nos 3 testes com `awsclient: not implemented`.

- [ ] **Passo 4: implementar.** Em `topic.go`, os imports ficam:

```go
import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)
```

  e o `ResolveTopic` vira:

```go
// ResolveTopic builds the ARN of the topic name and verifies that it exists
// (spec M4, decision 1). SNS has no lookup by name: the partition and the
// account come from the caller identity, which needs no permission, and
// GetTopicAttributes, allowed on the topic only, fails fast when the topic is
// missing or unreadable.
func ResolveTopic(ctx context.Context, id CallerIdentityAPI, api TopicAPI, region, name string) (Topic, error) {
	who, err := id.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Topic{}, fmt.Errorf("awsclient: resolve topic %s: caller identity: %w", name, err)
	}
	caller, err := arn.Parse(aws.ToString(who.Arn))
	if err != nil {
		return Topic{}, fmt.Errorf("awsclient: resolve topic %s: caller identity: %w", name, err)
	}
	topicARN := arn.ARN{
		Partition: caller.Partition, Service: "sns", Region: region,
		AccountID: aws.ToString(who.Account), Resource: name,
	}.String()
	if _, err := api.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)}); err != nil {
		return Topic{}, fmt.Errorf("awsclient: read attributes of topic %s: %w", name, err)
	}
	return Topic{ARN: topicARN}, nil
}
```

  Rodar `go mod tidy`. O `git diff go.mod` mostra `service/sts v1.51.1` passando para o bloco direto.

- [ ] **Passo 5: ver passar.** `go test -race -count=1 ./internal/adapters/awsclient/`: PASS.

- [ ] **Passo 6: testes do Fx.**
  - **`TestFxGraph`:** declarar `topic *awsclient.Topic` e acrescentar `&topic` ao `fx.Populate`.
  - **`internal/bootstrap/bootstrap_integration_test.go`:**
    - imports: acrescentar `"github.com/aws/aws-sdk-go-v2/service/sns"` antes de `".../service/sqs"`;
    - trocar o `integrationConfig` por:

```go
// integrationConfig points the whole graph at a database, queues and topic of
// the test's own: with the outbox publisher in the graph, the shared pda
// database would have its pending events published to the test's topic.
func integrationConfig(t *testing.T) config.Config {
	t.Helper()
	env := testkit.NewTestEnv(t, "bootstrap")
	testkit.UseRootAWS(t)
	root := testkit.RootAWSConfig(t)
	wager, dlq := testkit.CreateQueues(t, sqs.NewFromConfig(root))
	topic := testkit.NewEventsTopic(t, sqs.NewFromConfig(root), sns.NewFromConfig(root))
	cfg := config.Config{
		LogLevel: "error", HTTPAddr: testkit.FreeAddr(t), MetricsAddr: testkit.FreeAddr(t),
		ShutdownTimeout: 5 * time.Second, DatabaseURL: env.DB.AppURL, DBMaxConns: 2,
		DBLockTimeout: 2 * time.Second, WagerQueueName: wager, WagerDLQName: dlq,
		OIDCIssuer: testkit.KeycloakIssuer, OIDCJWKSURL: testkit.KeycloakIssuer + "/protocol/openid-connect/certs",
		OIDCAudience: "pda-api", OIDCClockSkew: time.Second, APIDocsEnabled: true,
		ReferenceRetryBaseDelay: 100 * time.Millisecond, ReferenceRetryMaxDelay: time.Second,
		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
		SNSEventsTopicName: topic.Name, OutboxBatchSize: 50, OutboxLease: 2 * time.Second,
		OutboxPollInterval: 100 * time.Millisecond, OutboxConcurrency: 8,
		OutboxRetryBaseDelay: 100 * time.Millisecond, OutboxRetryMaxDelay: time.Second,
	}
	t.Setenv("DATABASE_URL", cfg.DatabaseURL) // config.Load stays valid even if Fx calls it
	return cfg
}
```

  - **`TestFxFailFast`:** o `// Covers:` passa a `FX-02, AUTH-02, OUT-07 (I07c)`, e depois do caso `"missing queue"` entra:

```go
		{"missing topic", func(c *config.Config) { c.SNSEventsTopicName = "missing-" + c.SNSEventsTopicName }, "missing-events-"},
```

  - **`test/testkit/postgres.go`:** remover a função `AppDatabaseURL` (e o seu comentário). Ela não tem mais uso, e é a armadilha do achado.

- [ ] **Passo 7: ver falhar.**
  - `go test -race -count=1 -run '^TestFxGraph$' ./internal/bootstrap/` dá FAIL com `missing type: *awsclient.Topic`.
  - `go test -tags=integration -race -count=1 -run '^TestFxFailFast$' ./internal/bootstrap/` dá FAIL em `missing_topic`, com `Start() error = nil, want fail-fast error`.

- [ ] **Passo 8: implementar o módulo.** Em `internal/adapters/awsclient/module.go`:
  - imports: acrescentar `"github.com/aws/aws-sdk-go-v2/service/sts"` depois de `.../service/sqs`;
  - o `Module` vira:

```go
// Module provides the SQS, SNS and STS clients, resolves the queues and the
// events topic on start (fail fast) and contributes the "sqs" health checker.
// SNS stays out of the readiness (spec M4, decision 3).
var Module = fx.Module("aws",
	fx.Provide(
		func(lc fx.Lifecycle) (aws.Config, error) { return NewAWSConfig(newHTTPClient(lc)) },
		func(c aws.Config) *sqs.Client { return sqs.NewFromConfig(c) },
		func(c aws.Config) *sns.Client { return sns.NewFromConfig(c) },
		func(c aws.Config) *sts.Client { return sts.NewFromConfig(c) },
		newQueues,
		newTopic,
		fx.Annotate(NewQueueChecker, fx.As(new(observability.Checker)), fx.ResultTags(`group:"health_checkers"`)),
	),
	// The topic is verified on start even before anything publishes to it.
	fx.Invoke(func(*Topic) {}),
)
```

  - ao fim do arquivo:

```go

func newTopic(lc fx.Lifecycle, cfg config.Config, awsCfg aws.Config, id *sts.Client, api *sns.Client, log *slog.Logger) *Topic {
	t := &Topic{}
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
		defer cancel()
		resolved, err := ResolveTopic(ctx, id, api, awsCfg.Region, cfg.SNSEventsTopicName)
		if err != nil {
			return err
		}
		*t = resolved
		log.Info("sns topic resolved", "topic", cfg.SNSEventsTopicName)
		return nil
	}})
	return t
}
```

- [ ] **Passo 9: ver passar.** `go test -race -count=1 ./internal/bootstrap/` e `go test -tags=integration -race -count=1 ./internal/bootstrap/`: PASS.
  - Sem o `fx.Invoke`, o `missing_topic` continua vermelho: nada depende do `*Topic`, e o Fx não o constrói (spec decisão 22).

- [ ] **Passo 10: a política (exceção de TDD, development-workflow §4.4).**
  - `make infra-up`: o `aws-init` reaplica a política atual do repositório.
  - No `TestProvisioning` (`test/integration/provisioning_test.go`), depois do teste de `GetQueueAttributes` do `pda-wallet-service`:

```go
	if _, err := sns.NewFromConfig(testkit.AWSConfigWithKeys(t, profiles["pda-wallet-service"])).GetTopicAttributes(t.Context(),
		&sns.GetTopicAttributesInput{TopicArn: aws.String(topicARN)}); err != nil {
		t.Fatalf("pda-wallet-service GetTopicAttributes on %s: %v (want allowed: the topic is verified on start)", topicName, err)
	}
```

    e, entre as duas linhas `// Sensitivity:` do cabeçalho:

```go
// Sensitivity (M4): before sns:GetTopicAttributes entered the policy, the service's check failed with AccessDenied.
```

  - Rodar `go test -tags=integration -race -count=1 -run '^TestProvisioning$' ./test/integration/`.
    - Esperado: FAIL com `api error AccessDenied: User: arn:aws:iam::000000000000:user/pda-wallet-service is not authorized to perform: sns:GetTopicAttributes`.
  - Em `deploy/aws/policies/pda-wallet-service.json`, o `PublishEvents` passa a `"Action": ["sns:Publish", "sns:GetTopicAttributes"],`.
  - `make infra-up` de novo, e o teste passa.

- [ ] **Passo 11: regressão.** `go test -tags=integration -race -count=1 ./test/integration/ ./internal/bootstrap/`: PASS.
- [ ] **Passo 12: verificação manual do isolamento** (Foco de revisão 4). Com as réplicas do compose ainda na imagem do M3 (sem publisher):
  - abrir uma carteira por `curl` na `:8081`, como o M3 fazia;
  - contar os pendentes: `docker compose exec -T postgres psql -U postgres -d pda -tAc "SELECT count(*) FROM outbox_events WHERE published_at IS NULL"`;
  - rodar `go test -tags=integration -race -count=1 ./internal/bootstrap/`;
  - contar de novo: o número não muda.

  (A verificação só vale de verdade depois da Tarefa 10, com o publisher no grafo; repetir lá, no passo 12.)

**Checkpoint:** `go test -race ./internal/...` e `go test -tags=integration -race -count=1 ./internal/bootstrap/ ./test/...` verdes.

---

### Tarefa 6: métricas de outbox

As 6 métricas do messaging §8 (spec §3.6, decisão 19). O `observability.Metrics` passa a implementar a porta `outbox.Metrics` da Tarefa 8.

**Arquivos:**
- Implementação: `internal/observability/metrics.go` (alterar)
- Testes: `internal/observability/metrics_test.go` (alterar)

**Interfaces:**
- Produz: `(*observability.Metrics).Published(eventType string, lag time.Duration)`, `PublishFailed(eventType string)`, `LeaseReclaimed()` e `Backlog(pending int, oldestAge time.Duration)`.

- [ ] **Passo 1: stubs.** Com `"time"` nos imports de `metrics.go`, acrescentar ao fim:

```go

// Published counts a confirmed publication and its lag (published − occurred).
func (m *Metrics) Published(eventType string, lag time.Duration) {}

// PublishFailed counts a failed publication attempt.
func (m *Metrics) PublishFailed(eventType string) {}

// LeaseReclaimed counts an event whose expired lease was taken over.
func (m *Metrics) LeaseReclaimed() {}

// Backlog sets the outbox lag gauges.
func (m *Metrics) Backlog(pending int, oldestAge time.Duration) {}
```

- [ ] **Passo 2: escrever o teste.** Com `"time"` nos imports de `metrics_test.go`, acrescentar:

```go

// Covers: OBS-03, OUT-03, OUT-04 (spec M4, decision 19; messaging.md §8)
func TestMetrics_Outbox(t *testing.T) {
	reg := observability.NewRegistry()
	m := observability.NewMetrics(reg)
	m.Published("WalletBalanceChanged", 250*time.Millisecond)
	m.Published("WalletBalanceChanged", 2*time.Second)
	m.PublishFailed("WagerTransactionProcessed")
	m.LeaseReclaimed()
	m.LeaseReclaimed()
	m.Backlog(3, 90*time.Second)

	want := `
# HELP outbox_lease_reclaims_total Outbox events whose expired lease was taken over by a publisher.
# TYPE outbox_lease_reclaims_total counter
outbox_lease_reclaims_total 2
# HELP outbox_oldest_pending_age_seconds Age of the oldest outbox event not yet published.
# TYPE outbox_oldest_pending_age_seconds gauge
outbox_oldest_pending_age_seconds 90
# HELP outbox_pending_events Outbox events not yet published.
# TYPE outbox_pending_events gauge
outbox_pending_events 3
# HELP outbox_publish_failures_total Failed publication attempts of outbox events.
# TYPE outbox_publish_failures_total counter
outbox_publish_failures_total{event_type="WagerTransactionProcessed"} 1
# HELP outbox_published_total Outbox events published to the events topic.
# TYPE outbox_published_total counter
outbox_published_total{event_type="WalletBalanceChanged"} 2
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "outbox_published_total",
		"outbox_publish_failures_total", "outbox_lease_reclaims_total", "outbox_pending_events",
		"outbox_oldest_pending_age_seconds"); err != nil {
		t.Fatal(err)
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() != "outbox_publish_lag_seconds" {
			continue
		}
		h := f.GetMetric()[0].GetHistogram()
		if label := f.GetMetric()[0].GetLabel()[0]; label.GetName() != "event_type" || label.GetValue() != "WalletBalanceChanged" ||
			h.GetSampleCount() != 2 || h.GetSampleSum() != 2.25 {
			t.Fatalf("outbox_publish_lag_seconds = %v %v", f.GetMetric()[0].GetLabel(), h)
		}
		return
	}
	t.Fatal("outbox_publish_lag_seconds not registered")
}
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 -run '^TestMetrics_Outbox$' ./internal/observability/`
  - Esperado: FAIL no `GatherAndCompare`, com o diff das 5 métricas ausentes (`+# HELP outbox_…`).

- [ ] **Passo 4: implementar.** Trocar, em `metrics.go`, do comentário do `Metrics` até o fim do arquivo por:

```go
// Metrics implements app.Metrics and the outbox publisher's port with
// Prometheus collectors. M7 adds the rest of the catalog (ARCHITECTURE.md §13.2).
type Metrics struct {
	reconciliationDivergences prometheus.Counter

	outboxPublished      *prometheus.CounterVec
	outboxPublishFailed  *prometheus.CounterVec
	outboxLeaseReclaims  prometheus.Counter
	outboxPending        prometheus.Gauge
	outboxOldestPending  prometheus.Gauge
	outboxPublishLatency *prometheus.HistogramVec
}

// NewMetrics registers the collectors on reg.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	m := &Metrics{
		reconciliationDivergences: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "reconciliation_divergences_total",
			Help: "Reconciliations whose stored balance differs from the balance rebuilt from the ledger.",
		}),
		outboxPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_published_total",
			Help: "Outbox events published to the events topic.",
		}, []string{"event_type"}),
		outboxPublishFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "outbox_publish_failures_total",
			Help: "Failed publication attempts of outbox events.",
		}, []string{"event_type"}),
		outboxLeaseReclaims: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "outbox_lease_reclaims_total",
			Help: "Outbox events whose expired lease was taken over by a publisher.",
		}),
		outboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_pending_events",
			Help: "Outbox events not yet published.",
		}),
		outboxOldestPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "outbox_oldest_pending_age_seconds",
			Help: "Age of the oldest outbox event not yet published.",
		}),
		outboxPublishLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "outbox_publish_lag_seconds",
			Help:    "Time from the occurrence of an event to its confirmed publication.",
			Buckets: prometheus.ExponentialBucketsRange(0.005, 60, 12),
		}, []string{"event_type"}),
	}
	reg.MustRegister(m.reconciliationDivergences, m.outboxPublished, m.outboxPublishFailed,
		m.outboxLeaseReclaims, m.outboxPending, m.outboxOldestPending, m.outboxPublishLatency)
	return m
}

// ReconciliationDivergence counts a reconciliation that found a divergence.
func (m *Metrics) ReconciliationDivergence() { m.reconciliationDivergences.Inc() }

// Published counts a confirmed publication and its lag (published − occurred).
func (m *Metrics) Published(eventType string, lag time.Duration) {
	m.outboxPublished.WithLabelValues(eventType).Inc()
	m.outboxPublishLatency.WithLabelValues(eventType).Observe(lag.Seconds())
}

// PublishFailed counts a failed publication attempt.
func (m *Metrics) PublishFailed(eventType string) {
	m.outboxPublishFailed.WithLabelValues(eventType).Inc()
}

// LeaseReclaimed counts an event whose expired lease was taken over.
func (m *Metrics) LeaseReclaimed() { m.outboxLeaseReclaims.Inc() }

// Backlog sets the outbox lag gauges.
func (m *Metrics) Backlog(pending int, oldestAge time.Duration) {
	m.outboxPending.Set(float64(pending))
	m.outboxOldestPending.Set(oldestAge.Seconds())
}
```

- [ ] **Passo 5: ver passar.** `go test -race -count=1 ./internal/observability/`: PASS, e o `TestMetrics_ReconciliationDivergences` continua verde.

**Checkpoint:** `go test -race ./internal/observability/` verde.

---

### Tarefa 7: backoff e sink do SNS

Funções puras do backoff e da truncagem (spec decisões 6 e 15), e o mapeamento do messaging §5.2. São testes unitários: o dublê é só da API do SNS, para capturar a entrada.

**Arquivos:**
- Implementação: `internal/adapters/outbox/backoff.go`, `internal/adapters/outbox/sns_sink.go` (criar)
- Testes: `internal/adapters/outbox/backoff_test.go`, `internal/adapters/outbox/sns_sink_test.go` (criar)

**Interfaces:**
- Consome: `app.PendingEvent` (Tarefa 4), `awsclient.Topic` (Tarefa 5).
- Produz:
  - `retryDelay(attempts int, base, ceiling time.Duration) time.Duration` e `truncateError(string) string` (não exportados);
  - `outbox.SNSPublishAPI`;
  - `outbox.NewSNSSink(SNSPublishAPI, *awsclient.Topic) *outbox.SNSSink` e `(*SNSSink).Publish(ctx, app.PendingEvent) error`.

- [ ] **Passo 1: stubs.** `internal/adapters/outbox/backoff.go`:

```go
// Package outbox publishes the transactional outbox to the SNS FIFO topic
// (D-13): claim with a lease, publish outside any transaction, confirm only
// where the lease is still ours.
package outbox

import "time"

// retryDelay is the wait before the next attempt of a failed event.
func retryDelay(attempts int, base, ceiling time.Duration) time.Duration { return 0 }

// truncateError bounds last_error.
func truncateError(s string) string { return s }
```

- [ ] **Passo 2: escrever o teste** `internal/adapters/outbox/backoff_test.go`:

```go
package outbox

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Covers: OUT-04 (spec M4, decision 6)
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		attempts      int
		base, ceiling time.Duration
		want          time.Duration
	}{
		{0, time.Second, 5 * time.Minute, time.Second}, // the first failure waits base
		{1, time.Second, 5 * time.Minute, 2 * time.Second},
		{3, time.Second, 5 * time.Minute, 8 * time.Second},
		{8, time.Second, 5 * time.Minute, 256 * time.Second},
		{9, time.Second, 5 * time.Minute, 5 * time.Minute},         // 512 s capped
		{1_000_000, time.Second, 5 * time.Minute, 5 * time.Minute}, // no overflow
		{62, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour},
		{2, 100 * time.Millisecond, time.Second, 400 * time.Millisecond},
		{-1, time.Second, time.Minute, time.Second}, // never below base
	}
	for _, tc := range cases {
		if got := retryDelay(tc.attempts, tc.base, tc.ceiling); got != tc.want {
			t.Errorf("retryDelay(%d, %v, %v) = %v, want %v", tc.attempts, tc.base, tc.ceiling, got, tc.want)
		}
	}
}

// Covers: OUT-04 (spec M4, decision 15; data-model §3.5)
func TestTruncateError(t *testing.T) {
	if got := truncateError("sns: throttled"); got != "sns: throttled" {
		t.Fatalf("short message = %q, want it untouched", got)
	}
	long := strings.Repeat("a", 1023) + "é" + strings.Repeat("b", 10) // é straddles byte 1024
	got := truncateError(long)
	if len(got) > 1024 || !utf8.ValidString(got) || got != strings.Repeat("a", 1023) {
		t.Fatalf("truncateError = %d bytes (valid UTF-8: %v), want the 1023 bytes before the cut rune", len(got), utf8.ValidString(got))
	}
	if got := truncateError(strings.Repeat("x", 5000)); len(got) != 1024 {
		t.Fatalf("truncateError = %d bytes, want 1024", len(got))
	}
}
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 ./internal/adapters/outbox/`
  - Esperado: FAIL, com as 9 linhas `retryDelay(…) = 0s, want …` e `truncateError = 1035 bytes…`.

- [ ] **Passo 4: implementar** `backoff.go`:

```go
// Package outbox publishes the transactional outbox to the SNS FIFO topic
// (D-13): claim with a lease, publish outside any transaction, confirm only
// where the lease is still ours.
package outbox

import (
	"time"
	"unicode/utf8"
)

// maxErrorBytes bounds last_error (data-model §3.5).
const maxErrorBytes = 1024

// retryDelay is min(base × 2^attempts, ceiling) (spec M4, decision 6):
// attempts is the count stored before this failure, so the first failure
// waits base. The doubling stops at the ceiling, so it never overflows.
func retryDelay(attempts int, base, ceiling time.Duration) time.Duration {
	d := base
	for i := 0; i < attempts && d < ceiling; i++ {
		d *= 2
	}
	return min(d, ceiling)
}

// truncateError bounds last_error to maxErrorBytes without splitting a
// UTF-8 sequence (spec M4, decision 15).
func truncateError(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	cut := maxErrorBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
```

- [ ] **Passo 5: ver passar.** `go test -race -count=1 ./internal/adapters/outbox/`: PASS.

- [ ] **Passo 6: stub do sink** `internal/adapters/outbox/sns_sink.go`:

```go
package outbox

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sns"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)

// SNSPublishAPI is the subset of *sns.Client the sink uses.
type SNSPublishAPI interface {
	Publish(ctx context.Context, in *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

// SNSSink publishes outbox events to the SNS FIFO events topic.
type SNSSink struct {
	api   SNSPublishAPI
	topic *awsclient.Topic
}

// NewSNSSink publishes to topic, whose ARN is resolved on start.
func NewSNSSink(api SNSPublishAPI, topic *awsclient.Topic) *SNSSink {
	return &SNSSink{api: api, topic: topic}
}

// Publish sends one event.
func (s *SNSSink) Publish(ctx context.Context, e app.PendingEvent) error { return nil }
```

- [ ] **Passo 7: escrever o teste** `internal/adapters/outbox/sns_sink_test.go`:

```go
package outbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/app"
)

// captureSNS records the inputs; err makes Publish fail.
type captureSNS struct {
	inputs []*sns.PublishInput
	err    error
}

func (c *captureSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	c.inputs = append(c.inputs, in)
	if c.err != nil {
		return nil, c.err
	}
	return &sns.PublishOutput{MessageId: aws.String("m-1")}, nil
}

var pending = app.PendingEvent{
	EventID: "0192f2a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b", MessageGroupID: "0192f291-27dd-7d3f-8071-5f8685deef37",
	EventType: "WalletBalanceChanged", EventVersion: 1, CorrelationID: "corr-1",
	Payload: []byte(`{"eventId": "0192f2a0-1c2d-7e3f-8a9b-0c1d2e3f4a5b"}`),
}

// Covers: OUT-05, OUT-07 (messaging.md §5.2)
func TestSNSSinkPublishInput(t *testing.T) {
	api := &captureSNS{}
	topic := &awsclient.Topic{ARN: "arn:aws:sns:us-east-1:000000000000:wallet-events.fifo"}
	if err := outbox.NewSNSSink(api, topic).Publish(t.Context(), pending); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(api.inputs) != 1 {
		t.Fatalf("Publish calls = %d, want 1", len(api.inputs))
	}
	in := api.inputs[0]
	attr := func(name string) types.MessageAttributeValue { return in.MessageAttributes[name] }
	switch {
	case aws.ToString(in.TopicArn) != topic.ARN:
		t.Fatalf("TopicArn = %s", aws.ToString(in.TopicArn))
	case aws.ToString(in.Message) != string(pending.Payload): // the column text, never re-serialized
		t.Fatalf("Message = %s", aws.ToString(in.Message))
	case aws.ToString(in.MessageGroupId) != pending.MessageGroupID:
		t.Fatalf("MessageGroupId = %s, want the wallet", aws.ToString(in.MessageGroupId))
	case aws.ToString(in.MessageDeduplicationId) != pending.EventID:
		t.Fatalf("MessageDeduplicationId = %s, want the event id", aws.ToString(in.MessageDeduplicationId))
	case len(in.MessageAttributes) != 3,
		aws.ToString(attr("eventType").DataType) != "String" || aws.ToString(attr("eventType").StringValue) != "WalletBalanceChanged",
		aws.ToString(attr("eventVersion").DataType) != "Number" || aws.ToString(attr("eventVersion").StringValue) != "1",
		aws.ToString(attr("correlationId").DataType) != "String" || aws.ToString(attr("correlationId").StringValue) != "corr-1":
		t.Fatalf("MessageAttributes = %+v", in.MessageAttributes)
	}
}

// Covers: OUT-04
func TestSNSSinkReportsTheEvent(t *testing.T) {
	cause := errors.New("ThrottledException")
	err := outbox.NewSNSSink(&captureSNS{err: cause}, &awsclient.Topic{ARN: "arn"}).Publish(t.Context(), pending)
	if !errors.Is(err, cause) || !strings.Contains(err.Error(), pending.EventID) {
		t.Fatalf("Publish error = %v, want it to wrap the cause and name the event", err)
	}
}
```

- [ ] **Passo 8: ver falhar.** `go test -race -count=1 -run '^TestSNSSink' ./internal/adapters/outbox/`
  - Esperado: FAIL com `Publish calls = 0, want 1` e `Publish error = <nil>, want it to wrap…`.

- [ ] **Passo 9: implementar.** Em `sns_sink.go`, os imports ficam:

```go
import (
	"context"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sns/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)
```

  o comentário do `SNSSink` vira `// SNSSink publishes outbox events to the SNS FIFO events topic with the mapping of messaging.md §5.2.`, e o `Publish` vira:

```go
// Publish sends the payload read from the column, never re-serialized, so a
// republication carries the same eventId and content (OUT-05). The group is
// the wallet and the deduplication id is the eventId (D-13).
func (s *SNSSink) Publish(ctx context.Context, e app.PendingEvent) error {
	_, err := s.api.Publish(ctx, &sns.PublishInput{
		TopicArn:               aws.String(s.topic.ARN),
		Message:                aws.String(string(e.Payload)),
		MessageGroupId:         aws.String(e.MessageGroupID),
		MessageDeduplicationId: aws.String(e.EventID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType":     {DataType: aws.String("String"), StringValue: aws.String(e.EventType)},
			"eventVersion":  {DataType: aws.String("Number"), StringValue: aws.String(strconv.Itoa(e.EventVersion))},
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(e.CorrelationID)},
		},
	})
	if err != nil {
		return fmt.Errorf("outbox: publish event %s: %w", e.EventID, err)
	}
	return nil
}
```

- [ ] **Passo 10: ver passar.** `go test -race -count=1 ./internal/adapters/outbox/`: PASS.

**Checkpoint:** `go test -race ./internal/adapters/outbox/` verde.

---

### Tarefa 8: `testkit.Eventually` e o publisher, caminho feliz (I05a, I05b)

A versão mínima do loop: claim → publicação por grupo → confirmação. Falha, reclaim, backlog e espera do claim entram na Tarefa 9, e o stop gracioso na Tarefa 10. Cada teste do pacote tem **banco e tópico próprios** (spec decisão 21).

**Arquivos:**
- Implementação: `test/testkit/assert.go` (alterar), `internal/adapters/outbox/publisher.go` (criar)
- Testes: `test/testkit/assert_test.go`, `internal/adapters/outbox/helpers_integration_test.go`, `internal/adapters/outbox/publisher_integration_test.go` (criar)

**Interfaces:**
- Consome: `app.OutboxStore`, `postgres.NewOutboxStore`, `postgres.NewUnitOfWork`, `testkit.NewTestEnv` (Tarefa 4); `NewEventsTopic`, `NewAudit` (Tarefa 2); `SNSSink` (Tarefa 7); `observability.NewMetrics` (Tarefa 6).
- Produz:
  - `testkit.Eventually(tb, timeout time.Duration, what string, cond func(ctx context.Context) (bool, error))`;
  - `outbox.Sink`, `outbox.Metrics` e `outbox.Options{Owner string; BatchSize int; Lease, PollInterval time.Duration; Concurrency int; RetryBaseDelay, RetryMaxDelay time.Duration}`;
  - `outbox.NewPublisher(app.OutboxStore, Sink, Metrics, *slog.Logger, Options) *outbox.Publisher` e `(*Publisher).Run(ctx)`.

- [ ] **Passo 1: stub do `Eventually`.** Ao fim de `test/testkit/assert.go`:

```go

// Eventually polls cond until it holds, failing tb with what after timeout.
func Eventually(tb testing.TB, timeout time.Duration, what string, cond func(ctx context.Context) (bool, error)) {
	tb.Helper()
}
```

- [ ] **Passo 2: escrever o teste** `test/testkit/assert_test.go`:

```go
package testkit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// recordingTB keeps the failure instead of stopping the test.
type recordingTB struct {
	testing.TB
	failure string
}

func (r *recordingTB) Fatalf(format string, args ...any) { r.failure = fmt.Sprintf(format, args...) }

// Covers: test-plan §1 (Eventually with a deadline)
func TestEventually(t *testing.T) {
	calls := 0
	testkit.Eventually(t, time.Second, "third call", func(context.Context) (bool, error) {
		calls++
		return calls == 3, nil
	})
	if calls != 3 {
		t.Fatalf("cond called %d times, want 3 (polls until it holds)", calls)
	}

	rec := &recordingTB{TB: t}
	start := time.Now()
	testkit.Eventually(rec, 200*time.Millisecond, "never", func(context.Context) (bool, error) { return false, nil })
	if !strings.Contains(rec.failure, "never") || time.Since(start) < 200*time.Millisecond {
		t.Fatalf("after %v: failure %q, want a failure naming the condition after the timeout", time.Since(start), rec.failure)
	}

	rec = &recordingTB{TB: t}
	testkit.Eventually(rec, time.Second, "broken", func(context.Context) (bool, error) { return false, errors.New("boom") })
	if !strings.Contains(rec.failure, "boom") {
		t.Fatalf("failure %q, want the error of the condition", rec.failure)
	}
}
```

- [ ] **Passo 3: ver falhar.** `go test -race -count=1 -run '^TestEventually$' ./test/testkit/`
  - Esperado: FAIL com `cond called 0 times, want 3 (polls until it holds)`.

- [ ] **Passo 4: implementar.** Trocar o stub por:

```go

// pollInterval is how often Eventually checks its condition.
const pollInterval = 50 * time.Millisecond

// Eventually polls cond until it holds, failing tb with what after timeout or
// on an error of cond (test-plan §1: waits have a deadline, never a sleep). It
// detaches from the test's context, so it also works in t.Cleanup.
func Eventually(tb testing.TB, timeout time.Duration, what string, cond func(ctx context.Context) (bool, error)) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), timeout)
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		ok, err := cond(ctx)
		switch {
		case ctx.Err() != nil:
			tb.Fatalf("%s: not reached within %v", what, timeout)
			return
		case err != nil:
			tb.Fatalf("%s: %v", what, err)
			return
		case ok:
			return
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}
```

  Rodar de novo: PASS.

- [ ] **Passo 5: stub do publisher** `internal/adapters/outbox/publisher.go`:

```go
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// Sink publishes one event to the broker.
type Sink interface {
	Publish(ctx context.Context, e app.PendingEvent) error
}

// Metrics is what the publisher reports (messaging.md §8).
type Metrics interface {
	Published(eventType string, lag time.Duration)
	PublishFailed(eventType string)
	LeaseReclaimed()
	Backlog(pending int, oldestAge time.Duration)
}

// Options are the publisher settings (messaging.md §5.1).
type Options struct {
	Owner          string // locked_by of this instance
	BatchSize      int
	Lease          time.Duration
	PollInterval   time.Duration
	Concurrency    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// Publisher is the outbox publisher of one instance (D-13).
type Publisher struct{}

// NewPublisher builds a publisher; Run starts it.
func NewPublisher(store app.OutboxStore, sink Sink, m Metrics, log *slog.Logger, opts Options) *Publisher {
	return &Publisher{}
}

// Run publishes until ctx is canceled.
func (p *Publisher) Run(ctx context.Context) { <-ctx.Done() }
```

- [ ] **Passo 6: os helpers dos testes** `internal/adapters/outbox/helpers_integration_test.go`:

```go
//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

// fixture is a database and an events topic of the test's own: the claim sees
// the whole table, so publishers of different tests never share events.
type fixture struct {
	env   *testkit.Env
	audit *testkit.Audit
	sink  *outbox.SNSSink
	uow   *postgres.UnitOfWork
}

func newFixture(t *testing.T, name string) fixture {
	t.Helper()
	env := testkit.NewTestEnv(t, name)
	root := testkit.RootAWSConfig(t)
	q, n := sqs.NewFromConfig(root), sns.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, q, n)
	audit, err := testkit.NewAudit(t.Context(), q, topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		env: env, audit: audit, sink: outbox.NewSNSSink(n, &awsclient.Topic{ARN: topic.ARN}),
		uow: postgres.NewUnitOfWork(env.App, env.Config()),
	}
}

func newID() string { return uuid.Must(uuid.NewV7()).String() }

// balanceChanged seals a WalletBalanceChanged of wallet, as the app does.
func balanceChanged(t *testing.T, wallet string, version int64) events.Envelope {
	t.Helper()
	m := func(amount string) money.Money {
		v, err := money.Parse(amount, "BRL")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	ev, err := events.NewWalletBalanceChanged(events.WalletBalanceChanged{
		WalletID: wallet, TransactionID: newID(), TransactionKind: "BET", Direction: "DEBIT",
		Money: m("1.00"), BalanceBefore: m("100.00"), BalanceAfter: m("99.00"), WalletVersion: version,
		ChangedAt: events.NewTime(time.Now()),
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := events.Seal(newID(), "corr-"+wallet[:8], "", ev)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// insert records the envelopes in one unit of work and returns their ids.
func (f fixture) insert(t *testing.T, envs ...events.Envelope) []string {
	t.Helper()
	ctx := t.Context()
	if err := f.uow.Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, envs...) }); err != nil {
		t.Fatalf("insert: %v", err)
	}
	ids := make([]string, len(envs))
	for i, e := range envs {
		ids[i] = e.EventID()
	}
	return ids
}

// options are the accelerated settings of test-plan §3.3.
func (f fixture) options(owner string) outbox.Options {
	c := f.env.Config()
	return outbox.Options{
		Owner: owner, BatchSize: c.OutboxBatchSize, Lease: c.OutboxLease,
		PollInterval: c.OutboxPollInterval, Concurrency: c.OutboxConcurrency,
		RetryBaseDelay: c.OutboxRetryBaseDelay, RetryMaxDelay: c.OutboxRetryMaxDelay,
	}
}

// metrics is a registry of the publisher's own.
func metrics() (*observability.Metrics, *prometheus.Registry) {
	reg := observability.NewRegistry()
	return observability.NewMetrics(reg), reg
}

// start runs p until the returned stop is called, or the test ends.
func start(t *testing.T, p *outbox.Publisher) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx)
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("publisher did not stop within 10s")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// newPool is another pool as the app role: another publisher instance.
func (f fixture) newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), f.env.DB.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// pendingCount counts the unpublished events.
func (f fixture) pendingCount(ctx context.Context) (int, error) {
	var n int
	err := f.env.Owner.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`).Scan(&n)
	return n, err
}

// storedPayload is the payload column of an event.
func (f fixture) storedPayload(t *testing.T, id string) []byte {
	t.Helper()
	var payload []byte
	if err := f.env.Owner.QueryRow(t.Context(), `SELECT payload FROM outbox_events WHERE event_id = $1`, id).Scan(&payload); err != nil {
		t.Fatalf("payload of %s: %v", id, err)
	}
	return payload
}

// jsonEqual compares two JSON documents by content.
func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	decode := func(raw []byte) any {
		var v any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		return v
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// countingSink counts the successful publications of its publisher.
type countingSink struct {
	outbox.Sink
	n atomic.Int64
}

func (c *countingSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if err := c.Sink.Publish(ctx, e); err != nil {
		return err
	}
	c.n.Add(1)
	return nil
}

var discard = slog.New(slog.DiscardHandler)
```

- [ ] **Passo 7: escrever os testes** `internal/adapters/outbox/publisher_integration_test.go`:

```go
//go:build integration

package outbox_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-I05, OUT-03, OUT-05 (I05a)
//
// Two publishers, each with its own pool as two instances would have, share
// one outbox: every event reaches the topic with the content of the database,
// and none is left behind.
func TestOutboxConcurrentPublishers(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05a")
	var ids []string
	for i := range 200 {
		ids = append(ids, f.insert(t, balanceChanged(t, fmt.Sprintf("0192f291-27dd-7d3f-8071-%012d", i%20), int64(i/20+1)))...)
	}
	sinks := []*countingSink{{Sink: f.sink}, {Sink: f.sink}}
	for i, s := range sinks {
		m, _ := metrics()
		start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.newPool(t)), s, m, discard, f.options(fmt.Sprintf("publisher-%d", i))))
	}

	got := f.audit.WaitFor(t, ids...)
	for _, id := range ids {
		if !jsonEqual(t, got[id][0].Body, f.storedPayload(t, id)) {
			t.Fatalf("event %s: delivered %s, stored %s", id, got[id][0].Body, f.storedPayload(t, id))
		}
	}
	testkit.Eventually(t, 5*time.Second, "outbox drained", func(ctx context.Context) (bool, error) {
		n, err := f.pendingCount(ctx)
		return n == 0, err
	})
	if sinks[0].n.Load() == 0 || sinks[1].n.Load() == 0 {
		t.Fatalf("publications = %d and %d, want both publishers to share the outbox", sinks[0].n.Load(), sinks[1].n.Load())
	}
}

// Covers: OUT-10, E8 (I05b)
// Sensitivity: an Outbox().Insert decorator that also published the envelope
// made Absent fail ("delivered 1 time(s), want none").
//
// The publisher only sees committed rows: while the transaction that wrote the
// event is open, nothing is published; after the commit, it is.
func TestNoPublishBeforeCommit(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05b")
	m, _ := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), f.sink, m, discard, f.options("publisher")))

	env := balanceChanged(t, newID(), 2)
	inserted, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		ctx := context.WithoutCancel(t.Context())
		done <- f.uow.Do(ctx, func(r app.Repos) error {
			if err := r.Outbox().Insert(ctx, env); err != nil {
				return err
			}
			close(inserted)
			<-release // the commit waits for the test
			return nil
		})
	}()
	<-inserted
	f.audit.Absent(t, 2*time.Second, env.EventID())
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("commit: %v", err)
	}
	f.audit.WaitFor(t, env.EventID())
}
```

- [ ] **Passo 8: ver falhar.** `go test -tags=integration -race -count=1 -run 'TestOutboxConcurrentPublishers|TestNoPublishBeforeCommit' ./internal/adapters/outbox/`
  - Esperado: FAIL nos dois depois de ~10 s, com `audit: 200 of 200 events not delivered (first: […]): … context deadline exceeded` e `audit: 1 of 1 events not delivered…`.

- [ ] **Passo 9: implementar** `publisher.go` (versão mínima; as Tarefas 9 e 10 a completam):

```go
package outbox

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// Sink publishes one event to the broker.
type Sink interface {
	Publish(ctx context.Context, e app.PendingEvent) error
}

// Metrics is what the publisher reports (messaging.md §8).
type Metrics interface {
	Published(eventType string, lag time.Duration)
	PublishFailed(eventType string)
	LeaseReclaimed()
	Backlog(pending int, oldestAge time.Duration)
}

// Options are the publisher settings (messaging.md §5.1).
type Options struct {
	Owner          string // locked_by of this instance
	BatchSize      int
	Lease          time.Duration
	PollInterval   time.Duration
	Concurrency    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// storeTimeout bounds the confirmation.
const storeTimeout = 5 * time.Second

// Publisher is the outbox publisher of one instance (D-13). Any number of
// instances run one each over the same outbox.
type Publisher struct {
	store   app.OutboxStore
	sink    Sink
	metrics Metrics
	log     *slog.Logger
	opts    Options
}

// NewPublisher builds a publisher; Run starts it.
func NewPublisher(store app.OutboxStore, sink Sink, m Metrics, log *slog.Logger, opts Options) *Publisher {
	return &Publisher{store: store, sink: sink, metrics: m, log: log, opts: opts}
}

// Run publishes until ctx is canceled: claim a batch, publish it, and wait
// for the poll interval unless the batch was full.
func (p *Publisher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		batch, err := p.store.Claim(ctx, p.opts.Owner, p.opts.Lease, p.opts.BatchSize)
		if err != nil {
			p.log.Warn("outbox claim failed", "error", err.Error())
			return
		}
		p.publishBatch(ctx, batch)
		if len(batch) < p.opts.BatchSize {
			sleep(ctx, p.opts.PollInterval)
		}
	}
}

// publishBatch publishes up to Concurrency groups in parallel, each group in
// order of occurrence (spec M4, decision 9).
func (p *Publisher) publishBatch(ctx context.Context, batch []app.PendingEvent) {
	sem := make(chan struct{}, p.opts.Concurrency)
	var wg sync.WaitGroup
	for _, group := range groups(batch) {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Go(func() {
			defer func() { <-sem }()
			for i := range group {
				p.publish(ctx, group[i])
			}
		})
	}
	wg.Wait()
}

// publish sends one event and records the outcome.
func (p *Publisher) publish(ctx context.Context, e app.PendingEvent) {
	pubCtx, cancel := context.WithTimeout(ctx, p.opts.Lease/2)
	err := p.sink.Publish(pubCtx, e)
	cancel()
	storeCtx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	if err != nil {
		p.log.Warn("outbox publish failed", "eventId", e.EventID, "eventType", e.EventType, "error", err.Error())
		return
	}
	at, ok, err := p.store.MarkPublished(storeCtx, e.EventID, p.opts.Owner)
	switch {
	case err != nil: // the lease expires and the event is republished with the same eventId (OUT-06b)
		p.log.Warn("outbox confirmation failed", "eventId", e.EventID, "error", err.Error())
	case !ok:
		p.log.Info("outbox event confirmed by another instance", "eventId", e.EventID)
	default:
		p.metrics.Published(e.EventType, at.Sub(e.OccurredAt))
	}
}

// groups splits a batch by MessageGroupId, each group sorted by occurrence.
func groups(batch []app.PendingEvent) [][]app.PendingEvent {
	byGroup := map[string][]app.PendingEvent{}
	var order []string
	for i := range batch {
		g := batch[i].MessageGroupID
		if _, seen := byGroup[g]; !seen {
			order = append(order, g)
		}
		byGroup[g] = append(byGroup[g], batch[i])
	}
	out := make([][]app.PendingEvent, 0, len(order))
	for _, g := range order {
		events := byGroup[g]
		slices.SortFunc(events, func(a, b app.PendingEvent) int {
			return cmp.Or(a.OccurredAt.Compare(b.OccurredAt), cmp.Compare(a.EventID, b.EventID))
		})
		out = append(out, events)
	}
	return out
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

- [ ] **Passo 10: ver passar.** O mesmo comando do passo 8: PASS (I05a em ~1–2 s, I05b em ~2,5 s).
- [ ] **Passo 11: sensibilidade do I05b** (E8). Em `TestNoPublishBeforeCommit`, logo depois do `r.Outbox().Insert(...)`, acrescentar temporariamente a publicação antes do commit:

```go
			body, _ := env.MarshalJSON() // SABOTAGE: publish before the commit
			_ = f.sink.Publish(ctx, app.PendingEvent{EventID: env.EventID(), MessageGroupID: env.MessageGroupID(),
				EventType: string(env.Type()), EventVersion: 1, CorrelationID: env.CorrelationID(), Payload: body})
```

  - Esperado: FAIL com `event … delivered 1 time(s), want none`. Desfazer.

**Checkpoint:** `go test -race ./test/testkit/ ./internal/adapters/outbox/` e `go test -tags=integration -race -count=1 ./internal/adapters/outbox/` verdes.

---

### Tarefa 9: publisher, falhas, reclaim e backlog (I05c, I05d, I05f)

Falha com backoff sem descartar nada (decisões 6 e 7), trabalho abandonado contado (decisão 5 e achado 24), claim com o banco fora (decisão 11) e gauges de backlog (decisão 13).

**Arquivos:**
- Implementação: `internal/adapters/outbox/publisher.go` (alterar)
- Testes: `internal/adapters/outbox/failures_integration_test.go` (criar)

**Interfaces:**
- Consome: `retryDelay`, `truncateError` (Tarefa 7); `OutboxStore.MarkFailed`/`Backlog` (Tarefa 4); `Metrics.PublishFailed`/`LeaseReclaimed`/`Backlog` (Tarefa 6).
- Produz: nada novo exportado.

- [ ] **Passo 1: escrever os testes** `internal/adapters/outbox/failures_integration_test.go`:

```go
//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// flakySink fails the first failures calls and records when each call began.
type flakySink struct {
	outbox.Sink
	failures int

	mu    sync.Mutex
	calls []time.Time
}

func (s *flakySink) Publish(ctx context.Context, e app.PendingEvent) error {
	s.mu.Lock()
	s.calls = append(s.calls, time.Now())
	n := len(s.calls)
	s.mu.Unlock()
	if n <= s.failures {
		return fmt.Errorf("sns: injected failure %d", n)
	}
	return s.Sink.Publish(ctx, e)
}

func (s *flakySink) callTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

// metricIs reports whether the registry exposes exactly want for the metric.
func metricIs(reg *prometheus.Registry, want, name string) bool {
	return testutil.GatherAndCompare(reg, strings.NewReader(want), name) == nil
}

// Covers: OUT-04 (I05c)
//
// A publication that fails is retried with backoff until it succeeds: every
// failure counts an attempt and reschedules the event base × 2^attempts later.
func TestOutboxRetryBackoff(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05c")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	sink := &flakySink{Sink: f.sink, failures: 3}
	m, reg := metrics()
	opts := f.options("publisher")
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, opts))

	f.audit.WaitFor(t, id)
	calls := sink.callTimes()
	if len(calls) != 4 {
		t.Fatalf("Publish calls = %d, want 3 failures and 1 success", len(calls))
	}
	for i := 1; i < len(calls); i++ {
		want := opts.RetryBaseDelay << (i - 1) // 100 ms, 200 ms, 400 ms
		if gap := calls[i].Sub(calls[i-1]); gap < want {
			t.Errorf("attempt %d came %v after the previous one, want at least %v", i+1, gap, want)
		}
	}
	testkit.Eventually(t, 5*time.Second, "event confirmed", func(ctx context.Context) (bool, error) {
		var attempts int
		var published bool
		var lastError string
		err := f.env.Owner.QueryRow(ctx, `SELECT attempts, published_at IS NOT NULL, COALESCE(last_error, '')
			FROM outbox_events WHERE event_id = $1`, id).Scan(&attempts, &published, &lastError)
		if published && (attempts != 3 || lastError != "sns: injected failure 3") {
			return false, fmt.Errorf("attempts %d, last_error %q; want 3 and the last failure", attempts, lastError)
		}
		return published, err
	})
	if want := "# HELP outbox_publish_failures_total Failed publication attempts of outbox events.\n" +
		"# TYPE outbox_publish_failures_total counter\n" +
		"outbox_publish_failures_total{event_type=\"WalletBalanceChanged\"} 3\n"; !metricIs(reg, want, "outbox_publish_failures_total") {
		t.Fatal("outbox_publish_failures_total is not 3")
	}
}

// Covers: OUT-03, OUT-06 (I05d)
//
// Abandoned work is taken over: an event whose lease expired is reclaimed and
// published; an event with a live lease is left alone until the lease expires.
func TestOutboxLeaseRecovery(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_i05d")
	ids := f.insert(t, balanceChanged(t, newID(), 2), balanceChanged(t, newID(), 2))
	abandoned, busy := ids[0], ids[1]
	for id, lease := range map[string]string{abandoned: "-1 second", busy: "2 seconds"} {
		if _, err := f.env.Owner.Exec(t.Context(), `UPDATE outbox_events
			SET locked_by = 'dead-instance', locked_until = now() + $2::interval WHERE event_id = $1`, id, lease); err != nil {
			t.Fatal(err)
		}
	}
	m, reg := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), f.sink, m, discard, f.options("publisher")))

	f.audit.WaitFor(t, abandoned)
	f.audit.Absent(t, time.Second, busy)
	f.audit.WaitFor(t, busy)
	testkit.Eventually(t, 5*time.Second, "both reclaims counted", func(context.Context) (bool, error) {
		return metricIs(reg, "# HELP outbox_lease_reclaims_total Outbox events whose expired lease was taken over by a publisher.\n"+
			"# TYPE outbox_lease_reclaims_total counter\noutbox_lease_reclaims_total 2\n", "outbox_lease_reclaims_total"), nil
	})
}

// flakyStore fails the first claims, as an unavailable database would, and
// records when each claim began.
type flakyStore struct {
	app.OutboxStore
	failures int

	mu     sync.Mutex
	claims []time.Time
}

func (s *flakyStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.PendingEvent, error) {
	s.mu.Lock()
	s.claims = append(s.claims, time.Now())
	n := len(s.claims)
	s.mu.Unlock()
	if n <= s.failures {
		return nil, errors.New("postgres: 08006")
	}
	return s.OutboxStore.Claim(ctx, owner, lease, limit)
}

func (s *flakyStore) claimTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.claims...)
}

// Covers: OUT-04 (messaging.md §5.3, spec M4 decision 11)
//
// A failed claim never stops the publisher: it waits 1 s, then 2 s, and so on
// up to 30 s, and claims again.
func TestPublisherSurvivesClaimFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_claimfail")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	store := &flakyStore{OutboxStore: postgres.NewOutboxStore(f.env.App), failures: 2}
	m, _ := metrics()
	start(t, outbox.NewPublisher(store, f.sink, m, discard, f.options("publisher")))

	f.audit.WaitFor(t, id)
	claims := store.claimTimes()
	if len(claims) < 3 {
		t.Fatalf("claims = %d, want the 2 failures and a success", len(claims))
	}
	for i, want := range []time.Duration{time.Second, 2 * time.Second} {
		if gap := claims[i+1].Sub(claims[i]); gap < want {
			t.Errorf("claim %d came %v after the failed one, want at least %v", i+2, gap, want)
		}
	}
}

// switchSink fails while down is set.
type switchSink struct {
	outbox.Sink
	down atomic.Bool
}

func (s *switchSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if s.down.Load() {
		return errors.New("sns: unavailable")
	}
	return s.Sink.Publish(ctx, e)
}

// Covers: OUT-04, OBS-03 (spec M4, decision 13)
//
// While the broker is down, the backlog gauges show the pending event; once
// it is back and the event is published, they return to zero.
func TestOutboxBacklogGauges(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "outbox_backlog")
	id := f.insert(t, balanceChanged(t, newID(), 2))[0]
	sink := &switchSink{Sink: f.sink}
	sink.down.Store(true)
	m, reg := metrics()
	start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, f.options("publisher")))

	pending := func(n int) func(context.Context) (bool, error) {
		return func(context.Context) (bool, error) {
			return metricIs(reg, fmt.Sprintf("# HELP outbox_pending_events Outbox events not yet published.\n"+
				"# TYPE outbox_pending_events gauge\noutbox_pending_events %d\n", n), "outbox_pending_events"), nil
		}
	}
	testkit.Eventually(t, 5*time.Second, "backlog shows the pending event", pending(1))
	sink.down.Store(false)
	f.audit.WaitFor(t, id)
	testkit.Eventually(t, 5*time.Second, "backlog back to zero", pending(0))
}
```

- [ ] **Passo 2: ver falhar.** `go test -tags=integration -race -count=1 -run 'TestOutboxRetryBackoff|TestOutboxLeaseRecovery|TestPublisherSurvivesClaimFailures|TestOutboxBacklogGauges' ./internal/adapters/outbox/`
  - Esperado: FAIL nos 4:
    - I05c: `event confirmed: attempts 0, last_error ""; want 3 and the last failure`. Sem `MarkFailed`, o evento só volta quando o lease vence;
    - I05d: `both reclaims counted: not reached within 5s`;
    - claim: `audit: 1 of 1 events not delivered…`, porque o loop terminou no primeiro erro;
    - gauges: `backlog shows the pending event: not reached within 5s`.

- [ ] **Passo 3: implementar.** Em `publisher.go`:
  - trocar a constante `storeTimeout` (e o seu comentário) por:

```go
const (
	// claimRetryMin and claimRetryMax bound the wait after a failed claim,
	// the database being unavailable (messaging.md §5.3).
	claimRetryMin = time.Second
	claimRetryMax = 30 * time.Second
	// backlogEvery bounds how often the lag gauges are refreshed.
	backlogEvery = time.Second
	// storeTimeout bounds the confirmation and the failure record.
	storeTimeout = 5 * time.Second
)
```

  - no `Publisher`, depois de `opts    Options`, com uma linha em branco antes:

```go

	lastBacklog time.Time // only read and written by Run
```

  - o corpo do `Run` vira:

```go
	var claimDelay time.Duration
	for ctx.Err() == nil {
		p.refreshBacklog(ctx)
		batch, err := p.store.Claim(ctx, p.opts.Owner, p.opts.Lease, p.opts.BatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			claimDelay = min(max(2*claimDelay, claimRetryMin), claimRetryMax)
			p.log.Warn("outbox claim failed", "error", err.Error(), "retryIn", claimDelay.String())
			sleep(ctx, claimDelay)
			continue
		}
		claimDelay = 0
		p.publishBatch(ctx, batch)
		if len(batch) < p.opts.BatchSize {
			sleep(ctx, p.opts.PollInterval)
		}
	}
```

  - no início do `publishBatch`, antes de `sem := …`:

```go
	for i := range batch {
		if batch[i].Reclaimed {
			p.metrics.LeaseReclaimed()
			p.log.Info("outbox lease reclaimed", "eventId", batch[i].EventID)
		}
	}
```

  - no `publish`, o `if err != nil { p.log.Warn("outbox publish failed", …); return }` vira:

```go
	if err != nil {
		p.fail(storeCtx, e, err)
		return
	}
```

  - antes de `// groups splits a batch…`:

```go
// fail counts the attempt, schedules the next one and releases the lease
// (D-13). Nothing is discarded, whatever the error (spec M4, decision 7).
func (p *Publisher) fail(ctx context.Context, e app.PendingEvent, cause error) {
	p.metrics.PublishFailed(e.EventType)
	retryIn := retryDelay(e.Attempts, p.opts.RetryBaseDelay, p.opts.RetryMaxDelay)
	p.log.Warn("outbox publish failed", "eventId", e.EventID, "eventType", e.EventType,
		"attempts", e.Attempts+1, "retryIn", retryIn.String(), "error", cause.Error())
	ok, err := p.store.MarkFailed(ctx, e.EventID, p.opts.Owner, retryIn, truncateError(cause.Error()))
	switch {
	case err != nil: // the lease expires and the event is retried anyway
		p.log.Warn("outbox failure not recorded", "eventId", e.EventID, "error", err.Error())
	case !ok:
		p.log.Info("outbox event reclaimed before its failure was recorded", "eventId", e.EventID)
	}
}

// refreshBacklog updates the lag gauges at most once per backlogEvery (spec
// M4, decision 13). A failure keeps the previous values: the claim reports it.
func (p *Publisher) refreshBacklog(ctx context.Context) {
	if time.Since(p.lastBacklog) < backlogEvery {
		return
	}
	b, err := p.store.Backlog(ctx)
	if err != nil {
		return
	}
	p.lastBacklog = time.Now()
	p.metrics.Backlog(b.Pending, b.OldestAge)
}
```

- [ ] **Passo 4: ver passar.** O comando do passo 2 e o I05a/I05b: PASS. O claim com falhas leva ~3,5 s (esperas de 1 s e 2 s).

**Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/outbox/` verde.

---

### Tarefa 10: stop gracioso e composição Fx (I05g, I07b)

No stop, a publicação em andamento termina e é confirmada, e nenhuma outra começa (decisão 12). Depois vêm o módulo, a identidade (decisão 14) e o registro em `bootstrap.Options()`.

**Arquivos:**
- Implementação: `internal/adapters/outbox/publisher.go` (alterar), `internal/adapters/outbox/module.go` (criar), `internal/bootstrap/bootstrap.go` (alterar)
- Testes: `internal/adapters/outbox/stop_integration_test.go`, `internal/adapters/outbox/module_test.go` (criar); `internal/bootstrap/bootstrap_test.go` (alterar)

**Interfaces:**
- Consome: todo o pacote `outbox` (Tarefas 7–9); `*sns.Client` e `*awsclient.Topic` (Tarefa 5); `app.OutboxStore` (Tarefa 4); `*observability.Metrics` (Tarefa 6).
- Produz: `outbox.Module` e `instanceID() string` (não exportado).

- [ ] **Passo 1: escrever o teste do stop** `internal/adapters/outbox/stop_integration_test.go`:

```go
//go:build integration

package outbox_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/outbox"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
)

// blockingSink holds the first publication until release is closed.
type blockingSink struct {
	outbox.Sink
	entered, release chan struct{}
	calls            atomic.Int32
}

func (s *blockingSink) Publish(ctx context.Context, e app.PendingEvent) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
		<-s.release
	}
	return s.Sink.Publish(ctx, e)
}

// Covers: FX-03, OUT-06 (spec M4, decision 12)
//
// Not parallel: goleak sees every goroutine of the process.
//
// On stop, the publication in flight finishes and is confirmed; no other
// begins, and the rest of the batch keeps its lease for another instance.
func TestPublisherStop(t *testing.T) {
	f := newFixture(t, "outbox_stop")
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(),
		// idle keep-alive connections of the test's SNS client, not the publisher's
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"))
	wallet := newID()
	ids := f.insert(t, balanceChanged(t, wallet, 2), balanceChanged(t, wallet, 3)) // one group: in sequence
	sink := &blockingSink{Sink: f.sink, entered: make(chan struct{}), release: make(chan struct{})}
	m, _ := metrics()
	stop := start(t, outbox.NewPublisher(postgres.NewOutboxStore(f.env.App), sink, m, discard, f.options("publisher")))

	<-sink.entered
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stop()
	}()
	select {
	case <-stopped:
		t.Fatal("Run returned with a publication in flight")
	case <-time.After(300 * time.Millisecond):
	}
	close(sink.release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the publication in flight finished")
	}

	f.audit.WaitFor(t, ids[0])
	var published bool
	var lockedBy *string
	for i, want := range []bool{true, false} {
		if err := f.env.Owner.QueryRow(t.Context(), `SELECT published_at IS NOT NULL, locked_by
			FROM outbox_events WHERE event_id = $1`, ids[i]).Scan(&published, &lockedBy); err != nil {
			t.Fatal(err)
		}
		if published != want {
			t.Fatalf("event %d published = %v, want %v", i+1, published, want)
		}
	}
	if lockedBy == nil || *lockedBy != "publisher" || sink.calls.Load() != 1 {
		t.Fatalf("second event locked by %v after %d publications; want it still leased and never sent", lockedBy, sink.calls.Load())
	}
}
```

- [ ] **Passo 2: ver falhar.** `go test -tags=integration -race -count=1 -run '^TestPublisherStop$' ./internal/adapters/outbox/`
  - Esperado: FAIL com `audit: 1 of 1 events not delivered…`. O `Publish` em andamento herda o contexto cancelado do loop, falha no SNS e o evento não é confirmado.

- [ ] **Passo 3: implementar o stop** em `publisher.go`:
  - o comentário do `Run` ganha, ao fim: `After the cancellation no claim and no publication begins; the ones in flight finish and are confirmed before Run returns (spec M4, decision 12).`;
  - o comentário do `storeTimeout` vira `// storeTimeout bounds the confirmation and the failure record, which run detached from the cancellation of the loop.` (duas linhas);
  - no `publishBatch`, o laço do grupo vira:

```go
			for i := range group {
				if ctx.Err() != nil {
					return // the rest keeps its lease, which expires and is reclaimed
				}
				p.publish(ctx, group[i])
			}
```

  - no `publish`, o comentário e as duas criações de contexto viram:

```go
// publish sends one event and records the outcome. The publication and its
// record are detached from ctx's cancellation: once begun, they finish.
func (p *Publisher) publish(ctx context.Context, e app.PendingEvent) {
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.opts.Lease/2)
	err := p.sink.Publish(pubCtx, e)
	cancel()
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), storeTimeout)
```

- [ ] **Passo 4: ver passar.** O comando do passo 2: PASS. E `go test -tags=integration -race -count=1 ./internal/adapters/outbox/` inteiro: PASS.

- [ ] **Passo 5: stub da identidade** `internal/adapters/outbox/module.go`:

```go
package outbox

// instanceID is the locked_by of this process.
func instanceID() string { return "" }
```

- [ ] **Passo 6: escrever o teste** `internal/adapters/outbox/module_test.go`:

```go
package outbox

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Covers: OUT-03 (spec M4, decision 14; messaging.md §5.1)
func TestInstanceID(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	id := instanceID()
	prefix := host + "-" + strconv.Itoa(os.Getpid()) + "-"
	if !strings.HasPrefix(id, prefix) || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(strings.TrimPrefix(id, prefix)) {
		t.Fatalf("instanceID() = %q, want %s<8 hex>", id, prefix)
	}
	if instanceID() == id {
		t.Fatal("two identities in the same process are equal; want a random suffix")
	}
}
```

- [ ] **Passo 7: ver falhar.** `go test -race -count=1 -run '^TestInstanceID$' ./internal/adapters/outbox/`
  - Esperado: FAIL com `instanceID() = "", want <host>-<pid>-<8 hex>`.

- [ ] **Passo 8: implementar o módulo** `internal/adapters/outbox/module.go`:

```go
package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/google/uuid"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the outbox publisher of this instance (D-13, D-15): the loop
// starts with the application and, on stop, finishes the publications in
// flight before the pool and the AWS clients close.
var Module = fx.Module("outbox",
	fx.Provide(newModulePublisher),
	fx.Invoke(func(*Publisher) {}),
)

func newModulePublisher(lc fx.Lifecycle, cfg config.Config, store app.OutboxStore, api *sns.Client,
	topic *awsclient.Topic, m *observability.Metrics, log *slog.Logger,
) *Publisher {
	owner := instanceID()
	p := NewPublisher(store, NewSNSSink(api, topic), m, log, Options{
		Owner: owner, BatchSize: cfg.OutboxBatchSize, Lease: cfg.OutboxLease, PollInterval: cfg.OutboxPollInterval,
		Concurrency: cfg.OutboxConcurrency, RetryBaseDelay: cfg.OutboxRetryBaseDelay, RetryMaxDelay: cfg.OutboxRetryMaxDelay,
	})
	run, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				p.Run(run)
			}()
			log.Info("outbox publisher started", "owner", owner)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("outbox publisher stopping", "owner", owner)
			cancel()
			select {
			case <-done:
				log.Info("outbox publisher stopped", "owner", owner)
				return nil
			case <-ctx.Done():
				return fmt.Errorf("outbox publisher: stop: %w", ctx.Err())
			}
		},
	})
	return p
}

// instanceID is the locked_by of this process: <hostname>-<pid>-<8 hex>, so
// processes that share a host (tests, containers reusing a PID) never share a
// lease (spec M4, decision 14).
func instanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + "-" + strconv.Itoa(os.Getpid()) + "-" + uuid.NewString()[:8]
}
```

  O contexto do loop nasce no construtor, e não no `OnStart`, por causa do G118 do `gosec` (spec decisão 26).

- [ ] **Passo 9: ver passar.** `go test -race -count=1 ./internal/adapters/outbox/`: PASS.
- [ ] **Passo 10: registrar no Fx.** No `TestFxGraph`:
  - acrescentar o import `"github.com/KaioVinicios/pda/internal/adapters/outbox"`;
  - declarar `publisher *outbox.Publisher`;
  - acrescentar `&publisher` ao `fx.Populate`.

  Rodar `go test -race -count=1 -run '^TestFxGraph$' ./internal/bootstrap/`.
  - Esperado: FAIL com `missing type: *outbox.Publisher`.

  Em `internal/bootstrap/bootstrap.go`:
  - importar `"github.com/KaioVinicios/pda/internal/adapters/outbox"`;
  - acrescentar `outbox.Module,` entre `appModule,` e `httpapi.Module,`;
  - o comentário do `Options` vira:

```go
// Options returns the application modules in registration order (D-15):
// dependencies first, then the workers, HTTP last, so it starts last and stops
// first; the workers stop before the pool and the AWS clients close.
```

- [ ] **Passo 11: ver passar.** `go test -race -count=1 ./internal/bootstrap/` e `go test -tags=integration -race -count=1 ./internal/bootstrap/`: PASS. O `TestFxLifecycle` (I07b) agora sobe e para o publisher com o `goleak` limpo.
- [ ] **Passo 12: sensibilidade do lifecycle.** Trocar temporariamente o `cancel()` do `OnStop` por `_ = cancel` e rodar `go test -tags=integration -count=1 -run '^TestFxLifecycle$' ./internal/bootstrap/`.
  - Esperado: FAIL depois de ~30 s, com `"OnStop hook failed" … "outbox publisher: stop: context deadline exceeded"` e o `goleak` apontando a goroutine em `outbox.sleep`. Desfazer.
  - Refazer a verificação manual do passo 12 da Tarefa 5, agora com o publisher no grafo: o número de pendentes do banco compartilhado não muda.

**Checkpoint:** `go test -race ./...` e `go test -tags=integration -race -count=1 ./internal/... ./test/...` verdes.

---

### Tarefa 11: item 8 da consistência e I05e

O `AssertWalletConsistent` passa a exigir, para toda carteira de `test/integration`, a outbox publicada e entregue, com o conteúdo do banco (spec decisão 18). O I05e gera os 4 tipos pela API.

**Arquivos:**
- Implementação: `test/testkit/assert.go` (alterar)
- Testes: `test/integration/events_test.go` (criar)

**Interfaces:**
- Consome: `App.Audit` (Tarefa 3), `Eventually` (Tarefa 8), `Audit.WaitFor` (Tarefa 2).
- Produz: `testkit.OutboxPayloads(ctx, *pgxpool.Pool, walletID string) (map[string][]byte, error)`.

- [ ] **Passo 1: implementar o item 8** em `test/testkit/assert.go`:
  - imports: acrescentar `"bytes"`, `"encoding/json"` e `"reflect"`;
  - o comentário do `AssertWalletConsistent` vira:

```go
// AssertWalletConsistent is the verification of test-plan §6 for one wallet:
// the reconciliation of the API (item 1), the SQL checks of the ledger (items
// 2–6), the event matrix of the outbox (item 7) and the outbox published and
// delivered (item 8). It runs in t.Cleanup, so every call detaches from the
// test's context.
```

  - a última linha antes do `}` final da função: `a.assertOutboxDelivered(tb, walletID)`;
  - logo depois da função:

```go

// assertOutboxDelivered is item 8 of test-plan §6 (spec M4, decision 18): no
// event of the wallet stays unpublished, and every one of them reached the
// audit queue with the content of its payload column, on the contract.
func (a *App) assertOutboxDelivered(tb testing.TB, walletID string) {
	tb.Helper()
	Eventually(tb, AuditTimeout, "outbox of wallet "+walletID+" published", func(ctx context.Context) (bool, error) {
		var pending int
		err := a.env.Owner.QueryRow(ctx, `SELECT count(*) FROM outbox_events
			WHERE message_group_id = $1 AND published_at IS NULL`, walletID).Scan(&pending)
		return pending == 0, err
	})
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	stored, err := OutboxPayloads(ctx, a.env.Owner, walletID)
	if err != nil {
		tb.Fatalf("wallet %s: %v", walletID, err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	for id, deliveries := range a.Audit.WaitFor(tb, ids...) {
		for _, m := range deliveries {
			if same, err := sameJSON(m.Body, stored[id]); err != nil || !same {
				tb.Errorf("wallet %s: event %s delivered %s, stored %s (%v)", walletID, id, m.Body, stored[id], err)
			}
		}
	}
}

// OutboxPayloads returns the payload column of every outbox event of a
// wallet, by event id.
func OutboxPayloads(ctx context.Context, pool *pgxpool.Pool, walletID string) (map[string][]byte, error) {
	rows, err := pool.Query(ctx, `SELECT event_id::text, payload FROM outbox_events WHERE message_group_id = $1`, walletID)
	if err != nil {
		return nil, fmt.Errorf("outbox payloads: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return nil, fmt.Errorf("outbox payloads: %w", err)
		}
		out[id] = payload
	}
	return out, rows.Err()
}

// sameJSON compares two JSON documents by content: JSONB normalizes the text
// of the payload column (data-model §3.5).
func sameJSON(a, b []byte) (bool, error) {
	var va, vb any
	for raw, v := range map[*[]byte]*any{&a: &va, &b: &vb} {
		dec := json.NewDecoder(bytes.NewReader(*raw))
		dec.UseNumber()
		if err := dec.Decode(v); err != nil {
			return false, err
		}
	}
	return reflect.DeepEqual(va, vb), nil
}
```

- [ ] **Passo 2: escrever o I05e** `test/integration/events_test.go`:

```go
//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: OUT-07, OUT-08, OUT-09, OUT-11, OUT-12, OUT-13, TST-I05 (I05e)
//
// Every kind of event, produced through the API, reaches the audit queue on
// the contract of api/events.yaml (the Audit validates each delivery) and with
// the routing of messaging.md §5.2.
func TestEventContracts(t *testing.T) {
	t.Parallel()
	a := server.Client(t, "provider-a")
	w := server.OpenWallet(t, testkit.BRL("100.00")) // OPENING: INTERNAL
	bet := unique("bet")
	result(t, a, wager(w, "provider-a", "BET", "30.00", bet, ""), http.StatusOK)
	result(t, a, wager(w, "provider-a", "WIN", "10.00", unique("win"), bet), http.StatusOK) // with references
	result(t, a, wager(w, "provider-a", "LOSS", "0.00", unique("loss"), ""), http.StatusOK)
	result(t, a, wager(w, "provider-a", "BET", "1000.00", unique("bet"), ""), http.StatusUnprocessableEntity)
	result(t, a, wager(w, "provider-a", "REFUND", "5.00", unique("refund"), unique("bet")), http.StatusAccepted)

	stored, err := testkit.OutboxPayloads(t.Context(), server.Owner(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	if len(ids) != 9 {
		t.Fatalf("outbox of the wallet has %d events, want 9", len(ids))
	}
	got := server.Audit.WaitFor(t, ids...)
	seen := map[string]bool{}
	for id, payload := range stored {
		var head struct {
			EventType     string `json:"eventType"`
			CorrelationID string `json:"correlationId"`
			Data          struct {
				Origin                 string `json:"origin"`
				ReferenceTransactionID string `json:"referenceTransactionId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &head); err != nil {
			t.Fatal(err)
		}
		m := got[id][0]
		if m.GroupID != w.ID || m.DedupID != id ||
			m.Attributes["eventType"] != (testkit.Attribute{Type: "String", Value: head.EventType}) ||
			m.Attributes["eventVersion"] != (testkit.Attribute{Type: "Number", Value: "1"}) ||
			m.Attributes["correlationId"] != (testkit.Attribute{Type: "String", Value: head.CorrelationID}) {
			t.Errorf("%s %s: group %q dedup %q attributes %v", head.EventType, id, m.GroupID, m.DedupID, m.Attributes)
		}
		seen[head.EventType+" "+head.Data.Origin] = true
		if head.Data.ReferenceTransactionID != "" {
			seen["with references"] = true
		}
	}
	for _, want := range []string{
		"WagerTransactionProcessed INTERNAL", "WagerTransactionProcessed EXTERNAL", "with references",
		"WalletBalanceChanged ", "WagerTransactionRejected ", "WagerTransactionPendingReference ",
	} {
		if !seen[want] {
			t.Errorf("no %q among the delivered events", want)
		}
	}
}
```

- [ ] **Passo 3: rodar.** `go test -tags=integration -race -count=1 ./test/integration/`: PASS em ≈ 10 s.
  - O comportamento já existe desde a Tarefa 10, então os passos 4 e 5 fazem a checagem de sensibilidade (development-workflow §4.3).
- [ ] **Passo 4: sensibilidade do item 8.** Remover temporariamente `outbox.Module,` (e o import) de `bootstrap.Options()` e rodar `-run '^TestHappyPathFlow$' ./test/integration/`.
  - Esperado: FAIL com `outbox of wallet … published: not reached within 10s`. Desfazer.
  - Registrar no comentário do `assertOutboxDelivered`, depois do parágrafo: `// Sensitivity: without outbox.Module in the graph, every wallet failed "outbox of wallet … published".`
- [ ] **Passo 5: sensibilidade do I05e.** Remover temporariamente a linha do atributo `"correlationId"` do `SNSSink.Publish` e rodar `-run '^TestEventContracts$' ./test/integration/`.
  - Esperado: FAIL com `…: group "…" dedup "…" attributes map[eventType:{String …} eventVersion:{Number 1}]`. Desfazer.
  - Registrar no teste, depois da linha `// Covers:`: `// Sensitivity: without the correlationId attribute in the SNS sink, every event failed the attribute check.`

**Checkpoint:** `go test -tags=integration -race -count=1 ./...` verde.

---

### Tarefa 12: verificação e encerramento do marco

Segue a definição de pronto ([`development-workflow.md`](../../development-workflow.md) §5) e o "pronto quando" da spec §1.

- [ ] **Passo 1: qualidade.** `make check`.
  - Esperado: `0 issues.`, sem diff de formatação e `go mod tidy -diff` limpo.
  - Se o `gofumpt` reclamar de um literal composto, `make fmt` e revisar o diff.
- [ ] **Passo 2: integração.** `make test-integration` verde. Depois, mais duas execuções seguidas de `go test -tags=integration -race -count=1 ./internal/adapters/outbox/ ./internal/adapters/postgres/ ./internal/bootstrap/ ./test/...`, para descartar instabilidade.
- [ ] **Passo 3: compose.**
  - `docker compose up --build --wait`: o `aws-init` reaplica a política, e as 3 réplicas sobem saudáveis.
  - Nos logs, cada réplica mostra `sns topic resolved` e `outbox publisher started` com um `owner` próprio: `docker compose logs app-1 app-2 app-3 | grep -E 'sns topic|outbox publisher'`.
- [ ] **Passo 4: fluxo por `curl`.**
  - Obter o token do `wallet-service` (como no `api/requests.http`).
  - Fazer um `POST /wallets` com saldo na `:8081`.
  - Depois de ~1 s, ler `wallet-events-audit.fifo` com a chave raiz. Esperado: os 2 eventos da carteira (`WagerTransactionProcessed` com `origin` `INTERNAL` e `WalletBalanceChanged`), com `MessageGroupId` = `walletId` e os 3 atributos. O comando:

```sh
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 AWS_ENDPOINT_URL=http://localhost:4566 \
  aws sqs receive-message --queue-url "$(AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
  AWS_ENDPOINT_URL=http://localhost:4566 aws sqs get-queue-url --queue-name wallet-events-audit.fifo --query QueueUrl --output text)" \
  --max-number-of-messages 10 --wait-time-seconds 2 --message-attribute-names All --message-system-attribute-names MessageGroupId
```

  - `docker compose exec -T postgres psql -U postgres -d pda -tAc "SELECT count(*) FROM outbox_events WHERE published_at IS NULL"` deve dar `0`.
  - Parar uma réplica (`docker compose stop app-2`): o log mostra `outbox publisher stopping` e depois `…stopped`. Subir de novo com `docker compose start app-2`.
- [ ] **Passo 5: requisitos.** Marcar em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes (spec §9):
  - `[x]`: OUT-03, OUT-04, OUT-05, OUT-07 e OUT-10;
  - `[~]`: OUT-02 (falta a inbox, M5), OUT-06 (os cenários com processo interrompido são do M8), TST-I05 (falta a DLQ, M5), OBS-03 (métricas de outbox) e FX-01/FX-03 (módulo e stop do publisher);
  - o E8 fica comprovado pelo I05b.
- [ ] **Passo 6: documentos vivos.**
  - **`ARCHITECTURE.md`:**
    - §9.2: o ARN via STS, o contrato `api/events.yaml`, o stop que confirma o que está em voo e o backoff configurável;
    - §13.2: as 6 métricas de outbox como implementadas;
    - §16: o isolamento dos testes;
    - §17 e estado: M4 concluído.
  - **[`implementation-plan.md`](../../implementation-plan.md):** M4 marcado ✅, com "Entregue também" (`testkit.Eventually`, `NewTestEnv`, isolamento do `bootstrap`, `TestAuditCollector`) e os links da spec e deste plano; §5 com o risco do `interval` do pgx descartado.
  - **[`docs/dev/diary.md`](../diary.md):** entrada do M4 e o "Onde paramos" apontando para o M5.
- [ ] **Passo 7: resumo e proposta de commits.** Mostrar a saída dos passos 1–4 e propor commits atômicos (Conventional Commits, sem trailer de coautoria):
  1. `feat(config): add the outbox publisher settings`
  2. `feat(app): add the outbox store port`
  3. `feat(postgres): claim, confirm and fail outbox events`
  4. `feat(awsclient): resolve and verify the events topic on start`
  5. `feat(observability): add the outbox metrics`
  6. `feat(outbox): publish the outbox to the sns fifo topic`
  7. `feat(bootstrap): run the outbox publisher`
  8. `build(aws): allow the service to read the events topic attributes`
  9. `test(testkit): validate events against the contract and collect the audit queue`
  10. `test(integration): cover the outbox publisher and the event contracts`
  11. `docs: record m4 outbox decisions`
  12. `docs(dev): add m4 spec, plan and diary entry`

  O autor decide se, quando e como commitar.
