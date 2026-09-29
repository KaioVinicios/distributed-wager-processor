//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/migrations"
	"github.com/KaioVinicios/pda/test/testkit"
)

// schemaSnapshot lists every object the migrations own: columns, constraints,
// indexes, triggers, functions and grants (golang-migrate's own table apart).
const schemaSnapshot = `
SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' || is_nullable || ' ' || coalesce(column_default, '')
  FROM information_schema.columns WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
UNION ALL
SELECT 'constraint ' || conrelid::regclass || '.' || conname || ' ' || pg_get_constraintdef(oid)
  FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND conrelid::regclass::text <> 'schema_migrations'
UNION ALL
SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
UNION ALL
SELECT 'trigger ' || pg_get_triggerdef(oid) FROM pg_trigger WHERE NOT tgisinternal
UNION ALL
SELECT 'function ' || proname || ' ' || md5(prosrc) FROM pg_proc WHERE pronamespace = 'public'::regnamespace
UNION ALL
SELECT 'grant ' || table_name || ' ' || grantee || ' ' || privilege_type
  FROM information_schema.role_table_grants WHERE table_schema = 'public' AND grantee = 'pda_app'
ORDER BY 1`

func snapshot(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), schemaSnapshot)
	if err != nil {
		t.Fatalf("schema snapshot: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("schema snapshot: %v", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("schema snapshot: %v", err)
	}
	return out
}

// isolatedDB creates a database only this test uses.
func isolatedDB(t *testing.T, name string) (*testkit.Database, *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	db, drop, err := testkit.NewDatabase(ctx, name)
	if err != nil {
		t.Fatalf("testkit.NewDatabase: %v", err)
	}
	t.Cleanup(func() {
		if err := drop(); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	pool, err := pgxpool.New(t.Context(), db.OwnerURL)
	if err != nil {
		t.Fatalf("owner pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return db, pool
}

// Covers: TST-I01, DB-04, ART-05 (I01)
func TestMigrationsUpDownUp(t *testing.T) {
	db, pool := isolatedDB(t, "migrations")

	ups, err := fs.Glob(migrations.FS, "*.up.sql")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	m, err := db.Migrator()
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	version, dirty, err := m.Version()
	if err != nil || dirty || int(version) != len(ups) {
		t.Fatalf("after up: version %d dirty %v err %v; want version %d (one per up file), clean", version, dirty, err, len(ups))
	}

	first := snapshot(t, pool)
	for _, table := range []string{"wallets", "wager_transactions", "wallet_ledger_entries", "inbox_messages", "outbox_events"} {
		if !slices.ContainsFunc(first, func(l string) bool { return strings.HasPrefix(l, "column "+table+".") }) {
			t.Fatalf("table %s missing after up", table)
		}
	}

	if err := m.Down(); err != nil {
		t.Fatalf("down -all: %v", err)
	}
	if left := snapshot(t, pool); len(left) != 0 {
		t.Fatalf("down -all left %d objects behind, e.g. %q", len(left), left[0])
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("up again: %v", err)
	}
	if second := snapshot(t, pool); !slices.Equal(first, second) {
		t.Fatalf("schema differs after up → down → up:\nfirst:  %d objects\nsecond: %d objects", len(first), len(second))
	}
}
