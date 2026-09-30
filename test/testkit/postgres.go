package testkit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/migrations"
)

// pgHost is the compose PostgreSQL seen from the host.
const pgHost = "localhost:5432"

func databaseURL(user, password, db string) string {
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     pgHost,
		Path:     "/" + db,
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

// Database is an isolated database with the embedded migrations applied
// (test-plan §3.2).
type Database struct {
	Name     string
	OwnerURL string // pda_owner: migrations, setups and assertions the app role cannot do
	AppURL   string // pda_app: the role the application uses
}

var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// NewDatabase creates pda_t_<name>_<8 hex> as pda_owner, who has CREATEDB, and
// applies every embedded migration. drop removes the database, unless
// PDA_TEST_KEEP=1 keeps it for inspection.
func NewDatabase(ctx context.Context, name string) (db *Database, drop func() error, err error) {
	if !dbName.MatchString(name) {
		return nil, nil, fmt.Errorf("testkit: invalid database name %q", name)
	}
	vals, err := LoadDotEnv()
	if err != nil {
		return nil, nil, err
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, nil, err
	}
	db = &Database{Name: "pda_t_" + name + "_" + hex.EncodeToString(suffix)}
	db.OwnerURL = databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], db.Name)
	db.AppURL = databaseURL("pda_app", vals["PDA_APP_PASSWORD"], db.Name)
	admin := databaseURL("pda_owner", vals["PDA_OWNER_PASSWORD"], "pda")

	ident := pgx.Identifier{db.Name}.Sanitize()
	if err := adminExec(ctx, admin, "CREATE DATABASE "+ident); err != nil {
		return nil, nil, err
	}
	drop = func() error {
		if os.Getenv("PDA_TEST_KEEP") == "1" {
			return nil
		}
		// Detached: the caller's context may be done by the time it cleans up.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return dropDatabase(ctx, admin, db.Name)
	}
	m, err := db.Migrator()
	if err != nil {
		_ = drop()
		return nil, nil, err
	}
	defer closeMigrator(m)
	if err := m.Up(); err != nil {
		_ = drop()
		return nil, nil, fmt.Errorf("testkit: migrate up: %w", err)
	}
	return db, drop, nil
}

// Migrator returns golang-migrate over the embedded migrations, connected as
// pda_owner. The caller closes it with closeMigrator or m.Close.
func (d *Database) Migrator() (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("testkit: migrations source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5"+strings.TrimPrefix(d.OwnerURL, "postgres"))
	if err != nil {
		return nil, fmt.Errorf("testkit: migrate: %w", err)
	}
	return m, nil
}

func closeMigrator(m *migrate.Migrate) { _, _ = m.Close() }

