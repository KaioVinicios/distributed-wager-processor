# Teste de carga

Carga reproduzível nas 3 réplicas do compose, com a mistura do [`test-plan.md`](test-plan.md) §9, e a prova de que a carga não quebra a consistência. Decisão: D-21 em [`decisions.md`](decisions.md). Spec e plano: [`dev/specs/2026-09-30-m12-load-test-design.md`](dev/specs/2026-09-30-m12-load-test-design.md) · [`dev/plans/2026-09-30-m12-load-test.md`](dev/plans/2026-09-30-m12-load-test.md).

---

## 1. Como rodar

```sh
docker compose up --build -d --wait           # as 3 réplicas de pé
make load-test                                # 200 req/s por 60 s, 1.000 carteiras
RATE=400 DURATION=60s WALLETS=1000 make load-test
RATE=20 DURATION=5s WALLETS=50 make load-test # execução curta, para conferir o ambiente
```

| Variável | Padrão | Significado |
| --- | --- | --- |
| `RATE` | `200` | Requisições por segundo (modelo aberto: uma iteração = uma requisição) |
| `DURATION` | `60s` | Duração da janela medida |
| `WALLETS` | `1000` | Carteiras novas abertas no `setup()`, com 10.000,00 BRL cada |

- **O que roda:**
  - o `make load-test` chama o [`scripts/load-test.sh`](../scripts/load-test.sh);
  - o script roda o serviço `k6` do compose (`grafana/k6:2.3.0`, profile `load`) com o [`test/load/wager.js`](../test/load/wager.js);
  - depois, o script mede no PostgreSQL o atraso exato da outbox na janela da carga.
- **Saída:**
  - o resumo vai para o terminal;
  - o `.local/load/` (ignorado pelo git) guarda o `summary.txt`, o `summary.json` (dados brutos do k6, sem o token) e o `window.env` (início e fim da janela).
- **Código de saída diferente de zero** quando:
  - a taxa de erros passa de 1% (*threshold* do k6, saída 99);
  - a outbox não drena em 60 s depois da carga (saída 110);
  - alguma carteira não reconcilia (saída 110);
  - a janela não tem eventos ou tem eventos sem publicar (saída 1, do script);
  - o ambiente não está pronto: o `setup()` recusa antes de abrir carteiras (saída 107).

  A latência não reprova a execução: ela é medida e relatada.
- **Efeito no ambiente:** cada execução cria `WALLETS` carteiras e as transações da carga no banco `pda` de desenvolvimento. `docker compose down -v` zera tudo.

---

## 2. Metodologia

- **Modelo aberto.** O executor `constant-arrival-rate` dispara `RATE` requisições por segundo, independentemente das respostas. A latência não é mascarada pela espera do cliente (*coordinated omission*). Quando o sistema não acompanha, o k6 registra `dropped_iterations`.
- **Réplicas em rodízio.** Cada iteração vai para `app-1`, `app-2` ou `app-3`, pelo número da iteração. O k6 roda dentro da rede do compose e pede o token ao Keycloak pelo endereço interno: o `iss` é fixado pelo `KC_HOSTNAME`, então a aplicação o aceita.
- **Mistura sem rejeições por acaso.** A cada iteração, uma carteira é sorteada entre as `WALLETS`:
  - **70% BET** de 1,00 a 5,00, numa rodada nova;
  - **25% WIN** de 0,50 a 10,00, referenciando uma BET já processada pelo mesmo VU, na carteira e na rodada dela. Sem BET guardada, o WIN vai sem referência;
  - **5% REFUND** de uma BET do mesmo VU ainda sem compensação, com o mesmo valor. Sem BET guardada, a iteração vira BET.

  Como só o próprio VU usa as suas BETs, nenhuma referência fica pendente nem é compensada duas vezes, e o saldo nunca acaba. Por isso **só 200 `PROCESSED` é sucesso**, e qualquer outra resposta é erro.
- **Janela limpa:**
  - o `setup()` abre as carteiras e espera a outbox drenar antes de abrir a janela, para que os eventos da abertura não entrem na medida;
  - só as requisições da janela levam a tag `phase:load`, de onde saem throughput e percentis.
- **Contadores do servidor.** O k6 lê o `/metrics` das 3 réplicas antes e depois da carga e relata a diferença somada. Cada réplica expõe só os próprios contadores. Os contadores relatados:
  - `concurrency_conflicts_total` por motivo;
  - `wager_transactions_total` por resultado;
  - a contagem e a média de `outbox_publish_lag_seconds`.
