# M12 — Teste de carga: design

**Data:** 30/09/2026 · **Caminho:** *architectural* curto ([`development-workflow.md`](../../development-workflow.md) §2): o gerador de carga é um componente novo, mas nenhum código de produção muda · **Status:** aprovada pelo autor em 30/09/2026 ("prossiga"), com a exceção da decisão 8; executada (ajustes abaixo)

**Ajustes da execução** (valem sobre o texto desta spec; plano: [`dev/plans/2026-09-30-m12-load-test.md`](../plans/2026-09-30-m12-load-test.md)):
1. **O `setup()` espera a outbox drenar antes de abrir a janela.** Sem isso, os eventos da abertura das carteiras entrariam no delta da métrica de atraso, mas não no SQL, que filtra por `occurred_at`. Com a drenagem, as duas contagens medem o mesmo conjunto, e as execuções confirmaram que elas batem.
2. **O `LOAD_END` é o instante do `handleSummary()`**, depois da drenagem e da reconciliação. A reconciliação não grava eventos.
3. **A sabotagem da drenagem (§6) é `drain(0)` no `teardown()`.** Exigir `pending === -1` derrubaria primeiro a drenagem do `setup()` (ajuste 1). A detecção do zero é provada pela execução positiva, com a drenagem acima de 2 s (duas leituras zeradas, 1 s entre elas).
4. **Resultado `network_error`, além de `timeout`:** um status 0 sem o código de timeout do k6 (1050) é conexão recusada ou reiniciada.
5. **O serviço `k6` roda com o UID/GID de quem chama** (`LOAD_UID`/`LOAD_GID`), que é o risco 3 da §7 resolvido já de saída.
6. **Um `setup()` que falha imprime "setup failed, nothing was measured"**, no lugar de um resumo com latência 0 e "error rate 0.00% (ok)", que pareceria uma aprovação.
7. **O script não recria `.local/load`; só apaga os 3 arquivos de saída.** Recriar o diretório que serve de origem a um bind mount deixa o Docker Desktop com uma montagem defasada, e o k6 não consegue gravar a saída. Numa reprodução isolada, foram 15 falhas em 20 com `rm -rf` + `mkdir` e 0 em 30 com o diretório estável. No fluxo real, com o `teardown()` rápido, foi 1 falha em 3 antes da correção e 0 em 5 depois.
8. **A sonda das APIs do k6 2.3.0 (risco 1) passou em todos os itens**, então o plano B não foi usado. O `exec.test.fail` sai com o código 110, e o *threshold* cruzado, com 99.


- [`implementation-plan.md`](../../implementation-plan.md) M12, só o primeiro item: o teste de carga;
- [`test-plan.md`](../../test-plan.md) §9, cuja metodologia esta spec fecha;
- [`delivery-requirements.md`](../../delivery-requirements.md) TST-L01 ⭐.

**Fica fora, por decisão do autor (30/09):**
- o **tracing com OpenTelemetry** (OBS-05), porque o custo não compensa (decisão 9);
- o **dashboard**.

Esta spec registra só o **delta** em relação a `docs/`. Não há Go novo: o gerador é um script k6. Por isso o processo é o da exceção nova do §4.4 (decisão 8), aprovada pelo autor junto com a spec.

---

## 1. Objetivo e critério de pronto

**Objetivo:** medir as 3 réplicas do compose sob uma carga reproduzível, com a mistura do test-plan §9, e provar que a carga não quebra a consistência: nenhuma carteira diverge do ledger e nenhum evento fica sem publicar.

**Pronto quando** (evidência no chat, [`development-workflow.md`](../../development-workflow.md) §5):
1. `make load-test` sai com código 0 na execução canônica (`RATE=200`, `DURATION=60s`, `WALLETS=1000`, ou a taxa menor da decisão 7) com o compose recém-subido. O resumo impresso tem todos os números da §5.
2. **Checagem de sensibilidade** dos portões (§6) feita e registrada no plano.
3. `make check` verde. O `.golangci.yml` muda (decisão 10), e o `make check` é a validação dele pelo §4.4.
4. `make test-integration` e `make test-e2e` verdes. Nada do produto muda, mas o compose ganha um serviço, e a verificação final do workflow pede as duas suítes.
5. `docs/load-test.md` escrito com o comando, o ambiente, a metodologia, os resultados da execução canônica e a tabela de taxas (decisão 7).
6. TST-L01 marcado em `delivery-requirements.md`. OBS-05 fica registrado como cortado. `ARCHITECTURE.md` §17, `implementation-plan.md` M12, `README.md` (§9 e §11) e `dev/diary.md` atualizados.

