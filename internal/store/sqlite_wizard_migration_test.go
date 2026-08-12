package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestWizardOperationMigrationUpgradesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE wizard_sessions (
		id TEXT PRIMARY KEY, current_step INTEGER NOT NULL, status TEXT NOT NULL,
		topic TEXT NOT NULL DEFAULT '', category TEXT NOT NULL DEFAULT '', project_id TEXT NOT NULL DEFAULT '',
		state_json TEXT NOT NULL DEFAULT '{}', cost_micros INTEGER NOT NULL DEFAULT 0,
		failed_step INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', hook_confirm_ms INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, column := range []string{"version", "active_operation_id", "failed_operation_id"} {
		exists, err := sqliteColumnExists(store.db, "wizard_sessions", column)
		if err != nil || !exists {
			t.Fatalf("column %s: exists=%v err=%v", column, exists, err)
		}
	}
	var migrations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name='001_wizard_operation_journal'`).Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("migration count=%d err=%v", migrations, err)
	}
}
