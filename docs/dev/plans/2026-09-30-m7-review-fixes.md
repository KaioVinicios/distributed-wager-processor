# M7 — Correções da revisão: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` e `superpowers:test-driven-development` em cada tarefa ([`development-workflow.md`](../../development-workflow.md) §3; subagentes só com pedido explícito). **Sem passos de commit:** os commits são propostos no fim, com o autor autorizando. Commits deste repositório **não** levam trailer de coautoria de IA.

**Objetivo:** corrigir os achados da revisão do M7: erro de papel inválido visível ao operador (FX-02), IDs nos logs de falha do consumidor e do publisher (OBS-01), log e prova do fechamento dos clientes AWS (FX-05) e os ajustes de documentação.

**Arquitetura:** mudanças cirúrgicas: um ramo de `bootstrap.Options()`, loggers por mensagem e por evento (`log.With`) no consumidor e no publisher, um log no `OnStop` do cliente HTTP da AWS. Nenhuma porta, métrica ou contrato muda.

**Stack:** Go 1.27.1, Uber Fx, `log/slog`, AWS SDK v2; testes unitários e com a tag `integration`.

**Spec:** [`dev/specs/2026-09-30-m7-review-fixes-design.md`](../specs/2026-09-30-m7-review-fixes-design.md).

## Restrições globais

- `make check` verde ao fim de cada tarefa de código; `make test-integration` verde na Tarefa 5.
- `// Covers: …` acima de cada teste novo; `// Sensitivity: …` nos testes sobre comportamento existente.
- O red é uma **asserção** falhando, não um erro de compilação.
- Chaves de log em `camelCase` (`sloglint`: `no-mixed-args`, `static-msg`); nunca `fmt.Print*` (`forbidigo`).
- Nenhum log registra token, `Idempotency-Key`, `amount`, saldos nem corpos (OBS-02). O `walletId` e o `providerId` de uma mensagem ainda não validada entram cortados em 128 caracteres.

## Foco da revisão

1. **Mensagem ilegível** (`{`) → o log da DLQ tem só o `sqsMessageId`, sem `messageId` vazio (Tarefa 2).
2. **Campo gigante** no `walletId` de uma mensagem → o log traz no máximo 128 caracteres (Tarefa 2).
3. **Valor da variável de papel** → nunca aparece na saída do processo (Tarefa 1).
4. **Mensagens de um lote com IDs diferentes** → cada log usa os IDs da própria mensagem, nunca os de outra (o logger é criado por mensagem, dentro de `handle`; Tarefa 2).
5. **Stop com evento em voo** → os logs do publisher continuam com os IDs do evento (o logger é criado em `publish`; Tarefa 3).

---

## Mapa de arquivos

| Arquivo | Mudança | Tarefa |
| --- | --- | --- |
| `cmd/pda/main_test.go` (novo) | `TestMain` que executa `main()` sob `PDA_TEST_RUN_MAIN=1`; U24 | 1 |
| `internal/bootstrap/bootstrap.go` | ramo de erro sem `fx.NopLogger` | 1 |
| `internal/adapters/sqsconsumer/consumer.go` | logger por mensagem; `apply`, `delete`, `changeVisibility` recebem `*slog.Logger` | 2 |
| `internal/adapters/sqsconsumer/consumer_test.go` | fakes de `ChangeMessageVisibility` e `SendMessage`; `unitConsumerWith`; U25 | 2 |
| `internal/adapters/outbox/publisher.go` | logger por evento | 3 |
| `internal/adapters/outbox/publisher_log_test.go` (novo) | U26 | 3 |
| `internal/adapters/awsclient/config.go`, `module.go` | log `aws http client closed` no `OnStop`; o provider passa o logger | 4 |
| `internal/bootstrap/bootstrap_integration_test.go` | I07b afirma o fechamento da AWS | 4 |
| `docs/…`, `ARCHITECTURE.md` | spec §4 | 5 |

---

### Tarefa 1: Erro de papel inválido visível no processo real (U24)

**Arquivos:** criar `cmd/pda/main_test.go`; modificar `internal/bootstrap/bootstrap.go`.

- [ ] **Passo 1: escrever o teste** (`cmd/pda/main_test.go`)

