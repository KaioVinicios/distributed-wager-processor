# M2 — Persistência: design

**Data:** 29/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor e implementada em 29/09/2026

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M2;
- [`data-model.md`](../../data-model.md) §3–§7 (tabelas, triggers, grants, consultas críticas e migrations);
- D-09 (lock e `lock_timeout`), D-14 (fronteira transacional), D-16 (consultas do ledger) e D-17 (proteções no banco);
- [`test-plan.md`](../../test-plan.md) §3.2 (banco isolado) e §5: U09b, I01, I02a–e, I03a, I03b (parcial), I16 e I17–I19.

Esta spec registra só o **delta** em relação a `docs/`. O que já está decidido lá não é repetido.

---

## 1. Objetivo e critério de pronto

**Objetivo:** o schema completo, com todas as invariantes do banco, e o adapter `postgres` que o `app` do M3 e o consumidor do M5 vão usar. O domínio do M1 e o schema precisam concordar, e isso é provado com os fluxos reais gravados através dos triggers.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde, com os testes da §8 vistos falhando pelo motivo certo e depois passando.
3. `docker compose up --build --wait` sobe com o serviço `migrate` e as 3 réplicas saudáveis; `make migrate-down N=1` e `make migrate-up` funcionam.
4. Requisitos da §9 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Escopo (decidido com o autor em 29/09):** a persistência que o **M3 e o M5** consomem.

**Fora do escopo** (com o marco de destino):
- casos de uso, `Clock` e `IDGenerator` (M3);
- claim, ack, fail e lease da outbox (M4);
- `Lock` da transação e claim das pendências com `SKIP LOCKED` (M6);
- os trechos do I03b que dependem de caso de uso: replay com 500 (M3) e DLQ com `INTERNAL_PERMANENT_FAILURE` (M5);
- as partes 1 e 7 da verificação de consistência (test-plan §6), que dependem da API e da matriz de eventos (M3).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **As portas de persistência nascem no M2**, em `internal/app/ports.go`, com as sentinelas em `internal/app/errors.go`. `Clock`, `IDGenerator` e os casos de uso ficam no M3 | A assinatura `uow.Do(ctx, func(app.Repos) error)` obriga o adapter a conhecer as portas. As portas continuam pertencendo a quem as consome (`structure.md` §2) |
| 2 | **Os repositórios recebem e devolvem tipos do domínio** e reidratam com `wallet.Rehydrate` e `wagering.Rehydrate`. Uma linha que o domínio recusa vira `KindPermanent` | O `app` só faz I/O (abordagem A do M1). Dado corrompido é bug ou adulteração, nunca algo que um retry corrige |
| 3 | **Violações de unicidade que o `app` trata viram sentinelas** do `app`, embrulhadas num `apperrors.Error` (abordagem A, escolhida pelo autor em 29/09). As corridas (`ErrIdempotencyRace`, `ErrReversalRace`, `ErrInboxDuplicate`) são `KindTransient` | O `app` usa `errors.Is` e nunca vê nome de constraint nem SQLSTATE. Se um caminho esquecer de interceptar, o retry cai na releitura e termina em replay ou duplicata, nunca em 409 indevido nem em `FAILED` |
| 4 | **`lock_timeout` na transação inteira**, no início do `Do`, por `SELECT set_config('lock_timeout', $1, true)`, com o valor de `DB_LOCK_TIMEOUT` (padrão 5 s) | `SET LOCAL` não aceita parâmetro; `set_config(..., true)` é o equivalente parametrizável. Aplicado no início, também protege inbox, outbox e a antecipação de pendências |
| 5 | **`UnitOfWork.Snapshot`**: transação `REPEATABLE READ READ ONLY`, sem `lock_timeout` | A reconciliação precisa de um snapshot único (D-16); uma escrita acidental falha com `25006`, classificado como permanente |
| 6 | **Leituras fora de transação**: o módulo Fx fornece um `app.Repos` sobre o pool, além do `app.UnitOfWork` | D-14. Os mesmos repositórios servem aos dois casos por meio de uma interface interna `querier`, atendida por `pgx.Tx` e por `*pgxpool.Pool` |
| 7 | **`AdvanceDependents` grava `updated_at = GREATEST($now, created_at)`** e recebe `now` do Go, em vez do `now()` do banco do data-model §6 | Mesmo piso de relógio do M1: `wagering.Rehydrate` recusa `updated_at < created_at`, e o relógio de outra instância pode estar atrasado |
| 8 | **`outbox_events.next_attempt_at = now()` do banco** no `INSERT` | O claim do M4 compara com o `now()` do banco; o mesmo relógio nos dois lados evita atraso artificial por diferença de relógio |
| 9 | **Migrations só pelo serviço `migrate`** do compose (e pelo `testkit` nos testes). A aplicação não migra no start | Uma réplica nunca executa DDL (roles do D-17); as 3 réplicas não disputam a migração |
| 10 | **O `*pgconn.PgError` não atravessa o adapter**: o erro traduzido leva só SQLSTATE, constraint, sentinela e `Kind` | O `Detail` do PostgreSQL contém a linha inteira, e registrá-lo violaria o CHALLENGE §12 (sem payloads financeiros completos no log). Achado da revisão contra o `CHALLENGE.md` |
| 11 | **Erros do domínio na escrita e na leitura do adapter são `KindPermanent`** (`Snapshot()` recusado, `Rehydrate` recusado) | Pela D-05, um erro não classificado é transitório; aqui ele é sempre bug ou dado corrompido, e retry não resolve |
| 12 | **`result_balance_minor` é reconstruído na moeda da carteira**: as leituras de transação fazem `JOIN wallets` | A coluna não tem moeda própria, e numa rejeição `CURRENCY_MISMATCH` o saldo observado é o da carteira, não o da operação. Usar a moeda da operação trocaria a moeda do replay. Achado da validação do plano |
| 13 | **`Get` e `Lock` com ID que não é UUID canônico devolvem `ErrNotFound`** sem ir ao banco | Um path param malformado não pode virar `22P02`, que a tabela da §4.4 classifica como permanente (500). "Não existe essa linha" é a resposta verdadeira. Achado da validação do plano |
| 14 | **Uma UoW encerrada pelo `ctx` é transitória**: se o `ctx` terminou, o erro leva `context.Canceled`/`DeadlineExceeded` na cadeia e sai como `KindTransient` | Nada foi confirmado (ou o resultado do commit é desconhecido), e o caminho de idempotência torna o retry seguro (DOM-06). Evita que um erro secundário do cancelamento seja lido como permanente |

