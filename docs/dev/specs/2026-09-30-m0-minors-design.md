# Pendências menores do M0: servidor HTTP, `restart:` e logs do Fx: design

**Data:** 30/09/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 30/09/2026

**Origem:** as três pendências menores adiadas na verificação do M0 ([`diary.md`](../diary.md), 28–29/09). O autor decidiu corrigi-las antes do M10 (30/09), em vez de registrá-las como limitação.

---

## 1. Estado atual

| # | Pendência | Onde | Efeito |
| --- | --- | --- | --- |
| 1 | Um servidor HTTP que para sozinho depois do start não encerra o processo | `observability.ServeOnLifecycle`: a goroutine do `Serve` só registra `http server stopped unexpectedly` | A réplica fica de pé sem servir a API (ou a `/metrics`). O healthcheck a marca como *unhealthy*, mas o Docker não reinicia containers *unhealthy* fora do Swarm |
| 2 | As réplicas não têm política de reinício | `x-app` do `docker-compose.yml` | Um processo que cai (panic, OOM, a saída da pendência 1) deixa a réplica parada até alguém intervir |
| 3 | Os eventos do ciclo de vida do Fx saem em INFO | `bootstrap.OptionsFor`: `fxevent.SlogLogger` com o nível padrão | Cada start gera dezenas de linhas `provided`/`invoking`/`OnStart hook executed`, com *stacktrace*, que enterram os logs da aplicação (`docker compose logs app-1`) |

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **`ServeOnLifecycle` recebe o `fx.Shutdowner`.** Quando o `Serve` termina com um erro que não é `http.ErrServerClosed`, ele registra o ERROR que já existe e chama `Shutdown(fx.ExitCode(1))`. Um erro do próprio `Shutdown` também é registrado. Vale para os dois servidores (API e admin) | O Fx faz o stop ordenado de sempre (o consumidor libera as mensagens, o publisher e o worker terminam o item, o pool fecha por último), e o processo sai com código 1. Um `os.Exit(1)` direto pularia esse stop gracioso |
| 2 | **`restart: on-failure` no `x-app`** (as 3 réplicas). A infraestrutura continua sem política | Reinicia o que caiu (1 da decisão 1, 2 de um panic, 137 de um OOM) e não reinicia uma parada limpa (0 depois do `SIGTERM`). O `docker compose stop`/`kill` conta como parada manual, então as simulações manuais de falha continuam possíveis. O `unless-stopped` reiniciaria também depois de um restart do daemon, mas a infraestrutura não voltaria junto, então não traz ganho |
| 3 | **Eventos do Fx em DEBUG:** `UseLogLevel(slog.LevelDebug)` no `fxevent.SlogLogger`, extraído para `bootstrap.fxLogger`. Os erros do Fx (`start failed`, `invoke failed`, `OnStop hook failed`…) continuam em ERROR | Os componentes já registram o próprio ciclo de vida em INFO (`roles resolved`, `http server started`, `… stopping`/`stopped`, `postgres pool closed`, `aws http client closed`). Com `LOG_LEVEL=debug`, os eventos do Fx voltam. Perde-se o `received signal` em INFO; o `http server stopping` já marca o início da parada |

**Fora do escopo:** política de reinício na infraestrutura; reiniciar a réplica quando ela fica *unhealthy* (isso exigiria um orquestrador); qualquer mudança no `pda healthcheck`.

---

## 3. Testes

| ID | Teste | Prova |
| --- | --- | --- |
| U31 | `TestServeOnLifecycle_ShutsDownWhenServeFails` (`observability`) | App real do `fxtest`. O teste captura o listener pelo `http.Server.BaseContext` (chamado pelo `Serve` com o listener) e o fecha: o `Accept` falha e o `Serve` termina com erro. O `app.Wait()` entrega um `ShutdownSignal` com `ExitCode == 1` em até 2 s, e o log tem `http server stopped unexpectedly`. Red: com o `Shutdowner` recebido e ignorado, nenhum sinal chega |
| U31b | `TestServeOnLifecycle_ServesUntilStopped` (estendido) | Um `Shutdowner` que registra as chamadas não é chamado num stop normal (`ErrServerClosed`). Teste sobre comportamento que já existe, com sensibilidade: chamar o `Shutdown` sempre que o `Serve` retorna faz o teste falhar |
| U32 | `TestFxEventsLogAtDebug` (`bootstrap`, sem infraestrutura) | `fx.New` com `OptionsFor(config.Roles{})`, o logger decorado para escrever num buffer e um `fx.Invoke` que falha. Em `info`: nenhuma linha de evento do Fx (`provided`, `supplied`, `decorated`, `invoking`, `invoked`, `run`) e a linha `invoke failed` em ERROR. Em `debug`: a linha `provided` aparece em DEBUG, o que prova que os eventos foram rebaixados, e não perdidos. Red: hoje as linhas `provided` saem em INFO |
| — | Compose (exceção §4.4) | `docker compose config` e `docker compose up --build --wait` saudáveis. Depois, uma queda real: `kill -QUIT 1` num container auxiliar que compartilha o namespace de PIDs da `app-1`. O runtime do Go sai com código 2, e a réplica volta sozinha e saudável (`RestartCount` = 1). Um `SIGKILL` de dentro do namespace seria ignorado pelo PID 1, e o `SIGTERM` sai com 0, que não reinicia |

**Documentação afetada:** [`decisions.md`](../../decisions.md) D-15 (servidor que para encerra o processo; `restart: on-failure`) e D-18 (eventos do Fx em DEBUG); [`ARCHITECTURE.md`](../../../ARCHITECTURE.md) §11, §12 e §13.1; [`test-plan.md`](../../test-plan.md) (U31, U32); [`diary.md`](../diary.md) (pendências do M0 resolvidas).
