# R01 intermitente: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` e `superpowers:test-driven-development` ([`development-workflow.md`](../../development-workflow.md) §3). Commits só no fim, com a autorização do autor e sem trailer de coautoria de IA.

**Objetivo:** o R01 deixa de contar como "durante a queda" as respostas liberadas pelo `unpause`.

**Spec:** [`dev/specs/2026-10-01-r01-resume-window-design.md`](../specs/2026-10-01-r01-resume-window-design.md).

## Restrições globais

- Só `test/e2e/resilience_test.go` muda em Go. O produto não muda.
- `make infra-up` antes de qualquer execução do e2e. Nunca rodar o e2e junto com a integração.

---

### Tarefa 1: o fim da janela antes do `resume()`

**Arquivos:** `test/e2e/resilience_test.go` (`TestPostgresOutage`).

- [ ] **Passo 1: red com o atraso.** Depois de `resume()`, inserir temporariamente `time.Sleep(1500 * time.Millisecond)` antes de `resumedAt := time.Now()`. Rodar `go test -tags=e2e -race -p 1 -count=1 -timeout 15m -run '^TestPostgresOutage$' ./test/e2e/`. Esperado: FAIL com `answer during the outage = 200 …`, igual à execução da investigação.

- [ ] **Passo 2: a correção.** Trocar

```go
	resume()
	resumedAt := time.Now()
```

por

```go
	// The window ends when the resume begins: docker compose unpause releases
	// the database before it returns, and a request that was waiting for it
	// may then answer 200 legitimately (spec of the R01 window, 01/10).
	resuming := time.Now()
	resume()
```

e, no laço que conta as respostas, `c.at.After(resumedAt)` por `!c.at.Before(resuming)`. O atraso do passo 1 continua no lugar (agora depois do `resume()` e antes de `time.Sleep(time.Second)`).

- [ ] **Passo 3: green com o atraso.** O mesmo comando do passo 1. Esperado: PASS.

- [ ] **Passo 4: remover o atraso** e rodar o R01 com `-count=10`. Esperado: PASS nas 10 execuções.

- [ ] **Passo 5: documentos.**
  - [`test-plan.md`](../../test-plan.md), linha do R01: "as respostas são contadas até o início do `resume`".
  - [Diário](../diary.md): entrada com a causa e a correção.

- [ ] **Checkpoint:** `make check` verde.

### Tarefa 2: a suíte completa 3 vezes e os commits

- [ ] **Passo 1:** limpar o cache de testes (`go clean -testcache`) e rodar `make check`, `make test-integration` e `make test-e2e`, em sequência, 3 vezes. Esperado: as 9 execuções com saída 0.
- [ ] **Passo 2:** se as 9 passarem, fazer os commits da D-23 (proposta já apresentada) e mais:
  - `fix(e2e): end the r01 outage window before the resume` (`test/e2e/resilience_test.go`);
  - a spec, o plano, o `test-plan.md` e o diário do R01 no commit de documentação.

  Se alguma falhar, nenhum commit: investigar e reportar.
