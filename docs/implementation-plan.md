# Plano de Implementação

Ordem de construção, estrutura do código, marcos diários, riscos e o que cortar se o prazo apertar. Todo o conteúdo técnico já está decidido em [`decisions.md`](decisions.md), [`data-model.md`](data-model.md), [`transaction-lifecycle.md`](transaction-lifecycle.md), [`messaging.md`](messaging.md) e [`test-plan.md`](test-plan.md). Este documento só define a **sequência**.

**Premissa de prazo:** documentação concluída em 28/09 (seg). Entrega até **01/10 (qui)**, a confirmar o horário exato. O plano fecha as funcionalidades em 30/09 e deixa 01/10 para resiliência, documentação final e verificação a partir de um clone limpo.

---

## 1. Estratégia

1. **Eliminatórios primeiro.** Cada marco fecha um conjunto de critérios E1–E10 com teste. Nada opcional começa antes de todos os eliminatórios estarem verdes.
2. **Esqueleto funcionando no primeiro bloco.** Compose, Fx, health e token do Keycloak funcionando antes de qualquer regra de negócio. Isso tira da frente, logo cedo, os dois maiores riscos de infraestrutura: MiniStack e issuer do Keycloak.
3. **Spec → plano → TDD em todo marco aplicável.** Cada marco passa pelo fluxo de [`development-workflow.md`](development-workflow.md): spec (`superpowers:brainstorming`) e plano (`superpowers:writing-plans`) aprovados pelo autor antes da execução, e TDD (`superpowers:test-driven-development`) em todo código aplicável. O prazo reduz escopo, nunca o processo.
4. **Fatias verticais.** O HTTP síncrono vai de ponta a ponta (domínio → banco → API → auth) antes do SQS. Depois, SQS, outbox e worker reutilizam o mesmo caso de uso.
5. **Todo marco termina verificado:** vale a definição de pronto de [`development-workflow.md`](development-workflow.md) §5, com `make check` e os testes com tag do marco verdes e a saída dos comandos como evidência.
6. **Commits:** nenhum commit automático. No fim de cada marco, os commits são propostos e o autor autoriza ([`development-workflow.md`](development-workflow.md) §6).

---

## 2. Estrutura e stack

- **Estrutura de pastas e arquivos, camadas, regras de dependência, módulos Fx e convenções:** [`structure.md`](structure.md).
- **Stack, versões fixadas, conformidade com a stack obrigatória, formatação, lint e alvos do `Makefile`:** [`stack.md`](stack.md).

---

## 3. Marcos

Estimativas em horas de trabalho efetivo, **incluindo a spec e o plano** de cada marco ([`development-workflow.md`](development-workflow.md)). O dia 1 soma cerca de 13,5 h; se apertar, o M3 transborda para o início do dia 2 (ver o checkpoint do dia 1). Os IDs referem-se a `delivery-requirements.md` (requisitos) e `test-plan.md` (testes).

### Dia 1 — 29/09 (ter): fundação, domínio, banco e HTTP síncrono

#### M0 — Esqueleto, qualidade e spike de infraestrutura (~2,5 h) — ✅ concluído em 29/09

- `go mod init github.com/KaioVinicios/pda` com `go 1.27.1`, `Dockerfile` multi-stage (`golang:1.27.1-alpine` → `distroless/static-debian12:nonroot`), `.dockerignore` e `.env.example`.
- **Qualidade desde o primeiro commit:** `.golangci.yml`, `.editorconfig` e os alvos do `Makefile` definidos em [`stack.md`](stack.md) §5. `make check` precisa passar já no esqueleto.
- `docker-compose.yml` com `postgres` (init de roles), `keycloak` (import de `realm-pda.json` e `realm-other.json`), `ministack` (`AUTH=true`), `aws-init` (recursos, usuários IAM e `.local/aws/credentials`), `migrate` e `app-1..3`, todos com healthchecks. `docker compose up --build --wait` sobe tudo.
- Fx com os módulos `config`, `observability`, `postgres` (apenas pool + ping), `aws` (clientes SQS/SNS, verificação das filas no start) e `httpapi` (apenas `/health/live` e `/health/ready`, com PostgreSQL + SQS). Sonda `pda healthcheck` para o healthcheck das réplicas distroless.
- ✅ **Spike do MiniStack** (28/09, [`dev/spike-ministack.md`](dev/spike-ministack.md)): toda a topologia funciona sem plano B, e as políticas IAM são avaliadas com `AUTH=true`. Consequência: o `aws-init` também cria os usuários IAM e o arquivo de credenciais (D-02).
- ✅ **Spike do Keycloak** (28/09, [`dev/spike-keycloak.md`](dev/spike-keycloak.md)): `iss` estável com `KC_HOSTNAME` + backchannel dinâmico; o `realm-pda.json` precisa declarar o scope `roles` sem o mapper `audience resolve` (D-07).
- ✅ **Spike do lint** (28/09, [`dev/spike-lint.md`](dev/spike-lint.md)): os dois itens de [`stack.md`](stack.md) §4.2 foram confirmados.

