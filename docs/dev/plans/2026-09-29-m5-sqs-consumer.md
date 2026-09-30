# M5 — Consumidor SQS: plano de implementação

> **Execução:** `superpowers:executing-plans`, **inline** na própria sessão, sem subagentes ([`development-workflow.md`](../../development-workflow.md) §7). Cada tarefa segue `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Sem commits:** cada tarefa termina num *checkpoint* verificável, e os commits são propostos no fim do marco (§6 do workflow).

**Objetivo:** toda mensagem de `wager-transactions.fifo` passa pelo mesmo caso de uso do HTTP, com a inbox na mesma transação e o `DeleteMessage` só depois do commit; erros permanentes e de entrada vão à DLQ, os transitórios têm backoff e redrive, a queda do banco pausa o consumo e o `SIGTERM` não perde mensagem (**E5 SQS**, AUTH-09).

**Arquitetura:**
- **`app.ConsumeWager`** (spec decisão 1): inbox → `NewCommand` → `ProcessWager` com o `ProcessRequest.Inbox`, que grava a inbox em todo caminho de conclusão (resultado novo, replay, `FAILED`). A corrida na PK da inbox recomeça pelo `Find`.
- **`adapters/sqsconsumer`:** `envelope.go` (decodificação e hash), `handler.go` (`decide`, função pura da tabela de ações), `dlq.go`, `backoff.go`, `batch.go`, `health_gate.go` (pausa por ping), `consumer.go` (pollers, grupos, semáforo, liberação por prazo, shutdown em 5 passos) e `module.go`.
- **Ordem das tarefas:** config → métricas → `app` → partes puras do adapter → gate → `testkit` → consumidor (núcleo, depois grupo/prazo/pausa, depois shutdown e Fx) → ponta a ponta e políticas → encerramento.

**Stack:**
- Go 1.27.1, `pgx/v5`, `aws-sdk-go-v2` (`service/sqs` v1.52.1, já no `go.mod`), Prometheus `client_golang` v1.24.1 e `goleak` v1.3.0 (já no `go.mod`);
- **nova, só nos testes:** `aws-sdk-go-v2/service/iam` v1.64.1 (I04f, spec §2.2 decisão 26);
- PostgreSQL 18.6, Keycloak 26.7.4 e MiniStack 1.5.18 do compose.

**Spec:** [`docs/dev/specs/2026-09-29-m5-sqs-consumer-design.md`](../specs/2026-09-29-m5-sqs-consumer-design.md). Quem executa lê a spec, com os achados da §2.2.

**Validação prévia do plano:** o código abaixo foi escrito e testado numa cópia descartável do repositório (um `git clone` local, sem worktree nem branch), contra a infraestrutura do compose.
- **Cada tarefa:** o teste novo foi visto falhando pelo motivo previsto e depois passando. As versões intermediárias do `consumer.go` (Tarefas 8 e 9) foram testadas separadamente: a da Tarefa 8 passa nos seus testes e falha no `TestDeadlineRelease` e no `TestHealthGatePauses`; a da Tarefa 9 passa nos seus e falha no `TestConsumerShutdown`.
- **Estado final:**
  - `make check` verde (`0 issues.`, `gofmt`/`gofumpt`, `go mod tidy -diff`, `go vet` e `go test -race ./...`);
  - `go test -tags=integration -race -count=1 ./...` verde em 7 execuções seguidas depois das correções (≈ 25 s cada).
- **Sabotagens detectadas (15):** inbox fora da UoW; corrida da inbox sem nova rodada; sem `DeleteMessage` depois do commit; retry que apaga; envio à DLQ que falha e apaga; gate que nunca pausa; sem liberação por prazo; grupo que segue depois de um retry; shutdown que processa o que não começou; stop sem abortar no prazo; política sem `ChangeMessageVisibility`; consumidor fora do grafo; short polling sem pausa; falha de `DeleteMessage` não contada; receive com erro sem espera.
- **Imagem real:** a imagem compilada da cópia rodou na rede do compose com o usuário IAM `pda-wallet-service`. Uma mensagem enviada com as credenciais de `provider-a` virou um BET `PROCESSED` com `receivedVia = SQS`; a inbox registrou `PROCESSED`; os dois eventos foram publicados com `causationId = messageId` e o `correlationId` do atributo; uma mensagem com JSON quebrado chegou à DLQ com `MALFORMED_MESSAGE`; a fila ficou vazia; o `docker stop` parou HTTP → consumidor → publisher, nessa ordem.

Na execução, o código é redigitado seguindo o ciclo red → green de cada tarefa.

**Achados da validação** (registrados na spec §2.2, decisões 17–28). O principal: um long polling cancelado pelo cliente continua aberto no MiniStack e pega a próxima mensagem que ficar visível, escondendo-a por um visibility timeout. Ele explica uma falha intermitente do shutdown (os testes de shutdown usam short polling) e o flake preexistente do I05b do M4 (`Audit.Absent`, corrigido na Tarefa 7 com um teste que o reproduz de forma determinística).

## Restrições globais

- Module path `github.com/KaioVinicios/pda`; `go 1.27.1`. A única dependência nova é `aws-sdk-go-v2/service/iam` v1.64.1, importada só em `test/`.
- **Camadas:** `internal/app` ganha `ConsumeWager`, `InboxReceipt` e o campo `ProcessRequest.Inbox`, sem importar AWS nem pgx; `adapters/sqsconsumer` importa `app`, `apperrors`, `awsclient`, `config`, `observability` e `pgxpool` (o `Pinger`), nunca `adapters/postgres` (os testes podem).
- **Dinheiro nunca em `float`:** fora de `internal/observability`, nenhum `float64`; o helper de métricas dos testes devolve `int`.
- **Logs:** chaves em `camelCase`, mensagens estáticas (`sloglint`), nunca o corpo da mensagem: só `sqsMessageId`, `errorCode`, `errorCategory`, `retryIn` e o erro.
- **Contextos:** nunca em struct (`containedctx`); o `Consumer.Start(ctx)` deriva os seus com `context.WithoutCancel` (`contextcheck`). Ações na fila (delete, visibilidade, DLQ) rodam com contexto novo de 2 s, desacoplado do cancelamento do trabalho.
- **Testes de integração:** tag `integration` e `make infra-up` no ar; filas isoladas **por teste** no pacote `sqsconsumer`; `-race`; `// Covers: <IDs>` em todo teste; só `testing` da stdlib; esperas sempre com prazo (`testkit.Eventually`, `ReceiveDLQ`, `AssertQueueDrained`). Exceções justificadas de `time.Sleep`: janelas negativas ("nada acontece durante X") no `TestHealthGatePauses` e no `TestAuditAbsentLeavesNoPollBehind`.
- **`make lint` e `make fmt`** usam a imagem `golangci/golangci-lint:v2.14.0`, então o Docker precisa estar rodando. Os blocos de código abaixo já estão formatados pelo `gofumpt`.
- **Sem commits, branches ou worktrees.**

## Foco de revisão

Os cinco casos que a spec implica, mas não detalha, com mais chance de causar problema. Cada um tem teste na tarefa indicada:

1. **Broker fora durante o `ReceiveMessage`.** Esperado: o poller não morre nem gira em laço; espera 1 s, 2 s… até 30 s, e conta `sqs_receive_errors_total`. → Tarefa 8 (`TestReceiveFailureBackoff`).
2. **`DeleteMessage` falhando depois do commit.** Esperado: nada quebra, a falha é contada em `sqs_delete_errors_total` e a reentrega cai na inbox como duplicata. → Tarefa 8 (`TestDeleteFailureIsCounted`; a duplicata é o I04a).
3. **`SQS_WAIT_TIME=0` (aceito pelo config).** Esperado: sem laço sem espera; um receive vazio a cada 100 ms. → Tarefa 8 (`TestShortPollingPauses`).
4. **Produtor mandando `data` com tipo errado ou `null`.** Esperado: tipo errado é `MALFORMED_MESSAGE`; `null` e ausente têm o mesmo hash e caem na validação do domínio. → Tarefa 4 (`TestParseEnvelope`, `TestMessageHash`).
5. **Mensagem que chega logo depois de um receive cancelado.** Esperado: ela é recebida em seguida, não some por um visibility timeout. → Tarefa 7 (`TestAuditAbsentLeavesNoPollBehind`) e Tarefa 10 (testes de shutdown com short polling; a limitação no consumidor está no messaging §4.5).

---

## Mapa de arquivos

| Arquivo | Responsabilidade | Tarefa |
| --- | --- | --- |
| `internal/config/config.go`, `validate.go`, `config_test.go` | 7 variáveis `SQS_*` e a regra processamento < visibility | 1 |
| `test/testkit/env.go`, `internal/bootstrap/bootstrap_integration_test.go` | Tempos de teste do SQS; config do `bootstrap` a partir do `Env.Config()` | 1 |
| `internal/observability/metrics.go`, `metrics_test.go` | 9 métricas de SQS | 2 |
| `internal/app/process_wager.go` | `ProcessRequest.Inbox`, `InboxReceipt`, `recordInbox` em todo caminho | 3 |
| `internal/app/consume_wager.go` (novo) | `ConsumeWager`, `WagerMessage`, `ConsumeResult`, `ConsumerName`, `CodeMessageHashMismatch` | 3 |
| `internal/app/consume_wager_integration_test.go` (novo), `helpers_integration_test.go`, `faults_integration_test.go` | Testes do `ConsumeWager`; helper `input`; decorador da inbox | 3 |
| `internal/adapters/sqsconsumer/envelope.go` + teste | Decodificação em duas fases, hash, `correlationID` | 4 |
| `internal/adapters/sqsconsumer/handler.go`, `backoff.go`, `dlq.go`, `batch.go` + `handler_test.go` | `decide`, `retryDelay`, `dlqInput`, `groupBatch` | 5 |
| `internal/adapters/sqsconsumer/health_gate.go` + teste | Pausa por ping | 6 |
| `test/testkit/sqs.go` (novo), `audit.go`, `test/integration/harness_test.go` | Helpers de SQS; `Absent` sem cancelar receives | 7 |
| `internal/adapters/sqsconsumer/consumer.go` + `consumer_test.go`, `main_`/`helpers_`/`consumer_`/`failures_integration_test.go` | Consumidor (núcleo) e I04a–d, I03b-SQS, falha no envio à DLQ | 8 |
| `internal/adapters/sqsconsumer/order_integration_test.go` | Ordem do grupo, liberação por prazo, pausa | 9 |
| `internal/adapters/sqsconsumer/module.go`, `stop_integration_test.go`; `internal/bootstrap/*` | Shutdown em 5 passos, módulo Fx, grafo | 10 |
| `test/testkit/app.go`, `iam.go` (novo); `test/integration/sqs_test.go`, `policies_test.go` (novos); `go.mod` | I04e, ponta a ponta pelo SQS, I04f | 11 |
| `docs/*`, `ARCHITECTURE.md`, diário | Encerramento | 12 |

---
## Tarefa 1: Configuração do consumidor

**Arquivos:**
- Modificar: `internal/config/config.go`, `internal/config/validate.go`, `internal/config/config_test.go`
- Modificar: `test/testkit/env.go`, `internal/bootstrap/bootstrap_integration_test.go`

**Interfaces:**
- Produz: `config.Config.{SQSConsumerPollers, SQSReceiveBatch, SQSWaitTime, SQSVisibilityTimeout, SQSProcessingTimeout, SQSMaxInFlight, SQSRetryMaxDelay}` (spec §5.1) e os tempos de teste no `testkit.Env.Config()` (visibility 5 s, processamento 3 s, wait 1 s, retry máximo 1 s).

- [ ] **Passo 1: escrever os testes.** Em `internal/config/config_test.go`:

```diff
@@ -17,6 +17,8 @@ var allVars = []string{
 	"REFERENCE_RETRY_BASE_DELAY", "REFERENCE_RETRY_MAX_DELAY", "REFERENCE_MAX_ATTEMPTS", "REFERENCE_TTL",
 	"SNS_EVENTS_TOPIC_NAME", "OUTBOX_BATCH_SIZE", "OUTBOX_LEASE", "OUTBOX_POLL_INTERVAL", "OUTBOX_CONCURRENCY",
 	"OUTBOX_RETRY_BASE_DELAY", "OUTBOX_RETRY_MAX_DELAY",
+	"SQS_CONSUMER_POLLERS", "SQS_RECEIVE_BATCH", "SQS_WAIT_TIME", "SQS_VISIBILITY_TIMEOUT",
+	"SQS_PROCESSING_TIMEOUT", "SQS_MAX_IN_FLIGHT", "SQS_RETRY_MAX_DELAY",
 }
 
 const (
@@ -56,6 +58,9 @@ func validConfig() config.Config {
 		SNSEventsTopicName: "wallet-events.fifo",
 		OutboxBatchSize:    50, OutboxLease: 30 * time.Second, OutboxPollInterval: 500 * time.Millisecond,
 		OutboxConcurrency: 8, OutboxRetryBaseDelay: time.Second, OutboxRetryMaxDelay: 5 * time.Minute,
+		SQSConsumerPollers: 2, SQSReceiveBatch: 10, SQSWaitTime: 20 * time.Second,
+		SQSVisibilityTimeout: 30 * time.Second, SQSProcessingTimeout: 10 * time.Second,
+		SQSMaxInFlight: 16, SQSRetryMaxDelay: 300 * time.Second,
 	}
 }
 
@@ -118,6 +123,9 @@ func TestLoad_ReadsEnvironment(t *testing.T) {
 		"REFERENCE_TTL": "3s", "SNS_EVENTS_TOPIC_NAME": "events.fifo", "OUTBOX_BATCH_SIZE": "10",
 		"OUTBOX_LEASE": "2s", "OUTBOX_POLL_INTERVAL": "100ms", "OUTBOX_CONCURRENCY": "2",
 		"OUTBOX_RETRY_BASE_DELAY": "100ms", "OUTBOX_RETRY_MAX_DELAY": "1s",
+		"SQS_CONSUMER_POLLERS": "1", "SQS_RECEIVE_BATCH": "5", "SQS_WAIT_TIME": "1s",
+		"SQS_VISIBILITY_TIMEOUT": "5s", "SQS_PROCESSING_TIMEOUT": "3s", "SQS_MAX_IN_FLIGHT": "4",
+		"SQS_RETRY_MAX_DELAY": "1s",
 	}
 	for k, v := range env {
 		t.Setenv(k, v)
@@ -138,6 +146,9 @@ func TestLoad_ReadsEnvironment(t *testing.T) {
 		SNSEventsTopicName: "events.fifo", OutboxBatchSize: 10, OutboxLease: 2 * time.Second,
 		OutboxPollInterval: 100 * time.Millisecond, OutboxConcurrency: 2,
 		OutboxRetryBaseDelay: 100 * time.Millisecond, OutboxRetryMaxDelay: time.Second,
+		SQSConsumerPollers: 1, SQSReceiveBatch: 5, SQSWaitTime: time.Second,
+		SQSVisibilityTimeout: 5 * time.Second, SQSProcessingTimeout: 3 * time.Second,
+		SQSMaxInFlight: 4, SQSRetryMaxDelay: time.Second,
 	}
 	if got != want {
 		t.Fatalf("Load() = %+v, want %+v", got, want)
@@ -197,6 +208,20 @@ func TestValidate_RejectsInvalidValues(t *testing.T) {
 		{"zero outbox base delay", func(c *config.Config) { c.OutboxRetryBaseDelay = 0 }, "OUTBOX_RETRY_BASE_DELAY"},
 		{"outbox max below base", func(c *config.Config) { c.OutboxRetryMaxDelay = c.OutboxRetryBaseDelay / 2 }, "OUTBOX_RETRY_MAX_DELAY"},
 		{"outbox max above a day", func(c *config.Config) { c.OutboxRetryMaxDelay = 25 * time.Hour }, "OUTBOX_RETRY_MAX_DELAY"},
+		{"zero pollers", func(c *config.Config) { c.SQSConsumerPollers = 0 }, "SQS_CONSUMER_POLLERS"},
+		{"zero receive batch", func(c *config.Config) { c.SQSReceiveBatch = 0 }, "SQS_RECEIVE_BATCH"},
+		{"receive batch above 10", func(c *config.Config) { c.SQSReceiveBatch = 11 }, "SQS_RECEIVE_BATCH"},
+		{"negative wait time", func(c *config.Config) { c.SQSWaitTime = -time.Second }, "SQS_WAIT_TIME"},
+		{"wait time above 20s", func(c *config.Config) { c.SQSWaitTime = 21 * time.Second }, "SQS_WAIT_TIME"},
+		{"wait time not in seconds", func(c *config.Config) { c.SQSWaitTime = 1500 * time.Millisecond }, "SQS_WAIT_TIME"},
+		{"visibility below 1s", func(c *config.Config) { c.SQSVisibilityTimeout = 500 * time.Millisecond }, "SQS_VISIBILITY_TIMEOUT"},
+		{"visibility above 12h", func(c *config.Config) { c.SQSVisibilityTimeout = 13 * time.Hour }, "SQS_VISIBILITY_TIMEOUT"},
+		{"visibility not in seconds", func(c *config.Config) { c.SQSVisibilityTimeout = 30500 * time.Millisecond }, "SQS_VISIBILITY_TIMEOUT"},
+		{"zero processing timeout", func(c *config.Config) { c.SQSProcessingTimeout = 0 }, "SQS_PROCESSING_TIMEOUT"},
+		{"processing at visibility", func(c *config.Config) { c.SQSProcessingTimeout = c.SQSVisibilityTimeout }, "SQS_PROCESSING_TIMEOUT"},
+		{"zero max in flight", func(c *config.Config) { c.SQSMaxInFlight = 0 }, "SQS_MAX_IN_FLIGHT"},
+		{"retry max below 1s", func(c *config.Config) { c.SQSRetryMaxDelay = 500 * time.Millisecond }, "SQS_RETRY_MAX_DELAY"},
+		{"retry max above 12h", func(c *config.Config) { c.SQSRetryMaxDelay = 13 * time.Hour }, "SQS_RETRY_MAX_DELAY"},
 	}
 	for _, tc := range cases {
 		t.Run(tc.name, func(t *testing.T) {
```

- [ ] **Passo 2: stub — só os campos.** Em `internal/config/config.go`, acrescentar ao fim da `Config` (sem validação ainda):

```diff
@@ -52,6 +52,15 @@ type Config struct {
 	OutboxConcurrency    int           `env:"OUTBOX_CONCURRENCY" envDefault:"8"`
 	OutboxRetryBaseDelay time.Duration `env:"OUTBOX_RETRY_BASE_DELAY" envDefault:"1s"`
 	OutboxRetryMaxDelay  time.Duration `env:"OUTBOX_RETRY_MAX_DELAY" envDefault:"5m"`
+
+	// SQS consumer (D-12, messaging.md §4.1).
+	SQSConsumerPollers   int           `env:"SQS_CONSUMER_POLLERS" envDefault:"2"`
+	SQSReceiveBatch      int           `env:"SQS_RECEIVE_BATCH" envDefault:"10"`
+	SQSWaitTime          time.Duration `env:"SQS_WAIT_TIME" envDefault:"20s"`
+	SQSVisibilityTimeout time.Duration `env:"SQS_VISIBILITY_TIMEOUT" envDefault:"30s"`
+	SQSProcessingTimeout time.Duration `env:"SQS_PROCESSING_TIMEOUT" envDefault:"10s"`
+	SQSMaxInFlight       int           `env:"SQS_MAX_IN_FLIGHT" envDefault:"16"`
+	SQSRetryMaxDelay     time.Duration `env:"SQS_RETRY_MAX_DELAY" envDefault:"300s"`
 }
 
 // Load reads the environment and validates it. Errors name variables, never values.
```

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -race ./internal/config/`
Esperado: FAIL em 14 subtestes de `TestValidate_RejectsInvalidValues`, com `Validate() vars = [], want [SQS_…]`. `TestLoad_AppliesDefaults` e `TestLoad_ReadsEnvironment` já passam, porque os campos têm padrão.

- [ ] **Passo 4: implementar a validação.** Em `internal/config/validate.go`:

```diff
@@ -80,6 +80,7 @@ func (c Config) Validate() error {
 		fail("REFERENCE_TTL", "must be greater than 0")
 	}
 	c.validateOutbox(fail)
+	c.validateConsumer(fail)
 	return errors.Join(errs...)
 }
 
@@ -108,7 +109,41 @@ func (c Config) validateOutbox(fail func(v, reason string)) {
 	}
 }
 
+// validateConsumer checks the SQS consumer settings (messaging.md §4.1). SQS
+// takes the wait and the visibility in whole seconds.
+func (c Config) validateConsumer(fail func(v, reason string)) {
+	if c.SQSConsumerPollers < 1 {
+		fail("SQS_CONSUMER_POLLERS", "must be at least 1")
+	}
+	if c.SQSReceiveBatch < 1 || c.SQSReceiveBatch > maxReceiveBatch {
+		fail("SQS_RECEIVE_BATCH", "must be between 1 and 10")
+	}
+	if c.SQSWaitTime < 0 || c.SQSWaitTime > maxWaitTime || c.SQSWaitTime%time.Second != 0 {
+		fail("SQS_WAIT_TIME", "must be whole seconds between 0s and 20s")
+	}
+	visibilityOK := c.SQSVisibilityTimeout >= time.Second && c.SQSVisibilityTimeout <= maxVisibility &&
+		c.SQSVisibilityTimeout%time.Second == 0
+	if !visibilityOK {
+		fail("SQS_VISIBILITY_TIMEOUT", "must be whole seconds between 1s and 12h")
+	}
+	// Compared only with a valid visibility, so one mistake is reported once.
+	if c.SQSProcessingTimeout <= 0 || (visibilityOK && c.SQSProcessingTimeout >= c.SQSVisibilityTimeout) {
+		fail("SQS_PROCESSING_TIMEOUT", "must be greater than 0 and less than SQS_VISIBILITY_TIMEOUT")
+	}
+	if c.SQSMaxInFlight < 1 {
+		fail("SQS_MAX_IN_FLIGHT", "must be at least 1")
+	}
+	if c.SQSRetryMaxDelay < time.Second || c.SQSRetryMaxDelay > maxVisibility {
+		fail("SQS_RETRY_MAX_DELAY", "must be between 1s and 12h")
+	}
+}
+
 const (
+	// maxReceiveBatch, maxWaitTime and maxVisibility are the limits of
+	// ReceiveMessage and ChangeMessageVisibility.
+	maxReceiveBatch = 10
+	maxWaitTime     = 20 * time.Second
+	maxVisibility   = 12 * time.Hour
 	// maxRetryDelay bounds the retry delays; it is the upper bound
 	// wagering.NewReferenceRetryPolicy accepts.
 	maxRetryDelay = 24 * time.Hour
```

- [ ] **Passo 5: ver passar.**

Rodar: `go test -race ./internal/config/`
Esperado: `ok`.

- [ ] **Passo 6: tempos de teste no `testkit` e config do `bootstrap`.** O `StartApp` e os testes do `bootstrap` chamam `cfg.Validate()`; sem os campos novos, falhariam. Em `test/testkit/env.go`:

```diff
@@ -80,5 +80,12 @@ func (e *Env) Config() config.Config {
 		OutboxConcurrency:    8,
 		OutboxRetryBaseDelay: 100 * time.Millisecond,
 		OutboxRetryMaxDelay:  time.Second,
+		SQSConsumerPollers:   2,
+		SQSReceiveBatch:      10,
+		SQSWaitTime:          time.Second,
+		SQSVisibilityTimeout: 5 * time.Second,
+		SQSProcessingTimeout: 3 * time.Second,
+		SQSMaxInFlight:       16,
+		SQSRetryMaxDelay:     time.Second,
 	}
 }
