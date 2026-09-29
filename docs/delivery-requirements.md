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

- [ ] **ART-01** Código-fonte Go formatado com `gofmt`.
- [ ] **ART-02** `go.mod` e `go.sum` versionados, com a versão do Go declarada em `go.mod`.
- [ ] **ART-03** `Dockerfile` declarando a mesma versão do Go.
- [ ] **ART-04** `docker-compose.yml` subindo aplicação, PostgreSQL, Keycloak (ou outro IdP) e LocalStack/MiniStack.
- [ ] **ART-05** Migrations versionadas, com `up` e `down`.
- [ ] **ART-06** Provisionamento automático das filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo` com redrive, além do destino dos eventos de saída.
- [ ] **ART-07** Provisionamento automático do IdP (realm, clients, roles/scopes e identidades de teste).
- [ ] **ART-08** `README.md` completo (ver DOC-01).
- [~] **ART-09** `ARCHITECTURE.md` completo (ver DOC-02).
- [ ] **ART-10** `.env.example` com valores locais e nenhum segredo real.
- [ ] **ART-11** Os comandos abaixo, ou equivalentes documentados, funcionam a partir de um checkout limpo:
  ```sh
  docker compose up --build
  go test ./...
  go test -race ./...
  go vet ./...
  ```

---

## 2. Autenticação e autorização (§2)

- [ ] **AUTH-01** ⛔ Integração com um IdP externo OAuth 2.0/OIDC (Keycloak recomendado) executando no Docker Compose. Cadastro de senhas e emissão própria de tokens ficam fora do escopo.
  *Evidência:* container do IdP no compose e teste de integração obtendo um token real.
- [ ] **AUTH-02** ⛔ Validação completa do token: assinatura via JWKS, `iss`, `aud`, `exp`/`nbf` e algoritmo permitido. Credencial ausente, inválida ou expirada resulta em rejeição.
  *Evidência:* TST-A01.
- [ ] **AUTH-03** Fluxo `client_credentials` para a comunicação entre serviços.
- [ ] **AUTH-04** ⛔ O `providerId` autorizado vem da identidade autenticada, nunca apenas do corpo da requisição. Divergência entre token e corpo é rejeitada.
- [ ] **AUTH-05** ⛔ Provedores acessam apenas suas próprias transações, inclusive em replays e consultas (`GET /wagering/transactions/:id` e `GET /providers/:providerId/...`).
- [ ] **AUTH-06** ⛔ Operações de carteira (abertura, leitura, ledger, reconciliação) ficam restritas ao serviço interno.
- [ ] **AUTH-07** ⛔ Um acesso não autorizado não produz efeito financeiro nem expõe dados. A autorização acontece antes de qualquer escrita ou consulta de idempotência.
- [ ] **AUTH-08** `GET /health/live` e `GET /health/ready` são públicos.
- [ ] **AUTH-09** O acesso à mensageria é controlado por credenciais e políticas do broker, e o consumidor continua aplicando as validações de domínio.
- [ ] **AUTH-10** A escolha do IdP, a validação de credenciais e o modelo de permissões estão justificados no `ARCHITECTURE.md`.

---

## 3. Money (§5.1, §6.1)

- [ ] **MON-01** ⛔ Nenhum `float32`/`float64` no parsing, no cálculo, na serialização ou na persistência de dinheiro.
  *Evidência:* código do tipo, marshal/unmarshal próprios e testes.
- [ ] **MON-02** Value object imutável com valor e moeda, suportando: criação a partir de string decimal, zero por moeda, soma, subtração, negação, comparação e serialização.
- [ ] **MON-03** Representação em `int64` com unidades mínimas ou decimal exato, com limites documentados.
- [ ] **MON-04** Contrato externo `{"amount":"25.00","currency":"BRL"}`, com escala fixa de 2 casas e moeda ISO 4217.
- [ ] **MON-05** Rejeição de valores vazios, `NaN`, `Infinity`, notação científica, escala excedente e negativos nas entradas externas. Nada é arredondado silenciosamente.
- [ ] **MON-06** Se formas equivalentes forem aceitas (ex.: `"25"` → `"25.00"`), a normalização anterior ao hash de idempotência está documentada. *(Para `amount`, não se aplica: o formato é estrito e nenhuma forma equivalente é aceita (D-03). A única normalização, UUID em minúsculas, está documentada em D-08.)*
- [ ] **MON-07** Aritmética e comparação exigem moedas compatíveis, com erro tipado quando não forem.
- [ ] **MON-08** Com `int64`, overflow é tratado no parsing, na soma, na subtração e na negação (incluindo `math.MinInt64`).
- [ ] **MON-09** Valores negativos são permitidos em cálculos internos, mas não no saldo da carteira.
- [ ] **MON-10** A persistência preserva exatamente valor e moeda (ex.: `BIGINT` em unidades mínimas + `CHAR(3)`).
- [ ] **MON-11** Um `Money` não inicializado (zero value do struct) é rejeitado.
- [ ] **MON-12** Os cenários podem usar apenas BRL, desde que o tipo carregue a moeda e existam testes de incompatibilidade entre moedas.

---

## 4. Modelo de domínio: regras gerais (§6)

- [ ] **DOM-01** Entidades com estado encapsulado, construtores com validação e métodos explícitos de transição.
- [ ] **DOM-02** Criação e reidratação separadas. A reidratação não reaplica movimentações, transições nem emissão de eventos.
- [ ] **DOM-03** Valores de domínio não inicializados ou inválidos são rejeitados.
- [ ] **DOM-04** Erros de domínio classificáveis por tipo ou por `errors.Is`/`errors.As`.
- [ ] **DOM-05** Nenhum `panic` representa rejeição de negócio.
- [ ] **DOM-06** Toda operação de I/O recebe `context.Context` e respeita cancelamento e timeout.
- [ ] **DOM-07** O domínio não depende de Fx, HTTP, SQS nem de bibliotecas de persistência.
  *Evidência:* teste que verifica os imports do pacote de domínio (ex.: `go list -deps`) ou regra de lint.

---

## 5. Wallet (§6.2)

- [ ] **WAL-01** Campos: id, playerId, moeda, saldo, versão, `createdAt` e `updatedAt`.
- [ ] **WAL-02** Criação, reidratação e operações de débito/crédito expostas pelo agregado.
- [ ] **WAL-03** O par `(playerId, currency)` é único no banco. Uma segunda abertura resulta em conflito.
- [ ] **WAL-04** ⛔ Débitos preservam saldo `>= 0`, no domínio e por `CHECK` no banco.
- [ ] **WAL-05** A moeda da movimentação coincide com a moeda da carteira.
- [ ] **WAL-06** Toda mudança de saldo tem o lançamento de ledger correspondente no mesmo commit.
- [ ] **WAL-07** A versão inicial é `1` e só é incrementada quando o saldo muda (`LOSS` não incrementa).
- [ ] **WAL-08** ⛔ Disputas entre escritores não descartam uma atualização confirmada (sem lost update).
- [ ] **WAL-09** A estratégia de controle de concorrência está documentada.

---

## 6. WagerTransaction (§6.3)

- [ ] **TX-01** Tipos `OPENING`, `BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`. `OPENING` recebido por HTTP ou SQS é rejeitado.
- [ ] **TX-02** Uma transação externa registra: id interno, id externo, provedor, chave de idempotência, hash do payload, carteira, jogador, rodada, jogo, tipo, `Money`, referência externa opcional, estado e timestamps.
- [ ] **TX-03** Quando aplicável, persiste também: a referência interna resolvida, o `failureCode` e o resultado financeiro devolvido ao provedor (saldo observado).
- [ ] **TX-04** Uma transação `OPENING` registra identidade interna estável, carteira, jogador, moeda, valor, estado e timestamps. Os campos externos não se aplicam.
- [ ] **TX-05** O schema distingue origem interna de externa (ex.: coluna `origin` + `CHECK`) e impede um crédito inicial duplicado (índice único parcial).
- [ ] **TX-06** Máquina de estados validada pelo domínio: `PENDING`, `PENDING_REFERENCE`, `PROCESSED`, `REJECTED`, `FAILED`.
- [ ] **TX-07** Estados terminais (`PROCESSED`, `REJECTED`, `FAILED`) não aceitam novas transições.
- [ ] **TX-08** Um replay consulta o resultado persistido sem reaplicar a operação.
- [ ] **TX-09** ⛔ Todo `PENDING` confirmado tem retomada durável por outra instância. Operações sem dependências podem ser concluídas de forma síncrona, sem commit intermediário de aceite.
- [ ] **TX-10** A máquina de estados e a distinção entre falha transitória e permanente estão documentadas.

---

## 7. WalletLedgerEntry (§6.4)

- [ ] **LED-01** Campos: `id`, `walletId`, `transactionId`, direção (`DEBIT`/`CREDIT`), valor, saldo anterior, saldo posterior e `createdAt`.
- [ ] **LED-02** Um lançamento é imutável, e o construtor valida `balanceAfter = balanceBefore ± money`.
- [ ] **LED-03** ⛔ `UNIQUE (walletId, transactionId)` no banco.
- [ ] **LED-04** ⛔ Append-only imposto pelo banco: trigger bloqueando `UPDATE`/`DELETE`/`TRUNCATE` e/ou `REVOKE` de privilégios.
- [ ] **LED-05** `LOSS` e operações rejeitadas não produzem lançamento (garantido também por trigger no banco, [`data-model.md`](data-model.md) §4.2).
- [ ] **LED-06** `CHECK`s no banco: valor `> 0`, coerência before/after conforme a direção e `balanceAfter >= 0`.
- [ ] **LED-07** ⭐ Ledger de partidas dobradas.

---

## 8. Operações e referências (§7)

- [ ] **OPS-01** `BET`: débito com valor `> 0` e saldo suficiente. Sem saldo, a operação é `REJECTED` com um código próprio.
- [ ] **OPS-02** `WIN`: crédito com valor `> 0`. Pode referenciar uma aposta da mesma rodada.
- [ ] **OPS-03** `LOSS`: exige `amount == "0.00"` e a moeda da carteira. Não cria ledger nem altera a versão. Emite `WagerTransactionProcessed` sem `WalletBalanceChanged`.
- [ ] **OPS-04** `REFUND`: crédito que devolve integralmente o valor de uma `BET` processada.
- [ ] **OPS-05** `ROLLBACK`: movimento contrário que desfaz integralmente uma `BET`, `WIN` ou `REFUND` processada.
- [ ] **OPS-06** `referenceExternalTransactionId` é obrigatório em `REFUND`/`ROLLBACK` e é resolvido por `(providerId, referenceExternalTransactionId)`.
- [ ] **OPS-07** A operação e sua referência concordam em provedor, jogador, carteira, moeda e rodada, e o valor é igual. Reversões parciais não são aceitas.
- [ ] **OPS-08** ⛔ Uma referência não recebe duas reversões bem-sucedidas do mesmo tipo, com garantia no banco.
- [ ] **OPS-09** A política para combinações de `REFUND` e `ROLLBACK` sobre a mesma aposta está documentada e impede a devolução duplicada do mesmo débito.
- [ ] **OPS-10** Uma reversão que debitaria mais que o saldo disponível é `REJECTED`, auditável, com código **diferente** do usado para aposta sem saldo.
- [ ] **OPS-11** Valor zero só é aceito no saldo inicial e em `LOSS`.
- [ ] **OPS-12** Referência ainda ausente: a operação é persistida como `PENDING_REFERENCE` e um worker tenta de novo com backoff exponencial, inclusive após reinício (agenda persistida no banco).
- [ ] **OPS-13** Há um máximo de tentativas ou TTL. Ao esgotar, a operação vira `REJECTED` com código de referência não encontrada e emite o evento de rejeição.
- [ ] **OPS-14** Está documentado o comportamento quando a referência existe mas ainda está pendente, ou quando terminou sem sucesso.
- [ ] **OPS-15** Toda rejeição tem um `failureCode` estável e documentado, que distingue entrada corrigível de resultado definitivo.

---

## 9. Idempotência (§5.2, §9)

- [ ] **IDEM-01** ⛔ A idempotência é persistente e sobrevive ao reinício de todos os processos.
- [ ] **IDEM-02** O header `Idempotency-Key` é obrigatório no HTTP. O servidor não substitui silenciosamente a chave recebida.
- [ ] **IDEM-03** Hash determinístico dos campos de negócio em JSON canônico com chaves ordenadas. A chave e os metadados de transporte ficam fora do cálculo. Algoritmo, campos e normalizações estão documentados.
- [ ] **IDEM-04** O hash é equivalente entre HTTP e SQS para a mesma operação.
- [ ] **IDEM-05** Mesma chave e mesmo conteúdo: devolve o resultado persistido com `idempotentReplay: true`.
- [ ] **IDEM-06** Mesma chave com conteúdo diferente: conflito.
- [ ] **IDEM-07** ⛔ `(providerId, externalTransactionId)` não pode ser reaplicado usando outra chave.
- [ ] **IDEM-08** O replay de uma operação concluída devolve o saldo observado no processamento original.

---

## 10. Concorrência (§5.6, §5.7, §8)

- [ ] **CONC-01** ⛔ Coordenação por carteira. Locks globais são proibidos (nada de lock de tabela ou advisory lock único).
- [ ] **CONC-02** Estratégia escolhida (pessimista, otimista com retry limitado, update condicional ou combinação) justificada.
- [ ] **CONC-03** ⛔ As invariantes financeiras valem no banco, independentemente de locks locais e da deduplicação do SQS FIFO.
- [ ] **CONC-04** ⛔ Garantias demonstradas com **≥ 3 processos independentes**, cada um com suas conexões e memória.
- [ ] **CONC-05** Uma carteira com 100.00 BRL recebe duas apostas simultâneas de 80.00. Resultado esperado: 1 `PROCESSED`, 1 `REJECTED` por saldo insuficiente, saldo final 20.00 e 1 débito no ledger. Reenvios não alteram esse resultado.
- [ ] **CONC-06** Carteiras diferentes são processadas em paralelo.

---

## 11. API HTTP (§9)

- [ ] **HTTP-01** `POST /wallets`: segue o contrato do desafio. Com saldo inicial positivo, carteira + `OPENING` `PROCESSED` + lançamento de crédito + outbox (`WagerTransactionProcessed` e `WalletBalanceChanged`) entram no mesmo commit, com versão `1`. Com saldo zero, não há `OPENING`, ledger nem eventos financeiros. Uma carteira duplicada resulta em conflito.
- [ ] **HTTP-02** `GET /wallets/:walletId`.
- [ ] **HTTP-03** `GET /wallets/:walletId/ledger?cursor=...&limit=50`: cursor opaco, ordenação estável e limite máximo definido.
- [ ] **HTTP-04** `GET /wagering/transactions/:transactionId`: mostra pendências e códigos de rejeição/falha.
- [ ] **HTTP-05** `GET /providers/:providerId/wagering/transactions/:externalTransactionId`.
- [ ] **HTTP-06** `POST /wagering/transactions`: segue o contrato do desafio (`transactionId`, `status`, `balance`, `idempotentReplay`).
- [ ] **HTTP-07** `POST /wallets/:walletId/reconciliation`: reconstrói o saldo a partir do ledger (incluindo a abertura) em uma visão consistente (snapshot) e devolve `difference = stored − calculated`. Divergências aparecem na resposta, no log e em uma métrica. **Não altera o saldo.**
- [ ] **HTTP-08** `GET /health/live` (processo) e `GET /health/ready` (PostgreSQL + SQS).
- [ ] **HTTP-09** Códigos HTTP e corpos de resposta documentados e **distinguíveis** para: entrada inválida, conflito, rejeição de negócio, processamento pendente e indisponibilidade transitória.

---

## 12. Consumidor SQS (§10)

- [ ] **SQS-01** Filas `wager-transactions.fifo` e `wager-transactions-dlq.fifo` provisionadas, com redrive configurado.
- [ ] **SQS-02** HTTP e SQS compartilham o mesmo caso de uso e as mesmas garantias. A chave de idempotência vem de `data.idempotencyKey`.
- [ ] **SQS-03** Inbox com `UNIQUE (consumerName, messageId)`, usando o `messageId` do envelope como identidade durável. O hash é verificado em reentregas.
- [ ] **SQS-04** Inbox, conclusão do tratamento, domínio, ledger e outbox compartilham a mesma transação SQL.
- [ ] **SQS-05** A mensagem só é removida da fila após o commit do tratamento durável.
- [ ] **SQS-06** Uma rejeição de negócio confirmada é terminal e permite remover a mensagem.
- [ ] **SQS-07** Falha transitória leva a retry com backoff. Erro permanente ou tentativas esgotadas levam à DLQ.
- [ ] **SQS-08** Com uma `PENDING_REFERENCE` já persistida, a mensagem pode ser concluída e o worker de referências assume a continuidade.
- [ ] **SQS-09** Em `SIGTERM`, o consumidor para de buscar mensagens e conclui o trabalho em andamento dentro do prazo, ou libera a visibilidade para reentrega segura.
- [ ] **SQS-10** Documentados: limites de tentativas, visibility timeout, tratamento de mensagens inválidas, `MessageGroupId` e `MessageDeduplicationId`.
- [ ] **SQS-11** A concorrência entre as entradas HTTP e SQS foi validada.

---

## 13. Transactional outbox e eventos (§5.4, §6.5, §11)

- [ ] **OUT-01** Tabela de outbox com: identidade estável do evento, agregado, tipo, payload (snapshot imutável), `occurredAt`, tentativas, próximo envio e `publishedAt`.
- [ ] **OUT-02** ⛔ Os registros de outbox são gravados atomicamente com o estado da operação, o saldo, o ledger e a inbox.
- [ ] **OUT-03** ⛔ Um worker separado publica a outbox e suporta múltiplos publishers, disputa por registros (ex.: `FOR UPDATE SKIP LOCKED` + lease) e recuperação de trabalho abandonado.
- [ ] **OUT-04** Retry com backoff em falha de publicação.
- [ ] **OUT-05** Republicações preservam o `eventId`.
- [ ] **OUT-06** Recuperação demonstrada em dois cenários: (a) interrupção entre o commit e a publicação; (b) interrupção entre a publicação e a confirmação na outbox. Os eventos pendentes são assumidos por outra instância.
- [ ] **OUT-07** O destino dos eventos de saída está provisionado e os contratos de roteamento e consumo estão documentados.
- [ ] **OUT-08** Tipos concretos para `WagerTransactionProcessed`, `WagerTransactionRejected`, `WalletBalanceChanged` e `WagerTransactionPendingReference`.
- [ ] **OUT-09** Envelope com `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` (opcional), `occurredAt`, `version` e `data` tipado. Tipo e versão são definidos pelo construtor do evento.
- [ ] **OUT-10** ⛔ Nenhum evento é publicado antes do commit da transação que o originou.
- [ ] **OUT-11** O payload de `WalletBalanceChanged` inclui `walletId`, `transactionId`, `direction`, `money`, `balanceBefore`, `balanceAfter` e `walletVersion`.
- [ ] **OUT-12** Timestamps em UTC RFC 3339 e valores monetários como strings decimais.
- [ ] **OUT-13** Eventos de origem interna (`OPENING`) não exigem os metadados externos inaplicáveis.

---

## 14. Persistência (§4, §5.3, §5.8)

- [ ] **DB-01** PostgreSQL, com `pgx` e SQL explícito (preferencial; `sqlc` opcional).
- [ ] **DB-02** Transações, locks e constraints explícitos e verificáveis.
- [ ] **DB-03** ⛔ Unicidade, não negatividade e imutabilidade do ledger impostas pelo schema, pelas constraints e pelos mecanismos de proteção do banco.
- [ ] **DB-04** Migrations versionadas, com os comandos de aplicação e reversão documentados.
- [ ] **DB-05** `ARCHITECTURE.md` documenta a biblioteca escolhida, o mapeamento de `Money` e como a transação SQL é delimitada entre os repositórios.

---

## 15. Composição e ciclo de vida com Uber Fx (§4)

- [ ] **FX-01** Configuração, conexões, repositórios, casos de uso, handlers e workers compostos com `fx.Module`, `fx.Provide` e `fx.Invoke`, com injeção por construtor.
- [ ] **FX-02** A inicialização valida a configuração e as dependências (fail fast).
- [ ] **FX-03** Workers com cancelamento, prazos de execução e término observável.
- [ ] **FX-04** O shutdown interrompe novas entradas e conclui ou libera o trabalho em andamento.
- [ ] **FX-05** As dependências (pool do PostgreSQL, clientes) só são fechadas depois dos componentes que as usam.

---

## 16. Observabilidade (§12)

- [ ] **OBS-01** Logs em JSON com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`, quando disponíveis.
- [ ] **OBS-02** Logs sem credenciais, dados sensíveis ou payloads financeiros completos.
- [ ] **OBS-03** Métricas de: resultados por status, duplicatas, retries, DLQ, conflitos de concorrência, atraso da outbox, latência de processamento e divergências de reconciliação.
- [ ] **OBS-04** Health checks (HTTP-08).
- [ ] **OBS-05** ⭐ Tracing com OpenTelemetry e dashboards.

