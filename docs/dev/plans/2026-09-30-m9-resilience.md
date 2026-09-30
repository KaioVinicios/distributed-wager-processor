# M9 — Resiliência: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` e `superpowers:test-driven-development` em cada tarefa ([`development-workflow.md`](../../development-workflow.md) §3; subagentes só com pedido explícito). **Sem passos de commit:** os commits são propostos no fim, com o autor autorizando, e não levam trailer de coautoria de IA.

**Objetivo:** fechar os testes R01–R04 no cluster e2e de 3 processos e dar ao HTTP o prazo por requisição que o R01 exige (D-04).

**Arquitetura:**
- **`testkit`:** ganha o `Pause`, que roda `docker compose pause|unpause`, e o `Cluster.StopAsync`, `Instance.ReadyStatus` e `Harness.CloseIdleConnections`.
- **Produto:** ganha `HTTP_REQUEST_TIMEOUT` e um middleware `withDeadline` nas rotas autenticadas. O `context.DeadlineExceeded` já vira 503 pela D-05, então nada mais muda.
- **Testes R:** ficam em `test/e2e/resilience_test.go`, sem `t.Parallel()`.

**Stack:** Go 1.27.1, `net/http`, Uber Fx, pgx v5, AWS SDK v2, Docker Compose; testes unitários e com a tag `e2e`.

**Spec:** [`dev/specs/2026-09-30-m9-resilience-design.md`](../specs/2026-09-30-m9-resilience-design.md) (aprovada).

## Restrições globais

- `HTTP_REQUEST_TIMEOUT`: padrão `10s`; integração `10s`; e2e `5s`. Validação: `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30s` (`config.MaxHTTPRequestTimeout`, o `WriteTimeout` do servidor).
- Quedas: PostgreSQL por **15 s** (R01); MiniStack por **10 s** (R02).
- Instância parada no R03/R04: `DB_LOCK_TIMEOUT=4s`, `DB_MAX_CONNS=8` e, no R03, `SQS_PROCESSING_TIMEOUT=4s`.
- `SHUTDOWN_TIMEOUT` do e2e: 5 s. É o limite da saída no R03/R04.
- Todo teste R: sem `t.Parallel()`, com `defer cluster.Restore(t)`, `// Covers: …` e `// Sensitivity: …`.
- O red é uma **asserção** falhando. Um stub com a assinatura certa vale; um erro de compilação não conta.
- `make check` verde ao fim de cada tarefa de código. `make test-e2e` e `make test-integration` verdes na Tarefa 7.
- Sem `time.Sleep` como sincronização. Os `time.Sleep` dos testes R marcam só a **duração da queda**, que é o cenário, e trazem um comentário dizendo isso.
- Nunca `float` (E3/`forbidigo`): as métricas são lidas com `MetricValue` (inteiro), e a idade da outbox, por SQL com `::int`.

## Foco da revisão

1. **Um `panic` por timeout do `go test` com o serviço pausado** deve deixar a próxima execução funcional: o `make infra-up` faz `unpause` antes do `up` (Tarefa 2).
2. **Um commit em voo quando o prazo vence** tem resultado desconhecido: o reenvio com a mesma chave depois da queda deve responder 200, seja operação nova ou replay, e nunca 409 nem um segundo débito. O R01 reenvia todo não-200 e confere o saldo exato (Tarefa 3).
3. **Rotas públicas** não podem ganhar o prazo: um `/health/ready` com checagem lenta, mas dentro dos 2 s, precisa responder 200 (Tarefa 4, `TestEdgeRequestDeadline`).
4. **Uma conexão keep-alive** antiga para a instância que está parando não pode mascarar a recusa. O R04 fecha as conexões ociosas antes da requisição nova (Tarefa 6).
5. **Uma instância que não volta a ficar pronta depois do `unpause`** (pool quebrado) deve reprovar o teste. O R01 e o R02 esperam `ReadyStatus == 200` nas 3 instâncias antes do `Restore` (Tarefas 2 e 3).

---

## Mapa de arquivos

| Arquivo | Mudança | Tarefa |
| --- | --- | --- |
| `test/testkit/cluster.go` | `StopAsync`, `Stop` sobre ele, `Instance.ReadyStatus` | 1 |
| `test/testkit/harness.go` | `CloseIdleConnections` | 1 |
| `test/e2e/cluster_test.go` | `TestClusterStopAsync` | 1 |
| `test/testkit/compose.go` (novo) | `Pause` | 2 |
| `Makefile` | `unpause` no `infra-up` | 2 |
| `test/e2e/resilience_test.go` (novo) | R02, R01, R03, R04 e os helpers `consumerOnlyOn0`, `lockWallets`, `waitLogLine`, `logIndex`, `waitReady` | 2, 3, 5, 6 |
| `internal/config/config.go`, `validate.go`, `config_test.go` | `HTTPRequestTimeout`, `MaxHTTPRequestTimeout`, validação | 4 |
| `internal/adapters/httpapi/handler.go`, `routes.go`, `middleware.go`, `module.go`, `server.go` | `Options.RequestTimeout`, `withDeadline`, ligação, `WriteTimeout` pela constante | 4 |
| `internal/adapters/httpapi/edge_test.go` | `TestEdgeRequestDeadline` | 4 |
| `test/testkit/env.go`, `test/testkit/cluster.go` | `HTTPRequestTimeout` 10 s na integração e 5 s no e2e | 4 |
| `docs/…`, `ARCHITECTURE.md` | spec §6 (fecho) | 7 |

---

### Tarefa 1: `StopAsync`, `ReadyStatus` e `CloseIdleConnections` no `testkit`

