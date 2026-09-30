# M6 — Worker de referências: design

**Data:** 30/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor em 30/09/2026

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M6;
- D-11 (worker de referências), com o §6.3 do [`transaction-lifecycle.md`](../../transaction-lifecycle.md) e as consultas do [`data-model.md`](../../data-model.md) §6;
- [`messaging.md`](../../messaging.md) §7 (`causationId` do worker) e as 3 métricas de referência do [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) §13.2;
- [`test-plan.md`](../../test-plan.md) §5: I06 e I11, mais os testes da §8.

Esta spec registra só o **delta** em relação a `docs/`. A reavaliação, o reagendamento, a expiração e a política de retentativa já estão no domínio desde o M1 (`wagering.Settle`, `ReferenceRetryPolicy`), e o `settleAndPersist(…, insert = false)` já existe desde o M3, sem teste.

---

## 1. Objetivo e critério de pronto

**Objetivo:** toda operação em `PENDING_REFERENCE` é retomada por qualquer instância, a partir da agenda gravada no banco: vira `PROCESSED` (com movimento, lançamento e eventos), `REJECTED` (R3–R7, ou `REFERENCE_NOT_FOUND` ao expirar) ou é reagendada. Nenhuma pendência fica presa a uma instância, e duas instâncias nunca contam a mesma tentativa duas vezes. Fecha OPS-12, OPS-13 e TX-09 em processo e prepara o E7 (a prova com 3 processos é do C08b, no M8).

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde, com os testes da §8 vistos falhando pelo motivo certo e depois passando. Os testes escritos sobre comportamento que já existe (o `insert = false`, o I11) passam pela checagem de sensibilidade e registram `// Sensitivity: …`.
3. `docker compose up --build --wait` sobe as 3 réplicas saudáveis. Por `curl`: um REFUND antes da BET fica `PENDING_REFERENCE` (202), a BET chega e o REFUND vira `PROCESSED` em segundos, com saldo igual ao inicial e dois lançamentos. Um REFUND sem BET vira `REJECTED/REFERENCE_NOT_FOUND` ao esgotar o limite.
4. Requisitos da §9 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Fora do escopo** (com o marco de destino):
- o ponto de falha `references.after_claim` e o C08b (M8). O plano marca o local no código;
- a flag `REFERENCE_WORKER_ENABLED` (M7, junto com as outras 3 flags de papel da D-15);
- o resto do catálogo de métricas, o servidor admin e a verificação de segredos nos logs (M7).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Itens do lote em sequência**, um por vez em cada instância (escolha do autor em 30/09). Se o lote veio cheio **e** sem nenhum erro, o loop repete o claim sem esperar o intervalo | As 3 réplicas já dão paralelismo, e itens da mesma carteira se serializariam no lock de qualquer forma. A condição "sem erro" evita um laço apertado sobre itens que falham |
| 2 | **`TransactionRepository.ClaimDue(ctx, now, limit)`** devolve `[]PendingReference{ID, WalletID}` em **um único statement no pool**: `status = 'PENDING_REFERENCE' AND next_attempt_at <= $now`, `ORDER BY next_attempt_at`, `FOR UPDATE SKIP LOCKED`, `LIMIT`. Não é um lease: o `SKIP LOCKED` só pula as linhas que outra transação está processando naquele instante, e a checagem de verdade é a da decisão 3. O instante é o do `Clock` (o mesmo que `AdvanceDependents` grava) | O data-model §6 manda selecionar só os IDs e sair da transação, para travar a carteira antes da transação (sem deadlock com o HTTP). O `wallet_id` vem junto porque é ele que precisa ser travado primeiro, e é imutável |
| 3 | **Recheck sob os dois locks: status `PENDING_REFERENCE` e `next_attempt_at <= now`.** Se falhar, o item é ignorado (`Skipped`), sem escrita. O data-model §6 e o lifecycle §6.3 só citam o status | Achado desta spec: duas instâncias podem pegar o mesmo ID no claim (a primeira solta o lock ao terminar o statement). Se a primeira reagendou o item, o status continua `PENDING_REFERENCE`, e a segunda contaria uma tentativa a mais e antecipava a expiração. Com a checagem do horário, a segunda vê o `next_attempt_at` no futuro e sai |
| 4 | **`TransactionRepository.Lock(ctx, id)`**: `SELECT … FOR UPDATE` da transação, `ErrNotFound` quando ausente ou com ID não canônico (como `Get`) | Passo 2 do data-model §6. Como a carteira já foi travada, a ordem carteira → transação é a mesma do HTTP |
| 5 | **`app.ResolveReferences`** (`internal/app/resolve_references.go`), com `Claim(ctx, limit)` e `Resolve(ctx, PendingReference) (ResolveResult, error)`. Ele reusa o `ProcessWager` (mesma UoW, relógio, IDs, política e `settleAndPersist`) em vez de duplicar o pipeline | Estrutura já prevista no `structure.md`. A orquestração de escrita (transação → saldo → lançamento → outbox → antecipação) fica num lugar só |
| 6 | **`ResolveResult`** diz o desfecho: `Processed`, `Rejected` (com o `failureCode`), `Rescheduled`, `Skipped` ou `Failed`. Ele traz o `transactionId`, o `walletId` e as `attempts`. A expiração é `Rejected` com `REFERENCE_NOT_FOUND` | O `app` não importa Prometheus (D-18): o worker conta as métricas a partir do resultado |
| 7 | **`causationId` dos eventos do worker** é o `transactionId` da referência, quando ela existe no momento da avaliação (R2–R8), e vazio quando não existe (R1 e expiração). O `correlationId` continua sendo o original da transação (messaging §7). Para isso, o `settleAndPersist` passa a receber a causa como função da `Reference` lida (`func(wagering.Reference) string`); o HTTP e o SQS passam uma função que devolve o valor fixo de hoje | O `settleAndPersist` é quem lê a referência, então só ele conhece o `ID` dela. É o único ajuste no código do M3 |
| 8 | **Falha permanente** (`apperrors.KindPermanent` vinda do `Settle` ou da escrita) grava `FAILED` numa **UoW separada**: trava carteira → transação, refaz o recheck, `tx.Fail(now)`, `Update` e `AdvanceDependents`; sem lançamento nem evento (D-05, lifecycle §6.3). O log é `ERROR`. Se nem isso puder ser gravado, é transitório | Mesmo desenho do `recordFailure` do `ProcessWager`. As pendências que dependem dele saem logo com `REFERENCE_NOT_PROCESSED` |
| 9 | **Falha transitória** de um item (lock timeout, banco fora) gera um `WARN`, deixa a linha intacta (continua devida) e o loop segue para o próximo item. O item volta no ciclo seguinte, sem backoff próprio | A agenda é do banco: quem falhou não perde o lugar. `DB_LOCK_TIMEOUT` já limita a espera, e o intervalo do loop limita a frequência |
| 10 | **Cada item roda com um `context` desligado do cancelamento do loop** (`context.WithoutCancel`) e prazo fixo de 10 s | Um stop gracioso não interrompe uma transação em andamento (mesmo princípio da decisão 12 do M4). O prazo evita que um item preso segure o `OnStop` além do `SHUTDOWN_TIMEOUT` (padrão 20 s) |
| 11 | **Claim com o banco fora:** `WARN` e nova tentativa com backoff de 1 s a 30 s, que volta ao início no primeiro claim bem-sucedido. O worker nunca encerra o processo | Mesmo comportamento do publisher (M4, decisão 11) |
| 12 | **Variáveis novas:** `REFERENCE_POLL_INTERVAL` (padrão 500 ms, > 0) e `REFERENCE_BATCH_SIZE` (padrão 50, ≥ 1). Os tempos de teste ficam em 50 ms e no padrão | Sem elas, o I06 e o C07 esperariam segundos. Como o M4 e o M5, todas têm padrão: `.env.example` e compose não mudam |
| 13 | **Métricas:** `reference_pending_transactions` (gauge), `reference_retries_total` e `reference_expired_total`, com os nomes do `ARCHITECTURE.md` §13.2. O worker conta um retry por `Rescheduled` e uma expiração por `Rejected/REFERENCE_NOT_FOUND`. O gauge é atualizado pelo próprio worker, no máximo 1×/s, por `TransactionRepository.CountPendingReferences` (um `count` sobre o índice parcial `wager_tx_pending_due_idx`). A porta `references.Metrics` é do adapter, e o `observability.Metrics` a implementa | Como no M4 e no M5, cada marco entrega as métricas do que constrói. O M7 fecha o resto do catálogo |
| 14 | **Ordem de parada:** o worker continua parando **depois** do publisher (a ordem da D-15 não muda). Os eventos que o último item do worker gravar ficam na outbox e são publicados por outra instância ou no próximo start | A outbox é durável, então não há perda. Registrado como comportamento conhecido |
| 15 | **Logs:** `INFO` em cada desfecho terminal (`Processed`, `Rejected`, `Failed`), `DEBUG` no reagendamento, sempre com `transactionId`, `walletId`, `providerId`, `correlationId` (o original da transação), `outcome` e `attempts` | D-18 |