**Fora do escopo:**
- otimizar o sistema a partir dos resultados: um gargalo encontrado vai para o relatório e para as limitações, não vira correção neste marco;
- carga pelo SQS, porque o test-plan §9 fala em throughput e latência de requisição, o que é HTTP;
- execução no CI, porque os números dependem da máquina.

---

## 2. Achados da preparação

1. **O `iss` não depende do endereço do Keycloak.** O `KC_HOSTNAME` fixa o `iss` em `http://localhost:8080/realms/pda` ([`dev/spike-keycloak.md`](../spike-keycloak.md)). Por isso, o k6 dentro da rede do compose pode pedir o token em `keycloak:8080`, e a aplicação aceita o token.
2. **O histograma `outbox_publish_lag_seconds` é grosso demais para percentis.** São 12 buckets exponenciais entre 5 ms e 60 s, com razão de cerca de 2,3 entre os limites, e um p95 interpolado poderia errar por um fator de 2. O banco tem o valor exato: `published_at - occurred_at` em `outbox_events`. Os percentis do relatório vêm do SQL, e a métrica entra como contagem e média, conferidas contra o SQL (decisão 5).
3. **As métricas são acumuladas desde a subida do processo,** e cada réplica expõe só os próprios contadores (achado do M11). Os números do relatório são **deltas** (depois − antes), somados nas 3 réplicas.
4. **As regras das referências** ([`transaction-lifecycle.md`](../../transaction-lifecycle.md) §2) permitem uma mistura sem rejeições por acaso:
   - o WIN referencia uma BET `PROCESSED` da mesma rodada, carteira e jogador, e não há limite de WINs por BET;
   - o REFUND exige a BET `PROCESSED`, o mesmo valor e nenhuma compensação anterior.
5. **O token vale 5 min** (`accessTokenLifespan: 300`). A abertura das carteiras, a janela de 60 s e o pós-carga cabem nisso, mas o `teardown()` pede tokens novos, para não depender da duração do `setup()`.
6. **k6 2.3.0** é a versão atual (`grafana/k6:2.3.0`, publicada em 21/09/2026). O guia de migração para a v2 não afeta as APIs usadas aqui:
   - `constant-arrival-rate`, `setup`/`teardown` e `handleSummary`;
   - `http.batch` e `crypto.randomUUID`;
   - `k6/execution` (`exec.test.fail`).
   A primeira tarefa do plano confirma isso com um script mínimo (§7, risco 1).

---