**Arquivos:** modificar `test/testkit/cluster.go`, `test/testkit/harness.go`; testar em `test/e2e/cluster_test.go`.

**Interfaces produzidas:**
- `func (c *Cluster) StopAsync(i int) <-chan error`: envia `SIGTERM` à execução atual da instância i e devolve na hora. O canal recebe o resultado do `terminate` (nil só com código 0 dentro de `stopTimeout`).
- `func (n *Instance) ReadyStatus(tb testing.TB) int`: o status de `GET /health/ready` da instância, ou 0 num erro de transporte.
- `func (h *Harness) CloseIdleConnections()`: fecha as conexões keep-alive ociosas do cliente do harness.

- [ ] **Passo 1: stubs com a assinatura certa**

```go
// cluster.go
func (c *Cluster) StopAsync(i int) <-chan error {
	done := make(chan error, 1)
	done <- errors.New("testkit: StopAsync not implemented")
	return done
}

func (n *Instance) ReadyStatus(tb testing.TB) int { return -1 }

// harness.go
func (h *Harness) CloseIdleConnections() {}
```

- [ ] **Passo 2: escrever o teste** (`test/e2e/cluster_test.go`)

```go
// Covers: FX-04 (the harness of R03 and R04)
//
// StopAsync returns before the instance is gone and reports a clean stop;
// ReadyStatus follows the instance from ready to gone.
func TestClusterStopAsync(t *testing.T) {
	defer cluster.Restore(t)
	if got := cluster.Instance(2).ReadyStatus(t); got != http.StatusOK {
		t.Fatalf("ReadyStatus before the stop = %d, want 200", got)
	}
	stopped := cluster.StopAsync(2)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("StopAsync: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("instance 2 did not stop within 20s")
	}
	if code := cluster.Instance(2).WaitExit(t); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	cluster.CloseIdleConnections()
	if got := cluster.Instance(2).ReadyStatus(t); got != 0 {
		t.Fatalf("ReadyStatus after the stop = %d, want 0 (no process)", got)
	}
}
```

- [ ] **Passo 3: ver falhar**

Rodar `make infra-up && go test -tags=e2e -race -p 1 -count=1 -run '^TestClusterStopAsync$' ./test/e2e/...`.
Esperado: FAIL com `ReadyStatus before the stop = -1, want 200`.

- [ ] **Passo 4: implementar**

```go
// cluster.go

// StopAsync sends SIGTERM to the current run of instance i and returns at
// once; the channel receives the result of the stop once the process is gone:
// nil only for exit code 0 within stopTimeout (R03, R04 release their lock
// barrier while the instance stops).
func (c *Cluster) StopAsync(i int) <-chan error {
	done := make(chan error, 1)
	p := c.procs[i]
	go func() { done <- c.terminate(p) }()
	return done
}

// Stop ends instance i with SIGTERM and fails tb unless it exits with 0.
func (c *Cluster) Stop(tb testing.TB, i int) {
	tb.Helper()
	if err := <-c.StopAsync(i); err != nil {
		tb.Fatalf("instance %d: %v", i, err)
	}
}

// ReadyStatus is the status of GET /health/ready on this instance, or 0 when
// nothing answers.
func (n *Instance) ReadyStatus(tb testing.TB) int {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.c.targets[n.i].baseURL+"/health/ready", nil)
	if err != nil {
		tb.Fatal(err)
	}
	resp, err := n.c.http.Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// harness.go

// CloseIdleConnections drops the idle keep-alive connections of the harness
// client, so the next request dials again (R04: a stopping instance refuses).
func (h *Harness) CloseIdleConnections() { h.http.CloseIdleConnections() }
```

- [ ] **Passo 5: ver passar**

Rodar o mesmo comando do Passo 3 e depois `-run '^TestCluster'`. Esperado: PASS. O `TestClusterInstanceLifecycle` continua verde com o `Stop` reescrito.

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 2: `testkit.Pause`, `unpause` no `infra-up` e R02 (`TestSQSOutage`)

**Arquivos:** criar `test/testkit/compose.go` e `test/e2e/resilience_test.go`; modificar `Makefile`.

**Interfaces:**
- Consome: `Instance.ReadyStatus` (Tarefa 1); `waitPublished`, `wager`, `wagerRequest`, `unique` (de `test/e2e/helpers_test.go`).
- Produz:
  - `func Pause(tb testing.TB, service string) (resume func())`: `docker compose pause <service>` na raiz do repositório. O `resume` é idempotente e também fica registrado no `tb.Cleanup`;
  - `waitReady(t, status int)` em `resilience_test.go`: espera o `/health/ready` das 3 instâncias responder `status`.

- [ ] **Passo 1: stub do `Pause`** (`test/testkit/compose.go`)

```go
package testkit

import "testing"

// Pause freezes a service of the compose (test-plan §3.4, M9).
func Pause(tb testing.TB, service string) (resume func()) { return func() {} }
```

- [ ] **Passo 2: escrever o R02** (`test/e2e/resilience_test.go`)

