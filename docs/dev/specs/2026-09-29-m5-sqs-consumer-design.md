# M5 — Consumidor SQS: design

**Data:** 29/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor em 29/09/2026; achados da validação do plano incorporados (§2.2)

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M5;
- D-12 (consumidor SQS) e a parte de inbox da D-08;
- [`messaging.md`](../../messaging.md) §3 (contrato de entrada), §4 (consumidor, pausa por saúde, DLQ e shutdown) e a parte de SQS do §8 (métricas);
- [`transaction-lifecycle.md`](../../transaction-lifecycle.md) §5.4 e §6.2;
- [`test-plan.md`](../../test-plan.md) §5: I04a–f e a parte SQS do I03b.

Esta spec registra só o **delta** em relação a `docs/`.

---

## 1. Objetivo e critério de pronto

**Objetivo:** toda mensagem de `wager-transactions.fifo` passa pelo mesmo caso de uso do HTTP, com as mesmas garantias:
- a inbox é gravada na mesma transação do domínio, e a mensagem só é removida depois do commit;
- erros permanentes e de entrada chegam à DLQ por envio explícito, e os transitórios esgotados chegam por redrive;
- uma queda do PostgreSQL não consome tentativas;
- o `SIGTERM` não perde mensagem.

Fecha o eliminatório **E5 (SQS)** e o AUTH-09.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde, com os testes da §7 vistos falhando pelo motivo certo e depois passando. O `TestConsumeWagerAtomicInbox` também passa pela checagem de sensibilidade.
3. `docker compose up --build --wait` sobe as 3 réplicas saudáveis. Uma mensagem enviada com as credenciais de `provider-a` (`.local/aws/credentials`) é processada. O evento chega a `wallet-events-audit.fifo`, e a fila de entrada e a DLQ ficam vazias.
4. Requisitos da §8 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Fora do escopo** (com o marco de destino):
- o ponto de falha `consumer.after_commit_before_delete` e o C05a (M8). O plano marca o local no código;
- a flag `CONSUMER_ENABLED` (M7, junto com as outras flags de papel da D-15);
- o label `wager_duplicates_total{channel="http"}` (M7, com o resto do catálogo);
- o worker de referências e a continuidade de `PENDING_REFERENCE` (M6; SQS-08 fecha lá);
- os cenários C01b, C10 e R01/R03 com 3 processos (M8 e M9).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Caso de uso novo `app.ConsumeWager`** (§3.2) orquestra inbox → validação → `ProcessWager` (escolha do autor em 29/09) | O lifecycle §6.2 consulta a inbox **antes** do `NewCommand`, então essa etapa não cabe no `ProcessWager.Execute`, que recebe um `Command`. Comparar o hash e repetir a corrida são regras, e ficam no `app` (`structure.md` §2). O `sqsconsumer` só cuida do transporte |
| 2 | **`ProcessRequest.Inbox *InboxReceipt`:** quando preenchido, o `ProcessWager` grava a inbox em **todo caminho de conclusão** (§3.1) | É o "O `ProcessRequest` ganha o registro da inbox, gravado na mesma `uow.Do`" do implementation-plan. O HTTP passa `nil` e não muda |
| 3 | **Corrida da inbox:** `ErrInboxDuplicate` no commit faz o `ConsumeWager` recomeçar do `Find` da inbox, com até 3 tentativas | A nova rodada encontra a linha e compara o hash. Assim, uma corrida entre conteúdos diferentes também vira `MESSAGE_HASH_MISMATCH`, e não só "duplicata" como dizia o lifecycle §6.2 |
| 4 | **Hash da mensagem calculado no adapter**, sobre o struct tipado (`{"data":{…},"type":…}`, chaves em ordem lexicográfica, como o `canonicalPayload` do domínio). Um campo ausente e um `null` geram o mesmo hash | O hash depende do formato do envelope, que é da borda. Duas mensagens que viram o mesmo `Input` têm o mesmo hash, e campos desconhecidos já são rejeitados antes |
| 5 | **Erro permanente sem `FAILED` gravado** (a linha já gravada com a mesma chave não pode ser lida): DLQ com `errorCode = INTERNAL_ERROR`, sem inbox | É o 500 `INTERNAL_ERROR` do HTTP (lifecycle §6.1). A chave está ocupada, então nem o `FAILED` pode ser gravado |
| 6 | **Pausa por saúde acionada por ping:** depois de qualquer erro transitório, o `healthGate` faz `Ping` no pool (timeout de 1 s). Se o ping falha, os pollers param de receber e pingam a cada 2 s até o banco voltar. Se passa, o erro era da mensagem e segue o backoff (escolha do autor em 29/09) | O `apperrors` tem um único `KindTransient`, que também cobre lock timeout, `40P01` e corridas. O ping mede o estado real do banco, sem classificação nova e sem depender de a tradução reconhecer todo erro de conexão. O `/health/ready` já reflete a queda pelo checker `postgres` |
| 7 | **`SQS_PROCESSING_TIMEOUT < SQS_VISIBILITY_TIMEOUT`**, validado pelo config (escolha do autor em 29/09) | Uma mensagem não pode continuar sendo processada depois de voltar a ficar visível para outra instância |
| 8 | **Liberação por prazo:** antes de começar cada mensagem, se resta menos visibility (desde o recebimento) que o `SQS_PROCESSING_TIMEOUT`, ela e as seguintes do grupo são liberadas com `ChangeMessageVisibility(0)`, sem processar (escolha do autor em 29/09) | Num grupo processado em sequência, a 5ª mensagem espera as anteriores com o visibility correndo. Sem essa regra, ela poderia ser processada depois de reentregue: a inbox protege a correção, mas a ordem do grupo se perderia e o `DeleteMessage` falharia. É mais simples que um *heartbeat* de visibility |
| 9 | **Tempos de teste:** visibility de 5 s e processamento de 3 s, na integração e no e2e | O test-plan §3.3 tinha visibility de 3 s na integração e `DB_LOCK_TIMEOUT` de 2 s, o que não deixava espaço para a decisão 7. O backoff por `ChangeMessageVisibility` substitui o visibility, então o I04d não muda |
| 10 | **Ação por mensagem como função pura** `decide(result, err) action` (`delete`, `dlq(code, category)`, `retry(delay)`, `release`), com teste de tabela | Concentra a tabela do lifecycle §6.2 em um ponto testável sem SQS |
| 11 | **`context.Canceled` no shutdown vira `release`**, nunca retry nem DLQ | Lifecycle §8: não é falha |
| 12 | **Envio para a DLQ que falha vira `retry`**; o `DeleteMessage` que falha depois do commit só gera log e a métrica nova `sqs_delete_errors_total` | messaging §4.2 e §4.4. A reentrega cai na inbox como duplicata. O messaging pedia "log e métrica", sem nome |
| 13 | **`correlationId`:** o message attribute, quando segue o padrão do HTTP (`^[A-Za-z0-9._-]{1,128}$`, D-18); caso contrário, o `messageId` | Mesma regra de aceitação dos dois canais. O `messageId` (1 a 128 caracteres) cabe no `Text` do `events.yaml` |
| 14 | **Decodificação em duas fases:** envelope com `data` como `json.RawMessage` → `type` → `data`, as duas com `DisallowUnknownFields` | Uma mensagem de outro tipo recebe `UNSUPPORTED_MESSAGE_TYPE`, e não `MALFORMED_MESSAGE` por causa dos campos de `data` |
| 15 | **Métricas de SQS** entram agora no `observability.Metrics`, que implementa a porta `sqsconsumer.Metrics`. `wager_duplicates_total` ganha só os labels `sqs/inbox` e `sqs/idempotency` (replay) | Mesmo critério do M4 (decisão 19 da spec do M4). O `http/idempotency` entra no M7 |
| 16 | **O `Pinger` do gate é o `*pgxpool.Pool`** fornecido pelo `postgres.Module`, visto pelo adapter através de uma interface mínima | O `sqsconsumer` não pode importar o adapter `postgres` (`structure.md` §2), e o pool é tipo de biblioteca |

