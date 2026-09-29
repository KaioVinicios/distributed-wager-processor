# M3 — Contrato, casos de uso, HTTP e autenticação: design

**Data:** 29/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor e implementada em 29/09/2026; achados da validação do plano incorporados (§2.2)

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M3;
- o contrato HTTP de D-04, D-06, D-08 e D-20, com o catálogo do [`transaction-lifecycle.md`](../../transaction-lifecycle.md) §5 e a ordem de avaliação do §3;
- o pipeline HTTP do lifecycle §6.1 e a abertura do §6.4;
- D-07 (autenticação e autorização) e D-16 (paginação e reconciliação);
- [`test-plan.md`](../../test-plan.md) §5: A01–A04, I03b (parte HTTP), I08–I12, I15, C01a e C02 em processo, e os itens 1 e 7 da verificação de consistência (§6).

**Artefato central (contrato primeiro, D-20):** [`api/openapi.yaml`](../../../api/openapi.yaml), já escrito e validado com o kin-openapi v0.149.0 (documento e exemplos), e [`api/requests.http`](../../../api/requests.http). Os dois fazem parte desta spec e são aprovados junto com ela, **antes** de qualquer handler.

Esta spec registra só o **delta** em relação a `docs/`. O que já está decidido lá não é repetido.

---

## 1. Objetivo e critério de pronto

