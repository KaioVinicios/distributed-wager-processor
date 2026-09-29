# M1 — Domínio com TDD: design

**Data:** 29/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor e implementada em 29/09/2026

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M1;
- [`transaction-lifecycle.md`](../../transaction-lifecycle.md) §1–§5 e §7 (a parte de domínio de §6 e §8);
- D-03 (Money), D-05 (estados), D-08 (hash e idempotência), D-10 (reversões), D-11 (agenda de referências) e D-13 (envelope);
- [`messaging.md`](../../messaging.md) §6 (contratos dos eventos);
- [`test-plan.md`](../../test-plan.md) §5.1: U01–U12, **exceto U09b**, que é do pacote `postgres` (M2).

Esta spec registra só o **delta** em relação a `docs/`. O que já está decidido lá não é repetido.

---

## 1. Objetivo e critério de pronto

**Objetivo:** todas as regras financeiras e de estado em código puro (stdlib + `internal/domain/*`), testadas sem infraestrutura, prontas para o `app` (M3) só fazer I/O.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde (inclui `go test -race ./...`).
2. Todos os testes da tabela §9 desta spec foram vistos falhando pelo motivo certo e depois passando.
3. Os requisitos da §10 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
4. `docs/` e `ARCHITECTURE.md` refletem as decisões da §11.

**Fora do escopo** (com o marco de destino):
- casos de uso, portas, `Clock` e `IDGenerator` (M3);
- códigos que não nascem no domínio: `UNKNOWN_WALLET`, `WALLET_NOT_FOUND`, `WALLET_ALREADY_EXISTS`, `TRANSACTION_NOT_FOUND`, autenticação, `MALFORMED_REQUEST`, 415 e 503 (M3); `MALFORMED_MESSAGE`, `UNSUPPORTED_MESSAGE_TYPE` e `MESSAGE_HASH_MISMATCH` (M5);
- hash da inbox (`message_hash`), que depende do envelope SQS (M5);
- tradução de erros do PostgreSQL, U09b (M2).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Abordagem A: o domínio decide e aplica.** `wagering.Settle` avalia os passos 12–15 e R1–R8, movimenta a carteira pelo agregado e devolve o lançamento e os eventos. O `app` só busca e grava | Toda regra financeira fica testável sem fakes (U04a, U06), e HTTP, SQS e worker não têm como divergir. Aprovada pelo autor em 29/09 |
| 2 | **`wagering` importa `events`**, e `events` importa só `money` | Os eventos são devolvidos pelas transições (lifecycle §1). `events` usa `string` em `kind`/`direction`/`origin` para evitar ciclo |
| 3 | **IDs são `string`** com UUID canônico em minúsculas, validados pelo novo pacote `internal/domain/ident` | O domínio é só stdlib (`structure.md` §2, `ARCHITECTURE.md` §1). O `app` gera UUIDv7 com `google/uuid` e passa a string |
| 4 | **Metadados de transporte ficam fora do domínio.** O domínio devolve `events.Event` (dados tipados); o `app` monta o envelope com `events.Seal(eventID, correlationID, causationID, e)` antes do `INSERT` | O domínio recebe IDs prontos e não conhece `correlationId`/`causationId` de transporte. Tipo e versão continuam fixados pelo tipo Go do evento (OUT-09) |
| 5 | **Erro não classificado → `Transient`** em `apperrors.Classify` | Nada é persistido; um bug desconhecido não consome o `externalTransactionId` com um `FAILED` definitivo. Os adaptadores classificam explicitamente o que é conhecido. Escolhida pelo autor em 29/09 |
| 6 | **Chave de idempotência:** ASCII visível `^[\x21-\x7E]{1,255}$` | Interpretação de "1 a 255 caracteres imprimíveis" (lifecycle §3.1): sem espaço nem caractere não ASCII |
| 7 | **Entrada do comando com campos crus (`*string`)**; `nil` = ausente | A ordem de avaliação (U04d) exige checar presença (passo 3) antes do formato do dinheiro (passo 5), e distinguir ausente de vazio (`MISSING_*` × `INVALID_*`). Por isso o `Money` **não** é parseado no decode do JSON |
| 8 | **`Money` não tem método `IsZero()`** | O `omitzero` do `encoding/json` usa `IsZero()` e omitiria um `"0.00"` legítimo. O sinal é lido por `Sign()` |
| 9 | **Tempo normalizado no domínio:** todo instante gravado passa por `UTC().Truncate(time.Microsecond)` | É a precisão do PostgreSQL (data-model §2); a reidratação devolve o mesmo valor |
| 10 | **`updatedAt` nunca antes de `createdAt`:** as transições e os movimentos da carteira usam `max(now, createdAt)` no `updatedAt` | O relógio de outra instância pode estar atrás do instante de criação. Sem isso, o `CHECK wallets_updated_after_created` e a reidratação transformariam um atraso de milissegundos em falha permanente (foco de revisão do plano) |