### 2.1 Conformidade com o `CHALLENGE.md`

Revisada a pedido do autor antes da aprovação:

| Regra | Como o M2 a atende |
| --- | --- |
| §4: transações, locks e constraints explícitos e verificáveis; documentar biblioteca, mapeamento de `Money` e delimitação da transação | SQL à mão com `FOR UPDATE`, `lock_timeout` e constraints nomeadas; `uow.Do` visível na assinatura; `ARCHITECTURE.md` §3 no encerramento (DB-05) |
| §4: Fx compõe conexões e repositórios | O módulo `postgres` fornece `app.UnitOfWork` e `app.Repos`; I07a resolve os dois |
| §4: domínio independente de persistência | O adapter depende do domínio, nunca o contrário; U10 e `depguard` seguem valendo |
| §5.3 e §5.8: invariantes, unicidade, não negatividade e imutabilidade do ledger no banco | Migrations do data-model §3–§5; I02a–e provam cada uma |
| §5.5: ledger append-only | Trigger `PDA01` + sem grant de `UPDATE`/`DELETE`/`TRUNCATE`; I02b como app **e** como owner |
| §5.6: sem lock global | Só a linha da carteira é travada; o `lock_timeout` é por transação |
| §5.7 e §6.2: sem lost update, versão só com mudança de saldo | `UPDATE … WHERE version = $new - 1` + trigger `PDA03`; I02e e I19 |
| §6: I/O com `context` e respeito a cancelamento | Todo método recebe `ctx`; I16 |
| §6: criação × reidratação; valores não inicializados rejeitados | Leitura só por `Rehydrate`; escrita só por `Snapshot()`, com zero value recusado como permanente (decisão 11) |
| §6.1: persistência exata de valor e moeda | `BIGINT` + `CHAR(3)`; I18 compara ida e volta |
| §6.3: `PENDING` com retomada durável | `status` sem `PENDING` + `wager_tx_pending_schedule`; I17 grava `PENDING_REFERENCE` → `PROCESSED` |
| §6.4 e §7: lançamento único, `LOSS`/rejeição sem lançamento, uma reversão por referência | `ledger_wallet_tx_uq`, trigger `PDA04`, `wager_tx_single_reversal_uq`; I02a, I02c |
| §6.5 e §11: inbox com identidade, hash, recebimento e conclusão; outbox com retry e snapshot imutável | Colunas do data-model §3.4–§3.5, trigger `PDA05`; inbox e outbox no mesmo `Repos` da transação |
| §9: reconciliação sobre visão consistente | `uow.Snapshot` (`REPEATABLE READ READ ONLY`) + `Ledger.Sum` |
| §12: sem payloads financeiros completos no log | O `*pgconn.PgError` (com o `Detail` da linha) não sai do adapter (decisão 10) |
| §13: integração real com migrations, constraints, imutabilidade e atomicidade | PostgreSQL real; o único dublê é o decorador do I03a, que provoca uma falha pontual (test-plan §1) |
| §15: migrations com aplicação e reversão documentadas | Serviço `migrate` + `make migrate-up`/`migrate-down`; README no M10 (DB-04 parcial até lá) |

