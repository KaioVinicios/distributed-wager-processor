# Plano de Testes

Estratégia, infraestrutura, injeção de falhas e a rastreabilidade de cada teste até os requisitos de [`delivery-requirements.md`](delivery-requirements.md) §17 e os critérios eliminatórios (§0). Os cenários de negócio de referência estão em [`transaction-lifecycle.md`](transaction-lifecycle.md) §9.

---

## 1. Princípios

1. **Infraestrutura real:** PostgreSQL, Keycloak e MiniStack reais nos testes de integração e e2e. Dublês só entram para **provocar** uma falha pontual (ex.: um publisher que falha nas N primeiras chamadas e depois delega ao SNS real), nunca para substituir a infraestrutura (E10).
2. **Invariante verificada sempre:** toda carteira criada por um teste passa, no `t.Cleanup`, pela verificação completa de consistência (§6). Nenhum cenário termina sem conferir que `stored == Σ créditos − Σ débitos`.
3. **Rastreabilidade:** cada teste declara o que cobre em um comentário (`// Covers: TST-C02, CONC-05, E4`), e `grep -rn "Covers:"` gera a matriz de cobertura.
4. **Isolamento por dados e recursos:** cada pacote de teste recebe um banco próprio e filas e tópico com sufixo aleatório (§3). Os testes rodam em paralelo sem interferir uns nos outros.
5. **Determinismo em corridas:** os testes de concorrência usam uma barreira de largada (`sync.WaitGroup` + canal fechado) e são repetidos N vezes com carteiras novas, para expor não-determinismo. Nada de `time.Sleep` como sincronização: a espera usa polling com prazo (`testkit.Eventually`).

---

## 2. Níveis e comandos

| Nível | Build tag | Onde | Infraestrutura | Comando |
| --- | --- | --- | --- | --- |
| Unitário | — | `*_test.go` junto ao código | Nenhuma | `go test ./...` · `go test -race ./...` |
| Integração | `integration` | Adaptadores (`internal/.../postgres`, `.../sqs`, etc.) e `test/integration/` | PostgreSQL, Keycloak e MiniStack do compose; aplicação **em processo** via Fx | `go test -tags=integration -race ./...` |
| E2E | `e2e` | `test/e2e/` | Mesma infraestrutura, com **3 processos** do binário iniciados pelo teste | `go test -tags=e2e -race -p 1 -timeout 15m ./test/e2e/...` |

Alvos do `Makefile`:

```sh
make infra-up          # docker compose up -d --wait postgres keycloak ministack aws-init migrate
make test              # go test -race ./...                        (unitários)
make test-integration  # infra-up + go test -tags=integration -race ./...
make test-e2e          # infra-up + go test -tags=e2e -race -p 1 -timeout 15m ./test/e2e/...
make check             # fmt-check + lint + vet + tidy-check + go-version-check + test
```

A lista completa de alvos está em [`stack.md`](stack.md) §5.

- `go test ./...` roda em um **checkout limpo, sem Docker**, porque os testes com infraestrutura ficam fora sem a build tag.
- `go vet` também roda com as tags, para verificar os arquivos que o `go vet` padrão ignoraria.

**Estrutura:**

```
test/
  testkit/       # utilitários compartilhados (sem build tag, para compilar em todos os níveis)
    env.go       #   configuração a partir de env/.env; criação de banco, filas e tópico isolados
    auth.go      #   tokens reais do Keycloak por client; tokens forjados (assinatura, aud e iss inválidos)
    api.go       #   cliente HTTP tipado da API
    sqs.go       #   enviar WagerTransactionRequested; ler DLQ e fila de auditoria filtrando por id
    assert.go    #   AssertWalletConsistent, SnapshotCounts, Eventually
    cluster.go   #   (e2e) build do binário, start/kill/stop/restart de N processos
  integration/   # //go:build integration — cenários que atravessam vários componentes
  e2e/           # //go:build e2e — multi-instância, falhas e resiliência
```

---

## 3. Preparação do ambiente de teste

### 3.1 Infraestrutura compartilhada

Sobe com `make infra-up`. Os testes usam apenas a infraestrutura do compose e **não** dependem dos containers `app-*`, que podem estar rodando ao mesmo tempo sem interferir, graças ao isolamento de §3.2.

| Serviço | Uso nos testes |
| --- | --- |
| `postgres` | Os testes conectam como `pda_owner` para criar bancos isolados, e a aplicação conecta como `pda_app` |
| `keycloak` | Realm `pda` com os clients de teste (D-07). É compartilhado, já que só emite tokens |
| `ministack` | Os testes criam filas e tópicos próprios |

### 3.2 Isolamento por pacote (`testkit.NewEnv`)

Chamado no `TestMain` de cada pacote com tag:
1. Cria o banco `pda_t_<pacote>_<rand>` como `pda_owner` (que tem `CREATEDB`) e aplica as migrations embutidas com `golang-migrate` + `iofs`. Isso também exercita as migrations (TST-I01). Como o `TestMain` não tem `testing.TB`, a assinatura é `testkit.NewEnv(ctx, pkg) (*Env, func(), error)`, e a função devolvida faz a limpeza.
2. Cria `wager-<rand>.fifo`, `wager-dlq-<rand>.fifo` (redrive com `maxReceiveCount = 3`), o tópico `events-<rand>.fifo` e a fila de auditoria assinante.
3. Devolve uma `config.Config` apontando para esses recursos, com os tempos acelerados de §3.3.
4. No fim, derruba o banco e apaga filas e tópico. Com `PDA_TEST_KEEP=1`, mantém tudo para inspeção.

