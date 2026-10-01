# I05a intermitente no CI: o receive perdido da auditoria: design

**Data:** 30/09/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 30/09/2026 ("proceed", junto com o plano)

**Origem:** o job de integração do GitHub Actions falha na primeira execução e passa no re-run, sempre no `TestOutboxConcurrentPublishers` (I05a, `internal/adapters/outbox/publisher_integration_test.go`): `audit: 41 of 200 events not delivered … context deadline exceeded`. O log da execução foi baixado para a análise (fora do repositório).

---

## 1. Investigação (causa raiz)

**Evidências:**

1. **Todos os eventos chegaram à fila.** O log do MiniStack da execução que falhou tem 200 linhas `SNS FIFO publish to …events-644b8b1f` e 200 linhas `SNS fanout → SQS events-audit-644b8b1f` (a fila do I05a; as carteiras `0192f291-27dd-7d3f-8071-…` são só dele). No código do MiniStack 1.5.18 (`sns.py`, `_deliver_to_sqs`), o fanout só é registrado **depois** do `queue["messages"].append(msg)`, e uma duplicata descartada pela deduplicação do SNS retorna antes, sem log. Todas as 200 publicações ocorreram entre 22:18:35 e 22:18:36, e nenhuma depois disso. Portanto o publisher e o outbox estão corretos, e os 41 eventos estavam na fila durante os 9 s restantes do `WaitFor`.
2. **O MiniStack não registrou erro.** Não há `Error handling … request`, traceback, `queue not found` nem `queue policy denies`. Isso descarta a hipótese de um 500 seguido de retry.
3. **Uma mensagem recebida e não apagada bloqueia o grupo FIFO inteiro.** O `_collect_fifo` pula os grupos com mensagem em voo. Uma sonda local confirmou: com `g1-0` recebida e retida, os receives seguintes trouxeram só `g2-*` e depois vieram vazios. A fila de auditoria usa o visibility padrão (30 s), que é maior que o `AuditTimeout` (10 s). Com 20 carteiras de 10 eventos cada, perder um único lote esconde o resto dessas carteiras até o fim do prazo.
4. **Há um sinal de receive perdido.** O único aviso do pacote é `SDK 2026/09/30 22:18:36 WARN failed to discard remaining HTTP response body`, às 22:18:36, o instante em que a entrega parou. O smithy-go emite esse aviso quando a leitura do corpo de uma resposta falha no meio. O retryer do SDK repete em silêncio os erros `connection reset` e `use of closed network connection` (`aws/retry/retryable_error.go`). Se isso aconteceu num `ReceiveMessage` do `Audit`, o MiniStack já tinha marcado as mensagens como em voo, e o SDK as descartou ao repetir. **Não provado:** o logger do SDK escreve no stderr do processo, então o aviso pode ter vindo de outro teste paralelo do pacote. A documentação do `ReceiveRequestAttemptId` no SDK descreve esse modo de falha no FIFO: sem o parâmetro, "no retries work until the original visibility timeout expires". O MiniStack 1.5.18 só o valida e não o implementa, então ele não serve de correção aqui.
5. **Localmente o teste não reproduz:** leva cerca de 1 s isolado, 2,2 s na suíte completa e 2 a 2,5 s com o MiniStack limitado a 0,25 CPU. O problema não é lentidão.

