# M9 — Resiliência: design

**Data:** 30/09/2026 · **Caminho:** *bounded* ([`development-workflow.md`](../../development-workflow.md) §2), com spec e plano curtos · **Status:** aprovada pelo autor em 30/09/2026 ("prossiga")

**Ajustes da execução** (valem sobre o texto desta spec):
1. **Sensibilidade do R02:** a sabotagem "`MarkPublished` no caminho de falha" não é detectada. Com o broker congelado, o `Publish` que venceu o prazo no cliente fica no buffer do socket e é entregue depois do `unpause`. A execução viu 12 falhas reais nos logs, e mesmo assim todos os eventos chegaram à auditoria. Foi trocada por "backoff sem teto (1 h)", que é detectada. Registrado no `ARCHITECTURE.md` §16.
2. **Condições que chamam helpers com `t`** usam um `within(t, d, what, cond)` local, no lugar do `testkit.Eventually`, por causa do `contextcheck`.
3. **`make infra-up`:** faz `unpause` por serviço, porque `unpause a b` falha por inteiro, e espera 3 s depois de um `unpause` real, porque o Docker marca o container pausado como *unhealthy* e o `--wait` desiste.
4. **R03:**
   - usa `inboxNow` (contagem imediata), porque o `inboxOutcomes` espera todas as mensagens;
   - envia as 30 mensagens intercaladas por carteira: em bloco, o primeiro lote tinha só um grupo e o MiniStack não entregou os outros ao segundo poller.
5. **R01:** a parada do gerador de tráfego roda num `defer`. Um teste que falhava no meio deixava a goroutine chamando `t` depois do fim.

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M9;
- [`test-plan.md`](../../test-plan.md) §5.5 (R01–R04) e a última linha do §3.4 (`docker compose pause|unpause`);
- [`delivery-requirements.md`](../../delivery-requirements.md): F6 (§0.1), a parte de F4 que falta (R03), FX-04 e a prova de SQS-07 e SQS-09 com 3 processos.

Esta spec registra só o **delta** em relação a `docs/` e ao harness do M8 ([spec](2026-09-30-m8-e2e-harness-design.md)). Os fluxos já existem, então os testes R passam pela checagem de sensibilidade (§4.3 do workflow). A exceção é a mudança de produto da decisão 1, que nasce com um red real.

---

## 1. Objetivo e critério de pronto

**Objetivo:** provar no cluster de 3 processos que uma indisponibilidade temporária do PostgreSQL ou do MiniStack e um `SIGTERM` com trabalho em andamento não produzem movimentação duplicada, saldo negativo, mensagem na DLQ nem evento perdido.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-e2e` verde, com cada R visto falhando pela sabotagem da §5 e o `// Sensitivity: …` registrado.
3. `make test-integration` verde: a decisão 1 muda a borda HTTP e a `Config`, que os testes em processo usam.
4. FX-04 marcado e F6 com evidência em `delivery-requirements.md`, citando os testes. `ARCHITECTURE.md` (§12, §14, §16), `implementation-plan.md` e `dev/diary.md` atualizados.

**Fora do escopo:** qualquer outra mudança de comportamento do produto; teste de carga (M12); o README e o `docs/testing.md` (M10), que vão citar o cuidado da decisão 5.

---

## 2. Achados da preparação

1. **O HTTP não tem prazo.** Um `docker compose pause` congela o processo do PostgreSQL, mas o kernel do container continua aceitando e mantendo as conexões TCP. Uma query fica pendurada até o `ctx` terminar.
   - O `DB_LOCK_TIMEOUT` é aplicado pelo servidor, então não ajuda com o banco congelado.
   - O `WriteTimeout` de 30 s não cancela o contexto da requisição.
   - Resultado: durante a queda, o `POST /wagering/transactions` esperaria o `unpause` e responderia 200, em vez do 503 da D-04. O R01 falharia, e com razão.
   - O consumidor não tem o problema, porque já usa o `SQS_PROCESSING_TIMEOUT`. O `/health/ready` também não, porque usa 2 s por dependência.