---

## 3. Camada `app`

### 3.1 Porta nova em `internal/app/ports.go`

```go
// PendingReference is a due PENDING_REFERENCE operation: the wallet to lock
// first, then the operation.
type PendingReference struct{ ID, WalletID string }

// TransactionRepository ganha:
//   ClaimDue(ctx, now, limit) ([]PendingReference, error)   // um statement no pool
//   Lock(ctx, id) (*wagering.WagerTransaction, error)      // FOR UPDATE; ErrNotFound
//   CountPendingReferences(ctx) (int, error)               // gauge
```

### 3.2 `ResolveReferences`

```go
func NewResolveReferences(p *ProcessWager) *ResolveReferences
func (r *ResolveReferences) Claim(ctx context.Context, limit int) ([]PendingReference, error)
func (r *ResolveReferences) Resolve(ctx context.Context, ref PendingReference) (ResolveResult, error)
func (r *ResolveReferences) CountPending(ctx context.Context) (int, error)
```

`Resolve`, em uma `uow.Do`:
1. `Wallets().Lock(WalletID)` → `Transactions().Lock(ID)`;
2. recheck (decisão 3): fora de `PENDING_REFERENCE` ou com `next_attempt_at > now` → `Skipped`;
3. `settleAndPersist(ctx, r, tx, &w, now, false, referenceCause)`;
4. o `ResolveResult` sai do estado final da transação: `Processed`, `Rejected` (código), `Rescheduled` (`attempts` novo).

