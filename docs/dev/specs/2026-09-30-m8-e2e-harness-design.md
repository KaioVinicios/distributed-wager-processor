# M8 — Harness e2e e cenários multi-instância: design

**Data:** 30/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor em 30/09/2026 ("prossiga pra o plano de implementação")

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M8;
- D-19 (injeção de falhas por build tag) e D-15 (papéis habilitáveis, usados aqui como bisturi);
- [`test-plan.md`](../../test-plan.md) §3.4 (cluster e2e), §4 (pontos de falha), §5.4 (testes C) e §6 (verificação de consistência, que passa a valer com 3 processos);
- [`delivery-requirements.md`](../../delivery-requirements.md): CONC-04, CONC-06 e TST-C01..C12.

**Ajustes feitos no plano e na execução** (valem sobre o texto desta spec): (1) `RestartAll` virou `Restore`, que reinicia só as instâncias fora do estado base e termina com `AssertAllReady`; (2) `Instance.Metric` saiu, fica `Instance.MetricValue`; (3) o `Close` e o `Stop` recusam saída diferente de 0 e qualquer data race nos logs dos filhos (TST-C12); (4) a lógica do `faultinject` (`parse`, `trigger`) fica no arquivo sem tag, testada sem tag; (5) o ambiente dos filhos é montado do zero (`HOME` no diretório do cluster); (6) round-robin com contador `int` sob mutex; (7) no C06 o segundo publisher liga depois do crash, e C08a/C08b terminam a pendência por expiração; (8) sabotagens diferentes das da tabela §6 para C01b, C05c, C06, C07a, C08b e C10a; (9) nomes `TestClusterSpreadsRequests`, `TestClusterInstanceLifecycle` e U27–U29. **Da execução:** (10) o C10b passou a usar uma barreira no banco (`raceBehindLock`) e a rodar sem `t.Parallel()`: com a barreira no envio os canais não se encontravam e o teste passava sem o `FOR UPDATE`; (11) a sabotagem do C03b é `LOCK TABLE … EXCLUSIVE` (`SHARE ROW EXCLUSIVE` não conflita com o `ROW SHARE` de um `FOR UPDATE`); (12) `transactionOf` entrou na Tarefa 9, seu primeiro uso; (13) só o `exec` do binário precisa de `//nolint:gosec`.

Esta spec registra só o **delta** em relação a `docs/`. O `testkit` já tem banco e filas isolados, tokens reais e forjados, validação de contrato em toda troca HTTP e em toda mensagem de auditoria, `AssertWalletConsistent` com os 8 itens do §6, `Audit`, helpers de SQS e `Eventually`. O C01a e o C02 já rodam **em processo** desde o M3.

**Com este marco todos os eliminatórios estão cobertos** (E7 pelo cluster de 3 processos; E4, E5 e E6 ganham a prova multi-instância).

---

## 1. Objetivo e critério de pronto

**Objetivo:** provar com **3 processos independentes** do binário, cada um com seu pool e sua memória, que as garantias não dependem de instância única, e que um encerramento abrupto em cada ponto crítico não perde nem duplica movimento financeiro. Entregáveis:

1. `internal/faultinject` com os 6 pontos de `test-plan.md` §4, ativos só com a build tag `faultinject`;
2. `testkit.Cluster`: build, start, kill, stop, restart, cliente por instância, logs e código de saída;
3. `test/e2e` com os 16 testes nomeados da §5.4;
4. `make test-e2e` e o job `e2e` no CI.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):

