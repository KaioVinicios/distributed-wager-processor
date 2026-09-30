# Bancos de teste: `DROP` intermitente e bancos órfãos: plano

> **Para quem executa:** execução **inline** com `superpowers:executing-plans` e `superpowers:test-driven-development`. Sem subagentes e **sem passos de commit** (commits propostos no fim, com a autorização do autor, sem trailer de coautoria).

**Objetivo:** o `testkit` apaga os bancos de teste sem falhas intermitentes, e nenhuma falha de limpeza passa em silêncio.

**Arquitetura:** o `drop` de `testkit.NewDatabase` tenta `DROP DATABASE` simples, que espera 5 s e encerra o autovacuum sozinho, e só em `55006` repete com `FORCE`. O `cleanup` de `NewEnv` devolve `error`; `NewTestEnv` e os `TestMain` o tornam visível.

**Stack:** Go 1.27.1, pgx v5, PostgreSQL 18 do compose; testes com a tag `integration`.

**Spec:** [`dev/specs/2026-09-30-test-db-drop-design.md`](../specs/2026-09-30-test-db-drop-design.md).

## Restrições globais

- `make check` verde ao fim de cada tarefa de código.
- `// Covers: …` e `// Sensitivity: …` como no resto do repositório.
- Nenhum privilégio novo para `pda_owner`.
- A limpeza dos bancos órfãos (Tarefa 4) só roda com a lista conferida e sem testes em andamento.

## Foco da revisão

1. **Sessão viva que nunca fecha** (vazamento real) → o `drop` falha com erro visível depois de ~5 s, sem travar (Tarefa 1, I29 e I30).
2. **`PDA_TEST_KEEP=1`** → nada é apagado e o `cleanup` devolve `nil` (Tarefa 2; o ramo existente fica intacto).
3. **Falha de conexão como admin** no `drop` → o erro continua sendo devolvido, não engolido (Tarefa 1: `adminExec` já devolve).
4. **`cleanup` chamado duas vezes** → o segundo `Close` dos pools é inofensivo (`pgxpool.Close` é idempotente), e o segundo `DROP` de um banco já apagado falha. O I30 só chama o segundo depois de um primeiro que falhou.
5. **`TestMain` com testes verdes e `cleanup` falhando** → código de saída 1 (Tarefa 2).

---

### Tarefa 1: `DROP` sem `FORCE`, com recurso ao `FORCE` (I28, I29)

**Arquivos:**
- criar `test/testkit/postgres_integration_test.go` (pacote `testkit`, interno, tag `integration`);
- modificar `test/testkit/postgres.go`.

- [ ] **Passo 1: escrever os testes**

```go
//go:build integration

package testkit

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// newDB creates an isolated database and its drop, failing the test on error.
func newDB(t *testing.T, name string) (*Database, func() error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	db, drop, err := NewDatabase(ctx, name)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	return db, drop
}

// dbExists tells whether the database is still in pg_database.
func dbExists(t *testing.T, name string) bool {
	t.Helper()
	vals, err := LoadDotEnv()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], "pda"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	var n int
	if err := conn.QueryRow(t.Context(), `SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Covers: TST-I* infrastructure (I28; spec test-db-drop, decision 1)
// Sensitivity: DROP … WITH (FORCE) first → "permission denied to terminate process".
func TestDropWaitsForExitingSessions(t *testing.T) {
	db, drop := newDB(t, "drop_wait")
	conn, err := pgx.Connect(t.Context(), db.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	// The pda_app backend is still attached when the drop starts, as after a
	// pool Close whose backends have not exited yet.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		time.Sleep(300 * time.Millisecond)
		_ = conn.Close(context.WithoutCancel(t.Context()))
	}()
	err = drop()
	<-closed
	if err != nil {
		t.Fatalf("drop() = %v, want the database dropped once the session exits", err)
	}
	if dbExists(t, db.Name) {
		t.Fatalf("database %s still exists", db.Name)
	}
}