```go
package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// runMainEnv makes the test binary run main() instead of the tests, so a test
// observes what the real process prints and how it exits.
const runMainEnv = "PDA_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// Covers: FX-02, D-15 (U24; review of M7, finding 1)
// Sensitivity: fx.NopLogger back in the error branch of bootstrap.Options → the output is empty.
func TestMainReportsInvalidRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0])
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "HTTP_ENABLED=talvez-42")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("exit = %v, want code 1 (output %q)", err, out)
	}
	if !strings.Contains(string(out), "HTTP_ENABLED") || strings.Contains(string(out), "talvez-42") {
		t.Fatalf("output = %q, want HTTP_ENABLED named and its value absent", out)
	}
}
```

- [ ] **Passo 2: ver falhar**

Run: `go test ./cmd/pda -run TestMainReportsInvalidRole -v`
Expected: FAIL — `output = "", want HTTP_ENABLED named and its value absent` (o código 1 já acontece; a mensagem não).

- [ ] **Passo 3: implementar** (`internal/bootstrap/bootstrap.go`, em `Options`)

```go
	roles, err := config.RolesFromEnv()
	if err != nil {
		// No fx.WithLogger yet: Fx's console logger reports the error on stderr.
		return []fx.Option{fx.Error(err)}
	}
```

- [ ] **Passo 4: ver passar**

Run: `go test ./cmd/pda ./internal/bootstrap -v -run 'TestMainReportsInvalidRole|TestOptionsFor'` e `make check`
Expected: PASS; `make check` verde. O `TestFxFailFast/invalid role variable` (integração) continua válido: ele acrescenta o seu próprio `fx.NopLogger`.

- [ ] **Passo 5: sensibilidade.** Recoloque `fx.NopLogger` no ramo → U24 falha com saída vazia. Desfaça.

---

### Tarefa 2: IDs da mensagem nos logs de falha do consumidor (U25)

**Arquivos:** modificar `internal/adapters/sqsconsumer/consumer.go` e `consumer_test.go`.

- [ ] **Passo 1: preparar os fakes e o helper** (`consumer_test.go`)

Acrescente ao `fakeQueue` (hoje ele embute a interface nula; um retry ou um envio à DLQ causaria `panic`):

```go
func (q *fakeQueue) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (q *fakeQueue) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return &sqs.SendMessageOutput{}, nil
}
```

Troque `unitConsumer` para delegar a um helper que aceita o processador e o logger:

```go
func unitConsumer(t *testing.T, q *fakeQueue, m *countingMetrics, waitTime time.Duration) *Consumer {
	t.Helper()
	return unitConsumerWith(t, q, m, waitTime, duplicateProcessor{}, slog.New(slog.DiscardHandler))
}

func unitConsumerWith(t *testing.T, q *fakeQueue, m *countingMetrics, waitTime time.Duration, proc Processor, log *slog.Logger) *Consumer {
	t.Helper()
	c := NewConsumer(q, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, proc, upPinger{}, m, log, Options{
		Pollers: 1, ReceiveBatch: 10, WaitTime: waitTime, Visibility: 5 * time.Second,
		ProcessingTimeout: 3 * time.Second, MaxInFlight: 4, RetryMaxDelay: time.Second,
		ShutdownTimeout: time.Second, DLQName: "dlq",
	})
	c.Start(t.Context())
	t.Cleanup(func() { _ = c.Stop(context.WithoutCancel(t.Context())) })
	return c
}
```

- [ ] **Passo 2: escrever o teste** (fim de `consumer_test.go`; imports novos: `"bytes"`, `"strings"`, `"github.com/KaioVinicios/pda/internal/apperrors"`)