### 2.2 Achados da validação do plano

O plano foi validado numa cópia descartável do repositório (um `git clone` local, sem worktree nem branch), com o código de todas as tarefas testado contra a infraestrutura do compose. Estes achados completam a §2:

| # | Decisão | Motivo |
| --- | --- | --- |
| 17 | **Achado central: um long polling cancelado pelo cliente continua aberto no broker** até o fim do seu `WaitTimeSeconds`, e pega a mensagem que ficar visível nesse intervalo, escondendo-a por um visibility timeout. No shutdown, uma mensagem liberada pode voltar só depois desse atraso: registrado como limitação no messaging §4.5. Os testes de shutdown usam short polling (`WaitTime = 0`) | O teste de cancelamento no prazo falhava sem logs e passava com eles, e com `WaitTime = 0` passou sempre. Uma sonda confirmou o mecanismo no MiniStack: long poll de 5 s cancelado em 300 ms, mensagem enviada sem nenhum outro receive aberto; em 3 de 3 execuções ela ficou invisível e o receive seguinte veio vazio. Com outro receive já esperando, ele leva a mensagem, por isso o efeito é intermitente. Não há perda nem duplicidade: a mensagem reaparece |
| 18 | **O mesmo mecanismo era a causa do flake preexistente do I05b** (`TestNoPublishBeforeCommit`, M4): o `Audit.Absent` cancelava o long poll no fim da janela, e o poll órfão pegava o evento publicado logo depois do commit. O `Absent` passa a nunca cancelar um receive no meio | A base (`main`) também falhava, 1 vez em 4 execuções completas. O `TestAuditAbsentLeavesNoPollBehind` (novo) reproduz o problema de forma determinística: com o `Absent` do M4, falhou em 3 de 3 execuções; com a correção, passou em 3 de 3. Depois dela, 5 execuções completas passaram |
| 19 | **Short polling com pausa:** com `SQS_WAIT_TIME = 0`, um receive vazio espera 100 ms antes do próximo | Sem ela, a fila vazia seria consultada num laço sem espera. O config aceita 0 s |
| 20 | **`SQS_WAIT_TIME` e `SQS_VISIBILITY_TIMEOUT` em segundos inteiros**, e o `SQS_PROCESSING_TIMEOUT` só é comparado com um visibility válido | O SQS recebe os dois em segundos. Um visibility inválido não gera um segundo erro no prazo de processamento |
| 21 | **`decide` recebe uma `conclusion`** (duplicata, status, replay e erro), montada por `conclude(res, err)` | A tabela de ações fica testável sem reidratar transações do domínio |
| 22 | **Contextos por parâmetro:** `Consumer.Start(ctx)` deriva, sem cancelamento, o contexto de polling e o de trabalho, e os passa adiante; a struct guarda só as funções de cancelamento | O `containedctx` proíbe contexto em struct e o `contextcheck` exige que o `OnStart` passe o seu |
| 23 | **As mensagens seguintes de um grupo, liberadas depois de uma falha, também consomem recebimentos.** Com uma falha transitória persistente na primeira, elas chegam à DLQ junto (messaging §4.2) | É o comportamento do FIFO. A pausa por saúde evita que uma queda do banco provoque isso |
| 24 | **O I04a exige a DLQ vazia** | Sem essa verificação, a sabotagem "sem `DeleteMessage`" passava: a redrive drenava a fila principal |
| 25 | **O `TestHealthGatePauses` mede depois que os polls em andamento terminam** (retry de 2 s, medição a partir de 1,2 s) | A pausa segura receives **novos**; um poll já em andamento termina normalmente e pode entregar a mensagem que ficou visível |
| 26 | **`aws-sdk-go-v2/service/iam` v1.64.1**, só nos testes (I04f) | O I04f cria usuários IAM de teste com as políticas de `deploy/aws/policies` |
| 27 | **`testkit`:** `WagerMessage`, `SendMessage`, `ReceiveDLQ`, `QueueDepth` e `AssertQueueDrained` em `sqs.go`; `RenderPolicy` e `NewIAMUser` em `iam.go`; `App.SendWager`, `App.AssertQueueDrained` e `App.DLQDepth`. Os testes do `bootstrap` passam a partir do `Env.Config()` | Nomes finais da §6. Sem partir do `Env.Config()`, o consumidor do grafo de teste subiria com 0 pollers e semáforo sem capacidade |
| 28 | **Detalhes:** o hash da mensagem compara os textos recebidos (um UUID em maiúsculas muda o hash, embora o comando seja o mesmo); um campo de `data` com tipo errado (valor como número) é `MALFORMED_MESSAGE`, e não `INVALID_AMOUNT` como no HTTP; o replay de uma operação `FAILED` remove a mensagem (a falha já foi à DLQ com a entrega original) | — |

