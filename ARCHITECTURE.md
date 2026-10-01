# Arquitetura

Serviço em Go que movimenta carteiras de jogadores a partir de operações de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`), recebidas por **HTTP** e por **SQS**, com as mesmas garantias nos dois canais: precisão monetária, ledger auditável, idempotência persistente, coordenação entre várias instâncias e recuperação de falhas.

Este documento é **autossuficiente**: cada seção traz a decisão, o motivo e as consequências. Os detalhes operacionais (DDL, catálogos completos, parâmetros e roteiros de teste) estão nos documentos de [`docs/`](docs/), indicados em cada seção. O enunciado original está em [`CHALLENGE.md`](CHALLENGE.md).

> **Como este documento foi mantido.** Ele foi escrito antes da implementação, a partir das decisões de [`docs/decisions.md`](docs/decisions.md), e revisado ao fim de cada marco contra o código. A revisão final (M10, 30/09/2026) conferiu que todo teste e toda métrica citados aqui existem no código, e o M12 acrescentou o teste de carga. As limitações (§16) e o trabalho não concluído (§17) refletem o estado da entrega. Como executar e testar: [`README.md`](README.md) e [`docs/testing.md`](docs/testing.md).

---

## 1. Visão geral

```mermaid
flowchart LR
    subgraph Clientes
        PV["Provedores<br/>(provider-a, provider-b)"]
        WS["Serviço interno<br/>(wallet-service)"]
    end
    KC[Keycloak<br/>OIDC / client_credentials]
    Q[(SQS FIFO<br/>wager-transactions.fifo)]
    DLQ[(DLQ FIFO)]
    subgraph "pda × 3 instâncias (mesmo binário)"
        API[API HTTP]
        CON[Consumidor SQS]
        UC[[Caso de uso único<br/>ProcessWagerTransaction]]
        REF[Worker de referências]
        PUB[Publisher da outbox]
    end
    PG[(PostgreSQL<br/>carteiras · transações · ledger<br/>inbox · outbox)]
    SNS{{SNS FIFO<br/>wallet-events.fifo}}

    PV & WS -- token --> KC
    PV & WS -- "Bearer JWT" --> API
    PV --> Q --> CON
    Q -. redrive .-> DLQ
    API & CON --> UC --> PG
    REF --> UC
    PG --> PUB --> SNS