```

Em `internal/bootstrap/bootstrap_integration_test.go`, o `integrationConfig` passa a partir do `Env.Config()` (spec §2.2, decisão 27: sem isso, o consumidor do grafo subiria com 0 pollers):

```diff
@@ -32,17 +32,15 @@ func integrationConfig(t *testing.T) config.Config {
 	root := testkit.RootAWSConfig(t)
 	wager, dlq := testkit.CreateQueues(t, sqs.NewFromConfig(root))
 	topic := testkit.NewEventsTopic(t, sqs.NewFromConfig(root), sns.NewFromConfig(root))
-	cfg := config.Config{
-		LogLevel: "error", HTTPAddr: testkit.FreeAddr(t), MetricsAddr: testkit.FreeAddr(t),
-		ShutdownTimeout: 5 * time.Second, DatabaseURL: env.DB.AppURL, DBMaxConns: 2,
-		DBLockTimeout: 2 * time.Second, WagerQueueName: wager, WagerDLQName: dlq,
-		OIDCIssuer: testkit.KeycloakIssuer, OIDCJWKSURL: testkit.KeycloakIssuer + "/protocol/openid-connect/certs",
-		OIDCAudience: "pda-api", OIDCClockSkew: time.Second, APIDocsEnabled: true,
-		ReferenceRetryBaseDelay: 100 * time.Millisecond, ReferenceRetryMaxDelay: time.Second,
-		ReferenceMaxAttempts: 3, ReferenceTTL: 3 * time.Second,
-		SNSEventsTopicName: topic.Name, OutboxBatchSize: 50, OutboxLease: 2 * time.Second,
-		OutboxPollInterval: 100 * time.Millisecond, OutboxConcurrency: 8,
-		OutboxRetryBaseDelay: 100 * time.Millisecond, OutboxRetryMaxDelay: time.Second,
+	cfg := env.Config() // the accelerated times of test-plan §3.3
+	cfg.HTTPAddr, cfg.MetricsAddr, cfg.DBMaxConns = testkit.FreeAddr(t), testkit.FreeAddr(t), 2
+	cfg.WagerQueueName, cfg.WagerDLQName, cfg.SNSEventsTopicName = wager, dlq, topic.Name
+	cfg.OIDCIssuer, cfg.OIDCJWKSURL = testkit.KeycloakIssuer, testkit.KeycloakIssuer+"/protocol/openid-connect/certs"
+	cfg.OIDCAudience, cfg.OIDCClockSkew, cfg.APIDocsEnabled = "pda-api", time.Second, true
+	cfg.ReferenceRetryBaseDelay, cfg.ReferenceRetryMaxDelay = 100*time.Millisecond, time.Second
+	cfg.ReferenceMaxAttempts, cfg.ReferenceTTL = 3, 3*time.Second
+	if err := cfg.Validate(); err != nil {
+		t.Fatalf("integration config: %v", err)
 	}
 	t.Setenv("DATABASE_URL", cfg.DatabaseURL) // config.Load stays valid even if Fx calls it
 	return cfg
```

- [ ] **Checkpoint:** `go test -race ./internal/config/` e `go test -tags=integration -race -count=1 ./internal/bootstrap/` verdes.

---

## Tarefa 2: Métricas de SQS

**Arquivos:**
- Modificar: `internal/observability/metrics.go`, `internal/observability/metrics_test.go`

**Interfaces:**
- Produz: métodos de `*observability.Metrics` que implementam `sqsconsumer.Metrics` (Tarefa 8): `Received()`, `Processed(outcome string, d time.Duration)`, `Duplicate(layer string)`, `Retried(reason string)`, `SentToDLQ(reason string)`, `DLQDepth(queue string, n int)`, `ReceiveFailed()`, `DeleteFailed()`.

- [ ] **Passo 1: escrever o teste.** Acrescentar a `internal/observability/metrics_test.go`:

```diff
@@ -77,3 +77,67 @@ outbox_published_total{event_type="WalletBalanceChanged"} 2
 	}
 	t.Fatal("outbox_publish_lag_seconds not registered")
 }
+
+// Covers: OBS-03, SQS-03, SQS-07 (spec M5, decision 15; messaging.md §8)
+func TestMetrics_SQS(t *testing.T) {
+	reg := observability.NewRegistry()
+	m := observability.NewMetrics(reg)
+	m.Received()
+	m.Received()
+	m.Processed("processed", 40*time.Millisecond)
+	m.Processed("replay", 10*time.Millisecond)
+	m.Duplicate("inbox")
+	m.Retried("transient")
+	m.Retried("deadline_release")
+	m.SentToDLQ("UNKNOWN_WALLET")
+	m.DLQDepth("wager-transactions-dlq.fifo", 3)
+	m.ReceiveFailed()
+	m.DeleteFailed()
+
+	want := `
+# HELP sqs_delete_errors_total DeleteMessage calls that failed after the message was concluded.
+# TYPE sqs_delete_errors_total counter
+sqs_delete_errors_total 1
+# HELP sqs_dlq_depth Approximate number of messages in the dead-letter queue.
+# TYPE sqs_dlq_depth gauge
+sqs_dlq_depth{queue="wager-transactions-dlq.fifo"} 3
+# HELP sqs_dlq_sent_total Messages sent explicitly to the dead-letter queue.
+# TYPE sqs_dlq_sent_total counter
+sqs_dlq_sent_total{reason="UNKNOWN_WALLET"} 1
+# HELP sqs_messages_processed_total Messages concluded by the wager use case.
+# TYPE sqs_messages_processed_total counter
+sqs_messages_processed_total{outcome="processed"} 1
+sqs_messages_processed_total{outcome="replay"} 1
+# HELP sqs_messages_received_total Messages received from the wager queue.
+# TYPE sqs_messages_received_total counter
+sqs_messages_received_total 2
+# HELP sqs_receive_errors_total ReceiveMessage calls that failed.
+# TYPE sqs_receive_errors_total counter
+sqs_receive_errors_total 1
+# HELP sqs_retries_total Messages returned to the queue to be received again.
+# TYPE sqs_retries_total counter
+sqs_retries_total{reason="deadline_release"} 1
+sqs_retries_total{reason="transient"} 1
+# HELP wager_duplicates_total Repeated deliveries of an operation, by channel and deduplication layer.
+# TYPE wager_duplicates_total counter
+wager_duplicates_total{channel="sqs",layer="inbox"} 1
+`
+	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "sqs_delete_errors_total", "sqs_dlq_depth",
+		"sqs_dlq_sent_total", "sqs_messages_processed_total", "sqs_messages_received_total", "sqs_receive_errors_total",
+		"sqs_retries_total", "wager_duplicates_total"); err != nil {
+		t.Fatal(err)
+	}
+	families, err := reg.Gather()
+	if err != nil {
+		t.Fatal(err)
+	}
+	for _, f := range families {
+		if f.GetName() == "sqs_processing_duration_seconds" {
+			if n := len(f.GetMetric()); n != 2 {
+				t.Fatalf("sqs_processing_duration_seconds series = %d, want 2 (one per outcome)", n)
+			}
+			return
+		}
+	}
+	t.Fatal("sqs_processing_duration_seconds not registered")
+}
```

- [ ] **Passo 2: stub.** Acrescentar a `metrics.go` os 8 métodos com corpo vazio (`func (m *Metrics) Received() {}` etc.), para o teste compilar.

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -race -run TestMetrics_SQS ./internal/observability/`
Esperado: FAIL no `GatherAndCompare` (as métricas não estão registradas).

- [ ] **Passo 4: implementar.** Substituir os stubs pelo diff completo de `internal/observability/metrics.go`:

```diff
@@ -17,7 +17,8 @@ func NewRegistry() *prometheus.Registry {
 	return reg
 }
 
-// Metrics implements app.Metrics and the outbox publisher's port with
+// Metrics implements app.Metrics and the ports of the outbox publisher and of
+// the SQS consumer with
 // Prometheus collectors. M7 adds the rest of the catalog (ARCHITECTURE.md §13.2).
 type Metrics struct {
 	reconciliationDivergences prometheus.Counter
@@ -28,6 +29,16 @@ type Metrics struct {
 	outboxPending        prometheus.Gauge
 	outboxOldestPending  prometheus.Gauge
 	outboxPublishLatency *prometheus.HistogramVec
+
+	sqsReceived      prometheus.Counter
+	sqsProcessed     *prometheus.CounterVec
+	sqsDuration      *prometheus.HistogramVec
+	wagerDuplicates  *prometheus.CounterVec
+	sqsRetries       *prometheus.CounterVec
+	sqsDLQSent       *prometheus.CounterVec
+	sqsDLQDepth      *prometheus.GaugeVec
+	sqsReceiveErrors prometheus.Counter
+	sqsDeleteErrors  prometheus.Counter
 }
 
 // NewMetrics registers the collectors on reg.
@@ -62,9 +73,48 @@ func NewMetrics(reg *prometheus.Registry) *Metrics {
 			Help:    "Time from the occurrence of an event to its confirmed publication.",
 			Buckets: prometheus.ExponentialBucketsRange(0.005, 60, 12),
 		}, []string{"event_type"}),
+		sqsReceived: prometheus.NewCounter(prometheus.CounterOpts{
+			Name: "sqs_messages_received_total",
+			Help: "Messages received from the wager queue.",
+		}),
+		sqsProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
+			Name: "sqs_messages_processed_total",
+			Help: "Messages concluded by the wager use case.",
+		}, []string{"outcome"}),
+		sqsDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
+			Name:    "sqs_processing_duration_seconds",
+			Help:    "Time to conclude a message, from the start of its processing to its outcome.",
+			Buckets: prometheus.ExponentialBucketsRange(0.005, 10, 12),
+		}, []string{"outcome"}),
+		wagerDuplicates: prometheus.NewCounterVec(prometheus.CounterOpts{
+			Name: "wager_duplicates_total",
+			Help: "Repeated deliveries of an operation, by channel and deduplication layer.",
+		}, []string{"channel", "layer"}),
+		sqsRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
+			Name: "sqs_retries_total",
+			Help: "Messages returned to the queue to be received again.",
+		}, []string{"reason"}),
+		sqsDLQSent: prometheus.NewCounterVec(prometheus.CounterOpts{
+			Name: "sqs_dlq_sent_total",
+			Help: "Messages sent explicitly to the dead-letter queue.",
+		}, []string{"reason"}),
+		sqsDLQDepth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
+			Name: "sqs_dlq_depth",
+			Help: "Approximate number of messages in the dead-letter queue.",
+		}, []string{"queue"}),
+		sqsReceiveErrors: prometheus.NewCounter(prometheus.CounterOpts{
+			Name: "sqs_receive_errors_total",
+			Help: "ReceiveMessage calls that failed.",
+		}),
+		sqsDeleteErrors: prometheus.NewCounter(prometheus.CounterOpts{
+			Name: "sqs_delete_errors_total",
+			Help: "DeleteMessage calls that failed after the message was concluded.",
+		}),
 	}
 	reg.MustRegister(m.reconciliationDivergences, m.outboxPublished, m.outboxPublishFailed,
-		m.outboxLeaseReclaims, m.outboxPending, m.outboxOldestPending, m.outboxPublishLatency)
+		m.outboxLeaseReclaims, m.outboxPending, m.outboxOldestPending, m.outboxPublishLatency,
+		m.sqsReceived, m.sqsProcessed, m.sqsDuration, m.wagerDuplicates, m.sqsRetries, m.sqsDLQSent,
+		m.sqsDLQDepth, m.sqsReceiveErrors, m.sqsDeleteErrors)
 	return m
 }
 
@@ -90,3 +140,30 @@ func (m *Metrics) Backlog(pending int, oldestAge time.Duration) {
 	m.outboxPending.Set(float64(pending))
 	m.outboxOldestPending.Set(oldestAge.Seconds())
 }
+
+// Received counts a message received from the wager queue.
+func (m *Metrics) Received() { m.sqsReceived.Inc() }
+
+// Processed counts a message concluded by the use case and its duration.
+func (m *Metrics) Processed(outcome string, d time.Duration) {
+	m.sqsProcessed.WithLabelValues(outcome).Inc()
+	m.sqsDuration.WithLabelValues(outcome).Observe(d.Seconds())
+}
+
+// Duplicate counts a repeated SQS delivery caught by layer (inbox or idempotency).
+func (m *Metrics) Duplicate(layer string) { m.wagerDuplicates.WithLabelValues("sqs", layer).Inc() }
+
+// Retried counts a message returned to the queue.
+func (m *Metrics) Retried(reason string) { m.sqsRetries.WithLabelValues(reason).Inc() }
+
+// SentToDLQ counts an explicit send to the dead-letter queue.
+func (m *Metrics) SentToDLQ(reason string) { m.sqsDLQSent.WithLabelValues(reason).Inc() }
+
+// DLQDepth sets the approximate depth of the dead-letter queue.
+func (m *Metrics) DLQDepth(queue string, n int) { m.sqsDLQDepth.WithLabelValues(queue).Set(float64(n)) }
+
+// ReceiveFailed counts a failed ReceiveMessage.
+func (m *Metrics) ReceiveFailed() { m.sqsReceiveErrors.Inc() }
+
+// DeleteFailed counts a DeleteMessage that failed after the message was concluded.
+func (m *Metrics) DeleteFailed() { m.sqsDeleteErrors.Inc() }
```

- [ ] **Checkpoint:** `go test -race ./internal/observability/` verde.

---

## Tarefa 3: `app.ConsumeWager` e a inbox no `ProcessWager`

**Arquivos:**
- Criar: `internal/app/consume_wager.go`, `internal/app/consume_wager_integration_test.go`
- Modificar: `internal/app/process_wager.go`, `internal/app/helpers_integration_test.go`, `internal/app/faults_integration_test.go`

**Interfaces:**
- Consome: `app.InboxRepository` e `app.ErrInboxDuplicate` (M2), `ProcessWager` (M3).
- Produz (usado nas Tarefas 4, 5, 8, 10):
  - `app.ConsumerName = "wager-transactions-consumer"`, `app.CodeMessageHashMismatch = "MESSAGE_HASH_MISMATCH"`;
  - `type WagerMessage struct{ MessageID, MessageHash, MessageType, CorrelationID string; Input wagering.Input; ReceivedAt time.Time }`;
  - `type ConsumeResult struct{ Duplicate bool; Result ProcessResult }`;
  - `func NewConsumeWager(reads Repos, wagers *ProcessWager) *ConsumeWager` e `func (c *ConsumeWager) Execute(ctx, WagerMessage) (ConsumeResult, error)`;
  - `ProcessRequest.Inbox *InboxReceipt`, `type InboxReceipt struct{ Consumer, MessageID, MessageHash, MessageType string; ReceivedAt time.Time }`.

- [ ] **Passo 1: helpers dos testes.** Em `internal/app/helpers_integration_test.go`, extrair o `input` do `command` (o `ConsumeWager` recebe o `wagering.Input` cru):

```diff
@@ -134,6 +134,15 @@ type op struct {
 
 func command(t *testing.T, w wallet.Wallet, o op) wagering.Command {
 	t.Helper()
+	cmd, err := wagering.NewCommand(input(w, o))
+	if err != nil {
+		t.Fatalf("NewCommand %+v: %v", o, err)
+	}
+	return cmd
+}
+
+// input is the raw operation of o on w, as an edge decodes it.
+func input(w wallet.Wallet, o op) wagering.Input {
 	key, currency := o.key, o.currency
 	if key == "" {
 		key = o.provider + ":" + o.ext
@@ -149,11 +158,7 @@ func command(t *testing.T, w wallet.Wallet, o op) wagering.Command {
 	if o.ref != "" {
 		in.ReferenceExternalTransactionID = ptr(o.ref)
 	}
-	cmd, err := wagering.NewCommand(in)
-	if err != nil {
-		t.Fatalf("NewCommand %+v: %v", o, err)
-	}
-	return cmd
+	return in
 }
 
 func request(cmd wagering.Command) app.ProcessRequest {
```

Em `internal/app/faults_integration_test.go`, o decorador da inbox:

```diff
@@ -27,13 +27,28 @@ func (u *faultyUoW) Do(ctx context.Context, fn func(app.Repos) error) error {
 	return u.UnitOfWork.Do(ctx, func(r app.Repos) error { return fn(u.wrap(r, call)) })
 }
 
-// faultyRepos replaces the transaction or outbox repository when set.
+// faultyRepos replaces the transaction, outbox or inbox repository when set.
 type faultyRepos struct {
 	app.Repos
 	tx     app.TransactionRepository
 	outbox app.OutboxRepository
+	inbox  app.InboxRepository
 }
 
+func (f faultyRepos) Inbox() app.InboxRepository {
+	if f.inbox != nil {
+		return f.inbox
+	}
+	return f.Repos.Inbox()
+}
+
+type failingInbox struct {
+	app.InboxRepository
+	err error
+}
+
+func (f failingInbox) Insert(context.Context, app.InboxMessage) error { return f.err }
+
 func (f faultyRepos) Transactions() app.TransactionRepository {
 	if f.tx != nil {
 		return f.tx
```

- [ ] **Passo 2: escrever os testes.** Criar `internal/app/consume_wager_integration_test.go`:

