# Requisitos de Entrega

Checklist rastreável de tudo o que o [`CHALLENGE.md`](../CHALLENGE.md) exige. Cada requisito tem um ID estável, a seção de origem no desafio (`§n`) e a evidência que comprova o atendimento. Decisões de design ficam fora deste documento: aqui está o **quê**, não o **como**.

**Legenda:** `[ ]` pendente · `[~]` em andamento · `[x]` concluído e verificado · ⛔ critério eliminatório · ⭐ diferencial opcional

---

## 0. Critérios eliminatórios (gate de entrega)

Nenhum item abaixo pode falhar. Antes da entrega, cada um deve ter pelo menos um teste automatizado que o comprove.

| # | Critério eliminatório | Requisitos relacionados |
| --- | --- | --- |
| E1 | Ausência de autenticação efetiva nos endpoints de negócio | AUTH-01..07 |
| E2 | Acesso não autorizado a operações ou transações | AUTH-04..07, TST-A02, TST-A03 |
| E3 | Cálculo monetário em ponto flutuante | MON-01 |
| E4 | Saldo negativo por concorrência | WAL-04, CONC-*, DB-03 |
| E5 | Movimentação duplicada | IDEM-*, SQS-03, LED-03, OPS-08 |
| E6 | Idempotência restrita à memória | IDEM-01 |
| E7 | Dependência de uma única instância para funcionar corretamente | CONC-01, CONC-04, TX-09, OUT-03, TST-C04 |
| E8 | Publicação anterior ao commit | OUT-02, OUT-10 |
| E9 | Ausência de ledger auditável | LED-* |
| E10 | Substituição integral de PostgreSQL, SQS e IdP por mocks nos testes | TST-I*, TST-A01 |

### 0.1 Cenários de falha obrigatórios (§3)

Nenhum destes cenários pode gerar movimentação duplicada, saldo negativo ou perda de um evento confirmado no banco. Cada um tem requisitos e testes dedicados:

| # | Cenário (§3) | Requisitos | Testes ([`test-plan.md`](test-plan.md)) |
| --- | --- | --- | --- |
| F1 | Recebimento repetido da mesma operação, inclusive por HTTP e SQS | IDEM-*, SQS-03, SQS-11 | C01a/b, C10a/b, I04a |
| F2 | Reversão antes da transação referenciada | OPS-12..14 | C07a/b, I06 |
| F3 | Operações simultâneas na mesma carteira | CONC-*, WAL-08 | C01a, C02, C10b |
| F4 | Encerramento abrupto antes ou depois de um commit | SQS-05, SQS-09, TX-09, OUT-06 | C05a–c, C06a/b, C08a/b, R03 |
| F5 | Publicação repetida de um evento de integração | OUT-05, OUT-07 | C06a, I05a |
| F6 | Indisponibilidade temporária do PostgreSQL ou do SQS | SQS-07, OUT-04, HTTP-08 | R01, R02 |

Em todos eles, a verificação de consistência de [`test-plan.md`](test-plan.md) §6 confirma que não houve duplicidade nem saldo negativo, e a asserção de outbox vazia confirma que nenhum evento confirmado se perdeu.

---

## 1. Artefatos obrigatórios no repositório (§4, §15)