Um erro `KindPermanent` desfaz a UoW e segue para a gravação do `FAILED` (decisão 8). Um erro transitório ou de contexto sobe para o worker (decisão 9). Erros de invariante do domínio já são traduzidos por `domainError`.

---

## 4. Adapter `internal/adapters/postgres`

`transaction_repo.go` ganha `ClaimDue`, `Lock` e `CountPendingReferences`, usando o `txSelect`/`scanTransaction` existentes e o índice parcial `wager_tx_pending_due_idx` (data-model §3.2). Nenhuma migration nova: as colunas, o índice e os grants (`UPDATE` na tabela) já existem desde o M2. O `FOR UPDATE SKIP LOCKED` num statement isolado toma e solta os locks das linhas devolvidas dentro do próprio statement, sem esperar por ninguém, então não participa de nenhum ciclo de deadlock.

---

## 5. Adapter `internal/adapters/references`

| Arquivo | Conteúdo |
| --- | --- |
| `worker.go` | `Worker` com `Run(ctx)`: claim → itens em sequência → espera (timer sensível ao cancelamento). Portas do pacote: `Resolver` (`Claim`, `Resolve`, `CountPending`, implementada por `*app.ResolveReferences`) e `Metrics` |
| `backoff.go` | O backoff do claim com o banco fora (1 s a 30 s, com teto), função pura com teste unitário |
| `module.go` | `Module`: constrói o `Worker`, `fx.Invoke` para forçar a instanciação, `OnStart` que inicia o loop e `OnStop` que cancela e espera até o prazo do Fx, com logs de início e fim |

**Shutdown:** o `OnStop` cancela o loop. Nenhum claim nem item novo começa; o item em andamento termina e é confirmado (decisão 10). Os itens do lote que não começaram continuam devidos no banco, então não há nada a liberar (não há lease).

**Ordem no `bootstrap.Options()`:** config → observability → postgres → aws → auth → app → **references** → outbox → sqsconsumer → httpapi (o `structure.md` já a prevê). `appModule` fornece `app.NewResolveReferences`.

