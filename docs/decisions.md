# Registro de Decisões

Decisões técnicas e interpretações do [`CHALLENGE.md`](../CHALLENGE.md) adotadas antes da implementação. Este documento é a fonte do `ARCHITECTURE.md` final. Os IDs entre colchetes apontam para [`delivery-requirements.md`](delivery-requirements.md).

**Origem da decisão:** 🗳️ escolhida com o autor · ⚙️ padrão técnico adotado (revisável)

## Resumo

| ID | Tema | Decisão | Origem |
| --- | --- | --- | --- |
| D-01 | Stack base | Go 1.27.1, `net/http` ServeMux, pgx v5, golang-migrate, slog, Prometheus, UUIDv7 | ⚙️ |
| D-02 | Emulador AWS | MiniStack (MIT, sem conta) com `AUTH=true`, SQS FIFO + SNS FIFO, políticas IAM avaliadas | 🗳️ |
| D-03 | Money | `int64` em centavos, escala 2, formato de entrada estrito, sem normalização | 🗳️ |
| D-04 | Semântica HTTP | Processamento síncrono; 200 / 202 / 422 / 500 / 400 / 401 / 403 / 404 / 409 / 503 | 🗳️ |
| D-05 | Processamento e estados | Sem commit intermediário de `PENDING`; só `PENDING_REFERENCE` é persistido como espera | ⚙️ |
| D-06 | Rejeições e erros | Validação sem estado → 400 sem persistir; regra com estado → `REJECTED` persistido | ⚙️ |
| D-07 | Autenticação e autorização | Keycloak, `client_credentials`, claim `provider_id`, roles `provider` e `wallet-internal` | ⚙️ |
| D-08 | Idempotência | Chave por provedor; `(providerId, externalTransactionId)` único; SHA-256 sobre JSON canônico | ⚙️ |
| D-09 | Concorrência | `SELECT … FOR UPDATE` na carteira + `CHECK` no banco | 🗳️ |
| D-10 | Reversões | Uma compensação bem-sucedida por BET (REFUND **ou** ROLLBACK) | 🗳️ |
| D-11 | Referências pendentes | Backoff exponencial persistido; limite por tentativas **e** TTL; WIN também aguarda | 🗳️ |
| D-12 | Consumidor SQS | `MessageGroupId = walletId`, `MessageDeduplicationId = messageId`, DLQ após 10 recebimentos, pausa por saúde | ⚙️ |
| D-13 | Outbox e eventos | Claim com `SKIP LOCKED` + lease → SNS FIFO `wallet-events.fifo` → SQS de auditoria | 🗳️ |
| D-14 | Fronteira transacional | Unit of Work explícito, com repositórios vinculados à transação | ⚙️ |
| D-15 | Fx e processos | Um binário com todos os papéis, habilitáveis por env; 3 réplicas no compose | ⚙️ |
| D-16 | Ledger e reconciliação | Ledger versionado por carteira; cursor pela versão; reconciliação em `REPEATABLE READ` | ⚙️ |
| D-17 | Proteções do banco | Roles `owner` e `app` separadas, triggers de imutabilidade, `CHECK`s | ⚙️ |
| D-18 | Observabilidade | slog JSON; `/metrics` em porta administrativa separada | ⚙️ |
| D-19 | Estratégia de testes | Build tags + infraestrutura do compose; e2e com 3 processos; injeção de falhas por build tag | 🗳️ |
| D-20 | Documentação da API | OpenAPI 3.0.3 *design-first* em `api/openapi.yaml`, Swagger UI em `/docs`, contrato validado nos testes (kin-openapi) | 🗳️ |

---

## D-01 — Stack base ⚙️

As versões fixadas, as imagens, as ferramentas de lint e formatação e a conformidade com a stack obrigatória estão em [`stack.md`](stack.md). A organização do código está em [`structure.md`](structure.md).

| Item | Escolha | Motivo |
| --- | --- | --- |
| Go | **1.27.1** (`go 1.27.1` no `go.mod` e `golang:1.27.1-alpine` no Dockerfile) | Versão instalada localmente |
| Roteador | `net/http.ServeMux` com padrões de método e path | Suficiente para 9 rotas e sem dependência extra |
| Banco | PostgreSQL 18 + `pgx/v5` (`pgxpool`) com SQL explícito, sem `sqlc` | É o preferencial do desafio; locks e constraints ficam visíveis no código |
| Migrations | `golang-migrate` (arquivos `NNNNNN_nome.up.sql` e `.down.sql`) | `up` e `down` explícitos, com CLI oficial em container |
| IDs | UUIDv7 (`google/uuid`) | Ordenáveis no tempo; os exemplos do desafio já são v7 |
| JWT/OIDC | `coreos/go-oidc/v3` | Faz cache de JWKS e valida `iss`, `aud`, `exp` e o algoritmo |
| AWS | `aws-sdk-go-v2` (SQS e SNS) | SDK oficial |
| Logs | `log/slog` com handler JSON | Biblioteca padrão |
| Métricas | `prometheus/client_golang` | Padrão de mercado |
| Testes | `testing`, `go.uber.org/goleak`, `getkin/kin-openapi` | `goleak` detecta goroutines vazadas no shutdown do Fx; `kin-openapi` valida o contrato HTTP nos testes (D-20) |

---

## D-02 — Emulador AWS: MiniStack 🗳️ [AUTH-09, SQS-01, OUT-07]

**Decisão:** usar `ministackorg/ministack:1.5.18` na porta 4566 com **`AUTH=true`**, para que as políticas IAM sejam de fato avaliadas.

**Motivo:**
- Desde a versão 2026.03.0, a imagem `localstack/localstack` exige `LOCALSTACK_AUTH_TOKEN` e conta. Isso quebra a exigência de reproduzir a solução a partir de um checkout limpo (§15).
- O MiniStack tem licença MIT, não pede conta e é compatível com o SDK e a porta do LocalStack. O spike do M0 ([`dev/spike-ministack.md`](dev/spike-ministack.md)) confirmou:
  - SQS FIFO, DLQ com redrive, `ChangeMessageVisibility` e long polling;
  - SNS FIFO com assinatura SQS FIFO e `RawMessageDelivery`;
  - avaliação de políticas IAM com `AUTH=true`.
- Nenhum plano B é necessário.

**Como o AUTH-09 é cumprido** (decisão do autor em 28/09/2026, depois do spike):
1. **Principals reais.** O `aws-init` usa a chave raiz do emulador (`test`) só para provisionar. Ele cria os usuários IAM `pda-wallet-service`, `provider-a` e `provider-b`, cada um com uma **política de identidade** de menor privilégio ([`messaging.md`](messaging.md) §2.1). A fila de auditoria recebe a única **política de recurso**, que permite a entrega pelo tópico (`aws:SourceArn`).
2. **Credenciais fora do código.**
   - O `aws-init` gera as chaves (`CreateAccessKey`, que no emulador é sempre aleatória) e grava o arquivo `.local/aws/credentials`, em formato INI, com um profile por usuário. O diretório é um bind mount ignorado pelo git.
   - A aplicação lê as chaves pelo mecanismo nativo do SDK: `AWS_SHARED_CREDENTIALS_FILE` + `AWS_PROFILE=pda-wallet-service`.
