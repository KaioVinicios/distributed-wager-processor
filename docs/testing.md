# Testes: preparação e execução

Como preparar as dependências dos testes e executar os testes unitários, a integração, os cenários com **múltiplas instâncias** e as **simulações de falha**, com as build tags de cada nível. A estratégia, os casos e a rastreabilidade até os requisitos estão em [`test-plan.md`](test-plan.md); aqui ficam os comandos. O resumo para quem só quer rodar tudo está no [`README.md`](../README.md) §9.

---

## 1. Níveis e build tags

| Nível | Build tag | Onde ficam | Infraestrutura | Comando |
| --- | --- | --- | --- | --- |
| Unitário | — | `*_test.go` junto ao código | Nenhuma | `go test ./...` · `go test -race ./...` · `make test` |
| Integração | `integration` | `*_integration_test.go` em `internal/adapters/*`, `internal/app`, `internal/bootstrap` e `test/testkit`, e os cenários de `test/integration/` | PostgreSQL, Keycloak e MiniStack do compose; a aplicação roda **em processo**, pelo Fx | `make test-integration` |
| E2E (multi-instância e falhas) | `e2e` | `test/e2e/` | A mesma infraestrutura e **3 processos** do binário, iniciados pelo teste | `make test-e2e` |
| Pontos de falha | `faultinject` | `internal/faultinject/` | — | Não é um nível de teste: é a tag do **binário** que o e2e compila (§5.1) |

- **Sem Docker:** `go test ./...` compila e roda só os unitários, porque os arquivos com infraestrutura têm `//go:build integration` ou `//go:build e2e`. Os comandos do desafio (`go test ./...`, `go test -race ./...`, `go vet ./...`) funcionam num checkout limpo.
- **Tudo com `-race`:** os alvos do `make` usam `-race`, e o binário do e2e também é compilado com `-race`.
- **`go vet` com as tags:** o `make vet` roda `go vet ./...` e `go vet -tags=integration,e2e,faultinject ./...`, para cobrir os arquivos que o `go vet` padrão ignora.

---

## 2. Preparação das dependências

### 2.1 Ferramentas

Docker com Compose v2.24 ou mais novo, Go 1.27.1 e `make` ([`README.md`](../README.md) §1). Os testes usam só `testing` e a biblioteca padrão, mais `goleak` e `kin-openapi`, que vêm do `go.mod`. Nada precisa ser instalado à parte.

### 2.2 Infraestrutura: `make infra-up`

Os alvos `make test-integration` e `make test-e2e` chamam o `make infra-up` antes dos testes. Ele pode ser rodado sozinho, antes de chamar o `go test` direto:

```sh
make infra-up
```

1. **`unpause` de segurança:** tira da pausa o `postgres` e o `ministack`, um por comando, caso um teste de resiliência interrompido os tenha deixado pausados (§5.2).
2. **Sobe e espera** o `postgres`, o `keycloak` e o `ministack` com `docker compose up -d --wait`. O Keycloak leva de 30 a 60 s na primeira vez.
3. **Provisiona:** roda o `aws-init` (filas, tópico, usuários IAM e `.local/aws/credentials`) e o `migrate` até o fim.

As réplicas `app-1..3` **não** são necessárias para os testes. Se estiverem de pé, não interferem, porque os testes usam banco, filas e tópico próprios (§2.4). Só os testes de resiliência afetam as réplicas: enquanto o PostgreSQL ou o MiniStack está pausado, elas respondem 503.

### 2.3 O que os testes leem

- **Endereços fixos no host:** PostgreSQL em `localhost:5432`, Keycloak em `http://localhost:8080` e MiniStack em `http://localhost:4566`, as portas publicadas pelo compose.
- **`.env.example` e `.env`:** o `testkit` lê os mesmos arquivos do compose (o `.env` tem precedência), para obter as senhas de `pda_owner` e `pda_app` e os secrets dos clients do Keycloak.
- **`.local/aws/credentials`:** as chaves dos usuários IAM geradas pelo `aws-init`, usadas pela aplicação em processo e pelos testes de política do broker. Se o arquivo não existir, o teste falha com `open credentials (run make infra-up)`.

