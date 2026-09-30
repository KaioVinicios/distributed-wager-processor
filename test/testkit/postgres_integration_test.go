//go:build integration

package testkit

import (
	"context"
	"strings"
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
// Sensitivity: no fallback → "database … is being accessed by other users" after 5 s.
func TestDropEndsLeftoverOwnerSession(t *testing.T) {
	db, drop := newDB(t, "drop_force")
	conn, err := pgx.Connect(t.Context(), db.OwnerURL) // never closed by the test: a leaked session
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(t.Context())) }()
	if err := drop(); err != nil {
		t.Fatalf("drop() = %v, want the leftover owner session ended and the database dropped", err)
	}
	if dbExists(t, db.Name) {
		t.Fatalf("database %s still exists", db.Name)
	}
}

// Covers: TST-I* infrastructure (I30; spec test-db-drop, decision 3)
// Sensitivity: cleanup printing the drop error instead of returning it → cleanup() = nil.
func TestNewEnvCleanupReportsDropFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	env, cleanup, err := NewEnv(ctx, "cleanup_err")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(t.Context(), env.DB.AppURL) // alive during the cleanup: pda_owner cannot end it
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