- [x] **ART-01** Código-fonte Go formatado com `gofmt`. *(M0, 29/09: `make check` (`fmt-check`: `gofmt -l` vazio + gofumpt/goimports).)*
- [x] **ART-02** `go.mod` e `go.sum` versionados, com a versão do Go declarada em `go.mod`. *(M0, 29/09: `go.mod` com `go 1.27.1` e `go.sum`; `make tidy-check`.)*
- [x] **ART-03** `Dockerfile` declarando a mesma versão do Go. *(M0, 29/09: `make go-version-check` (go.mod = Dockerfile = 1.27.1).)*
- [x] **ART-04** `docker-compose.yml` subindo aplicação, PostgreSQL, Keycloak (ou outro IdP) e LocalStack/MiniStack. *(M0, 29/09: `docker compose up --build --wait`: postgres, keycloak, ministack, aws-init e app-1..3 saudáveis.)*
- [x] **ART-05** Migrations versionadas, com `up` e `down`. *(M2, 29/09: `TestMigrationsUpDownUp`; serviço `migrate` e `make migrate-up`/`migrate-down`.)*
- [x] **ART-06** Provisionamento automático das filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo` com redrive, além do destino dos eventos de saída. *(M0, 29/09: `deploy/aws/init.sh`; `TestProvisioning` (redrive `maxReceiveCount=10`, tópico FIFO, assinatura raw).)*
- [x] **ART-07** Provisionamento automático do IdP (realm, clients, roles/scopes e identidades de teste). *(M0, 29/09: `deploy/keycloak/realm-*.json` importados; tokens reais conferidos com `scripts/get-token.sh`.)*
- [ ] **ART-08** `README.md` completo (ver DOC-01).
- [~] **ART-09** `ARCHITECTURE.md` completo (ver DOC-02).
- [x] **ART-10** `.env.example` com valores locais e nenhum segredo real. *(M0, 29/09: `.env.example` só com valores locais; chaves AWS fora dele (geradas pelo `aws-init`).)*
- [ ] **ART-11** Os comandos abaixo, ou equivalentes documentados, funcionam a partir de um checkout limpo:
  ```sh
  docker compose up --build
  go test ./...
  go test -race ./...
  go vet ./...
  ```

---

## 2. Autenticação e autorização (§2)

- [x] **AUTH-01** ⛔ Integração com um IdP externo OAuth 2.0/OIDC (Keycloak recomendado) executando no Docker Compose. Cadastro de senhas e emissão própria de tokens ficam fora do escopo. *(M3, 29/09: A01a `TestAuthRealIdP` contra o Keycloak do compose, com o realm importado.)*
  *Evidência:* container do IdP no compose e teste de integração obtendo um token real.
- [x] **AUTH-02** ⛔ Validação completa do token: assinatura via JWKS, `iss`, `aud`, `exp`/`nbf` e algoritmo permitido. Credencial ausente, inválida ou expirada resulta em rejeição. *(M3, 29/09: A01b `TestAuthRejects` (sem token, malformado, assinatura forjada, `alg=none`, HS256, `aud` errado, outro realm, expirado); U16 `TestVerifier`; fail fast do JWKS em `TestFxFailFast` e `TestModule`.)*
  *Evidência:* TST-A01.
- [x] **AUTH-03** Fluxo `client_credentials` para a comunicação entre serviços. *(M3, 29/09: `TestAuthRealIdP` e todos os testes da API usam tokens reais de `client_credentials` (`testkit.Token`).)*
- [x] **AUTH-04** ⛔ O `providerId` autorizado vem da identidade autenticada, nunca apenas do corpo da requisição. Divergência entre token e corpo é rejeitada. *(M3, 29/09: A02b `TestProviderIsolationReplay`; `TestSubmitWagerHandler` (`PROVIDER_MISMATCH` antes do caso de uso); U15 `TestAuthPolicy`.)*
- [x] **AUTH-05** ⛔ Provedores acessam apenas suas próprias transações, inclusive em replays e consultas (`GET /wagering/transactions/:id` e `GET /providers/:providerId/...`). *(M3, 29/09: A02a `TestProviderIsolationQueries` (404 por id, 403 no path), A02b `TestProviderIsolationReplay`; `TestTransactionReadHandlers`.)*
- [x] **AUTH-06** ⛔ Operações de carteira (abertura, leitura, ledger, reconciliação) ficam restritas ao serviço interno. *(M3, 29/09: A02c `TestInternalOperationsRestricted`; `TestEdgeAuthentication`.)*
- [x] **AUTH-07** ⛔ Um acesso não autorizado não produz efeito financeiro nem expõe dados. A autorização acontece antes de qualquer escrita ou consulta de idempotência. *(M3, 29/09: A03 `TestUnauthorizedHasNoEffects` (contagem de todas as tabelas antes e depois); a autorização roda antes do caso de uso.)*
- [x] **AUTH-08** `GET /health/live` e `GET /health/ready` são públicos. *(M3, 29/09: A04 `TestPublicEndpoints` (só health e docs sem token); `TestEdgeAuthentication`.)*
- [x] **AUTH-09** O acesso à mensageria é controlado por credenciais e políticas do broker, e o consumidor continua aplicando as validações de domínio. *(M0, 29/09: M0: MiniStack `AUTH=true`, usuários IAM com políticas de identidade; `TestProvisioning` prova uma permissão e uma negação. Matriz completa no I04f (M5).)* *(M5, 29/09: I04f `TestBrokerPoliciesEnforced` aplica os documentos de `deploy/aws/policies/` a usuários IAM do teste e prova permissões e negações; o consumidor valida tudo pelo domínio (I04c).)*
- [x] **AUTH-10** A escolha do IdP, a validação de credenciais e o modelo de permissões estão justificados no `ARCHITECTURE.md`. *(M3, 29/09: `ARCHITECTURE.md` §10.)*

---

## 3. Money (§5.1, §6.1)

- [x] **MON-01** ⛔ Nenhum `float32`/`float64` no parsing, no cálculo, na serialização ou na persistência de dinheiro. *(M1, 29/09: U01g `TestNoFloatInMoney` (AST) + `forbidigo`; `TestMoneyJSON` (valores como string).)*
  *Evidência:* código do tipo, marshal/unmarshal próprios e testes.
- [x] **MON-02** Value object imutável com valor e moeda, suportando: criação a partir de string decimal, zero por moeda, soma, subtração, negação, comparação e serialização. *(M1, 29/09: `TestParseMoney`, `TestMoneyArithmetic`, `TestMoneyJSON`.)*
- [x] **MON-03** Representação em `int64` com unidades mínimas ou decimal exato, com limites documentados. *(M1, 29/09: `int64` em centavos; limites em `TestParseMoney` e `ARCHITECTURE.md` §2.)*
- [x] **MON-04** Contrato externo `{"amount":"25.00","currency":"BRL"}`, com escala fixa de 2 casas e moeda ISO 4217. *(M1, 29/09: `TestMoneyJSON`, `TestParseCurrency`.)*
- [x] **MON-05** Rejeição de valores vazios, `NaN`, `Infinity`, notação científica, escala excedente e negativos nas entradas externas. Nada é arredondado silenciosamente. *(M1, 29/09: `TestParseMoney`, `FuzzParseMoney`.)*
- [x] **MON-06** Se formas equivalentes forem aceitas (ex.: `"25"` → `"25.00"`), a normalização anterior ao hash de idempotência está documentada. *(Para `amount`, não se aplica: o formato é estrito e nenhuma forma equivalente é aceita (D-03). A única normalização, UUID em minúsculas, está documentada em D-08.)* *(M1, 29/09: não se aplica ao `amount` (D-03); a normalização de UUID é coberta por `TestPayloadHashHTTPEqualsSQS`.)*
- [x] **MON-07** Aritmética e comparação exigem moedas compatíveis, com erro tipado quando não forem. *(M1, 29/09: `TestMoneyCurrencyMismatch` (`ErrCurrencyMismatch`).)*
- [x] **MON-08** Com `int64`, overflow é tratado no parsing, na soma, na subtração e na negação (incluindo `math.MinInt64`). *(M1, 29/09: `TestParseMoney`, `TestMoneyArithmetic` (inclui `Negate(MinInt64)`), `TestSettleEdgeCases`.)*
- [~] **MON-09** Valores negativos são permitidos em cálculos internos, mas não no saldo da carteira. *(M1, 29/09: domínio: `TestMoneyArithmetic` (negativos internos), `TestWalletDebit` (saldo nunca negativo). Falta o `CHECK` no banco (M2).)*
- [ ] **MON-10** A persistência preserva exatamente valor e moeda (ex.: `BIGINT` em unidades mínimas + `CHAR(3)`).
- [x] **MON-11** Um `Money` não inicializado (zero value do struct) é rejeitado. *(M1, 29/09: `TestMoneyZeroValue`.)*
- [x] **MON-12** Os cenários podem usar apenas BRL, desde que o tipo carregue a moeda e existam testes de incompatibilidade entre moedas. *(M1, 29/09: `TestMoneyCurrencyMismatch`.)*

---

## 4. Modelo de domínio: regras gerais (§6)

- [x] **DOM-01** Entidades com estado encapsulado, construtores com validação e métodos explícitos de transição. *(M1, 29/09: `TestTransactionStateMachine`, `TestTransitionArgumentValidation`, `TestWalletDebit`.)*
- [x] **DOM-02** Criação e reidratação separadas. A reidratação não reaplica movimentações, transições nem emissão de eventos. *(M1, 29/09: `TestWalletRehydrate`, `TestRehydrate`.)*
- [x] **DOM-03** Valores de domínio não inicializados ou inválidos são rejeitados. *(M1, 29/09: `TestZeroValuesRejected` (money, wallet, wagering), `TestParseEnums`, `TestParse` (ident).)*
- [x] **DOM-04** Erros de domínio classificáveis por tipo ou por `errors.Is`/`errors.As`. *(M1, 29/09: erros sentinela e `*ValidationError`/`*ConflictError` verificados com `errors.Is/As`; `TestClassify`.)*
- [x] **DOM-05** Nenhum `panic` representa rejeição de negócio. *(M1, 29/09: `FuzzParseMoney`; rejeições são estados (`TestKindRules`), nunca `panic`.)*
- [~] **DOM-06** Toda operação de I/O recebe `context.Context` e respeita cancelamento e timeout. *(M3, 29/09: os casos de uso recebem e respeitam o `ctx`; I16 (M2). Consumidor e workers nos M5–M6.)*
- [x] **DOM-07** O domínio não depende de Fx, HTTP, SQS nem de bibliotecas de persistência. *(M1, 29/09: U10 `TestDomainHasNoInfraImports` + `depguard`.)*
  *Evidência:* teste que verifica os imports do pacote de domínio (ex.: `go list -deps`) ou regra de lint.

---

## 5. Wallet (§6.2)

- [x] **WAL-01** Campos: id, playerId, moeda, saldo, versão, `createdAt` e `updatedAt`. *(M1, 29/09: `TestWalletOpen`.)*
- [x] **WAL-02** Criação, reidratação e operações de débito/crédito expostas pelo agregado. *(M1, 29/09: `TestWalletDebit`, `TestWalletCredit`, `TestWalletRehydrate`.)*
- [x] **WAL-03** O par `(playerId, currency)` é único no banco. Uma segunda abertura resulta em conflito. *(M2, 29/09: `TestConstraints` (`wallets_player_currency_uq`), `TestWalletRepository` (`ErrWalletAlreadyExists`, `KindConflict`). O 409 HTTP é do M3.)*
- [~] **WAL-04** ⛔ Débitos preservam saldo `>= 0`, no domínio e por `CHECK` no banco. *(M1, 29/09: domínio: `TestWalletDebit`, `TestKindRules`. Falta a concorrência (M3/M8).)* *(M2, 29/09: `CHECK`: `TestConstraints` (saldo negativo).)*
- [x] **WAL-05** A moeda da movimentação coincide com a moeda da carteira. *(M1, 29/09: `TestWalletDebit` (moeda), `TestSettleEvaluationOrder`.)*
- [x] **WAL-06** Toda mudança de saldo tem o lançamento de ledger correspondente no mesmo commit. *(M2, 29/09: `TestLedgerCoupling`, `TestDomainFlowsPersist`, `TestFinancialAtomicity`.)*
- [x] **WAL-07** A versão inicial é `1` e só é incrementada quando o saldo muda (`LOSS` não incrementa). *(M1, 29/09: `TestWalletOpen`, `TestWalletDebit`, `TestSettleEdgeCases` (LOSS).)* *(M2, 29/09: trigger `PDA03`: `TestGuardTriggers`.)*
- [ ] **WAL-08** ⛔ Disputas entre escritores não descartam uma atualização confirmada (sem lost update).
- [ ] **WAL-09** A estratégia de controle de concorrência está documentada.

---

## 6. WagerTransaction (§6.3)

- [~] **TX-01** Tipos `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. `OPENING` recebido por HTTP ou SQS é rejeitado. *(M1, 29/09: `TestNewCommand` (`OPENING_NOT_ALLOWED`), `TestParseEnums`. Faltam as bordas HTTP/SQS (M3/M5).)*
- [~] **TX-02** Uma transação externa registra: id interno, id externo, provedor, chave de idempotência, hash do payload, carteira, jogador, rodada, jogo, tipo, `Money`, referência externa opcional, estado e timestamps. *(M1, 29/09: `TestNewExternal`, `TestRehydrate`. Falta a persistência (M2).)*
- [~] **TX-03** Quando aplicável, persiste também: a referência interna resolvida, o `failureCode` e o resultado financeiro devolvido ao provedor (saldo observado). *(M1, 29/09: `TestTransitionResults`, `TestRehydrate`. Falta a persistência (M2).)*
- [~] **TX-04** Uma transação `OPENING` registra identidade interna estável, carteira, jogador, moeda, valor, estado e timestamps. Os campos externos não se aplicam. *(M1, 29/09: `TestNewOpening`, `TestOpening`. Falta a persistência (M2).)*
- [x] **TX-05** O schema distingue origem interna de externa (ex.: coluna `origin` + `CHECK`) e impede um crédito inicial duplicado (índice único parcial). *(M2, 29/09: `TestConstraints` (`wager_tx_origin_kind`, `wager_tx_internal_fields`, `wager_tx_single_opening_uq`).)*
- [x] **TX-06** Máquina de estados validada pelo domínio: `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED`. *(M1, 29/09: `TestTransactionStateMachine`.)*
- [x] **TX-07** Estados terminais (`PROCESSED`, `REJECTED`, `FAILED`) não aceitam novas transições. *(M1, 29/09: `TestTransactionStateMachine` (terminais → `ErrInvalidTransition`).)* *(M2, 29/09: trigger `PDA02`: `TestTerminalTransactionImmutable`, `TestGuardTriggers`.)*
- [ ] **TX-08** Um replay consulta o resultado persistido sem reaplicar a operação.
- [~] **TX-09** ⛔ Todo `PENDING` confirmado tem retomada durável por outra instância. Operações sem dependências podem ser concluídas de forma síncrona, sem commit intermediário de aceite. *(M2, 29/09: `PENDING` não é persistível e `PENDING_REFERENCE` sempre tem agenda: `TestConstraints`; retomada de uma pendência gravada: `TestDomainFlowsPersist`. Falta o worker (M6) e a prova multi-instância (C08).)* *(M6, 30/09: o worker retoma as pendências gravadas, de qualquer instância: `TestResolveReferences`, `TestRecoveryAfterRestart` (I06), `TestPendingExpiresAfterDowntime` (I06b), `TestConcurrentWorkers` (2 workers, cada um com seu pool, em processo). Falta a prova com 3 processos e o crash do worker (C08b, M8).)*
- [x] **TX-10** A máquina de estados e a distinção entre falha transitória e permanente estão documentadas. *(M1, 29/09: `transaction-lifecycle.md` §1 e §8; `TestClassify`.)*