O isolamento é por banco porque o ledger é append-only e bloqueia `TRUNCATE`. Um banco novo por pacote é a forma de "limpar" sem abrir exceção nas proteções.

### 3.3 Tempos acelerados

| Variável | Produção | Integração | E2E |
| --- | --- | --- | --- |
| `SQS_VISIBILITY_TIMEOUT` | 30 s | 5 s | 5 s |
| `SQS_PROCESSING_TIMEOUT` | 10 s | 3 s | 3 s |
| `maxReceiveCount` (provisionado) | 10 | 3 | 3 |
| `SQS_RETRY_MAX_DELAY` | 300 s | 1 s | 2 s |
| `SQS_WAIT_TIME` | 20 s | 1 s (0 nos testes de shutdown) | 2 s |
| `OUTBOX_LEASE` | 30 s | 2 s | 3 s |
| `OUTBOX_POLL_INTERVAL` | 500 ms | 100 ms | 200 ms |
| `OUTBOX_RETRY_BASE_DELAY` | 1 s | 100 ms | 200 ms |
| `OUTBOX_RETRY_MAX_DELAY` | 5 min | 1 s | 2 s |
| `REFERENCE_RETRY_BASE_DELAY` | 1 s | 100 ms | 200 ms |
| `REFERENCE_MAX_ATTEMPTS` | 8 | 3 | 3 |
| `REFERENCE_TTL` | 10 min | 3 s | 5 s |
| `SHUTDOWN_TIMEOUT` | 20 s | 5 s | 5 s |
| `DB_LOCK_TIMEOUT` | 5 s | 2 s | 2 s |

### 3.4 Cluster e2e (`testkit.Cluster`)

- No `TestMain`, o binário é compilado **uma vez**: `go build -tags faultinject -race -o $TMP/pda ./cmd/pda`.
- `cluster.Start(3)` inicia 3 processos no host, com portas HTTP e de métricas distintas, o mesmo banco e as mesmas filas isoladas, e espera `/health/ready` de cada um. Cada processo tem seu próprio pool de conexões e sua própria memória (CONC-04).
- **Operações:**
  - `Kill(i)` envia `SIGKILL`, um encerramento abrupto;
  - `Stop(i)` envia `SIGTERM` e espera a saída;
  - `Restart(i, env)` reinicia o processo com outro ambiente, por exemplo com ou sem ponto de falha;
  - `Instance(i).Client()` devolve um cliente HTTP apontado para aquela instância.
- Os logs de cada processo vão para `t.TempDir()` e são anexados à saída quando o teste falha.
- Para simular indisponibilidade, o harness executa `docker compose pause|unpause postgres|ministack`.

---

## 4. Injeção de falhas

O pacote `internal/faultinject` tem duas implementações:
- `faultinject_on.go` (`//go:build faultinject`): lê `PDA_FAULT=<ponto>[,<ponto>]` e, ao passar pelo ponto, grava `FAULT_HIT <ponto>` no stderr e chama `os.Exit(137)`;
- `faultinject_off.go` (`//go:build !faultinject`): um no-op, que o compilador elimina.

**O binário de produção e a imagem Docker não contêm nenhum ponto de falha.**

| Ponto | Local exato | Simula |
| --- | --- | --- |
| `consumer.before_commit` | Dentro de `uow.Do`, depois de todas as escritas e antes do `COMMIT` | Crash antes do commit |
| `consumer.after_commit_before_delete` | Depois do `COMMIT`, antes do `DeleteMessage` | Crash entre o commit e a remoção (TST-C05) |
| `http.after_commit_before_response` | Depois do `COMMIT`, antes de escrever a resposta | O cliente não recebe a resposta e reenvia |
| `outbox.after_claim_before_publish` | Depois do commit do claim, antes do `Publish` | Crash com evento reservado |
| `outbox.after_publish_before_ack` | Depois do `Publish`, antes da confirmação | Republicação (TST-C06) |
| `references.after_claim` | Worker: depois de travar a pendência, antes de resolver | Crash do worker (TST-C08) |

O harness confirma que a falha realmente aconteceu: exige a linha `FAULT_HIT` no log do processo e o exit code 137. Sem isso, o teste falha, para não passar por acaso.

---

## 5. Casos de teste

### 5.1 Unitários (TST-U)