---

## 3. Camada `app`

### 3.1 `ProcessRequest.Inbox` (`internal/app/process_wager.go`)

```go
// InboxReceipt identifies the SQS message that carries the operation. When a
// ProcessRequest has one, every outcome records it in inbox_messages in the
// transaction that concludes the operation (SQS-04).
type InboxReceipt struct {
	Consumer    string
	MessageID   string
	MessageHash string
	MessageType string
	ReceivedAt  time.Time
}
```

`ProcessRequest` ganha `Inbox *InboxReceipt`. O `outcome` vem do status da transação resultante, e o `transaction_id` aponta para ela:

| Caminho | Onde a inbox é gravada | `outcome` |
| --- | --- | --- |
| Resultado novo | no fim da mesma `uow.Do`, depois do `settleAndPersist` | `PROCESSED` / `REJECTED` / `PENDING_REFERENCE` |
| Replay achado sob o lock | na mesma `uow.Do` | `IDEMPOTENT_REPLAY` |
| Replay achado antes da transação | numa `uow.Do` só com a inbox | `IDEMPOTENT_REPLAY` |
| Falha permanente depois do lock | na UoW separada do `recordFailure` | `FAILED` |

`ProcessedAt` é o `now` do caso de uso. Os erros (`KindInput`, `KindConflict`, `KindTransient`, `KindPermanent` sem `FAILED`) não gravam inbox: nada foi concluído.

