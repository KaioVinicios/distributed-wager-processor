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

#### M4 — Outbox publisher (~2,5 h)

- Adapter `outbox`: o `OutboxRepository` do M2 ganha claim com `SKIP LOCKED` + lease (o payload é `JSONB`: o publisher envia o JSON lido da coluna, idêntico em toda republicação), publicação no SNS FIFO, confirmação condicional, backoff, recuperação de lease e métricas de atraso.
- `testkit`: criação de tópico e fila de auditoria isolados, e leitura da fila de auditoria filtrando por id.
- Testes I05a–e.
- **Pronto desde o M3:** o `app` sela os envelopes (`eventId` UUIDv7) e os grava na outbox na mesma transação. O publisher só lê, publica e confirma.

**Cobre:** OUT-*, ART-06 (parcial). **Eliminatório: E8.**

#### M5 — Consumidor SQS (~3 h)

- Adapter `sqsconsumer`:
  - pollers, lotes agrupados por `MessageGroupId`, inbox na mesma UoW (o `InboxRepository` e o `app.ErrInboxDuplicate` já existem desde o M2) e `DeleteMessage` após o commit;
  - envio explícito para a DLQ, backoff com `ChangeMessageVisibility` e pausa por saúde;
  - shutdown em 5 passos ([`messaging.md`](messaging.md) §4).
- Testes I04a–f.
- **Pronto desde o M3:** o `ProcessWager` é o caso de uso do consumidor (`Via = SQS`, `CausationID` = `messageId`). O `ProcessRequest` ganha o registro da inbox, gravado na mesma `uow.Do`. O `testkit.StartApp` já cria filas isoladas.

**Cobre:** SQS-*, AUTH-09 (políticas avaliadas pelo MiniStack com `AUTH=true`, provadas pelo I04f). **Eliminatório: E5 (SQS).**

#### M6 — Worker de referências (~1,5 h)

- Adapter `references`: claim (novo método no `TransactionRepository`, com o `Lock` da transação), lock carteira → transação e chamada ao `wagering.Settle` (reavaliação, reagendamento e expiração já estão no domínio desde o M1); antecipação das pendências dependentes no caso de uso.
- Testes I06 e I11 (reversões completas).
- **Pronto desde o M3:** o worker chama `settleAndPersist(…, insert = false)` do `ProcessWager`, e a política de retentativa (`REFERENCE_*`) já vem do Fx.

**Cobre:** OPS-12..14, TX-09.

#### M7 — Observabilidade (~1 h)

- Métricas do catálogo (D-18 + [`messaging.md`](messaging.md) §8), servidor admin `:9090`, campos de log padronizados e a verificação de ausência de segredos nos logs.
- Testes I13, I14 e I07a–c (Fx).

**Cobre:** OBS-01..04, FX-*.

#### M8 — Harness e2e e cenários multi-instância (~3 h)

- `faultinject` (on/off) e os pontos de falha de [`test-plan.md`](test-plan.md) §4.
- `testkit.Cluster`: build, start, kill, stop e restart; `AssertWalletConsistent` completo (§6 do test-plan).
- Testes C01–C11.

**Pronto quando:** `make test-e2e` passa.

**Cobre:** CONC-04, CONC-06, TST-C*. **Eliminatório: E7.** Com isso, **todos os eliminatórios estão cobertos.**

> **Checkpoint do fim do dia 2:** funcionalidades completas. Dos testes do plano, faltam só os R.

### Dia 3 — 01/10 (qui): resiliência, documentação e entrega

#### M9 — Resiliência (~1,5 h)

Testes R01–R04: queda do PostgreSQL, queda do SQS e shutdown gracioso com HTTP e SQS.

#### M10 — Documentação de entrega (~2,5 h)

- `README.md` (DOC-01, DOC-05): pré-requisitos, variáveis, filas, migrations up/down, execução, exemplos `curl` com a obtenção do token, e comandos de teste com tempos aproximados.
- `ARCHITECTURE.md` (DOC-02, DOC-03): **já existe desde 28/09 como documento vivo**, mantido a cada marco. No M10 resta fechar as §16 (limitações) e §17 (trabalho não concluído) e fazer uma revisão final contra a implementação.
- `docs/testing.md` (DOC-04): preparação de dependências, integração, multi-instância e simulações de falha, incluindo as build tags.
- Atualização final do checklist de `delivery-requirements.md`.

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
| Deadlock entre o worker de referências e o HTTP | `40P01` nos testes | Ordem fixa de lock: carteira → transação ([`data-model.md`](data-model.md) §6) |
| Relógios divergentes entre instâncias | `updated_at < created_at` rejeitado pelo banco ou na reidratação | ✅ Tratado no M1: `updatedAt` tem `createdAt` como piso (`TestTransitionClockSkew`, `TestWalletDebit`) |
| ~~Erro de invariante do domínio tratado como transitório (D-05)~~ | 503 repetido em vez de `FAILED` | ✅ Tratado no M3: `domainError` traduz todo erro do domínio (U13), e um overflow de crédito vira `FAILED` (I21) |
| ~~Corrida entre as duas leituras de idempotência~~ | 409 indevido com a mesma chave | ✅ Tratado no M3: a transação da mesma chave é replay (I22, C01a) |
| ~~Domínio e schema divergirem (ordem de escrita, coerência ledger × transação)~~ | `PDA04` nos fluxos reais | ✅ Descartado no M2: o I17 grava todos os tipos pelo caminho real e passa pelos triggers |
| Payload da outbox comparado byte a byte | Republicação "diferente" do `MarshalJSON` | `JSONB` normaliza o texto; o contrato é o JSON lido da coluna (data-model §3.5), e os testes comparam como JSON |
| Estouro de prazo | Checkpoint do dia não atingido | Ordem de corte (§4), sempre preservando os eliminatórios |

---

## 6. Rastreabilidade: marcos × eliminatórios

| Eliminatório | Marco que implementa | Marco que comprova |
| --- | --- | --- |
| E1 Autenticação efetiva | M3 ✅ | M3 (A01a, A01b) |
| E2 Acesso não autorizado | M3 ✅ | M3 (A02a–c, A03) |
| E3 Ponto flutuante | M1 ✅ | M1 (U01a–g, com o U01g analisando a AST) |
| E4 Saldo negativo por concorrência | M2 ✅ (constraint) + M3 | M2 (I02a, `CHECK`), M3 (C02 em processo), M8 (C02, C10b) |
| E5 Movimentação duplicada | M3 ✅ (HTTP) + M5 | M3 (C01a em processo, I22), M5 (I04a), M8 (C01, C05, C10) |
| E6 Idempotência só em memória | M2 ✅ (índices únicos) + M3 ✅ | M2 (I02a, I18), M3 (I10, I21), M6 (I06), M8 (C08) |
| E7 Dependência de instância única | M3–M6 | M8 (cluster com 3 processos) |
| E8 Publicação antes do commit | M4 | M4 (I05b) |
| E9 Ledger auditável | M2 ✅ | M2 (I02b, I02c, I17 com `LedgerProblems`) + test-plan §6 |
| E10 Mocks no lugar da infraestrutura | M0 (infraestrutura real) | Todos os marcos com testes de integração e e2e |