**Pronto quando:**
- `docker compose up --build` deixa as 3 réplicas prontas;
- `make check` está verde;
- um `curl` obtém um token válido;
- o resultado dos spikes está anotado em `docs/dev/spike-ministack.md` e `docs/dev/spike-keycloak.md`, e as decisões resultantes (plano B, se necessário) estão refletidas em `decisions.md` (§5).

**Cobre:** ART-01..04, ART-06, ART-07, ART-10, HTTP-08, FX-01 (parcial), FX-02, AUTH-09 (parcial). Spec: [`dev/specs/2026-09-28-m0-skeleton-design.md`](dev/specs/2026-09-28-m0-skeleton-design.md).

#### M1 — Domínio com TDD (~3 h) — ✅ concluído em 29/09

- `ident` (UUID canônico; o domínio usa só a stdlib), `money` (U01a–g), `wallet` + `LedgerEntry` (U02, U07), `wagering`: máquina de estados, regras por tipo, ordem de avaliação, resolução R1–R8, catálogo de códigos e hash canônico (U03, U04a–d, U05a–c).
- **O domínio decide e aplica** (abordagem A da spec): `wagering.Settle` (passos 12–15 + R1–R8, movimento pelo agregado, lançamento e eventos) e `wagering.OpenWallet`. O `app` do M3 e o worker do M6 só fazem I/O em volta deles.
- `events`: 4 eventos tipados com interface selada e envelope por `events.Seal` (U08).
- `apperrors` (`Kind` e `Classify`, U09a; erro não classificado é transitório, D-05), o teste de imports do domínio (U10), a rejeição de zero values (U11) e a política de retentativa de referências (U12).
- Achado da validação do plano: `updatedAt` usa `createdAt` como piso, para que um relógio atrasado em outra instância não vire falha permanente.

**Pronto quando:** toda a tabela §5.1 do test-plan está verde com `-race`, exceto o U09b, que é do pacote `postgres` (M2).

**Cobre:** MON-*, DOM-*, WAL-01..07, TX-01..08, LED-01..02, OPS-01..11, OPS-15, IDEM-03..06, OUT-08, OUT-09, OUT-11..13 (vários parciais, completados no banco ou nas bordas; ver a spec). **Eliminatório: E3.** Spec: [`dev/specs/2026-09-29-m1-domain-design.md`](dev/specs/2026-09-29-m1-domain-design.md) · plano: [`dev/plans/2026-09-29-m1-domain.md`](dev/plans/2026-09-29-m1-domain.md).

#### M2 — Persistência (~3 h) — ✅ concluído em 29/09

- Migrations 000001–000006 conforme [`data-model.md`](data-model.md): tabelas, constraints, índices, triggers e grants.
- Adapter `postgres`: pool, `UnitOfWork`, repositórios de wallet, transaction, ledger, outbox e inbox, mapeamento de `Money`, tradução de SQLSTATE e nomes de constraint.
- `testkit.NewEnv` (apenas o banco isolado; as filas entram no M4/M5).
- Tradução de erros do PostgreSQL (U09b).
- Testes I01, I02a–e, I03a, I03b (parcial), I16, I17 (os fluxos do domínio gravados de ponta a ponta), I18 e I19.
- **Entregue também:** as portas de persistência em `internal/app` (`UnitOfWork` com `Do` e `Snapshot`, `Repos`, sentinelas das corridas), o serviço `migrate` no compose e `make migrate-up`/`migrate-down`, `DB_LOCK_TIMEOUT` e `testkit.LedgerProblems` (a parte SQL da verificação de consistência).

**Pronto quando:** `make test-integration` passa com esses testes.

**Cobre:** DB-01..03, DB-05, WAL-03, WAL-06, WAL-07, LED-03..06, TX-05, TX-07, OPS-08, IDEM-07, OUT-01, ART-05; parciais: DB-04 (README no M10), WAL-04, TX-09, IDEM-02, SQS-03. **Eliminatórios: E4 (constraint), E9.** Spec: [`dev/specs/2026-09-29-m2-persistence-design.md`](dev/specs/2026-09-29-m2-persistence-design.md) · plano: [`dev/plans/2026-09-29-m2-persistence.md`](dev/plans/2026-09-29-m2-persistence.md).

#### M3 — Contrato, casos de uso, HTTP e autenticação (~5 h) — ✅ concluído em 29/09