```go
//go:build integration

package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func newConsumeWager() *app.ConsumeWager { return app.NewConsumeWager(reads(), newProcessWager()) }

// message is o on w as a WagerTransactionRequested with messageID. The hash
// stands for the adapter's: any 64 hex digits that change with the content.
func message(w wallet.Wallet, o op, messageID string) app.WagerMessage {
	in := input(w, o)
	sum := sha256.Sum256([]byte(o.provider + o.kind + o.amount + o.ext + o.ref + o.key))
	return app.WagerMessage{
		MessageID: messageID, MessageHash: hex.EncodeToString(sum[:]), MessageType: "WagerTransactionRequested",
		CorrelationID: "corr-" + messageID, Input: in, ReceivedAt: time.Now(),
	}
}

// consume runs the message and fails the test on error.
func consume(t *testing.T, c *app.ConsumeWager, m app.WagerMessage) app.ConsumeResult {
	t.Helper()
	res, err := c.Execute(t.Context(), m)
	if err != nil {
		t.Fatalf("Execute %s: %v", m.MessageID, err)
	}
	return res
}

// inboxRow is the stored inbox row of messageID ("" outcome when absent).
type inboxRow struct{ hash, typ, txID, outcome string }

func inbox(t *testing.T, messageID string) inboxRow {
	t.Helper()
	var r inboxRow
	var txID *string
	err := env.Owner.QueryRow(t.Context(), `SELECT message_hash, message_type, transaction_id::text, outcome
		FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`, app.ConsumerName, messageID).
		Scan(&r.hash, &r.typ, &txID, &r.outcome)
	if err != nil && err.Error() != "no rows in result set" {
		t.Fatalf("inbox %s: %v", messageID, err)
	}
	if txID != nil {
		r.txID = *txID
	}
	return r
}

// Covers: SQS-02, SQS-03, SQS-04, SQS-06, SQS-08 (spec M5 §3)
func TestConsumeWager(t *testing.T) {
	t.Parallel()

	t.Run("new outcomes record the inbox in their transaction", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		cases := []struct {
			o      op
			status wagering.Status
		}{
			{op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, wagering.StatusProcessed},
			{op{provider: p, kind: "BET", amount: "500.00", ext: "bet-2"}, wagering.StatusRejected},
			{op{provider: p, kind: "REFUND", amount: "5.00", ext: "refund-1", ref: "bet-9"}, wagering.StatusPendingReference},
		}
		for _, tc := range cases {
			m := message(w, tc.o, "msg-"+p+"-"+tc.o.ext)
			res := consume(t, c, m)
			if res.Duplicate || res.Result.Replay || res.Result.Tx.Status() != tc.status {
				t.Fatalf("%s: result = %+v %s, want a new %s", tc.o.ext, res.Duplicate, describe(res.Result), tc.status)
			}
			tx := res.Result.Tx
			if got := inbox(t, m.MessageID); got != (inboxRow{m.MessageHash, "WagerTransactionRequested", tx.ID(), string(tc.status)}) {
				t.Fatalf("%s: inbox = %+v", tc.o.ext, got)
			}
			if tx.ReceivedVia() != wagering.ReceivedViaSQS || tx.CorrelationID() != m.CorrelationID {
				t.Fatalf("%s: via %s correlation %s", tc.o.ext, tx.ReceivedVia(), tx.CorrelationID())
			}
			if n := count(t, `SELECT count(*) FROM outbox_events WHERE causation_id = $1`, m.MessageID); n == 0 {
				t.Fatalf("%s: no event caused by the message", tc.o.ext)
			}
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("a redelivery with the same hash is a duplicate", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)
		first := consume(t, c, m)
		again := consume(t, c, m)
		if !again.Duplicate || again.Result.Tx != nil {
			t.Fatalf("redelivery = %+v, want a duplicate", again)
		}
		if got := inbox(t, m.MessageID); got.outcome != "PROCESSED" || got.txID != first.Result.Tx.ID() {
			t.Fatalf("inbox = %+v", got)
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
			t.Fatalf("%d operations, want 1", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("the same messageId with other content is MESSAGE_HASH_MISMATCH", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		consume(t, c, message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p))
		_, err := c.Execute(t.Context(), message(w, op{provider: p, kind: "BET", amount: "31.00", ext: "bet-1"}, "msg-"+p))
		wantError(t, err, apperrors.KindInput, app.CodeMessageHashMismatch)
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("invalid input and conflicts record nothing", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		consume(t, c, message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p+"-1"))
		cases := []struct {
			name string
			m    app.WagerMessage
			kind apperrors.Kind
			code string
		}{
			{
				"opening", message(w, op{provider: p, kind: "OPENING", amount: "1.00", ext: "open-1"}, "msg-"+p+"-2"),
				apperrors.KindInput, string(wagering.InputOpeningNotAllowed),
			},
			{
				"unknown wallet", message(walletLike(t, w, newID()), op{provider: p, kind: "BET", amount: "1.00", ext: "bet-2"}, "msg-"+p+"-3"),
				apperrors.KindInput, app.CodeUnknownWallet,
			},
			{
				"key reused", message(w, op{provider: p, kind: "BET", amount: "31.00", ext: "bet-1"}, "msg-"+p+"-4"),
				apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED",
			},
		}
		for _, tc := range cases {
			_, err := c.Execute(t.Context(), tc.m)
			wantError(t, err, tc.kind, tc.code)
			if got := inbox(t, tc.m.MessageID); got.outcome != "" {
				t.Fatalf("%s: inbox = %+v, want no row", tc.name, got)
			}
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})
}

// Covers: SQS-02, IDEM-04 (C10a in process)
func TestConsumeWagerReplayAcrossChannels(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
	overHTTP := process(t, newProcessWager(), w, bet)

	m := message(w, bet, "msg-"+p)
	res := consume(t, newConsumeWager(), m)
	if res.Duplicate || !res.Result.Replay || res.Result.Tx.ID() != overHTTP.Tx.ID() {
		t.Fatalf("result = %+v %s, want the replay of %s", res.Duplicate, describe(res.Result), overHTTP.Tx.ID())
	}
	if got := inbox(t, m.MessageID); got.outcome != "IDEMPOTENT_REPLAY" || got.txID != overHTTP.Tx.ID() {
		t.Fatalf("inbox = %+v", got)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID()); n != 2 {
		t.Fatalf("%d entries, want the opening and one debit", n)
	}
}

// Covers: TX-06, SQS-07, TST-I03 (I03b, the SQS part)
func TestConsumeWagerPermanentFailure(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
		return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
	}}
	pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
	m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)

	res := consume(t, app.NewConsumeWager(reads(), pw), m)
	wantResult(t, res.Result, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
	if got := inbox(t, m.MessageID); got.outcome != "FAILED" || got.txID != res.Result.Tx.ID() {
		t.Fatalf("inbox = %+v, want FAILED with the operation", got)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, res.Result.Tx.ID()); n != 0 {
		t.Fatalf("%d entries for a FAILED operation", n)
	}
	wantWallet(t, w.ID(), "100.00", 1)
}

// Covers: SQS-04
// Sensitivity: the inbox of a new outcome written over the pool after the
// commit → the operation is recorded and "error = <nil>".
func TestConsumeWagerAtomicInbox(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	broken := errors.New("inbox unavailable")
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
		return faultyRepos{Repos: r, inbox: failingInbox{r.Inbox(), broken}}
	}}
	pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))

	_, err := app.NewConsumeWager(reads(), pw).Execute(t.Context(), message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p))
	if !errors.Is(err, broken) || apperrors.Classify(err) != apperrors.KindTransient {
		t.Fatalf("error = %v, want the transient inbox failure", err)
	}
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
		t.Fatalf("%d operations recorded without their inbox row", n)
	}
	wantWallet(t, w.ID(), "100.00", 1)
}

// Covers: SQS-03, CONC-03
// Sensitivity: ConsumeWager without the new round on ErrInboxDuplicate →
// "delivery N: TRANSIENT: app: message already recorded in the inbox".
func TestConsumeWagerInboxRace(t *testing.T) {
	t.Parallel()

	t.Run("the same message 20 times at once", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)
		results, errs := consumeConcurrently(t, 20, func(int) app.WagerMessage { return m })
		fresh := 0
		for i, res := range results {
			if errs[i] != nil {
				t.Fatalf("delivery %d: %v", i, errs[i])
			}
			if !res.Duplicate {
				fresh++
			}
		}
		if fresh != 1 {
			t.Fatalf("%d deliveries concluded the message, want 1 and 19 duplicates", fresh)
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, m.MessageID); n != 1 {
			t.Fatalf("%d inbox rows", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("10 messages of the same operation at once", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
		_, errs := consumeConcurrently(t, 10, func(i int) app.WagerMessage {
			return message(w, bet, "msg-"+p+"-"+string(rune('a'+i)))
		})
		for i, err := range errs {
			if err != nil {
				t.Fatalf("message %d: %v", i, err)
			}
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id LIKE $1 AND outcome = 'PROCESSED'`, "msg-"+p+"-%"); n != 1 {
			t.Fatalf("%d PROCESSED inbox rows, want 1", n)
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id LIKE $1 AND outcome = 'IDEMPOTENT_REPLAY'`, "msg-"+p+"-%"); n != 9 {
			t.Fatalf("%d IDEMPOTENT_REPLAY inbox rows, want 9", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})
}

// consumeConcurrently runs n deliveries at once, each with its own use case.
func consumeConcurrently(t *testing.T, n int, msg func(i int) app.WagerMessage) ([]app.ConsumeResult, []error) {
	t.Helper()
	results, errs := make([]app.ConsumeResult, n), make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		c, m := newConsumeWager(), msg(i)
		wg.Go(func() {
			<-start
			results[i], errs[i] = c.Execute(t.Context(), m)
		})
	}
	close(start)
	wg.Wait()
	return results, errs
}
```

- [ ] **Passo 3: stubs.** Criar `internal/app/consume_wager.go` com o stub:

```go
package app

import (
	"context"
	"errors"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// ConsumerName identifies the wager consumer in inbox_messages (D-12).
const ConsumerName = "wager-transactions-consumer"

// CodeMessageHashMismatch: the same messageId was concluded with other
// content (lifecycle §5.4).
const CodeMessageHashMismatch = "MESSAGE_HASH_MISMATCH"

// WagerMessage is a parsed WagerTransactionRequested (messaging.md §3).
type WagerMessage struct {
	MessageID     string
	MessageHash   string // SHA-256 (lowercase hex) of the canonical {type, data}
	MessageType   string
	CorrelationID string
	Input         wagering.Input
	ReceivedAt    time.Time
}

// ConsumeResult is how a message was concluded. Duplicate: the inbox already
// had it with the same hash, and nothing was done.
type ConsumeResult struct {
	Duplicate bool
	Result    ProcessResult
}

// ConsumeWager is the use case of the SQS consumer (lifecycle §6.2).
type ConsumeWager struct {
	reads  Repos
	wagers *ProcessWager
}

// NewConsumeWager builds the use case. reads are the repositories over the
// pool, used for the inbox lookup before the transaction.
func NewConsumeWager(reads Repos, wagers *ProcessWager) *ConsumeWager {
	return &ConsumeWager{reads: reads, wagers: wagers}
}

// Execute concludes one message.
func (c *ConsumeWager) Execute(ctx context.Context, m WagerMessage) (ConsumeResult, error) {
	return ConsumeResult{}, errors.New("app: not implemented")
}
```

Em `internal/app/process_wager.go`, acrescentar o campo e o tipo (substituindo o fim da `ProcessRequest`):

```go
	// CausationID is the message that caused the operation ("" over HTTP).
	CausationID string
	// Inbox is the SQS message that carries the operation (nil over HTTP).
	Inbox *InboxReceipt
}

// InboxReceipt identifies the SQS message that carries the operation. When a
// ProcessRequest has one, every outcome records it in inbox_messages in the
// transaction that concludes the operation (SQS-04).
type InboxReceipt struct {
	Consumer    string
	MessageID   string
	MessageHash string
	MessageType string
	ReceivedAt  time.Time
}
```

- [ ] **Passo 4: ver falhar.**

Rodar: `go test -tags=integration -race -count=1 -run 'TestConsumeWager' ./internal/app/`
Esperado: FAIL nos 9 testes/subtestes, todos com `app: not implemented` (por exemplo, `TestConsumeWagerAtomicInbox: error = app: not implemented, want the transient inbox failure`).

- [ ] **Passo 5: implementar.** O diff completo de `internal/app/process_wager.go` (inclui o do passo 3):

```diff
@@ -20,6 +20,19 @@ type ProcessRequest struct {
 	CorrelationID string
 	// CausationID is the message that caused the operation ("" over HTTP).
 	CausationID string
+	// Inbox is the SQS message that carries the operation (nil over HTTP).
+	Inbox *InboxReceipt
+}
+
+// InboxReceipt identifies the SQS message that carries the operation. When a
+// ProcessRequest has one, every outcome records it in inbox_messages in the
+// transaction that concludes the operation (SQS-04).
+type InboxReceipt struct {
+	Consumer    string
+	MessageID   string
+	MessageHash string
+	MessageType string
+	ReceivedAt  time.Time
 }
 
 // ProcessResult is the persisted outcome: PROCESSED, PENDING_REFERENCE,
@@ -79,7 +92,13 @@ func isRace(err error) bool {
 func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (ProcessResult, error) {
 	cmd := req.Command
 	if replay, err := lookup(ctx, p.reads, cmd); err != nil || replay != nil {
-		return ProcessResult{Tx: replay, Replay: replay != nil}, err
+		if err == nil && req.Inbox != nil {
+			err = p.uow.Do(ctx, func(r Repos) error { return recordInbox(ctx, r, req.Inbox, replay, true, p.clock.Now()) })
+		}
+		if err != nil {
+			return ProcessResult{}, err
+		}
+		return ProcessResult{Tx: replay, Replay: true}, nil
 	}
 	now := p.clock.Now()
 	var res ProcessResult
@@ -95,6 +114,9 @@ func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (Process
 		replay, err := lookup(ctx, r, cmd)
 		if err != nil || replay != nil {
 			res = ProcessResult{Tx: replay, Replay: replay != nil}
+			if err == nil {
+				err = recordInbox(ctx, r, req.Inbox, replay, true, now)
+			}
 			return err
 		}
 		tx, err := wagering.NewExternal(p.ids.New(), cmd, req.Via, req.CorrelationID, now)
@@ -105,7 +127,7 @@ func (p *ProcessWager) attempt(ctx context.Context, req ProcessRequest) (Process
 			return err
 		}
 		res = ProcessResult{Tx: tx}
-		return nil
+		return recordInbox(ctx, r, req.Inbox, tx, false, now)
 	})
 	if apperrors.Classify(err) == apperrors.KindPermanent {
 		return p.recordFailure(ctx, req, now, err)
@@ -134,8 +156,10 @@ func (p *ProcessWager) recordFailure(ctx context.Context, req ProcessRequest, no
 		if err := r.Transactions().Insert(ctx, tx); err != nil {
 			return err
 		}
-		_, err = r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now)
-		return err
+		if _, err := r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now); err != nil {
+			return err
+		}
+		return recordInbox(ctx, r, req.Inbox, tx, false, now)
 	})
 	switch {
 	case isRace(err):
@@ -149,6 +173,35 @@ func (p *ProcessWager) recordFailure(ctx context.Context, req ProcessRequest, no
 	return ProcessResult{Tx: tx}, nil
 }
 
+// recordInbox writes the SQS delivery of the operation with the outcome of tx
+// (SQS-04), in the transaction of r; nothing over HTTP (in == nil). A second
+// delivery of the same message fails with ErrInboxDuplicate.
+func recordInbox(ctx context.Context, r Repos, in *InboxReceipt, tx *wagering.WagerTransaction, replay bool, now time.Time) error {
+	if in == nil {
+		return nil
+	}
+	return r.Inbox().Insert(ctx, InboxMessage{
+		ConsumerName: in.Consumer, MessageID: in.MessageID, MessageHash: in.MessageHash, MessageType: in.MessageType,
+		TransactionID: tx.ID(), Outcome: inboxOutcome(tx.Status(), replay), ReceivedAt: in.ReceivedAt, ProcessedAt: now,
+	})
+}
+
+// inboxOutcome is how the operation concluded the message (data-model §3.4).
+func inboxOutcome(s wagering.Status, replay bool) InboxOutcome {
+	switch {
+	case replay:
+		return InboxIdempotentReplay
+	case s == wagering.StatusRejected:
+		return InboxRejected
+	case s == wagering.StatusPendingReference:
+		return InboxPendingReference
+	case s == wagering.StatusFailed:
+		return InboxFailed
+	default:
+		return InboxProcessed
+	}
+}
+
 // lookup applies D-08: the transaction found by (providerId, idempotencyKey)
 // with the same hash is a replay; the same key with another hash, or the same
 // externalTransactionId under another key, is a conflict (KindConflict).
```

E o `internal/app/consume_wager.go` final:

```go
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// ConsumerName identifies the wager consumer in inbox_messages (D-12).
const ConsumerName = "wager-transactions-consumer"

// CodeMessageHashMismatch: the same messageId was concluded with other
// content (lifecycle §5.4).
const CodeMessageHashMismatch = "MESSAGE_HASH_MISMATCH"

// WagerMessage is a parsed WagerTransactionRequested (messaging.md §3).
type WagerMessage struct {
	MessageID     string
	MessageHash   string // SHA-256 (lowercase hex) of the canonical {type, data}
	MessageType   string
	CorrelationID string
	Input         wagering.Input
	ReceivedAt    time.Time
}

// ConsumeResult is how a message was concluded. Duplicate: the inbox already
// had it with the same hash, and nothing was done.
type ConsumeResult struct {
	Duplicate bool
	Result    ProcessResult
}

// ConsumeWager is the use case of the SQS consumer (lifecycle §6.2).
type ConsumeWager struct {
	reads  Repos
	wagers *ProcessWager
}

// NewConsumeWager builds the use case. reads are the repositories over the
// pool, used for the inbox lookup before the transaction.
func NewConsumeWager(reads Repos, wagers *ProcessWager) *ConsumeWager {
	return &ConsumeWager{reads: reads, wagers: wagers}
}

// Execute concludes one message: the inbox first (a redelivery is a duplicate,
// the same messageId with other content is MESSAGE_HASH_MISMATCH), then the
// stateless validation, then ProcessWager with the inbox receipt, which
// records it with the outcome. Errors are those of ProcessWager plus
// KindInput for the hash and the validation; nothing is recorded with them.
//
// Two consumers with the same message race on the inbox primary key: the
// loser rolls back and starts again from the inbox, which then has the row
// (spec M5, decision 3); after maxAttempts the race is returned, transient.
func (c *ConsumeWager) Execute(ctx context.Context, m WagerMessage) (ConsumeResult, error) {
	var err error
	for range maxAttempts {
		var res ConsumeResult
		if res, err = c.attempt(ctx, m); !errors.Is(err, ErrInboxDuplicate) {
			return res, err
		}
	}
	return ConsumeResult{}, err
}

func (c *ConsumeWager) attempt(ctx context.Context, m WagerMessage) (ConsumeResult, error) {
	seen, err := c.reads.Inbox().Find(ctx, ConsumerName, m.MessageID)
	if err != nil {
		return ConsumeResult{}, err
	}
	if seen != nil {
		if seen.MessageHash != m.MessageHash {
			return ConsumeResult{}, apperrors.New(apperrors.KindInput, CodeMessageHashMismatch,
				fmt.Errorf("app: message %s already concluded with other content", m.MessageID))
		}
		return ConsumeResult{Duplicate: true}, nil
	}
	cmd, err := wagering.NewCommand(m.Input)
	if err != nil {
		return ConsumeResult{}, domainError(err)
	}
	res, err := c.wagers.Execute(ctx, ProcessRequest{
		Command: cmd, Via: wagering.ReceivedViaSQS, CorrelationID: m.CorrelationID, CausationID: m.MessageID,
		Inbox: &InboxReceipt{
			Consumer: ConsumerName, MessageID: m.MessageID, MessageHash: m.MessageHash,
			MessageType: m.MessageType, ReceivedAt: m.ReceivedAt,
		},
	})
	return ConsumeResult{Result: res}, err
}
```

- [ ] **Passo 6: ver passar.**

Rodar: `go test -tags=integration -race -count=1 ./internal/app/`
Esperado: `ok` (os testes do M3 continuam verdes: o HTTP passa `Inbox = nil`).

- [ ] **Passo 7: sensibilidade** (já registrada nos comentários dos testes):
  1. no `attempt`, trocar `return recordInbox(ctx, r, req.Inbox, tx, false, now)` por `return nil` e gravar a inbox depois do `Do` com `p.reads` → `TestConsumeWagerAtomicInbox` falha com `error = <nil>`;
  2. no `ConsumeWager.Execute`, `for range 1` no lugar de `for range maxAttempts` → `TestConsumeWagerInboxRace` falha com `TRANSIENT: app: message already recorded in the inbox`.

  Desfazer e confirmar o verde.

- [ ] **Checkpoint:** `go test -tags=integration -race -count=1 ./internal/app/` verde.

---

## Tarefa 4: Envelope e hash da mensagem

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/envelope.go`, `internal/adapters/sqsconsumer/envelope_test.go`

**Interfaces:**
- Produz (Tarefas 5 e 8): `parseEnvelope(body, correlationAttr string, receivedAt time.Time) (app.WagerMessage, error)` — erro `KindInput` com `MALFORMED_MESSAGE` ou `UNSUPPORTED_MESSAGE_TYPE`; `correlationID(attr, messageID string) string`; as constantes `codeMalformedMessage` e `codeUnsupportedMessageType`; `validBody` (fixture dos testes do pacote).

- [ ] **Passo 1: escrever os testes.** Criar `internal/adapters/sqsconsumer/envelope_test.go`:

```go
package sqsconsumer

import (
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

const validBody = `{"messageId":"msg-123","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00.000Z",
"data":{"providerId":"provider-a","externalTransactionId":"transaction-123","idempotencyKey":"provider-a:transaction-123",
"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
"roundId":"round-987","gameId":"fortune-chimp","kind":"WIN","money":{"amount":"25.00","currency":"BRL"},
"referenceExternalTransactionId":"transaction-100"}}`

// Covers: SQS-07, SQS-10 (messaging.md §3.1, lifecycle §5.4; spec M5, decision 14)
func TestParseEnvelope(t *testing.T) {
	received := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a valid message becomes the input of the use case", func(t *testing.T) {
		m, err := parseEnvelope(validBody, "", received)
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if m.MessageID != "msg-123" || m.MessageType != "WagerTransactionRequested" || m.CorrelationID != "msg-123" ||
			!m.ReceivedAt.Equal(received) || len(m.MessageHash) != 64 {
			t.Fatalf("message = %+v", m)
		}
		cmd, err := wagering.NewCommand(m.Input)
		if err != nil {
			t.Fatalf("NewCommand: %v", err)
		}
		if cmd.IdempotencyKey() != "provider-a:transaction-123" || cmd.Kind() != wagering.KindWin ||
			cmd.Money().String() != "25.00" || cmd.ReferenceExternalTransactionID() != "transaction-100" {
			t.Fatalf("command = %+v", cmd)
		}
	})

	cases := []struct {
		name, body, code string
	}{
		{"not JSON", `{"messageId":`, codeMalformedMessage},
		{"not an object", `["msg"]`, codeMalformedMessage},
		{"trailing data", validBody + `{}`, codeMalformedMessage},
		{"unknown envelope field", strings.Replace(validBody, `"type"`, `"extra":1,"type"`, 1), codeMalformedMessage},
		{"unknown data field", strings.Replace(validBody, `"roundId"`, `"extra":1,"roundId"`, 1), codeMalformedMessage},
		{"data field of the wrong type", strings.Replace(validBody, `"amount":"25.00"`, `"amount":25`, 1), codeMalformedMessage},
		{"no messageId", strings.Replace(validBody, `"messageId":"msg-123",`, ``, 1), codeMalformedMessage},
		{"empty messageId", strings.Replace(validBody, `"msg-123"`, `""`, 1), codeMalformedMessage},
		{"messageId of 129 characters", strings.Replace(validBody, `"msg-123"`, `"`+strings.Repeat("é", 129)+`"`, 1), codeMalformedMessage},
		{"no occurredAt", strings.Replace(validBody, `"occurredAt":"2026-09-08T12:00:00.000Z",`, ``, 1), codeMalformedMessage},
		{"occurredAt not RFC 3339", strings.Replace(validBody, `2026-09-08T12:00:00.000Z`, `08/09/2026`, 1), codeMalformedMessage},
		{"no data", `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z"}`, codeMalformedMessage},
		{"null data", `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z","data":null}`, codeMalformedMessage},
		{"no type", strings.Replace(validBody, `"type":"WagerTransactionRequested",`, ``, 1), codeMalformedMessage},
		{"another type", strings.Replace(validBody, `WagerTransactionRequested`, `WalletOpened`, 1), codeUnsupportedMessageType},
		{"another type with other data", `{"messageId":"m","type":"WalletOpened","occurredAt":"2026-09-08T12:00:00Z","data":{"x":1}}`, codeUnsupportedMessageType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseEnvelope(tc.body, "", received)
			if apperrors.Classify(err) != apperrors.KindInput || apperrors.CodeOf(err) != tc.code {
				t.Fatalf("error = %v, want KindInput %s", err, tc.code)
			}
		})
	}

	t.Run("a messageId of 128 characters is accepted", func(t *testing.T) {
		if _, err := parseEnvelope(strings.Replace(validBody, `"msg-123"`, `"`+strings.Repeat("é", 128)+`"`, 1), "", received); err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
	})

	t.Run("invalid business fields are left to the use case", func(t *testing.T) {
		m, err := parseEnvelope(strings.Replace(validBody, `"kind":"WIN"`, `"kind":"OPENING"`, 1), "", received)
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		if _, err := wagering.NewCommand(m.Input); err == nil {
			t.Fatal("NewCommand accepted an OPENING")
		}
	})
}