2. **A pausa por saúde é por instância.** Cada instância só fecha a sua depois de sofrer o próprio erro transitório. Numa queda geral, a mesma mensagem pode ser recebida uma vez por instância antes de todas pausarem: a instância A falha e pausa, a mensagem volta depois do backoff e a instância B a recebe, e assim por diante.
   - Com 3 réplicas e `maxReceiveCount = 10`, sobra margem. Com o `maxReceiveCount = 3` dos testes e 3 consumidores, a mensagem pode chegar à DLQ na volta do banco.
   - O R01 isola o consumidor numa instância (decisão 3), e o `messaging.md` §4.3 passa a registrar a regra: o `maxReceiveCount` precisa superar com folga o número de instâncias consumidoras.

---

## 3. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **Prazo por requisição HTTP:** `HTTP_REQUEST_TIMEOUT`, com padrão de 10 s, 10 s na integração e 5 s no e2e. Um middleware aplica o prazo a toda rota autenticada (`roles != nil`), por fora do `authenticate`. O `context.DeadlineExceeded` já é transitório pela D-05, então vira 503 `TEMPORARILY_UNAVAILABLE` com `Retry-After: 1`, sem outra mudança. O `Validate` exige `DB_LOCK_TIMEOUT < HTTP_REQUEST_TIMEOUT < 30 s` (o `WriteTimeout`). As rotas públicas ficam de fora: o health já tem 2 s por dependência, e o docs é estático | Achado 1; escolha do autor. O padrão de 10 s é simétrico ao `SQS_PROCESSING_TIMEOUT`. Ser maior que o lock timeout preserva o 503 por lock timeout e a métrica `concurrency_conflicts_total{reason="lock_timeout"}`. Um commit em voo quando o prazo vence tem resultado desconhecido (D-14), e o reenvio com a mesma chave cai no replay |
| 2 | **Os testes R ficam em `test/e2e/resilience_test.go`, sem `t.Parallel()`**, sobre o cluster do pacote | Pausar o PostgreSQL ou o MiniStack do compose derruba qualquer teste simultâneo. O Go roda os testes sequenciais antes de retomar os paralelos, e o `make test-e2e` usa `-p 1`, então só o próprio teste é afetado |
| 3 | **No R01, o consumidor fica ligado só na instância 0** (bisturi do M8, decisão 5), e o HTTP continua nas 3. A queda dura **15 s** | Com um só consumidor, "a pausa segura as mensagens" vira uma asserção precisa, sem o encadeamento do achado 2. A duração cobre a primeira falha (3 s de prazo + 1 s de ping) e deixa uma janela de mais de um ciclo de reentrega (3 s + 2 s de backoff) com o consumidor pausado, que é onde a sabotagem da §5 aparece |
| 4 | **R03 e R04 usam uma barreira no banco:** o teste trava as carteiras (`FOR UPDATE` numa transação do `Owner()`), espera o trabalho bloquear no lock (`waitLockWaiters`, do C10b), envia o `SIGTERM` sem esperar, espera no log da instância a linha de parada do componente (`sqs consumer stopping` / `http server stopping`) e só então solta o lock | "Em andamento" passa a ser determinístico, e não uma questão de sorte com o tempo de processamento. Para não correr contra o lock timeout de 2 s, a instância parada sobe com `DB_LOCK_TIMEOUT=4s` (e, no R03, `SQS_PROCESSING_TIMEOUT=4s`, abaixo do visibility de 5 s) |
| 5 | **`testkit.Pause(tb, service) (resume func())`** roda `docker compose pause <service>` na raiz do repositório. Registra no `t.Cleanup` um `unpause` idempotente, e o `resume` devolvido pode ser chamado antes. O `make infra-up` passa a rodar `docker compose unpause postgres ministack` (ignorando erro) antes do `up`. O `test-plan` §3.4 recebe esse cuidado, e o `docs/testing.md` (M10) vai citá-lo | Um `panic` por timeout do `go test` pula os `Cleanup`, e um PostgreSQL congelado travaria a próxima execução e o ambiente de desenvolvimento. O `Makefile` é exceção aprovada do TDD, validada pelo `make check` e pela execução |
| 6 | **`Cluster.StopAsync(i) <-chan error`**: o `SIGTERM` e a espera do `terminate` numa goroutine, com o mesmo critério do `Stop` (código 0 dentro do `stopTimeout`). O `Stop` passa a usá-lo | A barreira da decisão 4 precisa soltar o lock **enquanto** o processo para. O `Stop` bloqueia e chama `tb.Fatalf`, que não pode ser chamado de outra goroutine |
| 7 | **No R02, o backlog é afirmado por `outbox_pending_events`**, que é um gauge de valor inteiro, lido com `MetricValue`. A idade do evento mais antigo é conferida por SQL | O `outbox_oldest_pending_age_seconds` é fracionário, e o `MetricValue` devolve `int64` para não abrir exceção no `forbidigo` do E3. O `test-plan` §5.5 é ajustado |