### 2.1 Conformidade com o `CHALLENGE.md`

Verificada com o autor antes do design:

| Regra | Como a abordagem A a preserva |
| --- | --- |
| §6: invariantes preservadas em **todas** as operações públicas (DOM-01) | As transições são exportadas (U03, `Fail` no `app`), mas **cada uma valida os próprios argumentos** (§8.2). `Settle` só orquestra |
| §6.2: alteração do saldo sob controle do agregado (WAL-02) | O saldo só muda por `wallet.Open`, `Debit` e `Credit`, que devolvem o `LedgerEntry`. Os campos da `Wallet` são privados em outro pacote |
| §11: tipo e versão definidos pelo construtor do evento (OUT-09) | Tipo e versão são métodos do tipo Go concreto; a interface `Event` é selada; o `Envelope` só nasce por `Seal` e tem campos privados |
| §6: reidratação sem transições nem eventos (DOM-02) | `Rehydrate` não chama transições. O `Settle` do worker sobre uma `PENDING_REFERENCE` reidratada é uma transição nova |
| §6.3: `PENDING` com retomada durável (TX-09) | `Settle` sempre sai de `PENDING` na mesma chamada; `Snapshot()` e `Rehydrate` recusam `PENDING` |
| §4: domínio independente de Fx, HTTP, SQS e persistência (DOM-07) | Só stdlib e `internal/domain/*`; U10 + `depguard` |

---

## 3. Pacotes e dependências

```
internal/domain/
├── doc.go              # package domain: documentação + âncora do U10
├── imports_test.go     # U10
├── ident/              # UUID canônico (novo)
├── money/
├── wallet/             # → money, ident
├── events/             # → money, ident
└── wagering/           # → money, wallet, events, ident
internal/apperrors/     # folha, só stdlib
```

**Arquivos novos em relação ao [`structure.md`](../../structure.md) §1** (a árvore já foi atualizada): `domain/doc.go`, `domain/imports_test.go`, `ident/ident.go`, `wagering/command.go` (substitui `validation.go`), `wagering/codes.go`, `wagering/idempotency.go`, `wagering/settle.go` (absorve `reference.go`; `rules.go` fica com o movimento por tipo e as regras R3–R6, usadas também pelo `Process`), `wagering/opening.go`, `events/event.go` e `events/time.go`.

**Convenções de todo o domínio:**
- testes no pacote externo (`package x_test`);
- sem `panic`; todo erro é sentinela (`ErrX`, embrulhado com detalhe via `%w`) ou tipado (`*XError`);
- instantes normalizados (decisão 9) por um helper não exportado em cada pacote.

---

## 4. `ident`, `money` e `apperrors`

### 4.1 `ident`

| API | Comportamento |
| --- | --- |
| `Parse(s string) (string, error)` | Aceita só `8-4-4-4-12` hexadecimal, sem distinguir caixa; devolve em minúsculas. Rejeita o UUID nulo, `{…}`, `urn:uuid:` e a forma sem hífens com `ErrInvalid` |
| `Valid(s string) bool` | `true` só para a forma canônica **já em minúsculas** e não nula (usada pelos construtores, que recebem IDs do `app`) |

### 4.2 `money` (D-03)

| API | Comportamento |
| --- | --- |
| `type Currency string`; `BRL`, `USD`, `EUR`; `ParseCurrency(s) (Currency, error)` | Só o código exato, em maiúsculas → `ErrInvalidCurrency` |
| `Parse(amount, currency string) (Money, error)` | `^(0\|[1-9][0-9]*)\.[0-9]{2}$`, sem sinal. Overflow de `int64` → `ErrInvalidAmount` embrulhando `ErrOverflow` (os dois casam com `errors.Is`). Valida `amount` antes de `currency` |
| `FromMinor(minor int64, c Currency) (Money, error)` | Persistência e cálculos internos. Aceita negativos |
| `Zero(c Currency) (Money, error)` | Zero da moeda |
| `Add`, `Sub`, `Cmp` (`(…, error)`), `Negate() (Money, error)` | Overflow → `ErrOverflow` (inclui `Negate(MinInt64)`); moedas diferentes → `ErrCurrencyMismatch` |
| `Minor() int64`, `Currency() Currency`, `Sign() int`, `String() string` | Leitura. `String()` = `"25.00"` / `"-5.00"`. Sem `IsZero()` (decisão 8) |
| `MarshalJSON` / `UnmarshalJSON` | `{"amount":"25.00","currency":"BRL"}`. O unmarshal é a entrada externa estrita: os dois campos obrigatórios, sem campos desconhecidos (decoder próprio com `DisallowUnknownFields`), sem negativos |