### 2.4 Isolamento por pacote

Cada pacote com tag chama `testkit.NewEnv` no seu `TestMain`, e os pacotes rodam em paralelo sem interferir entre si:

- **Banco próprio:** `pda_t_<pacote>_<aleatório>`, criado como `pda_owner`, com as migrations embutidas aplicadas. Isso também exercita as migrations. O isolamento é por banco porque o ledger é append-only e bloqueia `TRUNCATE`.
- **Filas e tópico próprios:** `wager-<aleatório>.fifo`, `wager-dlq-<aleatório>.fifo` (redrive depois de **3** recebimentos, para ser rápido), `events-<aleatório>.fifo` e uma fila de auditoria assinante. São criados pelo app de teste ou pelo `TestMain`.
- **Tempos acelerados:** visibility de 5 s, lease da outbox de 2–3 s, TTL de referência de 3–5 s etc. A tabela completa está em [`test-plan.md`](test-plan.md) §3.3.
- **Limpeza:** no fim, o banco é apagado (`DROP DATABASE` sem `FORCE`, que espera as sessões saírem) e as filas e o tópico são removidos. Uma falha de limpeza reprova o pacote.
- **`PDA_TEST_KEEP=1`:** mantém banco, filas e tópico (e os logs do cluster e2e) para inspeção.

```sh
PDA_TEST_KEEP=1 go test -tags=integration -race -count=1 -run '^TestInboxDeduplication$' ./internal/adapters/sqsconsumer/
docker compose exec postgres psql -U postgres -d pda -c "select datname from pg_database where datname like 'pda\_t\_%'"
```

---

## 3. Testes unitários

```sh
go test ./...                     # ~15 s com o cache de build
go test -race ./...               # idem, com o detector de corrida (make test)
go vet ./... && gofmt -l .        # sem avisos; o gofmt não imprime nada
make check                        # fmt-check + golangci-lint + vet (com e sem tags) + tidy-check + go-version-check + test
go test -race -run '^TestParseMoney$' ./internal/domain/money/   # um teste
```

O `make check` roda o `golangci-lint` pela imagem Docker fixada no `Makefile`. Para usar um binário local, na mesma versão: `make lint GOLANGCI_LINT=golangci-lint`.

**Cobrem:** `Money` (parsing, escala, limites, overflow, moedas; `FuzzParseMoney` e a análise da AST contra ponto flutuante), carteira e ledger, a máquina de estados, as regras dos cinco tipos externos, o valor zero, o hash e a decisão de idempotência, os eventos, a classificação de erros, a política de autorização, o verificador de tokens (JWKS local), a borda HTTP, as métricas e o grafo do Fx.

---

## 4. Testes de integração

```sh
make test-integration             # infra-up + go test -tags=integration -race -count=1 ./...
```

- **Tempo:** cerca de 50 s com a infraestrutura de pé. O alvo roda os unitários junto, porque `./...` inclui os pacotes sem tag.
- **Cobrem**, contra PostgreSQL, Keycloak e MiniStack reais:
  - migrations (`up` → `down -all` → `up`), constraints, triggers e imutabilidade do ledger (inclusive como dono das tabelas);
  - atomicidade financeira, inbox, reentrega, redrive e envio explícito à DLQ;
  - dois publishers disputando a outbox, retry com backoff e recuperação de lease;
  - recuperação depois de reiniciar o app e o worker de referências;
  - autenticação real e casos negativos de token, isolamento entre provedores, políticas IAM do broker;
  - composição Fx (grafo, start/stop com `goleak`, ordem do shutdown, fail fast);
  - e a validação de toda troca HTTP contra o `api/openapi.yaml` e de todo evento contra o `api/events.yaml`.
- **Um pacote ou um teste**, com a infraestrutura de pé:

```sh
go test -tags=integration -race -count=1 ./test/integration/
go test -tags=integration -race -count=1 -run '^TestMigrationsUpDownUp$' ./internal/adapters/postgres/
go test -tags=integration -race -count=1 -run '^TestAuthRejects$' ./test/integration/
```