---

## 3. Portas (`internal/app`)

```go
// ports.go
type UnitOfWork interface {
    // Do runs fn in one READ COMMITTED transaction with lock_timeout set:
    // commit when fn returns nil, rollback on error or panic (D-14).
    Do(ctx context.Context, fn func(Repos) error) error
    // Snapshot runs fn in a REPEATABLE READ READ ONLY transaction (D-16).
    Snapshot(ctx context.Context, fn func(Repos) error) error
}

type Repos interface {
    Wallets() WalletRepository
    Transactions() TransactionRepository
    Ledger() LedgerRepository
    Outbox() OutboxRepository
    Inbox() InboxRepository
}

type WalletRepository interface {
    Insert(ctx context.Context, w wallet.Wallet) error
    Lock(ctx context.Context, id string) (wallet.Wallet, error) // SELECT … FOR UPDATE
    Get(ctx context.Context, id string) (wallet.Wallet, error)
    UpdateBalance(ctx context.Context, w wallet.Wallet) error   // WHERE id = $1 AND version = $new - 1
}

type TransactionRepository interface {
    Insert(ctx context.Context, t *wagering.WagerTransaction) error
    Update(ctx context.Context, t *wagering.WagerTransaction) error // only state columns
    Get(ctx context.Context, id string) (*wagering.WagerTransaction, error)
    FindByIdempotencyKey(ctx context.Context, providerID, key string) (*wagering.WagerTransaction, error)
    FindByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)
    FindReference(ctx context.Context, providerID, referenceExternalID string) (wagering.Reference, error)
    AdvanceDependents(ctx context.Context, providerID, externalID string, now time.Time) (int64, error)
}

type LedgerRepository interface {
    Insert(ctx context.Context, e wallet.LedgerEntry) error
    List(ctx context.Context, walletID string, afterVersion int64, limit int) ([]wallet.LedgerEntry, error)
    Sum(ctx context.Context, walletID string) (LedgerSum, error)
}

type LedgerSum struct {
    NetMinor int64 // Σ CREDIT − Σ DEBIT
    Entries  int64
}

type OutboxRepository interface {
    Insert(ctx context.Context, envs ...events.Envelope) error
}

type InboxRepository interface {
    Find(ctx context.Context, consumer, messageID string) (*InboxMessage, error)
    Insert(ctx context.Context, m InboxMessage) error
}

type InboxOutcome string // PROCESSED | REJECTED | PENDING_REFERENCE | IDEMPOTENT_REPLAY | FAILED

type InboxMessage struct {
    ConsumerName  string
    MessageID     string
    MessageHash   string
    MessageType   string
    TransactionID string // "" → NULL
    Outcome       InboxOutcome
    ReceivedAt    time.Time
    ProcessedAt   time.Time
}
```