## 3. Decisões desta spec

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **k6 em Docker, como serviço `k6` do compose com `profiles: [load]`**, imagem `grafana/k6:2.3.0`. O serviço monta `./test/load` (somente leitura) e `./.local/load` (saída) e carrega o `.env.example`/`.env` para ler os secrets dos clients | Escolha do autor. Não exige instalação local, a versão fica fixada como as outras imagens, e o serviço já está na rede do compose (achado 1). O profile impede que o `docker compose up` o inicie |
| 2 | **Modelo aberto:** executor `constant-arrival-rate` com `RATE` (padrão 200 iterações/s; uma iteração = uma requisição), `DURATION` (padrão `60s`) e VUs pré-alocados suficientes para a taxa (`preAllocatedVUs = RATE`, `maxVUs = 2 × RATE`). Cada iteração vai para uma réplica, em rodízio pelo número da iteração | Escolha do autor. A latência não é mascarada pela espera do cliente (sem *coordinated omission*). Quando o sistema não sustenta a taxa, o k6 registra `dropped_iterations`, e isso entra no relatório |
| 3 | **Mistura por sorteio em cada iteração**, numa carteira sorteada entre as `WALLETS`. Cada VU guarda as BETs `PROCESSED` que enviou: <br>- **BET (70%):** de 1,00 a 5,00, numa rodada nova; <br>- **WIN (25%):** de 0,50 a 10,00, referenciando uma BET do VU, na carteira e na rodada dela. Sem BET guardada, o WIN vai sem referência para uma carteira sorteada; <br>- **REFUND (5%):** tira uma BET da lista do VU e a compensa com o mesmo valor. Sem BET guardada, a iteração vira BET. <br>As carteiras abrem com 10.000,00 | Achado 4. Como cada BET é compensada no máximo uma vez, e só pelo próprio VU, nenhuma resposta 422, 409 ou 202 é esperada. Com 12.000 operações em 1.000 carteiras (cerca de 12 por carteira), o saldo nunca acaba. A mistura real por tipo é contada e relatada, porque as alternativas ("sem BET guardada") a deslocam um pouco no começo da janela |
| 4 | **Identificadores:** `externalTransactionId = load-<runId>-<vu>-<iter>`, com `runId` gerado no `setup()` (instante em base 36); `Idempotency-Key = provider-a:<externalTransactionId>`; `playerId = crypto.randomUUID()`; `roundId = round-<externalTransactionId da BET>`; `gameId = load-test` | Repetir o teste não colide com as chaves de uma execução anterior, e as carteiras de cada execução são novas (a abertura é única por jogador e moeda) |
| 5 | **Medição:** <br>- **Latência e throughput:** só as requisições da janela, marcadas com a tag `phase:load`, com submétricas `http_req_duration{phase:load}` e `{phase:load,kind:BET\|WIN\|REFUND}`; p50, p95, p99 e máximo; <br>- **Resultado:** a métrica `load_outcomes`, um contador com a tag `result`: `processed` (200 `PROCESSED`), `http_<status>` ou `timeout`. A métrica `load_errors` é uma *rate* de tudo o que não é `processed`; <br>- **Deltas das métricas:** o `setup()` e o `teardown()` leem o `/metrics` de `app-1..3:9090` e calculam a soma das 3 réplicas para `concurrency_conflicts_total` por `reason`, `wager_transactions_total` por `outcome` e `outbox_publish_lag_seconds_count`/`_sum` (contagem e média); <br>- **Atraso exato da outbox:** depois do k6, o `scripts/load-test.sh` consulta no PostgreSQL o p50, p95, p99 e o máximo de `published_at - occurred_at`, com `percentile_cont`, dos eventos com `occurred_at` dentro da janela | Achados 2 e 3. A janela (início e fim, em UTC) vai do k6 para o script por `.local/load/window.env`, gravado pelo `handleSummary` a partir dos dados do `setup()`. As requisições do `setup()` e do `teardown()` não têm a tag e não contaminam os percentis |
| 6 | **Portões, que tiram o k6 da saída 0:** <br>1. `load_errors` ≥ 1% (*threshold*); <br>2. a outbox não drena: o `teardown()` consulta `outbox_pending_events` nas 3 réplicas a cada 1 s e, se a soma não chegar a 0 em 60 s, chama `exec.test.fail`. Senão, o tempo de drenagem vai para o relatório; <br>3. alguma carteira diverge: depois da drenagem, o `teardown()` chama `POST /wallets/{id}/reconciliation` nas `WALLETS` carteiras, em lotes de 50 (`http.batch`). Qualquer status diferente de 200 ou `consistent=false` chama `exec.test.fail` com o id da carteira. <br>A latência **não** é portão | Escolha do autor: a carga vira evidência de correção. A latência depende da máquina, então só é medida. O limite de 1% tolera um 503 por `lock_timeout` sob contenção, que é a semântica correta da D-04, mas reprova uma degradação real. Os erros aparecem no relatório pelo status de qualquer forma |
| 7 | **Relatório (`docs/load-test.md`):** <br>- comando; <br>- ambiente: CPU, memória, SO, versão do Docker e recursos do Docker Desktop; <br>- versões das imagens e configuração relevante: `DB_MAX_CONNS`, `DB_LOCK_TIMEOUT` e intervalos da outbox; <br>- metodologia (decisões 2–6); <br>- resultados da execução canônica; <br>- uma **tabela de taxas** com 3 execuções (100, 200 e 400 req/s), cada uma com throughput, p95, p99, erros e `dropped_iterations`, mostrando onde o sistema satura; <br>- leitura dos resultados e limitações. Se a execução canônica de 200 req/s já perder iterações, a taxa canônica cai para a maior taxa da tabela sem `dropped_iterations`, e isso fica registrado | Pedido do test-plan §9, mais o mínimo para que os números signifiquem algo fora desta máquina |
| 8 | **Exceção nova no §4.4 do workflow:** `test/load/*.js` (scripts k6), validados por: <br>- uma execução curta (`DURATION=5s RATE=20`); <br>- a execução canônica; <br>- a checagem de sensibilidade dos portões (§6). <br>O `scripts/load-test.sh` já está coberto pela exceção de `scripts/*.sh` (M11), e a validação dele é a mesma execução. O compose e o `Makefile` já são exceções | O script é ferramenta de teste em JavaScript, sem Go para testar com `go test`. A execução real e a sabotagem dos portões são a prova de que ele mede e reprova o que diz. Aprovada pelo autor em 30/09/2026, como pede o §4.4 |
| 9 | **OpenTelemetry fica fora** e vai para o `ARCHITECTURE.md` §17, com o esboço: <br>- `otelhttp` na borda e `otelpgx` no pool; <br>- o contexto de trace gravado na outbox e propagado como atributo da mensagem no SNS e no SQS; <br>- exporter OTLP para um Jaeger no compose. <br>O dashboard também fica fora | Escolha do autor ("só se não adicionar muito trabalho"). A versão mínima pede dependências, um módulo Fx, config, testes com exporter em memória e documentação, cerca de 2 a 3 h com o fluxo. Sem a propagação pela mensageria, o trace terminaria na requisição HTTP, justo onde o sistema é mais simples |
| 10 | **A exceção do `forbidigo` para `test/load/` sai do `.golangci.yml` e do [`stack.md`](../../stack.md) §4.2.** O [`structure.md`](../../structure.md) passa a listar `test/load/wager.js` e `scripts/load-test.sh` e perde o `scripts/dlq-redrive.sh`, que não existe desde o M10 (D-12, limitação 17) | Não há Go em `test/load/`, e uma exceção sem alvo só confunde. A linha do `dlq-redrive.sh` é uma divergência antiga, corrigida aqui porque a árvore de `scripts/` muda de qualquer jeito |