- `app`: `OpenWallet`, `ProcessWagerTransaction` (pipeline do lifecycle §6.1), `GetWallet`, `ListLedger`, `GetTransaction`, `GetTransactionByExternalId` e `Reconcile`. Os casos de uso chamam `wagering.NewCommand`, `CheckIdempotency`, `Settle` e `OpenWallet` e selam os eventos com `events.Seal`.
- **Persistência pronta (M2):** os casos de uso recebem `app.UnitOfWork` e `app.Repos` e seguem a ordem de escrita que os triggers exigem (transação no estado final → saldo → lançamento → outbox), como fazem os helpers do I17. As corridas chegam como sentinelas (`app.ErrIdempotencyRace`, `ErrReversalRace`, `ErrWalletAlreadyExists`), e `ErrNotFound` vira `UNKNOWN_WALLET`, `WALLET_NOT_FOUND` ou `TRANSACTION_NOT_FOUND`, conforme a rota. O I03b se completa aqui (replay com 500). Pendência da revisão do M2: `Ledger.List`/`Sum` e `AdvanceDependents` não filtram ID malformado como `Get`/`Lock`; os casos de uso validam o ID (ou leem a carteira) antes de chamá-los.
- **Tradução de erros do domínio:** o `app` mapeia explicitamente os erros de invariante (`money.ErrOverflow`, `wagering.ErrInvalidSnapshot`, `ErrInvalidArgument`, `ErrInvalidTransition`, `wallet.ErrInvalidLedgerEntry`…) para `apperrors.KindPermanent`, e `*ValidationError`/`*ConflictError` para `KindInput`/`KindConflict`. Sem isso, pela D-05, eles seriam tratados como transitórios.
- `auth`: verificador OIDC com `OIDC_ISSUER` separado de `OIDC_JWKS_URL`, `Principal`, roles e regras da matriz D-07.
- **Spec do marco = contrato primeiro (D-20):** `api/openapi.yaml` escrito e aprovado **antes** dos handlers, junto com `api/requests.http`.
- `httpapi`: as 9 rotas, DTOs, `problem+json`, middlewares de correlação, log e auth, limite de corpo e `DisallowUnknownFields`. Também `GET /docs` (Swagger UI) e `GET /openapi.yaml`.
- `testkit/contract.go`: todo teste HTTP valida requisição e resposta contra o OpenAPI (kin-openapi).
- Testes A01–A04, I08–I12, I15 e o C02 em processo, como verificação antecipada da concorrência.

- **Entregue também:**
  - o `testkit` da integração: o app em processo (`StartApp`), tokens reais e forjados, o validador de contrato em toda troca e o `AssertWalletConsistent` completo (test-plan §6, itens 1–7);
  - C01a e C02 em processo;
  - a métrica `reconciliation_divergences_total`.
- **Achado da validação do plano:** 50 envios iguais em paralelo às vezes produziam um 409 indevido, porque uma entrega confirmava entre as duas leituras de idempotência. O `lookup` trata a transação da mesma chave como replay (decisão 23 da spec).

**Pronto quando:** o fluxo completo por `curl` com token real funciona (abrir carteira → BET → replay → reconciliação), e os testes do marco estão verdes.

**Cobre:** AUTH-01..08, AUTH-10, HTTP-01..07, HTTP-09, IDEM-02, IDEM-08, CONC-01..03, CONC-05, DOC-06, TST-A01..A03, TST-I03; parciais: DOM-06, OBS-02, IDEM-01, IDEM-04, FX-01, TST-C01, TST-C02. **Eliminatórios: E1, E2, E5 (HTTP), E6.** Spec: [`dev/specs/2026-09-29-m3-contract-http-auth-design.md`](dev/specs/2026-09-29-m3-contract-http-auth-design.md) · plano: [`dev/plans/2026-09-29-m3-contract-http-auth.md`](dev/plans/2026-09-29-m3-contract-http-auth.md).

> **Checkpoint do fim do dia 1:** o HTTP síncrono está correto e protegido. Se M3 não fechar, ele passa na frente de tudo no dia 2.

### Dia 2 — 30/09 (qua): mensageria, workers e multi-instância

#### M4 — Outbox publisher (~2,5 h) — ✅ concluído em 29/09

- Adapter `outbox`: o `OutboxRepository` do M2 ganha claim com `SKIP LOCKED` + lease (o payload é `JSONB`: o publisher envia o JSON lido da coluna, idêntico em toda republicação), publicação no SNS FIFO, confirmação condicional, backoff, recuperação de lease e métricas de atraso.
- `testkit`: criação de tópico e fila de auditoria isolados, e leitura da fila de auditoria filtrando por id.
- Testes I05a–e.
- **Pronto desde o M3:** o `app` sela os envelopes (`eventId` UUIDv7) e os grava na outbox na mesma transação. O publisher só lê, publica e confirma.

- **Entregue também:**
  - o contrato formal dos eventos (`api/events.yaml`), validado em toda mensagem lida da fila de auditoria;
  - o item 8 da verificação de consistência: todo evento de cada carteira publicado e entregue com o conteúdo do banco;
  - `testkit.Eventually`, `testkit.NewTestEnv`, `TestAuditCollector` e o ARN do tópico resolvido no start (STS + `GetTopicAttributes`).
- **Achado da validação do plano:** os testes do `bootstrap` usavam o banco compartilhado `pda`; com o publisher no grafo, publicavam os eventos pendentes do ambiente de desenvolvimento num tópico de teste. Eles passaram a ter banco próprio (spec, decisão 20).