1. `make check` verde.
2. `make test-e2e` verde, com cada teste visto falhando pelo motivo certo ou pela sabotagem da §6, e `// Sensitivity: …` registrado em cada um.
3. `make test-integration` verde: a extração do `Harness` mexe no `testkit` que os 15 arquivos de `test/integration` usam.
4. Os requisitos da §8 marcados em `delivery-requirements.md`, cada um citando o teste.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Fora do escopo:** os testes R01–R04 e o helper `docker compose pause|unpause` (M9); o teste de carga (M12); qualquer mudança de comportamento do produto — o M8 só observa o que já existe, exceto os 6 pontos de falha, que são inertes sem a tag.

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Extrair `testkit.Harness`** com a infraestrutura compartilhada (banco, filas, tópico, contrato, `Audit`) e os helpers que hoje pendem de `*App` (`Client`, `OpenWallet`, `AssertWalletConsistent`, `SendWager`, `DLQDepth`, `Owner`). `App` (em processo) e `Cluster` (N processos) embutem `*Harness` | Sem isso, o e2e duplicaria a validação de contrato (D-20) e os 8 itens do §6, que o TST-C09 exige no `Cleanup` de todo cenário. Com embedding, nenhum dos 15 arquivos de `test/integration` muda: `server.Client(…)`, `server.Audit` e `server.Owner()` continuam resolvendo. Escolha do autor |
| 2 | **Um cluster por pacote, no `TestMain`.** Os 7 testes que matam uma instância **não** chamam `t.Parallel()`; os 9 de vazão chamam. O escalonador do Go roda os sequenciais até o fim antes de retomar os paralelos, então um cenário de crash nunca se sobrepõe a outro teste | 1 build e 3 starts por pacote, dentro do timeout de 15 min do §2 do test-plan. Cada teste de crash restaura num `defer` a instância que tocou e confere a prontidão das 3 no fim. Escolha do autor |
| 3 | **O round-robin só sorteia alvos vivos.** Cada processo já precisa de um watcher (o `faultinject` mata o processo por conta própria, e o harness tem de capturar o código 137 e a linha `FAULT_HIT`); o mesmo watcher zera o `live` do alvo. `Kill` e `Stop` marcam na hora. Sem nenhum vivo, o helper falha com mensagem clara | `AssertWalletConsistent` roda no `Cleanup` e faz um `POST` de reconciliação: com uma instância morta, um sorteio cego falharia por `connection refused` em vez do que o teste provava. Nenhum teste precisa de cuidado extra. Escolha do autor |
| 4 | **O ponto `consumer.before_commit` fica no caminho de commit do `uow.Do`** (`adapters/postgres/uow.go`, antes do `tx.Commit`), e **não** no `app`. O `Do` e o `Snapshot` compartilham o `run`, então o ponto vale só no caminho de escrita: o booleano `setLockTimeout`, que já distingue os dois, passa a se chamar `write` e guarda também o ponto | É literalmente o "local exato" que o test-plan §4 descreve ("dentro de `uow.Do`, depois de todas as escritas e antes do `COMMIT`"), e mantém intacta a regra de dependência do `structure.md` §2: só `adapters/*` importa `faultinject`; o `app` continua com `domain/*`, `apperrors` e stdlib. Uma reconciliação (`Snapshot`) não dispara o ponto, o que importa porque ela é justamente o que o `Cleanup` de toda carteira faz. Consequência: o ponto é genérico a toda escrita, então a instância armada morre no primeiro commit de qualquer coisa — resolvido pelas decisões 5 e 15 |
| 5 | **Papéis como bisturi nos testes de crash.** Com 3 consumidores, 3 publishers e 3 workers iguais, quem pega a mensagem, o evento ou a pendência é sorteio. O teste usa `Restart(i, EnvOf(roles))` para deixar **só** a instância com a falha armada com o componente ligado, deixa morrer, religa o componente em outra instância e restaura tudo num `defer` | Torna determinístico o que seria sorte. É exatamente o uso que o test-plan §3.4 previu para o `Restart(i, env)` ("por exemplo com ou sem ponto de falha") e o que as flags do M7 (D-15) habilitam. Sem isso, C05a, C05b, C06a/b e C08b passariam por acaso ou falhariam por acaso |
| 6 | **O ambiente dos processos filhos é derivado da própria `config.Config`**, por reflexão sobre as tags `env:` (`testkit.EnvOf`, §4.4, com teste unitário). O mesmo helper serve para `config.Roles`, que tem as suas quatro tags | Garante que o cluster nunca divirja dos tempos de `test-plan.md` §3.3 nem dos endereços que o `StartApp` já usa. Escrever as ~35 variáveis à mão seria uma segunda fonte da verdade, que silenciosamente envelhece |
| 7 | **A configuração base do `StartApp` é extraída** para um helper que o `StartCluster` também usa (filas e tópico isolados, OIDC do Keycloak do compose com `OIDC_CLOCK_SKEW=1s`, docs ligados, agenda de referências), e sobre ela o `StartCluster` aplica a **coluna E2E** de §3.3: `SQS_WAIT_TIME=2s`, `SQS_RETRY_MAX_DELAY=2s`, `OUTBOX_LEASE=3s`, `OUTBOX_POLL_INTERVAL=200ms`, `OUTBOX_RETRY_BASE_DELAY=200ms`, `OUTBOX_RETRY_MAX_DELAY=2s`, `REFERENCE_RETRY_BASE_DELAY=200ms`, `REFERENCE_POLL_INTERVAL=100ms`, `REFERENCE_MAX_ATTEMPTS=3`, `REFERENCE_TTL=5s` | O `Env.Config()` sozinho não é uma configuração válida: quem monta o app preenche OIDC, nomes de fila, tópico e agenda de referências, hoje só dentro do `StartApp`. Sem extrair, o cluster repetiria esses campos e um deles esquecido viraria zero value recusado pelo `Validate` — ou, pior, um tempo diferente do da integração. O lease de 3 s é o que o C06 mede: é o prazo em que outra instância reassume o evento de um publisher morto |
| 8 | **`Client.Try(tb, Request) (*Response, error)`**, ao lado do `Do`, que continua abortando no erro de transporte | O C05c precisa afirmar que a resposta **não** chega (o processo morre entre o commit e o `writeJSON`). O `Do` de hoje faria `tb.Fatalf` justamente no comportamento esperado. O M9 reusa no R04 |
| 9 | **Toda instância do cluster mantém `HTTP_ENABLED=true`**, e a prontidão do `Start`/`Restart` é sempre `GET /health/ready` até 200 | A D-15 registra que, com o HTTP desligado, não há rotas de health (elas vivem no `httpapi`), então uma instância sem HTTP exigiria uma segunda forma de prontidão. Nenhum cenário da §5.4 precisa desligar o HTTP: o bisturi da decisão 5 só mexe em consumidor, publisher e worker |
| 10 | **Logs dos filhos em memória e em arquivo.** Cada processo tem um buffer (para `Instance.Logs()` e `AssertFaultHit`) e um arquivo num diretório temporário do pacote, removido no `Close` e preservado com `PDA_TEST_KEEP=1`. `Cluster.AttachLogs(tb)` despeja os logs das instâncias na saída quando o teste falha | O §3.4 pede os logs anexados à saída na falha. O `TestMain` não tem `testing.TB`, então o diretório é do pacote, não `t.TempDir()` |
| 11 | **`Close()` do cluster roda antes do `cleanup()` do banco**, garantido pela ordem dos `defer` no `TestMain` (LIFO), e é idempotente | Desde 30/09 o `DROP DATABASE` é feito **sem `FORCE`** e o `cleanup` devolve o erro ([spec](2026-09-30-test-db-drop-design.md)): um processo filho ainda conectado como `pda_app` faria o `DROP` falhar com `55006` e reprovar o pacote. O `DROP` espera até 5 s pelos backends que estão saindo, o que cobre a cauda de um `SIGKILL` |
| 12 | **O binário é compilado uma vez**, no `StartCluster`: `go build -tags faultinject -race -o <tmp>/pda ./cmd/pda` | §3.4. Como o `StartCluster` é chamado uma vez pelo `TestMain` (decisão 2), não há necessidade de cache entre chamadas |
| 13 | **Os helpers de corpo e submissão (`wager`, `submit`, `result`, `wantResult`, `waitStatus`, `balanceOf`, `unique`) são duplicados em `test/e2e/helpers_test.go`**, em vez de promovidos ao `testkit` | São ~40 linhas de teste. Promovê-los mudaria as chamadas em 8 arquivos de integração já verdes, sem ganho para este marco. Registrado como duplicação conhecida; se crescer, vira um `Client.Submit` no `testkit` |
| 14 | **Um ponto de falha dispara sempre na primeira passagem** e mata o processo; não há contador nem filtro por operação | O processo morre, então "uma vez" e "sempre" coincidem. Em consequência, um ponto armado pode disparar num evento de preparação (por exemplo, o `WalletBalanceChanged` da abertura) em vez do evento que o teste acabou de criar. As asserções dos testes C06 são sobre o conjunto ("todo `eventId` chega à auditoria pelo menos uma vez, nada fica pendente"), então isso não as invalida; onde a identidade do item importa, a decisão 5 o isola |
| 15 | **Uma instância com falha armada sai do round-robin.** O `Restart` com `Fault(…)` marca o alvo como armado, e o `nextLive` da decisão 3 passa a exigir vivo **e** desarmado; `Instance(i)` continua alcançando qualquer um. Um `Restart` sem `PDA_FAULT` desarma | Achado da autorrevisão desta spec. Com o ponto da decisão 4 no commit de toda escrita e o HTTP sempre ligado (decisão 9), um `POST` que o round-robin mandasse para a instância armada a mataria antes do cenário, e o `FAULT_HIT` viria do item errado. Uma regra de disciplina ("não escreva na instância armada") produziria um flake silencioso; a exclusão é local ao harness e vale para todos os pontos |