O `-count=1` evita que o `go test` devolva um resultado em cache sem tocar a infraestrutura.

---

## 5. Múltiplas instâncias e simulações de falha (e2e)

```sh
make test-e2e                     # infra-up + go test -tags=e2e -race -p 1 -count=1 -timeout 15m ./test/e2e/...
```

**Tempo:** cerca de 2,5 min com a infraestrutura de pé.

**O cluster** (`testkit.Cluster`, [`test-plan.md`](test-plan.md) §3.4):
- **Build e processos:** no `TestMain`, o binário é compilado uma vez com `go build -tags faultinject -race ./cmd/pda`. O pacote então sobe **3 processos** no host, cada um com seu pool de conexões e sua memória, portas próprias (livres, escolhidas pelo teste), o mesmo banco e as mesmas filas isoladas. O start espera o `/health/ready` de cada um.
- **Um cluster por pacote**, daí o `-p 1`. O cliente distribui as requisições em round-robin, só entre as instâncias vivas e sem ponto de falha armado.
- **Operações do cluster:** `Kill` (`SIGKILL`), `Stop` (`SIGTERM`), `StopAsync` e `Restart`, este último com outro ambiente: com ou sem ponto de falha, ou com um papel desligado. Os cenários de crash ligam o componente em teste **só** na instância com a falha armada, para que o crash aconteça onde o teste espera.
- **Logs:** os de cada processo ficam em memória, para as asserções, e num diretório temporário `pda-cluster-*`, anexados à saída quando um teste falha.
- **Data race em processo filho:** um `WARNING: DATA RACE` em qualquer instância, ou uma saída com código diferente de 0 num stop gracioso, reprova o pacote.
- **Consistência:** toda carteira aberta por um teste passa, no `Cleanup`, pela verificação completa do [`test-plan.md`](test-plan.md) §6. Ela confere a reconciliação, que o saldo seja igual a Σ créditos − Σ débitos, a cadeia do ledger, as versões, a ausência de lançamento para rejeições, os eventos esperados na outbox e cada evento publicado e entregue com o conteúdo do banco.

| Cenários | Testes |
| --- | --- |
| Concorrência (3 instâncias) | `TestSameBet50xHTTP`, `TestSameBet50xSQS`, `TestTwoBetsCompete` (100.00 contra 2 × 80.00, 20 repetições), `TestWalletsInParallel`, `TestNoGlobalLock` |
| Crash no consumidor | `TestCrashAfterCommitBeforeDelete`, `TestCrashBeforeCommit` |
| Crash no HTTP e no publisher | `TestHTTPCrashAfterCommit`, `TestPublisherCrashAfterPublish`, `TestPublisherCrashAfterClaim` |
| Referências | `TestRefundBeforeBet`, `TestRefundReferenceExpires`, `TestReferenceWorkerCrash` |
| Reinício de todas as instâncias | `TestFullRestart` |
| HTTP × SQS | `TestHTTPThenSQSSameOperation`, `TestHTTPAndSQSConcurrent` |
| Quedas de infraestrutura e shutdown | `TestPostgresOutage`, `TestSQSOutage`, `TestGracefulShutdownSQS`, `TestGracefulShutdownHTTP` |
| O próprio harness | `TestClusterSpreadsRequests`, `TestClusterInstanceLifecycle`, `TestClusterStopAsync` |

**Um cenário isolado**, com a infraestrutura de pé (o `TestMain` sobe o cluster mesmo assim):

```sh
go test -tags=e2e -race -count=1 -run '^TestTwoBetsCompete$' ./test/e2e/
go test -tags=e2e -race -count=1 -run '^TestPostgresOutage$' -v ./test/e2e/
```

### 5.1 Pontos de falha (`PDA_FAULT`)

O pacote [`internal/faultinject`](../internal/faultinject/) tem duas implementações:
- **Com a tag `faultinject`**, o processo lê `PDA_FAULT=<ponto>[,<ponto>]`. Ao passar pelo ponto, ele grava `FAULT_HIT <ponto>` no stderr e sai com o código **137**, como num `SIGKILL`.
- **Sem a tag**, é um no-op que o compilador elimina. **A imagem Docker e o binário de produção não contêm nenhum ponto de falha.**

