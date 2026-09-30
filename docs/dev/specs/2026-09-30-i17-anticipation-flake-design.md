# I17 intermitente: a asserção da antecipação da pendência: design

**Data:** 30/09/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 30/09/2026 ("prossiga", junto com o plano)

**Origem:** a verificação do M8 ([diário](../diary.md)), em que o `make test-integration` falhou 1 vez em 4 execuções completas, sempre no mesmo teste do M2.

---

## 1. Investigação (causa raiz)

**Sintoma:** `TestDomainFlowsPersist` (I17, `internal/adapters/postgres/flows_integration_test.go`, commit `240a9f4` do M2) falha com `refund-2 was not advanced: next attempt …`.

**Evidências:**

1. **A asserção atual** (linha 125) exige que o `next_attempt_at` gravado do `refund-2`, depois da conclusão do `bet-3`, seja **anterior** ao horário agendado quando ele ficou pendente: `pending.NextAttemptAt().Before(byExt["refund-2"].NextAttemptAt())`.
2. **A agenda do teste** (`domain_integration_test.go:92`) põe a primeira retentativa a cerca de 100 ms ±20% da criação.
3. **O que o repositório grava:** o `processWith` chama `AdvanceDependents(…, now)` com o mesmo `now` da conclusão do `bet-3` (`domain_integration_test.go:229`), e o `AdvanceDependents` grava `next_attempt_at = now`.
4. **O que acontece sob carga:** entre a pendência do `refund-2` e a conclusão do `bet-3`, o teste faz duas leituras e uma transação completa. Com os 22 pacotes da integração rodando em paralelo e com `-race`, esse intervalo pode passar de ~80 ms. Aí o `now` do `bet-3` cai **depois** do horário original, e a asserção `Before` falha.
5. **Reprodução:** com um atraso diagnóstico de 200 ms antes do `bet-3`, o teste falhou 3 de 3 vezes, com a mesma mensagem. Sem o atraso, passou 5 de 5.
6. **O produto está correto:**
   - a pendência continua devida, porque o `ClaimDue` usa `next_attempt_at <= now`;
   - o `resume` logo em seguida a resolve;
   - o `AdvanceDependents` gravar um horário posterior a um que já estava vencido não atrasa nada: a pendência continua devida no próximo ciclo.

**Causa raiz:** a asserção verifica a antecipação de forma indireta, contra o relógio. Ela supõe que o `bet-3` conclui antes da primeira retentativa agendada, e isso não é garantido.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **A asserção passa a exigir o instante exato:** o `next_attempt_at` gravado do `refund-2` é igual ao `CompletedAt()` do `bet-3` | É o que a antecipação faz por contrato (D-11: "antecipa `next_attempt_at = now()`" na mesma transação que conclui a referência). Não depende do relógio, e prova o valor gravado, não só a direção. Os dois instantes passam pela mesma normalização (UTC truncado em microssegundos: `normalize` no domínio, `Truncate(time.Microsecond)` no repositório) |
| 2 | **O `AdvanceDependents` não muda** (sem `LEAST(next_attempt_at, now)`) | Gravar um instante posterior a um já vencido não atrasa a pendência. Mudar o produto não traria ganho e não conserta o teste: com `LEAST`, o valor ficaria igual ao original, e o `Before` continuaria falhando |
| 3 | **O atraso diagnóstico é só o red:** ele reprova a asserção atual, deve passar com a nova, e é removido antes do verde final | Prova que a nova asserção não depende do intervalo entre as duas operações. O teste não fica mais lento |
| 4 | **O risco sai da tabela §5 do `implementation-plan.md`**, marcado como tratado, e o diário registra a causa | Mesmo tratamento do flake do `DROP` de hoje |

---

## 3. Teste

| ID | Teste | Prova |
| --- | --- | --- |
| I17 | `TestDomainFlowsPersist` (alterado) | Com o atraso diagnóstico de 200 ms antes do `bet-3`: a asserção atual falha (red reproduzindo o defeito) e a nova passa. Sem o atraso: passa. **Sensibilidade:** um `AdvanceDependents` que não grava nada (`transaction_repo.go`) deixa o `next_attempt_at` no horário original, e a nova asserção falha (registrada em `// Sensitivity:`) |

**Pronto quando:**
1. `make check` verde.
2. O red e a sensibilidade do §3 vistos.
3. `make test-integration` verde 3 vezes seguidas.

---

## 4. Documentos

- `implementation-plan.md` §5: o risco "I17 intermitente" entra como tratado, com a causa.
- `diary.md`: um parágrafo na entrada do M8, com a causa e a correção.
- `test-plan.md`: nada muda; o I17 continua descrito como está.
