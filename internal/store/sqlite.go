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
	return &SQLiteStore{db: db}, nil
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
