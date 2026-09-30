# M7 — Correções da revisão: design

**Data:** 30/09/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), com spec e plano curtos · **Status:** escrita a pedido do autor ("escreva a spec toda de uma vez, e já parta direto para o plano"); a aprovação vale para spec e plano juntos, antes da execução

**Origem:** revisão do M7 (`83e19b7..7aac1e3`) feita depois do marco, com o Opus, contra a [spec do M7](2026-09-30-m7-observability-design.md) e o [`delivery-requirements.md`](../../delivery-requirements.md). Dois achados tornam falsas marcações `[x]` feitas no M7 (FX-02 no caminho novo e OBS-01); os demais são de documentação ou de evidência.

Esta spec registra só o **delta** em relação ao M7.

---

## 1. Achados e critério de pronto

| # | Achado | Gravidade | Requisito |
| --- | --- | --- | --- |
| 1 | Com uma variável de papel inválida (`HTTP_ENABLED=talvez`), o processo sai com código 1 **sem nenhuma mensagem**. O ramo de erro de `bootstrap.Options()` devolve `fx.NopLogger`, que descarta o evento `Started{Err}`. O `TestFxFailFast/invalid role variable` lê `app.Err()`, que nenhum operador vê. Reproduzido com o binário | Importante | FX-02 (erro claro no fail fast) |
| 2 | Os logs de falha do consumidor (retry transitório, envio à DLQ, falha no envio, no `DeleteMessage` e no `ChangeMessageVisibility`) trazem só o `sqsMessageId` do broker, embora o envelope já lido tenha `messageId`, `correlationId`, `walletId` e `providerId`. Uma mensagem que falha não se liga à operação pelos logs. Os logs do publisher trazem só o `eventId`, embora o evento tenha o `walletId` (`message_group_id`) e o `correlationId` | Importante | OBS-01 ("quando disponíveis") |
| 3 | O `pda healthcheck` consulta o `/health/ready` da porta da API; com `HTTP_ENABLED=false`, o container fica sempre *unhealthy*. Os documentos só dizem que "não há rotas de health" | Menor | D-15 (documentação) |
| 4 | A spec do M7 (decisão 2 e §3) diz que os papéis também entram na `Config`; a implementação usa só `fx.Supply(roles)`, e o ajuste não está na lista de ajustes da spec | Menor | — (documentação) |
| 5 | O I25 (`TestMetricsEndpoint`) afirma crescimento ≥ 1 em séries que outros testes paralelos também incrementam; é um teste de ligação, e o `test-plan` não diz isso | Menor | OBS-03 (evidência) |
| 6 | O OBS-02 cita "4 sabotagens detectadas"; só 2 são do OBS-02 (header `Authorization` e `amount`) | Menor | OBS-02 (evidência) |
| 7 | O FX-05 fala de "pool do PostgreSQL, clientes"; o `TestFxLifecycle` só afirma o pool. O fechamento dos clientes AWS não gera log nem é afirmado | Menor | FX-05 (evidência) |
| 8 | Uma operação que passa por `PENDING_REFERENCE` conta duas vezes em `wager_transactions_total` (`http`/`sqs` com `pending_reference`, depois `worker` com o desfecho). O §13.2 não diz isso | Menor | OBS-03 (documentação) |

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde.
3. Os testes da §3 vistos falhando pelo motivo certo e depois passando; os escritos sobre comportamento existente com `// Sensitivity: …`.
4. Os documentos da §4 atualizados.

