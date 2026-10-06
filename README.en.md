# Distributed Wager Processor

[Português](README.md) · **English**

[![CI](https://github.com/KaioVinicios/pda/actions/workflows/ci.yml/badge.svg)](https://github.com/KaioVinicios/pda/actions/workflows/ci.yml)
![Go 1.27.1](https://img.shields.io/badge/Go-1.27.1-00ADD8?logo=go&logoColor=white)
![PostgreSQL 18](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A Go service that moves player wallets based on game-provider operations (`BET`, `WIN`, `LOSS`, `REFUND` and `ROLLBACK`) received over **HTTP** and **SQS**, with the same guarantees on both channels: no floating-point money, an append-only auditable ledger, persistent idempotency, per-wallet coordination across several instances, a transactional inbox and outbox, and failure recovery.

The codename **`pda`** comes from the Portuguese *Processamento Distribuído de Apostas* ("distributed wager processing"). It is still the name of the binary, the database and the Keycloak realm.

- **Stack:** Go 1.27.1, Uber Fx, `net/http`, PostgreSQL 18 (`pgx/v5`, explicit SQL), Keycloak 26 (OIDC, `client_credentials`), SQS and SNS FIFO on MiniStack, `golang-migrate`, `log/slog`, Prometheus and Grafana.
- **Run it:** `docker compose up --build` starts the whole infrastructure and **3 independent replicas** of the service.
- **Background:** built as the solution to a backend engineering challenge. The original statement, in Portuguese, is in [`CHALLENGE.md`](CHALLENGE.md).
- **Language of the docs:** the main [`README.md`](README.md), [`ARCHITECTURE.md`](ARCHITECTURE.md) and [`docs/`](docs/) are in Portuguese. This page is the English version of the README, with the same section numbers.

---

## Highlights

Every guarantee below is enforced in code or in the database **and** backed by an automated test. Integration tests run against real PostgreSQL, Keycloak and MiniStack containers, and the e2e tests run 3 OS processes of the binary.

| Guarantee | How | Proven by |
| --- | --- | --- |
| **Exact money** | `int64` minor units with ISO 4217 currency; strict decimal-string parsing; overflow checks on every operation. No `float32`/`float64` anywhere | [`TestNoFloatInMoney`](internal/domain/money/nofloat_test.go) (AST scan) + `forbidigo` lint, [`FuzzParseMoney`](internal/domain/money/money_fuzz_test.go) |
| **Auditable, append-only ledger** | Triggers block `UPDATE`/`DELETE`/`TRUNCATE` (even for the table owner); the app role lacks those privileges; `CHECK (balance >= 0)` and coherence triggers in the schema; a reconciliation endpoint rebuilds the balance from the ledger | [`TestLedgerImmutable`](internal/adapters/postgres/protection_integration_test.go), [`TestConstraints`](internal/adapters/postgres/constraints_integration_test.go) |
| **Persistent idempotency on both channels** | HTTP and SQS share a single use case; unique keys in PostgreSQL; a replay returns the persisted result, including the balance observed originally | [`TestSameBet50xHTTP`, `TestSameBet50xSQS`](test/e2e/idempotency_test.go), [`TestHTTPAndSQSConcurrent`](test/e2e/channels_test.go), [`TestReplayReturnsOriginalBalance`](test/integration/wagering_test.go) |
| **Per-wallet coordination, no global lock** | `SELECT … FOR UPDATE` on the wallet row, plus a version check and the balance `CHECK` as further defenses. Two concurrent 80.00 bets on a 100.00 wallet, sent to different processes, give 1 debit and a final balance of 20.00, 20 times in a row | [`TestTwoBetsCompete`, `TestNoGlobalLock`](test/e2e/concurrency_test.go) |
| **Crash safety** | Transactional inbox and outbox; events are published only after commit; the outbox publisher uses leases, so another instance takes over abandoned work, keeping the same `eventId` | [`TestCrashAfterCommitBeforeDelete`, `TestHTTPCrashAfterCommit`](test/e2e/crash_test.go), [`TestPublisherCrashAfterPublish`](test/e2e/outbox_test.go), [`TestNoPublishBeforeCommit`](internal/adapters/outbox/publisher_integration_test.go), [`TestFullRestart`](test/e2e/restart_test.go) |
| **Out-of-order reversals** | A `REFUND`/`ROLLBACK` that arrives before its `BET` waits in `PENDING_REFERENCE`; a durable worker retries with backoff and rejects it with `REFERENCE_NOT_FOUND` after a TTL | [`TestRefundBeforeBet`, `TestRefundReferenceExpires`](test/e2e/references_test.go) |
| **Dependency outages** | Dependencies frozen with `docker compose pause` under live traffic. PostgreSQL down: 503 with `Retry-After`, then every operation recorded exactly once and no message sent to the DLQ. MiniStack down: HTTP keeps processing, and the outbox piles up and drains afterwards | [`TestPostgresOutage`, `TestSQSOutage`](test/e2e/resilience_test.go) |
| **Authentication and provider isolation** | Keycloak OIDC tokens validated locally (JWKS, `iss`, `aud`, `exp`); the provider comes from the token, never from the body, on HTTP and SQS alike; IAM policies on the broker | [`TestProviderIsolationQueries`, `TestUnauthorizedHasNoEffects`](test/integration/auth_test.go) |

After every scenario, [`AssertWalletConsistent`](test/testkit/assert.go) checks that the stored balance equals credits minus debits in the ledger. The binary used in the e2e tests is built with `-race`, and any reported data race fails the suite.

**Load test** ([`docs/load-test.md`](docs/load-test.md)): 100 req/s for 60 s across the 3 replicas, p50/p95/p99 of 3.8/14.3/98.2 ms, 0 errors, and 1,000 out of 1,000 wallets consistent at the end.

## Architecture at a glance

```mermaid
flowchart LR
    subgraph Clients
        PV["Providers<br/>(provider-a, provider-b)"]
        WS["Internal service<br/>(wallet-service)"]
    end
    KC[Keycloak<br/>OIDC / client_credentials]
    Q[(SQS FIFO<br/>wager-transactions.fifo)]
    DLQ[(DLQ FIFO)]
    subgraph "pda × 3 instances (same binary)"
        API[HTTP API]
        CON[SQS consumer]
        UC[[Single use case<br/>ProcessWagerTransaction]]
        REF[Reference worker]
        PUB[Outbox publisher]
    end
    PG[(PostgreSQL<br/>wallets · transactions · ledger<br/>inbox · outbox)]
    SNS{{SNS FIFO<br/>wallet-events.fifo}}

    PV & WS -- token --> KC
    PV & WS -- "Bearer JWT" --> API
    PV --> Q --> CON
    Q -. redrive .-> DLQ
    API & CON --> UC --> PG
    REF --> UC
    PG --> PUB --> SNS
```

- **One binary, four roles:** HTTP API, SQS consumer, outbox publisher and reference worker, each one toggled by an environment variable.
- **One use case for every entry point:** only the edge changes, so HTTP, SQS and the worker get the same guarantees.
- **PostgreSQL is the source of truth:** uniqueness, non-negative balances, immutability, idempotency, retry schedules and pending events all live in the database. Correctness never depends on in-memory state, on a specific instance or on SQS FIFO deduplication.
- **Layers:** `domain` (entities and rules, stdlib only), `app` (use cases and ports), `adapters` (PostgreSQL, HTTP, SQS, SNS, workers) and `bootstrap` (Fx wiring). A lint rule (`depguard`) and a test keep infrastructure out of the domain.

The decisions behind each of these points, with the trade-offs, are in [`ARCHITECTURE.md`](ARCHITECTURE.md) and in [`docs/decisions.md`](docs/decisions.md).

## How it was built

- **Docs first:** requirements, data model, transaction lifecycle, messaging contracts and a test plan were written before any code ([`docs/`](docs/)). Each of the 23 design decisions is recorded with its rationale in [`docs/decisions.md`](docs/decisions.md).
- **Spec → plan → TDD for every change**, bug fixes included ([`docs/development-workflow.md`](docs/development-workflow.md)). Tests written for behavior that already existed went through a sensitivity check: break the code, watch the test fail, revert.
- **Traceability:** [`docs/delivery-requirements.md`](docs/delivery-requirements.md) maps every requirement to the test that proves it. The specs, plans and the development diary are kept in [`docs/dev/`](docs/dev/).

---

## Contents

1. [Prerequisites](#1-prerequisites)
2. [Quick start](#2-quick-start)
3. [Services and ports](#3-services-and-ports)
4. [Environment variables](#4-environment-variables)
5. [Queues, topic and broker credentials](#5-queues-topic-and-broker-credentials)
6. [Migrations](#6-migrations)
7. [Authentication and test identities](#7-authentication-and-test-identities)
8. [Example calls](#8-example-calls)
9. [Tests](#9-tests)
10. [Operations and troubleshooting](#10-operations-and-troubleshooting)
11. [Where to find each deliverable](#11-where-to-find-each-deliverable)

---

## 1. Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Docker Engine + Docker Compose v2 | Compose **2.24 or newer** (`env_file` uses `required: false`) | Running everything. The application image is compiled during the build |
| Go | **1.27.1** (the one in `go.mod`) | Only to run the tests outside Docker |
| `make`, `curl` | Any | `Makefile` shortcuts and examples |
| `jq`, `uuidgen` | Any | Only for the examples in §8 |

- **Free host ports:** `5432` (PostgreSQL), `8080` (Keycloak), `4566` (MiniStack), `8081`–`8083` (replica APIs), `9091`–`9093` (metrics), `9090` (Prometheus) and `3000` (Grafana).
- **Memory:** the stack uses about 2 GB after starting and running a load test (Keycloak ~1 GB and Grafana ~650 MB are the largest). Give Docker 3 GB.
- **Nothing else to install:** lint runs from the `golangci-lint` image, migrations from the `migrate` image and the AWS CLI from the `amazon/aws-cli` image.

---

## 2. Quick start

```sh
git clone https://github.com/KaioVinicios/pda.git
cd pda
docker compose up --build            # in the foreground (Ctrl+C to stop); same as `make up`
# or in the background, waiting until everything is healthy:
docker compose up --build --wait
```

There is no manual step: `.env.example` already holds local values, and Compose provisions everything on startup, in this order:

1. `postgres` creates the `pda_owner` and `pda_app` roles (`deploy/postgres/01-roles.sh`), and `migrate` applies the migrations as `pda_owner`;
2. `ministack` starts with `AUTH=true`, and `aws-init` creates the queues, topic, subscription, IAM users and the `.local/aws/credentials` file;
3. `keycloak` imports the `pda` and `other` realms with clients, roles and mappers;
4. `app-1`, `app-2` and `app-3` start once all of the above is ready (healthcheck through the binary itself, `pda healthcheck`).

The first startup takes a few minutes (image downloads, compilation and about 30–60 s for Keycloak). With cached images, `up --build --wait` takes under 1 min.

```sh
curl -s localhost:8081/health/ready
# {"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}
```

- **Swagger UI:** <http://localhost:8081/docs> (gets the token from Keycloak and calls any replica). The contract is at <http://localhost:8081/openapi.yaml>.
- **Stopping:** `docker compose down` keeps the data; `docker compose down -v` (or `make down`) also deletes the PostgreSQL volume. MiniStack keeps queues and keys in memory only: on the next startup, `aws-init` recreates everything.

---

## 3. Services and ports

| Service | Host address | Role |
| --- | --- | --- |
| `app-1`, `app-2`, `app-3` | API on `localhost:8081`, `8082` and `8083`; metrics on `localhost:9091`, `9092` and `9093` (`/metrics`) | The same binary, each replica with its own pool and memory. All of them run the 4 roles: HTTP API, SQS consumer, outbox publisher and reference worker. `restart: on-failure` |
| `postgres` | `localhost:5432` (database `pda`) | Source of truth: wallets, transactions, ledger, inbox and outbox |
| `keycloak` | `localhost:8080` (console: `admin` / `admin-local`) | OIDC IdP; issues `client_credentials` tokens |
| `ministack` | `localhost:4566` | SQS and SNS FIFO, with IAM policies evaluated (`AUTH=true`) |
| `prometheus` | <http://localhost:9090> | Scrapes `/metrics` from the 3 replicas every 5 s (D-22) |
| `grafana` | <http://localhost:3000> (anonymous read-only; `admin` / `admin` to edit) | `PDA — visão geral` (overview) dashboard, provisioned from `deploy/grafana/` ([`ARCHITECTURE.md`](ARCHITECTURE.md) §13.3) |
| `migrate`, `aws-init` | — | One-shot startup tasks (migrations; queues, topic and IAM) |
| `k6` (`load` profile) | — | Load generator. Runs only with `make load-test`, never on `docker compose up` ([`docs/load-test.md`](docs/load-test.md)) |

API routes (full contract in [`api/openapi.yaml`](api/openapi.yaml)):

| Route | Who can call it |
| --- | --- |
| `GET /health/live`, `GET /health/ready`, `GET /docs`, `GET /openapi.yaml` | Public |
| `POST /wallets`, `GET /wallets/{walletId}`, `GET /wallets/{walletId}/ledger`, `POST /wallets/{walletId}/reconciliation` | Internal service (`wallet-internal`) |
| `POST /wagering/transactions` | Provider (`provider`), with the body's `providerId` equal to the token's |
| `GET /wagering/transactions/{transactionId}` | Provider (own transactions only) and internal service |
| `GET /providers/{providerId}/wagering/transactions/{externalTransactionId}` | Provider (its own `providerId`) and internal service |

---

## 4. Environment variables

### 4.1 Files

- **[`.env.example`](.env.example)** (versioned): **local** sample values, with no real secrets. Every Compose service (`env_file`) and the tests load it.
- **`.env`** (optional, git-ignored): overrides `.env.example` without editing it. For example, `LOG_LEVEL=debug` in a `.env` applies to the 3 replicas on the next `docker compose up`.
- **The AWS keys live in neither file:** `aws-init` generates them and writes them to `.local/aws/credentials` (§5).

### 4.2 Infrastructure (`.env.example`)

| Variable | Local value | Purpose |
| --- | --- | --- |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB` | `postgres`, `postgres-local`, `pda` | Container superuser and database |
| `PDA_OWNER_PASSWORD`, `PDA_APP_PASSWORD` | `pda-owner-local`, `pda-app-local` | Passwords of the `pda_owner` (migrations, DDL) and `pda_app` (application; no DDL and no `UPDATE`/`DELETE` on the ledger) roles |
| `DATABASE_OWNER_URL` | `postgres://pda_owner:…@postgres:5432/pda` | URL used by the `migrate` service |
| `KC_BOOTSTRAP_ADMIN_USERNAME`, `KC_BOOTSTRAP_ADMIN_PASSWORD` | `admin`, `admin-local` | Keycloak console |
| `PROVIDER_A_SECRET`, `PROVIDER_B_SECRET`, `WALLET_SERVICE_SECRET`, `NO_ROLE_CLIENT_SECRET`, `NO_AUDIENCE_CLIENT_SECRET`, `PROVIDER_SHORT_LIVED_SECRET`, `OTHER_PROVIDER_SECRET` | `<client>-local-secret` | Secrets of the clients imported into Keycloak (§7) |
| `SQS_EVENTS_AUDIT_QUEUE_NAME`, `SQS_MAX_RECEIVE_COUNT` | `wallet-events-audit.fifo`, `10` | Audit queue and the redrive `maxReceiveCount`. They are not in `.env.example`: they are `aws-init` defaults, which a `.env` can override |

### 4.3 Application

Configuration is read from the environment only and **validated at startup**. An invalid variable prevents startup with a message that names the variable without echoing its value, and the process exits with code 1. The production values below are the defaults; Compose only sets the addresses, `LOG_LEVEL` and the credentials.

**Process and HTTP**

| Variable | Default | Description |
| --- | --- | --- |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. At `debug`, Fx internal events are logged too |
| `HTTP_ADDR` | `:8080` | API address |
| `METRICS_ADDR` | `:9090` | Admin server (`/metrics`); must differ from `HTTP_ADDR` |
| `SHUTDOWN_TIMEOUT` | `20s` | Graceful shutdown deadline of each component; less than 30 s (the Fx `StopTimeout`) |
| `HTTP_REQUEST_TIMEOUT` | `10s` | Deadline of each authenticated request; when it expires, the response is 503 with `Retry-After`. Rule: `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30 s` |
| `API_DOCS_ENABLED` | `true` | Serves `/docs` (Swagger UI) and `/openapi.yaml` |
| `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED`, `REFERENCE_WORKER_ENABLED` | `true` | Instance roles. A disabled role leaves the Fx graph. `/health/ready` lives in the API: without `HTTP_ENABLED`, the container healthcheck fails |

**PostgreSQL**

| Variable | Default | Description |
| --- | --- | --- |
| `DATABASE_URL` | — (required) | `postgres://` URL of the `pda_app` role |
| `DB_MAX_CONNS` | `10` | Maximum pool size per instance |
| `DB_LOCK_TIMEOUT` | `5s` | `lock_timeout` of each write transaction; when it runs out, the operation is transient (503, or a retry on SQS) |

**OIDC (Keycloak)**

| Variable | Default | Description |
| --- | --- | --- |
| `OIDC_ISSUER` | — (required) | Expected `iss` in tokens (`http://localhost:8080/realms/pda`, the address clients use) |
| `OIDC_JWKS_URL` | — (required) | Where the application fetches the keys (`http://keycloak:8080/…/certs`, over the Compose network). Deliberately separate from the issuer, with no discovery |
| `OIDC_AUDIENCE` | `pda-api` | Required `aud` |
| `OIDC_CLOCK_SKEW` | `30s` | `exp` tolerance |

**AWS (MiniStack)**, read by the SDK itself

| Variable | Value in Compose | Description |
| --- | --- | --- |
| `AWS_REGION` | `us-east-1` | Required |
| `AWS_ENDPOINT_URL` | `http://ministack:4566` | Emulator endpoint (without it, the SDK would use real AWS) |
| `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE` | `/aws/credentials`, `pda-wallet-service` | The file generated by `aws-init`, mounted read-only, and the application's IAM user |

**SQS consumer**

| Variable | Default | Description |
| --- | --- | --- |
| `SQS_WAGER_QUEUE_NAME`, `SQS_WAGER_DLQ_NAME` | `wager-transactions.fifo`, `wager-transactions-dlq.fifo` | Input queue and DLQ (`.fifo` suffix required, different names) |
| `SQS_CONSUMER_POLLERS` | `2` | Long-polling pollers per instance |
| `SQS_RECEIVE_BATCH` | `10` | Messages per `ReceiveMessage` (1–10) |
| `SQS_WAIT_TIME` | `20s` | Long polling (whole seconds, 0–20 s) |
| `SQS_VISIBILITY_TIMEOUT` | `30s` | Visibility of each receive (whole seconds) |
| `SQS_PROCESSING_TIMEOUT` | `10s` | Per-message deadline; must be **less** than the visibility timeout |
| `SQS_MAX_IN_FLIGHT` | `16` | Messages processed concurrently per instance |
| `SQS_RETRY_MAX_DELAY` | `300s` | Ceiling of the transient-failure backoff, `min(2^receives s, ceiling)` |

**Outbox publisher (SNS)**

| Variable | Default | Description |
| --- | --- | --- |
| `SNS_EVENTS_TOPIC_NAME` | `wallet-events.fifo` | Output topic; checked at startup |
| `OUTBOX_BATCH_SIZE` | `50` | Events per claim (1–1000) |
| `OUTBOX_LEASE` | `30s` | Lease of a claim; once it expires, another instance takes over |
| `OUTBOX_POLL_INTERVAL` | `500ms` | Interval between polls that found no work |
| `OUTBOX_CONCURRENCY` | `8` | Groups (wallets) published in parallel |
| `OUTBOX_RETRY_BASE_DELAY`, `OUTBOX_RETRY_MAX_DELAY` | `1s`, `5m` | Exponential backoff of a failed publication |

**Reference worker**

| Variable | Default | Description |
| --- | --- | --- |
| `REFERENCE_RETRY_BASE_DELAY`, `REFERENCE_RETRY_MAX_DELAY` | `1s`, `60s` | Exponential backoff (±20% jitter) of an operation in `PENDING_REFERENCE` |
| `REFERENCE_MAX_ATTEMPTS` | `8` | Attempts before `REJECTED` with `REFERENCE_NOT_FOUND` |
| `REFERENCE_TTL` | `10m` | Maximum wall-clock wait (whichever comes first) |
| `REFERENCE_POLL_INTERVAL` | `500ms` | Interval between polls for due pending operations |
| `REFERENCE_BATCH_SIZE` | `50` | Pending operations per poll |

The messaging parameters are explained in [`docs/messaging.md`](docs/messaging.md) §4–§5, and the timings used in the tests in [`docs/test-plan.md`](docs/test-plan.md) §3.3.

---

## 5. Queues, topic and broker credentials

Initialization is **automatic and idempotent**: the `aws-init` service runs [`deploy/aws/init.sh`](deploy/aws/init.sh) with the emulator's root key (`test`) on every `docker compose up`, and the replicas start only after it finishes.

| Resource | Type | Configuration |
| --- | --- | --- |
| `wager-transactions.fifo` | SQS FIFO (input) | `VisibilityTimeout=30`, `ReceiveMessageWaitTimeSeconds=20`, 4-day retention, **redrive to the DLQ after 10 receives** |
| `wager-transactions-dlq.fifo` | SQS FIFO (DLQ) | 14-day retention |
| `wallet-events.fifo` | SNS FIFO (output) | Destination of the outbox events |
| `wallet-events-audit.fifo` | SQS FIFO | Subscribed to the topic (`RawMessageDelivery=true`); reference consumer of the events |

**Credentials and policies.** MiniStack runs with `AUTH=true` and evaluates IAM policies. `aws-init` creates one user per principal, with the identity policy from [`deploy/aws/policies/`](deploy/aws/policies/), and writes the keys to `.local/aws/credentials` (one profile per user):

| Profile | Allowed to |
| --- | --- |
| `pda-wallet-service` (the application) | Consume the input queue, send to the DLQ and publish to the topic |
| `provider-a`, `provider-b` | Only send to the input queue |

- Re-running the provisioning: `docker compose up aws-init`. Keys that are still valid are reused.
- **If MiniStack is recreated**, the old keys no longer exist, and `aws-init` generates new ones. The replicas read the file only at startup, so they need a restart: `docker compose restart app-1 app-2 app-3`.

**Inspecting the queues.** The AWS CLI runs from its image, on the Compose network. With the emulator's root key:

```sh
aws_root() {
  docker run --rm --network pda_default -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
    -e AWS_REGION=us-east-1 amazon/aws-cli:2.36.31 --endpoint-url http://ministack:4566 "$@"
}
aws_root sqs list-queues
aws_root sqs get-queue-attributes --attribute-names All \
  --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo
```

**DLQ and reprocessing.** Invalid messages and permanent failures reach the DLQ through an explicit send carrying the `errorCode`, `errorCategory`, `originalMessageId`, `consumerName` and `failedAt` attributes; transient failures get there after 10 receives, through the redrive. The codes are listed in [`docs/transaction-lifecycle.md`](docs/transaction-lifecycle.md) §5.4.
- **On AWS**, reprocessing is `aws sqs start-message-move-task --source-arn <DLQ arn>`.
- **MiniStack 1.5.18 does not implement that action** (it answers `InvalidAction`). Locally:
  1. read the message and its attributes: `aws_root sqs receive-message --queue-url …/wager-transactions-dlq.fifo --attribute-names All --message-attribute-names All`;
  2. fix the cause (an invalid message has to be fixed by the producer);
  3. ask the producer to **resend the same message, with the same `messageId`** and a fresh token in the `accessToken` attribute (the copy in the DLQ does not carry the token, D-23).
- Resending is safe: the inbox and idempotency absorb any repetition, and the token is left out of the message hash.

The input message contract (`WagerTransactionRequested`), the `MessageGroupId` (`walletId`) and the `MessageDeduplicationId` (`messageId`) are described in [`docs/messaging.md`](docs/messaging.md) §3.

---

## 6. Migrations

Migrations live in [`migrations/`](migrations/) (`golang-migrate` format, one `up`/`down` pair per version):

| Version | Content |
| --- | --- |
| `000001` | `wallets` and constraints |
| `000002` | `wager_transactions`, constraints and idempotency indexes |
| `000003` | `wallet_ledger_entries` and constraints |
| `000004` | `inbox_messages` and `outbox_events` |
| `000005` | Protection triggers (append-only ledger, ledger × wallet coherence, immutable terminal transaction, outbox snapshot) |
| `000006` | `GRANT`s for the `pda_app` role |

**Applying them** is automatic: the Compose `migrate` service applies pending migrations as `pda_owner` before the replicas start. The application never runs DDL.

```sh
make migrate-up                              # applies every pending migration
make migrate-down                            # reverts the last one (N=1)
make migrate-down N=3                        # reverts the last 3
docker compose run --rm migrate version      # current version
docker compose run --rm migrate down -all    # reverts everything
```

The `make` targets use the `migrate` service (image `migrate/migrate:v4.20.1`), with nothing to install locally. With the `migrate` CLI installed on the host, use PostgreSQL's published address:

```sh
migrate -path migrations -database "postgres://pda_owner:pda-owner-local@localhost:5432/pda?sslmode=disable" up
migrate -path migrations -database "postgres://pda_owner:pda-owner-local@localhost:5432/pda?sslmode=disable" down 1
```

- **Reverting deletes data.** The `down` files drop tables, the ledger included. Stop the replicas before reverting past `000006` (`docker compose stop app-1 app-2 app-3`).
- **Coverage:** `TestMigrationsUpDownUp` applies `up`, `down -all` and `up` again and compares the schema. Details in [`docs/data-model.md`](docs/data-model.md) §7.

---

## 7. Authentication and test identities

On startup, Keycloak imports the realms in [`deploy/keycloak/`](deploy/keycloak/) with clients, roles and protocol mappers: there is no manual setup. Every identity is a `client_credentials` client, with the local secrets from `.env.example`.

| Client (realm) | Role in `pda-api` | `provider_id` claim | Purpose |
| --- | --- | --- | --- |
| `provider-a` (`pda`) | `provider` | `provider-a` | Provider: submits and queries its own transactions |
| `provider-b` (`pda`) | `provider` | `provider-b` | Second provider, for isolation between providers |
| `wallet-service` (`pda`) | `wallet-internal` | — | Internal service: wallets, ledger and reconciliation |
| `no-role-client` (`pda`) | — | — | Negative case: valid token without a role (403) |
| `no-audience-client` (`pda`) | `provider` | `provider-a` | Negative case: token without `aud=pda-api` (401) |
| `provider-short-lived` (`pda`) | `provider` | `provider-a` | Token that lives 5 s, for the expiry test |
| `other-provider` (`other`) | `provider` | `provider-a` | Negative case: another realm, with another `iss` and other keys (401) |

- **Validation:** the application checks the RS256 signature against the JWKS, plus `iss`, `aud` and `exp`. **The provider comes from the token's `provider_id` claim, never from the body.** The permission model is in [`ARCHITECTURE.md`](ARCHITECTURE.md) §10.
- **Token lifetime:** tokens from the `pda` realm last 5 min. After that, request a new one.

```sh
scripts/get-token.sh provider-a          # prints the access token (reads the secret from .env.example/.env)
scripts/get-token.sh wallet-service
scripts/get-token.sh other-provider other

# the same, with curl only:
curl -s -d grant_type=client_credentials -d client_id=provider-a -d client_secret=provider-a-local-secret \
  http://localhost:8080/realms/pda/protocol/openid-connect/token | jq -r .access_token
```

Request tokens from `http://localhost:8080`, the public address that `OIDC_ISSUER` expects in `iss`.

---

## 8. Example calls

The commands below were run against Compose, and the responses are abridged. Each call goes to a different replica, to show that the result does not depend on the instance. Two alternatives cover the same flow: the [`api/requests.http`](api/requests.http) collection (VS Code REST Client or JetBrains HTTP Client) and the Swagger UI at <http://localhost:8081/docs>.

### 8.1 Tokens and variables

```sh
PROVIDER_TOKEN=$(scripts/get-token.sh provider-a)
INTERNAL_TOKEN=$(scripts/get-token.sh wallet-service)
PLAYER=$(uuidgen | tr 'A-Z' 'a-z')   # a wallet is unique per (player, currency)
R=$(date +%s)                         # suffix for external ids, so the walkthrough can be repeated
```

### 8.2 Open a wallet (internal service)

```sh
WALLET=$(curl -s -X POST localhost:8081/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER\",\"initialBalance\":{\"amount\":\"100.00\",\"currency\":\"BRL\"}}" \
  | tee /dev/stderr | jq -r .id)
# 201 {"id":"01a0f436-…","playerId":"eda59cd1-…","balance":{"amount":"100.00","currency":"BRL"},"version":1,…}
```

With a positive balance, opening a wallet writes, in the same transaction, the wallet (version 1), the `OPENING` transaction, the ledger credit and the outbox events. Opening again for the same player and currency returns 409 `WALLET_ALREADY_EXISTS`.

### 8.3 Place a bet (provider)

```sh
curl -s -w '  HTTP %{http_code}\n' -X POST localhost:8081/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: provider-a:bet-1-$R" \
  -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"bet-1-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"BET\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"}}"
# {"transactionId":"01a0f436-a4ee-…","status":"PROCESSED","balance":{"amount":"20.00","currency":"BRL"},"idempotentReplay":false}  HTTP 200
```

For the next operations, a function builds the same body:

```sh
# wager <port> <kind> <external id> <amount> [external id of the reference]
wager() {
  local ref=${5:+,\"referenceExternalTransactionId\":\"$5-$R\"}
  curl -s -w '  HTTP %{http_code}\n' -X POST "localhost:$1/wagering/transactions" \
    -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
    -H "Idempotency-Key: provider-a:$3-$R" \
    -d "{\"providerId\":\"provider-a\",\"externalTransactionId\":\"$3-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"$2\",\"money\":{\"amount\":\"$4\",\"currency\":\"BRL\"}$ref}"
}
```

### 8.4 Replay, rejection and conflict

```sh
wager 8082 BET bet-1 80.00    # same key and same content, on another replica
# {"transactionId":"01a0f436-a4ee-…","status":"PROCESSED","balance":{"amount":"20.00",…},"idempotentReplay":true}  HTTP 200

wager 8083 BET bet-2 80.00    # insufficient funds
# {"transactionId":"…","status":"REJECTED","balance":{"amount":"20.00",…},"failureCode":"INSUFFICIENT_FUNDS","failureCategory":"DEFINITIVE","idempotentReplay":false}  HTTP 422

wager 8081 BET bet-1 1.00     # same key, different content
# {"type":"about:blank","title":"Conflict","status":409,"code":"IDEMPOTENCY_KEY_REUSED","category":"CORRECTABLE",…}  HTTP 409
```

A replay returns the **persisted** result, including the balance observed during the original processing, even if the wallet has changed since. Business rejections are persisted and replayable too (422).

### 8.5 Reversal before its reference

```sh
wager 8082 REFUND refund-3 10.00 bet-3    # the referenced BET has not arrived yet
# {"transactionId":"01a0f436-a531-…","status":"PENDING_REFERENCE","idempotentReplay":false}  HTTP 202

wager 8083 BET bet-3 10.00
# {"transactionId":"01a0f436-a541-…","status":"PROCESSED","balance":{"amount":"10.00",…},…}  HTTP 200

sleep 1   # the worker completes the pending operation within ~0.5 s (REFERENCE_POLL_INTERVAL)
curl -s localhost:8081/providers/provider-a/wagering/transactions/refund-3-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq '{kind, status, referenceTransactionId, balance}'
# {"kind":"REFUND","status":"PROCESSED","referenceTransactionId":"01a0f436-a541-…","balance":{"amount":"20.00",…}}
```

The arrival of the BET brings the pending operation forward, and the reference worker of any replica completes it asynchronously: a query right after the BET may still show `PENDING_REFERENCE`, and the balance and version in §8.6 assume the REFUND is done. Without the reference, the pending operation expires as `REJECTED` with `REFERENCE_NOT_FOUND`, after 8 attempts or 10 min. The query by internal id is `GET /wagering/transactions/{transactionId}`.

### 8.6 Wallet, ledger and reconciliation (internal service)

```sh
curl -s localhost:8082/wallets/$WALLET -H "Authorization: Bearer $INTERNAL_TOKEN"
# {"id":"01a0f436-…","balance":{"amount":"20.00","currency":"BRL"},"version":4,…}

curl -s "localhost:8083/wallets/$WALLET/ledger?limit=2" -H "Authorization: Bearer $INTERNAL_TOKEN" \
  | jq -c '{entries: [.items[] | {direction, amount: .amount.amount, balanceAfter: .balanceAfter.amount, walletVersion}], nextCursor}'
# {"entries":[{"direction":"CREDIT","amount":"100.00","balanceAfter":"100.00","walletVersion":1},
#             {"direction":"DEBIT","amount":"80.00","balanceAfter":"20.00","walletVersion":2}],"nextCursor":"eyJ2IjoyfQ"}
# next page: …/ledger?limit=2&cursor=eyJ2IjoyfQ

curl -s -X POST localhost:8081/wallets/$WALLET/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
# {"walletId":"01a0f436-…","storedBalance":{"amount":"20.00",…},"calculatedBalance":{"amount":"20.00",…},
#  "difference":{"amount":"0.00","currency":"BRL"},"consistent":true,"checkedEntries":4}
```

Reconciliation rebuilds the balance from the ledger in a consistent snapshot and never changes the balance. A divergence shows up in the response, in the log and in the `reconciliation_divergences_total` metric.

### 8.7 Denied access

```sh
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wallets/$WALLET                    # 401 UNAUTHENTICATED
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wallets/$WALLET \
  -H "Authorization: Bearer $PROVIDER_TOKEN"                                                # 403 FORBIDDEN
TX=$(curl -s localhost:8081/providers/provider-a/wagering/transactions/bet-1-$R \
  -H "Authorization: Bearer $PROVIDER_TOKEN" | jq -r .transactionId)
curl -s -o /dev/null -w '%{http_code}\n' localhost:8081/wagering/transactions/$TX \
  -H "Authorization: Bearer $(scripts/get-token.sh provider-b)"                             # 404 TRANSACTION_NOT_FOUND
```

A provider never sees another provider's transaction: the 404 by id does not reveal whether it exists. A `providerId` in the body or path that differs from the token's returns 403. Over SQS, the same rule applies with the token carried by the message (§8.8).

### 8.8 The same operation over SQS

The provider sends with **its own** IAM credentials, from `.local/aws/credentials`, and with **its own** Keycloak token in the `accessToken` attribute: the broker decides who may send, and the token says who the provider is (D-23).

```sh
aws_as() {   # aws_as <profile> <args…>: AWS CLI from the image, on the Compose network, as an IAM user
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

aws_as provider-a sqs receive-message --queue-url $QUEUE     # the provider cannot consume
# … AccessDeniedException … not authorized to perform: sqs:ReceiveMessage …

# provider-b, with its own key and its own token, tries to refund provider-a's BET on provider-a's behalf
aws_as provider-b sqs send-message --queue-url $QUEUE \
  --message-group-id "$WALLET" --message-deduplication-id "spoof-$R" \
  --message-attributes "{\"accessToken\":{\"DataType\":\"String\",\"StringValue\":\"$(scripts/get-token.sh provider-b)\"}}" \
  --message-body "{\"messageId\":\"spoof-$R\",\"type\":\"WagerTransactionRequested\",\"occurredAt\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"data\":{\"providerId\":\"provider-a\",\"externalTransactionId\":\"spoof-$R\",\"idempotencyKey\":\"provider-a:spoof-$R\",\"playerId\":\"$PLAYER\",\"walletId\":\"$WALLET\",\"roundId\":\"round-1\",\"gameId\":\"fortune-chimp\",\"kind\":\"REFUND\",\"money\":{\"amount\":\"80.00\",\"currency\":\"BRL\"},\"referenceExternalTransactionId\":\"bet-1-$R\"}}"
aws_root sqs receive-message --queue-url http://ministack:4566/000000000000/wager-transactions-dlq.fifo \
  --message-attribute-names All --wait-time-seconds 5 --query 'Messages[].MessageAttributes.errorCode.StringValue'
# [ "PROVIDER_MISMATCH" ]
```

SQS goes through the same use case as HTTP. Resending the same operation over either channel lands on the replay, and the inbox deduplicates the same `messageId`. A message with no token, an invalid token or a token naming another provider goes to the DLQ (`UNAUTHENTICATED`, `FORBIDDEN` or `PROVIDER_MISMATCH`) with no effect, and the balance does not change.

### 8.9 Events and metrics

```sh
# events published by the outbox to SNS, read from the audit queue (aws_root is defined in §5)
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

Each replica exposes only its own counters, and the SQS message and the worker can land on any of them, so the loop reads all 3. In the Compose Prometheus (<http://localhost:9090>), sum by label (`sum by (channel, kind, outcome)`).

**Dashboard:** <http://localhost:3000> opens the `PDA — visão geral` (overview) dashboard in Grafana, already summing the 3 replicas: outcomes by status, duplicates, retries, DLQ, conflicts, outbox lag, latencies and reconciliation divergences. With `make load-test` running, the panels move in real time (refreshed every 5 s). The versioned JSON is the source, so changes made in the UI are not saved.

- **Audit queue:** it is FIFO, so it delivers the oldest events of the environment first. With 3 replicas publishing, the order among events of the same wallet **is not strict** (an event from the BET can arrive before those from the opening); for the balance, `walletVersion` is what counts ([`docs/messaging.md`](docs/messaging.md) §7). A message that has been read stays invisible for 30 s and then comes back. The event contracts are in [`api/events.yaml`](api/events.yaml) and [`docs/messaging.md`](docs/messaging.md) §6–§7.
- **Metrics and logs:** the metrics catalog is in [`ARCHITECTURE.md`](ARCHITECTURE.md) §13.2. Logs are JSON: `docker compose logs -f app-1`.

---

## 9. Tests

The tests use only `testing` and `go test`, all with `-race` in the `make` targets. Tests that need infrastructure sit behind **build tags**, so `go test ./...` runs on a clean checkout without Docker.

| Level | Command | Needs | Approximate time¹ |
| --- | --- | --- | --- |
| Unit | `go test ./...` · `go test -race ./...` (or `make test`) | Go only | ~15 s |
| Static | `go vet ./...` · `gofmt -l .` (empty) | Go only | seconds |
| Quality gate | `make check` (formatting, `golangci-lint`, `go vet` with and without tags, clean `go.mod`, Go version, unit tests with `-race`) | Go and Docker | ~15 s with a warm cache; longer the first time, which pulls the `golangci-lint` image and analyzes everything |
| Integration (`integration` tag) | `make test-integration` | Docker (starts the infrastructure by itself) | ~50 s with the infrastructure up |
| Multi-instance and failures (`e2e` tag) | `make test-e2e` | Docker (same) | ~2.5 min |
| Load (optional, outside CI) | `make load-test` (`RATE`, `DURATION`, `WALLETS`) | Compose up (`docker compose up --build -d --wait`) | ~1.5 min at 100 req/s; ~2 min at 200 req/s, including the outbox drain |

¹ Measured on a MacBook (Apple Silicon) with a warm Go build cache. When the infrastructure is not up yet, `make infra-up` adds about 1 min, because it waits for Keycloak. On a fresh clone, with empty Go caches, the first run adds module downloads and compilation: `go test ./...` took ~32 s, and `make test-e2e` ~2 min 20 s.

```sh
go test ./...                 # the 4 commands required by the challenge
go test -race ./...
go vet ./...
gofmt -l .                    # prints nothing

make check                    # the full quality gate
make test-integration         # real PostgreSQL, Keycloak and MiniStack, app in-process
make test-e2e                 # 3 processes of the binary, fault points, outages and shutdown
make load-test                # optional: k6 against the 3 running replicas (docs/load-test.md)
```

- **Do not run `make test-integration` and `make test-e2e` at the same time:** the resilience tests pause the shared PostgreSQL and MiniStack.
- **Details in [`docs/testing.md`](docs/testing.md):** how to prepare the dependencies, each package's isolation, the 3-process cluster, the fault points, the commands to run a single scenario and the manual failure simulations on Compose.
- **Traceability:** the test plan, linking each case to its requirements, is in [`docs/test-plan.md`](docs/test-plan.md). CI runs the 3 levels in [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

---

## 10. Operations and troubleshooting

- **Logs:** `docker compose logs -f app-1`. Logs are JSON, with `correlationId`, `messageId`, `transactionId`, `walletId` and `providerId`. A request may send `X-Correlation-Id`.
- **Roles per replica:** the `*_ENABLED` variables (§4.3) split API, consumer, publisher and worker. By default, every replica runs everything.
- **A replica going down:** `restart: on-failure` brings back a replica that crashed (panic, OOM, an HTTP server that stopped). A `docker compose stop`/`kill` is a manual stop and does not restart.
- **Shutdown:** a `SIGTERM` (`docker compose stop`) stops the API first, then the consumer (releasing messages not yet started), the publisher and the worker, and finally the PostgreSQL pool. The full order is in [`ARCHITECTURE.md`](ARCHITECTURE.md) §12.

| Symptom | Likely cause | What to do |
| --- | --- | --- |
| `bind: address already in use` on `up` | One of the ports from §1 is taken | Free the port or stop the other process |
| Replicas restart one after another and the startup log shows `UnrecognizedClientException` (*the security token included in the request is invalid*) | MiniStack was recreated and the keys in `.local/aws/credentials` belong to another run | `docker compose up aws-init && docker compose restart app-1 app-2 app-3` |
| `up --wait` fails with PostgreSQL or MiniStack *unhealthy* after an interrupted test | A resilience test left the container paused | `docker compose unpause postgres` and `docker compose unpause ministack`, one per command (together, `unpause` fails if one of them is not paused), or `make infra-up`, which already does it |
| `401` with a freshly issued token | Token requested from an address other than `localhost:8080`, or expired (5 min) | Request the token with `scripts/get-token.sh` |
| `up --wait` takes long on Keycloak | The first startup imports the realms | Wait: the healthcheck allows a 60 s `start_period` plus 30 attempts every 5 s |
| Blank `/docs` | Swagger UI is loaded from a CDN | Offline, use `/openapi.yaml` or [`api/requests.http`](api/requests.http) |

---

## 11. Where to find each deliverable

| Challenge requirement ([`CHALLENGE.md`](CHALLENGE.md) §15) | Where |
| --- | --- |
| Code, migrations and Docker Compose environment | [`cmd/`](cmd/), [`internal/`](internal/), [`migrations/`](migrations/), [`docker-compose.yml`](docker-compose.yml), [`Dockerfile`](Dockerfile) |
| Prerequisites, variables, queues, migrations, running, examples and tests | This README, §1–§9 |
| `.env.example` with no real secrets | [`.env.example`](.env.example) |
| IdP provisioning, test identities and authenticated flows | [`deploy/keycloak/`](deploy/keycloak/), §7 and §8 |
| Decisions (money, transactions, idempotency, locks, references, reversals, inbox/outbox, authentication, authorization, Fx and shutdown), limitations, interpretations and unfinished work | [`ARCHITECTURE.md`](ARCHITECTURE.md) |
| Test setup, integration, multiple instances, failure simulations and build tags | [`docs/testing.md`](docs/testing.md) |
| HTTP and event contracts | [`api/openapi.yaml`](api/openapi.yaml), [`api/events.yaml`](api/events.yaml) |
| Requirements checklist with the test that proves each one | [`docs/delivery-requirements.md`](docs/delivery-requirements.md) |
| Load test ⭐ (command, methodology, environment and results) | [`docs/load-test.md`](docs/load-test.md): 100 req/s, p99 of 98 ms, 1,000/1,000 wallets consistent |

The full system documentation is in [`docs/`](docs/) (map in [`ARCHITECTURE.md`](ARCHITECTURE.md) §18). Development notes (specs, plans, spikes and the diary) are in [`docs/dev/`](docs/dev/).
