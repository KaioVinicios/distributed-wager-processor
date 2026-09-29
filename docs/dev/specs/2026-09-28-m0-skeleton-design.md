# M0 — Esqueleto, qualidade e infraestrutura: design

**Data:** 28/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aguardando revisão do autor

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M0;
- [`structure.md`](../../structure.md) §1 e §3, no subconjunto do M0;
- [`stack.md`](../../stack.md) §2–§5;
- D-02, D-07 (provisionamento), D-15 (ordem Fx) e D-18 (logs e `/metrics`).

**Pré-requisitos concluídos:** spikes de MiniStack, Keycloak e lint ([`../spike-ministack.md`](../spike-ministack.md), [`../spike-keycloak.md`](../spike-keycloak.md), [`../spike-lint.md`](../spike-lint.md)) e a decisão do autor de aplicar `AUTH=true` no MiniStack (D-02).

Esta spec registra só o **delta** em relação a `docs/`. O que já está decidido lá não é repetido.

---

## 1. Objetivo e critério de pronto

**Objetivo:** esqueleto executável e verificável, antes de qualquer regra de negócio.
- `docker compose up --build` sobe a infraestrutura e as 3 réplicas prontas.
- `make check` passa.
- Um token real é obtido do Keycloak.
- O caminho `AUTH=true` → credenciais IAM → aplicação já funciona de ponta a ponta.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde.
3. `docker compose config` válido e `docker compose up --build --wait`, com todos os serviços saudáveis.
4. `GET /health/ready` responde 200 em `localhost:8081`, `:8082` e `:8083`, e `GET /metrics` responde em `:9091`, `:9092` e `:9093`.
5. `scripts/get-token.sh provider-a` devolve um token com `iss = http://localhost:8080/realms/pda`, `aud = pda-api` e `provider_id = provider-a`, e o token do `no-audience-client` vem sem `aud`.
6. Os requisitos da §8 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.

**Fora do escopo** (com o marco de destino):
- migrations e serviço `migrate` (M2);
- middlewares HTTP, rotas de negócio, `auth` e `openapi.yaml` (M3);
- verificação do tópico SNS e resolução do ARN dele (M4);
- papéis habilitáveis `*_ENABLED` (M3–M6, junto com os módulos que controlam);
- catálogo de métricas (M7);
- `test-e2e` e `testkit.Cluster` (M8).

---

## 2. Componentes Go e composição Fx

**Ordem de registro no M0:** `config` → `observability` → `postgres` → `aws` → `httpapi`. É um subconjunto da ordem de D-15; os módulos dos marcos seguintes entram nas posições já definidas.

### 2.1 Pacotes e API pública