// Covers: SQS-03 (messaging.md §3.3; spec M5, decision 4)
func TestMessageHash(t *testing.T) {
	hash := func(t *testing.T, body string) string {
		t.Helper()
		m, err := parseEnvelope(body, "", time.Now())
		if err != nil {
			t.Fatalf("parseEnvelope: %v", err)
		}
		return m.MessageHash
	}
	base := hash(t, validBody)

	same := map[string]string{
		"keys in another order": `{"data":{"money":{"currency":"BRL","amount":"25.00"},"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37",
"roundId":"round-987","referenceExternalTransactionId":"transaction-100","providerId":"provider-a",
"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","kind":"WIN","idempotencyKey":"provider-a:transaction-123",
"gameId":"fortune-chimp","externalTransactionId":"transaction-123"},"occurredAt":"2026-09-08T12:00:00.000Z",
"type":"WagerTransactionRequested","messageId":"msg-123"}`,
		"other spacing":    strings.ReplaceAll(validBody, `":"`, `" : "`),
		"other messageId":  strings.Replace(validBody, `"msg-123"`, `"msg-999"`, 1),
		"other occurredAt": strings.Replace(validBody, `2026-09-08T12:00:00.000Z`, `2026-09-09T08:30:00Z`, 1),
	}
	for name, body := range same {
		if got := hash(t, body); got != base {
			t.Errorf("%s: hash changed", name)
		}
	}
	differs := map[string]string{
		"other idempotencyKey": strings.Replace(validBody, `"provider-a:transaction-123"`, `"provider-a:other"`, 1),
		"other amount":         strings.Replace(validBody, `"25.00"`, `"25.01"`, 1),
		"other currency":       strings.Replace(validBody, `"BRL"`, `"USD"`, 1),
		"other reference":      strings.Replace(validBody, `"transaction-100"`, `"transaction-101"`, 1),
		"other game":           strings.Replace(validBody, `"fortune-chimp"`, `"fortune-ox"`, 1),
		"uppercase wallet id":  strings.Replace(validBody, `0192f291-27dd-7d3f-8071-5f8685deef37`, `0192F291-27DD-7D3F-8071-5F8685DEEF37`, 1),
	}
	for name, body := range differs {
		if got := hash(t, body); got == base {
			t.Errorf("%s: hash did not change", name)
		}
	}

	absent := hash(t, strings.Replace(validBody, `,
"referenceExternalTransactionId":"transaction-100"`, ``, 1))
	null := hash(t, strings.Replace(validBody, `"transaction-100"`, `null`, 1))
	if absent != null {
		t.Error("an absent field and a null one hash differently")
	}
}