**Objetivo:** o HTTP síncrono correto e protegido, de ponta a ponta: contrato → caso de uso → domínio → banco, com o token real do Keycloak. Fecha os eliminatórios E1, E2, E5 (HTTP) e E6 (HTTP), e antecipa o E4 e o E5 com os testes de concorrência em processo.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde, com os testes da §9 vistos falhando pelo motivo certo e depois passando; C01a e C02 com a checagem de sensibilidade.
3. `docker compose up --build --wait` sobe as 3 réplicas saudáveis, e o fluxo completo por `curl` com token real funciona (`api/requests.http`): abrir carteira → BET → replay → rejeição → reconciliação.
4. Requisitos da §10 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Fora do escopo** (com o marco de destino):
- o registro da inbox no `ProcessRequest` e o canal SQS (M5);
- o claim das pendências e o uso do `settleAndPersist` pelo worker (M6);
- o restante do catálogo de métricas, os campos de log padronizados via `ctx` e o I14 (M7);
- o ponto de falha `http.after_commit_before_response` e os cenários com 3 processos (M8).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Um tipo por caso de uso** (`OpenWallet`, `ProcessWager`, `Queries`, `Reconcile`), com um passo interno `settleAndPersist` compartilhado (abordagem A, escolhida pelo autor em 29/09) | Dependências explícitas por caso de uso; o M6 reutiliza o passo e o M5 só acrescenta a inbox, sem reescrever o M3 |
| 2 | **Resultado de negócio não é erro:** `ProcessWager.Execute` devolve `ProcessResult{Tx, Replay}` para `PROCESSED`, `PENDING_REFERENCE`, `REJECTED` e `FAILED`. Erro só para o que **não** foi persistido (`KindInput`, `KindConflict`, `KindTransient`, `KindPermanent`) | A borda escolhe 200/202/422/500 pelo estado persistido; é o mesmo critério nos replays (D-04) |
| 3 | **O `app` não conhece o `auth`.** A verificação do provedor (passo 8 do lifecycle §3.2, `403 PROVIDER_MISMATCH`) e a visibilidade das consultas (`404` para transação de outro provedor) ficam na borda, com as funções puras de `auth.Policy` | `depguard` (`structure.md` §2) e AUTH-07: a borda autoriza antes de chamar o caso de uso, portanto antes de qualquer consulta de idempotência ou escrita |
| 4 | **Corridas de unicidade** (`ErrIdempotencyRace`, `ErrReversalRace`) desfazem a UoW e **reexecutam o `Execute` desde a pré-checagem**, em até **3 tentativas**. Esgotadas, o erro é `KindTransient` (503) | A releitura resolve para replay, 409 ou `ALREADY_REVERSED` (D-08, D-10). Com o lock na carteira, a corrida só acontece com a mesma chave em carteiras diferentes; o limite evita laço em caso de bug |
| 5 | **`FAILED` numa segunda UoW:** um erro `KindPermanent` dentro da UoW, **a partir do lock da carteira inclusive** (ex.: carteira ou referência que o domínio recusa, overflow, trigger `PDA0x`), grava `NewExternal` → `Fail` → `Insert` + `AdvanceDependents`, sem lançamento nem outbox, e devolve `Result{FAILED}` (500 com corpo de resultado). Se essa gravação falhar, o erro é `KindTransient`. Se a falha permanente vier da **pré-checagem** (linha existente corrompida, chave ocupada), não há o que gravar: 500 `problem+json` `INTERNAL_ERROR` | Lifecycle §5.2 e D-05. A chave ocupada impede um segundo registro; o `INTERNAL_ERROR` diz que nada novo foi persistido |
| 6 | **Tradução dos erros do domínio num único helper (`domainError`):** `*wagering.ValidationError` → `KindInput`; `*wagering.ConflictError` → `KindConflict`; `*apperrors.Error` mantém a classificação; **qualquer outro erro que sai do domínio → `KindPermanent`** | O domínio não faz I/O: todo erro dele é entrada inválida ou invariante quebrada. Sem isso, pela D-05, um `money.ErrOverflow` viraria 503 em laço. Fecha a pendência anotada no diário do M2 |
| 7 | **A validação do `POST /wallets` fica no `app`** e devolve `*wagering.ValidationError` com os mesmos códigos e o mesmo formato de `field` do `NewCommand` (`MISSING_FIELD`, `INVALID_FIELD`, `INVALID_AMOUNT`, `INVALID_CURRENCY`) | Uma única forma de erro de entrada para a borda; a abertura não tem comando no domínio |
| 8 | **`ListLedger` e `Reconcile` leem a carteira (`Wallets().Get`) antes de `Ledger().List`/`Sum`**, e `AdvanceDependents` só recebe IDs de um `Command` validado ou de uma transação reidratada | 404 correto e fecha as pendências da revisão do M2 (ID malformado em `List`, `Sum` e `AdvanceDependents`) sem mudar o adapter |
| 9 | **Paginação estrita:** `limit` inteiro de 1 a 200 (padrão 50) e `cursor` base64url sem padding de `{"v":N}`, N ≥ 1, decodificado com `DisallowUnknownFields`. Fora disso: `400 INVALID_FIELD` com `field` = `limit` ou `cursor`. Sem ajuste silencioso. `nextCursor` sai da busca de `limit+1` linhas e é omitido na última página | D-16; o desafio pede cursor opaco e limite definido, e ajustar em silêncio esconderia erro do cliente |
| 10 | **Representações** (esquemas do `openapi.yaml`): `Wallet` com `createdAt`/`updatedAt` além dos campos do desafio; `TransactionResult` com `balance` só quando há saldo observado (`PROCESSED`/`REJECTED`) e `failureCode`/`failureCategory` só em `REJECTED`/`FAILED`; `Transaction` completa **sem** `idempotencyKey`; `201` do `POST /wallets` com `Location` | D-04 e HTTP-04. A chave não é necessária em nenhuma consulta, e expor menos é melhor |
| 11 | **Tipo JSON errado** (`"amount": 25.00`, `"money": "x"`, `"playerId": 1`) é detectado na decodificação (passo 1) e responde com o código do campo: `money.amount` → `INVALID_AMOUNT`, `money.currency` → `INVALID_CURRENCY`, `kind` → `INVALID_KIND`, demais → `INVALID_FIELD`, sempre com `field`. `null` equivale a ausente (`MISSING_FIELD`). Mais de um header `Idempotency-Key` → `INVALID_IDEMPOTENCY_KEY` | Lacuna do lifecycle §3.1. O número JSON nunca é convertido (a decodificação é para `*string`), portanto nunca passa por `float` (E3) |
| 12 | **Códigos novos (lifecycle §5.3):** `INTERNAL_ERROR` (500, `TRANSIENT`: nada persistido, reenviar com a mesma chave é seguro; cobre `panic` e a decisão 5), `ROUTE_NOT_FOUND` (404) e `METHOD_NOT_ALLOWED` (405, com `Allow`). **`Retry-After: 1`** em todo 503. `problem+json` com `type: "about:blank"`, `title` = texto do status, `detail` fixo por código (nunca ecoa valores), `code`, `category`, `field?` e `correlationId` | Um formato de erro uniforme em toda resposta; `structure.md` §4.2 já previa o recover → 500 |
| 13 | **`OIDC_CLOCK_SKEW`** (padrão 30 s; 1 s nos testes de integração), aplicado como `Config.Now = time.Now() − skew`. O `nbf` segue a tolerância fixa de 5 min da biblioteca | Verificado no código do go-oidc v3.21.0: o `exp` é comparado **sem** tolerância e o `nbf` tem 5 min fixos. Com 30 s fixos, o A01b (token de 5 s, espera de 6 s) nunca veria o 401 |
| 14 | **A role `provider` só vale com a claim `provider_id` preenchida**; sem ela, o token recebe `403 FORBIDDEN` | Nunca autorizar um "provedor vazio" (AUTH-04) |
| 15 | **JWKS buscado no `OnStart` do módulo `auth`** (200 e ao menos uma chave RSA, senão o start falha). Não entra no readiness. O compose passa a fazer as réplicas dependerem do Keycloak saudável | Fail fast (FX-02). O desafio define o readiness só com PostgreSQL e SQS |
| 16 | **Porta `app.Metrics`** com um único método agora, `ReconciliationDivergence()`, implementada pela `observability` com o contador `reconciliation_divergences_total`. O M7 acrescenta os demais métodos | HTTP-07 exige a métrica já no M3, e o `app` não importa Prometheus |
| 17 | **`correlationID` explícito** nos pedidos que o `app` registra em log ou grava (`ProcessRequest`, `OpenWallet`, `Reconcile`). O contexto de log via `ctx` fica para o M7 | YAGNI; o M7 padroniza os campos de log (OBS-01) |
| 18 | **Casos de uso testados contra o PostgreSQL real**, sem fakes em memória (escolha do autor em 29/09). Unitários só nas partes puras | Os fakes não reproduzem triggers, índices únicos nem corridas, que são justamente o que o `app` precisa provar |
| 19 | **I15 é unitário** (pacote `httpapi`, sem infraestrutura) | Compara o documento com a tabela de rotas; não precisa de banco nem de IdP, e roda no `go test ./...` |
| 20 | **O validador de contrato do `testkit`** valida **toda resposta**. A validação da **requisição** é pulada só quando o teste marca a requisição como deliberadamente inválida (I12, 415, limite de corpo; a autenticação não é validada pelo `openapi3filter`, então os casos do A01b não precisam pular); uma rota fora do documento (404/405 de rota) pula a validação | Os testes negativos precisam enviar o que o contrato proíbe, e a resposta deles continua sendo verificada |
| 21 | **Um app em processo por pacote de teste**, iniciado no `TestMain` por `env.StartApp`; as variáveis de AWS e OIDC são definidas por `os.Setenv` no `TestMain` | `t.Setenv` impede `t.Parallel()`; o isolamento já vem do banco e das filas do pacote |
| 22 | **Timeouts do servidor:** `ReadTimeout` 10 s e `WriteTimeout` 30 s, além do `ReadHeaderTimeout` de 5 s | Protege contra corpos lentos; 30 s cobre o pior caso de `lock_timeout` + retries |