| ID | Teste | Cobre |
| --- | --- | --- |
| U01a | `TestParseMoney`, tabela com válidos (`"0.00"`, `"25.00"`, máximo `int64`) e inválidos (`""`, `"25"`, `"25.0"`, `"025.00"`, `"+1.00"`, `"-1.00"`, `"1e3"`, `"NaN"`, `"Infinity"`, `"1.001"`, `" 1.00"`, overflow) | TST-U01, MON-04, MON-05, MON-08 |
| U01b | `TestMoneyArithmetic`: soma, subtração, negação e comparação; overflow em cada uma, incluindo `Negate(MinInt64)` | TST-U01, MON-02, MON-08 |
| U01c | `TestMoneyCurrencyMismatch`: todas as operações entre BRL e USD devolvem `ErrCurrencyMismatch` | TST-U01, MON-07, MON-12 |
| U01d | `TestMoneyJSON`: ida e volta pelo JSON; o JSON gerado contém strings, não números; um valor negativo (ex.: `difference`) sai como `"-5.00"`, mas a entrada externa negativa é rejeitada | MON-01, MON-04, MON-09 |
| U01e | `TestMoneyZeroValue`: `Money{}` é rejeitado em todas as operações | MON-11, DOM-03 |
| U01f | `FuzzParseMoney`: nunca entra em `panic`; o que é aceito sai idêntico na formatação de volta | MON-05, DOM-05 |
| U01g | `TestNoFloatInMoney`: analisa a AST do pacote `money` e falha se encontrar os identificadores `float32`, `float64` ou `strconv.ParseFloat`, ou um literal de ponto flutuante (`token.FLOAT`), que o `forbidigo` não captura ([`dev/spike-lint.md`](dev/spike-lint.md)) | MON-01, E3 |
| U02 | `TestWallet*`: criação com versão 1; débito sem saldo devolve erro; crédito e débito incrementam a versão; moeda diferente é rejeitada; `Rehydrate` não altera estado nem versão; `Wallet{}` (zero value) é rejeitada | TST-U02, WAL-*, DOM-02, DOM-03 |
| U03 | `TestTransactionStateMachine`: matriz de todos os pares origem × destino (válidos e inválidos); estado terminal devolve `ErrInvalidTransition`; `NewExternal` e `NewOpening` nascem em `PENDING` | TST-U03, TX-06, TX-07 |
| U04a | `TestKindRules`: tabela tipo × regra (valor, referência, movimento, eventos) de [`transaction-lifecycle.md`](transaction-lifecycle.md) §2 | TST-U04, OPS-01..07 |
| U04b | `TestZeroAmountPolicy`: zero é aceito só em `LOSS` e na abertura | TST-U04, OPS-11 |
| U04c | `TestReferenceResolution`: R1 a R8, com os códigos esperados | OPS-06..10, OPS-14 |
| U04d | `TestEvaluationOrder`: uma entrada com várias violações devolve sempre o primeiro código da ordem §3 | OPS-15 |
| U05a | `TestPayloadHashGolden`: vetores fixos → SHA-256 esperado. Mudanças de campo alteram o hash; chave, `messageId`, `occurredAt` e `received_via` não alteram | TST-U05, IDEM-03 |
| U05b | `TestPayloadHashHTTPEqualsSQS`: o mesmo negócio pelo corpo HTTP e pelo `data` SQS gera o mesmo hash. No M1, sobre a entrada do domínio (chave e caixa dos UUIDs diferentes); o M3 e o M5 estendem com o DTO e o envelope reais | IDEM-04 |
| U05c | `TestIdempotencyDecision`: mesma chave e mesmo hash → replay; hash diferente → conflito; mesmo `externalId` com outra chave → conflito | TST-U05, IDEM-05..07 |
| U06 | `TestOpening`: saldo maior que zero gera OPENING `PROCESSED`, 1 crédito e os 2 eventos sem metadados externos; saldo zero não gera transação, ledger nem eventos | TST-U06, HTTP-01, OUT-13 |
| U07 | `TestLedgerEntryInvariant`: o construtor rejeita `after ≠ before ± amount` | LED-02 |
| U08 | `TestEventConstructors`: tipo e versão fixos, envelope completo, RFC 3339 UTC, dinheiro em string | OUT-08, OUT-09, OUT-12 |
| U09a | `TestClassify`: `apperrors.Classify` reconhece cada `Kind` através de cadeias de `%w` | TX-10, DOM-04 |
| U09b | `TestPostgresErrorMapping` (pacote `postgres`, sem banco): SQLSTATEs `08*`, `40001`, `40P01`, `55P03`, `57P01`, `57014`, `53300` → transitório; `23505` por constraint → sentinela do `app` (`WALLET_ALREADY_EXISTS` como conflito; corridas de idempotência, reversão e inbox como transitórias) ou permanente; `PDA01`–`PDA05`, `22003`, `23502`, `23503`, `23514` e `25006` → permanente. O erro traduzido não carrega o `*pgconn.PgError` (o `Detail` traz a linha inteira) | TX-10 |
| U10 | `TestDomainHasNoInfraImports`: `go list -deps ./internal/domain/...` não contém `fx`, `net/http`, `aws` nem `pgx` | DOM-07 |
| U11 | `TestZeroValuesRejected`: `Money{}`, `Currency("")`, `Kind("")`, `Status("")`, `Wallet{}`, `WagerTransaction{}` e `LedgerEntry{}` são rejeitados pelas operações públicas. Um teste por pacote (`money`, `wallet`, `wagering`) | DOM-03 |
| U12 | `TestReferenceRetryPolicy`: sequência de atrasos (1, 2, 4, … s, com teto de 60 s), jitter dentro de ±20% e expiração por tentativas e por TTL, inclusive quando o TTL já venceu na primeira tentativa | OPS-12, OPS-13 |
| U13 | `TestDomainErrorKind` (`app`): todo erro que sai do domínio vira `KindPermanent`, exceto `*ValidationError` (`KindInput`), `*ConflictError` (`KindConflict`) e um `*apperrors.Error` já classificado, que sai intacto | TX-10, DOM-04 |
| U14 | `TestLedgerCursor` (`app`): ida e volta do cursor; base64 inválido, JSON inválido, campo extra e versão < 1 → `INVALID_FIELD`/`cursor`; `limit` fora de 1–200 → `INVALID_FIELD`/`limit` | HTTP-03 |
| U15 | `TestAuthPolicy` (`auth`): a matriz de D-07 inteira, inclusive `provider` sem `provider_id` e o `OPENING`, que não tem provedor | AUTH-04..06 |
| U16 | `TestVerifier` (`auth`, JWKS local via `httptest`): RS256 válido aceito; assinatura de outra chave, `alg=none`, HS256, `iss` e `aud` errados e expirado além da tolerância recusados; expirado dentro da tolerância aceito; JWKS inacessível ou vazio faz o start falhar | AUTH-02, FX-02 |
| U17 | `TestHTTPEdge` (`httpapi`, com stubs dos casos de uso): decodificação (tipo JSON errado, `MALFORMED_REQUEST`, 64 KB, dados depois do JSON), 415, `Idempotency-Key` repetida, `PROVIDER_MISMATCH`, mapeamento para status, `Retry-After`, correlação, 404/405 de rota e `panic` → 500 `INTERNAL_ERROR` | HTTP-09, D-04 |
| U18 | `TestContract` (`testkit`): o validador que todo teste HTTP usa recusa campo fora do schema, código fora do `enum`, status não documentado e requisição fora do contrato; aceita a requisição marcada como inválida e ignora rota fora do documento | D-20, DOC-06 |
| U19 | `TestModule`, `TestVerifierCheckKeys` (`auth`), `TestNewServerTimeouts` (`httpapi`) e `TestMetrics_ReconciliationDivergences` (`observability`): fail fast do JWKS, timeouts do servidor e a métrica da reconciliação | FX-02, OBS-03 |

