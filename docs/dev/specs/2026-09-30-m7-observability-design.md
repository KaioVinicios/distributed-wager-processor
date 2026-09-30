# M7 — Observabilidade e papéis do processo: design

**Data:** 30/09/2026 · **Caminho:** *architectural* ([`development-workflow.md`](../../development-workflow.md) §2) · **Status:** aprovada pelo autor em 30/09/2026 ("prossiga"), com os ajustes do plano abaixo

**Implementa:**
- [`implementation-plan.md`](../../implementation-plan.md) M7;
- D-18 (observabilidade) e D-15 (flags de papel, "entregues juntas no M7");
- o catálogo de métricas do [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) §13.2 e do [`messaging.md`](../../messaging.md) §8;
- [`test-plan.md`](../../test-plan.md): I13, I14 e I07a–c.

Esta spec registra só o **delta** em relação a `docs/`. O servidor admin `:9090`, o logger JSON, o health e 19 métricas já existem desde o M0–M6.

**Ajustes feitos no plano** (valem sobre o texto desta spec): (1) o método das duplicatas do `app.Metrics` é `WagerDuplicate`; (2) sem helper `LogAttrs` (decisão 11): as chaves são literais `camelCase`, conferidas pelo `sloglint` e pelo I14; (3) o log do consumidor mantém `sqsMessageId` (id do broker), e o `messageId` do envelope aparece na linha `wager concluded`; (4) os testes novos são I25 (`TestMetricsEndpoint`), I26 (`TestConcurrencyConflictMetric`) e I27 (`TestFxRoles`); (5) o 503 real com o PostgreSQL parado é do R01 (M9), e o I13 do M7 é o teste unitário mais o `TestFxRoles`. (6) os papéis não entram na `Config`: `config.RolesFromEnv` os lê antes do Fx, `fx.Supply(roles)` os entrega ao grafo e o `logRoles` faz o log de início (registrado na [revisão do M7](2026-09-30-m7-review-fixes-design.md)).

---

## 1. Objetivo e critério de pronto

**Objetivo:** fechar OBS-01..04 e FX-01, FX-04 e FX-05 com evidência:
1. o catálogo de métricas completo (§13.2), com os resultados por status, as duplicatas, os conflitos de concorrência, as reconciliações, as falhas de autenticação e o tráfego HTTP;
2. logs JSON com `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId` em todo evento que tenha esses dados, e a prova de que nenhum segredo nem payload completo é registrado;
3. os papéis do processo (HTTP, consumidor, publisher, worker) habilitáveis por env;
4. a prova da ordem de parada (HTTP → consumidor → publisher → worker → pool e AWS).

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make check` verde.
2. `make test-integration` verde, com os testes da §7 vistos falhando pelo motivo certo e depois passando; os testes escritos sobre comportamento que já existe registram `// Sensitivity: …`.
3. `docker compose up --build --wait` com as 3 réplicas saudáveis. Por `curl`, depois de um fluxo (abrir carteira → BET → replay → reconciliação → um 401), `:9091/metrics` mostra `wager_transactions_total`, `http_requests_total`, `auth_failures_total` e `reconciliation_runs_total` com valores coerentes; `docker compose logs app-1` mostra as linhas JSON com os IDs e sem token.
4. Requisitos da §8 marcados em [`delivery-requirements.md`](../../delivery-requirements.md), citando os testes.
5. `docs/` e `ARCHITECTURE.md` refletem as decisões da §2.

**Fora do escopo:** tracing e dashboards (OBS-05, M12); o harness e2e e o `faultinject` (M8); os testes R (M9); a prova de shutdown com 3 processos e tráfego (R03 e R04, M9).

---