---

## 3. `internal/faultinject`

Pacote folha, só stdlib. Fora do lint (o `.golangci.yml` não habilita a tag `faultinject`), coberto por `go vet -tags=faultinject`, que já está no `make vet`.

```go
// faultinject.go — sem build tag
// Point aborts the process when name is enabled, to simulate a crash at that
// exact place (test-plan §4). It is a no-op unless the binary is built with
// -tags faultinject, so the production binary and the Docker image carry no
// fault point at all.
func Point(name string) { hit(name) }

// faultinject_off.go  //go:build !faultinject
func hit(string) {}

// faultinject_on.go   //go:build faultinject
// enabled is read once: PDA_FAULT=<point>[,<point>].
var enabled = parse(os.Getenv("PDA_FAULT"))

func hit(name string) {
    if !enabled[name] {
        return
    }
    fmt.Fprintf(os.Stderr, "FAULT_HIT %s\n", name)
    os.Exit(137)
}
```

Os 6 pontos e o local de cada um:

| Ponto | Arquivo e local | Simula |
| --- | --- | --- |
| `consumer.before_commit` | `adapters/postgres/uow.go`, em `Do`, depois de `fn` devolver `nil` e antes do `tx.Commit` (decisão 4) | Crash antes do commit |
| `consumer.after_commit_before_delete` | `adapters/sqsconsumer/consumer.go`, em `apply`, no `case actDelete` (o comentário já está reservado no arquivo) | Crash entre o commit e a remoção |
| `http.after_commit_before_response` | `adapters/httpapi/wagering_handler.go`, em `submitWager`, depois do `Execute` e antes do `writeJSON` | O cliente não recebe a resposta e reenvia |
| `outbox.after_claim_before_publish` | `adapters/outbox/publisher.go`, em `publish`, antes do `sink.Publish` | Crash com evento reservado |
| `outbox.after_publish_before_ack` | `adapters/outbox/publisher.go`, em `publish`, entre o `Publish` bem-sucedido e o `MarkPublished` | Republicação |
| `references.after_claim` | `adapters/references/worker.go`, em `resolve`, antes do `resolver.Resolve` | Crash do worker com a pendência reservada |