| Pacote | Arquivos | API pública |
| --- | --- | --- |
| `cmd/pda` | `main.go`, `healthcheck.go` | `main`: sem argumentos, executa `fx.New(bootstrap.Options()...).Run()`; com `healthcheck`, executa a sonda da §4.2 |
| `internal/bootstrap` | `bootstrap.go` | `func Options() []fx.Option`: os módulos na ordem acima, `fx.StopTimeout(30 * time.Second)` e `fx.WithLogger` com `fxevent.SlogLogger`. Os testes acrescentam `fx.Replace(cfg)` |
| `internal/config` | `config.go`, `validate.go`, `module.go` | `type Config struct{...}` (§3); `func Load() (Config, error)`, que faz o parse e depois `Validate`; `func (c Config) Validate() error`; `var Module = fx.Module("config", fx.Provide(Load))` |
| `internal/observability` | `logger.go`, `metrics.go`, `health.go`, `httpserver.go`, `admin_server.go`, `module.go` | `func NewLogger(cfg config.Config) (*slog.Logger, error)`; `func NewRegistry() *prometheus.Registry`, com os coletores de Go e de processo; `type Checker interface { Name() string; Check(ctx context.Context) error }`; `func NewHealth(log *slog.Logger, checkers []Checker, timeout time.Duration) *Health` (o logger registra o motivo das falhas); `func (h *Health) Ready(ctx context.Context) Report`; servidor admin em `METRICS_ADDR` com `/metrics`; `ServeOnLifecycle(lc, srv, shutdownTimeout, log, name)`, compartilhado com o `httpapi`, faz o `Listen` síncrono no `OnStart` (porta ocupada faz o `Start` falhar) e o `Shutdown` no `OnStop` |
| `internal/adapters/postgres` | `pool.go`, `module.go` | `func NewPool(lc fx.Lifecycle, cfg config.Config, log *slog.Logger) (*pgxpool.Pool, error)`, com ping no `OnStart` e `Close` no `OnStop`; checker `postgres` (`pool.Ping`) |
| `internal/adapters/awsclient` | `config.go`, `queues.go`, `module.go` | `func NewAWSConfig(hc *http.Client) (aws.Config, error)` (`LoadDefaultConfig` com contexto interno e timeout de 5 s, porque construtores Fx não recebem `ctx`). O módulo passa um `*http.Client` próprio, criado a partir do transporte padrão do SDK, e fecha as conexões ociosas no `OnStop` (D-15: clientes AWS fechados por último); fornece `*sqs.Client` e `*sns.Client`; `type Queues struct { WagerURL, DLQURL string }`, resolvido no `OnStart` com `GetQueueUrl` + `GetQueueAttributes` (fail fast); checker `sqs` (`GetQueueAttributes` na fila de entrada) |
| `internal/adapters/httpapi` | `server.go`, `routes.go`, `health_handler.go`, `module.go` | `func NewMux(h *observability.Health) *http.ServeMux`; `func RegisterServer(lc, cfg, mux, log)`, via `fx.Invoke`, usa o `ServeOnLifecycle`. O servidor não é fornecido ao grafo, o que evita dois `*http.Server` (API e admin) no container |

### 2.2 Decisões

1. **Os checkers vêm dos adaptadores**, por um *value group* do Fx (`group:"health_checkers"`).
   - `observability` só agrega: executa os checkers em paralelo, com timeout por checker (padrão de 2 s, constante `observability.DefaultCheckTimeout`).
   - Assim `observability` não importa `pgx` nem `aws`.
   - O comentário de `health.go` em [`structure.md`](../../structure.md) é ajustado.
2. **Credenciais e endpoint AWS vêm da cadeia padrão do SDK** (`config.LoadDefaultConfig`). Nenhum código lê chaves.
   - No compose: `AWS_SHARED_CREDENTIALS_FILE` + `AWS_PROFILE`.
   - Nos testes: `AWS_ACCESS_KEY_ID=test` (a chave raiz), que tem precedência sobre o arquivo.
   - O endpoint vem de `AWS_ENDPOINT_URL`, também nativo do SDK.
   - Uma região vazia gera erro claro no `OnStart`.
3. **O tópico SNS não é verificado no M0.** `pda-wallet-service` só tem `sns:Publish`, então `GetTopicAttributes` seria negado. A resolução do ARN do tópico fica para a spec do M4.
4. **Resposta de health:**
   - `Content-Type: application/json`, corpo `{"status":"UP","checks":{"postgres":"UP","sqs":"UP"}}`;
   - 200 se todos os checkers estão UP, 503 se qualquer um está DOWN;
   - `/health/live` sempre responde `{"status":"UP"}` com 200;
   - a mensagem de erro de um checker vai **só para o log** (nível `warn`), nunca para o corpo;
   - o schema entra no `openapi.yaml` no M3.
5. **Sem middlewares no M0.** O servidor HTTP já sai com `ReadHeaderTimeout` (proteção contra Slowloris exigida pelo `gosec`), mas o recover, a correlação e o log de acesso entram no M3.

---

## 3. Configuração

