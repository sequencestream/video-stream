package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

var _ ProjectStore = (*SQLiteStore)(nil)

// SaveProject validates and upserts a project.
//
// Validation runs here rather than being left to callers because the store is
// the boundary: once an inconsistent document is on disk, every later read has
// to cope with it, and the render cache will happily key off a stale hash.
func (s *SQLiteStore) SaveProject(ctx context.Context, p model.Project) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("save project: %w", err)
	}

	document, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("encode project %s: %w", p.ID, err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save of project %s: %w", p.ID, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO projects (id, title, schema_version, document, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   title = excluded.title,
		   schema_version = excluded.schema_version,
		   document = excluded.document,
		   updated_at = excluded.updated_at`,
		p.ID, p.Title, p.SchemaVersion, string(document),
		p.CreatedAt.UTC().UnixMilli(), p.UpdatedAt.UTC().UnixMilli()); err != nil {
		return fmt.Errorf("upsert project %s: %w", p.ID, err)
	}

	// The projection is derived, so it is rebuilt rather than diffed: a diff
	// would have to reason about renamed and removed segs, and getting that
	// wrong leaves an orphan row that the cache lookup would happily return.
	if _, err := tx.ExecContext(ctx, `DELETE FROM segs WHERE project_id = ?`, p.ID); err != nil {
		return fmt.Errorf("clear seg index of project %s: %w", p.ID, err)
	}
	for i, seg := range p.Segs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO segs (project_id, seg_id, ordinal, content_hash, render_cache_key,
			                   duration_min_ms, duration_max_ms, protected)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, seg.SegID, i, seg.ContentHash, seg.RenderCacheKey,
			seg.DurationBudget.MinMS, seg.DurationBudget.MaxMS, boolToInt(seg.Protected)); err != nil {
			return fmt.Errorf("index seg %s of project %s: %w", seg.SegID, p.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save of project %s: %w", p.ID, err)
	}
	return nil
}

// GetProject returns the stored project, migrating the document if it predates
// this binary's schema version.
func (s *SQLiteStore) GetProject(ctx context.Context, id string) (model.Project, error) {
	var document string
	err := s.db.QueryRowContext(ctx, `SELECT document FROM projects WHERE id = ?`, id).Scan(&document)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Project{}, ErrProjectNotFound
	}
	if err != nil {
		return model.Project{}, fmt.Errorf("get project %s: %w", id, err)
	}

	migrated, err := model.DefaultMigrator.Migrate([]byte(document))
	if err != nil {
		return model.Project{}, fmt.Errorf("migrate project %s: %w", id, err)
	}

	var p model.Project
	if err := json.Unmarshal(migrated, &p); err != nil {
		return model.Project{}, fmt.Errorf("decode project %s: %w", id, err)
	}
	return p, nil
}

// ListProjects returns up to limit summaries, newest first.
func (s *SQLiteStore) ListProjects(ctx context.Context, limit int) ([]ProjectSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.title, p.schema_version, p.created_at, p.updated_at,
		        (SELECT COUNT(*) FROM segs WHERE segs.project_id = p.id)
		 FROM projects p ORDER BY p.updated_at DESC, p.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var out []ProjectSummary
	for rows.Next() {
		var (
			p                    ProjectSummary
			createdAt, updatedAt int64
		)
		if err := rows.Scan(&p.ID, &p.Title, &p.SchemaVersion, &createdAt, &updatedAt, &p.SegCount); err != nil {
			return nil, fmt.Errorf("scan project summary: %w", err)
		}
		p.CreatedAt = time.UnixMilli(createdAt).UTC()
		p.UpdatedAt = time.UnixMilli(updatedAt).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProject removes a project; the seg index follows by cascade.
func (s *SQLiteStore) DeleteProject(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete project %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for project %s: %w", id, err)
	}
	if n == 0 {
		return ErrProjectNotFound
	}
	return nil
}

// SegsByRenderCacheKey returns indexed segs carrying the given key.
func (s *SQLiteStore) SegsByRenderCacheKey(ctx context.Context, key string) ([]SegRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT project_id, seg_id, content_hash, render_cache_key,
		        duration_min_ms, duration_max_ms, protected
		 FROM segs WHERE render_cache_key = ? ORDER BY project_id, ordinal`, key)
	if err != nil {
		return nil, fmt.Errorf("look up render cache key: %w", err)
	}
	defer rows.Close()

	var out []SegRef
	for rows.Next() {
		var (
			ref       SegRef
			protected int
		)
		if err := rows.Scan(&ref.ProjectID, &ref.SegID, &ref.ContentHash, &ref.RenderCacheKey,
			&ref.DurationBudget.MinMS, &ref.DurationBudget.MaxMS, &protected); err != nil {
			return nil, fmt.Errorf("scan seg ref: %w", err)
		}
		ref.Protected = protected != 0
		out = append(out, ref)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
