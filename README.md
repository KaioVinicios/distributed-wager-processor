# pda: Processamento Distribuído de Apostas

Serviço em Go que movimenta carteiras de jogadores a partir de operações de provedores de jogos (`BET`, `WIN`, `LOSS`, `REFUND` e `ROLLBACK`), recebidas por **HTTP** e por **SQS**, com as mesmas garantias nos dois canais: dinheiro sem ponto flutuante, ledger append-only e auditável, idempotência persistente, coordenação por carteira entre várias instâncias, inbox e outbox transacionais e recuperação de falhas.

- **Stack:** Go 1.27.1, Uber Fx, `net/http`, PostgreSQL 18 (`pgx/v5`, SQL explícito), Keycloak 26 (OIDC, `client_credentials`), SQS e SNS FIFO no MiniStack, `golang-migrate`, `log/slog` e Prometheus.
- **Execução:** `docker compose up --build` sobe toda a infraestrutura e **3 réplicas** independentes do serviço.
- **Primeira vez aqui?** O [`docs/getting-started.md`](docs/getting-started.md) explica o projeto em linguagem simples e traz um roteiro de validação com os resultados esperados.
- **Decisões técnicas:** [`ARCHITECTURE.md`](ARCHITECTURE.md). **Testes:** [`docs/testing.md`](docs/testing.md). **Enunciado:** [`CHALLENGE.md`](CHALLENGE.md).

---

## Sumário

