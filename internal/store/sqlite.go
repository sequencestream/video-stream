package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver: no cgo, keeps a static binary
)

const schema = `
CREATE TABLE IF NOT EXISTS tasks (
	id         TEXT PRIMARY KEY,
	type       TEXT NOT NULL,
	title      TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL,
	payload    TEXT NOT NULL DEFAULT '{}',
	result     TEXT NOT NULL DEFAULT '{}',
	error      TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_status_created ON tasks(status, created_at);

CREATE TABLE IF NOT EXISTS projects (
	id             TEXT PRIMARY KEY,
	title          TEXT NOT NULL DEFAULT '',
	schema_version INTEGER NOT NULL,
	document       TEXT NOT NULL,
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL
);

-- segs is a projection of projects.document, rebuilt on every save. It exists
-- so that "which segs share this render cache key" is an index lookup instead
-- of a scan over every stored document; nothing in it is authoritative, which
-- is why adding a model field needs no DDL change here.
CREATE TABLE IF NOT EXISTS segs (
	project_id       TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	seg_id           TEXT NOT NULL,
	ordinal          INTEGER NOT NULL,
	content_hash     TEXT NOT NULL,
	render_cache_key TEXT NOT NULL,
	duration_min_ms  INTEGER NOT NULL,
	duration_max_ms  INTEGER NOT NULL,
	protected        INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (project_id, seg_id)
);
CREATE INDEX IF NOT EXISTS idx_segs_render_cache_key ON segs(render_cache_key);
CREATE INDEX IF NOT EXISTS idx_segs_content_hash ON segs(content_hash);

-- artifacts is keyed by render_cache_key rather than by seg, because two segs
-- with identical content legitimately share one rendered product. It carries
-- the artifact's real duration, which is the half of the reuse test that the
-- seg cannot answer for itself.
CREATE TABLE IF NOT EXISTS artifacts (
	render_cache_key TEXT PRIMARY KEY,
	duration_ms      INTEGER NOT NULL,
	uri              TEXT NOT NULL DEFAULT '',
	cost_micros      INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL
);

-- recompile_runs is the evidence behind the invalidation rate. The CHECK is
-- there because a run claiming more invalidated segs than it had would quietly
-- push the reported rate above 100%.
CREATE TABLE IF NOT EXISTS recompile_runs (
	id                TEXT PRIMARY KEY,
	project_id        TEXT NOT NULL,
	planned_at        INTEGER NOT NULL,
	total_segs        INTEGER NOT NULL,
	invalidated_segs  INTEGER NOT NULL,
	full_rerun        INTEGER NOT NULL DEFAULT 0,
	boundary          TEXT NOT NULL DEFAULT '',
	cost_saved_micros INTEGER NOT NULL DEFAULT 0,
	cache_hits         INTEGER NOT NULL DEFAULT 0,
	regenerated_segs   INTEGER NOT NULL DEFAULT 0,
	elapsed_ms          INTEGER NOT NULL DEFAULT 0,
	actual_cost_micros INTEGER NOT NULL DEFAULT 0,
	CHECK (total_segs >= 0 AND invalidated_segs >= 0 AND invalidated_segs <= total_segs)
);
CREATE INDEX IF NOT EXISTS idx_recompile_runs_project ON recompile_runs(project_id, planned_at);

-- radar_accounts is the set of accounts the user chose to watch. It is capped
-- in the radar package rather than here, because the cap is a rate-limit
-- judgement and not a storage invariant. The UNIQUE is on (platform, handle)
-- rather than on handle alone: the same creator name on two platforms is two
-- accounts with two audiences.
CREATE TABLE IF NOT EXISTS radar_accounts (
	id             TEXT PRIMARY KEY,
	platform       TEXT NOT NULL,
	handle         TEXT NOT NULL,
	display_name   TEXT NOT NULL DEFAULT '',
	category       TEXT NOT NULL DEFAULT '',
	followers      INTEGER NOT NULL DEFAULT 0,
	owned          INTEGER NOT NULL DEFAULT 0,
	added_at       INTEGER NOT NULL,
	last_polled_at INTEGER NOT NULL DEFAULT 0,
	CHECK (followers >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_radar_accounts_handle ON radar_accounts(platform, handle);

-- radar_observations holds one reading of one post's public metrics. The same
-- post appears many times, once per polling round, and that is the point: the
-- second derivative of the save and completion rates needs at least three
-- readings, so an upsert on post_id would destroy the only input that measure
-- has. This is the opposite of the artifacts table, where the newer row is the
-- better measurement.
CREATE TABLE IF NOT EXISTS radar_observations (
	id                   TEXT PRIMARY KEY,
	account_id           TEXT NOT NULL REFERENCES radar_accounts(id) ON DELETE CASCADE,
	post_id              TEXT NOT NULL,
	title                TEXT NOT NULL DEFAULT '',
	duration_seconds     INTEGER NOT NULL DEFAULT 0,
	published_at         INTEGER NOT NULL,
	observed_at          INTEGER NOT NULL,
	views                INTEGER NOT NULL DEFAULT 0,
	likes                INTEGER NOT NULL DEFAULT 0,
	comments             INTEGER NOT NULL DEFAULT 0,
	shares               INTEGER NOT NULL DEFAULT 0,
	saves                INTEGER NOT NULL DEFAULT 0,
	completion_rate      REAL NOT NULL DEFAULT 0,
	comment_samples      INTEGER NOT NULL DEFAULT 0,
	unanswered_questions INTEGER NOT NULL DEFAULT 0,
	CHECK (views >= 0 AND likes >= 0 AND comments >= 0 AND shares >= 0 AND saves >= 0),
	CHECK (completion_rate >= 0 AND completion_rate <= 1),
	CHECK (unanswered_questions >= 0 AND unanswered_questions <= comment_samples)
);
CREATE INDEX IF NOT EXISTS idx_radar_obs_account ON radar_observations(account_id, published_at);
CREATE INDEX IF NOT EXISTS idx_radar_obs_post ON radar_observations(post_id, observed_at);

-- structure_cards holds domain-neutral decompositions of viral works. The six
-- dimension columns are the authoritative structure; embedding is recall-only.
CREATE TABLE IF NOT EXISTS structure_cards (
	id                 TEXT PRIMARY KEY,
	source_post_id     TEXT NOT NULL DEFAULT '',
	source_category    TEXT NOT NULL DEFAULT '',
	hook_type          TEXT NOT NULL DEFAULT '',
	opening_visual     TEXT NOT NULL DEFAULT '',
	beat_sequence      TEXT NOT NULL DEFAULT '',
	density_curve      TEXT NOT NULL DEFAULT '',
	emotion_arc        TEXT NOT NULL DEFAULT '',
	controversy_anchor TEXT NOT NULL DEFAULT '',
	embedding          TEXT NOT NULL DEFAULT '[]',
	created_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_structure_cards_category ON structure_cards(source_category, created_at);

-- structure_edges is the graph over cards. Vectors recall candidates; edges
-- record structural relationships the product can traverse explicitly.
CREATE TABLE IF NOT EXISTS structure_edges (
	from_id    TEXT NOT NULL REFERENCES structure_cards(id) ON DELETE CASCADE,
	to_id      TEXT NOT NULL REFERENCES structure_cards(id) ON DELETE CASCADE,
	rel        TEXT NOT NULL DEFAULT 'similar',
	created_at INTEGER NOT NULL,
	PRIMARY KEY (from_id, to_id, rel)
);

-- topic_cards are cross-category ideas migrated from one structure card.
CREATE TABLE IF NOT EXISTS topic_cards (
	id                TEXT PRIMARY KEY,
	structure_card_id TEXT NOT NULL REFERENCES structure_cards(id) ON DELETE CASCADE,
	title             TEXT NOT NULL DEFAULT '',
	angle             TEXT NOT NULL DEFAULT '',
	migration_source  TEXT NOT NULL DEFAULT '',
	why_fits          TEXT NOT NULL DEFAULT '',
	target_category   TEXT NOT NULL DEFAULT '',
	user_theme        TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_topic_cards_structure ON topic_cards(structure_card_id, created_at);

-- script_polish_runs records token spend and termination for each polish loop.
CREATE TABLE IF NOT EXISTS script_polish_runs (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL DEFAULT '',
	stop_reason TEXT NOT NULL DEFAULT '',
	tokens_used INTEGER NOT NULL DEFAULT 0,
	cost_micros INTEGER NOT NULL DEFAULT 0,
	rounds      INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	CHECK (tokens_used >= 0 AND cost_micros >= 0 AND rounds >= 0)
);
CREATE INDEX IF NOT EXISTS idx_script_polish_project ON script_polish_runs(project_id, created_at);

-- compliance_passes records successful gate runs for fingerprint and reuse tracking.
CREATE TABLE IF NOT EXISTS compliance_passes (
	id                TEXT PRIMARY KEY,
	account_id        TEXT NOT NULL,
	structure_card_id TEXT NOT NULL,
	project_id        TEXT NOT NULL DEFAULT '',
	fingerprint       TEXT NOT NULL DEFAULT '[]',
	created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_compliance_account ON compliance_passes(account_id, created_at);
CREATE INDEX IF NOT EXISTS idx_compliance_reuse ON compliance_passes(account_id, structure_card_id, created_at);

CREATE TABLE IF NOT EXISTS style_packs (
	id             TEXT PRIMARY KEY,
	name           TEXT NOT NULL,
	schema_version INTEGER NOT NULL,
	document       TEXT NOT NULL,
	created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS hybrid_shots (
	project_id     TEXT NOT NULL,
	seg_id         TEXT NOT NULL,
	route          TEXT NOT NULL,
	reason         TEXT NOT NULL DEFAULT '',
	stock_query    TEXT NOT NULL DEFAULT '',
	ken_burns_json TEXT NOT NULL DEFAULT '',
	stock_json     TEXT NOT NULL DEFAULT '',
	updated_at     INTEGER NOT NULL,
	PRIMARY KEY (project_id, seg_id)
);

CREATE TABLE IF NOT EXISTS render_runs (
	id                   TEXT PRIMARY KEY,
	project_id           TEXT NOT NULL,
	resolution           TEXT NOT NULL,
	platform             TEXT NOT NULL DEFAULT 'youtube',
	subtitle_mode        TEXT NOT NULL DEFAULT 'soft',
	status               TEXT NOT NULL,
	finalized            INTEGER NOT NULL DEFAULT 0,
	include_bgm          INTEGER NOT NULL DEFAULT 0,
	bgm_uri              TEXT NOT NULL DEFAULT '',
	bgm_bpm              REAL NOT NULL DEFAULT 0,
	bgm_beat_offset_ms   INTEGER NOT NULL DEFAULT 0,
	bgm_gain_db          REAL NOT NULL DEFAULT 0,
	last_completed_stage TEXT NOT NULL DEFAULT '',
	output_uri           TEXT NOT NULL DEFAULT '',
	error                TEXT NOT NULL DEFAULT '',
	updated_at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_render_runs_project ON render_runs(project_id, updated_at);

CREATE TABLE IF NOT EXISTS render_shared_context (
	project_id       TEXT NOT NULL,
	render_cache_key TEXT NOT NULL,
	prompt           TEXT NOT NULL,
	seed             TEXT NOT NULL,
	ref_uri          TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (project_id, render_cache_key)
);

CREATE TABLE IF NOT EXISTS render_seg_artifacts (
	run_id           TEXT NOT NULL,
	project_id       TEXT NOT NULL,
	seg_id           TEXT NOT NULL,
	render_cache_key TEXT NOT NULL,
	stage            TEXT NOT NULL,
	uri              TEXT NOT NULL,
	PRIMARY KEY (run_id, seg_id, stage)
);
CREATE INDEX IF NOT EXISTS idx_render_seg_project ON render_seg_artifacts(project_id, seg_id);

CREATE TABLE IF NOT EXISTS wizard_sessions (
	id              TEXT PRIMARY KEY,
	current_step    INTEGER NOT NULL,
	status          TEXT NOT NULL,
	topic           TEXT NOT NULL DEFAULT '',
	category        TEXT NOT NULL DEFAULT '',
	project_id      TEXT NOT NULL DEFAULT '',
	state_json      TEXT NOT NULL DEFAULT '{}',
	cost_micros     INTEGER NOT NULL DEFAULT 0,
	failed_step     INTEGER NOT NULL DEFAULT 0,
	error           TEXT NOT NULL DEFAULT '',
	hook_confirm_ms INTEGER NOT NULL DEFAULT 0,
	version         INTEGER NOT NULL DEFAULT 1,
	active_operation_id TEXT NOT NULL DEFAULT '',
	failed_operation_id TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS wizard_operations (
	operation_id     TEXT PRIMARY KEY,
	session_id       TEXT NOT NULL DEFAULT '',
	kind             TEXT NOT NULL,
	step             INTEGER NOT NULL DEFAULT 0,
	expected_version INTEGER NOT NULL DEFAULT 0,
	request_json     TEXT NOT NULL DEFAULT '{}',
	request_hash     TEXT NOT NULL,
	status           TEXT NOT NULL,
	result_json      TEXT NOT NULL DEFAULT '',
	error_code       TEXT NOT NULL DEFAULT '',
	error            TEXT NOT NULL DEFAULT '',
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wizard_operations_session ON wizard_operations(session_id, created_at);

CREATE TABLE IF NOT EXISTS schema_migrations (
	name       TEXT PRIMARY KEY,
	applied_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS youtube_uploads (
	id          TEXT PRIMARY KEY,
	project_id  TEXT NOT NULL,
	session_id  TEXT NOT NULL DEFAULT '',
	video_id    TEXT NOT NULL DEFAULT '',
	video_path  TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	error       TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_youtube_uploads_project ON youtube_uploads(project_id, created_at);
`

