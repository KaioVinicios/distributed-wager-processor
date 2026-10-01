# I05a intermitente no CI: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` ([`development-workflow.md`](../../development-workflow.md) §7). As tarefas de código usam `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Não há passos de commit:** os commits são propostos no fim, e o autor autoriza ([`development-workflow.md`](../../development-workflow.md) §6).

**Objetivo:** o `testkit.Audit` para de esconder por 30 s um lote que recebeu e não registrou ou não apagou, e passa a contar cada mensagem SQS uma vez só.

**Arquitetura:** só o `testkit` muda. O receive do `Audit` pede um visibility de 2 s (decisão 1), e o `record` descarta a reentrega da mesma mensagem pelo `MessageId` (decisão 2). Os testes injetam a falha no transporte HTTP do SDK (decisão 5). Nenhum código de produção muda.

**Stack:** Go 1.27.1, `aws-sdk-go-v2` (SQS pelo protocolo JSON: `X-Amz-Target: AmazonSQS.<Ação>`), MiniStack 1.5.18, testes com a tag `integration`.

**Spec:** [`dev/specs/2026-09-30-audit-lost-receive-design.md`](../specs/2026-09-30-audit-lost-receive-design.md).

## Restrições globais

- Nada muda fora de `test/testkit/audit.go` e `test/integration/harness_test.go` (spec, decisão 4).
- `VisibilityTimeout` do receive do `Audit`: **2 s**.
- As entradas `Failed` do `DeleteMessageBatch` continuam sem virar erro (decisão 3).
- `make check` verde; `make test-integration` verde 3 vezes seguidas; `make test-e2e` verde.
- Sem commits automáticos.

## Foco da revisão

1. **O SDK não repetir o erro injetado** → o red falharia pelo motivo errado, com `connection reset` em vez de prazo. O Passo 2 da Tarefa 1 exige a mensagem `… not delivered … context deadline exceeded`. O erro injetado contém `connection reset`, que o `RetryableConnectionError` repete.
2. **O transporte nunca achar o que interceptar** (resposta sem `"Messages"`, ou nenhum `DeleteMessageBatch`) → o teste passaria sem provar nada. Os dois testes exigem `hit` antes das asserções.
3. **A reentrega quebrar a ordem do grupo** → o FIFO devolve primeiro a mensagem reentregue. O `TestAuditCountsRedeliveryOnce` depende disso, e o `TestAuditCollector` (`got[good][0]`) continua valendo. Os dois rodam no Passo 4 da Tarefa 2.
4. **Um delete lento, com mais de 2 s, sob carga** → a mensagem volta e é recebida de novo. É o caso que a decisão 2 cobre, provado pelo `TestAuditCountsRedeliveryOnce`.
5. **E2E e `Absent`** usam o mesmo `receive` → a verificação final roda `make test-e2e` e a integração completa 3 vezes, o que inclui o `TestAuditAbsentLeavesNoPollBehind` e o I05b.

---

### Tarefa 1: visibility curto no receive (receive perdido)

**Arquivos:**
- Modificar: `test/integration/harness_test.go` (imports, o tipo `onceHTTP`, o helper `auditThrough` e o teste novo, depois do `TestAuditAbsentLeavesNoPollBehind`)
- Modificar: `test/testkit/audit.go` (constante nova e o `ReceiveMessageInput` do `receive`, linhas ~138–149)

**Interfaces:**
- Consome: `testkit.RootAWSConfig(tb) aws.Config`, `testkit.NewEventsTopic(tb, *sqs.Client, *sns.Client) testkit.EventsTopic`, `testkit.NewAudit(ctx, *sqs.Client, queueURL) (*testkit.Audit, error)`, `testkit.SendMessage(tb, *sqs.Client, url, body string, testkit.SendOpts)`, `(*testkit.Audit).WaitFor(tb, ids...) map[string][]testkit.AuditMessage`, `auditEnvelope(eventID, walletID, extra string) string` (do próprio arquivo).
- Produz (para a Tarefa 2): `type onceHTTP struct{ next aws.HTTPClient; target string; intercept func(next aws.HTTPClient, req *http.Request) (*http.Response, bool, error); hit atomic.Bool }` e `func auditThrough(t *testing.T, c *onceHTTP) (*testkit.Audit, *sqs.Client, testkit.EventsTopic)`.