**Formato de "não encontrado":**
- `Get` e `Lock` devolvem `ErrNotFound`. O `app` traduz para `UNKNOWN_WALLET` (400), `WALLET_NOT_FOUND` (404) ou `TRANSACTION_NOT_FOUND` (404), conforme a rota.
- `FindByIdempotencyKey`, `FindByExternalID` e `Inbox.Find` devolvem `nil, nil`, o formato que `wagering.CheckIdempotency` já espera.
- `FindReference` devolve `wagering.Reference{}` quando a referência não existe. Quando existe, preenche `AlreadyReversed` na mesma consulta, com um `EXISTS` sobre as transações `REFUND`/`ROLLBACK` `PROCESSED` que a referenciam (índice `wager_tx_single_reversal_uq`).

**Sentinelas (`errors.go`) e o `Kind` com que o adapter as embrulha:**

| Sentinela | Origem | `Kind` | Code |
| --- | --- | --- | --- |
| `ErrNotFound` | `pgx.ErrNoRows` em `Get`/`Lock` | `NotFound` | — |
| `ErrWalletAlreadyExists` | `23505` em `wallets_player_currency_uq` | `Conflict` | `WALLET_ALREADY_EXISTS` |
| `ErrIdempotencyRace` | `23505` em `wager_tx_idempotency_uq` ou `wager_tx_external_id_uq` | `Transient` | — |
| `ErrReversalRace` | `23505` em `wager_tx_single_reversal_uq` | `Transient` | — |
| `ErrInboxDuplicate` | `23505` em `inbox_pk` | `Transient` | — |

---

## 4. Adapter `postgres`

### 4.1 Arquivos

| Arquivo | Responsabilidade |
| --- | --- |
| `querier.go` | Interface interna `querier` (`Exec`, `Query`, `QueryRow`), atendida por `pgx.Tx` e `*pgxpool.Pool` |
| `repos.go` | `repos{q querier}` implementa `app.Repos`; `NewRepos(pool) app.Repos` para as leituras |
| `uow.go` | `NewUnitOfWork(pool, cfg) app.UnitOfWork` |
| `wallet_repo.go`, `transaction_repo.go`, `ledger_repo.go`, `outbox_repo.go`, `inbox_repo.go` | SQL explícito por tabela |
| `money_mapping.go` | `Money` ↔ `(BIGINT, CHAR(3))`; `NULL` ↔ zero value |
| `errors.go` | `translate(err) error` (U09b) |
| `module.go` | Passa a fornecer `app.UnitOfWork` e `app.Repos` |

### 4.2 Unit of Work

- `Do`:
  1. `BeginTx` com `pgx.ReadCommitted`;
  2. `SELECT set_config('lock_timeout', $1, true)`, com `DB_LOCK_TIMEOUT` formatado em milissegundos (`'5000ms'`);
  3. `fn(repos{tx})`;
  4. se `fn` devolve `nil`, `Commit`; se devolve erro, rollback e o erro original;
  5. em `panic`, rollback e o `panic` é relançado.
- O rollback usa `context.WithoutCancel(ctx)` com prazo de 5 s. Um `ctx` cancelado (I16) ainda desfaz a transação, e o erro devolvido é o do `ctx` (transitório pelo `Classify`).
- O erro do `Commit` passa por `translate`: os triggers adiados (`PDA04`) só disparam no commit.
- `Snapshot`: igual, com `pgx.RepeatableRead` e `pgx.ReadOnly`, sem `lock_timeout`.
- Os repositórios chamam `translate` em todo erro de `Exec`/`Query`/`Scan`.

### 4.3 Mapeamento