### 3.2 `app.ConsumeWager` (`internal/app/consume_wager.go`)

```go
// ConsumerName identifies the wager consumer in inbox_messages (D-12).
const ConsumerName = "wager-transactions-consumer"

// WagerMessage is a parsed WagerTransactionRequested (messaging.md §3).
type WagerMessage struct {
	MessageID     string
	MessageHash   string // SHA-256 of the canonical {type, data}
	MessageType   string
	CorrelationID string
	Input         wagering.Input
	ReceivedAt    time.Time
}

// ConsumeResult is how a message was concluded. Duplicate: the inbox already
// had it with the same hash, and nothing was done.
type ConsumeResult struct {
	Duplicate bool
	Result    ProcessResult
}

func NewConsumeWager(reads Repos, wagers *ProcessWager) *ConsumeWager
func (c *ConsumeWager) Execute(ctx context.Context, m WagerMessage) (ConsumeResult, error)
```

Fluxo (até 3 rodadas):
1. `reads.Inbox().Find(ConsumerName, m.MessageID)`: mesmo hash → `Duplicate`; hash diferente → `apperrors.New(KindInput, "MESSAGE_HASH_MISMATCH", …)`.
2. `wagering.NewCommand(m.Input)` → `domainError` (`KindInput` com o código do lifecycle §5.3, inclusive `OPENING_NOT_ALLOWED`).
3. `ProcessWager.Execute` com `Via = SQS`, `CorrelationID = m.CorrelationID`, `CausationID = m.MessageID` e o `Inbox` preenchido.
4. `errors.Is(err, ErrInboxDuplicate)` → nova rodada a partir do passo 1. Esgotadas as rodadas, o erro volta, ainda transitório.

O código `MESSAGE_HASH_MISMATCH` vira constante do `app` (`CodeMessageHashMismatch`).

---

## 4. Adapter `internal/adapters/sqsconsumer`

### 4.1 Portas do pacote

```go
type Processor interface { // app.ConsumeWager; the I04d swaps a double in
	Execute(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error)
}
type QueueAPI interface { // subset of *sqs.Client
	ReceiveMessage(…) ; DeleteMessage(…) ; ChangeMessageVisibility(…) ; SendMessage(…) ; GetQueueAttributes(…)
}
type Pinger interface{ Ping(ctx context.Context) error } // *pgxpool.Pool
type Metrics interface {
	Received()
	Processed(outcome string, d time.Duration)
	Duplicate(layer string)
	Retried(reason string)
	SentToDLQ(reason string)
	DLQDepth(queue string, n int)
	ReceiveFailed()
	DeleteFailed()
}
```

### 4.2 Arquivos