### 2.1 Conformidade com o `CHALLENGE.md` (autorrevisão)

| Regra | Como o M3 a atende |
| --- | --- |
| §2: IdP externo OIDC, `client_credentials`, validação de credenciais justificada | Keycloak do compose; `Verifier` com assinatura (JWKS), `iss`, `aud`, `exp` e RS256; A01a/b com tokens reais e forjados; justificativa no `ARCHITECTURE.md` (AUTH-10) |
| §2: a identidade determina o `providerId`; provedores só acessam as próprias transações, **inclusive em replays** | Passo 8 na borda antes do `Execute` (a busca de idempotência só acontece com `body.providerId == token.provider_id`); 404 em `GET` por id de outro provedor; 403 no path de outro provedor; A02a/b |
| §2: operações de carteira restritas ao serviço interno | Role `wallet-internal` nas 4 rotas `/wallets*`; A02c |
| §5.1 e §6.1: nenhum `float` em parsing ou serialização; contrato `{"amount","currency"}` | DTOs com `*string`; número JSON recusado sem conversão (decisão 11); `Money` e `SignedMoney` no contrato com `pattern` |
| §5.2 e §9: idempotência persistente; chave obrigatória e nunca substituída; replay com o resultado e o saldo originais; conflito com conteúdo diferente; `(providerId, externalTransactionId)` não reaplicado com outra chave | `MISSING_IDEMPOTENCY_KEY`; a chave recebida vai intacta ao `Command`; replay pelo `result_balance` (I10); `409` com os dois códigos (I12) |
| §6: I/O com `context`; `panic` não representa rejeição; erros classificáveis | Todo caso de uso recebe `ctx`; rejeições são `Result`; recover só para bugs (500 `INTERNAL_ERROR`); `domainError` + `apperrors.Classify` (decisão 6) |
| §6.3: rejeição × falha permanente; replay sem reaplicar | `REJECTED` (422) × `FAILED` em UoW separada (500); replay devolve o estado persistido |
| §7: rejeições com `failureCode` estável que distingue corrigível de definitivo | `TransactionResult.failureCode` + `failureCategory`; `Problem.code` + `category` |
| §8: 100.00 vs 2×80.00; carteiras em paralelo | C02 em processo (20 repetições), além do C02 com 3 processos no M8 |
| §9: `POST /wallets` com `OPENING`, lançamento e eventos no mesmo commit; conflito na segunda abertura | `OpenWallet` numa UoW; `409 WALLET_ALREADY_EXISTS`; teste do caso de uso e item 7 da consistência |
| §9: paginação por cursor opaco com ordem estável | Cursor base64url de `{"v":N}` sobre `wallet_version` (decisão 9); I09 |
| §9: códigos e corpos distinguíveis para entrada inválida, conflito, rejeição, pendente e indisponibilidade | 400/409 `problem+json`, 422/202 `TransactionResult`, 503 `problem+json` + `Retry-After`; I12 cobre o catálogo |
| §9: reconciliação em visão consistente, `difference = stored − calculated`, divergência em resposta, log e métrica, sem alterar o saldo | `uow.Snapshot`; log `WARN` + `reconciliation_divergences_total`; I08 com divergência forçada |
| §9: health checks públicos | Rotas públicas desde o M0; A04 confirma que só elas (e os docs) respondem sem token |
| §12: logs sem credenciais nem payloads financeiros completos | O log de acesso não registra headers nem corpo; o motivo da falha de token vai só em `DEBUG`, sem o token; o `detail` dos erros não ecoa valores |
| §13: IdP real; credenciais ausentes, inválidas e expiradas; isolamento em consultas e replays; sem efeito em acesso não autorizado | A01–A03 contra o Keycloak do compose, com `SnapshotCounts` antes e depois |
| §13: infraestrutura real, sem mocks integrais | PostgreSQL e Keycloak reais nos testes do `app` e da API. Os únicos dublês são os stubs de caso de uso nos unitários do `httpapi` (decodificação e mapeamento, sem I/O) e o decorador do I03b, que provoca uma falha pontual (test-plan §1) |
| §4: Fx compõe casos de uso e handlers | Módulos `auth` e `app` (`bootstrap/app_module.go`); I07a resolve o grafo inteiro |

### 2.2 Achados da validação do plano

O plano foi validado numa cópia descartável do repositório, com o código de todas as tarefas testado contra a infraestrutura real. Estes achados completam a §2:

| # | Decisão | Motivo |
| --- | --- | --- |
| 23 | **No `lookup`, uma transação achada pelo `externalTransactionId` com a mesma chave é a transação da chave** (replay ou `IDEMPOTENCY_KEY_REUSED`), nunca `EXTERNAL_TRANSACTION_ID_CONFLICT` | As duas leituras de D-08 são comandos separados: uma entrega concorrente da mesma operação pode confirmar entre elas, e então só a segunda a encontra. O C01a (50 envios iguais em paralelo) produzia um 409 indevido de vez em quando. O I22 ganhou o teste determinístico |
| 24 | **Ordem dos middlewares:** correlação → log de acesso → recuperação de `panic` → fallback de rota (por rota: autenticação e corpo) | O 500 de um `panic` precisa de `correlationId` no corpo e de uma linha no log de acesso |
| 25 | **As filas isoladas são criadas pelo `StartApp`, e não pelo `NewEnv`.** O `Eventually` fica para o M4/M6. O `SnapshotCounts` é global, e o A03 roda sem `t.Parallel` | Os pacotes que não sobem o app (`postgres`, `app`) não dependem do MiniStack; no M3 não há trabalho assíncrono a esperar; contagens globais só são estáveis sem escritores em paralelo |
| 26 | **A regra `app` do `depguard` exclui os testes (`!$test`)**, como a do `apperrors` | Os testes de integração do `app` usam o adapter `postgres` real (decisão 18); o código de produção continua proibido de importá-lo |

---

## 3. Contrato (`api/`)

| Arquivo | Conteúdo |
| --- | --- |
| `openapi.yaml` | OpenAPI 3.0.3 com as 9 rotas mais `/docs` e `/openapi.yaml`. Esquemas `Money`, `SignedMoney`, `Wallet`, `LedgerPage`, `Reconciliation`, `WagerTransactionRequest`, `TransactionResult`, `Transaction`, `Problem` (com o catálogo de `code` em `enum`) e `Health`. Todos os objetos com `additionalProperties: false`. Segurança `keycloak` (OAuth2 `clientCredentials`) e `bearerAuth`; `x-required-role` em cada operação; exemplos para cada situação de D-04 |
| `requests.http` | Fluxo completo: tokens → abrir carteira → BET → replay → rejeição → 409 → consultas → reconciliação → 401 |
| `swagger.html` | Página do Swagger UI (`swagger-ui-dist` 5.33.0 pelo jsDelivr) apontando para `/openapi.yaml`; o *Authorize* usa o fluxo `clientCredentials`, que não precisa de página de redirecionamento. Criada no plano |
| `embed.go` | `package api`: `//go:embed openapi.yaml swagger.html` → `OpenAPI []byte` e `SwaggerHTML []byte`. Criado no plano |

Qualquer mudança no contrato durante a execução é uma mudança nesta spec: atualiza o `openapi.yaml`, a seção correspondente e o ledger do plano.

---

## 4. Casos de uso (`internal/app`)

### 4.1 Portas novas (`ports.go`)

```go
type Clock interface{ Now() time.Time }
type IDGenerator interface{ New() string } // UUIDv7 canônico
type Metrics interface{ ReconciliationDivergence() }
```

Implementações padrão no próprio `app`: `SystemClock` (`time.Now`) e `UUIDv7` (`google/uuid`, D-08). A política de retentativa é um `wagering.ReferenceRetryPolicy` montado no `bootstrap/app_module.go` a partir da config, com `rand.Int64N` (`math/rand/v2`).

### 4.2 `ProcessWager` (`process_wager.go`)

```go
type ProcessRequest struct {
	Command       wagering.Command
	Via           wagering.ReceivedVia
	CorrelationID string
	CausationID   string
}
type ProcessResult struct {
	Tx     *wagering.WagerTransaction
	Replay bool
}
func NewProcessWager(uow UnitOfWork, reads Repos, clock Clock, ids IDGenerator, policy wagering.ReferenceRetryPolicy, log *slog.Logger) *ProcessWager
func (p *ProcessWager) Execute(ctx context.Context, req ProcessRequest) (ProcessResult, error)
```

Fluxo de uma tentativa (`now := clock.Now()` uma vez):

1. **Pré-checagem** sobre `reads`: `FindByIdempotencyKey` + `FindByExternalID` → `CheckIdempotency`. Replay → `Result{Tx, Replay: true}`; `*ConflictError` → `KindConflict`. Uma transação achada só pela segunda busca, com a mesma chave, é tratada como a da chave (decisão 23).
2. **`uow.Do`:**
   1. `Wallets().Lock(cmd.WalletID())`; `ErrNotFound` → `apperrors.New(KindInput, "UNKNOWN_WALLET", …)`;
   2. idempotência verificada de novo, sob o lock; replay encerra a função sem escrita;
   3. `wagering.NewExternal(ids.New(), cmd, via, correlationID, now)`;
   4. `settleAndPersist(ctx, repos, tx, &w, now, insert=true, correlationID, causationID)`:
      - `Transactions().FindReference(providerID, refExtID)` se houver referência;
      - `wagering.Settle(tx, &w, ref, SettleParams{EntryID: ids.New(), Now: now, Policy: policy})`;
      - `events.Seal(ids.New(), correlationID, causationID, e)` para cada evento;
      - `Transactions().Insert(tx)` (ou `Update` quando `insert=false`, no M6) → se houve lançamento: `Wallets().UpdateBalance(w)` + `Ledger().Insert(entry)` → `Outbox().Insert(envs...)`;
      - se `tx.Status().IsTerminal()`: `AdvanceDependents(providerID, externalID, now)`.
3. **Erro:** `ErrIdempotencyRace`/`ErrReversalRace` → nova tentativa (decisão 4). `KindPermanent` a partir do passo 2.1, inclusive → `recordFailure` (decisão 5). Qualquer outro erro sobe classificado.