- **Leitura:** `money.FromMinor(minor, money.Currency(cur))`; os instantes são convertidos para UTC; `NULL` vira `""`, `money.Money{}` ou `time.Time{}`, que é o que o `Snapshot` do domínio espera. `Rehydrate` recusado → `apperrors.New(KindPermanent, "", err)`. `result_balance_minor` usa a moeda da carteira (decisão 12).
- **Valor de domínio inválido na escrita:** os repositórios gravam a partir de `Snapshot()` do domínio (`wallet.Wallet`, `*WagerTransaction`, `LedgerEntry` e `Envelope`). Um zero value, um `nil` ou um `PENDING` (`ErrUninitialized`, `ErrNotPersistable`) é recusado **antes** do SQL e embrulhado como `KindPermanent`. Sem isso, pela D-05, esse bug de programação viraria erro transitório e retry infinito (CHALLENGE §6: valores não inicializados são rejeitados).
- **Escrita:** `m.Minor()` e `string(m.Currency())`; zero value → `NULL` (`pgtype`/ponteiros). Os instantes já chegam em UTC truncado em µs (M1).
- **`Update` da transação:** grava só as colunas de estado (`status`, `reference_transaction_id`, `failure_code`, `result_balance_minor`, `attempts`, `next_attempt_at`, `expires_at`, `updated_at`, `completed_at`) `WHERE id = $1`. 0 linhas → permanente.
- **`UpdateBalance`:** `SET balance_minor, version, updated_at WHERE id = $1 AND version = $new - 1`. 0 linhas → permanente (o lock deveria garantir; data-model §6).
- **Outbox:** `payload` recebe os bytes de `Envelope.MarshalJSON()`; as demais colunas vêm dos getters do `Envelope`; `causation_id` vazio → `NULL`. Por ser `JSONB`, o PostgreSQL normaliza o texto (ordem das chaves, espaços): o conteúdo é o mesmo, os bytes não. O publisher (M4) envia o JSON lido da coluna, idêntico em toda republicação (OUT-05).
- **`Ledger.Sum`:** `COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint` e `COUNT(*)`. Um estouro no cast levanta `22003` → permanente.
- **`Ledger.List`:** `WHERE wallet_id = $1 AND wallet_version > $2 ORDER BY wallet_version LIMIT $3`. O `+1` da próxima página é do `app`.

### 4.4 Tradução de erros (U09b)

| Entrada | Saída |
| --- | --- |
| `23505` nas constraints da tabela de sentinelas (§3) | A sentinela, com o `Kind` indicado |
| Qualquer outro `23505`; `23502`, `23503`, `23514`, `22003`, `25006`, `PDA01`–`PDA05` | `Permanent` |
| Classe `08`; `40001`, `40P01`, `55P03`, `57P01`, `57014`, `53300` | `Transient` |
| Outros `*pgconn.PgError` | `Permanent` (SQL rejeitado pelo banco é bug) |
| Erros de `context`, de rede e demais erros não classificados | Passam intactos; o `Classify` os trata como transitórios (D-05) |

- **O `*pgconn.PgError` não sai do adapter** (CHALLENGE §12). O `Detail` de uma violação traz a linha inteira (`Failing row contains (…)`, `Key (…)=(…)`), ou seja, o payload financeiro completo, e iria parar no log de quem registrasse o erro. O erro traduzido carrega só o SQLSTATE e o nome da constraint (`postgres: 23505 wallets_player_currency_uq`), a sentinela quando houver, e o `Kind`. `Message`, `Detail`, `Where` e `Hint` são descartados.

---

## 5. Migrations e lacunas do `data-model`

As migrations `000001`–`000006` transcrevem o data-model §3–§5, e `migrations/embed.go` expõe `var FS embed.FS` (`//go:embed *.sql`). Lacunas fechadas (o data-model é atualizado junto com esta spec):