1. [Pré-requisitos](#1-pré-requisitos)
2. [Início rápido](#2-início-rápido)
3. [Serviços e portas](#3-serviços-e-portas)
4. [Variáveis de ambiente](#4-variáveis-de-ambiente)
5. [Filas, tópico e credenciais do broker](#5-filas-tópico-e-credenciais-do-broker)
6. [Migrations](#6-migrations)
7. [Autenticação e identidades de teste](#7-autenticação-e-identidades-de-teste)
8. [Exemplos de chamadas](#8-exemplos-de-chamadas)
9. [Testes](#9-testes)
10. [Operação e problemas comuns](#10-operação-e-problemas-comuns)
11. [Onde está cada entregável](#11-onde-está-cada-entregável)

---

## 1. Pré-requisitos

| Ferramenta | Versão | Para quê |
| --- | --- | --- |
| Docker Engine + Docker Compose v2 | Compose **2.24 ou mais novo** (o `env_file` usa `required: false`) | Toda a execução. A imagem da aplicação é compilada no próprio build |
| Go | **1.27.1** (a do `go.mod`) | Só para rodar os testes fora do Docker |
| `make`, `curl` | Qualquer | Atalhos do `Makefile` e exemplos |
| `jq`, `uuidgen` | Qualquer | Só nos exemplos da §8 |

- **Portas livres no host:** `5432` (PostgreSQL), `8080` (Keycloak), `4566` (MiniStack), `8081`–`8083` (API das réplicas), `9091`–`9093` (métricas), `9090` (Prometheus) e `3000` (Grafana).
- **Memória:** o conjunto usa cerca de 2 GB depois de subir e rodar uma carga (Keycloak ~1 GB e Grafana ~650 MB são os maiores). Reserve 3 GB para o Docker.
- **Nada mais precisa ser instalado:** o lint roda pela imagem do `golangci-lint`, as migrations pela imagem do `migrate` e a AWS CLI pela imagem `amazon/aws-cli`.

---

## 2. Início rápido

```sh
git clone https://github.com/KaioVinicios/pda.git
cd pda
docker compose up --build            # em primeiro plano (Ctrl+C para parar); o mesmo que `make up`
# ou, em segundo plano, esperando tudo ficar saudável:
docker compose up --build --wait
```

Não há passo manual: o `.env.example` já traz valores locais, e o compose provisiona tudo na subida, nesta ordem:

1. `postgres` cria as roles `pda_owner` e `pda_app` (`deploy/postgres/01-roles.sh`), e o `migrate` aplica as migrations como `pda_owner`;
2. `ministack` sobe com `AUTH=true`, e o `aws-init` cria filas, tópico, assinatura, usuários IAM e o arquivo `.local/aws/credentials`;
3. `keycloak` importa os realms `pda` e `other` com clients, roles e mappers;
4. `app-1`, `app-2` e `app-3` sobem depois que tudo acima está pronto (healthcheck pelo próprio binário, `pda healthcheck`).

A primeira subida leva alguns minutos (download das imagens, compilação e cerca de 30–60 s do Keycloak). Com as imagens em cache, o `up --build --wait` leva menos de 1 min.

```sh
curl -s localhost:8081/health/ready
# {"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}
```

- **Swagger UI:** <http://localhost:8081/docs> (obtém o token no Keycloak e chama qualquer réplica). O contrato fica em <http://localhost:8081/openapi.yaml>.
- **Parar:** `docker compose down` mantém os dados; `docker compose down -v` (ou `make down`) apaga também o volume do PostgreSQL. O MiniStack guarda filas e chaves só em memória: numa nova subida, o `aws-init` recria tudo.

---

## 3. Serviços e portas

| Serviço | Endereço no host | Papel |
| --- | --- | --- |
| `app-1`, `app-2`, `app-3` | API em `localhost:8081`, `8082` e `8083`; métricas em `localhost:9091`, `9092` e `9093` (`/metrics`) | O mesmo binário, cada réplica com seu pool e sua memória. Todas rodam os 4 papéis: API HTTP, consumidor SQS, publisher da outbox e worker de referências. `restart: on-failure` |
| `postgres` | `localhost:5432` (banco `pda`) | Fonte da verdade: carteiras, transações, ledger, inbox e outbox |
| `keycloak` | `localhost:8080` (console: `admin` / `admin-local`) | IdP OIDC; emite tokens `client_credentials` |
| `ministack` | `localhost:4566` | SQS e SNS FIFO, com as políticas IAM avaliadas (`AUTH=true`) |
| `prometheus` | <http://localhost:9090> | Coleta o `/metrics` das 3 réplicas a cada 5 s (D-22) |
| `grafana` | <http://localhost:3000> (anônimo, só leitura; `admin` / `admin` para editar) | Dashboard `PDA — visão geral`, provisionado de `deploy/grafana/` ([`ARCHITECTURE.md`](ARCHITECTURE.md) §13.3) |
| `migrate`, `aws-init` | — | Tarefas únicas da subida (migrations; filas, tópico e IAM) |
| `k6` (profile `load`) | — | Gerador do teste de carga. Só roda com `make load-test`, nunca no `docker compose up` ([`docs/load-test.md`](docs/load-test.md)) |

Rotas da API (contrato completo em [`api/openapi.yaml`](api/openapi.yaml)):

| Rota | Quem pode chamar |
| --- | --- |
| `GET /health/live`, `GET /health/ready`, `GET /docs`, `GET /openapi.yaml` | Público |
| `POST /wallets`, `GET /wallets/{walletId}`, `GET /wallets/{walletId}/ledger`, `POST /wallets/{walletId}/reconciliation` | Serviço interno (`wallet-internal`) |
| `POST /wagering/transactions` | Provedor (`provider`), com o `providerId` do corpo igual ao do token |
| `GET /wagering/transactions/{transactionId}` | Provedor (só as próprias) e serviço interno |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | Provedor (o próprio `providerId`) e serviço interno |

---

## 4. Variáveis de ambiente

### 4.1 Arquivos

- **[`.env.example`](.env.example)** (versionado): valores **locais** de exemplo, sem segredos reais. É carregado por todos os serviços do compose (`env_file`) e pelos testes.
- **`.env`** (opcional, ignorado pelo git): sobrescreve o `.env.example` sem editá-lo. Por exemplo, `LOG_LEVEL=debug` num `.env` vale para as 3 réplicas no próximo `docker compose up`.
- As **chaves AWS não ficam em nenhum dos dois:** o `aws-init` as gera e grava em `.local/aws/credentials` (§5).

### 4.2 Infraestrutura (`.env.example`)

| Variável | Valor local | Uso |
| --- | --- | --- |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | `postgres`, `postgres-local`, `pda` | Superusuário e banco do container |
| `PDA_OWNER_PASSWORD`, `PDA_APP_PASSWORD` | `pda-owner-local`, `pda-app-local` | Senhas das roles `pda_owner` (migrations, DDL) e `pda_app` (aplicação, sem DDL nem `UPDATE`/`DELETE` no ledger) |
| `DATABASE_OWNER_URL` | `postgres://pda_owner:…@postgres:5432/pda` | URL usada pelo serviço `migrate` |
| `KC_BOOTSTRAP_ADMIN_USERNAME`, `KC_BOOTSTRAP_ADMIN_PASSWORD` | `admin`, `admin-local` | Console do Keycloak |
| `PROVIDER_A_SECRET`, `PROVIDER_B_SECRET`, `WALLET_SERVICE_SECRET`, `NO_ROLE_CLIENT_SECRET`, `NO_AUDIENCE_CLIENT_SECRET`, `PROVIDER_SHORT_LIVED_SECRET`, `OTHER_PROVIDER_SECRET` | `<client>-local-secret` | Secrets dos clients importados no Keycloak (§7) |
| `SQS_EVENTS_AUDIT_QUEUE_NAME`, `SQS_MAX_RECEIVE_COUNT` | `wallet-events-audit.fifo`, `10` | Fila de auditoria e `maxReceiveCount` da redrive. Não estão no `.env.example`: são padrões do `aws-init`, que um `.env` pode sobrescrever |

### 4.3 Aplicação

A configuração é lida só do ambiente e **validada no start**. Uma variável inválida impede a subida com uma mensagem que nomeia a variável, sem ecoar o valor, e o processo sai com código 1. Os valores de produção abaixo são os padrões; o compose só define os endereços, o `LOG_LEVEL` e as credenciais.

**Processo e HTTP**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` ou `error`. Em `debug`, os eventos internos do Fx também aparecem |
| `HTTP_ADDR` | `:8080` | Endereço da API |
| `METRICS_ADDR` | `:9090` | Servidor admin (`/metrics`); precisa diferir do `HTTP_ADDR` |
| `SHUTDOWN_TIMEOUT` | `20s` | Prazo do shutdown gracioso de cada componente; menor que 30 s (o `StopTimeout` do Fx) |
| `HTTP_REQUEST_TIMEOUT` | `10s` | Prazo de cada requisição autenticada; vencido, responde 503 com `Retry-After`. Regra: `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30 s` |
| `API_DOCS_ENABLED` | `true` | Serve `/docs` (Swagger UI) e `/openapi.yaml` |
| `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED`, `REFERENCE_WORKER_ENABLED` | `true` | Papéis da instância. Um papel desligado sai do grafo do Fx. O `/health/ready` vive na API: sem `HTTP_ENABLED`, o healthcheck do container falha |

**PostgreSQL**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `DATABASE_URL` | — (obrigatória) | URL `postgres://` da role `pda_app` |
| `DB_MAX_CONNS` | `10` | Tamanho máximo do pool por instância |
| `DB_LOCK_TIMEOUT` | `5s` | `lock_timeout` de cada transação de escrita; esgotado, a operação é transitória (503 ou retry no SQS) |

**OIDC (Keycloak)**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `OIDC_ISSUER` | — (obrigatória) | `iss` esperado nos tokens (`http://localhost:8080/realms/pda`, o endereço que os clientes usam) |
| `OIDC_JWKS_URL` | — (obrigatória) | Onde a aplicação busca as chaves (`http://keycloak:8080/…/certs`, pela rede do compose). É separada do issuer de propósito, sem discovery |
| `OIDC_AUDIENCE` | `pda-api` | `aud` exigido |
| `OIDC_CLOCK_SKEW` | `30s` | Tolerância do `exp` |

**AWS (MiniStack)**, lidas pelo próprio SDK

| Variável | Valor no compose | Descrição |
| --- | --- | --- |
| `AWS_REGION` | `us-east-1` | Obrigatória |
| `AWS_ENDPOINT_URL` | `http://ministack:4566` | Endpoint do emulador (sem ela, o SDK usaria a AWS real) |
| `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE` | `/aws/credentials`, `pda-wallet-service` | O arquivo gerado pelo `aws-init`, montado só para leitura, e o usuário IAM da aplicação |

**Consumidor SQS**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `SQS_WAGER_QUEUE_NAME`, `SQS_WAGER_DLQ_NAME` | `wager-transactions.fifo`, `wager-transactions-dlq.fifo` | Filas de entrada e DLQ (sufixo `.fifo` obrigatório, nomes diferentes) |
| `SQS_CONSUMER_POLLERS` | `2` | Pollers de long polling por instância |
| `SQS_RECEIVE_BATCH` | `10` | Mensagens por `ReceiveMessage` (1–10) |
| `SQS_WAIT_TIME` | `20s` | Long polling (segundos inteiros, 0–20 s) |
| `SQS_VISIBILITY_TIMEOUT` | `30s` | Visibility de cada recebimento (segundos inteiros) |
| `SQS_PROCESSING_TIMEOUT` | `10s` | Prazo por mensagem; precisa ser **menor** que o visibility |
| `SQS_MAX_IN_FLIGHT` | `16` | Mensagens em processamento simultâneo por instância |
| `SQS_RETRY_MAX_DELAY` | `300s` | Teto do backoff de falha transitória, `min(2^recebimentos s, teto)` |

**Publisher da outbox (SNS)**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `SNS_EVENTS_TOPIC_NAME` | `wallet-events.fifo` | Tópico de saída; verificado no start |
| `OUTBOX_BATCH_SIZE` | `50` | Eventos por reserva (1–1000) |
| `OUTBOX_LEASE` | `30s` | Lease de uma reserva; vencido, outra instância reassume |
| `OUTBOX_POLL_INTERVAL` | `500ms` | Intervalo entre buscas sem trabalho |
| `OUTBOX_CONCURRENCY` | `8` | Grupos (carteiras) publicados em paralelo |
| `OUTBOX_RETRY_BASE_DELAY`, `OUTBOX_RETRY_MAX_DELAY` | `1s`, `5m` | Backoff exponencial de uma publicação que falhou |

**Worker de referências**

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `REFERENCE_RETRY_BASE_DELAY`, `REFERENCE_RETRY_MAX_DELAY` | `1s`, `60s` | Backoff exponencial (±20% de jitter) de uma operação em `PENDING_REFERENCE` |
| `REFERENCE_MAX_ATTEMPTS` | `8` | Tentativas antes de `REJECTED` com `REFERENCE_NOT_FOUND` |
| `REFERENCE_TTL` | `10m` | Espera máxima em tempo de relógio (o que vier primeiro) |
| `REFERENCE_POLL_INTERVAL` | `500ms` | Intervalo entre buscas de pendências vencidas |
| `REFERENCE_BATCH_SIZE` | `50` | Pendências por busca |

Os parâmetros de mensageria estão explicados em [`docs/messaging.md`](docs/messaging.md) §4–§5, e os tempos usados nos testes, em [`docs/test-plan.md`](docs/test-plan.md) §3.3.

---

## 5. Filas, tópico e credenciais do broker

A inicialização é **automática e idempotente**: o serviço `aws-init` roda [`deploy/aws/init.sh`](deploy/aws/init.sh) com a chave raiz do emulador (`test`) a cada `docker compose up`, e as réplicas só sobem depois que ele termina.

| Recurso | Tipo | Configuração |
| --- | --- | --- |
| `wager-transactions.fifo` | SQS FIFO (entrada) | `VisibilityTimeout=30`, `ReceiveMessageWaitTimeSeconds=20`, retenção de 4 dias, **redrive para a DLQ após 10 recebimentos** |
| `wager-transactions-dlq.fifo` | SQS FIFO (DLQ) | Retenção de 14 dias |
| `wallet-events.fifo` | SNS FIFO (saída) | Destino dos eventos da outbox |
| `wallet-events-audit.fifo` | SQS FIFO | Assina o tópico (`RawMessageDelivery=true`); consumidor de referência dos eventos |

**Credenciais e políticas.** O MiniStack roda com `AUTH=true` e avalia as políticas IAM. O `aws-init` cria um usuário por principal, com a política de identidade de [`deploy/aws/policies/`](deploy/aws/policies/), e grava as chaves em `.local/aws/credentials` (um profile por usuário):

| Profile | Pode |
| --- | --- |
| `pda-wallet-service` (a aplicação) | Consumir a fila de entrada, enviar para a DLQ e publicar no tópico |
| `provider-a`, `provider-b` | Só enviar para a fila de entrada |

- Reexecutar o provisionamento: `docker compose up aws-init`. As chaves ainda válidas são reaproveitadas.
- **Se o MiniStack for recriado**, as chaves antigas deixam de existir, e o `aws-init` gera outras. As réplicas leem o arquivo só no start, então é preciso reiniciá-las: `docker compose restart app-1 app-2 app-3`.

**Inspecionar as filas.** A AWS CLI roda pela imagem, na rede do compose. Com a chave raiz do emulador:

```sh
aws_root() {
  docker run --rm --network pda_default -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
    -e AWS_REGION=us-east-1 amazon/aws-cli:2.36.31 --endpoint-url http://ministack:4566 "$@"
}
aws_root sqs list-queues
aws_root sqs get-queue-attributes --attribute-names All \
  --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo
```

**DLQ e reprocessamento.** Chegam à DLQ as mensagens inválidas e as falhas permanentes, por envio explícito com os atributos `errorCode`, `errorCategory`, `originalMessageId`, `consumerName` e `failedAt`, e as falhas transitórias depois de 10 recebimentos, pela redrive. Os códigos estão em [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §5.4.
- **Na AWS**, o reprocessamento é `aws sqs start-message-move-task --source-arn <arn da DLQ>`.
- **O MiniStack 1.5.18 não implementa essa ação** (responde `InvalidAction`). Localmente:
  1. leia a mensagem e seus atributos: `aws_root sqs receive-message --queue-url …/wager-transactions-dlq.fifo --attribute-names All --message-attribute-names All`;
  2. corrija a causa (uma mensagem inválida precisa ser corrigida pelo produtor);
  3. peça ao produtor que **reenvie a mesma mensagem, com o mesmo `messageId`** e um token novo no atributo `accessToken` (a cópia na DLQ não traz o token, D-23).
- O reenvio é seguro: a inbox e a idempotência absorvem qualquer repetição, e o token fica fora do hash da mensagem.

O contrato da mensagem de entrada (`WagerTransactionRequested`), o `MessageGroupId` (`walletId`) e o `MessageDeduplicationId` (`messageId`) estão em [`docs/messaging.md`](docs/messaging.md) §3.

---

## 6. Migrations

As migrations ficam em [`migrations/`](migrations/) (formato `golang-migrate`, um par `up`/`down` por versão):

| Versão | Conteúdo |
| --- | --- |
| `000001` | `wallets` e constraints |
| `000002` | `wager_transactions`, constraints e índices de idempotência |
| `000003` | `wallet_ledger_entries` e constraints |
| `000004` | `inbox_messages` e `outbox_events` |
| `000005` | Triggers de proteção (ledger append-only, coerência ledger × carteira, transação terminal imutável, snapshot da outbox) |
| `000006` | `GRANT`s da role `pda_app` |

**Aplicação.** É automática: o serviço `migrate` do compose aplica as pendentes como `pda_owner` antes de as réplicas subirem. A aplicação nunca executa DDL.

```sh
make migrate-up                              # aplica todas as pendentes
make migrate-down                            # reverte a última (N=1)
make migrate-down N=3                        # reverte as 3 últimas
docker compose run --rm migrate version      # versão atual
docker compose run --rm migrate down -all    # reverte tudo
```

Os alvos do `make` usam o serviço `migrate` (imagem `migrate/migrate:v4.20.1`), sem instalação local. Com o CLI `migrate` instalado no host, use o endereço publicado do PostgreSQL:

```sh
migrate -path migrations -database "postgres://pda_owner:pda-owner-local@localhost:5432/pda?sslmode=disable" up
migrate -path migrations -database "postgres://pda_owner:pda-owner-local@localhost:5432/pda?sslmode=disable" down 1
```

- **Reverter apaga dados.** Os `down` removem tabelas, inclusive o ledger. Pare as réplicas antes de reverter além da `000006` (`docker compose stop app-1 app-2 app-3`).
- **Cobertura:** o teste `TestMigrationsUpDownUp` aplica `up`, `down -all` e `up` e compara o schema. Detalhes em [`docs/data-model.md`](docs/data-model.md) §7.

---

## 7. Autenticação e identidades de teste

O Keycloak importa, na subida, os realms de [`deploy/keycloak/`](deploy/keycloak/) com clients, roles e *protocol mappers*: não há cadastro manual. Todas as identidades são clients `client_credentials`, com os secrets locais do `.env.example`.

| Client (realm) | Role em `pda-api` | Claim `provider_id` | Uso |
| --- | --- | --- | --- |
| `provider-a` (`pda`) | `provider` | `provider-a` | Provedor: envia e consulta as próprias transações |
| `provider-b` (`pda`) | `provider` | `provider-b` | Segundo provedor, para o isolamento entre provedores |
| `wallet-service` (`pda`) | `wallet-internal` | — | Serviço interno: carteiras, ledger e reconciliação |
| `no-role-client` (`pda`) | — | — | Caso negativo: token válido sem role (403) |
| `no-audience-client` (`pda`) | `provider` | `provider-a` | Caso negativo: token sem `aud=pda-api` (401) |
| `provider-short-lived` (`pda`) | `provider` | `provider-a` | Token com 5 s de vida, para o teste de expiração |
| `other-provider` (`other`) | `provider` | `provider-a` | Caso negativo: outro realm, com outro `iss` e outras chaves (401) |

- **Validação:** a aplicação verifica a assinatura RS256 pelo JWKS, o `iss`, o `aud` e o `exp`. **O provedor vem da claim `provider_id` do token, nunca do corpo.** O modelo de permissões está em [`ARCHITECTURE.md`](ARCHITECTURE.md) §10.
- **Vida do token:** os tokens do realm `pda` valem 5 min. Depois disso, peça outro.

```sh
scripts/get-token.sh provider-a          # imprime o access token (lê o secret do .env.example/.env)
scripts/get-token.sh wallet-service
scripts/get-token.sh other-provider other

# o mesmo, só com curl:
curl -s -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-local-secret \
  http://localhost:8080/realms/pda/protocol/openid-connect/token | jq -r .access_token
```

Peça os tokens em `http://localhost:8080`, o endereço público que o `OIDC_ISSUER` espera no `iss`.

---

## 8. Exemplos de chamadas

Os comandos abaixo foram executados contra o compose, e as respostas estão resumidas. Cada chamada usa uma réplica diferente, para mostrar que o resultado não depende da instância. Há dois caminhos alternativos com o mesmo fluxo: a coleção [`api/requests.http`](api/requests.http) (REST Client do VS Code ou HTTP Client do JetBrains) e o Swagger UI em <http://localhost:8081/docs>.

### 8.1 Tokens e variáveis

```sh
PROVIDER_TOKEN=$(scripts/get-token.sh provider-a)
INTERNAL_TOKEN=$(scripts/get-token.sh wallet-service)
PLAYER=$(uuidgen | tr 'A-Z' 'a-z')   # a abertura é única por (jogador, moeda)
R=$(date +%s)                         # sufixo dos ids externos, para repetir o roteiro
```

### 8.2 Abrir uma carteira (serviço interno)

```sh
WALLET=$(curl -s -X POST localhost:8081/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" \
  | tee /dev/stderr | jq -r .id)
# 201 {"id":"01a0f436-…","playerId":"eda59cd1-…","balance":{"amount":"100.00","currency":"BRL"},"version":1,…}
```

Com saldo positivo, a abertura grava na mesma transação a carteira (versão 1), a transação `OPENING`, o crédito no ledger e os eventos na outbox. Repetir a abertura para o mesmo jogador e moeda responde 409 `WALLET_ALREADY_EXISTS`.

### 8.3 Apostar (provedor)

```sh
curl -s -w '  HTTP %{http_code}\n' -X POST localhost:8081/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:bet-1-$R" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"bet-1-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"}}"
# {"transactionId":"01a0f436-a4ee-…","status":"PROCESSED","balance":{"amount":"20.00","currency":"BRL"},"idempotentReplay":false}  HTTP 200
```

Para as próximas operações, uma função monta o mesmo corpo:

```sh
# wager <porta> <kind> <id externo> <valor> [id externo da referência]
wager() {
  local ref=${5:+,\"referenceExternalTransactionId\":\"$5-$R\"}
  curl -s -w '  HTTP %{http_code}\n' -X POST "localhost:$1/wagering/transactions" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: provider-a:$3-$R" \
    -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$3-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"$2\",\"money\":{\"amount\":\"$4\",\"currency\":\"BRL\"}$ref}"
}
```

### 8.4 Replay, rejeição e conflito

```sh
wager 8082 BET bet-1 80.00    # mesma chave e mesmo conteúdo, em outra réplica
# {"transactionId":"01a0f436-a4ee-…","status":"PROCESSED","balance":{"amount":"20.00",…},"idempotentReplay":true}  HTTP 200

wager 8083 BET bet-2 80.00    # saldo insuficiente
# {"transactionId":"…","status":"REJECTED","balance":{"amount":"20.00",…},"failureCode":"INSUFFICIENT_FUNDS","failureCategory":"DEFINITIVE","idempotentReplay":false}  HTTP 422

wager 8081 BET bet-1 1.00     # mesma chave, outro conteúdo
# {"type":"about:blank","title":"Conflict","status":409,"code":"IDEMPOTENCY_KEY_REUSED","category":"CORRECTABLE",…}  HTTP 409
```

O replay devolve o resultado **persistido**, inclusive o saldo observado no processamento original, mesmo que a carteira tenha mudado depois. Rejeições de negócio também são persistidas e replayáveis (422).

### 8.5 Reversão antes da referência

```sh
wager 8082 REFUND refund-3 10.00 bet-3    # a BET referenciada ainda não chegou
# {"transactionId":"01a0f436-a531-…","status":"PENDING_REFERENCE","idempotentReplay":false}  HTTP 202

wager 8083 BET bet-3 10.00
# {"transactionId":"01a0f436-a541-…","status":"PROCESSED","balance":{"amount":"10.00",…},…}  HTTP 200

sleep 1   # o worker conclui a pendência em até ~0,5 s (REFERENCE_POLL_INTERVAL)
curl -s localhost:8081/providers/provider-a/wagering/transactions/refund-3-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq '{kind, status, referenceTransactionId, balance}'
# {"kind":"REFUND","status":"PROCESSED","referenceTransactionId":"01a0f436-a541-…","balance":{"amount":"20.00",…}}
```

A chegada da BET antecipa a pendência, e o worker de referências de qualquer réplica a conclui, de forma assíncrona: uma consulta logo depois da BET ainda pode mostrar `PENDING_REFERENCE`, e o saldo e a versão da §8.6 contam com o REFUND concluído. Sem a referência, a pendência expira em `REJECTED` com `REFERENCE_NOT_FOUND`, depois de 8 tentativas ou 10 min. A consulta pelo id interno é `GET /wagering/transactions/{transactionId}`.

### 8.6 Carteira, ledger e reconciliação (serviço interno)

```sh
curl -s localhost:8082/wallets/$WALLET -H "Authorization: Bearer $INTERNAL_TOKEN"
# {"id":"01a0f436-…","balance":{"amount":"20.00","currency":"BRL"},"version":4,…}

curl -s "localhost:8083/wallets/$WALLET/ledger?limit=2" -H "Authorization: Bearer $INTERNAL_TOKEN" \
  | jq -c '{entries: [.items[] | {direction, amount: .amount.amount, balanceAfter: .balanceAfter.amount, walletVersion}], nextCursor}'
# {"entries":[{"direction":"CREDIT","amount":"100.00","balanceAfter":"100.00","walletVersion":1},
#             {"direction":"DEBIT","amount":"80.00","balanceAfter":"20.00","walletVersion":2}],"nextCursor":"eyJ2IjoyfQ"}
# próxima página: …/ledger?limit=2&cursor=eyJ2IjoyfQ

curl -s -X POST localhost:8081/wallets/$WALLET/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
# {"walletId":"01a0f436-…","storedBalance":{"amount":"20.00",…},"calculatedBalance":{"amount":"20.00",…},
#  "difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"checkedEntries":4}
```

A reconciliação reconstrói o saldo a partir do ledger num snapshot consistente e nunca altera o saldo. Uma divergência aparece na resposta, no log e na métrica `reconciliation_divergences_total`.

### 8.7 Acessos negados

```sh
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wallets/$WALLET                    # 401 UNAUTHENTICATED
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wallets/$WALLET \
  -H "Authorization: Bearer $PROVIDER_TOKEN"                                                # 403 FORBIDDEN
TX=$(curl -s localhost:8081/providers/provider-a/wagering/transactions/bet-1-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -r .transactionId)
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wagering/transactions/$TX \
  -H "Authorization: Bearer $(scripts/get-token.sh provider-b)"                             # 404 TRANSACTION_NOT_FOUND
```

Um provedor nunca vê a transação de outro: o 404 por id não revela se ela existe. Um `providerId` do corpo ou do path diferente do token responde 403. Pelo SQS, a mesma regra vale com o token da mensagem (§8.8).

### 8.8 A mesma operação pelo SQS

O provedor envia com as **suas** credenciais IAM, de `.local/aws/credentials`, e com o **seu** token do Keycloak no atributo `accessToken`: o broker decide quem pode enviar, e o token diz quem é o provedor (D-23).

```sh
aws_as() {   # aws_as <profile> <args…>: AWS CLI da imagem, na rede do compose, como um usuário IAM
  local profile=$1; shift
  docker run --rm --network pda_default -v "$PWD/.local/aws:/aws:ro" \
    -e AWS_SHARED_CREDENTIALS_FILE=/aws/credentials -e AWS_PROFILE="$profile" -e AWS_REGION=us-east-1 \
    amazon/aws-cli:2.36.31 --endpoint-url http://ministack:4566 "$@"
}
QUEUE=http://ministack:4566/000000000000/wager-transactions.fifo

aws_as provider-a sqs send-message --queue-url $QUEUE \
  --message-group-id "$WALLET" --message-deduplication-id "msg-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$(scripts/get-token.sh provider-a)\"}}" \
  --message-body "{\"messageId\":\"msg-$R\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"sqs-1-$R\",\"idempotencyKey\":\"provider-a:sqs-1-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"WIN\",\"money\":{\"amount\":\"5.00\",\"currency\":\"BRL\"}}}"

curl -s localhost:8082/providers/provider-a/wagering/transactions/sqs-1-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq '{kind, status, receivedVia, balance}'
# {"kind":"WIN","status":"PROCESSED","receivedVia":"SQS","balance":{"amount":"25.00","currency":"BRL"}}

aws_as provider-a sqs receive-message --queue-url $QUEUE     # o provedor não pode consumir
# … AccessDeniedException … not authorized to perform: sqs:ReceiveMessage …

# o provider-b, com a própria chave e o próprio token, tenta estornar a BET do provider-a em nome dele
aws_as provider-b sqs send-message --queue-url $QUEUE \
  --message-group-id "$WALLET" --message-deduplication-id "spoof-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$(scripts/get-token.sh provider-b)\"}}" \
  --message-body "{\"messageId\":\"spoof-$R\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"spoof-$R\",\"idempotencyKey\":\"provider-a:spoof-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"REFUND\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"bet-1-$R\"}}"
aws_root sqs receive-message --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo \
  --message-attribute-names All --wait-time-seconds 5 --query 'Messages[].MessageAttributes.errorCode.StringValue'
# [ "PROVIDER_MISMATCH" ]
```

O SQS passa pelo mesmo caso de uso do HTTP. Reenviar a mesma operação por qualquer canal cai no replay, e o mesmo `messageId` é deduplicado pela inbox. Uma mensagem sem token, com um token inválido ou que nomeia outro provedor vai para a DLQ (`UNAUTHENTICATED`, `FORBIDDEN` ou `PROVIDER_MISMATCH`) sem nenhum efeito, e o saldo não muda.

### 8.9 Eventos e métricas

```sh
# eventos publicados pela outbox no SNS, lidos da fila de auditoria (aws_root está na §5)
aws_root sqs receive-message --queue-url http://ministack:4566/000000000000/wallet-events-audit.fifo \
  --max-number-of-messages 10 --query 'Messages[].Body' --output json \
  | jq -r '.[] | fromjson | [.eventType, .data.walletId, .eventId] | @tsv'
# WagerTransactionProcessed  01a0f436-…  …
# WalletBalanceChanged       01a0f436-…  …

for p in 9091 9092 9093; do curl -s localhost:$p/metrics | grep '^wager_transactions_total'; done
# wager_transactions_total{channel="http",failure_code="",kind="BET",outcome="processed"} 1
# wager_transactions_total{channel="worker",failure_code="",kind="REFUND",outcome="processed"} 1
# wager_transactions_total{channel="sqs",failure_code="",kind="WIN",outcome="processed"} 1
# wager_transactions_total{channel="http",failure_code="INSUFFICIENT_FUNDS",kind="BET",outcome="rejected"} 1
# …
```

Cada réplica expõe só os próprios contadores, e a mensagem do SQS e o worker caem em qualquer uma, por isso o laço lê as 3. No Prometheus do compose (<http://localhost:9090>), some por rótulo (`sum by (channel, kind, outcome)`).

**Dashboard:** <http://localhost:3000> abre o `PDA — visão geral` no Grafana, já somando as 3 réplicas: resultados por status, duplicatas, retries, DLQ, conflitos, atraso da outbox, latências e divergências de reconciliação. Com `make load-test` rodando, os painéis se mexem em tempo real (atualização a cada 5 s). Como o JSON versionado é a fonte, alterações feitas na interface não são salvas.

- **Fila de auditoria:** é FIFO, então entrega primeiro os eventos mais antigos do ambiente. Com 3 réplicas publicando, a ordem entre eventos da mesma carteira **não é estrita** (um evento da BET pode chegar antes dos da abertura); para o saldo, vale o `walletVersion` ([`docs/messaging.md`](docs/messaging.md) §7). Uma mensagem lida fica invisível por 30 s e depois volta. Os contratos dos eventos estão em [`api/events.yaml`](api/events.yaml) e em [`docs/messaging.md`](docs/messaging.md) §6–§7.
- **Métricas e logs:** o catálogo de métricas está no [`ARCHITECTURE.md`](ARCHITECTURE.md) §13.2. Os logs são JSON: `docker compose logs -f app-1`.

---

## 9. Testes

Os testes usam só `testing` e `go test`, todos com `-race` nos alvos do `make`. Os testes que precisam de infraestrutura ficam atrás de **build tags**, então `go test ./...` roda num checkout limpo, sem Docker.

| Nível | Comando | Precisa de | Tempo aproximado¹ |
| --- | --- | --- | --- |
| Unitário | `go test ./...` · `go test -race ./...` (ou `make test`) | Só o Go | ~15 s |
| Estático | `go vet ./...` · `gofmt -l .` (vazio) | Só o Go | segundos |
| Portão de qualidade | `make check` (formatação, `golangci-lint`, `go vet` com e sem tags, `go.mod` limpo, versão do Go, unitários com `-race`) | Go e Docker | ~15 s com cache; mais na primeira vez, que baixa a imagem do `golangci-lint` e analisa tudo |
| Integração (tag `integration`) | `make test-integration` | Docker (sobe a infraestrutura sozinho) | ~50 s com a infraestrutura de pé |
| Multi-instância e falhas (tag `e2e`) | `make test-e2e` | Docker (idem) | ~2,5 min |
| Carga (opcional, fora do CI) | `make load-test` (`RATE`, `DURATION`, `WALLETS`) | O compose de pé (`docker compose up --build -d --wait`) | ~1,5 min a 100 req/s; ~2 min a 200 req/s, com a drenagem da outbox |

¹ Medidos num MacBook (Apple Silicon) com o cache de build do Go preenchido. Quando a infraestrutura ainda não está de pé, o `make infra-up` soma cerca de 1 min, porque espera o Keycloak ficar pronto. Num clone novo, com os caches do Go vazios, a primeira execução soma o download dos módulos e a compilação: `go test ./...` levou ~32 s, e o `make test-e2e` ~2 min 20 s.

```sh
go test ./...                 # os 4 comandos pedidos pelo desafio
go test -race ./...
go vet ./...
gofmt -l .                    # não imprime nada

make check                    # o portão de qualidade completo
make test-integration         # PostgreSQL, Keycloak e MiniStack reais, app em processo
make test-e2e                 # 3 processos do binário, pontos de falha, quedas e shutdown
make load-test                # opcional: k6 contra as 3 réplicas de pé (docs/load-test.md)
```

- **Não rode `make test-integration` e `make test-e2e` ao mesmo tempo:** os testes de resiliência pausam o PostgreSQL e o MiniStack compartilhados.
- **Detalhes em [`docs/testing.md`](docs/testing.md):** como preparar as dependências, o isolamento de cada pacote, o cluster de 3 processos, os pontos de falha, os comandos para rodar um cenário isolado e as simulações manuais de falha no compose.
- **Rastreabilidade:** o plano de testes com cada caso até os requisitos está em [`docs/test-plan.md`](docs/test-plan.md). O CI roda os 3 níveis em [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

---

## 10. Operação e problemas comuns

- **Logs:** `docker compose logs -f app-1`. Os logs são JSON, com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`. Uma requisição pode enviar `X-Correlation-Id`.
- **Papéis por réplica:** as variáveis `*_ENABLED` (§4.3) separam API, consumidor, publisher e worker. Por padrão, todas as réplicas rodam tudo.
- **Queda de uma réplica:** o `restart: on-failure` traz de volta uma réplica que caiu (panic, OOM, servidor HTTP que parou). Um `docker compose stop`/`kill` é uma parada manual e não reinicia.
- **Shutdown:** um `SIGTERM` (`docker compose stop`) para a API primeiro, depois o consumidor (libera as mensagens não iniciadas), o publisher e o worker, e por último o pool do PostgreSQL. A ordem completa está no [`ARCHITECTURE.md`](ARCHITECTURE.md) §12.

| Sintoma | Causa provável | O que fazer |
| --- | --- | --- |
| `bind: address already in use` no `up` | Uma das portas da §1 está ocupada | Libere a porta ou pare o outro processo |
| As réplicas reiniciam em sequência e o log de start mostra `UnrecognizedClientException` (*the security token included in the request is invalid*) | O MiniStack foi recriado e as chaves de `.local/aws/credentials` são de outra execução | `docker compose up aws-init && docker compose restart app-1 app-2 app-3` |
| `up --wait` falha com o PostgreSQL ou o MiniStack *unhealthy* depois de um teste interrompido | Um teste de resiliência deixou o container pausado | `docker compose unpause postgres` e `docker compose unpause ministack`, um por comando (juntos, o `unpause` falha se um deles não estiver pausado), ou `make infra-up`, que já faz isso |
| `401` com um token recém-emitido | Token pedido por outro endereço que não `localhost:8080`, ou expirado (5 min) | Peça o token com `scripts/get-token.sh` |
| `up --wait` demora no Keycloak | A primeira subida importa os realms | Espere: o healthcheck tolera 60 s de `start_period` e mais 30 tentativas a cada 5 s |
| `/docs` em branco | O Swagger UI vem de uma CDN | Sem internet, use `/openapi.yaml` ou [`api/requests.http`](api/requests.http) |

---

## 11. Onde está cada entregável

| Exigência do desafio (§15) | Onde |
| --- | --- |
| Código, migrations e ambiente Docker Compose | [`cmd/`](cmd/), [`internal/`](internal/), [`migrations/`](migrations/), [`docker-compose.yml`](docker-compose.yml), [`Dockerfile`](Dockerfile) |
| Pré-requisitos, variáveis, filas, migrations, execução, exemplos e testes | Este README, §1–§9 |
| `.env.example` sem segredos reais | [`.env.example`](.env.example) |
| Provisionamento do IdP, identidades de teste e fluxos autenticados | [`deploy/keycloak/`](deploy/keycloak/), §7 e §8 |
| Decisões (dinheiro, transações, idempotência, locks, referências, reversões, inbox/outbox, autenticação, autorização, Fx e shutdown), limitações, interpretações e trabalho não concluído | [`ARCHITECTURE.md`](ARCHITECTURE.md) |
| Preparação dos testes, integração, múltiplas instâncias, simulações de falha e build tags | [`docs/testing.md`](docs/testing.md) |
| Contratos HTTP e de eventos | [`api/openapi.yaml`](api/openapi.yaml), [`api/events.yaml`](api/events.yaml) |
| Checklist de requisitos com o teste que comprova cada um | [`docs/delivery-requirements.md`](docs/delivery-requirements.md) |
| Teste de carga ⭐ (comando, metodologia, ambiente e resultados) | [`docs/load-test.md`](docs/load-test.md): 100 req/s, p99 de 98 ms, 1.000/1.000 carteiras consistentes |

A documentação completa do sistema fica em [`docs/`](docs/) (mapa no [`ARCHITECTURE.md`](ARCHITECTURE.md) §18). As notas de desenvolvimento (specs, planos, spikes e diário) ficam em [`docs/dev/`](docs/dev/).
