# R03 intermitente: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` ([`development-workflow.md`](../../development-workflow.md) §7). As tarefas de código usam `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Não há passos de commit:** os commits são propostos no fim, e o autor autoriza ([`development-workflow.md`](../../development-workflow.md) §6).

**Objetivo:** o R03 (e o R01) só enviam mensagens depois que os long polls órfãos dos consumidores reiniciados pelo `consumerOnlyOn0` expiraram.

**Arquitetura:** só o `testkit` e o `test/e2e` mudam. O `Cluster` registra a última parada no `terminate` e expõe `AwaitOrphanPolls()`, que dorme o que a função pura `orphanWait` calcular (spec, decisões 1 e 2). O `consumerOnlyOn0` chama esse método no fim (decisão 3). Nenhum código de produção muda.

**Stack:** Go 1.27.1, testes unitários sem tag e e2e com a tag `e2e`, MiniStack 1.5.18.

**Spec:** [`dev/specs/2026-10-01-r03-orphan-poll-design.md`](../specs/2026-10-01-r03-orphan-poll-design.md).

## Restrições globais

- Nada muda fora de `test/testkit/cluster.go`, `test/testkit/harness_internal_test.go` e `test/e2e/resilience_test.go`, além dos documentos da spec §4.
- Margem: **200 ms** (`orphanMargin`).
- `make check` verde; R03 `-count=40` verde; `make test-e2e` e `make test-integration` verdes com o compose inteiro de pé.
- Sem commits automáticos.

## Foco da revisão

1. **O red ser só um erro de compilação** → o Passo 1 cria `orphanWait` em stub (`return 0`) e o teste falha numa asserção.
2. **A parada registrada antes da hora** → `lastStop` é gravado depois de o processo terminar (`<-p.done`), em todos os caminhos do `terminate` que de fato param um processo. A parada de uma instância que já tinha saído não abre poll novo, então não precisa ser registrada.
3. **Concorrência:** o `terminate` roda na goroutine do teste, e o `StopAsync` roda em outra. O `lastStop` usa um `sync.Mutex`.

---

### Tarefa 1: `orphanWait` (função pura)

**Arquivos:**
- Modificar: `test/testkit/harness_internal_test.go` (teste novo no fim)
- Modificar: `test/testkit/cluster.go` (constante `orphanMargin` e função `orphanWait`)

- [x] **Passo 1: o teste e o stub**

Em `harness_internal_test.go`:

```go
// Covers: R03 setup (spec r03-orphan-poll, decision 2)
func TestOrphanWait(t *testing.T) {
	stop := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name     string
		lastStop time.Time
		now      time.Time
		want     time.Duration
	}{
		{"poll still open", stop, stop.Add(500 * time.Millisecond), 1700 * time.Millisecond},
		{"poll already over", stop, stop.Add(3 * time.Second), 0},
		{"no instance stopped", time.Time{}, stop, 0},
	} {
		if got := orphanWait(c.lastStop, c.now, 2*time.Second); got != c.want {
			t.Errorf("%s: orphanWait = %v, want %v", c.name, got, c.want)
		}
	}
}
```

Em `cluster.go`, o stub:

```go
func orphanWait(lastStop, now time.Time, waitTime time.Duration) time.Duration { return 0 }
```

- [x] **Passo 2: red**

`go test -run TestOrphanWait ./test/testkit/`. Esperado: FAIL com `poll still open: orphanWait = 0s, want 1.7s`.

- [x] **Passo 3: implementação mínima**

```go
// orphanMargin covers the local latency between the stop and the broker.
const orphanMargin = 200 * time.Millisecond

// orphanWait is how long, at now, the long polls of a consumer stopped at
// lastStop may still be open at the broker: each ends at most waitTime after
// it began, before the stop (messaging §4.5). Zero lastStop: no consumer stopped.
func orphanWait(lastStop, now time.Time, waitTime time.Duration) time.Duration {
	if lastStop.IsZero() {
		return 0
	}
	return max(lastStop.Add(waitTime+orphanMargin).Sub(now), 0)
}
```

- [x] **Passo 4: green**

`go test -run TestOrphanWait ./test/testkit/`. Esperado: PASS.

### Tarefa 2: `Cluster.AwaitOrphanPolls` e o `consumerOnlyOn0`

**Arquivos:**
- Modificar: `test/testkit/cluster.go` (campos `waitTime`, `stopMu` e `lastStop`; registro no `terminate`; método novo)
- Modificar: `test/e2e/resilience_test.go` (`consumerOnlyOn0`)

- [x] **Passo 1: o cluster**
  - Guardar `cfg.SQSWaitTime` em `c.waitTime` no construtor, depois do `e2eTimes` e das opções.
  - No `terminate`, depois de `<-p.done` (inclusive no caminho do `Kill`), gravar `c.lastStop = time.Now()` sob o `stopMu`.
  - Criar o método:

```go
// AwaitOrphanPolls waits until the long polls that the consumers stopped by
// this cluster left open at the broker are over: a message sent before that
// can be taken by one and hidden for a visibility timeout (messaging §4.5).
func (c *Cluster) AwaitOrphanPolls() {
	c.stopMu.Lock()
	last := c.lastStop
	c.stopMu.Unlock()
	time.Sleep(orphanWait(last, time.Now(), c.waitTime))
}
```

- [x] **Passo 2: o chamador.** No fim do `consumerOnlyOn0`, chamar `cluster.AwaitOrphanPolls()` e atualizar o comentário da função: "and waits until the long polls of the consumers it stopped are over (spec r03-orphan-poll)".

- [x] **Passo 3: verificação do R03**

`go test -tags=e2e -race -p 1 -count=40 -timeout 20m -run 'TestGracefulShutdownSQS$' ./test/e2e/...`. Esperado: `ok`, com 0 falhas em 40 rodadas (antes: 3 em 28).

### Tarefa 3: documentos e verificação final

- [x] `messaging.md` §4.5 e `ARCHITECTURE.md` §16: a entrega ao órfão conta como recebimento para o `maxReceiveCount`.
- [x] `test-plan.md`, linha do R03: o envio só acontece depois de os polls órfãos expirarem.
- [x] `docs/dev/diary.md`: entrada com a causa e a correção.
- [x] `make check`, `make test-integration` e `make test-e2e`, com as saídas na mensagem final.