---

## 6. Configuração, métricas e Fx

- `internal/config`: os campos `ReferencePollInterval` e `ReferenceBatchSize`, com validação nomeando a variável, como as demais (`TestLoad`/`TestValidate`).
- `internal/observability/metrics.go`: as 3 métricas da decisão 13 (`ReferenceRetried()`, `ReferenceExpired()`, `ReferencePending(n)`), com `TestMetrics_References`.
- `deploy/` e `docker-compose.yml`: nada muda. A política IAM não é afetada (o worker só usa o banco).

---

## 7. `testkit`

| Arquivo | Ajuste |
| --- | --- |
| `env.go` | `Env.Config()` e o `StartApp` usam `REFERENCE_POLL_INTERVAL = 50 ms` |
| `app.go` | `StartApp(ctx, opts ...func(*config.Config))`: as opções ajustam a configuração antes do start (o I06 precisa de um TTL longo para o REFUND não expirar durante a queda). Os chamadores existentes não mudam |
| `assert.go` | Sem mudança prevista. O item 8 e a regra dos eventos por transação (`PendingReference` → um terminal) já valem para pendências resolvidas; o plano confirma com o I06 e o C2 |

---

## 8. Testes

TDD em todos: o teste vem antes do código ([`development-workflow.md`](../../development-workflow.md) §4), com o red sendo uma asserção falhando (use stubs).