| # | Lacuna | Decisão |
| --- | --- | --- |
| 1 | `wallet_guard_delete` e `wager_tx_guard_delete` citadas sem SQL | Funções que só levantam `PDA03` e `PDA02` |
| 2 | Trigger da outbox (§4.5) só em prosa | `outbox_guard_update`: `PDA05` se `(event_id, aggregate_type, aggregate_id, message_group_id, event_type, event_version, payload, correlation_id, causation_id, occurred_at)` mudar (`IS DISTINCT FROM`) ou se `published_at` voltar de preenchido para `NULL` |
| 3 | `TRUNCATE` em `wallets` e `wager_transactions` sem trigger | Continua sem trigger: o `pda_app` não tem o grant, o `TRUNCATE` falha pela FK do ledger e, com `CASCADE`, chega ao ledger e dispara `PDA01` |
| 4 | `inbox_messages` append-only só por grant | Continua assim (data-model §5) |
| 5 | `ledger_check_wallet` com carteira e transação inexistentes: as comparações com `NULL` não disparam o `RAISE` | A FK barra (`23503`); o I02c cobre o caso no schema completo |

---

## 6. Configuração, compose e Makefile

- **`config.Config`:** `DBLockTimeout time.Duration` (`DB_LOCK_TIMEOUT`, padrão `5s`, `> 0` na `Validate`).
- **`.env.example`:** `DATABASE_OWNER_URL=postgres://pda_owner:pda-owner-local@postgres:5432/pda?sslmode=disable`.
- **Serviço `migrate`:** `migrate/migrate:v4.20.1`, `./migrations:/migrations:ro`, `entrypoint: ["sh", "-c", "migrate -path /migrations -database \"$$DATABASE_OWNER_URL\" \"$$@\"", "--"]` e `command: ["up"]`, depende do `postgres` saudável. O `sh -c` é necessário porque a URL vem do `env_file`, que não participa da interpolação do compose.
- **Réplicas `app-*`:** passam a depender de `migrate: service_completed_successfully`.
- **Makefile:** `infra-up` inclui o `migrate`; `make migrate-up` e `make migrate-down N=1` usam `docker compose run --rm migrate …`.
- **`go.mod`:** `github.com/golang-migrate/migrate/v4 v4.20.1` (driver `database/pgx/v5` e fonte `source/iofs`), importado só pelo `test/testkit`.

---

## 7. `testkit`

- `testkit.NewEnv(ctx context.Context, pkg string) (*Env, func(), error)`, chamado no `TestMain` (que não tem `testing.TB`):
  1. lê `.env.example`/`.env` por uma variante do `DotEnv` sem TB;
  2. conecta como `pda_owner` no banco `pda` (`localhost:5432`) e cria `pda_t_<pkg>_<8 hex>`;
  3. aplica as migrations embutidas (`golang-migrate` + `iofs`, URL `pgx5://`);
  4. abre `App` (`pda_app`) e `Owner` (`pda_owner`) no banco novo;
  5. a função de cleanup fecha os pools e executa `DROP DATABASE … WITH (FORCE)`, exceto com `PDA_TEST_KEEP=1`. *(30/09: `DROP` sem `FORCE` e erro devolvido; ver a [spec](2026-09-30-test-db-drop-design.md).)*
- `Env` expõe `AppURL`, `OwnerURL`, `App`, `Owner *pgxpool.Pool` e `Config() config.Config` (`DatabaseURL` = `AppURL`, `DBLockTimeout` = 2 s).
- **Arquivos:** `NewEnv` em `test/testkit/env.go`, que no M4/M5 também cria as filas. `testkit.NewDatabase(ctx, pkg)` fica em `test/testkit/postgres.go` e faz a criação e a migração; o I01 a usa porque precisa de um banco só seu. O `AppDatabaseURL` do M0 continua lá, usado pelos testes do `bootstrap`.
- `testkit.LedgerProblems(ctx, pool, walletID) ([]string, error)`: a parte SQL da verificação de consistência (test-plan §6, itens 2–6); nenhum problema = consistente. O M3 acrescenta os itens 1 e 7.
- `testkit.AssertLedgerConsistent(tb, pool, walletID)` falha o teste com os problemas encontrados. Usa um contexto desacoplado, porque roda no `t.Cleanup`, quando o `t.Context()` já foi cancelado.
- Os testes de um pacote compartilham o banco, cada um com carteiras próprias, e rodam com `t.Parallel()`.