---

## 7. WalletLedgerEntry (§6.4)

- [x] **LED-01** Campos: `id`, `walletId`, `transactionId`, direção (`DEBIT`/`CREDIT`), valor, saldo anterior, saldo posterior e `createdAt`. *(M1, 29/09: `TestLedgerEntryInvariant`.)*
- [x] **LED-02** Um lançamento é imutável, e o construtor valida `balanceAfter = balanceBefore ± money`. *(M1, 29/09: `TestLedgerEntryInvariant`.)*
- [x] **LED-03** ⛔ `UNIQUE (walletId, transactionId)` no banco. *(M2, 29/09: `TestConstraints` (`ledger_wallet_tx_uq`).)*
- [x] **LED-04** ⛔ Append-only imposto pelo banco: trigger bloqueando `UPDATE`/`DELETE`/`TRUNCATE` e/ou `REVOKE` de privilégios. *(M2, 29/09: `TestLedgerImmutable` (owner, `PDA01`), `TestLedgerImmutableForApp` (`42501`), `TestAppRolePrivileges`.)*
- [x] **LED-05** `LOSS` e operações rejeitadas não produzem lançamento (garantido também por trigger no banco, [`data-model.md`](data-model.md) §4.2). *(M2, 29/09: `TestLedgerCoupling`, `TestDomainFlowsPersist`.)*
- [x] **LED-06** `CHECK`s no banco: valor `> 0`, coerência before/after conforme a direção e `balanceAfter >= 0`. *(M2, 29/09: `TestConstraints`.)*
- [ ] **LED-07** ⭐ Ledger de partidas dobradas.

---

## 8. Operações e referências (§7)