| ID | Teste | Pacote | O que prova | Cobre |
| --- | --- | --- | --- | --- |
| I18+ | `TestTransactionRepository` (`ClaimDue`, `Lock`, `CountPendingReferences`) | `postgres` | Só devolve as devidas e pendentes, por `next_attempt_at`, respeitando `limit`; pula uma linha travada por outra transação; `Lock` devolve a linha, `ErrNotFound` (ausente e ID malformado) e bloqueia uma segunda transação; a contagem | OPS-12, TX-09 |
| — | `TestResolveReferences` | `app` | (a) REFUND pendente + BET processada: `PROCESSED`, 2 lançamentos, saldo inicial, eventos `Processed` + `BalanceChanged` com `causationId` = id da BET e o `correlationId` original; (b) ainda ausente: `Rescheduled`, `attempts` + 1, sem evento; (c) ao esgotar as tentativas e ao vencer o TTL: `REJECTED/REFERENCE_NOT_FOUND`, evento `Rejected`, sem lançamento, `causationId` vazio; (d) referência `REJECTED`/`FAILED`: `REFERENCE_NOT_PROCESSED`; referência ainda pendente (R2): `Rescheduled`; (e) R4–R7 na retomada; (f) `ROLLBACK` que espera um `REFUND` que espera uma `BET`: a cadeia se resolve em cascata pela antecipação. **Sensibilidade:** trocar `Update` por `Insert` no `insert = false` faz falhar | OPS-12..14, TX-09 |
| — | `TestResolveReferencesSkips` | `app` | Item ainda não devido, item já terminal e item reagendado por outro worker entre o claim e o lock: `Skipped`, sem escrita e sem mudar `attempts`. **Sensibilidade:** remover a checagem do horário faz falhar | Decisão 3 |
| — | `TestResolveReferencesConcurrent` | `app` | Dois `Resolve` do mesmo item, com barreira de largada: uma avaliação só (`attempts` sobe 1×, um evento), e o outro é `Skipped` | Decisão 3, E7 |
| — | `TestResolveReferencesFailures` | `app` | Decorador faz a `Outbox().Insert` falhar como permanente: `FAILED` em UoW separada, sem lançamento nem evento, e o dependente vai a `REFERENCE_NOT_PROCESSED`; falha transitória: linha intacta e ainda devida; `context` cancelado no meio: nada gravado | Decisões 8, 9, TX-06 |
| — | `TestWorkerRepeatsFullCleanBatch`, `TestWorkerWaitsBetweenBatches`, `TestWorkerClaimBackoff`, `TestNextClaimDelay` | `references` | Com um `Resolver` dublê: lote cheio e sem erro repete o claim sem esperar; lote vazio, curto ou com erro espera o intervalo; erro do claim gera backoff de 1 s, 2 s… e volta ao início depois de um sucesso | Decisões 1, 11 |
| — | `TestWorkerStop` | `references` | Com um item bloqueado: o stop espera o item terminar, ele é confirmado, nenhum outro começa; `goleak.VerifyNone` passa | FX-03 (parcial), decisão 10 |
| — | `TestWorkerMetrics` | `references` | Um `Rescheduled` conta um retry; um `Rejected/REFERENCE_NOT_FOUND`, uma expiração; o gauge acompanha `CountPending` no máximo 1×/s | OBS-03 (parcial) |
| — | `TestResolveReferencesLockOrder` | `app` | Com a carteira travada por outra conexão, a avaliação enfileirada não segura a linha da operação (`FOR UPDATE NOWAIT` nela funciona): a ordem carteira → transação é a do HTTP. Determinístico; a mistura abaixo não prova isso (achado da execução) | D-09, decisão 4 |
| — | `TestConcurrentWorkers` | `references` | 2 workers (cada um com seu pool), 40 REFUNDs pendentes em 10 carteiras e as BETs chegando em paralelo: todos `PROCESSED` uma única vez, sem lançamento duplicado, `AssertWalletConsistent` em cada carteira | E7 (em processo), TX-09 |
| — | `TestWorkerVersusHTTP` | `test/integration` | BETs por HTTP (que antecipam as pendências) e o worker na mesma carteira, em paralelo: nenhum `40P01` nem lock timeout, todas as operações concluem | D-09 (ordem carteira → transação) |
| — | `TestConfigReferenceWorker`, `TestMetrics_References` | `config`, `observability` | As 2 variáveis novas e suas validações; nomes, tipos e valores das métricas | OBS-03 (parcial) |
| I06 | `TestRecoveryAfterRestart` | `test/integration` | O app 1 (TTL longo pela opção do `StartApp`) grava um REFUND antes da BET (202) e para. O app 2 sobe com o mesmo banco, recebe a BET, e o REFUND vira `PROCESSED`. Os replays da BET e do REFUND devolvem o resultado gravado com `idempotentReplay: true` | TST-I06, IDEM-01, OPS-12, E6 |
| I06b | `TestPendingExpiresAfterDowntime` | `test/integration` | O app 1 grava um REFUND com TTL curto e para; o app 2 sobe depois do TTL e rejeita com `REFERENCE_NOT_FOUND`: o TTL vale mesmo sem worker ativo | OPS-13 |
| I11 | `TestReversalRules` | `test/integration` | C4 (REFUND + ROLLBACK sobre a mesma BET → `ALREADY_REVERSED`), C5 (ROLLBACK do REFUND e novo REFUND → rejeitado), REFUND e ROLLBACK de WIN e de REFUND, `REVERSAL_INSUFFICIENT_FUNDS` diferente de `INSUFFICIENT_FUNDS`, e o mesmo REFUND antes da BET (retomado pelo worker). Saldos e lançamentos conferidos, e `AssertWalletConsistent`. **Sensibilidade** nos trechos sobre comportamento já existente | OPS-04..10 |
| — | `TestPendingReferenceResolved` | `test/integration` | C2 por HTTP: REFUND (202), BET (200) e o REFUND `PROCESSED` em até 2 s; eventos `PendingReference` → `Processed`/`BalanceChanged` na auditoria, dentro do contrato | OPS-12 |
| — | `TestPendingReferenceExpires` | `test/integration` | C3 por HTTP: REFUND sem BET vira `REJECTED/REFERENCE_NOT_FOUND`; evento `Rejected`; nenhum lançamento; `GET` mostra o desfecho | OPS-13 |
| — | `TestSQSPendingReferenceResolved` | `test/integration` | REFUND pelo SQS antes da BET: a mensagem é removida (inbox `PENDING_REFERENCE`, fila vazia), e depois da BET o REFUND é resolvido pelo worker | SQS-08 |
| I07a/b | `TestFxGraph`, `TestFxLifecycle` (existentes) | `bootstrap` | O grafo resolve o worker; ele sobe e para com o resto, e o `goleak` continua passando | FX-01, FX-03 |