- **Zero value:** `Money{}` devolve `ErrUninitialized` em toda operação que retorna erro, incluindo `MarshalJSON` (U01e, MON-11).
- **Limites:** −92.233.720.368.547.758,08 a 92.233.720.368.547.758,07; o parsing externo só aceita de `0.00` ao máximo positivo.
- **Erros:** `ErrInvalidAmount`, `ErrInvalidCurrency`, `ErrOverflow`, `ErrCurrencyMismatch`, `ErrUninitialized`.

### 4.3 `apperrors`

- `type Kind string`: `KindInput`, `KindNotFound`, `KindForbidden`, `KindConflict`, `KindBusiness`, `KindTransient`, `KindPermanent`.
- `type Error struct { Kind Kind; Code string; Err error }` com `Error()` e `Unwrap()`; `New(kind Kind, code string, err error) error`.
- `Classify(err error) Kind`, nesta ordem:
  1. `nil` → `""`;
  2. o **primeiro** `*Error` da cadeia de `%w` (`errors.As`) → o seu `Kind`;
  3. `context.Canceled` ou `context.DeadlineExceeded` → `KindTransient`;
  4. qualquer outro → `KindTransient` (decisão 5).
- `CodeOf(err error) string`: o `Code` do primeiro `*Error` da cadeia, ou `""`.
- O domínio **não** importa `apperrors`: o `app` (M3) e os adaptadores (M2) traduzem.

---

## 5. `wallet`

### 5.1 `Wallet`

| API | Comportamento |
| --- | --- |
| `Open(id, playerID string, initial money.Money, now time.Time) (Wallet, error)` | IDs válidos; `initial` inicializado e `>= 0`. Versão **1**, `balance = initial`, `createdAt = updatedAt = now` |
| `(w Wallet) OpeningEntry(entryID, txID string) (LedgerEntry, error)` | `CREDIT`, `before = 0`, `after = initial`, `walletVersion = 1`, `createdAt` da carteira. Só com versão 1 e saldo `> 0`; senão `ErrInvalidOpening` |
| `(w *Wallet) Debit(entryID, txID string, amount money.Money, now time.Time) (LedgerEntry, error)` | `amount > 0` (`ErrInvalidAmount`); moeda da carteira (`money.ErrCurrencyMismatch`); saldo `>= amount` (`ErrInsufficientFunds`). Sucesso: `version++`, `updatedAt = now`, lançamento `DEBIT` |
| `(w *Wallet) Credit(...)` | Igual, sem checagem de saldo; estouro → `money.ErrOverflow` |
| `Rehydrate(s Snapshot) (Wallet, error)` | Sem transições. Valida IDs, moeda suportada, saldo `>= 0`, versão `>= 1`, `updatedAt >= createdAt` → senão `ErrInvalidWallet` |
| `Snapshot() (Snapshot, error)`; getters `ID`, `PlayerID`, `Currency`, `Balance`, `Version`, `CreatedAt`, `UpdatedAt` | `Snapshot{ID, PlayerID string; Balance money.Money; Version int64; CreatedAt, UpdatedAt time.Time}` é o struct de mapeamento do M2 |

- Um `Debit`/`Credit` que falha deixa a carteira **intacta**.
- `Wallet{}` → `ErrUninitialized` em `Debit`, `Credit`, `OpeningEntry` e `Snapshot`; `(*Wallet)(nil)` → `ErrUninitialized` em `Debit` e `Credit`, que têm receptor ponteiro (U11). Os getters, `OpeningEntry` e `Snapshot` têm receptor valor.

### 5.2 `LedgerEntry`

- `type Direction string`: `DirectionDebit` (`"DEBIT"`), `DirectionCredit` (`"CREDIT"`).
- `NewLedgerEntry(p LedgerEntryParams) (LedgerEntry, error)`, com `LedgerEntryParams{ID, WalletID, TransactionID string; Direction Direction; Amount, BalanceBefore, BalanceAfter money.Money; WalletVersion int64; CreatedAt time.Time}`. Valida: IDs; direção conhecida; `amount > 0`; mesma moeda nos três valores; `before >= 0` e `after >= 0`; **`after == before ± amount`** conforme a direção (U07, LED-02); `walletVersion >= 1`. Erro: `ErrInvalidLedgerEntry`.
- **Um construtor para criar e reidratar:** o lançamento não tem transições nem eventos, então reidratar é validar as mesmas invariantes.
- Imutável: campos privados e getters. `LedgerEntry{}` é rejeitado onde é consumido (`Process`, U11).

**Erros do pacote:** `ErrInsufficientFunds`, `ErrInvalidAmount`, `ErrInvalidOpening`, `ErrInvalidWallet`, `ErrInvalidLedgerEntry`, `ErrUninitialized`; reutiliza `money.ErrCurrencyMismatch` e `money.ErrOverflow`.

---

## 6. `events`