| Arquivo | Conteúdo |
| --- | --- |
| `envelope.go` | `parseEnvelope(body string) (app.WagerMessage, *dlqReason)` com a decisão 14. JSON inválido, campo desconhecido, `messageId` ausente ou fora de 1 a 128 caracteres, `occurredAt` ausente ou fora de RFC 3339, `data` ausente → `MALFORMED_MESSAGE`. `type` diferente de `WagerTransactionRequested` → `UNSUPPORTED_MESSAGE_TYPE`. `data` com campo desconhecido → `MALFORMED_MESSAGE`. `messageHash` (decisão 4) e `correlationID(attr, messageId)` (decisão 13) |
| `handler.go` | `decide(res app.ConsumeResult, err error, receiveCount int, maxDelay time.Duration) action` (decisão 10) e a aplicação da ação na fila. Tabela na §4.3 |
| `dlq.go` | `dlqInput(msg, code, category, now)`: corpo original; `MessageGroupId` original ou `invalid-messages`; `MessageDeduplicationId` = id SQS original; atributos `errorCode`, `errorCategory`, `originalMessageId`, `consumerName` e `failedAt` (UTC, `.000Z`) |
| `backoff.go` | `retryDelay(receiveCount int, ceiling time.Duration) time.Duration = min(2^receiveCount s, ceiling)`, com o expoente limitado para não estourar |
| `batch.go` | `groupBatch(msgs) [][]types.Message`: por `MessageGroupId`, na ordem de chegada. Grupos em paralelo sob o semáforo `SQS_MAX_IN_FLIGHT` (compartilhado pelos pollers), mensagens do grupo em sequência. Um `retry` ou `release` no grupo libera as seguintes com `ChangeMessageVisibility(0)`. Liberação por prazo antes de cada mensagem (decisão 8) |
| `health_gate.go` | `healthGate` (decisão 6): `Report(ctx)` depois de um erro transitório (pinga e fecha se falhar); `Wait(ctx)` bloqueia os pollers enquanto fechado, pingando a cada 2 s. Log WARN ao pausar e INFO ao retomar |
| `consumer.go` | `Consumer`, `Options`, `NewConsumer(api QueueAPI, queues *awsclient.Queues, p Processor, gate Pinger, m Metrics, log *slog.Logger, opts Options)`, `Start()` e `Stop(ctx)`. Cada poller: `gate.Wait` → `ReceiveMessage` (`MaxNumberOfMessages`, `WaitTimeSeconds`, `VisibilityTimeout` do config; atributos de sistema `MessageGroupId` e `ApproximateReceiveCount`; atributo de mensagem `correlationId`) → processa o lote → recebe de novo. Erro no `ReceiveMessage`: backoff de 1 s a 30 s e `ReceiveFailed`. Cada mensagem roda com `context.WithTimeout(SQS_PROCESSING_TIMEOUT)` sobre um contexto de trabalho separado do de polling. Uma goroutine atualiza `sqs_dlq_depth` a cada 30 s |
| `module.go` | `fx.Module("sqsconsumer", …)`: `Options` do config, o `Consumer` e o lifecycle (§4.4). Um `fx.Invoke` força a instanciação |

### 4.3 Ação por resultado

| Resultado do `Processor` | Ação | Métricas |
| --- | --- | --- |
| `Duplicate` | `delete` | `duplicates{sqs,inbox}` |
| Resultado novo `PROCESSED` / `REJECTED` / `PENDING_REFERENCE` | `delete` | `processed{outcome}` |
| Replay | `delete` | `processed{replay}`, `duplicates{sqs,idempotency}` |
| `FAILED` | `dlq(INTERNAL_PERMANENT_FAILURE, DEFINITIVE)` | `processed{failed}`, `dlq_sent{…}` |
| `KindInput` / `KindConflict` (inclusive `UNKNOWN_WALLET` e `MESSAGE_HASH_MISMATCH`) | `dlq(code, CORRECTABLE)` | `dlq_sent{code}` |
| Envelope inválido (`parseEnvelope`, antes do `Processor`) | `dlq(MALFORMED_MESSAGE \| UNSUPPORTED_MESSAGE_TYPE, CORRECTABLE)` | `dlq_sent{code}` |
| `KindPermanent` sem resultado | `dlq(INTERNAL_ERROR, TRANSIENT)` (a categoria do lifecycle §5.3) | `dlq_sent{INTERNAL_ERROR}` |
| `context.Canceled` com o consumidor parando | `release` | — |
| Qualquer outro erro (transitório ou não classificado, D-05) | `retry(retryDelay(receiveCount))` + `gate.Report` | `retries{transient}` |
| Liberação por prazo | `release` | `retries{deadline_release}` |

**Invariante:** nenhum caminho chama `DeleteMessage` sem commit, sem duplicata confirmada na inbox ou sem um envio bem-sucedido para a DLQ. Entre o commit e o `DeleteMessage` fica o ponto de falha `consumer.after_commit_before_delete` (M8).

### 4.4 Shutdown (messaging §4.5)

`OnStop`, com prazo de `SHUTDOWN_TIMEOUT`:
1. cancela o contexto de polling (o long polling para na hora);
2. as mensagens recebidas e ainda não iniciadas recebem `release`;
3. espera as que estão em andamento (`WaitGroup`) até o prazo;
4. esgotado o prazo, cancela o contexto de trabalho: a transação faz rollback e as mensagens recebem `release`, com um contexto novo de 2 s;
5. só então retorna, com logs de início e fim. O pool fecha depois (D-15).

---

## 5. Configuração, métricas, Fx e política

### 5.1 Configuração (`internal/config`)