Cada marco acrescenta só as variáveis que usa. O catálogo consolidado entra no README no M10.

### 3.1 `config.Config` (M0)

| Variável | Campo | Padrão | Validação |
| --- | --- | --- | --- |
| `LOG_LEVEL` | `LogLevel string` | `info` | `debug`, `info`, `warn` ou `error` |
| `HTTP_ADDR` | `HTTPAddr string` | `:8080` | Não vazio e diferente de `METRICS_ADDR` |
| `METRICS_ADDR` | `MetricsAddr string` | `:9090` | Não vazio |
| `SHUTDOWN_TIMEOUT` | `ShutdownTimeout time.Duration` | `20s` | `> 0` e `< 30s` (o `fx.StopTimeout`) |
| `DATABASE_URL` | `DatabaseURL string` | — | Obrigatória; esquema `postgres` ou `postgresql` e host presente, via `net/url` |
| `DB_MAX_CONNS` | `DBMaxConns int32` | `10` | `≥ 1` |
| `SQS_WAGER_QUEUE_NAME` | `WagerQueueName string` | `wager-transactions.fifo` | Sufixo `.fifo` |
| `SQS_WAGER_DLQ_NAME` | `WagerDLQName string` | `wager-transactions-dlq.fifo` | Sufixo `.fifo` e diferente da fila principal |

Variáveis **nativas do SDK AWS**, fora da `Config`: `AWS_REGION`, `AWS_ENDPOINT_URL`, `AWS_SHARED_CREDENTIALS_FILE`, `AWS_PROFILE` e, nos testes, `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`.

### 3.2 Regras

- **Erros agregados** com `errors.Join`: uma execução mostra todos os problemas. Cada erro é um `*config.FieldError{Var, Reason}` e cita o **nome** da variável, **nunca o valor** (OBS-02).
- **O pacote `config` depende só da stdlib e de `caarlos0/env`.** O `pgxpool.ParseConfig` roda no adaptador e também falha no start.
- **Parse com `caarlos0/env`:** um erro de conversão (ex.: `SHUTDOWN_TIMEOUT=abc`) também é devolvido citando só o nome da variável. A mensagem do `caarlos0/env` inclui o valor, então é reescrita.

---

## 4. Infraestrutura

Exceções de TDD aprovadas ([`development-workflow.md`](../../development-workflow.md) §4.4), cada uma com a validação indicada.

### 4.1 `docker-compose.yml`

| Serviço | Imagem | Configuração | Healthcheck |
| --- | --- | --- | --- |
| `postgres` | `postgres:18.6-alpine` | `deploy/postgres/01-roles.sh` em `docker-entrypoint-initdb.d` cria `pda_owner` (`LOGIN CREATEDB`) e `pda_app` (`LOGIN`) e o banco `pda`, que pertence a `pda_owner`. Porta 5432 publicada | `pg_isready -U postgres -d pda` |
| `keycloak` | `quay.io/keycloak/keycloak:26.7.4` | `start-dev --import-realm`; `KC_HOSTNAME=http://localhost:8080`, `KC_HOSTNAME_BACKCHANNEL_DYNAMIC=true`, `KC_HEALTH_ENABLED=true`; `deploy/keycloak/` montado em `/opt/keycloak/data/import:ro`. Porta 8080 | `bash` + `/dev/tcp` em `:9000/health/ready` ([`../spike-keycloak.md`](../spike-keycloak.md) §5), com `start_period: 60s` |
| `ministack` | `ministackorg/ministack:1.5.18` | `AUTH=true`. Porta 4566 | O embutido da imagem |
| `aws-init` | `amazon/aws-cli:2.36.31` | `entrypoint: bash /deploy/aws/init.sh`, com a chave raiz `test`; monta `deploy/aws:ro` e `./.local/aws` (leitura e escrita); `depends_on: ministack: service_healthy` | Precisa terminar com sucesso |
| `app-1`, `app-2`, `app-3` | `build: .` | Âncora `x-app`; portas `808N:8080` e `909N:9090`; `./.local/aws:/aws:ro`; `AWS_SHARED_CREDENTIALS_FILE=/aws/credentials`; `AWS_PROFILE=pda-wallet-service`; `DATABASE_URL` com `pda_app`; `depends_on`: `postgres` saudável e `aws-init` concluído (`service_completed_successfully`) | `["/pda", "healthcheck"]` |

