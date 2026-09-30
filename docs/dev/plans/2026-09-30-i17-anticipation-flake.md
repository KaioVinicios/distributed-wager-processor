# I17 intermitente: plano de implementação

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` ([`development-workflow.md`](../../development-workflow.md) §7). A tarefa de código usa `superpowers:test-driven-development`. Os passos usam checkbox (`- [ ]`). **Não há passos de commit:** o commit é proposto no fim, antes dos commits do M8, e o autor autoriza ([`development-workflow.md`](../../development-workflow.md) §6).

**Objetivo:** tornar determinística a asserção da antecipação da pendência no I17 (`TestDomainFlowsPersist`), que hoje depende do relógio e falha sob carga.

**Arquitetura:** só o teste muda. Ele passa a exigir que o `next_attempt_at` gravado do `refund-2` seja o instante de conclusão do `bet-3`, que é o valor que o `AdvanceDependents` grava por contrato (D-11). Nenhum código de produção muda.

**Stack:** Go 1.27.1, `pgx/v5`, testes com a tag `integration`.

**Spec:** [`dev/specs/2026-09-30-i17-anticipation-flake-design.md`](../specs/2026-09-30-i17-anticipation-flake-design.md).

## Restrições globais

- `make check` verde; `make test-integration` verde 3 vezes seguidas no fim.
- O atraso diagnóstico é só o red: ele sai antes do verde final e nunca fica no teste.
- Nenhuma mudança no `AdvanceDependents` (spec, decisão 2).
- Sem commits automáticos.

## Foco da revisão

1. **Instantes com precisões diferentes** → a igualdade falharia por nanossegundos. Os dois lados passam por UTC truncado em microssegundos (`normalize` no domínio, `Truncate(time.Microsecond)` no repositório). Se o passo de green falhar por isso, compare os dois lados com `Truncate(time.Microsecond)` e registre o desvio.
2. **Relógio de outra instância atrás do `createdAt`** → não se aplica: o teste roda num processo só e usa o `CompletedAt()`, que não passa pelo piso `notBefore` do `updatedAt`.
3. **A nova asserção passando por acaso** → a sensibilidade (Passo 5) prova que um `AdvanceDependents` que não grava nada a derruba.

---

### Tarefa 1: a asserção da antecipação (I17)

**Arquivos:**
- Modificar: `internal/adapters/postgres/flows_integration_test.go` (o laço dos passos, linhas ~108–117, e a asserção das linhas 125–127)

**Interfaces:**
- Consome: `(*wagering.WagerTransaction).NextAttemptAt() time.Time` e `CompletedAt() time.Time` (existentes); `byExt map[string]*wagering.WagerTransaction` do próprio teste.

- [ ] **Passo 1: o red, com o atraso diagnóstico**

No laço `for _, s := range steps {` do `TestDomainFlowsPersist`, antes do `process`:

```go
	for _, s := range steps {
		if s.ext == "bet-3" {
			time.Sleep(200 * time.Millisecond) // DIAGNOSTIC ONLY: bet-3 after refund-2's first retry; removed in Passo 4
		}
		tx := process(t, command(t, w, p, s.kind, s.amount, s.ext, s.ref))
```

Run: `go test -tags=integration -race -count=3 -run '^TestDomainFlowsPersist$' ./internal/adapters/postgres/`
Expected: FAIL nas 3 execuções com `refund-2 was not advanced: next attempt …`, o mesmo sintoma da verificação do M8.

- [ ] **Passo 2: a nova asserção**

Troque:

```go
	if !pending.NextAttemptAt().Before(byExt["refund-2"].NextAttemptAt()) {
		t.Fatalf("refund-2 was not advanced: next attempt %v", pending.NextAttemptAt())
	}
```

por:

```go
	// The conclusion of bet-3 advanced refund-2 to its own instant: AdvanceDependents
	// gets the now of the conclusion (D-11). Comparing with the original schedule
	// would race the clock: bet-3 may conclude after refund-2's first retry.
	if !pending.NextAttemptAt().Equal(byExt["bet-3"].CompletedAt()) {
		t.Fatalf("refund-2 next attempt %v, want the conclusion of bet-3 %v", pending.NextAttemptAt(), byExt["bet-3"].CompletedAt())
	}
```

- [ ] **Passo 3: verde com o atraso**

Run: o mesmo do Passo 1.
Expected: PASS nas 3 execuções: a asserção não depende mais do intervalo entre as duas operações.

- [ ] **Passo 4: tirar o atraso**

Remova o bloco `if s.ext == "bet-3" { time.Sleep(…) }` do Passo 1.

Run: `go test -tags=integration -race -count=5 -run '^TestDomainFlowsPersist$' ./internal/adapters/postgres/`
Expected: PASS nas 5 execuções.

- [ ] **Passo 5: sensibilidade**

1. Em `internal/adapters/postgres/transaction_repo.go`, faça o `AdvanceDependents` devolver `return 0, nil` na primeira linha (não grava nada).
2. Run: o mesmo do Passo 4 com `-count=1`. Expected: FAIL com `refund-2 next attempt <horário original>, want the conclusion of bet-3 …`.
3. Desfaça. Run de novo → PASS.
4. No comentário acima do `TestDomainFlowsPersist`, acrescente a linha:

```go
// Sensitivity: an AdvanceDependents that writes nothing → refund-2 keeps its original schedule, not bet-3's conclusion.
```

- [ ] **Passo 6: checkpoint**

Run: `make check`
Expected: verde, `0 issues.`

---

### Tarefa 2: documentos e verificação

**Arquivos:**
- Modificar: `docs/implementation-plan.md` (§5, tabela de riscos), `docs/dev/diary.md` (entrada do M8), cabeçalho da spec (status)

- [ ] **Passo 1: documentos**

- `implementation-plan.md` §5: acrescente, antes da linha "Estouro de prazo", a linha:

```markdown
| ~~`TestDomainFlowsPersist` (I17) intermitente~~ | `refund-2 was not advanced` sob carga (1 em 4 execuções completas da integração, na verificação do M8) | ✅ Tratado em 30/09: a asserção comparava a antecipação com o horário agendado originalmente, uma corrida contra o relógio; passou a exigir o instante de conclusão do `bet-3`, que é o que o `AdvanceDependents` grava ([spec](dev/specs/2026-09-30-i17-anticipation-flake-design.md)) |
```

- `diary.md`: na entrada do M8, depois do item "Achados da execução", acrescente:

```markdown
- **I17 intermitente (fora do M8, corrigido em seguida):** o `TestDomainFlowsPersist` falhou 1 vez na verificação. A asserção da antecipação comparava o `next_attempt_at` com o horário agendado originalmente e perdia a corrida contra o relógio sob carga; reproduzido 3 de 3 com um atraso de 200 ms. Passou a exigir o instante de conclusão do `bet-3` ([spec](specs/2026-09-30-i17-anticipation-flake-design.md) → [plano](plans/2026-09-30-i17-anticipation-flake.md)).
```

- Spec: status → "aprovada pelo autor em 30/09/2026".

- [ ] **Passo 2: verificação (`superpowers:verification-before-completion`)**

Run:

```sh
make check
make test-integration && make test-integration && make test-integration
```

Expected: tudo verde; as 3 execuções da integração com 22 pacotes `ok`.

- [ ] **Passo 3: proposta de commit** (antes dos 8 do M8; o autor autoriza)

`test(postgres): assert the anticipation instant in the domain flows test` — `internal/adapters/postgres/flows_integration_test.go`, `docs/dev/specs/2026-09-30-i17-anticipation-flake-design.md`, `docs/dev/plans/2026-09-30-i17-anticipation-flake.md`. A linha de risco e o parágrafo do diário vão junto no commit de documentos do M8 (os dois arquivos já mudam lá).