`recordFailure` também é uma UoW: `NewExternal` (novo id) → `Fail(now)` → `Insert` + `AdvanceDependents`, com log `ERROR` contendo `transactionId`, `walletId`, `providerId`, `correlationId` e o erro sanitizado (nunca o payload). `ErrIdempotencyRace` aqui → releitura, que devolve o replay.

### 4.3 `OpenWallet` (`open_wallet.go`)

```go
type OpenWalletInput struct {
	PlayerID *string
	Initial  *wagering.MoneyInput
}
func (o *OpenWallet) Execute(ctx context.Context, in OpenWalletInput, correlationID string) (wallet.Wallet, error)
```

Validação na mesma ordem do `NewCommand` (lifecycle §3.1): primeiro as ausências (`playerId`, `initialBalance`, `initialBalance.amount`, `initialBalance.currency` → `MISSING_FIELD`), depois os formatos (`playerId` UUID → `INVALID_FIELD`; `amount` → `INVALID_AMOUNT`; `currency` → `INVALID_CURRENCY`), com `money.Parse` (formato estrito, não negativo; zero é aceito). Depois `wagering.OpenWallet` com IDs novos e, numa UoW: `Wallets().Insert` → (saldo > 0) `Transactions().Insert(opening)` + `Ledger().Insert(entry)` + `Outbox().Insert(envs...)`. `ErrWalletAlreadyExists` já chega como `KindConflict` `WALLET_ALREADY_EXISTS`.

### 4.4 `Queries` (`queries.go`) e cursor (`cursor.go`)

```go
func (q *Queries) GetWallet(ctx context.Context, id string) (wallet.Wallet, error)
func (q *Queries) ListLedger(ctx context.Context, walletID, cursor string, limit int) (LedgerPage, error)
func (q *Queries) GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error)
func (q *Queries) GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)

type LedgerPage struct {
	Entries    []wallet.LedgerEntry
	NextCursor string // "" on the last page
}
```

`ErrNotFound`/`nil` → `KindNotFound` com `WALLET_NOT_FOUND` ou `TRANSACTION_NOT_FOUND`. O `limit` chega já convertido pela borda; `ListLedger` valida a faixa e o cursor (decisão 9).

### 4.5 `Reconcile` (`reconcile.go`)

```go
type Reconciliation struct {
	WalletID                       string
	Stored, Calculated, Difference money.Money
	Consistent                     bool
	CheckedEntries                 int64
}
func (r *Reconcile) Execute(ctx context.Context, walletID, correlationID string) (Reconciliation, error)
```

Em `uow.Snapshot`: `Wallets().Get` → `Ledger().Sum` → `money.FromMinor(NetMinor, moeda da carteira)` → `Difference = Stored.Sub(Calculated)`. Divergência: log `WARN` (`walletId`, `correlationId`, os três valores) + `metrics.ReconciliationDivergence()`. Um overflow (na soma do banco, `22003`, ou no `Sub`) é `KindPermanent`.

### 4.6 `domainError` (`errors.go`)

Envolve **todas** as chamadas ao domínio no `app` (decisão 6). Teste de tabela com as sentinelas de `money`, `wallet`, `wagering` e `events`, com os dois tipos de erro de entrada e com um `*apperrors.Error` transitório que precisa sair intacto.

---

## 5. Autenticação (`internal/auth`)

| Arquivo | Conteúdo |
| --- | --- |
| `verifier.go` | `NewVerifier(cfg)`: `oidc.NewRemoteKeySet(OIDC_JWKS_URL)` + `oidc.NewVerifier(OIDC_ISSUER, keySet, &oidc.Config{ClientID: OIDC_AUDIENCE, SupportedSigningAlgs: []string{oidc.RS256}, Now: skewed})`, sem discovery (spike). `Authenticate(ctx, raw) (Principal, error)`: qualquer falha → `ErrUnauthenticated` (com a causa na cadeia, registrada só em `DEBUG`) |
| `principal.go` | `Principal{Subject, ClientID, ProviderID string; Roles []Role}`, `RoleProvider = "provider"`, `RoleWalletInternal = "wallet-internal"`, `WithPrincipal`/`FromContext`. Claims: `sub`, `azp`, `provider_id` e `resource_access["pda-api"].roles` |
| `policy.go` | `HasRole(p, role)` (a role `provider` exige `ProviderID`, decisão 14), `ActsAs(p, providerID)` e `CanSeeTransaction(p, txProviderID)`. Funções puras, com teste de tabela |
| `module.go` | `fx.Module("auth")`: provê o `*Verifier` e, no `OnStart`, busca o JWKS (decisão 15) |

O contexto do `RemoteKeySet` é derivado com `context.WithoutCancel` do contexto de start, porque o go-oidc o guarda para as buscas futuras.

---

## 6. HTTP (`internal/adapters/httpapi`)

### 6.1 Rotas e middlewares

`routes.go` tem uma única tabela `[]route{method, path, role, handler}`, que registra no `ServeMux` e que o I15 compara com o OpenAPI. `role` vazio = pública. `/docs` e `/openapi.yaml` só entram com `API_DOCS_ENABLED`.

Ordem, de fora para dentro (decisão 24):
1. **correlação** (`^[A-Za-z0-9._-]{1,128}$` ou UUIDv7 gerado) → header da resposta e `problem+json`;
2. **log de acesso**: método, padrão da rota casada, status, duração, `correlationId` e `providerId`;
3. **recover** → 500 `INTERNAL_ERROR`, log `ERROR` sem corpo nem headers;
4. **fallback de rota**: um `ServeMux` sem padrão casado responde 404 `ROUTE_NOT_FOUND` ou 405 `METHOD_NOT_ALLOWED` (com `Allow`) em `problem+json`;
5. **auth por rota**: `Authorization: Bearer` → `Verifier.Authenticate` (401) → `Policy.HasRole` (403) → `Principal` no `ctx`;
6. **corpo** (só POST com corpo): `Content-Type` `application/json` (parâmetro `charset` aceito), senão 415; `http.MaxBytesReader` de 64 KB.