Todos os locais são adaptadores, então nenhuma regra de dependência do `structure.md` §2 muda.

---

## 4. `testkit`

### 4.1 `harness.go` (novo)

```go
// Harness is the infrastructure a test package shares: the isolated database,
// queues and events topic, the OpenAPI contract, and one or more instances of
// the application to call. App has one (in process); Cluster has N processes.
type Harness struct {
    Audit                 *Audit
    WagerQueueURL, DLQURL string

    env      *Env
    sqs      *sqs.Client
    http     *http.Client
    contract *Contract
    targets  []*target
    next     atomic.Uint64
}

// target is one instance to call. live is false once its process has exited;
// armed is true while it runs with a fault point, and then it is left out of
// the round-robin (decisões 3 e 15).
type target struct {
    baseURL, metricsURL string
    live, armed         atomic.Bool
}

func (h *Harness) Client(tb testing.TB, clientID string) *Client   // round-robin
func (h *Harness) ClientWithToken(raw string) *Client
func (h *Harness) OpenWallet(tb testing.TB, initial Money) Wallet  // + Cleanup consistente
func (h *Harness) AssertWalletConsistent(tb testing.TB, walletID string)
func (h *Harness) SendWager(tb testing.TB, body string, o SendOpts) string
func (h *Harness) AssertQueueDrained(tb testing.TB)
func (h *Harness) DLQDepth(tb testing.TB) int
func (h *Harness) Owner() *pgxpool.Pool
func (h *Harness) nextLive(tb testing.TB) *target                  // decisões 3 e 15
```