```go
//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// sqsOutage is how long R02 keeps the MiniStack paused (test-plan §5.5).
const sqsOutage = 10 * time.Second

// Covers: F6, OUT-04, HTTP-08, OBS-04 (R02)
// Sensitivity: <preencher na Tarefa 2, Passo 5>
//
// The broker is paused: the instances report unready, the HTTP goes on, the
// outbox piles up and ages, and after the broker returns every event is
// published and reaches the audit queue (item 8 of the check of each wallet).
func TestSQSOutage(t *testing.T) {
	defer cluster.Restore(t)
	wallets := make([]testkit.Wallet, 3)
	ids := make([]string, 3)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("100.00"))
		ids[i] = wallets[i].ID
	}
	a := cluster.Client(t, "provider-a")

	resume := testkit.Pause(t, "ministack")
	pausedAt := time.Now()
	waitReady(t, http.StatusServiceUnavailable)
	for i := range 6 {
		bet := wager(wallets[i%3], "provider-a", "BET", "1.00", unique("bet"), "")
		if r := result(t, a, bet, http.StatusOK); r.Status != "PROCESSED" {
			t.Fatalf("BET during the outage = %+v, want PROCESSED", r)
		}
	}
	testkit.Eventually(t, sqsOutage, "outbox backlog reported", func(context.Context) (bool, error) {
		var pending int64
		for i := range 3 {
			pending += cluster.Instance(i).MetricValue(t, "outbox_pending_events")
		}
		return pending > 0, nil
	})
	testkit.Eventually(t, sqsOutage, "oldest pending event aging", func(ctx context.Context) (bool, error) {
		var age int
		err := cluster.Owner().QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM now() - min(occurred_at))::int, 0)
			FROM outbox_events WHERE published_at IS NULL AND message_group_id = ANY($1)`, ids).Scan(&age)
		return age >= 3, err
	})
	time.Sleep(time.Until(pausedAt.Add(sqsOutage))) // the length of the outage, not a synchronization
	resume()

	waitReady(t, http.StatusOK)
	for _, w := range wallets {
		waitPublished(t, w.ID)
	}
}

// waitReady waits until GET /health/ready of every instance answers status.
func waitReady(t *testing.T, status int) {
	t.Helper()
	testkit.Eventually(t, 30*time.Second, "every instance ready with "+http.StatusText(status), func(context.Context) (bool, error) {
		for i := range 3 {
			if cluster.Instance(i).ReadyStatus(t) != status {
				return false, nil
			}
		}
		return true, nil
	})
}
```

- [ ] **Passo 3: ver falhar**

Rodar `go test -tags=e2e -race -p 1 -count=1 -run '^TestSQSOutage$' ./test/e2e/...`.
Esperado: FAIL no `waitReady(503)` (`every instance ready with Service Unavailable` não acontece em 30 s), porque o stub não pausa nada.

- [ ] **Passo 4: implementar o `Pause` e o `infra-up`**

```go
package testkit

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// composeTimeout bounds one docker compose command.
const composeTimeout = time.Minute

// Pause freezes a service of the compose with docker compose pause, to
// simulate its unavailability (test-plan §3.4, M9): its processes stop, while
// the kernel of the container keeps accepting connections. resume unpauses it
// once; it is also registered in tb.Cleanup. A test that pauses must not run
// in parallel with any other.
func Pause(tb testing.TB, service string) (resume func()) {
	tb.Helper()
	compose(tb, "pause", service)
	var once sync.Once
	resume = func() { once.Do(func() { compose(tb, "unpause", service) }) }
	tb.Cleanup(resume)
	return resume
}