- [ ] **Passo 1: o teste**

Imports a acrescentar em `test/integration/harness_test.go`: `"errors"`, `"io"`, `"sync/atomic"` e `awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"`.

Depois do `TestAuditAbsentLeavesNoPollBehind`:

```go
// onceHTTP sends every request to the SDK transport, except the first one of
// the SQS action target that intercept picks (hit): the broker's answer to that
// one is replaced, as a network fault would (spec audit-lost-receive, decision 5).
type onceHTTP struct {
	next      aws.HTTPClient
	target    string // X-Amz-Target, e.g. AmazonSQS.ReceiveMessage
	intercept func(next aws.HTTPClient, req *http.Request) (*http.Response, bool, error)
	hit       atomic.Bool
}

func (c *onceHTTP) Do(req *http.Request) (*http.Response, error) {
	if c.hit.Load() || req.Header.Get("X-Amz-Target") != c.target {
		return c.next.Do(req)
	}
	resp, hit, err := c.intercept(c.next, req)
	if hit {
		c.hit.Store(true)
	}
	return resp, err
}

// auditThrough is an Audit of a new isolated topic whose SQS client goes
// through c; the returned client reaches the broker directly.
func auditThrough(t *testing.T, c *onceHTTP) (*testkit.Audit, *sqs.Client, testkit.EventsTopic) {
	t.Helper()
	root := testkit.RootAWSConfig(t)
	sqsClient := sqs.NewFromConfig(root)
	topic := testkit.NewEventsTopic(t, sqsClient, sns.NewFromConfig(root))
	c.next = awshttp.NewBuildableClient()
	faulty := root.Copy()
	faulty.HTTPClient = c
	audit, err := testkit.NewAudit(t.Context(), sqs.NewFromConfig(faulty), topic.AuditQueueURL)
	if err != nil {
		t.Fatal(err)
	}
	return audit, sqsClient, topic
}

// Covers: TST-I05 (spec audit-lost-receive, decision 1)
//
// A receive the broker served but whose response never reached the Audit (the
// SDK retries a connection reset) hides its messages only for the Audit's own
// visibility, not for the queue's 30 s: every event of the group still arrives
// within AuditTimeout (the flake of I05a on CI).
func TestAuditRecoversLostReceive(t *testing.T) {
	t.Parallel()
	lose := &onceHTTP{target: "AmazonSQS.ReceiveMessage", intercept: func(next aws.HTTPClient, req *http.Request) (*http.Response, bool, error) {
		resp, err := next.Do(req)
		if err != nil {
			return resp, false, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, false, err
		}
		if !bytes.Contains(body, []byte(`"Messages"`)) {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, false, nil
		}
		return nil, true, errors.New("read tcp: connection reset by peer")
	}}
	audit, sqsClient, topic := auditThrough(t, lose)
	wallet := testkit.NewID()
	ids := []string{testkit.NewID(), testkit.NewID(), testkit.NewID()}
	for _, id := range ids {
		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id})
	}
	audit.WaitFor(t, ids...)
	if !lose.hit.Load() {
		t.Fatal("no receive response was lost: the test proves nothing")
	}
}
```

- [ ] **Passo 2: o red**

Run: `go test -tags=integration -race -count=1 -run '^TestAuditRecoversLostReceive$' ./test/integration/`
Expected: FAIL em cerca de 10 s com `audit: 3 of 3 events not delivered (first: [...]): … context deadline exceeded`. O lote perdido fica em voo pelo visibility de 30 s da fila. Se a falha citar `connection reset`, o SDK não repetiu o erro: pare e reveja o Foco 1.

- [ ] **Passo 3: o visibility do receive**

Em `test/testkit/audit.go`, depois da constante `receiveTimeout`:

```go
// auditVisibility (seconds) hides a received batch only briefly: a batch the
// Audit did not record or delete (a response lost and retried by the SDK, a
// delete that did not arrive) comes back within AuditTimeout instead of
// blocking its FIFO group for the queue's 30 s (spec audit-lost-receive).
const auditVisibility = 2
```