- [x] **OPS-01** `BET`: débito com valor `> 0` e saldo suficiente. Sem saldo, a operação é `REJECTED` com um código próprio. *(M1, 29/09: `TestKindRules`.)*
- [x] **OPS-02** `WIN`: crédito com valor `> 0`. Pode referenciar uma aposta da mesma rodada. *(M1, 29/09: `TestKindRules`.)*
- [x] **OPS-03** `LOSS`: exige `amount == "0.00"` e a moeda da carteira. Não cria ledger nem altera a versão. Emite `WagerTransactionProcessed` sem `WalletBalanceChanged`. *(M1, 29/09: `TestKindRules`, `TestZeroAmountPolicy`.)*
- [x] **OPS-04** `REFUND`: crédito que devolve integralmente o valor de uma `BET` processada. *(M1, 29/09: `TestKindRules`.)*
- [x] **OPS-05** `ROLLBACK`: movimento contrário que desfaz integralmente uma `BET`, `WIN` ou `REFUND` processada. *(M1, 29/09: `TestKindRules`.)*
- [x] **OPS-06** `referenceExternalTransactionId` é obrigatório em `REFUND`/`ROLLBACK` e é resolvido por `(providerId, referenceExternalTransactionId)`. *(M1, 29/09: `TestNewCommand` (`REFERENCE_REQUIRED`), `TestReferenceResolution`.)*
- [x] **OPS-07** A operação e sua referência concordam em provedor, jogador, carteira, moeda e rodada, e o valor é igual. Reversões parciais não são aceitas. *(M1, 29/09: `TestReferenceResolution` (R5, R6), `TestSettleEvaluationOrder`.)*
- [x] **OPS-08** ⛔ Uma referência não recebe duas reversões bem-sucedidas do mesmo tipo, com garantia no banco. *(M1, 29/09: `TestReferenceResolution` (R7).)* *(M2, 29/09: `wager_tx_single_reversal_uq`: `TestConstraints`, `TestTransactionQueries` (`ErrReversalRace`), `TestDomainFlowsPersist` (`ALREADY_REVERSED`).)*
- [x] **OPS-09** A política para combinações de `REFUND` e `ROLLBACK` sobre a mesma aposta está documentada e impede a devolução duplicada do mesmo débito. *(M1, 29/09: `TestReferenceResolution` (R7, "ROLLBACK after a REFUND") + D-10.)* *(M6, 30/09: I11 `TestReversalRules` por HTTP, com o C2 resolvido pelo worker.)*
- [x] **OPS-10** Uma reversão que debitaria mais que o saldo disponível é `REJECTED`, auditável, com código **diferente** do usado para aposta sem saldo. *(M1, 29/09: `TestKindRules` (`REVERSAL_INSUFFICIENT_FUNDS`), `TestFailureCatalog`.)* *(M6, 30/09: I11 `TestReversalRules` por HTTP, com o C2 resolvido pelo worker.)*
- [x] **OPS-11** Valor zero só é aceito no saldo inicial e em `LOSS`. *(M1, 29/09: `TestZeroAmountPolicy`, `TestOpening`.)*
- [x] **OPS-12** Referência ainda ausente: a operação é persistida como `PENDING_REFERENCE` e um worker tenta de novo com backoff exponencial, inclusive após reinício (agenda persistida no banco). *(M1, 29/09: `TestSettleUnresolvedReference`, `TestReferenceRetryPolicy`. Falta o worker (M6).)* *(M6, 30/09: `TestResolveReferences` (retomada, reagendamento, cadeia em cascata), `TestReferenceWorker*`, `TestRecoveryAfterRestart` (I06: a pendência gravada pelo app 1 é resolvida pelo app 2), `TestPendingReferenceResolved` (C2 por HTTP) e `TestSQSPendingReferenceResolved`.)*
- [x] **OPS-13** Há um máximo de tentativas ou TTL. Ao esgotar, a operação vira `REJECTED` com código de referência não encontrada e emite o evento de rejeição. *(M1, 29/09: `TestSettleUnresolvedReference`, `TestReferenceRetryPolicy`. Falta o worker (M6).)* *(M6, 30/09: `TestResolveReferences` (esgotar por tentativas e por TTL, `REFERENCE_NOT_FOUND`, evento `Rejected` sem `causationId`, sem lançamento), `TestPendingReferenceExpires` (C3 por HTTP), `TestPendingExpiresAfterDowntime` (I06b: o TTL vale com todas as instâncias paradas) e a expiração real no compose, com 7 retries distribuídos entre as 3 réplicas.)*
- [x] **OPS-14** Está documentado o comportamento quando a referência existe mas ainda está pendente, ou quando terminou sem sucesso. *(M1, 29/09: `TestReferenceResolution` (R2, R3), `TestSettleUnresolvedReference`.)*
- [x] **OPS-15** Toda rejeição tem um `failureCode` estável e documentado, que distingue entrada corrigível de resultado definitivo. *(M1, 29/09: `TestFailureCatalog`, `TestEvaluationOrder`, `TestSettleEvaluationOrder`.)*

---

## 9. Idempotência (§5.2, §9)

- [~] **IDEM-01** ⛔ A idempotência é persistente e sobrevive ao reinício de todos os processos. *(M3, 29/09: idempotência persistida e usada pelo HTTP (`TestProcessWager`, `TestSameBet50xHTTP`). O reinício de todos os processos é o I06/C08.)*
- [x] **IDEM-02** O header `Idempotency-Key` é obrigatório no HTTP. O servidor não substitui silenciosamente a chave recebida. *(M2, 29/09: unicidade `(provider_id, idempotency_key)`: `TestConstraints`, `TestTransactionQueries`. O header é do M3.)* *(M3, 29/09: I12 (`MISSING_IDEMPOTENCY_KEY`, `INVALID_IDEMPOTENCY_KEY`); `TestSubmitWagerHandler` (chave repetida, a chave recebida vai intacta ao comando).)*
- [x] **IDEM-03** Hash determinístico dos campos de negócio em JSON canônico com chaves ordenadas. A chave e os metadados de transporte ficam fora do cálculo. Algoritmo, campos e normalizações estão documentados. *(M1, 29/09: `TestPayloadHashGolden` (SHA calculado com `shasum`).)*
- [~] **IDEM-04** O hash é equivalente entre HTTP e SQS para a mesma operação. *(M1, 29/09: `TestPayloadHashHTTPEqualsSQS` sobre a entrada do domínio. Faltam o DTO e o envelope reais (M3/M5).)* *(M3, 29/09: `TestSubmitWagerHandler` (o hash do DTO HTTP é o do comando do domínio). O envelope SQS é do M5.)*
- [x] **IDEM-05** Mesma chave e mesmo conteúdo: devolve o resultado persistido com `idempotentReplay: true`. *(M1, 29/09: `TestIdempotencyDecision`.)*
- [x] **IDEM-06** Mesma chave com conteúdo diferente: conflito. *(M1, 29/09: `TestIdempotencyDecision`.)*
- [x] **IDEM-07** ⛔ `(providerId, externalTransactionId)` não pode ser reaplicado usando outra chave. *(M1, 29/09: `TestIdempotencyDecision`.)* *(M2, 29/09: `wager_tx_external_id_uq`: `TestConstraints`, `TestTransactionQueries` (`ErrIdempotencyRace`).)*
- [x] **IDEM-08** O replay de uma operação concluída devolve o saldo observado no processamento original. *(M3, 29/09: I10 `TestReplayReturnsOriginalBalance`; I21 `TestProcessWager`.)*

---

## 10. Concorrência (§5.6, §5.7, §8)

- [x] **CONC-01** ⛔ Coordenação por carteira. Locks globais são proibidos (nada de lock de tabela ou advisory lock único). *(M3, 29/09: O lock é só da linha da carteira (I19, M2); `TestTwoBetsCompete` falha sem o `FOR UPDATE` (sensibilidade).)*
- [x] **CONC-02** Estratégia escolhida (pessimista, otimista com retry limitado, update condicional ou combinação) justificada. *(M3, 29/09: `ARCHITECTURE.md` §4 e D-09.)*
- [x] **CONC-03** ⛔ As invariantes financeiras valem no banco, independentemente de locks locais e da deduplicação do SQS FIFO. *(M3, 29/09: `TestTwoBetsCompete` (20 disputas); I22 `TestProcessWagerRaces`; o `CHECK` do saldo (I02a, M2).)*
- [ ] **CONC-04** ⛔ Garantias demonstradas com **≥ 3 processos independentes**, cada um com suas conexões e memória.
- [x] **CONC-05** Uma carteira com 100.00 BRL recebe duas apostas simultâneas de 80.00. Resultado esperado: 1 `PROCESSED`, 1 `REJECTED` por saldo insuficiente, saldo final 20.00 e 1 débito no ledger. Reenvios não alteram esse resultado. *(M3, 29/09: C02 `TestTwoBetsCompete` em processo (20 repetições, reenvios idênticos); o C02 com 3 processos vem no M8.)*
- [ ] **CONC-06** Carteiras diferentes são processadas em paralelo.

---

## 11. API HTTP (§9)