**Tipos concretos:** `WalletBalanceChanged`, `WagerTransactionProcessed`, `WagerTransactionRejected` e `WagerTransactionPendingReference`, com os campos de [`messaging.md`](../../messaging.md) §6.2–§6.5.
- Structs de dados com campos exportados e tags JSON; IDs, `transactionKind`, `kind`, `origin`, `direction`, `failureCode` e `failureCategory` como `string`; dinheiro como `money.Money`.
- Opcionais com `omitempty`, nunca `null`: metadados externos em `OPENING` (OUT-13) e referências.
- Instantes com `events.Time`, cujo `MarshalJSON` emite sempre UTC com 3 casas: `2006-01-02T15:04:05.000Z` (OUT-12).
- Cada tipo tem seu construtor (`NewWalletBalanceChanged(e WalletBalanceChanged) (WalletBalanceChanged, error)` etc.), que valida os campos obrigatórios e normaliza os instantes.
- `WalletBalanceChanged` e `WagerTransactionPendingReference` não têm instante no `data` (messaging §6.2 e §6.5): o instante da transição fica num campo com `json:"-"` (`ChangedAt`, `PendingAt`), que vira o `occurredAt` do envelope.

**Interface selada:**

```go
type Event interface {
    Type() Type                         // constante do tipo Go
    Version() int                       // 1 nos quatro
    Aggregate() (AggregateType, string) // Wallet + walletId | WagerTransaction + transactionId
    MessageGroupID() string             // sempre o walletId
    OccurredAt() time.Time
    validate() error                    // não exportado: só os 4 tipos do pacote implementam
}
```

`occurredAt`: `processedAt` no `Processed`, `rejectedAt` no `Rejected`, o instante da transição no `PendingReference` e o `createdAt` do lançamento no `BalanceChanged`. Os eventos de uma mesma operação compartilham o instante.

**Envelope:**
- `Seal(eventID, correlationID, causationID string, e Event) (Envelope, error)`: `eventID` válido (`ident.Valid`); `correlationID` com 1 a 128 caracteres; `causationID` opcional, até 128; `e != nil` e `e.validate()` (um literal montado sem construtor também é validado). Erro: `ErrInvalidEvent`.
- Campos privados e getters `EventID`, `Type`, `Version`, `AggregateType`, `AggregateID`, `MessageGroupID`, `CorrelationID`, `CausationID`, `OccurredAt`, `Event`, que alimentam as colunas da outbox (data-model §3.5).
- `MarshalJSON` → `{eventId, eventType, version, aggregateType, aggregateId, correlationId, causationId?, occurredAt, data}` (messaging §6.1). O `message_group_id` não entra no JSON.

---

## 7. `wagering`: da entrada ao comando

### 7.1 Entrada e validação sem estado

```go
type MoneyInput struct { Amount, Currency *string }
type Input struct {
    IdempotencyKey                                        *string
    ProviderID, ExternalTransactionID, PlayerID, WalletID *string
    RoundID, GameID, Kind                                 *string
    Money                                                 *MoneyInput
    ReferenceExternalTransactionID                        *string
}
func NewCommand(in Input) (Command, error) // erro: *ValidationError{Code InputCode; Field string}
```

Ordem fixa ([`transaction-lifecycle.md`](../../transaction-lifecycle.md) §3.1, passos 2–7); devolve o **primeiro** erro:

| Passo | Checagem | Código | `Field` |
| --- | --- | --- | --- |
| 2 | Chave `nil` / fora de `^[\x21-\x7E]{1,255}$` | `MISSING_IDEMPOTENCY_KEY` / `INVALID_IDEMPOTENCY_KEY` | `idempotencyKey` |
| 3a | Presença, nesta ordem: `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money`, `money.amount`, `money.currency` | `MISSING_FIELD` | o campo |
| 3b | Formato, na mesma ordem: textos com 1–128 runas, UTF-8 válido, sem caractere de controle; `playerId`/`walletId` por `ident.Parse`; `referenceExternalTransactionId` (se presente) com a regra de texto, verificado por último | `INVALID_FIELD` | o campo |
| 4 | `OPENING` / fora de `BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK` (sensível a caixa) | `OPENING_NOT_ALLOWED` / `INVALID_KIND` | `kind` |
| 5 | `amount` estrito; depois `currency` suportada | `INVALID_AMOUNT` / `INVALID_CURRENCY` | `money.amount` / `money.currency` |
| 6 | Zero fora de `LOSS` / `LOSS` diferente de zero | `ZERO_AMOUNT_NOT_ALLOWED` / `LOSS_AMOUNT_MUST_BE_ZERO` | `money.amount` |
| 7 | REFUND/ROLLBACK sem referência / BET/LOSS com referência / referência == `externalTransactionId` | `REFERENCE_REQUIRED` / `REFERENCE_NOT_ALLOWED` / `SELF_REFERENCE` | `referenceExternalTransactionId` |

`Command` é imutável, com UUIDs em minúsculas e getters: `IdempotencyKey`, `ProviderID`, `ExternalTransactionID`, `PlayerID`, `WalletID`, `RoundID`, `GameID`, `Kind`, `Money`, `ReferenceExternalTransactionID` (`""` quando ausente), `CanonicalPayload()` e `PayloadHash()`. `Command{}` é rejeitado por `NewExternal` (`ErrUninitialized`).