**`env_file`:** todos os serviços usam `[.env.example, {path: .env, required: false}]`.
- O `.env.example` é a fonte única dos valores locais: senhas do Postgres e do admin do Keycloak, secrets dos clients, `AWS_REGION` e `AWS_ENDPOINT_URL=http://ministack:4566`.
- O `.env`, opcional e já ignorado pelo git, permite sobrescrever sem editar o arquivo versionado.

### 4.2 `pda healthcheck`

- Monta `http://127.0.0.1:<porta de HTTP_ADDR>/health/ready`. Um `HTTP_ADDR` sem host (`:8080`) vira `127.0.0.1`.
- Faz um `GET` com timeout de 3 s.
- Sai com 0 para resposta 200 e com 1 para qualquer outro caso (outro status, conexão recusada ou timeout).
- Lê `HTTP_ADDR` direto do ambiente, sem `config.Load`, para não exigir `DATABASE_URL` só para sondar.

### 4.3 `deploy/aws/init.sh`

É idempotente ([`messaging.md`](../../messaging.md) §2) e roda com `set -euo pipefail`. Os passos:
1. Filas, DLQ com `RedrivePolicy`, tópico FIFO, fila de auditoria e assinatura com `RawMessageDelivery=true` (a assinatura só é criada se ainda não existir).
2. A política de recurso da fila de auditoria, a partir de `policies/wallet-events-audit.json`.
3. Os usuários `pda-wallet-service`, `provider-a` e `provider-b`, criados se não existirem.
   - A política de identidade de cada um vem de `policies/pda-wallet-service.json` ou `policies/provider.json`.
   - Os placeholders `${WAGER_QUEUE_ARN}`, `${DLQ_ARN}`, `${TOPIC_ARN}` e `${AUDIT_QUEUE_ARN}` são substituídos por `sed`, porque a imagem não tem `jq`.
4. Para cada usuário, **reaproveita** a chave do arquivo de credenciais atual se ela ainda existir no IAM. Se não existir, apaga as antigas e cria uma nova. Assim, reexecutar o `aws-init` não invalida réplicas já no ar.
5. Grava `.local/aws/credentials`, em formato INI com um profile por usuário, num arquivo temporário. Depois aplica `chmod 0644`, para o usuário `nonroot` da imagem ler, e renomeia com `mv` (escrita atômica).

**Validação:** `TestProvisioning` (§6.2).

### 4.4 Keycloak

- **`realm-pda.json`:**
  - os 6 clients de D-07, com secrets por placeholder `${ENV}` vindos do `.env.example`;
  - o client `pda-api` com as roles `provider` e `wallet-internal`;
  - o scope `roles` declarado **só** com o mapper `client roles` e `defaultDefaultClientScopes: ["roles"]`;
  - mappers `provider_id` e `aud-pda-api` por client, conforme a tabela de D-07;
  - service accounts com `clientRoles`;
  - `provider-short-lived` com `access.token.lifespan=5`;
  - `webOrigins` com `http://localhost:8081`, `:8082` e `:8083` (D-20).
- **`realm-other.json`:** o realm `other` com o client `other-provider`, que tem role, `provider_id` e audience válidos. Assim só o `iss` e as chaves diferem.
- **Plano B dos placeholders:** se o import do Keycloak 26.7.4 não resolver `${ENV}`, os secrets ficam literais no JSON (são valores locais de teste, `ARCHITECTURE.md` §16.9), e os testes A01–A04 do M3 pegam qualquer divergência com o `.env.example`.