// Covers: IDEM-04 (U05b, the SQS envelope)
//
// The data of the envelope and the same operation as the HTTP edge builds it
// (other case in the UUIDs, no key in the hash) produce the same payload hash.
func TestPayloadHashHTTPEqualsSQS(t *testing.T) {
	m, err := parseEnvelope(strings.NewReplacer(
		"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1",
		`"provider-a:transaction-123"`, `"another-key"`,
	).Replace(validBody), "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fromSQS, err := wagering.NewCommand(m.Input)
	if err != nil {
		t.Fatal(err)
	}
	s := func(v string) *string { return &v }
	fromHTTP, err := wagering.NewCommand(wagering.Input{
		IdempotencyKey: s("provider-a:transaction-123"), ProviderID: s("provider-a"), ExternalTransactionID: s("transaction-123"),
		PlayerID: s("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"), WalletID: s("0192f291-27dd-7d3f-8071-5f8685deef37"),
		RoundID: s("round-987"), GameID: s("fortune-chimp"), Kind: s("WIN"),
		Money:                          &wagering.MoneyInput{Amount: s("25.00"), Currency: s("BRL")},
		ReferenceExternalTransactionID: s("transaction-100"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fromSQS.PayloadHash() != fromHTTP.PayloadHash() {
		t.Fatalf("payload hash SQS %s != HTTP %s", fromSQS.PayloadHash(), fromHTTP.PayloadHash())
	}
}

// Covers: OBS-02 (D-18; spec M5, decision 13)
func TestCorrelationID(t *testing.T) {
	cases := []struct{ attr, want string }{
		{"", "msg-1"},
		{"7f1c9a4e-abc_1.2", "7f1c9a4e-abc_1.2"},
		{"has space", "msg-1"},
		{strings.Repeat("a", 128), strings.Repeat("a", 128)},
		{strings.Repeat("a", 129), "msg-1"},
		{"ação", "msg-1"},
	}
	for _, tc := range cases {
		if got := correlationID(tc.attr, "msg-1"); got != tc.want {
			t.Errorf("correlationID(%q) = %q, want %q", tc.attr, got, tc.want)
		}
	}
}
```

- [ ] **Passo 2: stub.** Criar `internal/adapters/sqsconsumer/envelope.go`:

```go
package sqsconsumer

import (
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// Codes of the SQS-only rejections (lifecycle §5.4).
const (
	codeMalformedMessage       = "MALFORMED_MESSAGE"
	codeUnsupportedMessageType = "UNSUPPORTED_MESSAGE_TYPE"
)

func parseEnvelope(body, correlationAttr string, receivedAt time.Time) (app.WagerMessage, error) {
	return app.WagerMessage{}, nil
}

func correlationID(attr, messageID string) string { return "" }
```

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -race ./internal/adapters/sqsconsumer/`
Esperado: FAIL em 21 testes/subtestes (o stub não decodifica nem rejeita nada).

- [ ] **Passo 4: implementar.** O `envelope.go` final:

```go
package sqsconsumer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// messageType is the only type the wager queue carries (messaging.md §3.1).
const messageType = "WagerTransactionRequested"

// Codes of the SQS-only rejections (lifecycle §5.4).
const (
	codeMalformedMessage       = "MALFORMED_MESSAGE"
	codeUnsupportedMessageType = "UNSUPPORTED_MESSAGE_TYPE"
)

// maxMessageIDLen bounds the messageId, in characters.
const maxMessageIDLen = 128

// correlationPattern is what a producer may send as the correlationId
// attribute: the rule of the HTTP X-Correlation-Id (D-18).
var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// envelope is the outer object; data is decoded once the type is known
// (spec M5, decision 14).
type envelope struct {
	MessageID  *string         `json:"messageId"`
	Type       *string         `json:"type"`
	OccurredAt *string         `json:"occurredAt"`
	Data       json.RawMessage `json:"data"`
}

// wagerData is the data of a WagerTransactionRequested. The fields are
// declared in the lexicographic order of their JSON names and omitted when
// nil, so that encoding it is the canonical JSON of the message hash, with an
// absent field and a null one alike (spec M5, decision 4).
type wagerData struct {
	ExternalTransactionID          *string    `json:"externalTransactionId,omitempty"`
	GameID                         *string    `json:"gameId,omitempty"`
	IdempotencyKey                 *string    `json:"idempotencyKey,omitempty"`
	Kind                           *string    `json:"kind,omitempty"`
	Money                          *moneyData `json:"money,omitempty"`
	PlayerID                       *string    `json:"playerId,omitempty"`
	ProviderID                     *string    `json:"providerId,omitempty"`
	ReferenceExternalTransactionID *string    `json:"referenceExternalTransactionId,omitempty"`
	RoundID                        *string    `json:"roundId,omitempty"`
	WalletID                       *string    `json:"walletId,omitempty"`
}

type moneyData struct {
	Amount   *string `json:"amount,omitempty"`
	Currency *string `json:"currency,omitempty"`
}

// parseEnvelope decodes a message body into the input of app.ConsumeWager.
// A body that is not a well-formed WagerTransactionRequested is KindInput with
// MALFORMED_MESSAGE or UNSUPPORTED_MESSAGE_TYPE; the business fields are left
// to the stateless validation of the use case.
func parseEnvelope(body, correlationAttr string, receivedAt time.Time) (app.WagerMessage, error) {
	var env envelope
	if err := decodeStrict([]byte(body), &env); err != nil {
		return app.WagerMessage{}, malformed(err)
	}
	switch {
	case env.MessageID == nil || *env.MessageID == "" || utf8.RuneCountInString(*env.MessageID) > maxMessageIDLen ||
		!utf8.ValidString(*env.MessageID):
		return app.WagerMessage{}, malformed(errors.New("messageId must have 1 to 128 characters"))
	case env.Type == nil:
		return app.WagerMessage{}, malformed(errors.New("type is required"))
	case env.OccurredAt == nil:
		return app.WagerMessage{}, malformed(errors.New("occurredAt is required"))
	}
	if _, err := time.Parse(time.RFC3339Nano, *env.OccurredAt); err != nil {
		return app.WagerMessage{}, malformed(errors.New("occurredAt must be RFC 3339"))
	}
	if *env.Type != messageType {
		return app.WagerMessage{}, apperrors.New(apperrors.KindInput, codeUnsupportedMessageType,
			fmt.Errorf("sqsconsumer: message type %q", *env.Type))
	}
	if len(env.Data) == 0 || bytes.Equal(env.Data, []byte("null")) {
		return app.WagerMessage{}, malformed(errors.New("data is required"))
	}
	var data wagerData
	if err := decodeStrict(env.Data, &data); err != nil {
		return app.WagerMessage{}, malformed(err)
	}
	hash, err := messageHash(data)
	if err != nil {
		return app.WagerMessage{}, err
	}
	return app.WagerMessage{
		MessageID: *env.MessageID, MessageHash: hash, MessageType: messageType,
		CorrelationID: correlationID(correlationAttr, *env.MessageID), Input: data.input(), ReceivedAt: receivedAt,
	}, nil
}

// decodeStrict decodes exactly one JSON value into v, refusing unknown fields
// and trailing data, as the HTTP edge does.
func decodeStrict(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the message")
	}
	return nil
}

func malformed(err error) error {
	return apperrors.New(apperrors.KindInput, codeMalformedMessage, fmt.Errorf("sqsconsumer: %w", err))
}

// messageHash is the SHA-256 (lowercase hex) of the canonical {"data","type"}
// (messaging.md §3.3): sorted keys, no spaces, no HTML escaping.
func messageHash(data wagerData) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Data wagerData `json:"data"`
		Type string    `json:"type"`
	}{data, messageType}); err != nil {
		return "", fmt.Errorf("sqsconsumer: message hash: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.TrimSuffix(buf.String(), "\n")))
	return hex.EncodeToString(sum[:]), nil
}

// input is the raw operation for wagering.NewCommand.
func (d wagerData) input() wagering.Input {
	in := wagering.Input{
		IdempotencyKey: d.IdempotencyKey, ProviderID: d.ProviderID, ExternalTransactionID: d.ExternalTransactionID,
		PlayerID: d.PlayerID, WalletID: d.WalletID, RoundID: d.RoundID, GameID: d.GameID, Kind: d.Kind,
		ReferenceExternalTransactionID: d.ReferenceExternalTransactionID,
	}
	if d.Money != nil {
		in.Money = &wagering.MoneyInput{Amount: d.Money.Amount, Currency: d.Money.Currency}
	}
	return in
}

// correlationID is the correlationId attribute when it follows the HTTP rule,
// else the messageId (messaging.md §3.2; spec M5, decision 13).
func correlationID(attr, messageID string) string {
	if correlationPattern.MatchString(attr) {
		return attr
	}
	return messageID
}
```

- [ ] **Checkpoint:** `go test -race ./internal/adapters/sqsconsumer/` verde.

---

## Tarefa 5: Decisão por resultado, backoff, DLQ e agrupamento

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/handler.go`, `backoff.go`, `dlq.go`, `batch.go`, `handler_test.go`

**Interfaces:**
- Produz (Tarefa 8): `type action`, `actDelete|actDLQ|actRetry|actRelease`, `type conclusion`, `conclude(app.ConsumeResult, error) conclusion`, `decide(c conclusion, stopping bool, receiveCount int, ceiling time.Duration) action`, `retryDelay(receiveCount int, ceiling time.Duration) time.Duration`, `dlqInput(dlqURL string, msg types.Message, a action, failedAt time.Time) *sqs.SendMessageInput`, `groupBatch([]types.Message) [][]types.Message`.

- [ ] **Passo 1: escrever os testes.** Criar `internal/adapters/sqsconsumer/handler_test.go`:

```go
package sqsconsumer

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: SQS-05, SQS-06, SQS-07 (lifecycle §6.2; spec M5 §4.3)
func TestDecide(t *testing.T) {
	transient := errors.New("connection refused")
	cases := []struct {
		name     string
		c        conclusion
		stopping bool
		want     action
	}{
		{
			"duplicate in the inbox",
			conclusion{duplicate: true},
			false,
			action{kind: actDelete, duplicate: "inbox"},
		},
		{
			"processed",
			conclusion{status: wagering.StatusProcessed},
			false,
			action{kind: actDelete, outcome: "processed"},
		},
		{
			"rejected",
			conclusion{status: wagering.StatusRejected},
			false,
			action{kind: actDelete, outcome: "rejected"},
		},
		{
			"pending reference",
			conclusion{status: wagering.StatusPendingReference},
			false,
			action{kind: actDelete, outcome: "pending_reference"},
		},
		{
			"replay",
			conclusion{status: wagering.StatusProcessed, replay: true},
			false,
			action{kind: actDelete, outcome: "replay", duplicate: "idempotency"},
		},
		{
			"replay of a FAILED",
			conclusion{status: wagering.StatusFailed, replay: true},
			false,
			action{kind: actDelete, outcome: "replay", duplicate: "idempotency"},
		},
		{
			"failed",
			conclusion{status: wagering.StatusFailed},
			false,
			action{kind: actDLQ, code: "INTERNAL_PERMANENT_FAILURE", category: "DEFINITIVE", outcome: "failed"},
		},
		{
			"invalid input",
			conclusion{err: apperrors.New(apperrors.KindInput, "INVALID_AMOUNT", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "INVALID_AMOUNT", category: "CORRECTABLE"},
		},
		{
			"unknown wallet",
			conclusion{err: apperrors.New(apperrors.KindInput, app.CodeUnknownWallet, errors.New("x"))},
			false,
			action{kind: actDLQ, code: "UNKNOWN_WALLET", category: "CORRECTABLE"},
		},
		{
			"conflict",
			conclusion{err: apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "IDEMPOTENCY_KEY_REUSED", category: "CORRECTABLE"},
		},
		{
			"permanent without a record",
			conclusion{err: apperrors.New(apperrors.KindPermanent, "", errors.New("x"))},
			false,
			action{kind: actDLQ, code: "INTERNAL_ERROR", category: "TRANSIENT"},
		},
		{
			"transient",
			conclusion{err: apperrors.New(apperrors.KindTransient, "", transient)},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"unclassified is transient (D-05)",
			conclusion{err: transient},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"deadline of the message is transient",
			conclusion{err: context.DeadlineExceeded},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
		{
			"canceled while stopping",
			conclusion{err: fmt.Errorf("uow: %w", context.Canceled)},
			true,
			action{kind: actRelease},
		},
		{
			"canceled while running is transient",
			conclusion{err: context.Canceled},
			false,
			action{kind: actRetry, delay: 4 * time.Second, transient: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decide(tc.c, tc.stopping, 2, 5*time.Minute); got != tc.want {
				t.Fatalf("decide = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Covers: SQS-07 (messaging.md §4.1)
func TestRetryDelay(t *testing.T) {
	cases := []struct {
		receiveCount int
		ceiling      time.Duration
		want         time.Duration
	}{
		{1, 300 * time.Second, 2 * time.Second},
		{2, 300 * time.Second, 4 * time.Second},
		{8, 300 * time.Second, 256 * time.Second},
		{9, 300 * time.Second, 300 * time.Second}, // 512 s capped
		{1_000_000, 300 * time.Second, 300 * time.Second},
		{0, 300 * time.Second, 2 * time.Second}, // an unknown count counts as the first
		{-3, 300 * time.Second, 2 * time.Second},
		{3, time.Second, time.Second},
	}
	for _, tc := range cases {
		if got := retryDelay(tc.receiveCount, tc.ceiling); got != tc.want {
			t.Errorf("retryDelay(%d, %v) = %v, want %v", tc.receiveCount, tc.ceiling, got, tc.want)
		}
	}
}

// Covers: SQS-07, SQS-10 (messaging.md §4.4)
func TestDLQInput(t *testing.T) {
	failedAt := time.Date(2026, 9, 29, 12, 0, 0, 123456789, time.FixedZone("BRT", -3*3600))
	msg := types.Message{
		MessageId: aws.String("sqs-id-1"), Body: aws.String(validBody),
		Attributes: map[string]string{"MessageGroupId": "wallet-1"},
	}
	in := dlqInput("https://dlq", msg, action{kind: actDLQ, code: "UNKNOWN_WALLET", category: "CORRECTABLE"}, failedAt)
	if aws.ToString(in.QueueUrl) != "https://dlq" || aws.ToString(in.MessageBody) != validBody ||
		aws.ToString(in.MessageGroupId) != "wallet-1" || aws.ToString(in.MessageDeduplicationId) != "sqs-id-1" {
		t.Fatalf("input = %+v", in)
	}
	want := map[string]string{
		"errorCode": "UNKNOWN_WALLET", "errorCategory": "CORRECTABLE", "originalMessageId": "sqs-id-1",
		"consumerName": app.ConsumerName, "failedAt": "2026-09-29T15:00:00.123Z",
	}
	if len(in.MessageAttributes) != len(want) {
		t.Fatalf("attributes = %v", in.MessageAttributes)
	}
	for k, v := range want {
		if a := in.MessageAttributes[k]; aws.ToString(a.DataType) != "String" || aws.ToString(a.StringValue) != v {
			t.Errorf("attribute %s = %s %s, want String %s", k, aws.ToString(a.DataType), aws.ToString(a.StringValue), v)
		}
	}

	msg.Attributes = nil
	if got := aws.ToString(dlqInput("https://dlq", msg, action{kind: actDLQ, code: "X", category: "Y"}, failedAt).MessageGroupId); got != "invalid-messages" {
		t.Fatalf("group without MessageGroupId = %q, want invalid-messages", got)
	}
}

// Covers: SQS-07 (messaging.md §4.2)
func TestGroupBatch(t *testing.T) {
	m := func(id, group string) types.Message {
		return types.Message{MessageId: aws.String(id), Attributes: map[string]string{"MessageGroupId": group}}
	}
	groups := groupBatch([]types.Message{m("1", "a"), m("2", "b"), m("3", "a"), m("4", "c"), m("5", "b"), m("6", "a")})
	var got [][]string
	for _, g := range groups {
		var ids []string
		for _, msg := range g {
			ids = append(ids, aws.ToString(msg.MessageId))
		}
		got = append(got, ids)
	}
	if fmt.Sprint(got) != "[[1 3 6] [2 5] [4]]" {
		t.Fatalf("groups = %v, want [[1 3 6] [2 5] [4]]", got)
	}
}
```

- [ ] **Passo 2: stub.** Criar `handler.go` com o stub abaixo (tipos reais e funções vazias; `backoff.go`, `dlq.go` e `batch.go` ainda não existem):

```go
package sqsconsumer

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

type actionKind int

const (
	actDelete actionKind = iota
	actDLQ
	actRetry
	actRelease
)

type action struct {
	kind           actionKind
	code, category string
	delay          time.Duration
	transient      bool
	outcome        string
	duplicate      string
}

type conclusion struct {
	duplicate bool
	status    wagering.Status
	replay    bool
	err       error
}

func decide(c conclusion, stopping bool, receiveCount int, ceiling time.Duration) action {
	return action{kind: -1}
}

func retryDelay(receiveCount int, ceiling time.Duration) time.Duration { return 0 }

func dlqInput(dlqURL string, msg types.Message, a action, failedAt time.Time) *sqs.SendMessageInput {
	return &sqs.SendMessageInput{}
}

func groupBatch(msgs []types.Message) [][]types.Message { return nil }
```

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -race -run 'TestDecide|TestRetryDelay|TestDLQInput|TestGroupBatch' ./internal/adapters/sqsconsumer/`
Esperado: FAIL em todos (`decide = {kind:-1 …}`, `retryDelay(1, 5m0s) = 0s`, `input = &{…}`, `groups = [], want [[1 3 6] [2 5] [4]]`).

- [ ] **Passo 4: implementar.** Substituir o stub pelos quatro arquivos. `handler.go` (sem `retryDelay`, `dlqInput` e `groupBatch`, que vão para os próprios arquivos; o `conclude` é usado na Tarefa 8):

```go
package sqsconsumer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Codes and categories of the explicit DLQ sends (lifecycle §5.2–§5.4).
const (
	codeInternalPermanentFailure = "INTERNAL_PERMANENT_FAILURE"
	codeInternalError            = "INTERNAL_ERROR"
	categoryCorrectable          = "CORRECTABLE"
	categoryDefinitive           = "DEFINITIVE"
	categoryTransient            = "TRANSIENT"
)

// actionKind is what happens to a message in the queue.
type actionKind int

const (
	actDelete  actionKind = iota // concluded: DeleteMessage
	actDLQ                       // permanent: SendMessage to the DLQ, then DeleteMessage
	actRetry                     // transient: ChangeMessageVisibility(delay)
	actRelease                   // not processed: ChangeMessageVisibility(0)
)

// action is the decision about one message (spec M5, decision 10), with what
// the metrics count.
type action struct {
	kind           actionKind
	code, category string        // actDLQ
	delay          time.Duration // actRetry
	transient      bool          // the health gate probes the database
	outcome        string        // sqs_messages_processed_total, "" when not concluded by the use case
	duplicate      string        // wager_duplicates_total layer, "" when not a duplicate
}

// conclusion is what the use case answered, reduced to what decides the action.
type conclusion struct {
	duplicate bool
	status    wagering.Status // of the operation; "" on an error
	replay    bool
	err       error
}

func conclude(res app.ConsumeResult, err error) conclusion {
	c := conclusion{duplicate: res.Duplicate, replay: res.Result.Replay, err: err}
	if err == nil && res.Result.Tx != nil {
		c.status = res.Result.Tx.Status()
	}
	return c
}

// decide maps a conclusion to the action of lifecycle §6.2. Only a
// cancellation while the consumer stops releases the message (lifecycle §8);
// every other error that is not input, conflict or permanent is retried with
// backoff, including the unclassified ones (D-05).
func decide(c conclusion, stopping bool, receiveCount int, ceiling time.Duration) action {
	if c.err != nil {
		switch apperrors.Classify(c.err) {
		case apperrors.KindInput, apperrors.KindConflict:
			code := apperrors.CodeOf(c.err)
			if code == "" {
				code = codeMalformedMessage
			}
			return action{kind: actDLQ, code: code, category: categoryCorrectable}
		case apperrors.KindPermanent:
			return action{kind: actDLQ, code: codeInternalError, category: categoryTransient}
		case apperrors.KindTransient, apperrors.KindNotFound, apperrors.KindForbidden, apperrors.KindBusiness:
			// Retried below; the use case returns none of the last three.
		}
		if stopping && errors.Is(c.err, context.Canceled) {
			return action{kind: actRelease}
		}
		return action{kind: actRetry, delay: retryDelay(receiveCount, ceiling), transient: true}
	}
	switch {
	case c.duplicate:
		return action{kind: actDelete, duplicate: "inbox"}
	case c.replay:
		return action{kind: actDelete, outcome: "replay", duplicate: "idempotency"}
	case c.status == wagering.StatusFailed:
		return action{kind: actDLQ, code: codeInternalPermanentFailure, category: categoryDefinitive, outcome: "failed"}
	default:
		return action{kind: actDelete, outcome: strings.ToLower(string(c.status))}
	}
}
```

`backoff.go`:

```go
package sqsconsumer

import "time"

// maxBackoffExponent keeps 2^n seconds far from overflowing a Duration.
const maxBackoffExponent = 30

// retryDelay is the visibility of a message that failed transiently:
// min(2^receiveCount s, ceiling) (messaging.md §4.1). A missing receive count
// counts as the first receive.
func retryDelay(receiveCount int, ceiling time.Duration) time.Duration {
	n := min(max(receiveCount, 1), maxBackoffExponent)
	return min(time.Duration(1<<n)*time.Second, ceiling)
}
```

`dlq.go`:

```go
package sqsconsumer

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
)

// invalidMessagesGroup groups the DLQ copies of messages without a group.
const invalidMessagesGroup = "invalid-messages"

// dlqInput is the explicit send of msg to the DLQ (messaging.md §4.4): the
// original body and group, deduplicated by the SQS id of the original, so a
// crash between the send and the delete leaves at most one extra copy.
func dlqInput(dlqURL string, msg types.Message, a action, failedAt time.Time) *sqs.SendMessageInput {
	group := msg.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = invalidMessagesGroup
	}
	str := func(v string) types.MessageAttributeValue {
		return types.MessageAttributeValue{DataType: aws.String("String"), StringValue: aws.String(v)}
	}
	return &sqs.SendMessageInput{
		QueueUrl:               aws.String(dlqURL),
		MessageBody:            msg.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: msg.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			"errorCode":         str(a.code),
			"errorCategory":     str(a.category),
			"originalMessageId": str(aws.ToString(msg.MessageId)),
			"consumerName":      str(app.ConsumerName),
			"failedAt":          str(failedAt.UTC().Format("2006-01-02T15:04:05.000Z")),
		},
	}
}
```

`batch.go`:

```go
package sqsconsumer

import "github.com/aws/aws-sdk-go-v2/service/sqs/types"

// groupBatch splits a received batch by MessageGroupId, keeping the order of
// arrival inside each group and the order of first appearance between groups
// (messaging.md §4.2).
func groupBatch(msgs []types.Message) [][]types.Message {
	index := map[string]int{}
	var groups [][]types.Message
	for _, m := range msgs {
		g := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		i, ok := index[g]
		if !ok {
			i = len(groups)
			index[g] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], m)
	}
	return groups
}
```

- [ ] **Checkpoint:** `go test -race ./internal/adapters/sqsconsumer/` verde.

---

## Tarefa 6: Pausa por saúde

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/health_gate.go`, `internal/adapters/sqsconsumer/health_gate_test.go`

**Interfaces:**
- Produz (Tarefa 8): `type Pinger interface{ Ping(ctx) error }`; `newHealthGate(p Pinger, log *slog.Logger, interval time.Duration) *healthGate` com `Report(ctx)`, `Wait(ctx) error` e `Paused() bool`.

- [ ] **Passo 1: escrever o teste.** Criar `health_gate_test.go`:

```go
package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// flakyPinger fails while down is set and counts the pings.
type flakyPinger struct {
	down  atomic.Bool
	pings atomic.Int32
}

func (p *flakyPinger) Ping(context.Context) error {
	p.pings.Add(1)
	if p.down.Load() {
		return errors.New("connection refused")
	}
	return nil
}

// Covers: SQS-07 (messaging.md §4.3; spec M5, decision 6)
func TestHealthGate(t *testing.T) {
	t.Run("a transient error with the database up keeps the gate open", func(t *testing.T) {
		p := &flakyPinger{}
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		if g.Paused() || p.pings.Load() != 1 {
			t.Fatalf("paused = %v after %d pings, want open after 1", g.Paused(), p.pings.Load())
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := g.Wait(ctx); err != nil {
			t.Fatalf("Wait on an open gate: %v", err)
		}
	})

	t.Run("a transient error with the database down pauses until the ping answers", func(t *testing.T) {
		p := &flakyPinger{}
		p.down.Store(true)
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		if !g.Paused() {
			t.Fatal("gate open with the database down")
		}
		waited := make(chan error, 1)
		go func() { waited <- g.Wait(t.Context()) }()
		select {
		case err := <-waited:
			t.Fatalf("Wait returned %v with the database down", err)
		case <-time.After(100 * time.Millisecond):
		}
		if p.pings.Load() < 3 {
			t.Fatalf("%d pings while paused, want the gate to keep probing", p.pings.Load())
		}
		p.down.Store(false)
		select {
		case err := <-waited:
			if err != nil || g.Paused() {
				t.Fatalf("Wait = %v, paused = %v; want resumed", err, g.Paused())
			}
		case <-time.After(time.Second):
			t.Fatal("Wait did not resume after the database came back")
		}
	})

	t.Run("Wait ends with its context", func(t *testing.T) {
		p := &flakyPinger{}
		p.down.Store(true)
		g := newHealthGate(p, slog.New(slog.DiscardHandler), 10*time.Millisecond)
		g.Report(t.Context())
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		if err := g.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Wait = %v, want the context error", err)
		}
	})
}
```

- [ ] **Passo 2: stub.** Criar `health_gate.go`:

```go
package sqsconsumer

import (
	"context"
	"log/slog"
	"time"
)

// Pinger probes the database: *pgxpool.Pool (spec M5, decision 16).
type Pinger interface {
	Ping(ctx context.Context) error
}

type healthGate struct{}

func newHealthGate(p Pinger, log *slog.Logger, interval time.Duration) *healthGate { return &healthGate{} }

func (g *healthGate) Paused() bool { return false }

func (g *healthGate) Report(ctx context.Context) {}

func (g *healthGate) Wait(ctx context.Context) error { return nil }
```

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -race -run TestHealthGate ./internal/adapters/sqsconsumer/`
Esperado: FAIL nos 3 subtestes (`paused = false after 0 pings`, `gate open with the database down`, `Wait = <nil>, want the context error`).

- [ ] **Passo 4: implementar.** O `health_gate.go` final:

```go
package sqsconsumer

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Pinger probes the database: *pgxpool.Pool (spec M5, decision 16).
type Pinger interface {
	Ping(ctx context.Context) error
}

// pingTimeout bounds each probe of the gate.
const pingTimeout = time.Second

// healthGate is the pause of messaging.md §4.3 (spec M5, decision 6): after
// a transient error it pings the database; if the ping fails, the pollers
// stop receiving, so the messages still in the queue do not spend receives,
// and probe every interval until the database answers.
type healthGate struct {
	pinger   Pinger
	log      *slog.Logger
	interval time.Duration
	paused   atomic.Bool
}

func newHealthGate(p Pinger, log *slog.Logger, interval time.Duration) *healthGate {
	return &healthGate{pinger: p, log: log, interval: interval}
}

// Paused tells whether the pollers are held.
func (g *healthGate) Paused() bool { return g.paused.Load() }

// Report is called after a message failed transiently: it closes the gate
// when the database does not answer.
func (g *healthGate) Report(ctx context.Context) {
	if g.ping(ctx) == nil {
		return
	}
	if g.paused.CompareAndSwap(false, true) {
		g.log.WarnContext(ctx, "sqs consumer paused: database unavailable")
	}
}

// Wait returns at once when the gate is open; otherwise it probes every
// interval until the database answers, or ctx ends.
func (g *healthGate) Wait(ctx context.Context) error {
	if !g.paused.Load() {
		return nil
	}
	tick := time.NewTicker(g.interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		if !g.paused.Load() {
			return nil
		}
		if g.ping(ctx) == nil {
			if g.paused.CompareAndSwap(true, false) {
				g.log.InfoContext(ctx, "sqs consumer resumed: database available")
			}
			return nil
		}
	}
}

func (g *healthGate) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	return g.pinger.Ping(ctx)
}
```

- [ ] **Checkpoint:** `go test -race ./internal/adapters/sqsconsumer/` verde.

---

## Tarefa 7: `testkit` de SQS e o `Audit.Absent`

**Arquivos:**
- Criar: `test/testkit/sqs.go`
- Modificar: `test/testkit/audit.go`, `test/integration/harness_test.go`

**Interfaces:**
- Produz (Tarefas 8, 10 e 11): `testkit.WagerData`, `WagerMessage(tb, messageID string, d WagerData) string`, `SendOpts{GroupID, DedupID, CorrelationID}`, `SendMessage(tb, client, queueURL, body string, o SendOpts) string`, `DLQMessage{Body, GroupID, DedupID, Attributes}`, `ReceiveDLQ(tb, client, dlqURL string, n int) []DLQMessage`, `QueueDepth(ctx, client, queueURL) (int, error)`, `AssertQueueDrained(tb, client, queueURL)`.

- [ ] **Passo 1: helpers de SQS.** Criar `test/testkit/sqs.go` (utilitário de teste, exercitado pelos testes das Tarefas 8–11):

```go
package testkit

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// WagerData is the data of a WagerTransactionRequested (messaging.md §3.1);
// empty fields are omitted.
type WagerData struct {
	ProviderID                     string `json:"providerId,omitempty"`
	ExternalTransactionID          string `json:"externalTransactionId,omitempty"`
	IdempotencyKey                 string `json:"idempotencyKey,omitempty"`
	PlayerID                       string `json:"playerId,omitempty"`
	WalletID                       string `json:"walletId,omitempty"`
	RoundID                        string `json:"roundId,omitempty"`
	GameID                         string `json:"gameId,omitempty"`
	Kind                           string `json:"kind,omitempty"`
	Money                          *Money `json:"money,omitempty"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
}

// WagerMessage is the body of a WagerTransactionRequested with messageID.
func WagerMessage(tb testing.TB, messageID string, d WagerData) string {
	tb.Helper()
	body, err := json.Marshal(map[string]any{
		"messageId": messageID, "type": "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "data": d,
	})
	if err != nil {
		tb.Fatal(err)
	}
	return string(body)
}

// SendOpts are the send attributes of messaging.md §3.2.
type SendOpts struct {
	GroupID       string
	DedupID       string // "" = a new one: the app's deduplication is exercised, not the FIFO's (TST-C11)
	CorrelationID string // "" = no correlationId attribute
}

// SendMessage sends body to the FIFO queue and returns the SQS message id.
func SendMessage(tb testing.TB, client *sqs.Client, queueURL, body string, o SendOpts) string {
	tb.Helper()
	if o.DedupID == "" {
		o.DedupID = NewID()
	}
	in := &sqs.SendMessageInput{
		QueueUrl: aws.String(queueURL), MessageBody: aws.String(body),
		MessageGroupId: aws.String(o.GroupID), MessageDeduplicationId: aws.String(o.DedupID),
	}
	if o.CorrelationID != "" {
		in.MessageAttributes = map[string]types.MessageAttributeValue{
			"correlationId": {DataType: aws.String("String"), StringValue: aws.String(o.CorrelationID)},
		}
	}
	out, err := client.SendMessage(tb.Context(), in)
	if err != nil {
		tb.Fatalf("send to %s: %v", queueURL, err)
	}
	return aws.ToString(out.MessageId)
}

// DLQMessage is a message read from a dead-letter queue.
type DLQMessage struct {
	Body       string
	GroupID    string
	DedupID    string
	Attributes map[string]string // message attributes, by name
}

// ReceiveDLQ reads and deletes n messages of the DLQ, failing tb if they do
// not arrive within 20 s.
func ReceiveDLQ(tb testing.TB, client *sqs.Client, dlqURL string, n int) []DLQMessage {
	tb.Helper()
	var got []DLQMessage
	Eventually(tb, 20*time.Second, strconv.Itoa(n)+" messages in the DLQ", func(ctx context.Context) (bool, error) {
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(dlqURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
			MessageAttributeNames:       []string{"All"},
		})
		if err != nil {
			return false, err
		}
		for _, m := range out.Messages {
			attrs := map[string]string{}
			for k, v := range m.MessageAttributes {
				attrs[k] = aws.ToString(v.StringValue)
			}
			got = append(got, DLQMessage{
				Body: aws.ToString(m.Body), Attributes: attrs,
				GroupID: m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
				DedupID: m.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
			})
			if _, err := client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(dlqURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
				return false, err
			}
		}
		return len(got) >= n, nil
	})
	if len(got) != n {
		tb.Fatalf("DLQ has %d messages, want %d", len(got), n)
	}
	return got
}

// QueueDepth is the visible plus in-flight messages of the queue.
func QueueDepth(ctx context.Context, client *sqs.Client, queueURL string) (int, error) {
	out, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		return 0, err
	}
	total := 0
	for _, k := range []types.QueueAttributeName{
		types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
	} {
		n, err := strconv.Atoi(out.Attributes[string(k)])
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// AssertQueueDrained waits until the queue has no visible nor in-flight message.
func AssertQueueDrained(tb testing.TB, client *sqs.Client, queueURL string) {
	tb.Helper()
	Eventually(tb, 20*time.Second, "queue drained", func(ctx context.Context) (bool, error) {
		n, err := QueueDepth(ctx, client, queueURL)
		return n == 0, err
	})
}
```

- [ ] **Passo 2: o teste que reproduz o flake do I05b** (bug: teste primeiro, workflow §4.2). Acrescentar a `test/integration/harness_test.go`:

```diff
@@ -116,3 +116,36 @@ func TestAuditCollector(t *testing.T) {
 	}
 	audit.Absent(t, time.Second, testkit.NewID())
 }
+
+// Covers: OUT-10 (spec M5 §2.2, decision 18)
+// Sensitivity: the Absent of M4, which canceled its last long poll at the end
+// of the window → "event sent after Absent: … not delivered".
+//
+// An event that arrives right after Absent is still collected: Absent leaves
+// no long poll canceled halfway, which the broker keeps open until its wait
+// ends and which takes the next message, hiding it for a visibility timeout
+// (the flake of I05b).
+func TestAuditAbsentLeavesNoPollBehind(t *testing.T) {
+	t.Parallel()
+	root := testkit.RootAWSConfig(t)
+	snsClient, sqsClient := sns.NewFromConfig(root), sqs.NewFromConfig(root)
+	topic := testkit.NewEventsTopic(t, sqsClient, snsClient)
+	audit, err := testkit.NewAudit(t.Context(), sqsClient, topic.AuditQueueURL)
+	if err != nil {
+		t.Fatal(err)
+	}
+	wallet := testkit.NewID()
+	for range 3 {
+		id := testkit.NewID()
+		audit.Absent(t, 1500*time.Millisecond, id)
+		// Straight into the audit queue: nothing else receives from it meanwhile.
+		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id})
+		time.Sleep(300 * time.Millisecond)
+		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
+		_, err := audit.Wait(ctx, id)
+		cancel()
+		if err != nil {
+			t.Fatalf("event sent after Absent: %v", err)
+		}
+	}
+}
```

- [ ] **Passo 3: ver falhar.**

Rodar: `go test -tags=integration -race -count=3 -run TestAuditAbsentLeavesNoPollBehind ./test/integration/`
Esperado: FAIL nas 3 execuções, com `event sent after Absent: audit: 1 of 1 events not delivered (…): … context deadline exceeded`. O último receive do `Absent` foi cancelado no meio; o poll continua aberto no MiniStack e pega a mensagem (spec §2.2, decisão 18).

- [ ] **Passo 4: corrigir o `Absent`.** Em `test/testkit/audit.go`:

```diff
@@ -17,6 +17,9 @@ import (
 // AuditTimeout bounds how long WaitFor waits for the events.
 const AuditTimeout = 10 * time.Second
 
+// receiveTimeout bounds one receive of Absent: well above its 1 s long poll.
+const receiveTimeout = 5 * time.Second
+
 // Attribute is one message attribute of a delivery.
 type Attribute struct {
 	Type  string // String or Number
@@ -90,12 +93,19 @@ func (a *Audit) WaitFor(tb testing.TB, ids ...string) map[string][]AuditMessage
 	return got
 }
 
-// Absent receives during window and fails tb if any of ids arrives.
+// Absent receives during window and fails tb if any of ids arrives. No
+// receive is canceled halfway: a long poll canceled by the client stays open
+// in the broker and would take, and hide for the visibility timeout, the
+// event delivered right after the window (spec M5 §2.2).
 func (a *Audit) Absent(tb testing.TB, window time.Duration, ids ...string) {
 	tb.Helper()
-	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), window)
-	defer cancel()
-	for a.receive(ctx) == nil {
+	for deadline := time.Now().Add(window); time.Now().Before(deadline); {
+		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), receiveTimeout)
+		err := a.receive(ctx)
+		cancel()
+		if err != nil {
+			tb.Fatalf("audit: %v", err)
+		}
 	}
 	for id, deliveries := range a.lookupAll(ids) {
 		if len(deliveries) > 0 {
```

- [ ] **Passo 5: ver passar.**

Rodar: `go test -tags=integration -race -count=3 -run TestAuditAbsentLeavesNoPollBehind ./test/integration/` e `go test -tags=integration -race -count=8 -run TestNoPublishBeforeCommit ./internal/adapters/outbox/`
Esperado: `ok` nos dois.

- [ ] **Checkpoint:** `go vet -tags=integration ./test/...` limpo e os dois comandos do passo 5 verdes.

---

## Tarefa 8: Consumidor — núcleo, DLQ e redrive

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/consumer.go` (versão da Tarefa 8), `consumer_test.go`
- Criar: `internal/adapters/sqsconsumer/main_integration_test.go`, `helpers_integration_test.go`, `consumer_integration_test.go`, `failures_integration_test.go`

**Interfaces:**
- Consome: Tarefas 2–7.
- Produz (Tarefas 9–11): `sqsconsumer.Processor`, `QueueAPI`, `Metrics`, `Options{Pollers, ReceiveBatch, WaitTime, Visibility, ProcessingTimeout, MaxInFlight, RetryMaxDelay, ShutdownTimeout, DLQName}`, `NewConsumer(api QueueAPI, queues *awsclient.Queues, proc Processor, db Pinger, m Metrics, log *slog.Logger, opts Options) *Consumer`, `(*Consumer).Start(ctx)` e `(*Consumer).Stop(ctx) error`.

- [ ] **Passo 1: testes unitários do laço de recebimento** (Foco de revisão 1–3). Criar `consumer_test.go`:

```go
package sqsconsumer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)

// fakeQueue answers the receives with batches, then with receiveErr or empty
// batches, and records the other calls.
type fakeQueue struct {
	QueueAPI
	mu         sync.Mutex
	batches    [][]types.Message
	receiveErr error
	deleteErr  error
	receives   []time.Time
	deletes    atomic.Int32
}

func (q *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.mu.Lock()
	q.receives = append(q.receives, time.Now())
	var batch []types.Message
	if len(q.batches) > 0 {
		batch, q.batches = q.batches[0], q.batches[1:]
	}
	err := q.receiveErr
	q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (q *fakeQueue) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	q.deletes.Add(1)
	return &sqs.DeleteMessageOutput{}, q.deleteErr
}

func (q *fakeQueue) GetQueueAttributes(context.Context, *sqs.GetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	return &sqs.GetQueueAttributesOutput{Attributes: map[string]string{"ApproximateNumberOfMessages": "0"}}, nil
}

func (q *fakeQueue) receiveTimes() []time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]time.Time(nil), q.receives...)
}

// countingMetrics counts what the consumer reports.
type countingMetrics struct {
	receiveFailed, deleteFailed, duplicates atomic.Int32
}

func (*countingMetrics) Received()                       {}
func (*countingMetrics) Processed(string, time.Duration) {}
func (m *countingMetrics) Duplicate(string)              { m.duplicates.Add(1) }
func (*countingMetrics) Retried(string)                  {}
func (*countingMetrics) SentToDLQ(string)                {}
func (*countingMetrics) DLQDepth(string, int)            {}
func (m *countingMetrics) ReceiveFailed()                { m.receiveFailed.Add(1) }
func (m *countingMetrics) DeleteFailed()                 { m.deleteFailed.Add(1) }

type upPinger struct{}

func (upPinger) Ping(context.Context) error { return nil }

type duplicateProcessor struct{}

func (duplicateProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	return app.ConsumeResult{Duplicate: true}, nil
}

func unitConsumer(t *testing.T, q *fakeQueue, m *countingMetrics, waitTime time.Duration) *Consumer {
	t.Helper()
	c := NewConsumer(q, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, duplicateProcessor{}, upPinger{}, m,
		slog.New(slog.DiscardHandler), Options{
			Pollers: 1, ReceiveBatch: 10, WaitTime: waitTime, Visibility: 5 * time.Second,
			ProcessingTimeout: 3 * time.Second, MaxInFlight: 4, RetryMaxDelay: time.Second,
			ShutdownTimeout: time.Second, DLQName: "dlq",
		})
	c.Start(t.Context())
	t.Cleanup(func() { _ = c.Stop(context.WithoutCancel(t.Context())) })
	return c
}

// Covers: SQS-10 (spec M5 §2.2, decision 19)
// Sensitivity: without the pause → "280147 empty receives in 350 ms".
func TestShortPollingPauses(t *testing.T) {
	q := &fakeQueue{}
	c := unitConsumer(t, q, &countingMetrics{}, 0)
	time.Sleep(350 * time.Millisecond)
	if err := c.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(q.receiveTimes()); n < 2 || n > 5 {
		t.Fatalf("%d empty receives in 350 ms, want one every 100 ms", n)
	}
}

// Covers: SQS-05 (messaging.md §4.2)
// Sensitivity: without DeleteFailed → "delete failures 0".
func TestDeleteFailureIsCounted(t *testing.T) {
	msg := types.Message{
		MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(validBody),
		Attributes: map[string]string{"MessageGroupId": "g"},
	}
	q := &fakeQueue{batches: [][]types.Message{{msg}}, deleteErr: errors.New("throttled")}
	m := &countingMetrics{}
	unitConsumer(t, q, m, 0)
	deadline := time.Now().Add(2 * time.Second)
	for m.deleteFailed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if m.deleteFailed.Load() != 1 || m.duplicates.Load() != 1 || q.deletes.Load() != 1 {
		t.Fatalf("delete failures %d, duplicates %d, deletes %d; want 1 each", m.deleteFailed.Load(), m.duplicates.Load(), q.deletes.Load())
	}
}

// Covers: SQS-07 (messaging.md §4.3)
// Sensitivity: without the wait → thousands of receives in 1.5 s.
func TestReceiveFailureBackoff(t *testing.T) {
	q := &fakeQueue{receiveErr: errors.New("connection refused")}
	m := &countingMetrics{}
	c := unitConsumer(t, q, m, time.Second)
	time.Sleep(1500 * time.Millisecond)
	if err := c.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	times := q.receiveTimes()
	if len(times) != 2 || times[1].Sub(times[0]) < time.Second || m.receiveFailed.Load() != 2 {
		t.Fatalf("%d receives and %d failures in 1.5 s, want 2 receives 1 s apart", len(times), m.receiveFailed.Load())
	}
}
```

- [ ] **Passo 2: testes de integração.** Criar `main_integration_test.go`:

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// env is this package's isolated database; every test creates queues of its own.
var env *testkit.Env

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	e, cleanup, err := testkit.NewEnv(ctx, "sqsconsumer")
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.NewEnv:", err)
		os.Exit(1)
	}
	env = e
	code := m.Run()
	cleanup()
	os.Exit(code)
}
```

`helpers_integration_test.go` (inclui os dublês usados também nas Tarefas 9 e 10):

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

// fixture is a pair of isolated queues (redrive after 3 receives) and a
// consumer over them, built without Fx.
type fixture struct {
	sqs    *sqs.Client
	queues awsclient.Queues
	reg    *prometheus.Registry
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	client := sqs.NewFromConfig(testkit.RootAWSConfig(t))
	wager, dlq := testkit.CreateQueues(t, client)
	queues, err := awsclient.ResolveQueues(t.Context(), client, wager, dlq)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{sqs: client, queues: queues, reg: observability.NewRegistry()}
}

// options are the accelerated settings of test-plan §3.3.
func options() sqsconsumer.Options {
	c := env.Config()
	return sqsconsumer.Options{
		Pollers: c.SQSConsumerPollers, ReceiveBatch: c.SQSReceiveBatch, WaitTime: c.SQSWaitTime,
		Visibility: c.SQSVisibilityTimeout, ProcessingTimeout: c.SQSProcessingTimeout, MaxInFlight: c.SQSMaxInFlight,
		RetryMaxDelay: c.SQSRetryMaxDelay, ShutdownTimeout: c.ShutdownTimeout, DLQName: "dlq",
	}
}

// consumerOpts customizes one consumer of a fixture.
type consumerOpts struct {
	proc   sqsconsumer.Processor // nil = the real use case
	api    sqsconsumer.QueueAPI  // nil = the SQS client
	pinger sqsconsumer.Pinger    // nil = the package database
	opts   *sqsconsumer.Options  // nil = options()
}

// start runs a consumer until the returned stop, or the end of the test.
func (f *fixture) start(t *testing.T, o consumerOpts) (c *sqsconsumer.Consumer, stop func() error) {
	t.Helper()
	if o.proc == nil {
		o.proc = newConsumeWager(newUoW())
	}
	if o.api == nil {
		o.api = f.sqs
	}
	if o.pinger == nil {
		o.pinger = env.App
	}
	opts := options()
	if o.opts != nil {
		opts = *o.opts
	}
	c = sqsconsumer.NewConsumer(o.api, &f.queues, o.proc, o.pinger, observability.NewMetrics(f.reg), slog.New(slog.DiscardHandler), opts)
	c.Start(t.Context())
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
			defer cancel()
			err = c.Stop(ctx)
		})
		return err
	}
	t.Cleanup(func() { _ = stop() })
	return c, stop
}

func (f *fixture) send(t *testing.T, body, group string) string {
	t.Helper()
	return testkit.SendMessage(t, f.sqs, f.queues.WagerURL, body, testkit.SendOpts{GroupID: group})
}

// metric is the value of the counter or gauge sample of name whose labels
// contain all of labels ("k=v"), or 0; the values the tests read are counts.
func (f *fixture) metric(t *testing.T, name string, labels ...string) int {
	t.Helper()
	families, err := f.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, fam := range families {
		if fam.GetName() != name {
			continue
		}
	metrics:
		for _, m := range fam.GetMetric() {
			have := map[string]string{}
			for _, l := range m.GetLabel() {
				have[l.GetName()] = l.GetValue()
			}
			for _, kv := range labels {
				k, v, _ := strings.Cut(kv, "=")
				if have[k] != v {
					continue metrics
				}
			}
			if c := m.GetCounter(); c != nil {
				return int(c.GetValue())
			}
			return int(m.GetGauge().GetValue())
		}
	}
	return 0
}

func newUoW() app.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

func newConsumeWager(uow app.UnitOfWork) *app.ConsumeWager {
	reads := postgres.NewRepos(env.App)
	policy, err := wagering.NewReferenceRetryPolicy(time.Minute, time.Minute, 3, 10*time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return app.NewConsumeWager(reads, app.NewProcessWager(uow, reads, app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler)))
}

// testWallet is a wallet opened through the use case.
type testWallet struct{ id, player string }

func openWallet(t *testing.T, initial string) testWallet {
	t.Helper()
	player, amount, currency := testkit.NewID(), initial, "BRL"
	w, err := app.NewOpenWallet(newUoW(), app.SystemClock{}, app.UUIDv7{}).Execute(t.Context(), app.OpenWalletInput{
		PlayerID: &player, InitialBalance: &wagering.MoneyInput{Amount: &amount, Currency: &currency},
	}, "corr-open")
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, w.ID()) })
	return testWallet{id: w.ID(), player: player}
}

// wager is a WagerTransactionRequested for w; ext is also the idempotency key suffix.
func wager(t *testing.T, messageID string, w testWallet, provider, kind, amount, ext, ref string) string {
	t.Helper()
	return testkit.WagerMessage(t, messageID, testkit.WagerData{
		ProviderID: provider, ExternalTransactionID: ext, IdempotencyKey: provider + ":" + ext,
		PlayerID: w.player, WalletID: w.id, RoundID: "round-1", GameID: "game-1", Kind: kind,
		Money: &testkit.Money{Amount: amount, Currency: "BRL"}, ReferenceExternalTransactionID: ref,
	})
}

func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// balance is the stored balance of the wallet.
func balance(t *testing.T, walletID string) string {
	t.Helper()
	w, err := postgres.NewRepos(env.App).Wallets().Get(t.Context(), walletID)
	if err != nil {
		t.Fatalf("wallet %s: %v", walletID, err)
	}
	return w.Balance().String()
}

// processorFunc adapts a function to sqsconsumer.Processor.
type processorFunc func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error)

func (f processorFunc) Execute(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
	return f(ctx, m)
}

// failingOutboxUoW makes every Outbox().Insert fail permanently, the forced
// permanent failure of I03b; the FAILED of the separate transaction is written.
type failingOutboxUoW struct{ app.UnitOfWork }

func (u failingOutboxUoW) Do(ctx context.Context, fn func(app.Repos) error) error {
	return u.UnitOfWork.Do(ctx, func(r app.Repos) error { return fn(outboxFails{r}) })
}

type outboxFails struct{ app.Repos }

func (outboxFails) Outbox() app.OutboxRepository { return failingOutbox{} }

type failingOutbox struct{}

func (failingOutbox) Insert(context.Context, ...events.Envelope) error {
	return apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))
}

// queueSpy counts the receives and fails the first failSends DLQ sends.
type queueSpy struct {
	sqsconsumer.QueueAPI
	receives  atomic.Int32
	failSends atomic.Int32
}

func (q *queueSpy) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.receives.Add(1)
	return q.QueueAPI.ReceiveMessage(ctx, in, optFns...)
}

func (q *queueSpy) SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	if q.failSends.Add(-1) >= 0 {
		return nil, errors.New("dlq unavailable")
	}
	return q.QueueAPI.SendMessage(ctx, in, optFns...)
}

// switchPinger fails while down is set.
type switchPinger struct{ down atomic.Bool }

func (p *switchPinger) Ping(ctx context.Context) error {
	if p.down.Load() {
		return errors.New("connection refused")
	}
	return env.App.Ping(ctx)
}
```

`consumer_integration_test.go` (I04a–c):

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: TST-I04, SQS-03, SQS-05, TST-C11 (I04a)
// Sensitivity: no DeleteMessage after the commit → the deliveries were
// redriven and "DLQ depth = 2" (before the DLQ check, the redrive drained the
// queue and the test passed).
func TestInboxDeduplication(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{})
	msgID := "msg-" + testkit.NewID()
	body := wager(t, msgID, w, p, "BET", "30.00", "bet-1", "")
	f.send(t, body, w.id) // two sends, two MessageDeduplicationIds
	f.send(t, body, w.id)

	testkit.Eventually(t, 20*time.Second, "the second delivery counted as an inbox duplicate", func(context.Context) (bool, error) {
		return f.metric(t, "wager_duplicates_total", "channel=sqs", "layer=inbox") == 1, nil
	})
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n, err := testkit.QueueDepth(t.Context(), f.sqs, f.queues.DLQURL); err != nil || n != 0 {
		t.Fatalf("DLQ depth = %d, %v; want both deliveries deleted, none redriven", n, err)
	}
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
		t.Fatalf("%d operations, want 1", n)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.id); n != 2 {
		t.Fatalf("%d entries, want the opening and one debit", n)
	}
	if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msgID); n != 1 {
		t.Fatalf("%d inbox rows, want 1", n)
	}
	if got := balance(t, w.id); got != "70.00" {
		t.Fatalf("balance = %s, want 70.00", got)
	}
}

// Covers: SQS-03 (I04b)
func TestInboxHashMismatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{})
	msgID := "msg-" + testkit.NewID()
	f.send(t, wager(t, msgID, w, p, "BET", "30.00", "bet-1", ""), w.id)
	testkit.Eventually(t, 20*time.Second, "the first message concluded", func(ctx context.Context) (bool, error) {
		var n int
		err := env.Owner.QueryRow(ctx, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, msgID).Scan(&n)
		return n == 1, err
	})

	f.send(t, wager(t, msgID, w, p, "BET", "31.00", "bet-1", ""), w.id)
	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != app.CodeMessageHashMismatch || !strings.Contains(got.Body, `"31.00"`) {
		t.Fatalf("DLQ = %+v, want the second message with MESSAGE_HASH_MISMATCH", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}

// Covers: SQS-07, SQS-10 (I04c)
func TestInvalidMessagesGoToDLQ(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	ghost := testWallet{id: testkit.NewID(), player: w.player}
	valid := wager(t, "msg-x", w, p, "BET", "1.00", "bet-x", "")
	cases := map[string]string{ // errorCode by body
		"MALFORMED_MESSAGE":        `{"messageId":`,
		"UNSUPPORTED_MESSAGE_TYPE": strings.Replace(valid, "WagerTransactionRequested", "WalletOpened", 1),
		"OPENING_NOT_ALLOWED":      wager(t, "msg-"+testkit.NewID(), w, p, "OPENING", "1.00", "open-1", ""),
		"UNKNOWN_WALLET":           wager(t, "msg-"+testkit.NewID(), ghost, p, "BET", "1.00", "bet-2", ""),
	}
	unknownField := strings.Replace(valid, `"type"`, `"extra":1,"type"`, 1)
	f.start(t, consumerOpts{})
	sent := map[string]string{} // SQS id → expected code
	bodies := map[string]string{}
	for code, body := range cases {
		id := f.send(t, body, "group-"+code)
		sent[id], bodies[id] = code, body
	}
	id := f.send(t, unknownField, "group-unknown-field")
	sent[id], bodies[id] = "MALFORMED_MESSAGE", unknownField

	for _, m := range testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, len(sent)) {
		orig := m.Attributes["originalMessageId"]
		if m.Attributes["errorCode"] != sent[orig] || m.Body != bodies[orig] || m.DedupID != orig ||
			m.Attributes["errorCategory"] != "CORRECTABLE" || m.Attributes["consumerName"] != app.ConsumerName ||
			!strings.HasSuffix(m.Attributes["failedAt"], "Z") || !strings.HasPrefix(m.GroupID, "group-") {
			t.Errorf("DLQ message = %+v, want errorCode %s for %s", m, sent[orig], orig)
		}
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
		t.Fatalf("%d operations recorded from invalid messages", n)
	}
}
```

`failures_integration_test.go` (I04d, I03b pelo SQS, falha no envio à DLQ):

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: SQS-07, TST-I05 (I04d)
// Sensitivity: a retry that also deleted the message → "1 messages in the
// DLQ: not reached within 20s".
func TestTransientFailureRedrive(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	var calls atomic.Int32
	proc := processorFunc(func(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
		calls.Add(1)
		return app.ConsumeResult{}, apperrors.New(apperrors.KindTransient, "", errors.New("forced transient failure"))
	})
	f.start(t, consumerOpts{proc: proc})
	body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
	f.send(t, body, "group-1")

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Body != body || got.Attributes["errorCode"] != "" {
		t.Fatalf("DLQ = %+v, want the original message moved by the redrive", got)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("processed %d times, want the 3 receives of the redrive policy", n)
	}
	if n := f.metric(t, "sqs_retries_total", "reason=transient"); n != 3 {
		t.Fatalf("sqs_retries_total{transient} = %v, want 3", n)
	}
	if f.metric(t, "sqs_dlq_sent_total") != 0 {
		t.Fatal("a transient failure was sent to the DLQ explicitly")
	}
}

// Covers: TX-06, SQS-07, TST-I03 (I03b, the SQS part)
func TestPermanentFailureToDLQ(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	f.start(t, consumerOpts{proc: newConsumeWager(failingOutboxUoW{newUoW()})})
	msgID := "msg-" + testkit.NewID()
	f.send(t, wager(t, msgID, w, p, "BET", "30.00", "bet-1", ""), w.id)

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != "INTERNAL_PERMANENT_FAILURE" || got.Attributes["errorCategory"] != "DEFINITIVE" {
		t.Fatalf("DLQ = %+v, want INTERNAL_PERMANENT_FAILURE", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := count(t, `SELECT count(*) FROM inbox_messages i JOIN wager_transactions t ON t.id = i.transaction_id
		WHERE i.message_id = $1 AND i.outcome = 'FAILED' AND t.status = 'FAILED'`, msgID); n != 1 {
		t.Fatalf("inbox FAILED rows with their FAILED operation = %d, want 1", n)
	}
	if b := balance(t, w.id); b != "100.00" {
		t.Fatalf("balance = %s, want 100.00", b)
	}
}

// Covers: SQS-05, SQS-07 (messaging.md §4.4)
// Sensitivity: deleting after a failed DLQ send → "1 messages in the DLQ: not
// reached within 20s".
func TestDLQSendFailure(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	spy := &queueSpy{QueueAPI: f.sqs}
	spy.failSends.Store(1)
	f.start(t, consumerOpts{api: spy})
	f.send(t, `{"messageId":`, "group-1")

	got := testkit.ReceiveDLQ(t, f.sqs, f.queues.DLQURL, 1)[0]
	if got.Attributes["errorCode"] != "MALFORMED_MESSAGE" {
		t.Fatalf("DLQ = %+v", got)
	}
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := f.metric(t, "sqs_retries_total", "reason=transient"); n != 1 {
		t.Fatalf("sqs_retries_total{transient} = %v, want the failed send retried once", n)
	}
	if n := f.metric(t, "sqs_dlq_sent_total", "reason=MALFORMED_MESSAGE"); n != 1 {
		t.Fatalf("sqs_dlq_sent_total = %v, want 1", n)
	}
}
```

- [ ] **Passo 3: stub.** Um `consumer.go` com os tipos e as portas da versão abaixo, mas com `Start` e `Stop` vazios (`func (c *Consumer) Start(ctx context.Context) {}`, `func (c *Consumer) Stop(ctx context.Context) error { return nil }`).

- [ ] **Passo 4: ver falhar.**

Rodar: `go test -race ./internal/adapters/sqsconsumer/` e `go test -tags=integration -race -count=1 -run 'TestInbox|TestInvalid|TestTransient|TestPermanent|TestDLQSend' ./internal/adapters/sqsconsumer/`
Esperado: FAIL — `0 empty receives in 350 ms`, `delete failures 0, duplicates 0, deletes 0`, `0 receives and 0 failures`; nos de integração, esperas vencidas (`… not reached within 20s`).

- [ ] **Passo 5: implementar a versão da Tarefa 8.** Sem a liberação do resto do grupo, sem a liberação por prazo, sem acionar o gate (Tarefa 9) e com um stop simples (Tarefa 10). `consumer.go`:

```go
// Package sqsconsumer consumes wager-transactions.fifo (D-12, messaging.md §4).
package sqsconsumer

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
)

// Processor concludes one message: app.ConsumeWager.
type Processor interface {
	Execute(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error)
}

// QueueAPI is the subset of *sqs.Client the consumer uses.
type QueueAPI interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

// Metrics is what the consumer reports (messaging.md §8).
type Metrics interface {
	Received()
	Processed(outcome string, d time.Duration)
	Duplicate(layer string)
	Retried(reason string)
	SentToDLQ(reason string)
	DLQDepth(queue string, n int)
	ReceiveFailed()
	DeleteFailed()
}

// Options are the consumer settings (messaging.md §4.1).
type Options struct {
	Pollers           int
	ReceiveBatch      int
	WaitTime          time.Duration
	Visibility        time.Duration
	ProcessingTimeout time.Duration
	MaxInFlight       int
	RetryMaxDelay     time.Duration
	ShutdownTimeout   time.Duration
	DLQName           string // label of sqs_dlq_depth
}

const (
	// receiveRetryMin and receiveRetryMax bound the wait after a failed
	// ReceiveMessage (messaging.md §4.3).
	receiveRetryMin = time.Second
	receiveRetryMax = 30 * time.Second
	// gateInterval is how often a paused consumer pings the database.
	gateInterval = 2 * time.Second
	// queueTimeout bounds each delete, visibility change and DLQ send, which
	// run detached from the cancellation of the work (messaging.md §4.5).
	queueTimeout = 2 * time.Second
	// emptyPollPause spaces the empty receives of short polling (SQS_WAIT_TIME=0).
	emptyPollPause = 100 * time.Millisecond
	// dlqDepthEvery is how often sqs_dlq_depth is refreshed.
	dlqDepthEvery = 30 * time.Second
	// correlationAttribute is the optional message attribute of messaging.md §3.2.
	correlationAttribute = "correlationId"
)

// Consumer is the SQS consumer of one instance (D-12). Any number of
// instances consume the same queue.
type Consumer struct {
	api     QueueAPI
	queues  *awsclient.Queues
	proc    Processor
	gate    *healthGate
	metrics Metrics
	log     *slog.Logger
	opts    Options
	sem     chan struct{}

	// stopPolling stops the receiving; abortWork cancels the processing in
	// flight, only when the shutdown deadline passes (messaging.md §4.5).
	stopPolling, abortWork context.CancelFunc
	stopping               atomic.Bool
	running                sync.WaitGroup
}

// NewConsumer builds a consumer; Start starts it.
func NewConsumer(api QueueAPI, queues *awsclient.Queues, proc Processor, db Pinger, m Metrics, log *slog.Logger, opts Options) *Consumer {
	return &Consumer{
		api: api, queues: queues, proc: proc, gate: newHealthGate(db, log, gateInterval), metrics: m, log: log,
		opts: opts, sem: make(chan struct{}, opts.MaxInFlight),
	}
}

// Start runs the pollers and the DLQ depth gauge until Stop, detached from the
// cancellation of ctx (the start of the application). poll carries the
// receiving and work the processing, so that the shutdown ends them apart.
func (c *Consumer) Start(ctx context.Context) {
	poll, stopPolling := context.WithCancel(context.WithoutCancel(ctx))
	work, abortWork := context.WithCancel(context.WithoutCancel(ctx))
	c.stopPolling, c.abortWork = stopPolling, abortWork
	for range c.opts.Pollers {
		c.running.Go(func() { c.pollLoop(poll, work) })
	}
	c.running.Go(func() { c.dlqDepthLoop(poll) })
}

// Stop is the shutdown of messaging.md §4.5: stop receiving; release what was
// received and not started; wait for the messages in flight until
// ShutdownTimeout (or ctx); then cancel them, which rolls their transactions
// back, and release them. No message is deleted without a commit.
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopping.Store(true)
	c.stopPolling()
	done := make(chan struct{})
	go func() {
		c.running.Wait()
		close(done)
	}()
	defer c.abortWork()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Consumer) pollLoop(poll, work context.Context) {
	var delay time.Duration
	for poll.Err() == nil {
		if err := c.gate.Wait(poll); err != nil {
			return
		}
		out, err := c.api.ReceiveMessage(poll, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(c.queues.WagerURL),
			MaxNumberOfMessages: int32(c.opts.ReceiveBatch),             //nolint:gosec // 1..10, validated by config
			WaitTimeSeconds:     int32(c.opts.WaitTime / time.Second),   //nolint:gosec // 0..20, validated by config
			VisibilityTimeout:   int32(c.opts.Visibility / time.Second), //nolint:gosec // ≤ 12 h, validated by config
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{
				types.MessageSystemAttributeNameMessageGroupId, types.MessageSystemAttributeNameApproximateReceiveCount,
			},
			MessageAttributeNames: []string{correlationAttribute},
		})
		if err != nil {
			if poll.Err() != nil {
				return
			}
			c.metrics.ReceiveFailed()
			delay = min(max(2*delay, receiveRetryMin), receiveRetryMax)
			c.log.Warn("sqs receive failed", "error", err.Error(), "retryIn", delay.String())
			select {
			case <-poll.Done():
				return
			case <-time.After(delay):
			}
			continue
		}
		delay = 0
		if len(out.Messages) == 0 && c.opts.WaitTime == 0 {
			// Short polling: an empty queue would otherwise be polled in a busy loop.
			select {
			case <-poll.Done():
				return
			case <-time.After(emptyPollPause):
			}
			continue
		}
		c.handleBatch(poll, work, out.Messages, time.Now())
	}
}

// handleBatch runs the groups of a batch in parallel and the messages of a
// group in order, and returns when all of them are settled.
func (c *Consumer) handleBatch(poll, work context.Context, msgs []types.Message, receivedAt time.Time) {
	for range msgs {
		c.metrics.Received()
	}
	var wg sync.WaitGroup
	for _, group := range groupBatch(msgs) {
		wg.Go(func() { c.handleGroup(poll, work, group, receivedAt) })
	}
	wg.Wait()
}

// handleGroup processes the group in order. A message that is retried or
// released holds the rest of its group, which is released too, so the order
// of the group survives (messaging.md §4.2). Before each message: the
// shutdown releases what was not started, and a message whose visibility may
// run out before its processing deadline is released unprocessed (spec M5,
// decision 8).
func (c *Consumer) handleGroup(poll, work context.Context, group []types.Message, receivedAt time.Time) {
	for i, msg := range group {
		select {
		case c.sem <- struct{}{}:
		case <-poll.Done():
			c.releaseAll(work, group[i:], "")
			return
		}
		c.handle(work, msg, receivedAt)
		<-c.sem
	}
}

// handle concludes one message and applies the action.
func (c *Consumer) handle(work context.Context, msg types.Message, receivedAt time.Time) action {
	start := time.Now()
	m, err := parseEnvelope(aws.ToString(msg.Body), aws.ToString(msg.MessageAttributes[correlationAttribute].StringValue), receivedAt)
	var res app.ConsumeResult
	if err == nil {
		ctx, cancel := context.WithTimeout(work, c.opts.ProcessingTimeout)
		res, err = c.proc.Execute(ctx, m)
		cancel()
	}
	a := decide(conclude(res, err), c.stopping.Load(), receiveCount(msg), c.opts.RetryMaxDelay)
	c.apply(work, msg, a, err)
	if a.outcome != "" {
		c.metrics.Processed(a.outcome, time.Since(start))
	}
	if a.duplicate != "" {
		c.metrics.Duplicate(a.duplicate)
	}
	return a
}

// apply performs the action on the queue, detached from the cancellation of
// the work, so a shutdown never leaves a concluded message undeleted.
func (c *Consumer) apply(work context.Context, msg types.Message, a action, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(work), queueTimeout)
	defer cancel()
	id := aws.ToString(msg.MessageId)
	switch a.kind {
	case actDelete:
		// Fault point consumer.after_commit_before_delete (M8).
		c.delete(ctx, msg)
	case actDLQ:
		if _, err := c.api.SendMessage(ctx, dlqInput(c.queues.DLQURL, msg, a, time.Now())); err != nil {
			delay := retryDelay(receiveCount(msg), c.opts.RetryMaxDelay)
			c.log.Warn("sqs dlq send failed", "sqsMessageId", id, "errorCode", a.code, "error", err.Error())
			c.changeVisibility(ctx, msg, delay)
			c.metrics.Retried("transient")
			return
		}
		c.metrics.SentToDLQ(a.code)
		c.log.Warn("sqs message sent to the dlq", "sqsMessageId", id, "errorCode", a.code, "errorCategory", a.category)
		c.delete(ctx, msg)
	case actRetry:
		c.log.Warn("sqs message failed transiently", "sqsMessageId", id, "retryIn", a.delay.String(), "error", errorText(cause))
		c.changeVisibility(ctx, msg, a.delay)
		c.metrics.Retried("transient")
	case actRelease:
		c.changeVisibility(ctx, msg, 0)
	}
}

// releaseAll makes msgs visible again at once; reason, when set, is counted.
func (c *Consumer) releaseAll(work context.Context, msgs []types.Message, reason string) {
	if len(msgs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(work), queueTimeout)
	defer cancel()
	for _, msg := range msgs {
		c.changeVisibility(ctx, msg, 0)
		if reason != "" {
			c.metrics.Retried(reason)
		}
	}
}

func (c *Consumer) delete(ctx context.Context, msg types.Message) {
	if _, err := c.api.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl: aws.String(c.queues.WagerURL), ReceiptHandle: msg.ReceiptHandle,
	}); err != nil {
		c.metrics.DeleteFailed()
		c.log.Warn("sqs delete failed", "sqsMessageId", aws.ToString(msg.MessageId), "error", err.Error())
	}
}

func (c *Consumer) changeVisibility(ctx context.Context, msg types.Message, d time.Duration) {
	if _, err := c.api.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl: aws.String(c.queues.WagerURL), ReceiptHandle: msg.ReceiptHandle,
		VisibilityTimeout: int32(d / time.Second), //nolint:gosec // ≤ 12 h, validated by config
	}); err != nil {
		c.log.Warn("sqs visibility change failed", "sqsMessageId", aws.ToString(msg.MessageId), "error", err.Error())
	}
}

// dlqDepthLoop refreshes sqs_dlq_depth, which also counts the redrives.
func (c *Consumer) dlqDepthLoop(poll context.Context) {
	tick := time.NewTicker(dlqDepthEvery)
	defer tick.Stop()
	for {
		out, err := c.api.GetQueueAttributes(poll, &sqs.GetQueueAttributesInput{
			QueueUrl:       aws.String(c.queues.DLQURL),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
		})
		if err == nil {
			if n, err := strconv.Atoi(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]); err == nil {
				c.metrics.DLQDepth(c.opts.DLQName, n)
			}
		}
		select {
		case <-poll.Done():
			return
		case <-tick.C:
		}
	}
}

// receiveCount is the ApproximateReceiveCount of msg, 1 when unknown.
func receiveCount(msg types.Message) int {
	n, err := strconv.Atoi(msg.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil {
		return 1
	}
	return n
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

- [ ] **Passo 6: ver passar.**

Rodar: os dois comandos do passo 4.
Esperado: `ok` nos dois (I04a, b, c, d, I03b-SQS e `TestDLQSendFailure`).

- [ ] **Passo 7: sensibilidade** (registrada nos comentários):
  1. remover o `c.delete(ctx, msg)` do `actDelete` → I04a: `DLQ depth = 2` (sem a checagem da DLQ, a redrive drenava a fila e o teste passava);
  2. `c.delete(ctx, msg)` no lugar do `changeVisibility` do `actRetry` → I04d: `1 messages in the DLQ: not reached within 20s`;
  3. `c.delete(ctx, msg)` depois do envio à DLQ que falhou → `TestDLQSendFailure`: `… not reached within 20s`;
  4. sem a pausa do short polling → `280147 empty receives in 350 ms`; sem `DeleteFailed()` → `delete failures 0`; `time.After(0)` no backoff do receive → milhares de receives.

- [ ] **Checkpoint:** `go test -race ./internal/adapters/sqsconsumer/` e `go test -tags=integration -race -count=1 -run 'TestInbox|TestInvalid|TestTransient|TestPermanent|TestDLQSend' ./internal/adapters/sqsconsumer/` verdes.

---

## Tarefa 9: Ordem do grupo, liberação por prazo e pausa

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/order_integration_test.go`
- Modificar: `internal/adapters/sqsconsumer/consumer.go`

- [ ] **Passo 1: escrever os testes.** Criar `order_integration_test.go`:

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: SQS-07 (messaging.md §4.2)
// Sensitivity: the group went on after a retry → "order = [bet-2 bet-3 bet-1]".
//
// A transient failure of the first message of a group releases the rest of
// the group: nothing of the group runs before the first one concludes.
func TestGroupOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	real := newConsumeWager(newUoW())
	var mu sync.Mutex
	var failed bool
	var order []string
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		mu.Lock()
		if *m.Input.ExternalTransactionID == "bet-1" && !failed {
			failed = true
			mu.Unlock()
			return app.ConsumeResult{}, errors.New("forced transient failure")
		}
		order = append(order, *m.Input.ExternalTransactionID)
		mu.Unlock()
		return real.Execute(ctx, m)
	})
	for i, amount := range []string{"10.00", "20.00", "30.00"} {
		ext := "bet-" + string(rune('1'+i))
		f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", amount, ext, ""), w.id)
	}
	f.start(t, consumerOpts{proc: proc})

	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, ",") != "bet-1,bet-2,bet-3" {
		t.Fatalf("order = %v, want bet-1,bet-2,bet-3", order)
	}
	if b := balance(t, w.id); b != "40.00" {
		t.Fatalf("balance = %s, want 40.00", b)
	}
}

// Covers: SQS-07 (spec M5, decision 8)
// Sensitivity: without the deadline check → "sqs_retries_total{deadline_release} = 0".
//
// A slow first message leaves the second one of its group with less
// visibility than its processing deadline: it is released unprocessed and
// concluded on a later receive.
func TestDeadlineRelease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	real := newConsumeWager(newUoW())
	var slowOnce sync.Once
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		if *m.Input.ExternalTransactionID == "bet-1" {
			slowOnce.Do(func() { time.Sleep(2500 * time.Millisecond) }) // visibility 5 s − 2.5 s < processing 3 s
		}
		return real.Execute(ctx, m)
	})
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "10.00", "bet-1", ""), w.id)
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "20.00", "bet-2", ""), w.id)
	f.start(t, consumerOpts{proc: proc})

	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if n := f.metric(t, "sqs_retries_total", "reason=deadline_release"); n < 1 {
		t.Fatalf("sqs_retries_total{deadline_release} = %v, want the second message released", n)
	}
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}