3. **Políticas versionadas e testadas.** Os documentos ficam em `deploy/aws/policies/`, são aplicados pelo `init.sh` e são reutilizados pelo teste I04f. Esse teste prova as negações: provedor não consome, serviço não envia na fila de entrada, ninguém fora da lista publica no tópico.
4. **Sem confiar na origem.** O consumidor aplica todas as validações de domínio.

**Limitações que continuam** (documentadas no `ARCHITECTURE.md` §16):
- O emulador **não verifica a assinatura SigV4**: o principal é identificado só pelo access key id. A **autorização** é real; a autenticação no broker é fraca localmente.
- O MiniStack só concede acesso por **política de identidade**. Ele respeita `Deny` em política de recurso e a política da fila na entrega do SNS, mas não aceita um `Allow` concedido só pela política de recurso, que na AWS basta dentro da mesma conta. Por isso as permissões ficam nas políticas de identidade, que também é o padrão usual na AWS.

**Testes de integração:** provisionam recursos isolados com a chave raiz. Só o I04f cria usuários com as políticas de `deploy/aws/policies/`.

---

## D-03 — Money 🗳️ [MON-*]

- **Representação:** `int64` em unidades mínimas (centavos) + código de moeda. Struct com campos privados e zero value inválido (`Money{}` é rejeitado).
- **Limites:** de −92.233.720.368.547.758,08 a 92.233.720.368.547.758,07. Todo parsing, soma, subtração e negação verifica overflow (`-math.MinInt64` gera erro).
- **Formato de entrada estrito, sem normalização:**
  - `amount` precisa casar com `^(0|[1-9][0-9]*)\.[0-9]{2}$`. Portanto `"25"`, `"25.0"`, `"025.00"`, `"+25.00"`, `"-1.00"`, `"1e3"`, `"NaN"` e `""` são rejeitados com 400.
  - `currency` precisa estar na lista de moedas ISO 4217 suportadas com 2 casas decimais: `BRL`, `USD` e `EUR`. Minúsculas (`"brl"`) são rejeitadas.
  - Como não há normalização, a string recebida já é a forma canônica usada no hash (D-08).
- **Sinal:** o parsing externo não aceita negativos. Valores negativos existem apenas em cálculos internos (`difference`, `Negate`) e só aparecem em **respostas**: uma `difference` negativa é serializada como `"-5.00"`.
- **Moedas:** qualquer operação ou comparação entre moedas diferentes devolve `ErrCurrencyMismatch`.
- **JSON:** `{"amount":"25.00","currency":"BRL"}` com marshal e unmarshal próprios. Nenhum `float` em nenhum ponto.
- **Persistência:** colunas `*_minor BIGINT` + `currency CHAR(3)`.

---

## D-04 — Semântica HTTP 🗳️ [HTTP-06, HTTP-09]

`POST /wagering/transactions` é **síncrono**: a operação é concluída na própria requisição.

| Situação | Status | Corpo | Persistido? |
| --- | --- | --- | --- |
| `PROCESSED` (novo ou replay) | **200** | Resultado da transação | Sim |
| `PENDING_REFERENCE` (novo ou replay) | **202** | Resultado da transação | Sim |
| `REJECTED` por regra de negócio (novo ou replay) | **422** | Resultado da transação + `failureCode` | Sim |
| Entrada inválida (formato, campo ausente, `OPENING`, carteira inexistente) | **400** | `problem+json` | Não |
| Token ausente, inválido ou expirado | **401** | `problem+json` + `WWW-Authenticate` | Não |
| Role insuficiente, ou `providerId` do corpo/path diferente do token | **403** | `problem+json` | Não |
| Recurso identificado por id que pertence a outro provedor, ou inexistente | **404** | `problem+json` | Não |
| Chave reutilizada com outro conteúdo, ou `externalTransactionId` com outra chave | **409** | `problem+json` | Não |
| Banco/broker indisponível, timeout ou lock timeout | **503** | `problem+json` + `Retry-After` | Não |
| Falha permanente de infraestrutura (`FAILED`, novo ou replay) | **500** | Resultado da transação + `failureCode` | Sim |

**Dois formatos de corpo, fáceis de distinguir:**
- **Resultado da transação** (operação registrada):
  ```json
  { "transactionId": "…", "status": "REJECTED", "failureCode": "INSUFFICIENT_FUNDS",
    "failureCategory": "DEFINITIVE", "balance": { "amount": "20.00", "currency": "BRL" },
    "idempotentReplay": false }
  ```
- **Erro** (nada registrado): `application/problem+json` no formato RFC 9457, com `type`, `title`, `status`, `code` estável e `detail`.

**Outras rotas:**
- `POST /wallets`: 201 na criação, 409 `WALLET_ALREADY_EXISTS`.
- `GET`s: 200 ou 404.
- `GET /wagering/transactions/:id` e `GET /providers/:providerId/wagering/transactions/:extId` devolvem a **representação completa** da transação: tipo, valor, referências, `status`, `failureCode`/`failureCategory`, `balance` quando concluída e, se `PENDING_REFERENCE`, `attempts`, `nextAttemptAt` e `expiresAt`. É assim que o cliente acompanha pendências (HTTP-04).
- `POST /wallets/:id/reconciliation`: 200 inclusive quando há divergência. `consistent: false` indica o problema.

**Detalhes do contrato** (spec do M3, 29/09/2026; o contrato completo está em [`api/openapi.yaml`](../api/openapi.yaml)):
- **`problem+json`:** `type: "about:blank"`, `title` com o texto do status, `status`, `detail` fixo por código (nunca ecoa valores recebidos), `code`, `category`, `field` (quando há um campo ou parâmetro culpado) e `correlationId`.
- **Tipo JSON errado** (ex.: `"amount": 25.00`, `"money": "x"`) é detectado na decodificação e responde com o código do campo: `INVALID_AMOUNT` para `money.amount`, `INVALID_CURRENCY` para `money.currency`, `INVALID_KIND` para `kind` e `INVALID_FIELD` para os demais. O número nunca é convertido, então não passa por `float`. `null` equivale a ausente (`MISSING_FIELD`).
- **Dois 500 distinguíveis pelo `Content-Type`:** `application/json` com `status: FAILED` é a falha permanente **registrada**; `application/problem+json` com `INTERNAL_ERROR` é um erro interno sem registro (`panic`, ou uma linha já gravada que não pode ser lida), e reenviar com a mesma chave é seguro.
- **Rotas:** caminho inexistente → 404 `ROUTE_NOT_FOUND`; método errado → 405 `METHOD_NOT_ALLOWED` com `Allow`. Os dois em `problem+json`, sem exigir token.
- **503:** sempre com `Retry-After: 1`.
- **Representações:** `Wallet` inclui `createdAt` e `updatedAt`; o `201` do `POST /wallets` traz `Location`. A representação de transação não expõe a `idempotencyKey`.

---

## D-05 — Processamento e máquina de estados ⚙️ [TX-06..10]

