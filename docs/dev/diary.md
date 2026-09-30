# Diário de desenvolvimento

Anotações curtas do autor: o que foi feito em cada sessão e onde o trabalho parou. Os detalhes ficam nas specs, nos planos e nos commits.

---

## 28/09/2026 (seg): documentação e planejamento

- Leitura do `CHALLENGE.md` e redação de toda a documentação de sistema em `docs/`:
  - requisitos de entrega, decisões D-01 a D-20, modelo de dados, ciclo de vida das transações e mensageria;
  - plano de testes, estrutura, stack, fluxo de desenvolvimento e plano de implementação M0–M12.
- `ARCHITECTURE.md` criado como documento vivo, revisado ao fim de cada marco.
- Regras de qualidade definidas: spec → plano → TDD → verificação por marco, com commits só com a autorização do autor.
- Revisão geral de todos os documentos contra o `CHALLENGE.md` e commit inicial em 13 commits atômicos.

## 28–29/09/2026 (seg–ter): M0, esqueleto, qualidade e infraestrutura

**Spikes** (`docs/dev/spike-*.md`):
- **MiniStack 1.5.18:** toda a topologia funciona (SQS FIFO, redrive, visibilidade, SNS FIFO → SQS FIFO com raw delivery). Com `AUTH=true`, as políticas IAM são **avaliadas**. O autor decidiu aplicá-las (D-02): usuários IAM com políticas de identidade e chaves geradas pelo `aws-init` em `.local/aws/credentials`.
- **Keycloak 26.7.4:** `iss` estável com `KC_HOSTNAME` + backchannel dinâmico, e verificação via JWKS direto, sem discovery. O mapper padrão `audience resolve` foi removido do realm (D-07).
- **Lint:** golangci-lint v2.14.0 funciona com Go 1.27.1, e o `forbidigo` captura `float`. O literal inferido (`x := 1.5`) fica para o U01g.

**Esqueleto** ([spec](specs/2026-09-28-m0-skeleton-design.md) → [plano](plans/2026-09-28-m0-skeleton.md) → execução com TDD):
- **Módulos Fx:** `config`, `observability`, `postgres`, `aws` e `httpapi`, com `/health/live`, `/health/ready` (PostgreSQL + SQS) e `/metrics` na porta admin.
- **`pda healthcheck`:** a sonda usada pelo compose, porque a imagem distroless não tem shell nem `curl`.
- **Compose:** postgres, keycloak (realms `pda` e `other`), ministack com `AUTH=true`, `aws-init` idempotente (reaproveita as chaves) e `app-1..3` saudáveis.
- **Testes:**
  - unitários em todos os pacotes;
  - integração: I07a–c e `TestProvisioning`, com checagem de sensibilidade por sabotagem;
  - o `goleak` revelou que os clientes AWS não fechavam conexões no shutdown, e isso foi corrigido.
- **Verificação do zero:** `make check`, `make test-integration` e `docker compose up --build --wait` verdes.
- **Requisitos:** ART-01..04, 06, 07, 10, FX-02, HTTP-08 e OBS-04 marcados; FX-01 e AUTH-09 parciais.
- **Minors adiados:**
  - servidor HTTP que morre depois do start não encerra o processo;
  - réplicas sem `restart:` no compose;
  - logs do Fx em nível INFO.

## 29/09/2026 (ter): M1, domínio com TDD

- [Spec](specs/2026-09-29-m1-domain-design.md) → [plano](plans/2026-09-29-m1-domain.md) → execução com TDD.
- **Abordagem A:** o domínio decide e aplica (`wagering.Settle` e `wagering.OpenWallet`); o `app` do M3 só fará I/O.
- **Pacotes:** `ident`, `money`, `wallet`, `events`, `wagering` e `apperrors`, com a tabela U do test-plan (exceto o U09b, do M2).
- **Decisões novas** (em `decisions.md`): erro não classificado é transitório; chave de idempotência em ASCII visível; IDs do domínio como string canônica; `eventId` atribuído no `Seal`.
- **Achado na validação do plano:** relógio de outra instância atrás do `createdAt` viraria falha permanente; `updatedAt` passou a ter `createdAt` como piso.