No `receive`, troque o comentário e o input:

```go
// receive reads one batch (long poll of 1 s, hidden for auditVisibility),
// records it and deletes it.
func (a *Audit) receive(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	out, err := a.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(a.url), MaxNumberOfMessages: 10, WaitTimeSeconds: 1, VisibilityTimeout: auditVisibility,
```

(o resto do input e da função não muda).

- [ ] **Passo 4: verde**

Run: o mesmo do Passo 2.
Expected: PASS em poucos segundos: o lote perdido volta depois de 2 s.

- [ ] **Passo 5: checkpoint**

Run: `make check`
Expected: verde, `0 issues.`

---

### Tarefa 2: cada mensagem uma vez (reentrega)

**Arquivos:**
- Modificar: `test/integration/harness_test.go` (import `"strings"` e o teste novo, depois do `TestAuditRecoversLostReceive`)
- Modificar: `test/testkit/audit.go` (campo `seen` no `Audit`, o `NewAudit`, o `record` e o comentário do tipo)

**Interfaces:**
- Consome: `onceHTTP` e `auditThrough` da Tarefa 1; o restante como na Tarefa 1.
- Produz: nada novo exportado. `Audit` ganha o campo interno `seen map[string]struct{}`.

- [ ] **Passo 1: o teste**

```go
// Covers: TST-I05 (spec audit-lost-receive, decision 2)
//
// A message the broker delivers again, because its delete did not arrive, is
// kept once: only a new message from the topic counts as a new delivery.
func TestAuditCountsRedeliveryOnce(t *testing.T) {
	t.Parallel()
	swallow := &onceHTTP{target: "AmazonSQS.DeleteMessageBatch", intercept: func(_ aws.HTTPClient, req *http.Request) (*http.Response, bool, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Request: req,
			Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
			Body:   io.NopCloser(strings.NewReader(`{"Successful":[],"Failed":[]}`)),
		}, true, nil
	}}
	audit, sqsClient, topic := auditThrough(t, swallow)
	wallet, id, last := testkit.NewID(), testkit.NewID(), testkit.NewID()
	send := func(id string) {
		t.Helper()
		testkit.SendMessage(t, sqsClient, topic.AuditQueueURL, auditEnvelope(id, wallet, ""), testkit.SendOpts{GroupID: wallet, DedupID: id})
	}
	send(id)
	audit.WaitFor(t, id)
	send(last)
	got := audit.WaitFor(t, last, id) // same group: last comes only with or after id's redelivery
	if !swallow.hit.Load() {
		t.Fatal("no delete was swallowed: the test proves nothing")
	}
	if n := len(got[id]); n != 1 {
		t.Fatalf("deliveries of %s = %d, want 1 (a redelivery counted as a new delivery)", id, n)
	}
}
```

- [ ] **Passo 2: o red**

Run: `go test -tags=integration -race -count=1 -run '^TestAuditCountsRedeliveryOnce$' ./test/integration/`
Expected: FAIL com `deliveries of <id> = 2, want 1 (a redelivery counted as a new delivery)`.

- [ ] **Passo 3: guardar cada mensagem uma vez**

Em `test/testkit/audit.go`, o comentário do tipo e o campo novo:

```go
// Audit collects the audit queue of one events topic. Every message is kept
// once by eventId, so tests running in parallel over the same queue each find
// their own events, and each message is checked against the event contract
// (spec M4, decision 17). A message the broker delivers again (its delete did
// not arrive) is not a new delivery; a new message from the topic is (spec
// audit-lost-receive, decision 2). Messages are deleted from the queue as soon
// as they are read.
type Audit struct {
	client   *sqs.Client
	url      string
	contract *EventContract

	mu   sync.Mutex
	byID map[string][]AuditMessage // "" keeps bodies without a readable eventId
	seen map[string]struct{}       // SQS MessageId of the messages kept
}
```

No `NewAudit`:

```go
	return &Audit{client: client, url: queueURL, contract: contract, byID: map[string][]AuditMessage{}, seen: map[string]struct{}{}}, nil
```

No começo do `record`:

```go
// record keeps one message, once; a.mu is held.
func (a *Audit) record(m types.Message) {
	if _, ok := a.seen[aws.ToString(m.MessageId)]; ok {
		return // delivered again by the broker: its delete did not arrive
	}
	a.seen[aws.ToString(m.MessageId)] = struct{}{}
	body := []byte(aws.ToString(m.Body))
```

(o resto do `record` não muda).

- [ ] **Passo 4: verde e vizinhos**

Run: `go test -tags=integration -race -count=3 -run '^TestAudit' ./test/integration/`
Expected: PASS nas 3 execuções de `TestAuditCollector` (2 entregas de `good`, que são mensagens distintas), `TestAuditAbsentLeavesNoPollBehind`, `TestAuditRecoversLostReceive` e `TestAuditCountsRedeliveryOnce`.

- [ ] **Passo 5: checkpoint**

Run: `make check`
Expected: verde, `0 issues.`

---

### Tarefa 3: documentos e verificação

**Arquivos:**
- Modificar: `docs/implementation-plan.md` (§5, tabela de riscos), `docs/dev/diary.md` (entrada nova antes de "Onde paramos"), cabeçalho da spec (status)

- [ ] **Passo 1: documentos**

- `implementation-plan.md` §5: acrescente, antes da linha "Estouro de prazo":

```markdown
| ~~`TestOutboxConcurrentPublishers` (I05a) intermitente no CI~~ | `audit: 41 of 200 events not delivered` na primeira execução do job; passa no re-run | ✅ Tratado em 30/09: os 200 eventos chegaram à fila (log do MiniStack), mas um lote recebido e não registrado pelo `testkit.Audit` (resposta perdida e repetida pelo SDK) ficava em voo pelos 30 s da fila, e o grupo FIFO junto. O `Audit` passou a receber com visibility de 2 s e a guardar cada mensagem uma vez pelo `MessageId` (`TestAuditRecoversLostReceive`, `TestAuditCountsRedeliveryOnce`; [spec](dev/specs/2026-09-30-audit-lost-receive-design.md)) |
```

- `diary.md`: nova entrada antes de `## Onde paramos`:

```markdown
## 30/09/2026 (qua): I05a intermitente no CI

- **Sintoma:** o job de integração falhava na primeira execução e passava no re-run, sempre no `TestOutboxConcurrentPublishers` (41 de 200 eventos "não entregues").
- **Causa:** o log do MiniStack mostrou os 200 eventos na fila de auditoria em cerca de 1 s. Um lote recebido e não registrado pelo `testkit.Audit` (o único aviso do SDK, às 22:18:36, é de uma resposta lida pela metade, e o SDK repete sem avisar) ficava em voo pelos 30 s de visibility, e o grupo FIFO junto, além do `AuditTimeout` de 10 s. Não reproduz localmente.
- **Correção:** o `Audit` recebe com visibility de 2 s e guarda cada mensagem uma vez pelo `MessageId`. Nada muda fora do `testkit` ([spec](specs/2026-09-30-audit-lost-receive-design.md) → [plano](plans/2026-09-30-audit-lost-receive.md)).
```

- Spec: status → "aprovada pelo autor em 30/09/2026".

- [ ] **Passo 2: verificação (`superpowers:verification-before-completion`)**

Run:

```sh
make check
make test-integration && make test-integration && make test-integration
make test-e2e
```

Expected: tudo verde; as 3 execuções da integração com todos os pacotes `ok`, e o e2e `ok`.

- [ ] **Passo 3: proposta de commits** (o autor autoriza)

1. `fix(testkit): recover audit batches that were received but not recorded`: `test/testkit/audit.go`, `test/integration/harness_test.go`.
2. `docs: record the audit lost receive fix`: a spec, o plano, `docs/decisions.md`, `docs/test-plan.md`, `docs/structure.md`, `docs/implementation-plan.md` e `docs/dev/diary.md`.

A pasta `temp/` (logs baixados do CI) fica fora dos commits.