- [x] **HTTP-01** `POST /wallets`: segue o contrato do desafio. Com saldo inicial positivo, carteira + `OPENING` `PROCESSED` + lançamento de crédito + outbox (`WagerTransactionProcessed` e `WalletBalanceChanged`) entram no mesmo commit, com versão `1`. Com saldo zero, não há `OPENING`, ledger nem eventos financeiros. Uma carteira duplicada resulta em conflito. *(M3, 29/09: `TestOpenWalletAPI`; I20 `TestOpenWallet`; `TestOpenWalletHandler`.)*
- [x] **HTTP-02** `GET /wallets/:walletId`. *(M3, 29/09: `TestOpenWalletAPI`; `TestQueries`.)*
- [x] **HTTP-03** `GET /wallets/:walletId/ledger?cursor=...&limit=50`: cursor opaco, ordenação estável e limite máximo definido. *(M3, 29/09: I09 `TestLedgerPagination` (120 lançamentos, 3 páginas, cursor opaco); `TestLedgerAPI`; U14 `TestLedgerCursor`.)*
- [x] **HTTP-04** `GET /wagering/transactions/:transactionId`: mostra pendências e códigos de rejeição/falha. *(M3, 29/09: `TestHappyPathFlow`; `TestReversalRules` (pendência com `attempts`, `nextAttemptAt`, `expiresAt`); `TestTransactionReadHandlers`.)*
- [x] **HTTP-05** `GET /providers/:providerId/wagering/transactions/:externalTransactionId`. *(M3, 29/09: `TestHappyPathFlow`; `TestTransactionReadHandlers`.)*
- [x] **HTTP-06** `POST /wagering/transactions`: segue o contrato do desafio (`transactionId`, `status`, `balance`, `idempotentReplay`). *(M3, 29/09: `TestHappyPathFlow`; I21 `TestProcessWager`; `TestSubmitWagerHandler`.)*
- [x] **HTTP-07** `POST /wallets/:walletId/reconciliation`: reconstrói o saldo a partir do ledger (incluindo a abertura) em uma visão consistente (snapshot) e devolve `difference = stored − calculated`. Divergências aparecem na resposta, no log e em uma métrica. **Não altera o saldo.** *(M3, 29/09: I08 `TestReconciliation` (divergência na resposta, no log e em `reconciliation_divergences_total`, sem alterar o saldo); `TestReconcile`.)*
- [x] **HTTP-08** `GET /health/live` (processo) e `GET /health/ready` (PostgreSQL + SQS). *(M0, 29/09: `health_test`, `health_handler_test` e I07b `TestFxLifecycle` (PostgreSQL + SQS).)*
- [x] **HTTP-09** Códigos HTTP e corpos de resposta documentados e **distinguíveis** para: entrada inválida, conflito, rejeição de negócio, processamento pendente e indisponibilidade transitória. *(M3, 29/09: I12 `TestHTTPErrorContract`; U17 `TestWriteError`; I15 `TestOpenAPIContract`.)*

---

## 12. Consumidor SQS (§10)

