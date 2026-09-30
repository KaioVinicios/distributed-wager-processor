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

## 30/09/2026 (qua): M8, harness e2e e cenários multi-instância

- [Spec](specs/2026-09-30-m8-e2e-harness-design.md) → [plano](plans/2026-09-30-m8-e2e-harness.md) → execução inline com TDD.
- **Escolhas do autor na spec:** extrair um `Harness` que o `App` e o `Cluster` embutem; um cluster por pacote no `TestMain` (crash em sequência, vazão em paralelo); round-robin só entre instâncias vivas.
- **Entregue:**
  - `internal/faultinject` e os 6 pontos de falha, todos em adaptadores;
  - o `testkit.Harness` (extração sem mudar nenhum teste de integração), o `testkit.Cluster` (build com `-tags faultinject -race`, kill/stop/restart, logs, recusa de data race nos filhos), o `EnvOf` e o `Client.Try`;
  - `test/e2e` com os 16 testes C e os 2 do próprio harness, `make test-e2e` e o job no CI.
- **Achados da execução:**
  - **o C10b do plano não provava nada:** a barreira no envio não faz HTTP e SQS se encontrarem, porque o SQS entrega depois de o HTTP responder; sem o `FOR UPDATE` o teste passava. Virou uma barreira no banco (os dois canais parados no lock da carteira e soltos juntos), e agora a sabotagem é detectada;
  - a sabotagem planejada do C03b (`SHARE ROW EXCLUSIVE`) não bloqueava nada; a certa é `EXCLUSIVE`;
  - três ajustes pequenos: `transactionOf` antecipado (lint `unused`), três `//nolint:gosec` desnecessários, e o `grep -c` sobre binário que no macOS não imprime nada (`grep -ac`).
- **I17 intermitente (fora do M8, corrigido em seguida):** o `TestDomainFlowsPersist` falhou 1 vez na verificação. A asserção da antecipação comparava o `next_attempt_at` com o horário agendado originalmente e perdia a corrida contra o relógio sob carga; reproduzido 3 de 3 com um atraso de 200 ms. Passou a exigir o instante de conclusão do `bet-3` ([spec](specs/2026-09-30-i17-anticipation-flake-design.md) → [plano](plans/2026-09-30-i17-anticipation-flake.md)).
- **Prova:** cada teste C visto falhando pelo motivo certo (os 6 pontos de falha) ou pela sabotagem registrada no `// Sensitivity:`; 4 sabotagens do harness detectadas, inclusive uma data race num processo filho.

## 30/09/2026 (qua): M9, resiliência

- [Spec](specs/2026-09-30-m9-resilience-design.md) → [plano](plans/2026-09-30-m9-resilience.md) → execução inline com TDD.
- **Escolha do autor na spec:** um prazo por requisição HTTP (`HTTP_REQUEST_TIMEOUT`), em vez de mudar o R01. Com o PostgreSQL congelado, o kernel mantém as conexões abertas, e o HTTP esperaria o banco em vez de responder 503.
- **Entregue:**
  - `testkit.Pause`, `Cluster.StopAsync`, `Instance.ReadyStatus`, `Harness.CloseIdleConnections` e o `unpause` no `make infra-up`;
  - `HTTP_REQUEST_TIMEOUT` com o middleware `withDeadline` (U30);
  - `test/e2e/resilience_test.go` com os R01–R04 e o `TestClusterStopAsync`.
- **Prova do achado:** o R01, escrito antes da mudança de produto, falhou exatamente com "no HTTP answer arrived during the outage: the requests waited for the database". Com o prazo no lugar, passou.
- **Achados da execução:**
  - a sabotagem planejada do R02 ("o caminho de falha confirma o evento") **não é detectável**: com o broker congelado, o `Publish` que venceu o prazo é entregue depois do `unpause`. A execução viu 12 falhas reais e todos os eventos na auditoria. A sabotagem virou "backoff sem teto";
  - o Docker marca o container pausado como *unhealthy*, e o `compose unpause a b` falha por inteiro. O `infra-up` passou a fazer `unpause` por serviço e a esperar 3 s;
  - no R03, o envio em bloco por carteira deixava um só grupo no primeiro lote. O envio passou a ser intercalado;
  - o `contextcheck` recusou helpers com `t` dentro do `Eventually` (virou um `within` local), e o gerador de tráfego do R01 continuava chamando `t` depois de um teste que falhou (virou um `defer`).
- **Sensibilidade:** 8 sabotagens detectadas nos 4 testes R (R01: 3, R02: 2, R03: 2, R04: 1) e 1 não detectável por construção (registrada no teste e no `ARCHITECTURE.md` §16).

## 30/09/2026 (qua): pendências menores do M0

- O autor decidiu corrigir, antes do M10, as 3 pendências adiadas no M0, em vez de registrá-las como limitação. [Spec](specs/2026-09-30-m0-minors-design.md) → [plano](plans/2026-09-30-m0-minors.md) → execução inline com TDD.
- **Entregue:**
  - um servidor HTTP (API ou admin) que para sozinho encerra o processo com código 1 pelo `fx.Shutdowner`, depois do stop ordenado (U31, U31b);
  - `restart: on-failure` nas 3 réplicas;
  - eventos do Fx em DEBUG (U32): o log de start de uma réplica caiu de dezenas de linhas com *stacktrace* para 10 linhas da aplicação.