```go
// lockedLog collects the consumer's log lines, written from its goroutines.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// line waits for the log line whose message is msg and returns it.
func (l *lockedLog) line(t *testing.T, msg string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		logs := l.buf.String()
		l.mu.Unlock()
		for line := range strings.Lines(logs) {
			if strings.Contains(line, `"msg":"`+msg+`"`) {
				return line
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no log line %q", msg)
	return ""
}

// failingProcessor answers every message with err.
type failingProcessor struct{ err error }

func (p failingProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	return app.ConsumeResult{}, p.err
}

// Covers: OBS-01 (U25; review of M7, finding 2)
// Sensitivity: apply logging with c.log instead of the message's logger → the lines lack messageId, walletId, providerId and correlationId.
func TestFailureLogsCarryTheMessageIDs(t *testing.T) {
	longWallet := strings.Repeat("w", 300)
	longBody := strings.Replace(validBody, "0192f291-27dd-7d3f-8071-5f8685deef37", longWallet, 1)
	cases := []struct {
		name, body, msg string
		proc            Processor
		want, absent    []string
	}{
		{
			"transient failure", validBody, "sqs message failed transiently",
			failingProcessor{errors.New("database unavailable")},
			[]string{`"sqsMessageId":"sqs-1"`, `"messageId":"msg-123"`, `"correlationId":"msg-123"`,
				`"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"`, `"providerId":"provider-a"`},
			nil,
		},
		{
			"sent to the dlq", validBody, "sqs message sent to the dlq",
			failingProcessor{apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", errors.New("no wallet"))},
			[]string{`"sqsMessageId":"sqs-1"`, `"messageId":"msg-123"`, `"correlationId":"msg-123"`,
				`"walletId":"0192f291-27dd-7d3f-8071-5f8685deef37"`, `"providerId":"provider-a"`},
			nil,
		},
		{
			"an unreadable message has only the broker id", "{", "sqs message sent to the dlq",
			duplicateProcessor{},
			[]string{`"sqsMessageId":"sqs-1"`},
			[]string{`"messageId"`, `"walletId"`, `"correlationId"`},
		},
		{
			"a huge wallet id is cut", longBody, "sqs message failed transiently",
			failingProcessor{errors.New("database unavailable")},
			[]string{`"walletId":"` + strings.Repeat("w", 128) + `"`},
			[]string{strings.Repeat("w", 129)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(tc.body),
				Attributes: map[string]string{"MessageGroupId": "g"},
			}
			logs := &lockedLog{}
			unitConsumerWith(t, &fakeQueue{batches: [][]types.Message{{msg}}}, &countingMetrics{}, 0, tc.proc,
				slog.New(slog.NewJSONHandler(logs, nil)))
			line := logs.line(t, tc.msg)
			for _, w := range tc.want {
				if !strings.Contains(line, w) {
					t.Errorf("log %s lacks %s", line, w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(line, a) {
					t.Errorf("log %s has %s", line, a)
				}
			}
		})
	}
}
```

- [ ] **Passo 3: ver falhar**

Run: `go test ./internal/adapters/sqsconsumer -run TestFailureLogsCarryTheMessageIDs -v`
Expected: FAIL por asserção nos três primeiros e no último caso: `log … lacks "messageId":"msg-123"` (e `walletId`, `providerId`, `correlationId`). O caso "unreadable" já passa (comportamento atual correto, é a guarda do achado 1 do foco). Se algum caso der `panic` de interface nula, o passo 1 não foi aplicado.

- [ ] **Passo 4: implementar** (`consumer.go`)

Em `handle`, crie o logger da mensagem e passe-o para `apply`:

```go
func (c *Consumer) handle(work context.Context, msg types.Message, receivedAt time.Time) action {
	start := time.Now()
	log := c.log.With("sqsMessageId", aws.ToString(msg.MessageId))
	m, err := parseEnvelope(aws.ToString(msg.Body), aws.ToString(msg.MessageAttributes[correlationAttribute].StringValue), receivedAt)
	var res app.ConsumeResult
	if err == nil {
		log = log.With(messageIDs(m)...)
		ctx, cancel := context.WithTimeout(work, c.opts.ProcessingTimeout)
		res, err = c.proc.Execute(ctx, m)
		cancel()
	}
	a := decide(conclude(res, err), c.stopping.Load(), receiveCount(msg), c.opts.RetryMaxDelay)
	c.apply(work, msg, a, err, log)
	// … o resto como está (métricas)
```

Acrescente, perto de `handle`:

```go
// messageIDs are the identifiers a parsed message gives to the logs of its
// handling (OBS-01): the envelope's messageId and correlationId, and the
// wallet and provider it names. Those two are still unvalidated input, so
// they are cut to maxMessageIDLen characters; slog's JSON handler escapes them.
func messageIDs(m app.WagerMessage) []any {
	ids := []any{"messageId", m.MessageID, "correlationId", m.CorrelationID}
	if v := m.Input.WalletID; v != nil {
		ids = append(ids, "walletId", cutRunes(*v, maxMessageIDLen))
	}
	if v := m.Input.ProviderID; v != nil {
		ids = append(ids, "providerId", cutRunes(*v, maxMessageIDLen))
	}
	return ids
}

// cutRunes keeps at most n characters of s.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
```

(importe `"unicode/utf8"`; confirme que `maxMessageIDLen` é 128 em `envelope.go`.)

Troque as assinaturas e os logs de `apply`, `delete` e `changeVisibility` para usar o logger recebido, sem repetir o `sqsMessageId` (já está no `With`):

