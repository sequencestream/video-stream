package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRenderSubtitleMigrationUpgradesExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE render_runs (
		id TEXT PRIMARY KEY, project_id TEXT NOT NULL, resolution TEXT NOT NULL,
		status TEXT NOT NULL, finalized INTEGER NOT NULL DEFAULT 0,
		last_completed_stage TEXT NOT NULL DEFAULT '', output_uri TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL
	);
	INSERT INTO render_runs (id, project_id, resolution, status, updated_at)
	VALUES ('legacy', 'project', '720p', 'completed', 1)`)
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
	for _, column := range []string{"platform", "subtitle_mode"} {
		exists, err := sqliteColumnExists(store.db, "render_runs", column)
		if err != nil || !exists {
			t.Fatalf("column %s: exists=%v err=%v", column, exists, err)
		}
	}
	run, err := store.GetRenderRun(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if run.Platform != "youtube" || run.SubtitleMode != "soft" {
		t.Fatalf("legacy defaults=%s/%s", run.Platform, run.SubtitleMode)
	}
}