// compose runs docker compose at the root of the repository, the project
// make infra-up started.
func compose(tb testing.TB, args ...string) {
	tb.Helper()
	root, err := findRepoRoot()
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), composeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("docker compose %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
```

Se o `gosec` (G204) apontar o `exec.CommandContext`, adicionar `//nolint:gosec // docker with the compose arguments of the test`. O `nolintlint` recusa a diretiva se ela não for necessária.

`Makefile`, o `infra-up` passa a começar por:

```make
infra-up:
	-$(COMPOSE) unpause postgres ministack 2>/dev/null
	$(COMPOSE) up -d --wait $(INFRA_SERVICES)
```

(o `-` do make ignora o erro de "não está pausado" ou "não existe").

- [ ] **Passo 5: ver passar e checar a sensibilidade**

Rodar o comando do Passo 3. Esperado: PASS.

Sabotagens (uma de cada vez, desfeitas logo depois):
1. **O checker do SQS sempre saudável:** o `Check` do checker de fila em `internal/adapters/awsclient/queues.go` devolve `nil`. Esperado: FAIL no `waitReady(503)`.
2. **O caminho de falha do publisher confirmando:** em `internal/adapters/outbox/publisher.go`, o ramo de erro do `Publish` chama `MarkPublished` no lugar de `MarkFailed`. Esperado: FAIL no `AssertWalletConsistent` do `Cleanup` (item 8: evento sem entrega na auditoria).

Registrar no teste:

```go
// Sensitivity: the SQS checker always nil → the instances stay ready during the outage; the failed
// Publish marked as published → an event never reaches the audit queue (item 8 of the wallet check).
```

Validar o `Makefile`: `docker compose pause postgres && make infra-up` termina com o PostgreSQL saudável.

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 3: R01 (`TestPostgresOutage`), o red do prazo HTTP

**Arquivos:** modificar `test/e2e/resilience_test.go`.

**Interfaces:**
- Consome: `Pause`, `waitReady`, `ReadyStatus`, `Instance.MetricValue`, `Instance.Logs`, `inboxOutcomes`, `sqsWager`, `balanceOf`, `result`.
- Produz: `consumerOnlyOn0(t, extra map[string]string)` (reusado no R03).

- [ ] **Passo 1: escrever o R01**

```go
// pgOutage is how long R01 keeps the PostgreSQL paused: the first failure of
// the consumer (3 s of deadline + 1 s of ping) and then more than one
// redelivery cycle (3 s + 2 s of backoff) with the consumer paused (spec M9, decision 3).
const pgOutage = 15 * time.Second

// consumerOnlyOn0 leaves the SQS consumer on instance 0 alone, with extra on
// it when not nil; instances 1 and 2 run every other role. The test defers
// cluster.Restore.
func consumerOnlyOn0(t *testing.T, extra map[string]string) {
	t.Helper()
	others := testkit.EnvOf(config.Roles{HTTP: true, OutboxPublisher: true, ReferenceWorker: true})
	cluster.Restart(t, 1, others)
	cluster.Restart(t, 2, others)
	if extra != nil {
		cluster.Restart(t, 0, extra)
	}
}

// httpCall is one BET of R01 and what came back.
type httpCall struct {
	bet            testkit.Wager
	at             time.Time
	status         int
	retryAfter     string
	code           string
	transportError error
}

// Covers: F6, SQS-07, HTTP-08, OBS-04, E5 (R01)
// Sensitivity: <preencher na Tarefa 4, Passo 8>
//
// The database is paused for 15 s under HTTP (3 instances) and SQS traffic.
// While it is down the HTTP answers 503 with Retry-After within
// HTTP_REQUEST_TIMEOUT, every instance reports unready and the consumer,
// alone on instance 0, stops receiving once paused. Afterwards every message
// is processed once, none reaches the DLQ, and every BET answered with 503
// is sent again with its key and recorded once.
func TestPostgresOutage(t *testing.T) {
	defer cluster.Restore(t)
	consumerOnlyOn0(t, nil)
	wallets := make([]testkit.Wallet, 5)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("1000.00"))
	}
	a := cluster.Client(t, "provider-a")
	consumer := cluster.Instance(0)

	var (
		mu       sync.Mutex
		calls    []httpCall
		msgs     []string
		perWallet = map[string]int{}
		inFlight sync.WaitGroup
	)
	stop := make(chan struct{})
	generated := make(chan struct{})
	go func() { // one BET by HTTP and one by SQS every 200 ms until stop
		defer close(generated)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			w := wallets[i%len(wallets)]
			bet := wager(w, "provider-a", "BET", "1.00", unique("bet"), "")
			inFlight.Go(func() {
				c := httpCall{bet: bet}
				resp, err := a.Try(t, wagerRequest(bet))
				c.at, c.transportError = time.Now(), err
				if err == nil {
					c.status, c.retryAfter = resp.Status, resp.Header.Get("Retry-After")
					if resp.Status == http.StatusServiceUnavailable {
						c.code = resp.Problem(t).Code
					}
				}
				mu.Lock()
				calls = append(calls, c)
				mu.Unlock()
			})
			msgID := unique("msg")
			cluster.SendWager(t, sqsWager(t, msgID, wager(w, "provider-a", "BET", "1.00", unique("sqs"), "")), testkit.SendOpts{GroupID: w.ID})
			mu.Lock()
			msgs = append(msgs, msgID)
			perWallet[w.ID] += 2
			mu.Unlock()
		}
	}()

	time.Sleep(time.Second) // traffic before the outage, not a synchronization
	resume := testkit.Pause(t, "postgres")
	pausedAt := time.Now()
	waitReady(t, http.StatusServiceUnavailable)
	testkit.Eventually(t, pgOutage, "consumer paused", func(context.Context) (bool, error) {
		return strings.Contains(consumer.Logs(), "sqs consumer paused"), nil
	})
	time.Sleep(3 * time.Second) // a receive already under way when the gate closed ends in this time
	if time.Until(pausedAt.Add(pgOutage)) < 2*time.Second {
		t.Fatalf("the consumer paused %v after the outage began: no window left to watch it", time.Since(pausedAt))
	}
	receivedPaused := consumer.MetricValue(t, "sqs_messages_received_total")
	time.Sleep(time.Until(pausedAt.Add(pgOutage))) // the length of the outage, not a synchronization
	if got := consumer.MetricValue(t, "sqs_messages_received_total"); got != receivedPaused {
		t.Fatalf("the paused consumer received %d messages during the outage, want none", got-receivedPaused)
	}
	resume()
	resumedAt := time.Now()
	time.Sleep(time.Second) // traffic after the outage, not a synchronization
	close(stop)
	<-generated
	inFlight.Wait()

	var during int
	for _, c := range calls {
		if c.at.Before(pausedAt.Add(time.Second)) || c.at.After(resumedAt) {
			continue
		}
		during++
		if c.status != http.StatusServiceUnavailable || c.retryAfter != "1" || c.code != "TEMPORARILY_UNAVAILABLE" {
			t.Errorf("answer during the outage = %d %q Retry-After %q (transport error %v), want 503 TEMPORARILY_UNAVAILABLE with Retry-After 1",
				c.status, c.code, c.retryAfter, c.transportError)
		}
	}
	if during == 0 {
		t.Fatal("no HTTP answer arrived during the outage: the requests waited for the database")
	}

	waitReady(t, http.StatusOK)
	for _, c := range calls {
		if c.status != http.StatusOK {
			if r := result(t, a, c.bet, http.StatusOK); r.Status != "PROCESSED" {
				t.Fatalf("resend of %s = %+v, want PROCESSED", c.bet.ExternalTransactionID, r)
			}
		}
	}
	testkit.Eventually(t, 60*time.Second, "every message processed", func(context.Context) (bool, error) {
		return inboxOutcomes(t, msgs...)["PROCESSED"] == len(msgs), nil
	})
	if outcomes := inboxOutcomes(t, msgs...); len(outcomes) != 1 {
		t.Fatalf("inbox outcomes = %v, want only PROCESSED", outcomes)
	}
	cluster.AssertQueueDrained(t)
	if n := cluster.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ depth = %d after the outage, want 0", n)
	}
	for _, w := range wallets {
		n := perWallet[w.ID]
		got := balanceOf(t, w.ID)
		if got.Balance != testkit.BRL(fmt.Sprintf("%d.00", 1000-n)) || got.Version != int64(1+n) {
			t.Fatalf("wallet %s = %+v, want %d debits of 1.00", w.ID, got, n)
		}
	}
}
```

Imports novos em `resilience_test.go`: `fmt`, `strings`, `sync`, `github.com/KaioVinicios/pda/internal/config`. **Ao escrever, conferir** o tipo de `testkit.Wallet.Version` (o `int64(…)` acima) e o nome do campo da chave externa em `testkit.Wager` (`ExternalTransactionID`, usado no C05c). Ajustar a conversão ao tipo real.

- [ ] **Passo 2: ver falhar pelo motivo do achado 1**

Rodar `go test -tags=e2e -race -p 1 -count=1 -run '^TestPostgresOutage$' ./test/e2e/...`.
Esperado: FAIL com `no HTTP answer arrived during the outage: the requests waited for the database`. As asserções anteriores (readiness 503, consumidor pausado e estável) passam.

**Se falhar em outro ponto, parar e investigar** (`superpowers:systematic-debugging`) antes de seguir: é uma premissa da spec que não vale.

Este é o red real da mudança de produto da Tarefa 4.

- [ ] **Checkpoint:** o `make check` pode ficar verde já (o R01 só roda com a tag `e2e`).

---

### Tarefa 4: `HTTP_REQUEST_TIMEOUT` e o middleware `withDeadline`

**Arquivos:**
- modificar `internal/config/config.go`, `internal/config/validate.go` e `internal/config/config_test.go`;
- modificar `internal/adapters/httpapi/handler.go`, `routes.go`, `middleware.go`, `module.go` e `server.go`;
- modificar `internal/adapters/httpapi/edge_test.go`, `test/testkit/env.go` e `test/testkit/cluster.go`.

**Interfaces produzidas:**
- `config.Config.HTTPRequestTimeout time.Duration` (`env:"HTTP_REQUEST_TIMEOUT" envDefault:"10s"`);
- `config.MaxHTTPRequestTimeout = 30 * time.Second`;
- `httpapi.Options.RequestTimeout time.Duration` (0 = sem prazo).

- [ ] **Passo 1: stubs**

`config.go`: o campo e a constante, ainda sem validação. `handler.go`: o campo `RequestTimeout` em `Options`, ainda sem uso.

```go
// config.go, junto de MaxShutdownTimeout
// MaxHTTPRequestTimeout is the WriteTimeout of the API server;
// HTTP_REQUEST_TIMEOUT must stay below it (D-04).
const MaxHTTPRequestTimeout = 30 * time.Second

// na struct, depois de DBLockTimeout
	HTTPRequestTimeout time.Duration `env:"HTTP_REQUEST_TIMEOUT" envDefault:"10s"`

// handler.go, em Options
	// RequestTimeout bounds every authenticated route (HTTP_REQUEST_TIMEOUT, D-04); 0 sets none.
	RequestTimeout time.Duration
```

- [ ] **Passo 2: escrever os testes**

`config_test.go`:
- `validConfig()` ganha `HTTPRequestTimeout: 10 * time.Second`;
- no teste do `Load` com todas as variáveis, o mapa `env` ganha `"HTTP_REQUEST_TIMEOUT": "4s"` e o `want` ganha `HTTPRequestTimeout: 4 * time.Second` (o `DB_LOCK_TIMEOUT` ali é 2 s);
- `TestValidate_RejectsInvalidValues` ganha:

```go
		{"request timeout at lock timeout", func(c *config.Config) { c.HTTPRequestTimeout = c.DBLockTimeout }, "HTTP_REQUEST_TIMEOUT"},
		{"request timeout at write timeout", func(c *config.Config) { c.HTTPRequestTimeout = config.MaxHTTPRequestTimeout }, "HTTP_REQUEST_TIMEOUT"},
```

`edge_test.go` (imports a mais: `context`, `log/slog`, `time`, `internal/domain/wallet`, `internal/observability`):

```go
// blockingReader answers only when the request's context ends, as a query
// on a database that does not answer.
type blockingReader struct{ stubWallets }

func (blockingReader) GetWallet(ctx context.Context, _ string) (wallet.Wallet, error) {
	<-ctx.Done()
	return wallet.Wallet{}, ctx.Err()
}

// slowChecker takes 100 ms, within the 2 s of a health check.
type slowChecker struct{}

func (slowChecker) Name() string { return "postgres" }
func (slowChecker) Check(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

// Covers: HTTP-09, D-04 (U30: request deadline)
func TestEdgeRequestDeadline(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	e := edge{logs: &syncBuffer{}, handler: httpapi.New(
		httpapi.Options{Log: log, RequestTimeout: 50 * time.Millisecond},
		httpapi.Services{Auth: stubAuth{}, Queries: blockingReader{},
			Health: observability.NewHealth(log, []observability.Checker{slowChecker{}}, 2*time.Second)},
	)}

	t.Run("an authenticated route ends with 503", func(t *testing.T) {
		answered := make(chan *httptest.ResponseRecorder, 1)
		go func() { answered <- e.do(t, call{method: http.MethodGet, path: "/wallets/w-1", token: tokenInternal}) }()
		select {
		case rec := <-answered:
			wantProblem(t, rec, http.StatusServiceUnavailable, "TEMPORARILY_UNAVAILABLE", "")
			if got := rec.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no answer within 2s: the request has no deadline")
		}
	})
	t.Run("the health has no deadline", func(t *testing.T) {
		if rec := e.do(t, call{method: http.MethodGet, path: "/health/ready"}); rec.Code != http.StatusOK {
			t.Fatalf("GET /health/ready = %d, want 200: a 100 ms check fits its 2 s", rec.Code)
		}
	})
}
```

(`httptest` já está importado em `stubs_test.go`, do mesmo pacote. Se o linter pedir, importar também em `edge_test.go`.)

- [ ] **Passo 3: ver falhar**

Rodar `go test -race -run 'TestValidate_RejectsInvalidValues|TestEdgeRequestDeadline' ./internal/config/... ./internal/adapters/httpapi/...`.
Esperado:
- os dois casos novos falham com `Validate() vars = [], want [HTTP_REQUEST_TIMEOUT]`;
- o `TestEdgeRequestDeadline/an authenticated route ends with 503` falha com `no answer within 2s: the request has no deadline`;
- `the health has no deadline` passa (é a guarda).

- [ ] **Passo 4: implementar**

`validate.go`, depois da checagem de `DB_LOCK_TIMEOUT`:

```go
	if c.HTTPRequestTimeout <= c.DBLockTimeout || c.HTTPRequestTimeout >= MaxHTTPRequestTimeout {
		fail("HTTP_REQUEST_TIMEOUT", "must be greater than DB_LOCK_TIMEOUT and less than "+MaxHTTPRequestTimeout.String())
	}
```

`middleware.go`:

```go
// withDeadline bounds an authenticated request by d (HTTP_REQUEST_TIMEOUT,
// D-04): a database that does not answer — frozen, its connections still
// open — ends as context.DeadlineExceeded, which is transient (D-05), so the
// answer is 503 with Retry-After. d = 0 sets no deadline.
func withDeadline(d time.Duration, next http.Handler) http.Handler {
	if d <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
```

`routes.go`, em `New`:

```go
		if rt.roles != nil {
			h = authenticate(s.Auth, opts.Log, opts.Metrics, rt.roles, h)
			h = withDeadline(opts.RequestTimeout, h)
		}
```

(e o comentário de `New` passa a citar o prazo: "then, per authenticated route, the request deadline, authentication and roles…").

`module.go`: `New(Options{DocsEnabled: p.Config.APIDocsEnabled, RequestTimeout: p.Config.HTTPRequestTimeout, Log: p.Log, Metrics: p.Metrics}, …)`.

`server.go`: `WriteTimeout: config.MaxHTTPRequestTimeout`, com o comentário ajustado ("the WriteTimeout bounds HTTP_REQUEST_TIMEOUT from above"). O `TestNewServerTimeouts` continua esperando 30 s.

`test/testkit/env.go`, em `Config()`: `HTTPRequestTimeout: 10 * time.Second,`.

`test/testkit/cluster.go`, em `e2eTimes`: `c.HTTPRequestTimeout = 5 * time.Second`.

- [ ] **Passo 5: ver passar**

Rodar o comando do Passo 3. Esperado: PASS. Depois, `go test -race ./test/testkit/...`: o `TestEnvOf` cobre a variável nova.

- [ ] **Passo 6: R01 verde**

Rodar `go test -tags=e2e -race -p 1 -count=1 -run '^TestPostgresOutage$' ./test/e2e/...`. Esperado: PASS.

- [ ] **Passo 7: sensibilidade do R01**

1. **`withDeadline` devolvendo `next` sempre:** esperado FAIL com `no HTTP answer arrived during the outage` (é o red da Tarefa 3, reconfirmado).
2. **`healthGate.Report` retornando logo no início** (`internal/adapters/sqsconsumer/health_gate.go`): esperado FAIL no `consumer paused` (nunca registrado).
3. **`healthGate.Wait` devolvendo `nil` sempre** (a pausa loga, mas não segura): esperado FAIL com `the paused consumer received N messages during the outage`.

- [ ] **Passo 8: registrar**

```go
// Sensitivity: withDeadline returning next → no HTTP answer during the outage; healthGate.Report a no-op
// → the consumer never pauses; healthGate.Wait always nil → the paused consumer keeps receiving.
```

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 5: R03 (`TestGracefulShutdownSQS`)

**Arquivos:** modificar `test/e2e/resilience_test.go`.

**Interfaces:**
- Consome: `consumerOnlyOn0` (Tarefa 3), `StopAsync` (Tarefa 1), `waitLockWaiters` (`channels_test.go`).
- Produz: `lockWallets`, `waitLogLine`, `logIndex` e `stoppedInstance` (reusados no R04).

- [ ] **Passo 1: escrever o R03**

```go
// stoppedInstance is the environment of the instance R03 and R04 stop: a
// lock timeout and a deadline wider than the time the test takes to release
// its barrier, and room in the pool for every session waiting (spec M9, decision 4).
var stoppedInstance = map[string]string{"DB_LOCK_TIMEOUT": "4s", "DB_MAX_CONNS": "8"}

// lockWallets holds the row lock of the wallets in a transaction of the
// owner until release, which also runs in Cleanup.
func lockWallets(t *testing.T, ids ...string) (release func()) {
	t.Helper()
	tx, err := cluster.Owner().Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = ANY($1) FOR UPDATE`, ids); err != nil {
		_ = tx.Rollback(context.WithoutCancel(t.Context()))
		t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// logIndex is the position of the first log line that has every part, or -1.
func logIndex(logs string, parts ...string) int {
	pos := 0
	for line := range strings.Lines(logs) {
		match := true
		for _, p := range parts {
			if !strings.Contains(line, p) {
				match = false
				break
			}
		}
		if match {
			return pos
		}
		pos += len(line)
	}
	return -1
}

// waitLogLine waits until the current run of the instance logs a line with every part.
func waitLogLine(t *testing.T, n *testkit.Instance, parts ...string) {
	t.Helper()
	testkit.Eventually(t, 5*time.Second, "log line with "+strings.Join(parts, " "), func(context.Context) (bool, error) {
		return logIndex(n.Logs(), parts...) >= 0, nil
	})
}

// Covers: SQS-09, FX-04, F4 (R03)
// Sensitivity: <preencher no Passo 4>
//
// The consumer, alone on instance 0, has one message of each of 3 wallets
// stuck on their locks and the rest received or queued when SIGTERM arrives.
// The instance stops receiving, waits for the 3 in flight (released by the
// test after the stop began), releases the rest and exits with 0 within
// SHUTDOWN_TIMEOUT, in the order HTTP → consumer → pool. The consumer of
// instance 1 then processes the other 27: every message exactly once.
func TestGracefulShutdownSQS(t *testing.T) {
	defer cluster.Restore(t)
	wallets := make([]testkit.Wallet, 3)
	ids := make([]string, 3)
	for i := range wallets {
		wallets[i] = cluster.OpenWallet(t, testkit.BRL("100.00"))
		ids[i] = wallets[i].ID
	}
	extra := maps.Clone(stoppedInstance)
	extra["SQS_PROCESSING_TIMEOUT"] = "4s"
	consumerOnlyOn0(t, extra)
	release := lockWallets(t, ids...)
	var msgs []string
	for _, w := range wallets {
		for range 10 {
			id := unique("msg")
			cluster.SendWager(t, sqsWager(t, id, wager(w, "provider-a", "BET", "1.00", unique("bet"), "")), testkit.SendOpts{GroupID: w.ID})
			msgs = append(msgs, id)
		}
	}
	waitLockWaiters(t, 3)

	start := time.Now()
	stopped := cluster.StopAsync(0)
	waitLogLine(t, cluster.Instance(0), `"msg":"sqs consumer stopping"`)
	release()
	if err := <-stopped; err != nil {
		t.Fatalf("instance 0: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("instance 0 stopped in %v, want less than SHUTDOWN_TIMEOUT (5s)", elapsed)
	}
	logs := cluster.Instance(0).Logs()
	httpStop := logIndex(logs, `"msg":"http server stopping"`, `"server":"api"`)
	consumerStop := logIndex(logs, `"msg":"sqs consumer stopped"`)
	poolClosed := logIndex(logs, `"msg":"postgres pool closed"`)
	if httpStop < 0 || consumerStop <= httpStop || poolClosed <= consumerStop {
		t.Fatalf("stop order HTTP %d → consumer %d → pool %d, want increasing:\n%s", httpStop, consumerStop, poolClosed, logs)
	}
	if got := inboxOutcomes(t, msgs...); len(got) != 1 || got["PROCESSED"] != 3 {
		t.Fatalf("inbox when instance 0 is gone = %v, want the 3 in flight PROCESSED and nothing else", got)
	}

	cluster.Restart(t, 1) // the consumer again, on another instance
	testkit.Eventually(t, 30*time.Second, "every message processed", func(context.Context) (bool, error) {
		return inboxOutcomes(t, msgs...)["PROCESSED"] == len(msgs), nil
	})
	if got := inboxOutcomes(t, msgs...); len(got) != 1 {
		t.Fatalf("inbox outcomes = %v, want only PROCESSED", got)
	}
	cluster.AssertQueueDrained(t)
	if n := cluster.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ depth = %d, want 0", n)
	}
	for _, w := range wallets {
		if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("90.00") || got.Version != 11 {
			t.Fatalf("wallet %s = %+v, want 10 debits", w.ID, got)
		}
	}
}
```

Import novo: `maps`. Os 30 s do `Eventually` cobrem o long poll órfão (messaging §4.5): uma mensagem liberada pode ficar invisível por um visibility timeout de 5 s.

- [ ] **Passo 2: rodar**

`go test -tags=e2e -race -p 1 -count=1 -run '^TestGracefulShutdownSQS$' ./test/e2e/...`. Esperado: PASS, porque o comportamento existe desde o M5. Se falhar, investigar antes de ajustar o teste.

- [ ] **Passo 3: sensibilidade**
1. **`Consumer.Stop` chamando `c.abortWork()` logo depois de `c.stopPolling()`:** esperado FAIL em `inbox when instance 0 is gone = map[], want the 3 in flight` (as transações das 3 foram desfeitas).
2. **`releaseAll` apagando em vez de liberar** (em `consumer.go`, `c.delete(ctx, msg, …)` no lugar do `changeVisibility`): esperado FAIL em `every message processed` (as mensagens liberadas no shutdown somem).

- [ ] **Passo 4: registrar**

```go
// Sensitivity: Stop canceling the work at once → the inbox is empty when instance 0 is gone; releaseAll
// deleting instead of releasing → the messages received and not started are lost.
```

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 6: R04 (`TestGracefulShutdownHTTP`)

**Arquivos:** modificar `test/e2e/resilience_test.go`.

**Interfaces consumidas:** `stoppedInstance`, `lockWallets`, `waitLockWaiters`, `waitLogLine`, `StopAsync`, `CloseIdleConnections`.

- [ ] **Passo 1: escrever o R04**

```go
// Covers: FX-04 (R04)
// Sensitivity: <preencher no Passo 3>
//
// Five BETs to instance 0 wait on the lock of their wallet when SIGTERM
// arrives. Once the API server is stopping, a new connection is refused and
// its operation never exists; the five, released by the test, answer 200;
// the instance exits with 0 within SHUTDOWN_TIMEOUT.
func TestGracefulShutdownHTTP(t *testing.T) {
	defer cluster.Restore(t)
	w := cluster.OpenWallet(t, testkit.BRL("100.00"))
	cluster.Restart(t, 0, stoppedInstance)
	c := cluster.Instance(0).Client(t, "provider-a")
	release := lockWallets(t, w.ID)
	type answer struct {
		resp *testkit.Response
		err  error
	}
	answers := make(chan answer, 5)
	for range 5 {
		bet := wager(w, "provider-a", "BET", "1.00", unique("bet"), "")
		go func() {
			resp, err := c.Try(t, wagerRequest(bet))
			answers <- answer{resp, err}
		}()
	}
	waitLockWaiters(t, 5)

	start := time.Now()
	stopped := cluster.StopAsync(0)
	waitLogLine(t, cluster.Instance(0), `"msg":"http server stopping"`, `"server":"api"`)
	cluster.CloseIdleConnections()
	late := wager(w, "provider-a", "BET", "1.00", unique("late"), "")
	if resp, err := c.Try(t, wagerRequest(late)); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("new request while stopping = %v %v, want connection refused", resp, err)
	}
	release()
	for range 5 {
		a := <-answers
		if a.err != nil || a.resp.Status != http.StatusOK {
			t.Fatalf("request in flight = %v %v, want 200", a.resp, a.err)
		}
	}
	if err := <-stopped; err != nil {
		t.Fatalf("instance 0: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("instance 0 stopped in %v, want less than SHUTDOWN_TIMEOUT (5s)", elapsed)
	}
	if got := balanceOf(t, w.ID); got.Balance != testkit.BRL("95.00") || got.Version != 6 {
		t.Fatalf("wallet = %+v, want the 5 debits in flight", got)
	}
	resp := cluster.Client(t, "provider-a").Do(t, testkit.Request{Method: http.MethodGet,
		Path: "/providers/provider-a/wagering/transactions/" + late.ExternalTransactionID})
	if resp.Status != http.StatusNotFound {
		t.Fatalf("the refused operation = %d, want 404: it was never recorded", resp.Status)
	}
}
```

Imports novos: `errors`, `syscall`.

- [ ] **Passo 2: rodar**

`go test -tags=e2e -race -p 1 -count=1 -run '^TestGracefulShutdownHTTP$' ./test/e2e/...`. Esperado: PASS (comportamento do M0/M7). Se o `Try` voltar com outro erro que não `ECONNREFUSED` (reuso de conexão), investigar o transporte antes de afrouxar a asserção.

- [ ] **Passo 3: sensibilidade**

Em `internal/observability/httpserver.go`, `srv.Close()` no lugar de `srv.Shutdown(ctx)`. Esperado: FAIL em `request in flight = <nil> … want 200`.

Registrar:

```go
// Sensitivity: srv.Close instead of srv.Shutdown → the requests in flight end without an answer.
```

- [ ] **Checkpoint:** `make check` verde.

---

### Tarefa 7: documentação e verificação do marco

**Arquivos:**
- modificar `ARCHITECTURE.md`, `docs/delivery-requirements.md`, `docs/implementation-plan.md`, `docs/test-plan.md`, `docs/structure.md` e `docs/dev/diary.md`;
- trocar o `Status` da spec, se houver ajuste da execução.

- [ ] **Passo 1: documentos**

- **`ARCHITECTURE.md`:**
  - §12: shutdown provado com trabalho em voo (R03, R04);
  - §13.3/§14: o 503 por prazo (`HTTP_REQUEST_TIMEOUT`);
  - §16: a pausa por instância e a regra do `maxReceiveCount`;
  - §17: o estado dos marcos atualizado até o M9.
- **`delivery-requirements.md`:**
  - FX-04 `[x]` (R03 `TestGracefulShutdownSQS`, R04 `TestGracefulShutdownHTTP`);
  - anotações do M9 em SQS-07 (R01), SQS-09 (R03), OUT-04 e HTTP-08 (R02, R01);
  - F6 com os testes R01/R02 na tabela §0.1 (já citados).
- **`test-plan.md`:** §5.1 ganha a linha U30 (`TestEdgeRequestDeadline` e os dois casos novos de `TestValidate_RejectsInvalidValues`); §5.4 ganha `TestClusterStopAsync` no parágrafo "O harness em si"; §5.5 recebe os ajustes da execução, se houver.
- **`structure.md`:** `test/testkit/compose.go` e `test/e2e/resilience_test.go` na árvore.
- **`implementation-plan.md`:** M9 ✅ com o "entregue também" (prazo HTTP, `Pause`, `StopAsync`) e os achados; risco novo "pausa por instância" tratado.
- **`dev/diary.md`:** entrada do M9 e o "Onde paramos" apontando para o M10.

- [ ] **Passo 2: verificação** (`superpowers:verification-before-completion`)

Rodar e colar a saída no chat:
- `make check`;
- `make test-integration`;
- `make test-e2e`.

Todos verdes. Se um teste R falhar de forma intermitente, investigar (`superpowers:systematic-debugging`) e registrar como achado, sem só repetir a execução.

- [ ] **Passo 3: proposta de commits** (sem executar)

1. `test(testkit): stop an instance asynchronously and read its readiness`;
2. `test(testkit): pause compose services for the outage tests`, com o `Makefile`;
3. `feat(httpapi): bound authenticated requests by HTTP_REQUEST_TIMEOUT`, com a config e o `testkit`;
4. `test(e2e): cover the outages and the graceful shutdown (R01 to R04)`;
5. `docs: record the m9 decisions and close the milestone`, com a spec e o plano.