## 2. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **`/health/ready` continua com PostgreSQL + SQS, sempre**, independentemente dos papéis ligados | O HTTP-08 fixa esse contrato e o OBS-04 aponta para ele. Nenhum requisito prevê readiness por papel, e os dois clientes continuam no grafo de qualquer instância. Limitação registrada: com `HTTP_ENABLED=false` não há rotas de health, porque elas vivem no `httpapi` |
| 2 | **Papéis:** `HTTP_ENABLED`, `CONSUMER_ENABLED`, `OUTBOX_PUBLISHER_ENABLED` e `REFERENCE_WORKER_ENABLED`, todos `true` por padrão. `bootstrap.OptionsFor(config.Roles)` monta o grafo sem os módulos desligados, e `Options()` é `OptionsFor(config.RolesFromEnv())`. `RolesFromEnv` lê só as quatro variáveis, antes do Fx, e um valor que não seja booleano é erro. A validação completa da `Config` continua no Fx (FX-02). As flags também entram na `Config` (mesmas variáveis), para o `Validate` e o log de início | Excluir um módulo exige conhecer os papéis antes de montar o grafo (D-15). `OptionsFor` deixa os testes escolherem os papéis sem mexer no ambiente. O servidor admin e a `observability` ficam sempre |
| 3 | **Todos os papéis desligados é válido** (só o admin sobe). O `bootstrap` registra um `WARN` no start | Simples para diagnóstico; recusar a combinação seria uma regra sem requisito |
| 4 | **Porta `app.Metrics`** ganha: `WagerConcluded(channel, kind, outcome, failureCode string, d time.Duration)`, `Duplicate(channel, layer string)`, `Conflict(reason string)` e `Reconciled(consistent bool)`. O `ProcessWager.Execute` conta uma vez por chamada: `wager_transactions_total` e `wager_processing_duration_seconds` em toda conclusão nova, `Duplicate("http"\|"sqs", "idempotency")` no replay e `Conflict("unique_race")` em cada corrida da idempotência ou da reversão que reroda. Como o consumidor já conta `wager_duplicates_total{channel=sqs}` (M5), o app só conta as duplicatas do canal HTTP, para não contar em dobro | O `app` continua sem importar Prometheus (D-18). Cada evento é contado no lugar que o conhece |
| 5 | **`outcome`** de `wager_transactions_total` = `processed`, `rejected`, `pending_reference` ou `failed`; `failure_code` só do catálogo (`OPS-15`) e vazio quando não há; `kind` = tipo da operação; `channel` = `http` ou `sqs`. O resultado do worker de referências também entra (`channel="worker"`), porque é ele que conclui as pendências | Cardinalidade limitada por enums fechados do domínio. Sem o canal do worker, as pendências resolvidas sumiriam da contagem de resultados |
| 6 | **`concurrency_conflicts_total{reason}`** com `lock_timeout` e `unique_race`. O `version_mismatch` do §13.2 sai: a estratégia é pessimista (D-09) e nenhum caminho o produz. Para contar o `lock_timeout`, o adapter `postgres` passa a envolver `55P03` e `40P01` na sentinela `app.ErrLockTimeout` (transitória), e o `ProcessWager` e o `ResolveReferences` a contam | Documentar uma label que nunca é emitida seria métrica falsa. O deadlock (`40P01`) entra junto porque, para o operador, é o mesmo sintoma: disputa de lock |
| 7 | **`reconciliation_runs_total{consistent}`** no `Reconcile`, ao lado do contador de divergências que já existe | ARCHITECTURE §13.2 |
| 8 | **Métricas HTTP no `httpapi`:** `http_requests_total{route,method,status}` e `http_request_duration_seconds{route,method,status}`, medidas no `logAccess` (que já conhece rota, método e status). O `route` é o **padrão** do `ServeMux` (`POST /wallets/{walletId}/reconciliation`), e `unmatched` para o que não casa, nunca o path bruto. A porta `httpapi.Metrics` é do adapter, e o `observability.Metrics` a implementa | Cardinalidade fixa. O log de acesso passa a gravar o mesmo `route` (fecha o minor do M3: `route` vazio em rota inexistente) |
| 9 | **`auth_failures_total{reason}`** com `unauthenticated` (sem token, malformado, assinatura inválida, expirado, `aud` errado), `forbidden` (role insuficiente) e `provider_mismatch`, contados no `authenticate` e no ponto onde o `PROVIDER_MISMATCH` é decidido. A resposta ao cliente não muda | Diagnóstico de acesso sem vazar o motivo fino (AUTH-02) |
| 10 | **Servidor admin:** continua servindo só `GET /metrics`. `GET /health/*` não entra na porta admin | O contrato de health é o do HTTP-08, na porta da API |
| 11 | **Chaves de log únicas:** `correlationId`, `messageId`, `transactionId`, `walletId`, `providerId`, sempre em `camelCase`. Um helper `observability.LogAttrs` (funções `WithWager`, `WithMessage`…) monta os atributos, para que o nome de cada chave exista num só lugar. O lint `sloglint` já força `camelCase` | OBS-01 exige as cinco chaves; centralizar evita `wallet_id` num arquivo e `walletId` em outro |
| 12 | **Uma linha por conclusão** no `ProcessWager` (`INFO "wager concluded"`): `transactionId`, `walletId`, `providerId`, `correlationId`, `messageId` (quando vem do SQS), `channel`, `kind`, `outcome`, `failureCode` e `replay`. Nunca `amount`, saldo, chave de idempotência nem payload. Para o SQS, o `messageId` é o do envelope (`InboxReceipt.MessageID`); os logs do consumidor que hoje usam `sqsMessageId` passam a usar `messageId` com o id do **envelope** e `sqsMessageId` só para o id do broker, quando for útil | Hoje só o log de acesso e dois logs do `app` têm IDs. Uma linha por conclusão cobre HTTP, SQS e worker com o mesmo formato |
| 13 | **Divergência da reconciliação:** o `WARN` deixa de registrar os três saldos (`storedBalance`, `calculatedBalance`, `difference`) e registra só `walletId`, `correlationId` e `entries`. Os valores continuam na resposta ao chamador autorizado | OBS-02 pede "sem payloads financeiros completos". Fecha a pendência que o M3 deixou para o I14 |
| 14 | **Log de erro sem valores:** os `error` registrados são o texto do erro classificado (`apperrors`), que não carrega linhas do banco (M2, "erros do banco sanitizados"). O I14 prova isso com um fluxo que passa por 401, 422, 409 e uma falha do banco | Garante que a política vale em todos os caminhos, não só no feliz |
| 15 | **FX-04 e FX-05 em processo:** o `TestFxLifecycle` (I07b) passa a registrar a **ordem** dos `OnStop` (HTTP, consumidor, publisher, worker, pool) por meio dos logs de parada e afirma que o pool só fecha depois de todos. O comportamento sob carga (30 mensagens, requisições em voo) continua sendo o R03 e o R04 (M9) | Fecha o que dá para provar sem o harness de processos |