### 7.2 Hash do payload (D-08)

- `PayloadHash()` = SHA-256 em hex minúsculo sobre o JSON canônico dos campos de D-08: chaves em ordem lexicográfica, `money` aninhado com as chaves também ordenadas, sem espaços, sem escape de HTML (`SetEscapeHTML(false)`), sem `\n` final, referência **omitida** quando ausente.
- Implementação: struct com os campos declarados em ordem alfabética e `omitempty` na referência, serializada por um `json.Encoder`.
- **Vetor golden** (U05a), a partir do exemplo do desafio:

  ```json
  {"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"BET","money":{"amount":"25.00","currency":"BRL"},"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","providerId":"provider-a","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"}
  ```

  O SHA-256 esperado é calculado **fora do Go** (`printf '%s' '<json>' | shasum -a 256`), e o comando fica registrado no teste.

### 7.3 Idempotência (U05c)

`CheckIdempotency(hash string, byKey, byExternalID *WagerTransaction) (replay *WagerTransaction, err error)`:
1. `byKey != nil`: mesmo hash → `(byKey, nil)`; hash diferente → `*ConflictError{Code: IDEMPOTENCY_KEY_REUSED}`.
2. Senão, `byExternalID != nil` → `*ConflictError{Code: EXTERNAL_TRANSACTION_ID_CONFLICT}`.
3. Senão → `(nil, nil)`.

### 7.4 Catálogo e enums

| Tipo | Valores | Extras |
| --- | --- | --- |
| `FailureCode` | Os 10 da lifecycle §5.1 + `INTERNAL_PERMANENT_FAILURE` | `Category() FailureCategory` (switch exaustivo); `ParseFailureCode` (reidratação); `IsRejection() bool` (§5.1) |
| `FailureCategory` | `CORRECTABLE`, `DEFINITIVE`, `TRANSIENT` | `TRANSIENT` só é usado pelas bordas |
| `InputCode` | Os 13 de validação da §7.1 + `IDEMPOTENCY_KEY_REUSED` e `EXTERNAL_TRANSACTION_ID_CONFLICT` | `Category()` = `CORRECTABLE` |
| `Kind`, `Status`, `Origin`, `ReceivedVia` | Data-model §3.2; `Status` inclui `PENDING` (só em memória) | `Parse*`; zero value (`""`) inválido |

---

## 8. `wagering`: entidade, transições, `Settle` e abertura

### 8.1 `WagerTransaction`

Campos privados espelhando o data-model §3.2; getters sem prefixo `Get`.

| API | Comportamento |
| --- | --- |
| `NewExternal(id string, cmd Command, via ReceivedVia, correlationID string, now time.Time) (*WagerTransaction, error)` | `PENDING`, `EXTERNAL`, `payloadHash = cmd.PayloadHash()`; `correlationID` com 1–128 caracteres; `cmd` inicializado |
| `NewOpening(id, walletID, playerID string, amount money.Money, correlationID string, now time.Time) (*WagerTransaction, error)` | `PENDING`, `INTERNAL`, `OPENING`, `amount > 0` |
| `Rehydrate(s Snapshot) (*WagerTransaction, error)` | Sem transições nem eventos. Valida enums, dinheiro, IDs e as constraints do data-model §3.2: origem × tipo × campos externos, política de valor e de referência, `failureCode` ⇔ `REJECTED`/`FAILED`, saldo de resultado em `PROCESSED`/`REJECTED`, `completedAt` ⇔ terminal, agenda **e referência** em `PENDING_REFERENCE`. Recusa `PENDING`. Erro: `ErrInvalidSnapshot` |
| `Snapshot() (Snapshot, error)` | Struct de mapeamento do M2. `PENDING` → `ErrNotPersistable` |

### 8.2 Transições

Todas devolvem `ErrInvalidTransition` a partir de estado terminal, de estado de origem não permitido ou de origem (`INTERNAL`/`EXTERNAL`) errada. Cada uma **valida os próprios argumentos** (erro `ErrInvalidArgument`), então chamá-las fora do `Settle` não quebra invariantes. Nenhuma altera o estado quando devolve erro.