### 5.2 Integração (TST-I)

| ID | Teste | Cobre |
| --- | --- | --- |
| I01 | `TestMigrationsUpDownUp`: `up` → snapshot do schema (`information_schema` + `pg_indexes` + `pg_trigger`) → `down -all` → `up` → o snapshot é idêntico | TST-I01, DB-04 |
| I02a | `TestConstraints`: tabela de SQL que violam cada constraint e índice de [`data-model.md`](data-model.md) §8, verificando o SQLSTATE e o nome da constraint. Roda num banco próprio com os triggers desligados, porque eles disparam antes dos `CHECK` e os esconderiam (os triggers são do I02b–e) | TST-I02, DB-03 |
| I02b | `TestLedgerImmutable` e `TestLedgerImmutableForApp`: `UPDATE`, `DELETE` e `TRUNCATE` no ledger falham como `pda_app` (`42501`) **e** como `pda_owner` (`PDA01`). `TestAppRolePrivileges`: os privilégios do `pda_app` são exatamente os do [`data-model.md`](data-model.md) §5 | TST-I02, LED-04, DB-03, E9 |
| I02c | `TestLedgerCoupling`: saldo alterado sem lançamento, e transação `PROCESSED` com movimento sem lançamento, falham no commit (`PDA04`). Lançamento incoerente com a carteira, lançamento para `LOSS` ou para transação não `PROCESSED`, e direção errada falham no insert (`PDA04`) | WAL-06, LED-05 |
| I02d | `TestTerminalTransactionImmutable`: `UPDATE` em `PROCESSED`, `REJECTED` e `FAILED` falha (`PDA02`) | TX-07 |
| I02e | `TestGuardTriggers`: colunas imutáveis e versão da carteira (`PDA03`), colunas imutáveis da transação (`PDA02`), `DELETE` em carteira e transação, snapshot da outbox e `published_at` voltando a `NULL` (`PDA05`) | WAL-07, TX-07, OUT-01 |
| I03a | `TestFinancialAtomicity`: uma falha **transitória** forçada no último `INSERT` da outbox (dublê de repositório) desfaz tudo: sem transação, sem ledger, sem outbox, saldo e versão intactos | TST-I03, WAL-06, OUT-02 |
| I03b | `TestPermanentFailureRecorded`: uma falha **permanente** forçada (decorador sobre o `Repos` real que falha no `Outbox().Insert`) grava `FAILED` em transação separada, sem lançamento nem alteração de saldo. O replay devolve 500 com o mesmo resultado. Pelo SQS, a inbox registra `FAILED` e a mensagem chega à DLQ com `INTERNAL_PERMANENT_FAILURE` | TX-06, SQS-07 |
| I04a | `TestInboxDeduplication`: a mesma mensagem enviada 2× com `MessageDeduplicationId` diferentes gera 1 transação, 1 lançamento, 1 linha na inbox e `wager_duplicates_total{layer="inbox"} = 1` | TST-I04, SQS-03, TST-C11 |
| I04b | `TestInboxHashMismatch`: mesmo `messageId` com `data` diferente vai para a DLQ com `MESSAGE_HASH_MISMATCH` | SQS-03 |
| I04c | `TestInvalidMessagesGoToDLQ`: JSON quebrado, `type` errado, `OPENING`, campo desconhecido e `UNKNOWN_WALLET` vão para a DLQ com o `errorCode` correto e são removidos da fila principal | SQS-07, SQS-10 |
| I04d | `TestTransientFailureRedrive`: um dublê do caso de uso devolve erro transitório para um `messageId`; depois de 3 recebimentos a mensagem está na DLQ, via redrive | SQS-07, TST-I05 |
| I04e | `TestBusinessRejectionDeletesMessage`: um BET sem saldo pelo SQS fica `REJECTED` e a mensagem sai da fila, sem ir para a DLQ | SQS-06 |
| I04f | `TestBrokerPoliciesEnforced`: aplica os documentos de `deploy/aws/policies/` a usuários IAM criados para o teste, sobre recursos isolados. Verifica o que é permitido (provedor envia; serviço consome, altera visibilidade, envia para a DLQ e publica) e o que é negado com `AccessDenied` (provedor consome ou publica, serviço envia na fila de entrada, usuário sem política faz qualquer coisa) | AUTH-09 |
| I04g | Testes a mais do M5 ([spec](dev/specs/2026-09-29-m5-sqs-consumer-design.md) §7): `TestConsumeWager`, `TestConsumeWagerReplayAcrossChannels`, `TestConsumeWagerAtomicInbox` (com sensibilidade), `TestConsumeWagerInboxRace`, `TestPermanentFailureToDLQ`, `TestGroupOrder`, `TestDeadlineRelease`, `TestHealthGatePauses`, `TestDLQSendFailure`, `TestConsumerShutdown` e `TestSQSEndToEnd` | SQS-02..07, SQS-09 |
| I05a | `TestOutboxConcurrentPublishers`: 2 publishers em processo, cada um com seu pool, e 200 eventos em 20 grupos. Todos são publicados, todo `eventId` aparece na fila de auditoria com o payload idêntico ao do banco (comparado como JSON), e nenhum fica pendente | TST-I05, OUT-03, OUT-05 |
| I05b | `TestNoPublishBeforeCommit`: um `uow.Do` insere na outbox e fica bloqueado antes do commit. Em 2 s, nada chega à fila de auditoria; depois do commit, o evento chega. Sensibilidade: um decorador que publica dentro do `Insert` faz o teste falhar | OUT-10, E8 |
| I05c | `TestOutboxRetryBackoff`: um `Sink` que falha nas 3 primeiras chamadas e depois delega ao SNS real faz `attempts` chegar a 3, respeita `next_attempt_at` (intervalos ≥ `base × 2ⁿ`), grava `last_error` e publica | OUT-04 |
| I05d | `TestOutboxLeaseRecovery`: um evento com lease vencido é reassumido, publicado e incrementa `outbox_lease_reclaims_total`; um evento com lease válido não é tocado até o lease vencer | OUT-03, OUT-06 |
| I05e | `TestEventContracts`: pela API, abertura, BET, WIN, LOSS, rejeição e REFUND pendente. Os 4 tipos de evento chegam à fila de auditoria e validam contra [`api/events.yaml`](../api/events.yaml) (campos, tipos, omissões), com `MessageGroupId`, `MessageDeduplicationId` e atributos corretos | OUT-07..13 |
| I05f | `TestPublisherSurvivesClaimFailures` e `TestOutboxBacklogGauges`: com o banco fora, o publisher espera 1 s, 2 s… e volta a publicar; com o broker fora, `outbox_pending_events` mostra o pendente e volta a zero depois da publicação | OUT-04, OBS-03 |
| I05g | `TestPublisherStop`: no stop, a publicação em andamento termina e é confirmada, nenhuma outra começa, e o resto do lote fica com o lease; `goleak` limpo | FX-03, OUT-06 |
| I06 | `TestRecoveryAfterRestart`: app 1 cria uma pendência de referência e para. App 2 sobe com o mesmo banco, a pendência é resolvida e os replays devolvem o resultado original | TST-I06, IDEM-01, OPS-12 |
| I07a | `TestFxGraph`: `fx.ValidateApp` com todos os módulos | TST-I07, FX-01 |
| I07b | `TestFxLifecycle`: `fxtest.New` → `Start` → tráfego → `Stop`. Depois do stop, os workers terminaram (logs de fim), o pool está fechado e `goleak.VerifyNone` passa | TST-I07, FX-03..05 |
| I07c | `TestFxFailFast`: configuração inválida ou banco inacessível fazem o `Start` falhar com um erro claro | FX-02 |
| I08 | `TestReconciliation`: carteira consistente dá `consistent: true`. Uma divergência forçada via owner com triggers desabilitados (só neste teste) é detectada em resposta, log e métrica, e o saldo **não** é alterado | HTTP-07 |
| I09 | `TestLedgerPagination`: 120 lançamentos com `limit` de 50 dão 3 páginas, sem repetição nem lacuna, e o cursor é opaco | HTTP-03 |
| I10 | `TestReplayReturnsOriginalBalance`: um BET é processado, outras operações mudam o saldo, e o replay do BET devolve o saldo original | IDEM-08 |
| I11 | `TestReversalRules`: cenários C4 e C5 do lifecycle, REFUND/ROLLBACK de WIN e REFUND, `REVERSAL_INSUFFICIENT_FUNDS` diferente de `INSUFFICIENT_FUNDS` | OPS-04..10 |
| I12 | `TestHTTPErrorContract`: cada linha do catálogo §5.3 do lifecycle devolve o status, o `Content-Type` e o `code` esperados. `INTERNAL_ERROR` e `TEMPORARILY_UNAVAILABLE` não são provocáveis sem sabotagem: o mapeamento deles é do U17, e o 503 real é do R01 | HTTP-09 |
| I13 | `TestHealthChecks`: `/health/live` 200; `/health/ready` 200 com dependências e 503 com o PostgreSQL pausado | HTTP-08, OBS-04 |
| I14 | `TestLogsHaveIdsWithoutSecrets`: logs capturados de um fluxo completo são JSON com os IDs e não contêm `Authorization`, o token nem o payload completo | OBS-01, OBS-02 |
| I15 | `TestOpenAPIContract`: `api/openapi.yaml` passa na validação do kin-openapi, e o conjunto método + path do documento é **idêntico** ao da tabela de rotas do `httpapi`. É unitário (sem infraestrutura), então roda no `go test ./...`. Além disso, todo teste HTTP de integração valida requisição e resposta contra o documento (`testkit/contract.go`) | HTTP-*, DOC-06 |
| I16 | `TestContextCancellation`: um `context` cancelado, ou com prazo vencido enquanto espera o lock de uma carteira, interrompe a operação no banco (o UoW faz rollback e devolve o erro do `context`, classificado como transitório), sem efeito parcial | DOM-06 |
| I17 | `TestDomainFlowsPersist`: abertura, BET, WIN (com e sem referência), LOSS, REFUND, ROLLBACK de WIN, rejeição e `PENDING_REFERENCE` → `PROCESSED`, produzidos por `OpenWallet`/`Settle` e gravados pelos repositórios, passam pelos triggers e pela verificação SQL de §6 | WAL-06, LED-05, TX-09 |
| I18 | `TestWalletRepository`, `TestOutboxRepository`, `TestInboxRepository`, `TestTransactionRepository`, `TestTransactionQueries` e `TestLedgerQueries`: ida e volta idêntica de carteira, transação, lançamento e inbox; não encontrado; `FindReference` com `AlreadyReversed`; antecipação de pendências; paginação e soma do ledger; payload da outbox igual ao envelope serializado; sentinelas das violações de unicidade. O M4 acrescenta o `TestOutboxStore`: o claim só pega eventos devidos, sem lease vivo e não publicados; dois claims concorrentes pegam conjuntos disjuntos; lease vencido é marcado como reassumido; confirmação e falha só com o dono do lease; `attempts`, agenda, liberação do lease e `last_error`; backlog | DB-01, DB-02, WAL-03, IDEM-02, SQS-03, OUT-03, OUT-04 |
| I19 | `TestUnitOfWork`: commit, rollback em erro e em `panic`, `lock_timeout` com a carteira travada por outra transação → transitório, `Snapshot` somente leitura | DB-02, CONC-01 (D-09) |
| I20 | `TestOpenWallet` (`app`): saldo positivo grava carteira v1, `OPENING`, crédito e os 2 eventos; saldo zero grava só a carteira; segunda abertura → `WALLET_ALREADY_EXISTS`; entradas inválidas com código e `field` | HTTP-01, WAL-03 |
| I21 | `TestProcessWager` (`app`): todos os desfechos, replay com o saldo original, os dois 409, `UNKNOWN_WALLET` sem escrita, antecipação das pendências quando a referência chega e `FAILED` por overflow de crédito | HTTP-06, IDEM-05..08, TX-06 |
| I22 | `TestProcessWagerRaces` (`app`): a mesma chave em duas carteiras, em paralelo, com barreira de largada: uma processa, a outra termina em 409 depois da releitura, sem escrita parcial Também a mesma chave confirmada entre as duas leituras de idempotência (decorador que esconde a linha da primeira): replay, nunca 409; e o limite de 3 tentativas por corrida | IDEM-07, CONC-03 |
| I23 | `TestQueries`, `TestListLedger` e `TestReconcile` (`app`): leituras com 404 também para id malformado, paginação estrita (`limit` e `cursor` recusados antes da leitura da carteira) e reconciliação com divergência forçada (triggers desligados só dentro da transação do owner, sob `ACCESS EXCLUSIVE`) | HTTP-02..05, HTTP-07 |
| I24 | `TestOpenWalletAPI`, `TestLedgerAPI`, `TestHappyPathFlow` e `TestHarness` (`test/integration`): abertura, leituras, o fluxo do "pronto quando" e o app em processo respondendo pelo contrato | HTTP-01..06, DOC-06 |
| — | `TestOutboxProblemsDetectsDivergence` (`postgres`, banco próprio, triggers desligados): sensibilidade do item 7 do §6, cada divergência da matriz do lifecycle §7 é reportada | TST-C09 |