**Cobre:** OUT-03, OUT-04, OUT-05, OUT-07, OUT-10; parciais: OUT-02 (inbox no M5), OUT-06 (C05c/C06 no M8), TST-I05 (DLQ no M5), OBS-03, FX-01, FX-03. **Eliminatório: E8.** Spec: [`dev/specs/2026-09-29-m4-outbox-publisher-design.md`](dev/specs/2026-09-29-m4-outbox-publisher-design.md) · plano: [`dev/plans/2026-09-29-m4-outbox-publisher.md`](dev/plans/2026-09-29-m4-outbox-publisher.md).

#### M5 — Consumidor SQS (~3 h) — ✅ concluído em 29/09

- Adapter `sqsconsumer`:
  - pollers, lotes agrupados por `MessageGroupId`, inbox na mesma UoW (o `InboxRepository` e o `app.ErrInboxDuplicate` já existem desde o M2) e `DeleteMessage` após o commit;
  - envio explícito para a DLQ, backoff com `ChangeMessageVisibility` e pausa por saúde;
  - shutdown em 5 passos ([`messaging.md`](messaging.md) §4).
- Testes I04a–f.
- **Pronto desde o M3:** o `ProcessWager` é o caso de uso do consumidor (`Via = SQS`, `CausationID` = `messageId`). O `ProcessRequest` ganha o registro da inbox, gravado na mesma `uow.Do`. O `testkit.StartApp` já cria filas isoladas.

- **Entregue também:**
  - o caso de uso `app.ConsumeWager` (inbox → validação → `ProcessWager`, com a inbox em todo caminho de conclusão e a corrida na PK da inbox recomeçando pelo `Find`);
  - a pausa por saúde acionada por ping, a liberação por prazo (prazo de processamento < visibility) e o short polling com pausa;
  - as 9 métricas de SQS, incluindo `sqs_delete_errors_total`;
  - o `testkit` de SQS e IAM, e o `StartApp` com as filas isoladas.
- **Achado da validação do plano:** um long polling cancelado pelo cliente continua aberto no MiniStack e esconde, por um visibility timeout, a próxima mensagem que ficar visível. Isso explica uma falha intermitente do shutdown (limitação no messaging §4.5) e era a causa do flake do I05b do M4, corrigido no `testkit.Audit.Absent` com um teste que o reproduz (spec, decisões 17 e 18).

**Cobre:** SQS-01..07, SQS-09, SQS-10, AUTH-09 (políticas avaliadas pelo MiniStack com `AUTH=true`, provadas pelo I04f), TST-I04, TST-I05, OUT-02; parciais: SQS-08 (M6), SQS-11 (C10b no M8), TST-C11 (M8), OBS-03, FX-01, FX-03. **Eliminatório: E5 (SQS).** Spec: [`dev/specs/2026-09-29-m5-sqs-consumer-design.md`](dev/specs/2026-09-29-m5-sqs-consumer-design.md) · plano: [`dev/plans/2026-09-29-m5-sqs-consumer.md`](dev/plans/2026-09-29-m5-sqs-consumer.md).

#### M6 — Worker de referências (~1,5 h) — ✅ concluído em 30/09

- Adapter `references`: claim (novo método no `TransactionRepository`, com o `Lock` da transação), lock carteira → transação e chamada ao `wagering.Settle` (reavaliação, reagendamento e expiração já estão no domínio desde o M1); antecipação das pendências dependentes no caso de uso.
- Testes I06 e I11 (reversões completas).
- **Pronto desde o M3:** o worker chama `settleAndPersist(…, insert = false)` do `ProcessWager`, e a política de retentativa (`REFERENCE_*`) já vem do Fx.

- **Entregue também:**
  - o caso de uso `app.ResolveReferences` (reusa o `ProcessWager`), com `Claim`, `Resolve` e o `FAILED` em UoW separada; o `settleAndPersist` passou a receber a causa como função da referência (o `causationId` dos eventos do worker é a referência que os destravou);
  - `TransactionRepository.ClaimDue`, `Lock` e `CountPendingReferences` (nenhuma migration);
  - `adapters/references` (loop em sequência, backoff do claim, stop gracioso) e o módulo Fx;
  - `REFERENCE_POLL_INTERVAL`, `REFERENCE_BATCH_SIZE` e as 3 métricas de referência;
  - `StartApp(ctx, opts...)` no `testkit`.
- **Achado da spec:** o recheck sob os locks confere o status **e** o horário (`next_attempt_at <= now`); só o status faria duas instâncias contarem a mesma tentativa.
- **Achados da execução:** o plano não foi validado antes numa cópia. (1) Os I06/I06b passavam com a opção do `StartApp` ignorada (o TTL padrão cabe na janela por sorte); passaram a afirmar a agenda pedida. (2) Duas sabotagens não eram detectadas pelos testes de mistura (`TestConcurrentWorkers`, `TestWorkerVersusHTTP`): a vítima de um deadlock pode ser o worker, cujo erro é transitório. A ordem carteira → transação ganhou uma prova determinística (`TestResolveReferencesLockOrder`) e o recheck, `TestResolveReferencesSkips`/`Concurrent`.