```go
func (c *Consumer) apply(work context.Context, msg types.Message, a action, cause error, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(work), queueTimeout)
	defer cancel()
	switch a.kind {
	case actDelete:
		// Fault point consumer.after_commit_before_delete (M8).
		c.delete(ctx, msg, log)
	case actDLQ:
		if _, err := c.api.SendMessage(ctx, dlqInput(c.queues.DLQURL, msg, a, time.Now())); err != nil {
			delay := retryDelay(receiveCount(msg), c.opts.RetryMaxDelay)
			log.Warn("sqs dlq send failed", "errorCode", a.code, "error", err.Error())
			c.changeVisibility(ctx, msg, delay, log)
			c.metrics.Retried("transient")
			return
		}
		c.metrics.SentToDLQ(a.code)
		log.Warn("sqs message sent to the dlq", "errorCode", a.code, "errorCategory", a.category)
		c.delete(ctx, msg, log)
	case actRetry:
		log.Warn("sqs message failed transiently", "retryIn", a.delay.String(), "error", errorText(cause))
		c.changeVisibility(ctx, msg, a.delay, log)
		c.metrics.Retried("transient")
		if a.transient {
			c.gate.Report(ctx)
		}
	case actRelease:
		c.changeVisibility(ctx, msg, 0, log)
	}
}
```

`delete(ctx, msg, log)` e `changeVisibility(ctx, msg, d, log)` passam a logar com `log.Warn("sqs delete failed", "error", …)` e `log.Warn("sqs visibility change failed", "error", …)`. Em `releaseAll`, chame `c.changeVisibility(ctx, msg, 0, c.log.With("sqsMessageId", aws.ToString(msg.MessageId)))` (mensagem não lida). Confira com `grep -n "c.delete(\|c.changeVisibility(\|c.apply(" internal/adapters/sqsconsumer/*.go` que nenhuma outra chamada ficou com a assinatura antiga.

- [ ] **Passo 5: ver passar**

Run: `go test -race ./internal/adapters/sqsconsumer -v` e `make check`
Expected: PASS em todo o pacote (unitários); `make check` verde.

- [ ] **Passo 6: sensibilidade.** Em `apply`, troque `log.Warn("sqs message failed transiently", …)` por `c.log.Warn(…, "sqsMessageId", aws.ToString(msg.MessageId), …)` → o caso "transient failure" falha. Remova o `cutRunes` do `walletId` → o caso "huge wallet id" falha. Desfaça.

---

### Tarefa 3: IDs do evento nos logs do publisher (U26)

**Arquivos:** criar `internal/adapters/outbox/publisher_log_test.go`; modificar `internal/adapters/outbox/publisher.go`.

- [ ] **Passo 1: escrever o teste** (`publisher_log_test.go`, pacote interno `outbox`)

```go
package outbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
)

// logStore fails the confirmation and accepts the failure record.
type logStore struct{ app.OutboxStore }

func (logStore) MarkPublished(context.Context, string, string) (time.Time, bool, error) {
	return time.Time{}, false, errors.New("connection reset")
}

func (logStore) MarkFailed(context.Context, string, string, time.Duration, string) (bool, error) {
	return true, nil
}

type logSink struct{ err error }

func (s logSink) Publish(context.Context, app.PendingEvent) error { return s.err }

type nopPublisherMetrics struct{}

func (nopPublisherMetrics) Published(string, time.Duration) {}
func (nopPublisherMetrics) PublishFailed(string)            {}
func (nopPublisherMetrics) LeaseReclaimed()                 {}
func (nopPublisherMetrics) Backlog(int, time.Duration)      {}

// Covers: OBS-01 (U26; review of M7, finding 2)
// Sensitivity: fail logging with p.log and only the eventId → the line lacks walletId and correlationId.
func TestPublisherLogsCarryTheEventIDs(t *testing.T) {
	e := app.PendingEvent{
		EventID: "evt-1", MessageGroupID: "wallet-1", EventType: "WalletBalanceChanged",
		CorrelationID: "corr-1", OccurredAt: time.Now(),
	}
	cases := []struct {
		name, msg string
		sinkErr   error
	}{
		{"publication failed", "outbox publish failed", errors.New("throttled")},
		{"confirmation failed", "outbox confirmation failed", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			p := NewPublisher(logStore{}, logSink{err: tc.sinkErr}, nopPublisherMetrics{}, slog.New(slog.NewJSONHandler(&logs, nil)),
				Options{Owner: "o", BatchSize: 1, Lease: time.Second, PollInterval: time.Second, Concurrency: 1,
					RetryBaseDelay: time.Second, RetryMaxDelay: time.Minute})
			p.publish(t.Context(), e)
			var line string
			for l := range strings.Lines(logs.String()) {
				if strings.Contains(l, `"msg":"`+tc.msg+`"`) {
					line = l
				}
			}
			for _, want := range []string{`"eventId":"evt-1"`, `"walletId":"wallet-1"`, `"correlationId":"corr-1"`} {
				if !strings.Contains(line, want) {
					t.Errorf("log %q lacks %s", line, want)
				}
			}
		})
	}
}
```