```

- **Um binário, quatro papéis:** API HTTP, consumidor SQS, publisher da outbox e worker de referências, habilitáveis por variável de ambiente. O `docker compose` sobe **3 réplicas** independentes, cada uma com seu pool de conexões e sua memória.
- **Um único caso de uso** (`ProcessWagerTransaction`) atende HTTP, SQS e o worker. Só a borda muda, então as garantias são as mesmas nos três caminhos.
- **O PostgreSQL é a fonte da verdade** de todas as garantias: unicidade, não negatividade, imutabilidade, idempotência, agendas de retentativa e eventos pendentes. Nenhuma correção depende de estado em memória, de uma instância específica ou da deduplicação do SQS.
- **Camadas:**
  - `domain` (entidades e regras, só stdlib);
  - `app` (casos de uso e portas);
  - `adapters` (PostgreSQL, HTTP, SQS, SNS, workers);
  - `bootstrap` (composição Fx).

  O domínio não conhece Fx, HTTP, SQS nem o driver do banco, e isso é verificado por lint (`depguard`) e por teste. Detalhes em [`docs/structure.md`](docs/structure.md).

**Stack:** Go 1.27.1, Uber Fx, `net/http`, PostgreSQL 18 com `pgx/v5`, Keycloak 26, SQS e SNS no MiniStack, `golang-migrate`, `log/slog` e Prometheus. Versões e ferramentas em [`docs/stack.md`](docs/stack.md).

---

## 2. Dinheiro

**Decisão:** `Money` é um value object imutável com `int64` em **unidades mínimas** (centavos) e a moeda ISO 4217. Nenhum `float32`/`float64` aparece no parsing, no cálculo, na serialização ou na persistência.

- **Por que `int64` e não uma biblioteca decimal:** com escala fixa de 2 casas, inteiros são exatos, rápidos, triviais de comparar e mapeiam direto para `BIGINT`. Uma biblioteca decimal resolveria um problema que não temos (escala variável) ao custo de uma dependência.
- **Limites:** de −92.233.720.368.547.758,08 a 92.233.720.368.547.758,07. Parsing, soma, subtração e negação verificam overflow e devolvem erro tipado, inclusive `-MinInt64`.
- **Entrada estrita, sem normalização:** `amount` precisa casar com `^(0|[1-9][0-9]*)\.[0-9]{2}$`. Portanto `""`, `"25"`, `"25.0"`, `"025.00"`, `"+1.00"`, `"-1.00"`, `"1e3"`, `"NaN"` e `"Infinity"` são rejeitados, e nada é arredondado. A moeda precisa estar em maiúsculas e entre as suportadas: `BRL`, `USD` e `EUR`, todas com 2 casas. Como não existem formas equivalentes aceitas, a string recebida já é a forma canônica usada no hash de idempotência.
- **Sinal:** valores negativos existem só em cálculos internos (`difference` da reconciliação, `Negate`). O saldo nunca fica negativo (§4).
- **Moedas:** toda operação ou comparação exige a mesma moeda. O contrário gera `ErrCurrencyMismatch`, verificável com `errors.Is`.
- **Serialização:** sempre `{"amount":"25.00","currency":"BRL"}`, com marshal e unmarshal próprios. Valores negativos só aparecem em respostas (ex.: `difference` = `"-5.00"`); na entrada, são rejeitados.
- **Zero value e JSON:** `Money{}` é rejeitado por toda operação que devolve erro, inclusive o marshal. O tipo não tem `IsZero()`, para que o `omitzero` do `encoding/json` não omita um `"0.00"` legítimo. O unmarshal recusa campo desconhecido, campo ausente, `null` e `amount` numérico.
- **Persistência:** colunas `*_minor BIGINT` + `currency CHAR(3)`; `NULL` ↔ `Money{}` (ausente). As somas no banco (reconciliação) retornam `numeric` e voltam como `bigint` pelo cast, que falha com erro permanente em caso de overflow. O saldo observado de uma transação (`result_balance_minor`) é um saldo da carteira: é reconstruído na moeda da carteira, que numa rejeição `CURRENCY_MISMATCH` difere da moeda da operação.
- **Garantia contra float:** o linter `forbidigo` proíbe `float32`/`float64` e `strconv.ParseFloat` fora do pacote de observabilidade, e um teste que analisa a AST do pacote `money` falha se encontrar ponto flutuante.

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-03.

---

## 3. Persistência e transações

### 3.1 Biblioteca

**`pgx/v5` com SQL explícito**, sem ORM e sem geração de código. É a opção preferencial do desafio e mantém **visíveis** no código tudo o que sustenta as garantias: `SELECT … FOR UPDATE`, `SET LOCAL lock_timeout`, `FOR UPDATE SKIP LOCKED` e constraints nomeadas. As migrations usam `golang-migrate`, com arquivos `up`/`down` versionados em `migrations/`, aplicados pelo serviço `migrate` do compose como `pda_owner` antes das réplicas (`make migrate-up` e `make migrate-down N=1`). A aplicação nunca executa DDL. Os testes aplicam os mesmos arquivos, embutidos, num banco isolado por pacote.

### 3.2 Delimitação da transação SQL

- **Unit of Work explícito:** `uow.Do(ctx, func(r Repos) error)`. `Repos` expõe os repositórios de carteira, transação, ledger, inbox e outbox, **todos ligados à mesma `pgx.Tx`**. Se a função retornar `nil`, há commit; qualquer erro ou `panic` leva a rollback.
- **A transação não fica escondida no `context`.** Quem recebe `Repos` está dentro da transação, e isso aparece na assinatura. As interfaces pertencem à camada `app`, e o domínio não conhece o UoW.
- **Dois modos:** `uow.Do` (`READ COMMITTED`, com `lock_timeout` na transação inteira) para escrever, e `uow.Snapshot` (`REPEATABLE READ READ ONLY`) para a reconciliação. Fora de transação, os mesmos repositórios servem as leituras sobre o pool.
- **Tipos do domínio nas portas:** os repositórios recebem e devolvem `Wallet`, `WagerTransaction` e `LedgerEntry`, reidratados pelo adapter. Linha que o domínio recusa, ou valor inválido na escrita, é erro permanente.
- **Erros do banco classificados no adapter:** as violações de unicidade que o `app` trata viram sentinelas (`ErrWalletAlreadyExists` como conflito; corridas de idempotência, reversão e inbox como transitórias, que um retry resolve pela releitura); SQLSTATEs transitórios (lock timeout, deadlock, conexão) viram `Transient`; o resto é `Permanent`. O erro do PostgreSQL não sai do adapter, porque o `Detail` traz a linha inteira. Uma transação interrompida pelo `context` é transitória.
- **Uma operação financeira é uma única transação:**
  1. trava a carteira;
  2. insere a transação no estado final;
  3. atualiza saldo e versão;
  4. grava o lançamento no ledger;
  5. grava os eventos na outbox;
  6. registra a mensagem na inbox, quando vem do SQS;
  7. antecipa as pendências que dependiam desta operação.

  Ou tudo é confirmado, ou nada é.

### 3.3 Garantias impostas pelo banco

As invariantes valem mesmo com bugs no código e com várias instâncias, porque estão no schema:

| Garantia | Mecanismo |
| --- | --- |
| Saldo nunca negativo | `CHECK (balance_minor >= 0)` |
| Uma carteira por `(playerId, currency)` | `UNIQUE` |
| Versão incrementada só quando o saldo muda | Trigger em `wallets` |
| Todo saldo alterado, e toda transação `PROCESSED` com movimento, tem o lançamento correspondente | Trigger `BEFORE INSERT` no ledger (coerência com a carteira) + constraint triggers **adiadas para o commit** em `wallets` e em `wager_transactions` |
| `LOSS` e operações não processadas não geram lançamento; valor, moeda e direção batem com a transação | Trigger `BEFORE INSERT` no ledger |
| `balanceAfter = balanceBefore ± amount` | `CHECK` no ledger |
| Um lançamento por `(walletId, transactionId)` | `UNIQUE` |
| Ledger append-only: correções financeiras só por novos lançamentos (reversões) | Triggers bloqueiam `UPDATE`/`DELETE`/`TRUNCATE` (valem até para o dono das tabelas) + a role da aplicação sem esses privilégios |
| Transação terminal imutável | Trigger em `wager_transactions` |
| Origem interna × externa, um único `OPENING` por carteira | `CHECK`s de coerência + índice único parcial |
| Idempotência | `UNIQUE (provider_id, idempotency_key)` e `UNIQUE (provider_id, external_transaction_id)` |
| Uma compensação bem-sucedida por referência | Índice único parcial (§8) |
| Snapshot da outbox imutável | Trigger |

- **Separação de roles:** as migrations rodam com `pda_owner`, e a aplicação usa `pda_app`, sem DDL e sem `UPDATE`/`DELETE` no ledger.
- **Ledger de entrada simples, por escolha.** Cada lançamento registra saldo anterior, saldo posterior e a versão da carteira (`UNIQUE (wallet_id, wallet_version)`). Isso forma uma **cadeia verificável** por carteira, suficiente para auditoria e reconciliação. O ledger de partidas dobradas (diferencial opcional) não foi adotado: ele exigiria contas de contrapartida (casa e provedor) sem ganho para as garantias pedidas.

Detalhes: [`docs/data-model.md`](docs/data-model.md) e [`docs/decisions.md`](docs/decisions.md) D-01, D-14, D-16 e D-17.

---

## 4. Concorrência e locks

**Decisão:** **lock pessimista por carteira.** Cada operação financeira define o `lock_timeout` local da transação (`DB_LOCK_TIMEOUT`, padrão 5 s, por `set_config`, o `SET LOCAL` parametrizável) e faz `SELECT … FROM wallets WHERE id = $1 FOR UPDATE`.

- **Sem lost update:** o lock serializa os escritores da mesma carteira. O `UPDATE` ainda confere `version = $old`, como segunda proteção, e o `CHECK (balance_minor >= 0)` é a última linha de defesa.
- **Sem lock global:** carteiras diferentes não disputam nada e avançam em paralelo. Com 3 processos, um teste segura o lock da carteira X e confirma que um BET na carteira Y conclui em menos de 1 s, enquanto o de X espera (C03b `TestNoGlobalLock`); 20 carteiras recebem 10 BETs simultâneos cada (C03a).
- **Sem deadlock:**
  - cada operação trava uma única carteira;
  - o worker de referências trava sempre **carteira → transação**, na mesma ordem do caminho HTTP;
  - a busca de pendências usa `SKIP LOCKED` e é feita em uma transação separada.
- **Independência de instância:** a coordenação é inteira no banco, então não depende de locks em memória, de afinidade de instância nem da deduplicação do SQS FIFO.
- **Contenção extrema:** um lock timeout é tratado como falha **transitória**: HTTP 503 com `Retry-After`, ou retry no SQS. A métrica `concurrency_conflicts_total` é incrementada.
- **Por que não controle otimista:** sob disputa real (o caso 100 vs 2×80), o otimista gera conflitos e retries que precisariam de limite e métricas próprios. O pessimista é determinístico e deixa o `balanceBefore` do ledger trivialmente correto.

**Caso obrigatório:** carteira com 100.00 e duas apostas simultâneas de 80.00. A primeira trava, debita e deixa 20.00. A segunda espera o lock, lê 20.00 e é `REJECTED` com `INSUFFICIENT_FUNDS`. Resultado: um débito no ledger, saldo 20.00, e os reenvios devolvem os mesmos resultados. Provado com as duas apostas em processos diferentes, 20 vezes (C02 `TestTwoBetsCompete`), e com uma aposta por HTTP e outra pelo SQS paradas no lock da carteira e soltas juntas (C10b `TestHTTPAndSQSConcurrent`).

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-09.

---

## 5. Máquina de estados e falhas

| Estado | Persistido? | Significado |
| --- | --- | --- |
| `PENDING` | **Não** | Estado inicial em memória. As operações sem dependência são concluídas na mesma transação |
| `PENDING_REFERENCE` | Sim | Aguarda uma referência; sempre com agenda de retentativa |
| `PROCESSED` | Sim, terminal | Concluída com sucesso |
| `REJECTED` | Sim, terminal | Recusada por regra de negócio, com `failureCode` estável |
| `FAILED` | Sim, terminal | Falha permanente de infraestrutura, registrada para auditoria |

- **Processamento síncrono:** o desafio permite concluir operações sem dependências "de forma síncrona, sem commit intermediário de aceite". Por isso `PENDING` nunca é persistido: o `CHECK` do status o exclui. Assim, "todo `PENDING` confirmado tem retomada durável" vale por construção. O único estado de espera persistido é `PENDING_REFERENCE`, que o worker retoma a partir de qualquer instância (§7).
- **Transições:** as transições são validadas no domínio (`ErrInvalidTransition`) e, para estados terminais, também no banco (trigger). A reidratação reconstrói o estado sem passar por transições e sem gerar eventos.
- **Orquestração no domínio:** `wagering.Settle` avalia as regras com estado e a referência, movimenta a carteira pelo agregado (`Debit`/`Credit`) e devolve o lançamento e os eventos; `wagering.OpenWallet` faz o mesmo para a abertura. HTTP, SQS e o worker de referências chamam a mesma função, e o caso de uso só faz I/O. Cada transição valida os próprios argumentos, então nenhuma chamada direta quebra as invariantes.
- **Classificação de falhas:** fica em um único ponto (`apperrors.Classify`), alimentado pelos adaptadores. Um erro que nenhuma camada classificou é tratado como transitório: nada é persistido e a operação pode ser reenviada depois da correção, em vez de virar um `FAILED` definitivo (D-05).
- **Erros do domínio:** o domínio não faz I/O, então o que ele devolve é entrada inválida ou invariante quebrada. Os casos de uso passam todo erro do domínio por uma única tradução: validação vira entrada inválida, conflito de idempotência vira conflito, e qualquer outro (overflow, transição inválida, dado corrompido) vira **permanente**. Sem ela, um overflow seria tratado como transitório e reenviado para sempre.
- **Onde o `FAILED` é gravado:** uma falha permanente a partir do lock da carteira grava a operação como `FAILED` numa **segunda** transação, sem lançamento nem eventos, e o replay devolve o mesmo 500. Se nem essa gravação for possível, a resposta é 503. Se a falha vier da leitura de idempotência (a linha já gravada com a chave não pode ser lida), não há o que gravar: a resposta é 500 `INTERNAL_ERROR`, sem registro.

| Classe | Exemplos | Efeito |
| --- | --- | --- |
| **Transitória** | Rede, timeout, SQLSTATE `08*`, `40001`, `40P01`, `55P03`, `57P01`, `53300`, throttling ou 5xx da AWS | Nada é persistido. HTTP responde 503; o SQS faz retry com backoff; os workers tentam na próxima varredura |
| **Permanente** | Violação de proteção do banco (SQLSTATEs próprios `PDA01`–`PDA05`), constraint imprevista, overflow, dado que não pode ser reidratado | `FAILED` gravado em uma transação separada, sem efeito financeiro. HTTP 500; no SQS, a inbox é gravada junto e a mensagem vai para a DLQ |
| **Regra de negócio** | Saldo insuficiente, divergência de referência etc. | `REJECTED` persistido e replayável. HTTP 422 |
| **Entrada inválida** | Formato, campo ausente, `OPENING` externo, carteira inexistente | Nada é persistido. HTTP 400; no SQS, a mensagem vai para a DLQ |

Detalhes: [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §1 e §8.

---

## 6. Idempotência

- **Persistente e no banco:** duas constraints sobre as transações externas.
  - `UNIQUE (provider_id, idempotency_key)`: a chave vale **por provedor**;
  - `UNIQUE (provider_id, external_transaction_id)`: a mesma operação financeira não pode ser reaplicada com outra chave.
- **Fluxo:**
  1. Busca por `(provedor do token, chave)`. Se o hash for igual, devolve o **resultado persistido** com `idempotentReplay: true`; se for diferente, 409 `IDEMPOTENCY_KEY_REUSED`.
  2. Busca por `(provedor, externalTransactionId)`. Se encontrar, é a mesma operação com outra chave: 409 `EXTERNAL_TRANSACTION_ID_CONFLICT`.
  3. Processa. Se houver corrida, a segunda requisição cai na `UNIQUE`, faz rollback e relê, em até 3 tentativas. A mesma verificação é repetida já sob o lock da carteira.
- **Corrida entre as duas buscas:** elas são leituras separadas, e uma entrega concorrente pode confirmar entre elas. Uma transação achada só pela segunda busca, **com a mesma chave**, é tratada como a da chave (replay), nunca como conflito. O teste de 50 envios paralelos da mesma aposta encontrou esse caso.
- **Replay fiel:** a transação guarda o saldo observado no processamento (`result_balance_minor`). O replay devolve **esse** saldo e o mesmo status HTTP original, mesmo que a carteira tenha mudado depois.
- **Hash do payload:** SHA-256 em hex sobre **JSON canônico**, com chaves em ordem lexicográfica e sem espaços.
  - **Campos:** `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId` (omitido quando ausente).
  - **Excluídos:** a chave de idempotência, `messageId`, `type`, `occurredAt`, headers e o canal de entrada.
  - **Única normalização:** os UUIDs são convertidos para a forma canônica em minúsculas. A `Idempotency-Key` aceita de 1 a 255 caracteres ASCII visíveis e nunca é alterada.
- **HTTP ≡ SQS:** o hash é calculado a partir do comando de domínio já validado, que é o mesmo nos dois canais. A mesma operação enviada por HTTP e por SQS é reconhecida como replay, e isso é coberto por testes que cruzam os canais.
- **Segunda camada no SQS:** a inbox deduplica por `(consumerName, messageId)` na mesma transação do tratamento (§9).

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-08.

---

## 7. Referências pendentes

- **Quando:** um `REFUND` ou `ROLLBACK` (ou um `WIN` que informa referência) chega antes da transação referenciada, ou a referência ainda está em `PENDING_REFERENCE`. A operação é persistida como `PENDING_REFERENCE` e o evento `WagerTransactionPendingReference` é emitido. O HTTP responde **202**, e no SQS a mensagem é concluída depois do commit da pendência.
- **Worker:** qualquer instância busca as pendências vencidas (um único statement com `FOR UPDATE SKIP LOCKED`, que devolve só `(id, walletId)` e não segura nada) e processa **um item por vez**, cada um em sua própria transação, travando carteira → transação (a ordem do HTTP). A agenda (`attempts`, `next_attempt_at`, `expires_at`) fica toda no banco, então sobrevive a reinícios.
- **Recheck sob os locks:** o item só é reavaliado se ainda estiver em `PENDING_REFERENCE` **e** com `next_attempt_at <= now`. Duas instâncias podem pegar o mesmo ID no claim; a segunda encontra o item reagendado pela primeira e o ignora, sem contar uma tentativa a mais.
- **Falhas do worker:** uma falha permanente grava `FAILED` numa transação separada (sem lançamento nem evento) e antecipa os dependentes, que saem com `REFERENCE_NOT_PROCESSED`. Uma falha transitória não muda nada: o item continua devido e volta no ciclo seguinte. Os eventos do worker citam, em `causationId`, a operação que destravou a pendência, e mantêm o `correlationId` original.
- **Backoff exponencial:** `min(1 s × 2^tentativas, 60 s)` com ±20% de jitter.
- **Limite:** 8 tentativas **ou** 10 minutos de TTL, o que vier primeiro. Com os padrões, as tentativas se esgotam em cerca de 3 min; o TTL limita a espera em tempo de relógio, inclusive com todas as instâncias paradas. Ao esgotar, a operação vira `REJECTED` com `REFERENCE_NOT_FOUND` e o evento `WagerTransactionRejected` é emitido. Tudo é configurável por ambiente.
- **Referência existente mas pendente:** continua aguardando, e o tempo conta para o mesmo limite.
- **Referência que terminou sem sucesso** (`REJECTED` ou `FAILED`): rejeição imediata com `REFERENCE_NOT_PROCESSED`. Esperar mais não mudaria o resultado.
- **Retomada antecipada:** quando uma operação chega a um estado terminal (processada, rejeitada ou com falha), a mesma transação antecipa as pendências que a referenciam. Na prática, a pendência é resolvida (ou rejeitada com `REFERENCE_NOT_PROCESSED`) logo depois que a referência chega, sem esperar o backoff.

Detalhes: [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §4 e [`docs/decisions.md`](docs/decisions.md) D-11.

---

## 8. Reversões

| Operação | Referências aceitas | Movimento |
| --- | --- | --- |
| `REFUND` | `BET` processada | Crédito do valor da aposta |
| `ROLLBACK` | `BET` / `WIN` / `REFUND` processada | Movimento contrário: crédito para `BET`; débito para `WIN` e `REFUND` |

- **Concordância obrigatória** com a referência em provedor (garantido pela busca), jogador, carteira, moeda e rodada (`REFERENCE_MISMATCH`), além de valor **exatamente igual**, sem reversão parcial (`REVERSAL_AMOUNT_MISMATCH`).
- **REFUND + ROLLBACK sobre a mesma aposta:** cada referência aceita **no máximo uma compensação bem-sucedida**. Para uma `BET`, isso significa um `REFUND` **ou** um `ROLLBACK`, nunca os dois; a segunda tentativa recebe `REJECTED` com `ALREADY_REVERSED`. A garantia está no banco: índice único parcial em `reference_transaction_id` para `REFUND`/`ROLLBACK` em `PROCESSED`. Isso é mais estrito que "não repetir o mesmo tipo" e torna impossível devolver o mesmo débito duas vezes.
- **`ROLLBACK` de um `REFUND`** é permitido uma vez: ele volta a cobrar a aposta. A `BET` continua com sua compensação registrada, então um novo `REFUND` é rejeitado. `ROLLBACK` de `ROLLBACK` não é aceito (`INVALID_REFERENCE_KIND`).
- **Reversão sem saldo:** um `ROLLBACK` que precisaria debitar mais que o saldo disponível é `REJECTED` com **`REVERSAL_INSUFFICIENT_FUNDS`**, diferente do `INSUFFICIENT_FUNDS` usado para `BET`. Fica persistido e auditável.
- **Sem efeito em cascata:** reverter uma aposta não reverte automaticamente o `WIN` da mesma rodada.

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-10 e catálogo de códigos em [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §5.

---

## 9. Inbox, outbox e mensageria

### 9.1 Entrada (SQS)

- **Filas:** `wager-transactions.fifo`, com redrive para `wager-transactions-dlq.fifo` após **10** recebimentos. O provisionamento é automático e idempotente.
- **`MessageGroupId = walletId`**: ordem dentro da carteira e paralelismo entre carteiras. **`MessageDeduplicationId = messageId`**: apenas uma otimização, porque a garantia vem da inbox e da idempotência.
- **Caso de uso:** `app.ConsumeWager` consulta a inbox, valida o `data` com as mesmas regras do HTTP e chama o mesmo `ProcessWager`, que grava a inbox em todo caminho de conclusão (resultado novo, replay e `FAILED`). O adaptador `sqsconsumer` só faz o transporte: decodifica o envelope, calcula o hash e decide a ação na fila pelo resultado.
- **Inbox:** `UNIQUE (consumer_name, message_id)` e hash do conteúdo, gravados **na mesma transação** do domínio, do ledger e da outbox. Na reentrega de uma mensagem já tratada, a mensagem é removida sem efeito. O mesmo `messageId` com conteúdo diferente vai para a DLQ. Dois consumidores com a mesma mensagem disputam a PK da inbox: o perdedor desfaz e recomeça pela consulta à inbox.
- **Remoção só depois do commit.** Rejeições de negócio, pendências de referência e replays também são concluídos e removidos. Um crash entre o commit e a remoção resulta em reentrega, que a inbox absorve.
- **Falha transitória:** a mensagem não é removida e a visibilidade é ajustada com backoff `min(2^recebimentos s, 300 s)`, cerca de 18 minutos até a DLQ. Depois de uma falha transitória, o consumidor faz um ping no PostgreSQL: se o banco não responde, os pollers **param de buscar mensagens** até ele voltar, para não consumir tentativas nem mandar mensagens válidas para a DLQ.
- **Grupos:** as mensagens de um lote são agrupadas por `MessageGroupId`; os grupos rodam em paralelo (semáforo por instância) e as mensagens do grupo em sequência. Uma falha transitória devolve o resto do grupo à fila, preservando a ordem.
- **Erro permanente:** mensagem inválida (formato, validação, `OPENING`, carteira inexistente, conflito de idempotência) ou falha permanente de infraestrutura (`FAILED`, registrado no banco junto com a inbox). Nos dois casos, envio explícito para a DLQ com os atributos `errorCode`, `errorCategory`, `originalMessageId`, `consumerName` e `failedAt`, seguido da remoção. Se o envio falhar, a mensagem não é removida e é tratada como transitória.
- **Visibility timeout de 30 s** e prazo de processamento de 10 s por mensagem; o start falha se o prazo não for menor que o visibility. Antes de começar cada mensagem, se o visibility restante não cobre o prazo, ela e as seguintes do grupo voltam à fila sem processar: nenhuma mensagem é processada depois de poder ter sido reentregue.

### 9.2 Saída (transactional outbox)

- **Os eventos são gravados na outbox na mesma transação** da mudança que os originou. O payload é o envelope completo, serializado uma única vez e **imutável** (trigger).
- **Publisher (qualquer instância):**
  1. reserva lotes com `FOR UPDATE SKIP LOCKED` e lease de 30 s;
  2. publica no **SNS FIFO** `wallet-events.fifo` fora da transação, com os grupos (`walletId`) em paralelo e cada grupo em sequência;
  3. confirma `published_at` só se ainda for o dono do lease;
  4. em caso de falha, **qualquer que seja o erro**, faz backoff `min(1 s × 2^tentativas, 5 min)` e libera o lease, sem nunca descartar o evento.

  Um lease vencido indica trabalho abandonado, e outra instância o reassume (`outbox_lease_reclaims_total`). Com o banco fora, o claim espera de 1 s a 30 s e tenta de novo; o publisher nunca derruba o processo.
- **Tópico:** o SNS não resolve tópico pelo nome. No start, o ARN é montado com a conta do `sts:GetCallerIdentity`, a região e o nome, e é verificado com `sns:GetTopicAttributes` (a única permissão extra do serviço). Tópico ausente derruba o start. O SNS não entra no readiness: com o broker fora, o HTTP continua e a outbox acumula.
- **Stop gracioso:** nenhum claim nem envio novo começa; os envios em andamento terminam e são confirmados, e o resto do lote fica com o lease, reassumido por outra instância.
- **Nenhuma publicação antes do commit:** o publisher só enxerga linhas confirmadas. Um teste segura uma transação aberta e confirma que nada é publicado.
- **Recuperação:** um crash entre o commit e a publicação faz o evento ficar pendente até ser publicado. Um crash entre a publicação e a confirmação faz o evento ser **republicado com o mesmo `eventId`** e o mesmo conteúdo (o JSON lido da coluna, nunca reserializado). O SNS FIFO deduplica dentro de 5 min, e os consumidores deduplicam pelo `eventId`.
- **Eventos:** `WagerTransactionProcessed` (inclusive `LOSS` e `OPENING`), `WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`.
  - Cada evento tem um tipo concreto, e o construtor define o tipo e a versão.
  - O envelope traz `eventId`, `eventType`, `version`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt` (RFC 3339 UTC) e `data` tipado. Os valores monetários vão como strings decimais.
  - **Contrato formal:** [`api/events.yaml`](api/events.yaml) (schemas OpenAPI 3.0.3). Os testes validam contra ele toda mensagem lida da fila de auditoria, e a verificação de consistência exige que todo evento de cada carteira seja publicado e entregue com o conteúdo do banco.