| Variável | Padrão | Validação |
| --- | --- | --- |
| `SQS_CONSUMER_POLLERS` | 2 | ≥ 1 |
| `SQS_RECEIVE_BATCH` | 10 | 1 a 10 |
| `SQS_WAIT_TIME` | 20 s | 0 a 20 s |
| `SQS_VISIBILITY_TIMEOUT` | 30 s | 1 s a 12 h |
| `SQS_PROCESSING_TIMEOUT` | 10 s | > 0 e < `SQS_VISIBILITY_TIMEOUT` |
| `SQS_MAX_IN_FLIGHT` | 16 | ≥ 1 |
| `SQS_RETRY_MAX_DELAY` | 300 s | 1 s a 12 h (limite do `ChangeMessageVisibility`) |

Todas têm padrão: o `.env.example` e o compose não mudam. O `StartApp` e o `Env.Config()` usam os tempos da decisão 9 (visibility 5 s, processamento 3 s, wait 1 s, retry máximo 1 s).

### 5.2 Métricas (`internal/observability/metrics.go`)

As de SQS do messaging §8, com os mesmos nomes, tipos e labels: `sqs_messages_received_total`, `sqs_messages_processed_total{outcome}`, `wager_duplicates_total{channel,layer}`, `sqs_retries_total{reason}`, `sqs_dlq_sent_total{reason}`, `sqs_dlq_depth{queue}`, `sqs_receive_errors_total` e `sqs_processing_duration_seconds{outcome}` (buckets de 5 ms a 10 s). Mais a nova `sqs_delete_errors_total` (decisão 12).

### 5.3 Fx

- `appModule` fornece `app.NewConsumeWager`.
- `bootstrap.Options()`: config → observability → postgres → aws → auth → app → outbox → **sqsconsumer** → httpapi. No stop: HTTP, consumidor, publisher (que ainda publica o que o consumidor confirmou), pool e clientes AWS.

### 5.4 Política

`deploy/aws/policies/pda-wallet-service.json` já concede tudo o que o consumidor usa, inclusive `sqs:GetQueueAttributes` na DLQ para o `sqs_dlq_depth`. Nada muda em `deploy/`.

---

## 6. `testkit`

| Arquivo | Conteúdo |
| --- | --- |
| `sqs.go` (novo) | `WagerMessage(tb, messageID, WagerData)`: monta o corpo do envelope. `SendMessage(tb, client, queueURL, body, SendOpts{GroupID, DedupID, CorrelationID})`. `ReceiveDLQ(tb, client, dlqURL, n)`: lê e apaga `n` mensagens da DLQ, com corpo e atributos, com prazo. `QueueDepth` e `AssertQueueDrained`: visíveis + invisíveis, com `Eventually` |
| `iam.go` (novo) | `RenderPolicy(tb, name, PolicyARNs)` e `NewIAMUser(tb, iamClient, policy)` (§2.2, decisão 26) |
| `audit.go` | `Absent` sem cancelar receives no meio (§2.2, decisão 18) |
| `app.go` | O `StartApp` guarda as URLs das filas isoladas. `App.SendWager`, `App.AssertQueueDrained` e `App.DLQDepth` delegam ao `sqs.go` |
| `env.go` | `Env.Config()` com os tempos novos |

---

## 7. Testes

TDD em todos: o teste vem antes do código ([`development-workflow.md`](../../development-workflow.md) §4), com o red sendo uma asserção falhando.