- Todo o processamento acontece em **uma única transação SQL**: travar a carteira, inserir a transação já no estado final, aplicar o movimento, gravar o ledger e a outbox, e marcar a inbox (quando vem do SQS).
- **`OPENING` segue a mesma máquina de estados:** nasce em `PENDING` (em memória) e é concluída por `Process` na mesma transação SQL da abertura.
- **Não existe commit intermediário de `PENDING`.** O estado `PENDING` só existe em memória e em testes, e TX-09 é atendido pela cláusula "operações sem dependências podem ser concluídas de forma síncrona". O único estado de espera persistido é `PENDING_REFERENCE`, e o worker de referências (D-11) garante sua retomada.
- **Transições válidas:**
  `PENDING → PROCESSED | REJECTED | FAILED | PENDING_REFERENCE`
  `PENDING_REFERENCE → PROCESSED | REJECTED | FAILED`
  Os estados terminais não aceitam novas transições, e um trigger no banco reforça isso (D-17).
- **Falha transitória:** o erro é devolvido ao chamador e nada é persistido. O HTTP responde 503 e o SQS faz retry. A classificação é feita por:
  - erros de conexão e rede;
  - `context.DeadlineExceeded`;
  - SQLSTATE `40001` (serialization failure), `40P01` (deadlock), `55P03` (lock timeout), `57P01` (admin shutdown), `53300` (too many connections), além da classe `08*` (connection exception);
  - **qualquer erro que nenhuma camada classificou** (decisão do autor em 29/09/2026, spec do M1). Um bug desconhecido não consome o `externalTransactionId` com um `FAILED` definitivo: o HTTP responde 503 e o SQS chega à DLQ pela redrive. Os adaptadores classificam explicitamente tudo o que é conhecido.
- **Falha permanente (`FAILED`):** erro que não é de negócio e não se resolve com retry, por exemplo um dado persistido corrompido que não pode ser reidratado, ou uma violação de constraint que o domínio não previu. Fica registrado para auditoria em uma transação separada, com `failureCode = INTERNAL_PERMANENT_FAILURE` e sem efeito financeiro. No HTTP, a resposta é 500. No SQS, essa mesma transação grava a inbox (`outcome = FAILED`), e a mensagem é enviada à DLQ, porque o desafio exige que erros permanentes cheguem à DLQ (§10).

---

## D-06 — Rejeições e erros de entrada ⚙️ [OPS-15]

**Regra:** tudo o que dá para validar **sem ler o banco** vira 400 e não é persistido. Tudo o que depende de estado vira `REJECTED`: é persistido, auditável e replayável.

| Tipo | Exemplos de código | Natureza |
| --- | --- | --- |
| Entrada inválida (400) | `INVALID_AMOUNT`, `INVALID_CURRENCY`, `MISSING_FIELD`, `INVALID_KIND`, `OPENING_NOT_ALLOWED`, `ZERO_AMOUNT_NOT_ALLOWED`, `LOSS_AMOUNT_MUST_BE_ZERO`, `REFERENCE_REQUIRED`, `UNKNOWN_WALLET`, … | `CORRECTABLE`: nada persistido; corrigir e reenviar com a mesma chave |
| Entrada incoerente com o estado (422, `REJECTED`) | `CURRENCY_MISMATCH`, `PLAYER_WALLET_MISMATCH`, `REFERENCE_MISMATCH`, `REVERSAL_AMOUNT_MISMATCH`, `INVALID_REFERENCE_KIND` | `CORRECTABLE`: o `externalTransactionId` foi consumido; enviar nova operação corrigida |
| Resultado de negócio (422, `REJECTED`) | `INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REFERENCE_NOT_FOUND`, `REFERENCE_NOT_PROCESSED`, `ALREADY_REVERSED` | `DEFINITIVE`: reenviar não muda o resultado |

As respostas trazem `failureCode` + `failureCategory`. `UNKNOWN_WALLET` é 400 porque a transação não pode ser persistida sem uma carteira válida (FK). O catálogo completo, com a semântica de cada código, fica em `transaction-lifecycle.md`.

---

## D-07 — Autenticação e autorização ⚙️ [AUTH-*]

**IdP:** Keycloak 26 com o realm `pda` importado automaticamente (`--import-realm`).

**Por que Keycloak:**
- OIDC completo, com `client_credentials`, JWKS e discovery; é também a recomendação do desafio.
- **Provisionamento declarativo:** o realm é importado de JSON, então a solução é reproduzível a partir de um checkout limpo.
- **Protocol mappers** colocam no token a claim `provider_id` fixa por client e a audiência `pda-api`. Assim, a identidade determina o provedor.
- Dex e Ory Hydra exigiriam mais montagem para ter claims customizadas por client e um provisionamento equivalente.

**Por que roles + claim, e não scopes:**
- A role responde "que tipo de chamador" (permissão de rota) e a claim responde "qual provedor" (escopo dos dados).
- As roles de client vêm sempre no token de `client_credentials`. Scopes opcionais precisariam ser pedidos a cada emissão.

**Clients** (confidenciais, com service account e apenas `client_credentials`):

| Client | Role (client `pda-api`) | Claim `provider_id` | Uso |
| --- | --- | --- | --- |
| `provider-a` | `provider` | `provider-a` | Provedor de testes |
| `provider-b` | `provider` | `provider-b` | Testes de isolamento |
| `wallet-service` | `wallet-internal` | — | Serviço interno de carteiras |
| `no-role-client` | — | — | Testes de 403 |
| `no-audience-client` | `provider` | `provider-a` | Sem audience mapper: testes de `aud` inválido |
| `provider-short-lived` | `provider` | `provider-a` | Token de 5 s para testes de expiração |

Um segundo realm, `other`, com um client de teste, fornece tokens com `iss` e chaves diferentes para os testes de emissor inválido.

**Validação no serviço:**
- Aceita só RS256.
- Chaves obtidas por JWKS, com cache.
- Verifica `iss`, `aud` e `exp`/`nbf` (tolerância de 30 s).
- O `aud` contém `pda-api`, adicionado por um audience mapper no Keycloak.

**Armadilha conhecida:** o `iss` do token depende do hostname que o cliente usou (`localhost` no host, `keycloak` na rede do compose). Para resolver (validado no spike, [`dev/spike-keycloak.md`](dev/spike-keycloak.md) §3):
- fixar `KC_HOSTNAME=http://localhost:8080` e `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`;
- no serviço, configurar `OIDC_ISSUER`, que é o `iss` esperado, separado de `OIDC_JWKS_URL`, que é por onde as chaves são buscadas;
- o verificador usa `oidc.NewRemoteKeySet(OIDC_JWKS_URL)` + `oidc.NewVerifier(OIDC_ISSUER, …)`, **sem discovery**. Dentro da rede, o discovery devolve um `issuer` (`localhost`) diferente da URL consultada (`keycloak`), e o go-oidc o recusaria.

**Segunda armadilha, a audience implícita:** o client scope padrão `roles` do Keycloak traz o mapper `audience resolve`, que põe em `aud` todo client do qual o token tem roles. Com ele, o `no-audience-client` receberia `aud: pda-api` e o teste de audience inválida não testaria nada. O `realm-pda.json` declara o scope `roles` **só com o mapper `client roles`**, e a audience vem exclusivamente do `oidc-audience-mapper` de cada client ([`dev/spike-keycloak.md`](dev/spike-keycloak.md) §2).