### 6.2 Handlers e DTOs

- `wagering_handler.go`: decodifica (decisão 11) → `wagering.Input` (a chave vem do header) → `wagering.NewCommand` (400) → `policy.ActsAs(principal, cmd.ProviderID())` (403) → `ProcessWager.Execute` → `TransactionResult` com o status de §6.3. `GET` por id: `Queries.GetTransaction` → `CanSeeTransaction` ou 404. `GET` por id externo: `ActsAs` ou `wallet-internal` (403 antes da consulta) → `Queries.GetTransactionByExternalID`.
- `wallets_handler.go`: `POST /wallets` (201 + `Location`), `GET /wallets/{id}`, `GET …/ledger` (`limit` não inteiro → 400 `INVALID_FIELD` já na borda) e `POST …/reconciliation` (sem corpo; um corpo enviado é ignorado).
- `docs_handler.go`: `GET /openapi.yaml` (`application/yaml`) e `GET /docs` (`text/html`), a partir do pacote `api`.
- `dto.go`: DTOs de entrada com ponteiros e `json` explícito (`musttag`); DTOs de saída montados a partir dos getters do domínio, com `omitempty` nos campos opcionais do contrato.
- Os handlers dependem de **interfaces pequenas** declaradas no `httpapi` (`wagerProcessor`, `walletOpener`, `queries`, `reconciler`), atendidas pelos casos de uso. É o que permite os unitários da borda com stubs.

### 6.3 Mapeamento (`status.go`, `problem.go`)

| Origem | HTTP | Corpo |
| --- | --- | --- |
| `Result` `PROCESSED` / `PENDING_REFERENCE` / `REJECTED` / `FAILED` | 200 / 202 / 422 / 500 | `TransactionResult` |
| `*wagering.ValidationError` | 400 | `problem+json` com `code` e `field` |
| `KindInput` / `KindNotFound` / `KindForbidden` / `KindConflict` | 400 / 404 / 403 / 409 | `problem+json` com `apperrors.CodeOf` |
| `KindTransient` (inclui erro não classificado e `ctx` cancelado) | 503 + `Retry-After: 1` | `TEMPORARILY_UNAVAILABLE` |
| `KindPermanent`, `KindBusiness` (não esperado na borda) | 500 | `INTERNAL_ERROR` |

`category` sai do catálogo: `WALLET_ALREADY_EXISTS` é `DEFINITIVE`; `TEMPORARILY_UNAVAILABLE` e `INTERNAL_ERROR` são `TRANSIENT`; os demais são `CORRECTABLE`. O `switch` sobre `apperrors.Kind` é exaustivo (`exhaustive`).

---

## 7. Configuração, composição e compose

| Item | Mudança |
| --- | --- |
| `config.Config` | `OIDCIssuer` (`OIDC_ISSUER`), `OIDCJWKSURL` (`OIDC_JWKS_URL`), `OIDCAudience` (`OIDC_AUDIENCE`, padrão `pda-api`), `OIDCClockSkew` (`OIDC_CLOCK_SKEW`, padrão `30s`), `APIDocsEnabled` (`API_DOCS_ENABLED`, padrão `true`), `ReferenceRetryBaseDelay` (`1s`), `ReferenceRetryMaxDelay` (`60s`), `ReferenceMaxAttempts` (`8`), `ReferenceTTL` (`10m`). `Validate`: URLs absolutas `http(s)`, audiência não vazia, skew ≥ 0, tempos > 0, tentativas ≥ 1, base ≤ máximo |
| `bootstrap` | `app_module.go` (`fx.Module("app")`: `SystemClock`, `UUIDv7`, a política e os 4 casos de uso); ordem `config → observability → postgres → aws → auth → app → httpapi` (D-15) |
| `observability` | `NewMetrics(reg)` satisfaz `app.Metrics` por estrutura; o `fx.As(new(app.Metrics))` fica no `bootstrap/app_module.go`, para que a `observability` não importe o `app`. O `httpapi` recebe o `*slog.Logger` para o log de acesso |
| `.env.example` | `OIDC_ISSUER=http://localhost:8080/realms/pda` e `OIDC_JWKS_URL=http://keycloak:8080/realms/pda/protocol/openid-connect/certs` |
| `docker-compose.yml` | As réplicas passam a depender de `keycloak: service_healthy` (decisão 15) |
| `.golangci.yml` | A regra `app` do `depguard` exclui os testes (`!$test`), decisão 26 |
| `go.mod` | `github.com/coreos/go-oidc/v3` v3.21.0 (e o `go-jose` v4 que ele traz, usado também pelo `testkit`) e `github.com/getkin/kin-openapi` v0.149.0 (só testes) |

---

## 8. `testkit`

