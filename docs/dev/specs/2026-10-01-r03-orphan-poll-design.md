# R03 intermitente: o long poll órfão dos consumidores reiniciados: design

**Data:** 01/10/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 01/10/2026 ("escreva a spec e o plano diretamente e aplique-os", junto com o plano)

**Origem:** `make test-e2e` falhou em `TestGracefulShutdownSQS` (R03) com o compose inteiro de pé (réplicas, Prometheus e Grafana): `resilience_test.go:373: 3 sessions waiting for the wallet lock: not reached within 5s`. Rodado sozinho, o teste passou.

---

## 1. Investigação (causa raiz)

**Evidências:**

1. **A falha está na preparação, não no shutdown.** A asserção que falha é o `waitLockWaiters(t, 3)`. Ele espera 5 s pelas 3 sessões que o consumidor da instância 0 abre no lock das carteiras, antes do `SIGTERM`. Nas rodadas que chegam ao shutdown, todas as asserções de SQS-09 e FX-04 passam.
2. **Taxa:** 2 falhas em 8 rodadas (`-count=8`), depois 1 em 20, sempre com a mesma mensagem.
3. **Assinatura de um visibility timeout.** Uma instrumentação temporária, já revertida, mediu o tempo até as 3 sessões chegarem ao lock em 40 rodadas. Em 37 delas, ficou entre 0 e 120 ms. Nas outras 3, levou 5,13 s, 5,16 s e 5,96 s, logo depois do visibility do e2e (`SQS_VISIBILITY_TIMEOUT` de 5 s, `test/testkit/env.go`). Com espera mais longa, a primeira rodada lenta quebrou de outro jeito: `30 messages in the inbox: not reached within 1m0s`, e 7 mensagens foram parar na DLQ. As rodadas seguintes do mesmo processo herdaram a DLQ (`DLQ depth = 7, want 0`).
4. **O mecanismo já está documentado.** Pelo [`messaging.md`](../../messaging.md) §4.5, um long poll cancelado pelo cliente continua aberto no broker até o fim do seu `WaitTimeSeconds`, e uma mensagem que chega nesse intervalo pode ser entregue a ele e ficar invisível por um visibility timeout. O `consumerOnlyOn0` reinicia instâncias que estavam com o consumidor ligado (as 3 no R03; a 1 e a 2 no R01). Cada uma deixa até 2 polls órfãos (`SQS_CONSUMER_POLLERS`), abertos por até `SQS_WAIT_TIME` (2 s no e2e, `e2eTimes`). O R03 envia as 30 mensagens logo depois do último reinício, ainda dentro dessa janela.
5. **A entrega ao órfão também gasta uma tentativa.** A fila de teste manda à DLQ depois de 3 recebimentos (`testkit.CreateQueues`). O R03 já usa 2: o da instância 0, que é liberado no shutdown, e o da instância 1. O recebimento do órfão é o terceiro, e é por isso que 7 mensagens foram para a DLQ no item 3.

**Causa raiz:** o teste envia mensagens enquanto os long polls dos consumidores que ele mesmo derrubou ainda podem estar abertos no broker. Quando um órfão pega um lote, a instância 0 não recebe as cabeças dos 3 grupos dentro do prazo e o teste falha na preparação. A carga extra do compose não é a causa; ela só muda a frequência. O produto se comporta como documentado: não há perda nem duplicidade.

**Achado secundário, que não será corrigido:** depois da falha, o cleanup reportou `version 1 != last ledger version 2`. O `LedgerProblems` (`test/testkit/postgres.go`) lê a carteira e o ledger em consultas separadas sobre o pool, sem snapshot, e a instância 0 confirma um BET entre as duas leituras, porque o cleanup já soltou o lock. Isso só aparece depois de uma falha, e a reconciliação, que usa `REPEATABLE READ`, deu `consistent`. Fica registrado aqui e fora do escopo.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **O `Cluster` registra o instante da última parada de instância** (`terminate`) e ganha `AwaitOrphanPolls()`, que dorme até `última parada + SQS_WAIT_TIME + 200 ms` | O fim do poll órfão acontece no broker e não é observável, então a espera é condicionada ao único limite conhecido: o poll termina até `SQS_WAIT_TIME` depois de ter começado, e começou antes da parada. Contar a partir da parada é conservador. Os 200 ms cobrem a latência local. O valor vem da configuração do cluster, não de uma constante solta |
| 2 | **O cálculo fica numa função pura**, `orphanWait(lastStop, now, waitTime)`, testada sem infraestrutura | É a única lógica nova. Um teste unitário dá o red deterministicamente, coisa que o flake de ~5 a 25% não daria |
| 3 | **O `consumerOnlyOn0` chama `cluster.AwaitOrphanPolls()` no fim** | É ele que cria os órfãos, e os dois chamadores (R01 e R03) enviam mensagens logo depois. Cada um fica cerca de 1 a 2 s mais lento |
| 4 | **O `waitLockWaiters` e o limite da DLQ de teste não mudam** | Aumentar o prazo não resolve: a rodada lenta do item 3 da §1 quebrou pela DLQ |
| 5 | **Nada muda no código de produção** | O comportamento já está documentado (messaging §4.5). A spec só acrescenta à limitação que a entrega ao órfão conta como recebimento para o `maxReceiveCount` (10 em produção, D-12) |

---

## 3. Testes

| Teste | Onde | Prova |
| --- | --- | --- |
| `TestOrphanWait` (novo, unitário, sem tag) | `test/testkit/harness_internal_test.go` | Parada há 500 ms com wait de 2 s → 1,7 s; parada há 3 s → 0; nenhuma parada (`time.Time{}`) → 0. **Red:** com `orphanWait` em stub retornando 0, o primeiro caso falha na asserção |
| R03 `TestGracefulShutdownSQS` (existente) | `test/e2e/resilience_test.go` | 40 rodadas seguidas (`-count=40`) sem falha, contra 3 falhas em 28 antes da correção |

**Pronto quando:**
1. O red do `TestOrphanWait` foi visto com a mensagem esperada.
2. `make check` verde.
3. R03 com `-count=40` verde.
4. `make test-e2e` e `make test-integration` verdes, com o compose inteiro de pé.

---

## 4. Documentos

- [`messaging.md`](../../messaging.md) §4.5 e o item correspondente de [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) §16: a entrega ao poll órfão conta como recebimento.
- [`test-plan.md`](../../test-plan.md), linha do R03: as mensagens só são enviadas depois de os polls órfãos expirarem.
- No fim, o [diário](../diary.md) registra a causa e a correção.