### 5.3 Autenticação e autorização (TST-A)

| ID | Teste | Cobre |
| --- | --- | --- |
| A01a | `TestAuthRealIdP`: um token real de `provider-a` via `client_credentials` é aceito | TST-A01, AUTH-01, AUTH-03 |
| A01b | `TestAuthRejects`: devolvem 401 sem token, token malformado, assinatura forjada (chave RSA gerada no teste), `alg=none`, `aud` errado (client `no-audience-client`), token do realm `other` (`iss` e chaves diferentes) e token expirado (de `provider-short-lived`, com 5 s de vida; os testes usam `OIDC_CLOCK_SKEW=1s` e esperam cerca de 7 s) | TST-A01, AUTH-02, E1 |
| A02a | `TestProviderIsolationQueries`: `provider-b` recebe 404 em `GET /wagering/transactions/{id de A}`, e 403 em `GET /providers/provider-a/...` | TST-A02, AUTH-05, E2 |
| A02b | `TestProviderIsolationReplay`: `provider-b` envia o corpo de A com a chave de A. Resultado: 403 `PROVIDER_MISMATCH`, e a resposta não contém nenhum dado de A | TST-A02, AUTH-04, AUTH-05 |
| A02c | `TestInternalOperationsRestricted`: tokens de provedor e de `no-role-client` recebem 403 em todas as rotas `/wallets*`, e `wallet-service` recebe 403 em `POST /wagering/transactions` | TST-A02, AUTH-06 |
| A03 | `TestUnauthorizedHasNoEffects`: para cada caso de A01b e A02, compara a contagem de linhas de todas as tabelas antes e depois (`SnapshotCounts`); os saldos e as métricas de resultado não mudam | TST-A03, AUTH-07, E2 |
| A04 | `TestPublicEndpoints`: `/health/*`, `/docs` e `/openapi.yaml` respondem sem token, e mais nenhuma rota responde sem token | AUTH-08, D-20 |