// SQLiteStore is the SQLite-backed TaskStore and ProjectStore.
type SQLiteStore struct {
	db *sql.DB
}

var _ TaskStore = (*SQLiteStore)(nil)

// OpenSQLite opens (creating if needed) the database at path and applies the
// schema. Parent directories are created so a first run needs no setup.
func OpenSQLite(path string) (*SQLiteStore, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}

	// WAL keeps the CLI's reads from blocking the daemon's writes; busy_timeout
	// absorbs the brief contention between concurrent workers.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := applySQLiteMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func applySQLiteMigrations(db *sql.DB) error {
	if err := applyColumnMigration(db, "001_wizard_operation_journal", "wizard_sessions", []struct{ name, ddl string }{
		{"version", `ALTER TABLE wizard_sessions ADD COLUMN version INTEGER NOT NULL DEFAULT 1`},
		{"active_operation_id", `ALTER TABLE wizard_sessions ADD COLUMN active_operation_id TEXT NOT NULL DEFAULT ''`},
		{"failed_operation_id", `ALTER TABLE wizard_sessions ADD COLUMN failed_operation_id TEXT NOT NULL DEFAULT ''`},
	}); err != nil {
		return err
	}
	if err := applyColumnMigration(db, "002_render_subtitle_delivery", "render_runs", []struct{ name, ddl string }{
		{"platform", `ALTER TABLE render_runs ADD COLUMN platform TEXT NOT NULL DEFAULT 'youtube'`},
		{"subtitle_mode", `ALTER TABLE render_runs ADD COLUMN subtitle_mode TEXT NOT NULL DEFAULT 'soft'`},
	}); err != nil {
		return err
	}
	if err := applyColumnMigration(db, "003_render_bgm", "render_runs", []struct{ name, ddl string }{
		{"include_bgm", `ALTER TABLE render_runs ADD COLUMN include_bgm INTEGER NOT NULL DEFAULT 0`},
		{"bgm_uri", `ALTER TABLE render_runs ADD COLUMN bgm_uri TEXT NOT NULL DEFAULT ''`},
		{"bgm_bpm", `ALTER TABLE render_runs ADD COLUMN bgm_bpm REAL NOT NULL DEFAULT 0`},
		{"bgm_beat_offset_ms", `ALTER TABLE render_runs ADD COLUMN bgm_beat_offset_ms INTEGER NOT NULL DEFAULT 0`},
		{"bgm_gain_db", `ALTER TABLE render_runs ADD COLUMN bgm_gain_db REAL NOT NULL DEFAULT 0`},
	}); err != nil {
		return err
	}
	return applyColumnMigration(db, "004_recompile_execution_metrics", "recompile_runs", []struct{ name, ddl string }{
		{"cache_hits", `ALTER TABLE recompile_runs ADD COLUMN cache_hits INTEGER NOT NULL DEFAULT 0`},
		{"regenerated_segs", `ALTER TABLE recompile_runs ADD COLUMN regenerated_segs INTEGER NOT NULL DEFAULT 0`},
		{"elapsed_ms", `ALTER TABLE recompile_runs ADD COLUMN elapsed_ms INTEGER NOT NULL DEFAULT 0`},
		{"actual_cost_micros", `ALTER TABLE recompile_runs ADD COLUMN actual_cost_micros INTEGER NOT NULL DEFAULT 0`},
	})
}