---

## 4. Testes

Todos em `test/e2e/resilience_test.go`. Cada um começa com `defer cluster.Restore(t)`, e toda carteira passa pelo `AssertWalletConsistent` no `Cleanup` (test-plan §6).

| ID | Teste | Procedimento | Asserções |
| --- | --- | --- | --- |
| R01 | `TestPostgresOutage` | Consumidor só na instância 0. 5 carteiras. Um gerador envia BETs de 1.00 por HTTP (round-robin nas 3 instâncias, chave única por operação) e por SQS (`messageId` único) a cada ~100 ms, antes, durante e depois da queda. `Pause("postgres")` por 15 s, depois `resume` | **Durante a queda:** <br>- as respostas HTTP que chegam entre 1 s depois do `pause` e o `resume` são todas 503 (o 1 s exclui uma requisição que terminou o trabalho no banco antes da queda) `TEMPORARILY_UNAVAILABLE` com `Retry-After: 1`, e há pelo menos uma; <br>- o `/health/ready` das 3 instâncias responde 503; <br>- a instância 0 registra `sqs consumer paused`, e o seu `sqs_messages_received_total`, lido 3 s depois disso, não cresce até o fim da queda. <br>**Depois:** <br>- toda operação HTTP que recebeu 503 é reenviada com a mesma chave e termina 200 (nova ou replay); <br>- toda mensagem tem exatamente 1 linha `PROCESSED` na inbox; <br>- a fila drena e a DLQ fica vazia; <br>- o saldo de cada carteira é o inicial − 1.00 × (operações), com 1 lançamento por operação |
| R02 | `TestSQSOutage` | 3 carteiras. `Pause("ministack")` por 10 s. Durante a queda, BETs pelo HTTP (o teste não usa o SQS enquanto o MiniStack está pausado) | **Durante a queda:** <br>- o `/health/ready` das 3 instâncias responde 503; <br>- as BETs respondem 200 `PROCESSED`; <br>- a soma de `outbox_pending_events` nas instâncias fica > 0, e a idade do evento pendente mais antigo, lida por SQL, cresce. <br>**Depois:** <br>- as instâncias voltam a ficar prontas; <br>- nenhum evento das carteiras fica sem `published_at`; <br>- todo evento chega à auditoria com o conteúdo do banco (item 8 do §6) |
| R03 | `TestGracefulShutdownSQS` | Consumidor só na instância 0 (com a decisão 4). 3 carteiras travadas, 30 mensagens (10 por carteira, grupos = carteiras). Depois de 3 sessões esperando o lock, `StopAsync(0)`, espera por `sqs consumer stopping`, e o lock é solto. Depois da saída, o consumidor é religado na instância 1 | - A instância 0 sai com código 0 em menos de `SHUTDOWN_TIMEOUT` (5 s), contados do `SIGTERM`; <br>- os logs de parada seguem a ordem HTTP → consumidor → pool; <br>- **logo depois da saída, a inbox tem exatamente 3 linhas `PROCESSED`**: as 3 mensagens que estavam em andamento terminaram, e nenhuma outra começou; <br>- depois do religamento, as 30 mensagens estão `PROCESSED` exatamente uma vez, a fila drena e a DLQ fica vazia, e cada carteira tem 10 débitos |
| R04 | `TestGracefulShutdownHTTP` | Instância 0 com a decisão 4. 1 carteira travada, 5 BETs de 1.00 enviadas à instância 0 em goroutines. Depois de 5 sessões esperando o lock, `StopAsync(0)` e espera por `http server stopping`. Uma requisição nova à instância 0, numa conexão nova. Depois disso, o lock é solto | - A requisição nova falha com `connection refused` (`syscall.ECONNREFUSED`), e a operação dela não existe no banco; <br>- as 5 requisições em andamento respondem 200 `PROCESSED`; <br>- a instância sai com código 0 em menos de `SHUTDOWN_TIMEOUT`; <br>- o saldo é 95.00, com versão 6 |