**Tolerância de relógio** (spec do M3, 29/09/2026): no go-oidc v3.21.0, o `exp` é comparado **sem** tolerância e o `nbf` tem uma tolerância fixa de 5 min. A tolerância de 30 s do `exp` é aplicada pelo `Config.Now` do verificador (`agora − OIDC_CLOCK_SKEW`), configurável; os testes de integração usam 1 s, para que o teste de token expirado (5 s de vida) não precise esperar 36 s.

**Fail fast e dependência do IdP:** o módulo `auth` busca o JWKS no `OnStart` (200 e ao menos uma chave RSA), e as réplicas do compose dependem do Keycloak saudável. O Keycloak não entra no readiness, que o desafio define como PostgreSQL + SQS.

**Healthcheck:** a imagem não tem `curl` nem `wget`. O compose usa `bash` com `/dev/tcp` contra `GET /health/ready` na porta de gerenciamento 9000 (`KC_HEALTH_ENABLED=true`).

**Matriz de permissões:**

| Rota | `provider` | `wallet-internal` | Pública |
| --- | --- | --- | --- |
| `GET /health/live`, `/health/ready` | — | — | ✅ |
| `GET /docs`, `GET /openapi.yaml` (D-20) | — | — | ✅ |
| `POST /wallets`, `GET /wallets/:id`, `GET /wallets/:id/ledger`, `POST /wallets/:id/reconciliation` | ❌ 403 | ✅ | |
| `POST /wagering/transactions` | ✅ se `body.providerId == token.provider_id`; senão 403 | ❌ 403 | |
| `GET /wagering/transactions/:id` | ✅ só as próprias; as de outro provedor dão **404** | ✅ todas | |
| `GET /providers/:providerId/wagering/transactions/:extId` | ✅ se `path.providerId == token.provider_id`; senão **403** | ✅ | |

**Regras gerais:**
- A autorização roda **antes** de qualquer leitura ou escrita.
- A busca de idempotência usa o `provider_id` do token.
- A role `provider` só vale com a claim `provider_id` preenchida. Um token de provedor sem a claim recebe 403 `FORBIDDEN`, nunca um "provedor vazio".
- O `app` não conhece o `auth`: a comparação do provedor e a visibilidade das consultas ficam na borda HTTP, antes de chamar o caso de uso.
- O 403 por divergência de provedor não depende da existência do recurso, e o 404 por id opaco não revela se ele existe. Os dois caminhos evitam enumeração.

**SQS:** a mensagem não carrega token. A confiança vem das credenciais e políticas do broker (D-02), e o consumidor aplica as mesmas validações de domínio do HTTP. **Limitação documentada:** em produção, cada provedor teria sua própria fila ou principal IAM, e o `providerId` seria derivado da origem da mensagem.

---

## D-08 — Idempotência ⚙️ [IDEM-*, SQS-03]

**Constraints (transações de origem externa):**
- `UNIQUE (provider_id, idempotency_key)`: a chave vale por provedor.
- `UNIQUE (provider_id, external_transaction_id)`.

**Fluxo, depois da autenticação:**
1. Busca por `(providerId do token, Idempotency-Key)`.
   - Encontrou e o hash é igual: devolve o resultado persistido com `idempotentReplay: true` e o status HTTP original.
   - Encontrou e o hash é diferente: 409 `IDEMPOTENCY_KEY_REUSED`.
2. Busca por `(providerId, externalTransactionId)`. Se encontrar, é a mesma operação com outra chave: 409 `EXTERNAL_TRANSACTION_ID_CONFLICT`.
3. Processa. Se duas requisições correrem ao mesmo tempo, a segunda viola a `UNIQUE`, faz rollback, relê e segue para o caminho 1 (no máximo 3 tentativas).

**Corrida entre as duas buscas** (achado da validação do plano do M3): as buscas 1 e 2 são leituras separadas, e uma entrega concorrente da mesma operação pode confirmar entre elas. Uma transação achada só pela busca 2 **com a mesma chave** é a transação da chave (caminho 1), nunca `EXTERNAL_TRANSACTION_ID_CONFLICT`.

**Hash do payload:**
- SHA-256 em hex sobre JSON canônico: chaves em ordem lexicográfica, sem espaços, strings em UTF-8.
- **Campos incluídos:** `providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money.amount`, `money.currency` e `referenceExternalTransactionId`. Este último é **omitido** quando ausente, sem serializar `null`.
- **Excluídos:** `Idempotency-Key`/`idempotencyKey`, `messageId`, `type`, `occurredAt`, headers e qualquer metadado de transporte.
- **Normalização:** os UUIDs são convertidos para a forma canônica em minúsculas. `amount` não é normalizado porque o formato já é estrito (D-03).
- **Chave de idempotência:** de 1 a 255 caracteres ASCII visíveis (`^[\x21-\x7E]{1,255}$`), sem espaço nem caractere não ASCII. A chave recebida nunca é alterada.
- **IDs no domínio:** como o domínio usa só a stdlib, os UUIDs circulam como `string` canônica em minúsculas, validada pelo pacote `internal/domain/ident`. O `app` gera os UUIDv7 com `google/uuid`.
- O hash é calculado a partir do comando de domínio já validado, que é o mesmo para HTTP e SQS. Assim os dois canais produzem hashes iguais, e há um teste cruzado que garante isso.

**Replay:** a transação guarda `result_balance_minor`, que é o saldo observado no processamento original, e o replay devolve esse valor em vez do saldo atual.

**Inbox (SQS):**
- `UNIQUE (consumer_name, message_id)`.
- O hash da mensagem é SHA-256 sobre o JSON canônico de `type` + `data` (inclui `idempotencyKey`).
- Mesmo `messageId` com hash diferente: erro permanente, a mensagem vai para a DLQ e a métrica é incrementada.

---

## D-09 — Concorrência: lock pessimista por carteira 🗳️ [CONC-*, WAL-08]

- Em toda operação financeira, a transação SQL (`READ COMMITTED`) define o `lock_timeout` local e em seguida executa `SELECT … FROM wallets WHERE id = $1 FOR UPDATE`.
  - O valor vem de `DB_LOCK_TIMEOUT` (padrão 5 s). Como `SET LOCAL` não aceita parâmetro, o UoW usa `SELECT set_config('lock_timeout', $1, true)`, que tem o mesmo efeito.
  - É aplicado no início de toda transação do `uow.Do`, e não só antes do lock da carteira. Assim também protege inbox, outbox e a antecipação de pendências.
- O saldo e a versão são recalculados pelo agregado e gravados com `UPDATE … SET balance_minor = $new, version = version + 1 WHERE id = $1 AND version = $old`. O lock já garante a exclusão; a condição na versão é uma segunda proteção.
- **Proteção no banco:** `CHECK (balance_minor >= 0)`. Mesmo com um bug no domínio, o saldo nunca fica negativo.
- **Sem deadlock:** cada operação trava uma única carteira.
- **Sem lock global:** carteiras diferentes não disputam nada.
- Um lock timeout é tratado como falha transitória: 503 no HTTP, retry no SQS, e a métrica `concurrency_conflicts_total` é incrementada.
- **Caso 100 vs 2×80:** a segunda transação espera o lock, lê o saldo de 20.00 e termina `REJECTED` com `INSUFFICIENT_FUNDS`.