- **Atraso da outbox exato.** Os 12 buckets do histograma `outbox_publish_lag_seconds` (5 ms a 60 s) são grossos demais para percentis. O script calcula p50, p95, p99 e máximo de `published_at - occurred_at` em `outbox_events`, por SQL, nos eventos da janela. A contagem do SQL e a da métrica precisam bater.
- **Portões de correção, depois da janela:**
  1. a outbox drena: duas leituras zeradas de `outbox_pending_events`, com 1 s entre elas, em até 60 s;
  2. todas as carteiras passam por `POST /wallets/{id}/reconciliation` e precisam responder `consistent: true`;
  3. a taxa de erros fica abaixo de 1%.
- **O que não é medido:** o caminho SQS, porque o test-plan pede throughput e latência de requisição. Também não há um ambiente dedicado: ver as limitações na §6.
- **Validação do próprio teste.** Cada portão foi sabotado numa execução curta e reprovou a execução (saídas 110, 110, 99 e 1). Os registros estão no plano do M12.

---

## 3. Ambiente

Medido em 30/09/2026, sobre o commit `c7a3640` com o teste de carga do M12. Nenhum código de produção mudou no M12.

| Item | Valor |
| --- | --- |
| Máquina | MacBook, Apple M1 (8 núcleos), 8 GiB de RAM, macOS 26.6.2 |
| Docker | Docker Desktop, engine 29.7.2. A VM tem **4 CPUs e 3,8 GiB**, divididos por todos os containers abaixo |
| Imagens | `pda:local` (Go 1.27.1, 3 réplicas), `postgres:18.6-alpine`, `quay.io/keycloak/keycloak:26.7.4`, `ministackorg/ministack:1.5.18`, `grafana/k6:2.3.0` |
| Réplicas | Padrões da `Config` ([`README.md`](../README.md) §4.3): `DB_MAX_CONNS` 10 por réplica, `DB_LOCK_TIMEOUT` 5 s, `HTTP_REQUEST_TIMEOUT` 10 s, `OUTBOX_POLL_INTERVAL` 500 ms, `OUTBOX_BATCH_SIZE` 50, `OUTBOX_CONCURRENCY` 8 e `LOG_LEVEL` `info`. As 3 réplicas rodam os 4 papéis |
| Estado inicial | Cada taxa rodou num compose recém-subido (`docker compose down -v` e `up --build -d --wait`) |

---

## 4. Resultados

**Execução canônica: 100 req/s.** A spec previa 200 req/s. Pela regra dela (decisão 7), a canônica passa a ser a maior taxa da tabela sem iterações perdidas, e a execução de 200 req/s perdeu 24. Saída completa, com a linha final do script:

```text
pda load test — run muowjwv6  rate=100/s duration=60s wallets=1000
requests      6001 sent · 100.0/s achieved · dropped 0
mix           BET 4220 (70.3%) · WIN 1494 (24.9%) · REFUND 287 (4.8%)
latency (ms)  all     p50 3.8 · p95 14.3 · p99 98.2 · max 253.3
              BET     p50 3.7 · p95 15.3 · p99 110.0 · max 253.3
              WIN     p50 3.9 · p95 13.5 · p99 76.8 · max 202.6
              REFUND  p50 3.7 · p95 9.3 · p99 29.7 · max 215.0
outcomes      processed 6001 · error rate 0.00% (< 1%: ok)
conflicts     lock_timeout 0 · unique_race 0   (delta, 3 replicas)
wagers        processed 6001 · rejected 0 · failed 0 · pending_reference 0   (delta)
outbox        drained in 3088 ms · lag metric: 12002 events, mean 266.5 ms
consistency   1000/1000 wallets reconciled, 0 divergent
outbox lag    12002 events · p50 253.6 · p95 508.4 · p99 600.1 · max 803.1 ms   (SQL, exact)
```

**Taxas.** Uma execução por taxa, 60 s, 1.000 carteiras:

| `RATE` | Enviadas (/s) | `dropped` | p50 / p95 / p99 / máx. (ms) | Erros | Conflitos | Atraso da outbox p50 / p99 (SQL) | Drenagem depois da carga | Resultado |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 100 | 6.001 (100,0) | 0 | 3,8 / 14,3 / 98,2 / 253 | 0 | 0 | 254 ms / 600 ms | 3,1 s | ✅ saída 0 |
| 200 | 11.977 (199,6) | 24 | 9,5 / 301 / 919 / 1.261 | 0 | 0 | 14,0 s / 36,4 s | 40,4 s | ✅ saída 0 |
| 400 | 17.546 (292,4) | 6.455 | 408 / 4.586 / 6.403 / 6.903 | 0 | 0 | 54,1 s / 81,8 s¹ | não drenou em 60 s | ❌ portão da drenagem (saída 1) |