**Testes novos de unidade (red real da decisão 1):**
- `TestEdgeRequestDeadline` (`httpapi`, com um stub de caso de uso que bloqueia até o `ctx` terminar): uma rota autenticada responde 503 `TEMPORARILY_UNAVAILABLE` com `Retry-After: 1` depois do prazo; o health não recebe o prazo.
- Casos novos em `TestValidate_RejectsInvalidValues` (`config`): `HTTP_REQUEST_TIMEOUT` ≤ `DB_LOCK_TIMEOUT` e ≥ 30 s são recusados.
- O `TestEnvOf` do `testkit` já cobre a variável nova, porque percorre todas as tags `env:`.

---

## 5. Sensibilidade

| Teste | Sabotagem | Falha esperada |
| --- | --- | --- |
| R01 | O middleware da decisão 1 desligado | Nenhuma resposta 503 na janela: as requisições esperam e respondem 200 depois do `unpause` |
| R01 | `healthGate.Report` sem efeito (a pausa nunca fecha) | O `sqs_messages_received_total` da instância 0 cresce durante a queda |
| R02 | O readiness sem o checker do SQS | O `/health/ready` responde 200 durante a queda |
| R02 | O caminho de falha do publisher confirmando o evento (`MarkPublished` em vez de `MarkFailed`) | Um evento publicado zero vezes: o item 8 do §6 falha |
| R03 | O `Stop` do consumidor cancelando o trabalho na hora (`abortWork` antes da espera) | A inbox tem 0 linhas depois da saída, em vez de 3 |
| R03 | As mensagens recebidas e não iniciadas apagadas no shutdown, em vez de liberadas | Nem todas as 30 mensagens são processadas |
| R04 | `srv.Close()` no lugar de `srv.Shutdown` | As requisições em andamento falham sem resposta |

---

## 6. Documentos atingidos

- **Agora, junto com esta spec:**
  - `decisions.md`: D-04 (prazo por requisição), D-12 (a regra do `maxReceiveCount`) e D-19 (o delta do M9);
  - `test-plan.md`: §3.3 (a linha `HTTP_REQUEST_TIMEOUT`), §3.4 (o cuidado do `pause`) e §5.5 (os procedimentos ajustados);
  - `messaging.md` §4.3: a pausa por instância e a regra do `maxReceiveCount`.
- **No fecho do marco:**
  - `ARCHITECTURE.md`: §12 (shutdown provado), §14 (o 503 por prazo) e §16 (a pausa por instância);
  - `delivery-requirements.md`: FX-04, F6, SQS-07 e SQS-09;
  - `implementation-plan.md` e `dev/diary.md`.

---

## 7. Riscos

| Risco | Mitigação |
| --- | --- |
| Um prazo de 5 s no e2e gera um 503 espúrio sob carga com `-race` | O R01 e o R02 reenviam os 503 com a mesma chave; os outros testes e2e não dependem de latência. Se aparecer, a coluna E2E sobe para 8 s, ainda abaixo da queda de 15 s |
| O `pause` deixa o PostgreSQL congelado depois de um `panic` | Decisão 5: `Cleanup` idempotente e `unpause` no `infra-up` |
| A barreira do R03/R04 corre contra o lock timeout | Decisão 4: `DB_LOCK_TIMEOUT=4s` na instância parada; a liberação depende só de uma linha de log |
| O long poll órfão (messaging §4.5) esconde por 5 s uma mensagem liberada no R03 | A espera do religamento usa `Eventually` com folga acima de um visibility timeout |
| O tempo do `make test-e2e` cresce | São cerca de 2 min a mais (15 s + 10 s de queda, as recuperações e 2 paradas), dentro do limite de 15 min |