---

## D-10 — Reversões: uma compensação por BET 🗳️ [OPS-04..10]

| Operação | Referências aceitas | Movimento |
| --- | --- | --- |
| `REFUND` | `BET` | Crédito do valor da BET |
| `ROLLBACK` | `BET` | Crédito do valor da BET |
| `ROLLBACK` | `WIN` | Débito do valor do WIN |
| `ROLLBACK` | `REFUND` | Débito do valor do REFUND (volta a cobrar a aposta) |

- **Garantia no banco:** `UNIQUE (reference_transaction_id) WHERE kind IN ('REFUND','ROLLBACK') AND status = 'PROCESSED'`.
  - Uma BET aceita no máximo **uma** compensação bem-sucedida, seja REFUND ou ROLLBACK. A segunda recebe `REJECTED` com `ALREADY_REVERSED`.
  - Um WIN ou um REFUND aceita no máximo um ROLLBACK.
  - Depois de `ROLLBACK(REFUND)`, a BET volta a estar debitada, mas o espaço de compensação dela continua ocupado pelo REFUND. Um novo REFUND é rejeitado, o que impede devolver o mesmo débito duas vezes.
  - `ROLLBACK` de `ROLLBACK` não é permitido (`INVALID_REFERENCE_KIND`).
- **Concordância obrigatória:** provedor (garantido pela busca), jogador, carteira, moeda, rodada (caso contrário, `REFERENCE_MISMATCH`), além de valor exatamente igual (caso contrário, `REVERSAL_AMOUNT_MISMATCH`).
- **Reversão com débito sem saldo suficiente:** `REJECTED` com `REVERSAL_INSUFFICIENT_FUNDS`, diferente do `INSUFFICIENT_FUNDS` usado para BET.
- Não há efeito em cascata: um ROLLBACK de BET não reverte automaticamente o WIN da mesma rodada.

---

## D-11 — Referências pendentes 🗳️ [OPS-12..14]

**Quando uma operação fica `PENDING_REFERENCE`:**
- REFUND, ROLLBACK ou WIN com referência cuja transação ainda não existe; ou
- a referência existe, mas está em `PENDING_REFERENCE`.

Nesses casos a operação é persistida como `PENDING_REFERENCE` e o evento `WagerTransactionPendingReference` é emitido uma única vez.

**Referência que já terminou sem sucesso** (`REJECTED` ou `FAILED`): `REJECTED` imediato com `REFERENCE_NOT_PROCESSED`.

**WIN com referência:** a referência precisa ser uma BET `PROCESSED` da mesma rodada, carteira, jogador e moeda. O valor pode ser diferente. Se ainda não existir, o WIN aguarda como os demais.

**Worker de referências:**
- Busca `status = 'PENDING_REFERENCE' AND next_attempt_at <= now()` com `FOR UPDATE SKIP LOCKED LIMIT n`.
- Processa cada item em sua própria transação, que também trava a carteira.
- **Backoff:** `next_attempt_at = now() + min(1s × 2^attempts, 60s) ± jitter de 20%`.
- **Limite:** 8 tentativas **ou** TTL de 10 min desde `created_at`, o que vier primeiro. Com os padrões, as 8 tentativas se esgotam em cerca de 3 min (1 + 2 + 4 + … + 60 + 60 s). O TTL limita a espera em tempo de relógio, inclusive quando nenhum worker rodou (por exemplo, com todas as instâncias paradas). Os valores são configuráveis por env, e os testes usam valores curtos.
- Ao esgotar o limite: `REJECTED` com `REFERENCE_NOT_FOUND` e emissão de `WagerTransactionRejected`.
- A agenda fica toda no banco, então sobrevive a reinícios e é assumida por qualquer instância.
- **Execução (M6):** os itens do lote são processados **em sequência** em cada instância (o paralelismo vem das réplicas); se o lote veio cheio e sem erros, o claim se repete sem esperar. O claim é **um único statement no pool** (`FOR UPDATE SKIP LOCKED`, `ORDER BY next_attempt_at`) que devolve `(id, walletId)`: não é um lease, só evita as linhas que outra transação processa naquele instante. Sob os locks (carteira, depois transação), o item só é reavaliado se ainda estiver em `PENDING_REFERENCE` **e** com `next_attempt_at <= now`; do contrário é ignorado. Sem a checagem do horário, duas instâncias que pegam o mesmo ID contariam a tentativa duas vezes.
- **`causationId` do worker:** o `transactionId` da referência, quando ela existe na avaliação; vazio quando não existe (R1 e expiração). O `correlationId` é o original da transação.
- **Falhas do worker:** falha permanente grava `FAILED` numa UoW separada (com `AdvanceDependents`, sem lançamento nem evento); falha transitória deixa a linha intacta, e ela volta no ciclo seguinte. Env: `REFERENCE_POLL_INTERVAL` (500 ms) e `REFERENCE_BATCH_SIZE` (50).
- **Otimização:** quando uma transação chega a um estado terminal (`PROCESSED`, `REJECTED` ou `FAILED`), a mesma transação SQL antecipa `next_attempt_at = now()` das pendências que a referenciam. Assim a pendência se resolve logo, seja para processar, seja para rejeitar com `REFERENCE_NOT_PROCESSED`.

**Canais:** o HTTP responde 202 e o cliente acompanha por `GET`. No SQS, a mensagem é removida após o commit da pendência (SQS-08).

---

## D-12 — Consumidor SQS ⚙️ [SQS-*]