// Covers: TST-I* infrastructure (I29; spec test-db-drop, decision 1)
// Sensitivity: no FORCE fallback → "database … is being accessed by other users" after 5 s.
func TestDropForcesLeftoverOwnerSession(t *testing.T) {
	db, drop := newDB(t, "drop_force")
	conn, err := pgx.Connect(t.Context(), db.OwnerURL) // never closed by the test: a leaked session
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if err := drop(); err != nil {
		t.Fatalf("drop() = %v, want FORCE to end the leftover owner session", err)
	}
	if dbExists(t, db.Name) {
		t.Fatalf("database %s still exists", db.Name)
	}
}
```

- [ ] **Passo 2: ver falhar**

Run: `make infra-up && go test -tags=integration -count=1 ./test/testkit -run 'TestDrop' -v`
Expected:
- o I28 falha com `drop() = testkit: DROP: ERROR: permission denied to terminate process (SQLSTATE 42501)`, determinístico, porque o backend `pda_app` está vivo no início do `DROP`;
- o I29 **passa**: é o comportamento atual do `FORCE`, e a sensibilidade dele vem no passo 5.
- Se o I28 passar, o `pda_app` fechou antes do `DROP`: aumente o `Sleep` e confirme o red antes de seguir.

- [ ] **Passo 3: implementar** (`test/testkit/postgres.go`)

Troque a última linha do `drop` por uma chamada a `dropDatabase(ctx, admin, ident)` e acrescente:

```go
// dropDatabase drops the database without FORCE first: PostgreSQL then ends
// the autovacuum workers attached to it and waits up to 5 s for the other
// sessions to exit, without the permission check that FORCE applies to every
// attached process (pda_owner lacks pg_signal_backend, so a worker or a
// backend still exiting made FORCE fail). Only a session still alive after
// that (55006, object in use) is ended with FORCE, as before.
func dropDatabase(ctx context.Context, admin, ident string) error {
	err := adminExec(ctx, admin, "DROP DATABASE "+ident)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "55006" {
		return adminExec(ctx, admin, "DROP DATABASE "+ident+" WITH (FORCE)")
	}
	return err
}
```

Imports novos: `"errors"` e `"github.com/jackc/pgx/v5/pgconn"`. O `adminExec` já embrulha com `%w`, então o `errors.As` alcança o `*pgconn.PgError`.

- [ ] **Passo 4: ver passar**

Run: `go test -tags=integration -count=1 ./test/testkit -run 'TestDrop' -v` e `make check`
Expected: PASS nos dois; o I29 leva ~5 s; `make check` verde.

- [ ] **Passo 5: sensibilidade.**
  - Volte temporariamente o `drop` para `"DROP DATABASE "+ident+" WITH (FORCE)"` → o I28 falha com `42501`.
  - Remova o recurso ao `FORCE` (devolva `err` direto) → o I29 falha com `55006` depois de ~5 s.
  - Desfaça as duas.

---

### Tarefa 2: `cleanup` que não engole o erro (I30)

**Arquivos:**
- modificar `test/testkit/env.go`;
- modificar os 4 `TestMain`: `test/integration/main_test.go`, `internal/app/main_integration_test.go`, `internal/adapters/sqsconsumer/main_integration_test.go`, `internal/adapters/postgres/main_integration_test.go`;
- acrescentar ao `test/testkit/postgres_integration_test.go`.

- [ ] **Passo 1: escrever o teste** (fim de `postgres_integration_test.go`; imports `"strings"`)

```go
// Covers: TST-I* infrastructure (I30; spec test-db-drop, decision 3)
// Sensitivity: cleanup printing the drop error instead of returning it → cleanup() = nil.
func TestNewEnvCleanupReportsDropFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	env, cleanup, err := NewEnv(ctx, "cleanup_err")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), env.DB.AppURL) // alive during the cleanup: FORCE cannot end it
	if err != nil {
		t.Fatal(err)
	}
	err = cleanup()
	_ = conn.Close(context.WithoutCancel(t.Context()))
	if err == nil || !strings.Contains(err.Error(), "DROP") {
		t.Fatalf("cleanup() = %v, want the DROP error", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("second cleanup() = %v, want the database dropped", err)
	}
	if dbExists(t, env.DB.Name) {
		t.Fatalf("database %s still exists", env.DB.Name)
	}
}
```

- [ ] **Passo 2: stub para compilar** (`env.go`)

Troque a assinatura para `(env *Env, cleanup func() error, err error)` e mantenha o comportamento, com o `cleanup` ainda engolindo o erro:

```go
	cleanup = func() error {
		app.Close()
		owner.Close()
		if err := drop(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: drop database:", err)
		}
		return nil
	}