### 5.4 Concorrência e recuperação (TST-C) — e2e com 3 instâncias

Todos rodam com `cluster.Start(3)`, distribuindo as requisições entre as instâncias em round-robin, a menos que o teste indique outra coisa. O C01a e o C02 também rodam **em processo** desde o M3 (`test/integration/`), como verificação antecipada do E4 e do E5 pelo HTTP.

| ID | Teste | Procedimento | Asserções | Cobre |
| --- | --- | --- | --- | --- |
| C01a | `TestSameBet50xHTTP` | 50 goroutines, mesmo corpo e mesma chave, distribuídas nas 3 instâncias | 1 transação, 1 débito, saldo = inicial − valor; todas as respostas com o mesmo `transactionId`; 49 com `idempotentReplay: true` | TST-C01, E5 |
| C01b | `TestSameBet50xSQS` | 50 mensagens com `messageId`s distintos, a mesma `idempotencyKey` e `MessageDeduplicationId` distintos | 1 débito; inbox com 50 linhas (1 `PROCESSED` + 49 `IDEMPOTENT_REPLAY`) | TST-C01, TST-C11 |
| C02 | `TestTwoBetsCompete` | Saldo 100.00; BET A de 80.00 na instância 1 e BET B de 80.00 na instância 2, com largada simultânea; repetido 20× com carteiras novas | 1 `PROCESSED` + 1 `REJECTED/INSUFFICIENT_FUNDS`; saldo 20.00; 1 débito; o reenvio de A e B não muda nada | TST-C02, CONC-05, E4 |
| C03a | `TestWalletsInParallel` | 20 carteiras × 10 BETs simultâneos | Tudo processado e consistente | TST-C03, CONC-06 |
| C03b | `TestNoGlobalLock` | O teste trava a carteira X (`FOR UPDATE` em uma transação aberta) e envia um BET para a carteira Y | O BET em Y conclui em menos de 1 s; o BET em X fica bloqueado até a liberação (ou 503 por lock timeout) | CONC-01, E7 |
| C04 | — | C01, C02, C03, C06 e C10 **já** rodam com 3 instâncias | — | TST-C04, CONC-04, E7 |
| C05a | `TestCrashAfterCommitBeforeDelete` | A instância 1 roda com `PDA_FAULT=consumer.after_commit_before_delete` e recebe uma mensagem | A instância 1 sai com código 137; depois do visibility timeout, a instância 2 recebe e trata como duplicata na inbox; 1 lançamento | TST-C05, SQS-05 |
| C05b | `TestCrashBeforeCommit` | `PDA_FAULT=consumer.before_commit` | Nada persiste; na reentrega, outra instância processa uma única vez | §3 "encerramento abrupto antes do commit" |
| C05c | `TestHTTPCrashAfterCommit` | `PDA_FAULT=http.after_commit_before_response`; o cliente reenvia para outra instância | Replay com o resultado original; 1 lançamento; os eventos da operação chegam à auditoria publicados por outra instância (interrupção entre o commit e a publicação) | IDEM-01, IDEM-08, OUT-06a |
| C06a | `TestPublisherCrashAfterPublish` | Duas instâncias publicando; a primeira roda com `PDA_FAULT=outbox.after_publish_before_ack` | A instância 1 sai; a instância 2 reassume depois do lease; todo `eventId` chega à auditoria pelo menos uma vez com os bytes idênticos; nenhum evento fica pendente | TST-C06, OUT-05, OUT-06b |
| C06b | `TestPublisherCrashAfterClaim` | `PDA_FAULT=outbox.after_claim_before_publish` | Outra instância publica depois do lease; nada é perdido | OUT-06a |
| C07a | `TestRefundBeforeBet` | REFUND (202), depois BET (200), por HTTP e por SQS | O REFUND fica `PROCESSED` em até 2 s; saldo = inicial; 2 lançamentos; eventos `PendingReference` → `Processed` | TST-C07, OPS-12 |
| C07b | `TestRefundReferenceExpires` | REFUND sem BET | `REJECTED/REFERENCE_NOT_FOUND` depois de esgotar as tentativas ou o TTL; evento `Rejected`; nenhum lançamento | TST-C07, OPS-13 |
| C08a | `TestFullRestart` | Cria processados, rejeitados e pendentes; `Kill` nas 3 instâncias; `Start(3)` de novo. O cenário de aceite assíncrono do desafio não se aplica: `PENDING` nunca é persistido (D-05), e a retomada durável é a de `PENDING_REFERENCE` (C08b) | Os replays devolvem os resultados originais; as pendências são resolvidas ou expiram; tudo consistente | TST-C08, IDEM-01 |
| C08b | `TestReferenceWorkerCrash` | `PDA_FAULT=references.after_claim` na instância 1 | Outra instância resolve a pendência | TST-C08, TX-09 |
| C09 | — | `AssertWalletConsistent` roda no `Cleanup` de todos os testes (§6) | — | TST-C09 |
| C10a | `TestHTTPThenSQSSameOperation` | A operação é processada por HTTP e depois chega a mesma pelo SQS; e o inverso | 1 lançamento; o segundo canal cai no replay (inbox `IDEMPOTENT_REPLAY`, HTTP `idempotentReplay: true`) | TST-C10, SQS-02, IDEM-04 |
| C10b | `TestHTTPAndSQSConcurrent` | A mesma operação por HTTP e SQS ao mesmo tempo; e, na mesma carteira de 100.00, 80.00 por HTTP e 80.00 por SQS | 1 débito em cada caso; o segundo cenário reproduz o C02 entre canais | TST-C10, SQS-11 |
| C11 | — | Os testes C01b, I04a e C10 usam `MessageDeduplicationId` distintos, de modo que a deduplicação exercitada é a da aplicação | — | TST-C11 |
| C12 | — | Todos os níveis rodam com `-race`, e o binário do e2e é compilado com `-race` | — | TST-C12 |