**Validação:** o critério 5 da §1 no M0 e os testes A01–A04 no M3.

### 4.5 Outros artefatos

| Artefato | Conteúdo | Validação |
| --- | --- | --- |
| `Dockerfile` | Conforme [`stack.md`](../../stack.md) §2.3. Build em `golang:1.27.1-alpine` com `CGO_ENABLED=0`, `-trimpath`, `-ldflags="-s -w"` e `GOFLAGS=-mod=readonly`, e `go mod download` numa camada separada. Runtime `distroless/static-debian12:nonroot`, com `EXPOSE 8080 9090` e `ENTRYPOINT ["/pda"]` | `docker compose up --build --wait` |
| `.dockerignore` | `.git`, `.claude`, `.local`, `docs`, `test`, `*.md` e artefatos de cobertura | Build |
| `.editorconfig` | Tabs para `*.go` e `Makefile`; 2 espaços para YAML, JSON e shell; LF; newline final | `make check` |
| `.golangci.yml` | Exatamente a de [`stack.md`](../../stack.md) §4 | `make lint` |
| `.gitignore` | Acrescenta `.local/` | `git check-ignore .local/aws/credentials` |
| `Makefile` | `up`, `down`, `infra-up` (`postgres keycloak ministack aws-init`), `fmt`, `fmt-check`, `lint`, `vet`, `vuln`, `tidy-check`, `go-version-check`, `test`, `test-integration` e `check`, conforme [`stack.md`](../../stack.md) §5 | `make check` |
| `.env.example` | Os valores locais da §4.1; nenhum segredo real (ART-10) | Usado pelo compose e pelo `testkit` |
| `scripts/get-token.sh` | `get-token.sh <client-id> [realm]`: lê o secret do `.env.example` (ou do `.env`) e imprime o `access_token` | Critério 5 da §1 |

---

## 5. Dependências Go adicionadas no M0

As versões fixadas já estão em [`stack.md`](../../stack.md) §2.1:
- `go.uber.org/fx`, `github.com/caarlos0/env/v11` e `github.com/jackc/pgx/v5`;
- `github.com/aws/aws-sdk-go-v2/{config,service/sqs,service/sns}` e `github.com/prometheus/client_golang`;
- só nos testes: `go.uber.org/goleak`.

---

## 6. Testes

Todos com `-race`, o comentário `// Covers:` e o ciclo TDD de [`development-workflow.md`](../../development-workflow.md) §4.2. Os IDs referem-se a [`test-plan.md`](../../test-plan.md) e [`delivery-requirements.md`](../../delivery-requirements.md).

### 6.1 Unitários (`go test -race ./...`, sem Docker)

| Pacote | Comportamentos | Cobre |
| --- | --- | --- |
| `config` | Padrões aplicados; `DATABASE_URL` obrigatória; cada regra da §3.1; vários erros agregados numa execução; erro de conversão e de validação sem o valor (URL com senha, duração inválida) | FX-02, OBS-02 |
| `observability` | `Health`: todos UP → UP; um DOWN → DOWN, com o nome do checker; checker lento → DOWN no timeout; checkers executados em paralelo (tempo total ≈ o maior timeout, não a soma); `NewLogger` recusa nível inválido e emite JSON | HTTP-08, OBS-04 |
| `httpapi` | `/health/live` 200; `/health/ready` 200 e 503 com o JSON da §2.2; o corpo do 503 não contém a mensagem do erro; método diferente de GET → 405 | HTTP-08 |
| `cmd/pda` | `healthcheck`: 200 → 0; 503 → 1; conexão recusada → 1; URL derivada de `HTTP_ADDR` (`:8080`, `127.0.0.1:9000`, `0.0.0.0:8080`) | ART-04 |
| `bootstrap` | **I07a** `TestFxGraph`: `fx.ValidateApp(bootstrap.Options()...)` resolve o grafo sem executar os construtores | FX-01 (parcial) |