---

## 3. `config` e `bootstrap`

- `config.Roles{HTTP, Consumer, OutboxPublisher, ReferenceWorker bool}` e `config.RolesFromEnv() (Roles, error)`. As quatro variáveis entram também na `Config`, com os mesmos padrões e nomes, para o `Validate` e o log de início `roles resolved`.
- `bootstrap.OptionsFor(Roles)`: a lista atual de `Options()`, sem `httpapi.Module`, `sqsconsumer.Module`, `outbox.Module` e `references.Module` quando o papel está desligado. `Options()` chama `RolesFromEnv`; se ele falhar, devolve uma opção `fx.Error(err)`, que aborta o start com a mensagem que nomeia a variável.
- `cmd/pda/main.go` não muda.
- **Consequência para o grafo:** o `appModule` continua provendo todos os casos de uso (construtores preguiçosos: o Fx só instancia o que alguém pede). O `auth.Module` (busca inicial do JWKS) só faz sentido com o HTTP ligado, então entra junto com o `httpapi.Module`.

## 4. `observability`

- `Metrics` ganha os campos e métodos da decisão 4 e das decisões 6–9 (a parte do `httpapi` e a do `auth`). `NewRegistry` deixa de dizer "arrives in M7".
- `logattrs.go`: as constantes de chave e os helpers da decisão 11.
- A `observability` continua sem importar `app`, `httpapi` nem adapters: as portas pertencem a quem as consome.

## 5. `app`, `httpapi` e adapters (mudanças cirúrgicas)

- `app.Metrics` (decisão 4), `app.ErrLockTimeout` (decisão 6), `ProcessWager` e `ResolveReferences` chamam a porta; o `Reconcile` chama `Reconciled`. O `nopMetrics` dos testes existentes ganha os métodos novos.
- `postgres/errors.go`: `55P03` e `40P01` viram `app.ErrLockTimeout` (mantendo `KindTransient`).
- `httpapi`: `logAccess` passa a receber a porta `Metrics`; `authenticate` conta as falhas; o log usa o `route` do padrão do mux.
- `sqsconsumer` e `references`: só a troca de `sqsMessageId` por `messageId` (decisão 12); as métricas deles não mudam.

## 6. Requisitos de segurança dos logs