| ID | Teste | Pacote | O que prova | Cobre |
| --- | --- | --- | --- | --- |
| — | `TestParseEnvelope` | `sqsconsumer` | Os casos de `MALFORMED_MESSAGE` e `UNSUPPORTED_MESSAGE_TYPE` da §4.2; o caso válido gera o `Input` certo | SQS-07, SQS-10 |
| — | `TestMessageHash` | `sqsconsumer` | Não muda com ordem das chaves, espaços, `messageId` ou `occurredAt`; muda com `idempotencyKey` e com qualquer campo de `data`; `null` = ausente | SQS-03 |
| U05b+ | `TestPayloadHashHTTPEqualsSQS` | `sqsconsumer` | O `data` do envelope e o `Input` equivalente (UUIDs em caixas diferentes) dão o mesmo `PayloadHash` | IDEM-04 |
| — | `TestCorrelationID`, `TestRetryDelay`, `TestGroupBatch`, `TestDLQInput` | `sqsconsumer` | Decisão 13; `min(2^n s, teto)` sem estouro; ordem no grupo; grupo, dedup e atributos do envio à DLQ | SQS-07, SQS-10 |
| — | `TestDecide` | `sqsconsumer` | A tabela da §4.3 inteira | SQS-05..07 |
| — | `TestHealthGate` | `sqsconsumer` | Com um `Pinger` dublê: fecha com o ping falhando, reabre quando volta, continua aberto quando o erro não é do banco | SQS-07 |
| — | `TestLoad`/`TestValidate` (existentes), `TestMetrics_SQS` | `config`, `observability` | As 7 variáveis e a regra da decisão 7; nomes, labels e valores das métricas | OBS-03 (parcial) |
| — | `TestConsumeWager` | `app` | Resultado novo `PROCESSED`, `REJECTED` e `PENDING_REFERENCE` com a inbox na mesma transação e o `transaction_id` certo; duplicata sem linhas novas; `MESSAGE_HASH_MISMATCH`; `KindInput` (validação, `OPENING`, `UNKNOWN_WALLET`) e `KindConflict` sem inbox | SQS-02..04, SQS-06 |
| C10a⁻ | `TestConsumeWagerReplayAcrossChannels` | `app` | HTTP (sem inbox) e depois SQS com a mesma chave: inbox `IDEMPOTENT_REPLAY`, 1 lançamento | SQS-02, IDEM-04 |
| I03b | `TestConsumeWagerPermanentFailure` | `app` | Decorador faz `Outbox().Insert` falhar como permanente: `FAILED` e inbox `FAILED` em transação separada, sem lançamento | TX-06, SQS-07 |
| — | `TestConsumeWagerAtomicInbox` | `app` | `Inbox().Insert` falhando desfaz tudo: sem transação, sem lançamento. **Sensibilidade:** gravar a inbox fora da UoW faz o teste falhar | SQS-04 |
| — | `TestConsumeWagerInboxRace` | `app` | 20 execuções concorrentes da mesma mensagem: 1 transação, 1 linha na inbox, 19 `Duplicate` | SQS-03 |
| I04a | `TestInboxDeduplication` | `sqsconsumer` | A mesma mensagem 2× com `MessageDeduplicationId` diferentes: 1 transação, 1 lançamento, 1 linha na inbox, `wager_duplicates_total{sqs,inbox} = 1` | TST-I04, SQS-03, TST-C11 |
| I04b | `TestInboxHashMismatch` | `sqsconsumer` | Mesmo `messageId` com `data` diferente: DLQ com `MESSAGE_HASH_MISMATCH`, fora da fila principal | SQS-03 |
| I04c | `TestInvalidMessagesGoToDLQ` | `sqsconsumer` | JSON quebrado, `type` errado, `OPENING`, campo desconhecido e `UNKNOWN_WALLET`: DLQ com o `errorCode` e os atributos certos; fila principal vazia | SQS-07, SQS-10 |
| I04d | `TestTransientFailureRedrive` | `sqsconsumer` | Um `Processor` dublê devolve transitório para um `messageId`: depois de 3 recebimentos, a mensagem está na DLQ por redrive (sem `errorCode`), e o gate não pausou | SQS-07, TST-I05 |
| — | `TestPermanentFailureToDLQ` | `sqsconsumer` | Um dublê devolve `FAILED`: DLQ com `INTERNAL_PERMANENT_FAILURE` | SQS-07 |
| — | `TestGroupOrder` | `sqsconsumer` | 3 mensagens do mesmo grupo, a 1ª transitória uma vez: as seguintes são liberadas e o processamento final segue a ordem | SQS-07, D-12 |
| — | `TestDeadlineRelease` | `sqsconsumer` | Um processamento lento esgota o prazo da 2ª do grupo: ela é liberada sem processar, processada depois, e conta em `sqs_retries_total{deadline_release}` | Decisão 8 |
| — | `TestHealthGatePauses` | `sqsconsumer` | Um erro transitório com o `Pinger` falhando fecha o gate: nenhum `ReceiveMessage`, a mensagem fica na fila com o `receiveCount` inalterado; com o ping de volta, é processada | SQS-07 |
| — | `TestDLQSendFailure` | `sqsconsumer` | Decorador faz o `SendMessage` para a DLQ falhar: a mensagem não é removida e vira retry | SQS-05, SQS-07 |
| — | `TestConsumerShutdown` | `sqsconsumer` | Com um processamento bloqueado: o stop espera o que está em andamento, libera o que não começou (outro receptor pega na hora) e, esgotado o prazo, cancela e libera; `goleak.VerifyNone` passa | SQS-09, FX-03 (parcial) |
| I04e | `TestBusinessRejectionDeletesMessage` | `test/integration` | BET sem saldo pelo SQS: `REJECTED` (pelo `GET` por id externo), fila e DLQ vazias, `WagerTransactionRejected` na auditoria dentro do contrato | SQS-06 |
| — | `TestSQSEndToEnd` | `test/integration` | BET pelo SQS processado; eventos com `causationId = messageId` e o `correlationId` do atributo; a mesma operação pelo HTTP cai no replay | SQS-02, SQS-04 |
| I04f | `TestBrokerPoliciesEnforced` | `test/integration` | Os documentos de `deploy/aws/policies/` aplicados a usuários IAM do teste, sobre recursos isolados: permitido (provedor envia; serviço consome, altera visibilidade, envia à DLQ, lê atributos, publica) e negado com `AccessDenied` (provedor consome ou publica; serviço envia na fila de entrada; usuário sem política faz qualquer coisa) | AUTH-09 |
| I07a/b | `TestFxGraph`, `TestFxLifecycle` (existentes) | `bootstrap` | O grafo resolve `ConsumeWager` e `sqsconsumer`; o consumidor sobe e para com o resto, e o `goleak` continua passando | FX-01, FX-03 |