---

## 8. Testes

Todos com `-race` e `// Covers:`. Os de integração ficam em `internal/adapters/postgres/*_integration_test.go` (tag `integration`), exceto onde indicado. A ordem do plano segue a regra de §4.2 do workflow: o teste contra o banco real falha porque a migration ou o repositório ainda não existe.

| ID | Teste | O que prova |
| --- | --- | --- |
| — | `TestLoad_DBLockTimeout` (unitário, `config`) | Padrão 5 s; zero ou negativo é recusado sem ecoar o valor |
| U09b | `TestPostgresErrorMapping` (unitário, `package postgres`, porque `translate` não é exportado) | Tabela da §4.4, com `errors.Is` nas sentinelas e `Classify` no `Kind`; o erro traduzido não contém o `Detail` nem o `Message` de entrada, e `errors.As(*pgconn.PgError)` é falso |
| I01 | `TestMigrationsUpDownUp` (banco próprio) | Snapshot de `information_schema.columns`, `table_constraints`, `pg_indexes`, `pg_trigger` e `pg_proc` idêntico depois de up → down-all → up |
| I02a | `TestConstraints` (banco próprio, triggers desligados) | Cada `CHECK`, `UNIQUE`, FK e índice único da §8 do data-model viola com o SQLSTATE e o nome esperados. Os triggers do 000005 disparam antes dos `CHECK` e os esconderiam; eles são testados pelo I02b–e |
| I02b | `TestLedgerImmutable` e `TestLedgerImmutableForApp` | `UPDATE`, `DELETE` e `TRUNCATE` no ledger: `PDA01` como `pda_owner` e `42501` como `pda_app` |
| I02b | `TestAppRolePrivileges` | A matriz de privilégios do `pda_app` é exatamente a do data-model §5, sem `CREATE` no schema |
| I02c | `TestLedgerCoupling` | `PDA04` no insert (carteira incoerente, `LOSS`, transação não `PROCESSED`, valor, moeda e direção errados) e no commit (saldo sem lançamento, `PROCESSED` sem lançamento) |
| I02d | `TestTerminalTransactionImmutable` | `UPDATE` em `PROCESSED`, `REJECTED` e `FAILED` → `PDA02` |
| I02e | `TestGuardTriggers` | `PDA03`: coluna imutável da carteira, versão sem mudança de saldo, saldo sem versão + 1, `DELETE`. `PDA02`: coluna imutável da transação, `DELETE`. `PDA05`: snapshot da outbox e `published_at` voltando a `NULL` |
| I03a | `TestFinancialAtomicity` | Um `Repos` decorado falha como transitório em `Outbox().Insert`, no fim de um BET real (`Settle` + repositórios). Nada persiste: nem transação, nem ledger, nem outbox; saldo e versão intactos |
| I03b (parcial) | `TestPermanentFailureRecorded` | `Fail` gravado numa segunda UoW, depois do rollback da primeira: a transação fica `FAILED` com `INTERNAL_PERMANENT_FAILURE`, sem lançamento, sem mudança de saldo, e reidrata |
| I16 | `TestContextCancellation` | `ctx` cancelado dentro do `Do`, e prazo vencido enquanto espera o lock de uma carteira travada por outra transação: o erro traz o do `ctx`, é transitório, a espera termina antes do `lock_timeout`, e nada persiste |
| I17 | `TestDomainFlowsPersist` | Abertura, BET, WIN, WIN com referência, LOSS, REFUND, ROLLBACK de WIN, rejeição e `PENDING_REFERENCE` → `PROCESSED` (via `Update`), todos com `OpenWallet`/`Settle` + repositórios, passam pelos triggers e pelo `AssertLedgerConsistent` |
| I18 | `TestWalletRepository`, `TestOutboxRepository`, `TestInboxRepository`, `TestTransactionRepository`, `TestTransactionQueries` e `TestLedgerQueries` | Ida e volta idêntica (`Snapshot` antes = depois) para carteira, transação (externa, interna, pendente, rejeitada), lançamento e inbox; `ErrNotFound` e `nil, nil`; `FindReference` com e sem `AlreadyReversed`; `AdvanceDependents` com o piso de `created_at`; `List` e `Sum`; payload da outbox igual (como JSON) ao `MarshalJSON`; rejeição `CURRENCY_MISMATCH` com o saldo na moeda da carteira; ID não canônico → `ErrNotFound`; `ErrWalletAlreadyExists`, `ErrIdempotencyRace`, `ErrReversalRace` e `ErrInboxDuplicate`; `Wallet{}`, `nil` e transação `PENDING` recusados antes do SQL, como permanentes |
| I19 | `TestUnitOfWork` | Commit; rollback em erro; rollback + `panic` relançado; carteira travada por outra transação → `55P03` transitório dentro de `DB_LOCK_TIMEOUT`; `Snapshot` recusa escrita (`25006`, permanente); nenhuma conexão fica presa ao pool |
| — | `TestLedgerProblemsDetectsDivergence` (banco próprio, triggers desligados) | Sensibilidade do `LedgerProblems`: cada divergência (saldo, cadeia, versão, lançamento de operação sem movimento, `PROCESSED` sem lançamento) é reportada |
| I07a | `TestFxGraph` (existente, `bootstrap`) | Passa a resolver `app.UnitOfWork` e `app.Repos` |