- [x] **SQS-01** Filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo` provisionadas, com redrive configurado. *(M5, 29/09: `TestProvisioning` (FIFO, redrive com `maxReceiveCount = 10`); filas isoladas com redrive após 3 recebimentos nos testes, provadas pelo I04d `TestTransientFailureRedrive`.)*
- [x] **SQS-02** HTTP e SQS compartilham o mesmo caso de uso e as mesmas garantias. A chave de idempotência vem de `data.idempotencyKey`. *(M5, 29/09: `app.ConsumeWager` chama o mesmo `ProcessWager`; `TestSQSEndToEnd` (SQS e depois HTTP = replay), `TestConsumeWagerReplayAcrossChannels` (HTTP e depois SQS), U05b `TestPayloadHashHTTPEqualsSQS` com o envelope real.)*
- [x] **SQS-03** Inbox com `UNIQUE (consumerName, messageId)`, usando o `messageId` do envelope como identidade durável. O hash é verificado em reentregas. *(M2, 29/09: `inbox_pk`: `TestConstraints`, `TestInboxRepository` (`ErrInboxDuplicate`). O consumidor e o hash são do M5.)* *(M5, 29/09: I04a `TestInboxDeduplication`, I04b `TestInboxHashMismatch`, `TestConsumeWagerInboxRace` (corrida na PK recomeça pelo `Find`), `TestMessageHash`.)*
- [x] **SQS-04** Inbox, conclusão do tratamento, domínio, ledger e outbox compartilham a mesma transação SQL. *(M5, 29/09: `TestConsumeWager` (inbox com o `transaction_id` do resultado), `TestConsumeWagerAtomicInbox` (inbox que falha desfaz tudo; com sensibilidade), `TestConsumeWagerPermanentFailure` (`FAILED` e inbox na mesma UoW separada).)*
- [x] **SQS-05** A mensagem só é removida da fila após o commit do tratamento durável. *(M5, 29/09: `TestDecide` (tabela de ações), I04a (DLQ vazia: sem `DeleteMessage`, a sabotagem é detectada), `TestDLQSendFailure` (envio à DLQ que falha não remove), `TestDeleteFailureIsCounted`. O crash entre commit e delete é o C05a do M8.)*
- [x] **SQS-06** Uma rejeição de negócio confirmada é terminal e permite remover a mensagem. *(M5, 29/09: I04e `TestBusinessRejectionDeletesMessage` (BET sem saldo: `REJECTED`, fila e DLQ vazias, `WagerTransactionRejected` na auditoria).)*
- [x] **SQS-07** Falha transitória leva a retry com backoff. Erro permanente ou tentativas esgotadas levam à DLQ. *(M5, 29/09: I04c `TestInvalidMessagesGoToDLQ`, I04d `TestTransientFailureRedrive`, `TestPermanentFailureToDLQ` (I03b pelo SQS), `TestRetryDelay`, `TestGroupOrder`, `TestDeadlineRelease`, `TestHealthGatePauses` (queda do banco não consome tentativas). A queda real do PostgreSQL com 3 processos é o R01 (M9).)*
- [x] **SQS-08** Com uma `PENDING_REFERENCE` já persistida, a mensagem pode ser concluída e o worker de referências assume a continuidade. *(M5, 29/09: `TestConsumeWager` grava a inbox `PENDING_REFERENCE` e `TestDecide` remove a mensagem. A continuidade é do worker (M6).)* *(M6, 30/09: `TestSQSPendingReferenceResolved`: o REFUND pelo SQS antes da BET fica pendente, a inbox registra `PENDING_REFERENCE`, a fila esvazia e o worker o resolve depois da BET.)*
- [x] **SQS-09** Em `SIGTERM`, o consumidor para de buscar mensagens e conclui o trabalho em andamento dentro do prazo, ou libera a visibilidade para reentrega segura. *(M5, 29/09: `TestConsumerShutdown` (espera o que está em andamento, libera o que não começou, cancela e libera no prazo; goleak), I07b `TestFxLifecycle`; stop ordenado no compose (HTTP → consumidor → publisher). Limitação do long poll órfão no messaging §4.5. Com 30 mensagens e 3 processos no R03 (M9).)*
- [x] **SQS-10** Documentados: limites de tentativas, visibility timeout, tratamento de mensagens inválidas, `MessageGroupId` e `MessageDeduplicationId`. *(M5, 29/09: messaging.md §3–§4 e D-12, com a validação dos prazos (`TestValidate_RejectsInvalidValues`) e o contrato de entrada (`TestParseEnvelope`). O README fecha no M10.)*
- [~] **SQS-11** A concorrência entre as entradas HTTP e SQS foi validada. *(M5, 29/09: os dois canais em sequência (`TestSQSEndToEnd`, `TestConsumeWagerReplayAcrossChannels`) e 10 mensagens da mesma operação em paralelo (`TestConsumeWagerInboxRace`). HTTP e SQS ao mesmo tempo é o C10b (M8).)*

---

## 13. Transactional outbox e eventos (§5.4, §6.5, §11)

- [x] **OUT-01** Tabela de outbox com: identidade estável do evento, agregado, tipo, payload (snapshot imutável), `occurredAt`, tentativas, próximo envio e `publishedAt`. *(M2, 29/09: `TestOutboxRepository`, `TestGuardTriggers` (`PDA05`), `TestConstraints`.)*
- [~] **OUT-02** ⛔ Os registros de outbox são gravados atomicamente com o estado da operação, o saldo, o ledger e a inbox. *(M4, 29/09: outbox na mesma UoW do domínio desde o M3 (I03a `TestFinancialAtomicity`); só linhas confirmadas são publicadas (I05b `TestNoPublishBeforeCommit`). Falta a inbox (M5).)* *(M5, 29/09: a inbox na mesma UoW: `TestConsumeWagerAtomicInbox`.)*
- [x] **OUT-03** ⛔ Um worker separado publica a outbox e suporta múltiplos publishers, disputa por registros (ex.: `FOR UPDATE SKIP LOCKED` + lease) e recuperação de trabalho abandonado. *(M4, 29/09: I05a `TestOutboxConcurrentPublishers` (2 publishers com pools próprios, 200 eventos); `TestOutboxStore` (claims concorrentes disjuntos, lease vencido reassumido); I05d `TestOutboxLeaseRecovery`. Com 3 processos no M8 (C06).)*
- [x] **OUT-04** Retry com backoff em falha de publicação. *(M4, 29/09: I05c `TestOutboxRetryBackoff`; `TestRetryDelay`; `TestPublisherSurvivesClaimFailures`; `TestOutboxBacklogGauges`.)*
- [x] **OUT-05** Republicações preservam o `eventId`. *(M4, 29/09: `TestSNSSinkPublishInput` (payload da coluna, `MessageDeduplicationId = eventId`); I05a (conteúdo igual ao do banco); I05d (evento reassumido publicado com o mesmo `eventId`).)*
- [~] **OUT-06** Recuperação demonstrada em dois cenários: (a) interrupção entre o commit e a publicação; (b) interrupção entre a publicação e a confirmação na outbox. Os eventos pendentes são assumidos por outra instância. *(M4, 29/09: lease vencido reassumido e republicado (I05d); stop gracioso confirma o que está em voo e deixa o resto com o lease (`TestPublisherStop`). Os cenários com processo interrompido são o C05c e o C06 do M8.)*
- [x] **OUT-07** O destino dos eventos de saída está provisionado e os contratos de roteamento e consumo estão documentados. *(M4, 29/09: `api/events.yaml` + messaging §5.2/§7; I05e `TestEventContracts`; `TestResolveTopic`; `TestFxFailFast` (tópico inexistente); `TestProvisioning`.)*
- [x] **OUT-08** Tipos concretos para `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`. *(M1, 29/09: `TestEventConstructors`, `TestSeal`, `TestEnvelopeJSON`.)*
- [x] **OUT-09** Envelope com `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt`, `version` e `data` tipado. Tipo e versão são definidos pelo construtor do evento. *(M1, 29/09: `TestSeal`, `TestEnvelopeJSON`.)*
- [x] **OUT-10** ⛔ Nenhum evento é publicado antes do commit da transação que o originou. *(M4, 29/09: I05b `TestNoPublishBeforeCommit`, com sensibilidade (publicar dentro da transação faz o teste falhar).)*
- [x] **OUT-11** O payload de `WalletBalanceChanged` inclui `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter` e `walletVersion`. *(M1, 29/09: `TestEnvelopeJSON`.)*
- [x] **OUT-12** Timestamps em UTC RFC 3339 e valores monetários como strings decimais. *(M1, 29/09: `TestEnvelopeJSON`.)*
- [x] **OUT-13** Eventos de origem interna (`OPENING`) não exigem os metadados externos inaplicáveis. *(M1, 29/09: `TestEnvelopeJSON`, `TestOpening`.)*

---

## 14. Persistência (§4, §5.3, §5.8)

- [x] **DB-01** PostgreSQL, com `pgx` e SQL explícito (preferencial; `sqlc` opcional). *(M2, 29/09: adapter `postgres`; `TestWalletRepository`, `TestTransactionRepository`.)*
- [x] **DB-02** Transações, locks e constraints explícitos e verificáveis. *(M2, 29/09: `TestUnitOfWork` (commit, rollback, `lock_timeout`, snapshot), `TestPostgresErrorMapping`.)*
- [x] **DB-03** ⛔ Unicidade, não negatividade e imutabilidade do ledger impostas pelo schema, pelas constraints e pelos mecanismos de proteção do banco. *(M2, 29/09: `TestConstraints`, `TestLedgerImmutable`, `TestLedgerCoupling`, `TestTerminalTransactionImmutable`, `TestGuardTriggers`.)*
- [~] **DB-04** Migrations versionadas, com os comandos de aplicação e reversão documentados. *(M2, 29/09: `TestMigrationsUpDownUp`; comandos em `data-model.md` §7 e no `Makefile`. Falta o README (M10).)*
- [x] **DB-05** `ARCHITECTURE.md` documenta a biblioteca escolhida, o mapeamento de `Money` e como a transação SQL é delimitada entre os repositórios. *(M2, 29/09: `ARCHITECTURE.md` §2 e §3.)*

---

## 15. Composição e ciclo de vida com Uber Fx (§4)

- [x] **FX-01** Configuração, conexões, repositórios, casos de uso, handlers e workers compostos com `fx.Module`, `fx.Provide` e `fx.Invoke`, com injeção por construtor. *(M0, 29/09: M0: `config`, `observability`, `postgres`, `aws` e `httpapi` com `fx.Module`/`Provide`/`Invoke`; I07a `TestFxGraph`. Completa no M3–M6.)* *(M3, 29/09: `TestFxGraph` com `auth`, `app` e o `httpapi` completo. Workers nos M4–M6.)* *(M4, 29/09: `TestFxGraph` com `OutboxStore`, `Topic` e `outbox.Publisher`.)* *(M5, 29/09: `TestFxGraph` com `ConsumeWager` e `sqsconsumer.Consumer`.)* *(M6, 30/09: `TestFxGraph` com `ResolveReferences` e `references.Worker`. Completa quando o M7 entregar as flags de papel.)* *(M7, 30/09: flags de papel: `TestRolesFromEnv`, `TestOptionsFor` (cada papel tira o seu módulo do grafo), `TestFxRoles` (I27).)*
- [x] **FX-02** A inicialização valida a configuração e as dependências (fail fast). *(M0, 29/09: I07c `TestFxFailFast` (banco inacessível, fila inexistente, config inválida); `config_test`; `ServeOnLifecycle` com porta ocupada.)*
- [x] **FX-03** Workers com cancelamento, prazos de execução e término observável. *(M4, 29/09: publisher com `OnStop` que cancela e espera, logs de início e fim: `TestPublisherStop`, I07b `TestFxLifecycle` (goleak). Consumidor e worker de referências no M5–M6.)* *(M5, 29/09: consumidor com shutdown em 5 passos: `TestConsumerShutdown`, I07b.)* *(M6, 30/09: worker com `OnStop` que cancela e espera o item em andamento, logs de início e fim: `TestWorkerStop` (goleak), `TestFxLifecycle`; no compose, a ordem HTTP → consumidor → publisher → worker.)* *(M7, 30/09: todos os workers com `OnStop` que cancela e espera; ordem provada em `TestFxLifecycle`.)*
- [~] **FX-04** O shutdown interrompe novas entradas e conclui ou libera o trabalho em andamento. *(M7, 30/09: `TestFxLifecycle` prova a ordem de parada (HTTP → consumidor → publisher → worker); o comportamento com requisições e mensagens em voo é o R03 e o R04 (M9).)*
- [x] **FX-05** As dependências (pool do PostgreSQL, clientes) só são fechadas depois dos componentes que as usam. *(M7, 30/09: `TestFxLifecycle`: o log de fechamento do pool vem depois do fim de todos os componentes, e o pool fica inutilizável; `goleak` limpo.)*

---

## 16. Observabilidade (§12)

- [x] **OBS-01** Logs em JSON com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`, quando disponíveis. *(M7, 30/09: `TestLogsHaveIdsWithoutSecrets` (I14: linhas `wager concluded` do HTTP e do SQS com os IDs e JSON em todas as linhas), `TestWagerConcludedLog`.)*
- [x] **OBS-02** Logs sem credenciais, dados sensíveis ou payloads financeiros completos. *(M3, 29/09: `TestEdgeAccessLog` (sem token), `problem+json` sem ecoar valores. A prova completa é o I14 (M7).)* *(M7, 30/09: I14 com marcadores únicos (token, `amount`, chave: nenhuma linha os contém); `TestReconcile` (o WARN sem saldos); 4 sabotagens detectadas.)*
- [x] **OBS-03** Métricas de: resultados por status, duplicatas, retries, DLQ, conflitos de concorrência, atraso da outbox, latência de processamento e divergências de reconciliação. *(M4, 29/09: as 6 métricas de outbox: `TestMetrics_Outbox`, `TestOutboxBacklogGauges`, I05c, I05d. O resto do catálogo vem no M7.)* *(M5, 29/09: as 9 métricas de SQS: `TestMetrics_SQS`, I04a, I04d, `TestDeadlineRelease`, `TestDLQSendFailure`, `TestDeleteFailureIsCounted`.)* *(M6, 30/09: `reference_pending_transactions`, `reference_retries_total` e `reference_expired_total`: `TestMetrics_References`, `TestWorkerMetrics`; conferidas em `:9091`–`:9093` depois de uma expiração real.)* *(M7, 30/09: catálogo completo: `TestMetrics_Wagers/Reconciliation/Auth/HTTP`, `TestProcessWagerMetrics`, `TestResolveReferencesMetrics`, `TestEdgeRequestMetrics`, `TestEdgeAuthFailureMetrics`, `TestPostgresErrorMapping` (`ErrLockTimeout`), e na ponta `TestMetricsEndpoint` (I25) e `TestConcurrencyConflictMetric` (I26).)*
- [x] **OBS-04** Health checks (HTTP-08). *(M0, 29/09: ver HTTP-08.)*
- [ ] **OBS-05** ⭐ Tracing com OpenTelemetry e dashboards.