---

## 17. Testes (§13)

### 17.1 Unitários

- [ ] **TST-U01** `Money`: parsing, operações, escala, limites numéricos, entradas inválidas e incompatibilidade de moedas.
- [ ] **TST-U02** Invariantes da carteira.
- [ ] **TST-U03** Transições de estado da transação.
- [ ] **TST-U04** Regras dos cinco tipos externos, incluindo a política de valor zero de cada um.
- [ ] **TST-U05** Conflito de payload com a mesma chave (hash canônico).
- [ ] **TST-U06** Abertura interna: metadados e eventos gerados.

### 17.2 Integração (containers reais: PostgreSQL, IdP e LocalStack/MiniStack)

- [ ] **TST-I01** Migrations: `up` e `down`.
- [ ] **TST-I02** Constraints e imutabilidade do ledger.
- [ ] **TST-I03** Atomicidade financeira.
- [ ] **TST-I04** Inbox e reentrega.
- [ ] **TST-I05** Outbox concorrente, retry e DLQ.
- [ ] **TST-I06** Recuperação após reinicialização.
- [ ] **TST-I07** Composição Fx: validação do grafo, start e stop, e liberação dos recursos dos workers (sem goroutines vazadas).

### 17.3 Autenticação e autorização

- [ ] **TST-A01** ⛔ Integração real com o IdP. Credenciais ausentes, inválidas e expiradas são rejeitadas.
- [ ] **TST-A02** ⛔ Isolamento entre provedores em consultas e replays, e restrição das operações internas.
- [ ] **TST-A03** ⛔ Acessos não autorizados não geram efeito financeiro nem expõem dados.

