# Diário de desenvolvimento

Anotações curtas do autor: o que foi feito em cada sessão e onde o trabalho parou. Os detalhes ficam nas specs, nos planos e nos commits.

---

## 28/09/2026 (seg): documentação e planejamento

- Leitura do `CHALLENGE.md` e redação de toda a documentação de sistema em `docs/`:
  - requisitos de entrega, decisões D-01 a D-20, modelo de dados, ciclo de vida das transações e mensageria;
  - plano de testes, estrutura, stack, fluxo de desenvolvimento e plano de implementação M0–M12.
- `ARCHITECTURE.md` criado como documento vivo, revisado ao fim de cada marco.
- Regras de qualidade definidas: spec → plano → TDD → verificação por marco, com commits só com a autorização do autor.
- Revisão geral de todos os documentos contra o `CHALLENGE.md` e commit inicial em 13 commits atômicos.

## 28–29/09/2026 (seg–ter): M0, esqueleto, qualidade e infraestrutura

**Spikes** (`docs/dev/spike-*.md`):
- **MiniStack 1.5.18:** toda a topologia funciona (SQS FIFO, redrive, visibilidade, SNS FIFO → SQS FIFO com raw delivery). Com `AUTH=true`, as políticas IAM são **avaliadas**. O autor decidiu aplicá-las (D-02): usuários IAM com políticas de identidade e chaves geradas pelo `aws-init` em `.local/aws/credentials`.
- **Keycloak 26.7.4:** `iss` estável com `KC_HOSTNAME` + backchannel dinâmico, e verificação via JWKS direto, sem discovery. O mapper padrão `audience resolve` foi removido do realm (D-07).
- **Lint:** golangci-lint v2.14.0 funciona com Go 1.27.1, e o `forbidigo` captura `float`. O literal inferido (`x := 1.5`) fica para o U01g.

**Esqueleto** ([spec](specs/2026-09-28-m0-skeleton-design.md) → [plano](plans/2026-09-28-m0-skeleton.md) → execução com TDD):
- **Módulos Fx:** `config`, `observability`, `postgres`, `aws` e `httpapi`, com `/health/live`, `/health/ready` (PostgreSQL + SQS) e `/metrics` na porta admin.
- **`pda healthcheck`:** a sonda usada pelo compose, porque a imagem distroless não tem shell nem `curl`.
- **Compose:** postgres, keycloak (realms `pda` e `other`), ministack com `AUTH=true`, `aws-init` idempotente (reaproveita as chaves) e `app-1..3` saudáveis.
- **Testes:**
  - unitários em todos os pacotes;
  - integração: I07a–c e `TestProvisioning`, com checagem de sensibilidade por sabotagem;
  - o `goleak` revelou que os clientes AWS não fechavam conexões no shutdown, e isso foi corrigido.
- **Verificação do zero:** `make check`, `make test-integration` e `docker compose up --build --wait` verdes.
- **Requisitos:** ART-01..04, 06, 07, 10, FX-02, HTTP-08 e OBS-04 marcados; FX-01 e AUTH-09 parciais.
- **Minors adiados:**
  - servidor HTTP que morre depois do start não encerra o processo;
  - réplicas sem `restart:` no compose;
  - logs do Fx em nível INFO.

## 29/09/2026 (ter): M1, domínio com TDD

- [Spec](specs/2026-09-29-m1-domain-design.md) → [plano](plans/2026-09-29-m1-domain.md) → execução com TDD.
- **Abordagem A:** o domínio decide e aplica (`wagering.Settle` e `wagering.OpenWallet`); o `app` do M3 só fará I/O.
- **Pacotes:** `ident`, `money`, `wallet`, `events`, `wagering` e `apperrors`, com a tabela U do test-plan (exceto o U09b, do M2).
- **Decisões novas** (em `decisions.md`): erro não classificado é transitório; chave de idempotência em ASCII visível; IDs do domínio como string canônica; `eventId` atribuído no `Seal`.
- **Achado na validação do plano:** relógio de outra instância atrás do `createdAt` viraria falha permanente; `updatedAt` passou a ter `createdAt` como piso.