| Parâmetro | Valor |
| --- | --- |
| Filas | `wager-transactions.fifo` → redrive para `wager-transactions-dlq.fifo` |
| `MessageGroupId` | `walletId`: ordem dentro da carteira e paralelismo entre carteiras |
| `MessageDeduplicationId` | `messageId` do envelope. A deduplicação de 5 min do FIFO é só uma otimização: a garantia vem da inbox e da idempotência |
| `maxReceiveCount` | 10 |
| Visibility timeout | 30 s. O prazo de processamento por mensagem é de 10 s, bem abaixo do visibility timeout |
| Recebimento | Long polling de 20 s, até 10 mensagens, N workers por instância |
| Falha transitória | Não remove; aplica `ChangeMessageVisibility` com backoff `min(2^receiveCount s, 300s)`, cerca de 18 min até a DLQ |
| Indisponibilidade do PostgreSQL | **Pausa por saúde:** os pollers param de chamar `ReceiveMessage` até o ping do banco voltar, de modo que uma queda geral não consome tentativas nem manda mensagens válidas para a DLQ (detalhes em `messaging.md` §4.3) |
| Mensagem inválida (JSON quebrado, `type` desconhecido, validação sem estado, `OPENING`, hash divergente na inbox) | Erro permanente: `SendMessage` explícito para a DLQ com o atributo `errorCode`, depois `DeleteMessage` e incremento da métrica. Uma cópia duplicada na DLQ é aceitável |
| Sucesso, `REJECTED` ou `PENDING_REFERENCE` | `DeleteMessage` **somente depois** do commit |
| `FAILED` (falha permanente de infraestrutura) | `FAILED` e inbox gravados em transação separada; depois, envio explícito para a DLQ com `errorCode = INTERNAL_PERMANENT_FAILURE` e `DeleteMessage` |
| `SIGTERM` | Cancela o long polling, espera o trabalho em andamento até o prazo de shutdown (20 s) e libera o que sobrar com `ChangeMessageVisibility(0)` |
| Nome do consumidor (inbox) | `wager-transactions-consumer` |
| Orquestração (M5) | Caso de uso `app.ConsumeWager`: inbox (`Find`, comparação do hash) → `NewCommand` → `ProcessWager` com o `ProcessRequest.Inbox` preenchido, que grava a inbox em todo caminho de conclusão (resultado novo, replay e `FAILED`). `ErrInboxDuplicate` no commit recomeça pelo `Find` (até 3 rodadas), então uma corrida entre conteúdos diferentes também vira `MESSAGE_HASH_MISMATCH`. O `sqsconsumer` só cuida do transporte |
| Gatilho da pausa por saúde (M5) | Depois de qualquer erro transitório, um `Ping` no pool (1 s). Se falha, os pollers pausam; se passa, o erro era da mensagem e segue o backoff. Não exige classificação nova no `apperrors` |
| Prazo × visibility (M5) | `SQS_PROCESSING_TIMEOUT < SQS_VISIBILITY_TIMEOUT`, validado no start. Antes de cada mensagem, se o visibility restante é menor que o prazo, ela e as seguintes do grupo são liberadas sem processar (`ChangeMessageVisibility(0)`) |
| Erro permanente sem `FAILED` (M5) | A linha já gravada com a mesma chave não pode ser lida: DLQ com `errorCode = INTERNAL_ERROR`, sem inbox (o 500 `INTERNAL_ERROR` do HTTP) |
| Falha do `DeleteMessage` após o commit (M5) | Log e `sqs_delete_errors_total`; a reentrega cai na inbox como duplicata |
| Long polling no shutdown (M5, achado da validação) | O poll cancelado pelo cliente continua aberto no broker até o fim do seu wait e pode esconder, por um visibility timeout, uma mensagem liberada nesse intervalo. Sem perda nem duplicidade (messaging §4.5). O `testkit.Audit.Absent` deixou de cancelar receives no meio pelo mesmo motivo |

---

## D-13 — Outbox e eventos 🗳️ [OUT-*]

- **Destino:** tópico SNS FIFO `wallet-events.fifo`. A fila `wallet-events-audit.fifo` o assina com `RawMessageDelivery` e serve de consumidor de referência nos testes. O `eventType` também vai como message attribute, para permitir filtros.
- **Roteamento:** `MessageGroupId = message_group_id` (sempre o `walletId`) e `MessageDeduplicationId = eventId`. Os contratos completos estão em `messaging.md`.
- **Tabela:** `event_id`, `aggregate_type`, `aggregate_id`, `message_group_id`, `event_type`, `event_version`, `payload jsonb`, `correlation_id`, `causation_id`, `occurred_at`, `attempts`, `next_attempt_at`, `locked_by`, `locked_until`, `published_at` e `last_error`. Um trigger impede alterar os campos do snapshot ([`data-model.md`](data-model.md) §4.5).
- **Publisher:**
  1. **Claim:** em uma transação curta, busca os pendentes cujo lease expirou com `FOR UPDATE SKIP LOCKED LIMIT n` e grava `locked_by`/`locked_until = now() + 30s`.
  2. **Publicação:** acontece fora da transação.
  3. **Confirmação:** `published_at = now()` somente onde `locked_by = eu`.
  4. **Falha:** `attempts++`, `next_attempt_at` com backoff limitado a 5 min e liberação do lease. Nenhum evento é descartado.
  5. **Trabalho abandonado:** um lease expirado é reassumido por outra instância.
  6. **Detalhes (M4, spec [`dev/specs/2026-09-29-m4-outbox-publisher-design.md`](dev/specs/2026-09-29-m4-outbox-publisher-design.md)):**
     - o backoff é `min(OUTBOX_RETRY_BASE_DELAY × 2^attempts, OUTBOX_RETRY_MAX_DELAY)`, calculado em Go com o `attempts` de antes da falha;
     - **todo** erro do `Publish` segue o caminho de falha, sem distinguir transitório de permanente, porque nada confirmado é descartado;
     - cada `Publish` tem timeout de `lease / 2`;
     - dentro do lote, os grupos (`walletId`) rodam em paralelo e cada grupo em sequência; uma falha não segura os eventos seguintes do grupo;
     - no shutdown gracioso, nenhum claim nem `Publish` novo começa, e os envios em andamento terminam e são confirmados;
     - os gauges de backlog são atualizados pelo publisher, no máximo 1×/s.
- **Tópico:** o ARN é resolvido no start. `sts:GetCallerIdentity` dá a partição e a conta, o ARN é montado com `AWS_REGION` e `SNS_EVENTS_TOPIC_NAME`, e `sns:GetTopicAttributes` confirma que o tópico existe (fail fast). A política do serviço ganha `sns:GetTopicAttributes` no recurso do tópico. O SNS não entra no readiness: com o broker fora, o HTTP continua e a outbox acumula.
- **Contrato formal:** [`api/events.yaml`](../api/events.yaml) define o envelope e os 4 eventos v1 com `additionalProperties: false`. Os testes validam contra ele toda mensagem lida da fila de auditoria.
- **Garantias:**
  - A entrega é at-least-once e o `eventId` é preservado nas republicações.
  - Os consumidores devem deduplicar por `eventId` e ordenar por `walletVersion`.
  - Com vários publishers, a ordem estrita por carteira não é garantida, e essa limitação fica documentada.
- **Envelope:** `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId?`, `occurredAt` (RFC 3339 UTC) e `version` (int), além de `data` tipado. Cada evento tem seu próprio struct e construtor; tipo e versão são métodos do tipo Go, e a interface `events.Event` é selada, então não há como criar um evento com tipo ou versão arbitrários. O domínio devolve os eventos tipados, e o `app` monta o envelope com `events.Seal(eventId, correlationId, causationId, evento)`, atribuindo o `eventId` antes do `INSERT` na outbox.

---

## D-14 — Fronteira transacional ⚙️ [DB-05]