// Covers: SQS-07 (messaging.md §4.3; spec M5, decision 6)
// Sensitivity: a gate that never pauses → "while paused: 1 processings and 2
// new receives".
func TestHealthGatePauses(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
	pinger := &switchPinger{}
	pinger.down.Store(true)
	real := newConsumeWager(newUoW())
	var calls atomic.Int32
	proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
		if calls.Add(1) == 1 {
			return app.ConsumeResult{}, errors.New("connection refused")
		}
		return real.Execute(ctx, m)
	})
	spy := &queueSpy{QueueAPI: f.sqs}
	opts := options()
	opts.RetryMaxDelay = 3 * time.Second // the failed message is visible again 2 s later
	f.start(t, consumerOpts{proc: proc, api: spy, pinger: pinger, opts: &opts})
	f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "30.00", "bet-1", ""), w.id)

	testkit.Eventually(t, 10*time.Second, "the first transient failure", func(context.Context) (bool, error) {
		return calls.Load() == 1, nil
	})
	// The gate holds new receives; a long poll already in flight (wait 1 s)
	// still ends normally, before the message is visible again.
	time.Sleep(1200 * time.Millisecond)
	receives := spy.receives.Load()
	time.Sleep(1500 * time.Millisecond) // past the 2 s retry delay: the message is visible again
	if calls.Load() != 1 || spy.receives.Load() != receives {
		t.Fatalf("while paused: %d processings and %d new receives, want none", calls.Load()-1, spy.receives.Load()-receives)
	}

	pinger.down.Store(false)
	testkit.AssertQueueDrained(t, f.sqs, f.queues.WagerURL)
	if b := balance(t, w.id); b != "70.00" {
		t.Fatalf("balance = %s, want 70.00", b)
	}
}
```

- [ ] **Passo 2: ver falhar.**

Rodar: `go test -tags=integration -race -count=1 -run 'TestGroupOrder|TestDeadlineRelease|TestHealthGatePauses' ./internal/adapters/sqsconsumer/`
Esperado: FAIL — `order = [bet-2 bet-3 bet-1], want bet-1,bet-2,bet-3`, `sqs_retries_total{deadline_release} = 0` e `while paused: 1 processings and 3 new receives, want none`.

- [ ] **Passo 3: implementar.** Em `handleGroup`, trocar:

```go
		c.handle(work, msg, receivedAt)
		<-c.sem