| Arquivo | Conteúdo |
| --- | --- |
| `app.go` | `env.StartApp(ctx) (*App, stop func(), error)` cria as filas isoladas (variante de `CreateQueues` sem `tb`, com a chave raiz, decisão 25), aponta a cadeia AWS do processo para elas com `os.Setenv` (decisão 21), preenche os `OIDC_*` (JWKS em `localhost`, skew 1 s) e os `REFERENCE_*` acelerados do test-plan §3.3, e sobe `fx.New(bootstrap.Options()..., fx.Replace(cfg))` numa porta livre, com os logs capturados |
| `auth.go` | `Token(tb, client)` real (`client_credentials` em `localhost:8080`, segredos do `.env.example`, cache por client, renovação perto do `exp`) e os forjados: chave RSA do teste, `alg=none`, HS256 com a chave pública como segredo, `iss` do realm `other`, `aud` do `no-audience-client` e expirado do `provider-short-lived` |
| `contract.go` | Carrega `api/openapi.yaml` (router `legacy` do kin-openapi) e valida requisição e resposta com o `openapi3filter` (autenticação tratada como no-op); regras de pulo da decisão 20 |
| `api.go` | `Client` por client do Keycloak, com um `RoundTripper` que passa tudo pelo `contract.go`; helpers `OpenWallet`, `PostWager`, `GetWallet`, `Ledger`, `GetTransaction`, `GetByExternalID`, `Reconcile` e `Raw` (para os negativos) |
| `assert.go` | `(*App).AssertWalletConsistent(tb, walletID)`: itens 1 (reconciliação pela API, `consistent` e `difference = 0.00`), 2–6 (`LedgerProblems`) e **7 (`OutboxProblems`, novo)**: toda transação terminal tem exatamente os eventos da matriz do lifecycle §7. Registrado no `Cleanup` por todo `OpenWallet` do `testkit`. `SnapshotCounts(tb, pool)`, global (o A03 roda sem `t.Parallel`); o `Eventually` fica para o M4/M6 (decisão 25) |

`OutboxProblems` recebe um teste de sensibilidade como o do `LedgerProblems`: banco próprio, cada divergência gravada à mão e reportada.

---

## 9. Testes

**Unitários (sem infraestrutura):**

| ID | Teste | Pacote | Asserções |
| --- | --- | --- | --- |
| U13 | `TestDomainErrorKind` | `app` | Decisão 6, sentinela por sentinela |
| U14 | `TestLedgerCursor` | `app` | Ida e volta; base64 inválido, JSON inválido, campo extra, `v < 1`, `v` não inteiro → `INVALID_FIELD`/`cursor`; `limit` 0, 201 → `INVALID_FIELD`/`limit` |
| U15 | `TestAuthPolicy` | `auth` | Matriz D-07 inteira, inclusive `provider` sem `provider_id` e `OPENING` sem provedor |
| U16 | `TestVerifier` | `auth` | JWKS local (`httptest`): aceita RS256 válido; recusa assinatura de outra chave, `alg=none`, HS256, `iss` e `aud` errados, expirado além do skew; aceita expirado dentro do skew; JWKS inacessível ou vazio → start falha |
| U17 | `TestHTTPEdge` | `httpapi` | Com stubs: decodificação (decisão 11, `MALFORMED`, 64 KB, dados depois do JSON), 415, `Idempotency-Key` repetida, `PROVIDER_MISMATCH`, mapeamento da §6.3, `Retry-After`, correlação (aceita, recusa e gera), 404/405 de rota, `panic` → 500 |
| I15 | `TestOpenAPIContract` | `httpapi` | O documento valida (com exemplos) e o conjunto método + path é idêntico à tabela de rotas |
| — | `TestConfig` (existente) | `config` | Padrões e validação das variáveis novas |

**Integração do `app` (`internal/app/*_integration_test.go`, PostgreSQL real):**

| ID | Teste | Asserções |
| --- | --- | --- |
| I20 | `TestOpenWallet` | Saldo positivo: carteira v1 + `OPENING` + crédito + 2 eventos; saldo zero: só a carteira; segunda abertura → `WALLET_ALREADY_EXISTS`; entradas inválidas com código e `field` |
| I21 | `TestProcessWager` | Todos os desfechos (PROCESSED com e sem movimento, REJECTED, PENDING_REFERENCE), replay com o saldo original, os dois 409, `UNKNOWN_WALLET` sem escrita, antecipação das pendências dependentes quando a referência chega, `FAILED` por overflow de crédito |
| I22 | `TestProcessWagerRaces` | Mesma chave em duas carteiras em paralelo: um processa, o outro termina em 409 depois da releitura; nenhuma escrita parcial |
| I03b | `TestPermanentFailureRecorded` | Decorador sobre o `Repos` real injeta um erro permanente no `Outbox().Insert`: `FAILED` gravado em UoW separada, sem lançamento nem saldo alterado; replay devolve o mesmo `FAILED`. A parte SQS é do M5 |

**Integração da API (`test/integration/`, app em processo + Keycloak real):**