- **Pacote `references`:** o `TestMain` cria o banco (`NewEnv`). Os testes com dublê não usam banco. Os de integração montam o worker direto, sem Fx, sobre o `ResolveReferences` real.
- **Pacote `app`:** os testes usam o banco do pacote e os decoradores de `faults_integration_test.go`. O tempo é controlado passando o `Clock` ao `ProcessWager` e ao `ResolveReferences` (um relógio de teste no pacote, se ainda não existir).
- **Testes existentes com pendência:** ver a §11.

---

## 9. Requisitos no encerramento

- **Completos:** OPS-12, OPS-13, OPS-14 (já em M1; passa a citar o worker), TX-09 (em processo), SQS-08 e TST-I06; OPS-04..10 confirmados pelo I11.
- **Parciais:**
  - E7: em processo (`TestConcurrentWorkers`, `TestResolveReferencesConcurrent`); a prova com 3 processos é do C08b (M8);
  - OBS-03: as 3 métricas de referência (o resto no M7);
  - FX-01 e FX-03: módulo e stop do worker.

---

## 10. Ajustes em `docs/` (aplicados logo após a aprovação desta spec, antes do plano)

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-11: itens em sequência, `ClaimDue` num statement, recheck de status **e** horário, causação, `FAILED` em UoW separada, `REFERENCE_POLL_INTERVAL`/`REFERENCE_BATCH_SIZE`. D-15: `REFERENCE_WORKER_ENABLED` no M7 |
| [`data-model.md`](../../data-model.md) | §6: o passo 3 do worker inclui o horário; o claim é um statement no pool; `Lock` da transação e a contagem de pendências |
| [`transaction-lifecycle.md`](../../transaction-lifecycle.md) | §6.3: o recheck com o horário e o caminho do `FAILED` |
| [`test-plan.md`](../../test-plan.md) | §3.3: `REFERENCE_POLL_INTERVAL` (500 ms / 50 ms / 50 ms) e `REFERENCE_BATCH_SIZE`. §5: os testes da §8 (I06b e os novos) |
| [`structure.md`](../../structure.md) | `app/resolve_references.go`, `references/backoff.go`, a nova assinatura do `StartApp` e as portas do `references` |
| [`messaging.md`](../../messaging.md) | §8: as 3 métricas de referência (nomes e tipos) |

No encerramento: `ARCHITECTURE.md`, `delivery-requirements.md`, `implementation-plan.md` (M6 ✅) e o diário.

---

## 11. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| **Testes de integração existentes criam pendências que hoje nunca são retomadas.** Com o worker no grafo do `StartApp` (TTL de 3 s e 3 tentativas), um REFUND sem BET vira `REJECTED` em cerca de 0,7 s. Afeta `wagering_test.go` ("a reversal before its reference waits") e o REFUND pendente do `events_test.go`, e a regra de eventos por transação do `AssertWalletConsistent` | O plano começa com o inventário (`grep` de REFUND, ROLLBACK e WIN com referência) e ajusta cada teste: a asserção passa a esperar o estado pendente **ou** a rejeição por expiração, ou o teste usa uma carteira/opção de TTL longo. O I11 e o `TestPendingReferenceExpires` cobrem a expiração de propósito |
| Instabilidade de tempo nos testes de expiração (TTL 3 s, backoff 100 ms, jitter ±20%) | Só limites inferiores nas asserções de tempo e `Eventually` com prazo. Os testes de `app` controlam o relógio, sem `sleep` |
| Item que falha sempre de forma transitória (lock preso) volta a cada ciclo | Aceito (decisão 9): o `DB_LOCK_TIMEOUT` e o intervalo limitam o custo, e o WARN com os IDs torna visível |
| Ciclo de deadlock por pendências de carteiras diferentes que se referenciam (o `AdvanceDependents` atualiza linhas de outra carteira sem travá-la) | Já existe no caminho HTTP desde o M3, e o `lock_timeout` o transforma num erro transitório. O `TestWorkerVersusHTTP` cobre a carteira única; o caso cruzado é registrado como limitação conhecida no `ARCHITECTURE.md` |
| Eventos do último item do worker esperam a próxima publicação (decisão 14) | A outbox é durável e qualquer instância publica; o `TestConcurrentWorkers` termina com a verificação de eventos entregues (item 8) |