**Causa raiz:** o `testkit.Audit` depende de que todo receive seja registrado e apagado. Quando isso falha (resposta perdida e repetida pelo SDK, ou um delete que não chega), a mensagem fica em voo pelo visibility de 30 s, e o grupo FIFO fica bloqueado junto, por mais tempo que o `AuditTimeout`. É o mesmo tipo de defeito do flake do I05b no M5 (decisão 18 da [spec do M5](2026-09-29-m5-sqs-consumer-design.md)): o observador esconde mensagens por um visibility timeout. O gatilho no CI fica fora do sistema testado e não foi provado; a correção não depende dele.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **O `Audit.receive` pede `VisibilityTimeout = 2 s`** no `ReceiveMessage` (constante do `testkit`), em vez do padrão da fila | Um lote que o `Audit` não registrou ou não apagou volta à fila em até 2 s, com folga para 4 ciclos dentro do `AuditTimeout` de 10 s. A correção não faz aparecer um evento que não chegou: só encurta o tempo em que uma mensagem **já entregue** fica escondida. Uma falha real do publisher continua reprovando o teste |
| 2 | **O `Audit` guarda cada mensagem SQS uma vez, pelo `MessageId`** | Com o visibility curto, um delete lento ou perdido faz o broker devolver a mesma mensagem. Contá-la de novo seria uma falsa duplicata do publisher e quebraria o `TestAuditCollector`, que exige exatamente 2 entregas de `good`. Uma republicação real que passe pela deduplicação do SNS é outra mensagem SQS, com outro `MessageId`, e continua sendo contada. A semântica passa de "cada vez que o broker entregou" para "cada mensagem que o tópico entregou" |
| 3 | **As entradas `Failed` do `DeleteMessageBatch` continuam sem virar erro** (proposta descartada na análise) | Quando o visibility vence e a mensagem é recebida de novo, o receipt handle muda, e o delete com o handle antigo falha com `ReceiptHandleIsInvalid`. Isso é benigno: a mensagem volta e é apagada no receive seguinte. Transformar isso em erro criaria um flake novo |
| 4 | **Nada muda fora do `testkit`** | O consumidor mantém o visibility de 30 s (D-12). A fila `wallet-events-audit.fifo` e seus atributos ficam iguais (messaging §2). O visibility vai só no receive do observador de teste. O `Absent` continua sem cancelar receives no meio (M5, decisões 17 e 18) |
| 5 | **Os testes reproduzem o gatilho pelo transporte HTTP do SDK**, não por um receive manual | Um `aws.HTTPClient` de teste repassa a requisição e entrega ao SDK um `connection reset` no lugar da resposta, o mesmo caminho do retry silencioso. Assim o receive perdido é do próprio `Audit`, com o visibility que ele pede. Sem API nova no `testkit`: o `NewAudit` já recebe o `*sqs.Client` |

---

## 3. Testes

Os dois ficam em `test/integration/harness_test.go`, junto do `TestAuditCollector` e do `TestAuditAbsentLeavesNoPollBehind`.

| Teste | Montagem | Prova |
| --- | --- | --- |
| `TestAuditRecoversLostReceive` | Cliente SQS do `Audit` com um transporte que perde a **primeira** resposta de `ReceiveMessage` que traz mensagens (repassa a requisição e devolve `connection reset`). Três eventos numa mesma carteira, enviados direto à fila de auditoria | `Wait` com `AuditTimeout` recebe os três. **Red:** sem a decisão 1, o lote perdido fica em voo por 30 s e o `Wait` falha no prazo |
| `TestAuditCountsRedeliveryOnce` | Transporte que engole o **primeiro** `DeleteMessageBatch` (responde 200 vazio sem repassar). Envia `id`, espera; depois envia `last` na mesma carteira e espera | Quando `last` chega, a reentrega de `id` já passou pelo `Audit` (o FIFO só libera `last` depois de `id`). `id` tem exatamente **1** entrega. **Red:** sem a decisão 2, ele tem 2 |

Os dois testes são novos e o red de cada um é o próprio defeito, com asserção falhando. O `TestAuditRecoversLostReceive` exige que o transporte tenha de fato perdido uma resposta, para não passar por acaso.

**Pronto quando:**
1. `make check` verde.
2. Os dois reds vistos, cada um com a mensagem esperada.
3. `make test-integration` verde 3 vezes seguidas, e `make test-e2e` verde (o e2e usa o mesmo `Audit` pelo `Harness`).
4. O CI é confirmado pelo autor depois do push, porque o gatilho não se reproduz localmente.

---

## 4. Documentos

- [`decisions.md`](../../decisions.md) D-13: um item sobre a leitura da auditoria nos testes (decisões 1 a 3). Atualizado junto com esta spec.
- [`test-plan.md`](../../test-plan.md) §3.3: linha do visibility da auditoria (2 s na integração e no e2e). Atualizado junto com esta spec.
- [`structure.md`](../../structure.md): descrição do `audit.go`. Atualizado junto com esta spec.
- No fim: [`implementation-plan.md`](../../implementation-plan.md) §5 ganha o risco "I05a intermitente no CI" como tratado, e o [diário](../diary.md) registra a causa e a correção.