| ID | Teste | Asserções |
| --- | --- | --- |
| A01a, A01b | `TestAuthRealIdP`, `TestAuthRejects` | Test-plan §5.3; o expirado espera `lifespan + skew + 1 s` (≈ 7 s) |
| A02a–c | `TestProviderIsolation*`, `TestInternalOperationsRestricted` | Test-plan §5.3 |
| A03 | `TestUnauthorizedHasNoEffects` | `SnapshotCounts` antes e depois de cada caso de A01b e A02 |
| A04 | `TestPublicEndpoints` | Só `/health/*`, `/docs` e `/openapi.yaml` respondem sem token, entre as rotas da tabela |
| I08 | `TestReconciliation` | Consistente; divergência forçada (owner, triggers desligados, só neste teste) aparece na resposta, no log capturado e na métrica, sem alterar o saldo |
| I09 | `TestLedgerPagination` | 120 lançamentos, `limit=50` → 3 páginas, sem repetição nem lacuna |
| I10 | `TestReplayReturnsOriginalBalance` | Test-plan §5.2 |
| I11 | `TestReversalRules` | Cenários C4 e C5 do lifecycle, reversões de WIN e REFUND, `REVERSAL_INSUFFICIENT_FUNDS` ≠ `INSUFFICIENT_FUNDS` |
| I12 | `TestHTTPErrorContract` | Cada linha do catálogo §5.3: status, `Content-Type` e `code`. `INTERNAL_ERROR` e `TEMPORARILY_UNAVAILABLE` ficam no U17 (mapeamento) e no R01 (503 real, M9), porque não são provocáveis sem sabotagem |
| — | `TestHappyPathFlow` | O fluxo do "pronto quando": abrir → BET → replay → rejeição → consultas → reconciliação |
| C01a | `TestSameBet50xHTTP` (em processo) | 1 transação, 1 débito, 49 replays com o mesmo `transactionId` |
| C02 | `TestTwoBetsCompete` (em processo) | 20 repetições: 1 `PROCESSED`, 1 `INSUFFICIENT_FUNDS`, saldo 20.00, 1 débito; reenvios idênticos |
| I07a | `TestFxGraph` (existente) | Passa a resolver `auth`, `app` e o `httpapi` completo |

O plano acrescenta os testes que o TDD de cada peça pede: `TestQueries`, `TestListLedger` e `TestReconcile` (`app`), `TestModule` e `TestVerifierCheckKeys` (`auth`), `TestNewServerTimeouts`, `TestMetrics_ReconciliationDivergences`, `TestContract` (o validador do `testkit`), `TestOutboxProblemsDetectsDivergence`, `TestHarness`, `TestOpenWalletAPI` e `TestLedgerAPI`.

Checagem de sensibilidade (§4.3) no C01a e no C02: sem o `FOR UPDATE` e sem a rechecagem sob lock, eles precisam falhar. Todos os testes que criam carteira passam pelo `AssertWalletConsistent` no `Cleanup`.

---

## 10. Requisitos no encerramento

- **Completos:** AUTH-01..08, AUTH-10 (no `ARCHITECTURE.md`), HTTP-01..07, HTTP-09, IDEM-02, IDEM-08, CONC-01..03, CONC-05 (em processo; o C02 com 3 processos segue no M8), DOC-06, TST-A01..A03, TST-I03.
- **Parciais:** DOM-06 (casos de uso com `ctx`; o consumidor e os workers vêm depois), OBS-02 (log de acesso sem headers nem corpo; a prova é o I14 do M7), IDEM-01 (persistente no HTTP; o reinício de todos os processos é o I06/C08), IDEM-04 (o DTO HTTP; o envelope SQS é do M5), FX-01 (+`auth` e `app`; workers no M4–M6), TST-C01/TST-C02 (em processo).
- **Eliminatórios:** E1, E2, E5 (HTTP) e E6 (HTTP).

---

## 11. Ajustes em `docs/` (feitos junto com esta spec)

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-20: exceções da validação de requisição e I15 contra a tabela de rotas. D-04: tipo JSON errado, `INTERNAL_ERROR`, 404/405 de rota, `Retry-After: 1`, formato do `problem+json`, `FAILED` × `INTERNAL_ERROR` no 500. D-07: `OIDC_CLOCK_SKEW`, tolerância do `nbf` da biblioteca, `provider` sem claim, JWKS no start e réplicas dependentes do Keycloak. D-16: paginação estrita. D-18: `app.Metrics` |
| [`transaction-lifecycle.md`](../../transaction-lifecycle.md) | §3.1: tipo JSON errado e `Idempotency-Key` repetida. §5.3: `INTERNAL_ERROR`, `ROUTE_NOT_FOUND`, `METHOD_NOT_ALLOWED`. §6.1: corridas com até 3 tentativas e o caso da pré-checagem |
| [`test-plan.md`](../../test-plan.md) | U13–U17; I15 unitário; I12 sem os dois códigos não provocáveis; I20–I22; I03b com decorador; A01b com a espera de 7 s; C01a e C02 também em processo; `OutboxProblems` no §6 |
| [`structure.md`](../../structure.md) | Sai o `fakes_test.go`; entram `auth` completo, `testkit/auth.go`, `contract.go`, `api.go`, `assert.go` e `StartApp` |

No encerramento: `ARCHITECTURE.md`, `delivery-requirements.md`, `implementation-plan.md` e o diário.

---

## 12. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| Marco grande (~5 h) estourar o dia 1 | O plano segue a ordem contrato → `app` → `auth` → `httpapi` → testes da API; o checkpoint do dia 1 aceita transbordar para o início do dia 2, sempre antes do M4 |
| Validação do kin-openapi divergir do servidor em detalhes (formatos, `additionalProperties`, `Content-Type` com `charset`) | Documento validado com exemplos já na spec; o I15 roda no `go test ./...`; divergências viram ajuste no contrato e nesta spec |
| Testes de auth lentos (token expirado) ou instáveis | Skew de 1 s nos testes, cache de tokens por client e um único teste que espera o vencimento |
| JWKS do Keycloak indisponível no start das réplicas | `depends_on: keycloak: service_healthy` e fail fast com erro claro |
| Corrida rara de idempotência entre carteiras diferentes não coberta | I22 força o cenário com barreira de largada e repetição |
| Log de acesso vazar dados | Lista fixa de campos; I14 no M7 verifica a ausência de segredos |