| Ponto | Onde | Simula | Teste |
| --- | --- | --- | --- |
| `consumer.before_commit` | Dentro do `uow.Do`, depois das escritas e antes do `COMMIT` | Crash antes do commit | `TestCrashBeforeCommit` |
| `consumer.after_commit_before_delete` | Depois do `COMMIT`, antes do `DeleteMessage` | Crash entre o commit e a remoção da mensagem | `TestCrashAfterCommitBeforeDelete` |
| `http.after_commit_before_response` | Depois do `COMMIT`, antes da resposta | Cliente sem resposta, que reenvia a outra instância | `TestHTTPCrashAfterCommit` |
| `outbox.after_claim_before_publish` | Depois de reservar o lote, antes do `Publish` | Crash com eventos reservados | `TestPublisherCrashAfterClaim` |
| `outbox.after_publish_before_ack` | Depois do `Publish`, antes da confirmação | Republicação com o mesmo `eventId` | `TestPublisherCrashAfterPublish` |
| `references.after_claim` | Depois de travar a pendência, antes de resolvê-la | Crash do worker de referências | `TestReferenceWorkerCrash` |

O harness exige a linha `FAULT_HIT` e o código 137. Sem isso, o teste falha em vez de passar por acaso.

### 5.2 Quedas de infraestrutura (`docker compose pause`)

Os testes R congelam um serviço compartilhado com `docker compose pause`, o que mantém as conexões TCP abertas mas sem resposta, como numa queda real. Depois, eles o soltam com `unpause`:

| Teste | Queda | O que prova |
| --- | --- | --- |
| `TestPostgresOutage` | PostgreSQL por 15 s, com tráfego HTTP e SQS | HTTP 503 com `Retry-After` (pelo `HTTP_REQUEST_TIMEOUT`), ready 503 nas 3 instâncias, o consumidor para de receber. Depois: tudo processado uma vez, nada na DLQ |
| `TestSQSOutage` | MiniStack por 10 s, com BETs pelo HTTP | O HTTP continua processando, e a outbox acumula e envelhece. Depois, ela esvazia e todos os eventos chegam à auditoria |
| `TestGracefulShutdownSQS` | `SIGTERM` com 3 mensagens paradas no lock | As mensagens em andamento terminam, as outras são liberadas e a saída é 0 dentro do `SHUTDOWN_TIMEOUT` |
| `TestGracefulShutdownHTTP` | `SIGTERM` com 5 requisições paradas no lock | As requisições em andamento respondem 200, uma conexão nova é recusada e a saída é 0 |

**Cuidados:**
- **Não rode `make test-integration` e `make test-e2e` ao mesmo tempo.** Um serviço pausado derruba qualquer teste simultâneo. Pelo mesmo motivo, os testes que pausam não usam `t.Parallel()`.
- **Execução interrompida:** o `unpause` fica no `Cleanup` do teste, mas um `panic` por timeout do `go test` pula os `Cleanup` e pode deixar o PostgreSQL ou o MiniStack pausados. Nesse caso, o Docker marca o container como *unhealthy*, e o `up --wait` desiste dele. O `make infra-up` já faz o `unpause`. À mão, faça um serviço por comando, porque o `unpause` com dois serviços falha por inteiro se um deles não estiver pausado:

```sh
docker compose unpause postgres
docker compose unpause ministack
```

### 5.3 Simulações manuais no compose

Com `docker compose up --build --wait` e os exemplos do [`README.md`](../README.md) §8, é possível reproduzir as falhas à mão:

```sh
# Queda do PostgreSQL: ready 503 e requisições 503 com Retry-After depois do HTTP_REQUEST_TIMEOUT (10 s)
docker compose pause postgres
curl -s localhost:8081/health/ready          # {"status":"DOWN","checks":{"postgres":"DOWN","sqs":"UP"}}
docker compose unpause postgres              # reenviar com a mesma Idempotency-Key é seguro

# Queda do broker: o HTTP continua processando e a outbox acumula até a volta
docker compose pause ministack
curl -s localhost:9091/metrics | grep '^outbox_pending_events'    # > 0 enquanto o broker está fora
docker compose unpause ministack                                  # volta a 0 depois da publicação

# Encerramento abrupto de uma réplica: as outras continuam; a parada manual não reinicia
docker compose kill app-1
docker compose start app-1

# Queda "de verdade": o runtime do Go sai com 2 e o restart: on-failure traz a réplica de volta
docker run --rm --pid=container:pda-app-1-1 --entrypoint kill postgres:18.6-alpine -QUIT 1
docker inspect -f '{{.RestartCount}} {{.State.Health.Status}}' pda-app-1-1

# Shutdown gracioso: a ordem aparece no log (API → consumidor → publisher → worker → pool → clientes AWS → admin)
docker compose stop app-2 && docker compose logs app-2 | tail -20
docker compose start app-2
```

---

## 6. Rastreabilidade

- Cada teste declara o que cobre num comentário `// Covers: …`, com os IDs de [`delivery-requirements.md`](delivery-requirements.md) e de [`test-plan.md`](test-plan.md). Assim, `grep -rn "Covers:" --include='*_test.go' .` gera a matriz de cobertura.
- Os testes escritos sobre comportamento que já existia registram a sabotagem usada para vê-los falhar: `// Sensitivity: …`.
- O [`delivery-requirements.md`](delivery-requirements.md) marca cada requisito do desafio com o teste que o comprova, e o [`test-plan.md`](test-plan.md) §7 liga cada critério eliminatório aos seus testes.

---

## 7. CI

O workflow [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) roda, em `push` na `main` e em pull requests, três jobs independentes:

| Job | Comando | Observação |
| --- | --- | --- |
| Unit tests | `make test` | Sem Docker |
| Integration tests | `make test-integration` | Sobe a infraestrutura com o compose do repositório; em falha, anexa os logs dos serviços |
| E2E tests | `make test-e2e` | Idem, com o cluster de 3 processos |

O lint fica de fora do CI de propósito: o `make check` é o portão local, antes de cada commit.

---

## 8. Problemas comuns

| Sintoma | Causa | O que fazer |
| --- | --- | --- |
| `connection refused` em `localhost:5432`, `8080` ou `4566` | A infraestrutura não está de pé | `make infra-up` |
| `open credentials (run make infra-up)` | O `aws-init` ainda não gerou `.local/aws/credentials` | `make infra-up` |
| `UnrecognizedClientException` nos testes com SQS | O MiniStack foi recriado depois do último `aws-init` | `make infra-up` (reexecuta o `aws-init`) |
| Testes falhando em massa com timeout, e `docker compose ps` mostrando `(Paused)` ou *unhealthy* | Um teste de resiliência interrompido deixou um serviço pausado | `make infra-up`, ou o `unpause` por serviço da §5.2 |
| Resultado sem tocar a infraestrutura (`(cached)`) | O `go test` reaproveitou o resultado | Use `-count=1`, como os alvos do `make` |
| Bancos `pda_t_*` sobrando | Execução com `PDA_TEST_KEEP=1`, ou morta com `kill -9` | `docker compose exec postgres psql -U postgres -d pda -c 'DROP DATABASE "<nome>"'`, ou `docker compose down -v` para zerar tudo |

---

## 9. Teste de carga (opcional)

O teste de carga não faz parte dos três níveis acima nem do CI, porque os números dependem da máquina. Ele mede as 3 réplicas do compose que já estão de pé, com o k6 rodando num container do próprio compose (profile `load`, D-21):

```sh
docker compose up --build -d --wait
make load-test                                  # 200 req/s por 60 s, 1.000 carteiras
RATE=20 DURATION=5s WALLETS=50 make load-test   # execução curta, para conferir o ambiente
```

A execução sai com código diferente de zero se:
- a taxa de erros passar de 1%;
- a outbox não drenar em 60 s;
- alguma carteira divergir do ledger na reconciliação feita depois da carga.

A metodologia, o ambiente medido e os resultados estão em [`load-test.md`](load-test.md).