- **Unit of Work explícito:** `uow.Do(ctx, func(tx Repos) error)`. `Repos` expõe os repositórios de wallet, transaction, ledger, outbox e inbox, todos ligados à mesma `pgx.Tx`.
- Commit se a função retornar `nil`, rollback em qualquer erro ou `panic`.
- A transação não é escondida no `context`: quem tem `Repos` está dentro da transação, e isso fica visível na assinatura.
- As interfaces ficam na camada de aplicação e o domínio não conhece o UoW.
- **Repositórios no Fx:** o módulo `postgres` fornece os repositórios e o `UnitOfWork` via `fx.Provide`. Fora de uma transação, os repositórios usam o pool (leituras). Dentro de `uow.Do`, o UoW instancia os mesmos repositórios sobre a `pgx.Tx`.
- **Snapshot de leitura:** `uow.Snapshot(ctx, fn)` roda em `REPEATABLE READ READ ONLY`, para a reconciliação (D-16).
- **Tipos do domínio nas portas:** os repositórios recebem e devolvem `wallet.Wallet`, `*wagering.WagerTransaction` e `wallet.LedgerEntry`, reidratados pelo adapter. Uma linha que o domínio recusa é erro permanente.
- **Corridas como sentinelas:** o adapter traduz as violações de unicidade que o `app` trata para sentinelas do `app` (`ErrWalletAlreadyExists` como conflito; `ErrIdempotencyRace`, `ErrReversalRace` e `ErrInboxDuplicate` como transitórias). O `app` usa `errors.Is` e nunca vê nome de constraint nem SQLSTATE. Uma corrida não interceptada é resolvida pelo retry, que cai na releitura.
- **Erros do banco sanitizados:** o erro traduzido leva só o SQLSTATE, o nome da constraint, a sentinela e o `Kind`. O `*pgconn.PgError` não sai do adapter, porque o `Detail` traz a linha inteira e acabaria no log (OBS-02). Um valor de domínio recusado pelo adapter (zero value, snapshot corrompido) é sempre permanente. Um ID que não é UUID canônico em `Get`/`Lock` resulta em "não encontrado", sem ir ao banco.
- **Unidade de trabalho interrompida pelo `context`:** se o `ctx` terminou, nada foi confirmado (ou o resultado do commit é desconhecido). O erro leva o do `ctx` na cadeia e é transitório, e o caminho de idempotência torna o retry seguro (DOM-06).

---

## D-15 — Composição Fx e processos ⚙️ [FX-*, CONC-04]

- **Um binário (`cmd/pda`) com papéis habilitáveis por env:** `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED` e `REFERENCE_WORKER_ENABLED` (todos `true` por padrão).
- **Módulos Fx:** `config`, `observability`, `postgres`, `aws`, `auth`, `app` (casos de uso), `references`, `outbox`, `sqsconsumer` e `httpapi`, **nessa ordem de registro** ([`structure.md`](structure.md) §3).
- **Ordem do start e do shutdown:** o Fx executa os `OnStart` na ordem de registro e os `OnStop` na ordem inversa. O HTTP é o último a iniciar (só aceita tráfego com tudo pronto) e o primeiro a parar:
  1. o servidor HTTP para de aceitar requisições (`Shutdown`);
  2. o consumidor é encerrado;
  3. o publisher da outbox e o worker de referências são encerrados;
  4. por último, o pool do PostgreSQL e os clientes AWS são fechados.
- **Workers:** cada um recebe um `context` cancelável e um `sync.WaitGroup`. O `OnStop` cancela e espera até o prazo, registrando em log o início e o fim.
- **Flags de papel (entregues no M7):** `bootstrap.OptionsFor(config.Roles)` monta o grafo sem os módulos dos papéis desligados; `bootstrap.Options()` lê as quatro variáveis (`config.RolesFromEnv`) antes do Fx, e um valor inválido aborta o start nomeando a variável, sem ecoar o valor. O servidor admin e a `observability` ficam sempre; o `auth` entra junto com o HTTP. Todos os papéis desligados é válido (só o admin sobe, com um `WARN`). Com `HTTP_ENABLED=false` não há rotas de health, porque elas vivem no `httpapi`.
- **Compose:** o serviço `app` roda com 3 réplicas (`app-1`, `app-2`, `app-3`), cada uma com seu pool e sua memória. Portas no host: API em `8081`, `8082` e `8083`, métricas em `9091`, `9092` e `9093`, e Keycloak em `8080`.
- **Arquitetura verificável:** um teste garante que o pacote `domain` não importa `fx`, `net/http`, `aws` nem `pgx`.

---

## D-16 — Ledger, paginação e reconciliação ⚙️ [LED-*, HTTP-03, HTTP-07]

- **Ledger de entrada simples, por escolha.** Saldo anterior e posterior mais a cadeia de versões bastam para auditoria e reconciliação. Partidas dobradas (diferencial opcional) exigiriam contas de contrapartida sem ganho para as garantias pedidas.
- **Versão no ledger:** cada lançamento grava `wallet_version`, que é a versão da carteira depois da mudança, com `UNIQUE (wallet_id, wallet_version)`. Isso forma uma cadeia verificável e dá ordem estável.
- **Paginação:** ordena por `wallet_version ASC`. O cursor é o base64url de `{"v":<últimaVersão>}` e é opaco para o cliente. O `limit` padrão é 50 e o máximo é 200. **Sem ajuste silencioso** (spec do M3): `limit` fora de 1–200 ou não inteiro, e `cursor` que não decodifica para `{"v":N}` com N ≥ 1, resultam em 400 `INVALID_FIELD` com `field` indicando o parâmetro. O `nextCursor` é omitido na última página.
- **Reconciliação:**
  - roda em uma transação `REPEATABLE READ READ ONLY`, um snapshot único;
  - compara `stored` com `Σ CREDIT − Σ DEBIT` (inclui `OPENING`);
  - calcula `difference = stored − calculated` e `checkedEntries = count`.
  - Se houver divergência: log `WARN` e incremento de `reconciliation_divergences_total`. Nada é alterado.

---

## D-17 — Proteções no banco ⚙️ [DB-03, LED-04]

- **Roles:** `pda_owner` executa as migrations e tem `CREATEDB`, para os testes criarem bancos isolados. `pda_app` é a role usada pela aplicação e **não** tem `UPDATE`/`DELETE`/`TRUNCATE` em `wallet_ledger_entries`.
- **Triggers:**
  - `BEFORE UPDATE OR DELETE` e `BEFORE TRUNCATE` no ledger, com `RAISE EXCEPTION`;
  - bloqueio de transição a partir de estado terminal em `wager_transactions`;
  - coerência ledger × carteira × transação: só entra lançamento de transação `PROCESSED` que não seja `LOSS`, com valor, moeda e direção coerentes; e, no commit, toda transação `PROCESSED` com movimento precisa ter o seu lançamento ([`data-model.md`](data-model.md) §4.2);
  - imutabilidade dos campos de snapshot da outbox.
- **`CHECK`s:**
  - saldo `>= 0`;
  - versão `>= 1`;
  - valor do ledger `> 0`;
  - `balance_after = balance_before ± amount` conforme a direção;
  - `origin = 'INTERNAL'` exige que os campos externos sejam `NULL` e `origin = 'EXTERNAL'` exige que sejam `NOT NULL`;
  - `LOSS` exige valor zero.
- **Índices únicos:**
  - `(player_id, currency)` em `wallets`;
  - `(wallet_id) WHERE kind = 'OPENING'`;
  - os índices de idempotência (D-08) e de reversão (D-10).

---

## D-18 — Observabilidade ⚙️ [OBS-*]