- **Contrato de consumo:**
  - a entrega é at-least-once, então o consumidor deduplica pelo `eventId`;
  - o `MessageGroupId` é o `walletId`;
  - o `walletVersion` ordena as mudanças de saldo;
  - uma fila `wallet-events-audit.fifo` assina o tópico e serve de consumidor de referência.

Detalhes: [`docs/messaging.md`](docs/messaging.md) e [`docs/decisions.md`](docs/decisions.md) D-12 e D-13.

---

## 10. Autenticação e autorização

### 10.1 Escolha do IdP: Keycloak

- **Padrões:** é um IdP OAuth 2.0/OIDC completo, com `client_credentials` para comunicação entre serviços, JWKS e discovery. É também a recomendação do desafio.
- **Provisionamento declarativo:** o realm inteiro (clients, roles, mappers e identidades de teste) é **importado de JSON** na subida do container. Assim, qualquer pessoa reproduz o ambiente autenticado a partir de um checkout limpo, sem passos manuais.
- **Claims controladas pelo IdP:** *protocol mappers* incluem no token uma claim `provider_id` fixa por client e a audiência da API. **É a identidade autenticada que determina o provedor**, nunca o corpo da requisição.
- **Alternativas mais leves**, como Dex ou Ory Hydra, exigiriam mais montagem para ter claims customizadas por client e um provisionamento declarativo equivalente.