O `Client` passa a guardar `h *Harness` e um `t *target` opcional (`nil` = round-robin por requisição), e ganha o `Try` da decisão 8. Nada mais na assinatura pública muda: `Do`, `Request`, `Response`, `JSON` e `Problem` ficam como estão.

### 4.2 `app.go` (em processo)

```go
type App struct {
    *Harness
    fx   *fx.App
    logs *syncBuffer
}

func (e *Env) StartApp(ctx context.Context, opts ...func(*config.Config)) (*App, func(), error)
func (a *App) Logs() string
func (a *App) Metric(tb testing.TB, name string) string
func (a *App) MetricValue(tb testing.TB, sample string) int64
```

`StartApp` mantém a assinatura e o comportamento; muda só por dentro, montando um `Harness` de um alvo. Os campos `BaseURL`/`MetricsURL` deixam de ser públicos (nenhum teste fora do `testkit` os usa). A montagem que hoje vive dentro dele — filas e tópico isolados, `Audit`, contrato e a configuração base da decisão 7 — é extraída para um helper que o `StartCluster` também usa, de modo que só a forma de subir a aplicação (Fx em processo × processos filhos) difere entre os dois.

### 4.3 `cluster.go` (novo)

```go
// Cluster is N processes of the binary over the shared database, queues and
// topic of the Harness (test-plan §3.4). Every instance keeps HTTP on, so
// readiness is always GET /health/ready.
type Cluster struct {
    *Harness
    bin   string
    dir   string            // logs of the processes
    base  map[string]string // environment of every instance
    procs []*proc
}

// StartCluster builds the binary once with -tags faultinject -race, starts n
// processes on free ports and waits for each to be ready. stop stops them all
// and must run before the database cleanup (decisão 11).
func (e *Env) StartCluster(ctx context.Context, n int, opts ...func(*config.Config)) (c *Cluster, stop func() error, err error)

func (c *Cluster) Instance(i int) *Instance
func (c *Cluster) Kill(tb testing.TB, i int)                             // SIGKILL
func (c *Cluster) Stop(tb testing.TB, i int)                             // SIGTERM + espera
func (c *Cluster) Restart(tb testing.TB, i int, env ...map[string]string) // para (se vivo) e sobe com o ambiente
func (c *Cluster) RestartAll(tb testing.TB)                              // C08a
func (c *Cluster) AssertAllReady(tb testing.TB)                          // fim de cada teste de crash
func (c *Cluster) AttachLogs(tb testing.TB)                              // decisão 10
func (c *Cluster) Close() error                                          // idempotente

// Fault builds the PDA_FAULT override of Restart; the role overrides come
// from EnvOf(config.Roles{…}).
func Fault(points ...string) map[string]string // PDA_FAULT=a,b

type Instance struct{ /* c *Cluster; i int */ }

func (n *Instance) Client(tb testing.TB, clientID string) *Client // alvo fixo
func (n *Instance) Logs() string
func (n *Instance) Metric(tb testing.TB, name string) string
func (n *Instance) WaitExit(tb testing.TB) int                    // espera a saída
func (n *Instance) AssertFaultHit(tb testing.TB, point string)    // FAULT_HIT + exit 137
```

`proc` guarda o `*exec.Cmd`, o buffer de log, o arquivo e um canal fechado pelo watcher com o código de saída. O watcher é o único a escrever `target.live`; o `Restart` é o único a escrever `target.armed`, conforme o ambiente traga ou não `PDA_FAULT` (decisão 15).

`AssertFaultHit` é a confirmação que o test-plan §4 exige: sem a linha `FAULT_HIT <ponto>` no log **e** sem o código 137, o teste falha, para não passar por acaso.