---

## 4. Componentes

**`docker-compose.yml`:** um serviço novo.

```yaml
  k6:
    image: grafana/k6:2.3.0
    profiles: [load]
    <<: *env-files                  # PROVIDER_A_SECRET e WALLET_SERVICE_SECRET
    environment:
      RATE: ${RATE:-200}
      DURATION: ${DURATION:-60s}
      WALLETS: ${WALLETS:-1000}
    volumes:
      - ./test/load:/scripts:ro
      - ./.local/load:/out
    command: ["run", "/scripts/wager.js"]
```

O serviço não declara `depends_on`: o script exige o compose de pé e checa o `/health/ready` das 3 réplicas antes de começar. O `env_file` vem da âncora `x-env-files`, que o compose já define.

**`test/load/wager.js`** (k6):
- **`options`:** o cenário `load` da decisão 2 e os *thresholds* (`load_errors: rate<0.01`), além de submétricas vazias (`http_req_duration{phase:load,kind:…}`) declaradas para aparecerem no resumo;
- **`setup()`:**
  1. confere o `/health/ready` de `app-1..3:8080` e falha antes da carga se alguma réplica não estiver pronta;
  2. pede os tokens;
  3. abre as `WALLETS` carteiras em lotes de 50 (`http.batch`), exigindo 201 em todas;
  4. lê o "antes" das métricas;
  5. devolve as carteiras, os tokens, o `runId`, o "antes" e o início da janela;
- **função padrão:** uma operação da decisão 3, com a tag `phase:load` e a tag `kind`, que conta o resultado em `load_outcomes` e `load_errors`;
- **`teardown()`:** grava o fim da janela, depois executa a drenagem, o "depois" das métricas e a reconciliação da decisão 6, e registra os deltas e o tempo de drenagem em métricas próprias (`load_outbox_drain_ms` e contadores de deltas) para o `handleSummary`;
- **`handleSummary(data)`:** imprime o resumo textual próprio (§5) no stdout e grava `/out/summary.json` (dados brutos) e `/out/window.env` (`LOAD_START`/`LOAD_END`, em UTC, RFC 3339).

**`scripts/load-test.sh`:**
1. cria `.local/load/` e apaga a saída anterior;
2. roda `docker compose --profile load run --rm k6` e repassa `RATE`, `DURATION` e `WALLETS`;
3. lê `.local/load/window.env`;
4. consulta a outbox:

```sh
docker compose exec -T postgres psql -U postgres -d pda -At -c "
  SELECT count(*),
         percentile_cont(ARRAY[0.5,0.95,0.99]) WITHIN GROUP (ORDER BY lag),
         max(lag)
  FROM (SELECT extract(epoch FROM published_at - occurred_at) * 1000 AS lag
        FROM outbox_events
        WHERE occurred_at BETWEEN '$LOAD_START' AND '$LOAD_END') l"
```

5. imprime o bloco do atraso da outbox e sai com o código do k6. Um k6 reprovado ainda imprime o que mediu.

**`Makefile`:**

```make
# Load test (test-plan §9) against the running compose: make up first.
load-test:
	scripts/load-test.sh
```

O alvo não depende do `up`: o teste mede o ambiente que está de pé, e o `setup()` recusa um ambiente que não está pronto.

---

## 5. Resumo impresso