## 29/09/2026 (ter): M2, persistência

- [Spec](specs/2026-09-29-m2-persistence-design.md) → [plano](plans/2026-09-29-m2-persistence.md) → execução com TDD.
- **Entregue:**
  - migrations 000001–000006 com todas as constraints, triggers e grants do data-model;
  - portas em `internal/app`, UoW (`Do` e `Snapshot`) e os 5 repositórios;
  - tradução de erros sem vazar o `Detail` do PostgreSQL;
  - serviço `migrate` no compose.
- **Decisões novas** (spec §2, `decisions.md` D-09 e D-14):
  - corridas de unicidade como sentinelas transitórias;
  - `lock_timeout` por `set_config` na transação inteira;
  - saldo observado na moeda da carteira;
  - ID não canônico → "não encontrado";
  - UoW interrompida pelo `ctx` → transitória.
- **Achados da validação do plano:**
  - o `result_balance_minor` numa rejeição `CURRENCY_MISMATCH` voltaria na moeda errada;
  - o `JSONB` normaliza o payload da outbox;
  - os triggers disparam antes dos `CHECK`, por isso o I02a roda com eles desligados.
- **Prova central:** o I17 grava todos os tipos (abertura, BET, WIN, LOSS, REFUND, ROLLBACK, rejeições e pendência resolvida) pelos repositórios, e o domínio do M1 e o schema concordam em tudo.

## 29/09/2026 (ter): M3, contrato, casos de uso, HTTP e autenticação

- [Spec](specs/2026-09-29-m3-contract-http-auth-design.md) (com o [`api/openapi.yaml`](../../api/openapi.yaml)) → [plano](plans/2026-09-29-m3-contract-http-auth.md) → execução inline com TDD.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano. Depois, o plano foi reaplicado do zero numa segunda cópia, passo a passo (red → green); o resultado ficou idêntico ao validado, e a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - os casos de uso `OpenWallet`, `ProcessWager` (com `FAILED` em UoW separada e retentativa das corridas), `Queries` e `Reconcile`;
  - o `auth`, com go-oidc, a matriz D-07 e o fail fast do JWKS;
  - o `httpapi`, com as 9 rotas, `problem+json`, os middlewares e os docs;
  - o `testkit`: app em processo, contrato validado em toda troca e consistência completa do test-plan §6.
- **Achado central:** 50 apostas iguais em paralelo às vezes geravam um 409 indevido, porque uma entrega confirmava entre as duas leituras de idempotência. Foi corrigido no `lookup`, com teste determinístico (decisão 23 da spec).
- **Outros achados da validação:**
  - a ordem dos middlewares;
  - as filas isoladas passam a ser criadas pelo `StartApp`;
  - a regra do `depguard` do `app` exclui os testes;
  - o go-oidc compara o `exp` sem tolerância, então a tolerância virou `OIDC_CLOCK_SKEW`.
- **Prova final:**
  - `make check` e `make test-integration` verdes, com três execuções estáveis;
  - cinco sabotagens detectadas;
  - compose com as 3 réplicas saudáveis e o fluxo por `curl` distribuído entre elas.

## 29/09/2026 (ter): M4, outbox publisher

- [Spec](specs/2026-09-29-m4-outbox-publisher-design.md) (com o [`api/events.yaml`](../../api/events.yaml)) → [plano](plans/2026-09-29-m4-outbox-publisher.md) → execução inline com TDD.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano, inclusive as versões intermediárias do `publisher.go`; a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - a porta `app.OutboxStore` e o `postgres.OutboxStore` (claim com `SKIP LOCKED` + lease, confirmação e falha condicionais, backlog);
  - o `adapters/outbox`: publisher por grupos, backoff sem descarte, reclaim, espera no claim com o banco fora, stop gracioso e módulo Fx;
  - o ARN do tópico resolvido no start (STS + `GetTopicAttributes`) e a política ajustada;
  - as 6 métricas de outbox;
  - o contrato `api/events.yaml`, o coletor da fila de auditoria e o item 8 da consistência.