---

## 17. Testes (§13)

### 17.1 Unitários

- [x] **TST-U01** `Money`: parsing, operações, escala, limites numéricos, entradas inválidas e incompatibilidade de moedas. *(M1, 29/09: `money_test.go`, `json_test.go`, `money_fuzz_test.go`, `nofloat_test.go`.)*
- [x] **TST-U02** Invariantes da carteira. *(M1, 29/09: `wallet_test.go`, `ledger_entry_test.go`.)*
- [x] **TST-U03** Transições de estado da transação. *(M1, 29/09: `TestTransactionStateMachine`.)*
- [x] **TST-U04** Regras dos cinco tipos externos, incluindo a política de valor zero de cada um. *(M1, 29/09: `TestKindRules`, `TestZeroAmountPolicy`, `TestReferenceResolution`, `TestEvaluationOrder`.)*
- [x] **TST-U05** Conflito de payload com a mesma chave (hash canônico). *(M1, 29/09: `TestPayloadHashGolden`, `TestIdempotencyDecision`.)*
- [x] **TST-U06** Abertura interna: metadados e eventos gerados. *(M1, 29/09: `TestOpening`.)*

### 17.2 Integração (containers reais: PostgreSQL, IdP e LocalStack/MiniStack)

- [ ] **TST-I01** Migrations: `up` e `down`.
- [ ] **TST-I02** Constraints e imutabilidade do ledger.
- [x] **TST-I03** Atomicidade financeira. *(M3, 29/09: I03a `TestFinancialAtomicity` (M2) e I03b `TestPermanentFailureRecorded` (`FAILED` em UoW separada, replay com 500).)*
- [x] **TST-I04** Inbox e reentrega. *(M5, 29/09: I04a–f e os testes do `ConsumeWager` (spec M5 §7).)*
- [x] **TST-I05** Outbox concorrente, retry e DLQ. *(M4, 29/09: I05a–g. A DLQ vem no M5.)* *(M5, 29/09: DLQ por redrive (I04d) e por envio explícito (I04b, I04c, `TestPermanentFailureToDLQ`).)*
- [ ] **TST-I06** Recuperação após reinicialização.
- [x] **TST-I07** Composição Fx: validação do grafo, start e stop, e liberação dos recursos dos workers (sem goroutines vazadas). *(M7, 30/09: `TestFxGraph`/`TestOptionsFor` (I07a), `TestFxLifecycle` (I07b), `TestFxFailFast` (I07c), `TestFxRoles` (I27).)*

### 17.3 Autenticação e autorização

- [x] **TST-A01** ⛔ Integração real com o IdP. Credenciais ausentes, inválidas e expiradas são rejeitadas. *(M3, 29/09: `TestAuthRealIdP` e `TestAuthRejects`.)*
- [x] **TST-A02** ⛔ Isolamento entre provedores em consultas e replays, e restrição das operações internas. *(M3, 29/09: `TestProviderIsolationQueries`, `TestProviderIsolationReplay` e `TestInternalOperationsRestricted`.)*
- [x] **TST-A03** ⛔ Acessos não autorizados não geram efeito financeiro nem expõem dados. *(M3, 29/09: `TestUnauthorizedHasNoEffects`.)*

### 17.4 Concorrência e recuperação

- [~] **TST-C01** A mesma aposta enviada 50× em paralelo gera um único débito. *(M3, 29/09: C01a `TestSameBet50xHTTP` em processo. Com 3 processos no M8.)*
- [~] **TST-C02** Disputa 100.00 vs 2× 80.00 (CONC-05). *(M3, 29/09: C02 `TestTwoBetsCompete` em processo. Com 3 processos no M8.)*
- [ ] **TST-C03** Carteiras distintas processadas simultaneamente.
- [ ] **TST-C04** Cenários relevantes repetidos com **≥ 3 instâncias independentes**.
- [ ] **TST-C05** Consumidor interrompido depois do commit e antes do `DeleteMessage`: a reentrega é tratada corretamente.
- [ ] **TST-C06** Dois publishers disputando a mesma outbox, com recuperação de publicação validada.
- [ ] **TST-C07** `REFUND`/`ROLLBACK` entregue antes da referência: resolução posterior **e** rejeição por expiração.
- [ ] **TST-C08** Após reinício da aplicação, idempotência, pendências e consistência financeira são preservadas. Se houver aceite assíncrono, o processo é interrompido após o `PENDING` e outra instância retoma.
- [ ] **TST-C09** Ao final de cada cenário, o saldo armazenado é igual a Σ créditos − Σ débitos do ledger.
- [ ] **TST-C10** Cenários que cruzam HTTP e SQS para a mesma operação.
- [~] **TST-C11** Os testes de duplicidade exercitam a deduplicação **da aplicação** (não só a do SQS FIFO), com recebimentos repetidos comprovados. *(M5, 29/09: I04a envia a mesma mensagem com `MessageDeduplicationId` diferentes e prova `wager_duplicates_total{sqs,inbox} = 1`. C01b e C10 no M8.)*
- [ ] **TST-C12** `go test -race` executado nos testes aplicáveis.