### 10.2 Validação de credenciais

- Tokens `Bearer` JWT validados localmente com `go-oidc`:
  - assinatura **RS256** (qualquer outro algoritmo, inclusive `none`, é recusado);
  - chaves obtidas por **JWKS**, com cache;
  - `iss`, `aud = pda-api` e `exp` com tolerância de 30 s (`OIDC_CLOCK_SKEW`). O go-oidc compara o `exp` sem tolerância, então ela é aplicada atrasando o relógio do verificador; o `nbf` segue a tolerância fixa de 5 min da biblioteca.
- **Fail fast:** o JWKS é buscado no start. Um IdP inacessível impede a subida com um erro claro, e as réplicas do compose esperam o Keycloak saudável. O Keycloak não entra no readiness, que o desafio define como PostgreSQL e SQS.
- **Provedor sem nome:** a role `provider` só vale com a claim `provider_id`. Um token de provedor sem ela recebe 403.
- **Issuer e URL do JWKS configurados separadamente**, porque o `iss` público (`localhost`) difere do endereço interno do Keycloak na rede do compose. O verificador busca as chaves direto do JWKS, sem discovery: de dentro da rede, o discovery devolveria um `issuer` diferente da URL consultada.
- **A audience vem só de um mapper explícito por client.** O mapper padrão `audience resolve` do Keycloak, que poria `pda-api` em `aud` para qualquer client com roles nele, é removido do realm. Assim, a checagem de `aud` tem efeito real.
- **Resposta 401 uniforme** para token inválido ou expirado (`WWW-Authenticate: Bearer realm="pda", error="invalid_token"`), sem revelar qual foi o caso. Sem token, o desafio vai sem o `error`, como pede a RFC 6750.