- **Achado central:** os testes do `bootstrap` usavam o banco compartilhado `pda` e, com o publisher no grafo, publicaram num tópico de teste os eventos pendentes do ambiente de desenvolvimento (12 eventos do fluxo manual do M3 não chegaram à auditoria). Agora têm banco próprio.
- **Prova final:**
  - `make check` e `make test-integration` verdes, e mais duas execuções estáveis;
  - 6 sabotagens detectadas (entre elas publicar antes do commit, o E8);
  - compose com as 3 réplicas publicando, os eventos de um `POST /wallets` na fila de auditoria e o stop gracioso nos logs.

## 29/09/2026 (ter): M5, consumidor SQS

- [Spec](specs/2026-09-29-m5-sqs-consumer-design.md) → [plano](plans/2026-09-29-m5-sqs-consumer.md) → execução inline com TDD.
- **Escolhas do autor na spec:** caso de uso `app.ConsumeWager` no `app` (e não a orquestração no adapter); pausa por saúde acionada por ping; prazo de processamento menor que o visibility, com liberação por prazo e tempos de teste de 5 s/3 s.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano, inclusive as versões intermediárias do `consumer.go`; a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - o `ConsumeWager` e a inbox em todo caminho de conclusão do `ProcessWager`;
  - o `adapters/sqsconsumer`: envelope e hash, decisão por resultado, DLQ explícita, backoff, grupos em paralelo com ordem no grupo, pausa por saúde, liberação por prazo e shutdown em 5 passos;
  - as 9 métricas de SQS e o módulo Fx;
  - o `testkit` de SQS e IAM, o I04f com as políticas reais e a ponta a ponta pelo SQS.
- **Achado central:** um long polling cancelado pelo cliente continua aberto no MiniStack e esconde, por um visibility timeout, a próxima mensagem que ficar visível (confirmado com uma sonda). Explica uma falha intermitente do teste de shutdown (limitação documentada) e era a causa do flake do I05b do M4, que também falhava na `main`: corrigido no `Audit.Absent`, com um teste que falha 3 de 3 vezes sem a correção.
- **Outros achados:** o I04a passava sem o `DeleteMessage` (a redrive drenava a fila) e agora exige a DLQ vazia; short polling ganhou pausa; wait e visibility em segundos inteiros; as mensagens seguintes de um grupo com a cabeça sempre falhando também chegam à DLQ (FIFO).
- **Prova final:**
  - `make check` verde e `make test-integration` verde três vezes seguidas;
  - 15 sabotagens detectadas;
  - compose com as 3 réplicas consumindo: um BET enviado como `provider-a` processado pelo SQS, eventos publicados com `causationId = messageId`, JSON quebrado na DLQ e o stop ordenado (HTTP → consumidor → publisher).

## 30/09/2026 (qua): M6, worker de referências

- [Spec](specs/2026-09-30-m6-reference-worker-design.md) → [plano](plans/2026-09-30-m6-reference-worker.md) → execução inline com TDD.
- **Escolhas do autor na spec:** itens do lote **em sequência** em cada instância (o paralelismo vem das réplicas) e as decisões 1–7 (claim de um statement, recheck sob os locks, `causationId`, `FAILED` isolado, métricas no marco).
- **Achado da spec:** o recheck confere status **e** horário. Só o status faria duas instâncias contarem a mesma tentativa duas vezes.
- **Plano sem validação prévia:** ao contrário do M3–M5, o código não foi testado numa cópia descartável; o autor pediu cautela na execução, então cada red e cada green foram lidos, e cada teste sobre comportamento existente passou por sabotagem.
- **Entregue:**
  - `app.ResolveReferences` (reusa o `ProcessWager`), `ClaimDue`/`Lock`/`CountPendingReferences` e o `adapters/references` (loop, backoff do claim, stop gracioso, módulo Fx);
  - `REFERENCE_POLL_INTERVAL`, `REFERENCE_BATCH_SIZE` e as 3 métricas de referência;
  - `StartApp(ctx, opts...)`, e os testes I06, I06b, C2/C3 por HTTP, SQS-08 e a concorrência.