| Método | De → para | Validação própria | Devolve |
| --- | --- | --- | --- |
| `Process(p ProcessParams) ([]events.Event, error)` com `ProcessParams{Entry *wallet.LedgerEntry; Reference *WagerTransaction; Balance money.Money; WalletVersion int64; Now time.Time}` | `PENDING` / `PENDING_REFERENCE` → `PROCESSED` | `Entry` obrigatório se o tipo movimenta e `nil` em `LOSS`; `Entry` com o mesmo `transactionId`, `walletId`, valor e moeda, e direção do tipo (no `ROLLBACK`, derivada de `Reference.Kind()`); `Balance`/`WalletVersion` iguais ao `BalanceAfter`/`WalletVersion` do lançamento. `Reference` obrigatória em REFUND, ROLLBACK e WIN com referência, com o `externalTransactionId` esperado; o id dela vira `referenceTransactionId`. Grava `resultBalance`, `completedAt` | `Processed` (+ `WalletBalanceChanged` se houve lançamento) |
| `Reject(code FailureCode, observed money.Money, now time.Time) ([]events.Event, error)` | idem → `REJECTED` | `code.IsRejection()`; só `EXTERNAL`. Grava `failureCode`, `resultBalance = observed`, `completedAt` | `Rejected` |
| `AwaitReference(now time.Time, policy ReferenceRetryPolicy) ([]events.Event, error)` | `PENDING` → `PENDING_REFERENCE` | Operação sem referência (BET, LOSS, WIN sem referência, OPENING) → `ErrInvalidTransition`; política zero → `ErrInvalidArgument`. `attempts = 0`, `nextAttemptAt = now + policy.Delay(0)`, `expiresAt = createdAt + TTL` | `PendingReference` |
| `RescheduleReference(now time.Time, policy ReferenceRetryPolicy) error` | `PENDING_REFERENCE` → `PENDING_REFERENCE` | Se `policy.Exhausted(attempts+1, expiresAt, now)`: `ErrReferenceExpired`, sem alterar. Senão `attempts++`, `nextAttemptAt = now + policy.Delay(attempts)` | — |
| `Fail(now time.Time) error` | `PENDING` / `PENDING_REFERENCE` → `FAILED` | Só `EXTERNAL` (o banco exige `INTERNAL` = `PROCESSED`; falha na abertura é rollback + 500). Grava `INTERNAL_PERMANENT_FAILURE`, `completedAt` | — |

`WagerTransaction{}` e `nil` → `ErrUninitialized` em todas as transições e no `Snapshot` (U11).

### 8.3 `ReferenceRetryPolicy` (D-11)

- `NewReferenceRetryPolicy(base, maxDelay time.Duration, maxAttempts int, ttl time.Duration, randN func(int64) int64) (ReferenceRetryPolicy, error)`: `base > 0`, `base <= maxDelay <= 24h` (o teto evita overflow no jitter), `maxAttempts >= 1`, `ttl > 0`; `randN == nil` → `rand.Int64N` (seguro entre goroutines). Erro: `ErrInvalidPolicy`.
- `Delay(n int) time.Duration` = `d ± 20%`, com `d = min(base·2ⁿ, maxDelay)` e o jitter `randN(2·(d/5)+1) − d/5`. Só aritmética inteira; o deslocamento para no teto antes de estourar.
- `Exhausted(attempts int, expiresAt, now time.Time) bool` = `attempts >= maxAttempts || !now.Before(expiresAt)`.
- `TTL() time.Duration`.
- Com os padrões: 1+2+4+8+16+32+60+60 s ≈ 3 min, como em D-11.

### 8.4 `Settle`

```go
type Reference    struct { Tx *WagerTransaction; AlreadyReversed bool } // Tx nil = não encontrada
type SettleParams struct { EntryID string; Now time.Time; Policy ReferenceRetryPolicy }
type Outcome      struct { Entry *wallet.LedgerEntry; Events []events.Event }
func Settle(tx *WagerTransaction, w *wallet.Wallet, ref Reference, p SettleParams) (Outcome, error)
```

**Pré-condições** (violação = bug de quem chama → erro, nunca rejeição): `tx` em `PENDING` ou `PENDING_REFERENCE` e `EXTERNAL`; `w` inicializada e `tx.WalletID() == w.ID()`; `ref.Tx`, quando presente, com o mesmo `providerId` e `externalTransactionId == tx.ReferenceExternalTransactionID()`; `EntryID` válido quando houver movimento.

**Avaliação** (lifecycle §3.4 e §4), parando no primeiro resultado:

| # | Checagem | Resultado |
| --- | --- | --- |
| 12 | `tx.PlayerID != w.PlayerID` | `Reject(PLAYER_WALLET_MISMATCH)` |
| 13 | `tx.Money.Currency != w.Currency` | `Reject(CURRENCY_MISMATCH)` |
| 14 | Só se o `tx` tem referência: | |
| R1/R2 | `ref.Tx == nil` ou `ref.Tx` em `PENDING_REFERENCE` | **Não resolvida** (abaixo) |
| R3 | `ref.Tx` em `REJECTED` ou `FAILED` | `Reject(REFERENCE_NOT_PROCESSED)` |
| R4 | Tipo não aceito: WIN→BET; REFUND→BET; ROLLBACK→BET, WIN ou REFUND | `Reject(INVALID_REFERENCE_KIND)` |
| R5 | Jogador, carteira, moeda ou rodada diferentes | `Reject(REFERENCE_MISMATCH)` |
| R6 | REFUND/ROLLBACK com valor diferente | `Reject(REVERSAL_AMOUNT_MISMATCH)` |
| R7 | REFUND/ROLLBACK com `ref.AlreadyReversed` | `Reject(ALREADY_REVERSED)` |
| 15 | Movimento: BET → `Debit`; WIN, REFUND → `Credit`; ROLLBACK → contrário da referência; LOSS → nenhum. `ErrInsufficientFunds` → `Reject(INSUFFICIENT_FUNDS)` na BET e `Reject(REVERSAL_INSUFFICIENT_FUNDS)` no ROLLBACK | `Process` |