### 5.5 Resiliência de infraestrutura (§3 do desafio) — e2e

| ID | Teste | Procedimento | Asserções | Cobre |
| --- | --- | --- | --- | --- |
| R01 | `TestPostgresOutage` | Tráfego HTTP e SQS contínuo; `docker compose pause postgres` por 10 s; `unpause` | Durante a queda: HTTP responde 503 com `Retry-After` e `/health/ready` responde 503. Depois: todas as mensagens SQS são processadas, **nenhuma vai para a DLQ** (pausa por saúde), sem duplicidade, tudo consistente | §3, SQS-07, E5 |
| R02 | `TestSQSOutage` | `docker compose pause ministack` por 10 s | Durante a queda: `/health/ready` responde 503 (SQS indisponível), mas o HTTP continua processando; a outbox acumula e `outbox_oldest_pending_age_seconds` sobe. Depois: a outbox esvazia e todos os eventos chegam à auditoria | §3, OUT-04 |
| R03 | `TestGracefulShutdownSQS` | 30 mensagens em andamento; `SIGTERM` na instância 1 | A instância sai dentro de `SHUTDOWN_TIMEOUT`, com logs de parada ordenada; nenhuma mensagem é perdida (todas processadas uma vez, pela 1 ou por outra instância) | SQS-09, FX-04 |
| R04 | `TestGracefulShutdownHTTP` | Requisições em andamento; `SIGTERM` | As requisições em andamento terminam, as novas são recusadas (conexão recusada) e o processo termina com código 0 | FX-04 |