- **Achados da execução:**
  - o `TestFxGraph` e o `TestResolveReferences` deram o red por asserção, mas o I06 e o I06b **passavam com a opção do `StartApp` ignorada** (o TTL padrão cabe na janela por sorte). Passaram a afirmar a agenda pedida (TTL longo, atraso lento), e então o red foi real;
  - duas sabotagens **não** eram detectadas pelos testes de mistura (`TestConcurrentWorkers`, `TestWorkerVersusHTTP`): a vítima de um deadlock pode ser o worker, cujo erro é transitório e reprocessado. A ordem carteira → transação ganhou uma prova determinística (`TestResolveReferencesLockOrder`: carteira travada por outra conexão, a avaliação enfileirada não pode segurar a linha da operação) e o recheck já tinha as suas (`Skips`, `Concurrent`);
  - a sabotagem "o consumidor retenta a pendência em vez de apagá-la" não é detectada pelo teste de ponta a ponta (a reentrega cai na inbox como duplicata e é apagada um segundo depois); o `TestDecide` do M5 é a prova do delete imediato;
  - o lint pegou um `switch` não exaustivo e o `contextcheck` num closure de `Eventually`.
- **Prova final:**
  - `make check` verde (`0 issues.`) e `make test-integration` verde três vezes seguidas, 21 pacotes;
  - 13 sabotagens detectadas e 3 não detectadas pelos testes a que se destinavam (as duas de mistura, cobertas por testes determinísticos, e a do SQS, coberta pelo `TestDecide`);
  - compose com as 3 réplicas: REFUND na réplica 1, BET na 2, REFUND resolvido e lido pela 3, reconciliação consistente; expiração real em cerca de 3,5 min com 7 retries distribuídos entre as 3 réplicas (`reference_retries_total` 1+5+1, `reference_expired_total` 1); parada de uma réplica na ordem HTTP → consumidor → publisher → worker.

## 30/09/2026 (qua): M7, observabilidade e papéis do processo

- [Spec](specs/2026-09-30-m7-observability-design.md) → [plano](plans/2026-09-30-m7-observability.md) → execução inline com TDD.
- **Decisão do autor:** o `/health/ready` segue sempre com PostgreSQL + SQS (HTTP-08), qualquer que seja o conjunto de papéis.
- **Entregue:**
  - `config.Roles` e `bootstrap.OptionsFor` (flags de papel), `app.Metrics` ampliada, os coletores que faltavam (`wager_transactions_total`, `concurrency_conflicts_total`, `reconciliation_runs_total`, `auth_failures_total`, `http_*`), a linha `wager concluded` e o WARN da reconciliação sem saldos;
  - testes: U20–U23, I25–I27, I14 e a ordem de parada no I07b.
- **Ajustes ao plano:** `WagerDuplicate` no lugar de `Duplicate` (a `observability.Metrics` já tinha o método do consumidor); sem helper de chaves de log; `version_mismatch` removida; IDs I25–I27 (o I24 já existia); o 503 real com o PostgreSQL parado ficou com o R01.
- **Achados da execução:** o primeiro desenho do I14 só olhava linhas com o id da carteira, e um vazamento do header no log de acesso passaria; passou a varrer todo o log com marcadores únicos. `MetricValue` devolve `int64` para não abrir exceção no `forbidigo` do E3.

## 30/09/2026 (qua): revisão do M7