```

por:

```go
		if c.opts.Visibility-time.Since(receivedAt) < c.opts.ProcessingTimeout {
			<-c.sem
			c.releaseAll(work, group[i:], "deadline_release")
			return
		}
		a := c.handle(work, msg, receivedAt)
		<-c.sem
		if a.kind == actRetry || a.kind == actRelease {
			c.releaseAll(work, group[i+1:], "")
			return
		}
```

E no `actRetry` do `apply`, trocar:

```go
		c.metrics.Retried("transient")
```

por:

```go
		c.metrics.Retried("transient")
		if a.transient {
			c.gate.Report(ctx)
		}
```

- [ ] **Passo 4: ver passar.**

Rodar: `go test -tags=integration -race -count=1 ./internal/adapters/sqsconsumer/` (exceto o `TestConsumerShutdown`, que ainda não existe)
Esperado: `ok`.

- [ ] **Passo 5: sensibilidade:** `if false && (a.kind == actRetry || …)` → `order = [bet-2 bet-3 bet-1]`; `if false {` na checagem do prazo → `deadline_release = 0`; `if true { return }` no início do `Report` → `1 processings and 2 new receives`.

- [ ] **Checkpoint:** o comando do passo 4 verde.

---

## Tarefa 10: Shutdown em 5 passos, módulo Fx e grafo

**Arquivos:**
- Criar: `internal/adapters/sqsconsumer/stop_integration_test.go`, `internal/adapters/sqsconsumer/module.go`
- Modificar: `internal/adapters/sqsconsumer/consumer.go`, `internal/bootstrap/app_module.go`, `internal/bootstrap/bootstrap.go`, `internal/bootstrap/bootstrap_test.go`

**Interfaces:**
- Produz: `sqsconsumer.Module` (registrado entre `outbox.Module` e `httpapi.Module`) e `app.NewConsumeWager` no `appModule`.

- [ ] **Passo 1: escrever o teste de shutdown.** Criar `stop_integration_test.go` (short polling: spec §2.2, decisão 17):

```go
//go:build integration

package sqsconsumer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/goleak"

	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/test/testkit"
)

// receiveNow receives one message of the queue with another consumer's
// credentials, as another instance would, within wait.
func (f *fixture) receiveNow(t *testing.T, wait time.Duration) (string, bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		out, err := f.sqs.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(f.queues.WagerURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 0, VisibilityTimeout: 30,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(out.Messages) == 1 {
			return aws.ToString(out.Messages[0].Body), true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", false
}

// shortPolling are the options of the shutdown tests. A long poll canceled
// by the client stays open in the broker until its wait ends and may take a
// message released meanwhile, hiding it for a visibility timeout (found in the
// plan validation, spec M5 §2.2): short polling leaves no such poll, so the
// tests see the release itself.
func shortPolling() *sqsconsumer.Options {
	opts := options()
	opts.WaitTime = 0
	return &opts
}

// Covers: SQS-09, FX-03 (messaging.md §4.5)
// Sensitivity: processing what was received and not started → "balance =
// 70.00, want the message in flight concluded (90.00)"; without the abort at
// the deadline → "Stop took 3s".
func TestConsumerShutdown(t *testing.T) {
	// Not parallel: goleak sees every goroutine of the process.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent(),
		// idle keep-alive connections of the test's SQS clients, not the consumer's
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"))

	t.Run("waits for the message in flight and releases the rest of its group", func(t *testing.T) {
		f := newFixture(t)
		w, p := openWallet(t, "100.00"), "provider-"+testkit.NewID()
		real := newConsumeWager(newUoW())
		started, release := make(chan struct{}), make(chan struct{})
		proc := processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
			if *m.Input.ExternalTransactionID == "bet-1" {
				close(started)
				<-release
			}
			return real.Execute(ctx, m)
		})
		second := wager(t, "msg-"+testkit.NewID(), w, p, "BET", "20.00", "bet-2", "")
		f.send(t, wager(t, "msg-"+testkit.NewID(), w, p, "BET", "10.00", "bet-1", ""), w.id)
		f.send(t, second, w.id)
		_, stop := f.start(t, consumerOpts{proc: proc, opts: shortPolling()})
		<-started

		stopped := make(chan error, 1)
		go func() { stopped <- stop() }()
		select {
		case err := <-stopped:
			t.Fatalf("Stop returned %v with a message in flight", err)
		case <-time.After(300 * time.Millisecond):
		}
		close(release)
		if err := <-stopped; err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if b := balance(t, w.id); b != "90.00" {
			t.Fatalf("balance = %s, want the message in flight concluded (90.00)", b)
		}
		if body, ok := f.receiveNow(t, time.Second); !ok || body != second {
			t.Fatalf("another consumer received %q, %v; want the second message at once", body, ok)
		}
	})

	t.Run("at the deadline the message in flight is canceled and released", func(t *testing.T) {
		f := newFixture(t)
		proc := processorFunc(func(ctx context.Context, _ app.WagerMessage) (app.ConsumeResult, error) {
			<-ctx.Done() // a transaction that only ends with its context
			return app.ConsumeResult{}, ctx.Err()
		})
		opts := shortPolling()
		opts.ShutdownTimeout = 300 * time.Millisecond
		var started atomic.Bool
		_, stop := f.start(t, consumerOpts{proc: processorFunc(func(ctx context.Context, m app.WagerMessage) (app.ConsumeResult, error) {
			started.Store(true)
			return proc(ctx, m)
		}), opts: opts})
		body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
		f.send(t, body, "group-1")
		testkit.Eventually(t, 10*time.Second, "the message in flight", func(ctx context.Context) (bool, error) {
			n, err := testkit.QueueDepth(ctx, f.sqs, f.queues.WagerURL)
			return n == 1 && started.Load(), err
		})

		begin := time.Now()
		if err := stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if took := time.Since(begin); took > 2*time.Second {
			t.Fatalf("Stop took %v, want the deadline (300 ms) plus the release", took)
		}
		if got, ok := f.receiveNow(t, time.Second); !ok || got != body {
			t.Fatalf("another consumer received %q, %v; want the canceled message at once", got, ok)
		}
	})
}
```

- [ ] **Passo 2: ver falhar.**

Rodar: `go test -tags=integration -race -count=2 -run TestConsumerShutdown ./internal/adapters/sqsconsumer/`
Esperado: FAIL — `Stop took 3s, want the deadline (300 ms) plus the release` sempre; `balance = 70.00, want the message in flight concluded (90.00)` na maioria das execuções (sem a checagem de `stopping`, o `select` do semáforo escolhe ao acaso entre seguir e liberar).

- [ ] **Passo 3: implementar os 5 passos.** Em `consumer.go`, trocar o `Stop` da Tarefa 8:

```go
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopping.Store(true)
	c.stopPolling()
	done := make(chan struct{})
	go func() {
		c.running.Wait()
		close(done)
	}()
	defer c.abortWork()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
```

por:

```go
func (c *Consumer) Stop(ctx context.Context) error {
	c.stopping.Store(true)
	c.stopPolling()
	ctx, cancel := context.WithTimeout(ctx, c.opts.ShutdownTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.running.Wait()
		close(done)
	}()
	select {
	case <-done:
		c.abortWork()
		return nil
	case <-ctx.Done():
	}
	c.log.Warn("sqs consumer: shutdown deadline reached, canceling the messages in flight")
	c.abortWork()
	select {
	case <-done:
		return nil
	case <-time.After(2 * queueTimeout):
		return ctx.Err()
	}
}
```

E em `handleGroup`, logo depois do `select` do semáforo, inserir:

```go
		if c.stopping.Load() {
			<-c.sem
			c.releaseAll(work, group[i:], "")
			return
		}
```

- [ ] **Passo 4: ver passar.**

Rodar: `go test -tags=integration -race -count=3 -run TestConsumerShutdown ./internal/adapters/sqsconsumer/`
Esperado: `ok`.

- [ ] **Passo 5: o grafo — teste primeiro.** Em `internal/bootstrap/bootstrap_test.go`:

```diff
@@ -9,13 +9,15 @@ import (
 
 	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
 	"github.com/KaioVinicios/pda/internal/adapters/outbox"
+	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
 	"github.com/KaioVinicios/pda/internal/app"
 	"github.com/KaioVinicios/pda/internal/auth"
 	"github.com/KaioVinicios/pda/internal/bootstrap"
 	"github.com/KaioVinicios/pda/internal/observability"
 )
 
-// Covers: TST-I07, FX-01 (I07a — M0 modules, M2 persistence, M3 auth, use cases and API, M4 outbox)
+// Covers: TST-I07, FX-01 (I07a — M0 modules, M2 persistence, M3 auth, use cases and API, M4 outbox, M5 consumer)
+// Sensitivity (M5): sqsconsumer.Module out of bootstrap.Options → "missing type: *sqsconsumer.Consumer".
 func TestFxGraph(t *testing.T) {
 	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/pda")
 	t.Setenv("OIDC_ISSUER", "http://localhost:8080/realms/pda")
@@ -38,9 +40,12 @@ func TestFxGraph(t *testing.T) {
 		store     app.OutboxStore
 		topic     *awsclient.Topic
 		publisher *outbox.Publisher
+		consume   *app.ConsumeWager
+		consumer  *sqsconsumer.Consumer
 	)
 	opts := append(bootstrap.Options(), fx.Populate(&health, &pool, &queues, &handler, &uow, &repos,
-		&verifier, &metrics, &open, &process, &queries, &reconcile, &store, &topic, &publisher))
+		&verifier, &metrics, &open, &process, &queries, &reconcile, &store, &topic, &publisher,
+		&consume, &consumer))
 	if err := fx.ValidateApp(opts...); err != nil {
 		t.Fatalf("fx.ValidateApp() = %v", err)
 	}
```

Rodar: `go test -race -run TestFxGraph ./internal/bootstrap/`
Esperado: FAIL com `missing type: *app.ConsumeWager` (ou `*sqsconsumer.Consumer`).

- [ ] **Passo 6: módulo e registro.** Criar `internal/adapters/sqsconsumer/module.go`:

```go
package sqsconsumer

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/config"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Module runs the SQS consumer of this instance (D-12, D-15): it starts with
// the application and, on stop, finishes or releases the messages in flight
// before the pool and the AWS clients close (messaging.md §4.5).
var Module = fx.Module("sqsconsumer",
	fx.Provide(newModuleConsumer),
	fx.Invoke(func(*Consumer) {}),
)

func newModuleConsumer(lc fx.Lifecycle, cfg config.Config, api *sqs.Client, queues *awsclient.Queues,
	proc *app.ConsumeWager, pool *pgxpool.Pool, m *observability.Metrics, log *slog.Logger,
) *Consumer {
	c := NewConsumer(api, queues, proc, pool, m, log, Options{
		Pollers: cfg.SQSConsumerPollers, ReceiveBatch: cfg.SQSReceiveBatch, WaitTime: cfg.SQSWaitTime,
		Visibility: cfg.SQSVisibilityTimeout, ProcessingTimeout: cfg.SQSProcessingTimeout,
		MaxInFlight: cfg.SQSMaxInFlight, RetryMaxDelay: cfg.SQSRetryMaxDelay,
		ShutdownTimeout: cfg.ShutdownTimeout, DLQName: cfg.WagerDLQName,
	})
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			c.Start(ctx)
			log.Info("sqs consumer started", "pollers", cfg.SQSConsumerPollers)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			log.Info("sqs consumer stopping")
			err := c.Stop(ctx)
			log.Info("sqs consumer stopped")
			return err
		},
	})
	return c
}
```

Em `internal/bootstrap/app_module.go`:

```diff
@@ -19,6 +19,7 @@ var appModule = fx.Module("app",
 		newReferencePolicy,
 		app.NewOpenWallet,
 		app.NewProcessWager,
+		app.NewConsumeWager,
 		app.NewQueries,
 		app.NewReconcile,
 	),
```

Em `internal/bootstrap/bootstrap.go`:

```diff
@@ -11,6 +11,7 @@ import (
 	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
 	"github.com/KaioVinicios/pda/internal/adapters/outbox"
 	"github.com/KaioVinicios/pda/internal/adapters/postgres"
+	"github.com/KaioVinicios/pda/internal/adapters/sqsconsumer"
 	"github.com/KaioVinicios/pda/internal/auth"
 	"github.com/KaioVinicios/pda/internal/config"
 	"github.com/KaioVinicios/pda/internal/observability"
@@ -30,6 +31,7 @@ func Options() []fx.Option {
 		auth.Module,
 		appModule,
 		outbox.Module,
+		sqsconsumer.Module,
 		httpapi.Module,
 	}
 }