**Referência não resolvida:** a partir de `PENDING` → `AwaitReference`; a partir de `PENDING_REFERENCE` → `Reject(REFERENCE_NOT_FOUND)` se `policy.Exhausted(attempts+1, …)`, senão `RescheduleReference`.

- Em `LOSS`, `Process` recebe `Entry = nil`, `Balance = w.Balance()` e `WalletVersion = w.Version()` (sem incremento).
- Em `Reject`, o saldo observado é `w.Balance()`; em rejeição e pendência a carteira fica intacta e `Outcome.Entry == nil`.
- O worker (M6) chama o mesmo `Settle`; reavaliar 12 e 13 é inofensivo (os dados não mudam).
- Os eventos seguem a matriz da lifecycle §7.

### 8.5 `OpenWallet` (U06, HTTP-01)

```go
type OpenParams struct {
    WalletID, PlayerID                        string
    Initial                                   money.Money
    TransactionID, EntryID, CorrelationID     string
    Now                                       time.Time
}
type Opening struct { Wallet wallet.Wallet; Tx *WagerTransaction; Entry *wallet.LedgerEntry; Events []events.Event }
func OpenWallet(p OpenParams) (Opening, error)
```

- `wallet.Open` → se `Initial > 0`: `NewOpening` → `w.OpeningEntry` → `Process` (versão 1). Eventos: `Processed` (`origin = INTERNAL`, sem metadados externos) + `WalletBalanceChanged` (`before = 0.00`, `walletVersion = 1`).
- Saldo zero: `Tx` e `Entry` nulos e nenhum evento. `TransactionID` e `EntryID` são ignorados.

---

## 9. Testes

Todos com `-race`, `// Covers:` e o ciclo de [`development-workflow.md`](../../development-workflow.md) §4.2 (red por asserção, com stubs).

| Pacote | Arquivo | Testes | Cobre |
| --- | --- | --- | --- |
| `ident` | `ident_test.go` | `TestParse` (tabela: canônico, maiúsculas → minúsculas, nulo, `{}`, `urn:`, sem hífens, curto, não hex), `TestValid` | DOM-03 |
| `money` | `money_test.go` | U01a `TestParseMoney`, U01b `TestMoneyArithmetic`, U01c `TestMoneyCurrencyMismatch`, U01e `TestMoneyZeroValue`, `TestParseCurrency` | MON-02..05, 07..09, 11, 12 |
| `money` | `json_test.go` | U01d `TestMoneyJSON` (inclui campo desconhecido, ausente e `null` no unmarshal, e `Money{}` no marshal) | MON-01, 04, 09 |
| `money` | `money_fuzz_test.go` | U01f `FuzzParseMoney` | MON-05, DOM-05 |
| `money` | `nofloat_test.go` | U01g `TestNoFloatInMoney` (AST: `float32`, `float64`, `ParseFloat`, `FormatFloat`, `token.FLOAT`) | MON-01, E3 |
| `wallet` | `wallet_test.go` | U02 `TestWalletOpen`, `TestWalletDebit`, `TestWalletCredit`, `TestWalletRehydrate`, `TestWalletOpeningEntry`, `TestZeroValuesRejected` | WAL-01, 02, 04, 05, 07, DOM-02, 03 |
| `wallet` | `ledger_entry_test.go` | U07 `TestLedgerEntryInvariant` | LED-01, 02 |
| `events` | `events_test.go` | U08 `TestEventConstructors`, `TestSeal`, `TestEnvelopeJSON` (golden por tipo: campos, omissões, tempo, dinheiro em string) | OUT-08, 09, 11..13 |
| `wagering` | `enums_test.go` | `TestParseEnums`, `TestFailureCatalog` | TX-01, TX-06, OPS-10, OPS-15, DOM-03 |
| `wagering` | `transaction_test.go` | `TestNewExternal`, `TestNewOpening`, `TestRehydrate` | TX-02..06, TX-09, DOM-01, DOM-02 |
| `wagering` | `command_test.go` | U04b `TestZeroAmountPolicy`, U04d `TestEvaluationOrder`, `TestNewCommand` (tabela de §7.1) | TX-01, OPS-06, 11, 15 |
| `wagering` | `payload_hash_test.go` | U05a `TestPayloadHashGolden`, U05b `TestPayloadHashHTTPEqualsSQS` (duas `Input` que diferem em chave e caixa dos UUIDs) | IDEM-03, 04 |
| `wagering` | `idempotency_test.go` | U05c `TestIdempotencyDecision` | IDEM-05..07 |
| `wagering` | `transitions_test.go` | U03 `TestTransactionStateMachine` (matriz origem × destino), `TestTransitionResults`, `TestTransitionArgumentValidation`, `TestTransitionClockSkew`, `TestZeroValuesRejected` (U11) | TX-06, 07, DOM-01..03 |
| `wagering` | `settle_test.go` | U04a `TestKindRules` (tipo × valor, referência, movimento e eventos), `TestSettleEvaluationOrder`, `TestSettleUnresolvedReference`, `TestSettlePreconditions`, `TestSettleEdgeCases` | OPS-01..05, 10, 12, 13, 15 |
| `wagering` | `reference_test.go` | U04c `TestReferenceResolution` (R1–R8) | OPS-06..10, 14 |
| `wagering` | `opening_test.go` | U06 `TestOpening` | HTTP-01 (domínio), TX-04, OUT-13 |
| `wagering` | `retry_policy_test.go` | U12 `TestReferenceRetryPolicy` | OPS-12, 13 |
| `apperrors` | `classify_test.go` | U09a `TestClassify`, `TestCodeOf` | TX-10, DOM-04 |
| `domain` | `imports_test.go` | U10 `TestDomainHasNoInfraImports` (`go list -deps`: só stdlib e `internal/domain/*`, sem `net/http` nem `database/sql`; o `go test` põe `GOROOT/bin` no `PATH`) | DOM-07 |