## 29/09/2026 (ter): M2, persistência

- [Spec](specs/2026-09-29-m2-persistence-design.md) → [plano](plans/2026-09-29-m2-persistence.md) → execução com TDD.
- **Entregue:**
  - migrations 000001–000006 com todas as constraints, triggers e grants do data-model;
  - portas em `internal/app`, UoW (`Do` e `Snapshot`) e os 5 repositórios;
  - tradução de erros sem vazar o `Detail` do PostgreSQL;
  - serviço `migrate` no compose.
- **Decisões novas** (spec §2, `decisions.md` D-09 e D-14):
  - corridas de unicidade como sentinelas transitórias;
  - `lock_timeout` por `set_config` na transação inteira;
  - saldo observado na moeda da carteira;
  - ID não canônico → "não encontrado";
  - UoW interrompida pelo `ctx` → transitória.
- **Achados da validação do plano:**
  - o `result_balance_minor` numa rejeição `CURRENCY_MISMATCH` voltaria na moeda errada;
  - o `JSONB` normaliza o payload da outbox;
  - os triggers disparam antes dos `CHECK`, por isso o I02a roda com eles desligados.
- **Prova central:** o I17 grava todos os tipos (abertura, BET, WIN, LOSS, REFUND, ROLLBACK, rejeições e pendência resolvida) pelos repositórios, e o domínio do M1 e o schema concordam em tudo.

## 29/09/2026 (ter): M3, contrato, casos de uso, HTTP e autenticação

- [Spec](specs/2026-09-29-m3-contract-http-auth-design.md) (com o [`api/openapi.yaml`](../../api/openapi.yaml)) → [plano](plans/2026-09-29-m3-contract-http-auth.md) → execução inline com TDD.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano. Depois, o plano foi reaplicado do zero numa segunda cópia, passo a passo (red → green); o resultado ficou idêntico ao validado, e a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - os casos de uso `OpenWallet`, `ProcessWager` (com `FAILED` em UoW separada e retentativa das corridas), `Queries` e `Reconcile`;
  - o `auth`, com go-oidc, a matriz D-07 e o fail fast do JWKS;
  - o `httpapi`, com as 9 rotas, `problem+json`, os middlewares e os docs;
  - o `testkit`: app em processo, contrato validado em toda troca e consistência completa do test-plan §6.
- **Achado central:** 50 apostas iguais em paralelo às vezes geravam um 409 indevido, porque uma entrega confirmava entre as duas leituras de idempotência. Foi corrigido no `lookup`, com teste determinístico (decisão 23 da spec).
- **Outros achados da validação:**
  - a ordem dos middlewares;
  - as filas isoladas passam a ser criadas pelo `StartApp`;
  - a regra do `depguard` do `app` exclui os testes;
  - o go-oidc compara o `exp` sem tolerância, então a tolerância virou `OIDC_CLOCK_SKEW`.
- **Prova final:**
  - `make check` e `make test-integration` verdes, com três execuções estáveis;
  - cinco sabotagens detectadas;
  - compose com as 3 réplicas saudáveis e o fluxo por `curl` distribuído entre elas.

## 29/09/2026 (ter): M4, outbox publisher

- [Spec](specs/2026-09-29-m4-outbox-publisher-design.md) (com o [`api/events.yaml`](../../api/events.yaml)) → [plano](plans/2026-09-29-m4-outbox-publisher.md) → execução inline com TDD.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano, inclusive as versões intermediárias do `publisher.go`; a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - a porta `app.OutboxStore` e o `postgres.OutboxStore` (claim com `SKIP LOCKED` + lease, confirmação e falha condicionais, backlog);
  - o `adapters/outbox`: publisher por grupos, backoff sem descarte, reclaim, espera no claim com o banco fora, stop gracioso e módulo Fx;
  - o ARN do tópico resolvido no start (STS + `GetTopicAttributes`) e a política ajustada;
  - as 6 métricas de outbox;
  - o contrato `api/events.yaml`, o coletor da fila de auditoria e o item 8 da consistência.