**Cobre:** OPS-12..14, TX-09 (em processo), SQS-08, TST-I06, I11; parciais: OBS-03, FX-01, FX-03, E7 (a prova com 3 processos é do C08b, M8). Spec: [`dev/specs/2026-09-30-m6-reference-worker-design.md`](dev/specs/2026-09-30-m6-reference-worker-design.md) · plano: [`dev/plans/2026-09-30-m6-reference-worker.md`](dev/plans/2026-09-30-m6-reference-worker.md).

#### M7 — Observabilidade (~1 h) — ✅ concluído em 30/09

- Métricas do catálogo (D-18 + [`messaging.md`](messaging.md) §8), servidor admin `:9090`, campos de log padronizados e a verificação de ausência de segredos nos logs.
- Testes I13, I14 e I07a–c (Fx).

- **Entregue também:**
  - as flags de papel (`HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED`, `REFERENCE_WORKER_ENABLED`) em `bootstrap.OptionsFor(config.Roles)`, com `TestFxRoles`;
  - `app.ErrLockTimeout` (o adapter traduz `55P03` e `40P01`) para o `concurrency_conflicts_total`;
  - a linha `wager concluded` por conclusão (HTTP, SQS e replays);
  - o WARN da reconciliação sem saldos, e o `route` do log de acesso com `unmatched`.
- **Decisão de escopo:** o `/health/ready` continua sempre com PostgreSQL + SQS (HTTP-08). O 503 com o PostgreSQL de fato parado é o R01 (M9).
- **Ajuste:** a label `version_mismatch` do `concurrency_conflicts_total` saiu, porque a estratégia é pessimista e nenhum caminho a produz.
- **Revisão pós-marco (Opus, 30/09):** dois achados corrigidos com TDD: (1) uma variável de papel inválida derrubava o processo sem mensagem (o `fx.NopLogger` engolia o erro; U24); (2) os logs de falha do consumidor e do publisher não tinham os IDs do OBS-01 (U25, U26). Também: log e prova do fechamento dos clientes AWS (FX-05) e ajustes de documentação. Spec: [`dev/specs/2026-09-30-m7-review-fixes-design.md`](dev/specs/2026-09-30-m7-review-fixes-design.md) · plano: [`dev/plans/2026-09-30-m7-review-fixes.md`](dev/plans/2026-09-30-m7-review-fixes.md).

**Cobre:** OBS-01..04, FX-01, FX-03, FX-05; FX-04 em processo (R03 e R04 no M9). Spec: [`dev/specs/2026-09-30-m7-observability-design.md`](dev/specs/2026-09-30-m7-observability-design.md) · plano: [`dev/plans/2026-09-30-m7-observability.md`](dev/plans/2026-09-30-m7-observability.md).

#### M8 — Harness e2e e cenários multi-instância (~3 h) — ✅ concluído em 30/09

- `faultinject` (on/off) e os 6 pontos de falha de [`test-plan.md`](test-plan.md) §4, todos em adaptadores.
- `testkit.Cluster`: build, start, kill, stop e restart; `AssertWalletConsistent` completo (§6 do test-plan) em toda carteira.
- Testes C01–C11 com 3 processos.

- **Entregue também:**
  - o `testkit.Harness`, extraído do `App` (banco, filas, tópico, contrato, `Audit` e os helpers), que o `App` em processo e o `Cluster` embutem; nenhum teste de integração mudou;
  - o round-robin só entre instâncias vivas e desarmadas, o `Client.Try` (resposta que não chega) e o `testkit.EnvOf` (ambiente dos processos derivado da `Config`, com a coluna E2E de §3.3);
  - a recusa de data race e de saída inesperada em qualquer processo filho (TST-C12), o `Restore` dos testes de crash e o job `e2e` no CI;
  - `TestClusterSpreadsRequests` e `TestClusterInstanceLifecycle` (o harness provado por ele mesmo).
- **Decisões da spec:** os cenários de crash usam as flags de papel como bisturi (o componente em teste ligado só na instância com a falha armada), e o ponto `consumer.before_commit` vive no caminho de escrita do `uow.Do`, sem exceção à regra de dependência.
- **Achados da execução:**
  - o C10b planejado **não se encontrava**: a barreira no envio não fazia HTTP e SQS concorrerem, porque o SQS entrega depois de o HTTP responder, e o teste passava sem o `FOR UPDATE`. Passou a usar uma barreira no banco (`raceBehindLock`: os dois canais parados no lock da carteira e soltos juntos) e a rodar sem `t.Parallel()`;
  - a sabotagem planejada para o C03b (`LOCK TABLE … SHARE ROW EXCLUSIVE`) não bloqueava nada, porque esse modo não conflita com o `ROW SHARE` de um `FOR UPDATE`; o lock global realista é `EXCLUSIVE`, detectado;
  - 3 das 4 diretivas `//nolint:gosec` previstas eram desnecessárias (o `nolintlint` as recusou).

**Pronto quando:** `make test-e2e` passa.

