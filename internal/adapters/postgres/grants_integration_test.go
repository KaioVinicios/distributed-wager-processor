//go:build integration

package postgres_test

import (
	"testing"
)

// Covers: TST-I02, LED-04, DB-03, E9 (I02b, as the application role)
func TestLedgerImmutableForApp(t *testing.T) {
	t.Parallel()
	s := seedWallet(t)
	mutations := map[string]stmt{
		"update":   exec(`UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1`, s.entry),
		"delete":   exec(`DELETE FROM wallet_ledger_entries WHERE id = $1`, s.entry),
		"truncate": exec(`TRUNCATE wallet_ledger_entries`),
	}
	for name, m := range mutations {
		t.Run("app "+name, func(t *testing.T) {
			wantSQLState(t, attempt(t, env.App, m), "42501", "")
		})
	}
}

// Covers: DB-03, LED-04 (D-17: the privileges of pda_app, data-model §5)
func TestAppRolePrivileges(t *testing.T) {
	t.Parallel()
	want := map[string]map[string]bool{
		"wallets":               {"SELECT": true, "INSERT": true, "UPDATE": true},
		"wager_transactions":    {"SELECT": true, "INSERT": true, "UPDATE": true},
		"wallet_ledger_entries": {"SELECT": true, "INSERT": true},
		"inbox_messages":        {"SELECT": true, "INSERT": true},
		"outbox_events":         {"SELECT": true, "INSERT": true, "UPDATE": true},
	}
	for table, allowed := range want {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			var has bool
			if err := env.Owner.QueryRow(t.Context(), `SELECT has_table_privilege('pda_app', $1, $2)`, table, privilege).Scan(&has); err != nil {
				t.Fatalf("has_table_privilege(%s, %s): %v", table, privilege, err)
			}
			if has != allowed[privilege] {
				t.Errorf("pda_app %s on %s = %v, want %v", privilege, table, has, allowed[privilege])
			}
		}
	}
	var canCreate bool
	if err := env.Owner.QueryRow(t.Context(), `SELECT has_schema_privilege('pda_app', 'public', 'CREATE')`).Scan(&canCreate); err != nil {
		t.Fatalf("has_schema_privilege: %v", err)
	}
	if canCreate {
		t.Error("pda_app can CREATE in schema public, want no DDL")
	}
}