---

## 9. Requisitos no encerramento

- **Completos:** ART-05, DB-01, DB-02, DB-03, DB-05 (pelo `ARCHITECTURE.md` §3), WAL-03, WAL-04 (o `CHECK`; a concorrência continua em M3/M8), WAL-06, WAL-07, LED-03, LED-04, LED-05, LED-06, TX-05, TX-07, TX-09 e OUT-01.
- **Parciais:** DB-04 (comandos no README no M10), IDEM-02/IDEM-07 (as constraints; o fluxo no M3), SQS-03 (a PK; o fluxo no M5), OPS-08 (o índice; o fluxo no M3/M6).
- **Eliminatórios:** E4 (constraint) e E9.

---

## 10. Ajustes em `docs/` (feitos junto com esta spec)

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-09: `DB_LOCK_TIMEOUT` e `set_config` na transação inteira. D-14: `Snapshot`, `Repos` sobre o pool, sentinelas das corridas |
| [`data-model.md`](../../data-model.md) | SQL das funções de `DELETE` e do trigger da outbox; nota do `set_config`; `GREATEST` na antecipação; `next_attempt_at = now()` na outbox |
| [`test-plan.md`](../../test-plan.md) | U09b com `57014` e `25006`; I02e e I17–I19; assinatura do `NewEnv` |
| [`structure.md`](../../structure.md) | `app/errors.go`, `querier.go`, `repos.go`, `testkit/env.go` (`NewEnv`) e `testkit/postgres.go` (`NewDatabase`, `AssertLedgerConsistent`) |

No encerramento: `ARCHITECTURE.md` §3–§4, `delivery-requirements.md`, `implementation-plan.md` e o diário.

---

## 11. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| Domínio e schema divergirem em algum fluxo (ex.: ordem de escrita exigida pelos triggers) | I17 grava todos os tipos pelo caminho real, antes do M3 depender disso |
| Driver `pgx/v5` do golang-migrate puxar outra versão do `pgx` | `go get` + `make tidy-check` na primeira tarefa; divergência vira ruling no plano |
| Imagem `migrate/migrate` sem `sh` | Validado com `docker compose run --rm migrate version` na tarefa do compose; plano B: `command` com a URL montada pelo `Makefile` |
| Testes lentos (um banco por pacote + migrations) | Um banco por pacote, não por teste; `t.Parallel()` com carteiras próprias |
| Teste de `lock_timeout` instável | A transação que segura o lock é aberta e confirmada pelo próprio teste; o prazo de espera é o `DB_LOCK_TIMEOUT` de 2 s, sem `Sleep` |