**U11** vira um `TestZeroValuesRejected` em cada pacote (`money`, `wallet`, `wagering`), cobrindo `Money{}`, `Currency("")`, `Kind("")`, `Status("")`, `Wallet{}`, `WagerTransaction{}` e `LedgerEntry{}`.

---

## 10. Requisitos no encerramento

- **`[x]`** quando o teste de domínio prova o requisito sozinho: MON-01..05, 07, 08, 11, 12; MON-06 (não se aplica, documentado); DOM-01..05, 07; WAL-01, 02, 05, 07; LED-01, 02; TX-06, 10 (a parte documental já existe); OPS-01..07, 09..11, 14, 15; IDEM-03, 05, 06; OUT-08, 09, 11, 12, 13; TST-U01..U06.
- **`[~]`** quando também depende do banco ou da borda, citando o teste de domínio: MON-09 e WAL-04 (`CHECK` no M2), TX-01 (bordas M3/M5), TX-02..04 (persistência M2), TX-07 (trigger `PDA02` no M2), OPS-08 (índice no M2), OPS-12, 13 (worker M6), IDEM-04 (DTO e envelope M3/M5), IDEM-07 (índice no M2).
- **Não mexer:** MON-10, DOM-06, WAL-03, 06, 08, TX-08, TX-09, OUT-10 (M2–M4).

---

## 11. Ajustes em `docs/` (feitos junto com esta spec)

| Documento | Ajuste |
| --- | --- |
| [`decisions.md`](../../decisions.md) | D-05: erro não classificado é transitório. D-08: charset da chave; IDs do domínio como string canônica. D-13: `eventId` atribuído no `Seal` |
| [`transaction-lifecycle.md`](../../transaction-lifecycle.md) | §1: eventos devolvidos como `events.Event`; orquestração por `wagering.Settle`/`OpenWallet`. §3.1: charset da chave. §8: padrão transitório |
| [`messaging.md`](../../messaging.md) | §6.1: `eventId` atribuído ao selar o envelope, antes do `INSERT` |
| [`structure.md`](../../structure.md) | Árvore do domínio (§3 desta spec), `apperrors`, regra `wagering → events` e `ident` |
| [`implementation-plan.md`](../../implementation-plan.md) | M1: pronto = §5.1 exceto U09b; "Cobre" sem OUT-10 (M4) |
| [`test-plan.md`](../../test-plan.md) | Notas em U05b e U11 |
| [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) | Revisado no encerramento do marco |

---

## 12. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| Escopo grande para ~3 h (5 pacotes, ~15 arquivos de teste) | Funções puras e testes em tabela. Ordem do plano por dependência: `ident` → `money` → `wallet` → `events` → `wagering` → `apperrors` → U10; cada pacote fecha verde antes do próximo |
| `exhaustive` e `musttag` apontarem muitos casos | `make lint` ao fim de cada pacote, não só no fim do marco |
| Divergência entre o domínio e as constraints do M2 | `Rehydrate` e as transições espelham o data-model §3.2; o I02a do M2 confirma |
| U10 depende do `go` durante o teste | O `go test` põe `GOROOT/bin` no início do `PATH` (Go 1.19+); na ausência, o teste falha com mensagem clara |
| Vetor golden do hash calculado pelo próprio código (teste tautológico) | O SHA esperado vem do `shasum` externo, com o comando no teste |