**Cobre:** CONC-04, CONC-06, IDEM-01, OUT-06, SQS-11, TX-09, TST-C01..C12. **Eliminatório: E7.** Com isso, **todos os eliminatórios estão cobertos.** Spec: [`dev/specs/2026-09-30-m8-e2e-harness-design.md`](dev/specs/2026-09-30-m8-e2e-harness-design.md) · plano: [`dev/plans/2026-09-30-m8-e2e-harness.md`](dev/plans/2026-09-30-m8-e2e-harness.md).

> **Checkpoint do fim do dia 2:** funcionalidades completas. Dos testes do plano, faltam só os R.

### Dia 3 — 01/10 (qui): resiliência, documentação e entrega

#### M9 — Resiliência (~1,5 h) — ✅ concluído em 30/09

Testes R01–R04: queda do PostgreSQL, queda do SQS e shutdown gracioso com HTTP e SQS.

- **Entregue também:**
  - **o prazo por requisição HTTP** (`HTTP_REQUEST_TIMEOUT`, 10 s; 5 s no e2e), com o middleware `withDeadline` nas rotas autenticadas. Com o PostgreSQL congelado, as conexões TCP ficam abertas e o HTTP esperaria o banco em vez de responder 503 (D-04). O R01 foi escrito antes e falhou exatamente por isso;
  - o `testkit.Pause` (`docker compose pause|unpause`, com `unpause` no `Cleanup`), o `unpause` por serviço no `make infra-up`, o `Cluster.StopAsync`, o `Instance.ReadyStatus` e o `Harness.CloseIdleConnections`.
- **Achados da spec:** a pausa por saúde é **por instância**, então numa queda geral cada instância consumidora pode gastar um recebimento da mensagem antes de pausar. O `maxReceiveCount` precisa superar com folga o número de instâncias (D-12), e o R01 liga o consumidor numa só.
- **Achados da execução:**
  - com o broker congelado, um `Publish` que venceu o prazo no cliente é entregue depois do `unpause`. Por isso, no R02, a sabotagem "o caminho de falha confirma o evento" não é detectável; a do backoff sem teto é;
  - o Docker marca um container pausado como *unhealthy*, e o `up --wait` desiste nele. Além disso, `compose unpause a b` falha por inteiro se um dos dois não está pausado;
  - no R03, 30 mensagens enviadas em bloco por carteira deixavam o primeiro lote com um só grupo, e o MiniStack não entregou os outros grupos ao segundo poller. O envio passou a ser intercalado.

**Cobre:** FX-04, F6, SQS-07 e SQS-09 com 3 processos, OUT-04 e HTTP-08 com a queda real. Spec: [`dev/specs/2026-09-30-m9-resilience-design.md`](dev/specs/2026-09-30-m9-resilience-design.md) · plano: [`dev/plans/2026-09-30-m9-resilience.md`](dev/plans/2026-09-30-m9-resilience.md).

#### M10 — Documentação de entrega (~2,5 h) — ✅ concluído em 30/09

- `README.md` (DOC-01, DOC-05): pré-requisitos, variáveis, filas, migrations up/down, execução, exemplos `curl` com a obtenção do token, e comandos de teste com tempos aproximados.
- `ARCHITECTURE.md` (DOC-02, DOC-03): **já existe desde 28/09 como documento vivo**, mantido a cada marco. No M10 resta fechar as §16 (limitações) e §17 (trabalho não concluído) e fazer uma revisão final contra a implementação.
- `docs/testing.md` (DOC-04): preparação de dependências, integração, multi-instância e simulações de falha, incluindo as build tags.
- Atualização final do checklist de `delivery-requirements.md`.
- **Pendências do M9:**
  - o README lista `HTTP_REQUEST_TIMEOUT` (padrão 10 s; `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30 s`) entre as variáveis;
  - o `docs/testing.md` avisa que `make test-integration` e `make test-e2e` não rodam ao mesmo tempo, porque os testes R pausam o PostgreSQL e o MiniStack compartilhados, e explica o `unpause` do `make infra-up` depois de uma execução interrompida (test-plan §3.4).

- **Antes do M10:** as 3 pendências menores do M0 foram corrigidas por decisão do autor, com spec, plano e TDD ([spec](dev/specs/2026-09-30-m0-minors-design.md) · [plano](dev/plans/2026-09-30-m0-minors.md)):
  - um servidor HTTP que para sozinho encerra o processo com código 1;
  - `restart: on-failure` nas réplicas;
  - eventos do Fx em DEBUG.
- **Entregue:**
  - `README.md` completo;
  - `docs/testing.md`;
  - `ARCHITECTURE.md` revisado contra o código, com a nota de topo, a tabela do §13.2, as limitações (18 itens), o trabalho não concluído e o mapa da documentação fechados;
  - o checklist fechado: só os ⭐ e o ART-11 (M11) continuam abertos.