- [ ] **Passo 2: ver falhar**

Run: `go test ./internal/adapters/outbox -run TestPublisherLogsCarryTheEventIDs -v`
Expected: FAIL por asserção: `lacks "walletId":"wallet-1"` e `lacks "correlationId":"corr-1"` nos dois casos (o `eventId` já existe).

- [ ] **Passo 3: implementar** (`publisher.go`)

Acrescente o helper e use-o em `publish`, `fail` e no log de lease reassumido de `publishBatch`:

```go
// eventLog is the logger of one event: its id, the wallet it belongs to
// (message_group_id, data-model §3.5) and its correlation (OBS-01).
func (p *Publisher) eventLog(e app.PendingEvent) *slog.Logger {
	return p.log.With("eventId", e.EventID, "walletId", e.MessageGroupID, "correlationId", e.CorrelationID)
}
```

- `publishBatch`: `p.eventLog(batch[i]).Info("outbox lease reclaimed")`.
- `publish`: `log := p.eventLog(e)` no início; `log.Warn("outbox confirmation failed", "error", err.Error())`; `log.Info("outbox event confirmed by another instance")`; passe `log` a `fail`.
- `fail(ctx, e, cause, log)`: `log.Warn("outbox publish failed", "eventType", e.EventType, "attempts", e.Attempts+1, "retryIn", retryIn.String(), "error", cause.Error())`, `log.Warn("outbox failure not recorded", "error", err.Error())`, `log.Info("outbox event reclaimed before its failure was recorded")`.

- [ ] **Passo 4: ver passar**

Run: `go test -race ./internal/adapters/outbox -v` e `make check`
Expected: PASS; `make check` verde.

- [ ] **Passo 5: sensibilidade.** Em `fail`, volte a usar `p.log.Warn("outbox publish failed", "eventId", e.EventID, …)` → o caso "publication failed" falha. Desfaça.

---

### Tarefa 4: Fechamento dos clientes AWS com log e prova (I07b)

**Arquivos:** modificar `internal/bootstrap/bootstrap_integration_test.go` e `internal/adapters/awsclient/config.go`.

- [ ] **Passo 1: estender o teste** (`TestFxLifecycle`, depois do laço da ordem)

```go
	// FX-05: the AWS clients close after every component that uses them.
	if aws := lineIndex(logs.String(), `"msg":"aws http client closed"`); aws <= order[3] {
		t.Fatalf("aws http client closed at %d, want after the reference worker stopped (%d)\n%s", aws, order[3], logs.String())
	}
```

Acrescente ao comentário de cobertura do teste: `// Sensitivity (review of M7): no log in the AWS OnStop → "aws http client closed at -1".`

- [ ] **Passo 2: ver falhar**

Run: `make infra-up && go test -tags=integration ./internal/bootstrap -run TestFxLifecycle -v`
Expected: FAIL — `aws http client closed at -1, want after the reference worker stopped`.

- [ ] **Passo 3: implementar** (`awsclient/config.go`)

`newHTTPClient` recebe o logger e registra o fechamento:

```go
func newHTTPClient(lc fx.Lifecycle, log *slog.Logger) *http.Client {
	tr := awshttp.NewBuildableClient().GetTransport()
	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		tr.CloseIdleConnections()
		log.Info("aws http client closed")
		return nil
	}})
	return &http.Client{Transport: tr}
}
```

Importe `"log/slog"`. O único chamador é o provider de `internal/adapters/awsclient/module.go:25`; ele passa a receber o logger do grafo:

```go
		func(lc fx.Lifecycle, log *slog.Logger) (aws.Config, error) { return NewAWSConfig(newHTTPClient(lc, log)) },
```

(acrescente `"log/slog"` aos imports de `module.go`, se ainda não estiver lá).

- [ ] **Passo 4: ver passar**