### 4.4 `configenv.go` (novo)

`EnvOf(v any) map[string]string` percorre os campos de um struct por reflexão, lê a tag `env:` e formata o valor (`time.Duration` → `String()`, `bool` → `FormatBool`, inteiros → `FormatInt`, `string` → como está). Serve para `config.Config` e para `config.Roles`, que é o outro struct com essas tags. Um teste unitário afirma que **toda** variável da `Config` e da `Roles` aparece no mapa e confere o formato de um caso por tipo, de modo que um campo novo não fique de fora em silêncio.

Ao ambiente derivado da `Config` o `StartCluster` soma o que não vem dela: `AWS_ENDPOINT_URL`, `AWS_REGION`, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` (a chave raiz do MiniStack, como o `StartApp` já usa) e as quatro variáveis de papel, todas ligadas por padrão.

---

## 5. `test/e2e`

### 5.1 `main_test.go`

```go
//go:build e2e

var cluster *testkit.Cluster

func run(m *testing.M) (code int) {
    env, cleanup, err := testkit.NewEnv(ctx, "e2e")      // defer cleanup()  → roda por último
    c, stop, err := env.StartCluster(ctx, 3)             // defer stop()     → roda primeiro
    cluster = c
    return m.Run()
}
```

Os dois `defer` levam o código de saída a 1 quando falham, como o `test/integration/main_test.go` já faz. A ordem LIFO é o que garante a decisão 11.

### 5.2 Distribuição e ordem

Os 9 testes de vazão chamam `t.Parallel()` e usam `cluster.Client(t, …)` (round-robin nas 3 instâncias, §5.4). Os 7 de crash não chamam `t.Parallel()`, usam `Instance(i)` onde a identidade importa, restauram num `defer` e terminam com `AssertAllReady`. Neles, o `cluster.Client` continua utilizável para o que não é do cenário: a instância armada está fora do sorteio (decisão 15).

### 5.3 Padrão dos testes de crash

```
1. preparar o estado pelas instâncias vivas (abrir carteira, criar a operação);
2. Restart(0, Fault(<ponto>), EnvOf(só o componente em teste ligado));
   Restart(1, EnvOf(sem o componente)); Restart(2, EnvOf(sem o componente));