- Nunca: `Authorization`, o token (nem trecho), `Idempotency-Key`, corpo da requisição, `amount`, saldo, `DATABASE_URL`, chaves AWS.
- Sempre: os IDs da decisão 11, quando existirem.
- O I14 procura essas marcas nos logs capturados, incluindo o valor exato do token e do `amount` usados no fluxo.

---

## 7. Testes

| ID | Teste | Prova |
| --- | --- | --- |
| U20 | `TestMetrics_Wagers`, `TestMetrics_HTTP`, `TestMetrics_Auth`, `TestMetrics_Reconciliation` (`observability`) | Cada coletor conta o que promete, com as labels do catálogo; `promlinter` valida os nomes |
| U21 | `TestRolesFromEnv`, `TestOptionsFor` (`config`, `bootstrap`) | Padrões `true`; valor inválido nomeia a variável; cada papel desligado tira o módulo do grafo (`fx.ValidateApp` sem o tipo) |
| U22 | `TestTranslateLockTimeout` (`postgres`) | `55P03` e `40P01` viram `app.ErrLockTimeout` e continuam `KindTransient` |
| U23 | `TestProcessWagerMetrics` (`app`) | Conclusão nova, replay e corrida contam uma vez, com `channel` e `outcome` certos; o replay do SQS não conta em dobro |
| I07a | `TestFxGraph` (existente) | Continua verde com todos os papéis; ganha um subteste por papel desligado |
| I07b | `TestFxLifecycle` (existente) | Passa a afirmar a ordem de parada (decisão 15) e `goleak` |
| I07c | `TestFxFailFast` (existente) | Ganha o caso de `HTTP_ENABLED=talvez` |
| I13 | `TestHealthChecks` | `/health/live` 200; `/health/ready` 200 e 503 com o PostgreSQL pausado; **com o consumidor desligado, o ready continua checando o SQS** (decisão 1) |
| I14 | `TestLogsHaveIdsWithoutSecrets` | Fluxo completo (abrir carteira, BET por HTTP, BET pelo SQS, REFUND antes da BET, replay, reconciliação, 401, 422, 409): todo log é JSON; as linhas de conclusão têm os IDs; nada contém o token, o `amount`, a `Idempotency-Key` nem o corpo |
| I24 | `TestMetricsEndpoint` | Depois do fluxo do I14, `/metrics` na porta admin traz os valores esperados de `wager_transactions_total`, `wager_duplicates_total`, `auth_failures_total`, `http_requests_total` (com `route` como padrão, sem IDs) e `reconciliation_runs_total`; `/metrics` não existe na porta da API |
| I25 | `TestConcurrencyConflictMetric` | Uma disputa forçada por `lock_timeout` incrementa `concurrency_conflicts_total{reason="lock_timeout"}` |

Testes escritos sobre comportamento que já existe (I13, ordem do I07b, log de acesso) passam pela checagem de sensibilidade.

---

## 8. Requisitos e documentos atingidos

- **Requisitos:** OBS-01, OBS-02, OBS-03 e OBS-04 (fechados); FX-01, FX-03, FX-04 (em processo) e FX-05; TST-I07. O R03 e o R04 (M9) completam FX-04 sob carga.
- **Documentos:**
  - `decisions.md`: D-15 (papéis entregues, `OptionsFor`) e D-18 (`version_mismatch` removida, conclusão por linha, reconciliação sem saldos, `/health/ready` sempre com PostgreSQL + SQS);
  - `ARCHITECTURE.md` §13.1–§13.3 e a interpretação 12 da §15;
  - `messaging.md` §8 e `structure.md` §3 (`OptionsFor`, `logattrs.go`);
  - `test-plan.md` (linhas U20–U23, I24, I25 e o que mudou em I07/I13/I14);
  - `delivery-requirements.md` e `diary.md` (fecho do marco).

## 9. Riscos

| Risco | Mitigação |
| --- | --- |
| Contar em dobro entre o `ProcessWager` e o consumidor | Decisão 4 (o app só conta `channel=http` nas duplicatas) e o U23 |
| Cardinalidade da label `route` | Decisão 8: padrão do mux, `unmatched` para o resto, com teste |
| Log de erro vazando dados | Decisões 13 e 14 e o I14 com valores marcadores (token, `amount`, chave) |
| Papel desligado deixa o grafo com tipo faltando | U21 valida o grafo de cada combinação |