```
pda load test — run <runId>  rate=200/s duration=60s wallets=1000
requests      12000 sent · 199.8/s achieved · dropped 0
mix           BET 8391 (69.9%) · WIN 3013 (25.1%) · REFUND 596 (5.0%)
latency (ms)  p50 … · p95 … · p99 … · max …   (and per kind)
outcomes      processed 11998 · http_503 2 · error rate 0.02%
conflicts     lock_timeout … · unique_race …   (delta, 3 replicas)
wagers        processed … · rejected … · failed …   (delta)
outbox        drained in … ms · lag metric: … events, mean … ms
consistency   1000/1000 wallets reconciled, 0 divergent
--- scripts/load-test.sh
outbox lag    … events · p50 … · p95 … · p99 … · max … ms   (SQL, exact)
```

O formato acima é o alvo. Os números são ilustrativos.

---

## 6. Checagem de sensibilidade

Pelo [`development-workflow.md`](../../development-workflow.md) §4.3, cada portão é sabotado, a execução curta (`DURATION=5s RATE=20 WALLETS=50`) precisa sair com código ≠ 0 e a sabotagem é desfeita:

| Portão | Sabotagem | Esperado |
| --- | --- | --- |
| Reconciliação | Conferir `consistent === false` no lugar de `true` | `exec.test.fail` com o id da primeira carteira, saída ≠ 0 |
| Drenagem | Exigir a soma de `outbox_pending_events` igual a `-1` | Falha depois de 60 s com "outbox not drained", saída ≠ 0 |
| Taxa de erros | Contar `processed` como erro | *Threshold* `load_errors` cruzado, saída ≠ 0 |
| Atraso da outbox (script) | Janela sem eventos (`LOAD_END` = `LOAD_START`) | O script imprime `0 events` e sai com código ≠ 0. Uma janela vazia é erro de medição, não um atraso zero |

Cada resultado vai para o plano no lugar do `// Sensitivity: …`, que não cabe em JavaScript como em Go, com o comando executado e a mensagem de falha observada.

---

## 7. Riscos

| # | Risco | Mitigação |
| --- | --- | --- |
| 1 | Uma API do k6 2.3.0 não se comporta como na documentação: `data.setup_data` no `handleSummary`, `exec.test.fail` no `teardown()` ou métricas do `teardown()` no resumo | A primeira tarefa do plano roda um script mínimo que exercita os três pontos, antes de escrever o gerador. Plano B: o `teardown()` imprime uma linha `LOAD_WINDOW …` no stdout, que o script lê do log, e o portão vira um contador com *threshold* `count==0` |
| 2 | O k6 e as 3 réplicas disputam CPU na mesma máquina, e a medida inclui o custo do gerador | Limitação declarada no relatório, com os recursos do Docker. A tabela de taxas mostra onde a máquina satura |
| 3 | A escrita em `./.local/load` pelo usuário não-root da imagem do k6 (uid 12345) falha no bind mount | O script cria o diretório antes. Se a escrita falhar, o serviço roda com `user: "${UID}:${GID}"` (ajuste do plano) |
| 4 | Com 400 req/s, a reconciliação de 1.000 carteiras ou a drenagem passam do token de 5 min | O `teardown()` pede tokens próprios (achado 5), e a drenagem tem um limite de 60 s |
| 5 | Os 503 por `lock_timeout` passam de 1% numa taxa alta e reprovam a execução | Na tabela de taxas, uma execução reprovada é relatada como está (é um ponto de saturação). Só a execução canônica precisa sair com código 0 |

---

## 8. Documentos afetados

**Nesta etapa** (decisão nova, [`development-workflow.md`](../../development-workflow.md) §3.1):
- [`decisions.md`](../../decisions.md): D-21 nova;
- [`test-plan.md`](../../test-plan.md) §9: a metodologia;
- [`development-workflow.md`](../../development-workflow.md) §4.4: a exceção da decisão 8;
- [`structure.md`](../../structure.md): a árvore de `test/load/` e `scripts/`;
- [`stack.md`](../../stack.md): a imagem `grafana/k6` em §2.2, o `make load-test` em §5, a exceção do `forbidigo` fora de §4.2 e a linha do OpenTelemetry em §7.

**No fim do marco**, com os números: `docs/load-test.md`, `delivery-requirements.md` (TST-L01 e OBS-05), `ARCHITECTURE.md` §17, `implementation-plan.md` M12, `README.md` (§9 e §11), `docs/testing.md` (seção curta que aponta para o `load-test.md`) e `dev/diary.md`.