3. provocar (mensagem, evento pendente, requisição);
4. Instance(0).WaitExit == 137 e AssertFaultHit(<ponto>);
5. Restart(1) limpo → outra instância retoma;
6. afirmar o resultado (um único lançamento, replay com o original, auditoria completa);
7. defer: Restart(0), Restart(2), AssertAllReady.
```

---

## 6. Testes

Arquivos: `idempotency_test.go`, `concurrency_test.go`, `crash_test.go`, `outbox_test.go`, `references_test.go`, `restart_test.go`, `channels_test.go`, `helpers_test.go`, `main_test.go`.

| ID | Teste | Procedimento e asserção (delta sobre o §5.4) | Sabotagem da sensibilidade |
| --- | --- | --- | --- |
| C01a | `TestSameBet50xHTTP` | Como o de M3, com as 50 goroutines distribuídas nas 3 instâncias | A busca de idempotência devolvendo nada → as duplicatas esgotam as retentativas e respondem 503 |
| C01b | `TestSameBet50xSQS` | 50 mensagens, `messageId` distintos, mesma `idempotencyKey`, `MessageDeduplicationId` distintos (TST-C11). 1 débito; inbox com 1 `PROCESSED` + 49 `IDEMPOTENT_REPLAY` | Não gravar a inbox no caminho de replay → contagem da inbox errada |
| C02 | `TestTwoBetsCompete` | 100.00 vs 2× 80.00, uma em cada instância, 20 repetições com carteiras novas | `FOR UPDATE` removido do lock da carteira |
| C03a | `TestWalletsInParallel` | 20 carteiras × 10 BETs simultâneos, distribuídos; tudo processado e consistente | — (teste de vazão; a consistência é o §6) |
| C03b | `TestNoGlobalLock` | O teste trava a carteira X com `FOR UPDATE` numa transação do `Owner()`; um BET em Y conclui em < 1 s, e um BET em X fica bloqueado até a liberação (ou 503 por lock timeout) | Trocar o lock de linha por um `LOCK TABLE wallets` → o BET em Y também bloqueia |
| C05a | `TestCrashAfterCommitBeforeDelete` | Consumidor só na instância 0, com o ponto; a mensagem é comitada e o processo morre antes do `DeleteMessage`. Depois do visibility timeout, a instância 1 recebe a reentrega e a trata como duplicata na inbox; 1 lançamento | Ignorar `PDA_FAULT` → sem `FAULT_HIT`; remover o `DeleteMessage` do caminho de duplicata → a fila não drena |
| C05b | `TestCrashBeforeCommit` | Idem com `consumer.before_commit`: nada persiste (nenhuma transação, nenhum lançamento, nenhuma linha de inbox); na reentrega, a instância 1 processa uma única vez | Mover o ponto para depois do `Commit` → a operação aparece persistida |
| C05c | `TestHTTPCrashAfterCommit` | `Instance(0).Client(…).Try(…)` não recebe resposta; o reenvio com a mesma chave em outra instância devolve o replay com o resultado original; 1 lançamento; os eventos da operação chegam à auditoria publicados por outra instância | O `result_balance_minor` não ser usado no replay → o saldo devolvido é o atual |
| C06a | `TestPublisherCrashAfterPublish` | Publisher desligado nas 3 enquanto os eventos acumulam; liga o 0 com `outbox.after_publish_before_ack`, que morre depois de publicar e antes de confirmar; liga o 1, que reassume depois do lease. Todo `eventId` na auditoria ao menos uma vez com o payload idêntico ao do banco (comparado como JSON), nada pendente | Confirmar o evento antes de publicar → um evento publicado zero vezes, ou o `MarkPublished` sem a condição do dono do lease |
| C06b | `TestPublisherCrashAfterClaim` | Idem com `outbox.after_claim_before_publish`: a instância 1 publica depois do lease; nada é perdido | Não liberar o lease no `Claim` de um lease vencido → o evento nunca é reassumido |
| C07a | `TestRefundBeforeBet` | REFUND (202) e depois BET (200), por HTTP e por SQS, em instâncias diferentes. O REFUND fica `PROCESSED`; saldo = inicial; 2 lançamentos; eventos `PendingReference` → `Processed` | Remover a antecipação (`AdvanceDependents`) → a pendência só resolve pelo backoff, e a espera de 2 s estoura |
| C07b | `TestRefundReferenceExpires` | REFUND sem BET: `REJECTED`/`REFERENCE_NOT_FOUND` ao esgotar tentativas ou TTL; evento `Rejected`; nenhum lançamento | Remover a checagem de TTL → nunca expira |
| C08a | `TestFullRestart` | Cria processados, rejeitados e uma pendência; `Kill` nas 3; `RestartAll`. Os replays devolvem os resultados originais; a pendência resolve ou expira; tudo consistente | Idempotência lida de um mapa em memória → o replay reprocessa (é a prova do E6) |
| C08b | `TestReferenceWorkerCrash` | Worker só na instância 0, com `references.after_claim`, que morre com a pendência reservada; worker de volta na 1, que a resolve | Remover o recheck de `next_attempt_at` sob os locks → a tentativa é contada duas vezes e a pendência expira antes |
| C10a | `TestHTTPThenSQSSameOperation` | A mesma operação por HTTP e depois pelo SQS, e o inverso, em instâncias diferentes. 1 lançamento; o segundo canal cai no replay (inbox `IDEMPOTENT_REPLAY`, HTTP `idempotentReplay: true`) | O hash do payload incluir a chave ou o `messageId` → os canais não se reconhecem |
| C10b | `TestHTTPAndSQSConcurrent` | A mesma operação por HTTP e SQS ao mesmo tempo; e, numa carteira de 100.00, 80.00 por HTTP e 80.00 por SQS. 1 débito em cada caso | `FOR UPDATE` removido → saldo negativo ou versão obsoleta |

C04, C09, C11 e C12 continuam sem teste próprio, como o §5.4 define: C04 é satisfeito por todos rodarem com 3 processos, C09 pelo `Cleanup` de toda carteira, C11 pelos `MessageDeduplicationId` distintos de C01b e C10, e C12 pelo `-race` em todos os níveis e no binário do cluster.

**Testes novos de unidade:** `TestConfigEnv` (§4.4) e `TestFaultPointIsNoOpWithoutTag` (o `Point` não faz nada no build padrão). Os pontos de falha em si nascem com red real: um teste do cluster arma o ponto e exige `FAULT_HIT` + 137, que falha enquanto o `Point` não estiver no lugar.

---

## 7. Bordas

- **`Makefile`:** alvo `test-e2e` (`infra-up` + `go test -tags=e2e -race -p 1 -count=1 -timeout 15m ./test/e2e/...`), incluído no `.PHONY`, como `stack.md` §5 já documenta. O `check` continua só unitário.
- **`.github/workflows/ci.yml`:** job `e2e` espelhando o `integration` (compose, logs na falha, `down -v` no fim), com `timeout-minutes: 25`. O cabeçalho do arquivo já anuncia esse job para o M8.
- **Lint:** `e2e` já está em `run.build-tags`, então `test/e2e` é lintado e precisa passar. `faultinject_on.go` fica fora do lint e é coberto pelo `go vet -tags=faultinject` do `make vet`.

---

## 8. Requisitos e documentos atingidos

- **Requisitos fechados:** CONC-04, CONC-06, IDEM-01, OUT-06, SQS-11, TST-C01..C12. Completam-se também TX-09 e as provas multi-instância de E4, E5, E6 e E7 na tabela §6 do `implementation-plan.md`.
- **Documentos:**
  - `test-plan.md`: a §4 ganha a nota da decisão 4 (o ponto do consumidor vive no commit do UoW e o cenário isola o papel) e a §3.4 ganha as decisões 5, 9 e 11; a §5.4 ganha os nomes de arquivo;
  - `decisions.md` D-19: a injeção de falhas dispara sempre e mata o processo (decisão 14), e os testes de crash isolam o componente por papel (decisão 5);
  - `structure.md`: `internal/faultinject/*`, `test/e2e/*` e os arquivos novos do `testkit` (`harness.go`, `cluster.go`, `configenv.go`);
  - `ARCHITECTURE.md`: a seção de testes e as limitações que o marco confirmar;
  - `delivery-requirements.md` e `dev/diary.md`: fecho do marco;
  - `implementation-plan.md`: o M8 marcado como concluído, com o "entregue também" e os achados.

---

## 9. Riscos

| Risco | Mitigação |
| --- | --- |
| 16 testes e2e com `-race` nos filhos estouram o timeout de 15 min | Cluster único (decisão 2). Se apertar, C10a/b são os últimos a entrar: o test-plan §8 prioriza os testes de eliminatório e depois C01–C08, e todo o resto deste marco sustenta eliminatório |
| `AuditTimeout` de 10 s apertado com lease de 3 s mais o reclaim depois de um crash | Medido no C06; se faltar, um prazo próprio para o e2e em vez de aumentar o de integração |
| Um processo vazado reprova o pacote (o `DROP` sem `FORCE` falha com `55006`) | Decisão 11: `Close()` idempotente no `defer` antes do `cleanup`, e a espera de 5 s do `DROP` cobre a cauda de um `SIGKILL` |
| Um teste de crash esquece de restaurar a instância e quebra os seguintes | `defer` com `Restart` e `AssertAllReady` no fim de cada teste de crash; os paralelos só começam depois de todos os sequenciais (decisão 2) |
| Um ponto armado dispara num item de preparação, não no do cenário | Decisão 14: as asserções de C06 são sobre o conjunto; onde a identidade importa, o papel isola o componente (decisão 5) e a instância armada fica fora do round-robin (decisão 15) |
| A extração do `Harness` quebra os testes de integração já verdes | O embedding preserva as chamadas (`server.Client`, `server.Audit`, `server.Owner()`, `server.Metric*`, `server.Logs`); `make test-integration` faz parte do "pronto quando" |
| O `Restart` sobe um processo com ambiente inválido e morre calado | Desde a revisão do M7 o `bootstrap` reporta o erro no stderr e sai com 1 (U24); a espera de prontidão falha com os logs do filho anexados (decisão 10) |