---

## 6. Verificação de consistência (`testkit.AssertWalletConsistent`)

Registrada automaticamente para toda carteira criada via `testkit`. Executada no `t.Cleanup`, depois de `Eventually` confirmar que não há pendências nem outbox em aberto relacionadas à carteira. Os itens 2–6 são SQL puro, em `testkit.LedgerProblems` (M2), cuja sensibilidade é provada por um teste que grava cada divergência com os triggers desligados; o M3 acrescenta os itens 1 e 7, e o M4 o item 8.

1. `POST /wallets/{id}/reconciliation` devolve `consistent: true` e `difference = 0.00`.
2. SQL direto: `balance_minor == Σ CREDIT − Σ DEBIT`.
3. **Cadeia do ledger:** as versões são estritamente crescentes de 1 em 1, e o `balance_before` de cada lançamento é igual ao `balance_after` do anterior. O primeiro lançamento tem `balance_before = 0`.
4. `wallets.version == max(wallet_version)` do ledger, ou `1` se não houver lançamentos.
5. Não existem lançamentos para transações `REJECTED`, `FAILED`, `PENDING_REFERENCE` ou `LOSS`.
6. Cada transação `PROCESSED` com movimento tem exatamente 1 lançamento.
7. Toda transação terminal tem exatamente os eventos da matriz do lifecycle §7 na outbox (`testkit.OutboxProblems`, SQL, com teste de sensibilidade como o do `LedgerProblems`).
8. **Outbox publicada e entregue:** nenhum evento da carteira fica sem `published_at` (espera com prazo), e cada `eventId` chegou à fila de auditoria com o conteúdo igual ao da coluna `payload`, comparado como JSON e validado contra `api/events.yaml`.

---

## 7. Rastreabilidade: critérios eliminatórios

| Eliminatório | Testes que o comprovam |
| --- | --- |
| E1 Autenticação efetiva | A01a, A01b |
| E2 Acesso não autorizado | A02a–c, A03 |
| E3 Ponto flutuante | U01a–g (U01g analisa o código) |
| E4 Saldo negativo por concorrência | C02, C10b, I02a (`CHECK`) |
| E5 Movimentação duplicada | C01a/b, C05a/c, C10a/b, I04a, R01 |
| E6 Idempotência só em memória | I06, C05c, C08a |
| E7 Dependência de instância única | C01–C10 com 3 processos, C03b, C08b, C06a |
| E8 Publicação antes do commit | I05b |
| E9 Ledger auditável | I02b, I02c, §6 em todos os testes |
| E10 Mocks no lugar da infraestrutura | Princípio 1: todos os testes de integração e e2e usam PostgreSQL, Keycloak e MiniStack reais |

---

## 8. Critérios de saída

- [ ] `go test -race ./...` verde em um checkout limpo, sem Docker.
- [ ] `make test-integration` e `make test-e2e` verdes a partir de `make infra-up`.
- [ ] Cada linha das tabelas §5 e §7 implementada, ou registrada como não concluída no `ARCHITECTURE.md`, com o motivo.
- [ ] `gofmt -l .` vazio e `go vet` (com e sem tags) sem avisos.
- [ ] O README documenta os comandos deste plano e o tempo aproximado de cada nível.

**Prioridade se o prazo apertar**, em ordem:
1. Os testes que comprovam eliminatórios (§7).
2. C01 a C08.
3. I05e, I09, I12 e I14.
4. R01 a R04.

Os itens que ficarem de fora entram na seção de limitações.

---

## 9. Opcional — teste de carga ⭐

Se sobrar tempo: um cenário com `vegeta` ou `k6`, 3 instâncias, 1.000 carteiras, 60 s, mistura de 70% BET, 25% WIN e 5% REFUND. Deve registrar ambiente, throughput, p50/p95/p99, erros, conflitos (`concurrency_conflicts_total`) e atraso da outbox (`outbox_publish_lag_seconds`). O comando e o relatório ficam em `docs/load-test.md`.