¹ Só dos eventos já publicados quando o script consultou o banco: 11.441 dos 35.092 eventos da janela ainda estavam na outbox.

**Em todas as taxas, inclusive a de 400 req/s:**
- 0 erros e 0 rejeições: toda requisição enviada terminou em 200 `PROCESSED`;
- 0 conflitos (`lock_timeout` e `unique_race`);
- 1.000 de 1.000 carteiras consistentes na reconciliação.

**Conferências cruzadas, todas fechadas:**
- os `processed` do k6 são iguais ao delta de `wager_transactions_total{outcome="processed"}`;
- com a outbox drenada, a contagem do SQL é igual à da métrica `outbox_publish_lag_seconds`, com 2 eventos por operação;
- a mistura soma o total enviado.

---

## 5. Leitura

- **Até 100 req/s, o sistema sobra.**
  - O caminho HTTP responde em 3,8 ms na mediana e 98 ms no p99.
  - A outbox publica tudo em menos de 1 s. O atraso mediano de ~250 ms é metade do `OUTBOX_POLL_INTERVAL` (500 ms): em carga baixa, o atraso é dominado pelo intervalo de varredura, não pela publicação.
- **O primeiro limite é a publicação da outbox**, de cerca de 240 eventos/s nesta máquina: na execução de 200 req/s, os 23.954 eventos foram publicados em ~100 s (60 s de carga e 40 s de drenagem). Cada operação gera 2 eventos (`WagerTransactionProcessed` e `WalletBalanceChanged`), então acima de ~120 operações/s a outbox acumula.
  - **A 200 req/s:** são 400 eventos/s contra ~240 publicados, ou ~160 eventos/s acumulados, ~9.600 em 60 s. Drenar isso a ~240/s leva ~40 s, e a drenagem medida foi de 40,4 s. O atraso p99 chega a 36 s, mas todos os eventos são publicados.
  - **A 400 req/s:** o acúmulo passa de 20 mil eventos e não drena nos 60 s do portão.
- **O gargalo da publicação é o MiniStack.** Uma execução de observação a 200 req/s, com amostras do `docker stats` a cada 5 s:
  - o container do MiniStack ficou em 95–98% de CPU, o teto de um processo, durante toda a carga e toda a drenagem;
  - as 3 réplicas ficaram abaixo de 30% cada;
  - o PostgreSQL chegou a ~2,5 CPUs.

  A amostragem pesa na máquina: essa execução perdeu 394 iterações e teve p95 de 1,9 s. Por isso ela serve só para atribuir o gargalo, e não entra na tabela. Numa AWS real, a vazão de publicação dependeria dos limites do SNS FIFO, e não de um emulador de um processo. O desenho também pesa: o publisher envia um evento por chamada (`Publish`), e o envio em lote (`PublishBatch`, até 10 mensagens por chamada) seria o primeiro ajuste. O ganho não foi medido.
- **A cauda da latência HTTP cresce com a máquina saturada.** A mediana continua baixa a 200 req/s (9,5 ms), mas o p95 vai a 0,3 s e o p99 a 0,9 s. Nesse ponto, os 4 CPUs da VM estão divididos entre o PostgreSQL, o MiniStack e o próprio k6. A 400 req/s, o tempo de resposta sobe para segundos e o k6 esgota os 800 VUs: as 6.455 iterações perdidas são requisições que o gerador não conseguiu iniciar, não respostas de erro.
- **Contenção por carteira não aparece.** Com 1.000 carteiras sorteadas e locks curtos, nenhuma espera chegou ao `DB_LOCK_TIMEOUT` de 5 s.
- **A correção não depende da carga.** Mesmo com a máquina saturada, o saldo de toda carteira bateu com o ledger, nenhuma operação foi rejeitada ou duplicada, e a outbox só atrasou: nenhum evento foi perdido nas execuções que drenaram. A execução de 400 req/s foi reprovada pelo portão de drenagem, que existe justamente para tornar esse atraso visível.

---

## 6. Limitações

- **Gerador e sistema na mesma máquina.** O k6, as 3 réplicas, o PostgreSQL, o Keycloak e o MiniStack dividem as 4 CPUs da VM do Docker Desktop. O custo do gerador entra na medida, e a saturação é da máquina inteira, não de um componente isolado.
- **MiniStack é um emulador.** A latência de publicação no SNS e a entrega na fila de auditoria não representam a AWS.
- **Uma execução por taxa**, sem repetição nem intervalo de confiança. Os números mostram ordem de grandeza e o ponto de saturação, não metas de produção.
- **O banco cresce a cada execução.** Para comparar taxas, cada execução da §4 rodou num compose recém-subido (`docker compose down -v`).
- **O teste não roda no CI**, porque o resultado depende da máquina.