### 10.3 Modelo de permissões

| Client (identidade) | Role em `pda-api` | Claim `provider_id` |
| --- | --- | --- |
| `provider-a`, `provider-b` | `provider` | `provider-a`, `provider-b` |
| `wallet-service` | `wallet-internal` | — |

| Rota | `provider` | `wallet-internal` | Pública |
| --- | --- | --- | --- |
| `/health/*`, `/docs`, `/openapi.yaml` | | | ✅ |
| `POST /wallets`, `GET /wallets/{id}`, `GET /wallets/{id}/ledger`, `POST /wallets/{id}/reconciliation` | 403 | ✅ | |
| `POST /wagering/transactions` | ✅ se `providerId` do corpo = token; senão 403 | 403 | |
| `GET /wagering/transactions/{id}` | ✅ só as próprias; de outro provedor → **404** | ✅ | |
| `GET /providers/{providerId}/wagering/transactions/{extId}` | ✅ se `providerId` do path = token; senão 403 | ✅ | |

- **Por que roles + claim, e não scopes:**
  - Os clients de `client_credentials` são identidades fixas de serviço.
  - A **role** responde "que tipo de chamador é este" (provedor ou serviço interno), e a **claim** responde "qual provedor". São duas dimensões diferentes: permissão de rota e escopo dos dados.
  - As roles de client vêm sempre no token. Scopes opcionais do Keycloak precisariam ser pedidos a cada emissão, o que abre espaço para erro de configuração.
  - Todas as decisões ficam em um único ponto (`auth/policy.go`).
- **Sem efeito nem vazamento:**
  - A autorização roda **antes** de qualquer leitura ou escrita, e a busca de idempotência usa o provedor do token. Um provedor não consegue fazer replay da operação de outro.
  - O 403 por divergência de provedor não depende da existência do recurso, e o 404 por id opaco não revela se o recurso existe. Os dois caminhos evitam enumeração.
- **Mensageria:** o acesso às filas e ao tópico é controlado por credenciais e **políticas IAM avaliadas pelo broker**: MiniStack com `AUTH=true`, um usuário IAM por principal e políticas de identidade de menor privilégio. Os provedores podem só enviar; o serviço pode consumir, publicar e enviar para a DLQ. O teste I04f prova as negações. O consumidor aplica **todas** as validações de domínio sem confiar na origem (ver limitações).

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-07 e [`docs/messaging.md`](docs/messaging.md) §2.1.

---

## 11. Composição com Uber Fx

- **Módulos:** um `fx.Module` por componente, registrados nesta ordem: `config`, `observability`, `postgres`, `aws`, `auth`, `app`, `references`, `outbox`, `sqsconsumer` e `httpapi`. Os construtores (configuração, conexões, repositórios, Unit of Work, casos de uso, handlers e workers) são injetados com `fx.Provide`, e `fx.Invoke` instancia os componentes com ciclo de vida (servidor e workers).
- **Fx só nas bordas:** `domain` e `app` são Go puro. `bootstrap` registra os construtores dos casos de uso, e cada adaptador conecta os próprios hooks.
- **Inicialização com validação (fail fast):**
  - configuração validada antes de qualquer conexão;
  - ping no PostgreSQL;
  - busca inicial do JWKS;
  - verificação das filas.

  Qualquer falha impede o start com um erro claro. Os servidores HTTP (API e admin) fazem o `Listen` de forma síncrona no `OnStart` (`observability.ServeOnLifecycle`): uma porta ocupada também impede o start, em vez de falhar silenciosamente numa goroutine.
- **Health checks por composição:** cada adaptador contribui um *checker* por *value group* do Fx (`group:"health_checkers"`), e o `observability` só os agrega. Assim o pacote de observabilidade não depende de `pgx` nem do SDK AWS.
- **Workers observáveis:** cada worker recebe um `context` cancelável e um `WaitGroup`, com prazos por item e logs de início e fim. O `OnStop` cancela e espera até o prazo.
- **Papéis por ambiente:** `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED` e `REFERENCE_WORKER_ENABLED`. Os módulos desligados nem são incluídos no grafo.
- **Servidor que para sozinho:** se o servidor da API ou o admin para de servir por conta própria, o `ServeOnLifecycle` registra o erro e pede ao Fx o encerramento com código 1 (`fx.Shutdowner`). O stop ordenado acontece como num `SIGTERM`, e o compose reinicia a réplica (`restart: on-failure`). Assim, uma réplica nunca fica de pé sem a API.
- **Verificação:** testes de `fx.ValidateApp`, de start/stop com tráfego real e de ausência de goroutines vazadas (`goleak`).

Detalhes: [`docs/structure.md`](docs/structure.md) §3 e [`docs/decisions.md`](docs/decisions.md) D-15.

---

## 12. Shutdown

O Fx executa os `OnStart` na ordem de registro e os `OnStop` na ordem inversa. As dependências são registradas primeiro e o HTTP por último: o HTTP só aceita tráfego com tudo pronto, **as entradas param antes e as dependências fecham por último**. Prazo total: `fx.StopTimeout` de 30 s; o shutdown dos componentes usa 20 s.

1. **HTTP:** `http.Server.Shutdown` deixa de aceitar conexões e conclui as requisições em andamento.
2. **Consumidor SQS:**
   - cancela o long polling na hora;
   - libera a visibilidade das mensagens recebidas e ainda não iniciadas;
   - espera as que estão em andamento até o prazo;
   - se o prazo vencer, cancela as restantes, faz rollback e libera a visibilidade para reentrega segura.
3. **Publisher e worker de referências:** param de reservar trabalho e terminam o item atual. Um lease reservado e não publicado vence e é reassumido por outra instância. O worker para depois do publisher: os eventos do último item dele ficam na outbox e são publicados por outra instância ou no próximo start.
4. **Dependências:** o pool do PostgreSQL e os clientes AWS (conexões ociosas do cliente HTTP do SDK) são fechados **depois** que todos os componentes que os usam terminaram, e o servidor admin (`/metrics`) é o último a parar. O teste I07b verifica isso com `goleak`.

**Encerramento abrupto (`SIGKILL`) é seguro por construção:** nada é removido do SQS sem commit, o lease da outbox expira e as pendências ficam agendadas no banco. Os testes e2e demonstram isso com um cluster de 3 processos e pontos de falha que encerram a instância com o código 137 no lugar exato (`internal/faultinject`, só no binário compilado com a tag `faultinject`): consumidor antes do commit e entre o commit e o `DeleteMessage` (C05b, C05a), HTTP entre o commit e a resposta (C05c), publisher com o evento reservado e entre a publicação e a confirmação (C06b, C06a), worker com a pendência reservada (C08b), e as 3 instâncias mortas com `SIGKILL` e reiniciadas (C08a).

**Shutdown gracioso provado com trabalho em andamento** (M9). O teste trava as carteiras, envia o `SIGTERM` com o trabalho parado no lock e só solta o lock depois de ver a parada no log.
- **R03 (SQS):** a instância para de receber, conclui as 3 mensagens em andamento, libera as recebidas e não iniciadas e sai com 0 dentro do `SHUTDOWN_TIMEOUT`, na ordem HTTP → consumidor → pool. Outra instância processa o resto, cada mensagem uma única vez.
- **R04 (HTTP):** uma conexão nova é recusada assim que o `Shutdown` começa, e as 5 requisições em andamento respondem 200.