### 17.5 Opcionais

- [ ] **TST-L01** ⭐ Teste de carga com comando reproduzível, ambiente, metodologia, throughput, p50/p95/p99, erros, conflitos e atraso da outbox.

---

## 18. Documentação da entrega (§15)

- [ ] **DOC-01** O `README.md` cobre: pré-requisitos, variáveis de ambiente, inicialização das filas, aplicação e reversão das migrations, execução da aplicação, exemplos de chamadas (incluindo como obter o token) e comandos de teste.
- [~] **DOC-02** O `ARCHITECTURE.md` registra as decisões sobre: dinheiro, transações, idempotência, locks, referências pendentes, reversões, inbox/outbox, autenticação, autorização, uso do Fx e shutdown. Também reúne as justificativas pedidas ao longo do desafio (IdP, biblioteca de banco, mapeamento de Money, fronteira transacional, concorrência, máquina de estados, falhas transitórias vs permanentes, hash, códigos HTTP, parâmetros SQS e roteamento de eventos).
- [~] **DOC-03** O `ARCHITECTURE.md` explicita limitações, interpretações adotadas e trabalho não concluído. *(Interpretações e limitações escritas em 28/09; trabalho não concluído é fechado na entrega.)*
- [ ] **DOC-04** Há um documento separado sobre como preparar as dependências dos testes e executar a integração, as múltiplas instâncias e as simulações de falha, incluindo build tags, se usadas.
- [ ] **DOC-05** Há instruções para executar os fluxos autenticados com as identidades de teste provisionadas.
- [x] **DOC-06** ⭐ Contrato OpenAPI (`api/openapi.yaml`) servido em `/openapi.yaml`, Swagger UI autenticável em `/docs` e coleção `api/requests.http`, com o contrato validado nos testes (D-20). *(M3, 29/09: `api/openapi.yaml`, `/docs`, `/openapi.yaml` e `api/requests.http`; I15 `TestOpenAPIContract`; `TestContract` (o validador do `testkit`); `TestEdgeDocs`.)*

### 18.1 Onde cada exigência de documentação é atendida

| Exigência do desafio | Onde |
| --- | --- |
| Escolha do IdP, validação de credenciais, modelo de permissões (§2) | `ARCHITECTURE.md` §10 · D-07 |
| Biblioteca de banco, mapeamento de `Money`, delimitação da transação (§4) | `ARCHITECTURE.md` §2–§3 · D-01, D-03, D-14 |
| Aplicação e reversão das migrations (§4, §15) | `README.md` (M10) · `data-model.md` §7 |
| Representação e limites de `Money`; normalização antes do hash (§6.1) | `ARCHITECTURE.md` §2 · D-03, D-08 |
| Estratégia de concorrência (§6.2) | `ARCHITECTURE.md` §4 · D-09 |
| Máquina de estados; falha transitória × permanente (§6.3) | `ARCHITECTURE.md` §5 · `transaction-lifecycle.md` §1, §8 |
| REFUND × ROLLBACK; referência pendente ou que falhou; `failureCode` (§7) | `ARCHITECTURE.md` §7–§8 · `transaction-lifecycle.md` §4–§5 |
| Algoritmo, campos e normalizações do hash (§9) | `ARCHITECTURE.md` §6 · D-08 |
| Códigos HTTP e corpos por situação (§9) | `ARCHITECTURE.md` §14 · `api/openapi.yaml` (M3) · D-04 |
| Tentativas, visibility timeout, mensagens inválidas, `MessageGroupId` e `MessageDeduplicationId` (§10) | `ARCHITECTURE.md` §9.1 · `messaging.md` §3–§4 |
| Contratos de roteamento e consumo dos eventos (§11) | `ARCHITECTURE.md` §9.2 · `messaging.md` §5–§7 |
| Decisões de dinheiro, transações, idempotência, locks, referências, reversões, inbox/outbox, auth, Fx e shutdown (§15) | `ARCHITECTURE.md` §2–§12 |
| Limitações, interpretações e trabalho não concluído (§15) | `ARCHITECTURE.md` §15–§17 |
| README: pré-requisitos, variáveis, filas, migrations, execução, exemplos, testes, IdP e identidades (§15) | `README.md` (M10) |
| Preparação dos testes, integração, multi-instância, falhas e build tags, em documento separado (§15) | `docs/testing.md` (M10) |
| Teste de carga ⭐ (§14) | `docs/load-test.md` (M12) |

---

## 19. Mapa de pontuação (§14)

| Critério | Pts | Requisitos que sustentam o critério |
| --- | ---: | --- |
| Integridade financeira | 20 | MON, WAL, LED, OPS, HTTP-07 |
| Concorrência | 20 | CONC, WAL-08, TST-C01..C04 |
| Idempotência | 15 | IDEM, SQS-03, TX-08 |
| Mensageria e recuperação | 15 | SQS, OUT, TX-09, OPS-12..13, TST-C05..C08 |
| Modelagem e arquitetura | 10 | DOM, TX-06..07, FX, AUTH |
| Testes | 10 | TST-* |
| Observabilidade | 5 | OBS |
| Documentação | 5 | DOC, ART |

---

## 20. Interpretações em aberto

Pontos que o desafio deixa para o candidato decidir. ✅ **Todos resolvidos em [`decisions.md`](decisions.md)** (D-02, D-03, D-04, D-07, D-08, D-10, D-11, D-12, D-13, D-18, D-19) e consolidados como interpretações adotadas no [`ARCHITECTURE.md`](../ARCHITECTURE.md) §15.

1. **HTTP síncrono ou assíncrono:** `POST /wagering/transactions` responde já processado (`200`/`201`) ou aceita e processa depois (`202`)? Qual status HTTP usar para uma rejeição de negócio confirmada?
2. **`providerId` do corpo diferente do token:** `403` ou `422`?
3. **Transação de outro provedor:** `403` ou `404` (para evitar enumeração)?
4. **Escopo da `Idempotency-Key`:** única globalmente ou por provedor?
5. **Mesmo `(providerId, externalTransactionId)` com outra chave:** conflito `409` (leitura literal de IDEM-07) ou replay?
6. **Política REFUND × ROLLBACK:** ex.: `ROLLBACK` de um `REFUND` reabre o débito? `REFUND` após `ROLLBACK` da mesma `BET` é rejeitado?
7. **`WIN` com referência:** validar que a referência é uma `BET` processada da mesma rodada? E se ela ainda não existir, `WIN` também fica `PENDING_REFERENCE`?
8. **Referência existente mas `PENDING`/`PENDING_REFERENCE`:** continua esperando. **Referência `REJECTED`/`FAILED`:** rejeição definitiva com código próprio?
9. **Normalização de `amount`:** aceitar só o formato estrito `^\d+\.\d{2}$` ou normalizar `"25"` → `"25.00"`?
10. **Políticas do broker no LocalStack:** confirmar se a imposição de IAM está disponível na edição usada (ou no MiniStack). Se não estiver, documentar a limitação e a configuração equivalente em produção.
11. **`MessageGroupId`:** `walletId` (ordem por carteira) ou outro agrupamento? **`MessageDeduplicationId`:** `messageId` do envelope?
12. **Destino dos eventos de saída:** tópico SNS FIFO ou fila SQS FIFO dedicada?
13. **Endpoint de métricas:** público na rede interna ou autenticado?
14. **Estratégia de testes:** build tags (`integration`, `e2e`) para que `go test ./...` rode sem containers, ou testcontainers-go para rodar tudo com Docker disponível?