- **Verificação dentro do marco:**
  - todos os exemplos do README executados contra o compose, com uma chamada por réplica;
  - `make migrate-down N=1` e `make migrate-up` (versão 6 → 5 → 6);
  - as simulações manuais de queda do PostgreSQL e do MiniStack, e a ordem de parada no log;
  - tempos medidos: unitários ~13 s, integração ~47 s e e2e ~133 s, com a infraestrutura de pé;
  - por script, todo teste e toda métrica citados no `ARCHITECTURE.md` existem no código (26 métricas).
- **Achados:**
  - o MiniStack 1.5.18 não implementa `StartMessageMoveTask`. O reprocessamento da DLQ passou a ser o reenvio pelo produtor, sem script (D-12, messaging §4.4, limitação 17);
  - a tabela de métricas do §13.2 estava partida em duas por uma linha em branco;
  - no `README`, o `unpause` precisa ser um serviço por comando (o achado do M9 também vale para o uso manual).

#### M11 — Verificação a partir de um clone limpo (~1 h)

1. Fazer `git clone` do repositório em um diretório temporário.
2. Executar `docker compose up --build`.
3. Rodar os exemplos do README.
4. Rodar `go test ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`, `make test-integration` e `make test-e2e`.
5. Tudo o que falhar é corrigido ou documentado.

#### M12 — Folga / opcionais (restante)

Só se M9–M11 estiverem verdes. Por ordem: teste de carga (test-plan §9), tracing com OpenTelemetry e dashboard.

---

## 4. Ordem de corte (se o prazo apertar)

Cortar **de cima para baixo**. Cada item cortado vai para "trabalho não concluído" no `ARCHITECTURE.md`.

| # | Corte | Impacto | Substituto |
| --- | --- | --- | --- |
| 1 | M12 (carga, OTel, dashboard) | Só diferenciais | — |
| 2 | R02 e R04 | Menos evidência de resiliência | R01 e R03 continuam |
| 3 | Realm `other` e `no-audience-client` | Menos casos negativos de token | Assinatura forjada e expiração continuam |
| 4 | Lotes agrupados por `MessageGroupId` no consumidor | Menos paralelismo por instância | Processar em sequência por poller, que continua correto |
| 5 | Triggers adiadas `wallet_balance_has_ledger` e `wager_tx_processed_has_ledger` | Menos defesa em profundidade | O trigger `ledger_matches_wallet` e o UoW continuam |
| 6 | I14 e I09 como testes separados | Menos cobertura de observabilidade e paginação | Validação manual registrada no README |

**Nunca cortar:** qualquer item que sustente E1–E10, a verificação de consistência do test-plan §6, o README reproduzível e o `ARCHITECTURE.md`.

---

## 5. Riscos