### 17.4 Concorrência e recuperação

- [ ] **TST-C01** A mesma aposta enviada 50× em paralelo gera um único débito.
- [ ] **TST-C02** Disputa 100.00 vs 2× 80.00 (CONC-05).
- [ ] **TST-C03** Carteiras distintas processadas simultaneamente.
- [ ] **TST-C04** Cenários relevantes repetidos com **≥ 3 instâncias independentes**.
- [ ] **TST-C05** Consumidor interrompido depois do commit e antes do `DeleteMessage`: a reentrega é tratada corretamente.
- [ ] **TST-C06** Dois publishers disputando a mesma outbox, com recuperação de publicação validada.
- [ ] **TST-C07** `REFUND`/`ROLLBACK` entregue antes da referência: resolução posterior **e** rejeição por expiração.
- [ ] **TST-C08** Após reinício da aplicação, idempotência, pendências e consistência financeira são preservadas. Se houver aceite assíncrono, o processo é interrompido após o `PENDING` e outra instância retoma.
- [ ] **TST-C09** Ao final de cada cenário, o saldo armazenado é igual a Σ créditos − Σ débitos do ledger.
- [ ] **TST-C10** Cenários que cruzam HTTP e SQS para a mesma operação.
- [ ] **TST-C11** Os testes de duplicidade exercitam a deduplicação **da aplicação** (não só a do SQS FIFO), com recebimentos repetidos comprovados.
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
- [ ] **DOC-06** ⭐ Contrato OpenAPI (`api/openapi.yaml`) servido em `/openapi.yaml`, Swagger UI autenticável em `/docs` e coleção `api/requests.http`, com o contrato validado nos testes (D-20).

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