**Fora do escopo:**
- os logs de erro do HTTP (`status.go`), que já trazem o `correlationId` e se ligam ao log de acesso, que tem `route` e `providerId`;
- um healthcheck para instâncias sem HTTP (achado 3 fica como limitação registrada; nenhum marco planejado separa papéis por container);
- o flake do `TestMigrationsUpDownUp` (registrado no diário, fora deste escopo por decisão do autor).

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **O ramo de erro de `Options()` devolve só `fx.Error(err)`**, sem `fx.NopLogger`. Sem `fx.WithLogger` no grafo, o Fx usa o logger de console no stderr, que imprime `[Fx] ERROR Failed to start: config: HTTP_ENABLED: invalid value`. O erro já vem redigido (`redactParseError`), então o valor não aparece | Menor mudança que torna o erro visível; o formato (texto do Fx, não JSON) é o mesmo de qualquer falha antes de existir um `*slog.Logger` |
| 2 | **Prova pelo processo real:** `cmd/pda` ganha um `TestMain` que, com `PDA_TEST_RUN_MAIN=1`, executa `main()`. O teste roda o próprio binário de teste com `HTTP_ENABLED=talvez-42` e afirma código 1, a variável nomeada na saída e o valor ausente | Afirma o que o operador vê, não `app.Err()`. Unitário: o erro acontece antes de qualquer conexão |
| 3 | **O consumidor monta um logger por mensagem:** `c.log.With("sqsMessageId", …)` e, se o envelope foi lido, também `messageId`, `correlationId`, `walletId` e `providerId`. `apply`, `delete` e `changeVisibility` recebem esse logger. O `walletId` e o `providerId` ainda não foram validados pelo caso de uso: entram só se presentes, cortados em 128 caracteres (o mesmo limite do `messageId`); o handler JSON do slog os escapa. Mensagem que não pôde ser lida continua só com o `sqsMessageId` | Um só lugar monta os IDs; todo log de falha de uma mensagem os carrega. O corte evita que um campo gigante de uma mensagem maliciosa infle o log |
| 4 | **O publisher monta um logger por evento:** `p.log.With("eventId", …, "walletId", e.MessageGroupID, "correlationId", e.CorrelationID)`, usado em todos os logs de um evento (falha de publicação, de confirmação, de registro da falha, confirmado por outra instância, lease reassumido). O `message_group_id` é sempre o `walletId` ([`data-model.md`](../../data-model.md) §3.5) | OBS-01 com os dados que o evento já tem |
| 5 | **O fechamento dos clientes AWS gera log** (`aws http client closed`), e o `TestFxLifecycle` afirma que ele vem depois do fim dos workers. A ordem entre o pool e os clientes AWS não é afirmada: nenhum dos dois usa o outro | Torna verdadeira a evidência do FX-05 |
| 6 | **Achados 3–8 viram documentação:** limitação do healthcheck (D-15 e `ARCHITECTURE.md` §15, item 12), ajuste 6 na spec do M7, I25 descrito como teste de ligação, evidência do OBS-02 corrigida e contagem dupla das pendências no §13.2 | Sem código; só o registro fica correto |

---

## 3. Testes

| ID | Teste | Prova |
| --- | --- | --- |
| U24 | `TestMainReportsInvalidRole` (`cmd/pda`) | O processo real com `HTTP_ENABLED=talvez-42` sai com código 1 e imprime `HTTP_ENABLED`, sem `talvez-42` |
| U25 | `TestFailureLogsCarryTheMessageIDs` (`sqsconsumer`, unitário) | Retry transitório e envio à DLQ registram `sqsMessageId`, `messageId`, `correlationId`, `walletId` e `providerId`; uma mensagem ilegível registra só o `sqsMessageId` |
| U26 | `TestPublisherLogsCarryTheEventIDs` (`outbox`, unitário) | Falha de publicação e falha de confirmação registram `eventId`, `walletId` e `correlationId` |
| I07b | `TestFxLifecycle` (existente) | Passa a afirmar `aws http client closed` depois do fim do worker |

---

## 4. Documentos

- `decisions.md` D-15: limitação do healthcheck com `HTTP_ENABLED=false`; D-18: os logs de falha do consumidor e do publisher carregam os IDs.
- `ARCHITECTURE.md`: §13.1 (IDs nos logs de falha do SQS e da outbox; erros HTTP se ligam ao log de acesso pelo `correlationId`); §13.2 (uma pendência conta duas vezes); §15 item 12 (healthcheck).
- `test-plan.md`: U24–U26, I07b e o I25 como teste de ligação.
- `delivery-requirements.md`: FX-02, FX-05, OBS-01 e OBS-02 com a evidência corrigida.
- Spec do M7: ajuste 6 (papéis fora da `Config`, via `fx.Supply`).
- `implementation-plan.md` (M7) e `diary.md`: registro da revisão e das correções.