func applyColumnMigration(db *sql.DB, name, table string, columns []struct{ name, ddl string }) error {
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name=?`, name).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	for _, column := range columns {
		exists, err := sqliteColumnExists(db, table, column.name)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(column.ddl); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().UTC().UnixMilli())
	return err
}

func sqliteColumnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Create inserts a new task row.
func (s *SQLiteStore) Create(ctx context.Context, t Task) error {
	if t.ID == "" {
		return errors.New("task id must not be empty")
	}
	if !t.Status.Valid() {
		return fmt.Errorf("invalid task status %q", t.Status)
	}

	payload, err := encodeMap(t.Payload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	result, err := encodeMap(t.Result)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO tasks (id, type, title, status, payload, result, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Type, t.Title, string(t.Status), payload, result, t.Error,
		t.CreatedAt.UTC().UnixMilli(), t.UpdatedAt.UTC().UnixMilli())
	if err != nil {
		return fmt.Errorf("insert task %s: %w", t.ID, err)
	}
	return nil
}

// Get returns the task with the given id.
func (s *SQLiteStore) Get(ctx context.Context, id string) (Task, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, title, status, payload, result, error, created_at, updated_at
		 FROM tasks WHERE id = ?`, id)

	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %s: %w", id, err)
	}
	return t, nil
}