```

- [ ] **Passo 7: ver passar.**

Rodar: `go test -race ./internal/bootstrap/` e `go test -tags=integration -race -count=1 ./internal/bootstrap/`
Esperado: `ok` nos dois (o `TestFxLifecycle` sobe e para o consumidor com o `goleak` passando).

- [ ] **Passo 8: sensibilidade:** `if false {` na checagem de `stopping` → `balance = 70.00`; o `Stop` da Tarefa 8 → `Stop took 3s`; `fx.Invoke(func() { _ = sqsconsumer.Module })` no lugar do módulo → `missing type: *sqsconsumer.Consumer`.

- [ ] **Checkpoint:** `go test -tags=integration -race -count=1 ./internal/adapters/sqsconsumer/ ./internal/bootstrap/` verde.

---

## Tarefa 11: Ponta a ponta pelo SQS e políticas do broker

**Arquivos:**
- Criar: `test/testkit/iam.go`, `test/integration/sqs_test.go`, `test/integration/policies_test.go`
- Modificar: `test/testkit/app.go`, `go.mod`, `go.sum`

**Interfaces:**
- Produz: `App.WagerQueueURL`, `App.DLQURL`, `App.SendWager(tb, body, SendOpts) string`, `App.AssertQueueDrained(tb)`, `App.DLQDepth(tb) int`; `testkit.PolicyARNs`, `RenderPolicy(tb, name, PolicyARNs) string`, `NewIAMUser(tb, *iam.Client, policy string) AWSKeys`.

- [ ] **Passo 1: dependência.** `go get github.com/aws/aws-sdk-go-v2/service/iam@v1.64.1` (fica `// indirect` até o passo 3; o `go mod tidy` a torna direta).

- [ ] **Passo 2: `StartApp` com as filas.** Em `test/testkit/app.go`:

```diff
@@ -16,6 +16,7 @@ import (
 	"github.com/aws/aws-sdk-go-v2/service/sqs"
 	"go.uber.org/fx"
 
+	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
 	"github.com/KaioVinicios/pda/internal/bootstrap"
 	"github.com/KaioVinicios/pda/internal/observability"
 )
@@ -28,8 +29,11 @@ type App struct {
 	MetricsURL string
 	// Audit reads the audit queue of the app's isolated events topic.
 	Audit *Audit
+	// WagerQueueURL and DLQURL are the app's isolated queues.
+	WagerQueueURL, DLQURL string
 
 	env      *Env
+	sqs      *sqs.Client
 	http     *http.Client
 	contract *Contract
 	logs     *syncBuffer
@@ -67,6 +71,11 @@ func (e *Env) StartApp(ctx context.Context) (*App, func(), error) {
 	if err != nil {
 		return nil, nil, err
 	}
+	queues, err := awsclient.ResolveQueues(ctx, sqsClient, wager, dlq)
+	if err != nil {
+		removeQueues()
+		return nil, nil, err
+	}
 	topic, removeTopic, err := CreateEventsTopic(ctx, sqsClient, snsClient)
 	if err != nil {
 		removeQueues()
@@ -120,6 +129,7 @@ func (e *Env) StartApp(ctx context.Context) (*App, func(), error) {
 	}
 	a := &App{
 		BaseURL: "http://" + httpAddr, MetricsURL: "http://" + metricsAddr, Audit: audit,
+		WagerQueueURL: queues.WagerURL, DLQURL: queues.DLQURL, sqs: sqsClient,
 		env: e, http: &http.Client{Timeout: requestTimeout}, contract: contract, logs: logs,
 	}
 	stop := func() {
@@ -189,3 +199,25 @@ func (a *App) OpenWallet(tb testing.TB, initial Money) Wallet {
 	tb.Cleanup(func() { a.AssertWalletConsistent(tb, w.ID) })
 	return w
 }
+
+// SendWager sends body to the app's wager queue and returns the SQS message id.
+func (a *App) SendWager(tb testing.TB, body string, o SendOpts) string {
+	tb.Helper()
+	return SendMessage(tb, a.sqs, a.WagerQueueURL, body, o)
+}
+
+// AssertQueueDrained waits until the app's wager queue is empty.
+func (a *App) AssertQueueDrained(tb testing.TB) {
+	tb.Helper()
+	AssertQueueDrained(tb, a.sqs, a.WagerQueueURL)
+}
+
+// DLQDepth is the visible plus in-flight messages of the app's DLQ.
+func (a *App) DLQDepth(tb testing.TB) int {
+	tb.Helper()
+	n, err := QueueDepth(tb.Context(), a.sqs, a.DLQURL)
+	if err != nil {
+		tb.Fatal(err)
+	}
+	return n
+}
```

- [ ] **Passo 3: helpers de IAM.** Criar `test/testkit/iam.go`:

```go
package testkit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// PolicyARNs are the resources the policy templates of deploy/aws/policies
// name, as aws-init renders them.
type PolicyARNs struct {
	WagerQueue, DLQ, Topic, AuditQueue string
}

// RenderPolicy reads deploy/aws/policies/<name>.json with the placeholders
// replaced by arns, the same substitution as deploy/aws/init.sh.
func RenderPolicy(tb testing.TB, name string, arns PolicyARNs) string {
	tb.Helper()
	raw, err := os.ReadFile(filepath.Join(RepoRoot(tb), "deploy", "aws", "policies", name+".json"))
	if err != nil {
		tb.Fatalf("policy %s: %v", name, err)
	}
	return strings.NewReplacer("${WAGER_QUEUE_ARN}", arns.WagerQueue, "${DLQ_ARN}", arns.DLQ,
		"${TOPIC_ARN}", arns.Topic, "${AUDIT_QUEUE_ARN}", arns.AuditQueue).Replace(string(raw))
}

// NewIAMUser creates an IAM user of the test with the identity policy
// (none when policy is "") and an access key; everything is deleted at
// cleanup unless PDA_TEST_KEEP=1.
func NewIAMUser(tb testing.TB, client *iam.Client, policy string) AWSKeys {
	tb.Helper()
	ctx := tb.Context()
	name := "test-" + NewID()[:23]
	if _, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(name)}); err != nil {
		tb.Fatalf("create user: %v", err)
	}
	if policy != "" {
		if _, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
			UserName: aws.String(name), PolicyName: aws.String(name + "-policy"), PolicyDocument: aws.String(policy),
		}); err != nil {
			tb.Fatalf("put user policy: %v", err)
		}
	}
	key, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(name)})
	if err != nil {
		tb.Fatalf("create access key: %v", err)
	}
	tb.Cleanup(func() {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return
		}
		ctx := context.WithoutCancel(ctx)
		_, _ = client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(name), AccessKeyId: key.AccessKey.AccessKeyId})
		if policy != "" {
			_, _ = client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(name), PolicyName: aws.String(name + "-policy")})
		}
		_, _ = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(name)})
	})
	return AWSKeys{AccessKeyID: aws.ToString(key.AccessKey.AccessKeyId), SecretAccessKey: aws.ToString(key.AccessKey.SecretAccessKey)}
}
```

- [ ] **Passo 4: os testes.** Criar `test/integration/sqs_test.go` (I04e e ponta a ponta):

```go
//go:build integration

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// sqsWager is the WagerTransactionRequested of the operation, with the key
// {providerId}:{externalTransactionId}, as the HTTP helpers use.
func sqsWager(t *testing.T, messageID string, body testkit.Wager) string {
	t.Helper()
	money := body.Money
	return testkit.WagerMessage(t, messageID, testkit.WagerData{
		ProviderID: body.ProviderID, ExternalTransactionID: body.ExternalTransactionID,
		IdempotencyKey: body.ProviderID + ":" + body.ExternalTransactionID, PlayerID: body.PlayerID,
		WalletID: body.WalletID, RoundID: body.RoundID, GameID: body.GameID, Kind: body.Kind, Money: &money,
		ReferenceExternalTransactionID: body.ReferenceExternalTransactionID,
	})
}

// transactionOf waits until the operation exists and reads it as its provider.
func transactionOf(t *testing.T, provider, ext string) testkit.Transaction {
	t.Helper()
	c := server.Client(t, provider)
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		resp := c.Do(t, testkit.Request{Method: http.MethodGet, Path: "/providers/" + provider + "/wagering/transactions/" + ext})
		if resp.Status == http.StatusOK {
			var tx testkit.Transaction
			resp.JSON(t, &tx)
			return tx
		}
	}
	t.Fatalf("operation %s not recorded within 20s", ext)
	return testkit.Transaction{}
}

// eventsOf waits for every outbox event of the wallet in the audit queue and
// returns their envelopes.
func eventsOf(t *testing.T, walletID string) []map[string]any {
	t.Helper()
	stored, err := testkit.OutboxPayloads(t.Context(), server.Owner(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(stored))
	for id := range stored {
		ids = append(ids, id)
	}
	var out []map[string]any
	for _, deliveries := range server.Audit.WaitFor(t, ids...) {
		var env map[string]any
		if err := json.Unmarshal(deliveries[0].Body, &env); err != nil {
			t.Fatal(err)
		}
		out = append(out, env)
	}
	return out
}

// Covers: SQS-06 (I04e)
func TestBusinessRejectionDeletesMessage(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("10.00"))
	bet := wager(w, "provider-a", "BET", "25.00", unique("bet"), "")
	server.SendWager(t, sqsWager(t, unique("msg"), bet), testkit.SendOpts{GroupID: w.ID})

	tx := transactionOf(t, "provider-a", bet.ExternalTransactionID)
	if tx.Status != "REJECTED" || tx.FailureCode != "INSUFFICIENT_FUNDS" || tx.ReceivedVia != "SQS" {
		t.Fatalf("operation = %+v, want REJECTED INSUFFICIENT_FUNDS over SQS", tx)
	}
	server.AssertQueueDrained(t)
	if n := server.DLQDepth(t); n != 0 {
		t.Fatalf("DLQ has %d messages, want none", n)
	}
	rejected := false
	for _, env := range eventsOf(t, w.ID) {
		rejected = rejected || env["eventType"] == "WagerTransactionRejected"
	}
	if !rejected {
		t.Fatal("no WagerTransactionRejected in the audit queue")
	}
}

// Covers: SQS-02, SQS-04, IDEM-04
//
// A BET over SQS is processed with the message as the cause of its events and
// the correlation id of the attribute; the same operation over HTTP afterwards
// is the replay of the SQS one.
func TestSQSEndToEnd(t *testing.T) {
	t.Parallel()
	w := server.OpenWallet(t, testkit.BRL("100.00"))
	bet := wager(w, "provider-a", "BET", "30.00", unique("bet"), "")
	msgID, corr := unique("msg"), unique("corr")
	server.SendWager(t, sqsWager(t, msgID, bet), testkit.SendOpts{GroupID: w.ID, CorrelationID: corr})

	tx := transactionOf(t, "provider-a", bet.ExternalTransactionID)
	if tx.Status != "PROCESSED" || tx.ReceivedVia != "SQS" || tx.Balance == nil || tx.Balance.Amount != "70.00" {
		t.Fatalf("operation = %+v, want PROCESSED over SQS with balance 70.00", tx)
	}
	server.AssertQueueDrained(t)
	caused := 0
	for _, env := range eventsOf(t, w.ID) {
		if data, _ := env["data"].(map[string]any); data["transactionId"] != tx.TransactionID {
			continue // the events of the opening
		}
		caused++
		if env["causationId"] != msgID || env["correlationId"] != corr {
			t.Errorf("%s: causation %v correlation %v, want %s %s", env["eventType"], env["causationId"], env["correlationId"], msgID, corr)
		}
	}
	if caused != 2 {
		t.Fatalf("%d events of the BET, want WagerTransactionProcessed and WalletBalanceChanged", caused)
	}

	replay := result(t, server.Client(t, "provider-a"), bet, http.StatusOK)
	wantResult(t, replay, "PROCESSED", "", "70.00", true)
	if replay.TransactionID != tx.TransactionID {
		t.Fatalf("HTTP replay of %s, want %s", replay.TransactionID, tx.TransactionID)
	}
}
```

E `test/integration/policies_test.go` (I04f):

```go
//go:build integration

package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"

	"github.com/KaioVinicios/pda/test/testkit"
)

// Covers: AUTH-09 (I04f; messaging.md §2.1)
// Sensitivity: sqs:ChangeMessageVisibility removed from
// policies/pda-wallet-service.json → "service ChangeMessageVisibility on the
// wager queue: … AccessDeniedException".
//
// The policy documents of deploy/aws/policies, applied to IAM users of the
// test over isolated resources, allow exactly the matrix of messaging §2.1:
// MiniStack evaluates them with AUTH=true (D-02).
func TestBrokerPoliciesEnforced(t *testing.T) {
	t.Parallel()
	root := testkit.RootAWSConfig(t)
	rootSQS, rootSNS := sqs.NewFromConfig(root), sns.NewFromConfig(root)
	wager, dlq := testkit.CreateQueues(t, rootSQS)
	wagerURL, wagerARN := queueURLAndARN(t, rootSQS, wager)
	dlqURL, dlqARN := queueURLAndARN(t, rootSQS, dlq)
	topic := testkit.NewEventsTopic(t, rootSQS, rootSNS)
	_, auditARN := queueURLAndARN(t, rootSQS, topic.AuditQueueURL[strings.LastIndex(topic.AuditQueueURL, "/")+1:])
	arns := testkit.PolicyARNs{WagerQueue: wagerARN, DLQ: dlqARN, Topic: topic.ARN, AuditQueue: auditARN}

	users := iam.NewFromConfig(root)
	as := func(keys testkit.AWSKeys) (*sqs.Client, *sns.Client) {
		cfg := testkit.AWSConfigWithKeys(t, keys)
		return sqs.NewFromConfig(cfg), sns.NewFromConfig(cfg)
	}
	providerSQS, providerSNS := as(testkit.NewIAMUser(t, users, testkit.RenderPolicy(t, "provider", arns)))
	serviceSQS, serviceSNS := as(testkit.NewIAMUser(t, users, testkit.RenderPolicy(t, "pda-wallet-service", arns)))
	nobodySQS, _ := as(testkit.NewIAMUser(t, users, ""))

	body := testkit.WagerMessage(t, "msg-"+testkit.NewID(), testkit.WagerData{Kind: "BET"})
	send := func(c *sqs.Client, url string) error {
		_, err := c.SendMessage(t.Context(), &sqs.SendMessageInput{
			QueueUrl: aws.String(url), MessageBody: aws.String(body),
			MessageGroupId: aws.String("g"), MessageDeduplicationId: aws.String(testkit.NewID()),
		})
		return err
	}
	receive := func(c *sqs.Client) (*sqs.ReceiveMessageOutput, error) {
		return c.ReceiveMessage(t.Context(), &sqs.ReceiveMessageInput{QueueUrl: aws.String(wagerURL), WaitTimeSeconds: 1})
	}
	publish := func(c *sns.Client) error {
		_, err := c.Publish(t.Context(), &sns.PublishInput{
			TopicArn: aws.String(topic.ARN), Message: aws.String("{}"),
			MessageGroupId: aws.String("g"), MessageDeduplicationId: aws.String(testkit.NewID()),
		})
		return err
	}
	attrs := func(c *sqs.Client, url string) error {
		_, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(url), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		return err
	}

	// Allowed.
	allow(t, "provider SendMessage to the wager queue", send(providerSQS, wagerURL))
	got, err := receive(serviceSQS)
	allow(t, "service ReceiveMessage from the wager queue", err)
	if err == nil && len(got.Messages) != 1 {
		t.Fatalf("service received %d messages, want the provider's", len(got.Messages))
	}
	if err == nil {
		handle := got.Messages[0].ReceiptHandle
		_, err = serviceSQS.ChangeMessageVisibility(t.Context(), &sqs.ChangeMessageVisibilityInput{
			QueueUrl: aws.String(wagerURL), ReceiptHandle: handle, VisibilityTimeout: 0,
		})
		allow(t, "service ChangeMessageVisibility on the wager queue", err)
		got, err = receive(serviceSQS)
		allow(t, "service ReceiveMessage again", err)
		if err == nil && len(got.Messages) == 1 {
			_, err = serviceSQS.DeleteMessage(t.Context(), &sqs.DeleteMessageInput{QueueUrl: aws.String(wagerURL), ReceiptHandle: got.Messages[0].ReceiptHandle})
			allow(t, "service DeleteMessage on the wager queue", err)
		}
	}
	allow(t, "service GetQueueAttributes of the wager queue", attrs(serviceSQS, wagerURL))
	allow(t, "service SendMessage to the DLQ", send(serviceSQS, dlqURL))
	allow(t, "service GetQueueAttributes of the DLQ", attrs(serviceSQS, dlqURL))
	allow(t, "service Publish to the topic", publish(serviceSNS))
	_, err = serviceSNS.GetTopicAttributes(t.Context(), &sns.GetTopicAttributesInput{TopicArn: aws.String(topic.ARN)})
	allow(t, "service GetTopicAttributes", err)

	// Denied.
	_, err = receive(providerSQS)
	deny(t, "provider ReceiveMessage from the wager queue", err)
	deny(t, "provider Publish to the topic", publish(providerSNS))
	deny(t, "provider SendMessage to the DLQ", send(providerSQS, dlqURL))
	deny(t, "service SendMessage to the wager queue", send(serviceSQS, wagerURL))
	deny(t, "user without policy SendMessage to the wager queue", send(nobodySQS, wagerURL))
	_, err = receive(nobodySQS)
	deny(t, "user without policy ReceiveMessage", err)
}

func queueURLAndARN(t *testing.T, c *sqs.Client, name string) (url, arn string) {
	t.Helper()
	out, err := c.GetQueueUrl(t.Context(), &sqs.GetQueueUrlInput{QueueName: aws.String(name)})
	if err != nil {
		t.Fatalf("queue %s: %v", name, err)
	}
	a, err := c.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: out.QueueUrl, AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("arn of %s: %v", name, err)
	}
	return aws.ToString(out.QueueUrl), a.Attributes["QueueArn"]
}

func allow(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v, want allowed", what, err)
	}
}

func deny(t *testing.T, what string, err error) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || !strings.Contains(apiErr.ErrorCode(), "AccessDenied") {
		t.Errorf("%s: %v, want AccessDenied", what, err)
	}
}
```

- [ ] **Passo 5: ver passar** (o comportamento já existe desde a Tarefa 10; estes testes cobrem mais de um marco e passam de primeira, então vale a sensibilidade, workflow §4.3).

Rodar: `go mod tidy && go test -tags=integration -race -count=1 ./test/integration/`
Esperado: `ok`.

- [ ] **Passo 6: sensibilidade:** retirar `"sqs:ChangeMessageVisibility",` de `deploy/aws/policies/pda-wallet-service.json` → I04f: `service ChangeMessageVisibility on the wager queue: … AccessDeniedException`; retirar o `sqsconsumer.Module` do `bootstrap.Options()` → `TestSQSEndToEnd`: `operation … not recorded within 20s`. Desfazer.

- [ ] **Checkpoint:** `go test -tags=integration -race -count=1 ./test/...` verde e `go mod tidy -diff` vazio.

---

## Tarefa 12: Verificação e encerramento do marco

- [ ] **Passo 1: qualidade.** `make fmt` (se o `gofumpt` reclamar) e `make check`. Esperado: `0 issues.` e todos os pacotes `ok`.

- [ ] **Passo 2: integração, três vezes.** `make test-integration` três vezes seguidas. Esperado: tudo `ok` nas três (≈ 25 s cada).

- [ ] **Passo 3: compose.** `docker compose up --build --wait`: as 3 réplicas saudáveis, cada uma com `sqs consumer started` no log. Depois:
  1. abrir uma carteira por `POST /wallets` (token de `wallet-service`);
  2. enviar um BET com as credenciais de `provider-a` (`.local/aws/credentials`, via `amazon/aws-cli` na rede `pda_default`, com o atributo `correlationId`);
  3. confirmar: o `GET /providers/provider-a/wagering/transactions/{ext}` mostra `PROCESSED` e `receivedVia = SQS`; a fila está vazia; a outbox da carteira tem os eventos publicados com `causationId = messageId`; uma mensagem com JSON quebrado chega à DLQ com `MALFORMED_MESSAGE`;
  4. `docker compose stop app-1`: no log, `http server stopped` → `sqs consumer stopped` → `outbox publisher stopped`.

- [ ] **Passo 4: `delivery-requirements.md`.** Marcar `[x]`, citando os testes: SQS-01 (`TestProvisioning`), SQS-02 (`TestSQSEndToEnd`, `TestConsumeWagerReplayAcrossChannels`), SQS-03 (I04a, I04b, `TestConsumeWagerInboxRace`), SQS-04 (`TestConsumeWagerAtomicInbox`), SQS-05 (I04a, `TestDLQSendFailure`), SQS-06 (I04e), SQS-07 (I04c, I04d, `TestPermanentFailureToDLQ`, `TestHealthGatePauses`), SQS-09 (`TestConsumerShutdown`), SQS-10 (messaging §4 + `TestParseEnvelope`), TST-I04, TST-I05, AUTH-09 (I04f), OUT-02. Parciais: SQS-08 (M6), SQS-11 (C10b no M8), TST-C11 (C01b/C10 no M8).

- [ ] **Passo 5: docs.**
  - `ARCHITECTURE.md`: §9.1 (consumidor: `ConsumeWager`, ações, DLQ, pausa por ping, liberação por prazo), §11 (módulo `sqsconsumer`), §12 (shutdown em 5 passos), §13.2 (métricas de SQS), §16 (limitações: long poll órfão no shutdown; as seguintes de um grupo chegam à DLQ junto com uma cabeça que falha sempre).
  - `implementation-plan.md`: M5 ✅ com "Entregue também" e o achado central; riscos atualizados (o flake do I05b resolvido).
  - `docs/dev/diary.md`: entrada do M5 e "Onde paramos".

- [ ] **Passo 6: revisão.** `superpowers:verification-before-completion` e, antes de propor os commits, `superpowers:requesting-code-review` sobre o diff do marco.

- [ ] **Passo 7: proposta de commits** (Conventional Commits, sem trailer de coautoria):
  1. `feat(config): add the sqs consumer settings`
  2. `feat(observability): add the sqs consumer metrics`
  3. `feat(app): consume wager messages with the inbox in the same unit of work`
  4. `feat(sqsconsumer): parse the envelope and decide the action per outcome`
  5. `feat(sqsconsumer): consume the wager queue with dlq, backoff, health pause and graceful stop`
  6. `feat(bootstrap): run the sqs consumer`
  7. `test(testkit): add sqs and iam helpers and stop canceling audit long polls`
  8. `test(integration): cover the sqs flow end to end and the broker policies`
  9. `docs: record m5 consumer decisions`
  10. `docs(dev): add m5 spec, plan and diary entry`