- **Pacote `sqsconsumer`:** o `TestMain` cria o banco (`NewEnv`). Cada teste cria filas isoladas (`CreateQueues`, redrive após 3 recebimentos) e monta o consumidor direto, sem Fx, sobre o `ConsumeWager` real ou um dublê do `Processor`. As carteiras são abertas pelo `OpenWallet` real.
- **Pacote `app`:** os testes do `ConsumeWager` usam o banco do pacote e os decoradores de falha já existentes em `faults_integration_test.go`.

---

## 8. Requisitos no encerramento

- **Completos:** SQS-01..07, SQS-09, SQS-10, TST-I04, AUTH-09 e **E5 (SQS)**; OUT-02 (a inbox fecha a parte que faltava); TST-I05 (a DLQ era o que faltava).
- **Parciais:**
  - SQS-08: o `PENDING_REFERENCE` pelo SQS grava a inbox (`TestConsumeWager`) e a mensagem é removida (`TestDecide`); a continuidade é do worker (M6);
  - SQS-11: C10a em processo; C10b com 3 processos no M8;
  - TST-C11: I04a; C01b e C10 no M8;
  - OBS-03 e FX-01/FX-03: métricas e módulo do consumidor.

---

## 9. Ajustes em `docs/` (aplicados logo após a aprovação desta spec, antes do plano)

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-12: `ConsumeWager`, pausa por ping, prazo < visibility e liberação por prazo, `INTERNAL_ERROR` na DLQ, `sqs_delete_errors_total`. D-15: `CONSUMER_ENABLED` no M7 |
| [`messaging.md`](../../messaging.md) | §3.3: hash sobre o struct tipado (ausente = `null`). §4.1: validação dos prazos. §4.2: liberação por prazo e a corrida da inbox. §4.3: gatilho por ping. §4.4: `INTERNAL_ERROR`. §8: `sqs_delete_errors_total` e os valores de `reason` |
| [`transaction-lifecycle.md`](../../transaction-lifecycle.md) | §5.4: `INTERNAL_ERROR` como motivo de DLQ. §6.2: a corrida da inbox recomeça pelo `Find` |
| [`test-plan.md`](../../test-plan.md) | §3.3: visibility 5 s na integração e linha nova `SQS_PROCESSING_TIMEOUT`. §5: testes da §7 |
| [`structure.md`](../../structure.md) | `app/consume_wager.go`, `sqsconsumer/backoff.go`, `testkit/sqs.go` e as portas do `sqsconsumer` |

No encerramento: `ARCHITECTURE.md`, `delivery-requirements.md`, `implementation-plan.md` e o diário.

---

## 10. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| Tempos do MiniStack (redrive, visibility) instáveis no I04d e no `TestDeadlineRelease` | Prazos com folga e só limites inferiores nas asserções de tempo. Se aparecer instabilidade, ela é medida e registrada no plano antes de qualquer ajuste |
| `ApproximateReceiveCount` ou `ChangeMessageVisibility(0)` com comportamento diferente no MiniStack | O spike do M0 validou os dois; os testes de grupo e de shutdown são a prova |
| Testes de consumidor em paralelo no mesmo pacote disputando filas | Filas isoladas por teste |
| Semáforo compartilhado segurando mensagens recebidas enquanto o visibility corre | A liberação por prazo (decisão 8) cobre também a espera pelo semáforo |
| `goleak` no `TestConsumerShutdown` com o SDK AWS | Os clientes HTTP do SDK são fechados pelo `awsclient`, como no M4; o teste ignora só as goroutines conhecidas da biblioteca, se houver |