- O M7 foi executado com o Sonnet; o autor pediu uma revisão do marco inteiro com o Opus, contra a spec e o `delivery-requirements.md`.
- **Achados importantes (corrigidos com TDD):** uma variável de papel inválida derrubava o processo **sem mensagem** (o `fx.NopLogger` do ramo de erro engolia o evento do Fx; o teste lia `app.Err()`, que ninguém vê), e os logs de **falha** do consumidor e do publisher não tinham os IDs do OBS-01 (só `sqsMessageId`/`eventId`).
- **Correções:** o ramo de erro devolve só `fx.Error` (U24 executa o processo real); loggers por mensagem e por evento (U25, U26); log `aws http client closed` afirmado no I07b (FX-05).
- **Documentação:** limitação do healthcheck com `HTTP_ENABLED=false`, I25 descrito como teste de ligação, evidência do OBS-02 corrigida, contagem dupla das pendências no §13.2 e o ajuste 6 na spec do M7.
- [Spec](specs/2026-09-30-m7-review-fixes-design.md) → [plano](plans/2026-09-30-m7-review-fixes.md), aprovados juntos a pedido do autor.

## 30/09/2026 (qua): bancos de teste, `DROP` intermitente

- **Investigação:** o log do PostgreSQL tinha 7 falhas de `permission denied to terminate process` em 6 pacotes, não só no teste de migrations. As outras eram engolidas pelo `cleanup` do `NewEnv`/`NewTestEnv`, que só imprimia: 9 bancos `pda_t_*` órfãos. O código do PostgreSQL 18 (`TerminateOtherDBBackends`) mostrou a causa: com `FORCE`, o `DROP` checa a permissão de encerrar todo processo ligado ao banco, e `pda_owner` não tem `pg_signal_backend`. O autovacuum (medido com log temporário: 43 ações em bancos de teste em 4 execuções) e os backends `pda_app` ainda saindo disparavam a falha. O `DROP` sem `FORCE` encerra o autovacuum sozinho e espera 5 s.
- **Correção:** `DROP` sem `FORCE`; uma sessão `pda_owner` que sobre é encerrada com `pg_terminate_backend` e o `DROP` é repetido; o `cleanup` devolve o erro (`TestMain` sai com 1, `NewTestEnv` usa `tb.Errorf`). Testes I28–I30.
- **Achado da execução:** o plano previa repetir com `FORCE` depois da espera, mas o I29 falhou uma vez assim: durante os 5 s o autovacuum volta ao banco, e o `FORCE` bate na mesma checagem. O `FORCE` saiu de vez (ruling registrado).
- **Limpeza:** os bancos órfãos (os 9 da investigação, mais os deixados pelas falhas de hoje e pelos reds e sabotagens desta correção) foram apagados pelo superusuário do compose, com a lista conferida antes.

## Onde paramos

- **M7 concluído (commits aguardando autorização).** Próximo passo: **M8, harness e2e e cenários multi-instância**, começando pela spec.
- **Pendências em aberto:**
  - confirmar o horário exato da entrega (assumido 01/10);
  - decidir se os 3 minors do M0 entram em algum marco;
  - **flake do `TestMigrationsUpDownUp`: resolvido em 30/09** ([spec](specs/2026-09-30-test-db-drop-design.md)). Não era um flake do teste, mas um defeito do `testkit` que afetava todos os pacotes: o `DROP … WITH (FORCE)` falhava ao acaso, e fora do teste de migrations a falha era silenciosa;
  - minors adiados na revisão do M2: inbox aceita instantes zerados; repositórios sobre o pool podem escrever fora do UoW (só a convenção da D-14 impede). O filtro de ID malformado de `List`/`Sum`/`AdvanceDependents` ficou resolvido no M3, pelos casos de uso (decisão 8);
  - minors adiados na revisão do M3: o log de acesso gravava `route` vazio e o WARN da reconciliação registrava os saldos (ambos resolvidos no M7); o `settleAndPersist` com `insert = false` ganhou teste no M6 (`TestResolveReferences`).