- **Achado central:** os testes do `bootstrap` usavam o banco compartilhado `pda` e, com o publisher no grafo, publicaram num tópico de teste os eventos pendentes do ambiente de desenvolvimento (12 eventos do fluxo manual do M3 não chegaram à auditoria). Agora têm banco próprio.
- **Prova final:**
  - `make check` e `make test-integration` verdes, e mais duas execuções estáveis;
  - 6 sabotagens detectadas (entre elas publicar antes do commit, o E8);
  - compose com as 3 réplicas publicando, os eventos de um `POST /wallets` na fila de auditoria e o stop gracioso nos logs.

## 29/09/2026 (ter): M5, consumidor SQS

- [Spec](specs/2026-09-29-m5-sqs-consumer-design.md) → [plano](plans/2026-09-29-m5-sqs-consumer.md) → execução inline com TDD.
- **Escolhas do autor na spec:** caso de uso `app.ConsumeWager` no `app` (e não a orquestração no adapter); pausa por saúde acionada por ping; prazo de processamento menor que o visibility, com liberação por prazo e tempos de teste de 5 s/3 s.
- **Validação do plano:** todo o código foi escrito e testado numa cópia descartável antes do plano, inclusive as versões intermediárias do `consumer.go`; a execução no repositório repetiu os mesmos reds e greens.
- **Entregue:**
  - o `ConsumeWager` e a inbox em todo caminho de conclusão do `ProcessWager`;
  - o `adapters/sqsconsumer`: envelope e hash, decisão por resultado, DLQ explícita, backoff, grupos em paralelo com ordem no grupo, pausa por saúde, liberação por prazo e shutdown em 5 passos;
  - as 9 métricas de SQS e o módulo Fx;
  - o `testkit` de SQS e IAM, o I04f com as políticas reais e a ponta a ponta pelo SQS.
- **Achado central:** um long polling cancelado pelo cliente continua aberto no MiniStack e esconde, por um visibility timeout, a próxima mensagem que ficar visível (confirmado com uma sonda). Explica uma falha intermitente do teste de shutdown (limitação documentada) e era a causa do flake do I05b do M4, que também falhava na `main`: corrigido no `Audit.Absent`, com um teste que falha 3 de 3 vezes sem a correção.
- **Outros achados:** o I04a passava sem o `DeleteMessage` (a redrive drenava a fila) e agora exige a DLQ vazia; short polling ganhou pausa; wait e visibility em segundos inteiros; as mensagens seguintes de um grupo com a cabeça sempre falhando também chegam à DLQ (FIFO).
- **Prova final:**
  - `make check` verde e `make test-integration` verde três vezes seguidas;
  - 15 sabotagens detectadas;
  - compose com as 3 réplicas consumindo: um BET enviado como `provider-a` processado pelo SQS, eventos publicados com `causationId = messageId`, JSON quebrado na DLQ e o stop ordenado (HTTP → consumidor → publisher).

## Onde paramos

- **M5 concluído (commits aguardando autorização).** Próximo passo: **M6, worker de referências**, começando pela spec.
- **Pendências em aberto:**
  - confirmar o horário exato da entrega (assumido 01/10);
  - decidir se os 3 minors do M0 entram em algum marco;
  - minors adiados na revisão do M2: inbox aceita instantes zerados; repositórios sobre o pool podem escrever fora do UoW (só a convenção da D-14 impede). O filtro de ID malformado de `List`/`Sum`/`AdvanceDependents` ficou resolvido no M3, pelos casos de uso (decisão 8);
  - minors adiados na revisão do M3: o log de acesso grava `route` vazio para rotas inexistentes; o WARN da reconciliação registra os três saldos (confirmar a política no I14 do M7); o `settleAndPersist` com `insert = false` só ganha teste no M6.