```

Em `NewTestEnv`: `tb.Cleanup(func() { _ = cleanup() })` (provisório). Nos 4 `TestMain`: `_ = cleanup()` (e, no `test/integration/main_test.go`, `defer func() { _ = cleanup() }()`).

- [ ] **Passo 3: ver falhar**

Run: `go test -tags=integration -count=1 ./test/testkit -run TestNewEnvCleanupReportsDropFailure -v`
Expected: FAIL: `cleanup() = <nil>, want the DROP error`, depois de ~5 s de espera do `DROP` simples.

- [ ] **Passo 4: implementar**

`env.go`:

```go
	cleanup = func() error {
		app.Close()
		owner.Close()
		return drop()
	}
```

(remova os imports `fmt` e `os`, se deixarem de ser usados.)

`NewTestEnv`:

```go
	tb.Cleanup(func() {
		if err := cleanup(); err != nil {
			tb.Errorf("testkit: drop database %s: %v", env.DB.Name, err)
		}
	})
```

Os 4 `TestMain` trocam o código de saída quando a limpeza falha. Padrão (`internal/app`, `sqsconsumer`, `postgres`):

```go
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "testkit: cleanup:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
```

`test/integration/main_test.go` usa `run(m) int` com `defer cleanup()`: troque o `defer` por um que ajuste o retorno nomeado.

```go
func run(m *testing.M) (code int) {
	…
	defer func() {
		if err := cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: cleanup:", err)
			if code == 0 {
				code = 1
			}
		}
	}()
```

Confira a ordem: o `stop` do `StartApp` precisa rodar **antes** do `cleanup`. Em `defer`, isso quer dizer que o `defer` do `cleanup` é registrado **antes** do `defer stop()`.

- [ ] **Passo 5: ver passar**

Run: `go test -tags=integration -count=1 ./test/testkit -v` e `make check`
Expected: PASS em I28–I30; `make check` verde.

- [ ] **Passo 6: sensibilidade.** Volte o `cleanup` a imprimir e devolver `nil` → o I30 falha. Desfaça.

---

### Tarefa 3: Verificação da suíte

- [ ] **Passo 1:** anote o instante de início (`date -u +%Y-%m-%dT%H:%M:%SZ`) e rode `make test-integration` 4 vezes seguidas.
  Expected: as 4 com saída 0 e todos os pacotes `ok`.
- [ ] **Passo 2:** `docker compose logs --since <início> postgres | grep -c "permission denied to terminate process"`.
  Expected: `0`.
- [ ] **Passo 3:** `docker compose exec -T postgres psql -U postgres -d pda -Atc "select datname from pg_database where datname like 'pda\_t\_%'"`.
  Expected: só os 9 órfãos antigos (a lista do Passo 1 da Tarefa 4), nenhum novo.

---

### Tarefa 4: Limpeza dos bancos órfãos e documentos

- [ ] **Passo 1: conferir a lista**, sem testes rodando:
  `docker compose exec -T postgres psql -U postgres -d pda -Atc "select datname from pg_database where datname like 'pda\_t\_%' order by 1"`.
  Expected: os 9 nomes da spec (§1, evidência 2), mais nada criado depois da Tarefa 3.
- [ ] **Passo 2: apagar**, como superusuário do compose, um por um:
  `for d in <lista>; do docker compose exec -T postgres psql -U postgres -d pda -c "DROP DATABASE \"$d\" WITH (FORCE)"; done`.
  Depois repita o Passo 1. Expected: lista vazia.
- [ ] **Passo 3: documentos** (spec §4):
  - `test-plan.md`: I28–I30 na §5.2 e a nota no §3.2.
  - `implementation-plan.md` §5: a linha do flake do `TestMigrationsUpDownUp` vira "✅ Tratado em 30/09:" com a causa (`FORCE` sem `pg_signal_backend` diante do autovacuum ou de um backend saindo) e a correção.
  - `diary.md`: a entrada do flake em "Onde paramos" vira "resolvido (ver [spec](specs/2026-09-30-test-db-drop-design.md))", e uma entrada curta no dia 30/09.
  - Spec do M2, item 5 da lista do `testkit`: acrescente "(30/09: sem `FORCE` primeiro; ver [spec](2026-09-30-test-db-drop-design.md))".
- [ ] **Passo 4:** `make check` final. Reporte as saídas das Tarefas 3 e 4, proponha commits atômicos e espere a autorização.