- **Prova:**
  - reds pelo motivo certo (`no shutdown after the server stopped serving`; 47 eventos do Fx em INFO);
  - sensibilidade do U31b (`a normal stop called Shutdown 1 times`);
  - no compose, `kill -QUIT 1` na `app-1` (o runtime do Go sai com 2) e a réplica volta sozinha, *healthy*, com `RestartCount` = 1; um `docker compose kill` não a reinicia.
- **Ajuste da execução:** o `docker compose config` também renderiza a extensão `x-app`, então a contagem de `on-failure` dá 4. A conferência por serviço mostra só as 3 réplicas.

## 30/09/2026 (qua): M10, documentação de entrega

- Roteiro no chat, aprovado pelo autor (M10 é documentação: sem spec nem TDD, [`development-workflow.md`](../development-workflow.md) §2).
- **Entregue:**
  - `README.md`: pré-requisitos, início rápido, serviços e portas, todas as variáveis, filas e credenciais do broker, migrations, identidades de teste, exemplos e testes com tempos, operação e problemas comuns, e o mapa dos entregáveis;
  - `docs/testing.md`;
  - o fecho do `ARCHITECTURE.md` (§16–§18 e a revisão contra o código) e do checklist.
- **Prova:**
  - cada exemplo do README rodou contra o compose: abertura, BET, replay em outra réplica, 422, 409, REFUND antes da BET (202 → resolvido pelo worker), ledger paginado, reconciliação consistente, 401/403/404, BET pelo SQS com as credenciais IAM do `provider-a`, `AccessDenied` ao consumir como provedor, eventos na auditoria e métricas;
  - as simulações de queda do `docs/testing.md` rodaram no compose;
  - `migrate-down`/`migrate-up`.
- **Decisão do autor:** o reprocessamento da DLQ não é exigido pelo desafio. Como o MiniStack não implementa `StartMessageMoveTask`, fica documentado, sem script (D-12).

## 30/09/2026 (qua): M11, verificação a partir de um clone limpo

- Roteiro no chat, aprovado pelo autor. O M11 não tem spec nem TDD: é `verification-before-completion` do começo ao fim ([`development-workflow.md`](../development-workflow.md) §2).
- **"Do zero", na escolha do autor:**
  - `docker compose down -v` do ambiente de desenvolvimento, porque o compose fixa o projeto `pda` e as portas;
  - `git clone` do GitHub (`49c647f`) no scratchpad;
  - `docker compose build --no-cache`;
  - `GOCACHE`/`GOMODCACHE` vazios;
  - as imagens base já baixadas ficaram, mas cada tag fixa foi conferida no registry.
- **Prova:**
  - `up --build --wait` saudável em 44 s;
  - os 4 comandos do desafio com saída 0 (32 s e 36 s frios);
  - `make check` (25 s), `make test-integration` (48 s, 22 pacotes) e `make test-e2e` (140 s), todos com saída 0;
  - a §6 do README passou pelas versões 6→5→6→3→nenhuma→6;
  - 477 links relativos sem quebra e as 8 imagens existentes;
  - `git status --porcelain` vazio no fim e nenhum banco `pda_t_*` sobrando.
- **Método dos exemplos:** os blocos `sh` das §5, §7 e §8 foram extraídos do próprio `README.md` por script e executados sem adaptação, em `bash` e em `zsh`. Um script é mais rápido que uma pessoa digitando, e isso expôs duas dependências de tempo que a execução manual do M10 escondia.
- **Achados, corrigidos pela exceção do §4.4:**
  - o `get-token.sh` não terminava a saída com quebra de linha. O autor aprovou `scripts/*.sh` como exceção;
  - a §8.5 consultava o REFUND antes do worker (~0,3 s);
  - a §8.9 lia as métricas só da `app-1`, e as três linhas do exemplo tinham saído nela por sorte;
  - a §8.9 também passou a avisar que a ordem dos eventos da mesma carteira não é estrita.

  Cada correção foi validada aplicando o diff no clone e rodando os exemplos de novo.

## Onde paramos

- **M11 concluído (commits aguardando autorização).** Todos os marcos M0–M11 estão fechados. Depois do M11, o ambiente de desenvolvimento volta a subir a partir do repositório original (o `aws-init` regenera o `.local/aws/credentials`). Próximo passo: **M12, opcionais**, se houver folga (teste de carga, OpenTelemetry, dashboard).
- **Pendências em aberto:**
  - confirmar o horário exato da entrega (assumido 01/10);
  - **os 3 minors do M0: resolvidos em 30/09** ([spec](specs/2026-09-30-m0-minors-design.md));
  - **flake do `TestMigrationsUpDownUp`: resolvido em 30/09** ([spec](specs/2026-09-30-test-db-drop-design.md)). Não era um flake do teste, mas um defeito do `testkit` que afetava todos os pacotes: o `DROP … WITH (FORCE)` falhava ao acaso, e fora do teste de migrations a falha era silenciosa;
  - minors adiados na revisão do M2: inbox aceita instantes zerados; repositórios sobre o pool podem escrever fora do UoW (só a convenção da D-14 impede). O filtro de ID malformado de `List`/`Sum`/`AdvanceDependents` ficou resolvido no M3, pelos casos de uso (decisão 8);
  - minors adiados na revisão do M3: o log de acesso gravava `route` vazio e o WARN da reconciliação registrava os saldos (ambos resolvidos no M7); o `settleAndPersist` com `insert = false` ganhou teste no M6 (`TestResolveReferences`).