---

## 13. Observabilidade

### 13.1 Logs

JSON estruturado (`log/slog`), com chaves em `camelCase`:
- **Identificadores**, quando disponíveis: `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`.
- **Propagação do `correlationId`:** vem do header `X-Correlation-Id` (ou é gerado), ou do atributo da mensagem SQS. É gravado na transação e segue nos eventos.
- **Uma linha por conclusão:** `wager concluded`, com os cinco identificadores (o `messageId` só no SQS), `channel`, `kind`, `outcome`, `failureCode` e `replay`. Vale para HTTP, SQS e replays. O log de acesso usa o padrão da rota (`unmatched` quando nenhuma casa).
- **Falhas também têm os IDs:** os logs de falha do consumidor SQS trazem `sqsMessageId`, `messageId`, `correlationId`, `walletId` e `providerId` (quando o envelope foi lido); os do publisher, `eventId`, `walletId` e `correlationId`. Os erros HTTP registram o `correlationId`, que os liga ao log de acesso (`route`, `providerId`).
- **Ciclo de vida:** cada componente registra o próprio start e stop em INFO (`http server started`, `sqs consumer stopping`, `postgres pool closed`…). Os eventos internos do Fx ficam em DEBUG (`LOG_LEVEL=debug`), e os erros do Fx, em ERROR.
- **Nunca são registrados:** tokens, headers de autorização, a `Idempotency-Key`, segredos, valores (`amount`, saldos) e corpos. O WARN da reconciliação traz só `walletId`, `correlationId` e `entries`; os saldos ficam na resposta ao chamador autorizado. O teste `TestLogsHaveIdsWithoutSecrets` prova isso com marcadores únicos.

### 13.2 Métricas

As métricas Prometheus ficam em `/metrics`, em uma porta administrativa separada da API.

| Métrica | Tipo | Labels | Atende |
| --- | --- | --- | --- |
| `wager_transactions_total` | counter | `channel` (`http`, `sqs`, `worker`), `kind`, `outcome`, `failure_code` | Resultados por status |
| `wager_processing_duration_seconds` | histogram | `channel`, `outcome` | Latência de processamento |
| `wager_duplicates_total` | counter | `channel`, `layer` (`inbox`, `idempotency`) | Duplicatas |
| `concurrency_conflicts_total` | counter | `reason` (`lock_timeout`, `unique_race`) | Conflitos de concorrência |
| `reference_pending_transactions` | gauge | — | Pendências abertas (atualizado pelo worker no máximo 1×/s) |
| `reference_retries_total` / `reference_expired_total` | counter | — | Retries e expirações de referência |
| `sqs_messages_processed_total` | counter | `outcome` | Resultados no SQS |
| `sqs_retries_total` | counter | `reason` | Retries |
| `sqs_dlq_sent_total` / `sqs_dlq_depth` | counter / gauge | `reason` / `queue` | DLQ |
| `sqs_processing_duration_seconds` | histogram | `outcome` | Latência no consumidor |
| `outbox_pending_events` / `outbox_oldest_pending_age_seconds` | gauge | — | Atraso da outbox |
| `outbox_publish_lag_seconds` | histogram | `event_type` | Atraso da outbox (publicação − ocorrência) |
| `outbox_published_total` / `outbox_publish_failures_total` / `outbox_lease_reclaims_total` | counter | `event_type` / — | Publicação, retries e recuperação |
| `reconciliation_runs_total` | counter | `consistent` | Reconciliações |
| `reconciliation_divergences_total` | counter | — | Divergências de reconciliação |
| `auth_failures_total` | counter | `reason` (`unauthenticated`, `forbidden`, `provider_mismatch`) | Diagnóstico de acesso |
| `http_requests_total` / `http_request_duration_seconds` | counter / histogram | `route` (padrão da rota ou `unmatched`), `method`, `status` | Tráfego HTTP |

O catálogo está completo desde o M7. Os gauges da outbox e o de referências são atualizados pelos próprios workers, no máximo 1×/s. `version_mismatch` não existe: o controle é pessimista (§4) e nenhum caminho produziria a label. Uma operação que passa por `PENDING_REFERENCE` aparece duas vezes em `wager_transactions_total`: com `pending_reference` no canal de entrada e com o desfecho no canal `worker`.

Métricas auxiliares do consumidor (`sqs_messages_received_total`, `sqs_receive_errors_total`) estão em [`docs/messaging.md`](docs/messaging.md) §8.

### 13.3 Health checks

- `GET /health/live`: o processo está de pé.
- `GET /health/ready`: PostgreSQL (ping) **e** SQS (`GetQueueAttributes`), executados em paralelo, com 2 s de timeout cada, mesmo que a dependência ignore o cancelamento. Responde 503 enquanto uma dependência estiver indisponível.
- **Corpo:** `{"status":"UP|DOWN","checks":{"postgres":"UP","sqs":"DOWN"}}`. O motivo de uma falha vai só para o log, nunca para a resposta.
- **Queda de uma dependência** (M9, R01 e R02): com o PostgreSQL ou o MiniStack congelados, o ready vai a 503 nas 3 instâncias e volta a 200 sozinho depois da queda.
- **Sempre os dois:** o ready checa PostgreSQL e SQS qualquer que seja o conjunto de papéis ligados. Com `HTTP_ENABLED=false` não há rotas de health (elas vivem no `httpapi`); o admin `:9090` serve só `/metrics`.
- **Healthcheck do container:** a imagem distroless não tem shell nem `curl`, então o compose usa o próprio binário (`pda healthcheck`), que consulta o `/health/ready` local.

**Reconciliação:** `POST /wallets/{id}/reconciliation` reconstrói o saldo a partir do ledger em uma transação `REPEATABLE READ READ ONLY` (visão consistente). Calcula `difference = stored − calculated`. As divergências aparecem na resposta, no log e na métrica, e **nada é alterado**.

---

## 14. Contrato HTTP e documentação da API

- **Contrato único:** `api/openapi.yaml` (OpenAPI 3.0.3, escrito à mão antes dos handlers), servido em `GET /openapi.yaml`. O Swagger UI em `GET /docs` permite obter o token no Keycloak e chamar qualquer uma das 3 instâncias. Todos os testes HTTP validam requisição e resposta contra o documento.
- **Situações distinguíveis pelo contrato:**

| Situação | Status | Corpo |
| --- | --- | --- |
| Processada (nova ou replay) | 200 | Resultado da transação (`transactionId`, `status`, `balance`, `idempotentReplay`) |
| Aguardando referência | 202 | Resultado da transação |
| Rejeição de negócio | 422 | Resultado da transação + `failureCode` + `failureCategory` |
| Falha permanente | 500 | Resultado da transação + `failureCode` + `failureCategory` |
| Entrada inválida | 400 | `application/problem+json` (RFC 9457) com `code` estável |
| Não autenticado / proibido / não encontrado | 401 / 403 / 404 | `problem+json` |
| Conflito de idempotência / carteira duplicada | 409 | `problem+json` |
| Indisponibilidade transitória | 503 | `problem+json` + `Retry-After` |

- **Categorias de falha:** cada código é `CORRECTABLE` (corrigir a entrada), `DEFINITIVE` (resultado de negócio, e reenviar não muda nada) ou `TRANSIENT` (tentar de novo).
- **Dois 500, distinguíveis pelo `Content-Type`:** `application/json` com `status: FAILED` é a falha permanente **registrada**; `application/problem+json` com `INTERNAL_ERROR` é um erro interno sem registro (um `panic`, por exemplo), e reenviar com a mesma chave é seguro.
- **Prazo por requisição** (`HTTP_REQUEST_TIMEOUT`, 10 s; M9): toda rota autenticada roda com esse prazo.
  - Motivo: com o banco congelado, as conexões TCP continuam abertas e uma query esperaria indefinidamente. O `lock_timeout` é aplicado pelo servidor, e o `WriteTimeout` não cancela a requisição.
  - Efeito: o prazo vencido é transitório e responde 503 com `Retry-After`. O reenvio com a mesma chave cai no replay se o commit tiver acontecido.
  - Validação: `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30 s`. Provado pelo R01 (queda do PostgreSQL com 3 instâncias).