// List returns up to limit tasks, newest first.
func (s *SQLiteStore) List(ctx context.Context, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, title, status, payload, result, error, created_at, updated_at
		 FROM tasks ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	return collectTasks(rows)
}

// MarkRunning transitions a pending task to running.
func (s *SQLiteStore) MarkRunning(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		string(StatusRunning), time.Now().UTC().UnixMilli(), id, string(StatusPending))
	if err != nil {
		return fmt.Errorf("mark task %s running: %w", id, err)
	}
	return requireAffected(res, id)
}

// Finish writes the terminal status of a task.
func (s *SQLiteStore) Finish(ctx context.Context, id string, status Status, result map[string]any, taskErr string) error {
	if !status.Terminal() {
		return fmt.Errorf("status %q is not terminal", status)
	}
	encoded, err := encodeMap(result)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, result = ?, error = ?, updated_at = ? WHERE id = ?`,
		string(status), encoded, taskErr, time.Now().UTC().UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("finish task %s: %w", id, err)
	}
	return requireAffected(res, id)
}

// Unfinished returns non-terminal tasks, oldest first.
func (s *SQLiteStore) Unfinished(ctx context.Context) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, title, status, payload, result, error, created_at, updated_at
		 FROM tasks WHERE status IN (?, ?) ORDER BY created_at ASC, id ASC`,
		string(StatusPending), string(StatusRunning))
	if err != nil {
		return nil, fmt.Errorf("list unfinished tasks: %w", err)
	}
	defer rows.Close()

	return collectTasks(rows)
}

// Close closes the database handle.
func (s *SQLiteStore) Close() error { return s.db.Close() }

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(sc scanner) (Task, error) {
	var (
		t                    Task
		status               string
		payload, result      string
		createdAt, updatedAt int64
	)
	if err := sc.Scan(&t.ID, &t.Type, &t.Title, &status, &payload, &result, &t.Error, &createdAt, &updatedAt); err != nil {
		return Task{}, err
	}

	t.Status = Status(status)
	t.CreatedAt = time.UnixMilli(createdAt).UTC()
	t.UpdatedAt = time.UnixMilli(updatedAt).UTC()

	var err error
	if t.Payload, err = decodeMap(payload); err != nil {
		return Task{}, fmt.Errorf("decode payload of %s: %w", t.ID, err)
	}
	if t.Result, err = decodeMap(result); err != nil {
		return Task{}, fmt.Errorf("decode result of %s: %w", t.ID, err)
	}
	return t, nil
}

func collectTasks(rows *sql.Rows) ([]Task, error) {
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func requireAffected(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func encodeMap(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeMap(s string) (map[string]any, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}