### 6.2 Integração (tag `integration`, depois de `make infra-up`)

| Teste | Arquivo | O que prova | Cobre |
| --- | --- | --- | --- |
| **I07b (parcial)** `TestFxLifecycle` | `internal/bootstrap/bootstrap_integration_test.go` | Com o Postgres real (ping no banco `pda` como `pda_app`) e filas isoladas criadas com a chave raiz: `Start`, `/health/ready` 200, `Stop` dentro do prazo, pool fechado e `goleak.VerifyNone` | FX-03..05 (parcial) |
| **I07c** `TestFxFailFast` | idem | Uma `DATABASE_URL` apontando para uma porta fechada e uma fila inexistente fazem o `Start` falhar com um erro que cita a dependência | FX-02 |
| `TestProvisioning` | `test/integration/provisioning_test.go` | Valida o `init.sh`:<br>- as filas de produção existem com `FifoQueue`, a `RedrivePolicy` com `maxReceiveCount=10` e o ARN da DLQ;<br>- o tópico é FIFO e a assinatura da auditoria tem `RawMessageDelivery=true`;<br>- `.local/aws/credentials` tem os 3 profiles;<br>- `pda-wallet-service` consegue `GetQueueAttributes` na fila de entrada, e `provider-a` recebe `AccessDenied` em `ReceiveMessage` | ART-06, AUTH-09 (prévia do I04f) |

**`testkit` no M0** (`test/testkit/`):
- `dotenv.go` lê o `.env.example`, com o `.env` sobrepondo;
- `sqs.go` cria e remove filas isoladas (`wager-<rand>.fifo` e a DLQ com redrive) com a chave raiz.
- O `NewEnv` completo, com banco isolado e migrations, entra no M2.

---

## 7. Riscos do marco

| Risco | Mitigação |
| --- | --- |
| O import do Keycloak não resolve placeholders `${ENV}` | Plano B da §4.4 |
| Permissão do arquivo de credenciais no bind mount (Linux: dono root, leitor uid 65532) | `chmod 0644` no `init.sh`. O `TestProvisioning` lê o arquivo, e o `compose up --wait` exercita a leitura pelo `nonroot` |
| O compose derruba as réplicas se o `aws-init` falhar em outra execução | O script é idempotente e recria as chaves. As réplicas só sobem depois de `service_completed_successfully` |
| A imagem do golangci-lint roda em linux/arm64 com um cache montado do host | O alvo do `Makefile` monta `GOMODCACHE` e o cache do lint, como em [`stack.md`](../../stack.md) §5 |

---

## 8. Requisitos cobertos e ajustes de documentação

**Marcados ao fim do M0**, cada um citando o teste ou a validação:
- ART-01, ART-02, ART-03 (`make check`: `fmt-check`, `tidy-check`, `go-version-check`);
- ART-04 (`compose up --wait`);
- ART-06 (`TestProvisioning`);
- ART-07 (critério 5 da §1);
- ART-10;
- FX-02 (I07c e testes de `config`);
- HTTP-08 (testes de `observability` e `httpapi`, e I07b).
- **Ficam parciais:** FX-01 e AUTH-09, que fecham no M3 e no M5.

**Correção no plano:** o [`implementation-plan.md`](../../implementation-plan.md) M0 dizia "Cobre: ART-01..07", mas **ART-05** (migrations) pertence ao M2. O plano é corrigido para ART-01..04, ART-06, ART-07 e ART-10, e o M2 passa a citar ART-05.

**Ajuste em [`structure.md`](../../structure.md):**
- `observability/health.go` passa a ser descrito como "agregador de checkers (value group); os checkers vêm dos adaptadores";
- `awsclient/queues.go` e `cmd/pda/healthcheck.go` entram na árvore;
- `test/testkit/dotenv.go` também entra.