- **Uniformidade:** rota inexistente (404 `ROUTE_NOT_FOUND`) e método errado (405 `METHOD_NOT_ALLOWED`) também respondem em `problem+json`. Todo 503 traz `Retry-After: 1`. Um valor com tipo JSON errado (ex.: `"amount": 25.00`) responde com o código do campo, e o número nunca é convertido, então não passa por ponto flutuante.
- **Correlação:** o `X-Correlation-Id` recebido (até 128 caracteres `[A-Za-z0-9._-]`) ou um gerado volta em toda resposta, vai no `correlationId` dos erros e é gravado na transação e nos eventos.
- **Acompanhamento de pendências:** `GET /wagering/transactions/{id}` e `GET /providers/{providerId}/wagering/transactions/{extId}` devolvem a representação completa da transação, inclusive `failureCode` e, para pendências, `attempts`, `nextAttemptAt` e `expiresAt`.

Detalhes: [`docs/decisions.md`](docs/decisions.md) D-04 e D-20, e o catálogo completo em [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §5.

---

## 15. Interpretações adotadas

Pontos em que o desafio deixa margem, e a leitura adotada:

1. **Processamento síncrono.** HTTP e SQS concluem a operação na hora. `PENDING` não é persistido, e o único estado de espera é `PENDING_REFERENCE` (§5).
2. **Rejeição de negócio é persistida e replayável** (422), enquanto a entrada inválida verificável sem estado não é persistida (400). O critério é depender ou não do estado do banco.
3. **`Idempotency-Key` vale por provedor.** Dois provedores podem usar a mesma chave sem conflito.
4. **O mesmo `(providerId, externalTransactionId)` com outra chave é conflito (409)**, e nunca replay. É a leitura literal de "não pode ser reaplicada usando outra chave".
5. **O replay devolve o status HTTP e o saldo originais**, inclusive para rejeições (422 com `idempotentReplay: true`).
6. **"Duas reversões do mesmo tipo"** foi lido de forma mais estrita: uma única compensação bem-sucedida por referência (§8).
7. **`WIN` com referência:** a referência, quando informada, é validada como `BET` processada da mesma rodada e, se ainda não existir, aguarda como as reversões. O valor do `WIN` pode ser diferente do da aposta.
8. **Referência `REJECTED`/`FAILED`** leva a rejeição imediata (`REFERENCE_NOT_PROCESSED`). Uma referência ainda pendente mantém a espera.
9. **Operações de carteira** (abertura, leitura, ledger, reconciliação) são exclusivas do serviço interno. O provedor vê o saldo apenas nos resultados das suas transações.
10. **Divergência de provedor → 403, recurso de outro provedor por id → 404.** As duas respostas evitam enumeração.
11. **`POST /wallets` não usa `Idempotency-Key`.** A unicidade `(playerId, currency)` torna a repetição um conflito (409), como o desafio pede.
12. **`LOSS` devolve o saldo corrente e não altera a versão.** Emite só `WagerTransactionProcessed`.
13. **Formato monetário estrito, sem normalização**, e moedas limitadas a BRL, USD e EUR (2 casas).
14. **A reconciliação sempre responde 200.** A divergência é sinalizada por `consistent: false`, log e métrica.
15. **`FAILED` só para falha permanente fora das regras de negócio**, registrada em uma transação separada, sem efeito financeiro. No SQS, a mensagem correspondente também vai para a DLQ, que o desafio exige para erros permanentes.
16. **Mensagens SQS não carregam token.** A autorização do canal é do broker (§10.3).
17. **"Caracteres imprimíveis" da `Idempotency-Key`** foi lido como ASCII visível (`0x21`–`0x7E`): sem espaço e sem caracteres não ASCII.
18. **Erro não classificado é transitório** (D-05). Diante de um erro desconhecido, responder 503 e permitir o reenvio é preferível a gravar um `FAILED` definitivo, que consumiria o `externalTransactionId`.

---

## 16. Limitações conhecidas

1. **O broker local autoriza, mas autentica fracamente.**
   - O MiniStack com `AUTH=true` avalia as políticas IAM, mas **não verifica a assinatura SigV4**: o principal é identificado só pelo access key id.
   - Ele também só concede acesso por política de identidade, não por `Allow` em política de recurso.
   - Em produção, as credenciais viriam de roles IAM (IRSA ou task role), sem chaves estáticas.
   - As chaves IAM do emulador são aleatórias e vivem só na memória do MiniStack. Se ele for recriado, o `aws-init` gera chaves novas e as réplicas precisam ser reiniciadas (`docker compose restart app-1 app-2 app-3`), porque o SDK lê o arquivo de credenciais no start. Reexecutar o `aws-init` com o MiniStack no ar reaproveita as chaves.
   - *Por que MiniStack:* a imagem atual do LocalStack exige conta e token, o que impediria reproduzir a solução a partir de um checkout limpo.
2. **O `providerId` no SQS não está vinculado a um principal autenticado.** Uma única fila recebe todos os provedores. Em produção, cada provedor teria sua fila ou principal IAM, e o `providerId` seria derivado da origem.
3. **A ordem dos eventos por carteira não é estrita** com vários publishers. Os consumidores ordenam pelo `walletVersion` e deduplicam pelo `eventId`.
4. **O envio explícito para a DLQ seguido de remoção não é atômico.** Um crash entre os dois passos pode deixar uma cópia a mais na DLQ, que a deduplicação FIFO reduz.
   - **Long poll órfão no shutdown:** um long polling cancelado pelo cliente continua aberto no broker até o fim do seu wait e pode esconder, por um visibility timeout, uma mensagem liberada nesse intervalo. Não há perda nem duplicidade, só atraso (verificado no MiniStack, [`docs/messaging.md`](docs/messaging.md) §4.5).
   - **Grupo com a cabeça sempre falhando:** as mensagens seguintes do grupo são liberadas junto a cada tentativa e também consomem recebimentos, então chegam à DLQ com ela. É o comportamento do FIFO; a pausa por saúde evita isso numa queda do banco.
5. **Contenção extrema em uma única carteira** gera 503 por lock timeout (5 s) em vez de enfileirar indefinidamente.
6. **Inbox e outbox crescem sem limpeza.** Produção exigiria retenção ou arquivamento dos registros concluídos.
7. **Ledger de entrada simples** (§3.3). Partidas dobradas não foram implementadas.
8. **O `/docs` carrega o Swagger UI por CDN.** Sem internet, é preciso usar o `/openapi.yaml` ou o `api/requests.http`.
9. **Segredos** do `.env.example` e do realm são valores locais de teste. Não há integração com um gerenciador de segredos nem rotação de credenciais.
10. **Não há rate limiting** nem cotas por provedor.
11. **Um erro permanente do SNS não descarta o evento.** Um tópico apagado ou uma política revogada fazem a outbox acumular (`outbox_oldest_pending_age_seconds` sobe) até a intervenção; nada confirmado se perde.
12. **Papéis por env (D-15).** `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED` e `REFERENCE_WORKER_ENABLED` (todos `true` por padrão) tiram o módulo do grafo; o admin `:9090` sempre sobe. O `/health/ready` continua exigindo PostgreSQL e SQS. Limitação: o `pda healthcheck` do container consulta a porta da API, então um container com `HTTP_ENABLED=false` ficaria sempre *unhealthy*.
13. **A espera por referências é finita.** Uma reversão cuja referência fique retida além do limite (por exemplo, numa indisponibilidade prolongada da fila) é rejeitada com `REFERENCE_NOT_FOUND`. O limite é configurável (`REFERENCE_MAX_ATTEMPTS`, `REFERENCE_TTL`).
14. **Ciclo de lock entre carteiras diferentes que se referenciam.** A antecipação de dependentes atualiza linhas de pendências de outra carteira sem travá-la. Duas pendências que se referenciam de carteiras diferentes poderiam, em teoria, formar um ciclo entre o worker e uma requisição HTTP. Isso já vale para o caminho HTTP desde o M3, e o `lock_timeout` o transforma num erro transitório (503 ou nova tentativa do worker); a ordem carteira → transação dentro da mesma carteira é provada por `TestResolveReferencesLockOrder`.
15. **A pausa por saúde é por instância** (M9). Numa queda geral do PostgreSQL, cada instância só pausa depois do próprio erro transitório, então uma mensagem pode ser recebida uma vez por instância consumidora antes de todas pausarem. Por isso o `maxReceiveCount` precisa superar com folga o número de instâncias: são 10 contra 3 réplicas ([`docs/messaging.md`](docs/messaging.md) §4.3).
16. **Um `Publish` que vence o prazo pode ser entregue depois.** Com o broker congelado, a requisição já enviada fica no buffer do socket e é processada quando o broker volta, embora o publisher já a tenha contado como falha e agendado outra tentativa. O evento chega de novo com o mesmo `eventId`, o que é o at-least-once de sempre (deduplicado pelo SNS FIFO em 5 min e pelos consumidores) (M9, R02).
17. **Reprocessamento da DLQ só pelo produtor, localmente.** O MiniStack 1.5.18 não implementa `StartMessageMoveTask` (responde `InvalidAction`). Na AWS, a mensagem volta da DLQ com `aws sqs start-message-move-task`. Localmente, o produtor reenvia a mesma mensagem, com o mesmo `messageId`, e a inbox e a idempotência tornam o reenvio seguro ([`README.md`](README.md) §5). Não há ferramenta própria de redrive.
18. **Réplica *unhealthy* não é reiniciada.** O `restart: on-failure` do compose traz de volta um processo que **saiu** com erro, inclusive quando um servidor HTTP para sozinho (§11). Sem um orquestrador, porém, o Docker não reinicia um container só porque o healthcheck falha.
19. **A vazão local da outbox é a do emulador.** No teste de carga (M12), a publicação ficou em cerca de 240 eventos/s, com o MiniStack no teto de 1 CPU. Como cada operação gera 2 eventos, acima de ~120 operações/s a outbox acumula e drena depois. Nenhum evento se perde, mas o atraso cresce: o p99 foi de 0,6 s a 100 req/s e de 36 s a 200 req/s ([`docs/load-test.md`](docs/load-test.md)). A capacidade na AWS depende dos limites do SNS FIFO, não medidos aqui. O publisher envia um evento por chamada (`Publish`), e o envio em lote (`PublishBatch`) seria o primeiro ajuste, com o ganho ainda não medido.

---

## 17. Trabalho não concluído

**Estado na entrega (30/09/2026):** os marcos M0 a M12 de [`docs/implementation-plan.md`](docs/implementation-plan.md) estão concluídos. Do M12, só o teste de carga foi feito. Isso inclui:
- todos os requisitos obrigatórios e os critérios eliminatórios E1–E10;
- os testes unitários, de integração, de múltiplas instâncias (C01–C12) e de resiliência (R01–R04);
- os três níveis rodando no CI.
- o teste de carga (TST-L01, M12): `make load-test` com k6 nas 3 réplicas, com a reconciliação de todas as carteiras e a drenagem da outbox como portões. Os resultados estão em [`docs/load-test.md`](docs/load-test.md).

O [`docs/delivery-requirements.md`](docs/delivery-requirements.md) liga cada requisito ao teste que o comprova, e o histórico dos marcos está no [`docs/dev/diary.md`](docs/dev/diary.md).

**Não feito (diferenciais opcionais do desafio):**
- **Tracing com OpenTelemetry e dashboards** (OBS-05), cortados no M12 (D-21). Os logs trazem os identificadores de correlação, e as métricas Prometheus cobrem o catálogo pedido (§13).
  - **Motivo do corte:** a versão mínima (`otelhttp` + `otelpgx`) produziria traces que terminam na requisição HTTP, justo onde o sistema é mais simples. O valor está no caminho assíncrono, e a versão útil não cabia no prazo.
  - **Como seria feito:**
    - `otelhttp` na borda e `otelpgx` no pool do pgx;
    - o contexto de trace (`traceparent`) gravado em cada linha da outbox, na mesma transação do evento;
    - o publisher abriria o span de publicação a partir dessa coluna e propagaria o contexto como atributo de mensagem do SNS, para os consumidores dos eventos;
    - o consumidor da fila de apostas continuaria o trace do provedor quando a mensagem trouxesse o contexto nos atributos;
    - o worker de referências ligaria o seu span ao da transação que destravou a pendência;
    - exporter OTLP para um Jaeger no compose.
- **Ledger de partidas dobradas** (LED-07). O ledger de entrada simples com cadeia verificável foi uma escolha (§3.3).

**Fora do escopo, com o que faltaria para produção:**
- **Retenção da inbox e da outbox:** os registros concluídos nunca são apagados (§16, item 6).
- **Operação da DLQ:** não há ferramenta própria de redrive (§16, item 17), nem alarme sobre `sqs_dlq_depth`.
- **Rate limiting e cotas por provedor** (§16, item 10).
- **Gerenciador de segredos e rotação de credenciais:** os segredos são os valores locais do `.env.example` (§16, item 9).
- **Papéis separados por container:** as flags existem, mas o healthcheck do container depende da API (§16, item 12).
- **Imagens fixadas por digest:** as imagens são fixadas só por tag ([`docs/stack.md`](docs/stack.md) §2.2).

**Pendências menores conhecidas** (sem efeito nas garantias; registradas nas revisões dos marcos):
- **Instantes zerados na inbox:** a inbox aceita um instante zerado de recebimento ou conclusão. Todo caminho do código grava instantes reais.
- **Escrita fora do UoW:** os repositórios sobre o pool, usados nas leituras, também expõem escritas. Só a convenção da D-14 impede usá-los fora de um `uow.Do`.
- **Log do healthcheck:** o healthcheck do Docker gera uma linha de log de acesso do `/health/ready` em INFO a cada 5 s por réplica.

**Verificação a partir de um clone limpo** (M11, 30/09): feita num `git clone` do GitHub, com o ambiente zerado e os caches do Go vazios. A subida, os exemplos do README, os comandos do desafio, o `make check` e as suítes de integração e e2e passaram. Os três achados eram do README e do `scripts/get-token.sh` e foram corrigidos. Nenhum ficou pendente.

---

## 18. Mapa da documentação

| Documento | Conteúdo |
| --- | --- |
| [`docs/getting-started.md`](docs/getting-started.md) | Guia para iniciantes: o projeto em linguagem simples, como subir e um roteiro de validação com payloads e respostas esperadas |
| [`README.md`](README.md) | Como executar, configurar e testar: pré-requisitos, variáveis, filas, migrations, identidades de teste e exemplos |
| [`docs/testing.md`](docs/testing.md) | Preparação das dependências dos testes, integração, múltiplas instâncias, simulações de falha e build tags |
| [`docs/load-test.md`](docs/load-test.md) | Teste de carga: comando, metodologia, ambiente e resultados (D-21) |
| [`api/openapi.yaml`](api/openapi.yaml) · [`api/events.yaml`](api/events.yaml) | Contratos da API HTTP e dos eventos de saída, validados nos testes |
| [`docs/decisions.md`](docs/decisions.md) | Registro detalhado de decisões (D-01 a D-21) |
| [`docs/data-model.md`](docs/data-model.md) | Schema, constraints, triggers, roles, consultas críticas e migrations |
| [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) | Máquina de estados, regras por tipo, referências, catálogo de códigos e pipelines |
| [`docs/messaging.md`](docs/messaging.md) | Filas, consumidor, DLQ, outbox e contratos dos eventos |
| [`docs/test-plan.md`](docs/test-plan.md) | Estratégia e casos de teste, rastreados até os requisitos |
| [`docs/structure.md`](docs/structure.md) | Organização do código e regras de dependência |
| [`docs/stack.md`](docs/stack.md) | Stack, versões, lint e formatação |
| [`docs/delivery-requirements.md`](docs/delivery-requirements.md) | Checklist dos requisitos do desafio |
| [`docs/implementation-plan.md`](docs/implementation-plan.md) | Marcos, riscos e ordem de corte |
| [`docs/development-workflow.md`](docs/development-workflow.md) | Fluxo spec → plano → TDD → verificação |
| [`docs/dev/`](docs/dev/) | Notas de desenvolvimento: specs e planos de cada marco, spikes e diário |