// dropDatabase drops the database without FORCE: PostgreSQL then ends the
// autovacuum workers attached to it and waits up to 5 s for the other
// sessions to exit. FORCE is never used: it checks the permission to end
// every attached process, and pda_owner lacks pg_signal_backend, so an
// autovacuum worker or a backend still exiting made it fail at random. A
// session still alive after the wait (55006, object in use) is ended only if
// it is pda_owner's own, which needs no extra privilege, and the drop is
// retried once.
func dropDatabase(ctx context.Context, admin, name string) error {
	ident := pgx.Identifier{name}.Sanitize()
	err := adminExec(ctx, admin, "DROP DATABASE "+ident)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55006" {
		return err
	}
	if err := adminExec(ctx, admin, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname = `+quoteLiteral(name)+` AND usename = current_user AND pid <> pg_backend_pid()`); err != nil {
		return err
	}
	return adminExec(ctx, admin, "DROP DATABASE "+ident)
}

// quoteLiteral quotes a database name already checked by dbName.
func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func adminExec(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("testkit: connect as pda_owner: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		return fmt.Errorf("testkit: %s: %w", strings.Fields(sql)[0], err)
	}
	return nil
}

// AssertLedgerConsistent fails the test when LedgerProblems finds any. It is
// meant for t.Cleanup, where the test's context is already canceled, so it
// uses a detached one.
func AssertLedgerConsistent(tb testing.TB, pool *pgxpool.Pool, walletID string) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), 30*time.Second)
	defer cancel()
	problems, err := LedgerProblems(ctx, pool, walletID)
	if err != nil {
		tb.Fatalf("wallet %s: %v", walletID, err)
	}
	for _, p := range problems {
		tb.Errorf("wallet %s: %s", walletID, p)
	}
}

// LedgerProblems runs the SQL part of the consistency verification of
// test-plan §6 (items 2–6) for one wallet and returns what is wrong; none
// means consistent. M3 adds the reconciliation endpoint (item 1) and the event
// matrix (item 7).
func LedgerProblems(ctx context.Context, pool *pgxpool.Pool, walletID string) ([]string, error) {
	var problems []string
	var balance, version int64
	if err := pool.QueryRow(ctx, `SELECT balance_minor, version FROM wallets WHERE id = $1`, walletID).
		Scan(&balance, &version); err != nil {
		return nil, fmt.Errorf("read wallet: %w", err)
	}

	// 2. stored == Σ CREDIT − Σ DEBIT
	var net int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE direction WHEN 'CREDIT' THEN amount_minor ELSE -amount_minor END), 0)::bigint
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&net); err != nil {
		return nil, fmt.Errorf("ledger sum: %w", err)
	}
	if net != balance {
		problems = append(problems, fmt.Sprintf("balance %d != Σ credits − Σ debits %d", balance, net))
	}

	// 3. chain: consecutive versions, before = previous after, the first starts at 0.
	// 4. wallets.version == max(wallet_version), or 1 without entries.
	rows, err := pool.Query(ctx, `
		SELECT wallet_version, balance_before_minor, balance_after_minor
		FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY wallet_version`, walletID)
	if err != nil {
		return nil, fmt.Errorf("ledger chain: %w", err)
	}
	defer rows.Close()
	last, prevAfter := int64(1), int64(0)
	first := true
	for rows.Next() {
		var v, before, after int64
		if err := rows.Scan(&v, &before, &after); err != nil {
			return nil, fmt.Errorf("ledger chain: %w", err)
		}
		switch {
		case first && (before != 0 || (v != 1 && v != 2)):
			problems = append(problems, fmt.Sprintf("first entry at version %d starts at %d, want version 1 or 2 from 0", v, before))
		case !first && (v != last+1 || before != prevAfter):
			problems = append(problems, fmt.Sprintf("entry v%d (before %d) does not follow v%d (after %d)", v, before, last, prevAfter))
		}
		first, last, prevAfter = false, v, after
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ledger chain: %w", err)
	}
	if version != last {
		problems = append(problems, fmt.Sprintf("version %d != last ledger version %d", version, last))
	}

	// 5. no entries for REJECTED, FAILED, PENDING_REFERENCE or LOSS.
	// 6. every PROCESSED operation that moves the balance has exactly one entry.
	var orphans, missing int64
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM wallet_ledger_entries l JOIN wager_transactions t ON t.id = l.transaction_id
		   WHERE l.wallet_id = $1 AND (t.status <> 'PROCESSED' OR t.kind = 'LOSS')),
		  (SELECT count(*) FROM wager_transactions t
		   WHERE t.wallet_id = $1 AND t.status = 'PROCESSED' AND t.kind <> 'LOSS'
		     AND (SELECT count(*) FROM wallet_ledger_entries l WHERE l.transaction_id = t.id) <> 1)`,
		walletID).Scan(&orphans, &missing); err != nil {
		return nil, fmt.Errorf("ledger coupling: %w", err)
	}
	if orphans != 0 {
		problems = append(problems, fmt.Sprintf("%d entries of operations that do not move the balance", orphans))
	}
	if missing != 0 {
		problems = append(problems, fmt.Sprintf("%d moving operations without exactly one entry", missing))
	}
	return problems, nil
}