| Risco | Sinal | Mitigação |
| --- | --- | --- |
| ~~O MiniStack não suporta SNS FIFO ou a assinatura FIFO~~ | ✅ Descartado no spike do M0 | — |
| ~~Redrive ou `ChangeMessageVisibility` com comportamento diferente no MiniStack~~ | ✅ Descartado no spike do M0 (o I04d continua sendo a prova) | — |
| `iss` divergente entre host e container | 401 com um token válido | ✅ Validado no spike: `KC_HOSTNAME` + `KC_HOSTNAME_BACKCHANNEL_DYNAMIC` + `OIDC_ISSUER`/`OIDC_JWKS_URL` separados, sem discovery (D-07) |
| Chaves IAM aleatórias no emulador | App sem credenciais válidas depois de recriar o MiniStack | O `aws-init` reescreve `.local/aws/credentials` a cada execução, e as réplicas dependem dele (`service_completed_successfully`) |
| Keycloak lento para subir (30–60 s) | Timeout no `compose up` | Healthcheck em `/health/ready` do Keycloak, `--wait` e `start_period` generoso |
| Testes de concorrência instáveis | Falhas intermitentes | Barreira de largada, `Eventually` com prazo e repetição N× com carteiras novas (test-plan §1) |
| ~~Deadlock entre o worker de referências e o HTTP~~ | `40P01` nos testes | ✅ Tratado no M6: ordem fixa de lock, carteira → transação ([`data-model.md`](data-model.md) §6), provada por `TestResolveReferencesLockOrder` |
| Relógios divergentes entre instâncias | `updated_at < created_at` rejeitado pelo banco ou na reidratação | ✅ Tratado no M1: `updatedAt` tem `createdAt` como piso (`TestTransitionClockSkew`, `TestWalletDebit`) |
| ~~Erro de invariante do domínio tratado como transitório (D-05)~~ | 503 repetido em vez de `FAILED` | ✅ Tratado no M3: `domainError` traduz todo erro do domínio (U13), e um overflow de crédito vira `FAILED` (I21) |
| ~~Corrida entre as duas leituras de idempotência~~ | 409 indevido com a mesma chave | ✅ Tratado no M3: a transação da mesma chave é replay (I22, C01a) |
| ~~Domínio e schema divergirem (ordem de escrita, coerência ledger × transação)~~ | `PDA04` nos fluxos reais | ✅ Descartado no M2: o I17 grava todos os tipos pelo caminho real e passa pelos triggers |
| Payload da outbox comparado byte a byte | Republicação "diferente" do `MarshalJSON` | `JSONB` normaliza o texto; o contrato é o JSON lido da coluna (data-model §3.5), e os testes comparam como JSON |
| ~~`time.Duration` codificado como `interval` no pgx~~ | Erro no claim ou na falha | ✅ Descartado no M4: o pgx codifica sem ajuste (`TestOutboxStore`) |
| ~~Testes publicando os eventos do ambiente de desenvolvimento~~ | Eventos do banco `pda` somem da auditoria | ✅ Tratado no M4: todo teste que sobe o publisher tem banco próprio |
| ~~Flake do I05b (`TestNoPublishBeforeCommit`)~~ | Evento "não entregue" em 1 de 4 execuções completas | ✅ Tratado no M5: o `Audit.Absent` cancelava um long poll no meio, e o poll órfão pegava o evento (`TestAuditAbsentLeavesNoPollBehind`) |
| Long poll órfão no shutdown do consumidor | Mensagem liberada volta só depois de um visibility timeout | Aceito e documentado (messaging §4.5): sem perda nem duplicidade |
| Duas instâncias contam a mesma tentativa de uma pendência | Expiração antes do limite | ✅ Tratado no M6: o recheck sob os locks confere status e horário (`TestResolveReferencesSkips`, `TestResolveReferencesConcurrent`) |
| Deadlock entre o worker e o HTTP por ordem de lock invertida | `40P01` ou lock timeout | ✅ Tratado no M6: carteira → transação, provado por `TestResolveReferencesLockOrder` |
| ~~`TestMigrationsUpDownUp` falha de forma intermitente~~ | `permission denied to terminate process (42501)` ao derrubar o banco de teste | ✅ Tratado em 30/09: o `DROP … WITH (FORCE)` checa a permissão de encerrar **todo** processo ligado ao banco, e `pda_owner` não tem `pg_signal_backend`; um worker de autovacuum ou um backend `pda_app` ainda saindo fazia o `DROP` falhar, e fora do teste de migrations a falha era silenciosa (bancos órfãos). O `testkit` passou a usar `DROP` sem `FORCE` e o `cleanup` devolve o erro (I28–I30; [spec](dev/specs/2026-09-30-test-db-drop-design.md)) |
| ~~`TestDomainFlowsPersist` (I17) intermitente~~ | `refund-2 was not advanced` sob carga (1 em 4 execuções completas da integração, na verificação do M8) | ✅ Tratado em 30/09: a asserção comparava a antecipação com o horário agendado originalmente, uma corrida contra o relógio; passou a exigir o instante de conclusão do `bet-3`, que é o que o `AdvanceDependents` grava ([spec](dev/specs/2026-09-30-i17-anticipation-flake-design.md)) |
| ~~HTTP pendurado com o banco congelado~~ | Requisição sem resposta durante uma queda do PostgreSQL | ✅ Tratado no M9: `HTTP_REQUEST_TIMEOUT` responde 503 com `Retry-After` (R01, `TestEdgeRequestDeadline`) |
| Queda geral do banco gastando recebimentos em várias instâncias | Mensagem válida na DLQ depois da volta do banco | Aceito e documentado no M9: a pausa é por instância, e o `maxReceiveCount` (10) supera com folga as 3 réplicas (messaging §4.3) |
| Estouro de prazo | Checkpoint do dia não atingido | Ordem de corte (§4), sempre preservando os eliminatórios |

---

## 6. Rastreabilidade: marcos × eliminatórios

| Eliminatório | Marco que implementa | Marco que comprova |
| --- | --- | --- |
| E1 Autenticação efetiva | M3 ✅ | M3 (A01a, A01b) |
| E2 Acesso não autorizado | M3 ✅ | M3 (A02a–c, A03) |
| E3 Ponto flutuante | M1 ✅ | M1 (U01a–g, com o U01g analisando a AST) |
| E4 Saldo negativo por concorrência | M2 ✅ (constraint) + M3 | M2 (I02a, `CHECK`), M3 (C02 em processo), M8 ✅ (C02 com 3 processos, C10b entre canais) |
| E5 Movimentação duplicada | M3 ✅ (HTTP) + M5 ✅ | M3 (C01a em processo, I22), M5 (I04a), M8 ✅ (C01a/b, C05a–c, C10a/b) |
| E6 Idempotência só em memória | M2 ✅ (índices únicos) + M3 ✅ | M2 (I02a, I18), M3 (I10, I21), M6 (I06), M8 ✅ (C05c, C08a) |
| E7 Dependência de instância única | M3–M6 ✅ | M6 (dois workers em processo), M8 ✅ (cluster com 3 processos: C01–C10, C03b, C06a, C08b) |
| E8 Publicação antes do commit | M4 ✅ | M4 (I05b) |
| E9 Ledger auditável | M2 ✅ | M2 (I02b, I02c, I17 com `LedgerProblems`) + test-plan §6 |
| E10 Mocks no lugar da infraestrutura | M0 (infraestrutura real) | Todos os marcos com testes de integração e e2e |
