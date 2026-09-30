# Bancos de teste: `DROP` intermitente e bancos órfãos: design

**Data:** 30/09/2026 · **Caminho:** correção pontual (*bounded*, [`development-workflow.md`](../../development-workflow.md) §2), spec e plano curtos · **Status:** aprovada pelo autor em 30/09/2026 ("execute"), com o ajuste da execução abaixo

**Origem:** a falha intermitente do `TestMigrationsUpDownUp`, registrada no diário e nos riscos do [`implementation-plan.md`](../../implementation-plan.md) durante o M7. O autor pediu a investigação depois dos commits da revisão do M7.

**Ajuste da execução (vale sobre a decisão 1):** o recurso ao `FORCE` saiu. Depois dos 5 s de espera do `DROP` simples, o autovacuum volta a entrar no banco, e o `FORCE` bate na mesma checagem de permissão: o I29 falhou uma vez assim. No lugar dele, as sessões da própria role `pda_owner` são encerradas com `pg_terminate_backend`, o que dispensa privilégio extra, e o `DROP` simples é repetido. O I29 passou a se chamar `TestDropEndsLeftoverOwnerSession`.

---

## 1. Investigação (causa raiz)

**Sintoma:** `drop database: testkit: DROP: ERROR: permission denied to terminate process (SQLSTATE 42501)`, ao fim de um teste. Visto 1 vez em 4 execuções completas do `make test-integration` no M7.

**Evidências:**

1. **Não é exclusivo do teste de migrations.** O log do PostgreSQL do compose tem 7 ocorrências hoje, em 7 bancos de 6 pacotes: `integration` (2), `bootstrap`, `sqsconsumer`, `refs_concurrent`, `migrations` e `postgres`. Todas têm o mesmo `DETAIL`: *"Only roles with privileges of the role whose process is being terminated or with privileges of the "pg_signal_backend" role may terminate this process."*
2. **As outras falhas foram silenciosas.** `testkit.NewEnv` (usado nos `TestMain`) e `testkit.NewTestEnv` só imprimem o erro do `DROP` no stderr. Só o `TestMigrationsUpDownUp` falha, porque o seu `isolatedDB` usa `t.Errorf`. Consequência: **9 bancos `pda_t_*` órfãos** no PostgreSQL do compose (os 7 acima e mais 2). Mais uma falha silenciosa apareceu durante a investigação, com as duas execuções completas dando `ok`.
3. **O mecanismo, no código do PostgreSQL 18** (`TerminateOtherDBBackends`, `src/backend/storage/ipc/procarray.c`, `REL_18_STABLE`):
   - `DROP DATABASE … WITH (FORCE)` coleta **todo** processo conectado ao banco, inclusive workers de autovacuum;
   - para cada um, exige que quem executa tenha privilégio sobre a role do processo ou seja membro de `pg_signal_backend`;
   - `pda_owner` não é superusuário nem membro de `pg_signal_backend` (conferido em `pg_roles` e `pg_auth_members`).
   - Resultado: o `DROP` falha se, **naquele instante**, houver no banco:
     - um **worker de autovacuum**, cujo `roleId` é inválido, de modo que `has_privs_of_role` é falso e o `DETAIL` é o mesmo do log;
     - ou um **backend `pda_app` ainda saindo**: o `pgxpool.Close()` fecha o socket, mas o backend sai de forma assíncrona.
4. **O autovacuum roda nos bancos de teste.** Com `log_autovacuum_min_duration=0` ligado temporariamente, 4 execuções completas registraram 43 análises e vacuums em tabelas de catálogo de bancos `pda_t_*`. As migrations geram muita alteração de catálogo, e o `up → down → up` gera ainda mais.
5. **O banco de migrations só tem sessões `pda_owner`** (pool e migrator). Ali o processo bloqueante não pode ser da aplicação, e o autovacuum é a única explicação. Nos bancos com `pda_app`, as duas causas são possíveis e dão o mesmo `DETAIL`.
6. **O `DROP DATABASE` sem `FORCE` não tem esse problema** (`CountOtherDBBackends`):
   - envia `SIGTERM` aos workers de autovacuum do banco **sem checar privilégio**;
   - espera até 5 s (50 × 100 ms) que as outras sessões saiam;
   - só falha, com `55006` (*database is being accessed by other users*), se sobrar uma sessão viva.