Run: `go test -tags=integration -race ./internal/bootstrap -v` e `make check`
Expected: PASS. Se o índice da AWS vier antes do worker, a premissa (os hooks seguem a ordem de construção, e o cliente AWS é construído pelo `Invoke` do `awsclient`, antes do worker) está errada: investigue com `superpowers:systematic-debugging` antes de mudar o teste.

---

### Tarefa 5: Verificação e documentos

- [ ] **Passo 1: prova de código**

Run: `make check` e `make test-integration`.
Expected: `0 issues.`, saída 0 e todos os pacotes `ok`. Se o `TestMigrationsUpDownUp` falhar de novo, registre a ocorrência no diário (o flake já está documentado) e rode de novo; não mude o teste.

- [ ] **Passo 2: prova do binário**

Run: `go build -o /tmp/pda-fix ./cmd/pda && HTTP_ENABLED=talvez /tmp/pda-fix; echo "exit=$?"`
Expected: `[Fx] ERROR … HTTP_ENABLED …` no stderr e `exit=1`.

- [ ] **Passo 3: documentos** (spec §4)

- `decisions.md` D-15: acrescente "O `pda healthcheck` consulta o `/health/ready` da API; um container com `HTTP_ENABLED=false` ficaria sempre *unhealthy* (limitação: nenhum marco separa papéis por container)." e "Uma variável de papel inválida aborta o start com a mensagem do Fx no stderr." D-18, no "Delta do M7": os logs de falha do consumidor carregam `sqsMessageId`, `messageId`, `correlationId`, `walletId` e `providerId` (os dois últimos cortados em 128 caracteres); os do publisher, `eventId`, `walletId` e `correlationId`.
- `ARCHITECTURE.md`: §13.1, uma linha para os logs de falha do SQS e da outbox, e "os erros HTTP registram o `correlationId`, que liga ao log de acesso (`route`, `providerId`)"; §13.2, depois da tabela: "Uma operação que passa por `PENDING_REFERENCE` aparece duas vezes em `wager_transactions_total`: com `pending_reference` no canal de entrada e com o desfecho no canal `worker`."; §15 item 12: a limitação do healthcheck.
- `test-plan.md`: linhas U24, U25, U26; I07b com "e o fechamento dos clientes AWS depois dos workers"; I25 começando por "Teste de ligação: …" e dizendo que a semântica exata está nos U20, U23 e nos testes do edge.
- `delivery-requirements.md`: FX-02 ganha *(M7, revisão, 30/09: `TestMainReportsInvalidRole` (U24): o processo real nomeia a variável inválida no stderr, sem o valor.)*; FX-05 cita também o `aws http client closed` depois dos workers; OBS-01 cita U25 e U26; OBS-02 troca "4 sabotagens detectadas" por "2 sabotagens detectadas (header `Authorization` no log de acesso, `amount` na linha de conclusão)".
- Spec do M7: acrescente o ajuste "(6) os papéis não entram na `Config`: `config.RolesFromEnv` os lê antes do Fx e `fx.Supply(roles)` os entrega ao grafo; o `logRoles` faz o log de início".
- `implementation-plan.md` (M7): um item "**Revisão pós-marco (Opus, 30/09):**" com os dois achados importantes corrigidos e a referência a esta spec e a este plano.
- `diary.md`: entrada curta "30/09/2026 (qua): revisão do M7" (achados, correções, testes) e "Onde paramos" inalterado quanto ao próximo passo (M8).

- [ ] **Passo 4: verificação final**

Run: `make check` depois dos documentos. Reporte as saídas dos passos 1, 2 e 4, proponha commits atômicos (Conventional Commits, sem trailer de coautoria) e espere a autorização do autor.

---

## Autoavaliação

- **Cobertura da spec:** decisão 1–2 → Tarefa 1; 3 → Tarefa 2; 4 → Tarefa 3; 5 → Tarefa 4; 6 e §4 → Tarefa 5. Achados 1–8 → Tarefas 1, 2/3, 5, 5, 5, 5, 4, 5.
- **Assinaturas:** `apply(work, msg, a, cause, log)`, `delete(ctx, msg, log)`, `changeVisibility(ctx, msg, d, log)`, `(*Publisher).eventLog(e)`, `fail(ctx, e, cause, log)`, `newHTTPClient(lc, log)`.
- **Risco:** a Tarefa 4 depende da ordem de construção dos hooks no Fx; o passo 4 diz o que fazer se a premissa falhar.