- **Logs:** slog JSON com `correlationId` (vem do header `X-Correlation-Id`, aceito só com até 128 caracteres `[A-Za-z0-9._-]`, ou é gerado; é propagado pelos eventos), `messageId`, `transactionId`, `walletId` e `providerId`. Nunca registram tokens, secrets nem o payload completo. **Ordem dos middlewares HTTP** (spec do M3): correlação → log de acesso → recuperação de `panic` → fallback de rota; assim o 500 de um `panic` tem `correlationId` e entra no log de acesso.
- **Métricas:** `/metrics` fica na porta administrativa `:9090`, separada da API e não exposta como rota de negócio. O catálogo de métricas está no [`ARCHITECTURE.md`](../ARCHITECTURE.md) §13.2. O `app` não importa Prometheus: ele declara a porta `app.Metrics`, e a `observability` a implementa. A porta ganhou no M7 `Reconciled`, `WagerConcluded`, `WagerDuplicate` e `Conflict`; o `httpapi` declara a sua própria (`HTTPRequest`, `AuthFailure`).
- **Readiness:** `/health/ready` faz ping no PostgreSQL e chama `GetQueueAttributes` na fila principal, com timeout de 2 s cada. **Sempre os dois**, qualquer que seja o conjunto de papéis (HTTP-08 fixa o contrato; ambos os clientes continuam no grafo).
- **Delta do M7:**
  - uma linha `wager concluded` por conclusão no `ProcessWager` (HTTP, SQS e replays), com `transactionId`, `walletId`, `providerId`, `correlationId`, `channel`, `kind`, `outcome`, `failureCode`, `replay` e, no SQS, `messageId`; nunca valores, chaves de idempotência nem corpos. O worker de referências mantém as suas linhas próprias;
  - o WARN da reconciliação não registra mais saldos (só `walletId`, `correlationId` e `entries`);
  - `concurrency_conflicts_total{reason}` tem `lock_timeout` (`55P03` e deadlock `40P01`, via `app.ErrLockTimeout`) e `unique_race`; a label `version_mismatch` saiu, porque a estratégia é pessimista e nenhum caminho a produz;
  - `wager_transactions_total` e `wager_processing_duration_seconds` ganham `channel` = `http`, `sqs` ou `worker`; as duplicatas do SQS continuam contadas pelo consumidor, e o `app` só conta as do HTTP;
  - `http_requests_total` e `http_request_duration_seconds` usam o **padrão** da rota (`POST /wallets/{walletId}/reconciliation`) e `unmatched` para o que não casa; o log de acesso grava o mesmo `route`;
  - `auth_failures_total{reason}`: `unauthenticated`, `forbidden` e `provider_mismatch`.

---

## D-19 — Estratégia de testes 🗳️ [TST-*]

| Nível | Comando | Infraestrutura |
| --- | --- | --- |
| Unitário | `go test ./...` e `go test -race ./...` | Nenhuma. Roda em um checkout limpo sem Docker |
| Integração | `go test -tags=integration -race ./...` | `make infra-up` (postgres, keycloak, ministack, aws-init, migrate); cada pacote cria banco e filas isolados, e a aplicação roda em processo via Fx |
| E2E / multi-instância | `go test -tags=e2e -race -p 1 -timeout 15m ./test/e2e/...` | A infraestrutura do compose, com 3 processos do binário iniciados pelo próprio teste (banco e filas isolados) |

- **Injeção de falhas:** o pacote `faultinject` só funciona quando o binário é compilado com `-tags faultinject`; no build normal ele não faz nada. Os pontos de falha são habilitados por env, por exemplo `PDA_FAULT=consumer.after_commit_before_delete`, e causam `os.Exit(137)` para simular uma interrupção abrupta. A lista completa de pontos está em `test-plan.md` §4.
- **Duplicidade no SQS:** os testes enviam reentregas com `MessageDeduplicationId` diferentes, ou após a janela de deduplicação. Assim a deduplicação exercitada é a da aplicação (inbox e idempotência), e não a do FIFO (TST-C11).
- **Invariante final em todos os cenários:** `stored == Σ créditos − Σ débitos` (TST-C09).

---

## D-20 — Documentação da API: OpenAPI + Swagger UI 🗳️ [HTTP-*, DOC-01, DOC-06]

- **Contrato:** `api/openapi.yaml` em **OpenAPI 3.0.3**, escrito à mão (*design-first*). É o artefato central da spec do M3 e a **fonte única** do contrato HTTP:
  - as 9 rotas de negócio + health + docs;
  - schemas `Money`, `Wallet`, `TransactionResult`, `LedgerPage`, `Reconciliation` e `Problem`, com o catálogo de `code` como `enum`;
  - headers `Idempotency-Key`, `X-Correlation-Id`, `Retry-After` e `WWW-Authenticate`;
  - exemplos para cada situação de D-04.
- **Por que 3.0.3:** tem a maior compatibilidade com kin-openapi, Swagger UI, Postman, Insomnia e Bruno, e o contrato não precisa de nenhum recurso da 3.1.
- **Segurança no contrato:**
  - `keycloak`: OAuth2 `clientCredentials` com `tokenUrl = http://localhost:8080/realms/pda/protocol/openid-connect/token`;
  - `bearerAuth`: JWT colado manualmente.
  - Cada operação declara a role exigida em `x-required-role` e na descrição, seguindo a matriz de D-07. As rotas públicas têm `security: []`.
- **Servidores:** `http://localhost:8081`, `:8082` e `:8083` (`app-1..3`). O avaliador escolhe a instância no próprio Swagger UI, o que também serve para demonstrar as várias instâncias.
- **Exposição:**
  - `GET /openapi.yaml` serve o arquivo embutido no binário (`embed`), sempre a versão da build;
  - `GET /docs` serve uma página HTML embutida que carrega o `swagger-ui-dist` **5.33.0** pelo CDN jsDelivr, com versão fixada. Por ser a mesma origem da API, não há CORS na API;
  - as duas rotas são públicas, como o health, e podem ser desligadas com `API_DOCS_ENABLED=false` (padrão `true`).
- **Keycloak:** os clients de teste declaram `webOrigins` com `http://localhost:8081-8083`, para que o botão *Authorize* do Swagger UI obtenha o token direto do navegador. Os segredos são os valores locais do `.env.example`.
- **Coleção:** `api/requests.http` (REST Client do VS Code / HTTP Client do JetBrains) com o fluxo completo: token → abrir carteira → BET → replay → rejeição → reconciliação.
- **Anti-divergência:** `getkin/kin-openapi` v0.149.0, **só nos testes**. O cliente HTTP do `testkit` valida cada requisição e cada resposta de todos os testes de integração contra o `api/openapi.yaml` (`openapi3filter`), então uma resposta fora do contrato quebra o teste. As únicas exceções são as requisições que o teste marca como deliberadamente inválidas (I12, 415, limite de corpo) e as rotas fora do documento (404/405 de rota): nelas só a resposta é validada. O I15 é unitário e verifica o documento e a paridade entre as rotas do OpenAPI e a tabela de rotas do `httpapi`, que é a mesma que registra no `ServeMux`.
- **Limitação:** o `/docs` precisa de internet no navegador por causa do CDN. Sem internet, basta importar o `/openapi.yaml` numa ferramenta de API ou usar o `api/requests.http`.