**Causa raiz:** o `testkit` apaga os bancos com `WITH (FORCE)` executado por uma role sem `pg_signal_backend`. O `FORCE` troca a espera tolerante do PostgreSQL por uma checagem de permissão que falha diante de processos transitórios. A spec do M2 não registra o motivo do `FORCE`.

**Causa secundária:** o `cleanup` de `NewEnv` e de `NewTestEnv` engole o erro, e isso transformou uma falha em vazamento silencioso.

---

## 2. Decisões

| # | Decisão | Motivo |
| --- | --- | --- |
| 1 | **`drop` executa `DROP DATABASE` sem `FORCE`** e, só se ele falhar com `55006` (sessão viva depois de 5 s), repete com `WITH (FORCE)` | Resolve as duas causas (autovacuum e backend saindo) pelo caminho que o próprio PostgreSQL trata. O `FORCE` fica como recurso para uma sessão `pda_owner` esquecida, o comportamento do M2 |
| 2 | **Nenhum privilégio novo** (`pg_signal_backend`) para `pda_owner` | Seria dar à role das migrations o poder de encerrar qualquer sessão não superusuária, só para servir aos testes |
| 3 | **O `cleanup` de `NewEnv` passa a devolver `error`.** `NewTestEnv` o reporta com `tb.Errorf`, e os 4 `TestMain` que usam `NewEnv` trocam o código de saída para 1 quando ele falha | Uma falha de limpeza não pode mais passar em silêncio nem vazar bancos |
| 4 | **Os 9 bancos órfãos são apagados uma vez**, pelo superusuário do compose, com a lista conferida antes. Só bancos `pda_t_*` e com nenhum teste rodando | Limpeza do estado deixado pelo defeito. É destrutivo, mas só atinge bancos de teste descartáveis |
| 5 | **O flake sai do diário e dos riscos como resolvido**, com o resumo da causa | O registro de 30/09 dizia "hipótese não verificada"; agora há causa e correção |

---

## 3. Testes (pacote `testkit`, tag `integration`)

| ID | Teste | Prova |
| --- | --- | --- |
| I28 | `TestDropWaitsForExitingSessions` | Uma sessão `pda_app` fecha 300 ms depois do início do `drop`. O `drop` espera e apaga o banco, que deixa de existir em `pg_database`. Com `WITH (FORCE)` direto, o red é determinístico: `permission denied to terminate process` |
| I29 | `TestDropForcesLeftoverOwnerSession` | Uma sessão `pda_owner` nunca fechada: o `DROP` simples esgota os 5 s, e o `FORCE` a encerra e apaga o banco. Preserva o comportamento do M2 (teste sobre comportamento existente, com sensibilidade) |
| I30 | `TestNewEnvCleanupReportsDropFailure` | Uma sessão `pda_app` viva durante o `cleanup`: ele devolve o erro do `DROP`, em vez de só imprimir. Depois de fechar a sessão, um segundo `cleanup` apaga o banco |

O I28 cobre diretamente a causa do backend ainda saindo. O autovacuum não é reproduzível de forma determinística. A prova para ele é o mecanismo do §1 (evidência 6) e a ausência de novas ocorrências no log do PostgreSQL em 4 execuções completas depois da correção.

**Pronto quando:**
1. `make check` verde.
2. I28–I30 vistos falhando pelo motivo certo, ou com sensibilidade, e depois passando.
3. `make test-integration` verde 4 vezes seguidas, sem nenhuma nova linha `permission denied to terminate process` no log do PostgreSQL desde o início das execuções e sem bancos `pda_t_*` sobrando ao final.

---

## 4. Documentos

- `test-plan.md`: I28–I30 e uma nota no §3.2 (isolamento de banco) sobre o `DROP` sem `FORCE` e a falha visível do `cleanup`.
- `implementation-plan.md` §5 (riscos): o flake marcado como tratado, com a causa.
- `diary.md`: a entrada de "Onde paramos" sobre o flake vira "resolvido", com um parágrafo de causa.
- Spec do M2 (item 5 da lista de passos do `testkit`): nota apontando para esta spec.
